#!/bin/sh
# Stop the service before the files go away, so systemd does not keep restarting
# a binary that is no longer there.
set -e

if [ -d /run/systemd/system ]; then
	systemctl stop deckhand.service >/dev/null 2>&1 || true
	systemctl disable deckhand.service >/dev/null 2>&1 || true
fi
