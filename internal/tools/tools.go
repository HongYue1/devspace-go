package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snakex21/devspace-go/internal/shells"
)

const (
	defaultReadLimitLines = 2000
	maxReadLimitLines     = 5000
	maxToolOutputBytes    = 256 * 1024
	maxSearchFileBytes    = 2 * 1024 * 1024
	maxSearchMatches      = 500
)

// skippedDirs are folders whose contents are never the source someone is
// looking for: version-control metadata, fetched dependencies, and this
// server's own state.
//
// "dist" and "build" used to be listed here as well. Plenty of projects keep
// real, hand-written source in a folder by one of those names, and a search
// that silently cannot see a folder that exists is worse than one that walks
// some generated output.
var skippedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true,
	".next":        true, ".turbo": true, ".cache": true,
	".devspace": true, ".devspace-state": true, ".webcoder": true, ".webcoder-state": true,
}

var configuredShell = "auto"

// SetShell records the shell preference for the bash tool. It accepts "auto",
// any id reported by the shells package, or an absolute path to an interpreter.
//
// The value is no longer lowercased: an absolute path on a case sensitive
// filesystem has to survive intact. Resolving here rather than on first use
// means a misconfigured shell is visible in startup output instead of
// surfacing as a confusing failure on some later command.
func SetShell(shell string) {
	configuredShell = strings.TrimSpace(shell)
	if configuredShell == "" {
		configuredShell = "auto"
	}
	shells.Reset()
	setSelection(computeSelection(configuredShell))
}

// maxBatchReadFiles caps how many files one read call may fetch, so a batch
// read cannot quietly turn into a whole-repository dump.
const maxBatchReadFiles = 20

