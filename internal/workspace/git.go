package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout bounds every git invocation. Describing a repository is a
// convenience, so a slow or wedged git must never be able to hold an
// open_workspace call open.
const gitTimeout = 10 * time.Second

// maxDirtyPathsReported caps the sample of uncommitted paths carried in a
// workspace summary. The count is always exact; only the list is trimmed.
const maxDirtyPathsReported = 20

// GitState describes the version control state of a workspace root.
//
// An agent that opens a workspace and starts editing cannot otherwise tell
// uncommitted work it created from uncommitted work that was already there.
// Reporting the dirty set up front turns that guess into a fact.
type GitState struct {
	Available           bool     `json:"available" jsonschema:"Whether git could be run at all. False means git is missing or failed, not that the tree is clean."`
	IsRepo              bool     `json:"isRepo" jsonschema:"Whether the workspace root is inside a git repository."`
	Branch              string   `json:"branch,omitempty" jsonschema:"Checked out branch name. Empty when HEAD is detached or the repository has no commits yet."`
	Head                string   `json:"head,omitempty" jsonschema:"Short commit id of HEAD."`
	Detached            bool     `json:"detached,omitempty" jsonschema:"Whether HEAD is detached rather than on a branch."`
	DirtyCount          int      `json:"dirtyCount" jsonschema:"Exact number of uncommitted paths: staged, unstaged and untracked together."`
	DirtyPaths          []string `json:"dirtyPaths,omitempty" jsonschema:"Uncommitted paths relative to the workspace root, at most 20 of them."`
	DirtyPathsTruncated bool     `json:"dirtyPathsTruncated,omitempty" jsonschema:"Whether dirtyPaths is a sample rather than the whole set."`
	Staged              int      `json:"staged" jsonschema:"Number of paths with staged changes."`
	Unstaged            int      `json:"unstaged" jsonschema:"Number of tracked paths with unstaged changes."`
	Untracked           int      `json:"untracked" jsonschema:"Number of untracked paths."`
	LastCommit          string   `json:"lastCommit,omitempty" jsonschema:"Short id and subject of the most recent commit."`
	Summary             string   `json:"summary" jsonschema:"One line description of the repository state, safe to show to a person."`
}

// runGit runs one git command inside root and returns its standard output.
func runGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// gitInstalled reports whether there is a git to run at all, so a missing git
// can be described differently from a directory that is not a repository.
func gitInstalled() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// dirtyEntry is one line of git status output, already resolved to a path
// relative to the workspace root.
type dirtyEntry struct {
	Path      string
	Staged    bool
	Unstaged  bool
	Untracked bool
}

// DirtyPaths reports every uncommitted path under root, relative to root.
//
// The second return value reports whether git could be consulted. A false
// there means "unknown", which is not the same as an empty list meaning
// "clean", and callers must not conflate the two.
func DirtyPaths(root string) ([]string, bool) {
	entries, ok := dirtyEntries(filepath.Clean(root))
	if !ok {
		return nil, false
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths, true
}

// dirtyEntries parses git status for root.
//
// Porcelain paths are always relative to the repository root, which is not
// necessarily the workspace root, so each one is trimmed by the workspace's own
// repository relative prefix. Both halves of that comparison come from git on
// purpose: comparing a git path against an operating system path fails when the
// two spell the same directory differently, which they do for Windows 8.3 short
// paths and for symlinked temporary directories. The symptom of getting this
// wrong is reporting a dirty tree as clean, which is the one answer here that is
// worse than no answer at all.
func dirtyEntries(root string) ([]dirtyEntry, bool) {
	prefix, ok := repositoryPrefix(root)
	if !ok {
		return nil, false
	}

	// core.quotepath=false keeps non-ASCII names readable instead of escaped,
	// and the "-- ." pathspec lets git scope the answer to the workspace subtree.
	out, err := runGit(root, "-c", "core.quotepath=false", "status", "--porcelain=v1", "-uall", "--", ".")
	if err != nil {
		return nil, false
	}

	var entries []dirtyEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}

		status := line[:2]
		path := strings.TrimSpace(line[3:])

		// A rename reads "old -> new". The new name is the one on disk.
		if index := strings.LastIndex(path, " -> "); index >= 0 {
			path = path[index+4:]
		}
		path = strings.Trim(path, "\"")
		if path == "" {
			continue
		}

		relative, inside := trimRepoPrefix(prefix, path)
		if !inside {
			continue
		}

		entries = append(entries, dirtyEntry{
			Path:      relative,
			Staged:    status[0] != ' ' && status[0] != '?',
			Unstaged:  status[1] != ' ' && status[1] != '?',
			Untracked: status == "??",
		})
	}
	return entries, true
}

