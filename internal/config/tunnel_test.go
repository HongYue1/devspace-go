package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTunnelDefaultsToAuto(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")

	cfg := LoadConfig()
	if cfg.Tunnel.Provider != TunnelAuto {
		t.Errorf("provider is %q, want auto", cfg.Tunnel.Provider)
	}
	if cfg.Tunnel.Domain != "" || cfg.Tunnel.Authtoken != "" {
		t.Errorf("a fresh config already carries %q/%q", cfg.Tunnel.Domain, cfg.Tunnel.Authtoken)
	}
}

func TestTunnelSettingsComeFromTheEnvironment(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")
	t.Setenv("WEBCODER_TUNNEL_PROVIDER", "NGROK")
	t.Setenv("WEBCODER_TUNNEL_DOMAIN", "https://Example.ngrok-free.app/")
	t.Setenv("WEBCODER_TUNNEL_AUTHTOKEN", "  token  ")

	cfg := LoadConfig()
	if cfg.Tunnel.Provider != TunnelNgrok {
		t.Errorf("provider is %q, want ngrok", cfg.Tunnel.Provider)
	}
	if cfg.Tunnel.Domain != "example.ngrok-free.app" {
		t.Errorf("domain is %q, want it normalized", cfg.Tunnel.Domain)
	}
	if cfg.Tunnel.Authtoken != "token" {
		t.Errorf("authtoken is %q, want it trimmed", cfg.Tunnel.Authtoken)
	}
}

// The agent reads NGROK_AUTHTOKEN itself, so a machine that is already set up
// that way should not have to configure the token twice.
func TestTheAgentsOwnAuthtokenEnvIsHonoured(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")
	t.Setenv("NGROK_AUTHTOKEN", "agent-token")

	if cfg := LoadConfig(); cfg.Tunnel.Authtoken != "agent-token" {
		t.Errorf("authtoken is %q, want agent-token", cfg.Tunnel.Authtoken)
	}
}

func TestTheDedicatedAuthtokenEnvWins(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")
	t.Setenv("WEBCODER_TUNNEL_AUTHTOKEN", "chosen")
	t.Setenv("NGROK_AUTHTOKEN", "agent-token")

	if cfg := LoadConfig(); cfg.Tunnel.Authtoken != "chosen" {
		t.Errorf("authtoken is %q, want chosen", cfg.Tunnel.Authtoken)
	}
}

func TestTunnelSettingsComeFromTheFile(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, `{"tunnel":{"provider":"ngrok","domain":"https://Example.ngrok-free.app/","authtoken":"stored"}}`)

	cfg := LoadConfig()
	if cfg.Tunnel.Provider != TunnelNgrok {
		t.Errorf("provider is %q, want ngrok", cfg.Tunnel.Provider)
	}
	if cfg.Tunnel.Domain != "example.ngrok-free.app" {
		t.Errorf("domain is %q, want it normalized", cfg.Tunnel.Domain)
	}
	if cfg.Tunnel.Authtoken != "stored" {
		t.Errorf("authtoken is %q, want stored", cfg.Tunnel.Authtoken)
	}
}

func TestAFileWithoutATunnelBlockKeepsTheDefault(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, `{"port":7676}`)

	if cfg := LoadConfig(); cfg.Tunnel.Provider != TunnelAuto {
		t.Errorf("provider is %q, want auto", cfg.Tunnel.Provider)
	}
}

// A file written by hand can name the block and leave the provider out.
func TestAnEmptyProviderInTheFileFallsBackToAuto(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, `{"tunnel":{"domain":"example.ngrok-free.app"}}`)

	cfg := LoadConfig()
	if cfg.Tunnel.Provider != TunnelAuto {
		t.Errorf("provider is %q, want auto", cfg.Tunnel.Provider)
	}
	if cfg.Tunnel.Domain != "example.ngrok-free.app" {
		t.Errorf("domain is %q", cfg.Tunnel.Domain)
	}
}

func TestNormalizeTunnelDomainAcceptsWhatTheDashboardShows(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"example.ngrok-free.app", "example.ngrok-free.app"},
		{"https://example.ngrok-free.app", "example.ngrok-free.app"},
		{"http://example.ngrok-free.app/", "example.ngrok-free.app"},
		{"  https://Example.NGROK-free.app/  ", "example.ngrok-free.app"},
		{"", ""},
	}

	for _, test := range tests {
		if got := NormalizeTunnelDomain(test.in); got != test.want {
			t.Errorf("%q became %q, want %q", test.in, got, test.want)
		}
	}
}

func TestTunnelProvidersListsEveryChoice(t *testing.T) {
	want := []TunnelProvider{TunnelAuto, TunnelNgrok, TunnelCloudflared, TunnelPinggy, TunnelOff}
	got := TunnelProviders()

	if len(got) != len(want) {
		t.Fatalf("providers are %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("provider %d is %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTheNamedTunnelComesFromTheEnvironment(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")
	t.Setenv("WEBCODER_TUNNEL_CLOUDFLARED", "  webcoder  ")

	if cfg := LoadConfig(); cfg.Tunnel.Cloudflared != "webcoder" {
		t.Errorf("named tunnel is %q, want it trimmed", cfg.Tunnel.Cloudflared)
	}
}

func TestTheNamedTunnelComesFromTheFile(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, `{"tunnel":{"provider":"cloudflared","cloudflared":"webcoder"}}`)

	cfg := LoadConfig()
	if cfg.Tunnel.Provider != TunnelCloudflared {
		t.Errorf("provider is %q, want cloudflared", cfg.Tunnel.Provider)
	}
	if cfg.Tunnel.Cloudflared != "webcoder" {
		t.Errorf("named tunnel is %q, want webcoder", cfg.Tunnel.Cloudflared)
	}
}

// A fresh config has no tunnel named, which is what keeps the quick tunnel as
// the default behaviour.
func TestAFreshConfigNamesNoTunnel(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")

	if cfg := LoadConfig(); cfg.Tunnel.Cloudflared != "" {
		t.Errorf("a fresh config already names %q", cfg.Tunnel.Cloudflared)
	}
}

// clearTunnelEnv keeps a real environment from leaking into these tests.
func clearTunnelEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"WEBCODER_TUNNEL_PROVIDER",
		"WEBCODER_TUNNEL_DOMAIN",
		"WEBCODER_TUNNEL_AUTHTOKEN",
		"WEBCODER_TUNNEL_CLOUDFLARED",
		"WEBCODER_AUTH_TOKEN",
		"NGROK_AUTHTOKEN",
	} {
		t.Setenv(name, "")
	}
}

// tunnelConfigDir points LoadConfig at a throwaway folder, holding the given
// config file when one is wanted.
func tunnelConfigDir(t *testing.T, contents string) string {
	t.Helper()

	dir := t.TempDir()
	if contents != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WEBCODER_CONFIG_DIR", dir)
	return dir
}
