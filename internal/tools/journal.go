package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snakex21/devspace-go/internal/workspace"
)

const (
	// journalCapacity bounds the write journal. It is a ring, so a very long
	// session forgets its oldest writes rather than growing without limit, and
	// the number forgotten is always reported.
	journalCapacity = 1000

	defaultRecentChangesLimit = 50
	maxRecentChangesLimit     = 500
)

// Operation names recorded in the write journal.
const (
	ChangeWrite  = "write"
	ChangeEdit   = "edit"
	ChangeMove   = "move"
	ChangeRemove = "remove"
	ChangeMkdir  = "mkdir"
)

// changeRecord is one modification this server process made.
type changeRecord struct {
	sequence int
	op       string
	root     string
	path     string
	// related is the other path involved in a move. It counts as touched for
	// provenance purposes without earning a row of its own.
	related string
	detail  string
	bytes   int64
	at      time.Time
}

// changeJournal is the process wide record of everything this server wrote.
//
// It exists because a client that compacts its context loses the agent's
// memory of its own edits, and an agent that cannot remember writing a file
// will eventually conclude that something else wrote it.
type changeJournal struct {
	mu      sync.Mutex
	entries []changeRecord
	issued  int
	dropped int
}

var writeJournal = &changeJournal{}

func (j *changeJournal) record(entry changeRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.issued++
	entry.sequence = j.issued
	entry.at = time.Now()
	entry.root = filepath.Clean(entry.root)
	j.entries = append(j.entries, entry)

	if len(j.entries) > journalCapacity {
		surplus := len(j.entries) - journalCapacity
		j.entries = append(j.entries[:0], j.entries[surplus:]...)
		j.dropped += surplus
	}
}

func (j *changeJournal) snapshotEntries() ([]changeRecord, int, int) {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]changeRecord, len(j.entries))
	copy(out, j.entries)
	return out, j.issued, j.dropped
}

// reset clears the journal. Only tests need it.
func (j *changeJournal) reset() {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.entries = nil
	j.issued = 0
	j.dropped = 0
}

// recordChange notes one modification. Callers pass the workspace relative
// path they were given, so the journal reads in the same terms as the tool
// calls that produced it.
func recordChange(op, root, path string, bytes int64, detail string) {
	writeJournal.record(changeRecord{
		op:     op,
		root:   root,
		path:   normalizeRelativePath(path),
		detail: detail,
		bytes:  bytes,
	})
}

// recordMove notes a rename, which touches two paths at once.
func recordMove(root, from, to string, bytes int64) {
	writeJournal.record(changeRecord{
		op:      ChangeMove,
		root:    root,
		path:    normalizeRelativePath(to),
		related: normalizeRelativePath(from),
		detail:  "from " + normalizeRelativePath(from),
		bytes:   bytes,
	})
}

// normalizeRelativePath renders a path the way the rest of the output does:
// forward slashes, no leading "./".
func normalizeRelativePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == "." {
		return "."
	}
	slashed := filepath.ToSlash(filepath.Clean(trimmed))
	return strings.TrimPrefix(slashed, "./")
}

// journalKey normalizes a path for comparison, so a path recorded as
// "internal\tools\tools.go" and the "internal/tools/tools.go" that git reports
// are recognised as the same file.
func journalKey(path string) string {
	key := normalizeRelativePath(path)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(key)
	}
	return key
}

// RecentChangesInput represents the input for the recent_changes tool.
type RecentChangesInput struct {
	WorkspaceID   string `json:"workspaceId" jsonschema:"Workspace identifier returned by open_workspace."`
	Limit         int    `json:"limit,omitempty" jsonschema:"How many changes to return, newest first. Defaults to 50, maximum 500."`
	Path          string `json:"path,omitempty" jsonschema:"Only report changes at or under this path, relative to the workspace root."`
	Op            string `json:"op,omitempty" jsonschema:"Only report one kind of change: write, edit, move, remove or mkdir."`
	AllWorkspaces bool   `json:"allWorkspaces,omitempty" jsonschema:"Include changes made in other workspaces this server has open, not just this one."`
	SkipGitStatus bool   `json:"skipGitStatus,omitempty" jsonschema:"Skip the git cross-check. The cross-check is what separates your own edits from another process's, so only skip it when git is unavailable or slow."`
}

// ChangeEntry is one journal row as reported to a caller.
type ChangeEntry struct {
	Sequence      int    `json:"sequence" jsonschema:"Monotonic counter for this server process. Higher means later."`
	Op            string `json:"op" jsonschema:"One of write, edit, move, remove or mkdir."`
	Path          string `json:"path" jsonschema:"Path relative to the workspace root, forward slashed."`
	At            string `json:"at" jsonschema:"When the change was made, RFC 3339 in UTC."`
	Ago           string `json:"ago" jsonschema:"How long ago the change was made, in words."`
	Bytes         int64  `json:"bytes,omitempty" jsonschema:"Size of the file after the change, when it was known."`
	Detail        string `json:"detail,omitempty" jsonschema:"Extra context, such as the number of edits applied or the source path of a move."`
	WorkspaceRoot string `json:"workspaceRoot,omitempty" jsonschema:"Absolute root the change was made in. Only set when allWorkspaces was used."`
}