// ReadInput represents the input for the read tool.
type ReadInput struct {
	WorkspaceID  string   `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path         string   `json:"path,omitempty" jsonschema:"File path to read, relative to the workspace root. Give either path or paths."`
	Paths        []string `json:"paths,omitempty" jsonschema:"Several file paths to read in one call, relative to the workspace root, at most 20. Every requested file gets its own entry in files[] with its own metadata, and a file that cannot be read reports the reason there instead of failing the whole call. offset and limit apply to each file separately."`
	Offset       int      `json:"offset,omitempty" jsonschema:"1-indexed line number to start reading from. Defaults to 1. An offset past the end of the file is not an error: the response says so and still reports the true line count."`
	Limit        int      `json:"limit,omitempty" jsonschema:"Maximum number of lines to read. Defaults to 2000, capped at 5000. Check isTruncated and nextLine to find out whether you saw the whole file."`
	MetadataOnly bool     `json:"metadataOnly,omitempty" jsonschema:"Return totalLines, bytes, sha256, modifiedAt and lineEnding with no content at all. Use it to refresh a precondition for write or edit, or to check whether a file changed under you, without spending context on the body."`
}

// FileRead is one file's worth of read result, including enough metadata to
// tell a partial read from a complete one and to guard the next write.
type FileRead struct {
	Path          string `json:"path" jsonschema:"Path that was read, relative to the workspace root."`
	Content       string `json:"content" jsonschema:"The lines that were read, joined with newlines, and nothing else. Empty when metadataOnly was set, when the file is empty, or when the offset is past the end of the file."`
	StartLine     int    `json:"startLine" jsonschema:"1-indexed line number of the first line returned. Zero when no content was returned."`
	EndLine       int    `json:"endLine" jsonschema:"1-indexed line number of the last line returned. Zero when no content was returned."`
	ReturnedLines int    `json:"returnedLines" jsonschema:"How many lines of content this entry carries."`
	TotalLines    int    `json:"totalLines" jsonschema:"How many lines the whole file has. Always the true count, even when only part of the file was returned."`
	NextLine      int    `json:"nextLine" jsonschema:"Offset that would read the next page. Zero once the end of the file has been reached."`
	IsTruncated   bool   `json:"isTruncated" jsonschema:"Whether lines remain after endLine. False means you have seen the file through to its end."`
	PastEndOfFile bool   `json:"pastEndOfFile" jsonschema:"Whether the requested offset is past the last line of the file. totalLines still reports the real length."`
	Bytes         int64  `json:"bytes" jsonschema:"Size of the whole file on disk."`
	Sha256        string `json:"sha256" jsonschema:"SHA-256 of the whole file as stored on disk, even when only part of it was returned. Pass it back as expectedSha256 on write or edit to be told if the file changed in the meantime."`
	ModifiedAt    string `json:"modifiedAt" jsonschema:"Modification time of the file, RFC 3339 in UTC. Usable as expectedModifiedAt."`
	LineEnding    string `json:"lineEnding" jsonschema:"Line ending found in the file: LF, CRLF, mixed or none."`
	Error         string `json:"error,omitempty" jsonschema:"Why this file could not be read. Only used for a paths batch; a single path failure is returned as a tool error instead."`
}

// ReadOutput represents the output for the read tool.
type ReadOutput struct {
	Result string     `json:"result" jsonschema:"The rendered report: the file content plus a closing line saying which lines of how many were returned, or one section per file for a batch read."`
	Files  []FileRead `json:"files" jsonschema:"One entry per requested path, in the order requested. Always populated, including for a single path read."`

	// The fields below describe the single requested file, so the common case
	// needs no indexing. They are zero when paths was used.
	Path          string `json:"path,omitempty" jsonschema:"Path that was read. Empty when paths was used; see files[]."`
	StartLine     int    `json:"startLine" jsonschema:"First line returned for a single path read. Zero when paths was used; see files[]."`
	EndLine       int    `json:"endLine" jsonschema:"Last line returned for a single path read. Zero when paths was used; see files[]."`
	ReturnedLines int    `json:"returnedLines" jsonschema:"Lines returned for a single path read. Zero when paths was used; see files[]."`
	TotalLines    int    `json:"totalLines" jsonschema:"True line count for a single path read. Zero when paths was used; see files[]."`
	NextLine      int    `json:"nextLine" jsonschema:"Offset that reads the next page for a single path read. Zero at end of file."`
	IsTruncated   bool   `json:"isTruncated" jsonschema:"Whether a single path read left lines unread. False means the file was seen to its end."`
	Bytes         int64  `json:"bytes" jsonschema:"File size for a single path read."`
	Sha256        string `json:"sha256,omitempty" jsonschema:"SHA-256 for a single path read, ready to use as expectedSha256."`
	ModifiedAt    string `json:"modifiedAt,omitempty" jsonschema:"Modification time for a single path read."`
	LineEnding    string `json:"lineEnding,omitempty" jsonschema:"Line ending for a single path read."`
}

// ReadFile reads one file, or a batch of them, and reports exactly how much of
// each one came back.
func ReadFile(ctx context.Context, req *mcp.CallToolRequest, input ReadInput, wsRoot string) (*mcp.CallToolResult, ReadOutput, error) {
	requested, single, err := readTargets(input)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, ReadOutput{}, nil
	}

	out := ReadOutput{}
	for _, relative := range requested {
		absPath := filepath.Clean(filepath.Join(wsRoot, relative))
		out.Files = append(out.Files, readOneFile(absPath, relative, input.Offset, input.Limit, input.MetadataOnly))
	}

	// A single path keeps the original contract: the failure is a tool error,
	// not a row in a report that a caller might skim past.
	if single && out.Files[0].Error != "" {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("%s", out.Files[0].Error))
		return result, ReadOutput{}, nil
	}

	if single {
		file := out.Files[0]
		out.Path = file.Path
		out.StartLine = file.StartLine
		out.EndLine = file.EndLine
		out.ReturnedLines = file.ReturnedLines
		out.TotalLines = file.TotalLines
		out.NextLine = file.NextLine
		out.IsTruncated = file.IsTruncated
		out.Bytes = file.Bytes
		out.Sha256 = file.Sha256
		out.ModifiedAt = file.ModifiedAt
		out.LineEnding = file.LineEnding
	}
	out.Result = renderRead(out.Files, single, input.MetadataOnly)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: out.Result},
		},
	}, out, nil
}

// readTargets works out which files were asked for, and whether this is the
// single file form whose error handling differs.
func readTargets(input ReadInput) ([]string, bool, error) {
	var paths []string
	if strings.TrimSpace(input.Path) != "" {
		paths = append(paths, input.Path)
	}
	for _, path := range input.Paths {
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}

	switch {
	case len(paths) == 0:
		return nil, false, fmt.Errorf("read needs path, or a paths array with at least one entry")
	case len(paths) > maxBatchReadFiles:
		return nil, false, fmt.Errorf("read accepts at most %d paths at once, got %d", maxBatchReadFiles, len(paths))
	}
	return paths, len(input.Paths) == 0, nil
}

// readOneFile reads one file and describes how much of it came back.
//
// The scan deliberately runs to the end of the file even after the limit is
// full: totalLines has to be the real count, because a caller that cannot see
// how much it is missing will assume it is missing nothing.
func readOneFile(absPath, displayPath string, offset, rawLimit int, metadataOnly bool) FileRead {
	out := FileRead{Path: normalizeRelativePath(displayPath)}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			out.Error = fmt.Sprintf("file not found: %s", displayPath)
		} else {
			out.Error = fmt.Sprintf("failed to stat file: %v", err)
		}
		return out
	}
	if info.IsDir() {
		out.Error = fmt.Sprintf("path is a directory, not a file: %s", displayPath)
		return out
	}

	out.Bytes = info.Size()
	out.ModifiedAt = fingerprintTime(info.ModTime())
	out.LineEnding = sniffLineEnding(absPath)
	if sum, hashErr := hashFile(absPath); hashErr == nil {
		out.Sha256 = sum
	}

	// The size guard only applies to an unbounded request for content. Asking
	// for one page of a huge file, or for metadata alone, is always allowed.
	if !metadataOnly && rawLimit == 0 && info.Size() > maxSearchFileBytes {
		out.Error = fmt.Sprintf("file is too large (%d bytes). Read with offset/limit or inspect with shell", info.Size())
		return out
	}

	if offset <= 0 {
		offset = 1
	}
	limit := rawLimit
	if limit <= 0 {
		limit = defaultReadLimitLines
	}
	if limit > maxReadLimitLines {
		limit = maxReadLimitLines
	}

	file, err := os.Open(absPath)
	if err != nil {
		out.Error = fmt.Sprintf("failed to open file: %v", err)
		return out
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lines []string
	total := 0
	for scanner.Scan() {
		total++
		if total < offset || metadataOnly || len(lines) >= limit {
			continue
		}
		lines = append(lines, scanner.Text())
	}
	if scanErr := scanner.Err(); scanErr != nil {
		out.Error = fmt.Sprintf("failed to read file: %v", scanErr)
		return out
	}

	out.TotalLines = total
	if metadataOnly {
		return out
	}

	out.ReturnedLines = len(lines)
	if total == 0 {
		return out
	}
	if offset > total {
		out.StartLine = offset
		out.PastEndOfFile = true
		return out
	}

	out.StartLine = offset
	out.EndLine = offset + len(lines) - 1
	out.Content = strings.Join(lines, "\n")
	out.IsTruncated = out.EndLine < total
	if out.IsTruncated {
		out.NextLine = out.EndLine + 1
	}
	return out
}

// sniffLineEnding reports the line ending style of a file from its first
// 64 KiB, which is enough to tell LF from CRLF and to notice a mix of both.
func sniffLineEnding(absPath string) string {
	file, err := os.Open(absPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	buffer := make([]byte, 64*1024)
	read, err := file.Read(buffer)
	if read <= 0 || (err != nil && err != io.EOF) {
		return "none"
	}
	sample := string(buffer[:read])

	feeds := strings.Count(sample, "\n")
	pairs := strings.Count(sample, "\r\n")
	switch {
	case feeds == 0:
		return "none"
	case pairs == 0:
		return "LF"
	case pairs == feeds:
		return "CRLF"
	default:
		return "mixed"
	}
}

// renderRead lays the result out as text. A single file is still returned as
// its own content, followed by one line saying how much of it that was.
func renderRead(files []FileRead, single, metadataOnly bool) string {
	if single {
		file := files[0]
		switch {
		case metadataOnly:
			return describeFileMetadata(file)
		case file.PastEndOfFile:
			return fmt.Sprintf("[offset %d is past the end of %s, which has %d line(s)]",
				file.StartLine, file.Path, file.TotalLines)
		case file.TotalLines == 0:
			return fmt.Sprintf("[%s is empty: 0 lines, %d bytes]", file.Path, file.Bytes)
		}
		return truncateOutput(file.Content + "\n\n" + readFooter(file))
	}

	var report strings.Builder
	for index, file := range files {
		if index > 0 {
			report.WriteString("\n\n")
		}
		switch {
		case file.Error != "":
			fmt.Fprintf(&report, "=== %s === error: %s", file.Path, file.Error)
		case metadataOnly:
			report.WriteString(describeFileMetadata(file))
		case file.PastEndOfFile:
			fmt.Fprintf(&report, "=== %s === offset %d is past the end of the file, which has %d line(s)",
				file.Path, file.StartLine, file.TotalLines)
		case file.TotalLines == 0:
			fmt.Fprintf(&report, "=== %s === empty file, 0 lines", file.Path)
		default:
			fmt.Fprintf(&report, "=== %s === %s\n%s", file.Path, readFooter(file), file.Content)
		}
	}
	return truncateOutput(report.String())
}

// readFooter is the line that says whether the whole file was seen, so a
// partial read can never be mistaken for a complete one.
func readFooter(file FileRead) string {
	if file.IsTruncated {
		return fmt.Sprintf("[lines %d-%d of %d; %d line(s) not shown, continue with offset %d]",
			file.StartLine, file.EndLine, file.TotalLines, file.TotalLines-file.EndLine, file.NextLine)
	}
	return fmt.Sprintf("[lines %d-%d of %d; end of file]", file.StartLine, file.EndLine, file.TotalLines)
}

// describeFileMetadata renders a metadataOnly entry.
func describeFileMetadata(file FileRead) string {
	if file.Error != "" {
		return fmt.Sprintf("%s: %s", file.Path, file.Error)
	}
	return fmt.Sprintf("%s: %d line(s), %s, %s line endings, modified %s, sha256 %s",
		file.Path, file.TotalLines, formatSize(file.Bytes), file.LineEnding, file.ModifiedAt, shortSha(file.Sha256))
}

// WriteInput represents the input for the write tool.
type WriteInput struct {
	WorkspaceID        string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path               string `json:"path" jsonschema:"File path to write, relative to the workspace root."`
	Content            string `json:"content" jsonschema:"Complete new file content."`
	ExpectedSha256     string `json:"expectedSha256,omitempty" jsonschema:"Optional precondition: the sha256 this file had when you last read it, as returned by read, write or edit. If the file no longer matches, nothing is written and the error says the file changed rather than blaming your content. Omit it to overwrite unconditionally. Pass the empty-file sentinel from a read that reported the file missing to require that it still does not exist."`
	ExpectedModifiedAt string `json:"expectedModifiedAt,omitempty" jsonschema:"Optional precondition: the modifiedAt this file had when you last read it, RFC 3339. Checked in addition to expectedSha256 when both are given. Prefer expectedSha256, which does not depend on filesystem timestamp resolution."`
}

// WriteOutput represents the output for the write tool.
type WriteOutput struct {
	Result     string `json:"result" jsonschema:"Write result message."`
	Path       string `json:"path" jsonschema:"Path that was written, relative to the workspace root."`
	Created    bool   `json:"created" jsonschema:"True when the file did not exist before this call, false when an existing file was overwritten."`
	Bytes      int64  `json:"bytes" jsonschema:"Size on disk after the write, verified by reading the file back."`
	Sha256     string `json:"sha256" jsonschema:"SHA-256 of the file as now stored on disk. Pass it as expectedSha256 on your next write or edit of this file."`
	ModifiedAt string `json:"modifiedAt" jsonschema:"Modification time after the write, RFC 3339 in UTC."`
	LineEnding string `json:"lineEnding" jsonschema:"Line ending the file now uses: LF, CRLF, mixed or none."`
}

// MkdirInput represents a directory creation operation.
type MkdirInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"Directory path to create, relative to the workspace root."`
}

