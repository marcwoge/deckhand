<div align="center">

# ⚓ Deckhand

**Der stille Matrose für deine Server.**
Er beobachtet deine GitHub-Repositories, holt jedes neue Release oder jeden neuen Commit und führt den einen Befehl aus, der es live schaltet.

[English](README.md) · [Dokumentation](docs/de/) · [Roadmap](ROADMAP.md) · [Sicherheit](SECURITY.md)

</div>

---

## Warum Deckhand

Du hast ein Release getaggt. Jetzt muss jemand ein Terminal öffnen, sich per SSH auf den Server verbinden, `git pull` ausführen und einen Container neu starten. Schon wieder. Auf drei Maschinen.

Deckhand ist eine einzelne kleine Binärdatei, die genau diese Aufgabe erledigt — und sonst nichts:

```yaml
watch:
  - name: shop
    repo: dein-name/shop
    trigger: { type: release }
    path: /srv/shop
    run:
      - ["docker", "compose", "up", "-d"]
```

Das ist die komplette Einrichtung. Neues Release auf GitHub → der Code landet auf der Maschine → dein Befehl läuft → ein Health-Check bestätigt, dass es funktioniert hat. Falls nicht, stellt Deckhand die vorherige Version wieder her, bevor du es merkst.

### Was ihn unterscheidet

| | |
|---|---|
| **Keine eingehenden Ports** | Deckhand fragt bei GitHub an; GitHub ruft nie an. Funktioniert hinter NAT, ohne Firewall-Regeln, ohne abzusichernden Webhook-Endpunkt. |
| **Der Befehl steht in deiner Config, nicht im Repo** | Selbst ein kompromittiertes Repository kann nicht ändern, *was* auf deiner Maschine ausgeführt wird. Das ist die zentrale Sicherheitsgrenze. |
| **Atomare Releases** | Jede Revision wird nach `releases/<sha>` entpackt; erst wenn sie vollständig ist, wird der Link `current` umgelegt. Nichts läuft jemals gegen ein halbfertiges Verzeichnis. |
| **Automatischer Rollback** | Ein fehlgeschlagener Befehl oder Health-Check stellt das vorherige Release wieder her und führt den Befehl erneut aus — es kommt also der Dienst zurück, nicht nur die Dateien. |
| **Zeitfenster** | „Nur zwischen 22:00 und 05:00, und nie Freitagnachmittag." Trigger außerhalb des Fensters werden zusammengefasst: Du bekommst ein Deployment mit dem neuesten Code, nicht zwölf. |
| **Eine Binärdatei, drei Plattformen** | Linux, macOS, Windows. Keine Laufzeitumgebung, kein Interpreter, keine Abhängigkeiten. `deckhand service install` registriert ihn bei systemd, launchd oder der Windows-Aufgabenplanung. |
| **Meldungen — und eine Fernbedienung** | Push aufs Handy per ntfy, Slack oder Webhook — oder ein Telegram-Bot, den du `/status` fragen und `/rollback shop` befehlen kannst; abgefragt wie GitHub, also weiterhin ohne eingehenden Port. |
| **Merkt den eigenen Tod** | Ein Herzschlag an healthchecks.io oder Uptime Kuma — denn ein abgestürzter Worker sendet keine Meldungen, und Stille sieht genauso aus wie Erfolg. |
| **Alles wird protokolliert** | Ein fortlaufendes Audit-Log im JSON-Lines-Format über jeden Trigger, jede Revision, jeden Befehl und jeden Exit-Code. |

---

## Schnellstart

**1. Installieren**

