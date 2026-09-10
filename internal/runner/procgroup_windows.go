//go:build windows

package runner

import (
	"os/exec"
	"strconv"
)

func setProcessGroup(cmd *exec.Cmd) {}

// killGroup uses taskkill so child processes are terminated as well.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
