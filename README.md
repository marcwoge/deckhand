<div align="center">

# ⚓ Deckhand

**The quiet crew member for your servers.**
It watches your GitHub repositories, pulls every new release or commit, and runs the one command that makes it live.

[Deutsch](README.de.md) · [Documentation](docs/en/) · [Roadmap](ROADMAP.md) · [Security](SECURITY.md)

</div>

---

## Why Deckhand

You tagged a release. Now someone has to open a terminal, SSH into a box, `git pull`, and restart a container. Again. On three machines.

Deckhand is a single small binary that does exactly that job and nothing else:

```yaml
watch:
  - name: shop
    repo: your-name/shop
    trigger: { type: release }
    path: /srv/shop
    run:
      - ["docker", "compose", "up", "-d"]
```

That is the whole setup. New release appears on GitHub → the code lands on the machine → your command runs → a health check confirms it worked. If it did not work, Deckhand puts the previous version back before you notice.

### What makes it different

| | |
|---|---|
| **No inbound ports** | Deckhand asks GitHub; GitHub never calls in. Works behind NAT, no firewall rules, no webhook endpoint to secure. |
| **The command lives in your config, not in the repo** | Even a compromised repository cannot change *what* runs on your machine. This is the central safety boundary. |
| **Atomic releases** | Each revision is unpacked into `releases/<sha>` and a `current` link is switched over once it is complete. Nothing ever runs against a half-written directory. |
| **Automatic rollback** | A failing command or health check restores the previous release and re-runs the command, so the service comes back — not just the files. |
| **Deploy windows** | "Only between 22:00 and 05:00, and never on Friday afternoon." Triggers that arrive outside the window are coalesced: you get one deployment with the newest code, not twelve. |
| **One binary, three platforms** | Linux, macOS, Windows. No runtime, no interpreter, no dependencies. `deckhand service install` registers it with systemd, launchd or the Windows Task Scheduler. |
| **Alerts, and a remote control** | Push to your phone via ntfy, Slack or a webhook — or a Telegram bot you can ask `/status` and tell `/rollback shop`, polled like GitHub so there is still no inbound port. |
| **Notices its own death** | A heartbeat to healthchecks.io or Uptime Kuma, because a crashed worker sends no alerts — and silence looks exactly like success. |
| **Everything is logged** | An append-only JSON Lines audit trail of every trigger, revision, command and exit code. |

---

## Quick start

**1. Install**

Download the binary for your platform from the [latest release](https://github.com/marcwoge/deckhand/releases/latest), or build it yourself:

```bash
go install github.com/marcwoge/deckhand/cmd/deckhand@latest
```

**2. Create a configuration**

```bash
deckhand init --config deckhand.yaml   # writes a commented example, mode 0600
```

Edit it to describe what you want:

```yaml
version: 1

defaults:
  poll_interval: 60s
  timezone: Europe/Berlin

github:
  token_env: DECKHAND_GITHUB_TOKEN     # keep the token out of the file

watch:
  - name: shop
    repo: your-name/shop
    trigger:
      type: release                     # release | branch | tag
      tag_match: "v*"
    path: /srv/shop
    shared: [".env", "data/"]           # survives every deployment
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d"]
    health:
      http: http://localhost:8080/healthz
      retries: 10

notify:
  on: [failure, rollback, halt]
  channels:
    - type: ntfy                    # push to your phone
      url: https://ntfy.sh/deckhand-a7f3k9m2q8

heartbeat:                          # so you notice if deckhand itself dies
  url: https://hc-ping.com/your-uuid
```

**3. Create a token**

Use a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new) with **`Contents: Read-only`** on exactly the repositories you list — nothing else. Public repositories you do not own need no token at all (but see [rate limits](docs/en/configuration.md#rate-limits)).

```bash
export DECKHAND_GITHUB_TOKEN=github_pat_...
```

**4. Check and run**

```bash
deckhand check        # validates the config and points out risky settings
deckhand doctor       # talks to GitHub: is the token right, does the trigger resolve,
                      # is the path writable? Read-only and safe on production.
deckhand deploy shop  # deploy once, right now, to see it work
deckhand run          # start the worker in the foreground
```

**5. Install as a service**

```bash
deckhand service install            # current user
sudo deckhand service install --system --user deckhand   # machine-wide
```

That is it. From here on, tagging a release is the deployment.

---

## Daily operation

| Command | What it does |
|---|---|
| `deckhand status` | What every watch is on, when it last succeeded, what is broken |
| `deckhand history [watch]` | The audit trail, newest last |
| `deckhand deploy <watch> [--force]` | Deploy now, ignoring the time window |
| `deckhand rollback <watch>` | Go back to the previous revision |
| `deckhand pause "database migration"` | Hold every deployment |
| `deckhand resume` / `deckhand resume <watch>` | Release the hold / clear a halted watch |
| `deckhand check` | Validate the configuration before restarting the service |
| `deckhand doctor` | Check connectivity, credentials, permissions and whether each trigger resolves |
| `systemctl reload deckhand` | Apply configuration changes without stopping deployments |

After three consecutive failures a watch halts itself instead of looping, tells you why, and waits for `deckhand resume <watch>`.

### What a deployment looks like on disk

```
/srv/shop/
├── current -> releases/9f3c2ab...      the live tree
├── releases/
│   ├── 9f3c2ab.../                     this release
│   └── 41ae08c.../                     the previous one, ready for rollback
└── shared/
    ├── .env                            linked into every release
    └── data/
```

Your command runs in `current`, and gets `DECKHAND_SHA`, `DECKHAND_REF`, `DECKHAND_RELEASE_DIR`, `DECKHAND_SHARED_DIR` and more in its environment. See [configuration](docs/en/configuration.md#environment-variables).

---

## Maintenance

* **Updating Deckhand:** replace the binary and restart the service. State lives outside the binary and survives upgrades.
* **Rotating the token:** update the environment variable or token file and restart. Nothing else refers to it.
* **Disk usage:** old releases are pruned automatically (`keep_releases`, default 5). The local git mirror is shared between deployments and stays small.
* **Logs:** `journalctl -u deckhand -f` (Linux), `~/Library/Logs/deckhand.log` (macOS), `deckhand history` on any platform.
* **Backups:** back up `shared/` — it holds your configuration and data. Everything else can be re-fetched from GitHub.

Full documentation: **[docs/en/](docs/en/)** — [configuration](docs/en/configuration.md) · [triggers](docs/en/triggers.md) · [time windows](docs/en/time-windows.md) · [notifications](docs/en/notifications.md) · [security](docs/en/security.md) · [running as a service](docs/en/services.md) · [troubleshooting](docs/en/troubleshooting.md)

---

## Security in one paragraph

Deckhand assumes your repository is maintained and trustworthy — that is its stated threat model. Within that assumption it is deliberately narrow: it opens no listening port, it needs no more than read access to the repositories you name, it refuses to start as root or with a world-readable config, it never passes its token to your deploy commands, it executes commands as an argv vector without a shell, it disables git hooks so repository content cannot change how git behaves, and it validates every path in the exported archive so a hostile repository cannot write outside its own release directory. The full threat model is in [SECURITY.md](SECURITY.md) and [docs/en/security.md](docs/en/security.md).

---

## Contributing

Deckhand is small on purpose, and pull requests are welcome — especially platform testing, documentation and translations. Start with [CONTRIBUTING.md](CONTRIBUTING.md) and the [architecture notes](docs/dev/architecture.md). Open an issue before large changes so we can agree on the shape first.

## License

[Apache License 2.0](LICENSE)
