package main

// exampleConfig is written by "deckhand init".
const exampleConfig = `# deckhand configuration - see docs/en/configuration.md
#
# Keep this file readable only by the account deckhand runs as:
#   chmod 600 deckhand.yaml
version: 1

defaults:
  poll_interval: 60s        # how often GitHub is asked; unchanged answers are free
  timezone: Europe/Berlin   # used by every time window below
  command_timeout: 10m
  strategy: releases        # releases = atomic switch + rollback | inplace
  keep_releases: 5
  failure_limit: 3          # stop a watch after this many failures in a row

github:
  # Preferred: keep the token out of this file entirely.
  #   export DECKHAND_GITHUB_TOKEN=github_pat_...
  token_env: DECKHAND_GITHUB_TOKEN
  # token_file: /etc/deckhand/token   # alternative, must be chmod 600

# Optional. Remove this block if you do not want notifications.
# notify:
#   on: [failure, rollback]           # add "success" if you want the good news too
#   webhook: https://ntfy.sh/your-private-topic
#   format: ntfy                      # json (default) | slack | ntfy

watch:
  # ---------------------------------------------------------------------
  # Deploy a new release of your own app and restart the containers.
  # ---------------------------------------------------------------------
  - name: shop
    repo: your-name/shop
    trigger:
      type: release         # release | branch | tag
      prerelease: false
      tag_match: "v*"
    path: /srv/shop
    shared:                 # survives every deployment
      - .env
      - data/
    run:
      - ["docker", "compose", "pull"]
      - ["docker", "compose", "up", "-d", "--remove-orphans"]
    health:
      http: http://localhost:8080/healthz
      retries: 10
      interval: 3s
      initial_delay: 5s
    rollback: auto

  # ---------------------------------------------------------------------
  # Staging: every merge into develop, but only at night.
  # ---------------------------------------------------------------------
  - name: api-staging
    repo: your-name/api
    trigger:
      type: branch
      branch: develop
    path: /srv/api-staging
    window:
      allow:
        - "Mon-Fri 22:00-05:00"
        - "Sat-Sun *"
      # blackout:
      #   - "Fri 16:00-23:59"
    run:
      - ["systemctl", "--user", "restart", "api-staging"]

  # ---------------------------------------------------------------------
  # A public repository you do not control: no token, signature required.
  # ---------------------------------------------------------------------
  # - name: upstream-tool
  #   repo: someorg/sometool
  #   auth: none
  #   trigger:
  #     type: release
  #   verify:
  #     require_signed_tag: true
  #     allowed_signers: /etc/deckhand/allowed_signers
  #   path: /opt/sometool
  #   run:
  #     - ["/usr/local/bin/rebuild-sometool.sh"]
`
