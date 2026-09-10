# Running as a service

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
sudo systemctl restart deckhand    # after editing the config
```

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

Run the installer from an elevated shell for a machine-wide task
(`--system` runs it as `SYSTEM`).

### Symlinks on Windows

The `releases` strategy needs a `current` link. Deckhand creates a symlink if
it can, and otherwise falls back to a directory junction, which works without
special privileges. Enabling Developer Mode gives you real symlinks. If neither
suits your setup, use `strategy: inplace`.

## Running it yourself

`deckhand run` is a plain foreground process that stops cleanly on SIGINT or
SIGTERM, so any supervisor works — Docker, runit, supervisord, a Proxmox
container's init. There is nothing special about the built-in installers.
