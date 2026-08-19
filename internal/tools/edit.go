package tools

import (
	"errors"
	"fmt"
	"strconv"
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

// Match outcomes for one requested edit.
const (
	matchStatusMatched      = "matched"
	matchStatusFailed       = "failed"
	matchStatusNotAttempted = "not_attempted"
)

// editAttempt records what happened to a single edit in the batch.
//
// A caller told only that "edit 2 did not match" still has to guess whether
// edits 1 and 3 landed. Recording every edit's outcome turns that guess into a
// fact, which is what makes a failed batch safe to re-issue.
type editAttempt struct {
	index       int
	status      string
	how         string
	occurrences int
	lines       []int
}

// note renders the line a successful edit contributes to the success report.
func (a editAttempt) note() string {
	switch {
	case a.occurrences > 1:
		return fmt.Sprintf("  edit %d: %d occurrences on lines %s, %s",
			a.index, a.occurrences, joinInts(a.lines), a.how)
	case len(a.lines) == 1:
		return fmt.Sprintf("  edit %d: line %d, %s", a.index, a.lines[0], a.how)
	default:
		return fmt.Sprintf("  edit %d: %s", a.index, a.how)
	}
}

// describe renders one row of the per-edit status list in a failure report.
func (a editAttempt) describe() string {
	switch a.status {
	case matchStatusMatched:
		switch {
		case a.occurrences > 1:
			return fmt.Sprintf("matched %d occurrences on lines %s, %s (safe to re-issue unchanged)",
				a.occurrences, joinInts(a.lines), a.how)
		case len(a.lines) == 1:
			return fmt.Sprintf("matched at line %d, %s (safe to re-issue unchanged)", a.lines[0], a.how)
		default:
			return "matched (safe to re-issue unchanged)"
		}
	case matchStatusFailed:
		return "FAILED, described below"
	default:
		return "not attempted, because an earlier edit failed first"
	}
}

// renderEditFailure states the outcome for the file and for every edit in the
// batch, then hands over to the diagnostic for the edit that actually failed.
func renderEditFailure(total int, attempts []editAttempt, cause error) error {
	failing := 0
	for _, attempt := range attempts {
		if attempt.status == matchStatusFailed {
			failing = attempt.index
		}
	}

	head, tail := splitFirstLine(cause.Error())
	head = strings.TrimPrefix(head, fmt.Sprintf("edit %d: ", failing))

	var message strings.Builder
	if failing > 0 && total > 1 {
		fmt.Fprintf(&message, "edit %d of %d failed: %s", failing, total, head)
	} else {
		message.WriteString(head)
	}
	message.WriteString("\n  file unchanged: no edits were applied, because edit is all or nothing")

	if total > 1 {
		message.WriteString("\n  edit status:")
		for _, attempt := range attempts {
			fmt.Fprintf(&message, "\n    %d. %s", attempt.index, attempt.describe())
		}
	}
	if tail != "" {
		message.WriteString("\n")
		message.WriteString(tail)
	}
	return errors.New(message.String())
}

// splitFirstLine separates a message's headline from its detail.
func splitFirstLine(text string) (string, string) {
	if index := strings.Index(text, "\n"); index >= 0 {
		return text[:index], text[index+1:]
	}
	return text, ""
}

// joinInts renders line numbers for a message.
func joinInts(numbers []int) string {
	rendered := make([]string, 0, len(numbers))
	for _, number := range numbers {
		rendered = append(rendered, strconv.Itoa(number))
	}
	return strings.Join(rendered, ", ")
}

// lineNumbersAt maps byte offsets to 1-based line numbers.
func lineNumbersAt(content string, starts []int) []int {
	numbers := make([]int, 0, len(starts))
	for _, start := range starts {
		numbers = append(numbers, lineNumberAt(content, start))
	}
	return numbers
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
	if looksLikeReadFooter(needle) {
		message.WriteString("\n  note: oldText contains read's closing summary, such as [lines 1-40 of 120; end of file]. That line reports on the read and is not in the file, so remove it from oldText.")
	}
	message.WriteString("\n  tip: pass dryRun to test an edit without writing")

	return errors.New(message.String())
}

// joinLineNumbers renders the 1-based line number of each offset.
func joinLineNumbers(content string, starts []int) string {
	return joinInts(lineNumbersAt(content, starts))
}

// looksLikeReadFooter spots the summary line that read appends to its output.
// That line describes the read, so pasting it back as oldText can never match.
func looksLikeReadFooter(needle string) bool {
	if !strings.Contains(needle, "[lines ") {
		return false
	}
	return strings.Contains(needle, "; end of file]") ||
		strings.Contains(needle, "not shown, continue with offset")
}

// editAmbiguousError lists where the duplicate matches are, so the caller can
// extend oldText instead of guessing.
func editAmbiguousError(number int, content string, starts []int) error {
	return fmt.Errorf(
		"edit %d: oldText matches %d times (lines %s), must be unique; add surrounding context to oldText, or set replaceAll or expectedOccurrences",
		number, len(starts), joinLineNumbers(content, starts))
}

// replaceSpans rewrites every span, after checking the match count against
// what the caller said to expect.
func replaceSpans(content string, spans [][2]int, edit EditBlock, number int, how string) (string, editAttempt, error) {
	starts := make([]int, 0, len(spans))
	for _, span := range spans {
		starts = append(starts, span[0])
	}
	attempt := editAttempt{
		index:       number,
		status:      matchStatusFailed,
		how:         how,
		occurrences: len(spans),
		lines:       lineNumbersAt(content, starts),
	}

	switch {
	case edit.ExpectedOccurrences > 0 && len(spans) != edit.ExpectedOccurrences:
		return "", attempt, fmt.Errorf(
			"edit %d: oldText matches %d times (lines %s), expectedOccurrences is %d",
			number, len(spans), joinLineNumbers(content, starts), edit.ExpectedOccurrences)
	case edit.ExpectedOccurrences == 0 && !edit.ReplaceAll && len(spans) > 1:
		return "", attempt, editAmbiguousError(number, content, starts)
	}

	// Rewrite from the end so the earlier offsets stay valid.
	updated := content
	for i := len(spans) - 1; i >= 0; i-- {
		updated = updated[:spans[i][0]] + edit.NewText + updated[spans[i][1]:]
	}

	attempt.status = matchStatusMatched
	return updated, attempt, nil
}

// applyEdit replaces oldText, falling back to a whitespace tolerant line match
// when the exact text is absent.
func applyEdit(content string, edit EditBlock, number int, path, original string, allowTolerant bool) (string, editAttempt, error) {
	failed := editAttempt{index: number, status: matchStatusFailed}

	if edit.OldText == "" {
		return "", failed, fmt.Errorf("edit %d: oldText must not be empty", number)
	}
	if edit.ExpectedOccurrences < 0 {
		return "", failed, fmt.Errorf("edit %d: expectedOccurrences must not be negative", number)
	}

	if starts := findOccurrences(content, edit.OldText); len(starts) > 0 {
		spans := make([][2]int, 0, len(starts))
		for _, start := range starts {
			spans = append(spans, [2]int{start, start + len(edit.OldText)})
		}
		return replaceSpans(content, spans, edit, number, "exact match")
	}

	if allowTolerant {
		if blocks := findLineBlocks(content, edit.OldText); len(blocks) > 0 {
			return replaceSpans(content, blocks, edit, number, "matched ignoring trailing whitespace")
		}
	}

	return "", failed, editNotFoundError(number, path, original, content, edit.OldText)
}
