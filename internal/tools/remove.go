package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RemoveInput represents the input for the remove tool.
type RemoveInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"File or directory to delete, relative to the workspace root or absolute inside it."`
	Recursive   bool   `json:"recursive,omitempty" jsonschema:"Delete a directory and everything inside it. Required for a directory that is not empty."`
	DryRun      bool   `json:"dryRun,omitempty" jsonschema:"Report what would be deleted without deleting anything."`
}

// RemoveOutput represents the output for the remove tool.
type RemoveOutput struct {
	Path        string `json:"path" jsonschema:"Path that was deleted, relative to the workspace root."`
	Status      string `json:"status" jsonschema:"removed when the delete happened, dry_run when it was only reported."`
	Files       int    `json:"files" jsonschema:"Number of files deleted."`
	Directories int    `json:"directories" jsonschema:"Number of directories deleted."`
	Bytes       int64  `json:"bytes" jsonschema:"Total size of the deleted files in bytes."`
	Result      string `json:"result" jsonschema:"Human readable result message."`
}

// removalTally counts what a delete would touch, so the caller learns the size
// of the change before or after it happens.
type removalTally struct {
	files       int
	directories int
	bytes       int64
	gitRepo     string
}

// pathInsideRoot reports whether target is root or sits inside it. This repeats
// the check the server already made, because a delete is the one operation
// where a path bug cannot be undone.
func pathInsideRoot(target, root string) bool {
	cleanTarget := filepath.Clean(target)
	cleanRoot := filepath.Clean(root)

	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		cleanTarget = strings.ToLower(cleanTarget)
		cleanRoot = strings.ToLower(cleanRoot)
	}

	if cleanTarget == cleanRoot {
		return true
	}
	if !strings.HasSuffix(cleanRoot, string(filepath.Separator)) {
		cleanRoot += string(filepath.Separator)
	}
	return strings.HasPrefix(cleanTarget, cleanRoot)
}

// samePath reports whether two paths name the same location.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// tallyRemoval measures a target without following symlinks. A link is counted
// as the single entry it is, never as the tree it points at.
func tallyRemoval(absPath string, info fs.FileInfo) removalTally {
	if !info.IsDir() {
		return removalTally{files: 1, bytes: info.Size()}
	}

	tally := removalTally{}
	filepath.WalkDir(absPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry still gets deleted; do not fail the count
		}
		if entry.IsDir() {
			tally.directories++
			if entry.Name() == ".git" && tally.gitRepo == "" {
				tally.gitRepo = path
			}
			return nil
		}
		tally.files++
		if fileInfo, statErr := entry.Info(); statErr == nil {
			tally.bytes += fileInfo.Size()
		}
		return nil
	})
	return tally
}

// countDirEntries reports how many entries a directory holds, so refusing a
// non-recursive delete can say why.
func countDirEntries(absPath string) int {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return 0
	}
	return len(entries)
}

// RemovePath deletes a file or directory inside the workspace.
//
// Without this, deleting anything meant reaching for the shell, which every
// other file operation is documented to avoid.
func RemovePath(ctx context.Context, req *mcp.CallToolRequest, input RemoveInput, wsRoot string) (*mcp.CallToolResult, RemoveOutput, error) {
	fail := func(err error) (*mcp.CallToolResult, RemoveOutput, error) {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, RemoveOutput{}, nil
	}

	requested := strings.TrimSpace(input.Path)
	if requested == "" {
		return fail(fmt.Errorf("path must not be empty"))
	}

	absPath := requested
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(wsRoot, absPath)
	}
	absPath = filepath.Clean(absPath)

	if !pathInsideRoot(absPath, wsRoot) {
		return fail(fmt.Errorf("path %s is outside the workspace root %s", requested, wsRoot))
	}
	if samePath(absPath, wsRoot) {
		return fail(fmt.Errorf("refusing to delete the workspace root %s", wsRoot))
	}
	if filepath.Base(absPath) == ".git" {
		return fail(fmt.Errorf("refusing to delete %s; deleting git metadata destroys the repository's history", requested))
	}

	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fail(fmt.Errorf("path does not exist: %s", requested))
		}
		return fail(fmt.Errorf("cannot inspect %s: %v", requested, err))
	}

	isDir := info.IsDir()
	entries := 0
	if isDir {
		entries = countDirEntries(absPath)
		if entries > 0 && !input.Recursive {
			return fail(fmt.Errorf("%s is a directory holding %d entries; pass recursive to delete it and everything inside",
				requested, entries))
		}
	}

	tally := tallyRemoval(absPath, info)
	display := formatRemovePath(absPath, wsRoot)

	var summary strings.Builder
	verb := "Deleted"
	status := "removed"
	if input.DryRun {
		verb = "Would delete"
		status = "dry_run"
	}

	if isDir {
		fmt.Fprintf(&summary, "%s directory %s: %d file(s), %d directory(ies), %s",
			verb, display, tally.files, tally.directories, formatSize(tally.bytes))
	} else {
		fmt.Fprintf(&summary, "%s %s (%s)", verb, display, formatSize(tally.bytes))
	}
	if tally.gitRepo != "" {
		fmt.Fprintf(&summary, "\nnote: this includes the git repository at %s",
			formatRemovePath(filepath.Dir(tally.gitRepo), wsRoot))
	}

	if input.DryRun {
		summary.WriteString("\nNothing was deleted. Call again without dryRun to delete it.")
		text := summary.String()
		return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
			}, RemoveOutput{
				Path:        display,
				Status:      status,
				Files:       tally.files,
				Directories: tally.directories,
				Bytes:       tally.bytes,
				Result:      text,
			}, nil
	}

	if isDir && input.Recursive {
		err = os.RemoveAll(absPath)
	} else {
		err = os.Remove(absPath)
	}
	if err != nil {
		return fail(fmt.Errorf("cannot delete %s: %v", requested, err))
	}

	text := summary.String()
	return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, RemoveOutput{
			Path:        display,
			Status:      status,
			Files:       tally.files,
			Directories: tally.directories,
			Bytes:       tally.bytes,
			Result:      text,
		}, nil
}

// formatRemovePath renders a path relative to the workspace root when it sits
// inside it, so messages stay readable.
func formatRemovePath(absPath, wsRoot string) string {
	rel, err := filepath.Rel(wsRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}
