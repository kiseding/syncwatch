package api

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/kiseding/syncwatch/internal/auth"
)

type contextKey string

const (
	ctxRole    contextKey = "role"
	ctxToken   contextKey = "token"
	ctxViewerID contextKey = "viewer_id"
)

// CORSMiddleware adds CORS headers to all responses.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates JWT tokens for API requests.
func AuthMiddleware(tm *auth.TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				// Check query param for WebSocket
				tokenStr := r.URL.Query().Get("token")
				if tokenStr == "" {
					writeError(w, http.StatusUnauthorized, "authorization required")
					return
				}
				authHeader = "Bearer " + tokenStr
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err := tm.Validate(tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), ctxRole, claims.Role)
			ctx = context.WithValue(ctx, ctxToken, tokenStr)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// HostOnlyMiddleware ensures the request comes from a host role.
func HostOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, ok := r.Context().Value(ctxRole).(string)
		if !ok || role != "host" {
			writeError(w, http.StatusForbidden, "host only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitMiddleware limits authentication attempts by IP.
func RateLimitMiddleware(rl *auth.RateLimiter, limitPerMin int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := getClientIP(r)
			if !rl.Allow(ip, limitPerMin) {
				writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// getClientIP extracts the client IP from the request, handling proxies.
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// GetRole extracts the role from the request context.
func GetRole(r *http.Request) string {
	role, _ := r.Context().Value(ctxRole).(string)
	return role
}
