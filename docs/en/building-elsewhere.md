# Building elsewhere

A compiler, `node_modules` or a Docker build has no business on a machine that
serves traffic. It widens the attack surface, competes for memory, and makes each
deployment slightly different from the last. Build once somewhere else, deploy
the artefact you tested.

Deckhand supports this by watching a **container image** instead of a git
repository. The build happens wherever you like; the production machine notices a
new digest and restarts the service.

```
  build host or CI                registry                 production VPS
  ────────────────                ────────                 ──────────────
  docker build          ──push──▶  ghcr.io/you/app  ◀─poll─  deckhand
                                        │                       │
                                        └───────pull────────────▶│
                                                                 ▼
                                                     docker compose up -d
```

Nothing is pushed to production and no port is opened there: Deckhand asks the
registry whether the digest changed, exactly as it asks GitHub about commits.

## Why the digest, not the tag

A tag moves. `latest` today is not `latest` tomorrow, and a rebuilt `v1.4.0`
still says `v1.4.0`. The **digest** is the image's content hash and changes
whenever anything in the image does — so Deckhand watches the digest behind a
tag. Rebuild and push under the same tag, and the deployment triggers.

That also means the trigger is the *artefact*, not the commit. If Deckhand
watched git while the build ran in parallel, it would deploy before the image
existed. Watching the registry removes the race entirely.

## Configuring production

```yaml
version: 1

registry:
  ghcr.io:
    username: your-github-username
    password_env: DECKHAND_GHCR_TOKEN     # a PAT with read:packages

watch:
  - name: shop
    trigger:
      type: image
      image: ghcr.io/your-name/shop
      tag: latest                          # or tag_match: "v*"
    path: /srv/shop                         # the directory holding compose.yaml
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d", "--remove-orphans"]
    health:
      http: http://localhost:8080/healthz
      retries: 10
    rollback: auto
```

No `repo`, no `strategy`, no `shared` — there is no checkout. `path` is a
directory **you** maintain, holding the compose file. Deckhand never writes
there; it only runs your command in it.

For a public image the `registry:` block can be left out entirely.

### Pin the digest in your compose file

This one line is what makes rollback work:

```yaml
services:
  app:
    image: ${DECKHAND_IMAGE_REF}      # ghcr.io/you/shop@sha256:...
```

Deckhand passes the exact digest in the environment. Writing `image:
ghcr.io/you/shop:latest` instead would work for deploying, but a rollback would
pull `latest` again — that is, the broken image. With `DECKHAND_IMAGE_REF`, going
back means starting the previous digest.

Available to your commands:

| Variable | Example |
|---|---|
| `DECKHAND_IMAGE` | `ghcr.io/you/shop` |
| `DECKHAND_IMAGE_TAG` | `latest` |
| `DECKHAND_IMAGE_DIGEST` | `sha256:aaaa…` |
| `DECKHAND_IMAGE_REF` | `ghcr.io/you/shop@sha256:aaaa…` |

`DECKHAND_SHA` and `DECKHAND_REF` are set too, so a command written for a git
watch keeps working.

## Option A: building in CI

For a public repository GitHub's standard runners are free and unmetered, and
every build starts from a clean machine. `examples/build-and-push.yml` is a
complete workflow; the essential part:

```yaml
permissions:
  contents: read
  packages: write

steps:
  - uses: actions/checkout@<sha>   # pin actions to a commit
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

`secrets.GITHUB_TOKEN` is provided automatically — no secret to manage.

## Option B: building on your own host

Useful for a private repository where minutes are metered, or when the build
needs an environment CI cannot easily give it. On the build host:

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u your-name --password-stdin
docker build -t ghcr.io/your-name/shop:latest .
docker push ghcr.io/your-name/shop:latest
```

`examples/build-on-host.sh` wraps this with the parts that matter in practice:
building a tag *and* `latest`, failing before pushing if tests fail, and not
pushing a dirty working tree by accident.

You can even let Deckhand run that script on the build host — one watch with a
git trigger that builds and pushes, and on production a second watch with an
image trigger that deploys. Deckhand then holds both ends without either machine
reaching into the other. Note that pushing needs a registry credential with
write access on the build host; production keeps read-only.

## Registry credentials

For **ghcr.io**, a classic PAT with `read:packages` is enough on production.
Fine-grained tokens do not cover packages at the time of writing, so this is one
place where a classic token is the right answer. Keep it out of the config file:

```bash
sudo install -m 600 -o deckhand /dev/null /etc/deckhand/env
echo 'DECKHAND_GHCR_TOKEN=ghp_...' | sudo tee -a /etc/deckhand/env
```

A public package needs no credential at all.

**Docker Hub** limits anonymous manifest requests severely (a few hundred per
six hours, shared per IP). With several watches polling every minute you will
hit that. Either authenticate, or raise `poll_interval` to a few minutes.
`ghcr.io` is far more generous.

## Checking it

```bash
deckhand check     # shows: trigger: a new digest on image tag latest
deckhand doctor    # confirms the registry answers and the digest resolves
deckhand deploy shop
```

`check` also warns that this watch builds nothing — a reminder that something
else has to push the image, which is the point.
