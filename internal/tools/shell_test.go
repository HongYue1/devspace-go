package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func perOS(windows, unix string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return unix
}

func runBash(t *testing.T, command string, timeout int) (*mcp.CallToolResult, BashOutput) {
	t.Helper()

	result, out, err := RunBash(context.Background(), nil, BashInput{
		Command: command,
		Timeout: timeout,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("RunBash returned a transport error: %v", err)
	}
	return result, out
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if result == nil || len(result.Content) == 0 {
		t.Fatal("expected the result to carry content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}

// TestRunBashReportsTheRealExitCode covers PowerShell flattening every native
// failure to 1, which hid what the command actually returned.
func TestRunBashReportsTheRealExitCode(t *testing.T) {
	_, out := runBash(t, perOS("cmd /c exit 3", "exit 3"), 30)

	if !strings.Contains(out.Result, "[exit code: 3]") {
		t.Fatalf("expected the real exit code 3, got:\n%s", out.Result)
	}
}

// TestRunBashKeepsASuccessfulPipelineSuccessful covers a pipeline whose reader
// closes the pipe early, which was reported as a failure even though the
// command had done its job.
func TestRunBashKeepsASuccessfulPipelineSuccessful(t *testing.T) {
	result, out := runBash(t, perOS(
		"cmd /c echo hello | Select-Object -First 1",
		"printf 'hello\\nworld\\n' | head -1",
	), 30)

	if !strings.Contains(out.Result, "hello") {
		t.Fatalf("expected the pipeline output, got:\n%s", out.Result)
	}
	if strings.Contains(out.Result, "[exit code:") {
		t.Fatalf("a successful pipeline should not report a failure, got:\n%s", out.Result)
	}
	if result.IsError {
		t.Fatal("a successful pipeline should not be an error result")
	}
}

// TestRunBashTimesOutInsteadOfHanging covers a command that keeps running.
// The call used to block until the process was killed by hand, because the
// shell was killed but its children kept the output pipe open.
//
// The timeout is generous because the shell has to start, print, and still be
// running when the timer fires; a tight budget makes the test measure shell
// startup rather than the timeout.
func TestRunBashTimesOutInsteadOfHanging(t *testing.T) {
	started := time.Now()
	result, out := runBash(t, perOS(
		"[Console]::Out.WriteLine('started'); [Console]::Out.Flush(); Start-Sleep -Seconds 60",
		"echo started; sleep 60",
	), 5)
	elapsed := time.Since(started)

	if elapsed > 30*time.Second {
		t.Fatalf("the call should return shortly after the timeout, took %s", elapsed)
	}
	if !result.IsError {
		t.Fatal("a timeout should be reported as an error")
	}
	if !strings.Contains(out.Result, "timed out after 5s") {
		t.Fatalf("expected a timeout message, got:\n%s", out.Result)
	}
	if !strings.Contains(out.Result, "started") {
		t.Fatalf("expected the output printed before the timeout, got:\n%s", out.Result)
	}
	if resultText(t, result) != out.Result {
		t.Fatal("the text content and the structured output should agree")
	}
}

func TestNormalizeTimeoutAppliesTheDocumentedDefaultAndCap(t *testing.T) {
	cases := []struct {
		requested int
		want      int
	}{
		{requested: 0, want: 30},
		{requested: -5, want: 30},
		{requested: 7, want: 7},
		{requested: 5000, want: 300},
	}

	for _, tc := range cases {
		if got := normalizeTimeout(tc.requested); got != tc.want {
			t.Errorf("normalizeTimeout(%d) = %d, want %d", tc.requested, got, tc.want)
		}
	}
}

func TestWrapPowerShellCommandLeavesAnEmptyCommandAlone(t *testing.T) {
	if got := wrapPowerShellCommand("   "); got != "   " {
		t.Fatalf("an empty command should be passed through, got %q", got)
	}
}

func TestWrapPowerShellCommandReturnsTheLastNativeExitCode(t *testing.T) {
	wrapped := wrapPowerShellCommand("git status")

	if !strings.Contains(wrapped, "exit $LASTEXITCODE") {
		t.Fatalf("the wrapper should end by exiting with the real code, got:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "git status") {
		t.Fatalf("the wrapper should keep the command intact, got:\n%s", wrapped)
	}
}
