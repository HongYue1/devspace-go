package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog/log"
	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/locales"
	"github.com/snakex21/devspace-go/internal/logger"
	"github.com/snakex21/devspace-go/internal/store"
	"github.com/snakex21/devspace-go/internal/tools"
	"github.com/snakex21/devspace-go/internal/tunnel"
	"github.com/snakex21/devspace-go/internal/version"
	"github.com/snakex21/devspace-go/internal/workspace"
)

// boolPtr returns a pointer to the given bool value (for ToolAnnotations pointer fields).
func boolPtr(b bool) *bool { return &b }

const (
	cloudflaredURLTimeout  = 30 * time.Second
	cloudflaredMaxAttempts = 2
)

// Server represents the running MCP WebCoder server.
type Server struct {
	cfg        *config.Config
	httpServer *http.Server
	tunnelStop context.CancelFunc
	tunnelURL  string
	registry   *workspace.Registry
	store      *store.Store
}

// New creates a new MCP WebCoder server.
func New(cfg *config.Config) (*Server, error) {
	logger.Init(string(cfg.Logging.Level), string(cfg.Logging.Format))
	tools.SetShell(cfg.Shell)

	s, err := store.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	registry := workspace.NewRegistry(cfg, s)

	return &Server{
		cfg:      cfg,
		registry: registry,
		store:    s,
	}, nil
}

// Start begins listening for connections.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"name":"mcp-webcoder"}`)
	})

	// MCP endpoint using stateless Streamable HTTP. Workspace state is tracked
	// separately by workspaceId, so transport sessions only add another failure
	// mode when a proxy or web client drops an Mcp-Session-Id between requests.
	handler := s.streamableMCPHandler()

	mux.Handle("/mcp", handler)

	// Legacy SSE endpoint for MCP clients that still expect /sse.
	sseHandler := mcp.NewSSEHandler(
		func(r *http.Request) *mcp.Server {
			return s.createMcpServer()
		},
		&mcp.SSEOptions{DisableLocalhostProtection: true},
	)
	mux.Handle("/sse", sseHandler)

	// Only the header read is bounded: MCP calls stream for as long as the tool
	// needs, so a write or idle deadline would cut them off mid-answer.
	s.httpServer = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port),
		Handler:           s.loggingMiddleware(s.authMiddleware(mux)),
		ReadHeaderTimeout: 20 * time.Second,
	}

	// Publishing the server is non-fatal: a local-only run is still useful.
	s.tunnelURL = s.startTunnel()

	// Graceful shutdown
	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint

		log.Info().Msg(locales.T("server.shutdown"))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := s.httpServer.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("server shutdown error")
		}
		if s.tunnelStop != nil {
			s.tunnelStop()
		}
		if s.store != nil {
			s.store.Close()
		}
		close(idleConnsClosed)
	}()

	// A person reading a console wants one tidy block; a log collector wants
	// the structured lines. Pick by log format instead of printing both.
	if s.cfg.Logging.Format == config.LogText {
		s.printStartupSummary()
	} else {
		log.Info().
			Str("host", s.cfg.Host).
			Int("port", s.cfg.Port).
			Msg(locales.T("server.listening"))

		log.Info().
			Strs("allowed_roots", s.cfg.AllowedRoots).
			Msg(locales.T("server.roots"))

		log.Info().
			Bool("skills", s.cfg.SkillsEnabled).
			Str("tool_mode", string(s.cfg.ToolMode)).
			Str("tool_naming", string(s.cfg.ToolNaming)).
			Msg(locales.T("server.config"))
	}

	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	<-idleConnsClosed
	return nil
}