// repositoryRoot reports the top level of the repository containing root.
func repositoryRoot(root string) (string, bool) {
	out, err := runGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", false
	}
	return filepath.Clean(top), true
}

// repositoryPrefix reports where root sits inside its repository, in git's own
// --show-prefix form: slash separated, either empty at the top level or ending
// in a slash. An error means root is not in a repository at all.
func repositoryPrefix(root string) (string, bool) {
	out, err := runGit(root, "rev-parse", "--show-prefix")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// trimRepoPrefix turns a repository relative path into a workspace relative
// one, reporting false when the path sits outside the workspace subtree.
func trimRepoPrefix(prefix, repoRelative string) (string, bool) {
	path := strings.TrimPrefix(strings.TrimSpace(repoRelative), "./")
	if prefix == "" {
		return path, path != ""
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	trimmed := strings.TrimPrefix(path, prefix)
	return trimmed, trimmed != ""
}

// DescribeGitState summarises the repository state of a workspace root.
func DescribeGitState(root string) GitState {
	root = filepath.Clean(root)
	state := GitState{Available: gitInstalled()}

	if !state.Available {
		state.Summary = "git is not installed, so uncommitted work could not be checked"
		return state
	}

	if _, ok := repositoryRoot(root); !ok {
		state.Summary = "not a git repository"
		return state
	}
	state.IsRepo = true

	if branch, err := runGit(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		name := strings.TrimSpace(branch)
		if name == "HEAD" {
			state.Detached = true
		} else {
			state.Branch = name
		}
	}
	if head, err := runGit(root, "rev-parse", "--short", "HEAD"); err == nil {
		state.Head = strings.TrimSpace(head)
	}
	if commit, err := runGit(root, "log", "-1", "--format=%h %s"); err == nil {
		state.LastCommit = strings.TrimSpace(commit)
	}

	entries, ok := dirtyEntries(root)
	if !ok {
		state.Summary = describeGitSummary(state, false)
		return state
	}

	state.DirtyCount = len(entries)
	for _, entry := range entries {
		switch {
		case entry.Untracked:
			state.Untracked++
		default:
			if entry.Staged {
				state.Staged++
			}
			if entry.Unstaged {
				state.Unstaged++
			}
		}
		if len(state.DirtyPaths) < maxDirtyPathsReported {
			state.DirtyPaths = append(state.DirtyPaths, entry.Path)
		}
	}
	state.DirtyPathsTruncated = state.DirtyCount > len(state.DirtyPaths)
	state.Summary = describeGitSummary(state, true)
	return state
}

// describeGitSummary renders the one line form of a repository state.
func describeGitSummary(state GitState, statusKnown bool) string {
	var parts []string

	switch {
	case state.Detached && state.Head != "":
		parts = append(parts, "detached at "+state.Head)
	case state.Branch != "":
		parts = append(parts, "on branch "+state.Branch)
	default:
		parts = append(parts, "no commits yet")
	}

	switch {
	case !statusKnown:
		parts = append(parts, "uncommitted work unknown (git status failed)")
	case state.DirtyCount == 0:
		parts = append(parts, "working tree clean")
	default:
		parts = append(parts, fmt.Sprintf("%d uncommitted file(s): %d staged, %d unstaged, %d untracked",
			state.DirtyCount, state.Staged, state.Unstaged, state.Untracked))
	}

	if state.LastCommit != "" {
		parts = append(parts, "last commit "+state.LastCommit)
	}
	return strings.Join(parts, ", ")
}
