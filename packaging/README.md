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
