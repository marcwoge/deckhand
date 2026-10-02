//go:build windows

package procgroup

import (
	"os/exec"
	"strconv"
)

// Setup has nothing to do on Windows: there are no process groups to join, and
// the tree is walked at kill time instead.
func Setup(cmd *exec.Cmd) {}

// Kill terminates the child and everything below it. /T is the part that
// matters: without it a "cmd /c" wrapper dies and leaves what it started
// running, which is exactly how a hung command survives its own timeout.
func Kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
