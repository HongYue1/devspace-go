package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
)

// testTunnelURL matches the fake hostnames the helper process prints.
var testTunnelURL = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.trycloudflare\.com`).FindString

// TestTunnelHelperProcess stands in for cloudflared, ngrok or ssh.
//
// It is this same test binary re-executed with GO_TUNNEL_HELPER set, which is
// the only way to get a child process that misbehaves on purpose without
// needing a real tunnel provider installed on the machine running the tests.
func TestTunnelHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_TUNNEL_HELPER")
	if mode == "" {
		t.Skip("not a helper invocation")
	}

	switch mode {
	case "flood-after-url":
		// The URL first, so the parent stops waiting for it, and then far more
		// output than a pipe buffer will hold. A parent that stops reading once
		// it has the URL leaves this process blocked in a write forever, which
		// is exactly how a live tunnel used to wedge.
		fmt.Println("https://flood.trycloudflare.com")
		filler := strings.Repeat("x", 200)
		for i := 0; i < 10000; i++ {
			fmt.Printf("%d %s\n", i, filler)
		}
		fmt.Println("SENTINEL-PAST-THE-FLOOD")
		time.Sleep(time.Minute)
	case "long-line-before-url":
		// One line far past any single read buffer, with the URL behind it.
		fmt.Println(strings.Repeat("y", 300*1024))
		fmt.Println("https://long-line.trycloudflare.com")
		fmt.Println("SENTINEL-PAST-THE-LONG-LINE")
		time.Sleep(time.Minute)
	case "exit-after-url":
		fmt.Println("https://restarted.trycloudflare.com")
		os.Exit(3)
	case "linger-after-url":
		fmt.Println("https://lingering.trycloudflare.com")
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// helperTunnel builds a provider command that is this test binary in helper mode.
func helperTunnel(mode string) func(context.Context) *exec.Cmd {
	self := os.Args[0]
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, self, "-test.run=^TestTunnelHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_TUNNEL_HELPER="+mode)
		return cmd
	}
}

// waitForTunnel polls because these outcomes come from a supervisor goroutine
// and a child process, so there is nothing to synchronise on directly.
func waitForTunnel(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(limit)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, limit)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sawLine(output *tunnelLog, want string) bool {
	if output == nil {
		return false
	}
	for _, line := range output.tail(tunnelLogLines) {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func TestAFloodOfProviderOutputDoesNotWedgeTheTunnel(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	t.Cleanup(s.stopTunnel)

	res := s.runTunnel(tunnelSpec{
		provider: "cloudflared",
		detail:   "flood",
		timeout:  30 * time.Second,
		build:    helperTunnel("flood-after-url"),
		match:    testTunnelURL,
	})
	if res.url != "https://flood.trycloudflare.com" {
		t.Fatalf("no URL from the provider: url=%q err=%v reason=%q", res.url, res.err, res.reason)
	}

	// The sentinel is written after two megabytes, so it only ever arrives if
	// the pipes kept being drained after the URL was matched.
	waitForTunnel(t, 30*time.Second, "the output past the flood", func() bool {
		return sawLine(res.output, "SENTINEL-PAST-THE-FLOOD")
	})
}

func TestALineTooLongForOneBufferDoesNotStopTheDrain(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	t.Cleanup(s.stopTunnel)

	res := s.runTunnel(tunnelSpec{
		provider: "cloudflared",
		detail:   "long line",
		timeout:  30 * time.Second,
		build:    helperTunnel("long-line-before-url"),
		match:    testTunnelURL,
	})
	if res.url != "https://long-line.trycloudflare.com" {
		t.Fatalf("a line longer than one read buffer hid the URL behind it: url=%q err=%v", res.url, res.err)
	}

	waitForTunnel(t, 30*time.Second, "the output past the long line", func() bool {
		return sawLine(res.output, "SENTINEL-PAST-THE-LONG-LINE")
	})
	if !sawLine(res.output, "line truncated at") {
		t.Error("an oversized line should be recorded as truncated, not dropped or glued to the next one")
	}
}

func TestATunnelThatDiesIsRestartedAndThenGivenUpOn(t *testing.T) {
	s := &Server{
		cfg:          config.DefaultConfig(),
		restartBase:  time.Millisecond,
		restartMax:   5 * time.Millisecond,
		restartLimit: 2,
	}
	t.Cleanup(s.stopTunnel)

	res := s.runTunnel(tunnelSpec{
		provider: "cloudflared",
		detail:   "dies at once",
		timeout:  30 * time.Second,
		build:    helperTunnel("exit-after-url"),
		match:    testTunnelURL,
	})
	if res.url == "" {
		t.Fatalf("no URL from the provider: err=%v reason=%q", res.err, res.reason)
	}

	waitForTunnel(t, 30*time.Second, "a restart", func() bool {
		return s.TunnelReport(0).Restarts >= 1
	})
	waitForTunnel(t, 30*time.Second, "giving up once the restart limit was reached", func() bool {
		return s.TunnelReport(0).State == tunnelDown
	})

	report := s.TunnelReport(0)
	if !strings.Contains(report.LastExit, "exit status 3") {
		t.Errorf("the exit status of the dead provider should be reported, got %q", report.LastExit)
	}

	if result := s.serverStatus(0).Result; !strings.Contains(result, "is down") {
		t.Errorf("server_status should say the tunnel is down, got:\n%s", result)
	}
}

func TestAStoppedTunnelIsNotRestarted(t *testing.T) {
	s := &Server{
		cfg:         config.DefaultConfig(),
		restartBase: time.Millisecond,
		restartMax:  2 * time.Millisecond,
	}

	res := s.runTunnel(tunnelSpec{
		provider: "cloudflared",
		detail:   "lingers",
		timeout:  30 * time.Second,
		build:    helperTunnel("linger-after-url"),
		match:    testTunnelURL,
	})
	if res.url == "" {
		t.Fatalf("no URL from the provider: err=%v reason=%q", res.err, res.reason)
	}
	if got := s.TunnelReport(0).State; got != tunnelUp {
		t.Fatalf("a running tunnel should report %q, got %q", tunnelUp, got)
	}

	s.stopTunnel()
	waitForTunnel(t, 30*time.Second, "the stopped state", func() bool {
		return s.TunnelReport(0).State == tunnelStopped
	})

	// Long enough for a supervisor that treats every exit as a failure to have
	// started a replacement.
	time.Sleep(300 * time.Millisecond)
	if got := s.TunnelReport(0); got.State != tunnelStopped || got.Restarts != 0 {
		t.Fatalf("a tunnel that was asked to stop must not be restarted, got %+v", got)
	}
}

func TestThePinggyCommandLineExitsInsteadOfHanging(t *testing.T) {
	args := pinggyArgs(8080)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"ServerAliveInterval=30",
		"ServerAliveCountMax=3",
		"ExitOnForwardFailure=yes",
		"-R R0:localhost:8080",
		"a.pinggy.io",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the pinggy command line is missing %s: %s", want, joined)
		}
	}

	for _, arg := range args {
		if arg == "-N" {
			t.Error("-N asks for no session channel, and pinggy announces the URL over that channel")
		}
	}
}

func TestTheHealthBodyReportsTheTunnelToo(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}

	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest(http.MethodGet, healthPath, nil))

	var body struct {
		OK     bool   `json:"ok"`
		Name   string `json:"name"`
		Tunnel struct {
			State    string `json:"state"`
			Restarts int    `json:"restarts"`
		} `json:"tunnel"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the health body is not valid json: %v: %s", err, rec.Body.String())
	}

	if !body.OK || body.Name != "mcp-webcoder" {
		t.Errorf("the fields clients already read must not move: %s", rec.Body.String())
	}
	if body.Tunnel.State != tunnelOff {
		t.Errorf("with no tunnel running the state should be %q, got %q", tunnelOff, body.Tunnel.State)
	}
}

func TestServerStatusSaysWhenThereIsNoTunnel(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	out := s.serverStatus(0)

	if out.Tunnel.State != tunnelOff {
		t.Errorf("state = %q, want %q", out.Tunnel.State, tunnelOff)
	}
	if !strings.Contains(out.Result, "none configured") {
		t.Errorf("the summary should say there is no tunnel, got:\n%s", out.Result)
	}
	if !strings.Contains(out.Result, "not started") {
		t.Errorf("a server that never called Start has no uptime to report, got:\n%s", out.Result)
	}
	if out.Version == "" {
		t.Error("the version should always be reported")
	}
}
