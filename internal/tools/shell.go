package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// defaultCommandTimeout is applied when the caller does not ask for one.
	defaultCommandTimeout = 30
	// maxCommandTimeout caps what a caller may ask for.
	maxCommandTimeout = 300
	// exitGrace bounds the wait for a killed process tree to be reaped, so an
	// unkillable grandchild cannot hold the call open.
	exitGrace = 3 * time.Second
)

// commandResult describes a finished command, including any output captured
// before a timeout cut it short.
type commandResult struct {
	output      string
	exitCode    int
	timedOut    bool
	startFailed bool
	err         error
}

// safeBuffer guards the buffer because output is read after a timeout, while
// the goroutine copying from the command may still be writing into it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runCommand runs a command and always returns, even when the command leaves
// a child process holding the output pipe.
//
// CombinedOutput could not do that. It waits for the pipe to close, and
// exec.CommandContext kills only the shell it started, so a dev server or any
// other grandchild kept the pipe open and the call hung until that process
// was killed by hand.
func runCommand(ctx context.Context, cwd, name string, args []string, timeoutSec int) commandResult {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	configureProcessGroup(cmd)

	var buf safeBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return commandResult{exitCode: -1, startFailed: true, err: err}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()

	select {
	case err := <-done:
		return commandResult{output: buf.String(), exitCode: exitCodeOf(err), err: err}
	case <-timer.C:
		killProcessTree(cmd)
		reap(done)
		return commandResult{output: buf.String(), exitCode: -1, timedOut: true}
	case <-ctx.Done():
		killProcessTree(cmd)
		reap(done)
		return commandResult{output: buf.String(), exitCode: -1, err: ctx.Err()}
	}
}

// reap waits briefly for a killed process to be collected.
func reap(done <-chan error) {
	select {
	case <-done:
	case <-time.After(exitGrace):
	}
}

// exitCodeOf reports the code the command actually exited with.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// normalizeTimeout applies the documented default and cap.
func normalizeTimeout(requested int) int {
	if requested <= 0 {
		return defaultCommandTimeout
	}
	if requested > maxCommandTimeout {
		return maxCommandTimeout
	}
	return requested
}

// timeoutReport explains the timeout and keeps whatever the command printed
// before it was stopped, which is usually the interesting part.
func timeoutReport(output string, timeoutSec int) string {
	report := fmt.Sprintf("Command timed out after %ds; its process tree was terminated.", timeoutSec)
	if strings.TrimSpace(output) == "" {
		return report + "\n(no output before the timeout)"
	}
	return report + "\n\n" + truncateOutput(output)
}

// powerShellArgs builds the argument list for powershell.exe.
func powerShellArgs(command string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-Command", wrapPowerShellCommand(command)}
}

// wrapPowerShellCommand makes PowerShell report the exit code of the last
// native command it ran.
//
// powershell.exe -Command flattens any native failure to 1, so a command that
// exited 3 was reported as 1, and the status it reports for a pipeline is the
// status of the last element, so a pipe closed early by Select-Object -First
// could turn a command that succeeded into a failure. Running the command in
// a script block and exiting with $LASTEXITCODE reports the real code in both
// cases.
//
// PowerShell errors that are not native command failures still surface as
// text rather than as an exit code, which is how -Command reports them too.
func wrapPowerShellCommand(command string) string {
	if strings.TrimSpace(command) == "" {
		return command
	}
	return "$global:LASTEXITCODE = 0\n& {\n" + command + "\n}\nexit $LASTEXITCODE"
}
