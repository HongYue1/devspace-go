package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snakex21/devspace-go/internal/shells"
)

const (
	defaultReadLimitLines = 2000
	maxReadLimitLines     = 5000
	maxToolOutputBytes    = 256 * 1024
	maxSearchFileBytes    = 2 * 1024 * 1024
	maxSearchMatches      = 500
)

var skippedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "dist": true, "build": true,
	".next": true, ".turbo": true, ".cache": true,
	".devspace": true, ".devspace-state": true, ".webcoder": true, ".webcoder-state": true,
}

var configuredShell = "auto"

// SetShell records the shell preference for the bash tool. It accepts "auto",
// any id reported by the shells package, or an absolute path to an interpreter.
//
// The value is no longer lowercased: an absolute path on a case sensitive
// filesystem has to survive intact. Resolving here rather than on first use
// means a misconfigured shell is visible in startup output instead of
// surfacing as a confusing failure on some later command.
func SetShell(shell string) {
	configuredShell = strings.TrimSpace(shell)
	if configuredShell == "" {
		configuredShell = "auto"
	}
	shells.Reset()
	setSelection(computeSelection(configuredShell))
}

// ReadInput represents the input for the read tool.
type ReadInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"File path to read, relative to the workspace root."`
	Offset      int    `json:"offset,omitempty" jsonschema:"1-indexed line number to start reading from."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of lines to read."`
}

// ReadOutput represents the output for the read tool.
type ReadOutput struct {
	Result string `json:"result" jsonschema:"File contents."`
}

// ReadFile reads a file and returns its content.
func ReadFile(ctx context.Context, req *mcp.CallToolRequest, input ReadInput, wsRoot string) (*mcp.CallToolResult, ReadOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)

	info, err := os.Stat(absPath)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("file not found: %s", input.Path))
		return result, ReadOutput{}, nil
	}

	if info.IsDir() {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("path is a directory, not a file: %s", input.Path))
		return result, ReadOutput{}, nil
	}
	if info.Size() > maxSearchFileBytes && input.Limit == 0 {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("file is too large (%d bytes). Read with offset/limit or inspect with shell", info.Size()))
		return result, ReadOutput{}, nil
	}

	file, err := os.Open(absPath)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to open file: %v", err))
		return result, ReadOutput{}, nil
	}
	defer file.Close()

	var content string

	if input.Offset <= 0 {
		input.Offset = 1
	}
	if input.Limit <= 0 {
		input.Limit = defaultReadLimitLines
	}
	if input.Limit > maxReadLimitLines {
		input.Limit = maxReadLimitLines
	}

	if input.Offset > 0 || input.Limit > 0 {
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		linesRead := 0
		var lines []string
		truncated := false

		for scanner.Scan() {
			lineNum++
			if input.Offset > 0 && lineNum < input.Offset {
				continue
			}
			lines = append(lines, scanner.Text())
			linesRead++
			if input.Limit > 0 && linesRead >= input.Limit {
				truncated = true
				break
			}
		}

		content = strings.Join(lines, "\n")
		if truncated {
			content += fmt.Sprintf("\n\n[truncated after %d lines; use offset/limit to read more]", input.Limit)
		}
		if scanner.Err() != nil {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("failed to read file: %v", scanner.Err()))
			return result, ReadOutput{}, nil
		}
	} else {
		data, err := io.ReadAll(file)
		if err != nil {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("failed to read file: %v", err))
			return result, ReadOutput{}, nil
		}
		content = string(data)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: content},
		},
	}, ReadOutput{Result: content}, nil
}

// WriteInput represents the input for the write tool.
type WriteInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"File path to write, relative to the workspace root."`
	Content     string `json:"content" jsonschema:"Complete new file content."`
}

// WriteOutput represents the output for the write tool.
type WriteOutput struct {
	Result string `json:"result" jsonschema:"Write result message."`
}

// MkdirInput represents a directory creation operation.
type MkdirInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"Directory path to create, relative to the workspace root."`
}

// MkdirOutput represents the output for mkdir.
type MkdirOutput struct {
	Result string `json:"result" jsonschema:"Directory creation result message."`
}

// MoveInput represents a file or directory move/rename operation.
type MoveInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	SourcePath  string `json:"sourcePath" jsonschema:"Existing file or directory path, relative to the workspace root."`
	TargetPath  string `json:"targetPath" jsonschema:"Destination file or directory path, relative to the workspace root."`
	Overwrite   bool   `json:"overwrite,omitempty" jsonschema:"Overwrite destination if it already exists. Default false."`
}

// MoveOutput represents the output for move.
type MoveOutput struct {
	Result string `json:"result" jsonschema:"Move result message."`
}

