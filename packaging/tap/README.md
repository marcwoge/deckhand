# Deckhand for Homebrew

A Homebrew tap for [Deckhand](https://github.com/marcwoge/deckhand) — a small
worker that watches your GitHub repositories, pulls each new release or commit
into its own directory, runs the command you configured, and rolls back when the
health check fails.

```bash
brew tap marcwoge/deckhand
brew install marcwoge/deckhand/deckhand
```

The fully qualified name is not decoration. Homebrew's **tap trust** means that
tapping a third-party repository does not by itself let you install from it by
short name — code in a tap runs with your privileges, so trust is per item. If
you would rather type `brew install deckhand` afterwards:

```bash
brew trust --formula marcwoge/deckhand/deckhand
```

Then:

```bash
deckhand init --config ~/.config/deckhand/deckhand.yaml
deckhand check            # validates the file and warns about risky settings
deckhand doctor           # connectivity, token scope, permissions
deckhand service install  # a launchd agent that survives a reboot
```

Upgrading is the usual `brew upgrade deckhand`.

## What you are installing

The formula installs the **release binary**, not a source build: one static
binary, no runtime, no interpreter, no dependencies beyond `git`.

Its checksums are not typed in by hand. This tap updates itself from the
release's own `SHA256SUMS`, and before trusting that file it verifies the
[cosign](https://docs.sigstore.dev) signature over it — keyless, so there is no
signing key anywhere, and the signature is bound to Deckhand's release workflow
and recorded in the public transparency log. If that verification fails, the
formula is not updated.

So the chain is: Deckhand's release workflow builds and signs → this tap checks
the signature and copies the hashes → Homebrew checks the hash of what it
downloads.

## Keeping it current

A [workflow](.github/workflows/update.yml) runs daily and rewrites
`Formula/deckhand.rb` when there is a newer release. To pick one up immediately,
run it by hand from the Actions tab — or open an issue on
[marcwoge/deckhand](https://github.com/marcwoge/deckhand/issues) if it looks
stuck.

## Documentation, and getting involved

Everything lives in the main repository, in
[English](https://github.com/marcwoge/deckhand/tree/main/docs/en) and
[German](https://github.com/marcwoge/deckhand/tree/main/docs/de):
configuration, triggers, time windows, notifications, security, running as a
service, upgrading, troubleshooting.

Bugs and ideas belong on
[marcwoge/deckhand](https://github.com/marcwoge/deckhand/issues) — this
repository holds nothing but the formula. Contributions are welcome there; the
developer documentation is under `docs/dev/`.

Apache-2.0, like Deckhand itself.
