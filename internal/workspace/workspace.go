package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/store"
)

// Mode represents workspace opening mode.
type Mode string

const (
	ModeCheckout Mode = "checkout"
	ModeWorktree Mode = "worktree"
)

// WorktreeInfo describes a git worktree workspace.
type WorktreeInfo struct {
	Path        string `json:"path"`
	BaseRef     string `json:"baseRef"`
	BaseSha     string `json:"baseSha"`
	DirtySource bool   `json:"dirtySource"`
	Detached    bool   `json:"detached"`
	Managed     bool   `json:"managed"`
}

// Workspace represents an open workspace session.
type Workspace struct {
	ID         string        `json:"id"`
	Root       string        `json:"root"`
	Mode       Mode          `json:"mode"`
	SourceRoot string        `json:"sourceRoot,omitempty"`
	Worktree   *WorktreeInfo `json:"worktree,omitempty"`

	// Notice carries a one-off message about automatic recovery. It is set on
	// the copy handed back to a caller and never on the cached workspace.
	Notice string `json:"notice,omitempty"`
}

// WorkspaceContext holds a workspace along with discovered agents files.
type WorkspaceContext struct {
	Workspace            *Workspace        `json:"workspace"`
	AgentsFiles          []AgentsFile      `json:"agentsFiles"`
	AvailableAgentsFiles []AgentsFileEntry `json:"availableAgentsFiles"`
}

// AgentsFile represents a loaded AGENTS.md/CLAUDE.md file.
type AgentsFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// AgentsFileEntry represents a discovered but unloaded agents file.
type AgentsFileEntry struct {
	Path string `json:"path"`
}

// Registry manages workspace sessions in-memory and persisted to SQLite.
type Registry struct {
	cfg        *config.Config
	store      *store.Store
	workspaces map[string]*Workspace
	mu         sync.RWMutex
}

// NewRegistry creates a new workspace registry.
func NewRegistry(cfg *config.Config, s *store.Store) *Registry {
	return &Registry{
		cfg:        cfg,
		store:      s,
		workspaces: make(map[string]*Workspace),
	}
}

// OpenWorkspace opens a local directory as a workspace.
func (r *Registry) OpenWorkspace(rootPath string, mode Mode, baseRef string) (*WorkspaceContext, error) {
	if mode == "" {
		mode = ModeCheckout
	}
	if strings.TrimSpace(rootPath) == "" || strings.EqualFold(strings.TrimSpace(rootPath), "default") {
		rootPath = r.defaultRoot()
	}

	if mode == ModeWorktree {
		return nil, fmt.Errorf("worktree mode not yet implemented in Go version")
	}

	return r.openCheckoutWorkspace(rootPath)
}

