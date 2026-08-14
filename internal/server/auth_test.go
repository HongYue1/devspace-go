package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

// reachedOrigin is the status the guarded handler answers with, so a test can
// tell "the middleware let it through" from any status the middleware itself
// might return.
const reachedOrigin = http.StatusTeapot

func guarded(t *testing.T, token string, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.AuthToken = token
	server := &Server{cfg: cfg}

	handler := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(reachedOrigin)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// An existing local setup has no token configured and must keep working.
func TestWithoutATokenEverythingIsAllowedThrough(t *testing.T) {
	got := guarded(t, "", httptest.NewRequest(http.MethodGet, "/sse", nil)).Code
	if got != reachedOrigin {
		t.Errorf("status %d, want the request served", got)
	}
}

func TestTheConfiguredTokenGetsThrough(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer s3cret-token-value")

	if got := guarded(t, "s3cret-token-value", request).Code; got != reachedOrigin {
		t.Errorf("status %d, want the request served", got)
	}
}

// RFC 7235 makes the scheme case-insensitive, and clients do send "bearer".
func TestTheSchemeIsCaseInsensitive(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "bearer s3cret-token-value")

	if got := guarded(t, "s3cret-token-value", request).Code; got != reachedOrigin {
		t.Errorf("status %d, want the request served", got)
	}
}

func TestAWrongOrMissingTokenIsRefused(t *testing.T) {
	for name, header := range map[string]string{
		"no header":     "",
		"wrong token":   "Bearer not-the-token",
		"empty token":   "Bearer ",
		"prefix only":   "Bearer",
		"wrong scheme":  "Basic s3cret-token-value",
		"bare token":    "s3cret-token-value",
		"trailing junk": "Bearer s3cret-token-value-extra",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if header != "" {
				request.Header.Set("Authorization", header)
			}

			response := guarded(t, "s3cret-token-value", request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", response.Code)
			}
			if response.Header().Get("WWW-Authenticate") == "" {
				t.Error("a 401 without WWW-Authenticate leaves the client guessing")
			}
		})
	}
}

// The health check is what a tunnel or an uptime monitor probes, and holding
// the token would spread it past the one client that needs it.
func TestTheHealthCheckStaysOpen(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, healthPath, nil)

	if got := guarded(t, "s3cret-token-value", request).Code; got != reachedOrigin {
		t.Errorf("status %d, want the health check served", got)
	}
}
