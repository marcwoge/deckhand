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
