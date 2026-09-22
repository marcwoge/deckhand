# Running as a service

There are two ways to get a running service, and they do not fight each other.

**From a package** (`.deb`, `.rpm`): the package ships a unit in
`/usr/lib/systemd/system/deckhand.service`, creates a `deckhand` system account,
and leaves the service **disabled**. A deployment worker with no configuration
has nothing to deploy, so starting it is a decision, not a side effect of
installing a package. See [from a package](#from-a-package) below.

**From the binary**, with the installer:

```bash
deckhand service install                                   # current user
sudo deckhand service install --system --user deckhand     # whole machine
deckhand service uninstall
```

The installer writes the platform's service definition, enables it and starts
it. It uses the config path you pass with `--config`, so make sure that path is
readable by the account the service runs as.

## Linux (systemd)

A user install writes `~/.config/systemd/user/deckhand.service`; a system
install writes `/etc/systemd/system/deckhand.service` with a hardened unit:

```ini
[Service]
ExecStart=/usr/local/bin/deckhand run --config /etc/deckhand/deckhand.yaml
Restart=always
RestartSec=10
User=deckhand
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
```

Put the token in an environment file rather than the unit:

```bash
sudo install -m 600 -o deckhand /dev/null /etc/deckhand/env
echo 'DECKHAND_GITHUB_TOKEN=github_pat_...' | sudo tee /etc/deckhand/env
sudo systemctl edit deckhand    # add: [Service] / EnvironmentFile=/etc/deckhand/env
```

For a **user install**, deployments stop when the user logs out unless lingering
is enabled:

```bash
sudo loginctl enable-linger $USER
```

Everyday commands:

```bash
systemctl status deckhand          # add --user for a user install
journalctl -u deckhand -f
sudo systemctl reload deckhand     # apply config changes without stopping
sudo systemctl restart deckhand    # full restart
```

`reload` sends SIGHUP. Deckhand re-reads the configuration, waits for any
deployment that is already running, and starts again with the new settings. An
invalid file is reported and otherwise ignored — the worker keeps running with
what it had, because stopping deployments over a typo is worse than slightly
stale settings. Run `deckhand check` first and you will not find out the hard
way.

## macOS (launchd)

A user install writes `~/Library/LaunchAgents/io.deckhand.worker.plist` and
logs to `~/Library/Logs/deckhand.log`. `--system` writes
`/Library/LaunchDaemons/io.deckhand.worker.plist` and logs to `/var/log`.

```bash
launchctl list | grep deckhand
tail -f ~/Library/Logs/deckhand.log
launchctl kickstart -k gui/$(id -u)/io.deckhand.worker   # restart
```

A user agent runs only while that user is logged in. For a machine that should
deploy unattended, use `--system`.

## Windows (Task Scheduler)

`deckhand service install` registers a scheduled task named **Deckhand** that
starts at boot.

A scheduled task is used rather than a Windows service on purpose: the Service
Control Manager expects services to speak its control protocol, and would
terminate an ordinary console program shortly after starting it. If you want a
true service, wrap Deckhand with a supervisor such as
[WinSW](https://github.com/winsw/winsw) or NSSM — a native service is on the
[roadmap](../../ROADMAP.md).

```powershell
schtasks /Query /TN Deckhand
schtasks /End   /TN Deckhand
schtasks /Run   /TN Deckhand
```

Windows has no SIGHUP, so configuration changes need a restart of the task
(`schtasks /End` then `/Run`) rather than a reload.

Run the installer from an elevated shell for a machine-wide task
(`--system` runs it as `SYSTEM`).

### Symlinks on Windows

The `releases` strategy needs a `current` link. Deckhand creates a symlink if
it can, and otherwise falls back to a directory junction, which works without
special privileges. Enabling Developer Mode gives you real symlinks. If neither
suits your setup, use `strategy: inplace`.

## From a package

```bash
# Debian, Ubuntu
sudo apt install ./deckhand_0.1.1_amd64.deb

# Fedora, RHEL, openSUSE
sudo rpm -i deckhand-0.1.1-1.x86_64.rpm
```

What the package does:

| | |
|---|---|
| `/usr/bin/deckhand` | the binary |
| `/usr/lib/systemd/system/deckhand.service` | the unit, hardened the same way `service install --system` writes it |
| `/etc/deckhand/` | mode 0750, `root:deckhand` — root writes the configuration, the service reads it |
| `/var/lib/deckhand/` | mode 0750, owned by `deckhand` — state and the audit log |
| `/usr/share/doc/deckhand/` | this documentation, in English and German |
| `/usr/share/deckhand/scripts/` | `encrypt-credentials.sh` and `install-release.sh` |
| the `deckhand` user | a system account with no login shell and no home of its own |

What it deliberately does **not** do: enable the service, start it, write a
configuration, or invent a token. The unit also carries
`ConditionPathExists=/etc/deckhand/deckhand.yaml`, so an enabled service without
a configuration stays quiet instead of filling the journal with the same error.

After installing:

```bash
sudo deckhand init --config /etc/deckhand/deckhand.yaml
sudo chown root:deckhand /etc/deckhand/deckhand.yaml
sudo chmod 640 /etc/deckhand/deckhand.yaml
```

Then edit it, and set the state directory so a command **you** run reads the same
state as the service:

```yaml
defaults:
  state_dir: /var/lib/deckhand
```

Check it as the account that will run it — not as root, which would create state
files the service cannot write:

```bash
sudo -u deckhand deckhand check --config /etc/deckhand/deckhand.yaml
sudo -u deckhand deckhand doctor --config /etc/deckhand/deckhand.yaml
sudo systemctl enable --now deckhand
```

### Overriding the packaged unit

The unit lives in `/usr/lib/systemd/system`, which is the vendor location. So:

* a drop-in in `/etc/systemd/system/deckhand.service.d/*.conf` changes single
  settings and survives upgrades — this is what you want for
  `LoadCredentialEncrypted=`, extra `ReadWritePaths=` or a different user;
* a file at `/etc/systemd/system/deckhand.service` replaces the unit entirely,
  which is also what `deckhand service install --system` writes. The package
  will not touch it.

Removing the package stops and disables the service but keeps `/etc/deckhand`,
`/var/lib/deckhand` and the `deckhand` user: they hold credentials, history and
possibly the ownership of your deployment directories, and only you know whether
they are still wanted.

### Homebrew and Scoop

There is no tap and no bucket yet ([issue #8](https://github.com/marcwoge/deckhand/issues/8)),
so the formula and the manifest are published with each release and installed
from their URL:

```bash
brew install https://github.com/marcwoge/deckhand/releases/latest/download/deckhand.rb
```

```powershell
scoop install https://github.com/marcwoge/deckhand/releases/latest/download/deckhand.json
```

Both are generated from the release binaries and their checksums appear in the
signed `SHA256SUMS`, so a tampered formula is caught by the same check as a
tampered binary. Neither installs a service — use `deckhand service install`
after configuring.

### Building the packages yourself

```bash
go build -o dist/deckhand_v0.1.1_linux_amd64 ./cmd/deckhand
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0
./packaging/build-packages.sh --tag v0.1.1
```

The release workflow runs exactly these two scripts, so what CI publishes is
what you get locally.

## Running it yourself

`deckhand run` is a plain foreground process that stops cleanly on SIGINT or
SIGTERM, so any supervisor works — Docker, runit, supervisord, a Proxmox
container's init. There is nothing special about the built-in installers.
