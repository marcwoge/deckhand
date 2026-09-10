//go:build !windows

package watcher

func systemStateDir() string { return "/var/lib/deckhand" }