func (r *Registry) openCheckoutWorkspace(rootPath string) (*WorkspaceContext, error) {
	root, err := AssertAllowedPath(rootPath, r.cfg.AllowedRoots)
	if err != nil {
		return nil, fmt.Errorf("workspace root not allowed: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}

	// Verify it's a directory
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root must be a directory: %s", rootPath)
	}

	// Create workspace
	ws := &Workspace{
		ID:   "ws_" + uuid.New().String(),
		Root: root,
		Mode: ModeCheckout,
	}

	// Persist session
	if r.store != nil {
		r.store.CreateSession(&store.WorkspaceSession{
			ID:   ws.ID,
			Root: ws.Root,
			Mode: string(ws.Mode),
		})
	}

	// Cache in memory
	r.mu.Lock()
	r.workspaces[ws.ID] = ws
	r.mu.Unlock()

	// Load agents files
	agentsFiles := r.loadInitialAgentsFiles(root)
	availableAgentsFiles := r.findAvailableAgentsFiles(root, agentsFiles)

	return &WorkspaceContext{
		Workspace:            ws,
		AgentsFiles:          agentsFiles,
		AvailableAgentsFiles: availableAgentsFiles,
	}, nil
}

// OpenDefaultWorkspace opens the first configured allowed root. This avoids
// sending local absolute paths through remote MCP clients that may block them.
func (r *Registry) OpenDefaultWorkspace() (*WorkspaceContext, error) {
	return r.openCheckoutWorkspace(r.defaultRoot())
}

func (r *Registry) defaultRoot() string {
	if len(r.cfg.AllowedRoots) > 0 && strings.TrimSpace(r.cfg.AllowedRoots[0]) != "" {
		return r.cfg.AllowedRoots[0]
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// GetWorkspace retrieves a workspace by ID. A client that reconnects often
// sends an identifier this process no longer knows about, so instead of
// failing the call, an unusable identifier falls back to the most recent
// workspace and the returned copy carries a notice about the change.
func (r *Registry) GetWorkspace(id string) (*Workspace, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.EqualFold(id, "default") || strings.EqualFold(id, "latest") || strings.EqualFold(id, "last") {
		return r.GetDefaultWorkspace()
	}

	r.mu.RLock()
	ws, ok := r.workspaces[id]
	r.mu.RUnlock()

	if ok {
		if r.store != nil {
			r.store.TouchSession(id)
		}
		return ws, nil
	}

	// Try to restore from store
	if r.store != nil {
		session, err := r.store.GetSession(id)
		if err != nil {
			return r.recoverWorkspace(id, "this server has no record of it")
		}

		root, err := AssertAllowedPath(session.Root, r.cfg.AllowedRoots)
		if err != nil {
			return r.recoverWorkspace(id, fmt.Sprintf("its root %s is no longer an allowed root", session.Root))
		}

		ws = &Workspace{
			ID:   session.ID,
			Root: root,
			Mode: Mode(session.Mode),
		}

		r.mu.Lock()
		r.workspaces[ws.ID] = ws
		r.mu.Unlock()

		r.store.TouchSession(id)
		return ws, nil
	}

	return r.recoverWorkspace(id, "this server has no record of it")
}

// recoverWorkspace falls back to a usable workspace so a stale identifier does
// not fail the call. It never consults GetDefaultWorkspace, which would route
// back here for the same broken session.
func (r *Registry) recoverWorkspace(id, reason string) (*Workspace, error) {
	if ws := r.latestUsableWorkspace(id); ws != nil {
		return noticeOfRecovery(ws, id, reason), nil
	}

	wsCtx, err := r.OpenDefaultWorkspace()
	if err != nil {
		return nil, fmt.Errorf(
			"workspaceId %s is unusable because %s, and no replacement workspace could be opened: %w",
			id, reason, err)
	}
	return noticeOfRecovery(wsCtx.Workspace, id, reason), nil
}

// latestUsableWorkspace restores the most recent stored session whose root is
// still allowed, skipping the identifier that just failed.
func (r *Registry) latestUsableWorkspace(skipID string) *Workspace {
	if r.store == nil {
		return nil
	}

	session, err := r.store.GetLatestSession()
	if err != nil || session.ID == skipID {
		return nil
	}

	root, err := AssertAllowedPath(session.Root, r.cfg.AllowedRoots)
	if err != nil {
		return nil
	}

	ws := &Workspace{
		ID:   session.ID,
		Root: root,
		Mode: Mode(session.Mode),
	}

	r.mu.Lock()
	r.workspaces[ws.ID] = ws
	r.mu.Unlock()

	r.store.TouchSession(ws.ID)
	return ws
}

// noticeOfRecovery copies a workspace and attaches the explanation, so the
// cached entry stays clean and the message is delivered exactly once.
func noticeOfRecovery(ws *Workspace, id, reason string) *Workspace {
	recovered := *ws
	recovered.Notice = fmt.Sprintf(
		"Note: workspaceId %s could not be used because %s. Reconnected to %s (workspaceId %s); paths are relative to that root.",
		id, reason, recovered.Root, recovered.ID)
	return &recovered
}

// GetDefaultWorkspace retrieves the latest workspace, or opens the default root
// if no session exists yet.
func (r *Registry) GetDefaultWorkspace() (*Workspace, error) {
	if r.store != nil {
		if session, err := r.store.GetLatestSession(); err == nil {
			return r.GetWorkspace(session.ID)
		}
	}

	wsCtx, err := r.OpenDefaultWorkspace()
	if err != nil {
		return nil, err
	}
	return wsCtx.Workspace, nil
}

// ResolvePath resolves a relative path within a workspace to an absolute path.
func (r *Registry) ResolvePath(ws *Workspace, inputPath string) (string, error) {
	absPath, err := ResolvePath(inputPath, ws.Root, []string{ws.Root})
	if err != nil {
		return "", err
	}

	if !IsPathInsideRoot(absPath, ws.Root) {
		return "", fmt.Errorf("path is outside workspace root: %s", inputPath)
	}

	return absPath, nil
}

// ResolveWorkingDirectory resolves a working directory for shell commands.
func (r *Registry) ResolveWorkingDirectory(ws *Workspace, workingDir string) (string, error) {
	if workingDir == "" {
		return ws.Root, nil
	}

	absPath, err := r.ResolvePath(ws, workingDir)
	if err != nil {
		return ws.Root, nil // Default to workspace root
	}
	return absPath, nil
}

// loadInitialAgentsFiles loads AGENTS.md/CLAUDE.md files from the root and agent dir.
func (r *Registry) loadInitialAgentsFiles(root string) []AgentsFile {
	agentDir := filepath.Clean(r.cfg.AgentDir)
	contextNames := []string{"AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

	var files []AgentsFile

	for _, name := range contextNames {
		// Check workspace root
		path := filepath.Join(root, name)
		if data, err := os.ReadFile(path); err == nil {
			files = append(files, AgentsFile{
				Path:    path,
				Content: string(data),
			})
		}

		// Check agent directory
		if agentDir != "" && agentDir != root {
			path := filepath.Join(agentDir, name)
			if data, err := os.ReadFile(path); err == nil {
				files = append(files, AgentsFile{
					Path:    path,
					Content: string(data),
				})
			}
		}
	}

	return files
}

// findAvailableAgentsFiles discovers all AGENTS.md/CLAUDE.md files in the workspace.
func (r *Registry) findAvailableAgentsFiles(root string, loaded []AgentsFile) []AgentsFileEntry {
	loadedPaths := make(map[string]bool)
	for _, f := range loaded {
		loadedPaths[filepath.Clean(f.Path)] = true
	}

	contextNames := map[string]bool{
		"AGENTS.md": true,
		"CLAUDE.md": true,
	}

	var discovered []AgentsFileEntry
	WalkWorkspace(root, func(path string, info os.FileInfo) error {
		name := strings.ToUpper(info.Name())
		if contextNames[info.Name()] || contextNames[name] {
			cleanPath := filepath.Clean(path)
			if !loadedPaths[cleanPath] {
				discovered = append(discovered, AgentsFileEntry{Path: path})
			}
		}
		return nil
	})

	return discovered
}

// FormatPath formats a path relative to the workspace root.
func FormatPath(path string, workspaceRoot string) string {
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
