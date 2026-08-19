package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// numberedLines builds a file whose content names its own line numbers, so a
// test can tell which page of it came back.
func numberedLines(count int) string {
	var content strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&content, "line%d\n", i)
	}
	return content.String()
}

// seedReadFiles writes every named file, creating parent directories, and
// returns the workspace root they live in.
func seedReadFiles(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readIn(t *testing.T, root string, input ReadInput) ReadOutput {
	t.Helper()

	result, out, err := ReadFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("ReadFile returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("read failed: %s", resultError(result))
	}
	return out
}

// readFailure runs a read that is expected to be refused and returns why.
func readFailure(t *testing.T, root string, input ReadInput) string {
	t.Helper()

	result, _, err := ReadFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("ReadFile returned a transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("expected the read to be refused")
	}
	return resultError(result)
}

// TestReadFirstPageSaysMoreRemains is the defect this metadata exists for: a
// caller must be able to tell a partial read from a whole file.
func TestReadFirstPageSaysMoreRemains(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"notes.txt": numberedLines(10)})

	out := readIn(t, root, ReadInput{Path: "notes.txt", Limit: 4})

	if out.TotalLines != 10 {
		t.Errorf("totalLines = %d, want 10", out.TotalLines)
	}
	if out.StartLine != 1 || out.EndLine != 4 {
		t.Errorf("returned lines %d-%d, want 1-4", out.StartLine, out.EndLine)
	}
	if out.ReturnedLines != 4 {
		t.Errorf("returnedLines = %d, want 4", out.ReturnedLines)
	}
	if !out.IsTruncated {
		t.Error("isTruncated = false although six lines were never returned")
	}
	if out.NextLine != 5 {
		t.Errorf("nextLine = %d, want 5", out.NextLine)
	}
	if len(out.Files) != 1 {
		t.Fatalf("files has %d entries, want 1", len(out.Files))
	}
	content := out.Files[0].Content
	if !strings.Contains(content, "line4") || strings.Contains(content, "line5") {
		t.Errorf("content = %q, want line1 through line4 only", content)
	}
}

// TestReadLastPageIsNotTruncated makes sure the flag goes back down, so a
// caller can stop paging.
func TestReadLastPageIsNotTruncated(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"notes.txt": numberedLines(10)})

	out := readIn(t, root, ReadInput{Path: "notes.txt", Offset: 9, Limit: 4})

	if out.StartLine != 9 || out.EndLine != 10 {
		t.Errorf("returned lines %d-%d, want 9-10", out.StartLine, out.EndLine)
	}
	if out.IsTruncated {
		t.Error("isTruncated = true at the end of the file")
	}
	if out.NextLine != 0 {
		t.Errorf("nextLine = %d, want 0 at the end of the file", out.NextLine)
	}
	if out.TotalLines != 10 {
		t.Errorf("totalLines = %d, want 10", out.TotalLines)
	}
}

// TestReadPastEndOfFileReportsTheRealLineCount stops an offset mistake from
// looking exactly like an empty file.
func TestReadPastEndOfFileReportsTheRealLineCount(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"notes.txt": numberedLines(10)})

	out := readIn(t, root, ReadInput{Path: "notes.txt", Offset: 50})

	if len(out.Files) != 1 {
		t.Fatalf("files has %d entries, want 1", len(out.Files))
	}
	file := out.Files[0]
	if !file.PastEndOfFile {
		t.Error("pastEndOfFile = false for an offset past the last line")
	}
	if file.TotalLines != 10 {
		t.Errorf("totalLines = %d, want the true count 10", file.TotalLines)
	}
	if file.Content != "" {
		t.Errorf("content = %q, want nothing", file.Content)
	}
	if !strings.Contains(out.Result, "10") {
		t.Errorf("result = %q, want it to state the real line count", out.Result)
	}
}

// TestReadBatchReturnsEveryRequestedFile covers the reconnaissance case that
// used to cost one round trip per file.
func TestReadBatchReturnsEveryRequestedFile(t *testing.T) {
	root := seedReadFiles(t, map[string]string{
		"a.txt":     numberedLines(3),
		"sub/b.txt": numberedLines(7),
	})

	out := readIn(t, root, ReadInput{Paths: []string{"a.txt", "sub/b.txt"}})

	if len(out.Files) != 2 {
		t.Fatalf("files has %d entries, want 2", len(out.Files))
	}
	if !strings.Contains(out.Files[0].Path, "a.txt") {
		t.Errorf("first entry is %q, want the first requested path", out.Files[0].Path)
	}
	if !strings.Contains(out.Files[1].Path, "b.txt") {
		t.Errorf("second entry is %q, want the second requested path", out.Files[1].Path)
	}
	if out.Files[0].TotalLines != 3 || out.Files[1].TotalLines != 7 {
		t.Errorf("totalLines = %d and %d, want 3 and 7",
			out.Files[0].TotalLines, out.Files[1].TotalLines)
	}
	for _, file := range out.Files {
		if file.Sha256 == "" {
			t.Errorf("%s came back without a sha256, so it cannot be used as a precondition", file.Path)
		}
		if file.ModifiedAt == "" {
			t.Errorf("%s came back without a modifiedAt", file.Path)
		}
	}
	if !strings.Contains(out.Result, "a.txt") || !strings.Contains(out.Result, "b.txt") {
		t.Errorf("result does not name both files:\n%s", out.Result)
	}
}

