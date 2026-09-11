# Roadmap

This document describes what Deckhand is meant to be when it is finished, and
which parts of it exist today. Every feature carries a status, so anyone
picking up the project can tell at a glance what is done, what is in progress
and what is still open.

Status: ✅ done · 🚧 in progress · 📋 planned · 💭 idea, not committed

---

## The finished product

Deckhand is a single self-contained binary that runs as a background service on
Linux, macOS and Windows. It watches one or more GitHub repositories — your
own, ones you contribute to, or public repositories belonging to someone else —
and reacts when a new release, tag or commit appears on a branch you named.

When that happens it fetches exactly that revision, places it on disk in a way
that can be switched over atomically and rolled back, and then runs a command
that you defined in a local configuration file: restart a container, reload a
web server, run a migration, whatever the deployment needs. If a time window is
configured, it waits for that window and deploys the newest revision once it
opens; if none is configured, it deploys immediately. A health check decides
whether the deployment counts as successful, and a failed deployment is rolled
back automatically.

It reports what it did over the channels you configure — push to a phone, a
team chat, your own endpoint — and a Telegram bot doubles as a remote control
you can ask for status or tell to roll back. A heartbeat to an outside service
makes sure that even the worker's own death is noticed.

It does all of this without opening a single inbound port, without needing more
than read access to the repositories, and without ever letting repository
content decide what gets executed on the machine.

The intended user is someone who runs a handful of servers, not a fleet, and
who wants tagging a release to be the whole deployment procedure. Deckhand is
explicitly *not* a CI system, an orchestrator, or a replacement for Kubernetes.

---

## v0.1 — the working core ✅ released

