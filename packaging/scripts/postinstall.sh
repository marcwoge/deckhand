#!/bin/sh
# Runs after the package is unpacked, on both deb and rpm.
#
# It deliberately does not enable or start the service: without a configuration
# Deckhand has nothing to deploy, and a package that quietly starts a worker
# holding deploy credentials is not a package anybody wants.
set -e

USER_NAME="deckhand"
CONFIG_DIR="/etc/deckhand"
STATE_DIR="/var/lib/deckhand"

if ! getent passwd "$USER_NAME" >/dev/null 2>&1; then
	# A system account with no login, no home and no shell: it needs to read its
	# config, write its state and run the configured commands, nothing else.
	useradd --system --no-create-home --home-dir /var/lib/deckhand \
		--shell /usr/sbin/nologin --comment "Deckhand deployment worker" \
		"$USER_NAME" 2>/dev/null ||
		useradd --system --no-create-home --home-dir /var/lib/deckhand \
			--shell /sbin/nologin --comment "Deckhand deployment worker" \
			"$USER_NAME"
fi

# Root owns the configuration; the service user may read it. Deckhand refuses to
# start with a config that group or others can write, so this is the shape it
# wants.
mkdir -p "$CONFIG_DIR"
chown root:"$USER_NAME" "$CONFIG_DIR" 2>/dev/null || true
chmod 750 "$CONFIG_DIR"

# systemd would create this on first start (StateDirectory=), but "deckhand
# check" run by hand before the first start needs it too - and finding the state
# directory missing is a confusing way to learn that.
mkdir -p "$STATE_DIR"
chown "$USER_NAME":"$USER_NAME" "$STATE_DIR" 2>/dev/null || true
chmod 750 "$STATE_DIR"

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ ! -f "$CONFIG_DIR/deckhand.yaml" ]; then
	cat <<'MESSAGE'

Deckhand is installed but not configured, and not running.

  1. Write a configuration:
       sudo deckhand init --config /etc/deckhand/deckhand.yaml
       sudo chown root:deckhand /etc/deckhand/deckhand.yaml
       sudo chmod 640 /etc/deckhand/deckhand.yaml

     Then set the state directory in it, so a command you run by hand reads the
     same state as the service:
       defaults:
         state_dir: /var/lib/deckhand

  2. Put your GitHub token somewhere the worker can read and you can rotate:
       sudo install -o root -g deckhand -m 640 /dev/null /etc/deckhand/github-token
       sudo sh -c 'printf %s "github_pat_..." > /etc/deckhand/github-token'
     then in the configuration:
       github:
         token_file: /etc/deckhand/github-token

  3. Check it, then start it:
       sudo -u deckhand deckhand check --config /etc/deckhand/deckhand.yaml
       sudo -u deckhand deckhand doctor --config /etc/deckhand/deckhand.yaml
       sudo systemctl enable --now deckhand

Documentation: /usr/share/doc/deckhand/  ·  https://github.com/marcwoge/deckhand

MESSAGE
fi
