# Time windows

A window says *when* a deployment may start. Without one, Deckhand deploys as
soon as it notices the change — which is usually what you want for staging, and
occasionally alarming for production.

```yaml
window:
  timezone: Europe/Berlin        # optional; defaults to defaults.timezone
  allow:
    - "Mon-Fri 22:00-05:00"
    - "Sat-Sun *"
  blackout:
    - "Fri 16:00-23:59"
```

A deployment may start when it matches **at least one** `allow` rule and **no**
`blackout` rule. With no rules at all, the window is always open.

## Rule syntax

```
<days> <times>
```

**Days** — `Mon`, `Tue`, `Wed`, `Thu`, `Fri`, `Sat`, `Sun` (case-insensitive,
long names accepted), as a range (`Mon-Fri`), a list (`Sat,Sun`), or `*` for
every day.

**Times** — `HH:MM-HH:MM` in 24-hour form, or `*` for the whole day.

| Rule | Meaning |
|---|---|
| `Mon-Fri 22:00-05:00` | Weeknights, from 22:00 until 05:00 the next morning |
| `Sat,Sun *` | All weekend |
| `* 02:00-04:00` | Every night between two and four |
| `Sun` | All of Sunday (times may be omitted) |
| `22:00-05:00` | Every night (days may be omitted) |

### Windows that cross midnight

`Mon-Fri 22:00-05:00` belongs to the day it **starts** on. Wednesday 23:00 is
inside it, and so is Thursday 04:00 — that is still Wednesday night. Saturday
03:00 is inside it too, because Friday night is a weeknight. Saturday 23:00 is
not.

This is almost always what people mean, but it is worth checking against your
own expectation before relying on it.

### Timezones

Rules are evaluated in `window.timezone`, falling back to `defaults.timezone`,
falling back to the machine's zone. Use a real zone name such as
`Europe/Berlin` rather than a fixed offset, so daylight saving is handled for
you. `deckhand status` shows the zone each window is using.

## Coalescing

While the window is closed, Deckhand keeps polling and keeps only the *newest*
revision it has seen. When the window opens, that one revision is deployed.
Twelve merges overnight produce one deployment at 22:00, not twelve.

The queued revision is recorded in the audit log as a `queue` event, so
`deckhand history` shows what has been waiting and since when.

## Deploying anyway

Windows apply to automatic deployments only. A human can always override:

```bash
deckhand deploy shop            # now, regardless of the window
deckhand deploy shop --force    # even if the revision is already current
```

## Holding everything

To stop deployments across all watches — during a database migration, say:

```bash
deckhand pause "migrating the database"
deckhand resume
```

The hold survives restarts; it is a file in the state directory. `deckhand
status` shows it prominently, so a forgotten pause does not quietly become a
mystery.
