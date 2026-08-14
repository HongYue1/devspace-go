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

	"github.com/snakex21/devspace-go/internal/shells"
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

// shellSelection is the shell the bash tool runs, resolved once from the
// configured preference.
//
// Resolution is deliberately forgiving: a preference naming a shell that is
// not installed falls back to the best detected shell rather than disabling
// the bash tool, and the reason is kept so startup output, the doctor command
// and the tool description can all report it.
type shellSelection struct {
	shell    shells.Shell
	fallback string
	err      error
}

var (
	selectionMu sync.Mutex
	selection   *shellSelection
)

// currentShell returns the resolved selection, resolving on first use so that
// a caller which never calls SetShell still gets a working shell.
func currentShell() shellSelection {
	selectionMu.Lock()
	defer selectionMu.Unlock()
	if selection == nil {
		resolved := computeSelection(configuredShell)
		selection = &resolved
	}
	return *selection
}

func setSelection(sel shellSelection) {
	selectionMu.Lock()
	defer selectionMu.Unlock()
	selection = &sel
}

func computeSelection(preference string) shellSelection {
	sh, err := shells.Resolve(preference)
	if err == nil {
		return shellSelection{shell: sh}
	}

	pref := strings.ToLower(strings.TrimSpace(preference))
	if pref == "" || pref == "auto" {
		return shellSelection{err: err}
	}

	auto, autoErr := shells.Resolve("auto")
	if autoErr != nil {
		return shellSelection{err: err}
	}
	return shellSelection{shell: auto, fallback: err.Error()}
}

// shellArgs hands the command to the chosen shell the way that shell expects.
func shellArgs(sh shells.Shell, command string) []string {
	switch sh.Kind {
	case shells.KindPowerShell:
		return powerShellArgs(command)
	case shells.KindCmd:
		return []string{"/C", command}
	default:
		return []string{"-c", command}
	}
}

// ShellStatus reports the shell the bash tool will use and, when the
// configured one could not be used, why.
func ShellStatus() (label string, fallback string, err error) {
	sel := currentShell()
	if sel.err != nil {
		return "", "", sel.err
	}
	return sel.shell.Label(), sel.fallback, nil
}

// ShellHint describes the chosen shell for the bash tool description, so the
// caller is told which syntax actually works instead of being told PowerShell
// unconditionally.
func ShellHint() string {
	sel := currentShell()
	if sel.err != nil {
		return "No supported shell was found on this machine, so this tool cannot run commands."
	}
	switch sel.shell.Kind {
	case shells.KindPowerShell:
		return fmt.Sprintf("Commands run in %s (%s), where && is not a command separator (use ;) and 2>&1 is not valid redirection.", sel.shell.ID, sel.shell.Path)
	case shells.KindCmd:
		return fmt.Sprintf("Commands run in %s (%s), where && chains commands and 2>&1 redirects.", sel.shell.ID, sel.shell.Path)
	default:
		return fmt.Sprintf("Commands run in %s (%s), a POSIX shell where && and 2>&1 both work.", sel.shell.ID, sel.shell.Path)
	}
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
