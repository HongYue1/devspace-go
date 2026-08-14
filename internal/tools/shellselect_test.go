package tools

import (
	"os"
	"strings"
	"testing"

	"github.com/snakex21/devspace-go/internal/shells"
)

// restoreShell puts the package back to its default preference. The selection
// is process-wide state, so every test that touches it has to hand it back.
func restoreShell(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetShell("auto") })
}

func TestShellArgsMatchesTheShellFamily(t *testing.T) {
	posix := shellArgs(shells.Shell{ID: "bash", Kind: shells.KindPosix}, "go test ./...")
	if strings.Join(posix, "|") != "-c|go test ./..." {
		t.Fatalf("posix args are %v", posix)
	}

	cmdArgs := shellArgs(shells.Shell{ID: "cmd", Kind: shells.KindCmd}, "dir")
	if strings.Join(cmdArgs, "|") != "/C|dir" {
		t.Fatalf("cmd args are %v", cmdArgs)
	}

	ps := shellArgs(shells.Shell{ID: "powershell", Kind: shells.KindPowerShell}, "git status")
	if len(ps) != 4 || ps[0] != "-NoProfile" || ps[2] != "-Command" {
		t.Fatalf("powershell args are %v", ps)
	}
	if !strings.Contains(ps[3], "LASTEXITCODE") {
		t.Fatalf("powershell command lost the exit code wrapper: %q", ps[3])
	}
}

func TestComputeSelectionResolvesAutomatically(t *testing.T) {
	sel := computeSelection("auto")
	if sel.err != nil {
		t.Fatalf("auto did not resolve: %v", sel.err)
	}
	if sel.shell.Path == "" {
		t.Fatal("auto resolved to a shell with no path")
	}
	if sel.fallback != "" {
		t.Fatalf("auto reported a fallback: %q", sel.fallback)
	}
}

// TestComputeSelectionFallsBackInsteadOfBreakingBash covers a typo in
// configuration: the bash tool keeps working and says what happened.
func TestComputeSelectionFallsBackInsteadOfBreakingBash(t *testing.T) {
	sel := computeSelection("definitely-not-a-shell")
	if sel.err != nil {
		t.Fatalf("a bad preference disabled the tool: %v", sel.err)
	}
	if sel.shell.Path == "" {
		t.Fatal("fallback produced no shell")
	}
	if !strings.Contains(sel.fallback, "unknown shell") {
		t.Fatalf("fallback does not explain itself: %q", sel.fallback)
	}
}

// TestComputeSelectionHonoursAnExplicitPath uses the test binary itself as the
// interpreter path, so the assertion holds on any machine.
func TestComputeSelectionHonoursAnExplicitPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot determine the test binary path: %v", err)
	}

	sel := computeSelection(exe)
	if sel.err != nil {
		t.Fatalf("an existing path was rejected: %v", sel.err)
	}
	if sel.shell.Path != exe {
		t.Fatalf("resolved path is %q, want %q", sel.shell.Path, exe)
	}
	if sel.fallback != "" {
		t.Fatalf("an explicit path reported a fallback: %q", sel.fallback)
	}
}

func TestSetShellUpdatesWhatBashReports(t *testing.T) {
	restoreShell(t)

	SetShell("auto")
	label, fallback, err := ShellStatus()
	if err != nil {
		t.Fatalf("ShellStatus: %v", err)
	}
	if label == "" {
		t.Fatal("ShellStatus returned no label")
	}
	if fallback != "" {
		t.Fatalf("auto reported a fallback: %q", fallback)
	}

	SetShell("definitely-not-a-shell")
	if _, fallback, err = ShellStatus(); err != nil {
		t.Fatalf("ShellStatus after a bad preference: %v", err)
	}
	if fallback == "" {
		t.Fatal("a bad preference was reported as fine")
	}
}

func TestShellHintNamesTheShellInUse(t *testing.T) {
	restoreShell(t)
	SetShell("auto")

	hint := ShellHint()
	sel := currentShell()
	if !strings.Contains(hint, sel.shell.ID) {
		t.Fatalf("hint %q does not name %q", hint, sel.shell.ID)
	}
	if !strings.Contains(hint, sel.shell.Path) {
		t.Fatalf("hint %q does not give the path", hint)
	}
}
