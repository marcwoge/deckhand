#!/bin/sh
# Install or update Deckhand from its signed GitHub releases.
#
# Deckhand keeps everything else current but is itself updated by hand, which
# means security fixes land late - the worst kind of late. A built-in
# "deckhand self-update" is issue #15; until then this script does the same job
# from the outside, with the same verification.
#
# A deployment worker that updates itself is a supply-chain path to every machine
# it runs on, so verification is not optional here: the checksum file is checked
# against its cosign signature, which is bound to this repository's release
# workflow and recorded in the public transparency log. Checking SHA256SUMS
# alone would prove nothing - it comes from the same place as the binary.
#
# Usage:
#   sudo ./scripts/install-release.sh                     # latest release
#   sudo ./scripts/install-release.sh --version v0.1.0    # a specific one, also the way back
#   sudo ./scripts/install-release.sh --check             # is an update available? exit 1 if yes
#   sudo ./scripts/install-release.sh --dest /usr/local/bin/deckhand --unit deckhand.service
#
# Requirements: curl (or wget), sha256sum (or shasum), and cosign
#   https://docs.sigstore.dev/system_config/installation/

set -eu

REPO="marcwoge/deckhand"
IDENTITY="https://github.com/$REPO/.github/workflows/release.yml@.*"
ISSUER="https://token.actions.githubusercontent.com"

VERSION=""
DEST=""
UNIT="deckhand.service"
RESTART="auto"
CHECK_ONLY=0
FORCE=0

while [ $# -gt 0 ]; do
	case "$1" in
	--version) VERSION="${2:?--version needs a tag}"; shift ;;
	--dest) DEST="${2:?--dest needs a path}"; shift ;;
	--unit) UNIT="${2:?--unit needs a name}"; shift ;;
	--restart) RESTART="${2:?--restart needs auto|systemctl|none}"; shift ;;
	--check) CHECK_ONLY=1 ;;
	--force) FORCE=1 ;;
	-h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown option $1" >&2; exit 2 ;;
	esac
	shift
done

die() { echo "error: $*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
	# $1 url, $2 output
	if have curl; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "neither curl nor wget is installed"
	fi
}

sha256() {
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "neither sha256sum nor shasum is installed"
	fi
}

# Where is the binary now?
if [ -z "$DEST" ]; then
	DEST="$(command -v deckhand || true)"
	[ -n "$DEST" ] || DEST="/usr/local/bin/deckhand"
fi

CURRENT="none"
if [ -x "$DEST" ]; then
	CURRENT="$("$DEST" version 2>/dev/null | sed -n '1s/^deckhand \([^ ]*\).*/\1/p')"
	[ -n "$CURRENT" ] || CURRENT="unknown"
fi

# Which release?
if [ -z "$VERSION" ]; then
	api="https://api.github.com/repos/$REPO/releases/latest"
	tmp="$(mktemp)"
	fetch "$api" "$tmp" || die "cannot reach the GitHub API"
	VERSION="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp" | head -1)"
	rm -f "$tmp"
	[ -n "$VERSION" ] || die "cannot determine the latest release"
fi

echo "installed: $CURRENT"
echo "available: $VERSION"

if [ "$CURRENT" = "$VERSION" ] && [ "$FORCE" -eq 0 ]; then
	echo "already up to date."
	exit 0
fi
if [ "$CHECK_ONLY" -eq 1 ]; then
	echo "an update is available."
	# Non-zero so a monitoring check can act on it.
	exit 1
fi

have cosign || die "cosign is required to verify the release.
Install it from https://docs.sigstore.dev/system_config/installation/ - an
unverified update mechanism on a deployment worker is worse than none."

case "$(uname -s)" in
Linux) os="linux" ;;
Darwin) os="darwin" ;;
*) die "unsupported system $(uname -s); on Windows use the release page" ;;
esac
case "$(uname -m)" in
x86_64|amd64) arch="amd64" ;;
aarch64|arm64) arch="arm64" ;;
armv6l|armv7l|arm) arch="arm" ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

