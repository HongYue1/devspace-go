package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// caseInsensitiveFS reports whether path comparison should ignore case.
// Windows and macOS are case insensitive by default. Linux is not, and folding
// case there would treat /srv/Docs and /srv/docs as the same directory.
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// IsPathInsideRoot reports whether targetPath is the root itself or sits
// inside it.
func IsPathInsideRoot(targetPath, root string) bool {
	cleanPath := filepath.Clean(targetPath)
	cleanRoot := filepath.Clean(root)

	if caseInsensitiveFS {
		cleanPath = strings.ToLower(cleanPath)
		cleanRoot = strings.ToLower(cleanRoot)
	}

	if cleanPath == cleanRoot {
		return true
	}

	if !strings.HasSuffix(cleanRoot, string(filepath.Separator)) {
		cleanRoot += string(filepath.Separator)
	}
	return strings.HasPrefix(cleanPath, cleanRoot)
}

// ResolvePath resolves a relative or absolute path against a working directory and validates
// it is inside one of the allowed roots.
func ResolvePath(inputPath, cwd string, allowedRoots []string) (string, error) {
	// If the path is absolute, use it directly
	var resolved string
	if filepath.IsAbs(inputPath) {
		resolved = filepath.Clean(inputPath)
	} else {
		// Handle ~/ paths
		if strings.HasPrefix(inputPath, "~/") {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("cannot resolve home directory: %w", err)
			}
			resolved = filepath.Clean(filepath.Join(homeDir, inputPath[2:]))
		} else {
			resolved = filepath.Clean(filepath.Join(cwd, inputPath))
		}
	}

	// Check if resolved path is inside any allowed root
	for _, root := range allowedRoots {
		cleanRoot := filepath.Clean(root)
		if IsPathInsideRoot(resolved, cleanRoot) {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("path %s is outside allowed roots", inputPath)
}

// AssertAllowedPath checks that the absolute path is inside one of the allowed roots.
func AssertAllowedPath(absPath string, allowedRoots []string) (string, error) {
	cleanPath := filepath.Clean(absPath)
	for _, root := range allowedRoots {
		cleanRoot := filepath.Clean(root)
		if IsPathInsideRoot(cleanPath, cleanRoot) {
			return cleanPath, nil
		}
	}
	return "", fmt.Errorf("path %s is outside allowed roots", absPath)
}

// pathKey returns a comparison key for a path. On a filesystem that ignores
// case, two spellings of one file must collapse to the same key, otherwise
// the same file is treated as two.
func pathKey(path string) string {
	clean := filepath.Clean(path)
	if caseInsensitiveFS {
		return strings.ToLower(clean)
	}
	return clean
}

// WalkWorkspace walks a directory tree, skipping blacklisted directories.
func WalkWorkspace(root string, visitor func(path string, info os.FileInfo) error) error {
	skipDirs := map[string]bool{
		".git":            true,
		".hg":             true,
		".svn":            true,
		".devspace":       true,
		".devspace-state": true,
		".webcoder":       true,
		".webcoder-state": true,
		"node_modules":    true,
		"dist":            true,
		"build":           true,
		".next":           true,
		".turbo":          true,
		".cache":          true,
	}

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		return visitor(path, info)
	})
}

// RootInfo describes one configured root. A caller that cannot see the server
// console has no other way to learn which projects are reachable, and guessing
// wastes calls on directories that are not allowed.
type RootInfo struct {
	Path         string `json:"path" jsonschema:"Absolute path of the configured root."`
	Exists       bool   `json:"exists" jsonschema:"Whether the path exists on disk right now."`
	IsGitRepo    bool   `json:"isGitRepo" jsonschema:"Whether the root is a git repository."`
	Branch       string `json:"branch,omitempty" jsonschema:"Checked out branch, or a short commit id when the repository has a detached head."`
	LastModified string `json:"lastModified,omitempty" jsonschema:"RFC 3339 timestamp of the root's own modification time."`
	IsDefault    bool   `json:"isDefault" jsonschema:"Whether this is the root that open_default_workspace opens."`
}

// DescribeRoots inspects each configured root.
func DescribeRoots(roots []string, defaultRoot string) []RootInfo {
	infos := make([]RootInfo, 0, len(roots))
	for _, root := range roots {
		infos = append(infos, describeRoot(root, defaultRoot))
	}
	return infos
}

// describeRoot reads the branch straight from .git/HEAD rather than running
// git, so listing roots stays cheap and works when git is missing.
func describeRoot(root, defaultRoot string) RootInfo {
	clean := filepath.Clean(root)
	info := RootInfo{
		Path:      clean,
		IsDefault: pathKey(clean) == pathKey(defaultRoot),
	}

	stat, err := os.Stat(clean)
	if err != nil {
		return info
	}
	info.Exists = true
	info.LastModified = stat.ModTime().UTC().Format(time.RFC3339)

	gitPath := filepath.Join(clean, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		return info
	}
	info.IsGitRepo = true

	// A worktree or submodule has a .git file pointing elsewhere, so a missing
	// HEAD here means the branch is unknown, not that this is not a repository.
	head, err := os.ReadFile(filepath.Join(gitPath, "HEAD"))
	if err != nil {
		return info
	}
	info.Branch = branchFromHead(string(head))
	return info
}

// branchFromHead extracts a branch name from the contents of .git/HEAD,
// falling back to a short commit id for a detached head.
func branchFromHead(head string) string {
	head = strings.TrimSpace(head)
	if rest, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(head, "ref: "); ok {
		return rest
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}
