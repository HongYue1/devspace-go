package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// startTestJob runs a command in the background and stops it when the test
// ends, so a stuck job cannot outlive the test binary.
func startTestJob(t *testing.T, command string) BashOutput {
	t.Helper()

	res, out, err := RunBash(context.Background(), nil, BashInput{
		Command:    command,
		Background: true,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("RunBash returned an error: %v", err)
	}
	if res.IsError {
		t.Fatalf("starting a background job failed: %s", out.Result)
	}
	if out.JobID == "" {
		t.Fatal("a background command must return a jobId")
	}

	t.Cleanup(func() {
		if j, ok := backgroundJobs.lookup(out.JobID); ok {
			j.kill()
		}
	})
	return out
}

func TestBackgroundCommandReturnsBeforeTheCommandFinishes(t *testing.T) {
	start := time.Now()
	out := startTestJob(t, "sleep 5")

	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("starting a background job blocked for %s; it must return at once", elapsed)
	}
	if out.Status != JobRunning {
		t.Fatalf("status = %q, want %q", out.Status, JobRunning)
	}
}

func TestJobStatusWaitsForTheJobInsteadOfSleeping(t *testing.T) {
	out := startTestJob(t, "echo hello-from-job")

	res, status, err := JobStatus(context.Background(), nil, JobStatusInput{
		JobID: out.JobID,
		Wait:  maxJobWaitSeconds,
	})
	if err != nil {
		t.Fatalf("JobStatus returned an error: %v", err)
	}
	if res.IsError {
		t.Fatalf("JobStatus failed: %s", status.Result)
	}

	if status.Status != JobExited {
		t.Fatalf("status = %q, want %q; waiting must not answer before the job ends", status.Status, JobExited)
	}
	if status.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", status.ExitCode)
	}
	if !strings.Contains(status.Output, "hello-from-job") {
		t.Errorf("output %q does not contain what the command printed", status.Output)
	}
	if status.NextLine <= 1 {
		t.Errorf("nextLine = %d; it must advance past the lines already returned", status.NextLine)
	}
}

func TestJobStatusReturnsOnlyNewOutput(t *testing.T) {
	out := startTestJob(t, "echo first-line")

	_, first, err := JobStatus(context.Background(), nil, JobStatusInput{
		JobID: out.JobID,
		Wait:  maxJobWaitSeconds,
	})
	if err != nil {
		t.Fatalf("JobStatus returned an error: %v", err)
	}
	if !strings.Contains(first.Output, "first-line") {
		t.Fatalf("first read %q does not contain the output", first.Output)
	}

	_, second, err := JobStatus(context.Background(), nil, JobStatusInput{
		JobID:     out.JobID,
		SinceLine: first.NextLine,
	})
	if err != nil {
		t.Fatalf("JobStatus returned an error: %v", err)
	}
	if second.Output != "" {
		t.Errorf("reading from the cursor returned %q again; a poll must not repeat output", second.Output)
	}
}

func TestJobKillStopsARunningJobAndIsSafeToRepeat(t *testing.T) {
	out := startTestJob(t, "sleep 10")

	res, killed, err := JobKill(context.Background(), nil, JobKillInput{JobID: out.JobID})
	if err != nil {
		t.Fatalf("JobKill returned an error: %v", err)
	}
	if res.IsError {
		t.Fatalf("JobKill failed: %s", killed.Result)
	}
	if killed.Status == JobRunning {
		t.Fatalf("status = %q; the job should no longer be running", killed.Status)
	}

	res, again, err := JobKill(context.Background(), nil, JobKillInput{JobID: out.JobID})
	if err != nil {
		t.Fatalf("JobKill returned an error: %v", err)
	}
	if res.IsError {
		t.Fatalf("stopping a finished job must be reported, not an error: %s", again.Result)
	}
}

func TestJobListReportsAStartedJob(t *testing.T) {
	out := startTestJob(t, "sleep 5")

	_, list, err := JobList(context.Background(), nil, JobListInput{})
	if err != nil {
		t.Fatalf("JobList returned an error: %v", err)
	}

	for _, summary := range list.Jobs {
		if summary.JobID == out.JobID {
			return
		}
	}
	t.Fatalf("job %s is missing from job_list:\n%s", out.JobID, list.Result)
}

func TestTimeoutPastTheForegroundCeilingBecomesAJob(t *testing.T) {
	res, out, err := RunBash(context.Background(), nil, BashInput{
		Command: "echo promoted-marker",
		Timeout: maxSyncCommandTimeout + 60,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("RunBash returned an error: %v", err)
	}
	if res.IsError {
		t.Fatalf("RunBash failed: %s", out.Result)
	}

	if out.JobID == "" {
		t.Fatal("a timeout past the foreground ceiling must become a background job, not a request the client abandons")
	}
	t.Cleanup(func() {
		if j, ok := backgroundJobs.lookup(out.JobID); ok {
			j.kill()
		}
	})
}

func TestJobStatusRejectsAnUnknownJob(t *testing.T) {
	res, _, err := JobStatus(context.Background(), nil, JobStatusInput{JobID: "job_does_not_exist"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown jobId must be reported as an error")
	}
}

func TestNormalizeJobTimeoutAppliesTheDocumentedDefaultAndCap(t *testing.T) {
	cases := []struct {
		requested int
		want      int
	}{
		{0, defaultJobTimeout},
		{-5, defaultJobTimeout},
		{45, 45},
		{maxJobTimeout + 1, maxJobTimeout},
	}

	for _, c := range cases {
		if got := normalizeJobTimeout(c.requested); got != c.want {
			t.Errorf("normalizeJobTimeout(%d) = %d, want %d", c.requested, got, c.want)
		}
	}
}

func TestLineSinkStreamsFromACursor(t *testing.T) {
	sink := &lineSink{}
	sink.Write([]byte("one\ntwo\r\n"))

	lines, next, total, dropped := sink.snapshot(1, 0)
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("lines = %q; want one and two without the carriage return", lines)
	}
	if total != 2 || dropped != 0 {
		t.Fatalf("total = %d, dropped = %d; want 2 and 0", total, dropped)
	}

	sink.Write([]byte("three"))
	sink.flush()

	lines, _, total, _ = sink.snapshot(next, 0)
	if len(lines) != 1 || lines[0] != "three" {
		t.Fatalf("lines after the cursor = %q; want only the new line", lines)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3; a final line without a newline must still be stored", total)
	}
}

func TestLineSinkDropsOldestOutputAndKeepsNumberingMonotonic(t *testing.T) {
	sink := &lineSink{}
	line := strings.Repeat("x", 1024)
	for i := 0; i < 2000; i++ {
		sink.Write([]byte(fmt.Sprintf("%s\n", line)))
	}

	_, next, total, dropped := sink.snapshot(1, maxJobLines)
	if dropped == 0 {
		t.Fatal("2 MB of output must drop the oldest lines instead of growing without bound")
	}
	if total != 2000 {
		t.Errorf("total = %d, want 2000; the count must include dropped lines", total)
	}
	if next <= dropped {
		t.Errorf("next = %d must sit past the %d dropped lines", next, dropped)
	}
}