// TestReadBatchReportsAMissingFileWithoutLosingTheOthers keeps one typo from
// wasting the whole batch.
func TestReadBatchReportsAMissingFileWithoutLosingTheOthers(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"a.txt": numberedLines(3)})

	out := readIn(t, root, ReadInput{Paths: []string{"a.txt", "ghost.txt"}})

	if len(out.Files) != 2 {
		t.Fatalf("files has %d entries, want 2", len(out.Files))
	}
	if out.Files[0].Error != "" {
		t.Errorf("the readable file reported an error: %s", out.Files[0].Error)
	}
	if out.Files[0].Content == "" {
		t.Error("the readable file came back empty")
	}
	if out.Files[1].Error == "" {
		t.Error("the missing file did not say why it could not be read")
	}
}

func TestReadBatchRefusesMoreThanTwentyPaths(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"a.txt": "one\n"})

	paths := make([]string, maxBatchReadFiles+1)
	for i := range paths {
		paths[i] = "a.txt"
	}

	message := readFailure(t, root, ReadInput{Paths: paths})
	if !strings.Contains(message, fmt.Sprintf("%d", maxBatchReadFiles)) {
		t.Errorf("refusal does not state the limit: %s", message)
	}
}

// TestReadMetadataOnlySkipsTheBody is the cheap way to refresh a precondition
// without paying for the file twice.
func TestReadMetadataOnlySkipsTheBody(t *testing.T) {
	content := numberedLines(10)
	root := seedReadFiles(t, map[string]string{"notes.txt": content})

	out := readIn(t, root, ReadInput{Path: "notes.txt", MetadataOnly: true})

	if len(out.Files) != 1 {
		t.Fatalf("files has %d entries, want 1", len(out.Files))
	}
	file := out.Files[0]
	if file.Content != "" {
		t.Errorf("content = %q, want nothing for a metadata only read", file.Content)
	}
	if file.TotalLines != 10 {
		t.Errorf("totalLines = %d, want 10", file.TotalLines)
	}
	if file.Bytes != int64(len(content)) {
		t.Errorf("bytes = %d, want %d", file.Bytes, len(content))
	}
	if file.Sha256 == "" {
		t.Error("sha256 is empty, so the read cannot refresh a precondition")
	}
	if file.LineEnding != "LF" {
		t.Errorf("lineEnding = %q, want LF", file.LineEnding)
	}
}

// TestReadSha256CoversTheBytesOnDisk is the property that makes the hash worth
// having: it must describe the file, not the normalized text read returns, or
// it could never detect a writer that only changed line endings.
func TestReadSha256CoversTheBytesOnDisk(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"main.go": crlfSource})

	out := readIn(t, root, ReadInput{Path: "main.go"})

	if want := hashBytes([]byte(crlfSource)); out.Sha256 != want {
		t.Errorf("sha256 = %s, want the hash of the CRLF bytes on disk %s", out.Sha256, want)
	}
	if out.LineEnding != "CRLF" {
		t.Errorf("lineEnding = %q, want CRLF", out.LineEnding)
	}
}

// TestReadFooterIsNotPartOfTheContent guards the trap this footer creates: it
// belongs to the report, never to the file, or it would poison an edit.
func TestReadFooterIsNotPartOfTheContent(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"notes.txt": numberedLines(10)})

	out := readIn(t, root, ReadInput{Path: "notes.txt"})

	if !strings.Contains(out.Result, "of 10") {
		t.Errorf("result does not summarise the read:\n%s", out.Result)
	}
	if strings.Contains(out.Files[0].Content, "of 10") {
		t.Errorf("the report leaked into the file content:\n%s", out.Files[0].Content)
	}
	if !strings.HasSuffix(strings.TrimRight(out.Files[0].Content, "\n"), "line10") {
		t.Errorf("content does not end with the last line of the file:\n%q", out.Files[0].Content)
	}
}

// TestReadSinglePathMirrorsItsEntry keeps the common case free of indexing.
func TestReadSinglePathMirrorsItsEntry(t *testing.T) {
	root := seedReadFiles(t, map[string]string{"notes.txt": numberedLines(4)})

	out := readIn(t, root, ReadInput{Path: "notes.txt"})

	if len(out.Files) != 1 {
		t.Fatalf("files has %d entries, want 1", len(out.Files))
	}
	file := out.Files[0]
	if out.Path != file.Path || out.TotalLines != file.TotalLines || out.Sha256 != file.Sha256 {
		t.Errorf("top level fields disagree with files[0]: %+v vs %+v", out, file)
	}
}