[v0.1.0](https://github.com/marcwoge/deckhand/releases/tag/v0.1.0) covers the
complete loop from trigger to running service. Binaries for seven platforms,
with signed checksums and build provenance.

### Configuration
| Feature | Status |
|---|---|
| YAML configuration with strict validation (unknown keys are errors) | ✅ |
| Multiple watches per config, each with its own settings | ✅ |
| Defaults block inherited by every watch | ✅ |
| `deckhand init` writes a commented starter config | ✅ |
| `deckhand check` validates and warns about risky settings | ✅ |
| Token from environment variable, file or (discouraged) inline | ✅ |
| GitHub Enterprise Server support (`github.api`, `github.host`) | ✅ |
| Split configuration across an include directory | ✅ |

### Triggers
| Feature | Status |
|---|---|
| New release, with pre-release and tag-pattern filters | ✅ |
| New commit on a named branch (covers merges and pushes) | ✅ |
| New tag matching a pattern, ordered by version | ✅ |
| Conditional polling with ETags — unchanged answers cost no rate limit | ✅ |
| Automatic backoff on API errors and rate limits | ✅ |
| Optional webhook receiver as an alternative to polling | 💭 |

### Deployment
| Feature | Status |
|---|---|
| Atomic release directories with a `current` link | ✅ |
| In-place strategy for setups that cannot use a link | ✅ |
| `shared:` paths for configuration and data that outlive a release | ✅ |
| Automatic pruning of old releases (`keep_releases`) | ✅ |
| Release metadata file (`.deckhand-release.json`) in every release | ✅ |
| Windows junction fallback where symlinks are unavailable | ✅ |
| Private submodule support | 📋 |

### Running your command
| Feature | Status |
|---|---|
| Commands as an argv vector, no shell by default | ✅ |
| Explicit `shell:` opt-in per command | ✅ |
| Per-command working directory, timeout and environment | ✅ |
| Deployment context in `DECKHAND_*` environment variables | ✅ |
| Process-group termination on timeout (unix and Windows) | ✅ |
| `on_failure:` commands | ✅ |
| Pre-deploy commands (`before:`) | 📋 |

### Reliability
| Feature | Status |
|---|---|
| HTTP and command health checks with retries | ✅ |
| Automatic rollback to the revision that was running, re-running the command | ✅ |
| Manual `deckhand rollback` | ✅ |
| Circuit breaker: a watch halts after N consecutive failures | ✅ |
| One deployment at a time per watch | ✅ |
| State survives restarts, upgrades and reboots | ✅ |
| State records what is physically live, not only what last succeeded | ✅ |
| Lock file so two Deckhand processes cannot fight over one path | ✅ |

### Time windows
| Feature | Status |
|---|---|
| Weekday and clock rules (`Mon-Fri 22:00-05:00`), wrapping past midnight | ✅ |
| Blackout rules that override allow rules | ✅ |
| Per-watch timezone, defaulting to the global one | ✅ |
| Trigger coalescing: one deployment with the newest revision | ✅ |
| Calendar-date blackouts, one-off and yearly | ✅ |

### Operating it
| Feature | Status |
|---|---|
| `status`, `history`, `deploy`, `rollback`, `pause`, `resume` | ✅ |
| `deckhand doctor`: connectivity, credentials, permissions, resolvable triggers | ✅ |
| Reload the configuration on SIGHUP without stopping deployments | ✅ |
| Append-only JSON Lines audit log | ✅ |
| Service installation for systemd, launchd and Windows Task Scheduler | ✅ |
| Hardened systemd unit (NoNewPrivileges, ProtectSystem, …) | ✅ |
| Notifications via webhook, Slack, ntfy or Telegram | ✅ |
| Several notification channels, each with its own event filter | ✅ |
| Authenticated ntfy (bearer token) with per-event priorities | ✅ |
| Telegram remote control: /status, /history, /deploy, /rollback, /pause | ✅ |
| Heartbeat to a dead-man's-switch service | ✅ |
| `--json` output for `status` | ✅ |
| Audit log rotation, read back across rotations | ✅ |
| Prometheus metrics on localhost | 💭 |

### Security
| Feature | Status |
|---|---|
| Commands come from the config, never from the repository | ✅ |
| Warning when a command points inside the deployed tree | ✅ |
| Refuses to run as root without `--allow-root` | ✅ |
| Refuses a group- or world-writable config, or a readable token file | ✅ |
| The token is never passed to deploy commands | ✅ |
| The token reaches git through the environment, not the command line | ✅ |
| Git hooks and exotic transports disabled for every git call | ✅ |
| Archive extraction validates every path (no traversal, no tar-slip) | ✅ |
| Signature verification for tags and commits (`verify:`) | ✅ |
| Pin a watch to an exact SHA | ✅ |
| Allowed-author lists | ✅ |
| Signed releases (cosign, keyless) with build provenance | ✅ verified against v0.1.0 |

---

## v0.2 — sharper edges

Complete apart from the two items still marked open.

| Feature | Status |
|---|---|
| Calendar-date blackout windows, one-off and yearly | ✅ |
| Configuration includes (`include:` / `<config>.d`) | ✅ |
| Audit log rotation and retention | ✅ |
| Lock file against concurrent Deckhand instances | ✅ |
| Allowed-author verification | ✅ |
| Reload configuration without restarting (SIGHUP) | ✅ |
| `deckhand doctor` — diagnose token scope, connectivity and permissions | ✅ |
| Signed releases with build provenance | ✅ |
| `before:` commands and deploy stages | 📋 |
| Private submodule support | 📋 |

## v0.3 — platform polish

| Feature | Status |
|---|---|
| Native Windows service (Service Control Manager) instead of a scheduled task | 📋 [#7](https://github.com/marcwoge/deckhand/issues/7) |
| Packages: `.deb`, `.rpm`, Homebrew formula, Scoop manifest | 📋 [#8](https://github.com/marcwoge/deckhand/issues/8) |
| Testing on real macOS and Windows machines | 📋 [#10](https://github.com/marcwoge/deckhand/issues/10) |
| Prometheus metrics endpoint bound to localhost | 💭 |
| Optional inbound webhook mode for people who can expose a port | 💭 |

## Explicitly out of scope

These come up often enough to be worth naming. Deckhand will not grow into:

* a build system — build in CI, publish an artifact or an image, deploy that;
* a container orchestrator — it restarts your compose stack, it does not schedule it;
* a secrets manager — it reads a token and passes environment variables through;
* a multi-machine coordinator — each machine runs its own Deckhand, independently.

---

## Contributing to the roadmap

Items marked 📋 are open for pull requests and most of them have an issue with
implementation notes attached; items marked 💭 need a discussion first.
Platform testing on macOS and Windows is tracked in
[#10](https://github.com/marcwoge/deckhand/issues/10) and is the single most
useful thing an outside contributor can help with. Please open an issue describing the use case before implementing an idea
item, so we can agree on the shape. See [CONTRIBUTING.md](CONTRIBUTING.md).
