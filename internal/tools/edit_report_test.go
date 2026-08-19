package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// failingEdit runs a batch that is expected to fail and returns both halves of
// the answer: the message and the structured report.
func failingEdit(t *testing.T, root string, input EditInput) (string, EditOutput) {
	t.Helper()

	result, out, err := EditFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("EditFile returned a transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("expected the edit to fail")
	}
	return resultError(result), out
}

// TestEditFailureStatesThatNothingWasApplied answers the question a caller asks
// first after a failed batch: is the file half edited?
func TestEditFailureStatesThatNothingWasApplied(t *testing.T) {
	root, path := writeFixture(t, "notes.txt", "alpha\nbeta\n")

	message, out := failingEdit(t, root, EditInput{
		Path: "notes.txt",
		Edits: []EditBlock{
			{OldText: "alpha", NewText: "ALPHA"},
			{OldText: "gamma", NewText: "GAMMA"},
		},
	})

	if !strings.Contains(message, "file unchanged: no edits were applied") {
		t.Errorf("failure does not state the outcome for the file:\n%s", message)
	}
	if !strings.Contains(message, "edit 2 of 2 failed") {
		t.Errorf("failure does not say which edit of how many failed:\n%s", message)
	}
	if out.Status != "failed" {
		t.Errorf("status = %q, want failed", out.Status)
	}
	if out.Applied {
		t.Error("applied = true for a failed batch")
	}
	if got := readFixture(t, path); got != "alpha\nbeta\n" {
		t.Errorf("file = %q, want it untouched", got)
	}
}

// TestEditFailureReportsTheStatusOfEveryEdit tells a caller which sub-edits are
// safe to re-issue, instead of leaving it to guess.
func TestEditFailureReportsTheStatusOfEveryEdit(t *testing.T) {
	root, _ := writeFixture(t, "notes.txt", "alpha\nbeta\n")

	message, out := failingEdit(t, root, EditInput{
		Path: "notes.txt",
		Edits: []EditBlock{
			{OldText: "alpha", NewText: "ALPHA"},
			{OldText: "gamma", NewText: "GAMMA"},
		},
	})

	for _, want := range []string{
		"edit status:",
		"1. matched at line 1",
		"safe to re-issue unchanged",
		"2. FAILED",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("failure is missing %q:\n%s", want, message)
		}
	}

	if len(out.Edits) != 2 {
		t.Fatalf("edits has %d entries, want one per requested edit", len(out.Edits))
	}
	if out.Edits[0].Status != matchStatusMatched {
		t.Errorf("edit 1 status = %q, want %q", out.Edits[0].Status, matchStatusMatched)
	}
	if out.Edits[0].Index != 1 || out.Edits[1].Index != 2 {
		t.Errorf("indexes = %d and %d, want 1 and 2", out.Edits[0].Index, out.Edits[1].Index)
	}
	if len(out.Edits[0].Lines) != 1 || out.Edits[0].Lines[0] != 1 {
		t.Errorf("edit 1 lines = %v, want [1]", out.Edits[0].Lines)
	}
	if out.Edits[1].Status != matchStatusFailed {
		t.Errorf("edit 2 status = %q, want %q", out.Edits[1].Status, matchStatusFailed)
	}
}

// TestEditFailureMarksLaterEditsNotAttempted separates "your text was wrong"
// from "we never got that far".
func TestEditFailureMarksLaterEditsNotAttempted(t *testing.T) {
	root, _ := writeFixture(t, "notes.txt", "alpha\nbeta\n")

	message, out := failingEdit(t, root, EditInput{
		Path: "notes.txt",
		Edits: []EditBlock{
			{OldText: "gamma", NewText: "GAMMA"},
			{OldText: "beta", NewText: "BETA"},
		},
	})

	if !strings.Contains(message, "not attempted, because an earlier edit failed first") {
		t.Errorf("failure does not explain the untried edit:\n%s", message)
	}
	if len(out.Edits) != 2 {
		t.Fatalf("edits has %d entries, want 2", len(out.Edits))
	}
	if out.Edits[1].Status != matchStatusNotAttempted {
		t.Errorf("edit 2 status = %q, want %q", out.Edits[1].Status, matchStatusNotAttempted)
	}
}

