#!/bin/sh
# Build a signed apt and dnf repository, ready to be served by GitHub Pages.
#
# This is what turns "download a .deb" into "apt install deckhand": one source
# line, and from then on the package is upgraded by the machine's own package
# manager along with everything else.
#
# The price is a signing key. apt verifies a repository by the signature over its
# Release file and refuses an unsigned one - rightly, since the repository is
# what tells a machine which files to install as root. That key is long-lived,
# which is the opposite of the keyless cosign signatures on the releases
# themselves; docs/en/security.md says what each of the two protects.
#
#   ./packaging/build-repo.sh --pool dist --site site
#
# Requirements: gpg, apt-ftparchive (apt-utils), createrepo_c
#   sudo apt-get install -y apt-utils createrepo-c gnupg

set -eu

POOL=""
SITE="site"
BASE_URL="https://marcwoge.github.io/deckhand"
KEY_ID=""
KEEP=5

while [ $# -gt 0 ]; do
	case "$1" in
	--pool) POOL="${2:?--pool needs the directory holding the new packages}"; shift ;;
	--site) SITE="${2:?--site needs an output directory}"; shift ;;
	--base-url) BASE_URL="${2:?--base-url needs a URL}"; shift ;;
	--key-id) KEY_ID="${2:?--key-id needs a key id or e-mail}"; shift ;;
	--keep) KEEP="${2:?--keep needs a number of versions}"; shift ;;
	-h|--help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown option $1" >&2; exit 2 ;;
	esac
	shift
done

die() { echo "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is not installed (apt-get install -y apt-utils createrepo-c gnupg)"; }

need gpg
need apt-ftparchive
need createrepo_c

gpg --list-secret-keys ${KEY_ID:+"$KEY_ID"} >/dev/null 2>&1 ||
	die "no secret key${KEY_ID:+ for $KEY_ID} in this keyring; nothing could be signed"

mkdir -p "$SITE/deb" "$SITE/rpm"

