package server

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/snakex21/devspace-go/internal/locales"
	"github.com/snakex21/devspace-go/internal/tunnel"
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

	credentials := s.cfg.CredentialsFile()
	if credentials != "" {
		if _, err := os.Stat(credentials); err != nil {
			fmt.Println()
			fmt.Printf("    tunnel.credentials points at %s, which cannot be read\n", credentials)
			fmt.Println("    create the tunnel again, or reset the setting to use the cloudflared default")
			return ""
		}
	}

	if conflict := configFileWithIngress(cloudflaredConfigCandidates()); conflict != "" {
		fmt.Println()
		fmt.Printf("    %s defines ingress rules, which can outrank the port published here\n", conflict)
		fmt.Println("    rename that file if the tunnel does not reach this server")
	}

	fmt.Println()
	fmt.Printf("  %s\n", locales.T("tunnel.starting_cloudflared"))
	fmt.Printf("    %s\n", tunnelExe)
	fmt.Printf("    tunnel %s on %s\n", name, public)
	fmt.Println()

	res := s.runTunnel(tunnelSpec{
		provider: "cloudflared",
		detail:   "named tunnel " + name,
		timeout:  namedTunnelTimeout,
		build: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, tunnelExe, namedTunnelArgs(s.cfg.Host, s.cfg.Port, name, credentials)...)
		},
		// The hostname was routed when the tunnel was created, so the only
		// thing to wait for is the edge accepting a connection. That makes the
		// public URL known in advance, and reported the moment it is reachable.
		match: func(line string) string {
			if strings.Contains(line, namedTunnelReady) {
				return public
			}
			return ""
		},
	})

	switch {
	case res.err != nil:
		fmt.Printf("  warning: %s (cloudflared): %v\n", locales.T("error.cmd_failed"), res.err)
		return ""
	case res.url == "":
		res.output.report()
		return ""
	}
	return res.url
}

// namedTunnelArgs builds the cloudflared command line.
//
// The origin is passed as --url so it always matches the port this server
// listens on, and credentials are passed explicitly when configured, so the app
// folder carries the tunnel rather than the home folder. Credentials identify
// the tunnel by UUID, so naming it by UUID avoids needing the login
// certificate as well.
func namedTunnelArgs(host string, port int, name, credentials string) []string {
	args := []string{"tunnel", "run", "--url", fmt.Sprintf("http://%s:%d", host, port)}
	if credentials != "" {
		args = append(args, "--credentials-file", credentials)
	}
	return append(args, name)
}

// These two live in internal/tunnel, where the setup command uses them as well,
// so a warning here and a warning there always name the same file.
func cloudflaredConfigCandidates() []string {
	return tunnel.CloudflaredConfigCandidates()
}

func configFileWithIngress(candidates []string) string {
	return tunnel.ConfigFileWithIngress(candidates)
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
