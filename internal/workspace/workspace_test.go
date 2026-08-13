package workspace

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

func newTestRegistry(roots ...string) *Registry {
	return NewRegistry(&config.Config{AllowedRoots: roots}, nil)
}

// TestGetWorkspaceRecoversFromAStaleID covers a client reconnecting with an
// identifier this process never issued. The call should keep working instead
// of telling the caller to reopen the workspace by hand.
func TestGetWorkspaceRecoversFromAStaleID(t *testing.T) {
	root := t.TempDir()
	registry := newTestRegistry(root)

	ws, err := registry.GetWorkspace("ws_00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("a stale workspaceId should recover, got: %v", err)
	}
	if ws.Root != filepath.Clean(root) {
		t.Fatalf("recovered root is %s, want %s", ws.Root, filepath.Clean(root))
	}
	if ws.Notice == "" {
		t.Fatal("recovery should tell the caller that the root changed")
	}
	if !strings.Contains(ws.Notice, ws.Root) {
		t.Fatalf("the notice should name the new root:\n%s", ws.Notice)
	}
}

func TestGetWorkspaceKeepsAKnownID(t *testing.T) {
	root := t.TempDir()
	registry := newTestRegistry(root)

	opened, err := registry.OpenWorkspace(root, ModeCheckout, "")
	if err != nil {
		t.Fatal(err)
	}

	ws, err := registry.GetWorkspace(opened.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ID != opened.Workspace.ID {
		t.Fatalf("got workspace %s, want %s", ws.ID, opened.Workspace.ID)
	}
	if ws.Notice != "" {
		t.Fatalf("a healthy workspace should not carry a notice: %s", ws.Notice)
	}
}

// TestRecoveryLeavesTheCachedWorkspaceClean checks that the one-off notice is
// not stored, so it cannot repeat on every later call.
func TestRecoveryLeavesTheCachedWorkspaceClean(t *testing.T) {
	root := t.TempDir()
	registry := newTestRegistry(root)

	recovered, err := registry.GetWorkspace("ws_stale")
	if err != nil {
		t.Fatal(err)
	}

	again, err := registry.GetWorkspace(recovered.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Notice != "" {
		t.Fatalf("the notice should be delivered once, got it again: %s", again.Notice)
	}
}

func TestIsPathInsideRootAcceptsTheRootItself(t *testing.T) {
	root := filepath.Join("srv", "app")

	if !IsPathInsideRoot(root, root) {
		t.Fatal("the root should count as inside itself")
	}
}

func TestIsPathInsideRootRejectsASiblingSharingAPrefix(t *testing.T) {
	root := filepath.Join("srv", "app")
	sibling := filepath.Join("srv", "application", "main.go")

	if IsPathInsideRoot(sibling, root) {
		t.Fatalf("%s is not inside %s", sibling, root)
	}
}

// TestIsPathInsideRootFollowsTheFilesystemCaseRules covers case folding that
// used to be applied everywhere, which made two distinct directories on a
// case sensitive filesystem look like one.
func TestIsPathInsideRootFollowsTheFilesystemCaseRules(t *testing.T) {
	root := filepath.Join("srv", "Docs")
	other := filepath.Join("srv", "docs", "file.txt")

	if got := IsPathInsideRoot(other, root); got != caseInsensitiveFS {
		t.Fatalf("IsPathInsideRoot(%q, %q) = %v on %s, want %v",
			other, root, got, runtime.GOOS, caseInsensitiveFS)
	}
}
