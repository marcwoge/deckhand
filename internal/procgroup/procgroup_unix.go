//go:build !windows

package procgroup

import (
	"os/exec"
	"syscall"
)

// Setup puts the child into a process group of its own, so that signalling the
// group reaches everything it starts.
func Setup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Kill terminates the child and its group.
func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		// No group, or it is already gone: fall back to the process itself.
		return cmd.Process.Kill()
	}
	return nil
}
