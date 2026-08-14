package config

import "testing"

func TestTheAuthTokenComesFromTheEnvironment(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")
	t.Setenv("WEBCODER_AUTH_TOKEN", "  s3cret-token-value  ")

	if cfg := LoadConfig(); cfg.AuthToken != "s3cret-token-value" {
		t.Errorf("token is %q, want it trimmed", cfg.AuthToken)
	}
}

func TestTheAuthTokenComesFromTheFile(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, `{"authToken":"s3cret-token-value"}`)

	if cfg := LoadConfig(); cfg.AuthToken != "s3cret-token-value" {
		t.Errorf("token is %q, want the one from the file", cfg.AuthToken)
	}
}

// No token by default: a loopback-only run is protected by the machine, and
// requiring one there would break every existing local setup.
func TestAFreshConfigHasNoAuthToken(t *testing.T) {
	clearTunnelEnv(t)
	tunnelConfigDir(t, "")

	if cfg := LoadConfig(); cfg.AuthToken != "" {
		t.Errorf("a fresh config already carries a token: %q", cfg.AuthToken)
	}
}
