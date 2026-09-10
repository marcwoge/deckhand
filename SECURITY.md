# Security policy

## Reporting a vulnerability

Please report security issues privately through
[GitHub's private vulnerability reporting](https://github.com/marcwoge/deckhand/security/advisories/new)
rather than in a public issue. You can expect an acknowledgement within a few
days. Please include what you did, what happened, and what you expected.

## Threat model

Deckhand's stated assumption is that **the watched repository is maintained and
not compromised**. It is a deployment worker, not a sandbox: if you tell it to
run `docker compose up` against code from a repository, that code will
eventually execute on your machine. Nothing below changes that.

What Deckhand does do is make sure it is not itself the weak point, and that
the blast radius of a mistake stays small.

### What an attacker would have to do

| Attack | Why it does not work |
|---|---|
| Reach the worker over the network | There is no listening socket. Deckhand only makes outbound HTTPS requests to the GitHub API. |
| Make the worker run a different command | Commands come from the local configuration file, never from the repository. The repository can change *what code is deployed*, never *what is executed*. |
| Steal the GitHub token from a deploy command | Deploy commands receive an explicit allow-list of environment variables. The token is not in it. |
| Read the token from the process list | The token is passed to git through the child process environment, never on a command line. |
| Use a git hook committed to the repository | `core.hooksPath` is emptied for every git invocation, and hooks are not fetched by clone in the first place. |
| Escape the release directory with a crafted archive | Every entry of the exported tree is validated: absolute paths, `..`, links pointing outside the tree and writes through a symlinked directory are all refused. Device nodes are skipped. |
| Redirect the fetch to a local or exotic transport | `protocol.ext.allow` and `protocol.file.allow` are set to `never` unless the operator deliberately configured a local source. |
| Modify the config to gain privileges | Deckhand refuses to start with a group- or world-writable config, or a token file readable by others. |
| Have the worker run as root | Deckhand refuses to start as root unless `--allow-root` is passed explicitly. |

### What is still your responsibility

* **Token scope.** Use a fine-grained personal access token with `Contents: Read-only`, limited to the repositories you actually watch. Deckhand never needs write access, and never needs any other permission.
* **The service account.** Run Deckhand as a dedicated unprivileged user that owns only the deployment directories. Do not give it `sudo` unless a specific command needs it, and then only for that command via a narrow sudoers rule.
* **Your deploy command.** `docker compose up` runs the compose file from the repository, which is repository-controlled by design. That is the trade you are making — it is fine for a repository you maintain, and worth thinking about for one you do not.
* **Commands inside the deployed tree.** If your `run:` command points at a script inside `path:`, the repository decides what runs. Deckhand warns about this in `deckhand check` and requires `allow_repo_scripts: true` to acknowledge it.
* **Repositories you do not control.** For those, prefer release triggers over branch triggers, and turn on `verify.require_signed_tag` with an `allowed_signers` file.
* **Where your notifications go.** A notification names repositories, hosts and error messages. On the public ntfy.sh a topic has no access control at all, so use a long random name or host ntfy yourself. Telegram's servers can read what you send them. Neither is disqualifying for deployment metadata, but both are choices you should make knowingly.

### The Telegram remote control

Enabling `commands: true` lets a chat trigger deployments and rollbacks, so it deserves its own paragraph:

* Commands are accepted **only** from the configured `chat_id`. Any other chat is ignored without a reply, so a stranger who finds the bot cannot even confirm it exists.
* Commands older than five minutes are refused, and the backlog is skipped at startup — otherwise restarting deckhand could execute a `/deploy` sent yesterday, since Telegram holds undelivered messages for a day.
* The handled-update offset is persisted, so a restart never replays a command.
* Stealing the bot token is not enough to deploy: it allows reading notifications and impersonating the bot, but not sending from your chat ID.
* The bot can do nothing that `deckhand deploy` and `deckhand rollback` cannot. It is a remote control for existing commands, not a new privilege.

If that trade is not one you want, leave `commands` off and use the channel for alerts only.

### Reducing the blast radius further

* Give the service account no shell (`/usr/sbin/nologin`) and no home directory beyond its state directory.
* Use the hardened systemd unit that `deckhand service install --system` writes; it sets `NoNewPrivileges`, `ProtectSystem=full`, `ProtectHome=read-only` and friends.
* Keep `shared/` mode 0600 for secrets, and back it up separately.
* Watch the audit log (`deckhand history`), or forward notifications to a channel you actually read.

## How this repository is protected

Deckhand is a deployment tool, so a compromised release would run on other
people's servers. The project is set up accordingly:

* **Releases are signed.** The checksum file is signed with cosign in keyless
  mode and every binary carries build provenance, so a tampered download can be
  detected. The verification commands are in every release's notes.
* **Workflows have minimal permissions.** CI declares `contents: read` and uses
  no secrets, so a pull request from a fork cannot reach anything. Only the
  release workflow has write access, and it runs on version tags, which only a
  maintainer can push.
* **`pull_request`, never `pull_request_target`.** Fork code never runs with
  access to repository secrets.
* **Dependencies are watched.** Dependabot tracks the Go module and the
  workflow actions monthly.
* **Secret scanning with push protection** is enabled, and the history has been
  checked: no credential has ever been committed.

If you are hardening a fork of this project, consider pinning the workflow
actions to commit SHAs rather than tags. A tag can be moved; a SHA cannot.

## No warranty

Deckhand is provided under the Apache License 2.0, which disclaims all warranty
(section 7) and limits liability (section 8). It fetches code and runs commands
on your machines because you told it to. Read [docs/en/security.md](docs/en/security.md)
before pointing it at anything you care about, and test a watch against a
staging target first.

## Supported versions

Deckhand is pre-1.0. Security fixes are made on the latest released version.
