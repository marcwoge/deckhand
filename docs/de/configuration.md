# Konfigurationsreferenz

Deckhand liest eine einzige YAML-Datei. Unbekannte Schlüssel sind ein Fehler,
keine Warnung — ein Tippfehler stoppt den Worker bei `deckhand check`, statt
still das Falsche zu tun, wenn es drei Uhr nachts ist.

Suchreihenfolge für die Datei:

1. `--config <pfad>`
2. `$DECKHAND_CONFIG`
3. `./deckhand.yaml` bzw. `./deckhand.yml`
4. `~/.config/deckhand/deckhand.yaml`
5. `/etc/deckhand/deckhand.yaml` (Linux/macOS) oder `%ProgramData%\deckhand\deckhand.yaml` (Windows)

Die Datei darf für Gruppe und andere nicht schreibbar sein. `deckhand init`
legt sie mit den Rechten 0600 an.

---

## Oberste Ebene

```yaml
version: 1          # Pflicht, derzeit immer 1
defaults: {}        # gilt für jeden Watch, sofern nicht überschrieben
github: {}          # API-Zugang
notify: {}          # optionale Benachrichtigungen
heartbeat: {}       # optionaler Totmannschalter
watch: []           # ein Eintrag je beobachtetem Repository
```

## `defaults`

Alles hier lässt sich pro Watch überschreiben.

| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `poll_interval` | `60s` | Wie oft GitHub gefragt wird. Minimum `10s`. Unveränderte Antworten kommen aus dem Cache und kosten kein Rate-Limit. |
| `timezone` | Systemzone | Zone, in der alle Zeitfenster ausgewertet werden, z. B. `Europe/Berlin`. |
| `command_timeout` | `10m` | Wie lange ein einzelner Befehl laufen darf, bevor er samt Kindprozessen beendet wird. |
| `strategy` | `releases` | `releases` (atomarer Wechsel, Rollback) oder `inplace`. |
| `keep_releases` | `5` | Wie viele alte Release-Verzeichnisse aufbewahrt werden. |
| `failure_limit` | `3` | Anzahl aufeinanderfolgender Fehlschläge, nach der ein Watch sich selbst stoppt. |
| `state_dir` | siehe unten | Wo Zustand, Git-Spiegel und Audit-Log liegen. |
| `env` | – | Umgebungsvariablen, die jedem Befehl mitgegeben werden. |
| `audit.max_size` | `10MB` | Ab dieser Größe wird das Audit-Log rotiert. `0` schaltet die Rotation ab. |
| `audit.keep` | `5` | Wie viele rotierte Logs aufbewahrt werden. `deckhand history` liest alle. |

Das Standard-Zustandsverzeichnis ist `~/.local/state/deckhand` für einen
normalen Benutzer, `/var/lib/deckhand` für root und
`%ProgramData%\deckhand\state` unter Windows. `$DECKHAND_STATE_DIR` überschreibt
es.

## `include`

Eine große Datei aufzuteilen ist optional, hält aber eine Maschine mit vielen
Deployments überschaubar — und erlaubt es einem Konfigurationswerkzeug, eine
Datei abzulegen, ohne eine gemeinsame umzuschreiben.

```yaml
include: deckhand.d      # relativ zur Hauptdatei, oder ein absoluter Pfad
```

Jede `*.yaml` und `*.yml` in diesem Verzeichnis wird in lexikalischer Reihenfolge
gelesen (benenne sie also `10-shop.yaml`, `20-api.yaml`) und darf `watch:`-
Einträge beisteuern — und nur die. `defaults`, `github`, `notify` und
`heartbeat` bleiben in der Hauptdatei, damit nie die Frage aufkommt, welcher
Wert gewinnt. Eingebundene Dateien müssen dieselben Rechteprüfungen bestehen,
und doppelte Watch-Namen werden gemeldet.

Ist `include` nicht gesetzt und liegt neben der Konfiguration ein Verzeichnis
`<config>.d`, wird es automatisch verwendet.

## `github`

| Schlüssel | Bedeutung |
|---|---|
| `token_env` | Name der Umgebungsvariable mit dem Token. **Empfohlen.** |
| `token_file` | Pfad zu einer Datei mit dem Token. Darf für andere nicht lesbar sein. |
| `token` | Der Token direkt in der Datei. Funktioniert, legt aber ein Geheimnis in eine Datei, die man versehentlich committet. |
| `api` | Basis-URL der API. Für GitHub Enterprise Server ändern, z. B. `https://ghe.example.com/api/v3`. |
| `host` | Git-Host zum Klonen. Standard `github.com`. |

