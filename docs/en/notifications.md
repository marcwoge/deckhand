# Notifications and monitoring

Deckhand can tell you what it did, let you ask what it is doing, and make sure
you notice when it stops working at all. Those are three separate problems, and
this page covers each.

```yaml
notify:
  on: [failure, rollback, halt]     # success, failure, rollback, halt, or "all"
  channels:
    - type: telegram
      token_env: DECKHAND_TELEGRAM_TOKEN
      chat_id: "123456789"
      commands: true
    - type: ntfy
      url: https://ntfy.example.com/deploy
      token_env: DECKHAND_NTFY_TOKEN

heartbeat:
  url: https://hc-ping.com/your-uuid
  interval: 5m
```

Channels are independent: each one can have its own `on:` list, and one broken
endpoint never stops the others or the deployment itself.

## Telegram — alerts plus a remote control

Telegram is the only channel with a way back. You can ask it questions:

```
You:  /status
Bot:  WATCH        REPO             TRIGGER      REVISION       LAST SUCCESS  STATE
      shop         acme/shop        releases     v1.4.1 9f3c2a  2h ago        ok
      api-staging  acme/api         branch main  41ae08c        12m ago       ok
      backup       acme/backup      releases     —              never         HALTED (3 failures)

You:  /rollback shop
Bot:  ↩️ Rolling back shop.
```

Like the GitHub client, this **polls** the Telegram API rather than receiving a
webhook — so it still needs no inbound port and works behind NAT.

### Setting it up

1. Message [@BotFather](https://t.me/botfather) on Telegram, send `/newbot`,
   pick a name. You get a token that looks like `123456:AAE...`.
2. Send any message to your new bot — a bot cannot start a conversation.
3. Find your chat ID:
   ```bash
   curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | grep -o '"id":[0-9-]*' | head -1
   ```
4. Configure it, keeping the token out of the file:
   ```yaml
   notify:
     channels:
       - type: telegram
         token_env: DECKHAND_TELEGRAM_TOKEN
         chat_id: "123456789"
         commands: true          # omit for alerts only
   ```

`deckhand check` verifies the token at startup and reports the bot's username,
so a typo shows up immediately instead of at the first failed deployment.

### Commands

| Command | What it does |
|---|---|
| `/status` | What every watch is on, and what is broken |
| `/history [watch]` | The last ten audit entries |
| `/deploy <watch>` | Deploy now, ignoring the time window |
| `/rollback <watch>` | Back to the previous revision |
| `/pause [reason]` | Hold every deployment |
| `/resume [watch]` | Release the hold, or clear a halted watch |
| `/help` | The list above |

Deployments take minutes, so `/deploy` answers immediately and the outcome
arrives as a normal notification.

### What protects the remote control

* **Only your chat is obeyed.** Commands from any other chat ID are ignored
  entirely — no reply, not even an error, so a stranger who finds the bot
  cannot even confirm it exists.
* **Stale commands are refused.** Telegram holds undelivered messages for a
  day. Without a check, restarting Deckhand could execute a `/deploy` you sent
  yesterday. Anything older than five minutes is refused with an explanation.
* **The backlog is skipped at startup**, for the same reason.
* **The offset is persisted**, so a restart never replays a command that was
  already handled.
* **Stealing the bot token is not enough to deploy.** It lets an attacker read
  the notifications and impersonate the bot, but commands are only accepted
  from your chat ID, which the token does not grant.

The honest trade-off: **Telegram's servers see the content** — repository
names, hostnames, error messages. Transport is encrypted, storage on their side
is not end-to-end. For deployment metadata that is usually an acceptable trade;
if it is not, use self-hosted ntfy below, and keep `commands` off.

## ntfy — push without an account

[ntfy](https://ntfy.sh) is a small pub-sub service: anything POSTed to a topic
URL appears as a push notification in the app (Android, iOS, web). No account,
open source, and you can host it yourself.

```yaml
- type: ntfy
  url: https://ntfy.example.com/deploy
  token_env: DECKHAND_NTFY_TOKEN
  priority:
    success: low
    halt: urgent
```

Deckhand sets the priority so alerts sound different from good news:

| Event | Default priority | Effect |
|---|---|---|
| `success` | `low` | Quiet, no sound |
| `failure`, `rollback` | `high` | Normal alert |
| `halt` | `urgent` | Gets through Do Not Disturb |

Override any of them under `priority:`. Valid values are `min`, `low`,
`default`, `high` and `urgent`.

### A warning about the public ntfy.sh

**On ntfy.sh a topic has no access control.** Anyone who guesses the name reads
your deployment history — repositories, hostnames, error messages — and can
post to it as well. `deckhand check` warns you when it sees an unauthenticated
ntfy.sh topic.

Two ways to fix it:

* **A long random topic name** — `deckhand-a7f3k9m2q8` rather than `deckhand`.
  Obscurity, but it raises the bar considerably.
* **Host ntfy yourself** — one container — and set `auth-default-access: deny-all`.
  Then Deckhand's token is a real credential, sent as `Authorization: Bearer`,
  never in the URL.

## Slack, Mattermost, Discord and your own endpoint

```yaml
- type: slack
  url: https://hooks.slack.com/services/...    # also Mattermost, or Discord + /slack
- type: webhook
  url: https://automation.example.com/deckhand  # the full event as JSON
```

The `webhook` type posts the complete event object, which suits n8n, Home
Assistant or anything else you drive yourself:

```json
{"event":"rollback","watch":"shop","repo":"acme/shop","ref":"v1.4.0",
 "sha":"9f3c2ab8","host":"web-01","detail":"health check failed…","time":"2026-09-10T14:04:08Z"}
```

## The heartbeat — noticing that Deckhand itself is gone

Every channel above only fires when something goes wrong. That leaves one blind
spot, and it is the dangerous one: **if Deckhand crashes, the machine is
powered off, or the network breaks, you get no notification at all.** Silence
looks exactly like everything being fine.

A dead-man's switch closes it. Deckhand pings a URL on a schedule; if the ping
stops arriving, the service on the other end raises the alarm.

```yaml
heartbeat:
  url: https://hc-ping.com/your-uuid-here
  interval: 5m
  method: GET      # GET (default), POST or HEAD
```

Two services that work out of the box:

* **[healthchecks.io](https://healthchecks.io)** — hosted, free for a handful
  of checks. Create a check, set the period to a little more than your
  interval, paste its ping URL.
* **[Uptime Kuma](https://uptime.kuma.pet)** — self-hosted, one container.
  Create a *Push* monitor and use the push URL it gives you.

Notes:

* The first ping happens **at startup**, not after the first interval, so a
  worker that dies right after a restart is still noticed.
* The URL path is a credential — anyone who has it can fake your heartbeat.
  Deckhand keeps it out of logs and out of `deckhand check` output.
* A failing heartbeat endpoint is logged, at a decreasing rate, and never
  affects deployments.

## Which combination to pick

| You want | Use |
|---|---|
| The least setup | ntfy with a long random topic |
| Ask questions from your phone | Telegram with `commands: true` |
| Nothing leaves your machines | Self-hosted ntfy + self-hosted Uptime Kuma |
| Team visibility | Slack or Mattermost, plus a heartbeat |

Whatever you choose, **configure the heartbeat too**. It is the difference
between knowing a deployment failed and knowing your deployment worker has been
dead for a week.
