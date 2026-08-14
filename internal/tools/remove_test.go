package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRemovalFixture creates a file and any parent directories it needs.
func writeRemovalFixture(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// removeIn runs the remove tool and reports whether it refused, what it said,
// and what it counted.
func removeIn(t *testing.T, root string, input RemoveInput) (bool, string, RemoveOutput) {
	t.Helper()

	result, out, err := RemovePath(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("RemovePath returned a transport error: %v", err)
	}
	if result == nil {
		t.Fatal("expected the remove tool to return a result")
	}
	return result.IsError, resultError(result), out
}

func TestRemoveDeletesAFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "notes.txt")
	writeRemovalFixture(t, target, "12345")

	failed, message, out := removeIn(t, root, RemoveInput{Path: "notes.txt"})
	if failed {
		t.Fatalf("deleting a file failed: %s", message)
	}
	if out.Status != "removed" {
		t.Errorf("status = %q, want removed", out.Status)
	}
	if out.Files != 1 {
		t.Errorf("files = %d, want 1", out.Files)
	}
	if out.Bytes != 5 {
		t.Errorf("bytes = %d, want 5", out.Bytes)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("the file is still on disk")
	}
}

func TestRemoveRefusesANonEmptyDirectoryWithoutRecursive(t *testing.T) {
	root := t.TempDir()
	writeRemovalFixture(t, filepath.Join(root, "build", "out.bin"), "x")

	failed, message, _ := removeIn(t, root, RemoveInput{Path: "build"})
	if !failed {
		t.Fatal("a directory with contents must not be deleted without recursive")
	}
	if !strings.Contains(message, "recursive") {
		t.Errorf("the refusal must say how to proceed, got: %s", message)
	}
	if _, err := os.Stat(filepath.Join(root, "build", "out.bin")); err != nil {
		t.Errorf("the contents must survive a refused delete: %v", err)
	}
}

func TestRemoveDeletesATreeWithRecursive(t *testing.T) {
	root := t.TempDir()
	writeRemovalFixture(t, filepath.Join(root, "build", "a.bin"), "aa")
	writeRemovalFixture(t, filepath.Join(root, "build", "deep", "b.bin"), "bbb")

	failed, message, out := removeIn(t, root, RemoveInput{Path: "build", Recursive: true})
	if failed {
		t.Fatalf("recursive delete failed: %s", message)
	}
	if out.Files != 2 {
		t.Errorf("files = %d, want 2", out.Files)
	}
	if out.Directories != 2 {
		t.Errorf("directories = %d, want 2 for build and build/deep", out.Directories)
	}
	if out.Bytes != 5 {
		t.Errorf("bytes = %d, want 5", out.Bytes)
	}
	if _, err := os.Stat(filepath.Join(root, "build")); !os.IsNotExist(err) {
		t.Error("the directory is still on disk")
	}
}

func TestRemoveDryRunLeavesEverythingInPlace(t *testing.T) {
	root := t.TempDir()
	writeRemovalFixture(t, filepath.Join(root, "build", "a.bin"), "aa")

	failed, message, out := removeIn(t, root, RemoveInput{Path: "build", Recursive: true, DryRun: true})
	if failed {
		t.Fatalf("dry run failed: %s", message)
	}
	if out.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", out.Status)
	}
	if out.Files != 1 {
		t.Errorf("files = %d, want 1; a dry run still reports what would go", out.Files)
	}
	if _, err := os.Stat(filepath.Join(root, "build", "a.bin")); err != nil {
		t.Errorf("a dry run must not delete anything: %v", err)
	}
}

func TestRemoveDeletesAnEmptyDirectoryWithoutRecursive(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	failed, message, _ := removeIn(t, root, RemoveInput{Path: "empty"})
	if failed {
		t.Fatalf("deleting an empty directory failed: %s", message)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Error("the directory is still on disk")
	}
}

func TestRemoveRefusesTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()

	failed, message, _ := removeIn(t, root, RemoveInput{Path: ".", Recursive: true})
	if !failed {
		t.Fatal("the workspace root must never be deletable")
	}
	if !strings.Contains(message, "workspace root") {
		t.Errorf("the refusal must say what it protected, got: %s", message)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the root must survive: %v", err)
	}
}

func TestRemoveRefusesGitMetadata(t *testing.T) {
	root := t.TempDir()
	writeRemovalFixture(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")

	failed, message, _ := removeIn(t, root, RemoveInput{Path: ".git", Recursive: true})
	if !failed {
		t.Fatal("deleting .git must be refused; it destroys the repository history")
	}
	if !strings.Contains(message, "history") {
		t.Errorf("the refusal must say what is at stake, got: %s", message)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "HEAD")); err != nil {
		t.Errorf("git metadata must survive: %v", err)
	}
}

func TestRemoveRefusesAPathOutsideTheRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "secrets.txt")
	writeRemovalFixture(t, outside, "keep me")

	failed, message, _ := removeIn(t, root, RemoveInput{Path: filepath.Join("..", "secrets.txt")})
	if !failed {
		t.Fatal("a path outside the workspace root must be refused")
	}
	if !strings.Contains(message, "outside the workspace root") {
		t.Errorf("the refusal must say why, got: %s", message)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the file outside the root must survive: %v", err)
	}
}

func TestRemoveReportsAMissingPath(t *testing.T) {
	root := t.TempDir()

	failed, message, _ := removeIn(t, root, RemoveInput{Path: "never-existed.txt"})
	if !failed {
		t.Fatal("deleting something that is not there must be reported")
	}
	if !strings.Contains(message, "does not exist") {
		t.Errorf("the message must say the path is missing, got: %s", message)
	}
}
