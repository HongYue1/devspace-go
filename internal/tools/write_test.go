package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runWrite(t *testing.T, root string, input WriteInput) WriteOutput {
	t.Helper()

	result, out, err := WriteFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("WriteFile returned an error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("WriteFile failed: %s", resultError(result))
	}
	return out
}

func TestWriteReportsVerifiedByteCount(t *testing.T) {
	root := t.TempDir()
	content := "package main\n\nfunc main() {}\n"

	out := runWrite(t, root, WriteInput{Path: "main.go", Content: content})

	if !strings.Contains(out.Result, fmt.Sprintf("%d bytes", len(content))) {
		t.Fatalf("the result should report the byte count:\n%s", out.Result)
	}

	info, err := os.Stat(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(content)) {
		t.Fatalf("the file holds %d bytes, want %d", info.Size(), len(content))
	}
}

// TestWriteStoresLongContentWithoutTruncation covers the report of a long file
// arriving on disk cut off part way through a line.
func TestWriteStoresLongContentWithoutTruncation(t *testing.T) {
	root := t.TempDir()

	var builder strings.Builder
	for i := 0; builder.Len() < 2<<20; i++ {
		fmt.Fprintf(&builder, "line %06d: %s\n", i, strings.Repeat("x", 64))
	}
	content := builder.String()

	out := runWrite(t, root, WriteInput{Path: "long.txt", Content: content})

	stored := readFixture(t, filepath.Join(root, "long.txt"))
	if stored != content {
		t.Fatalf("stored %d bytes, want %d", len(stored), len(content))
	}
	if !strings.Contains(out.Result, fmt.Sprintf("%d bytes", len(content))) {
		t.Fatalf("the result should report the byte count:\n%s", out.Result)
	}
}

func TestWriteCountsBytesNotRunes(t *testing.T) {
	root := t.TempDir()
	content := "h\u00e9llo w\u00f6rld \u2705\n"

	out := runWrite(t, root, WriteInput{Path: "unicode.txt", Content: content})

	if !strings.Contains(out.Result, fmt.Sprintf("%d bytes", len(content))) {
		t.Fatalf("the byte count should count bytes, not runes:\n%s", out.Result)
	}
}

func TestWriteLeavesNoTemporaryFilesBehind(t *testing.T) {
	root := t.TempDir()

	runWrite(t, root, WriteInput{Path: "a.txt", Content: "one"})
	runWrite(t, root, WriteInput{Path: "a.txt", Content: "two"})

	if stored := readFixture(t, filepath.Join(root, "a.txt")); stored != "two" {
		t.Fatalf("the rewrite did not land: %q", stored)
	}
	assertOnlyEntries(t, root, "a.txt")
}

// TestWriteFailsLoudlyAndCleansUp covers a write that cannot be completed: it
// must report the failure rather than report success, and must not litter the
// directory with temporary files.
func TestWriteFailsLoudlyAndCleansUp(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "blocked"), 0755); err != nil {
		t.Fatal(err)
	}

	result, _, err := WriteFile(context.Background(), nil,
		WriteInput{Path: "blocked", Content: "data"}, root)
	if err != nil {
		t.Fatalf("WriteFile returned an error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("writing over a directory should fail loudly")
	}

	assertOnlyEntries(t, root, "blocked")
}

func TestWriteAtomicKeepsTheOldFileWhenTheRenameFails(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "kept")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := writeFileAtomic(target, []byte("data"), 0644); err == nil {
		t.Fatal("writeFileAtomic should refuse to replace a directory")
	}

	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatal("the original entry should survive a failed write")
	}
	assertOnlyEntries(t, root, "kept")
}

func assertOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("directory holds %v, want %v", names, want)
	}
}
