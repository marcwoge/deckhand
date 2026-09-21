# Woanders bauen

Ein Compiler, `node_modules` oder ein Docker-Build haben auf einer Maschine, die
Anfragen bedient, nichts zu suchen. Das vergrößert die Angriffsfläche, streitet
um Speicher und lässt jedes Deployment ein wenig anders ausfallen als das
vorherige. Einmal woanders bauen, das geprüfte Artefakt ausrollen.

Deckhand unterstützt das, indem es ein **Container-Image** beobachtet statt eines
Git-Repositories. Gebaut wird, wo du willst; die Produktionsmaschine bemerkt
einen neuen Digest und startet den Dienst neu.

```
  Build-Host oder CI              Registry                 Produktions-VPS
  ──────────────────              ────────                 ───────────────
  docker build          ──push──▶  ghcr.io/du/app  ◀─Abruf─  deckhand
                                        │                       │
                                        └───────pull────────────▶│
                                                                 ▼
                                                     docker compose up -d
```

Nichts wird auf die Produktion geschoben und dort kein Port geöffnet: Deckhand
fragt die Registry, ob sich der Digest geändert hat — genau wie es GitHub nach
Commits fragt.

## Warum der Digest und nicht der Tag

Ein Tag wandert. `latest` heute ist nicht `latest` morgen, und ein neu gebautes
`v1.4.0` heißt weiterhin `v1.4.0`. Der **Digest** ist der Inhalts-Hash des Images
und ändert sich, sobald sich irgendetwas darin ändert — deshalb beobachtet
Deckhand den Digest hinter einem Tag. Neu bauen und unter demselben Tag pushen
löst das Deployment aus.

Das heißt auch: Auslöser ist das *Artefakt*, nicht der Commit. Würde Deckhand Git
beobachten, während der Build parallel läuft, würde es deployen, bevor das Image
existiert. Die Registry zu beobachten beseitigt dieses Wettrennen vollständig.

## Produktion konfigurieren

```yaml
version: 1

registry:
  ghcr.io:
    username: dein-github-name
    password_env: DECKHAND_GHCR_TOKEN     # ein PAT mit read:packages

watch:
  - name: shop
    trigger:
      type: image
      image: ghcr.io/dein-name/shop
      tag: latest                          # oder tag_match: "v*"
    path: /srv/shop                         # das Verzeichnis mit der compose.yaml
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d", "--remove-orphans"]
    health:
      http: http://localhost:8080/healthz
      retries: 10
    rollback: auto
```

Kein `repo`, kein `strategy`, kein `shared` — es gibt keinen Checkout. `path` ist
ein Verzeichnis, das **du** pflegst und in dem die Compose-Datei liegt. Deckhand
schreibt dort nie hinein, es führt nur deinen Befehl darin aus.

Für ein öffentliches Image kann der `registry:`-Block ganz entfallen.

### Den Digest in der Compose-Datei festnageln

Diese eine Zeile ist es, die den Rollback funktionsfähig macht:

```yaml
services:
  app:
    image: ${DECKHAND_IMAGE_REF}      # ghcr.io/du/shop@sha256:...
```

Deckhand übergibt den exakten Digest in der Umgebung. Stünde dort stattdessen
`image: ghcr.io/du/shop:latest`, würde das Deployen funktionieren — aber ein
Rollback würde wieder `latest` ziehen, also das defekte Image. Mit
`DECKHAND_IMAGE_REF` bedeutet Zurückgehen, den vorherigen Digest zu starten.

Für deine Befehle verfügbar:

| Variable | Beispiel |
|---|---|
| `DECKHAND_IMAGE` | `ghcr.io/du/shop` |
| `DECKHAND_IMAGE_TAG` | `latest` |
| `DECKHAND_IMAGE_DIGEST` | `sha256:aaaa…` |
| `DECKHAND_IMAGE_REF` | `ghcr.io/du/shop@sha256:aaaa…` |

`DECKHAND_SHA` und `DECKHAND_REF` werden ebenfalls gesetzt, damit ein Befehl, der
für einen Git-Watch geschrieben wurde, weiter funktioniert.

## Variante A: in der CI bauen

Für ein öffentliches Repository sind GitHubs Standard-Runner kostenlos und
unbegrenzt, und jeder Build startet auf einer frischen Maschine.
`examples/build-and-push.yml` ist ein vollständiger Workflow; das Wesentliche:

```yaml
permissions:
  contents: read
  packages: write

steps:
  - uses: actions/checkout@<sha>   # Actions auf einen Commit pinnen
  - uses: docker/login-action@<sha>
    with:
      registry: ghcr.io
      username: ${{ github.actor }}
      password: ${{ secrets.GITHUB_TOKEN }}
  - uses: docker/build-push-action@<sha>
    with:
      push: true
      tags: ghcr.io/${{ github.repository }}:latest
```

`secrets.GITHUB_TOKEN` wird automatisch bereitgestellt — kein Geheimnis zu
verwalten.

## Variante B: auf dem eigenen Host bauen

Nützlich bei einem privaten Repository, wo Minuten gezählt werden, oder wenn der
Build eine Umgebung braucht, die die CI nicht leicht hergibt. Auf dem Build-Host:

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u dein-name --password-stdin
docker build -t ghcr.io/dein-name/shop:latest .
docker push ghcr.io/dein-name/shop:latest
```

`examples/build-on-host.sh` verpackt das mit den Dingen, die in der Praxis
zählen: einen Tag *und* `latest` bauen, vor dem Push abbrechen wenn Tests
fehlschlagen, und nicht versehentlich einen unaufgeräumten Arbeitsbaum pushen.

Du kannst diese Skript auch von Deckhand auf dem Build-Host ausführen lassen —
ein Watch mit Git-Trigger, der baut und pusht, und auf der Produktion ein zweiter
Watch mit Image-Trigger, der deployt. Deckhand hält dann beide Enden, ohne dass
eine Maschine in die andere greift. Beachte: Zum Pushen braucht der Build-Host
Registry-Zugangsdaten mit Schreibrecht; die Produktion bleibt nur lesend.

## Registry-Zugangsdaten

Für **ghcr.io** genügt auf der Produktion ein klassischer PAT mit
`read:packages`. Fine-grained Tokens deckten Packages zum jetzigen Zeitpunkt
nicht ab — hier ist ein klassischer Token also die richtige Antwort. Halte ihn
aus der Konfigurationsdatei heraus:

```bash
sudo install -m 600 -o deckhand /dev/null /etc/deckhand/env
echo 'DECKHAND_GHCR_TOKEN=ghp_...' | sudo tee -a /etc/deckhand/env
```

Ein öffentliches Package braucht überhaupt keine Zugangsdaten.

**Docker Hub** begrenzt anonyme Manifest-Anfragen stark (einige hundert pro sechs
Stunden, geteilt je IP). Mit mehreren Watches im Minutentakt läufst du dagegen.
Entweder authentifizieren oder `poll_interval` auf einige Minuten erhöhen.
`ghcr.io` ist deutlich großzügiger.

## Prüfen

```bash
deckhand check     # zeigt: trigger: a new digest on image tag latest
deckhand doctor    # bestätigt, dass die Registry antwortet und der Digest auflöst
deckhand deploy shop
```

`check` warnt außerdem, dass dieser Watch nichts baut — eine Erinnerung daran,
dass etwas anderes das Image pushen muss. Genau das ist der Sinn.