// startupSummary renders the startup block: what is listening, what it can
// reach, which shell it will use, and how it is configured.
func (s *Server) startupSummary() []string {
	type field struct {
		label string
		value string
	}

	fields := []field{{"Listening", fmt.Sprintf("http://%s:%d", s.cfg.Host, s.cfg.Port)}}

	if len(s.cfg.AllowedRoots) == 0 {
		fields = append(fields, field{"Roots", "none configured"})
	}
	for i, root := range s.cfg.AllowedRoots {
		label := "Roots"
		if i > 0 {
			label = ""
		}
		fields = append(fields, field{label, root})
	}

	shell, fallback, err := tools.ShellStatus()
	switch {
	case err != nil:
		fields = append(fields, field{"Shell", fmt.Sprintf("unavailable: %v", err)})
	case fallback != "":
		fields = append(fields, field{"Shell", shell}, field{"", fallback})
	default:
		fields = append(fields, field{"Shell", shell})
	}

	if s.tunnelURL != "" {
		// Only the configured URL is stable. A fallback provider can hand back a
		// random one while a domain or a tunnel name sits unused in the config,
		// and calling that stable would invite a client to keep it.
		stability := "new URL after a restart"
		switch {
		case s.cfg.Tunnel.Domain != "" && strings.Contains(s.tunnelURL, s.cfg.Tunnel.Domain):
			stability = "stable"
		case s.cfg.Tunnel.Cloudflared != "" && s.tunnelURL == strings.TrimRight(s.cfg.PublicBaseURL, "/"):
			stability = "stable"
		}
		fields = append(fields, field{"Public", fmt.Sprintf("%s (%s)", s.tunnelURL, stability)})
	}

	// Silence on a local-only run, because the machine is the protection there.
	// Once a public URL exists the absence of a token is the most important
	// thing on this screen: it is a remote shell for anyone who finds it.
	switch {
	case s.cfg.AuthToken != "":
		fields = append(fields, field{"Auth", "bearer token required"})
	case s.tunnelURL != "":
		fields = append(fields,
			field{"Auth", "OPEN - anyone with the URL can run commands here"},
			field{"", "close it with: mcp-webcoder config set authToken <token>"},
		)
	}

	fields = append(fields,
		field{"Tools", fmt.Sprintf("%s mode, %s names, skills %s",
			s.cfg.ToolMode, s.cfg.ToolNaming, onOff(s.cfg.SkillsEnabled))},
		field{"Logs", fmt.Sprintf("%s at %s level, request log %s",
			s.cfg.Logging.Format, s.cfg.Logging.Level, onOff(s.cfg.Logging.Requests))},
	)

	lines := make([]string, 0, len(fields))
	for _, f := range fields {
		lines = append(lines, fmt.Sprintf("  %-10s %s", f.label, f.value))
	}
	return lines
}

func (s *Server) printStartupSummary() {
	fmt.Println()
	fmt.Printf("MCP WebCoder %s\n", version.String())
	for _, line := range s.startupSummary() {
		fmt.Println(line)
	}
	fmt.Println()
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func (s *Server) streamableMCPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return s.createMcpServer()
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                  true,
			JSONResponse:               true,
			DisableLocalhostProtection: true,
		},
	)
}

// startTunnel attempts to start a tunnel to expose the server publicly.
// Tries cloudflared first, falls back to pinggy.
// Returns the public URL if successful. Non-fatal.
// tunnelLogLines is how much provider output to keep for a failure report.
const tunnelLogLines = 12

// tunnelLog keeps the tail of a tunnel provider's output.
//
// Every line the provider printed used to go straight to the console, which is
// what buried the URL under cloudflared's banner and progress messages. The
// lines are now debug level, and the tail is printed only when the provider
// fails to produce a URL, so a failure is still explainable.
type tunnelLog struct {
	provider string
	mu       sync.Mutex
	lines    []string
}

func newTunnelLog(provider string) *tunnelLog {
	return &tunnelLog{provider: provider}
}

func (t *tunnelLog) add(line string) {
	log.Debug().Str("provider", t.provider).Msg(line)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > tunnelLogLines {
		t.lines = t.lines[len(t.lines)-tunnelLogLines:]
	}
}

// report prints the captured tail, for when the provider returned no URL.
func (t *tunnelLog) report() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return
	}

	fmt.Printf("    last %s output:\n", t.provider)
	for _, line := range t.lines {
		fmt.Printf("      %s\n", line)
	}
}

// retryCloudflared gives cloudflared a second chance, because its first attempt
// fails often enough on a cold start to be worth the wait.
func retryCloudflared(startCloudflared func() string) string {
	for attempt := 0; attempt < cloudflaredMaxAttempts; attempt++ {
		if url := startCloudflared(); url != "" {
			return url
		}
	}
	return ""
}

func (s *Server) startTunnel() string {
	return s.startTunnelWithProviders(s.startNgrok, s.startCloudflared, s.startPinggy)
}

// startTunnelWithProviders picks a provider from the configuration.
//
// "auto" keeps the historical order, cloudflared then pinggy, unless a domain or
// an authtoken is configured: naming either is a clear request for a URL that
// survives restarts, so ngrok goes first. Naming a provider outright skips the
// fallbacks, because quietly handing back a random URL defeats the point of
// asking for a fixed one.
//
// A named Cloudflare tunnel outranks all of it. Both it and a reserved ngrok
// domain are stable, but only the tunnel names a hostname the account already
// owns and has already routed.
func (s *Server) startTunnelWithProviders(startNgrok, startCloudflared, startPinggy func() string) string {
	// A named tunnel fails for configuration reasons, a wrong name or missing
	// credentials, which a second attempt would only repeat.
	named := s.cfg.Tunnel.Cloudflared != ""
	runCloudflared := func() string {
		if named {
			return startCloudflared()
		}
		return retryCloudflared(startCloudflared)
	}

	switch s.cfg.Tunnel.Provider {
	case config.TunnelOff:
		return ""
	case config.TunnelNgrok:
		return startNgrok()
	case config.TunnelCloudflared:
		return runCloudflared()
	case config.TunnelPinggy:
		return startPinggy()
	}

	if named {
		if url := runCloudflared(); url != "" {
			return url
		}
	}

	if s.cfg.Tunnel.Domain != "" || s.cfg.Tunnel.Authtoken != "" {
		if url := startNgrok(); url != "" {
			return url
		}
	}

	// Skipped once a tunnel is named: the same binary has just failed, and a
	// quick tunnel would hand back a random URL no client is pointed at.
	if !named {
		if url := runCloudflared(); url != "" {
			return url
		}
	}

	if url := startPinggy(); url != "" {
		return url
	}

	fmt.Printf("  warning: %s\n", locales.T("tunnel.cloudflared_timeout"))
	return ""
}

