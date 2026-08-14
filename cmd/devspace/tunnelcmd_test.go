package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

const testTunnelID = "79b790a5-1434-40f4-90d2-deea39178af7"

func TestSetupCreatesATunnelAndWiresTheConfig(t *testing.T) {
	dir := useTunnelConfig(t)
	calls := []string{}
	run := scriptedCloudflared(t, map[string][]scriptedAnswer{
		"list":   {{output: "[]"}},
		"create": {{output: "Created tunnel mcp-example-com with id " + testTunnelID}},
		"route":  {{output: "Added CNAME mcp.example.com which will route to this tunnel"}},
	}, &calls)

	cfg := loadStoredConfig()
	var out bytes.Buffer
	if err := setupTunnel(cfg, "mcp.example.com", "", run, &out); err != nil {
		t.Fatalf("setupTunnel: %v", err)
	}

	if cfg.Tunnel.Provider != config.TunnelCloudflared {
		t.Errorf("provider is %q, want cloudflared", cfg.Tunnel.Provider)
	}
	if cfg.Tunnel.Cloudflared != testTunnelID {
		t.Errorf("tunnel is %q, want the id %q", cfg.Tunnel.Cloudflared, testTunnelID)
	}
	if want := "cloudflared-mcp-example-com.json"; cfg.Tunnel.Credentials != want {
		t.Errorf("credentials are %q, want %q", cfg.Tunnel.Credentials, want)
	}
	if want := "https://mcp.example.com"; cfg.PublicBaseURL != want {
		t.Errorf("publicUrl is %q, want %q", cfg.PublicBaseURL, want)
	}
	// A tunnel makes the server reachable from anywhere, so setup must not leave
	// it open.
	if cfg.AuthToken == "" {
		t.Error("no bearer token was generated")
	}
	if !credentialsPresent(filepath.Join(dir, "cloudflared-mcp-example-com.json")) {
		t.Error("the credentials file was not written beside the config")
	}
	if !strings.Contains(strings.Join(calls, "\n"), "--credentials-file") {
		t.Errorf("calls were %v, want the credentials file requested", calls)
	}
}

func TestSetupReusesATunnelThatAlreadyExists(t *testing.T) {
	dir := useTunnelConfig(t)
	calls := []string{}
	// No answer for "create": asking to create a second tunnel fails the test.
	run := scriptedCloudflared(t, map[string][]scriptedAnswer{
		"list":  {{output: `[{"id":"` + testTunnelID + `","name":"webcoder"}]`}},
		"token": {{output: ""}},
		"route": {{output: "Added CNAME"}},
	}, &calls)

	cfg := loadStoredConfig()
	var out bytes.Buffer
	if err := setupTunnel(cfg, "mcp.example.com", "webcoder", run, &out); err != nil {
		t.Fatalf("setupTunnel: %v", err)
	}

	if want := "cloudflared-webcoder.json"; cfg.Tunnel.Credentials != want {
		t.Errorf("credentials are %q, want %q", cfg.Tunnel.Credentials, want)
	}
	if !credentialsPresent(filepath.Join(dir, "cloudflared-webcoder.json")) {
		t.Error("credentials were not written for the existing tunnel")
	}
	if !strings.Contains(strings.Join(calls, "\n"), "tunnel token") {
		t.Errorf("calls were %v, want credentials fetched for the existing tunnel", calls)
	}
}

