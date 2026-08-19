package tools

import (
	"context"
	"strings"
	"testing"
)

// TestJobToolsAcceptTheWorkspaceIdTheShellRequires removes a trap that cost a
// real session several retries: bash demands a workspaceId, so passing the same
// argument to the job tools is the obvious next move, and it used to be
// rejected as an unexpected property.
func TestJobToolsAcceptTheWorkspaceIdTheShellRequires(t *testing.T) {
	job := startTestJob(t, "echo hello-from-job")

	res, status, err := JobStatus(context.Background(), nil, JobStatusInput{
		JobID:       job.JobID,
		WorkspaceID: "ws_does_not_matter",
		Wait:        maxJobWaitSeconds,
	})
	if err != nil {
		t.Fatalf("job_status rejected a workspaceId: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("job_status rejected a workspaceId: %s", status.Result)
	}
	if status.JobID != job.JobID {
		t.Errorf("jobId = %q, want %q", status.JobID, job.JobID)
	}

	listRes, _, err := JobList(context.Background(), nil, JobListInput{WorkspaceID: "ws_does_not_matter"})
	if err != nil {
		t.Fatalf("job_list rejected a workspaceId: %v", err)
	}
	if listRes != nil && listRes.IsError {
		t.Fatal("job_list rejected a workspaceId")
	}

	killRes, _, err := JobKill(context.Background(), nil, JobKillInput{
		JobID:       job.JobID,
		WorkspaceID: "ws_does_not_matter",
	})
	if err != nil {
		t.Fatalf("job_kill rejected a workspaceId: %v", err)
	}
	if killRes != nil && killRes.IsError {
		t.Fatal("job_kill rejected a workspaceId")
	}
}

// TestBackgroundReportSpellsOutTheFollowUpCall covers the text every job hands
// back, including the auto-background case, which renders through the same
// report.
func TestBackgroundReportSpellsOutTheFollowUpCall(t *testing.T) {
	job := startTestJob(t, "sleep 5")

	if !strings.Contains(job.Result, "job_status") {
		t.Errorf("the report does not name the follow-up tool:\n%s", job.Result)
	}
	// Whitespace inside the rendered call is not the contract. Naming the jobId
	// argument, carrying the real id and showing the wait argument are, so the
	// comparison ignores spacing.
	compact := strings.ReplaceAll(job.Result, " ", "")
	if !strings.Contains(compact, "\"jobId\":\""+job.JobID+"\"") {
		t.Errorf("the report does not spell out the exact call shape:\n%s", job.Result)
	}
	if !strings.Contains(compact, "\"wait\":") {
		t.Errorf("the report does not show the wait argument:\n%s", job.Result)
	}
}

func TestJobStatusSuggestsTheNextCallWhileRunning(t *testing.T) {
	job := startTestJob(t, "sleep 5")

	_, status, err := JobStatus(context.Background(), nil, JobStatusInput{JobID: job.JobID})
	if err != nil {
		t.Fatalf("JobStatus returned an error: %v", err)
	}

	if status.Status != JobRunning {
		t.Skipf("the job already finished with status %q", status.Status)
	}
	if status.NextCall == "" {
		t.Fatal("nextCall is empty while the job is still running")
	}
	if !strings.Contains(status.NextCall, "job_status") || !strings.Contains(status.NextCall, job.JobID) {
		t.Errorf("nextCall = %q, want a ready to use job_status call", status.NextCall)
	}
	if !strings.Contains(status.Result, "job_status") {
		t.Errorf("result does not repeat the follow-up call:\n%s", status.Result)
	}
}

// TestJobStatusStopsSuggestingACallOnceEverythingIsRead keeps the hint honest,
// so an idle poll loop has a clear place to stop.
func TestJobStatusStopsSuggestingACallOnceEverythingIsRead(t *testing.T) {
	job := startTestJob(t, "echo hello-from-job")

	_, status, err := JobStatus(context.Background(), nil, JobStatusInput{
		JobID: job.JobID,
		Wait:  maxJobWaitSeconds,
	})
	if err != nil {
		t.Fatalf("JobStatus returned an error: %v", err)
	}
	if status.Status == JobRunning {
		t.Skip("the job was still running after the maximum wait")
	}
	if status.NextCall != "" {
		t.Errorf("nextCall = %q, want nothing once the job is done and drained", status.NextCall)
	}
}
