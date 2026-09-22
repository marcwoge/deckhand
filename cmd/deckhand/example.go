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
  # Where state and the audit log live. Set it when the service runs as its own
  # account, so that a command you run by hand reads the same state as the
  # service does. The .deb and .rpm packages use /var/lib/deckhand.
  # state_dir: /var/lib/deckhand
  # audit:
  #   max_size: 10MB        # rotate the audit log past this size (0 = never)
  #   keep: 5

# Split the watches below into their own files when this one gets long:
# include: deckhand.d       # a directory of *.yaml files, each with watch entries

github:
  # Preferred: keep the token out of this file entirely.
  #   export DECKHAND_GITHUB_TOKEN=github_pat_...
  token_env: DECKHAND_GITHUB_TOKEN
  # token_file: /etc/deckhand/token   # alternative, must be chmod 600

  # Instead of a personal token you can authenticate as a GitHub App. Its key
  # never expires and mints hourly installation tokens, and the identity is the
  # app rather than a person - worth it for several machines, for an
  # organisation, or when deployments must not break when someone leaves.
  # Remove the token_env line above when you use this.
  #
  # app:
  #   id: "123456"
  #   private_key_file: /etc/deckhand/app-private-key.pem   # chmod 600
  #   # installation_id: 12345678   # omit to discover it per repository

# Optional. Remove this block if you do not want notifications.
#
# notify:
#   on: [failure, rollback, halt]     # add "success" if you want the good news too
#   channels:
#
#     # Push to your phone. On the public ntfy.sh a topic is readable by anyone
#     # who guesses the name, so use a long random one - or host ntfy yourself
#     # and set a token, as shown here.
#     - type: ntfy
#       url: https://ntfy.example.com/deploy
#       token_env: DECKHAND_NTFY_TOKEN
#       priority:                     # min | low | default | high | urgent
#         success: low
#         halt: urgent
#
#     # Telegram doubles as a remote control: send /status, /deploy or
#     # /rollback to the bot. Commands are only accepted from this chat_id.
#     - type: telegram
#       token_env: DECKHAND_TELEGRAM_TOKEN
#       chat_id: "123456789"
#       commands: true
#
#     # Also works: type: slack (incoming webhook) and type: webhook (full
#     # JSON event to your own endpoint).

# Tells an outside service that deckhand is alive. Without it, a crashed
# worker produces no alert at all - silence looks exactly like success.
# Works with healthchecks.io and with a self-hosted Uptime Kuma push monitor.
#
# heartbeat:
#   url: https://hc-ping.com/your-uuid-here
#   interval: 5m

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
      #   - "Fri 16:00-23:59"     # weekday plus time
      #   - "12-24..12-26"        # every Christmas
      #   - "2026-11-27"          # one specific date
    run:
      - ["systemctl", "--user", "restart", "api-staging"]

  # ---------------------------------------------------------------------
  # Deploy an image built somewhere else, so this machine needs no build
  # toolchain. See docs/en/building-elsewhere.md.
  # ---------------------------------------------------------------------
  # - name: shop-from-registry
  #   trigger:
  #     type: image
  #     image: ghcr.io/your-name/shop
  #     tag: latest
  #   path: /srv/shop            # a directory you maintain, holding compose.yaml
  #   run:
  #     - ["docker", "compose", "pull"]
  #     - ["docker", "compose", "up", "-d"]
  #   health:
  #     http: http://localhost:8080/healthz
  #
  # In that compose.yaml, pin the digest so rollback works:
  #   services:
  #     app:
  #       image: ${DECKHAND_IMAGE_REF}
  #
  # For a private image add credentials at the top level:
  # registry:
  #   ghcr.io:
  #     username: your-name
  #     password_env: DECKHAND_GHCR_TOKEN

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
