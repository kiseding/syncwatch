package api

import (
	"encoding/json"
	"net/http"

	"github.com/kiseding/syncwatch/internal/auth"
)

// Router sets up all HTTP routes.
type Router struct {
	mux              *http.ServeMux
	handler          *Handler
	tokenManager     *auth.TokenManager
	rateLimiter      *auth.RateLimiter
	ratePerMin       int
	passwordHash     string
	adminPasswordHash string
}

// NewRouter creates a new API router.
func NewRouter(h *Handler, tm *auth.TokenManager, rl *auth.RateLimiter, ratePerMin int, passwordHash, adminPasswordHash string) *Router {
	if adminPasswordHash == "" {
		adminPasswordHash = passwordHash // fallback to viewer password
	}
	return &Router{
		mux:              http.NewServeMux(),
		handler:          h,
		tokenManager:     tm,
		rateLimiter:      rl,
		ratePerMin:       ratePerMin,
		passwordHash:     passwordHash,
		adminPasswordHash: adminPasswordHash,
	}
}

// Setup configures all routes and returns the http.Handler with middleware.
func (rt *Router) Setup() http.Handler {
	// Auth endpoint (public)
	rt.mux.HandleFunc("POST /api/auth", rt.handleAuth)

	// Playback control (host only)
	hostOnly := HostOnlyMiddleware
	rt.mux.Handle("POST /api/playback/play", hostOnly(http.HandlerFunc(rt.handler.Play)))
	rt.mux.Handle("POST /api/playback/pause", hostOnly(http.HandlerFunc(rt.handler.Pause)))
	rt.mux.Handle("POST /api/playback/resume", hostOnly(http.HandlerFunc(rt.handler.Resume)))
	rt.mux.Handle("POST /api/playback/seek", hostOnly(http.HandlerFunc(rt.handler.Seek)))
	rt.mux.Handle("POST /api/playback/speed", hostOnly(http.HandlerFunc(rt.handler.SetSpeed)))
	rt.mux.Handle("POST /api/playback/audio", hostOnly(http.HandlerFunc(rt.handler.SwitchAudio)))

	// Status and media (authenticated)
	authRequired := AuthMiddleware(rt.tokenManager)
	rt.mux.Handle("GET /api/status", http.HandlerFunc(rt.handler.Status))
	rt.mux.Handle("GET /api/media/info", authRequired(http.HandlerFunc(rt.handler.MediaInfo)))
	rt.mux.Handle("GET /api/media/scan", authRequired(http.HandlerFunc(rt.handler.MediaScan)))
	rt.mux.Handle("GET /api/state", authRequired(http.HandlerFunc(rt.handler.SignalingState)))

	return CORSMiddleware(rt.mux)
}

// wrap applies rate limiting to auth and wraps with host check
func (rt *Router) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// handleAuth handles password authentication.
func (rt *Router) handleAuth(w http.ResponseWriter, r *http.Request) {
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

// HandleAuth is the public handler for POST /api/auth (viewer login).
func (rt *Router) HandleAuth(w http.ResponseWriter, r *http.Request) {
	rt.handleAuth(w, r)
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

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.Setup().ServeHTTP(w, r)
}