// WriteFile creates or overwrites a file.
func WriteFile(ctx context.Context, req *mcp.CallToolRequest, input WriteInput, wsRoot string) (*mcp.CallToolResult, WriteOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to create directory: %v", err))
		return result, WriteOutput{}, nil
	}

	written, err := writeFileAtomic(absPath, []byte(input.Content), existingFileMode(absPath))
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, WriteOutput{}, nil
	}

	result := fmt.Sprintf("Wrote %s (%d bytes verified on disk).", input.Path, written)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, WriteOutput{Result: result}, nil
}

// MakeDirectory creates a directory and all missing parents inside a workspace.
func MakeDirectory(ctx context.Context, req *mcp.CallToolRequest, input MkdirInput, wsRoot string) (*mcp.CallToolResult, MkdirOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)

	if err := os.MkdirAll(absPath, 0755); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to create directory: %v", err))
		return result, MkdirOutput{}, nil
	}

	result := fmt.Sprintf("Created directory %s.", input.Path)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result}},
	}, MkdirOutput{Result: result}, nil
}

// MovePath moves or renames a file/directory inside a workspace.
func MovePath(ctx context.Context, req *mcp.CallToolRequest, input MoveInput, wsRoot string) (*mcp.CallToolResult, MoveOutput, error) {
	sourceAbs := filepath.Clean(filepath.Join(wsRoot, input.SourcePath))
	targetAbs := filepath.Clean(filepath.Join(wsRoot, input.TargetPath))

	if _, err := os.Stat(sourceAbs); err != nil {
		result := &mcp.CallToolResult{}
		if os.IsNotExist(err) {
			result.SetError(fmt.Errorf("source not found: %s", input.SourcePath))
		} else {
			result.SetError(fmt.Errorf("failed to stat source: %v", err))
		}
		return result, MoveOutput{}, nil
	}

	if _, err := os.Stat(targetAbs); err == nil {
		if !input.Overwrite {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("target already exists: %s", input.TargetPath))
			return result, MoveOutput{}, nil
		}
		if err := os.RemoveAll(targetAbs); err != nil {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("failed to remove existing target: %v", err))
			return result, MoveOutput{}, nil
		}
	} else if !os.IsNotExist(err) {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to stat target: %v", err))
		return result, MoveOutput{}, nil
	}

	if err := os.MkdirAll(filepath.Dir(targetAbs), 0755); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to create target parent directory: %v", err))
		return result, MoveOutput{}, nil
	}

	if err := os.Rename(sourceAbs, targetAbs); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to move path: %v", err))
		return result, MoveOutput{}, nil
	}

	result := fmt.Sprintf("Moved %s to %s.", input.SourcePath, input.TargetPath)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result}},
	}, MoveOutput{Result: result}, nil
}

