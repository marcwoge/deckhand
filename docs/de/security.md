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

`deckhand secrets` zeigt jedes Credential, seine Quelle und die Rechte jeder
Datei — nie einen Wert:

```
$ deckhand secrets
CREDENTIAL        SOURCE                          NOTE
github token      command pass                    fetched from a secret manager
watch shop        file /etc/deckhand/shop-token    mode 0600
notify #1 (ntfy)  environment DECKHAND_NTFY       visible in /proc/<pid>/environ to root and this user
```

### Credentials im Ruhezustand

Der ehrliche Ausgangspunkt: **ein Credential mit einem Schlüssel zu
verschlüsseln, der auf derselben Maschine liegt, bringt fast nichts.** Deckhand
muss es unbeaufsichtigt entschlüsseln, also kann wer den Geheimtext lesen kann
in der Regel auch den Schlüssel lesen. „Token-Datei verschlüsseln“ sieht
sicherer aus und ändert nichts.

Drei Dinge helfen wirklich, und Deckhand unterstützt alle drei.

**1. Aus etwas holen, das widerrufen und protokollieren kann.** Jedes Credential
kann aus einem Kommando kommen:

```yaml
github:
  token_command: ["pass", "show", "deckhand/github"]
  token_ttl: 1h                 # wie lange der Wert wiederverwendet wird, Standard 1h

registry:
  ghcr.io:
    password_command: ["op", "read", "op://infra/ghcr/token"]

notify:
  channels:
    - type: telegram
      token_command: ["vault", "kv", "get", "-field=token", "secret/deckhand/telegram"]

watch:
  - name: shop
    auth:
      token_command: ["sops", "-d", "--extract", '["shop"]', "/etc/deckhand/secrets.yaml"]
```

Damit funktionieren `pass`, gopass, das 1Password-CLI, Bitwarden, Vault,
`aws secretsmanager get-secret-value`, `sops`, `age` oder ein eigenes
Wrapper-Skript — ohne eine Zeile anbieterspezifischen Code in Deckhand.

Wie es sich verhält:

* **Standardausgabe ist das Credential** und wird nie geloggt. Standardfehler
  ist die Diagnose und *wird* gezeigt, wenn das Kommando scheitert. Ein
  Fehlschlag nennt Programmname und stderr, nie die Argumente — ein Argument
  kann selbst das Geheimnis enthalten.
* **Der Wert wird für `token_ttl` gecacht** (Standard eine Stunde; aktualisiert
  wird nur das GitHub-Credential, Registry- und Benachrichtigungs-Credentials
  werden einmal beim Start gelesen). Vault vor jeder Anfrage zu fragen wäre
  Verschwendung und würde jedes Deployment davon abhängig machen, dass der
  Secret-Manager in genau dieser Sekunde läuft.
* **Eine gescheiterte Aktualisierung behält den gecachten Wert** und meldet das,
  damit ein kurzer Ausfall des Secret-Managers kein Deployment verhindert.
* **Das Kommando läuft beim Start**, ein kaputtes scheitert also bei
  `deckhand check` und nicht beim ersten Deployment. Nach 30 Sekunden wird es
  beendet.

**2. An die Maschine binden mit systemd-creds.** Unter Linux mit systemd ist das
bereits gelöst, und Deckhand braucht dafür keinen Code:

```bash
systemd-creds encrypt --name=github-token - /etc/deckhand/github-token.cred <<<"github_pat_..."
```

```ini
[Service]
LoadCredentialEncrypted=github-token:/etc/deckhand/github-token.cred
```

```yaml
github:
  token_file: ${CREDENTIALS_DIRECTORY}/github-token
```

systemd entschlüsselt es nach `$CREDENTIALS_DIRECTORY`, ein tmpfs, das nur der
Dienst lesen kann. Das ist echter Schutz: an TPM oder Host-Schlüssel gebunden
ist die Datei auf einer anderen Maschine wertlos, und der Klartext landet nie
auf dauerhaftem Speicher. `deckhand service install` schreibt die passenden
Kommandos für die konfigurierten Credential-Dateien als Kommentare in die
erzeugte Unit.

**3. Andere Prozesse nicht in den Speicher schauen lassen.** Das macht Deckhand
von selbst: unter Linux wird beim Start `PR_SET_DUMPABLE` gelöscht, damit ein
anderer Prozess desselben Benutzers sich nicht anhängen kann und der Kernel
keinen Core-Dump schreibt. Die erzeugte systemd-Unit setzt zusätzlich
`LimitCORE=0`.

Eines noch zu `token_env`: eine Umgebungsvariable ist **nicht** privater als
eine 0600-Datei. Alles, was `/proc/<pid>/environ` lesen kann — root und Prozesse
desselben Benutzers — liest sie mit. Das ist kein Loch, nur nicht der Schutz,
den man annimmt.

## Anmeldung als GitHub App

Ein Personal Access Token ist der schnellste Weg, und für eine Maschine mit
Lesezugriff völlig in Ordnung. Eine GitHub App lohnt den Mehraufwand, wenn:

