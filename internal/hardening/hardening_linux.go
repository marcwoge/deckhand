//go:build linux

// Package hardening applies the process-level protections that only make sense
// at runtime: a worker holding deployment tokens should not be readable by
// another process running as the same user, and should not write those tokens
// into a core dump.
package hardening

import (
	"fmt"
	"syscall"
)

// prSetDumpable is PR_SET_DUMPABLE from linux/prctl.h. It is not in the
// standard library's constants, and pulling in golang.org/x/sys for one number
// is not worth a second dependency.
const prSetDumpable = 4

// Apply clears the dumpable flag. With it clear, the kernel refuses a core dump
// of this process and refuses ptrace from another process of the same user - so
// reading Deckhand's memory needs root, which could read the token files
// anyway.
//
// It is not a sandbox: root can still do everything. It closes the cheapest
// path to a token, which is another process of the same user attaching to this
// one.
func Apply() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return fmt.Errorf("PR_SET_DUMPABLE: %w", errno)
	}
	return nil
}
