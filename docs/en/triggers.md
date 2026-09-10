# Triggers

A trigger answers one question: *which revision should be on this machine right
now?* Deckhand asks GitHub that question on every poll and deploys when the
answer changes.

## `release` — the recommended default

```yaml
trigger:
  type: release
  tag_match: "v*"        # optional glob
  prerelease: false      # optional, default false
```

Deploys the newest published release. Drafts are always ignored; pre-releases
are ignored unless you opt in. `tag_match` lets several environments follow the
same repository:

```yaml
tag_match: "v*"          # production follows v1.4.0
tag_match: "*-beta"      # a beta box follows 1.5.0-beta
```

Use this when a human decides what goes live. Publishing a release is an
explicit act, which makes it a good deployment gate.

## `branch` — continuous deployment

```yaml
trigger:
  type: branch
  branch: main
```

Deploys the current head of a branch, which covers both direct pushes and
merged pull requests — a merge moves the branch head, and that is what
Deckhand watches.

Use this for staging environments, or for production if your `main` is always
releasable. Combine it with a [time window](time-windows.md) if you do not want
a merge at 16:55 on Friday to become a deployment at 16:55 on Friday.

Note that a branch trigger follows whoever can push to that branch. For a
repository you do not control, prefer `release` with signature verification.

## `tag` — releases without GitHub Releases

```yaml
trigger:
  type: tag
  tag_match: "v*"
```

Deploys the highest tag matching the pattern. Tags are compared by version
where possible, so `v1.10.0` correctly sorts above `v1.9.0`, and lexically
otherwise.

Use this when your workflow pushes tags but does not create GitHub Releases.

## How polling works

Every poll is a conditional request carrying the ETag of the previous answer.
When nothing changed GitHub replies `304 Not Modified`, which costs no rate
limit and transfers no body. The revision is only fetched from git when the
answer actually changed, and then only into a local mirror that is reused for
every later deployment.

Polls are jittered by up to 10% so that many watches do not all fire in the
same second, and API errors back off exponentially up to fifteen minutes.

## What happens after the trigger fires

1. The [time window](time-windows.md) is checked. If it is closed, the revision
   is remembered and re-checked on every poll — if three more commits land
   before the window opens, you get one deployment with the newest one.
2. The local git mirror is updated.
3. Any `verify:` checks run.
4. The revision is exported into a new release directory, shared paths are
   linked in, and `current` is switched over.
5. Your `run:` commands execute.
6. The health check decides whether it worked.
7. On failure: rollback, `on_failure:` commands, notification.

Only one deployment per watch runs at a time. Different watches run
independently and in parallel.
