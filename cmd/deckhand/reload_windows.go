//go:build windows

package main

import (
	"context"

	"github.com/marcwoge/deckhand/internal/watcher"
)

// Windows has no SIGHUP. Restart the scheduled task to pick up configuration
// changes; see docs/en/services.md.
func watchForReload(context.Context, *watcher.Engine) {}