// startPinggy creates a tunnel via pinggy.io using SSH.
//
// The free service issues a new hostname every session, so this is a fallback
// that keeps the server reachable, not a way to keep one URL.
func (s *Server) startPinggy() string {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "" // ssh not available
	}

	fmt.Println()
	fmt.Printf("  %s\n", locales.T("tunnel.starting_pinggy"))
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	target := fmt.Sprintf("R0:localhost:%d", s.cfg.Port)
	cmd := exec.CommandContext(ctx, sshPath,
		"-p", "443",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-R", target,
		"a.pinggy.io",
	)

	stdout, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		fmt.Printf("  warning: %s (pinggy): %v\n", locales.T("error.cmd_failed"), err)
		cancel()
		return ""
	}

	// Pinggy prints URL to stdout
	urlRegex := regexp.MustCompile(`https://[a-zA-Z0-9]+\.(a\.)?pinggy\.(link|io|xyz)`)
	done := make(chan string, 1)
	output := newTunnelLog("pinggy")

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)
			if match := urlRegex.FindString(line); match != "" {
				done <- match
				return
			}
		}
	}()

	// Also check stderr
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)
			if match := urlRegex.FindString(line); match != "" {
				select {
				case done <- match:
				default:
				}
				return
			}
		}
	}()

	select {
	case url := <-done:
		s.tunnelStop = cancel
		printTunnelURL(url)
		return url
	case <-time.After(15 * time.Second):
		cancel()
		output.report()
		return ""
	}
}

// startCloudflared creates a tunnel via cloudflared.
//
// A configured tunnel name means the account owns a hostname for this server
// already, so the named tunnel replaces the quick one rather than joining it.
func (s *Server) startCloudflared() string {
	if name := s.cfg.Tunnel.Cloudflared; name != "" {
		return s.startNamedCloudflared(name)
	}

	tunnelExe := findCloudflaredExecutable()
	if tunnelExe == "" {
		return ""
	}

	fmt.Println()
	fmt.Printf("  %s\n", locales.T("tunnel.starting_cloudflared"))
	fmt.Printf("    %s\n", tunnelExe)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, tunnelExe, "tunnel", "--url", fmt.Sprintf("http://%s:%d", s.cfg.Host, s.cfg.Port))

	stdout, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		fmt.Printf("  warning: %s (cloudflared): %v\n", locales.T("error.cmd_failed"), err)
		cancel()
		return ""
	}

	urlRegex := regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)
	done := make(chan string, 1)
	output := newTunnelLog("cloudflared")

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if match := urlRegex.FindString(scanner.Text()); match != "" {
				done <- match
				return
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			output.add(line)
			if match := urlRegex.FindString(line); match != "" {
				select {
				case done <- match:
				default:
				}
				return
			}
		}
	}()

	select {
	case url := <-done:
		s.tunnelStop = cancel
		printTunnelURL(url)
		return url
	case <-time.After(cloudflaredURLTimeout):
		cancel()
		output.report()
		return ""
	}
}

// findCloudflaredExecutable reports where the connector is, or "" when there is
// none on this machine. The search itself lives in internal/tunnel, beside the
// code that can download a copy, so both agree on what counts as installed.
func findCloudflaredExecutable() string {
	return tunnel.FindCloudflared()
}

// tunnelBannerWidth is the minimum inner width of the tunnel banner, wide
// enough that a hostname and its path fit without stretching the frame.
const tunnelBannerWidth = 54

func printTunnelURL(url string) {
	mcpURL := url + "/mcp"
	sseURL := url + "/sse"
	lines := []string{
		locales.T("tunnel.active"),
		"",
		locales.T("tunnel.paste_chatgpt"),
		"  MCP  " + mcpURL,
		"  SSE  " + sseURL,
		"",
		locales.T("tunnel.try_sse"),
	}
	width := tunnelBannerWidth
	for _, line := range lines {
		if lineWidth := utf8.RuneCountInString(line); lineWidth > width {
			width = lineWidth
		}
	}

	border := "+" + strings.Repeat("-", width+4) + "+"
	fmt.Println()
	fmt.Println(border)
	for _, line := range lines {
		padding := strings.Repeat(" ", width-utf8.RuneCountInString(line))
		fmt.Printf("|  %s%s  |\n", line, padding)
	}
	fmt.Println(border)
	fmt.Println()
}

// createMcpServer creates a new MCP server with all tools registered.
func (s *Server) createMcpServer() *mcp.Server {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "mcp-webcoder", Version: version.String()},
		&mcp.ServerOptions{
			Instructions: s.serverInstructions(),
		},
	)

	s.registerTools(mcpServer)
	return mcpServer
}