// MkdirOutput represents the output for mkdir.
type MkdirOutput struct {
	Result string `json:"result" jsonschema:"Directory creation result message."`
}

// MoveInput represents a file or directory move/rename operation.
type MoveInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	SourcePath  string `json:"sourcePath" jsonschema:"Existing file or directory path, relative to the workspace root."`
	TargetPath  string `json:"targetPath" jsonschema:"Destination file or directory path, relative to the workspace root."`
	Overwrite   bool   `json:"overwrite,omitempty" jsonschema:"Overwrite destination if it already exists. Default false."`
}

// MoveOutput represents the output for move.
type MoveOutput struct {
	Result string `json:"result" jsonschema:"Move result message."`
}

// WriteFile creates or overwrites a file.
func WriteFile(ctx context.Context, req *mcp.CallToolRequest, input WriteInput, wsRoot string) (*mcp.CallToolResult, WriteOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)

	// The precondition is checked before any directory is created, so a refused
	// write leaves the filesystem exactly as it found it.
	want := precondition{Sha256: input.ExpectedSha256, ModifiedAt: input.ExpectedModifiedAt}
	if err := want.check(absPath, input.Path); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, WriteOutput{}, nil
	}

	existed := false
	if info, statErr := os.Stat(absPath); statErr == nil && !info.IsDir() {
		existed = true
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to create directory: %v", err))
		return result, WriteOutput{}, nil
	}

	content, keptCRLF := matchExistingLineEnding(absPath, input.Content)

	written, err := writeFileAtomic(absPath, []byte(content), existingFileMode(absPath))
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, WriteOutput{}, nil
	}

	detail := "overwrote existing file"
	if !existed {
		detail = "created"
	}
	recordChange(ChangeWrite, wsRoot, input.Path, written, detail)

	state, _ := statFile(absPath)
	out := WriteOutput{
		Path:       normalizeRelativePath(input.Path),
		Created:    !existed,
		Bytes:      written,
		Sha256:     state.Sha256,
		ModifiedAt: state.ModifiedAt,
		LineEnding: sniffLineEnding(absPath),
	}

	result := fmt.Sprintf("Wrote %s (%d bytes verified on disk).", input.Path, written)
	if keptCRLF {
		result = fmt.Sprintf("Wrote %s (%d bytes verified on disk, CRLF line endings kept).", input.Path, written)
	}
	if !existed {
		result += " The file did not exist before this call."
	}
	result += fmt.Sprintf("\nsha256 %s — pass it as expectedSha256 next time to be told if anything else touches this file first.", shortSha(state.Sha256))
	out.Result = result

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, out, nil
}

