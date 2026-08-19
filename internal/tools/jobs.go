package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// defaultJobTimeout is the wall clock budget for a background job when the
	// caller does not ask for one. A background job is not holding a request
	// open, so it can afford far more time than a foreground command.
	defaultJobTimeout = 3600
	// maxJobTimeout caps what a caller may ask for.
	maxJobTimeout = 86400
	// maxJobOutputBytes caps what one job keeps in memory. The oldest lines are
	// dropped first and the count of dropped lines is reported, so a chatty
	// build can neither grow without bound nor quietly lose its tail.
	maxJobOutputBytes = 1024 * 1024
	// maxTrackedJobs bounds how many finished jobs stay readable.
	maxTrackedJobs = 64
	// jobRetention is how long a finished job stays readable.
	jobRetention = 2 * time.Hour
	// defaultJobLines and maxJobLines bound one job_status answer.
	defaultJobLines = 200
	maxJobLines     = 2000
	// maxJobWaitSeconds caps the optional wait in job_status. It stays well
	// inside the request budget of the clients seen so far, so waiting for a
	// job to finish never costs the caller the answer.
	maxJobWaitSeconds = 25
	// maxSyncCommandTimeout is the longest a foreground command may ask for.
	// Past this point a client abandons the request and the caller loses the
	// output entirely, so a longer request becomes a background job.
	maxSyncCommandTimeout = 120
)

// Job states reported to the caller.
const (
	JobRunning = "running"
	JobExited  = "exited"
	JobKilled  = "killed"
	JobTimeout = "timeout"
)

// lineSink collects command output as whole lines so a caller can read it
// incrementally with a cursor instead of receiving the same bytes again on
// every poll.
type lineSink struct {
	mu      sync.Mutex
	lines   []string
	partial []byte
	held    int
	dropped int
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, b := range p {
		if b == '\n' {
			s.appendLine(strings.TrimRight(string(s.partial), "\r"))
			s.partial = s.partial[:0]
			continue
		}
		s.partial = append(s.partial, b)
	}
	return len(p), nil
}

// appendLine stores one line, discarding the oldest lines once the job has
// printed more than the in-memory cap allows. The caller must hold the mutex.
func (s *lineSink) appendLine(line string) {
	s.lines = append(s.lines, line)
	s.held += len(line) + 1

	for s.held > maxJobOutputBytes && len(s.lines) > 1 {
		s.held -= len(s.lines[0]) + 1
		s.lines = s.lines[1:]
		s.dropped++
	}
}

// flush stores a trailing line that never got its newline, which is how most
// commands print their last line before exiting.
func (s *lineSink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.partial) > 0 {
		s.appendLine(strings.TrimRight(string(s.partial), "\r"))
		s.partial = s.partial[:0]
	}
}

// snapshot returns stored lines starting at a 1-based cursor, along with the
// cursor to pass next time. Line numbers count lines that were dropped, so a
// cursor stays monotonic even when output was trimmed.
func (s *lineSink) snapshot(since, limit int) (lines []string, next, total, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	total = s.dropped + len(s.lines)
	if since <= 0 {
		since = 1
	}
	if limit <= 0 {
		limit = defaultJobLines
	}
	if limit > maxJobLines {
		limit = maxJobLines
	}

	first := since - s.dropped - 1
	if first < 0 {
		first = 0
	}
	if first > len(s.lines) {
		first = len(s.lines)
	}

	end := len(s.lines)
	if end-first > limit {
		end = first + limit
	}

	out := make([]string, end-first)
	copy(out, s.lines[first:end])
	return out, s.dropped + end + 1, total, s.dropped
}

// job is one command running outside the lifetime of a single tool call.
type job struct {
	id      string
	command string
	cwd     string
	shell   string
	timeout int

	sink *lineSink
	cmd  *exec.Cmd
	done chan struct{}

	mu        sync.Mutex
	status    string
	exitCode  int
	killed    bool
	startedAt time.Time
	endedAt   time.Time
}

// state reports the job's current bookkeeping under one lock.
func (j *job) state() (status string, exitCode int, startedAt, endedAt time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status, j.exitCode, j.startedAt, j.endedAt
}

func (j *job) running() bool {
	status, _, _, _ := j.state()
	return status == JobRunning
}

// elapsed reports how long the job ran, or has been running.
func (j *job) elapsed() time.Duration {
	status, _, startedAt, endedAt := j.state()
	if status == JobRunning || endedAt.IsZero() {
		return time.Since(startedAt)
	}
	return endedAt.Sub(startedAt)
}

