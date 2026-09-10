# Als Dienst betreiben

```bash
deckhand service install                                   # aktueller Benutzer
sudo deckhand service install --system --user deckhand     # ganze Maschine
deckhand service uninstall
```

Der Installer schreibt die Dienstdefinition der Plattform, aktiviert sie und
startet sie. Er verwendet den Konfigurationspfad aus `--config` — achte darauf,
dass dieser Pfad für das Konto lesbar ist, unter dem der Dienst läuft.

## Linux (systemd)

Eine Benutzerinstallation schreibt `~/.config/systemd/user/deckhand.service`,
eine Systeminstallation `/etc/systemd/system/deckhand.service` mit einer
gehärteten Unit:

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

Lege den Token in eine Umgebungsdatei, nicht in die Unit:

```bash
sudo install -m 600 -o deckhand /dev/null /etc/deckhand/env
echo 'DECKHAND_GITHUB_TOKEN=github_pat_...' | sudo tee /etc/deckhand/env
sudo systemctl edit deckhand    # ergänze: [Service] / EnvironmentFile=/etc/deckhand/env
```

Bei einer **Benutzerinstallation** enden die Deployments, sobald sich der
Benutzer abmeldet — außer Lingering ist aktiviert:

```bash
sudo loginctl enable-linger $USER
```

Befehle für den Alltag:

```bash
systemctl status deckhand          # bei Benutzerinstallation mit --user
journalctl -u deckhand -f
sudo systemctl reload deckhand     # Konfiguration übernehmen, ohne zu stoppen
sudo systemctl restart deckhand    # vollständiger Neustart
```

`reload` sendet SIGHUP. Deckhand liest die Konfiguration neu, wartet auf ein
gerade laufendes Deployment und startet mit den neuen Einstellungen wieder. Eine
ungültige Datei wird gemeldet und ansonsten ignoriert — der Worker läuft mit dem
weiter, was er hatte, denn Deployments wegen eines Tippfehlers zu stoppen ist
schlimmer als leicht veraltete Einstellungen. Führe vorher `deckhand check` aus,
dann erfährst du es nicht auf die harte Tour.

## macOS (launchd)

Eine Benutzerinstallation schreibt
`~/Library/LaunchAgents/io.deckhand.worker.plist` und loggt nach
`~/Library/Logs/deckhand.log`. `--system` schreibt
`/Library/LaunchDaemons/io.deckhand.worker.plist` und loggt nach `/var/log`.

```bash
launchctl list | grep deckhand
tail -f ~/Library/Logs/deckhand.log
launchctl kickstart -k gui/$(id -u)/io.deckhand.worker   # neu starten
```

Ein Benutzer-Agent läuft nur, solange dieser Benutzer angemeldet ist. Für eine
Maschine, die unbeaufsichtigt deployen soll, nimm `--system`.

## Windows (Aufgabenplanung)

`deckhand service install` legt eine geplante Aufgabe namens **Deckhand** an,
die beim Systemstart beginnt.

Bewusst eine geplante Aufgabe statt eines Windows-Dienstes: Der Dienst-Manager
erwartet, dass Dienste sein Steuerungsprotokoll sprechen, und würde ein normales
Konsolenprogramm kurz nach dem Start wieder beenden. Wenn du einen echten Dienst
möchtest, kapsle Deckhand mit einem Supervisor wie
[WinSW](https://github.com/winsw/winsw) oder NSSM — ein nativer Dienst steht auf
der [Roadmap](../../ROADMAP.md).

```powershell
schtasks /Query /TN Deckhand
schtasks /End   /TN Deckhand
schtasks /Run   /TN Deckhand
```

Windows kennt kein SIGHUP; Konfigurationsänderungen brauchen dort einen Neustart
der Aufgabe (`schtasks /End`, dann `/Run`) statt eines Reloads.

Für eine maschinenweite Aufgabe führe den Installer in einer Shell mit erhöhten
Rechten aus (`--system` lässt sie als `SYSTEM` laufen).

### Symlinks unter Windows

Die Strategie `releases` braucht einen Link für `current`. Deckhand legt einen
Symlink an, wenn er darf, und weicht sonst auf eine Verzeichnis-Junction aus,
die ohne besondere Rechte funktioniert. Mit aktiviertem Entwicklermodus bekommst
du echte Symlinks. Passt beides nicht, nimm `strategy: inplace`.

## Selbst betreiben

`deckhand run` ist ein ganz normaler Vordergrundprozess, der sich bei SIGINT
oder SIGTERM sauber beendet — jeder Supervisor funktioniert also: Docker, runit,
supervisord, das init eines Proxmox-Containers. An den eingebauten Installern
ist nichts Besonderes.