// registerTools registers all MCP WebCoder tools on the MCP server.
func (s *Server) registerTools(server *mcp.Server) {
	names := s.toolNames()

	// open_workspace
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "open_workspace",
			Description: "Open a local project directory as a coding workspace. If path is empty or 'default', opens the first configured allowed root. Call this once per project folder or worktree before reading, editing, searching, writing, or running commands. Reuse the returned workspaceId for later calls in the same folder; if it ever stops working, pass 'default' or 'latest' as the workspaceId to reach the most recent workspace instead of reopening. Returns any AGENTS instruction files found in the project and the repository's git state, including how many paths are uncommitted and a sample of them, so pre-existing work is visible before you edit over it. If a remote client blocks local absolute paths, call open_default_workspace instead.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input OpenWorkspaceInput) (*mcp.CallToolResult, OpenWorkspaceOutput, error) {
			mode := workspace.ModeCheckout
			if input.Mode == "worktree" {
				mode = workspace.ModeWorktree
			}

			wsCtx, err := s.registry.OpenWorkspace(input.Path, mode, input.BaseRef)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(fmt.Errorf("failed to open workspace: %v", err))
				return result, OpenWorkspaceOutput{}, nil
			}

			var agentsFiles []AgentsFileOutput
			for _, f := range wsCtx.AgentsFiles {
				agentsFiles = append(agentsFiles, AgentsFileOutput{
					Path:    workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
					Content: f.Content,
				})
			}
			var availableAgentsFiles []AvailableAgentsFileOutput
			for _, f := range wsCtx.AvailableAgentsFiles {
				availableAgentsFiles = append(availableAgentsFiles, AvailableAgentsFileOutput{
					Path: workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
				})
			}

			instruction := "Use this workspaceId in all subsequent tool calls for this project. Do not call open_workspace again for this same folder unless this workspaceId stops working, the user asks to reopen, or you switch to a different folder/worktree."

			resultText := fmt.Sprintf("Opened workspace %s\nRoot: %s\nMode: %s\n%s\n%s",
				wsCtx.Workspace.ID, wsCtx.Workspace.Root, wsCtx.Workspace.Mode,
				describeGitForOpen(wsCtx.Git), instruction)

			return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: resultText}},
				}, OpenWorkspaceOutput{
					WorkspaceID:          wsCtx.Workspace.ID,
					Root:                 wsCtx.Workspace.Root,
					Mode:                 string(wsCtx.Workspace.Mode),
					AgentsFiles:          agentsFiles,
					AvailableAgentsFiles: availableAgentsFiles,
					Git:                  wsCtx.Git,
					Instruction:          instruction,
				}, nil
		},
	)

	// open_default_workspace avoids passing local absolute paths through clients
	// that may block filesystem-looking arguments before they reach MCP WebCoder.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "open_default_workspace",
			Description: "Open the default configured workspace without sending a local path. Use this when open_workspace with an absolute Windows/macOS/Linux path is blocked by the MCP client. Returns a workspaceId for the first allowed root, together with its AGENTS instruction files and git state, including how many paths the repository already has uncommitted.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input OpenDefaultWorkspaceInput) (*mcp.CallToolResult, OpenWorkspaceOutput, error) {
			wsCtx, err := s.registry.OpenDefaultWorkspace()
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(fmt.Errorf("failed to open default workspace: %v", err))
				return result, OpenWorkspaceOutput{}, nil
			}

			var agentsFiles []AgentsFileOutput
			for _, f := range wsCtx.AgentsFiles {
				agentsFiles = append(agentsFiles, AgentsFileOutput{
					Path:    workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
					Content: f.Content,
				})
			}
			var availableAgentsFiles []AvailableAgentsFileOutput
			for _, f := range wsCtx.AvailableAgentsFiles {
				availableAgentsFiles = append(availableAgentsFiles, AvailableAgentsFileOutput{
					Path: workspace.FormatPath(f.Path, wsCtx.Workspace.Root),
				})
			}

			instruction := "Use this workspaceId in all subsequent tool calls for this project. You may also pass workspaceId 'default' or 'latest' if the exact ID is stale after reconnecting."
			resultText := fmt.Sprintf("Opened default workspace %s\nRoot: %s\nMode: %s\n%s\n%s",
				wsCtx.Workspace.ID, wsCtx.Workspace.Root, wsCtx.Workspace.Mode,
				describeGitForOpen(wsCtx.Git), instruction)

			return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: resultText}},
				}, OpenWorkspaceOutput{
					WorkspaceID:          wsCtx.Workspace.ID,
					Root:                 wsCtx.Workspace.Root,
					Mode:                 string(wsCtx.Workspace.Mode),
					AgentsFiles:          agentsFiles,
					AvailableAgentsFiles: availableAgentsFiles,
					Git:                  wsCtx.Git,
					Instruction:          instruction,
				}, nil
		},
	)

	// read
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Read,
			Description: "Read one or many files inside an open workspace. Use this for file inspection instead of shell commands like cat. Pass path for a single file, or paths for up to 20 files in one call, which turns a reconnaissance pass into one round trip. Every file reports totalLines, startLine, endLine, isTruncated and nextLine, so a partial read is never mistaken for a whole file: while isTruncated is true, call again with offset set to nextLine. An offset past the end of the file reports the real line count instead of looking like an empty file. Each file also carries sha256, modifiedAt and lineEnding; pass that sha256 to edit or write as expectedSha256 to be told if anything else changed the file in between. Set metadataOnly to get size, line count, line endings and sha256 without spending context on the contents. The text result ends with a bracketed summary line that describes the read and is not part of the file, so when copying text into an edit take it from files[].content. Call open_workspace first and pass workspaceId.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.ReadInput) (*mcp.CallToolResult, tools.ReadOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.ReadOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.ReadOutput{}, nil
			}

			res, out, err := tools.ReadFile(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// write
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Write,
			Description: fmt.Sprintf("Create or completely overwrite a file inside an open workspace. Prefer %s for targeted changes to existing files, because this replaces the entire file. Optionally pass expectedSha256, taken from an earlier read, write or edit of the same file, to make the call fail loudly instead of silently overwriting work that arrived after you last looked. Returns created, bytes, sha256, modifiedAt and lineEnding, and that sha256 is the value to pass as expectedSha256 next time. Call open_workspace first and pass workspaceId.", names.Edit),
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.WriteInput) (*mcp.CallToolResult, tools.WriteOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.WriteOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.WriteOutput{}, nil
			}

			res, out, err := tools.WriteFile(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// mkdir
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Mkdir,
			Description: "Create a directory inside an open workspace, including missing parent directories. Use this instead of shell mkdir/New-Item. Call open_workspace first and pass workspaceId.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(false),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.MkdirInput) (*mcp.CallToolResult, tools.MkdirOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MkdirOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MkdirOutput{}, nil
			}

			res, out, err := tools.MakeDirectory(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// move
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Move,
			Description: "Move or rename a file/directory inside an open workspace. Creates missing parent directories for the destination. Use this instead of shell Move-Item/mv. Call open_workspace first and pass workspaceId.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.MoveInput) (*mcp.CallToolResult, tools.MoveOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MoveOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.SourcePath)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MoveOutput{}, nil
			}
			_, err = s.registry.ResolvePath(ws, input.TargetPath)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.MoveOutput{}, nil
			}

			res, out, err := tools.MovePath(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// remove
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Remove,
			Description: fmt.Sprintf("Delete a file or directory inside an open workspace. Pass recursive for a directory that is not empty, and dryRun to see what would go before it goes. Use this instead of shell rm/Remove-Item so deletes stay inside the workspace root; the root itself and .git directories are refused. Prefer %s when the intent is to relocate something rather than lose it. Call open_workspace first and pass workspaceId.", names.Move),
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
				OpenWorldHint:   boolPtr(false),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.RemoveInput) (*mcp.CallToolResult, tools.RemoveOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.RemoveOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.RemoveOutput{}, nil
			}

			res, out, err := tools.RemovePath(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// edit
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Edit,
			Description: fmt.Sprintf("Edit one file inside an open workspace by replacing exact text blocks. Each edit must match uniquely unless you set replaceAll or expectedOccurrences. Every edit is applied or none is: when one fails, the file is left byte for byte unchanged, and the error says so explicitly, reports the status of every edit in the batch as matched, failed or not attempted, and shows the closest text in the file to the block that did not match. Edits reported as matched are safe to re-issue unchanged. Set dryRun to report where the edits would land without writing. Optionally pass expectedSha256 from an earlier read, write or edit so that a file changed by someone else is reported as exactly that, rather than as oldText not being found. Prefer this over %s for targeted changes. Call open_workspace first and pass workspaceId.", names.Write),
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: boolPtr(true),
				IdempotentHint:  false,
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.EditInput) (*mcp.CallToolResult, tools.EditOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.EditOutput{}, nil
			}

			_, err = s.registry.ResolvePath(ws, input.Path)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.EditOutput{}, nil
			}

			res, out, err := tools.EditFile(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// Full mode tools: grep, glob, ls
	if s.cfg.ToolMode == config.ToolModeFull {
		// grep
		mcp.AddTool(server,
			&mcp.Tool{
				Name:        names.Grep,
				Description: "Search file contents inside an open workspace. Use this before broad reads when looking for symbols, text, or usage sites. Zero matches is reported together with filesSearched, filesSkipped and the search root that was actually walked, so an honest absence can be told apart from a search that never opened a file: filesSearched of zero means the path, the include glob or the skip list excluded everything and the pattern was never really tested. Version control, dependency and build directories are always skipped. The pattern is a Go regular expression and is case sensitive unless caseInsensitive is set. Call open_workspace first and pass workspaceId.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			func(ctx context.Context, req *mcp.CallToolRequest, input tools.GrepInput) (*mcp.CallToolResult, tools.GrepOutput, error) {
				ws, err := s.registry.GetWorkspace(input.WorkspaceID)
				if err != nil {
					result := &mcp.CallToolResult{}
					result.SetError(err)
					return result, tools.GrepOutput{}, nil
				}
				res, out, err := tools.GrepFiles(ctx, req, input, ws.Root)
				return prefixNotice(res, ws), out, err
			},
		)

		// glob
		mcp.AddTool(server,
			&mcp.Tool{
				Name:        names.Glob,
				Description: "Find files by glob pattern inside an open workspace. Use this to discover filenames or narrow file sets before reading. Call open_workspace first and pass workspaceId.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			func(ctx context.Context, req *mcp.CallToolRequest, input tools.GlobInput) (*mcp.CallToolResult, tools.GlobOutput, error) {
				ws, err := s.registry.GetWorkspace(input.WorkspaceID)
				if err != nil {
					result := &mcp.CallToolResult{}
					result.SetError(err)
					return result, tools.GlobOutput{}, nil
				}
				res, out, err := tools.FindFiles(ctx, req, input, ws.Root)
				return prefixNotice(res, ws), out, err
			},
		)

		// ls
		mcp.AddTool(server,
			&mcp.Tool{
				Name:        names.Ls,
				Description: "List a directory inside an open workspace. Use this for directory inspection before reading files, and in place of shelling out to ls -l: every entry is returned with name, path, type, size in bytes and an RFC 3339 modifiedAt, which is enough for a provenance or conflict check. Set details to put full timestamps in the text listing as well. Call open_workspace first and pass workspaceId.",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			func(ctx context.Context, req *mcp.CallToolRequest, input tools.LsInput) (*mcp.CallToolResult, tools.LsOutput, error) {
				ws, err := s.registry.GetWorkspace(input.WorkspaceID)
				if err != nil {
					result := &mcp.CallToolResult{}
					result.SetError(err)
					return result, tools.LsOutput{}, nil
				}
				res, out, err := tools.ListDirectory(ctx, req, input, ws.Root)
				return prefixNotice(res, ws), out, err
			},
		)
	}

	// bash (the shell that was configured, or the best one detected)
	shellHint := tools.ShellHint()
	bashDesc := fmt.Sprintf(
		"Run a shell command inside an open workspace. %s A foreground command times out after 30 seconds by default; asking for more than 120 starts a background job instead, because a request that long is abandoned by the client before it can answer. On timeout the whole process tree is terminated. Set background to true for servers, watchers, long builds, and long test runs: it returns a jobId at once, job_status streams the output and can wait for the end, and job_kill stops it. Use only for tests, builds, git inspection, and commands that are better executed by the shell. Do not use %s to create, move, rename, modify, or delete files. Prefer %s for file inspection, %s for creating directories, %s for moves/renames, %s for deletes, and %s/%s for file changes. Call open_workspace first and pass workspaceId.",
		shellHint, names.Bash, names.Read, names.Mkdir, names.Move, names.Remove, names.Edit, names.Write,
	)
	if s.cfg.ToolMode == config.ToolModeMinimal {
		bashDesc = fmt.Sprintf(
			"Run a shell command inside an open workspace. %s A foreground command times out after 30 seconds by default; asking for more than 120 starts a background job instead, and job_status then streams its output. On timeout the whole process tree is terminated. In minimal tool mode, %s, %s, and %s are disabled; use shell commands for search and directory inspection. Do not use %s to create, modify, or delete files. Prefer %s for direct file reads and %s for deletes. Call open_workspace first and pass workspaceId.",
			shellHint, names.Grep, names.Glob, names.Ls, names.Bash, names.Read, names.Remove,
		)
	}

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        names.Bash,
			Description: bashDesc,
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.BashInput) (*mcp.CallToolResult, tools.BashOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.BashOutput{}, nil
			}
			res, out, err := tools.RunBash(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	// job_status, job_kill and job_list are the read and stop side of a
	// background command. They take a jobId rather than a workspaceId, so a
	// reconnecting client can still reach a job it started earlier.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "job_status",
			Description: fmt.Sprintf("Report a background job started by %s and stream its output. Pass the nextLine from the previous answer as sinceLine to read only what is new, and set wait to block up to 25 seconds for the job to finish instead of sleeping in a shell command. This tool is addressed by jobId, not by workspaceId; a workspaceId is accepted and ignored, so reusing the arguments you passed to the shell is harmless. Each answer includes nextCall, the exact follow-up call to make, already filled in.", names.Bash),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		tools.JobStatus,
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "job_kill",
			Description: "Stop a background job and every process it started. Stopping a job that already finished is reported, not an error. Addressed by jobId, not by workspaceId; a workspaceId is accepted and ignored.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: boolPtr(true),
				IdempotentHint:  true,
				OpenWorldHint:   boolPtr(false),
			},
		},
		tools.JobKill,
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "job_list",
			Description: "List the background jobs this server is tracking, newest first, with their state and how much output each has produced. Takes no required arguments; a workspaceId is accepted and ignored, because jobs belong to the server process rather than to one workspace.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		tools.JobList,
	)

	// list_roots
	// recent_changes answers "what have I actually written?" from the server's
	// own journal, which outlives the caller's memory of its own session.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "recent_changes",
			Description: "List the files this server process changed through its own write, edit, move, remove and mkdir tools, newest first, with timestamps, and cross-check them against the paths git reports as uncommitted. Call this when you have lost track of your own edits, before concluding that another process is writing files concurrently, or to confirm that an edit really landed. unexplainedDirtyPaths is the important field: it lists uncommitted paths this server never wrote, and it is the only evidence that something else touched the repository. The journal is held in memory, is capped at the last 1000 changes, and starts empty when the server restarts, so an empty journal means this process has written nothing rather than that the tree is clean.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input tools.RecentChangesInput) (*mcp.CallToolResult, tools.RecentChangesOutput, error) {
			ws, err := s.registry.GetWorkspace(input.WorkspaceID)
			if err != nil {
				result := &mcp.CallToolResult{}
				result.SetError(err)
				return result, tools.RecentChangesOutput{}, nil
			}

			res, out, err := tools.RecentChanges(ctx, req, input, ws.Root)
			return prefixNotice(res, ws), out, err
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_roots",
			Description: "List the project roots this server allows, with the git branch of each. Call this when you do not already know which folder to open, instead of guessing paths or hunting for the project with directory listings.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, input ListRootsInput) (*mcp.CallToolResult, ListRootsOutput, error) {
			roots := workspace.DescribeRoots(s.registry.AllowedRoots(), s.registry.DefaultRoot())

			lines := make([]string, 0, len(roots)+1)
			for _, root := range roots {
				var details []string
				if root.IsDefault {
					details = append(details, "default")
				}
				if !root.Exists {
					details = append(details, "missing")
				}
				switch {
				case root.IsGitRepo && root.Branch != "":
					details = append(details, "git "+root.Branch)
				case root.IsGitRepo:
					details = append(details, "git")
				}

				line := root.Path
				if len(details) > 0 {
					line += "  (" + strings.Join(details, ", ") + ")"
				}
				lines = append(lines, line)
			}
			lines = append(lines, "Open one of these with open_workspace, or call open_default_workspace for the default root.")

			text := strings.Join(lines, "\n")
			return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: text}},
				}, ListRootsOutput{
					Roots:  roots,
					Result: text,
				}, nil
		},
	)
}