// supervise waits for the command, enforcing the job's own timeout, and
// records how it ended.
func (j *job) supervise() {
	waited := make(chan error, 1)
	go func() { waited <- j.cmd.Wait() }()

	timer := time.NewTimer(time.Duration(j.timeout) * time.Second)
	defer timer.Stop()

	select {
	case err := <-waited:
		j.sink.flush()
		j.finish(exitCodeOf(err))
	case <-timer.C:
		killProcessTree(j.cmd)
		reap(waited)
		j.sink.flush()
		j.settle(JobTimeout, -1)
	}

	close(j.done)
}

// finish records a natural exit, reporting a job that was killed on request as
// killed rather than as whatever exit code the signal produced.
func (j *job) finish(exitCode int) {
	j.mu.Lock()
	killed := j.killed
	j.mu.Unlock()

	if killed {
		j.settle(JobKilled, exitCode)
		return
	}
	j.settle(JobExited, exitCode)
}

func (j *job) settle(status string, exitCode int) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.status != JobRunning {
		return
	}
	j.status = status
	j.exitCode = exitCode
	j.endedAt = time.Now()
}

// kill stops the job's whole process tree. Killing an already finished job is
// not an error, so a caller does not have to race the job to clean up.
func (j *job) kill() bool {
	j.mu.Lock()
	if j.status != JobRunning {
		j.mu.Unlock()
		return false
	}
	j.killed = true
	j.mu.Unlock()

	killProcessTree(j.cmd)
	return true
}

// jobRegistry tracks background jobs for the life of the process.
type jobRegistry struct {
	mu     sync.Mutex
	jobs   map[string]*job
	order  []string
	lastID int
}

var backgroundJobs = &jobRegistry{jobs: make(map[string]*job)}

// start launches a command in the background and returns immediately.
func (r *jobRegistry) start(cwd, shellLabel, name string, args []string, command string, timeoutSec int) (*job, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	configureProcessGroup(cmd)

	sink := &lineSink{}
	cmd.Stdout = sink
	cmd.Stderr = sink

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.lastID++
	j := &job{
		id:        fmt.Sprintf("job_%d", r.lastID),
		command:   command,
		cwd:       cwd,
		shell:     shellLabel,
		timeout:   timeoutSec,
		sink:      sink,
		cmd:       cmd,
		done:      make(chan struct{}),
		status:    JobRunning,
		exitCode:  -1,
		startedAt: time.Now(),
	}
	r.jobs[j.id] = j
	r.order = append(r.order, j.id)
	r.pruneLocked()
	r.mu.Unlock()

	go j.supervise()
	return j, nil
}

func (r *jobRegistry) lookup(id string) (*job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

// list returns the tracked jobs, newest first.
func (r *jobRegistry) list() []*job {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*job, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		if j, ok := r.jobs[r.order[i]]; ok {
			out = append(out, j)
		}
	}
	return out
}

// pruneLocked forgets finished jobs that are old or in excess of the cap. A
// running job is never forgotten, however many there are, because dropping it
// would leave a process nobody can report on or stop.
func (r *jobRegistry) pruneLocked() {
	keep := make([]string, 0, len(r.order))
	for _, id := range r.order {
		j, ok := r.jobs[id]
		if !ok {
			continue
		}
		status, _, _, endedAt := j.state()
		if status != JobRunning && !endedAt.IsZero() && time.Since(endedAt) > jobRetention {
			delete(r.jobs, id)
			continue
		}
		keep = append(keep, id)
	}

	for len(keep) > maxTrackedJobs {
		oldest := -1
		for i, id := range keep {
			if j, ok := r.jobs[id]; ok && !j.running() {
				oldest = i
				break
			}
		}
		if oldest < 0 {
			break
		}
		delete(r.jobs, keep[oldest])
		keep = append(keep[:oldest], keep[oldest+1:]...)
	}

	r.order = keep
}

// normalizeJobTimeout applies the background default and cap.
func normalizeJobTimeout(requested int) int {
	if requested <= 0 {
		return defaultJobTimeout
	}
	if requested > maxJobTimeout {
		return maxJobTimeout
	}
	return requested
}