ASSET="deckhand_${VERSION}_${os}_${arch}"
BASE="https://github.com/$REPO/releases/download/$VERSION"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "downloading $ASSET"
fetch "$BASE/$ASSET" "$WORK/$ASSET" || die "no release asset $ASSET in $VERSION"
fetch "$BASE/SHA256SUMS" "$WORK/SHA256SUMS" || die "no SHA256SUMS in $VERSION"
fetch "$BASE/SHA256SUMS.bundle" "$WORK/SHA256SUMS.bundle" ||
	die "no SHA256SUMS.bundle in $VERSION - this release is not signed, refusing"

echo "verifying the signature"
cosign verify-blob "$WORK/SHA256SUMS" \
	--bundle "$WORK/SHA256SUMS.bundle" \
	--certificate-identity-regexp "$IDENTITY" \
	--certificate-oidc-issuer "$ISSUER" ||
	die "the checksum file is not signed by $REPO's release workflow; refusing"

echo "verifying the checksum"
want="$(grep " \{1,2\}\*\{0,1\}${ASSET}\$" "$WORK/SHA256SUMS" | cut -d' ' -f1 | head -1)"
[ -n "$want" ] || die "$ASSET is not listed in SHA256SUMS"
got="$(sha256 "$WORK/$ASSET")"
[ "$want" = "$got" ] || die "checksum mismatch for $ASSET: expected $want, got $got"
echo "  signature and checksum are good"

# The new binary must at least be able to describe itself before it replaces a
# working one.
chmod 755 "$WORK/$ASSET"
"$WORK/$ASSET" version >/dev/null 2>&1 || die "the downloaded binary does not run on this machine"

dir="$(dirname "$DEST")"
[ -w "$dir" ] || die "$dir is not writable; run with sudo"

PREVIOUS=""
if [ -e "$DEST" ]; then
	PREVIOUS="$DEST.$CURRENT"
	# On unix the running file can be renamed while the process runs, because
	# the kernel keeps the open inode. Keeping it means a rollback needs no
	# network access.
	mv "$DEST" "$PREVIOUS"
fi
if ! cp "$WORK/$ASSET" "$DEST"; then
	[ -n "$PREVIOUS" ] && mv "$PREVIOUS" "$DEST"
	die "could not install $DEST; the previous binary is back in place"
fi
chmod 755 "$DEST"
echo "installed $VERSION to $DEST"
[ -n "$PREVIOUS" ] && echo "  previous binary kept as $PREVIOUS"

if ! "$DEST" version >/dev/null 2>&1; then
	if [ -n "$PREVIOUS" ]; then
		mv "$PREVIOUS" "$DEST"
		die "the new binary does not run; rolled back to $CURRENT"
	fi
	die "the new binary does not run"
fi

# The configuration must still be valid for the new version, or the service will
# not come back up.
if "$DEST" check >/dev/null 2>&1; then
	echo "  deckhand check still passes"
else
	echo "  warning: deckhand check does not pass with this version - run it and read the output" >&2
fi

case "$RESTART" in
none) echo "not restarting (--restart none)" ;;
systemctl|auto)
	if have systemctl && systemctl list-unit-files "$UNIT" >/dev/null 2>&1 &&
		systemctl is-active --quiet "$UNIT" 2>/dev/null; then
		echo "restarting $UNIT"
		systemctl restart "$UNIT" || die "could not restart $UNIT; roll back with: mv $PREVIOUS $DEST"
		sleep 2
		systemctl is-active --quiet "$UNIT" ||
			die "$UNIT did not come back up. Roll back with: mv $PREVIOUS $DEST && systemctl restart $UNIT"
		echo "  $UNIT is running $VERSION"
	elif [ "$RESTART" = "systemctl" ]; then
		die "$UNIT is not an active systemd unit"
	else
		echo "no active $UNIT found; restart Deckhand yourself so it picks up the new binary"
	fi
	;;
*) die "--restart takes auto, systemctl or none" ;;
esac