* **Nichts ablaufen soll.** Ein fine-grained Token hält höchstens ein Jahr, und
  wenn er verfällt, stehen alle Deployments. Der private Schlüssel einer App
  läuft nicht ab; er erzeugt Installation-Tokens, die eine Stunde leben und sich
  selbst erneuern.
* **Deployments nicht an einer Person hängen sollen.** Ein Token gehört deinem
  Konto. Eine App ist eine eigene Identität und übersteht es, wenn jemand geht.
* **Mehrere Maschinen oder Konten im Spiel sind.** Eine App kann in mehreren
  Konten installiert sein, und ihr Rate-Limit gilt je Installation statt geteilt
  mit allem anderen, was du tust.

Der Preis ist echt und gehört genannt: Der private Schlüssel liegt dauerhaft auf
der Maschine, läuft nie ab und gilt für jede Installation der App. Ein
gestohlener `Contents: Read`-Token ist weniger wert als ein gestohlener
App-Schlüssel. Behandle ihn wie einen SSH-Host-Key — Eigentümer ist der
Dienstbenutzer, Rechte 0600, niemals in einem Repository.

### App anlegen

1. **Settings → Developer settings → GitHub Apps → New GitHub App**
   (für eine Organisation: deren Einstellungen, gleicher Pfad).
2. Gib ihr einen erkennbaren Namen — er erscheint im Audit-Log. Als Homepage-URL
   genügt dein Repository.
3. Unter **Webhook** den Haken bei *Active* entfernen. Deckhand fragt ab und
   braucht keinen Webhook.
4. **Repository permissions → Contents: Read-only.** Sonst nichts. Kein
   Metadata-Write, keine Actions, keine Workflows.
5. **Where can this GitHub App be installed** → *Only on this account*, sofern du
   keinen Grund hast, sie zu teilen.
6. Anlegen, dann die **App ID** oben auf der Seite notieren.
7. Unten **Generate a private key**. Die `.pem` wird genau einmal
   heruntergeladen — es gibt keine zweite Gelegenheit, nur einen neuen Schlüssel.
8. Links **Install App** → Konto wählen → *Only select repositories* → die
   Repositories auswählen, die Deckhand beobachten soll.

### Einrichten

```bash
sudo install -m 600 -o deckhand ~/Downloads/deine-app.2026-09-19.private-key.pem \
  /etc/deckhand/app-private-key.pem
```

```yaml
github:
  app:
    id: "123456"
    private_key_file: /etc/deckhand/app-private-key.pem
    # installation_id: 12345678   # optional, siehe unten
```

Entferne `token_env` / `token_file` beim Umstellen — Deckhand weist eine
Konfiguration mit beidem zurück, statt stillschweigend eines auszuwählen.

| Schlüssel | Bedeutung |
|---|---|
| `id` | Die App ID aus den Einstellungen der App. Ihre Client ID funktioniert auch. |
| `private_key_file` | Pfad zur `.pem`. Darf für andere nicht lesbar sein. |
| `private_key_env` | Der Schlüsselinhalt in einer Umgebungsvariable, für Umgebungen, die Geheimnisse einspeisen. Literale `\n`-Folgen werden akzeptiert. |
| `private_key` | Direkt in der Datei. Funktioniert, legt den Schlüssel aber in eine Datei, die man versehentlich committet. |
| `installation_id` | Nagelt alle Repositories auf eine Installation fest. Lass es weg, dann fragt Deckhand GitHub, welche Installation welches Repository abdeckt — das willst du, wenn die App in mehreren Konten installiert ist. |

Vor dem Start des Dienstes prüfen:

```bash
deckhand check     # zeigt: github auth: GitHub App 123456 (...)
deckhand doctor    # bestätigt die App bei GitHub und nennt ihren Namen
```

`doctor` nennt den Slug der App (`authenticated as GitHub App @deine-app`), und
weil es zusätzlich jedes Repository liest, beweist es, dass die Installation sie
wirklich abdeckt — der häufigste Fehler ist, die App zu installieren und dabei
ein Repository zu vergessen.

### Was Deckhand damit macht

Nichts, das du verwalten musst. Vor jeder API-Anfrage und jedem Fetch holt es
einen Token und erneuert ihn, sobald weniger als zehn Minuten übrig sind. Tokens
werden je Installation zwischengespeichert, und scheitert eine Erneuerung,
während der aktuelle Token noch gilt, läuft das Deployment mit dem alten weiter
statt an einem Schluckauf zu scheitern. Der Token erreicht git über den
Askpass-Helfer, genau wie ein Personal Access Token — er erscheint also nie in
einer Prozessliste oder in `.git/config`.

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
* Unter Linux löscht der Prozess `PR_SET_DUMPABLE`: kein Prozess desselben
  Benutzers kann sich anhängen, und es kann kein Core-Dump geschrieben werden.
* Ein Credential aus einem Kommando wird nie geloggt, und die Argumente des
  Kommandos auch nicht.

## Ein Problem melden

Nutze bitte [Private Vulnerability Reporting](https://github.com/marcwoge/deckhand/security/advisories/new)
und kein öffentliches Issue für Sicherheitsfehler.
