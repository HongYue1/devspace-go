//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the shell in its own process group so that every
// process it starts can be signalled at once.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree signals the whole process group.
//
// Killing only the shell left grandchildren alive holding the output pipe, so
// the read blocked long after the timeout had fired.
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
