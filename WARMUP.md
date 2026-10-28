# Warmup fork

This fork of [`router-for-me/CLIProxyAPI`](https://github.com/router-for-me/CLIProxyAPI)
v8.0.36 adds one feature: a built-in, centrally scheduled **Codex window warmup**.

It replaces the third-party `cpa-window-starter` plugin. Because it lives in the
core, it goes through the normal request path instead of a plugin host call.

## What it does

At each configured local time it sends **exactly one** short request per selected
Codex credential, pinned to that credential, so the five-hour window starts
earlier than the first real use.

The request is executed through the same handler as an inbound client request
(`BaseAPIHandler.ExecuteModel` with the credential pinned via
`handlers.WithPinnedAuthID`), so all of the following apply normally:

- credential selection, priority and weights (`routing`)
- quota cooldowns and availability gates
- payload override / inference rules
- protocol translation
- usage statistics

It never writes to the auth directory and never modifies tokens.

## Configuration

Top-level `warmup` block. The full annotated form lives in
[`config.example.yaml`](config.example.yaml).

```yaml
warmup:
  enabled: false
  timezone: "Europe/Moscow"
  model: "gpt-6-luna"
  reasoning-effort: "low"
  prompt: "Reply with exactly: ok"
  accounts: []          # [] = every active Codex credential, else auth file names
  schedules: ["06:00"]  # daily, 24-hour HH:MM, at most 24, no duplicates
  run-on-start: false
```

### Validation

Configuration errors fail the parse, so a bad block stops the reload instead of
silently doing nothing:

| Setting | Rule |
| --- | --- |
| `timezone` | valid IANA zone (default `UTC`) |
| `schedules` | `HH:MM`, 24-hour, at most 24 entries, no duplicates |
| `model` | required when `enabled: true` |
| `schedules` | at least one entry when `enabled: true` |

`enabled: false` is the default and makes the whole block inert.

## Behaviour details

- **Idempotency.** Each run records its completed slot in
  `warmup-state.json` next to the config file, written atomically. A restart
  never repeats a completed slot, and a double timer fire cannot run it twice.
- **Cooldown safety.** A credential that is disabled, has a rejected token, or is
  inside a cooldown for this model is skipped and logged, never warmed.
- **Isolation.** One credential failing does not stop the others.
- **Logging.** Every attempt logs `auth_index` (the auth file name), the
  account, the status, and — when the upstream reports them —
  `X-Codex-Primary-Used-Percent` and `X-Codex-Primary-Reset-After-Seconds`.
- **Hot reload.** Editing the config is enough; the scheduler picks up new
  settings without a restart.
- **`run-on-start`.** Catches up a missed slot once at startup, but only if it
  was missed within the last 12 hours.

## Building

```sh
docker build -f Dockerfile.warmup -t cliproxyapi:warmup .
```

`Dockerfile.warmup` is intentionally separate from the upstream `Dockerfile`.
The IANA database is embedded in the binary (`time/tzdata`), so time zones work
regardless of the base image.

## Files

All feature code is additive; the upstream touch points are three small hooks.

| Path | Purpose |
| --- | --- |
| `internal/config/config_warmup.go` | config type, parsing and validation |
| `internal/warmup/` | scheduler, slot logic, state store, request building |
| `sdk/cliproxy/service_warmup.go` | lifecycle wiring |
| `Dockerfile.warmup` | dedicated image |

Touch points in existing files (kept minimal for upstream merges):

- `internal/config/config.go` — one `Warmup` field
- `internal/config/config_v8.go` — allow the `warmup` root section
- `internal/config/parse.go` — validate on parse
- `internal/logging/global_logger.go` — 3 lines adding the warmup fields to the
  formatter's allow-list
- `internal/api/server_reload.go` — `RequestExecutor()` accessor
- `sdk/cliproxy/service.go`, `service_config.go`, `service_lifecycle.go` — start,
  reload and stop the scheduler

## Tests

```sh
go test ./internal/warmup/ ./internal/config/
```

The tests never contact an upstream; the executor is mocked.

## Rollout

The production image is kept on the host, so rollback is one line of YAML.

```sh
cd /home/dim/cli-proxy-api
cp compose.yaml compose.yaml.bak-$(date +%Y%m%d-%H%M%S)
docker compose stop cli-proxy-api
sed -i 's#image: eceasy/cli-proxy-api@sha256:.*#image: cliproxyapi:warmup#' compose.yaml
docker compose up -d
```

Verify:

```sh
curl -s http://192.168.0.61:8317/        # welcome JSON
docker logs --tail 50 cli-proxy-api      # no errors; "warmup scheduler started"
```

### Manual single-account run before committing to 06:00

`enabled: true` with an empty `schedules` list is rejected on purpose. To do the
one-account trial, set a schedule a few minutes ahead, watch the log for the
`auth_index=...` line, confirm `X-Codex-Primary-Used-Percent` moved, then set
the real schedule:

```yaml
warmup:
  enabled: true
  timezone: "Europe/Moscow"
  model: "gpt-6-luna"
  reasoning-effort: "low"
  prompt: "Reply with exactly: ok"
  accounts: ["codex-87582b03-booxdasfoland@gmail.com-team.json"]
  schedules: ["06:00"]
```

Finally disable plugins and set the real schedule; both are hot-reloaded:

```yaml
plugins:
  enabled: false
```

### Rollback

Restore the backed-up `compose.yaml` and run `docker compose up -d`.
Setting `warmup.enabled: false` alone already makes the feature inert.

## Notes and decisions

- `run-on-start` catch-up is capped at 12 hours. Without a cap, a week of
  downtime would fire a warmup a week late and shift the window away from the
  intended start.
- The cooldown check is model-scoped: a credential cooling on a different model
  is still warmed for the configured one.
- A successful HTTP response with no model output is recorded as `empty`, not
  `success`. The window did start, but the log stays honest.
- The fork is based on v8.0.36 — the version the target server runs — so a
  version upgrade stays a separate, revertible step from this feature.

