package tools

import (
	"context"
	"strings"
	"testing"
)

// The helpers below drive the real tools, because the point of the journal is
// that the tools record themselves without being asked to.

func journalWrite(t *testing.T, root, path, content string) WriteOutput {
	t.Helper()

	result, out, err := WriteFile(context.Background(), nil, WriteInput{Path: path, Content: content}, root)
	if err != nil {
		t.Fatalf("WriteFile returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("write failed: %s", resultError(result))
	}
	return out
}

func journalEdit(t *testing.T, root, path, oldText, newText string) {
	t.Helper()

	result, _, err := EditFile(context.Background(), nil, EditInput{
		Path:  path,
		Edits: []EditBlock{{OldText: oldText, NewText: newText}},
	}, root)
	if err != nil {
		t.Fatalf("EditFile returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("edit failed: %s", resultError(result))
	}
}

func journalMkdir(t *testing.T, root, path string) {
	t.Helper()

	result, _, err := MakeDirectory(context.Background(), nil, MkdirInput{Path: path}, root)
	if err != nil {
		t.Fatalf("MakeDirectory returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("mkdir failed: %s", resultError(result))
	}
}

func journalMove(t *testing.T, root, from, to string) {
	t.Helper()

	result, _, err := MovePath(context.Background(), nil, MoveInput{SourcePath: from, TargetPath: to}, root)
	if err != nil {
		t.Fatalf("MovePath returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("move failed: %s", resultError(result))
	}
}

func journalRemove(t *testing.T, root, path string) {
	t.Helper()

	result, _, err := RemovePath(context.Background(), nil, RemoveInput{Path: path}, root)
	if err != nil {
		t.Fatalf("RemovePath returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("remove failed: %s", resultError(result))
	}
}

// recentChangesIn asks the journal what this process wrote. The git cross-check
// is skipped because a temporary directory is not a repository.
func recentChangesIn(t *testing.T, root string, input RecentChangesInput) RecentChangesOutput {
	t.Helper()

	input.SkipGitStatus = true
	result, out, err := RecentChanges(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("RecentChanges returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("recent_changes failed: %s", resultError(result))
	}
	return out
}

// TestRecentChangesRecordsEveryKindOfChangeNewestFirst is the answer to "what
// have I written?" for a caller that has lost its own history.
func TestRecentChangesRecordsEveryKindOfChangeNewestFirst(t *testing.T) {
	writeJournal.reset()

	root := t.TempDir()
	journalWrite(t, root, "a.txt", "one\n")
	journalWrite(t, root, "b.txt", "two\n")
	journalEdit(t, root, "a.txt", "one", "uno")
	journalMkdir(t, root, "sub")
	journalMove(t, root, "b.txt", "sub/c.txt")
	journalRemove(t, root, "sub/c.txt")

	out := recentChangesIn(t, root, RecentChangesInput{})

	if len(out.Changes) == 0 {
		t.Fatal("the journal is empty after six changes")
	}
	if got := out.Changes[0].Op; got != string(ChangeRemove) {
		t.Errorf("newest op = %q, want %q", got, string(ChangeRemove))
	}
	for i := 1; i < len(out.Changes); i++ {
		if out.Changes[i-1].Sequence <= out.Changes[i].Sequence {
			t.Fatalf("changes are not newest first: sequence %d came before %d",
				out.Changes[i-1].Sequence, out.Changes[i].Sequence)
		}
	}

	seen := make(map[string]bool)
	for _, change := range out.Changes {
		seen[change.Op] = true
		if change.At == "" {
			t.Errorf("change %d has no timestamp", change.Sequence)
		}
		if change.Ago == "" {
			t.Errorf("change %d does not say how long ago it happened", change.Sequence)
		}
		if change.Path == "" {
			t.Errorf("change %d has no path", change.Sequence)
		}
	}
	for _, op := range []string{
		string(ChangeWrite), string(ChangeEdit), string(ChangeMkdir),
		string(ChangeMove), string(ChangeRemove),
	} {
		if !seen[op] {
			t.Errorf("no %s entry was recorded:\n%s", op, out.Result)
		}
	}
	if out.TotalRecorded < 6 {
		t.Errorf("totalRecorded = %d, want at least 6", out.TotalRecorded)
	}
}

func TestRecentChangesFiltersByOperationAndPath(t *testing.T) {
	writeJournal.reset()

	root := t.TempDir()
	journalWrite(t, root, "src/main.go", "package main\n")
	journalWrite(t, root, "docs/readme.md", "hello\n")
	journalEdit(t, root, "src/main.go", "package main", "package app")

	edits := recentChangesIn(t, root, RecentChangesInput{Op: string(ChangeEdit)})
	if edits.Returned != 1 {
		t.Fatalf("the edit filter returned %d changes, want 1:\n%s", edits.Returned, edits.Result)
	}
	if edits.Changes[0].Op != string(ChangeEdit) {
		t.Errorf("op = %q, want edit", edits.Changes[0].Op)
	}

	docs := recentChangesIn(t, root, RecentChangesInput{Path: "docs"})
	if docs.Returned != 1 {
		t.Fatalf("the docs filter returned %d changes, want 1:\n%s", docs.Returned, docs.Result)
	}
	if !strings.Contains(docs.Changes[0].Path, "readme.md") {
		t.Errorf("path = %q, want the file under docs", docs.Changes[0].Path)
	}
}

func TestRecentChangesLimitStillSaysHowManyMatched(t *testing.T) {
	writeJournal.reset()

	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		journalWrite(t, root, name+".txt", "x\n")
	}

	out := recentChangesIn(t, root, RecentChangesInput{Limit: 2})

	if out.Returned != 2 || len(out.Changes) != 2 {
		t.Errorf("returned = %d with %d changes, want 2 of each", out.Returned, len(out.Changes))
	}
	if out.Matched != 5 {
		t.Errorf("matched = %d, want 5 so the caller knows what the limit hid", out.Matched)
	}
}

func TestRecentChangesIsScopedToOneWorkspaceUnlessAsked(t *testing.T) {
	writeJournal.reset()

	first := t.TempDir()
	second := t.TempDir()
	journalWrite(t, first, "first.txt", "1\n")
	journalWrite(t, second, "second.txt", "2\n")

	mine := recentChangesIn(t, first, RecentChangesInput{})
	if mine.Returned != 1 {
		t.Fatalf("returned = %d, want only this workspace's change:\n%s", mine.Returned, mine.Result)
	}
	if !strings.Contains(mine.Changes[0].Path, "first.txt") {
		t.Errorf("path = %q, want first.txt", mine.Changes[0].Path)
	}

	both := recentChangesIn(t, first, RecentChangesInput{AllWorkspaces: true})
	if both.Returned != 2 {
		t.Errorf("returned = %d with allWorkspaces, want 2", both.Returned)
	}
}

// TestRecentChangesOnAnEmptyJournalStillAnswers matters most right after a
// restart, when "I wrote nothing" is the useful answer.
func TestRecentChangesOnAnEmptyJournalStillAnswers(t *testing.T) {
	writeJournal.reset()

	out := recentChangesIn(t, t.TempDir(), RecentChangesInput{})

	if out.Returned != 0 {
		t.Errorf("returned = %d, want 0", out.Returned)
	}
	if out.Verdict == "" {
		t.Error("verdict is empty; a caller needs one sentence it can act on")
	}
	if out.Result == "" {
		t.Error("result is empty")
	}
	if out.GitChecked {
		t.Error("gitChecked = true although the cross-check was skipped")
	}
}

func TestRecentChangesRejectsAnUnknownOperationFilter(t *testing.T) {
	writeJournal.reset()

	root := t.TempDir()
	journalWrite(t, root, "a.txt", "x\n")

	result, out, err := RecentChanges(context.Background(), nil,
		RecentChangesInput{Op: "rewrite", SkipGitStatus: true}, root)
	if err != nil {
		t.Fatalf("RecentChanges returned a transport error: %v", err)
	}
	if refused := result != nil && result.IsError; !refused && out.Returned != 0 {
		t.Errorf("an unknown op filter matched %d changes; want a refusal or an empty answer", out.Returned)
	}
}

func TestRecentChangesRecordsSizeAndDetail(t *testing.T) {
	writeJournal.reset()

	root := t.TempDir()
	journalWrite(t, root, "a.txt", "hello\n")

	out := recentChangesIn(t, root, RecentChangesInput{})

	if len(out.Changes) != 1 {
		t.Fatalf("changes has %d entries, want 1", len(out.Changes))
	}
	if out.Changes[0].Bytes != 6 {
		t.Errorf("bytes = %d, want 6", out.Changes[0].Bytes)
	}
	if out.Changes[0].Detail == "" {
		t.Error("detail is empty; a write should say whether it created the file")
	}
	if out.FilesTouched != 1 {
		t.Errorf("filesTouched = %d, want 1", out.FilesTouched)
	}
}
