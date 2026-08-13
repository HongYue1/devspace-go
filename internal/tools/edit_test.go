package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const crlfSource = "package main\r\n" +
	"\r\n" +
	"import \"fmt\"\r\n" +
	"\r\n" +
	"func main() {\r\n" +
	"\tfmt.Println(\"hello\")\r\n" +
	"}\r\n"

func writeFixture(t *testing.T, name, content string) (root, path string) {
	t.Helper()

	root = t.TempDir()
	path = filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, path
}

// readFixture returns the exact bytes on disk, line endings included.
func readFixture(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// resultError returns the message a failed tool call reports to the client.
func resultError(result *mcp.CallToolResult) string {
	if err := result.GetError(); err != nil {
		return err.Error()
	}

	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func runEdit(t *testing.T, root string, input EditInput) (EditOutput, string) {
	t.Helper()

	result, out, err := EditFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("EditFile returned a transport error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("edit failed: %s", resultError(result))
	}
	return out, out.Result
}

// editError runs an edit that is expected to fail and returns the message.
func editError(t *testing.T, root string, input EditInput) string {
	t.Helper()

	result, _, err := EditFile(context.Background(), nil, input, root)
	if err != nil {
		t.Fatalf("EditFile returned a transport error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("expected the edit to fail")
	}
	return resultError(result)
}

// TestEditAppliesLFOldTextToCRLFFile reproduces the reported defect. On a
// Windows checkout every tracked file is CRLF while read returns LF, so oldText
// copied from read could never match.
func TestEditAppliesLFOldTextToCRLFFile(t *testing.T) {
	root, path := writeFixture(t, "main.go", crlfSource)

	_, message := runEdit(t, root, EditInput{
		Path: "main.go",
		Edits: []EditBlock{{
			OldText: "func main() {\n\tfmt.Println(\"hello\")\n}",
			NewText: "func main() {\n\tfmt.Println(\"goodbye\")\n}",
		}},
	})

	updated := readFixture(t, path)
	if !strings.Contains(updated, "goodbye") {
		t.Fatalf("edit did not apply; message: %s", message)
	}
}

// TestEditKeepsCRLFLineEndings makes sure the tolerant match does not quietly
// rewrite every line ending in the file.
func TestEditKeepsCRLFLineEndings(t *testing.T) {
	root, path := writeFixture(t, "main.go", crlfSource)

	runEdit(t, root, EditInput{
		Path:  "main.go",
		Edits: []EditBlock{{OldText: "hello", NewText: "goodbye"}},
	})

	updated := readFixture(t, path)
	if strings.Count(updated, "\n") != strings.Count(updated, "\r\n") {
		t.Fatalf("edit introduced bare LF line endings:\n%q", updated)
	}
}

// TestEditAcceptsTextReturnedByRead is the end to end version of the defect:
// whatever read hands back must be usable as oldText.
func TestEditAcceptsTextReturnedByRead(t *testing.T) {
	root, path := writeFixture(t, "main.go", crlfSource)

	_, readOut, err := ReadFile(context.Background(), nil, ReadInput{Path: "main.go"}, root)
	if err != nil {
		t.Fatalf("ReadFile returned an error: %v", err)
	}

	lines := strings.Split(strings.TrimRight(readOut.Result, "\n"), "\n")
	if len(lines) < 6 {
		t.Fatalf("unexpected read output:\n%s", readOut.Result)
	}
	oldText := strings.Join(lines[4:6], "\n")

	runEdit(t, root, EditInput{
		Path: "main.go",
		Edits: []EditBlock{{
			OldText: oldText,
			NewText: oldText + "\n\t// added",
		}},
	})

	if updated := readFixture(t, path); !strings.Contains(updated, "// added") {
		t.Fatalf("text returned by read was rejected as oldText:\n%q", updated)
	}
}

func TestEditToleratesTrailingWhitespace(t *testing.T) {
	root, path := writeFixture(t, "notes.txt", "alpha\r\nbeta   \r\ngamma\r\n")

	runEdit(t, root, EditInput{
		Path: "notes.txt",
		Edits: []EditBlock{{
			OldText: "alpha\nbeta\ngamma",
			NewText: "alpha\nBETA\ngamma",
		}},
	})

	if updated := readFixture(t, path); !strings.Contains(updated, "BETA") {
		t.Fatalf("trailing whitespace blocked the match:\n%q", updated)
	}
}

func TestEditNotFoundExplainsWhy(t *testing.T) {
	root, _ := writeFixture(t, "main.go", crlfSource)

	message := editError(t, root, EditInput{
		Path: "main.go",
		Edits: []EditBlock{{
			OldText: "func main() {\n\t\tfmt.Println(\"hello\")\n}",
			NewText: "x",
		}},
	})

	for _, want := range []string{
		"line endings",
		"closest match: line",
		"first difference at line",
		"oldText:",
		"file:",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic is missing %q:\n%s", want, message)
		}
	}
}

func TestEditAmbiguousReportsEveryLine(t *testing.T) {
	root, _ := writeFixture(t, "dup.txt", "same\r\nother\r\nsame\r\n")

	message := editError(t, root, EditInput{
		Path:  "dup.txt",
		Edits: []EditBlock{{OldText: "same", NewText: "changed"}},
	})

	if !strings.Contains(message, "lines 1, 3") {
		t.Fatalf("ambiguity error should name both lines:\n%s", message)
	}
}

func TestEditDryRunLeavesFileUnchanged(t *testing.T) {
	root, path := writeFixture(t, "main.go", crlfSource)

	out, _ := runEdit(t, root, EditInput{
		Path:   "main.go",
		DryRun: true,
		Edits:  []EditBlock{{OldText: "hello", NewText: "goodbye"}},
	})

	if out.Status != "dry_run" {
		t.Fatalf("status = %q, want dry_run", out.Status)
	}
	if updated := readFixture(t, path); updated != crlfSource {
		t.Fatalf("dry run modified the file:\n%q", updated)
	}
	if !strings.Contains(out.Result, "line") {
		t.Fatalf("dry run should report where the edit would land:\n%s", out.Result)
	}
}

// TestEditIsAllOrNothing checks that a later failing edit does not leave the
// earlier ones on disk.
func TestEditIsAllOrNothing(t *testing.T) {
	root, path := writeFixture(t, "main.go", crlfSource)

	editError(t, root, EditInput{
		Path: "main.go",
		Edits: []EditBlock{
			{OldText: "hello", NewText: "goodbye"},
			{OldText: "does not exist anywhere", NewText: "x"},
		},
	})

	if updated := readFixture(t, path); updated != crlfSource {
		t.Fatalf("a failed batch was partially written:\n%q", updated)
	}
}

// TestEditLeavesMixedFileByteExact guards the one case where normalising would
// damage the file.
func TestEditLeavesMixedFileByteExact(t *testing.T) {
	mixed := "alpha\r\nbeta\ngamma\r\n"
	root, path := writeFixture(t, "mixed.txt", mixed)

	runEdit(t, root, EditInput{
		Path:  "mixed.txt",
		Edits: []EditBlock{{OldText: "beta", NewText: "BETA"}},
	})

	updated := readFixture(t, path)
	if updated != "alpha\r\nBETA\ngamma\r\n" {
		t.Fatalf("mixed line endings were rewritten:\n%q", updated)
	}
}

func TestEditRejectsEmptyOldText(t *testing.T) {
	root, _ := writeFixture(t, "main.go", crlfSource)

	message := editError(t, root, EditInput{
		Path:  "main.go",
		Edits: []EditBlock{{OldText: "", NewText: "x"}},
	})
	if !strings.Contains(message, "must not be empty") {
		t.Fatalf("unexpected message: %s", message)
	}
}

func TestDetectLineEnding(t *testing.T) {
	cases := []struct {
		content string
		want    string
	}{
		{"a\r\nb\r\n", lineEndingCRLF},
		{"a\nb\n", lineEndingLF},
		{"a\r\nb\r\nc\n", lineEndingCRLF},
		{"no newline", lineEndingLF},
	}
	for _, test := range cases {
		if got := detectLineEnding(test.content); got != test.want {
			t.Errorf("detectLineEnding(%q) = %q, want %q", test.content, got, test.want)
		}
	}
}