// JobStatusInput represents the input for the job_status tool.
type JobStatusInput struct {
	JobID       string `json:"jobId" jsonschema:"Job identifier returned by the shell tool when background is true."`
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"Ignored. Jobs are addressed by jobId alone, but this is accepted so that passing the same workspaceId used for the shell tool is never rejected as an unexpected property."`
	SinceLine   int    `json:"sinceLine,omitempty" jsonschema:"Return output from this 1-based line onward. Pass the nextLine from the previous call to stream new output without repeating what you already read."`
	MaxLines    int    `json:"maxLines,omitempty" jsonschema:"Maximum output lines to return. Defaults to 200, max 2000."`
	Wait        int    `json:"wait,omitempty" jsonschema:"Seconds to wait for the job to finish before answering. Defaults to 0, which answers immediately. Max 25. Prefer this over sleeping in a shell command."`
}

// JobStatusOutput represents the output for the job_status tool.
type JobStatusOutput struct {
	JobID        string `json:"jobId" jsonschema:"Job identifier."`
	Status       string `json:"status" jsonschema:"running, exited, killed, or timeout."`
	ExitCode     int    `json:"exitCode" jsonschema:"Exit code once the job has finished; -1 while it is still running."`
	Command      string `json:"command" jsonschema:"The command this job is running."`
	ElapsedMS    int64  `json:"elapsedMs" jsonschema:"How long the job ran, in milliseconds."`
	Output       string `json:"output" jsonschema:"Output lines from sinceLine onward."`
	NextLine     int    `json:"nextLine" jsonschema:"Pass this as sinceLine on the next call to continue where this answer stopped."`
	TotalLines   int    `json:"totalLines" jsonschema:"Total lines the job has printed so far."`
	DroppedLines int    `json:"droppedLines,omitempty" jsonschema:"Oldest lines discarded because the job exceeded the in-memory output cap."`
	NextCall     string `json:"nextCall,omitempty" jsonschema:"The exact follow-up call worth making, already filled in with jobId and sinceLine. Empty once the job has finished and all of its output has been returned."`
	Result       string `json:"result" jsonschema:"Human readable summary of the job and the returned output."`
}

// JobStatus reports a background job's state and streams its output from a
// cursor.
func JobStatus(ctx context.Context, req *mcp.CallToolRequest, input JobStatusInput) (*mcp.CallToolResult, JobStatusOutput, error) {
	j, ok := backgroundJobs.lookup(strings.TrimSpace(input.JobID))
	if !ok {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("no background job %q; call job_list to see the jobs this server is tracking", input.JobID))
		return result, JobStatusOutput{}, nil
	}

	wait := input.Wait
	if wait > maxJobWaitSeconds {
		wait = maxJobWaitSeconds
	}
	if wait > 0 && j.running() {
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		defer timer.Stop()
		select {
		case <-j.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}

	lines, next, total, dropped := j.sink.snapshot(input.SinceLine, input.MaxLines)
	status, exitCode, _, _ := j.state()
	elapsed := j.elapsed()

	var report strings.Builder
	fmt.Fprintf(&report, "%s %s after %s\n$ %s\n", j.id, status, elapsed.Round(time.Millisecond), j.command)
	if dropped > 0 {
		fmt.Fprintf(&report, "[%d earlier lines dropped; the job printed more than the %d byte cap]\n", dropped, maxJobOutputBytes)
	}
	if len(lines) > 0 {
		report.WriteString(strings.Join(lines, "\n"))
		report.WriteString("\n")
	} else if total == 0 {
		report.WriteString("(no output yet)\n")
	} else {
		report.WriteString("(no new output)\n")
	}

	switch status {
	case JobRunning:
		fmt.Fprintf(&report, "[still running; poll job_status with sinceLine %d, or set wait to block up to %ds]", next, maxJobWaitSeconds)
	case JobTimeout:
		fmt.Fprintf(&report, "[timed out after %ds and its process tree was terminated]", j.timeout)
	case JobKilled:
		report.WriteString("[killed on request]")
	default:
		fmt.Fprintf(&report, "[finished with exit code %d]", exitCode)
	}

	// Spelling out the next call removes the guesswork about which arguments
	// this tool takes, which is the part callers most often get wrong.
	nextCall := ""
	switch {
	case status == JobRunning:
		nextCall = fmt.Sprintf(`job_status {"jobId":%q,"sinceLine":%d,"wait":%d}`, j.id, next, maxJobWaitSeconds)
	case next <= total:
		nextCall = fmt.Sprintf(`job_status {"jobId":%q,"sinceLine":%d}`, j.id, next)
	}
	if nextCall != "" {
		fmt.Fprintf(&report, "\nNext call: %s", nextCall)
	}

	text := truncateOutput(report.String())
	return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, JobStatusOutput{
			JobID:        j.id,
			Status:       status,
			ExitCode:     exitCode,
			Command:      j.command,
			ElapsedMS:    elapsed.Milliseconds(),
			Output:       strings.Join(lines, "\n"),
			NextLine:     next,
			TotalLines:   total,
			DroppedLines: dropped,
			NextCall:     nextCall,
			Result:       text,
		}, nil
}

// JobKillInput represents the input for the job_kill tool.
type JobKillInput struct {
	JobID       string `json:"jobId" jsonschema:"Job identifier to stop."`
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"Ignored. Jobs are addressed by jobId alone, but this is accepted so that passing the same workspaceId used for the shell tool is never rejected as an unexpected property."`
}

