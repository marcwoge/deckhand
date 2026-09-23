# Als Dienst betreiben

Es gibt zwei Wege zu einem laufenden Dienst, und sie stören sich nicht.

**Aus einem Paket** (`.deb`, `.rpm`): das Paket legt eine Unit in
`/usr/lib/systemd/system/deckhand.service` ab, richtet ein Systemkonto
`deckhand` ein und lässt den Dienst **deaktiviert**. Ein Deployment-Worker ohne
Konfiguration hat nichts zu deployen — ihn zu starten ist eine Entscheidung,
kein Nebeneffekt einer Paketinstallation. Siehe [Aus einem Paket](#aus-einem-paket).

**Aus der Binärdatei**, mit dem Installer:

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

## Aus einem Paket

### Aus dem Paket-Repository

Das ist die Variante, die sich wie jedes andere Paket auf der Maschine verhält —
Updates inklusive:

```bash
# Debian, Ubuntu
sudo curl -fsSLo /usr/share/keyrings/deckhand-archive-keyring.gpg \
  https://marcwoge.github.io/deckhand/deckhand-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/deckhand-archive-keyring.gpg] https://marcwoge.github.io/deckhand/deb ./" |
  sudo tee /etc/apt/sources.list.d/deckhand.list
sudo apt update && sudo apt install deckhand
```

```bash
# Fedora, RHEL, openSUSE
sudo curl -fsSLo /etc/yum.repos.d/deckhand.repo https://marcwoge.github.io/deckhand/deckhand.repo
sudo dnf install deckhand
```

Sowohl die Repository-Metadaten als auch die Pakete sind mit einem GPG-Schlüssel
signiert; sein Fingerprint steht auf
[der Startseite des Repositories](https://marcwoge.github.io/deckhand/). apt
lehnt ein unsigniertes Repository ab — zu Recht, denn das Repository bestimmt,
welche Dateien die Maschine als root installiert. Dieser Schlüssel ist
langlebig, anders als die keyless cosign-Signatur über die Checksummen jedes
Releases; [security.md](security.md#zwei-signaturen-zwei-verschiedene-aufgaben)
erklärt, was welche schützt.

Das Repository hält die fünf neuesten Releases, eine ältere Version lässt sich
also weiterhin festnageln:

```bash
sudo apt install deckhand=0.1.2
```

### Aus einer heruntergeladenen Datei

Wer kein fremdes Repository hinzufügen will: jedes Release enthält die Pakete
auch als einfache Assets:

```bash
# Debian, Ubuntu
sudo apt install ./deckhand_0.1.1_amd64.deb

# Fedora, RHEL, openSUSE
sudo rpm -i deckhand-0.1.1-1.x86_64.rpm
```

Was das Paket anlegt:

| | |
|---|---|
| `/usr/bin/deckhand` | die Binärdatei |
| `/usr/lib/systemd/system/deckhand.service` | die Unit, genauso gehärtet wie die von `service install --system` |
| `/etc/deckhand/` | Modus 0750, `root:deckhand` — root schreibt die Konfiguration, der Dienst liest sie |
| `/var/lib/deckhand/` | Modus 0750, Eigentümer `deckhand` — Zustand und Audit-Log |
| `/usr/share/doc/deckhand/` | diese Dokumentation, englisch und deutsch |
| `/usr/share/deckhand/scripts/` | `encrypt-credentials.sh` und `install-release.sh` |
| der Benutzer `deckhand` | ein Systemkonto ohne Login-Shell und ohne eigenes Home |

Was es bewusst **nicht** tut: den Dienst aktivieren, ihn starten, eine
Konfiguration schreiben oder einen Token erfinden. Die Unit hat außerdem
`ConditionPathExists=/etc/deckhand/deckhand.yaml` — ein aktivierter Dienst ohne
Konfiguration bleibt also still, statt das Journal mit demselben Fehler zu
füllen.

Nach der Installation:

```bash
sudo deckhand init --config /etc/deckhand/deckhand.yaml
sudo chown root:deckhand /etc/deckhand/deckhand.yaml
sudo chmod 640 /etc/deckhand/deckhand.yaml
```

Dann bearbeiten — und das Zustandsverzeichnis setzen, damit ein Befehl, den
**du** aufrufst, denselben Zustand liest wie der Dienst:

```yaml
defaults:
  state_dir: /var/lib/deckhand
```

Prüfen unter dem Konto, das den Dienst später betreibt — nicht als root, das
würde Zustandsdateien anlegen, die der Dienst nicht schreiben kann:

```bash
sudo -u deckhand deckhand check --config /etc/deckhand/deckhand.yaml
sudo -u deckhand deckhand doctor --config /etc/deckhand/deckhand.yaml
sudo systemctl enable --now deckhand
```

### Die Paket-Unit übersteuern

Die Unit liegt in `/usr/lib/systemd/system`, dem Ort für Paket-Units. Also:

* ein Drop-in in `/etc/systemd/system/deckhand.service.d/*.conf` ändert einzelne
  Einstellungen und übersteht Upgrades — genau das Richtige für
  `LoadCredentialEncrypted=`, zusätzliche `ReadWritePaths=` oder einen anderen
  Benutzer;
* eine Datei unter `/etc/systemd/system/deckhand.service` ersetzt die Unit
  vollständig — das schreibt auch `deckhand service install --system`. Das Paket
  fasst sie nicht an.

Das Paket zu entfernen stoppt und deaktiviert den Dienst, behält aber
`/etc/deckhand`, `/var/lib/deckhand` und den Benutzer `deckhand`: dort liegen
Credentials und Historie, und dem Benutzer gehören womöglich deine
Deployment-Verzeichnisse. Nur du weißt, ob das noch gebraucht wird.

### Homebrew und Scoop

```bash
brew tap marcwoge/deckhand
brew install marcwoge/deckhand/deckhand     # Tap Trust: mit vollem Namen installieren
```

```powershell
scoop bucket add deckhand https://github.com/marcwoge/scoop-deckhand
scoop install deckhand
```

Homebrews **Tap Trust** ist der Grund für den vollen Namen: ein Tap allein
erlaubt noch keine Installation über den Kurznamen, weil Code in einem Tap mit
deinen Rechten läuft. `brew trust --formula marcwoge/deckhand/deckhand` macht
den Kurznamen danach nutzbar.

Beide installieren die Release-Binärdatei und halten sich selbst aktuell: Tap und
Bucket erzeugen ihre Datei täglich aus dem neuesten Release neu und nehmen die
Checksummen aus der `SHA256SUMS` des Releases — nachdem die cosign-Signatur
darüber geprüft wurde. Was sich nicht verifizieren lässt, wird nicht
aktualisiert.

Wer kein Tap und keinen Bucket hinzufügen will: jedes Release enthält Formel und
Manifest auch als einfache Assets:

```bash
brew install https://github.com/marcwoge/deckhand/releases/latest/download/deckhand.rb
scoop install https://github.com/marcwoge/deckhand/releases/latest/download/deckhand.json
```

Einen Dienst richtet keines von beiden ein; dafür `deckhand service install` nach
dem Konfigurieren.

### Die Pakete selbst bauen

```bash
go build -o dist/deckhand_v0.1.1_linux_amd64 ./cmd/deckhand
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0
./packaging/build-packages.sh --tag v0.1.1
```

Der Release-Workflow ruft genau diese zwei Skripte auf — was CI veröffentlicht,
kommt also auch lokal heraus.

## Selbst betreiben

`deckhand run` ist ein ganz normaler Vordergrundprozess, der sich bei SIGINT
oder SIGTERM sauber beendet — jeder Supervisor funktioniert also: Docker, runit,
supervisord, das init eines Proxmox-Containers. An den eingebauten Installern
ist nichts Besonderes.
