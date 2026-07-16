package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiseding/syncwatch/internal/auth"
)

func TestLoginRolesAndHostAuthorization(t *testing.T) {
	viewerHash, _ := auth.HashPassword("viewer")
	hostHash, _ := auth.HashPassword("host")
	tm := auth.NewTokenManager("test-secret", 60)
	rl := auth.NewRateLimiter()
	defer rl.Stop()
	router := NewRouter(tm, rl, 60, viewerHash, hostHash)

	viewerToken := loginToken(t, router.HandleAuth, "viewer")
	hostToken := loginToken(t, router.HandleAdminAuth, "host")

	protected := router.HostOnly(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	viewerRec := httptest.NewRecorder()
	viewerReq := httptest.NewRequest(http.MethodPost, "/protected", nil)
	viewerReq.Header.Set("Authorization", "Bearer "+viewerToken)
	protected(viewerRec, viewerReq)
	if viewerRec.Code != http.StatusForbidden {
		t.Fatalf("viewer reached host route: %d", viewerRec.Code)
	}

	hostRec := httptest.NewRecorder()
	hostReq := httptest.NewRequest(http.MethodPost, "/protected", nil)
	hostReq.Header.Set("Authorization", "Bearer "+hostToken)
	protected(hostRec, hostReq)
	if hostRec.Code != http.StatusNoContent {
		t.Fatalf("host was rejected: %d", hostRec.Code)
	}
}

func loginToken(t *testing.T, handler http.HandlerFunc, password string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(`{"password":"`+password+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Token
}