// MakeDirectory creates a directory and all missing parents inside a workspace.
func MakeDirectory(ctx context.Context, req *mcp.CallToolRequest, input MkdirInput, wsRoot string) (*mcp.CallToolResult, MkdirOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)

	if err := os.MkdirAll(absPath, 0755); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to create directory: %v", err))
		return result, MkdirOutput{}, nil
	}

	recordChange(ChangeMkdir, wsRoot, input.Path, 0, "")

	result := fmt.Sprintf("Created directory %s.", input.Path)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result}},
	}, MkdirOutput{Result: result}, nil
}

// MovePath moves or renames a file/directory inside a workspace.
func MovePath(ctx context.Context, req *mcp.CallToolRequest, input MoveInput, wsRoot string) (*mcp.CallToolResult, MoveOutput, error) {
	sourceAbs := filepath.Clean(filepath.Join(wsRoot, input.SourcePath))
	targetAbs := filepath.Clean(filepath.Join(wsRoot, input.TargetPath))

	if _, err := os.Stat(sourceAbs); err != nil {
		result := &mcp.CallToolResult{}
		if os.IsNotExist(err) {
			result.SetError(fmt.Errorf("source not found: %s", input.SourcePath))
		} else {
			result.SetError(fmt.Errorf("failed to stat source: %v", err))
		}
		return result, MoveOutput{}, nil
	}

	if _, err := os.Stat(targetAbs); err == nil {
		if !input.Overwrite {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("target already exists: %s", input.TargetPath))
			return result, MoveOutput{}, nil
		}
		if err := os.RemoveAll(targetAbs); err != nil {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("failed to remove existing target: %v", err))
			return result, MoveOutput{}, nil
		}
	} else if !os.IsNotExist(err) {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to stat target: %v", err))
		return result, MoveOutput{}, nil
	}

	if err := os.MkdirAll(filepath.Dir(targetAbs), 0755); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to create target parent directory: %v", err))
		return result, MoveOutput{}, nil
	}

	if err := os.Rename(sourceAbs, targetAbs); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to move path: %v", err))
		return result, MoveOutput{}, nil
	}

	movedBytes := int64(0)
	if info, statErr := os.Stat(targetAbs); statErr == nil && !info.IsDir() {
		movedBytes = info.Size()
	}
	recordMove(wsRoot, input.SourcePath, input.TargetPath, movedBytes)

	result := fmt.Sprintf("Moved %s to %s.", input.SourcePath, input.TargetPath)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result}},
	}, MoveOutput{Result: result}, nil
}

