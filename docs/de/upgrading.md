# Aktualisieren

## Die kurze Fassung

**An deiner Konfiguration muss sich nichts ändern.** Das Format ist weiterhin
`version: 1`, kein Schlüssel ist entfallen, und jede Datei, die mit v0.1.0
funktionierte, lädt weiterhin. Alles unten ist freiwillig.

Prüfe es zuerst selbst:

```bash
deckhand check           # liest die Konfiguration und zeigt, was passieren wird
deckhand secrets         # woher jedes Credential kommt und welche Rechte es hat
deckhand doctor          # Erreichbarkeit, Token-Rechte, Pfade
```

Wenn `check` durchläuft, bist du fertig — die neuen Funktionen sind zuschaltbar.

## Die eine Verhaltensänderung

In Pfaden zu Credential-Dateien werden jetzt **Umgebungsvariablen expandiert**,
damit `${CREDENTIALS_DIRECTORY}/github-token` mit systemds verschlüsselten
Credentials funktioniert. Betroffen sind `token_file`, `password_file` und
`private_key_file`.

Die praktische Folge: ein Pfad mit einem literalen `$` wird jetzt interpretiert.

```bash
# Kommt das bei dir vor? Keine Ausgabe heißt: nicht betroffen.
grep -nE '(token_file|password_file|private_key_file):.*\$' /etc/deckhand/deckhand.yaml
```

Ein Pfad wie `/etc/deckhand/my$token` muss umbenannt werden. Alles ohne `$` —
also jeder normale Pfad — verhält sich genau wie vorher.

## Lohnend, nach Nutzen sortiert

### 1. Den Service-Installer erneut laufen lassen

Die erzeugte systemd-Unit hat `LimitCORE=0` bekommen (ein Core-Dump eines
Prozesses mit Tokens würde sie sonst auf die Platte schreiben) und, wenn deine
Konfiguration Credential-Dateien nutzt, auskommentierte
`systemd-creds`-Anweisungen.

```bash
sudo deckhand service install --system     # schreibt die Unit neu, behält deine Einstellungen
sudo systemctl restart deckhand
```

Eigene Drop-ins unter `/etc/systemd/system/deckhand.service.d/` bleiben
unberührt.

### 2. Ein Credential pro Watch

Ein Token, der jedes beobachtete Repository lesen kann, ist für einen Angreifer
mehr wert als ein Token pro Repository — und kein einzelner Token erreicht
Repositories in verschiedenen Accounts.

```yaml
# vorher
github:
  token_env: DECKHAND_GITHUB_TOKEN
watch:
  - name: shop
    repo: acme/shop
  - name: api
    repo: other-org/api

# nachher
github:
  token_env: DECKHAND_GITHUB_TOKEN      # bleibt der Rückfall
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

Lege jeden Token als fein granulierten PAT für genau dieses Repository an,
`Contents: Read-only`. Details in
[configuration](configuration.md#ein-credential-pro-watch).

### 3. Credentials aus dem Secret-Manager

Wenn du `pass`, Vault, 1Password, Bitwarden oder sops schon betreibst, kann
Deckhand dort fragen statt eine Datei zu halten:

```yaml
# vorher
github:
  token_file: /etc/deckhand/github-token

# nachher
github:
  token_command: ["pass", "show", "deckhand/github"]
  token_ttl: 1h
