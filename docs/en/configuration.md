# Configuration reference

Deckhand reads a single YAML file. Unknown keys are an error, not a warning —
a typo will stop the worker at `deckhand check` rather than silently doing the
wrong thing at three in the morning.

Where the file is looked for, in order:

1. `--config <path>`
2. `$DECKHAND_CONFIG`
3. `./deckhand.yaml` or `./deckhand.yml`
4. `~/.config/deckhand/deckhand.yaml`
5. `/etc/deckhand/deckhand.yaml` (Linux/macOS) or `%ProgramData%\deckhand\deckhand.yaml` (Windows)

The file must not be writable by group or others. `deckhand init` creates it
with mode 0600.

---

## Top level

```yaml
version: 1          # required, currently always 1
defaults: {}        # inherited by every watch
github: {}          # API access
notify: {}          # optional notifications
watch: []           # one entry per repository being watched
```

## `defaults`

Everything here can be overridden per watch.

| Key | Default | Meaning |
|---|---|---|
| `poll_interval` | `60s` | How often GitHub is asked. Minimum `10s`. Unchanged answers are served from cache and cost no rate limit. |
| `timezone` | system zone | The zone every time window is evaluated in, e.g. `Europe/Berlin`. |
| `command_timeout` | `10m` | How long a single command may run before it and its children are killed. |
| `strategy` | `releases` | `releases` (atomic switch, rollback) or `inplace`. |
| `keep_releases` | `5` | How many old release directories to keep. |
| `failure_limit` | `3` | Consecutive failures after which a watch halts itself. |
| `state_dir` | see below | Where state, the git mirror and the audit log live. |
| `env` | – | Environment variables added to every command. |

The default state directory is `~/.local/state/deckhand` for a normal user,
`/var/lib/deckhand` for root, and `%ProgramData%\deckhand\state` on Windows.
`$DECKHAND_STATE_DIR` overrides it.

## `github`

| Key | Meaning |
|---|---|
| `token_env` | Name of the environment variable holding the token. **Preferred.** |
| `token_file` | Path to a file containing the token. Must not be readable by others. |
| `token` | The token inline. Works, but puts a credential in a file you might commit. |
| `api` | API base URL. Change for GitHub Enterprise Server, e.g. `https://ghe.example.com/api/v3`. |
| `host` | Git host used for cloning. Defaults to `github.com`. |

If none of the three token settings is given, `$DECKHAND_GITHUB_TOKEN` is used.

**Which token?** A [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new)
with **`Contents: Read-only`**, restricted to the repositories you list.
Deckhand never writes to GitHub and needs no other permission. For repositories
where you are a contributor rather than the owner, the same token works as long
as your account can read them.

### Rate limits

| Situation | Limit |
|---|---|
| With a token | 5,000 requests per hour |
| Without a token | 60 requests per hour, per IP address |

Deckhand sends conditional requests: when nothing changed, GitHub answers `304
Not Modified`, which does **not** count against the limit. In practice a
token comfortably covers dozens of watches at a 60-second interval.

Without a token, one watch at 60 seconds would already exhaust the anonymous
budget, so Deckhand automatically slows unauthenticated watches to a
five-minute interval. Configuring a token is worth it even for public
repositories.

## `notify`

Optional. Remove the block entirely if you do not want notifications.

```yaml
notify:
  on: [failure, rollback]        # success, failure, rollback, halt — or "all"
  webhook: https://ntfy.sh/your-private-topic
  format: ntfy                   # json (default) | slack | ntfy
```

`json` posts the full event object; `slack` posts `{"text": "..."}` suitable
for an incoming webhook; `ntfy` posts plain text with a title header.

## `watch`

Each entry describes one repository.

### Identity

| Key | Required | Meaning |
|---|---|---|
| `name` | yes | Unique name, used in commands, logs and status output. |
| `repo` | yes | `owner/name`. |
| `auth` | no | `token` (default) or `none` for public repositories you want to poll anonymously. |
| `enabled` | no | Set to `false` to keep an entry in the file without running it. |
| `clone_url` | no | Overrides where the code is fetched from. Rarely needed. |

### What to watch

```yaml
trigger:
  type: release        # release | branch | tag
  branch: main         # required for type: branch
  tag_match: "v*"      # glob, for release and tag
  prerelease: false    # include pre-releases (release only)
```

See [triggers.md](triggers.md) for how to choose.

### Where it goes

| Key | Default | Meaning |
|---|---|---|
| `path` | required | The deployment directory. Created if missing. |
| `strategy` | `releases` | `releases` builds `path/releases/<sha>` and a `path/current` link. `inplace` writes the repository files straight into `path`. |
| `shared` | – | Paths that live outside the release and are linked into each one. End a directory with `/`. |
| `keep_releases` | `5` | Old releases to keep. |

With the `releases` strategy the layout is:

```
/srv/app/
├── current -> releases/9f3c2ab...
├── releases/9f3c2ab.../
└── shared/.env
```

The first time a shared path is deployed, an existing file from the repository
is *moved* into `shared/` and becomes the starting point; from then on your
copy is authoritative and the repository's version is ignored.

