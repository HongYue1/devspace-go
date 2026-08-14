package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/snakex21/devspace-go/internal/locales"
)

// namedTunnelTimeout is how long cloudflared gets to register a connection. A
// named tunnel authenticates and then dials the edge, which takes longer than a
// quick tunnel printing a hostname.
const namedTunnelTimeout = 45 * time.Second

// namedTunnelReady is the line cloudflared prints once the edge has accepted a
// connection. There is nothing else to wait for: the hostname was routed to the
// tunnel when it was created, not now.
const namedTunnelReady = "Registered tunnel connection"

// startNamedCloudflared runs a tunnel the Cloudflare account already owns.
//
// The URL survives restarts, so a client can be pointed at it once, which a
// quick tunnel cannot offer. What it costs is a hostname this process cannot
// discover: cloudflared prints the tunnel's UUID and never the route, so the
// public URL has to come from publicUrl.
//
// Nothing is installed. The ingress rule is passed as --url instead of a
// config file so it always matches the port this server actually listens on,
// and the process is a child of this one, so it goes away when the server does.
// A copied folder therefore stays self-contained: no service, no state outside
// the Cloudflare credentials the account holder created.
func (s *Server) startNamedCloudflared(name string) string {
	tunnelExe := findCloudflaredExecutable()
	if tunnelExe == "" {
		fmt.Println("    cloudflared is not in tools/ or on PATH")
		return ""
	}

	public := strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	if !isRoutableTunnelURL(public) {
		fmt.Println()
		fmt.Printf("    tunnel.cloudflared names %q, but publicUrl is %s\n", name, s.cfg.PublicBaseURL)
		fmt.Println("    cloudflared cannot report the hostname routed to a tunnel, so set it here:")
		fmt.Println("    mcp-webcoder config set publicUrl https://mcp.example.com")
		return ""
	}

	fmt.Println()
	fmt.Printf("🔗  %s\n", locales.T("tunnel.starting_cloudflared"))
	fmt.Printf("    %s\n", tunnelExe)
	fmt.Printf("    tunnel %s on %s\n", name, public)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, tunnelExe,
		"tunnel", "run",
		"--url", fmt.Sprintf("http://%s:%d", s.cfg.Host, s.cfg.Port),
		name,
	)

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		fmt.Printf("⚠️  %s (cloudflared): %v\n", locales.T("error.cmd_failed"), err)
		cancel()
		return ""
	}

	ready := make(chan struct{}, 1)
	output := newTunnelLog("cloudflared")

	watch := func(stream io.Reader) {
		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)
			if strings.Contains(line, namedTunnelReady) {
				select {
				case ready <- struct{}{}:
				default:
				}
				return
			}
		}
	}
	go watch(stdout)
	go watch(stderr)

	select {
	case <-ready:
		s.tunnelStop = cancel
		printTunnelURL(public)
		return public
	case <-time.After(namedTunnelTimeout):
		cancel()
		output.report()
		return ""
	}
}

// isRoutableTunnelURL reports whether a configured publicUrl can front a tunnel.
//
// The default is the loopback address the server listens on, and announcing
// that as the public URL would be worse than announcing nothing: the startup
// banner would look correct while every client failed to connect.
func isRoutableTunnelURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}

	switch strings.ToLower(parsed.Hostname()) {
	case "", "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return false
	}
	return true
}
