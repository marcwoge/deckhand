//go:build windows

package watcher

import (
	"os"
	"path/filepath"
)

func systemStateDir() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "deckhand", "state")
	}
	return filepath.Join(os.TempDir(), "deckhand")
}

// userStateDir is where a per-user install keeps its state.
//
// Not ~/.local/state, which is a unix convention: on Windows that puts a
// dotted directory in the profile root, where nothing else lives and no backup
// or roaming policy expects it. LOCALAPPDATA is the place for per-machine user
// state, and it is where the configuration already defaults to its
// counterpart under ProgramData.
func userStateDir() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "deckhand", "state")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "AppData", "Local", "deckhand", "state")
	}
	return systemStateDir()
}