# New packages join whatever the site already carries, so several versions stay
# installable and "apt install deckhand=0.1.1" keeps working.
if [ -n "$POOL" ]; then
	found=0
	for f in "$POOL"/*.deb; do
		[ -f "$f" ] || continue
		cp -f "$f" "$SITE/deb/"
		found=$((found + 1))
	done
	for f in "$POOL"/*.rpm; do
		[ -f "$f" ] || continue
		cp -f "$f" "$SITE/rpm/"
		found=$((found + 1))
	done
	echo "added $found package(s) from $POOL"
fi

# --- keep the pool from growing forever -------------------------------------
# Versions, newest last. sort -V understands "0.1.2~dev" sorting before "0.1.2",
# which is the whole point of that spelling.
versions() {
	# $1 directory, $2 extension, $3 field separator position
	for f in "$1"/*."$2"; do
		[ -f "$f" ] || continue
		basename "$f"
	done | sed "$3" | sort -uV
}

prune() {
	# $1 directory, $2 extension, $3 sed expression yielding the version
	keep_list="$(versions "$1" "$2" "$3" | tail -n "$KEEP")"
	[ -n "$keep_list" ] || return 0
	for f in "$1"/*."$2"; do
		[ -f "$f" ] || continue
		v="$(basename "$f" | sed "$3")"
		if ! printf '%s\n' "$keep_list" | grep -qxF "$v"; then
			echo "  pruning $(basename "$f")"
			rm -f "$f"
		fi
	done
}

echo "keeping the newest $KEEP version(s):"
# deckhand_0.1.2_amd64.deb -> 0.1.2
prune "$SITE/deb" deb 's/^[^_]*_\(.*\)_[^_]*\.deb$/\1/'
# deckhand-0.1.2-1.x86_64.rpm -> 0.1.2
prune "$SITE/rpm" rpm 's/^[^-]*-\(.*\)-[0-9]*\.[^.]*\.rpm$/\1/'

# --- the apt repository ------------------------------------------------------
# A flat repository: Packages and Release sit next to the .deb files, so the
# source line needs no suite or component and there is no dists/ tree to keep
# consistent. Every architecture lives in the one Packages file.
echo "indexing the apt repository"
(
	cd "$SITE/deb"
	apt-ftparchive packages . > Packages
	# -n leaves the name and timestamp out, so identical input gives an
	# identical file and the published site does not churn on every build.
	gzip -9ncf Packages > Packages.gz
	arches="$(awk '/^Architecture: /{print $2}' Packages | sort -u | tr '\n' ' ')"
	[ -n "$arches" ] || arches="amd64"
	# Remove the previous run's files first: apt-ftparchive hashes what it finds
	# in the directory, and a stale Release would end up listed inside the new
	# one.
	rm -f Release InRelease Release.gpg
	apt-ftparchive \
		-o APT::FTPArchive::Release::Origin=Deckhand \
		-o APT::FTPArchive::Release::Label=Deckhand \
		-o APT::FTPArchive::Release::Suite=stable \
		-o APT::FTPArchive::Release::Codename=stable \
		-o APT::FTPArchive::Release::Architectures="$arches" \
		-o APT::FTPArchive::Release::Description="Deckhand deployment worker" \
		release . > Release.tmp
	mv Release.tmp Release

	# InRelease is what current apt fetches; Release.gpg is kept for older apt.
	gpg --batch --yes ${KEY_ID:+--local-user "$KEY_ID"} --clearsign -o InRelease Release
	gpg --batch --yes ${KEY_ID:+--local-user "$KEY_ID"} --detach-sign --armor -o Release.gpg Release
	echo "  architectures: $arches"
)

# --- the dnf repository -----------------------------------------------------
echo "indexing the dnf repository"
createrepo_c --quiet --update "$SITE/rpm"
rm -f "$SITE/rpm/repodata/repomd.xml.asc"
gpg --batch --yes ${KEY_ID:+--local-user "$KEY_ID"} \
	--detach-sign --armor -o "$SITE/rpm/repodata/repomd.xml.asc" \
	"$SITE/rpm/repodata/repomd.xml"

# --- the key, in both shapes people need ------------------------------------
gpg --armor --export ${KEY_ID:+"$KEY_ID"} > "$SITE/deckhand.asc"
# apt wants a keyring, not armour, under /usr/share/keyrings.
gpg --dearmor < "$SITE/deckhand.asc" > "$SITE/deckhand-archive-keyring.gpg"
[ -s "$SITE/deckhand.asc" ] || die "the public key came out empty"

cat > "$SITE/deckhand.sources" <<SOURCES
Types: deb
URIs: $BASE_URL/deb
Suites: ./
Signed-By: /usr/share/keyrings/deckhand-archive-keyring.gpg
SOURCES

cat > "$SITE/deckhand.repo" <<REPO
[deckhand]
name=Deckhand
baseurl=$BASE_URL/rpm
enabled=1
# The packages are signed, and so is the repository metadata.
gpgcheck=1
repo_gpgcheck=1
gpgkey=$BASE_URL/deckhand.asc
REPO

# --- a landing page, because the URL will be pasted into a browser ----------
KEY_FPR="$(gpg --with-colons --fingerprint ${KEY_ID:+"$KEY_ID"} 2>/dev/null |
	awk -F: '/^fpr:/{print $10; exit}')"
DEB_VERSIONS="$(versions "$SITE/deb" deb 's/^[^_]*_\(.*\)_[^_]*\.deb$/\1/' | tr '\n' ' ')"

cat > "$SITE/index.html" <<PAGE
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Deckhand package repository</title>
<style>
  :root { color-scheme: light dark; --fg: #1a1a1a; --bg: #fff; --muted: #555;
          --code-bg: #f4f4f5; --border: #e4e4e7; }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e8e8e8; --bg: #131316; --muted: #a1a1aa;
            --code-bg: #1e1e22; --border: #2e2e34; }
  }
  * { box-sizing: border-box; }
  body { margin: 0 auto; padding: 2.5rem 1rem 4rem; max-width: 46rem;
         font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         color: var(--fg); background: var(--bg); }
  h1 { font-size: 1.6rem; margin: 0 0 .25rem; }
  h2 { font-size: 1.1rem; margin: 2.2rem 0 .6rem; }
  p.lead { color: var(--muted); margin: 0 0 2rem; }
  pre { background: var(--code-bg); border: 1px solid var(--border);
        border-radius: 6px; padding: .85rem 1rem; overflow-x: auto;
        font-size: .875rem; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  footer { margin-top: 3rem; padding-top: 1rem; border-top: 1px solid var(--border);
           color: var(--muted); font-size: .875rem; }
  a { color: inherit; }
</style>
</head>
<body>
<h1>Deckhand package repository</h1>
<p class="lead">Watch GitHub repositories, pull new revisions, run your command.
Add this repository once and Deckhand is upgraded by your package manager along
with everything else.</p>

<h2>Debian, Ubuntu</h2>
<pre><code>sudo curl -fsSLo /usr/share/keyrings/deckhand-archive-keyring.gpg \\
  $BASE_URL/deckhand-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/deckhand-archive-keyring.gpg] $BASE_URL/deb ./" |
  sudo tee /etc/apt/sources.list.d/deckhand.list
sudo apt update
sudo apt install deckhand</code></pre>

<h2>Fedora, RHEL, openSUSE</h2>
<pre><code>sudo curl -fsSLo /etc/yum.repos.d/deckhand.repo $BASE_URL/deckhand.repo
sudo dnf install deckhand</code></pre>

<h2>What you are trusting</h2>
<p>The repository metadata is signed with this key, and the packages are signed
with it too:</p>
<pre><code>$KEY_FPR</code></pre>
<p>That key exists because apt and dnf verify a repository before installing
anything as root. The release binaries themselves are signed separately and
without any key at all, using <a href="https://docs.sigstore.dev">cosign</a> in
keyless mode — see
<a href="https://github.com/marcwoge/deckhand/blob/main/docs/en/security.md">security.md</a>.
Prefer not to add a third-party repository? Every release also carries the plain
<code>.deb</code>, <code>.rpm</code> and binaries.</p>

<p>Versions currently in the repository: <code>$DEB_VERSIONS</code></p>

<footer>
<a href="https://github.com/marcwoge/deckhand">Deckhand on GitHub</a> ·
<a href="https://github.com/marcwoge/deckhand/tree/main/docs/en">Documentation</a> ·
<a href="https://github.com/marcwoge/deckhand/tree/main/docs/de">Deutsch</a> ·
Apache-2.0
</footer>
</body>
</html>
PAGE

# GitHub Pages runs Jekyll by default, which would drop files it considers
# special and mangle nothing useful here.
: > "$SITE/.nojekyll"

echo
echo "the site is in $SITE:"
echo "  apt:  $BASE_URL/deb          ($(ls -1 "$SITE/deb"/*.deb 2>/dev/null | wc -l) packages)"
echo "  dnf:  $BASE_URL/rpm          ($(ls -1 "$SITE/rpm"/*.rpm 2>/dev/null | wc -l) packages)"
echo "  key:  $BASE_URL/deckhand.asc ($KEY_FPR)"
