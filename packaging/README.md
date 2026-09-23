# Packaging

Everything needed to turn the release binaries into packages. The release
workflow runs the two scripts here and nothing else, so what CI publishes is
what you get locally.

| File | What it is |
|---|---|
| `nfpm.yaml` | the `.deb` and `.rpm` contents, for [nfpm](https://nfpm.goreleaser.com) |
| `deckhand.service` | the systemd unit the packages install, in the vendor location |
| `scripts/postinstall.sh` | creates the `deckhand` account, `/etc/deckhand`, `/var/lib/deckhand`, prints what to do next |
| `scripts/preremove.sh` | stops and disables the service before the files go |
| `scripts/postremove.sh` | reloads systemd; keeps configuration, state and the account |
| `build-packages.sh` | builds every `.deb` and `.rpm` from binaries in `dist/` |
| `generate-manifests.sh` | writes the Homebrew formula and the Scoop manifest with real checksums |
| `build-repo.sh` | builds the signed apt and dnf repository for GitHub Pages |
| `fetch-release-packages.sh` | collects recent releases' packages, so the repository can be rebuilt from scratch |
| `tap/` | the contents of the `homebrew-deckhand` repository |
| `bucket/` | the contents of the `scoop-deckhand` repository |

## Building locally

```bash
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0

TAG=v0.1.1
for arch in amd64 arm64 arm; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$TAG" \
    -o "dist/deckhand_${TAG}_linux_${arch}" ./cmd/deckhand
done

./packaging/build-packages.sh --tag "$TAG"
./packaging/generate-manifests.sh --tag "$TAG"     # needs the darwin and windows binaries too
```

## The Homebrew tap and the Scoop bucket

`tap/` and `bucket/` are the complete contents of two small repositories. They
are kept here so they are versioned with the thing they install, but they have to
live in repositories of their own — Homebrew requires the name
`homebrew-<tap>`, and a Scoop bucket is a repository you add by URL.

Create `marcwoge/homebrew-deckhand` and `marcwoge/scoop-deckhand` on GitHub
(public, no README, no licence — the contents below bring their own), then:

```bash
# the tap
cp -r packaging/tap ~/homebrew-deckhand
cd ~/homebrew-deckhand
git init -b main
git add .
git commit -m "The Deckhand formula"
git remote add origin https://github.com/marcwoge/homebrew-deckhand.git
git push -u origin main

# the bucket
cp -r packaging/bucket ~/scoop-deckhand
cd ~/scoop-deckhand
git init -b main
git add .
git commit -m "The Deckhand manifest"
git remote add origin https://github.com/marcwoge/scoop-deckhand.git
git push -u origin main
```

Then check that it works:

```bash
brew tap marcwoge/deckhand && brew install deckhand
```

```powershell
scoop bucket add deckhand https://github.com/marcwoge/scoop-deckhand
scoop install deckhand
```

### They keep themselves current

Each repository carries a workflow that runs daily, regenerates its file from the
newest release and commits only if something changed. Two properties are worth
knowing:

* **It needs no secret.** A repository's own `GITHUB_TOKEN` may push to that
  repository, which is why the updater lives in the tap and the bucket rather
  than in Deckhand's release workflow — a cross-repository push would need a
  personal access token, stored forever, with write access to both.
* **It verifies before it trusts.** The hashes come from the release's own
  `SHA256SUMS`, and cosign checks the signature over that file first. If the
  signature does not verify, the formula is not updated. So the chain is: the
  release workflow signs → the tap checks the signature and copies the hashes →
  Homebrew or Scoop checks the hash of what it downloads.

Both also accept a tag from the Actions tab (**Run workflow → tag**), which is
how you pick up a release immediately instead of waiting for the daily run, or
pin back to an older one.

The logic itself is not duplicated: both workflows check out this repository and
run `generate-manifests.sh --from-release`, so there is one generator and the
local `--tag` mode is the same code path.

## The apt and dnf repository

`build-repo.sh` turns the packages into a signed apt and dnf repository that
GitHub Pages serves. That is the difference between "download a .deb" and

```bash
sudo apt install deckhand
```

with upgrades arriving through the machine's own package manager from then on.

### How it is published

The release workflow, after publishing the release itself:

1. `fetch-release-packages.sh --keep 5` downloads the `.deb` and `.rpm` assets of
   the five most recent stable releases. **The releases are the archive**; the
   repository is only an index over them. Nothing accumulates in git history, and
   `apt install deckhand=0.1.1` keeps working for as long as that release is
   within the window.
2. `build-repo.sh` writes the index, signs it, and adds the key, the `.repo` file
   and a landing page.
3. The result is deployed to Pages as an artifact — no `gh-pages` branch, so the
   repository size does not grow by a few megabytes per release forever.

Everything degrades cleanly without the signing secret: the packages are built
unsigned, the repository is not published, and the release itself is unaffected.

### The signing key, and why it exists at all

apt verifies a repository by the signature over its `Release` file and refuses an
unsigned one. It is right to: the repository is what tells a machine which files
to install as root. So this needs a **long-lived GPG key** — the opposite of the
keyless cosign signature over `SHA256SUMS`, which exists precisely so that no key
has to be kept.

Both are real, and they protect different things:

| | protects | key |
|---|---|---|
| cosign over `SHA256SUMS` | that a release came from this repository's workflow | none, keyless |
| GPG over `Release` / `repomd.xml` | that the repository a machine polls is ours | long-lived |

Creating it, once, on a machine you trust:

```bash
# RSA rather than ed25519: older rpm builds do not verify EdDSA reliably.
gpg --batch --passphrase '' --quick-generate-key \
  "Deckhand Package Signing <you@example.com>" rsa4096 sign never

gpg --armor --export-secret-keys "Deckhand Package Signing" > deckhand-signing-key.asc
```

Then paste the contents of `deckhand-signing-key.asc` into the repository secret
**`REPO_GPG_PRIVATE_KEY`** (Settings → Secrets and variables → Actions), back the
file up somewhere offline, and delete it from the machine.

Two deliberate choices:

* **No passphrase.** A passphrase stored in the same secret store as the key
  protects nothing. The workflow refuses to start signing if the key cannot sign
  unattended, rather than failing halfway through publishing.
* **Sign-only, no expiry.** An expiring repository key breaks `apt update` on
  every machine that added it, at a moment nobody chose. Rotation is a deliberate
  act: generate a new key, publish it, and keep signing with both for a while.

If the key ever leaks, whoever has it can serve packages to everyone who added
this repository. Revoke it (`gpg --gen-revoke`), publish the revocation, generate
a new one, and say so in a release note.

### Building and testing it locally

The whole thing runs without GitHub, which is how it was verified:

```bash
sudo apt-get install -y apt-utils createrepo-c gnupg
export GNUPGHOME=/tmp/gk && mkdir -p $GNUPGHOME && chmod 700 $GNUPGHOME
gpg --batch --passphrase '' --quick-generate-key "Test Signing <t@example.invalid>" rsa4096 sign never
gpg --armor --export-secret-keys "Test Signing" > /tmp/key.asc

SIGNING_KEY_FILE=/tmp/key.asc ./packaging/build-packages.sh --tag v0.1.3 --arches amd64:amd64
./packaging/build-repo.sh --pool dist --site site \
  --base-url http://127.0.0.1:8099 --key-id "Test Signing"

python3 -m http.server 8099 --directory site &
sudo install -m644 site/deckhand-archive-keyring.gpg /usr/share/keyrings/
echo "deb [signed-by=/usr/share/keyrings/deckhand-archive-keyring.gpg] http://127.0.0.1:8099/deb ./" |
  sudo tee /etc/apt/sources.list.d/deckhand.list
sudo apt update && sudo apt install deckhand
```

Keep `$GNUPGHOME` short: gpg-agent's socket lives inside it and a path near 108
characters fails with "No agent running", which is a memorable way to lose an
hour.

Worth repeating the negative test too — append a line to `site/deb/Packages`,
clear `/var/lib/apt/lists/127.0.0.1*` so apt actually refetches, and
`apt update` must refuse it with a hash mismatch. Without clearing the list apt
serves the cached index and the test silently proves nothing.

## Decisions worth knowing

**The package never starts the service.** A deployment worker with no
configuration has nothing to deploy, and one that quietly starts while holding
deploy credentials is not a package anybody wants. `postinstall` prints the three
steps to a running service instead.

**Nothing in `/etc` is a conffile.** The configuration is written by the
operator, never by the package, so there is nothing for dpkg to prompt about on
upgrade — and nothing that can be overwritten.

**The unit goes to `/usr/lib/systemd/system`.** That is the vendor location, so a
drop-in under `/etc/systemd/system/deckhand.service.d/` or a full replacement at
`/etc/systemd/system/deckhand.service` — which is what
`deckhand service install --system` writes — both win, and survive upgrades.

**Removal keeps the credentials, the state and the account.** They cannot be
regenerated, an upgrade runs the same script, and the account may own your
deployment directories.

**32-bit arm is `armhf`.** nfpm needs the variant spelled out (`arm7`) to name it
that; plain `arm` would produce a package for an architecture neither dpkg nor
rpm knows. `build-packages.sh` maps Go's `arm` to it.

**The manifests are generated before `SHA256SUMS`.** Both end up in the signed
checksum file, so a tampered formula is caught by the same check as a tampered
binary.