// RecentChangesOutput represents the output for the recent_changes tool.
type RecentChangesOutput struct {
	Verdict               string        `json:"verdict" jsonschema:"One sentence answering whether the uncommitted work in this workspace came from this server process or from something else."`
	Changes               []ChangeEntry `json:"changes" jsonschema:"Recorded changes, newest first."`
	Returned              int           `json:"returned" jsonschema:"How many changes are in this response."`
	Matched               int           `json:"matched" jsonschema:"How many recorded changes matched the filters, before the limit was applied."`
	TotalRecorded         int           `json:"totalRecorded" jsonschema:"How many changes this server process has made in total, across every workspace."`
	Dropped               int           `json:"dropped,omitempty" jsonschema:"How many of the oldest changes have been forgotten because the journal is a fixed size ring."`
	FilesTouched          int           `json:"filesTouched" jsonschema:"How many distinct files this server process changed in this workspace."`
	DirtyCount            int           `json:"dirtyCount" jsonschema:"Number of uncommitted paths git reports for this workspace."`
	DirtyPaths            []string      `json:"dirtyPaths,omitempty" jsonschema:"Uncommitted paths git reports, relative to the workspace root."`
	UnexplainedDirtyPaths []string      `json:"unexplainedDirtyPaths,omitempty" jsonschema:"Uncommitted paths this server process never wrote. These are the only files that could have been changed by something else."`
	GitChecked            bool          `json:"gitChecked" jsonschema:"Whether the git cross-check ran. False means uncommitted work is unknown, which is not the same as clean."`
	Result                string        `json:"result" jsonschema:"The whole report as text."`
}

// RecentChanges reports what this server process has written.
func RecentChanges(ctx context.Context, req *mcp.CallToolRequest, input RecentChangesInput, wsRoot string) (*mcp.CallToolResult, RecentChangesOutput, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = defaultRecentChangesLimit
	}
	if limit > maxRecentChangesLimit {
		limit = maxRecentChangesLimit
	}

	opFilter := strings.ToLower(strings.TrimSpace(input.Op))
	if opFilter != "" && !isKnownChangeOp(opFilter) {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("unknown op %q: use write, edit, move, remove or mkdir", input.Op))
		return result, RecentChangesOutput{}, nil
	}

	entries, total, dropped := writeJournal.snapshotEntries()
	rootKey := journalKey(wsRoot)
	if filepath.IsAbs(wsRoot) {
		rootKey = absoluteKey(wsRoot)
	}

	pathFilter := ""
	if strings.TrimSpace(input.Path) != "" {
		pathFilter = journalKey(input.Path)
	}

	touched := map[string]bool{}
	var matched []changeRecord

	for _, entry := range entries {
		inThisWorkspace := absoluteKey(entry.root) == rootKey

		// Provenance only makes sense within one root, so the touched set is
		// always scoped to this workspace even when the listing is not.
		if inThisWorkspace {
			touched[journalKey(entry.path)] = true
			if entry.related != "" {
				touched[journalKey(entry.related)] = true
			}
		}
		if !inThisWorkspace && !input.AllWorkspaces {
			continue
		}
		if opFilter != "" && entry.op != opFilter {
			continue
		}
		if pathFilter != "" && !pathMatchesFilter(entry, pathFilter) {
			continue
		}
		matched = append(matched, entry)
	}

	out := RecentChangesOutput{
		Matched:       len(matched),
		TotalRecorded: total,
		Dropped:       dropped,
		FilesTouched:  len(touched),
	}

	// Newest first, and only the tail once the limit is applied.
	for index := len(matched) - 1; index >= 0 && len(out.Changes) < limit; index-- {
		entry := matched[index]
		row := ChangeEntry{
			Sequence: entry.sequence,
			Op:       entry.op,
			Path:     entry.path,
			At:       entry.at.UTC().Format(time.RFC3339),
			Ago:      humanizeAge(time.Since(entry.at)),
			Bytes:    entry.bytes,
			Detail:   entry.detail,
		}
		if input.AllWorkspaces {
			row.WorkspaceRoot = entry.root
		}
		out.Changes = append(out.Changes, row)
	}
	out.Returned = len(out.Changes)

	if !input.SkipGitStatus {
		if dirty, ok := workspace.DirtyPaths(wsRoot); ok {
			out.GitChecked = true
			out.DirtyCount = len(dirty)
			out.DirtyPaths = dirty
			for _, path := range dirty {
				if !touched[journalKey(path)] {
					out.UnexplainedDirtyPaths = append(out.UnexplainedDirtyPaths, path)
				}
			}
		}
	}

	out.Verdict = changeVerdict(out)
	out.Result = renderRecentChanges(out, input, limit)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: out.Result},
		},
	}, out, nil
}

