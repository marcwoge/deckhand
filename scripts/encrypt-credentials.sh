#!/bin/sh
# Move Deckhand's credential files into systemd's encrypted credential store.
#
# Nothing about the configuration format has changed, so this is an upgrade, not
# a required migration - see docs/en/upgrading.md. What it buys is real, though:
# a credential encrypted with systemd-creds is bound to this machine's TPM or
# host key, so the file on disk is worthless if copied elsewhere, and the
# plaintext only ever exists in a tmpfs that the service alone can read.
#
# For every credential file in the configuration it
#   1. encrypts it to /etc/deckhand/<name>.cred with systemd-creds,
#   2. writes a systemd drop-in with the matching LoadCredentialEncrypted lines,
#   3. points the configuration at ${CREDENTIALS_DIRECTORY}/<name>,
#   4. verifies the result with "deckhand check" and undoes everything if that
#      fails.
#
# It never deletes a plaintext credential: that is the one step a mistake makes
# unrecoverable, so it prints the commands and leaves them to you.
#
# Usage:
#   sudo ./scripts/encrypt-credentials.sh                 # show what would happen
#   sudo ./scripts/encrypt-credentials.sh --apply
#   sudo ./scripts/encrypt-credentials.sh --apply --config /etc/deckhand/deckhand.yaml

set -eu

CONFIG=""
DECKHAND=""
UNIT="deckhand.service"
DROPIN_DIR="/etc/systemd/system/deckhand.service.d"
CRED_DIR="/etc/deckhand"
APPLY=0

while [ $# -gt 0 ]; do
	case "$1" in
	--apply) APPLY=1 ;;
	--config) CONFIG="${2:?--config needs a path}"; shift ;;
	--deckhand) DECKHAND="${2:?--deckhand needs a path}"; shift ;;
	--unit) UNIT="${2:?--unit needs a name}"; shift ;;
	--cred-dir) CRED_DIR="${2:?--cred-dir needs a path}"; shift ;;
	--dropin-dir) DROPIN_DIR="${2:?--dropin-dir needs a path}"; shift ;;
	-h|--help) sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown option $1" >&2; exit 2 ;;
	esac
	shift
done

die() { echo "error: $*" >&2; exit 1; }

[ -n "$DECKHAND" ] || DECKHAND="$(command -v deckhand || true)"
[ -n "$DECKHAND" ] || die "deckhand not found in PATH; pass --deckhand /path/to/deckhand"
command -v systemd-creds >/dev/null 2>&1 ||
	die "systemd-creds not found; it needs systemd 250 or newer"

# Resolve the config the way deckhand itself does, so the file this script edits
# is the file deckhand reads.
if [ -z "$CONFIG" ]; then
	CONFIG="$("$DECKHAND" check 2>/dev/null | sed -n '1s/^config \(.*\) is valid$/\1/p')"
	[ -n "$CONFIG" ] || die "cannot tell which config is in use; pass --config"
fi
[ -w "$CONFIG" ] || die "$CONFIG is not writable; run with sudo"

echo "config:      $CONFIG"
echo "unit:        $UNIT"
echo "credentials: $CRED_DIR/<name>.cred"
[ "$APPLY" -eq 1 ] || echo "mode:        dry run (pass --apply to make the changes)"
echo

# name<TAB>kind<TAB>path<TAB>mode, one credential per line.
CREDS="$("$DECKHAND" secrets --tsv --config "$CONFIG")" ||
	die "deckhand secrets failed; fix the configuration first"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PLAN="$WORK/plan"
: > "$PLAN"

# One file can back more than one credential - a token shared by two watches.
# Encrypt it once, under the first name, and point both at that.
printf '%s\n' "$CREDS" | while IFS='	' read -r name kind path mode; do
	[ "$kind" = "file" ] || continue
	[ -n "$name" ] && [ -n "$path" ] || continue
	case "$path" in
	*CREDENTIALS_DIRECTORY*) continue ;;
	esac
	if cut -f2 "$PLAN" | grep -qxF "$path"; then
		echo "  $path is already planned under another name, reusing it"
		continue
	fi
	[ -r "$path" ] || { echo "  skipping $name: cannot read $path"; continue; }
	printf '%s\t%s\n' "$name" "$path" >> "$PLAN"
done

