package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/snakex21/devspace-go/internal/version"
)

// ServerStatusInput asks this server to describe itself.
type ServerStatusInput struct {
	RecentLines int `json:"recentLines,omitempty" jsonschema:"How many lines of the tunnel provider's own output to include, oldest first, capped at 12. Defaults to none. When a tunnel has exited, the reason is usually in these lines."`
}

// ServerStatusOutput is what this server can say about itself.
type ServerStatusOutput struct {
	Result    string       `json:"result" jsonschema:"Human readable summary of the server and its tunnel."`
	Version   string       `json:"version" jsonschema:"Build version of this server."`
	Uptime    string       `json:"uptime" jsonschema:"How long this server has been serving, or 'not started' before it began listening."`
	Address   string       `json:"address,omitempty" jsonschema:"Host and port this server listens on locally."`
	PublicURL string       `json:"publicUrl,omitempty" jsonschema:"URL the tunnel publishes, falling back to the configured public base URL when no tunnel is running. Empty when this server is reachable locally only."`
	Tunnel    TunnelStatus `json:"tunnel" jsonschema:"State of the public tunnel. A provider that has died leaves publicUrl in place, so read tunnel.state rather than assuming the URL still reaches this server."`
}

// handleHealth answers /healthz.
//
// The tunnel block is here because ok on its own was misleading: it only ever
// proved that the local listener was alive, which stayed true while the tunnel
// in front of it was dead and the published URL reached nobody. A monitor that
// polls this endpoint can now see that for itself.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	body := struct {
		OK      bool         `json:"ok"`
		Name    string       `json:"name"`
		Version string       `json:"version"`
		Tunnel  TunnelStatus `json:"tunnel"`
	}{
		OK:      true,
		Name:    "mcp-webcoder",
		Version: version.String(),
		Tunnel:  s.TunnelReport(0),
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Debug().Err(err).Msg("could not write the health response")
	}
}

// serverStatus describes the server and its tunnel.
//
// The tunnel half is the point. From outside, a published URL that has stopped
// working looks exactly like a network problem at the caller's end, and until
// this existed there was no way to tell those apart: /healthz answered ok as
// long as the local listener was alive, which it always was.
func (s *Server) serverStatus(recentLines int) ServerStatusOutput {
	if recentLines < 0 {
		recentLines = 0
	}
	if recentLines > tunnelLogLines {
		recentLines = tunnelLogLines
	}

	status := s.TunnelReport(recentLines)

	uptime := "not started"
	if !s.startedAt.IsZero() {
		uptime = time.Since(s.startedAt).Round(time.Second).String()
	}

	public := status.URL
	if public == "" && s.cfg != nil {
		public = strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	}

	out := ServerStatusOutput{
		Version:   version.String(),
		Uptime:    uptime,
		PublicURL: public,
		Tunnel:    status,
	}
	if s.cfg != nil {
		out.Address = fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	}

	summary := fmt.Sprintf("mcp-webcoder %s, up %s", out.Version, out.Uptime)
	if out.Address != "" {
		summary += ", listening on " + out.Address
	}
	lines := []string{summary}

	switch status.State {
	case tunnelUp:
		line := fmt.Sprintf("Tunnel: up via %s", status.Provider)
		if status.Detail != "" {
			line += " (" + status.Detail + ")"
		}
		if status.Uptime != "" {
			line += ", running for " + status.Uptime
		}
		if public != "" {
			line += ", published at " + public
		}
		lines = append(lines, line)
	case tunnelRestarting:
		lines = append(lines, fmt.Sprintf(
			"Tunnel: %s exited (%s) and is being restarted, so the published URL does not reach this server right now.",
			status.Provider, reasonOrUnknown(status.LastExit)))
	case tunnelDown:
		lines = append(lines, fmt.Sprintf(
			"Tunnel: %s is down after %d restart attempts (%s). Restart this server to try again.",
			status.Provider, status.Restarts, reasonOrUnknown(status.LastExit)))
	case tunnelStopped:
		lines = append(lines, fmt.Sprintf("Tunnel: %s was stopped deliberately.", status.Provider))
	default:
		lines = append(lines, "Tunnel: none configured, so this server is reachable on its local address only.")
	}

	// A restart count is the one number that explains a URL a client can no
	// longer reach, so it is reported even while the tunnel is healthy again.
	if status.State == tunnelUp && status.Restarts > 0 {
		lines = append(lines, fmt.Sprintf(
			"It has been restarted %d time(s) since this server started; the last exit was %s %s.",
			status.Restarts, reasonOrUnknown(status.LastExit), orRecently(status.LastExitAgo)))
	}
	if status.PID != 0 {
		lines = append(lines, fmt.Sprintf("Provider process id: %d", status.PID))
	}
	if len(status.Recent) > 0 {
		lines = append(lines, fmt.Sprintf("Last %d line(s) of %s output:", len(status.Recent), status.Provider))
		for _, line := range status.Recent {
			lines = append(lines, "  "+line)
		}
	}

	out.Result = strings.Join(lines, "\n")
	return out
}

func reasonOrUnknown(reason string) string {
	if reason == "" {
		return "for a reason the provider did not report"
	}
	return reason
}

func orRecently(ago string) string {
	if ago == "" {
		return "recently"
	}
	return ago
}
