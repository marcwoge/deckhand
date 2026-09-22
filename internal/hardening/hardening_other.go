//go:build !linux

package hardening

// Apply does nothing on platforms without PR_SET_DUMPABLE. macOS and Windows
// have equivalents (PT_DENY_ATTACH, process mitigation policies), but both need
// per-platform system calls and neither is as clearly a win as the Linux flag,
// so they are left for a platform owner to add.
func Apply() error { return nil }