// EditInput represents an edit operation.
type EditInput struct {
	WorkspaceID string      `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string      `json:"path" jsonschema:"File path to edit, relative to the workspace root."`
	Edits       []EditBlock `json:"edits" jsonschema:"Array of edit operations."`
	DryRun      bool        `json:"dryRun,omitempty" jsonschema:"Report where each edit would land without writing the file."`
}

// EditBlock represents a single find-and-replace operation.
type EditBlock struct {
	OldText             string `json:"oldText" jsonschema:"Text to replace. Must match uniquely unless replaceAll or expectedOccurrences is set. Line endings and trailing whitespace are ignored if the exact text is not found."`
	NewText             string `json:"newText" jsonschema:"Replacement text."`
	ReplaceAll          bool   `json:"replaceAll,omitempty" jsonschema:"Replace every occurrence instead of requiring a unique match."`
	ExpectedOccurrences int    `json:"expectedOccurrences,omitempty" jsonschema:"Replace exactly this many occurrences and fail if the file contains a different number."`
}

// EditOutput represents the output for the edit tool.
type EditOutput struct {
	Status string `json:"status" jsonschema:"Edit status."`
	Result string `json:"result" jsonschema:"Edit result message."`
}

// EditFile performs find-and-replace edits on a file.
func EditFile(ctx context.Context, req *mcp.CallToolRequest, input EditInput, wsRoot string) (*mcp.CallToolResult, EditOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)

	data, err := os.ReadFile(absPath)
	if err != nil {
		result := &mcp.CallToolResult{}
		if os.IsNotExist(err) {
			result.SetError(fmt.Errorf("file not found: %s", input.Path))
		} else {
			result.SetError(fmt.Errorf("failed to read file: %v", err))
		}
		return result, EditOutput{}, nil
	}

	if len(input.Edits) == 0 {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("edits must not be empty"))
		return result, EditOutput{}, nil
	}

	original := string(data)

	// A file that mixes CRLF and LF is matched byte for byte, so that lines the
	// caller did not touch keep the ending they already had.
	mixed := hasMixedLineEndings(original)
	ending := detectLineEnding(original)

	content := original
	if !mixed {
		content = normalizeNewlines(original)
	}
	before := content

	notes := make([]string, 0, len(input.Edits))
	for i, edit := range input.Edits {
		updated, note, editErr := applyEdit(content, edit, i+1, input.Path, original, !mixed)
		if editErr != nil {
			result := &mcp.CallToolResult{}
			result.SetError(editErr)
			return result, EditOutput{}, nil
		}
		content = updated
		notes = append(notes, note)
	}

	if content == before {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("no changes made to file"))
		return result, EditOutput{}, nil
	}

	output := content
	if !mixed {
		output = restoreLineEndings(content, ending)
	}
	detail := strings.Join(notes, "\n")

	if input.DryRun {
		message := fmt.Sprintf("Dry run for %s: %d edit(s) would apply, %d bytes.\n%s",
			input.Path, len(input.Edits), len(output), detail)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: message},
			},
		}, EditOutput{Status: "dry_run", Result: message}, nil
	}

	written, err := writeFileAtomic(absPath, []byte(output), existingFileMode(absPath))
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, EditOutput{}, nil
	}

	message := fmt.Sprintf("Edited %s: %d edit(s) applied, %d bytes verified on disk.\n%s",
		input.Path, len(input.Edits), written, detail)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: message},
		},
	}, EditOutput{Status: "applied", Result: message}, nil
}

// GrepInput represents the input for the grep tool.
type GrepInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Pattern     string `json:"pattern" jsonschema:"Search pattern."`
	Path        string `json:"path,omitempty" jsonschema:"Optional path scope relative to the workspace root."`
	Include     string `json:"include,omitempty" jsonschema:"Optional include glob."`
}

// GrepOutput represents the output for the grep tool.
type GrepOutput struct {
	Result string `json:"result" jsonschema:"Grep results."`
}

// GrepFiles searches file contents for a pattern.
func GrepFiles(ctx context.Context, req *mcp.CallToolRequest, input GrepInput, wsRoot string) (*mcp.CallToolResult, GrepOutput, error) {
	searchPath := wsRoot
	if input.Path != "" {
		searchPath = filepath.Join(wsRoot, input.Path)
	}

	re, err := regexp.Compile(input.Pattern)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("invalid regex pattern: %v", err))
		return result, GrepOutput{}, nil
	}

	include := input.Include
	var lines []string
	matches := 0
	skipped := 0
	start := time.Now()

	// WalkDir avoids one stat syscall per entry, which matters on large trees,
	// and shares the walk budget with glob instead of keeping its own copy.
	_ = filepath.WalkDir(searchPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > searchWalkTimeout || matches >= maxSearchMatches {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if skippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if include != "" {
			ok, _ := filepath.Match(include, entry.Name())
			if !ok {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxSearchFileBytes || looksBinaryPath(path) {
			skipped++
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			text := scanner.Text()
			if re.MatchString(text) {
				rel, _ := filepath.Rel(wsRoot, path)
				lines = append(lines, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(rel), lineNo, text))
				matches++
				if matches >= maxSearchMatches {
					break
				}
			}
		}
		return nil
	})

	output := strings.Join(lines, "\n")
	if output == "" {
		output = fmt.Sprintf("No matches found for pattern: %s", input.Pattern)
	} else if matches >= maxSearchMatches {
		output += fmt.Sprintf("\n\n[truncated after %d matches]", maxSearchMatches)
	}
	if skipped > 0 {
		output += fmt.Sprintf("\n[skipped %d large/binary files]", skipped)
	}
	output = truncateOutput(output)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: output},
		},
	}, GrepOutput{Result: output}, nil
}

// GlobInput represents the input for the glob tool.
type GlobInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Pattern     string `json:"pattern" jsonschema:"File glob pattern."`
	Path        string `json:"path,omitempty" jsonschema:"Optional path scope relative to the workspace root."`
}

// GlobOutput represents the output for the glob tool.
type GlobOutput struct {
	Result string `json:"result" jsonschema:"Glob results."`
}

// FindFiles finds files matching a glob pattern.
func FindFiles(ctx context.Context, req *mcp.CallToolRequest, input GlobInput, wsRoot string) (*mcp.CallToolResult, GlobOutput, error) {
	pattern := normalizeGlobPattern(input.Pattern)
	if pattern == "" {
		pattern = "**/*"
	}
	if _, err := compileGlob(pattern); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("invalid glob pattern %q: %v", input.Pattern, err))
		return result, GlobOutput{}, nil
	}

	searchPath := wsRoot
	if input.Path != "" {
		searchPath = filepath.Join(wsRoot, input.Path)
	}

	var matches []string
	truncated := false
	start := time.Now()

	// WalkDir avoids one stat syscall per entry, which matters on large trees.
	_ = filepath.WalkDir(searchPath, func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || time.Since(start) > searchWalkTimeout {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			// Never skip the directory the caller explicitly scoped to.
			if entryPath != searchPath && skippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// The pattern is matched against the path relative to the search
		// scope; results stay relative to the workspace root.
		rel, err := filepath.Rel(searchPath, entryPath)
		if err != nil || !MatchGlob(pattern, filepath.ToSlash(rel)) {
			return nil
		}

		reported, err := filepath.Rel(wsRoot, entryPath)
		if err != nil {
			reported = rel
		}
		matches = append(matches, filepath.ToSlash(reported))
		if len(matches) >= maxSearchMatches {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})

	result := strings.Join(matches, "\n")
	if result == "" {
		scope := "the workspace root"
		if input.Path != "" {
			scope = input.Path
		}
		result = fmt.Sprintf("No files found matching: %s (searched %s)", input.Pattern, scope)
	} else if truncated {
		result += fmt.Sprintf("\n\n[truncated after %d files]", maxSearchMatches)
	}
	result = truncateOutput(result)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, GlobOutput{Result: result}, nil
}

// LsInput represents the input for the ls tool.
type LsInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"Directory path to list, relative to the workspace root."`
}

