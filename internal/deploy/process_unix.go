//go:build !windows

package deploy

import (
	"os"
	"syscall"
)

// processAlive reports whether a pid belongs to a running process. Signal 0
// performs the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to someone else.
	return err == syscall.EPERM
}