func isKnownChangeOp(op string) bool {
	switch op {
	case ChangeWrite, ChangeEdit, ChangeMove, ChangeRemove, ChangeMkdir:
		return true
	}
	return false
}

// absoluteKey normalizes a root for comparison between journal entries.
func absoluteKey(root string) string {
	key := filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(key)
	}
	return key
}

// pathMatchesFilter matches a file or anything under a directory.
func pathMatchesFilter(entry changeRecord, filter string) bool {
	for _, candidate := range []string{entry.path, entry.related} {
		if candidate == "" {
			continue
		}
		key := journalKey(candidate)
		if key == filter || strings.HasPrefix(key, filter+"/") {
			return true
		}
	}
	return false
}

// changeVerdict answers the question the tool exists for in one sentence.
func changeVerdict(out RecentChangesOutput) string {
	caveat := ""
	if out.Dropped > 0 {
		caveat = fmt.Sprintf(" The %d oldest changes have been forgotten, so treat that attribution as a strong hint rather than proof.", out.Dropped)
	}

	switch {
	case !out.GitChecked && out.FilesTouched == 0:
		return "This server process has not written anything in this workspace, and git was not consulted."
	case !out.GitChecked:
		return fmt.Sprintf("This server process changed %d file(s) in this workspace. Git was not consulted, so uncommitted work was not cross-checked.", out.FilesTouched)
	case out.DirtyCount == 0:
		return "The working tree is clean, so nothing this server wrote is still uncommitted."
	case len(out.UnexplainedDirtyPaths) == 0:
		return fmt.Sprintf("Every one of the %d uncommitted file(s) was written by this server process, so there is no sign of another writer.", out.DirtyCount)
	default:
		return fmt.Sprintf("%d of the %d uncommitted file(s) were never written by this server process: they were already modified before this session, or something outside this server changed them.%s",
			len(out.UnexplainedDirtyPaths), out.DirtyCount, caveat)
	}
}

// renderRecentChanges lays the report out as text, since that is the form most
// clients actually show to the model.
func renderRecentChanges(out RecentChangesOutput, input RecentChangesInput, limit int) string {
	var report strings.Builder
	report.WriteString(out.Verdict)

	if out.Returned == 0 {
		switch {
		case out.TotalRecorded == 0:
			report.WriteString("\n\nNo changes recorded. This server process has not written, edited, moved or removed anything since it started.")
		case input.Path != "" || input.Op != "":
			fmt.Fprintf(&report, "\n\nNo recorded change matched the filters (path %q, op %q), out of %d change(s) in total.",
				input.Path, input.Op, out.TotalRecorded)
		default:
			fmt.Fprintf(&report, "\n\nNo changes recorded in this workspace. This server process has made %d change(s) elsewhere; pass allWorkspaces to see them.", out.TotalRecorded)
		}
	} else {
		fmt.Fprintf(&report, "\n\n%d most recent change(s), newest first:", out.Returned)
		for _, change := range out.Changes {
			fmt.Fprintf(&report, "\n  #%d %-6s %s  (%s", change.Sequence, change.Op, change.Path, change.Ago)
			if change.Bytes > 0 {
				fmt.Fprintf(&report, ", %s", formatSize(change.Bytes))
			}
			if change.Detail != "" {
				fmt.Fprintf(&report, ", %s", change.Detail)
			}
			report.WriteString(")")
			if change.WorkspaceRoot != "" {
				fmt.Fprintf(&report, "\n         in %s", change.WorkspaceRoot)
			}
		}
		if out.Matched > out.Returned {
			fmt.Fprintf(&report, "\n  [%d older matching change(s) not shown; raise limit past %d to see them]", out.Matched-out.Returned, limit)
		}
	}

	if out.GitChecked {
		fmt.Fprintf(&report, "\n\nGit reports %d uncommitted path(s).", out.DirtyCount)
		if len(out.UnexplainedDirtyPaths) > 0 {
			report.WriteString("\nNot written by this server process:")
			for _, path := range out.UnexplainedDirtyPaths {
				fmt.Fprintf(&report, "\n  %s", path)
			}
			report.WriteString("\nCheck those with git diff before assuming they are yours to overwrite.")
		}
	} else if !input.SkipGitStatus {
		report.WriteString("\n\nGit could not be consulted here, so uncommitted work is unknown rather than absent.")
	}

	if out.Dropped > 0 {
		fmt.Fprintf(&report, "\n\n[the journal keeps the last %d changes; %d older one(s) have been forgotten]", journalCapacity, out.Dropped)
	}

	return truncateOutput(report.String())
}

// humanizeAge renders a duration the way a person reads one.
func humanizeAge(age time.Duration) string {
	switch {
	case age < time.Second:
		return "just now"
	case age < time.Minute:
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm ago", int(age.Hours()), int(age.Minutes())%60)
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours())/24)
	}
}
