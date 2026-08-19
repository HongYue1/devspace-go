package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeIn runs the write tool and fails the test if it refused.
func writeIn(t *testing.T, root string, input WriteInput) WriteOutput {
	t.Helper()

	result, out, err := WriteFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("WriteFile returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("write failed: %s", resultError(result))
	}
	return out
}

// writeFailure runs a write that is expected to be refused and returns why.
func writeFailure(t *testing.T, root string, input WriteInput) string {
	t.Helper()

	result, _, err := WriteFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("WriteFile returned a transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("expected the write to be refused")
	}
	return resultError(result)
}

// foreignWrite changes a file behind the tools' back, standing in for the other
// process a precondition is meant to catch.
func foreignWrite(t *testing.T, root, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAcceptsTheShaItJustReturned(t *testing.T) {
	root := t.TempDir()

	first := writeIn(t, root, WriteInput{Path: "notes.txt", Content: "one\n"})
	if !first.Created {
		t.Error("created = false for a file that did not exist")
	}
	if first.Sha256 == "" {
		t.Fatal("write returned no sha256, so it cannot be chained")
	}

	second := writeIn(t, root, WriteInput{
		Path:           "notes.txt",
		Content:        "two\n",
		ExpectedSha256: first.Sha256,
	})
	if second.Created {
		t.Error("created = true when overwriting an existing file")
	}
	if second.Sha256 == first.Sha256 {
		t.Error("sha256 did not change after the content changed")
	}
}

// TestWriteRefusesAStaleSha is the failure the whole feature exists for.
func TestWriteRefusesAStaleSha(t *testing.T) {
	root := t.TempDir()
	first := writeIn(t, root, WriteInput{Path: "notes.txt", Content: "one\n"})

	foreignWrite(t, root, "notes.txt", "someone else was here\n")

	message := writeFailure(t, root, WriteInput{
		Path:           "notes.txt",
		Content:        "two\n",
		ExpectedSha256: first.Sha256,
	})

	if !strings.Contains(message, "changed since you last read it") {
		t.Errorf("refusal does not name the real problem: %s", message)
	}
	if !strings.Contains(message, "nothing was written") {
		t.Errorf("refusal does not say the file was left alone: %s", message)
	}
	if got := readFixture(t, filepath.Join(root, "notes.txt")); got != "someone else was here\n" {
		t.Errorf("the refused write still touched the file: %q", got)
	}
}

func TestWritePreconditionOnAFileThatDisappearedSaysSo(t *testing.T) {
	root := t.TempDir()
	first := writeIn(t, root, WriteInput{Path: "notes.txt", Content: "one\n"})

	if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}

	message := writeFailure(t, root, WriteInput{
		Path:           "notes.txt",
		Content:        "two\n",
		ExpectedSha256: first.Sha256,
	})
	if !strings.Contains(message, "does not exist any more") {
		t.Errorf("refusal does not say the file is gone: %s", message)
	}
}

// TestWriteAcceptsAReformattedModifiedAt keeps the timestamp precondition from
// punishing a caller that re-zoned the value it was handed.
func TestWriteAcceptsAReformattedModifiedAt(t *testing.T) {
	root := t.TempDir()
	first := writeIn(t, root, WriteInput{Path: "notes.txt", Content: "one\n"})

	stamp, err := time.Parse(time.RFC3339, first.ModifiedAt)
	if err != nil {
		t.Fatalf("modifiedAt %q is not RFC 3339: %v", first.ModifiedAt, err)
	}
	elsewhere := stamp.In(time.FixedZone("UTC+2", 2*60*60)).Format(time.RFC3339Nano)

	writeIn(t, root, WriteInput{
		Path:               "notes.txt",
		Content:            "two\n",
		ExpectedModifiedAt: elsewhere,
	})
}

// TestEditRefusesAStaleShaInsteadOfBlamingTheText is item 2's real point: the
// two failures have different fixes, so they must read differently.
func TestEditRefusesAStaleShaInsteadOfBlamingTheText(t *testing.T) {
	root := t.TempDir()
	first := writeIn(t, root, WriteInput{Path: "notes.txt", Content: "alpha\n"})

	foreignWrite(t, root, "notes.txt", "beta\n")

	message := editError(t, root, EditInput{
		Path:           "notes.txt",
		Edits:          []EditBlock{{OldText: "alpha", NewText: "gamma"}},
		ExpectedSha256: first.Sha256,
	})

	if !strings.Contains(message, "precondition failed") {
		t.Errorf("a stale copy was reported as a text mismatch: %s", message)
	}
	if !strings.Contains(message, "this is not a text matching failure") {
		t.Errorf("refusal does not separate the two failure modes: %s", message)
	}
	if got := readFixture(t, filepath.Join(root, "notes.txt")); got != "beta\n" {
		t.Errorf("the refused edit still touched the file: %q", got)
	}
}

// TestEditChainsTheShaItReturns is the round trip a long editing session
// depends on: read once, then keep the guard alive across edits for free.
func TestEditChainsTheShaItReturns(t *testing.T) {
	root := t.TempDir()
	writeIn(t, root, WriteInput{Path: "notes.txt", Content: "alpha\nbeta\n"})

	read := readIn(t, root, ReadInput{Path: "notes.txt", MetadataOnly: true})

	first, _ := runEdit(t, root, EditInput{
		Path:           "notes.txt",
		Edits:          []EditBlock{{OldText: "alpha", NewText: "ALPHA"}},
		ExpectedSha256: read.Sha256,
	})
	if first.Sha256 == "" {
		t.Fatal("a successful edit returned no sha256, so the chain breaks")
	}

	runEdit(t, root, EditInput{
		Path:           "notes.txt",
		Edits:          []EditBlock{{OldText: "beta", NewText: "BETA"}},
		ExpectedSha256: first.Sha256,
	})

	if got := readFixture(t, filepath.Join(root, "notes.txt")); got != "ALPHA\nBETA\n" {
		t.Errorf("file = %q, want both edits applied", got)
	}
}

// TestPreconditionsAreOptional protects every caller written before they
// existed.
func TestPreconditionsAreOptional(t *testing.T) {
	root := t.TempDir()

	writeIn(t, root, WriteInput{Path: "notes.txt", Content: "one\n"})
	foreignWrite(t, root, "notes.txt", "two\n")
	writeIn(t, root, WriteInput{Path: "notes.txt", Content: "three\n"})

	runEdit(t, root, EditInput{
		Path:  "notes.txt",
		Edits: []EditBlock{{OldText: "three", NewText: "four"}},
	})

	if got := readFixture(t, filepath.Join(root, "notes.txt")); got != "four\n" {
		t.Errorf("file = %q, want the unconditional edit to apply", got)
	}
}
