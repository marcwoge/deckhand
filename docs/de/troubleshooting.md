# Fehlersuche

Fang hier an:

```bash
deckhand check      # ist die Konfiguration in Ordnung?
deckhand status     # was hält jeder Watch für seinen Zustand?
deckhand history    # was ist tatsächlich passiert, und wann?
```

---

### „config ... is writable by group or others"

```bash
chmod 600 /etc/deckhand/deckhand.yaml
```

Deckhand liest keine Konfiguration, die jemand anderes umschreiben könnte —
denn diese Konfiguration entscheidet, welche Befehle auf der Maschine laufen.

### „refusing to run as root"

Lege einen Dienstbenutzer an, siehe
[security.md](security.md#2-ein-benutzer-der-nur-deployen-kann). Falls du einen
echten Grund hast, als root zu laufen, geht das mit `deckhand run --allow-root`
— aber greif erst dazu, wenn du entschieden hast, dass der Grund wirklich echt
ist.

### „not found (repository missing, renamed, or the token lacks access)"

Nach Häufigkeit sortiert:

1. Die **Repository-Liste** des Tokens enthält dieses Repository nicht.
   Fine-grained Tokens gelten je Repository; ein neuer Watch heißt meist auch,
   den Token zu bearbeiten.
2. Der Token ist abgelaufen.
3. `repo:` ist falsch geschrieben, oder das Repository wurde umbenannt.

GitHub antwortet für Repositories, die du nicht sehen darfst, bewusst mit `404`
statt `403` — ein Rechteproblem sieht deshalb aus wie ein fehlendes Repository.

### „no matching release found"

Das Repository hat kein veröffentlichtes Release, das auf deine Filter passt.
Prüfe, ob überhaupt Releases existieren und nicht alle Entwürfe oder
Pre-Releases sind, und ob `tag_match` zu den tatsächlichen Tag-Namen passt
(`v*` passt nicht auf `1.2.3`).

### „GitHub rate limit exhausted"

Fast immer ein unauthentifizierter Watch: 60 Anfragen pro Stunde und
IP-Adresse, geteilt mit allem anderen auf dieser Maschine. Konfiguriere einen
Token — das hebt das Limit auf 5.000 pro Stunde und gilt auch für öffentliche
Repositories.

### Der Watch ist gestoppt („halted")

```
[shop] halted after 3 consecutive failures
```

Deckhand hat absichtlich aufgehört, es weiter zu versuchen. Ursache suchen:

```bash
deckhand status                 # zeigt den letzten Fehler
deckhand history shop --limit 5
```

Beheben, dann:

```bash
deckhand resume shop
```

### Das Deployment lief durch, aber die Anwendung hat sich nicht geändert

Sieh nach, was dein Befehl tatsächlich getan hat:

```bash
deckhand history shop
ls -l /srv/shop/current
cat /srv/shop/current/.deckhand-release.json
```

Häufige Ursachen: Der Dienst liest aus einem anderen Pfad als `current`; das
Container-Image wurde nicht neu gebaut (`docker compose up -d` allein baut ein
lokal gebautes Image nicht neu — ergänze einen Schritt `docker compose build`);
oder der Prozess hält die Konfiguration im Speicher und braucht einen Neustart
statt eines Reloads.

### Es wird sofort alles zurückgerollt

Dein Health-Check schlägt fehl. Teste ihn von Hand:

```bash
curl -i http://localhost:8080/healthz
```

Übliche Abhilfen: `initial_delay` erhöhen, damit die Anwendung Zeit zum Starten
hat; `retries` erhöhen; oder den Check auf einen Endpunkt richten, der nicht
davon abhängt, dass ein nachgelagerter Dienst läuft.

### `.env` verschwindet nach jedem Deployment

Trag sie unter `shared:` ein:

```yaml
shared: [".env", "data/"]
```

Dateien im Release-Verzeichnis werden bei jedem Deployment ersetzt. shared-Pfade
liegen in `path/shared/` und werden in jedes Release hineinverlinkt, überleben
also. Beim ersten Deployment wird eine vorhandene Fassung aus dem Repository
nach `shared/` verschoben und dient als Startwert.

### „cannot create link ... enable Developer Mode" (Windows)

Die Strategie `releases` braucht einen Link für `current`. Aktiviere den
Entwicklermodus für echte Symlinks, führe den Installer mit erhöhten Rechten
aus, oder stelle diesen Watch auf `strategy: inplace` um.

### Es passiert überhaupt nichts

* Läuft der Worker? `systemctl status deckhand`, `launchctl list | grep deckhand`, `schtasks /Query /TN Deckhand`.
* Ist alles pausiert? `deckhand status` sagt das in der ersten Zeile.
* Ist das Zeitfenster zu? `deckhand history` zeigt ein `queue`-Ereignis mit dem Öffnungszeitpunkt.
* Ist der Watch deaktiviert? `enabled: false` in der Konfiguration.
* Schaust du auf das richtige Zustandsverzeichnis? Ein Dienst, der als
  `deckhand` läuft, und deine Shell als du selbst verwenden verschiedene. Gib
  dasselbe `DECKHAND_STATE_DIR` an oder führe die CLI als Dienstbenutzer aus:
  `sudo -u deckhand deckhand status --config /etc/deckhand/deckhand.yaml`.

### Das Audit-Log direkt lesen

Es ist JSON Lines, ein Ereignis je Zeile:

```bash
jq -r 'select(.result=="failed") | "\(.time) \(.watch) \(.message)"' \
  /var/lib/deckhand/audit.jsonl
```

### Immer noch festgefahren

Öffne ein Issue mit der Ausgabe von `deckhand check`, den passenden Zeilen aus
`deckhand history`, deiner Deckhand-Version (`deckhand version`) und deiner
Plattform. Bitte schwärze Tokens — Deckhand schwärzt sie in seiner eigenen
Ausgabe, aber nicht in dem, was du von anderswo einfügst.
