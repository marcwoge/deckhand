# Troubleshooting

Start here:

```bash
deckhand check      # is the configuration sane?
deckhand status     # what does each watch think its state is?
deckhand history    # what actually happened, and when?
```

---

### "config ... is writable by group or others"

```bash
chmod 600 /etc/deckhand/deckhand.yaml
```

Deckhand will not read a config that someone else can rewrite, because that
config decides which commands run on the machine.

### "refusing to run as root"

Create a service user — see [security.md](security.md#2-a-user-that-can-only-deploy).
If you have a genuine reason to run as root, `deckhand run --allow-root` does
it, but reach for that only after deciding the reason really is genuine.

### "not found (repository missing, renamed, or the token lacks access)"

In order of likelihood:

1. The token's **repository access** list does not include this repository.
   Fine-grained tokens are per-repository; adding a new watch usually means
   editing the token too.
2. The token has expired.
3. `repo:` is misspelled, or the repository was renamed.

GitHub deliberately answers `404` rather than `403` for repositories you cannot
see, so a permission problem looks like a missing repository.

### "no matching release found"

The repository has no published release matching your filters. Check that
releases exist and are not all drafts or pre-releases, and that `tag_match`
matches the actual tag names (`v*` does not match `1.2.3`).

### "GitHub rate limit exhausted"

Almost always an unauthenticated watch: 60 requests per hour, per IP address,
shared by everything on that machine. Configure a token — it raises the limit
to 5,000 per hour and applies to public repositories too.

### The watch is halted

```
[shop] halted after 3 consecutive failures
```

Deckhand stopped retrying on purpose. Find the cause:

```bash
deckhand status                 # shows the last error
deckhand history shop --limit 5
```

Fix it, then:

```bash
deckhand resume shop
```

### The deployment succeeded but the application did not change

Check what your command actually did:

```bash
deckhand history shop
ls -l /srv/shop/current
cat /srv/shop/current/.deckhand-release.json
```

Common causes: the service reads from a path other than `current`; the
container image was not rebuilt (`docker compose up -d` alone does not rebuild
a locally built image — add a `docker compose build` step); or the process
caches configuration and needs a restart rather than a reload.

### Everything is rolled back immediately

Your health check is failing. Test it by hand:

```bash
curl -i http://localhost:8080/healthz
```

Typical fixes: raise `initial_delay` so the application has time to start,
raise `retries`, or point the check at an endpoint that does not depend on a
downstream service being up.

### `.env` keeps disappearing after a deployment

Add it to `shared:`:

```yaml
shared: [".env", "data/"]
```

Files in a release directory are replaced with every deployment. Shared paths
live in `path/shared/` and are linked into each release, so they survive. The
first deployment moves an existing repository copy into `shared/` as a starting
point.

### "cannot create link ... enable Developer Mode" (Windows)

The `releases` strategy needs a link for `current`. Enable Developer Mode for
real symlinks, run the installer elevated, or switch that watch to
`strategy: inplace`.

### The Telegram bot does not answer

* Did you message the bot first? A bot cannot open a conversation, and until it
  has one it has no chat to reply to.
* Is `chat_id` the chat you are writing from? Commands from any other chat are
  ignored silently, by design. The log line `ignored a message from chat N`
  tells you the ID you actually wrote from.
* Did deckhand log `listening for commands from chat … as @yourbot` at startup?
  If not, the token was rejected — `deckhand check` shows the channel.
* A command that gets `⌛ Ignored: that command is … old` was sitting on
  Telegram's servers. Send it again.

### Notifications never arrive

Run `deckhand check`: it lists every channel, where it points and whether a
credential was found. Then look for a line starting with `notify:` in the log —
a channel that fails is reported there and never stops the deployment.

For ntfy, test the endpoint by hand:

```bash
curl -H "Authorization: Bearer $DECKHAND_NTFY_TOKEN" -d "test" https://ntfy.example.com/deploy
```

### Nothing happens at all

* Is the worker running? `systemctl status deckhand`, `launchctl list | grep deckhand`, `schtasks /Query /TN Deckhand`.
* Is everything paused? `deckhand status` says so on the first line.
* Is the window closed? `deckhand history` shows a `queue` event with the time it will open.
* Is the watch disabled? `enabled: false` in the config.
* Are you looking at the right state directory? A service running as `deckhand`
  and your shell as yourself use different ones. Pass the same
  `DECKHAND_STATE_DIR`, or run the CLI as the service user:
  `sudo -u deckhand deckhand status --config /etc/deckhand/deckhand.yaml`.

### Reading the audit log directly

It is JSON Lines, one event per line:

```bash
jq -r 'select(.result=="failed") | "\(.time) \(.watch) \(.message)"' \
  /var/lib/deckhand/audit.jsonl
```

### Still stuck

Open an issue with the output of `deckhand check`, the relevant lines from
`deckhand history`, your Deckhand version (`deckhand version`) and your
platform. Please redact tokens — Deckhand redacts them in its own output, but
not in anything you paste from elsewhere.
