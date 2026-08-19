package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFixture creates a repository in a temporary directory, skipping the test
// when this machine has no git to run.
func gitFixture(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGitFixture(t, root, "init")
	return root
}

func runGitFixture(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %s failed on this machine: %v: %s", strings.Join(args, " "), err, output)
	}
}

func commitEverything(t *testing.T, root, message string) {
	t.Helper()

	runGitFixture(t, root, "add", ".")
	runGitFixture(t, root,
		"-c", "user.email=test@example.com",
		"-c", "user.name=Test",
		"commit", "-m", message,
	)
}

// TestDescribeGitStateOutsideARepositoryIsNotAnError keeps open_workspace usable
// in a plain directory, which is a normal way to use this server.
func TestDescribeGitStateOutsideARepositoryIsNotAnError(t *testing.T) {
	state := DescribeGitState(t.TempDir())

	if state.IsRepo {
		t.Error("isRepo = true for a directory that is not a repository")
	}
	if state.DirtyCount != 0 {
		t.Errorf("dirtyCount = %d, want 0", state.DirtyCount)
	}
	if state.Summary == "" {
		t.Error("summary is empty, so open_workspace would have nothing to report")
	}
}

// TestDescribeGitStateCountsUncommittedWork is the point of item 9: an agent
// must learn that uncommitted work exists before it starts editing.
func TestDescribeGitStateCountsUncommittedWork(t *testing.T) {
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := DescribeGitState(root)

	if !state.Available {
		t.Skip("git could not be run")
	}
	if !state.IsRepo {
		t.Fatalf("isRepo = false inside a repository: %+v", state)
	}
	if state.DirtyCount != 1 {
		t.Fatalf("dirtyCount = %d, want 1: %v", state.DirtyCount, state.DirtyPaths)
	}
	if state.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", state.Untracked)
	}
	if len(state.DirtyPaths) != 1 || !strings.Contains(state.DirtyPaths[0], "new.txt") {
		t.Errorf("dirtyPaths = %v, want one entry naming new.txt", state.DirtyPaths)
	}
	if state.DirtyPathsTruncated {
		t.Error("dirtyPathsTruncated = true for a single dirty file")
	}
	if !strings.Contains(state.Summary, "1") {
		t.Errorf("summary = %q, want it to mention the one uncommitted file", state.Summary)
	}
}

func TestDescribeGitStateReportsACleanRepository(t *testing.T) {
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitEverything(t, root, "initial")

	state := DescribeGitState(root)

	if !state.IsRepo {
		t.Fatalf("isRepo = false inside a repository: %+v", state)
	}
	if state.DirtyCount != 0 {
		t.Errorf("dirtyCount = %d, want 0: %v", state.DirtyCount, state.DirtyPaths)
	}
	if state.Head == "" {
		t.Error("head is empty after a commit")
	}
	if state.LastCommit == "" {
		t.Error("lastCommit is empty after a commit")
	}
	if state.Summary == "" {
		t.Error("summary is empty for a clean repository")
	}
}

// TestDescribeGitStateSeparatesStagedFromUnstaged keeps the counts meaningful
// enough to judge provenance.
func TestDescribeGitStateSeparatesStagedFromUnstaged(t *testing.T) {
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitEverything(t, root, "initial")

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "add", "b.txt")

	state := DescribeGitState(root)

	if state.DirtyCount != 2 {
		t.Fatalf("dirtyCount = %d, want 2: %v", state.DirtyCount, state.DirtyPaths)
	}
	if state.Staged != 1 {
		t.Errorf("staged = %d, want 1", state.Staged)
	}
	if state.Unstaged != 1 {
		t.Errorf("unstaged = %d, want 1", state.Unstaged)
	}
}

// TestDirtyPathsAgreesWithDescribeGitState keeps recent_changes and
// open_workspace from telling a caller two different stories.
func TestDirtyPathsAgreesWithDescribeGitState(t *testing.T) {
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitEverything(t, root, "initial")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, ok := DirtyPaths(root)
	state := DescribeGitState(root)

	if !ok {
		t.Skip("git status could not be read")
	}
	if len(paths) != state.DirtyCount {
		t.Errorf("DirtyPaths returned %d paths but DescribeGitState counted %d", len(paths), state.DirtyCount)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], "a.txt") {
		t.Errorf("paths = %v, want one entry naming a.txt", paths)
	}
}
