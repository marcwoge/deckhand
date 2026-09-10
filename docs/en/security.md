# Security

The short version is in [SECURITY.md](../../SECURITY.md); this page is the
practical setup guide.

## The one thing to understand

**Deckhand takes the command from your configuration file, never from the
repository.** The repository decides what code is deployed; your config decides
what is executed. Keep that boundary and a compromised repository is a bad day,
not a compromised machine.

The boundary leaks in exactly two ways, both of which you choose:

* Your command runs code from the repository anyway — `docker compose up` reads
  the compose file from the deployed tree, `npm start` runs a script from
  `package.json`. That is normal and fine for a repository you maintain.
* Your command *is* a file in the repository. `deckhand check` warns about
  this, and Deckhand requires `allow_repo_scripts: true` before you can do it.

## Setting it up properly

### 1. A token that can only read

Create a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new):

* **Repository access:** only the repositories you actually watch.
* **Permissions:** `Contents: Read-only`. Nothing else. Not metadata write, not
  actions, not workflows.
* **Expiry:** set one, and put the renewal in your calendar.

Public repositories that belong to someone else need no token — set
`auth: none` on that watch. A token still helps with rate limits, see
[configuration](configuration.md#rate-limits).

Keep the token out of the config file:

```bash
# systemd: /etc/deckhand/env, mode 0600, owned by the service user
DECKHAND_GITHUB_TOKEN=github_pat_...
```

```yaml
github:
  token_env: DECKHAND_GITHUB_TOKEN
```

Deckhand refuses to read a `token_file` that others can read, and refuses to
start at all with a config that group or others can write.

### 2. A user that can only deploy

```bash
sudo useradd --system --home-dir /var/lib/deckhand --shell /usr/sbin/nologin deckhand
sudo mkdir -p /var/lib/deckhand /srv/shop
sudo chown -R deckhand:deckhand /var/lib/deckhand /srv/shop
```

Deckhand refuses to run as root unless you pass `--allow-root`. Take the refusal
seriously: a deployment worker that runs as root turns every deployment mistake
into a root-level mistake.

If a command genuinely needs privileges, grant exactly that command:

```
# /etc/sudoers.d/deckhand
deckhand ALL=(root) NOPASSWD: /bin/systemctl restart shop.service
```

```yaml
run:
  - ["sudo", "-n", "/bin/systemctl", "restart", "shop.service"]
```

For Docker, adding the service user to the `docker` group is equivalent to
giving it root. If that matters to you, use a rootless Docker socket or a
narrow sudoers rule instead.

### 3. Verification for code you do not control

```yaml
verify:
  require_signed_tag: true
  allowed_signers: /etc/deckhand/allowed_signers
```

The allowed-signers file is git's SSH signature format, one principal per line:

```
maintainer@example.com ssh-ed25519 AAAAC3NzaC1lZDI1...
```

Prefer `release` or `tag` triggers over `branch` triggers for foreign
repositories: a branch follows anyone who can push, a signed tag follows a key
you named.

You can also pin a watch to one revision while you evaluate an update:

```yaml
verify:
  pin_sha: "9f3c2ab8e4d1..."
```

### 4. Keep the blast radius small

* One deployment directory per watch, owned by the service user, nothing else in it.
* Secrets in `shared/`, mode 0600, backed up separately from the code.
* Use the hardened systemd unit written by `deckhand service install --system`.
* Point notifications at a channel someone reads, and check `deckhand history`
  when something looks odd.

## What Deckhand does on its own

You do not have to configure any of this:

* No listening socket; only outbound HTTPS to the GitHub API.
* The token is passed to git through the child process environment, so it does
  not appear in the process list — and is never passed to your deploy commands.
* Every git call runs with `core.hooksPath` emptied, `credential.helper`
  cleared, and `protocol.ext`/`protocol.file` refused, so repository content
  cannot change how git behaves.
* The exported tree is unpacked in-process with every path validated: absolute
  paths, `..`, links pointing outside the release and writes through a
  symlinked directory are refused; device nodes are skipped.
* Commands run as an argv vector without a shell unless you ask for one, and
  are killed together with their children when they exceed their timeout.
* A watch that fails `failure_limit` times in a row halts instead of looping.

## Reporting a problem

Use [private vulnerability reporting](https://github.com/marcwoge/deckhand/security/advisories/new).
Please do not open a public issue for a security bug.
