package tools

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// newSearchFixture builds a small tree that mirrors the shape of a real Go
// repository, including a package directory named "tools".
func newSearchFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := []string{
		"README.md",
		"main.go",
		"cmd/devspace/main.go",
		"internal/server/server.go",
		"internal/tools/tools.go",
		"internal/tools/glob.go",
		"docs/guide.md",
		"assets/logo.png",
		"node_modules/left-pad/index.js",
	}

	for _, rel := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("// FindFiles fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func globPaths(t *testing.T, root, pattern, scope string) []string {
	t.Helper()

	_, out, err := FindFiles(context.Background(), nil, GlobInput{
		Pattern: pattern,
		Path:    scope,
	}, root)
	if err != nil {
		t.Fatalf("FindFiles(%q) returned an error: %v", pattern, err)
	}
	if strings.HasPrefix(out.Result, "No files found matching") {
		return nil
	}

	paths := strings.Split(strings.TrimSpace(out.Result), "\n")
	sort.Strings(paths)
	return paths
}

func assertGlobContains(t *testing.T, pattern string, got []string, want ...string) {
	t.Helper()

	for _, expected := range want {
		found := false
		for _, candidate := range got {
			if candidate == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("glob %q did not return %q; got %v", pattern, expected, got)
		}
	}
}

func assertGlobExcludes(t *testing.T, pattern string, got []string, unwanted ...string) {
	t.Helper()

	for _, excluded := range unwanted {
		for _, candidate := range got {
			if candidate == excluded {
				t.Fatalf("glob %q unexpectedly returned %q; got %v", pattern, excluded, got)
			}
		}
	}
}

// TestGlobDoublestarMatchesEveryFile reproduces the reported defect: every
// pattern containing a separator, including "**/*", matched nothing because the
// pattern was compared against the base name only.
func TestGlobDoublestarMatchesEveryFile(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "**/*", "")
	if len(got) == 0 {
		t.Fatal(`glob "**/*" returned no files`)
	}

	assertGlobContains(t, "**/*", got,
		"README.md",
		"cmd/devspace/main.go",
		"internal/server/server.go",
		"assets/logo.png",
	)
}

func TestGlobMatchesPathPatterns(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "internal/**/*.go", "")
	assertGlobContains(t, "internal/**/*.go", got,
		"internal/server/server.go",
		"internal/tools/tools.go",
	)
	assertGlobExcludes(t, "internal/**/*.go", got, "main.go", "cmd/devspace/main.go")

	got = globPaths(t, root, "**/*.md", "")
	assertGlobContains(t, "**/*.md", got, "README.md", "docs/guide.md")
}

// TestGlobBareNamePatternMatchesAtEveryDepth locks in the one behaviour that
// did work before, so the fix stays backwards compatible.
func TestGlobBareNamePatternMatchesAtEveryDepth(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "*.go", "")
	assertGlobContains(t, "*.go", got,
		"main.go",
		"cmd/devspace/main.go",
		"internal/tools/tools.go",
	)
}

// TestGlobSearchesDirectoriesNamedTools covers a second defect found while
// fixing the matcher: "tools" was in the skip list, so a Go project's own
// internal/tools package was invisible to both glob and grep.
func TestGlobSearchesDirectoriesNamedTools(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "**/tools.go", "")
	assertGlobContains(t, "**/tools.go", got, "internal/tools/tools.go")
}

func TestGrepSearchesDirectoriesNamedTools(t *testing.T) {
	root := newSearchFixture(t)

	_, out, err := GrepFiles(context.Background(), nil, GrepInput{
		Pattern: "FindFiles fixture",
	}, root)
	if err != nil {
		t.Fatalf("GrepFiles returned an error: %v", err)
	}
	if !strings.Contains(out.Result, "internal/tools/tools.go") {
		t.Fatalf("grep did not search internal/tools; got:\n%s", out.Result)
	}
}

// TestGlobReturnsAssetFiles covers files that glob used to drop because it
// applied grep's binary and size filters to a filename-only search.
func TestGlobReturnsAssetFiles(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "**/*.png", "")
	assertGlobContains(t, "**/*.png", got, "assets/logo.png")
}

func TestGlobSkipsDependencyDirectories(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "**/*.js", "")
	if len(got) != 0 {
		t.Fatalf("glob searched a dependency directory; got %v", got)
	}
}

func TestGlobScopeLimitsResultsToSubtree(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "**/*.go", "internal")
	assertGlobContains(t, "**/*.go", got,
		"internal/server/server.go",
		"internal/tools/tools.go",
	)
	assertGlobExcludes(t, "**/*.go", got, "main.go", "cmd/devspace/main.go")
}

func TestGlobBraceAlternation(t *testing.T) {
	root := newSearchFixture(t)

	got := globPaths(t, root, "**/*.{md,png}", "")
	assertGlobContains(t, "**/*.{md,png}", got,
		"README.md",
		"docs/guide.md",
		"assets/logo.png",
	)
	assertGlobExcludes(t, "**/*.{md,png}", got, "main.go")
}

// TestGlobAcceptsWindowsSeparators accepts the pattern spelling a Windows
// client is likely to send.
func TestGlobAcceptsWindowsSeparators(t *testing.T) {
	root := newSearchFixture(t)

	pattern := `internal\**\*.go`
	got := globPaths(t, root, pattern, "")
	assertGlobContains(t, pattern, got, "internal/server/server.go")
}

func TestGlobInvalidPatternReportsError(t *testing.T) {
	root := newSearchFixture(t)

	result, _, err := FindFiles(context.Background(), nil, GlobInput{Pattern: "["}, root)
	if err != nil {
		t.Fatalf("FindFiles returned a transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("an unterminated character class should be reported, not silently ignored")
	}
}

func TestMatchGlobSyntax(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*", "a.go", true},
		{"**/*", "a/b/c.go", true},
		{"**", "a/b/c.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "a/b/c.md", false},
		// A pattern with no separator falls back to a base-name match so that
		// "*.go" keeps finding files at every depth.
		{"*.go", "a/b/c.go", true},
		{"*.go", "a/b/c.md", false},
		// "**/" spans zero or more segments.
		{"internal/**/*.go", "internal/main.go", true},
		{"internal/**/*.go", "internal/tools/tools.go", true},
		{"internal/**/*.go", "cmd/main.go", false},
		// "*" never crosses a separator.
		{"docs/*.md", "docs/guide.md", true},
		{"docs/*.md", "docs/nested/guide.md", false},
		{"docs/**", "docs/nested/guide.md", true},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[ab].go", "b.go", true},
		{"[ab].go", "c.go", false},
		{"[!ab].go", "c.go", true},
		{"[!ab].go", "a.go", false},
		{"*.{md,go}", "notes.md", true},
		{"*.{md,go}", "notes.txt", false},
		{"./**/*.go", "a/b/c.go", true},
	}

	for _, test := range cases {
		if got := MatchGlob(test.pattern, test.path); got != test.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", test.pattern, test.path, got, test.want)
		}
	}
}

func TestMatchGlobFollowsFilesystemCaseSensitivity(t *testing.T) {
	got := MatchGlob("**/*.GO", "a/b/c.go")
	if got != caseInsensitiveFS {
		t.Fatalf("MatchGlob(%q, %q) = %v, want %v", "**/*.GO", "a/b/c.go", got, caseInsensitiveFS)
	}
}