// JobKillOutput represents the output for the job_kill tool.
type JobKillOutput struct {
	JobID  string `json:"jobId" jsonschema:"Job identifier."`
	Status string `json:"status" jsonschema:"State of the job after the request."`
	Result string `json:"result" jsonschema:"Human readable result message."`
}

// JobKill stops a background job and everything it started.
func JobKill(ctx context.Context, req *mcp.CallToolRequest, input JobKillInput) (*mcp.CallToolResult, JobKillOutput, error) {
	j, ok := backgroundJobs.lookup(strings.TrimSpace(input.JobID))
	if !ok {
		result := &mcp.CallToolResult{}
		result.SetError(fmt.Errorf("no background job %q; call job_list to see the jobs this server is tracking", input.JobID))
		return result, JobKillOutput{}, nil
	}

	message := fmt.Sprintf("Stopped %s and its process tree.", j.id)
	if !j.kill() {
		status, exitCode, _, _ := j.state()
		message = fmt.Sprintf("%s had already finished (%s, exit code %d); nothing to stop.", j.id, status, exitCode)
	} else {
		// Give the supervisor a moment to record the kill so the reported
		// status is the one the caller will see on the next poll.
		timer := time.NewTimer(exitGrace)
		defer timer.Stop()
		select {
		case <-j.done:
		case <-timer.C:
		}
	}

	status, _, _, _ := j.state()
	return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: message}},
		}, JobKillOutput{
			JobID:  j.id,
			Status: status,
			Result: message,
		}, nil
}

// JobListInput represents the input for the job_list tool.
type JobListInput struct {
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"Ignored. Jobs are tracked per server process, not per workspace, but this is accepted so that passing the same workspaceId used for the shell tool is never rejected as an unexpected property."`
}

// JobSummary describes one tracked job.
type JobSummary struct {
	JobID      string `json:"jobId" jsonschema:"Job identifier."`
	Status     string `json:"status" jsonschema:"running, exited, killed, or timeout."`
	ExitCode   int    `json:"exitCode" jsonschema:"Exit code once the job has finished; -1 while it is still running."`
	Command    string `json:"command" jsonschema:"The command this job is running."`
	ElapsedMS  int64  `json:"elapsedMs" jsonschema:"How long the job ran, in milliseconds."`
	TotalLines int    `json:"totalLines" jsonschema:"Total lines the job has printed so far."`
}

// JobListOutput represents the output for the job_list tool.
type JobListOutput struct {
	Jobs   []JobSummary `json:"jobs" jsonschema:"Tracked jobs, newest first."`
	Result string       `json:"result" jsonschema:"Human readable summary."`
}

// JobList reports the background jobs this server is tracking.
func JobList(ctx context.Context, req *mcp.CallToolRequest, input JobListInput) (*mcp.CallToolResult, JobListOutput, error) {
	tracked := backgroundJobs.list()

	summaries := make([]JobSummary, 0, len(tracked))
	lines := make([]string, 0, len(tracked))
	for _, j := range tracked {
		status, exitCode, _, _ := j.state()
		_, _, total, _ := j.sink.snapshot(1, 1)
		elapsed := j.elapsed()

		summaries = append(summaries, JobSummary{
			JobID:      j.id,
			Status:     status,
			ExitCode:   exitCode,
			Command:    j.command,
			ElapsedMS:  elapsed.Milliseconds(),
			TotalLines: total,
		})
		lines = append(lines, fmt.Sprintf("%s  %s  exit=%d  %s  %d line(s)  $ %s",
			j.id, status, exitCode, elapsed.Round(time.Millisecond), total, singleLine(j.command)))
	}

	text := strings.Join(lines, "\n")
	if text == "" {
		text = "No background jobs. Start one by calling the shell tool with background set to true."
	}
	text = truncateOutput(text)

	return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, JobListOutput{
			Jobs:   summaries,
			Result: text,
		}, nil
}

// singleLine collapses a multi-line command so one job occupies one row.
func singleLine(command string) string {
	flat := strings.Join(strings.Fields(strings.ReplaceAll(command, "\n", " ")), " ")
	if len(flat) > 120 {
		return flat[:117] + "..."
	}
	return flat
}
