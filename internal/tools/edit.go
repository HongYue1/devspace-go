package tools

import (
	"errors"
	"fmt"
	"strings"
)

const (
	lineEndingLF   = "\n"
	lineEndingCRLF = "\r\n"
)

// maxDiagnosticSpans bounds the near-match scan so a very large file cannot
// make a failed edit expensive.
const maxDiagnosticSpans = 50000

// trailingSpace is the set of characters ignored at the end of a line when the
// exact match fails.
const trailingSpace = " \t\v\f\r"

// detectLineEnding reports the dominant line ending of content.
func detectLineEnding(content string) string {
	crlf := strings.Count(content, lineEndingCRLF)
	lf := strings.Count(content, lineEndingLF) - crlf
	if crlf > lf {
		return lineEndingCRLF
	}
	return lineEndingLF
}

// hasMixedLineEndings reports whether content mixes CRLF and LF. Such a file is
// edited byte for byte so that untouched lines keep their original ending.
func hasMixedLineEndings(content string) bool {
	crlf := strings.Count(content, lineEndingCRLF)
	lf := strings.Count(content, lineEndingLF) - crlf
	return crlf > 0 && lf > 0
}

// normalizeNewlines converts CRLF and lone CR to LF.
func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, lineEndingCRLF, lineEndingLF)
	return strings.ReplaceAll(s, "\r", lineEndingLF)
}

// restoreLineEndings rewrites LF back to the file's own line ending.
func restoreLineEndings(s, ending string) string {
	if ending == lineEndingLF {
		return s
	}
	return strings.ReplaceAll(s, lineEndingLF, lineEndingCRLF)
}

func trimRight(s string) string {
	return strings.TrimRight(s, trailingSpace)
}

// lineNumberAt returns the 1-based line number of a byte offset.
func lineNumberAt(content string, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	return strings.Count(content[:offset], lineEndingLF) + 1
}

// lineSpan records where one line sits inside the content.
type lineSpan struct {
	start      int // first byte of the line
	contentEnd int // byte after the last character, before the newline
	next       int // first byte of the following line
}

func splitLineSpans(content string) []lineSpan {
	spans := make([]lineSpan, 0, strings.Count(content, lineEndingLF)+1)
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			spans = append(spans, lineSpan{start: start, contentEnd: i, next: i + 1})
			start = i + 1
		}
	}
	return append(spans, lineSpan{start: start, contentEnd: len(content), next: len(content)})
}

// findOccurrences returns the byte offsets of every non-overlapping occurrence
// of needle in content.
func findOccurrences(content, needle string) []int {
	if needle == "" {
		return nil
	}
	var offsets []int
	for from := 0; from <= len(content)-len(needle); {
		i := strings.Index(content[from:], needle)
		if i < 0 {
			break
		}
		offsets = append(offsets, from+i)
		from += i + len(needle)
	}
	return offsets
}

// needleLines splits oldText into whole lines, reporting whether it ended on a
// line boundary.
func needleLines(needle string) ([]string, bool) {
	lines := strings.Split(needle, lineEndingLF)
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1], true
	}
	return lines, false
}

// findLineBlocks locates oldText as a run of whole lines, comparing them with
// trailing whitespace removed. This recovers the common case where the caller
// copied a block that differs only in invisible characters.
func findLineBlocks(content, needle string) [][2]int {
	lines, endsWithNewline := needleLines(needle)
	if len(lines) == 0 {
		return nil
	}

	spans := splitLineSpans(content)
	if len(spans) > maxDiagnosticSpans {
		return nil
	}

	var blocks [][2]int
	for i := 0; i+len(lines) <= len(spans); i++ {
		matched := true
		for j, line := range lines {
			span := spans[i+j]
			if trimRight(content[span.start:span.contentEnd]) != trimRight(line) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}

		last := spans[i+len(lines)-1]
		end := last.contentEnd
		if endsWithNewline {
			end = last.next
		}
		blocks = append(blocks, [2]int{spans[i].start, end})
	}
	return blocks
}

// editDiagnostic describes the closest thing to oldText that the file contains.
type editDiagnostic struct {
	line      int
	matched   int
	total     int
	firstDiff int
	expected  string
	actual    string
}

