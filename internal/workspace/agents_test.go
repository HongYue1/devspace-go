package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

func newAgentsRegistry(t *testing.T, root string) *Registry {
	t.Helper()

	return NewRegistry(&config.Config{
		AllowedRoots: []string{root},
		AgentDir:     t.TempDir(),
	}, nil)
}

func writeAgentsFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func agentsFilePaths(files []AgentsFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func agentsEntryPaths(entries []AgentsFileEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

// TestLoadInitialAgentsFilesLoadsOneFileOnce covers a case insensitive
// filesystem answering to both AGENTS.md and AGENTS.MD, which read and listed
// the same file twice.
func TestLoadInitialAgentsFilesLoadsOneFileOnce(t *testing.T) {
	root := t.TempDir()
	writeAgentsFile(t, root, "AGENTS.md", "instructions")

	files := newAgentsRegistry(t, root).loadInitialAgentsFiles(root)

	if len(files) != 1 {
		t.Fatalf("expected one instruction file, got %d: %v", len(files), agentsFilePaths(files))
	}
	if files[0].Content != "instructions" {
		t.Fatalf("unexpected content: %q", files[0].Content)
	}
}

func TestLoadInitialAgentsFilesKeepsGenuinelyDifferentFiles(t *testing.T) {
	root := t.TempDir()
	writeAgentsFile(t, root, "AGENTS.md", "agents")
	writeAgentsFile(t, root, "CLAUDE.md", "claude")

	files := newAgentsRegistry(t, root).loadInitialAgentsFiles(root)

	if len(files) != 2 {
		t.Fatalf("expected both instruction files, got %d: %v", len(files), agentsFilePaths(files))
	}
}

func TestFindAvailableAgentsFilesSkipsAlreadyLoadedFiles(t *testing.T) {
	root := t.TempDir()
	loadedPath := writeAgentsFile(t, root, "AGENTS.md", "root")
	writeAgentsFile(t, filepath.Join(root, "docs"), "AGENTS.md", "docs")

	discovered := newAgentsRegistry(t, root).findAvailableAgentsFiles(root, []AgentsFile{
		{Path: loadedPath, Content: "root"},
	})

	if len(discovered) != 1 {
		t.Fatalf("expected only the file that was not loaded, got %d: %v", len(discovered), agentsEntryPaths(discovered))
	}
	if filepath.Base(filepath.Dir(discovered[0].Path)) != "docs" {
		t.Fatalf("expected the copy under docs, got %s", discovered[0].Path)
	}
}

// TestFindAvailableAgentsFilesRecognisesOtherSpellings covers an uppercase
// name being compared against a lowercase table, so it was never discovered.
func TestFindAvailableAgentsFilesRecognisesOtherSpellings(t *testing.T) {
	root := t.TempDir()
	writeAgentsFile(t, filepath.Join(root, "docs"), "AGENTS.MD", "shouty")

	discovered := newAgentsRegistry(t, root).findAvailableAgentsFiles(root, nil)

	if len(discovered) != 1 {
		t.Fatalf("expected AGENTS.MD to be discovered, got %d: %v", len(discovered), agentsEntryPaths(discovered))
	}
}

// TestOpenWorkspaceListsEachInstructionFileOnce is the reported symptom: one
// file on disk showing up as two instruction files.
func TestOpenWorkspaceListsEachInstructionFileOnce(t *testing.T) {
	root := t.TempDir()
	writeAgentsFile(t, root, "AGENTS.md", "instructions")

	ctx, err := newAgentsRegistry(t, root).OpenWorkspace(root, ModeCheckout, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(ctx.AgentsFiles) != 1 {
		t.Fatalf("expected one instruction file, got %d: %v", len(ctx.AgentsFiles), agentsFilePaths(ctx.AgentsFiles))
	}
	if len(ctx.AvailableAgentsFiles) != 0 {
		t.Fatalf("a loaded file should not be offered again, got %v", agentsEntryPaths(ctx.AvailableAgentsFiles))
	}
}
