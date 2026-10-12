# Event schema v1

One JSON object per line, on `sebab watch` stdout and on the engine socket.
Every event has `v` (always 1), `type` and `ts` (RFC 3339 UTC, milliseconds).

Compatibility rules: frontends **must ignore unknown fields and unknown `type`
values**. New fields and types are additive; removing or renaming a field bumps
`v`. Golden files for every type live in `internal/event/testdata`.

## Types

### hello

First event of every stream.

```json
{"v":1,"type":"hello","ts":"...","engine":"0.1.0","monitors":7,"heartbeat_ms":15000,"source":"embedded"}
```

`source` is `embedded` (started by `sebab watch`) or `daemon`.

### monitors

The configured monitors, after `hello` and after every config reload. Targets
are redacted (`api_key=[REDACTED]`); auth shows only the type.

```json
{"v":1,"type":"monitors","ts":"...","monitors":[{"id":"staging-db","name":"Staging DB","kind":"tcp","target":"tcp://db.staging.example:5432","interval_s":60,"paused":false}]}
```

### check

The state of one monitor after a check.

```json
{"v":1,"type":"check","ts":"...",
 "monitor":{"id":"payments-api","name":"Payments API","kind":"http","target":"https://api.example.com/health"},
 "status":"down","observed":"down","side":"theirs",
 "cause":{"layer":"tls","code":"TLS_EXPIRED","summary":"TLS certificate expired 2 days ago","hint":"Renew the certificate on the server. DNS and TCP are fine."},
 "timings_ms":{"dns":12,"tcp":38,"tls":null,"ttfb":null,"total":51},
 "since":"...","notify":true,
 "notification":{"id":"payments-api:down:1760187660000","title":"Payments API is down","body":"TLS certificate expired 2 days ago. Renew the certificate on the server. DNS and TCP are fine.","urgency":"critical"}}
```

- `status`: flap-controlled state (`up`, `degraded`, `down`, `unknown`, `paused`).
- `observed`: the raw result of this check (it can differ from `status` while a
  change waits for `fail_threshold` / `recover_threshold`).
- `side`: `yours`, `theirs` or `unknown`; absent when healthy.
- `cause`: absent when healthy. `code` is one of the codes below.
- `timings_ms`: a step that did not complete is `null`.
- `details` (optional): `http_status`, `address`, `family`, `tls_not_after`, `retry_after_s`.
- `monitor.insecure_tls`: true when TLS verification is disabled.
- `recent_ms` (optional): the latest total check times in ms, oldest first; `-1` marks a failed check. Used for
  sparklines; present when local history is enabled.
- `notify`: the engine decided this event should produce a notification. Show
  `notification` and dedupe by `notification.id`.

### summary

```json
{"v":1,"type":"summary","ts":"...","up":5,"degraded":1,"down":1,"unknown":0,"paused":0,"offline":false}
```

`offline_cause` is set when `offline` is true.

### notice

Engine-level messages: `offline`, `online`, `vpn_down`, `vpn_up`, `resumed`
(suspend detected), `config_reloaded`. Same `notify` / `notification` rules.

### heartbeat

Sent every `heartbeat_ms`. A frontend that sees none for 3 intervals reports
the engine as stalled.

### error

```json
{"v":1,"type":"error","ts":"...","code":"CONFIG_INVALID","message":"monitor 'x': interval below 30s"}
```

Codes: `CONFIG_INVALID`, `CONFIG_MISSING`, `SECRET_MISSING` (with `monitor`), `INTERNAL`.

## Cause codes

| Layer | Code | Status | Default side |
|---|---|---|---|
| local | `LOCAL_NO_LINK`, `LOCAL_NO_ROUTE`, `LOCAL_OFFLINE`, `LOCAL_DNS_BROKEN` | unknown | yours |
| vpn | `VPN_DOWN` | paused | yours |
| dns | `DNS_NXDOMAIN`, `DNS_SERVFAIL`, `DNS_NO_ADDRESS` | down | theirs |
| dns | `DNS_TIMEOUT` | down | unknown (theirs when other names resolve) |
| tcp | `TCP_REFUSED`, `TCP_TIMEOUT` | down | theirs |
| tcp | `TCP_UNREACHABLE` | down | unknown |
| tls | `TLS_EXPIRED`, `TLS_HOSTNAME_MISMATCH`, `TLS_UNTRUSTED`, `TLS_HANDSHAKE_FAILED` | down | theirs |
| tls | `TLS_NOT_YET_VALID` | down | unknown (check the local clock) |
| tls | `TLS_EXPIRING_SOON` | degraded | theirs |
| http | `HTTP_BAD_GATEWAY`, `HTTP_UNAVAILABLE`, `HTTP_SERVER_ERROR`, `HTTP_CLIENT_ERROR`, `HTTP_TIMEOUT`, `HTTP_UNEXPECTED_STATUS`, `HTTP_UNEXPECTED_REDIRECT`, `HTTP_REDIRECT_LOOP` | down | theirs |
| http | `HTTP_AUTH` | down | yours (token) |
| http | `HTTP_RATE_LIMITED`, `HTTP_SLOW` | degraded | theirs |

With the canary disabled, DNS and TCP failures report side `unknown`.

## Socket protocol

Connect to `$XDG_RUNTIME_DIR/sebab/sebab.sock` and send one JSON line:

| Request | Response |
|---|---|
| `{"cmd":"subscribe"}` | snapshot (hello, monitors, latest checks, summary; never `notify`) then live events |
| `{"cmd":"check","monitor":"id"}` | `{"ok":true}` (empty id: all monitors) |
| `{"cmd":"reload"}` | `{"ok":true}` or `{"ok":false,"error":"..."}` |
| `{"cmd":"report","monitor":"id"}` | `{"ok":true,"markdown":"..."}` |
| `{"cmd":"status"}` | `{"ok":true,"engine":"0.1.0","source":"daemon","pid":123}` |