```

Genauso gibt es `password_command` (Registry), `token_command`
(Benachrichtigungskanäle und Watch-Credentials) und `private_key_command`
(App-Schlüssel). Der Wert wird für `token_ttl` gecacht, und ein kurzer Ausfall
des Secret-Managers behält das gecachte Credential, statt ein Deployment
scheitern zu lassen. Siehe [security](security.md#credentials-im-ruhezustand).

### 4. Credential-Dateien im Ruhezustand verschlüsseln (Linux, systemd 250+)

Das ist der Schritt, der eine Datei auf der Platte wirklich schützt:
`systemd-creds` verschlüsselt an TPM oder Host-Schlüssel, eine kopierte Datei
ist damit wertlos, und der Klartext existiert nur in einem tmpfs, das der Dienst
lesen kann.

Dafür gibt es ein Skript. Ohne `--apply` zeigt es nur, was es täte; es legt eine
Sicherung der Konfiguration mit Zeitstempel an und nimmt alles zurück, wenn
`deckhand check` danach nicht durchläuft:

```bash
sudo ./scripts/encrypt-credentials.sh                 # nur anzeigen
sudo ./scripts/encrypt-credentials.sh --apply
sudo systemctl restart deckhand
```

Es löscht die Klartext-Credentials **absichtlich nicht** — es gibt die
`shred`-Befehle aus und überlässt das dir, nachdem du den Dienst wieder laufen
gesehen hast.

Von Hand, pro Credential:

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

Dann `systemctl daemon-reload`, neu starten, prüfen, dass es läuft — und erst
danach den Klartext löschen.

### 5. Das Telegram-Menü

Nichts zu konfigurieren. Hat ein Telegram-Kanal `commands: true`, schick `/menu`
(oder `/start`) und du bekommst Knöpfe für Status, Verlauf, Deploy, Rollback und
Pause, mit Bestätigung, bevor etwas deployt oder zurückgerollt wird. Die
Befehlsliste wird beim Start an Telegram gemeldet, ein `/` im Chat zeigt sie
also mit Beschreibung. Siehe [notifications](notifications.md#das-menü).

## Deckhand selbst aktuell halten

Deckhand hält alles andere aktuell und wird selbst per Hand aktualisiert — damit
kommen Sicherheitsfixes spät, und spät ist hier die schlechteste Variante. Ein
eingebautes `deckhand self-update` ist
[Issue #15](https://github.com/marcwoge/deckhand/issues/15); bis dahin macht
`scripts/install-release.sh` dasselbe von außen, mit derselben Prüfung:

```bash
sudo ./scripts/install-release.sh                   # neuestes Release
sudo ./scripts/install-release.sh --check           # Exit 1, wenn es ein Update gibt
sudo ./scripts/install-release.sh --version v0.1.0  # ein bestimmtes — auch der Weg zurück
```

Es lädt das passende Release-Asset, **prüft die Checksummen-Datei gegen ihre
cosign-Signatur** (an den Release-Workflow dieses Repositories gebunden und im
öffentlichen Transparenz-Log vermerkt), dann die Checksumme des Assets, behält
die alte Binary daneben, ruft `deckhand check` auf und startet den Dienst neu —
mit Rückrollen, wenn die neue Binary nicht läuft oder der Dienst nicht
zurückkommt. Es braucht installiertes `cosign`; nur `SHA256SUMS` zu prüfen würde
nichts beweisen, weil die Checksummen-Datei von derselben Stelle kommt wie die
Binary.

Unbeaufsichtigt ist ein Timer der ehrlichere Weg, statt Deckhand sich selbst
deployen zu lassen:

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

Ein Watch auf dieses Repository geht auch und ist reizvoll, weil es symmetrisch
ist:

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

Nur: dabei ersetzt das Deployment das, was es ausführt, und der Neustart des
Dienstes beendet genau den Deploy-Schritt, der ihn auslöst. Es funktioniert — die
Binary liegt vor dem Neustart schon an ihrem Platz —, aber der Audit-Eintrag
dieses Deployments sieht wie ein Fehlschlag aus. Der Timer hat diese
Umständlichkeit nicht.

**Das Argument gegen automatische Updates überhaupt**, der Ehrlichkeit halber:
ein Worker, der sich selbst aktualisiert, kann sich selbst kaputtmachen — und
dann stehen alle Deployments dieser Maschine, bis sich jemand einloggt. Das ist
schlimmer als eine etwas alte Version. Deshalb behält das Skript die alte
Binary, prüft bevor es etwas ersetzt, rollt zurück wenn der Dienst nicht
zurückkommt, und läuft nur, wenn du es anstößt.

## Zurück auf die alte Binary

Die neuen Schlüssel sind Ergänzungen, das heißt aber auch: eine Konfiguration
mit `token_command`, einem `auth:`-Block oder `${CREDENTIALS_DIRECTORY}` lädt
auf einer älteren Binary **nicht**. Wenn du zurück musst, stelle die Sicherung
der Konfiguration zusammen mit der Binary wieder her — `deckhand service
install` behält nichts von deiner Konfiguration, und
`scripts/encrypt-credentials.sh` legt `deckhand.yaml.bak.<zeitstempel>` daneben
ab. `install-release.sh` behält die alte Binary als `deckhand.<alte-version>`,
der Weg zurück braucht also kein Netz:

```bash
sudo mv /usr/local/bin/deckhand.v0.1.0 /usr/local/bin/deckhand
sudo systemctl restart deckhand
```
