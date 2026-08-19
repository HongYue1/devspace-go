package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepZeroMatchesSaysHowManyFilesWereSearched turns "no matches" from a
// claim about the pattern into a statement about what was actually read.
func TestGrepZeroMatchesSaysHowManyFilesWereSearched(t *testing.T) {
	root := writeGrepFixture(t, "code.go", "alpha\nbeta\n")

	out := grepIn(t, root, GrepInput{Pattern: "zeta"})

	if out.Matches != 0 {
		t.Fatalf("matches = %d, want 0", out.Matches)
	}
	if out.FilesSearched != 1 {
		t.Errorf("filesSearched = %d, want 1", out.FilesSearched)
	}
	if out.FilesMatched != 0 {
		t.Errorf("filesMatched = %d, want 0", out.FilesMatched)
	}
	if !strings.Contains(out.Result, "searched 1 file(s)") {
		t.Errorf("miss report does not say what was searched:\n%s", out.Result)
	}
	if !strings.Contains(out.Result, "case sensitive") {
		t.Errorf("miss report drops the case sensitivity tip:\n%s", out.Result)
	}
}

// TestGrepSearchingNothingSaysItProvesNothing is the trap worth naming: an
// include glob that matches no file looks exactly like an absent pattern.
func TestGrepSearchingNothingSaysItProvesNothing(t *testing.T) {
	root := writeGrepFixture(t, "code.go", "alpha\n")

	out := grepIn(t, root, GrepInput{Pattern: "alpha", Include: "*.rs"})

	if out.FilesSearched != 0 {
		t.Fatalf("filesSearched = %d, want 0", out.FilesSearched)
	}
	if !strings.Contains(out.Result, "no file was searched") {
		t.Errorf("miss report does not warn that nothing was read:\n%s", out.Result)
	}
	if !strings.Contains(out.Result, "include glob") {
		t.Errorf("miss report does not blame the include glob:\n%s", out.Result)
	}
}

func TestGrepMissingSearchRootIsCalledOut(t *testing.T) {
	root := writeGrepFixture(t, "code.go", "alpha\n")

	out := grepIn(t, root, GrepInput{Pattern: "alpha", Path: "nowhere"})

	if out.FilesSearched != 0 {
		t.Errorf("filesSearched = %d, want 0", out.FilesSearched)
	}
	if !strings.Contains(out.Result, "does not exist") {
		t.Errorf("miss report does not say the search root is missing:\n%s", out.Result)
	}
}

// TestGrepHitsReportTheirScope keeps the same counts available when the search
// did find something, so a caller can judge coverage either way.
func TestGrepHitsReportTheirScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("needle\nbeta\nneedle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c.go"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := grepIn(t, root, GrepInput{Pattern: "needle"})

	if out.Matches != 3 {
		t.Errorf("matches = %d, want 3", out.Matches)
	}
	if out.FilesMatched != 2 {
		t.Errorf("filesMatched = %d, want 2", out.FilesMatched)
	}
	if out.FilesSearched != 3 {
		t.Errorf("filesSearched = %d, want 3", out.FilesSearched)
	}
	if out.SearchRoot == "" {
		t.Error("searchRoot is empty, so the scope of the answer is unstated")
	}
	if out.Truncated {
		t.Error("truncated = true for a search well under the cap")
	}
	if !strings.Contains(out.Result, "file(s) searched") {
		t.Errorf("hit report does not state its scope:\n%s", out.Result)
	}
}
