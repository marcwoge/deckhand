# Architecture

Deckhand is a single Go binary with no dependencies beyond a YAML parser. It is
small enough to read in an afternoon; this page is the map.

```
cmd/deckhand/          CLI: subcommands, flag parsing, the git askpass helper
internal/config/       YAML types, validation, time-window parsing
internal/gh/           GitHub REST client with ETag caching
internal/deploy/       git operations, release directories, links, state
internal/runner/       command execution, timeouts, process groups
internal/health/       post-deploy checks
internal/notify/       webhook, Slack and ntfy notifications
internal/audit/        append-only JSON Lines log
internal/service/      systemd, launchd, Windows Task Scheduler installers
internal/watcher/      the polling loop and the deployment sequence
```

## The loop

One goroutine per watch, in `internal/watcher/engine.go`:

```
timer fires
  └─ resolve()            ask GitHub what the trigger points at (conditional GET)
       └─ compare with state.LastSHA
            └─ pending = target            (newest target wins; older ones are dropped)
                 └─ halted? paused? window closed?  → keep pending, log once per hour
                      └─ deployTarget()
```

`deployTarget` in `internal/watcher/deploy.go` holds a per-watch mutex, so a
manual `deckhand deploy` and the polling loop cannot overlap.

## The deployment sequence

```
Fetch      update the bare mirror in <state>/watches/<name>/repo.git
Verify     optional signature and pin checks
Prepare    git archive <sha> | extract into releases/<sha>.incomplete
           link shared paths, write .deckhand-release.json, rename into place
Activate   swap the current symlink (atomic: create + rename)
Run        execute the run: commands in current/
Health     HTTP or command check with retries
           on failure → Rollback: relink previous release, run the commands again
Record     state.json, audit log, notification, prune old releases
```

Two decisions are worth knowing about:

**`git archive` instead of a worktree.** The tree is exported as a tar stream
and unpacked in-process. That means no `.git` inside the release, no worktree
bookkeeping to get out of sync, no external `tar` on Windows — and, most
usefully, every path can be validated before it touches the filesystem. See
`extractTar` in `internal/deploy/git.go` and its tests.

**Rollback re-runs the commands.** Relinking `current` only moves files. The
service is only actually back when the command that starts it has run again.

## The command bot

`internal/watcher/commands.go` long-polls the Telegram API — the same outbound
-only shape as the GitHub client, so enabling it opens no port. Three details
matter and are covered by tests:

* the backlog is skipped at startup and the update offset is persisted, so a
  restart never replays a command;
* commands older than `maxCommandAge` are refused, because Telegram keeps
  undelivered messages for a day;
* commands from any chat other than the configured one get no reply at all.

`runCommandBotWith` is the loop split out from credential setup so a stub client
can drive it in tests.

## State

Per watch, in `<state_dir>/watches/<name>/`:

* `repo.git` — the bare mirror, shared by every deployment of that watch
* `state.json` — last SHA, current and previous release, failure count, halted flag

Globally, in `<state_dir>/`:

* `audit.jsonl` — the append-only log
* `paused` — present when deployments are globally held
* `telegram_offset` — the last handled Telegram update

State is written atomically (temp file plus rename) and is deliberately simple
to read and repair by hand. A corrupt `state.json` is treated as a fresh start
rather than a fatal error.

## Security-relevant code

If you change any of these, say so explicitly in the pull request:

| Where | What it guarantees |
|---|---|
| `runner.buildEnv` | Deploy commands get an allow-list environment; the token is not in it |
| `runner.Run` | argv execution without a shell; process-group kill on timeout |
| `deploy.gitFlags` | Hooks and exotic transports disabled for every git call |
| `deploy.extractTar` | No path traversal, no tar-slip, no device nodes |
| `deploy.gitEnv` | The token reaches git via the environment, not the command line |
| `config.checkPermissions` | Refuses a writable config |
| `config.ResolveToken` | Refuses a world-readable token file |
| `main.warnings` | Warns when a command lives inside the deployed tree |
| `watcher.handleUpdate` | Telegram commands are accepted only from the configured chat, and only when recent |
| `notify.post` | Credentials go in an Authorization header, never in a URL, and never into an error message |

## Testing

```bash
go test ./...
```

The deploy tests create real git repositories in temporary directories and run
real deployments against them, including rollback and shared-path persistence.
The GitHub client is tested against an `httptest` server that asserts
conditional requests are actually being made. `internal/deploy/extract_test.go`
is the hostile-archive suite — add a case there whenever you touch extraction.

There is no mocking framework and no test helper library. Please keep it that
way.

## Adding a configuration option

1. Add the field and its validation in `internal/config/config.go`.
2. Use it where it belongs; keep the default behaviour unchanged.
3. Document it in `docs/en/configuration.md` **and** `docs/de/configuration.md`.
4. Add a row to the [roadmap](../../ROADMAP.md) table.
5. Add a test if the option can be got wrong.
