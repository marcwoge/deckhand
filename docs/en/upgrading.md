# Upgrading

## The short version

**Nothing in your configuration has to change.** The format is still
`version: 1`, no key was removed, and every file that worked with v0.1.0 still
loads. Everything described below is optional.

Verify it yourself before anything else:

```bash
deckhand check           # parses the config and reports what it will do
deckhand secrets         # where each credential comes from, and its permissions
deckhand doctor          # connectivity, token scope, paths
```

If `check` passes, you are done — the new features are opt-in.

## The one behaviour change

Credential **file paths now expand environment variables**, so that
`${CREDENTIALS_DIRECTORY}/github-token` works with systemd's encrypted
credentials. This affects `token_file`, `password_file` and
`private_key_file`.

The practical consequence: a path containing a literal `$` is now interpreted.

```bash
# Does your configuration have one? If this prints nothing, you are unaffected.
grep -nE '(token_file|password_file|private_key_file):.*\$' /etc/deckhand/deckhand.yaml
```

A path like `/etc/deckhand/my$token` needs renaming. Anything without a `$` —
which is every normal path — behaves exactly as before.

## Worth doing, in order of value

### 1. Re-run the service installer

The generated systemd unit gained `LimitCORE=0` (a crash dump of a process
holding tokens would otherwise write them to disk) and, when your configuration
uses credential files, commented `systemd-creds` instructions.

```bash
sudo deckhand service install --system     # rewrites the unit, keeps your settings
sudo systemctl restart deckhand
```

Your own drop-ins under `/etc/systemd/system/deckhand.service.d/` are untouched.

### 2. A credential per watch

One token that can read every watched repository is worth more to an attacker
than one token per repository — and no single token reaches repositories in
different accounts.

```yaml
# before
github:
  token_env: DECKHAND_GITHUB_TOKEN
watch:
  - name: shop
    repo: acme/shop
  - name: api
    repo: other-org/api

# after
github:
  token_env: DECKHAND_GITHUB_TOKEN      # still the fallback
watch:
  - name: shop
    repo: acme/shop
    auth:
      token_file: /etc/deckhand/tokens/shop
  - name: api
    repo: other-org/api
    auth:
      token_env: DECKHAND_TOKEN_API
```

