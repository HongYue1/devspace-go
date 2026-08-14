package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGrepFixture puts one file in a fresh root so a test can point at exact
// line numbers.
func writeGrepFixture(t *testing.T, name, content string) string {
	t.Helper()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func grepIn(t *testing.T, root string, input GrepInput) GrepOutput {
	t.Helper()

	_, out, err := GrepFiles(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("GrepFiles returned an error: %v", err)
	}
	return out
}

func TestGrepContextLinesSurroundTheMatch(t *testing.T) {
	root := writeGrepFixture(t, "code.go", "one\ntwo\nthree\nneedle\nfive\nsix\nseven\n")

	out := grepIn(t, root, GrepInput{Pattern: "needle", ContextLines: 2})

	want := []string{
		"code.go-2-two",
		"code.go-3-three",
		"code.go:4:needle",
		"code.go-5-five",
		"code.go-6-six",
	}
	for _, line := range want {
		if !strings.Contains(out.Result, line) {
			t.Errorf("result is missing %q:\n%s", line, out.Result)
		}
	}
	if strings.Contains(out.Result, "code.go-1-one") {
		t.Errorf("two lines of context must not reach line 1:\n%s", out.Result)
	}
	if out.Matches != 1 {
		t.Errorf("matches = %d, want 1; context lines are not matches", out.Matches)
	}
}

func TestGrepWithoutContextKeepsOneLinePerMatch(t *testing.T) {
	root := writeGrepFixture(t, "code.go", "needle\nfiller\nneedle\n")

	out := grepIn(t, root, GrepInput{Pattern: "needle"})

	if strings.Contains(out.Result, "--") {
		t.Errorf("a search without context must not print group separators:\n%s", out.Result)
	}
	if strings.Contains(out.Result, "filler") {
		t.Errorf("a search without context must not print neighbours:\n%s", out.Result)
	}
	if out.Matches != 2 {
		t.Errorf("matches = %d, want 2", out.Matches)
	}
}

func TestGrepCaseInsensitiveFindsEitherCase(t *testing.T) {
	root := writeGrepFixture(t, "code.go", "Needle\nNEEDLE\nhaystack\n")

	if sensitive := grepIn(t, root, GrepInput{Pattern: "needle"}); sensitive.Matches != 0 {
		t.Errorf("matches = %d, want 0; the default search stays case sensitive", sensitive.Matches)
	}
	if insensitive := grepIn(t, root, GrepInput{Pattern: "needle", CaseInsensitive: true}); insensitive.Matches != 2 {
		t.Errorf("matches = %d, want 2", insensitive.Matches)
	}
}

func TestGrepMaxMatchesStopsEarlyAndSaysSo(t *testing.T) {
	root := writeGrepFixture(t, "code.go", strings.Repeat("needle\n", 20))

	out := grepIn(t, root, GrepInput{Pattern: "needle", MaxMatches: 3})

	if out.Matches != 3 {
		t.Errorf("matches = %d, want 3", out.Matches)
	}
	if !strings.Contains(out.Result, "[truncated after 3 matches]") {
		t.Errorf("a capped search must say it stopped early:\n%s", out.Result)
	}
}
