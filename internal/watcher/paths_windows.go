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
