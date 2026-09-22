#!/bin/sh
# Runs after the files are removed.
#
# The configuration, the credentials and the state are left alone on purpose:
# they are yours, they cannot be regenerated, and an upgrade runs this too.
# So is the deckhand user, which may still own deployment directories.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

case "$1" in
purge)
	cat <<'MESSAGE'
Deckhand is removed. Left behind on purpose, because only you know whether they
are still wanted:
  /etc/deckhand        configuration and credentials
  /var/lib/deckhand    state and the audit log
  the "deckhand" user, which may own deployment directories
MESSAGE
	;;
esac
