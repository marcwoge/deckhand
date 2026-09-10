# Benachrichtigungen und Überwachung

Deckhand kann dir sagen, was er getan hat, dich fragen lassen, was er gerade
tut, und dafür sorgen, dass du es merkst, wenn er überhaupt nicht mehr läuft.
Das sind drei verschiedene Probleme, und diese Seite behandelt jedes davon.

```yaml
notify:
  on: [failure, rollback, halt]     # success, failure, rollback, halt oder "all"
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
```

Kanäle sind unabhängig: Jeder kann seine eigene `on:`-Liste haben, und ein
defekter Endpunkt hält weder die anderen Kanäle noch das Deployment auf.

## Telegram — Meldungen plus Fernbedienung

Telegram ist der einzige Kanal mit Rückweg. Du kannst Fragen stellen:

```
Du:  /status
Bot: WATCH        REPO             TRIGGER      REVISION       LAST SUCCESS  STATE
     shop         acme/shop        releases     v1.4.1 9f3c2a  2h ago        ok
     api-staging  acme/api         branch main  41ae08c        12m ago       ok
     backup       acme/backup      releases     —              never         HALTED (3 failures)

Du:  /rollback shop
Bot: ↩️ Rolling back shop.
```

Wie beim GitHub-Client wird die Telegram-API **abgefragt** statt einen Webhook
zu empfangen — es braucht also weiterhin keinen eingehenden Port und
funktioniert hinter NAT.

### Einrichtung