// ToolNames holds the tool naming configuration.
type ToolNames struct {
	Read   string
	Write  string
	Mkdir  string
	Move   string
	Remove string
	Edit   string
	Grep   string
	Glob   string
	Ls     string
	Bash   string
}

func (s *Server) toolNames() ToolNames {
	if s.cfg.ToolNaming == config.NamingLegacy {
		return ToolNames{
			Read:   "read_file",
			Write:  "write_file",
			Mkdir:  "create_directory",
			Move:   "move_path",
			Remove: "remove_path",
			Edit:   "edit_file",
			Grep:   "grep_files",
			Glob:   "find_files",
			Ls:     "list_directory",
			Bash:   "run_shell",
		}
	}
	return ToolNames{
		Read:   "read",
		Write:  "write",
		Mkdir:  "mkdir",
		Move:   "move",
		Remove: "remove",
		Edit:   "edit",
		Grep:   "grep",
		Glob:   "glob",
		Ls:     "ls",
		Bash:   "bash",
	}
}

func (s *Server) serverInstructions() string {
	names := s.toolNames()

	inspection := fmt.Sprintf("Prefer %s, %s, %s, and %s for file inspection. ",
		names.Read, names.Grep, names.Glob, names.Ls)
	if s.cfg.ToolMode == config.ToolModeMinimal {
		inspection = fmt.Sprintf("In minimal tool mode, %s, %s, and %s are disabled; use %s with command-line tools such as grep, rg, find, ls, and tree for search and directory inspection. ",
			names.Grep, names.Glob, names.Ls, names.Bash)
	}

	agentsMd := "Follow instructions returned by open_workspace. Before working under a path listed in availableAgentsFiles, use read to inspect that instruction file and follow it. "

	return fmt.Sprintf(
		"Use MCP WebCoder as a local coding workspace. Call list_roots to see which project folders this server allows, then open_workspace once per folder or worktree to obtain a workspaceId; if local absolute paths are blocked by the client, call open_default_workspace instead. Reuse that same workspaceId for all later file, search, edit, write, mkdir, move, remove, and shell tools in that folder. If the workspaceId becomes stale after reconnecting, pass workspaceId 'default' or 'latest' to use the most recent/default workspace. %s%sPrefer %s for targeted modifications, %s only for new files or complete rewrites, %s for directory creation, %s for moves/renames, %s for deletes, and %s for tests, builds, git inspection, package scripts, and commands that are better executed by the shell. Do not create, move, rename, modify, or delete files with %s. Anything that may outlive a single call, such as a CI watch, a long build, or a dev server, belongs in a background job: call %s with background set to true, read it with job_status, and stop it with job_kill. %s",
		agentsMd,
		inspection,
		names.Edit, names.Write, names.Mkdir, names.Move, names.Remove, names.Bash, names.Bash, names.Bash, tools.ShellHint(),
	)
}

