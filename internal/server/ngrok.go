package server

import (
	"context"
	"fmt"
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

	res := s.runTunnel(tunnelSpec{
		provider: "ngrok",
		detail:   s.cfg.Tunnel.Domain,
		timeout:  ngrokURLTimeout,
		build: func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, agent, tunnel.NgrokArgs(s.cfg.Host, s.cfg.Port, s.cfg.Tunnel.Domain)...)
			if token := s.cfg.Tunnel.Authtoken; token != "" {
				// Handed over as an environment variable rather than an
				// argument, so the token does not show up in a process list.
				cmd.Env = append(os.Environ(), "NGROK_AUTHTOKEN="+token)
			}
			return cmd
		},
		match: tunnel.URLFromNgrokLine,
		fatal: tunnel.NgrokFatalLine,
	})

	switch {
	case res.err != nil:
		fmt.Printf("    ngrok would not start: %v\n", res.err)
		return ""
	case res.reason != "":
		// Waiting out the whole timeout adds nothing once the agent has said it
		// is giving up.
		fmt.Printf("    ngrok gave up: %s\n", res.reason)
		res.output.report()
		s.ngrokTokenHint()
		return ""
	case res.url == "":
		res.output.report()
		s.ngrokTokenHint()
		return ""
	}
	return res.url
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
