package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

// TestIsDiscoveryRequestClassifiesProbes pins the paths that filled the console
// with http_request lines: metadata lookups, OAuth discovery and preflights.
func TestIsDiscoveryRequestClassifiesProbes(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodOptions, "/mcp", true},
		{http.MethodOptions, "/oauth/authorize", true},
		{http.MethodGet, "/.well-known/mcp.json", true},
		{http.MethodGet, "/.well-known/oauth-authorization-server", true},
		{http.MethodGet, "/.well-known/openid-configuration", true},
		{http.MethodPost, "/oauth/token", true},
		{http.MethodGet, "/authorize", true},
		{http.MethodPost, "/token", true},
		{http.MethodGet, "/favicon.ico", true},
		{http.MethodPost, "/mcp", false},
		{http.MethodGet, "/mcp", false},
		{http.MethodHead, "/mcp", false},
		{http.MethodGet, "/mcp-app-assets/app.js", false},
	}

	for _, c := range cases {
		if got := isDiscoveryRequest(c.method, c.path); got != c.want {
			t.Fatalf("isDiscoveryRequest(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestTunnelLogKeepsOnlyTheTail(t *testing.T) {
	output := newTunnelLog("cloudflared")
	for i := 0; i < tunnelLogLines*2; i++ {
		output.add(fmt.Sprintf("line %d", i))
	}

	if len(output.lines) != tunnelLogLines {
		t.Fatalf("kept %d lines, want %d", len(output.lines), tunnelLogLines)
	}
	if first := output.lines[0]; first != fmt.Sprintf("line %d", tunnelLogLines) {
		t.Fatalf("oldest kept line is %q", first)
	}
}

func TestStartupSummaryReportsWhatMatters(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 7676
	cfg.AllowedRoots = []string{"C:/one", "C:/two"}
	server := &Server{cfg: cfg}

	summary := strings.Join(server.startupSummary(), "\n")

	for _, want := range []string{
		"Listening",
		"127.0.0.1:7676",
		"Roots",
		"C:/one",
		"C:/two",
		"Shell",
		"Tools",
		"Logs",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary is missing %q:\n%s", want, summary)
		}
	}

	// A second root is listed without repeating the label.
	if strings.Count(summary, "Roots") != 1 {
		t.Fatalf("the Roots label repeats:\n%s", summary)
	}
}

func TestStartupSummaryFlagsMissingRoots(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AllowedRoots = nil
	server := &Server{cfg: cfg}

	summary := strings.Join(server.startupSummary(), "\n")
	if !strings.Contains(summary, "none configured") {
		t.Fatalf("summary does not flag missing roots:\n%s", summary)
	}
}