The `inplace` strategy updates the files that are in the repository and leaves
everything else in `path` alone. Files deleted from the repository stay on
disk. Use it when something outside Deckhand insists on a fixed path.

### What to run

```yaml
run:
  - ["docker", "compose", "pull"]
  - ["docker", "compose", "up", "-d"]
```

Each command is an argv list: the first element is the program, the rest are
arguments passed verbatim. There is no shell, so quoting, `&&`, `|` and `$VAR`
do not apply. This is deliberate — it means nothing in a repository name, tag
or branch can ever be interpreted as a command.

When you genuinely need shell features, ask for them:

```yaml
run:
  - cmd: ["make", "deploy"]
    dir: build              # relative to the release directory
    timeout: 20m
    env:
      TARGET: production
  - shell: "docker compose logs --tail 5 | tee -a /var/log/deploy.log"
```

`on_failure:` takes the same form and runs when a deployment fails, after the
rollback attempt. `DECKHAND_ERROR` holds the failure message.

#### Environment variables

Commands receive a deliberately small environment: `PATH`, `HOME`, `USER`,
locale and temp-directory variables, `DOCKER_HOST`, `SSH_AUTH_SOCK`, and the
platform equivalents on Windows — plus anything you set in `env:`. Deckhand's
own environment, including the GitHub token, is **not** passed through.

On top of that, every command gets:

| Variable | Example |
|---|---|
| `DECKHAND_WATCH` | `shop` |
| `DECKHAND_REPO` | `acme/shop` |
| `DECKHAND_SHA` | `9f3c2ab8...` (full) |
| `DECKHAND_SHORT_SHA` | `9f3c2ab8` |
| `DECKHAND_REF` | `v1.4.0` or `main` |
| `DECKHAND_KIND` | `release`, `tag` or `branch` |
| `DECKHAND_EVENT` | `deploy`, `rollback` or `failure` |
| `DECKHAND_RELEASE_DIR` | `/srv/shop/releases/9f3c2ab8...` |
| `DECKHAND_WORKDIR` | `/srv/shop/current` |
| `DECKHAND_SHARED_DIR` | `/srv/shop/shared` |
| `DECKHAND_PREVIOUS_SHA` | the revision being replaced |
| `DECKHAND_HOST` | the machine's hostname |

### Health check and rollback

```yaml
health:
  http: http://localhost:8080/healthz
  expect_status: 200      # default 200
  initial_delay: 5s       # wait before the first attempt
  retries: 10
  interval: 3s
  timeout: 10s            # per attempt
rollback: auto            # auto (default) | off
```

Instead of `http:` you can use `cmd:` with any command; a zero exit status
means healthy.

If the deploy commands fail, or the health check never passes, and `rollback`
is `auto`, Deckhand switches back to the previous release **and runs the deploy
commands again** — so the service actually returns, not just the files. Without
a health check a deployment counts as successful as soon as your commands exit
zero, which is often not the same thing.

### Verification

For repositories you do not control:

```yaml
verify:
  require_signed_tag: true
  require_signed_commit: false
  allowed_signers: /etc/deckhand/allowed_signers   # SSH allowed-signers format
  pin_sha: ""                                       # deploy only this revision
```

### Timing and safety

| Key | Default | Meaning |
|---|---|---|
| `window` | – | See [time-windows.md](time-windows.md). Absent means deploy immediately. |
| `poll_interval` | from defaults | |
| `command_timeout` | from defaults | |
| `failure_limit` | from defaults | |
| `run_on_start` | `false` | Deploy once at startup even if nothing changed. |
| `allow_repo_scripts` | `false` | Acknowledge that a `run:` command lives inside the deployed tree. |

---

## A complete example

```yaml
version: 1

defaults:
  poll_interval: 60s
  timezone: Europe/Berlin
  command_timeout: 10m

github:
  token_env: DECKHAND_GITHUB_TOKEN

notify:
  on: [failure, rollback]
  webhook: https://ntfy.sh/deploy-alerts-8f3a
  format: ntfy

watch:
  - name: shop
    repo: acme/shop
    trigger:
      type: release
      tag_match: "v*"
    path: /srv/shop
    shared: [".env", "data/", "storage/uploads/"]
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d", "--remove-orphans"]
    health:
      http: http://localhost:8080/healthz
      initial_delay: 5s
      retries: 20
    rollback: auto

  - name: api-staging
    repo: acme/api
    trigger:
      type: branch
      branch: develop
    path: /srv/api-staging
    window:
      allow: ["Mon-Fri 22:00-05:00", "Sat-Sun *"]
    run:
      - ["systemctl", "--user", "restart", "api-staging"]
    health:
      cmd: ["systemctl", "--user", "is-active", "--quiet", "api-staging"]
      retries: 5

  - name: upstream-tool
    repo: someorg/sometool
    auth: none
    trigger:
      type: release
    verify:
      require_signed_tag: true
      allowed_signers: /etc/deckhand/allowed_signers
    path: /opt/sometool
    run:
      - ["/usr/local/bin/rebuild-sometool.sh"]
```