// prefixNotice puts a workspace recovery notice in front of a successful tool
// result, so a caller whose workspaceId went stale learns that the root moved
// before it reads any paths in the output.
func prefixNotice(result *mcp.CallToolResult, ws *workspace.Workspace) *mcp.CallToolResult {
	if ws == nil || ws.Notice == "" || result == nil || result.IsError {
		return result
	}

	result.Content = append([]mcp.Content{&mcp.TextContent{Text: ws.Notice}}, result.Content...)
	return result
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		stopWatchdog := warnWhenSlow(r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
		stopWatchdog()

		path := r.URL.Path
		if !s.cfg.Logging.Requests {
			return
		}
		if !s.cfg.Logging.Assets && strings.HasPrefix(path, "/mcp-app-assets") {
			return
		}

		// Discovery and CORS preflight probes are most of the traffic and say
		// nothing about what the client is doing, so they stay at debug even
		// when request logging is on.
		event := log.Info
		if isDiscoveryRequest(r.Method, path) {
			event = log.Debug
		}

		event().
			Str("method", r.Method).
			Str("path", path).
			Str("remote_addr", r.RemoteAddr).
			Dur("duration_ms", time.Since(start)).
			Msg("http_request")
	})
}

// slowRequestAfter is how long a request may run before the server says so.
var slowRequestAfter = 10 * time.Second

