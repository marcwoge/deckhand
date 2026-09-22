# Deckhand for Scoop

A [Scoop](https://scoop.sh) bucket for
[Deckhand](https://github.com/marcwoge/deckhand) — a small worker that watches
your GitHub repositories, pulls each new release or commit into its own
directory, runs the command you configured, and rolls back when the health check
fails.

```powershell
scoop bucket add deckhand https://github.com/marcwoge/scoop-deckhand
scoop install deckhand
```

Then:

```powershell
deckhand init --config $env:USERPROFILE\deckhand\deckhand.yaml
deckhand check            # validates the file and warns about risky settings
deckhand doctor           # connectivity, token scope, permissions
deckhand service install  # a scheduled task that survives a reboot
```

Upgrading is the usual `scoop update deckhand`.

## What you are installing

The manifest installs the **release binary** — one static executable, no
runtime, no interpreter. `git` is needed at runtime and is not pulled in
automatically: `scoop install git`, or use Git for Windows.

The hashes are not typed in by hand. This bucket updates itself from the
release's own `SHA256SUMS`, and before trusting that file it verifies the
[cosign](https://docs.sigstore.dev) signature over it — keyless, so there is no
signing key anywhere, and the signature is bound to Deckhand's release workflow
and recorded in the public transparency log. If that check fails, the manifest is
not updated.

## Windows specifics worth knowing

* `deckhand service install` registers a **scheduled task**, not a real service —
  the Service Control Manager terminates a console program that does not speak
  its control protocol. It starts at boot and restarts on failure. See
  [issue #7](https://github.com/marcwoge/deckhand/issues/7).
* The `current` link into a release directory is a symlink when Developer Mode is
  on, and a junction otherwise. If neither works, `strategy: inplace` is the way
  out.
* Windows and macOS are not yet tested on real machines beyond CI —
  [issue #10](https://github.com/marcwoge/deckhand/issues/10) collects reports,
  and one saying "this worked" is as useful as one saying it did not.

## Keeping it current

A [workflow](.github/workflows/update.yml) runs daily and rewrites
`bucket/deckhand.json` when there is a newer release. Run it by hand from the
Actions tab to pick one up immediately.

## Documentation, and getting involved

Everything lives in the main repository, in
[English](https://github.com/marcwoge/deckhand/tree/main/docs/en) and
[German](https://github.com/marcwoge/deckhand/tree/main/docs/de).

Bugs and ideas belong on
[marcwoge/deckhand](https://github.com/marcwoge/deckhand/issues) — this
repository holds nothing but the manifest.

Apache-2.0, like Deckhand itself.