1. Schreibe [@BotFather](https://t.me/botfather) auf Telegram an, sende
   `/newbot` und wähle einen Namen. Du erhältst einen Token der Form
   `123456:AAE...`.
2. Schicke deinem neuen Bot irgendeine Nachricht — ein Bot kann ein Gespräch
   nicht von sich aus beginnen.
3. Ermittle deine Chat-ID:
   ```bash
   curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | grep -o '"id":[0-9-]*' | head -1
   ```
4. Trage es ein, und halte den Token aus der Datei heraus:
   ```yaml
   notify:
     channels:
       - type: telegram
         token_env: DECKHAND_TELEGRAM_TOKEN
         chat_id: "123456789"
         commands: true          # weglassen, wenn du nur Meldungen willst
   ```

`deckhand check` prüft den Token beim Start und nennt den Benutzernamen des
Bots — ein Tippfehler fällt also sofort auf und nicht erst beim ersten
fehlgeschlagenen Deployment.

### Befehle

| Befehl | Wirkung |
|---|---|
| `/status` | Auf welchem Stand jeder Watch ist und was klemmt |
| `/history [watch]` | Die letzten zehn Audit-Einträge |
| `/deploy <watch>` | Sofort deployen, ohne Rücksicht auf das Zeitfenster |
| `/rollback <watch>` | Zurück auf die vorherige Revision |
| `/pause [grund]` | Alle Deployments anhalten |
| `/resume [watch]` | Anhalten aufheben oder gestoppten Watch freigeben |
| `/help` | Die Liste oben |

Deployments dauern Minuten, deshalb antwortet `/deploy` sofort; das Ergebnis
kommt als gewöhnliche Benachrichtigung.

### Was die Fernbedienung schützt

* **Nur dein Chat wird befolgt.** Befehle von jeder anderen Chat-ID werden
  vollständig ignoriert — keine Antwort, nicht einmal ein Fehler. Ein Fremder,
  der den Bot findet, kann also nicht einmal bestätigen, dass es ihn gibt.
* **Alte Befehle werden abgelehnt.** Telegram hält unzugestellte Nachrichten
  einen Tag lang vor. Ohne Prüfung könnte ein Neustart von Deckhand ein
  `/deploy` ausführen, das du gestern gesendet hast. Alles, was älter als fünf
  Minuten ist, wird mit einer Erklärung abgelehnt.
* **Der Rückstau wird beim Start übersprungen**, aus demselben Grund.
* **Der Offset wird gespeichert**, damit ein Neustart keinen bereits
  bearbeiteten Befehl wiederholt.
* **Ein gestohlener Bot-Token reicht nicht zum Deployen.** Er erlaubt
  Mitlesen und das Auftreten als Bot, aber Befehle werden nur von deiner
  Chat-ID angenommen — und die verschafft der Token nicht.

Die ehrliche Abwägung: **Die Telegram-Server sehen die Inhalte** — Repository-
Namen, Hostnamen, Fehlermeldungen. Der Transport ist verschlüsselt, die
Speicherung auf deren Seite nicht Ende-zu-Ende. Für Deployment-Metadaten ist
das meist ein vertretbarer Handel; wenn nicht, nimm selbst gehostetes ntfy und
lass `commands` aus.

## ntfy — Push ohne Konto

[ntfy](https://ntfy.sh) ist ein kleiner Pub-Sub-Dienst: Was an eine Themen-URL
gesendet wird, erscheint als Push-Nachricht in der App (Android, iOS, Web).
Kein Konto, quelloffen, und selbst hostbar.

```yaml
- type: ntfy
  url: https://ntfy.example.com/deploy
  token_env: DECKHAND_NTFY_TOKEN
  priority:
    success: low
    halt: urgent
```

Deckhand setzt die Priorität so, dass Alarme anders klingen als gute
Nachrichten:

| Ereignis | Standardpriorität | Wirkung |
|---|---|---|
| `success` | `low` | Leise, ohne Ton |
| `failure`, `rollback` | `high` | Normaler Alarm |
| `halt` | `urgent` | Durchdringt „Nicht stören" |

Unter `priority:` lässt sich jede davon überschreiben. Gültig sind `min`,
`low`, `default`, `high` und `urgent`.

### Eine Warnung zum öffentlichen ntfy.sh

**Auf ntfy.sh hat ein Thema keinerlei Zugriffskontrolle.** Wer den Namen errät,
liest deine Deployment-Historie mit — Repositories, Hostnamen, Fehlermeldungen
— und kann selbst hineinschreiben. `deckhand check` warnt dich, wenn es ein
unauthentifiziertes ntfy.sh-Thema sieht.

Zwei Auswege:

* **Ein langer Zufallsname** — `deckhand-a7f3k9m2q8` statt `deckhand`. Das ist
  Verschleierung, aber sie hebt die Hürde erheblich.
* **ntfy selbst hosten** — ein Container — mit `auth-default-access: deny-all`.
  Dann ist Deckhands Token eine echte Zugangsberechtigung, gesendet als
  `Authorization: Bearer`, niemals in der URL.

## Slack, Mattermost, Discord und ein eigener Endpunkt

```yaml
- type: slack
  url: https://hooks.slack.com/services/...    # auch Mattermost, oder Discord + /slack
- type: webhook
  url: https://automation.example.com/deckhand  # das vollständige Ereignis als JSON
```

Der Typ `webhook` sendet das komplette Ereignisobjekt — passend für n8n, Home
Assistant oder was auch immer du selbst betreibst:

```json
{"event":"rollback","watch":"shop","repo":"acme/shop","ref":"v1.4.0",
 "sha":"9f3c2ab8","host":"web-01","detail":"health check failed…","time":"2026-09-10T14:04:08Z"}
```

## Der Herzschlag — merken, dass Deckhand selbst weg ist

Alle Kanäle oben melden sich nur, wenn etwas schiefgeht. Das lässt einen
blinden Fleck — und zwar den gefährlichen: **Wenn Deckhand abstürzt, die
Maschine ausgeschaltet wird oder das Netz ausfällt, bekommst du überhaupt keine
Meldung.** Stille sieht dann genauso aus wie „alles in Ordnung".

Ein Totmannschalter schließt diese Lücke. Deckhand pingt regelmäßig eine URL
an; bleibt der Ping aus, schlägt der Dienst am anderen Ende Alarm.

```yaml
heartbeat:
  url: https://hc-ping.com/deine-uuid
  interval: 5m
  method: GET      # GET (Standard), POST oder HEAD
```

Zwei Dienste, die direkt funktionieren:

* **[healthchecks.io](https://healthchecks.io)** — gehostet, für eine Handvoll
  Prüfungen kostenlos. Prüfung anlegen, Periode etwas größer als dein Intervall
  setzen, Ping-URL einfügen.
* **[Uptime Kuma](https://uptime.kuma.pet)** — selbst gehostet, ein Container.
  Lege einen *Push*-Monitor an und nimm die URL, die er dir gibt.

Hinweise:

* Der erste Ping erfolgt **beim Start**, nicht erst nach dem ersten Intervall —
  ein Worker, der direkt nach einem Neustart stirbt, fällt also trotzdem auf.
* Der Pfad der URL ist ein Geheimnis: Wer ihn hat, kann deinen Herzschlag
  fälschen. Deckhand hält ihn aus den Logs und aus der Ausgabe von
  `deckhand check` heraus.
* Ein defekter Herzschlag-Endpunkt wird protokolliert, mit abnehmender
  Häufigkeit, und beeinflusst Deployments nie.

## Welche Kombination

| Du willst | Nimm |
|---|---|
| Möglichst wenig Einrichtung | ntfy mit langem Zufallsthema |
| Vom Handy aus nachfragen | Telegram mit `commands: true` |
| Nichts verlässt deine Maschinen | Selbst gehostetes ntfy + selbst gehostetes Uptime Kuma |
| Sichtbarkeit im Team | Slack oder Mattermost, plus Herzschlag |

Was du auch wählst: **richte den Herzschlag mit ein.** Er ist der Unterschied
zwischen „ich weiß, dass ein Deployment fehlgeschlagen ist" und „mein
Deployment-Worker ist seit einer Woche tot".