// TestEditFailureKeepsTheClosestMatchDiagnostics guards the part of the report
// that already worked well.
func TestEditFailureKeepsTheClosestMatchDiagnostics(t *testing.T) {
	root, _ := writeFixture(t, "notes.txt", "func main() {\n\tprintln(\"hello\")\n}\n")

	message, _ := failingEdit(t, root, EditInput{
		Path:  "notes.txt",
		Edits: []EditBlock{{OldText: "println(\"hallo\")", NewText: "println(\"bye\")"}},
	})

	if !strings.Contains(message, "dryRun") {
		t.Errorf("failure lost the dryRun tip:\n%s", message)
	}
	if !strings.Contains(message, "line") {
		t.Errorf("failure lost the nearest line diagnostic:\n%s", message)
	}
	if !strings.Contains(message, "file unchanged: no edits were applied") {
		t.Errorf("a single edit failure should also state the outcome:\n%s", message)
	}
}

// TestEditSuccessReportsWhereEachEditLandedAndAFreshSha is the success side of
// the same contract.
func TestEditSuccessReportsWhereEachEditLandedAndAFreshSha(t *testing.T) {
	root, path := writeFixture(t, "notes.txt", "alpha\nbeta\n")

	out, message := runEdit(t, root, EditInput{
		Path: "notes.txt",
		Edits: []EditBlock{
			{OldText: "alpha", NewText: "ALPHA"},
			{OldText: "beta", NewText: "BETA"},
		},
	})

	if out.Status != "applied" || !out.Applied {
		t.Errorf("status = %q and applied = %v, want applied", out.Status, out.Applied)
	}
	if len(out.Edits) != 2 {
		t.Fatalf("edits has %d entries, want 2", len(out.Edits))
	}
	for _, edit := range out.Edits {
		if edit.Status != matchStatusMatched {
			t.Errorf("edit %d status = %q, want matched", edit.Index, edit.Status)
		}
	}
	if out.Sha256 == "" || out.ModifiedAt == "" {
		t.Errorf("a successful edit must report sha256 and modifiedAt, got %q and %q", out.Sha256, out.ModifiedAt)
	}
	if want := hashBytes([]byte(readFixture(t, path))); out.Sha256 != want {
		t.Errorf("sha256 = %s, want the hash of the bytes on disk %s", out.Sha256, want)
	}
	if !strings.Contains(message, "sha256") {
		t.Errorf("success message does not offer the sha256 for the next call:\n%s", message)
	}
}

// TestEditDryRunSaysNothingWasWritten removes the ambiguity that made a dry run
// read like a completed edit.
func TestEditDryRunSaysNothingWasWritten(t *testing.T) {
	root, path := writeFixture(t, "notes.txt", "alpha\nbeta\n")

	out, message := runEdit(t, root, EditInput{
		Path:   "notes.txt",
		Edits:  []EditBlock{{OldText: "alpha", NewText: "ALPHA"}},
		DryRun: true,
	})

	if out.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", out.Status)
	}
	if out.Applied {
		t.Error("applied = true for a dry run")
	}
	if !strings.Contains(message, "Nothing was written") {
		t.Errorf("dry run does not say it wrote nothing:\n%s", message)
	}
	if len(out.Edits) != 1 || out.Edits[0].Status != matchStatusMatched {
		t.Errorf("a dry run should still report where the edit would land: %+v", out.Edits)
	}
	if got := readFixture(t, path); got != "alpha\nbeta\n" {
		t.Errorf("file = %q, want it untouched", got)
	}
	if filepath.Base(path) != "notes.txt" {
		t.Fatalf("unexpected fixture path %s", path)
	}
}