Ist keine der drei Token-Angaben gesetzt, wird `$DECKHAND_GITHUB_TOKEN`
verwendet.

**Welcher Token?** Ein [fine-grained Personal Access Token](https://github.com/settings/personal-access-tokens/new)
mit **`Contents: Read-only`**, beschränkt auf die Repositories, die du einträgst.
Deckhand schreibt niemals nach GitHub und braucht keine weitere Berechtigung.
Für Repositories, bei denen du Contributor statt Eigentümer bist, funktioniert
derselbe Token, solange dein Konto sie lesen darf.

### Rate-Limits

| Situation | Limit |
|---|---|
| Mit Token | 5.000 Anfragen pro Stunde |
| Ohne Token | 60 Anfragen pro Stunde und IP-Adresse |

Deckhand stellt bedingte Anfragen: Hat sich nichts geändert, antwortet GitHub
mit `304 Not Modified` — und das zählt **nicht** gegen das Limit. In der Praxis
deckt ein Token bequem dutzende Watches im 60-Sekunden-Takt ab.

Ohne Token wäre das anonyme Budget schon mit einem einzigen Watch im
60-Sekunden-Takt aufgebraucht. Deckhand verlangsamt unauthentifizierte Watches
deshalb automatisch auf fünf Minuten. Ein Token lohnt sich also auch für
öffentliche Repositories.

## `notify`

Optional. Lass den Block weg, wenn du keine Benachrichtigungen willst. Alle
Einzelheiten, auch zur Telegram-Fernbedienung, stehen in
[notifications.md](notifications.md).

Ein einzelnes Ziel ohne Zugangsdaten lässt sich kurz schreiben:

```yaml
notify:
  on: [failure, rollback, halt]  # success, failure, rollback, halt — oder "all"
  webhook: https://ntfy.sh/dein-privates-topic
  format: ntfy                   # json (Standard) | slack | ntfy
```

Alles andere — mehrere Ziele, ein Token, Telegram — nutzt `channels`:

```yaml
notify:
  on: [failure, rollback, halt]
  channels:
    - type: telegram
      token_env: DECKHAND_TELEGRAM_TOKEN
      chat_id: "123456789"
      commands: true             # nimmt /status, /deploy, /rollback aus diesem Chat an
    - type: ntfy
      url: https://ntfy.example.com/deploy
      token_env: DECKHAND_NTFY_TOKEN
      priority:                  # min | low | default | high | urgent
        success: low
        halt: urgent
    - type: webhook
      url: https://automation.example.com/deckhand
      on: [all]                  # dieser Kanal überschreibt die globale Liste
```

| Schlüssel | Gilt für | Bedeutung |
|---|---|---|
| `type` | alle | `ntfy`, `slack`, `webhook` oder `telegram`. |
| `url` | außer telegram | Wohin gesendet wird. Muss http(s) sein. |
| `token` / `token_env` / `token_file` | alle | Zugangsdaten. Werden als `Authorization: Bearer` gesendet, nie in der URL. Für telegram Pflicht. |
| `chat_id` | telegram | Welcher Chat Meldungen erhält und Befehle senden darf. |
| `commands` | telegram | Aktiviert die Fernbedienung. Höchstens ein Kanal. |
| `on` | alle | Überschreibt die globale Ereignisliste für diesen Kanal. |
| `priority` | ntfy | ntfy-Priorität je Ereignis. |

Kanäle sind unabhängig: Ein defekter Endpunkt hält weder die anderen auf noch
beeinflusst er das Deployment.

## `heartbeat`

Optional, aber empfohlen. Jeder Benachrichtigungskanal meldet sich nur, wenn
etwas schiefgeht — ein abgestürzter Worker oder eine ausgeschaltete Maschine
erzeugt also gar keine Meldung. Ein Totmannschalter bemerkt die Stille.

```yaml
heartbeat:
  url: https://hc-ping.com/deine-uuid
  interval: 5m        # Standard 5m, Minimum 30s
  method: GET         # GET (Standard), POST oder HEAD
  timeout: 15s
```

Funktioniert mit [healthchecks.io](https://healthchecks.io) und mit einem
selbst gehosteten [Uptime Kuma](https://uptime.kuma.pet) Push-Monitor. Der
erste Ping geht beim Start raus. Siehe
[notifications.md](notifications.md#der-herzschlag--merken-dass-deckhand-selbst-weg-ist).

## `watch`

Jeder Eintrag beschreibt ein Repository.

### Identität

| Schlüssel | Pflicht | Bedeutung |
|---|---|---|
| `name` | ja | Eindeutiger Name, erscheint in Befehlen, Logs und Statusausgabe. |
| `repo` | ja | `eigentümer/name`. |
| `auth` | nein | `token` (Standard) oder `none` für öffentliche Repositories, die anonym abgefragt werden sollen. |
| `enabled` | nein | Auf `false` setzen, um einen Eintrag zu behalten, ohne ihn auszuführen. |
| `clone_url` | nein | Überschreibt, woher der Code geholt wird. Selten nötig. |

### Was beobachtet wird

```yaml
trigger:
  type: release        # release | branch | tag
  branch: main         # Pflicht bei type: branch
  tag_match: "v*"      # Glob-Muster, für release und tag
  prerelease: false    # Pre-Releases einbeziehen (nur release)
```

Zur Auswahl siehe [triggers.md](triggers.md).

### Wohin es kommt

| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `path` | Pflicht | Das Deployment-Verzeichnis. Wird angelegt, falls es fehlt. |
| `strategy` | `releases` | `releases` legt `path/releases/<sha>` an und verlinkt `path/current`. `inplace` schreibt die Repository-Dateien direkt nach `path`. |
| `shared` | – | Pfade, die außerhalb des Releases liegen und in jedes hineinverlinkt werden. Verzeichnisse mit `/` abschließen. |
| `keep_releases` | `5` | Anzahl aufbewahrter alter Releases. |

Mit der Strategie `releases` sieht es so aus:

```
/srv/app/
├── current -> releases/9f3c2ab...
├── releases/9f3c2ab.../
└── shared/.env
```

Beim ersten Deployment eines shared-Pfads wird eine vorhandene Datei aus dem
Repository nach `shared/` *verschoben* und dient als Startwert; ab dann ist
deine Fassung maßgeblich und die Version aus dem Repository wird ignoriert.

Die Strategie `inplace` aktualisiert die Dateien, die im Repository liegen, und
lässt alles andere in `path` unberührt. Aus dem Repository gelöschte Dateien
bleiben auf der Platte liegen. Nutze sie, wenn etwas außerhalb von Deckhand auf
einem festen Pfad besteht.

### Was ausgeführt wird

```yaml
run:
  - ["docker", "compose", "pull"]
  - ["docker", "compose", "up", "-d"]
```

Jeder Befehl ist eine Argumentliste: Das erste Element ist das Programm, der
Rest sind Argumente, die unverändert übergeben werden. Es gibt keine Shell, also
wirken weder Anführungszeichen noch `&&`, `|` oder `$VAR`. Das ist Absicht — so
kann nichts in einem Repository-, Tag- oder Branch-Namen jemals als Befehl
interpretiert werden.

Wenn du Shell-Funktionen wirklich brauchst, fordere sie ausdrücklich an:

```yaml
run:
  - cmd: ["make", "deploy"]
    dir: build              # relativ zum Release-Verzeichnis
    timeout: 20m
    env:
      TARGET: production
  - shell: "docker compose logs --tail 5 | tee -a /var/log/deploy.log"
```

`on_failure:` hat dieselbe Form und läuft nach einem gescheiterten Deployment,
im Anschluss an den Rollback-Versuch. `DECKHAND_ERROR` enthält die Fehlermeldung.

#### Umgebungsvariablen

Befehle erhalten bewusst eine kleine Umgebung: `PATH`, `HOME`, `USER`, Locale-
und Temp-Variablen, `DOCKER_HOST`, `SSH_AUTH_SOCK` sowie die Windows-Pendants —
dazu alles, was du unter `env:` setzt. Deckhands eigene Umgebung, insbesondere
der GitHub-Token, wird **nicht** durchgereicht.

Zusätzlich bekommt jeder Befehl:

| Variable | Beispiel |
|---|---|
| `DECKHAND_WATCH` | `shop` |
| `DECKHAND_REPO` | `acme/shop` |
| `DECKHAND_SHA` | `9f3c2ab8...` (vollständig) |
| `DECKHAND_SHORT_SHA` | `9f3c2ab8` |
| `DECKHAND_REF` | `v1.4.0` oder `main` |
| `DECKHAND_KIND` | `release`, `tag` oder `branch` |
| `DECKHAND_EVENT` | `deploy`, `rollback` oder `failure` |
| `DECKHAND_RELEASE_DIR` | `/srv/shop/releases/9f3c2ab8...` |
| `DECKHAND_WORKDIR` | `/srv/shop/current` |
| `DECKHAND_SHARED_DIR` | `/srv/shop/shared` |
| `DECKHAND_PREVIOUS_SHA` | die Revision, die ersetzt wird |
| `DECKHAND_HOST` | Hostname der Maschine |

### Health-Check und Rollback

```yaml
health:
  http: http://localhost:8080/healthz
  expect_status: 200      # Standard 200
  initial_delay: 5s       # Wartezeit vor dem ersten Versuch
  retries: 10
  interval: 3s
  timeout: 10s            # je Versuch
rollback: auto            # auto (Standard) | off
```

Statt `http:` kannst du `cmd:` mit einem beliebigen Befehl verwenden; Exit-Code
null bedeutet gesund.

Scheitern die Deploy-Befehle oder besteht der Health-Check nie, und steht
`rollback` auf `auto`, schaltet Deckhand auf das vorherige Release zurück **und
führt die Deploy-Befehle erneut aus** — so kommt tatsächlich der Dienst zurück,
nicht nur die Dateien. Ohne Health-Check gilt ein Deployment als erfolgreich,
sobald deine Befehle mit null enden, und das ist oft nicht dasselbe.

### Prüfungen

Für Repositories, die du nicht kontrollierst:

```yaml
verify:
  require_signed_tag: true
  require_signed_commit: false
  allowed_signers: /etc/deckhand/allowed_signers   # SSH-allowed-signers-Format
  pin_sha: ""                                       # nur genau diese Revision deployen
  allowed_authors:                                  # Autor oder Committer muss passen
    - maintainer@example.com
    - release-bot@example.com
```

`allowed_authors` vergleicht die Autor- und Committer-Adressen des Commits, ohne
Rücksicht auf Groß- und Kleinschreibung. Beachte: Eine Autorenzeile ist
Metadaten, die jeder setzen kann — sie schützt vor Versehen, nicht vor einem
Angreifer. Dafür brauchst du eine Signatur.

### Zeit und Sicherheit

| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `window` | – | Siehe [time-windows.md](time-windows.md). Ohne Angabe wird sofort deployt. |
| `poll_interval` | aus defaults | |
| `command_timeout` | aus defaults | |
| `failure_limit` | aus defaults | |
| `run_on_start` | `false` | Beim Start einmal deployen, auch wenn sich nichts geändert hat. |
| `allow_repo_scripts` | `false` | Bestätigt, dass ein `run:`-Befehl innerhalb des ausgelieferten Verzeichnisses liegt. |

---

## Ein vollständiges Beispiel

```yaml
version: 1

defaults:
  poll_interval: 60s
  timezone: Europe/Berlin
  command_timeout: 10m

github:
  token_env: DECKHAND_GITHUB_TOKEN

notify:
  on: [failure, rollback, halt]
  channels:
    - type: telegram
      token_env: DECKHAND_TELEGRAM_TOKEN
      chat_id: "123456789"
      commands: true
    - type: ntfy
      url: https://ntfy.example.com/deploy
      token_env: DECKHAND_NTFY_TOKEN

heartbeat:
  url: https://hc-ping.com/deine-uuid
  interval: 5m

watch:
  - name: shop
    repo: acme/shop
    trigger:
      type: release
      tag_match: "v*"
    path: /srv/shop
    shared: [".env", "data/", "storage/uploads/"]
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d", "--remove-orphans"]
    health:
      http: http://localhost:8080/healthz
      initial_delay: 5s
      retries: 20
    rollback: auto

  - name: api-staging
    repo: acme/api
    trigger:
      type: branch
      branch: develop
    path: /srv/api-staging
    window:
      allow: ["Mon-Fri 22:00-05:00", "Sat-Sun *"]
    run:
      - ["systemctl", "--user", "restart", "api-staging"]
    health:
      cmd: ["systemctl", "--user", "is-active", "--quiet", "api-staging"]
      retries: 5

  - name: upstream-tool
    repo: someorg/sometool
    auth: none
    trigger:
      type: release
    verify:
      require_signed_tag: true
      allowed_signers: /etc/deckhand/allowed_signers
    path: /opt/sometool
    run:
      - ["/usr/local/bin/rebuild-sometool.sh"]
```
