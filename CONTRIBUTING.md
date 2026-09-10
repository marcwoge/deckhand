# Contributing to Deckhand

Thanks for considering it. Deckhand is small on purpose, and the goal is to keep
it that way: one binary, no dependencies beyond a YAML parser, and behaviour
that an operator can predict without reading the source.

## Ways to help that are genuinely useful

* **Platform testing.** Deckhand is developed on Linux. Reports from macOS and
  Windows — especially around service installation, symlinks and junctions —
  are the most valuable thing you can send.
* **Documentation.** Both languages are maintained side by side; if you change
  one, please change the other, or say in the pull request that you could not.
* **Translations** of the user documentation into further languages.
* **Bug reports** with a configuration snippet and the relevant output of
  `deckhand check` and `deckhand history`.

## Before you write code

Open an issue first for anything beyond a bug fix. Items marked 📋 in the
[roadmap](ROADMAP.md) are ready for implementation; items marked 💭 need a
discussion about whether they belong in Deckhand at all.

## Development

```bash
git clone https://github.com/marcwoge/deckhand
cd deckhand
go build ./...
go test ./...
```

There is nothing else to install. The test suite creates real git repositories
in temporary directories and runs real deployments against them, so `git` must
be on your `PATH`.

```bash
gofmt -l .          # must print nothing
go vet ./...        # must be clean
go test ./...       # must pass
GOOS=windows go build ./...   # and darwin, and linux/arm64
```

Please run all four before opening a pull request.

**Use the Go version from `go.mod`.** A newer toolchain compiles happily against
standard-library functions that the declared version does not have, so code that
builds on your machine can still fail in CI. Pin the toolchain for a check and
the problem shows up before you push:

```bash
GOTOOLCHAIN=go1.22.0 go vet ./... && GOTOOLCHAIN=go1.22.0 go test ./...
``` CI runs the same checks, and
deliberately only on pushes to `main` and on tags — it is not there to iterate
for you.

## What good code looks like here

* **The safety rules in [SECURITY.md](SECURITY.md) are not negotiable.** A pull
  request that lets repository content influence what is executed, or that
  widens the environment handed to deploy commands, will not be merged.
* **Errors are for operators, not for developers.** `poll_interval must be at
  least 10s` beats `validation error in field 3`. Say what is wrong and what to
  do about it.
* **New configuration keys need three things:** validation in `internal/config`,
  documentation in `docs/en/` and `docs/de/`, and a line in the roadmap table.
* **Comments explain why, not what.** The code says what it does.
* **Tests cover the decision, not the plumbing.** Look at
  `internal/config/window_test.go` for the tone.

Architecture notes are in [docs/dev/architecture.md](docs/dev/architecture.md).

## Workflow changes

Actions in `.github/workflows/` are pinned to commit SHAs with the version in a
trailing comment:

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7
```

Please keep it that way when you add one. A tag can be moved by whoever controls
the action's repository, and the release workflow runs with write permissions.
Dependabot updates pinned SHAs perfectly well.

## Commits and pull requests

Write commit subjects in the imperative mood (`add calendar blackout windows`),
keep unrelated changes in separate commits, and describe in the pull request
what an operator would notice.

## Code of conduct

Be decent. Assume the other person is trying to help. Maintainers may remove
comments and contributors that make the project unpleasant to work on.