// LsOutput represents the output for the ls tool.
type LsOutput struct {
	Result string `json:"result" jsonschema:"Directory listing."`
}

// ListDirectory lists the contents of a directory.
func ListDirectory(ctx context.Context, req *mcp.CallToolRequest, input LsInput, wsRoot string) (*mcp.CallToolResult, LsOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)

	entries, err := os.ReadDir(absPath)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to read directory: %v", err))
		return result, LsOutput{}, nil
	}

	var lines []string
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			lines = append(lines, entry.Name())
			continue
		}

		prefix := "-"
		if entry.IsDir() {
			prefix = "d"
		}

		size := info.Size()
		sizeStr := formatSize(size)
		modTime := info.ModTime().Format("Jan 02 15:04")
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}

		lines = append(lines, fmt.Sprintf("%s %8s %s %s", prefix, sizeStr, modTime, name))
	}

	result := strings.Join(lines, "\n")
	if result == "" {
		result = "Empty directory"
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, LsOutput{Result: result}, nil
}

// BashInput represents the input for the bash tool.
type BashInput struct {
	WorkspaceID      string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Command          string `json:"command" jsonschema:"Shell command to run."`
	WorkingDirectory string `json:"workingDirectory,omitempty" jsonschema:"Optional working directory relative to the workspace root."`
	Timeout          int    `json:"timeout,omitempty" jsonschema:"Timeout in seconds. Defaults to 30, max 300. On timeout the whole process tree is terminated and whatever the command printed first is returned."`
}

// BashOutput represents the output for the bash tool.
type BashOutput struct {
	Result string `json:"result" jsonschema:"Shell command output."`
}

// RunBash executes a shell command in the shell reported by currentShell:
// the configured one when it is installed, otherwise the best one detected on
// this machine.
func RunBash(ctx context.Context, req *mcp.CallToolRequest, input BashInput, wsRoot string) (*mcp.CallToolResult, BashOutput, error) {
	cwd := wsRoot
	if input.WorkingDirectory != "" {
		cwd = filepath.Join(wsRoot, input.WorkingDirectory)
	}

	timeout := normalizeTimeout(input.Timeout)

	sel := currentShell()
	if sel.err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(sel.err)
		return result, BashOutput{}, nil
	}

	res := runCommand(ctx, cwd, sel.shell.Path, shellArgs(sel.shell, input.Command), timeout)

	if res.timedOut {
		report := timeoutReport(res.output, timeout)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: report},
			},
			IsError: true,
		}, BashOutput{Result: report}, nil
	}

	if res.startFailed {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("command failed to start: %v", res.err))
		return result, BashOutput{}, nil
	}

	report := res.output
	if strings.TrimSpace(report) == "" {
		report = "(no output)"
	}
	switch {
	case res.exitCode > 0:
		report += fmt.Sprintf("\n[exit code: %d]", res.exitCode)
	case res.exitCode < 0 && res.err != nil:
		report += fmt.Sprintf("\n[terminated: %v]", res.err)
	}
	report = truncateOutput(report)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: report},
		},
	}, BashOutput{Result: report}, nil
}

// --- helpers ---

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGT"[exp])
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func truncateOutput(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	return s[:maxToolOutputBytes] + fmt.Sprintf("\n\n[output truncated after %d bytes]", maxToolOutputBytes)
}

func looksBinaryPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".obj", ".o", ".a",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf",
		".zip", ".tar", ".gz", ".7z", ".rar",
		".db", ".sqlite", ".sqlite3", ".wasm":
		return true
	default:
		return false
	}
}
