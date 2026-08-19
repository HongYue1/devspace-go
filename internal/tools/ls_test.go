package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lsFixture builds a root holding one file and one directory that contains a
// file, so counts and types can both be checked.
func lsFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "inner.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func lsIn(t *testing.T, root string, input LsInput) LsOutput {
	t.Helper()

	result, out, err := ListDirectory(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("ListDirectory returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("ls failed: %s", resultError(result))
	}
	return out
}

func lsEntryNamed(t *testing.T, out LsOutput, name string) LsEntry {
	t.Helper()

	for _, entry := range out.Entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("no entry named %q in %+v", name, out.Entries)
	return LsEntry{}
}

// TestLsAlwaysReturnsStructuredEntries is the reason details is only about the
// text: the machine readable half is never worth withholding, and shelling out
// to ls -l for an mtime should never be necessary.
func TestLsAlwaysReturnsStructuredEntries(t *testing.T) {
	root := lsFixture(t)

	out := lsIn(t, root, LsInput{Path: "."})

	if len(out.Entries) != 2 {
		t.Fatalf("entries has %d items, want 2: %+v", len(out.Entries), out.Entries)
	}
	if out.Files != 1 || out.Directories != 1 {
		t.Errorf("files = %d and directories = %d, want 1 of each", out.Files, out.Directories)
	}

	file := lsEntryNamed(t, out, "notes.txt")
	if file.Type != "file" {
		t.Errorf("type = %q, want file", file.Type)
	}
	if file.Size != 5 {
		t.Errorf("size = %d, want 5", file.Size)
	}
	if _, err := time.Parse(time.RFC3339, file.ModifiedAt); err != nil {
		t.Errorf("modifiedAt = %q, which is not RFC 3339: %v", file.ModifiedAt, err)
	}

	if dir := lsEntryNamed(t, out, "sub"); dir.Type != "dir" {
		t.Errorf("type = %q, want dir", dir.Type)
	}
}

// TestLsEntryPathIsReadyToRead saves a caller from rebuilding paths by hand.
func TestLsEntryPathIsReadyToRead(t *testing.T) {
	root := lsFixture(t)

	entry := lsEntryNamed(t, lsIn(t, root, LsInput{Path: "."}), "notes.txt")
	out := readIn(t, root, ReadInput{Path: entry.Path})

	if !strings.Contains(out.Files[0].Content, "hello") {
		t.Errorf("reading the path ls reported gave %q", out.Files[0].Content)
	}
}

func TestLsDetailsPutsFullTimestampsInTheText(t *testing.T) {
	root := lsFixture(t)

	out := lsIn(t, root, LsInput{Path: ".", Details: true})

	entry := lsEntryNamed(t, out, "notes.txt")
	if !strings.Contains(out.Result, entry.ModifiedAt) {
		t.Errorf("details listing does not show the full timestamp %s:\n%s", entry.ModifiedAt, out.Result)
	}
}

func TestLsWithoutDetailsKeepsTheTextShort(t *testing.T) {
	root := lsFixture(t)

	out := lsIn(t, root, LsInput{Path: "."})

	entry := lsEntryNamed(t, out, "notes.txt")
	if strings.Contains(out.Result, entry.ModifiedAt) {
		t.Errorf("plain listing spends context on full timestamps:\n%s", out.Result)
	}
}

func TestLsEmptyDirectorySaysSoAndReturnsNoEntries(t *testing.T) {
	out := lsIn(t, t.TempDir(), LsInput{Path: "."})

	if !strings.HasPrefix(out.Result, "Empty directory") {
		t.Errorf("result = %q, want it to start with Empty directory", out.Result)
	}
	if len(out.Entries) != 0 {
		t.Errorf("entries = %+v, want none", out.Entries)
	}
	if out.Files != 0 || out.Directories != 0 {
		t.Errorf("files = %d and directories = %d, want 0 of each", out.Files, out.Directories)
	}
}
