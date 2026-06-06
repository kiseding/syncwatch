package api

import (
	"encoding/json"
	"net/http"

	"github.com/kiseding/syncwatch/internal/auth"
)

// Router handles API authentication and middleware for routes.
type Router struct {
	tokenManager      *auth.TokenManager
	rateLimiter       *auth.RateLimiter
	ratePerMin        int
	passwordHash      string
	adminPasswordHash string
}

// NewRouter creates a new API router.
func NewRouter(tm *auth.TokenManager, rl *auth.RateLimiter, ratePerMin int, passwordHash, adminPasswordHash string) *Router {
	if adminPasswordHash == "" {
		adminPasswordHash = passwordHash // fallback to viewer password
	}
	return &Router{
		tokenManager:      tm,
		rateLimiter:       rl,
		ratePerMin:        ratePerMin,
		passwordHash:      passwordHash,
		adminPasswordHash: adminPasswordHash,
	}
}

// HandleAuth handles viewer login (POST /api/auth).
func (rt *Router) HandleAuth(w http.ResponseWriter, r *http.Request) {
	// Rate limit check
	ip := getClientIP(r)
	if !rt.rateLimiter.Allow(ip, rt.ratePerMin) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "password required")
		return
	}

	// Verify password
	valid, err := auth.VerifyPassword(req.Password, rt.passwordHash)
	if err != nil || !valid {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	// Viewer login — always viewer role
	token, err := rt.tokenManager.Generate("viewer")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"token": token,
		"role":  "viewer",
	})
}

// HandleAdminAuth handles admin/host login.
func (rt *Router) HandleAdminAuth(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !rt.rateLimiter.Allow(ip, rt.ratePerMin) {
		writeError(w, http.StatusTooManyRequests, "too many attempts")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	valid, err := auth.VerifyPassword(req.Password, rt.adminPasswordHash)
	if err != nil || !valid {
		writeError(w, http.StatusUnauthorized, "invalid admin password")
		return
	}

	token, err := rt.tokenManager.Generate("host")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"token": token,
		"role":  "host",
	})
}

// HostOnly wraps a handler with auth+host-only access check.
func (rt *Router) HostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		AuthMiddleware(rt.tokenManager)(HostOnlyMiddleware(http.HandlerFunc(next))).ServeHTTP(w, r)
	}
}

// AuthRequired wraps a handler with JWT auth check.
func (rt *Router) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		AuthMiddleware(rt.tokenManager)(http.HandlerFunc(next)).ServeHTTP(w, r)
	}
}
