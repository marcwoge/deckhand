# Zeitfenster

Ein Zeitfenster legt fest, *wann* ein Deployment beginnen darf. Ohne Fenster
deployt Deckhand, sobald es die Änderung bemerkt — für Staging meist genau
richtig, für die Produktion gelegentlich beunruhigend.

```yaml
window:
  timezone: Europe/Berlin        # optional; sonst defaults.timezone
  allow:
    - "Mon-Fri 22:00-05:00"
    - "Sat-Sun *"
  blackout:
    - "Fri 16:00-23:59"
```

Ein Deployment darf starten, wenn es auf **mindestens eine** `allow`-Regel und
auf **keine** `blackout`-Regel passt. Ohne jede Regel ist das Fenster immer
offen.

## Regelsyntax

```
<Tage> <Zeiten>
```

**Tage** — `Mon`, `Tue`, `Wed`, `Thu`, `Fri`, `Sat`, `Sun` (Groß-/Kleinschreibung
egal, lange Namen erlaubt), als Bereich (`Mon-Fri`), als Liste (`Sat,Sun`) oder
`*` für jeden Tag.

**Zeiten** — `HH:MM-HH:MM` im 24-Stunden-Format, oder `*` für den ganzen Tag.

| Regel | Bedeutung |
|---|---|
| `Mon-Fri 22:00-05:00` | Werktagsnächte, von 22:00 bis 05:00 am Folgemorgen |
| `Sat,Sun *` | Das ganze Wochenende |
| `* 02:00-04:00` | Jede Nacht zwischen zwei und vier |
| `Sun` | Der ganze Sonntag (Zeiten dürfen fehlen) |
| `22:00-05:00` | Jede Nacht (Tage dürfen fehlen) |

### Kalenderdaten

Eine Regel, die mit einer Ziffer beginnt, wird als Datum gelesen, nicht als
Wochentag:

| Regel | Bedeutung |
|---|---|
| `2026-12-24..2026-12-27` | Diese vier Tage, einmalig |
| `2026-12-31` | Dieser eine Tag |
| `12-24..12-26` | Dieselben Tage **jedes Jahr** |
| `12-27..01-02` | Ein jährlicher Bereich über den Jahreswechsel |
| `2026-12-31 18:00-23:59` | Ein Datum mit Zeitspanne |

Am nützlichsten sind sie als Sperrzeiten:

```yaml
window:
  allow: ["Mon-Fri 22:00-05:00"]
  blackout:
    - "12-24..12-26"          # jedes Weihnachten
    - "2026-11-27"            # ein bestimmter Release-Stopp
```

### Fenster über Mitternacht

`Mon-Fri 22:00-05:00` gehört zu dem Tag, an dem es **beginnt**. Mittwoch 23:00
liegt darin, Donnerstag 04:00 ebenfalls — das ist noch die Mittwochnacht.
Samstag 03:00 liegt auch darin, denn die Freitagnacht ist eine Werktagsnacht.
Samstag 23:00 dagegen nicht.

Das entspricht fast immer dem, was gemeint ist — prüfe es trotzdem einmal gegen
deine eigene Erwartung, bevor du dich darauf verlässt.

### Zeitzonen

Regeln werden in `window.timezone` ausgewertet, ersatzweise in
`defaults.timezone`, ersatzweise in der Zone der Maschine. Verwende einen echten
Zonennamen wie `Europe/Berlin` statt eines festen Offsets, dann wird die
Sommerzeit für dich mitgedacht. `deckhand status` zeigt die verwendete Zone an.

## Zusammenfassen von Triggern

Solange das Fenster geschlossen ist, fragt Deckhand weiter ab und behält nur die
*neueste* gesehene Revision. Öffnet sich das Fenster, wird genau diese eine
Revision deployt. Zwölf Merges über Nacht ergeben ein Deployment um 22:00, nicht
zwölf.

Die wartende Revision wird als `queue`-Ereignis im Audit-Log festgehalten;
`deckhand history` zeigt also, was seit wann ansteht.

## Trotzdem deployen

Zeitfenster gelten nur für automatische Deployments. Ein Mensch darf immer:

```bash
deckhand deploy shop            # sofort, unabhängig vom Fenster
deckhand deploy shop --force    # auch wenn die Revision schon aktuell ist
```

## Alles anhalten

Um Deployments über alle Watches hinweg zu stoppen — etwa während einer
Datenbankmigration:

```bash
deckhand pause "Datenbankmigration läuft"
deckhand resume
```

Das Anhalten übersteht Neustarts; es ist eine Datei im Zustandsverzeichnis.
`deckhand status` zeigt es gut sichtbar an, damit eine vergessene Pause nicht
still zum Rätsel wird.
