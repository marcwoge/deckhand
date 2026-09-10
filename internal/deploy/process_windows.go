//go:build windows

package deploy

import (
	"os/exec"
	"strconv"
	"strings"
)

// processAlive asks the task list, since Windows has no signal 0.
func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		// Without an answer, assume the holder is alive: refusing to deploy is
		// safer than two deployments at once.
		return true
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
