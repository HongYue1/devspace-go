package server

import (
	"strings"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

// Naming a provider is a decision, not a preference. Quietly falling back to a
// provider that hands out a random URL would defeat the point of asking for a
// fixed one.
func TestNamingAProviderSkipsTheFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		provider config.TunnelProvider
		want     string
	}{
		{"ngrok", config.TunnelNgrok, "https://fixed.ngrok-free.app"},
		{"cloudflared", config.TunnelCloudflared, "https://example.trycloudflare.com"},
		{"pinggy", config.TunnelPinggy, "https://example.pinggy.link"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Tunnel.Provider = test.provider
			server := &Server{cfg: cfg}

			url := server.startTunnelWithProviders(
				func() string { return "https://fixed.ngrok-free.app" },
				func() string { return "https://example.trycloudflare.com" },
				func() string { return "https://example.pinggy.link" },
			)
			if url != test.want {
				t.Fatalf("got %q, want %q", url, test.want)
			}
		})
	}
}

func TestProviderOffPublishesNothing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tunnel.Provider = config.TunnelOff
	server := &Server{cfg: cfg}

	url := server.startTunnelWithProviders(
		refuseNgrok(t),
		refuseProvider(t, "cloudflared"),
		refuseProvider(t, "pinggy"),
	)
	if url != "" {
		t.Fatalf("got %q, want no URL at all", url)
	}
}

// A reserved domain is only useful through ngrok, so configuring one moves ngrok
// ahead of the providers that issue a new URL every run.
func TestAConfiguredDomainPutsNgrokFirst(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tunnel.Domain = "example.ngrok-free.app"
	server := &Server{cfg: cfg}

	url := server.startTunnelWithProviders(
		func() string { return "https://example.ngrok-free.app" },
		refuseProvider(t, "cloudflared"),
		refuseProvider(t, "pinggy"),
	)
	if url != "https://example.ngrok-free.app" {
		t.Fatalf("got %q, want the reserved domain", url)
	}
}

func TestAnAuthtokenAlonePutsNgrokFirst(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tunnel.Authtoken = "token"
	server := &Server{cfg: cfg}

	url := server.startTunnelWithProviders(
		func() string { return "https://random.ngrok-free.app" },
		refuseProvider(t, "cloudflared"),
		refuseProvider(t, "pinggy"),
	)
	if url != "https://random.ngrok-free.app" {
		t.Fatalf("got %q, want ngrok to be tried first", url)
	}
}

// Trying ngrok first should not cost the fallbacks: a missing agent or a stale
// token still leaves a usable server.
func TestAutoFallsBackWhenNgrokFails(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tunnel.Authtoken = "token"
	server := &Server{cfg: cfg}

	cloudflaredCalls := 0
	url := server.startTunnelWithProviders(
		func() string { return "" },
		func() string {
			cloudflaredCalls++
			return "https://example.trycloudflare.com"
		},
		refuseProvider(t, "pinggy"),
	)

	if url != "https://example.trycloudflare.com" {
		t.Fatalf("got %q, want the cloudflared URL", url)
	}
	if cloudflaredCalls != 1 {
		t.Fatalf("cloudflared called %d times, want 1", cloudflaredCalls)
	}
}

// The summary has to say whether the URL can be pasted into a client once or has
// to be replaced after every restart.
func TestSummaryCallsAReservedDomainStable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tunnel.Domain = "example.ngrok-free.app"
	server := &Server{cfg: cfg, tunnelURL: "https://example.ngrok-free.app"}

	summary := strings.Join(server.startupSummary(), "\n")
	if !strings.Contains(summary, "Public") {
		t.Fatalf("summary has no public URL:\n%s", summary)
	}
	if !strings.Contains(summary, "https://example.ngrok-free.app (stable)") {
		t.Errorf("summary does not call the URL stable:\n%s", summary)
	}
}

func TestSummaryWarnsThatARandomURLWillChange(t *testing.T) {
	server := &Server{
		cfg:       config.DefaultConfig(),
		tunnelURL: "https://example.trycloudflare.com",
	}

	summary := strings.Join(server.startupSummary(), "\n")
	if !strings.Contains(summary, "new URL after a restart") {
		t.Errorf("summary does not warn that the URL changes:\n%s", summary)
	}
}

func TestSummaryHasNoPublicLineWithoutATunnel(t *testing.T) {
	server := &Server{cfg: config.DefaultConfig()}

	if summary := strings.Join(server.startupSummary(), "\n"); strings.Contains(summary, "Public") {
		t.Errorf("summary claims a public URL with no tunnel:\n%s", summary)
	}
}

// refuseProvider returns a provider that fails the test if anything starts it.
func refuseProvider(t *testing.T, name string) func() string {
	t.Helper()
	return func() string {
		t.Errorf("%s was started when it should not have been", name)
		return ""
	}
}

func refuseNgrok(t *testing.T) func() string {
	t.Helper()
	return refuseProvider(t, "ngrok")
}
