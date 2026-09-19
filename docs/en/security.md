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

## Authenticating as a GitHub App

A personal access token is the quickest way to start, and for one machine with
read access it is perfectly fine. A GitHub App is worth the extra setup when:

* **Nothing should expire.** A fine-grained token lasts a year at most, and when
  it lapses every deployment stops. An app's private key does not expire; it
  mints installation tokens that live one hour and renew themselves.
* **Deployments should not hang on a person.** A token belongs to your account.
  An app is its own identity, so deployments survive someone leaving.
* **Several machines or accounts are involved.** One app can be installed in
  several accounts, and its rate limit is per installation rather than shared
  with everything else you do.

The trade is real and worth stating: the private key sits on the machine
permanently and never expires, and it covers every installation of the app. A
stolen `Contents: Read` token is worth less than a stolen app key. Treat the key
like an SSH host key — owned by the service account, mode 0600, never in a
repository.

### Creating the app

1. **Settings → Developer settings → GitHub Apps → New GitHub App**
   (for an organisation: its settings, same path).
2. Name it something recognisable — it appears in the audit log. Homepage URL
   can be your repository.
3. Under **Webhook**, untick *Active*. Deckhand polls; it needs no webhook.
4. **Repository permissions → Contents: Read-only.** Nothing else. Not metadata
   write, not actions, not workflows.
5. **Where can this GitHub App be installed** → *Only on this account*, unless
   you have reason to share it.
6. Create it, then note the **App ID** at the top of the page.
7. **Generate a private key** at the bottom. The `.pem` downloads once — there
   is no second chance, only a new key.
8. **Install App** in the left sidebar → choose the account → *Only select
   repositories* → pick the ones Deckhand will watch.

### Configuring it

```bash
sudo install -m 600 -o deckhand ~/Downloads/your-app.2026-09-19.private-key.pem \
  /etc/deckhand/app-private-key.pem
```

```yaml
github:
  app:
    id: "123456"
    private_key_file: /etc/deckhand/app-private-key.pem
    # installation_id: 12345678   # optional, see below
```

Remove `token_env` / `token_file` when you switch — Deckhand refuses a config
carrying both, rather than silently picking one.

| Key | Meaning |
|---|---|
| `id` | The App ID from the app's settings page. Its client ID also works. |
| `private_key_file` | Path to the `.pem`. Must not be readable by others. |
| `private_key_env` | The key's contents in an environment variable, for setups that inject secrets. Literal `\n` sequences are accepted. |
| `private_key` | Inline. Works, but puts the key in a file you might commit. |
| `installation_id` | Pins every repository to one installation. Leave it out and Deckhand asks GitHub which installation covers each repository, which is what you want when the app is installed in more than one account. |

Check it before starting the service:

```bash
deckhand check     # says: github auth: GitHub App 123456 (...)
deckhand doctor    # confirms the app against GitHub and names it
```

`doctor` reports the app's slug (`authenticated as GitHub App @your-app`), and
because it also reads each repository it proves the installation actually covers
them — the mistake people make is installing the app but forgetting to add a
repository.

### What Deckhand does with it

Nothing you have to manage. Before each API request and each fetch it asks for a
token, renewing when less than ten minutes remain. Tokens are cached per
installation, and if a renewal fails while the current token is still valid, the
deployment proceeds on the old one rather than failing over a hiccup. The token
reaches git through the askpass helper, exactly as a personal token does, so it
never appears in a process list or in `.git/config`.

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