if [ ! -s "$PLAN" ]; then
	echo "no credential files to encrypt."
	echo "Credentials from an environment variable or a command are not affected;"
	echo "docs/en/security.md says what each source protects against."
	exit 0
fi

echo "will encrypt:"
while IFS='	' read -r name path; do
	echo "  $name  <-  $path"
done < "$PLAN"
echo

if [ "$APPLY" -eq 0 ]; then
	echo "Nothing changed. Re-run with --apply."
	exit 0
fi

STAMP="$(date +%Y%m%d%H%M%S)"
BACKUP="$CONFIG.bak.$STAMP"
cp -p "$CONFIG" "$BACKUP"
echo "backup:      $BACKUP"

mkdir -p "$CRED_DIR" "$DROPIN_DIR"
chmod 700 "$CRED_DIR" 2>/dev/null || true

DROPIN="$DROPIN_DIR/credentials.conf"
DROPIN_EXISTED=0
if [ -f "$DROPIN" ]; then
	DROPIN_EXISTED=1
	cp -p "$DROPIN" "$DROPIN.bak.$STAMP"
fi

restore() {
	echo "rolling back the configuration" >&2
	cp -p "$BACKUP" "$CONFIG"
	if [ "$DROPIN_EXISTED" -eq 1 ]; then
		cp -p "$DROPIN.bak.$STAMP" "$DROPIN"
	else
		rm -f "$DROPIN"
	fi
	systemctl daemon-reload 2>/dev/null || true
}

{
	echo "# Written by scripts/encrypt-credentials.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ)."
	echo "# Each credential is decrypted into \$CREDENTIALS_DIRECTORY, a tmpfs only"
	echo "# this service can read. The .cred files are bound to this machine."
	echo "[Service]"
} > "$DROPIN"

NEW_CONFIG="$WORK/config"
cp -p "$CONFIG" "$NEW_CONFIG"

while IFS='	' read -r name path; do
	out="$CRED_DIR/$name.cred"
	if ! systemd-creds encrypt --name="$name" "$path" "$out"; then
		restore
		die "systemd-creds could not encrypt $path (no TPM? try --with-key=host)"
	fi
	chmod 600 "$out"
	echo "LoadCredentialEncrypted=$name:$out" >> "$DROPIN"
	# Replace the exact path that came out of the configuration, nothing else.
	awk -v old="$path" -v new="\${CREDENTIALS_DIRECTORY}/$name" '
		{ i = index($0, old); if (i > 0) $0 = substr($0, 1, i-1) new substr($0, i+length(old)); print }
	' "$NEW_CONFIG" > "$NEW_CONFIG.tmp" && mv "$NEW_CONFIG.tmp" "$NEW_CONFIG"
	echo "  encrypted $name"
done < "$PLAN"

cp -p "$NEW_CONFIG" "$CONFIG"
systemctl daemon-reload 2>/dev/null || true

# Verify the way the service will see it: decrypt into a directory of our own and
# let deckhand check read the credentials from there.
echo
echo "verifying:"
VERIFY="$WORK/credentials"
mkdir -p "$VERIFY"
chmod 700 "$VERIFY"
ok=1
while IFS='	' read -r name path; do
	systemd-creds decrypt --name="$name" "$CRED_DIR/$name.cred" "$VERIFY/$name" || ok=0
	[ -f "$VERIFY/$name" ] && chmod 600 "$VERIFY/$name"
done < "$PLAN"
if [ "$ok" -ne 1 ]; then
	restore
	die "an encrypted credential could not be decrypted again; the configuration was restored"
fi
if ! CREDENTIALS_DIRECTORY="$VERIFY" "$DECKHAND" check --config "$CONFIG" > /dev/null; then
	restore
	die "deckhand check failed with the encrypted credentials; the configuration was restored"
fi
echo "  deckhand check passes with the encrypted credentials"

echo
echo "done. Two things are left for you, on purpose:"
echo
echo "1. Restart the service so it picks up the credentials:"
echo "     systemctl restart $UNIT && systemctl status $UNIT"
echo
echo "2. Once it is running, remove the plaintext credentials. This is not done"
echo "   automatically because it cannot be undone:"
while IFS='	' read -r name path; do
	echo "     shred -u $path        # or: rm $path"
done < "$PLAN"
echo
echo "The previous configuration is kept at $BACKUP."