// The second run is the one that matters: everything is in place, the DNS record
// exists, and nothing should be created, rewritten or refused.
func TestSetupRunTwiceChangesNothing(t *testing.T) {
	dir := useTunnelConfig(t)
	credentials := filepath.Join(dir, "cloudflared-webcoder.json")
	if err := os.WriteFile(credentials, []byte(`{"TunnelSecret":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := []string{}
	run := scriptedCloudflared(t, map[string][]scriptedAnswer{
		"list": {{output: `[{"id":"` + testTunnelID + `","name":"webcoder"}]`}},
		"route": {{
			output: "failed to add route: code: 1003, reason: An A, AAAA, or CNAME record with that host already exists.",
			err:    errors.New("exit status 1"),
		}},
	}, &calls)

	cfg := loadStoredConfig()
	// As if a previous run had stored the id.
	cfg.Tunnel.Cloudflared = testTunnelID
	var out bytes.Buffer
	if err := setupTunnel(cfg, "mcp.example.com", "", run, &out); err != nil {
		t.Fatalf("setupTunnel: %v", err)
	}

	if cfg.Tunnel.Cloudflared != testTunnelID {
		t.Errorf("tunnel is %q, want it unchanged", cfg.Tunnel.Cloudflared)
	}
	if want := "cloudflared-webcoder.json"; cfg.Tunnel.Credentials != want {
		t.Errorf("credentials are %q, want %q", cfg.Tunnel.Credentials, want)
	}
	content, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "kept") {
		t.Errorf("the credentials file holds %q, want it untouched", content)
	}
}

func TestSetupStopsWhenTheRouteIsRefusedForAnotherReason(t *testing.T) {
	useTunnelConfig(t)
	calls := []string{}
	run := scriptedCloudflared(t, map[string][]scriptedAnswer{
		"list":   {{output: "[]"}},
		"create": {{output: "Created tunnel with id " + testTunnelID}},
		"route": {{
			output: "failed to add route: code: 1004, reason: zone not found",
			err:    errors.New("exit status 1"),
		}},
	}, &calls)

	cfg := loadStoredConfig()
	var out bytes.Buffer
	err := setupTunnel(cfg, "mcp.example.com", "", run, &out)
	if err == nil {
		t.Fatal("a refused route was reported as success")
	}
	if !strings.Contains(err.Error(), "zone not found") {
		t.Errorf("error was %q, want cloudflared's own reason", err)
	}
	if cfg.PublicBaseURL == "https://mcp.example.com" {
		t.Error("the config was pointed at a hostname that was never routed")
	}
}

// A machine that has never been authorised has no cert.pem, which is a step to
// take rather than an error to report.
func TestSetupLogsInWhenTheMachineHasNoCertificate(t *testing.T) {
	useTunnelConfig(t)
	calls := []string{}
	run := scriptedCloudflared(t, map[string][]scriptedAnswer{
		"list": {
			{
				output: "Cannot determine default origin certificate path. No file cert.pem in [~/.cloudflared]",
				err:    errors.New("exit status 1"),
			},
			{output: "[]"},
		},
		"login":  {{output: "Please open the following URL and log in"}},
		"create": {{output: "Created tunnel with id " + testTunnelID}},
		"route":  {{output: "Added CNAME"}},
	}, &calls)

	cfg := loadStoredConfig()
	var out bytes.Buffer
	if err := setupTunnel(cfg, "mcp.example.com", "", run, &out); err != nil {
		t.Fatalf("setupTunnel: %v", err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "tunnel login") {
		t.Errorf("calls were %v, want a login", calls)
	}
}

func TestSetupReportsAFailureThatIsNotAboutLogin(t *testing.T) {
	useTunnelConfig(t)
	calls := []string{}
	run := scriptedCloudflared(t, map[string][]scriptedAnswer{
		"list": {{output: "dial tcp: lookup api.cloudflare.com: no such host", err: errors.New("exit status 1")}},
	}, &calls)

	cfg := loadStoredConfig()
	var out bytes.Buffer
	err := setupTunnel(cfg, "mcp.example.com", "", run, &out)
	if err == nil {
		t.Fatal("a failed list was reported as success")
	}
	if !strings.Contains(err.Error(), "no such host") {
		t.Errorf("error was %q, want the underlying reason", err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "login") {
		t.Errorf("calls were %v, want no login for an unrelated failure", calls)
	}
}

func TestTheHostnameHasToBeAHostname(t *testing.T) {
	if got, err := tunnelHostname("  https://MCP.Example.com/  "); err != nil || got != "mcp.example.com" {
		t.Errorf("got %q, %v; want mcp.example.com", got, err)
	}
	for _, raw := range []string{"", "   ", "example", "mcp.example.com/mcp"} {
		if got, err := tunnelHostname(raw); err == nil {
			t.Errorf("%q was accepted as %q", raw, got)
		}
	}
}

func TestTheTunnelNameFollowsTheHostname(t *testing.T) {
	if got := tunnelNameFor("MCP.Example.com"); got != "mcp-example-com" {
		t.Errorf("got %q, want mcp-example-com", got)
	}
	if got := credentialsFileName("web coder"); got != "cloudflared-web-coder.json" {
		t.Errorf("got %q, want a safe file name", got)
	}
}

func TestTheTunnelIDIsFoundInEitherOutputShape(t *testing.T) {
	cases := []string{
		"Created tunnel webcoder with id " + testTunnelID,
		`{"id":"` + testTunnelID + `","name":"webcoder"}`,
		"2026-08-14T13:00:00Z INF Tunnel credentials written\nCreated tunnel webcoder with id " + testTunnelID + "\n",
	}
	for _, output := range cases {
		if got := tunnelIDFromCreate(output); got != testTunnelID {
			t.Errorf("read %q from %q", got, output)
		}
	}
	if got := tunnelIDFromCreate("nothing useful here"); got != "" {
		t.Errorf("invented the id %q", got)
	}
}

func TestAnUnknownTunnelCommandExplainsItself(t *testing.T) {
	var out bytes.Buffer
	if code := runTunnel([]string{"provision"}, &out); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(out.String(), "tunnel setup") {
		t.Errorf("output was %q, want the usage", out.String())
	}
}

func TestSetupWithoutAHostnameShowsTheUsage(t *testing.T) {
	var out bytes.Buffer
	if code := runTunnelSetup(nil, &out); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(out.String(), "<hostname>") {
		t.Errorf("output was %q, want the usage", out.String())
	}
}

// useTunnelConfig gives the test its own config folder and keeps the machine's
// real cloudflared config and environment out of the result.
func useTunnelConfig(t *testing.T) string {
	t.Helper()
	dir := useTempConfig(t)
	t.Setenv("WEBCODER_AUTH_TOKEN", "")
	t.Setenv("TUNNEL_CONFIG", filepath.Join(t.TempDir(), "absent.yml"))
	return dir
}

type scriptedAnswer struct {
	output string
	err    error
}

// scriptedCloudflared answers each subcommand from a script, records what it was
// asked, and writes a credentials file wherever cloudflared was told to put one.
func scriptedCloudflared(t *testing.T, answers map[string][]scriptedAnswer, calls *[]string) cloudflaredRunner {
	t.Helper()

	return func(args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if len(args) < 2 {
			t.Fatalf("cloudflared was called with %v", args)
		}

		for index, arg := range args {
			if arg != "--credentials-file" && arg != "--cred-file" {
				continue
			}
			if index+1 >= len(args) {
				t.Fatalf("%v asks for a credentials file without a path", args)
			}
			if err := os.WriteFile(args[index+1], []byte(`{"TunnelSecret":"written"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		remaining, ok := answers[args[1]]
		if !ok || len(remaining) == 0 {
			t.Fatalf("unexpected cloudflared call: %v", args)
		}
		answers[args[1]] = remaining[1:]
		return remaining[0].output, remaining[0].err
	}
}
