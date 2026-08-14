package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/snakex21/devspace-go/internal/tunnel"
)

// ngrokURLTimeout is how long the agent gets to report a public URL. It has to
// reach ngrok's edge and claim the domain, which takes longer than cloudflared's
// quick tunnel, so it gets more room.
const ngrokURLTimeout = 45 * time.Second

// startNgrok publishes the server through the ngrok agent.
//
// This is the only provider here that can keep one URL across restarts, so a
// client can be pointed at it once and then left alone. That only holds with a
// reserved domain configured; without one the agent issues a random hostname,
// and the caller is told so rather than left to find out after the next restart.
func (s *Server) startNgrok() string {
	agent, err := s.ngrokAgent()
	if err != nil {
		fmt.Printf("    ngrok: %v\n", err)
		return ""
	}

	fmt.Println()
	if s.cfg.Tunnel.Domain != "" {
		fmt.Printf("    starting ngrok on %s\n", s.cfg.Tunnel.Domain)
	} else {
		fmt.Println("    starting ngrok with no reserved domain, so this URL will not survive a restart")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, agent, tunnel.NgrokArgs(s.cfg.Host, s.cfg.Port, s.cfg.Tunnel.Domain)...)
	if token := s.cfg.Tunnel.Authtoken; token != "" {
		// Handed over as an environment variable rather than an argument, so the
		// token does not show up in a process list.
		cmd.Env = append(os.Environ(), "NGROK_AUTHTOKEN="+token)
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		fmt.Printf("    ngrok would not start: %v\n", err)
		cancel()
		return ""
	}

	found := make(chan string, 1)
	failed := make(chan string, 1)
	output := newTunnelLog("ngrok")

	watch := func(stream io.Reader) {
		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)

			if url := tunnel.URLFromNgrokLine(line); url != "" {
				select {
				case found <- url:
				default:
				}
				return
			}
			if code := tunnel.NgrokFatalLine(line); code != "" {
				select {
				case failed <- code:
				default:
				}
				return
			}
		}
	}
	go watch(stdout)
	go watch(stderr)

	select {
	case url := <-found:
		s.tunnelStop = cancel
		printTunnelURL(url)
		return url
	case code := <-failed:
		// Waiting out the whole timeout adds nothing once the agent has said it
		// is giving up.
		cancel()
		fmt.Printf("    ngrok gave up: %s\n", code)
		output.report()
		s.ngrokTokenHint()
		return ""
	case <-time.After(ngrokURLTimeout):
		cancel()
		output.report()
		s.ngrokTokenHint()
		return ""
	}
}

// ngrokAgent returns the agent to run, fetching it once if this machine has none.
func (s *Server) ngrokAgent() (string, error) {
	if existing := tunnel.FindNgrok(); existing != "" {
		return existing, nil
	}

	toolsDir := tunnel.ToolsDir()
	fmt.Println()
	fmt.Printf("    fetching the ngrok agent into %s\n", toolsDir)

	agent, downloaded, err := tunnel.EnsureNgrok(toolsDir, nil)
	if err != nil {
		return "", err
	}
	if downloaded {
		fmt.Printf("    installed %s\n", agent)
	}
	return agent, nil
}

// ngrokTokenHint explains the most common reason the agent refuses to run.
func (s *Server) ngrokTokenHint() {
	if s.cfg.Tunnel.Authtoken == "" {
		fmt.Println("    ngrok needs an authtoken: mcp-webcoder config set tunnel.authtoken YOUR_TOKEN")
	}
}
