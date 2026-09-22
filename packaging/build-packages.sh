#!/bin/sh
# Build the .deb and .rpm packages from binaries already in dist/.
#
# The release workflow calls this, and so can you: everything it needs is nfpm
# and the built binaries, so a package can be reproduced locally before it is
# ever published.
#
#   go build -o dist/deckhand_v0.1.1_linux_amd64 ./cmd/deckhand
#   ./packaging/build-packages.sh --tag v0.1.1
#
# Requirements: nfpm (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0)

set -eu

TAG=""
DIST="dist"
# "<go arch>:<package arch>". 32-bit arm is armhf in a package, and nfpm wants the
# variant spelled out to get there; plain "arm" would produce a package for an
# architecture neither dpkg nor rpm knows.
ARCHES="amd64:amd64 arm64:arm64 arm:arm7"

while [ $# -gt 0 ]; do
	case "$1" in
	--tag) TAG="${2:?--tag needs a version like v0.1.1}"; shift ;;
	--dist) DIST="${2:?--dist needs a directory}"; shift ;;
	--arches) ARCHES="${2:?--arches needs a list of <go arch>:<package arch>}"; shift ;;
	-h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown option $1" >&2; exit 2 ;;
	esac
	shift
done

[ -n "$TAG" ] || { echo "error: --tag is required" >&2; exit 2; }
NFPM="$(command -v nfpm || true)"
if [ -z "$NFPM" ] && command -v go >/dev/null 2>&1; then
	# "go install" puts it in GOPATH/bin, which is not always on PATH.
	candidate="$(go env GOPATH)/bin/nfpm"
	[ -x "$candidate" ] && NFPM="$candidate"
fi
[ -n "$NFPM" ] || {
	echo "error: nfpm not found. Install it with:" >&2
	echo "  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0" >&2
	exit 1
}

# Package versions carry no leading "v": both dpkg and rpm sort it as part of the
# version and it would make 0.10.0 look older than 0.9.0.
VERSION="${TAG#v}"

mkdir -p build
trap 'rm -rf build' EXIT

for pair in $ARCHES; do
	arch="${pair%%:*}"
	pkgarch="${pair##*:}"
	binary="$DIST/deckhand_${TAG}_linux_${arch}"
	if [ ! -f "$binary" ]; then
		echo "skipping $arch: no $binary"
		continue
	fi
	# nfpm expands environment variables in its own fields but not inside
	# "contents", so the binary is staged at a fixed path instead.
	install -m 0755 "$binary" build/deckhand
	for packager in deb rpm; do
		VERSION="$VERSION" ARCH="$pkgarch" \
			"$NFPM" package --config packaging/nfpm.yaml --packager "$packager" --target "$DIST/"
	done
done

echo
echo "built:"
ls -1 "$DIST"/*.deb "$DIST"/*.rpm 2>/dev/null || echo "  nothing - were the binaries built?"