// warnWhenSlow logs once if a request is still running after slowRequestAfter,
// and returns a function that cancels the warning when the request finishes. A
// request that hangs never reaches the completion log, so without this a
// stalled call leaves no trace at all.
func warnWhenSlow(method, path string) func() {
	timer := time.AfterFunc(slowRequestAfter, func() {
		log.Warn().
			Str("method", method).
			Str("path", path).
			Dur("running_ms", slowRequestAfter).
			Msg("http_request_slow")
	})
	return func() { timer.Stop() }
}

// isDiscoveryRequest reports whether a request is a client probe rather than
// real MCP traffic: metadata endpoints, OAuth discovery, and CORS preflights.
func isDiscoveryRequest(method, path string) bool {
	if method == http.MethodOptions {
		return true
	}
	for _, prefix := range []string{
		"/.well-known/",
		"/oauth/",
		"/authorize",
		"/token",
		"/favicon.ico",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// describeGitForOpen renders the repository state for the text summary of an
// opened workspace. Uncommitted work is worth seeing before the first edit
// rather than after a confusing conflict.
func describeGitForOpen(state workspace.GitState) string {
	report := "Git: " + state.Summary
	if state.DirtyCount > 0 && len(state.DirtyPaths) > 0 {
		report += "\nUncommitted:"
		for _, path := range state.DirtyPaths {
			report += "\n  " + path
		}
		if state.DirtyPathsTruncated {
			report += fmt.Sprintf("\n  ...and %d more", state.DirtyCount-len(state.DirtyPaths))
		}
		report += "\nThat work was already there before this call. Ask recent_changes which of it this server process wrote."
	}
	return report
}

// --- types ---

type OpenWorkspaceInput struct {
	Path    string `json:"path" jsonschema:"Absolute path of the project folder to open. Pass an empty string or 'default' for the first configured root; call list_roots to see which roots this server allows."`
	Mode    string `json:"mode,omitempty" jsonschema:"How to open the folder: 'checkout' (the default) works directly in it. 'worktree' is not implemented yet and is rejected."`
	BaseRef string `json:"baseRef,omitempty" jsonschema:"Git ref a worktree would be created from. Ignored in checkout mode."`
}

type OpenDefaultWorkspaceInput struct{}

type ListRootsInput struct{}

type ListRootsOutput struct {
	Roots  []workspace.RootInfo `json:"roots" jsonschema:"Configured roots, in the order this server accepts them."`
	Result string               `json:"result" jsonschema:"Human readable list of the configured roots."`
}

type OpenWorkspaceOutput struct {
	WorkspaceID          string                      `json:"workspaceId"`
	Root                 string                      `json:"root"`
	Mode                 string                      `json:"mode"`
	AgentsFiles          []AgentsFileOutput          `json:"agentsFiles"`
	AvailableAgentsFiles []AvailableAgentsFileOutput `json:"availableAgentsFiles"`
	Git                  workspace.GitState          `json:"git" jsonschema:"Repository state at the moment the workspace was opened. A non-zero dirtyCount means uncommitted work was already there before you touched anything; recent_changes says whether this server process wrote it."`
	Instruction          string                      `json:"instruction"`
}

type AgentsFileOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type AvailableAgentsFileOutput struct {
	Path string `json:"path"`
}