// EditInput represents an edit operation.
type EditInput struct {
	WorkspaceID        string      `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path               string      `json:"path" jsonschema:"File path to edit, relative to the workspace root."`
	Edits              []EditBlock `json:"edits" jsonschema:"Array of edit operations. Applied all or nothing: if any one of them fails to match, the file is left untouched and the error reports the status of every edit in the array."`
	DryRun             bool        `json:"dryRun,omitempty" jsonschema:"Report where each edit would land without writing the file."`
	ExpectedSha256     string      `json:"expectedSha256,omitempty" jsonschema:"Optional precondition: the sha256 this file had when you last read it, as returned by read, write or edit. If the file no longer matches, nothing is written and the error says the file changed under you, which is a different problem from oldText not matching. Omit it to edit whatever is on disk now."`
	ExpectedModifiedAt string      `json:"expectedModifiedAt,omitempty" jsonschema:"Optional precondition: the modifiedAt this file had when you last read it, RFC 3339. Checked in addition to expectedSha256 when both are given. Prefer expectedSha256, which does not depend on filesystem timestamp resolution."`
}

// EditBlock represents a single find-and-replace operation.
type EditBlock struct {
	OldText             string `json:"oldText" jsonschema:"Text to replace. Must match uniquely unless replaceAll or expectedOccurrences is set. Line endings and trailing whitespace are ignored if the exact text is not found."`
	NewText             string `json:"newText" jsonschema:"Replacement text."`
	ReplaceAll          bool   `json:"replaceAll,omitempty" jsonschema:"Replace every occurrence instead of requiring a unique match."`
	ExpectedOccurrences int    `json:"expectedOccurrences,omitempty" jsonschema:"Replace exactly this many occurrences and fail if the file contains a different number."`
}

// EditOutput represents the output for the edit tool.
type EditOutput struct {
	Status     string      `json:"status" jsonschema:"applied, dry_run, or failed."`
	Result     string      `json:"result" jsonschema:"Edit result message."`
	Path       string      `json:"path" jsonschema:"Path that was edited, relative to the workspace root."`
	Applied    bool        `json:"applied" jsonschema:"Whether the file on disk changed. False for a dry run and for every failure: edit is all or nothing, so a failed call never leaves part of the batch applied."`
	Edits      []EditMatch `json:"edits" jsonschema:"One entry per requested edit, in order, saying whether it matched, failed, or was never attempted because an earlier edit failed first."`
	Bytes      int64       `json:"bytes" jsonschema:"Size on disk after the edit, or the size the file would have had for a dry run."`
	Sha256     string      `json:"sha256" jsonschema:"SHA-256 of the file as now stored on disk. Pass it as expectedSha256 on your next edit of this file. Empty for a dry run or a failure."`
	ModifiedAt string      `json:"modifiedAt" jsonschema:"Modification time after the edit, RFC 3339 in UTC. Empty for a dry run or a failure."`
}

// EditMatch reports what happened to one requested edit.
type EditMatch struct {
	Index       int    `json:"index" jsonschema:"1-based position of this edit in the edits array."`
	Status      string `json:"status" jsonschema:"matched, failed, or not_attempted."`
	How         string `json:"how,omitempty" jsonschema:"How it matched: exact match, or matched ignoring trailing whitespace."`
	Occurrences int    `json:"occurrences,omitempty" jsonschema:"How many occurrences of oldText this edit matched."`
	Lines       []int  `json:"lines,omitempty" jsonschema:"1-based line numbers where this edit matched."`
}

// EditFile performs find-and-replace edits on a file.
func EditFile(ctx context.Context, req *mcp.CallToolRequest, input EditInput, wsRoot string) (*mcp.CallToolResult, EditOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)
	absPath = filepath.Clean(absPath)
	displayPath := normalizeRelativePath(input.Path)

	// Checked before the file is even read, so that a file which moved under the
	// caller is reported as exactly that rather than as a text mismatch.
	want := precondition{Sha256: input.ExpectedSha256, ModifiedAt: input.ExpectedModifiedAt}
	if err := want.check(absPath, input.Path); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, EditOutput{Status: "failed", Result: err.Error(), Path: displayPath}, nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		result := &mcp.CallToolResult{}
		if os.IsNotExist(err) {
			result.SetError(fmt.Errorf("file not found: %s", input.Path))
		} else {
			result.SetError(fmt.Errorf("failed to read file: %v", err))
		}
		return result, EditOutput{}, nil
	}

	if len(input.Edits) == 0 {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("edits must not be empty"))
		return result, EditOutput{}, nil
	}

	original := string(data)

	// A file that mixes CRLF and LF is matched byte for byte, so that lines the
	// caller did not touch keep the ending they already had.
	mixed := hasMixedLineEndings(original)
	ending := detectLineEnding(original)

	content := original
	if !mixed {
		content = normalizeNewlines(original)
	}
	before := content

	attempts := make([]editAttempt, 0, len(input.Edits))
	notes := make([]string, 0, len(input.Edits))
	for i, edit := range input.Edits {
		updated, attempt, editErr := applyEdit(content, edit, i+1, input.Path, original, !mixed)
		attempts = append(attempts, attempt)
		if editErr != nil {
			// Every later edit is reported as untried, so the caller can tell
			// "this one is wrong" from "this one was never looked at".
			for j := i + 1; j < len(input.Edits); j++ {
				attempts = append(attempts, editAttempt{index: j + 1, status: matchStatusNotAttempted})
			}
			failure := renderEditFailure(len(input.Edits), attempts, editErr)
			result := &mcp.CallToolResult{}
			result.SetError(failure)
			return result, EditOutput{
				Status:  "failed",
				Result:  failure.Error(),
				Path:    displayPath,
				Applied: false,
				Edits:   editMatches(attempts),
			}, nil
		}
		content = updated
		notes = append(notes, attempt.note())
	}

	if content == before {
		message := fmt.Sprintf("no changes made to file: all %d edit(s) matched %s, but newText is identical to the text it replaced, so the file was left untouched",
			len(input.Edits), displayPath)
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("%s", message))
		return result, EditOutput{
			Status:  "failed",
			Result:  message,
			Path:    displayPath,
			Applied: false,
			Edits:   editMatches(attempts),
		}, nil
	}

	output := content
	if !mixed {
		output = restoreLineEndings(content, ending)
	}
	detail := strings.Join(notes, "\n")

	if input.DryRun {
		message := fmt.Sprintf("Dry run for %s: %d edit(s) would apply, %d bytes.\n%s",
			input.Path, len(input.Edits), len(output), detail)
		message += "\nNothing was written. Re-issue the same call without dryRun to apply it."
		return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: message},
				},
			}, EditOutput{
				Status:  "dry_run",
				Result:  message,
				Path:    displayPath,
				Applied: false,
				Edits:   editMatches(attempts),
				Bytes:   int64(len(output)),
			}, nil
	}

	written, err := writeFileAtomic(absPath, []byte(output), existingFileMode(absPath))
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(err)
		return result, EditOutput{Status: "failed", Path: displayPath, Edits: editMatches(attempts)}, nil
	}

	recordChange(ChangeEdit, wsRoot, input.Path, written, fmt.Sprintf("%d edit(s)", len(input.Edits)))
	state, _ := statFile(absPath)

	message := fmt.Sprintf("Edited %s: %d edit(s) applied, %d bytes verified on disk.\n%s",
		input.Path, len(input.Edits), written, detail)
	message += fmt.Sprintf("\nsha256 %s - pass it as expectedSha256 next time to be told if anything else touches this file first.", shortSha(state.Sha256))
	return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: message},
			},
		}, EditOutput{
			Status:     "applied",
			Result:     message,
			Path:       displayPath,
			Applied:    true,
			Edits:      editMatches(attempts),
			Bytes:      written,
			Sha256:     state.Sha256,
			ModifiedAt: state.ModifiedAt,
		}, nil
}

// editMatches exposes the per-edit outcomes in the tool's output schema.
func editMatches(attempts []editAttempt) []EditMatch {
	matches := make([]EditMatch, 0, len(attempts))
	for _, attempt := range attempts {
		matches = append(matches, EditMatch{
			Index:       attempt.index,
			Status:      attempt.status,
			How:         attempt.how,
			Occurrences: attempt.occurrences,
			Lines:       attempt.lines,
		})
	}
	return matches
}

// maxGrepContextLines caps how many lines of context one match may carry, so
// a wide context cannot turn a small search into a whole-file dump.
const maxGrepContextLines = 10

// contextLine is a line held back as possible leading context for a match that
// has not happened yet.
type contextLine struct {
	number int
	text   string
}

// GrepInput represents the input for the grep tool.
type GrepInput struct {
	WorkspaceID     string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Pattern         string `json:"pattern" jsonschema:"Search pattern, as a Go regular expression."`
	Path            string `json:"path,omitempty" jsonschema:"Optional path scope relative to the workspace root."`
	Include         string `json:"include,omitempty" jsonschema:"Optional include glob matched against the file name."`
	CaseInsensitive bool   `json:"caseInsensitive,omitempty" jsonschema:"Match without regard to case."`
	ContextLines    int    `json:"contextLines,omitempty" jsonschema:"Lines of context to show before and after each match, up to 10. Context lines are prefixed path-line- and matches path:line:, as in grep."`
	MaxMatches      int    `json:"maxMatches,omitempty" jsonschema:"Stop after this many matches. Defaults to 500, which is also the maximum."`
}

// GrepOutput represents the output for the grep tool.
type GrepOutput struct {
	Result        string `json:"result" jsonschema:"Grep results, or an explanation of why nothing matched."`
	Matches       int    `json:"matches" jsonschema:"Number of matching lines found."`
	FilesSearched int    `json:"filesSearched" jsonschema:"How many files were actually opened and scanned. Zero matches out of zero files searched says nothing about the pattern: the path, the include glob or the skip list excluded everything."`
	FilesMatched  int    `json:"filesMatched" jsonschema:"How many distinct files contained at least one match."`
	FilesSkipped  int    `json:"filesSkipped" jsonschema:"How many files were skipped for being too large or binary. Version control, dependency and build directories are skipped without being counted here."`
	SearchRoot    string `json:"searchRoot" jsonschema:"The path the search actually walked, relative to the workspace root."`
	Truncated     bool   `json:"truncated" jsonschema:"Whether the search stopped at maxMatches, meaning more matches may exist."`
}

// GrepFiles searches file contents for a pattern.
func GrepFiles(ctx context.Context, req *mcp.CallToolRequest, input GrepInput, wsRoot string) (*mcp.CallToolResult, GrepOutput, error) {
	searchPath := wsRoot
	if input.Path != "" {
		searchPath = filepath.Join(wsRoot, input.Path)
	}

	pattern := input.Pattern
	if input.CaseInsensitive {
		pattern = "(?i)" + pattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("invalid regex pattern: %v", err))
		return result, GrepOutput{}, nil
	}

	contextLines := input.ContextLines
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > maxGrepContextLines {
		contextLines = maxGrepContextLines
	}

	matchCap := maxSearchMatches
	if input.MaxMatches > 0 && input.MaxMatches < matchCap {
		matchCap = input.MaxMatches
	}

	include := input.Include
	var lines []string
	matches := 0
	skipped := 0
	filesSearched := 0
	filesWithMatches := 0
	start := time.Now()

	// WalkDir avoids one stat syscall per entry, which matters on large trees,
	// and shares the walk budget with glob instead of keeping its own copy.
	_ = filepath.WalkDir(searchPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > searchWalkTimeout || matches >= matchCap {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if skippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if include != "" {
			ok, _ := filepath.Match(include, entry.Name())
			if !ok {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxSearchFileBytes || looksBinaryPath(path) {
			skipped++
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		filesSearched++

		rel, _ := filepath.Rel(wsRoot, path)
		display := filepath.ToSlash(rel)

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		// before holds candidate leading context, trailing counts the context
		// lines still owed after a match, and lastPrinted keeps one line from
		// being emitted twice when two matches sit close together.
		var before []contextLine
		trailing := 0
		lastPrinted := 0
		lineNo := 0
		fileHadMatch := false

		for scanner.Scan() {
			lineNo++
			text := scanner.Text()

			switch {
			case re.MatchString(text):
				if !fileHadMatch {
					fileHadMatch = true
					filesWithMatches++
				}
				for _, held := range before {
					if held.number <= lastPrinted {
						continue
					}
					if lastPrinted > 0 && held.number > lastPrinted+1 {
						lines = append(lines, "--")
					}
					lines = append(lines, fmt.Sprintf("%s-%d-%s", display, held.number, held.text))
					lastPrinted = held.number
				}
				before = before[:0]

				if contextLines > 0 && lastPrinted > 0 && lineNo > lastPrinted+1 {
					lines = append(lines, "--")
				}
				lines = append(lines, fmt.Sprintf("%s:%d:%s", display, lineNo, text))
				lastPrinted = lineNo
				trailing = contextLines
				matches++
				if matches >= matchCap {
					return nil
				}
			case trailing > 0:
				lines = append(lines, fmt.Sprintf("%s-%d-%s", display, lineNo, text))
				lastPrinted = lineNo
				trailing--
			case contextLines > 0:
				if len(before) == contextLines {
					before = before[1:]
				}
				before = append(before, contextLine{number: lineNo, text: text})
			}
		}
		return nil
	})

	searchRoot := "."
	if input.Path != "" {
		searchRoot = normalizeRelativePath(input.Path)
	}
	_, statErr := os.Stat(searchPath)
	rootMissing := statErr != nil

	output := strings.Join(lines, "\n")
	truncated := matches >= matchCap

	if matches == 0 {
		// A bare "no matches" reads like proof that the pattern is absent. It is
		// only proof that these files were searched, so say which files those were.
		output = renderGrepMiss(input, searchRoot, rootMissing, filesSearched, skipped)
	} else {
		if truncated {
			output += fmt.Sprintf("\n\n[truncated after %d matches]", matchCap)
		}
		if skipped > 0 {
			output += fmt.Sprintf("\n[skipped %d large/binary files]", skipped)
		}
		output += fmt.Sprintf("\n[%d match(es) in %d file(s), %d file(s) searched under %s]",
			matches, filesWithMatches, filesSearched, searchRoot)
	}
	output = truncateOutput(output)

	return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: output},
			},
		}, GrepOutput{
			Result:        output,
			Matches:       matches,
			FilesSearched: filesSearched,
			FilesMatched:  filesWithMatches,
			FilesSkipped:  skipped,
			SearchRoot:    searchRoot,
			Truncated:     truncated,
		}, nil
}

// renderGrepMiss explains a search that found nothing.
func renderGrepMiss(input GrepInput, searchRoot string, rootMissing bool, filesSearched, skipped int) string {
	var report strings.Builder
	fmt.Fprintf(&report, "No matches found for pattern: %s", input.Pattern)

	if rootMissing {
		fmt.Fprintf(&report, "\n  the search root %s does not exist, so no file was opened at all", searchRoot)
		report.WriteString("\n  check the path, or list its parent directory to see what is really there")
		return report.String()
	}

	fmt.Fprintf(&report, "\n  searched %d file(s) under %s", filesSearched, searchRoot)
	if input.Include != "" {
		fmt.Fprintf(&report, ", limited to names matching %s", input.Include)
	}
	if skipped > 0 {
		fmt.Fprintf(&report, "\n  skipped %d file(s) for being too large or binary", skipped)
	}

	if filesSearched == 0 {
		report.WriteString("\n  no file was searched, so this result says nothing about the pattern")
		if input.Include != "" {
			fmt.Fprintf(&report, ": the include glob %s matched no file name", input.Include)
		}
		report.WriteString("\n  note: .git, node_modules and build cache directories are always skipped")
		return report.String()
	}

	if !input.CaseInsensitive {
		report.WriteString("\n  tip: matching is case sensitive unless caseInsensitive is set")
	}
	report.WriteString("\n  tip: pattern is a Go regular expression, so . ( ) [ ] * + ? | need escaping to match literally")
	return report.String()
}

// GlobInput represents the input for the glob tool.
type GlobInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Pattern     string `json:"pattern" jsonschema:"File glob pattern."`
	Path        string `json:"path,omitempty" jsonschema:"Optional path scope relative to the workspace root."`
}

// GlobOutput represents the output for the glob tool.
type GlobOutput struct {
	Result string `json:"result" jsonschema:"Glob results."`
}

// FindFiles finds files matching a glob pattern.
func FindFiles(ctx context.Context, req *mcp.CallToolRequest, input GlobInput, wsRoot string) (*mcp.CallToolResult, GlobOutput, error) {
	pattern := normalizeGlobPattern(input.Pattern)
	if pattern == "" {
		pattern = "**/*"
	}
	if _, err := compileGlob(pattern); err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("invalid glob pattern %q: %v", input.Pattern, err))
		return result, GlobOutput{}, nil
	}

	searchPath := wsRoot
	if input.Path != "" {
		searchPath = filepath.Join(wsRoot, input.Path)
	}

	var matches []string
	truncated := false
	start := time.Now()

	// WalkDir avoids one stat syscall per entry, which matters on large trees.
	_ = filepath.WalkDir(searchPath, func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || time.Since(start) > searchWalkTimeout {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			// Never skip the directory the caller explicitly scoped to.
			if entryPath != searchPath && skippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// The pattern is matched against the path relative to the search
		// scope; results stay relative to the workspace root.
		rel, err := filepath.Rel(searchPath, entryPath)
		if err != nil || !MatchGlob(pattern, filepath.ToSlash(rel)) {
			return nil
		}

		reported, err := filepath.Rel(wsRoot, entryPath)
		if err != nil {
			reported = rel
		}
		matches = append(matches, filepath.ToSlash(reported))
		if len(matches) >= maxSearchMatches {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})

	result := strings.Join(matches, "\n")
	if result == "" {
		scope := "the workspace root"
		if input.Path != "" {
			scope = input.Path
		}
		result = fmt.Sprintf("No files found matching: %s (searched %s)", input.Pattern, scope)
	} else if truncated {
		result += fmt.Sprintf("\n\n[truncated after %d files]", maxSearchMatches)
	}
	result = truncateOutput(result)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, GlobOutput{Result: result}, nil
}

// LsInput represents the input for the ls tool.
type LsInput struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Path        string `json:"path" jsonschema:"Directory path to list, relative to the workspace root. Use . for the root itself."`
	Details     bool   `json:"details,omitempty" jsonschema:"Show full RFC 3339 modification times in the text listing instead of the short Jan 02 15:04 form. The structured entries always carry name, type, size and modifiedAt whether or not this is set, so there is no need to shell out to ls -l."`
}

// LsEntry is one directory entry described in full, so that a provenance or
// conflict check never has to fall back to the shell for a timestamp.
type LsEntry struct {
	Name       string `json:"name" jsonschema:"Entry name, with no path attached."`
	Path       string `json:"path" jsonschema:"Path relative to the workspace root, ready to pass to read or edit."`
	Type       string `json:"type" jsonschema:"file, dir, symlink or other."`
	Size       int64  `json:"size" jsonschema:"Size in bytes. Not meaningful for directories."`
	ModifiedAt string `json:"modifiedAt" jsonschema:"Modification time, RFC 3339 in UTC."`
}

// LsOutput represents the output for the ls tool.
type LsOutput struct {
	Result      string    `json:"result" jsonschema:"Directory listing as text."`
	Path        string    `json:"path" jsonschema:"Directory that was listed, relative to the workspace root."`
	Entries     []LsEntry `json:"entries" jsonschema:"One entry per item in the directory, always populated."`
	Files       int       `json:"files" jsonschema:"How many entries are regular files."`
	Directories int       `json:"directories" jsonschema:"How many entries are directories."`
}

// ListDirectory lists the contents of a directory.
func ListDirectory(ctx context.Context, req *mcp.CallToolRequest, input LsInput, wsRoot string) (*mcp.CallToolResult, LsOutput, error) {
	absPath := filepath.Join(wsRoot, input.Path)

	entries, err := os.ReadDir(absPath)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("failed to read directory: %v", err))
		return result, LsOutput{}, nil
	}

	displayPath := normalizeRelativePath(input.Path)
	out := LsOutput{Path: displayPath}

	var lines []string
	for _, entry := range entries {
		name := entry.Name()
		record := LsEntry{
			Name: name,
			Path: joinDisplayPath(displayPath, name),
			Type: describeDirEntry(entry),
		}

		info, err := entry.Info()
		if err != nil {
			out.Entries = append(out.Entries, record)
			lines = append(lines, name)
			continue
		}
		record.Size = info.Size()
		record.ModifiedAt = fingerprintTime(info.ModTime())

		switch record.Type {
		case "dir":
			out.Directories++
		case "file":
			out.Files++
		}
		out.Entries = append(out.Entries, record)

		prefix := "-"
		displayName := name
		if entry.IsDir() {
			prefix = "d"
			displayName += "/"
		}

		stamp := info.ModTime().Format("Jan 02 15:04")
		if input.Details {
			stamp = record.ModifiedAt
		}

		lines = append(lines, fmt.Sprintf("%s %8s %s %s", prefix, formatSize(record.Size), stamp, displayName))
	}

	result := strings.Join(lines, "\n")
	if result == "" {
		result = fmt.Sprintf("Empty directory: %s has no entries", displayPath)
	} else {
		result += fmt.Sprintf("\n[%d entr(ies): %d file(s), %d director(ies)]",
			len(out.Entries), out.Files, out.Directories)
	}
	out.Result = result

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, out, nil
}

// describeDirEntry names an entry's kind for the structured listing.
func describeDirEntry(entry os.DirEntry) string {
	switch {
	case entry.IsDir():
		return "dir"
	case entry.Type()&os.ModeSymlink != 0:
		return "symlink"
	case entry.Type().IsRegular():
		return "file"
	default:
		return "other"
	}
}

// joinDisplayPath builds the workspace relative path of a directory entry.
func joinDisplayPath(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

// BashInput represents the input for the bash tool.
type BashInput struct {
	WorkspaceID      string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Command          string `json:"command" jsonschema:"Shell command to run."`
	WorkingDirectory string `json:"workingDirectory,omitempty" jsonschema:"Optional working directory relative to the workspace root."`
	Timeout          int    `json:"timeout,omitempty" jsonschema:"Timeout in seconds for a command run in the foreground. Defaults to 30. A value above 120 starts a background job instead, because a request that long is usually abandoned by the client before it can answer. On timeout the whole process tree is terminated and whatever the command printed first is returned."`
	Background       bool   `json:"background,omitempty" jsonschema:"Start the command in the background and return a jobId immediately instead of waiting for it. Use this for watches, long builds, long test runs, and dev servers. Read its output with job_status and stop it with job_kill."`
}

// BashOutput represents the output for the bash tool.
type BashOutput struct {
	Result string `json:"result" jsonschema:"Shell command output, or the job details when the command was started in the background."`
	JobID  string `json:"jobId,omitempty" jsonschema:"Identifier of the background job, set only when the command was started in the background."`
	Status string `json:"status,omitempty" jsonschema:"State of the background job right after it started."`
}

// RunBash executes a shell command in the shell reported by currentShell:
// the configured one when it is installed, otherwise the best one detected on
// this machine.
func RunBash(ctx context.Context, req *mcp.CallToolRequest, input BashInput, wsRoot string) (*mcp.CallToolResult, BashOutput, error) {
	cwd := wsRoot
	if input.WorkingDirectory != "" {
		cwd = filepath.Join(wsRoot, input.WorkingDirectory)
	}

	sel := currentShell()
	if sel.err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(sel.err)
		return result, BashOutput{}, nil
	}

	// A command that asks for more time than a foreground call can survive is
	// started as a background job instead. The client abandons a request that
	// long, and an abandoned request loses every line the command printed,
	// which is the worst of both outcomes.
	if input.Background || input.Timeout > maxSyncCommandTimeout {
		return startBackgroundBash(cwd, sel, input)
	}

	timeout := normalizeTimeout(input.Timeout)
	res := runCommand(ctx, cwd, sel.shell.Path, shellArgs(sel.shell, input.Command), timeout)

	if res.timedOut {
		report := timeoutReport(res.output, timeout)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: report},
			},
			IsError: true,
		}, BashOutput{Result: report}, nil
	}

	if res.startFailed {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("command failed to start: %v", res.err))
		return result, BashOutput{}, nil
	}

	report := res.output
	if strings.TrimSpace(report) == "" {
		report = "(no output)"
	}
	switch {
	case res.exitCode > 0:
		report += fmt.Sprintf("\n[exit code: %d]", res.exitCode)
	case res.exitCode < 0 && res.err != nil:
		report += fmt.Sprintf("\n[terminated: %v]", res.err)
	}
	report = truncateOutput(report)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: report},
		},
	}, BashOutput{Result: report}, nil
}

// startBackgroundBash hands a command to the job registry and returns at once,
// so the caller keeps a jobId even when the command outlives the request that
// started it.
func startBackgroundBash(cwd string, sel shellSelection, input BashInput) (*mcp.CallToolResult, BashOutput, error) {
	timeout := normalizeJobTimeout(input.Timeout)

	j, err := backgroundJobs.start(cwd, sel.shell.Path, sel.shell.Path,
		shellArgs(sel.shell, input.Command), input.Command, timeout)
	if err != nil {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("command failed to start: %v", err))
		return result, BashOutput{}, nil
	}

	report := fmt.Sprintf(
		"Started %s in the background (timeout %ds).\n$ %s\nRead its output with job_status, which can wait up to %ds for it to finish. Stop it with job_kill and list jobs with job_list.\nNext call: job_status {\"jobId\": %q, \"wait\": %d}, then call it again with the nextLine it returns as sinceLine until done is true.\nThe job tools are addressed by jobId alone: workspaceId is accepted and ignored there, so passing it along is harmless.",
		j.id, timeout, input.Command, maxJobWaitSeconds, j.id, maxJobWaitSeconds)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: report},
		},
	}, BashOutput{Result: report, JobID: j.id, Status: JobRunning}, nil
}

// --- helpers ---

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGT"[exp])
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func truncateOutput(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	return s[:maxToolOutputBytes] + fmt.Sprintf("\n\n[output truncated after %d bytes]", maxToolOutputBytes)
}

func looksBinaryPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".obj", ".o", ".a",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf",
		".zip", ".tar", ".gz", ".7z", ".rar",
		".db", ".sqlite", ".sqlite3", ".wasm":
		return true
	default:
		return false
	}
}
