# Sicherheit

Die Kurzfassung steht in [SECURITY.md](../../SECURITY.md) (englisch); diese
Seite ist die praktische Einrichtungsanleitung.

## Das Eine, das man verstehen muss

**Deckhand nimmt den Befehl aus deiner Konfigurationsdatei, niemals aus dem
Repository.** Das Repository bestimmt, welcher Code ausgeliefert wird; deine
Konfiguration bestimmt, was ausgeführt wird. Halte diese Grenze ein, und ein
kompromittiertes Repository ist ein schlechter Tag — keine kompromittierte
Maschine.

Die Grenze wird genau auf zwei Wegen durchlässig, und über beide entscheidest du:

* Dein Befehl führt ohnehin Code aus dem Repository aus — `docker compose up`
  liest die Compose-Datei aus dem ausgelieferten Verzeichnis, `npm start` führt
  ein Skript aus `package.json` aus. Das ist normal und für ein Repository, das
  du selbst pflegst, völlig in Ordnung.
* Dein Befehl *ist* eine Datei aus dem Repository. `deckhand check` warnt davor,
  und Deckhand verlangt `allow_repo_scripts: true`, bevor du das tun darfst.

## Saubere Einrichtung

### 1. Ein Token, der nur lesen kann

Erstelle einen [fine-grained Personal Access Token](https://github.com/settings/personal-access-tokens/new):

* **Repository-Zugriff:** nur die Repositories, die du tatsächlich beobachtest.
* **Berechtigungen:** `Contents: Read-only`. Sonst nichts. Kein Metadata-Write,
  keine Actions, keine Workflows.
* **Ablaufdatum:** setze eines und trag dir die Erneuerung in den Kalender.

Öffentliche Repositories anderer Leute brauchen keinen Token — setze bei diesem
Watch `auth: none`. Für die Rate-Limits hilft ein Token trotzdem, siehe
[Konfiguration](configuration.md#rate-limits).

Halte den Token aus der Konfigurationsdatei heraus:

```bash
# systemd: /etc/deckhand/env, Rechte 0600, Eigentümer ist der Dienstbenutzer
DECKHAND_GITHUB_TOKEN=github_pat_...
```

```yaml
github:
  token_env: DECKHAND_GITHUB_TOKEN
```

Deckhand weigert sich, eine `token_file` zu lesen, die andere lesen können, und
startet gar nicht erst mit einer Konfiguration, die Gruppe oder andere
beschreiben dürfen.

### 2. Ein Benutzer, der nur deployen kann

```bash
sudo useradd --system --home-dir /var/lib/deckhand --shell /usr/sbin/nologin deckhand
sudo mkdir -p /var/lib/deckhand /srv/shop
sudo chown -R deckhand:deckhand /var/lib/deckhand /srv/shop
```

Deckhand weigert sich, als root zu laufen, sofern du nicht `--allow-root`
angibst. Nimm diese Weigerung ernst: Ein Deployment-Worker, der als root läuft,
macht aus jedem Deployment-Fehler einen Fehler mit Root-Rechten.

Braucht ein Befehl wirklich Privilegien, gib genau diesen einen frei:

```
# /etc/sudoers.d/deckhand
deckhand ALL=(root) NOPASSWD: /bin/systemctl restart shop.service
```

```yaml
run:
  - ["sudo", "-n", "/bin/systemctl", "restart", "shop.service"]
```

Bei Docker gilt: Den Dienstbenutzer in die Gruppe `docker` aufzunehmen ist
gleichbedeutend damit, ihm root zu geben. Wenn dir das wichtig ist, nutze einen
rootless Docker-Socket oder eine eng gefasste sudoers-Regel.

### 3. Prüfungen für fremden Code

```yaml
verify:
  require_signed_tag: true
  allowed_signers: /etc/deckhand/allowed_signers
```

Die allowed-signers-Datei nutzt das SSH-Signaturformat von git, ein Principal
je Zeile:

```
maintainer@example.com ssh-ed25519 AAAAC3NzaC1lZDI1...
```

Bevorzuge bei fremden Repositories `release`- oder `tag`-Trigger gegenüber
`branch`-Triggern: Ein Branch folgt jedem, der pushen darf, ein signierter Tag
folgt einem Schlüssel, den du benannt hast.

Du kannst einen Watch auch auf eine Revision festnageln, während du ein Update
prüfst:

```yaml
verify:
  pin_sha: "9f3c2ab8e4d1..."
```

### 4. Den Schadensradius klein halten

* Ein Deployment-Verzeichnis je Watch, dem Dienstbenutzer gehörend, sonst nichts darin.
* Geheimnisse in `shared/` mit Rechten 0600, getrennt vom Code gesichert.
* Nutze die gehärtete systemd-Unit, die `deckhand service install --system` schreibt.
* Richte Benachrichtigungen auf einen Kanal, den jemand liest, und schau in
  `deckhand history`, wenn etwas seltsam aussieht.

## Was Deckhand von sich aus tut

Davon musst du nichts konfigurieren:

* Kein lauschender Socket; nur ausgehendes HTTPS zur GitHub-API.
* Der Token wird git über die Umgebung des Kindprozesses übergeben, erscheint
  also nicht in der Prozessliste — und wird niemals an deine Deploy-Befehle
  weitergereicht.
* Jeder git-Aufruf läuft mit geleertem `core.hooksPath`, geleertem
  `credential.helper` und verweigertem `protocol.ext`/`protocol.file`, damit
  Repository-Inhalte das Verhalten von git nicht ändern können.
* Der exportierte Baum wird im Prozess selbst entpackt, wobei jeder Pfad geprüft
  wird: absolute Pfade, `..`, Links aus dem Release heraus und Schreibvorgänge
  durch ein verlinktes Verzeichnis werden abgelehnt; Gerätedateien werden
  übersprungen.
* Befehle laufen als Argumentvektor ohne Shell, sofern du nicht ausdrücklich
  eine anforderst, und werden bei Zeitüberschreitung samt Kindprozessen beendet.
* Ein Watch, der `failure_limit` Mal hintereinander scheitert, stoppt sich
  selbst, statt in einer Schleife zu kreisen.

## Ein Problem melden

Nutze bitte [Private Vulnerability Reporting](https://github.com/marcwoge/deckhand/security/advisories/new)
und kein öffentliches Issue für Sicherheitsfehler.
