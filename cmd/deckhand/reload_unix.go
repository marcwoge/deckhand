//go:build !windows

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/marcwoge/deckhand/internal/watcher"
)

// watchForReload re-reads the configuration on SIGHUP:
//
//	systemctl reload deckhand    or    kill -HUP $(pidof deckhand)
func watchForReload(ctx context.Context, eng *watcher.Engine) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hup)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if err := eng.Reload(ctx); err != nil {
					log.Printf("[config] %v", err)
				}
			}
		}
	}()
}
