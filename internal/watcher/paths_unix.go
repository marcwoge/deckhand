//go:build !windows

package watcher

import (
	"os"
	"path/filepath"
)

func systemStateDir() string { return "/var/lib/deckhand" }

// userStateDir is where a per-user install keeps its state. XDG_STATE_HOME is
// honoured because this is exactly what it is for, and ~/.local/state is its
// documented default.
func userStateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "deckhand")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "deckhand")
	}
	return filepath.Join(os.TempDir(), "deckhand")
}
