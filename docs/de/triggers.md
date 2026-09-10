# Trigger

Ein Trigger beantwortet eine einzige Frage: *Welche Revision soll gerade jetzt
auf dieser Maschine liegen?* Deckhand stellt GitHub diese Frage bei jedem
Abruf und deployt, wenn sich die Antwort ändert.

## `release` — die empfohlene Voreinstellung

```yaml
trigger:
  type: release
  tag_match: "v*"        # optionales Glob-Muster
  prerelease: false      # optional, Standard false
```

Deployt das neueste veröffentlichte Release. Entwürfe werden immer ignoriert,
Pre-Releases nur auf ausdrücklichen Wunsch berücksichtigt. Mit `tag_match`
können mehrere Umgebungen demselben Repository folgen:

```yaml
tag_match: "v*"          # Produktion folgt v1.4.0
tag_match: "*-beta"      # eine Beta-Maschine folgt 1.5.0-beta
```

Nimm das, wenn ein Mensch entscheiden soll, was live geht. Ein Release zu
veröffentlichen ist eine bewusste Handlung — und damit ein gutes Tor für ein
Deployment.

## `branch` — Continuous Deployment

```yaml
trigger:
  type: branch
  branch: main
```

Deployt den aktuellen Kopf eines Branches. Das deckt sowohl direkte Pushes als
auch gemergte Pull Requests ab — ein Merge bewegt den Branch-Kopf, und genau
den beobachtet Deckhand.

Nimm das für Staging-Umgebungen, oder für die Produktion, wenn dein `main`
immer auslieferbar ist. Kombiniere es mit einem [Zeitfenster](time-windows.md),
wenn ein Merge um 16:55 am Freitag nicht zu einem Deployment um 16:55 am
Freitag werden soll.

Beachte: Ein Branch-Trigger folgt jedem, der auf diesen Branch pushen darf. Für
ein Repository, das du nicht kontrollierst, ist `release` mit Signaturprüfung
die bessere Wahl.

## `tag` — Releases ohne GitHub Releases

```yaml
trigger:
  type: tag
  tag_match: "v*"
```

Deployt den höchsten Tag, der auf das Muster passt. Tags werden nach Möglichkeit
als Versionsnummern verglichen, sodass `v1.10.0` korrekt über `v1.9.0` steht,
sonst lexikografisch.

Nimm das, wenn dein Arbeitsablauf Tags setzt, aber keine GitHub Releases anlegt.

## Wie das Abfragen funktioniert

Jede Abfrage ist eine bedingte Anfrage mit dem ETag der vorherigen Antwort. Hat
sich nichts geändert, antwortet GitHub mit `304 Not Modified` — das kostet kein
Rate-Limit und überträgt keinen Inhalt. Die Revision wird nur dann per git
geholt, wenn sich die Antwort tatsächlich geändert hat, und auch dann nur in
einen lokalen Spiegel, den alle späteren Deployments weiterverwenden.

Abfragen werden um bis zu 10 % zufällig verzögert, damit nicht alle Watches in
derselben Sekunde losschlagen. Bei API-Fehlern wird exponentiell bis auf
fünfzehn Minuten zurückgeschaltet.

## Was nach dem Auslösen passiert

1. Das [Zeitfenster](time-windows.md) wird geprüft. Ist es geschlossen, wird
   die Revision gemerkt und bei jeder Abfrage aktualisiert — landen bis zur
   Öffnung drei weitere Commits, gibt es ein Deployment mit dem neuesten.
2. Der lokale Git-Spiegel wird aktualisiert.
3. Etwaige `verify:`-Prüfungen laufen.
4. Die Revision wird in ein neues Release-Verzeichnis exportiert, shared-Pfade
   werden hineinverlinkt, und `current` wird umgelegt.
5. Deine `run:`-Befehle laufen.
6. Der Health-Check entscheidet, ob es funktioniert hat.
7. Bei Fehlschlag: Rollback, `on_failure:`-Befehle, Benachrichtigung.

Je Watch läuft immer nur ein Deployment gleichzeitig. Verschiedene Watches
arbeiten unabhängig und parallel.
