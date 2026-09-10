//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child into its own process group so a timeout can
// take down everything it spawned, not just the direct child.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