Lade die Binärdatei für deine Plattform aus dem [aktuellen Release](https://github.com/marcwoge/deckhand/releases/latest) — oder baue sie selbst:

```bash
go install github.com/marcwoge/deckhand/cmd/deckhand@latest
```

**2. Konfiguration anlegen**

```bash
deckhand init --config deckhand.yaml   # schreibt ein kommentiertes Beispiel, Rechte 0600
```

Passe sie an:

```yaml
version: 1

defaults:
  poll_interval: 60s
  timezone: Europe/Berlin

github:
  token_env: DECKHAND_GITHUB_TOKEN     # der Token bleibt außerhalb der Datei

watch:
  - name: shop
    repo: dein-name/shop
    trigger:
      type: release                     # release | branch | tag
      tag_match: "v*"
    path: /srv/shop
    shared: [".env", "data/"]           # überlebt jedes Deployment
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d"]
    health:
      http: http://localhost:8080/healthz
      retries: 10

notify:
  on: [failure, rollback, halt]
  channels:
    - type: ntfy                    # Push aufs Handy
      url: https://ntfy.sh/deckhand-a7f3k9m2q8

heartbeat:                          # damit du merkst, wenn Deckhand selbst stirbt
  url: https://hc-ping.com/deine-uuid
```

**3. Token erstellen**

Verwende einen [fine-grained Personal Access Token](https://github.com/settings/personal-access-tokens/new) mit **`Contents: Read-only`** auf genau den Repositories, die du einträgst — mehr nicht. Für fremde öffentliche Repositories brauchst du gar keinen Token (beachte aber die [Rate-Limits](docs/de/configuration.md#rate-limits)).

```bash
export DECKHAND_GITHUB_TOKEN=github_pat_...
```

**4. Prüfen und starten**

```bash
deckhand check        # prüft die Konfiguration und weist auf riskante Einstellungen hin
deckhand doctor       # fragt GitHub: stimmt der Token, löst der Trigger auf,
                      # ist der Pfad beschreibbar? Nur lesend, auch produktiv gefahrlos.
deckhand deploy shop  # einmal sofort deployen, um zu sehen, dass es läuft
deckhand run          # Worker im Vordergrund starten
```

**5. Als Dienst installieren**

```bash
deckhand service install            # für den aktuellen Benutzer
sudo deckhand service install --system --user deckhand   # für die ganze Maschine
```

Fertig. Ab jetzt ist das Taggen eines Releases das Deployment.

---

## Täglicher Betrieb

| Befehl | Wirkung |
|---|---|
| `deckhand status` | Auf welchem Stand jeder Watch ist, wann er zuletzt erfolgreich war, was klemmt |
| `deckhand history [watch]` | Das Audit-Log, neueste Einträge zuletzt |
| `deckhand deploy <watch> [--force]` | Sofort deployen, ohne Rücksicht auf das Zeitfenster |
| `deckhand rollback <watch>` | Zurück auf die vorherige Revision |
| `deckhand pause "Datenbankmigration"` | Alle Deployments anhalten |
| `deckhand resume` / `deckhand resume <watch>` | Anhalten aufheben / gestoppten Watch freigeben |
| `deckhand check` | Konfiguration prüfen, bevor der Dienst neu gestartet wird |
| `deckhand doctor` | Erreichbarkeit, Zugangsdaten, Rechte und ob jeder Trigger auflösbar ist |
| `systemctl reload deckhand` | Konfigurationsänderungen übernehmen, ohne Deployments zu stoppen |

Nach drei aufeinanderfolgenden Fehlschlägen stoppt sich ein Watch selbst, statt in einer Schleife zu kreisen, nennt den Grund und wartet auf `deckhand resume <watch>`.

### So sieht ein Deployment auf der Festplatte aus

```
/srv/shop/
├── current -> releases/9f3c2ab...      der aktive Stand
├── releases/
│   ├── 9f3c2ab.../                     dieses Release
│   └── 41ae08c.../                     das vorherige, bereit für den Rollback
└── shared/
    ├── .env                            wird in jedes Release eingehängt
    └── data/
```

Dein Befehl läuft in `current` und erhält `DECKHAND_SHA`, `DECKHAND_REF`, `DECKHAND_RELEASE_DIR`, `DECKHAND_SHARED_DIR` und weitere Werte in seiner Umgebung. Siehe [Konfiguration](docs/de/configuration.md#umgebungsvariablen).

---

## Wartung

* **Deckhand aktualisieren:** Binärdatei ersetzen, Dienst neu starten. Der Zustand liegt außerhalb der Binärdatei und übersteht Updates.
* **Token wechseln:** Umgebungsvariable oder Token-Datei ändern, Dienst neu starten. Nichts anderes verweist darauf.
* **Speicherplatz:** Alte Releases werden automatisch aufgeräumt (`keep_releases`, Standard 5). Der lokale Git-Spiegel wird von allen Deployments geteilt und bleibt klein.
* **Logs:** `journalctl -u deckhand -f` (Linux), `~/Library/Logs/deckhand.log` (macOS), `deckhand history` auf jeder Plattform.
* **Backups:** Sichere `shared/` — dort liegen deine Konfiguration und deine Daten. Alles andere lässt sich jederzeit wieder von GitHub holen.

Vollständige Dokumentation: **[docs/de/](docs/de/)** — [Konfiguration](docs/de/configuration.md) · [Trigger](docs/de/triggers.md) · [Zeitfenster](docs/de/time-windows.md) · [Benachrichtigungen](docs/de/notifications.md) · [Sicherheit](docs/de/security.md) · [Als Dienst betreiben](docs/de/services.md) · [Fehlersuche](docs/de/troubleshooting.md)

---

## Sicherheit in einem Absatz

Deckhand setzt voraus, dass dein Repository gepflegt und vertrauenswürdig ist — das ist das erklärte Bedrohungsmodell. Innerhalb dieser Annahme ist er bewusst eng gefasst: Er öffnet keinen lauschenden Port, er braucht nicht mehr als Lesezugriff auf die genannten Repositories, er verweigert den Start als root oder mit einer weltweit lesbaren Konfiguration, er reicht seinen Token niemals an deine Deploy-Befehle weiter, er führt Befehle als Argument-Vektor ohne Shell aus, er deaktiviert Git-Hooks, damit Repository-Inhalte das Verhalten von git nicht ändern können, und er prüft jeden Pfad im exportierten Archiv, damit ein feindseliges Repository nicht außerhalb seines eigenen Release-Verzeichnisses schreiben kann. Das vollständige Bedrohungsmodell steht in [SECURITY.md](SECURITY.md) und [docs/de/security.md](docs/de/security.md).

---

## Mitarbeit

Deckhand ist absichtlich klein, und Pull Requests sind willkommen — besonders Plattformtests, Dokumentation und Übersetzungen. Beginne mit [CONTRIBUTING.md](CONTRIBUTING.md) und den [Architektur-Notizen](docs/dev/architecture.md). Öffne vor größeren Änderungen bitte ein Issue, damit wir uns vorher über die Richtung einig sind.

## Lizenz

[Apache License 2.0](LICENSE)
