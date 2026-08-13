//go:build windows

package tools

import (
	"os/exec"
	"strconv"
)

// configureProcessGroup is a no-op on Windows. Grandchildren are reached with
// taskkill /T rather than a process group signal.
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessTree stops the shell and everything it started.
//
// Killing only the shell left grandchildren alive holding the output pipe, so
// the read blocked long after the timeout had fired.
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	_ = kill.Run()
	_ = cmd.Process.Kill()
}
