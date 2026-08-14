package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

// cloudflaredIngressKey is the config-file key that makes cloudflared refuse
// --url. A config file it loads on its own therefore decides which port is
// published, which is the one failure that looks unrelated to its cause.
const cloudflaredIngressKey = "ingress:"

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
		fmt.Printf("    %s defines ingress rules, so cloudflared will refuse --url\n", conflict)
		fmt.Println("    rename that file to let this server publish the port it already listens on")
	}

	fmt.Println()
	fmt.Printf("🔗  %s\n", locales.T("tunnel.starting_cloudflared"))
	fmt.Printf("    %s\n", tunnelExe)
	fmt.Printf("    tunnel %s on %s\n", name, public)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, tunnelExe, namedTunnelArgs(s.cfg.Host, s.cfg.Port, name, credentials)...)

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

// cloudflaredConfigCandidates lists the config files cloudflared loads by
// itself, in the order it looks for them.
func cloudflaredConfigCandidates() []string {
	if explicit := strings.TrimSpace(os.Getenv("TUNNEL_CONFIG")); explicit != "" {
		return []string{explicit}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".cloudflared", "config.yml"),
		filepath.Join(home, ".cloudflared", "config.yaml"),
	}
}

// configFileWithIngress reports the first candidate that defines ingress rules,
// so the reason cloudflared is about to refuse --url can be named before it
// does. A commented-out section does not count.
func configFileWithIngress(candidates []string) string {
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), cloudflaredIngressKey) {
				return path
			}
		}
	}
	return ""
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
