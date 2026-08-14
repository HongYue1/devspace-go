package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheCommandLinePublishesThePortTheServerListensOn(t *testing.T) {
	got := strings.Join(namedTunnelArgs("127.0.0.1", 7676, "webcoder", ""), " ")

	want := "tunnel run --url http://127.0.0.1:7676 webcoder"
	if got != want {
		t.Errorf("args are %q, want %q", got, want)
	}
}

func TestConfiguredCredentialsReachCloudflared(t *testing.T) {
	credentials := filepath.Join("app", ".webcoder", "tunnel.json")
	args := namedTunnelArgs("127.0.0.1", 7676, "79b790a5", credentials)

	if !strings.Contains(strings.Join(args, " "), "--credentials-file "+credentials) {
		t.Errorf("args are %v, want the credentials file", args)
	}
	// cloudflared reads the tunnel to run as the last argument, so a flag added
	// ahead of it must not push it out of place.
	if args[len(args)-1] != "79b790a5" {
		t.Errorf("args are %v, want the tunnel last", args)
	}
}

func TestAConfigFileWithIngressIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	body := "tunnel: 79b790a5\ncredentials-file: creds.json\ningress:\n  - service: http_status:404\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if got := configFileWithIngress([]string{path}); got != path {
		t.Errorf("reported %q, want %q", got, path)
	}
}

func TestAConfigFileWithoutIngressIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte("tunnel: 79b790a5\n# ingress: was here\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if got := configFileWithIngress([]string{path, filepath.Join(dir, "absent.yml")}); got != "" {
		t.Errorf("reported %q, want nothing", got)
	}
}

func TestAnExplicitConfigEnvIsTheOnlyCandidate(t *testing.T) {
	chosen := filepath.Join("app", "cloudflared.yml")
	t.Setenv("TUNNEL_CONFIG", "  "+chosen+"  ")

	candidates := cloudflaredConfigCandidates()
	if len(candidates) != 1 || candidates[0] != chosen {
		t.Errorf("candidates are %v, want just %q", candidates, chosen)
	}
}