// nearestMatch finds the window of lines that most closely resembles oldText.
func nearestMatch(content, needle string) *editDiagnostic {
	lines, _ := needleLines(needle)
	if len(lines) == 0 {
		return nil
	}

	spans := splitLineSpans(content)
	if len(spans) < len(lines) || len(spans) > maxDiagnosticSpans {
		return nil
	}

	best, bestScore := -1, 0
	for i := 0; i+len(lines) <= len(spans); i++ {
		score := 0
		for j, line := range lines {
			span := spans[i+j]
			if trimRight(content[span.start:span.contentEnd]) == trimRight(line) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}

	// Require a third of the lines to line up before calling it a near miss.
	if best < 0 || bestScore == 0 || bestScore*3 < len(lines) {
		return nil
	}

	diagnostic := &editDiagnostic{line: best + 1, matched: bestScore, total: len(lines)}
	for j, line := range lines {
		span := spans[best+j]
		actual := content[span.start:span.contentEnd]
		if trimRight(actual) != trimRight(line) {
			diagnostic.firstDiff = best + j + 1
			diagnostic.expected = line
			diagnostic.actual = actual
			break
		}
	}
	return diagnostic
}

// describeLineEnding names the ending style for an error message.
func describeLineEnding(content string) string {
	if hasMixedLineEndings(content) {
		return "mixed CRLF and LF"
	}
	if detectLineEnding(content) == lineEndingCRLF {
		return "CRLF"
	}
	return "LF"
}

// editNotFoundError explains why oldText did not match, instead of only saying
// that it did not.
func editNotFoundError(number int, path, original, content, needle string) error {
	var message strings.Builder
	fmt.Fprintf(&message, "edit %d: oldText not found in %s", number, path)
	fmt.Fprintf(&message, "\n  file: %d lines, %s line endings",
		len(splitLineSpans(content)), describeLineEnding(original))

	if diagnostic := nearestMatch(content, needle); diagnostic != nil {
		fmt.Fprintf(&message, "\n  closest match: line %d (%d of %d lines match)",
			diagnostic.line, diagnostic.matched, diagnostic.total)
		if diagnostic.firstDiff > 0 {
			fmt.Fprintf(&message, "\n  first difference at line %d:", diagnostic.firstDiff)
			fmt.Fprintf(&message, "\n    oldText: %q", diagnostic.expected)
			fmt.Fprintf(&message, "\n    file:    %q", diagnostic.actual)
		}
	} else {
		message.WriteString("\n  no similar text found; re-read the file and copy oldText from it")
	}

	if hasMixedLineEndings(original) {
		message.WriteString("\n  note: this file mixes CRLF and LF, so oldText must match byte for byte")
	} else {
		message.WriteString("\n  note: line endings and trailing whitespace are already ignored when matching")
	}
	message.WriteString("\n  tip: pass dryRun to test an edit without writing")

	return errors.New(message.String())
}

// editAmbiguousError lists where the duplicate matches are, so the caller can
// extend oldText instead of guessing.
func editAmbiguousError(number int, content string, starts []int) error {
	lines := make([]string, 0, len(starts))
	for _, start := range starts {
		lines = append(lines, fmt.Sprintf("%d", lineNumberAt(content, start)))
	}
	return fmt.Errorf(
		"edit %d: oldText matches %d times (lines %s), must be unique; add surrounding context to oldText",
		number, len(starts), strings.Join(lines, ", "))
}

// applyEdit replaces one occurrence of oldText, falling back to a whitespace
// tolerant line match when the exact text is absent.
func applyEdit(content string, edit EditBlock, number int, path, original string, allowTolerant bool) (string, string, error) {
	if edit.OldText == "" {
		return "", "", fmt.Errorf("edit %d: oldText must not be empty", number)
	}

	if starts := findOccurrences(content, edit.OldText); len(starts) > 0 {
		if len(starts) > 1 {
			return "", "", editAmbiguousError(number, content, starts)
		}
		start := starts[0]
		note := fmt.Sprintf("  edit %d: line %d, exact match", number, lineNumberAt(content, start))
		return content[:start] + edit.NewText + content[start+len(edit.OldText):], note, nil
	}

	if allowTolerant {
		if blocks := findLineBlocks(content, edit.OldText); len(blocks) > 0 {
			if len(blocks) > 1 {
				starts := make([]int, 0, len(blocks))
				for _, block := range blocks {
					starts = append(starts, block[0])
				}
				return "", "", editAmbiguousError(number, content, starts)
			}
			start, end := blocks[0][0], blocks[0][1]
			note := fmt.Sprintf("  edit %d: line %d, matched ignoring trailing whitespace",
				number, lineNumberAt(content, start))
			return content[:start] + edit.NewText + content[end:], note, nil
		}
	}

	return "", "", editNotFoundError(number, path, original, content, edit.OldText)
}
