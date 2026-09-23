#!/bin/sh
# Collect the .deb and .rpm files of the most recent releases into one directory.
#
# The published repository is rebuilt from scratch on every release, so it needs
# the older packages from somewhere. They are already on GitHub as release assets,
# which makes the releases the archive and the repository just an index over them
# - no second copy to keep, and nothing accumulating in git history forever.
#
#   ./packaging/fetch-release-packages.sh --pool pool --keep 5
#
# Set GH_TOKEN (or GITHUB_TOKEN) to avoid the unauthenticated API rate limit.

set -eu

POOL="pool"
KEEP=5
REPO="marcwoge/deckhand"

while [ $# -gt 0 ]; do
	case "$1" in
	--pool) POOL="${2:?--pool needs a directory}"; shift ;;
	--keep) KEEP="${2:?--keep needs a number of releases}"; shift ;;
	--repo) REPO="${2:?--repo needs owner/name}"; shift ;;
	-h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown option $1" >&2; exit 2 ;;
	esac
	shift
done

TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

api() {
	if [ -n "$TOKEN" ]; then
		curl -fsSL -H "Authorization: Bearer $TOKEN" \
			-H "Accept: application/vnd.github+json" "$1"
	else
		curl -fsSL -H "Accept: application/vnd.github+json" "$1"
	fi
}

mkdir -p "$POOL"

# Releases come back newest first. Prereleases and drafts are left out: a stable
# repository should not hand someone a release candidate because it sorts higher.
releases="$(api "https://api.github.com/repos/$REPO/releases?per_page=30" |
	python3 -c '
import json, sys
data = json.load(sys.stdin)
for release in data:
    if release.get("draft") or release.get("prerelease"):
        continue
    urls = [
        asset["browser_download_url"]
        for asset in release.get("assets", [])
        if asset["name"].endswith((".deb", ".rpm"))
    ]
    if urls:
        print(release["tag_name"] + " " + " ".join(urls))
')"

[ -n "$releases" ] || { echo "no release carries packages yet"; exit 0; }

count=0
printf '%s\n' "$releases" | while read -r line; do
	count=$((count + 1))
	[ "$count" -le "$KEEP" ] || break
	tag="${line%% *}"
	echo "$tag"
	for url in ${line#* }; do
		name="${url##*/}"
		if [ -f "$POOL/$name" ]; then
			echo "  have $name"
			continue
		fi
		curl -fsSL -o "$POOL/$name" "$url" && echo "  got $name"
	done
done

echo
echo "$(ls -1 "$POOL" | wc -l) file(s) in $POOL"