Create each token as a fine-grained PAT restricted to that one repository,
`Contents: Read-only`. Details in
[configuration](configuration.md#a-credential-per-watch).

### 3. Credentials from your secret manager

If you already run `pass`, Vault, 1Password, Bitwarden or sops, Deckhand can ask
it instead of holding a file:

```yaml
# before
github:
  token_file: /etc/deckhand/github-token

# after
github:
  token_command: ["pass", "show", "deckhand/github"]
  token_ttl: 1h
```

The same works for `password_command` (registry), `token_command` (notification
channels and per-watch credentials) and `private_key_command` (app key). The
value is cached for `token_ttl`, and a brief outage of the secret manager keeps
the cached credential instead of failing a deployment. See
[security](security.md#credentials-at-rest).

### 4. Encrypt credential files at rest (Linux, systemd 250+)

This is the one that actually protects a file on disk: `systemd-creds` encrypts
to the TPM or a host key, so a copied file is worthless, and the plaintext only
ever exists in a tmpfs the service can read.

There is a script for it. It is a dry run unless you pass `--apply`, it keeps a
timestamped backup of your configuration, and it undoes everything if
`deckhand check` does not pass afterwards:

```bash
sudo ./scripts/encrypt-credentials.sh                 # show what it would do
sudo ./scripts/encrypt-credentials.sh --apply
sudo systemctl restart deckhand
```

It deliberately does **not** delete your plaintext credentials — it prints the
`shred` commands and leaves that to you, after you have seen the service come
back up.

By hand, per credential:

```bash
sudo systemd-creds encrypt --name=github-token \
  /etc/deckhand/github-token /etc/deckhand/github-token.cred
```

```ini
# /etc/systemd/system/deckhand.service.d/credentials.conf
[Service]
LoadCredentialEncrypted=github-token:/etc/deckhand/github-token.cred
```

```yaml
github:
  token_file: ${CREDENTIALS_DIRECTORY}/github-token
```

Then `systemctl daemon-reload`, restart, confirm it works, and only then delete
the plaintext.

### 5. The Telegram menu

Nothing to configure. If a Telegram channel has `commands: true`, send `/menu`
(or `/start`) and you get buttons for status, history, deploy, rollback and
pause, with a confirmation step before anything is deployed or rolled back. The
command list is published to Telegram on startup, so typing `/` offers them with
descriptions. See [notifications](notifications.md#the-menu).

## Keeping Deckhand itself up to date

Deckhand keeps everything else current but is updated by hand, which means
security fixes land late — the worst kind of late. A built-in `deckhand
self-update` is [issue #15](https://github.com/marcwoge/deckhand/issues/15);
until then `scripts/install-release.sh` does the same job from the outside, with
the same verification:

```bash
sudo ./scripts/install-release.sh                   # latest release
sudo ./scripts/install-release.sh --check           # exits 1 if an update exists
sudo ./scripts/install-release.sh --version v0.1.0  # a specific one - also the way back
```

It downloads the release asset for this machine, **verifies the checksum file
against its cosign signature** (bound to this repository's release workflow and
recorded in the public transparency log), then the asset's checksum, keeps the
previous binary next to the new one, runs `deckhand check`, and restarts the
service — rolling back if the new binary does not run or the unit does not come
back up. It needs `cosign` installed; checking `SHA256SUMS` alone would prove
nothing, because the checksum file comes from the same place as the binary.

Unattended, the honest way is a timer rather than having Deckhand deploy itself:

```ini
# /etc/systemd/system/deckhand-update.service
[Unit]
Description=Update Deckhand from its signed releases

[Service]
Type=oneshot
ExecStart=/opt/deckhand/install-release.sh --unit deckhand.service
```

```ini
# /etc/systemd/system/deckhand-update.timer
[Unit]
Description=Check for a new Deckhand release

[Timer]
OnCalendar=Sun 03:30
RandomizedDelaySec=30m
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl enable --now deckhand-update.timer
```

A watch on this repository works too, and is tempting because it is symmetrical:

```yaml
watch:
  - name: deckhand-itself
    repo: marcwoge/deckhand
    auth: none
    trigger: { type: release }
    path: /opt/deckhand-src
    window: ["Sun 03:00-05:00"]
    run:
      - ["/opt/deckhand-src/current/scripts/install-release.sh", "--restart", "systemctl"]
```

Be aware of what that means: the deployment replaces the thing performing it, and
restarting the service kills the deploy step that is doing the restarting. It
works — the binary is already in place before the restart — but the audit entry
for that deployment will look like a failure. The timer has none of this
awkwardness.

**The argument against updating automatically at all**, worth stating: a worker
that updates itself can break itself, and when it does, every deployment on that
machine stops until someone logs in. That is worse than running a slightly old
version. Which is why the script keeps the previous binary, verifies before
replacing anything, rolls back when the service does not come back, and only
runs when you tell it to.

## Rolling back to the previous binary

The new configuration keys are additive, so a configuration that uses
`token_command`, an `auth:` block or `${CREDENTIALS_DIRECTORY}` will **not** load
on an older binary. If you need to go back, restore the configuration backup
alongside the binary — `deckhand service install` keeps nothing of your config,
and `scripts/encrypt-credentials.sh` writes `deckhand.yaml.bak.<timestamp>` next
to it. `install-release.sh` keeps the previous binary as
`deckhand.<old-version>`, so going back needs no network access:

```bash
sudo mv /usr/local/bin/deckhand.v0.1.0 /usr/local/bin/deckhand
sudo systemctl restart deckhand
```
