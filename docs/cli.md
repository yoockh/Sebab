# sebab command line

Every command prints help with `-h`. Secrets are never accepted as arguments:
they come from stdin, from a one-shot handoff file, or from an `env:NAME`
reference.

## sebab check

```sh
sebab check https://api.example.com/health
sebab check db.example.com:5432
sebab check --json --expect 200,204 https://api.example.com/health
sebab check --auth bearer --secret-stdin https://api.example.com/me < token.txt
sebab check --monitor payments-api          # a configured monitor
sebab check --monitor payments-api --markdown
```

Runs the layered probe (local network, VPN, DNS, TCP, TLS, HTTP) once and
prints the result. Works without a config file and without a running engine.
After a failure it checks the canary targets to decide whose side the problem
is on (`--no-canary` skips that).

Output (human):

```
https://app.example.com:8443/ok
  [ ok ] local link up, route ok
  [ ok ] dns   12 ms
  [ ok ] tcp   38 ms 192.0.2.10:8443 (ipv4)
  [FAIL] tls   TLS certificate expired 2 days ago
DOWN  TLS_EXPIRED  (side: theirs)
Hint: Renew the certificate on the server. DNS and TCP are fine.
total 51 ms
```

`--json` prints one `check` event (schema v1, see `event-schema.md`).

### Exit codes

| Code | Meaning |
|---|---|
| 0 | up |
| 1 | degraded (slow, rate limited, certificate expiring soon) |
| 2 | usage error (bad flags, bad target) |
| 3 | internal error |
| 10 | local network failure (`LOCAL_*`) |
| 11 | VPN precondition failed (`VPN_DOWN`) |
| 12 | DNS failure (`DNS_*`) |
| 13 | TCP failure (`TCP_*`) |
| 14 | TLS failure (`TLS_*`) |
| 15 | HTTP failure (`HTTP_*`) |

## sebab monitor

```sh
sebab monitor add --name "Payments API" --target https://api.example.com/health
sebab monitor add --name "Admin API" --target https://admin.example.com/ping \
    --auth bearer --secret admin-api-token --vpn wg0
sebab monitor add --name DB --target db.example.com:5432 --interval 2m
sebab monitor list [--json]
sebab monitor show ID [--raw]
sebab monitor edit ID --interval 5m --expect 200
sebab monitor pause ID
sebab monitor resume ID
sebab monitor rm ID
sebab monitor mute ID        # keep checking and showing it, never notify
sebab monitor unmute ID
```

Quiet hours: `[defaults] quiet_hours = "22:00-07:00"` (local time, may wrap midnight) suppresses every
notification in that window; states are still tracked and shown.

`add` and `edit` run a test check first and print it (skip with `--no-test`).
Validation errors are specific ("interval below 30s", "missing port"). With
`--json` the result is `{"ok":true,"id":"..."}` or `{"ok":false,"errors":[...]}`.

Auth flags: `--auth bearer|header|basic`, `--header-name X-API-Key`,
`--username NAME`, and the secret as `--secret NAME` (a stored secret or
`env:VARIABLE`), `--secret-stdin`, or `--secret-file PATH` (a handoff file).
A value given with `--secret-stdin` or `--secret-file` is stored under the
reference name (default `<id>-token`) in the keyring, or in the 0600 secrets
file when no keyring is running.

Assertions (repeatable `--assert`, Phase 3) check the response content after the status code:
`body=TEXT` (body contains TEXT), `json:PATH=VALUE` (for example `json:data.items[0].state=ready`;
omit `=VALUE` to only require the path), `header:NAME=VALUE` (header contains VALUE). Prefix `!` to
negate (`--assert '!body=Maintenance'`), `--assert none` clears them. A failing assertion is
`HTTP_ASSERTION_FAILED`, which also catches captive portals that answer 200 with a login page.

Other flags: `--interval`, `--timeout`, `--slow` (HTTP_SLOW threshold),
`--expect`, `--vpn IFACE`, `--ca-file PEM`, `--insecure`, `--method GET|HEAD`,
`--no-follow`, `--allow-redirect host1,host2`, `--paused`.

## sebab secret

```sh
printf %s "$TOKEN" | sebab secret set admin-api-token   # value from stdin
sebab secret set admin-api-token --backend file < token.txt
sebab secret list            # names, backend and status; never values
sebab secret rm admin-api-token
sebab secret handoff         # prints a fresh 0600 handoff file path (used by the panel)
```

## sebab watch

```sh
sebab watch --format jsonl
```

Streams events as JSON lines. Attaches to a running engine (a `sebab daemon`
or another `watch`) through `$XDG_RUNTIME_DIR/sebab/sebab.sock`; otherwise it
runs an embedded engine that serves that socket. Config edits are applied
live. `--embedded` never attaches. `--session TOKEN` lets
`sebab kill-session TOKEN` stop this process (used by frontends to restart a
stalled stream).

## sebab daemon

Runs the engine without a frontend: it serves the socket and sends desktop
notifications itself unless a frontend that displays notifications is
attached. See `packaging/systemd/sebab.service`.

## sebab waybar

Prints one Waybar JSON object per state change. See `frontends/waybar`.

## sebab history

```sh
sebab history payments-api            # last 24 h: availability, p50/p95, sparkline, status changes
sebab history --since 168h            # all monitors, last week
sebab history payments-api --json
```

History lives in `$XDG_STATE_HOME/sebab/history` (0600 files, pruned after
`[history] keep_days`, default 7; disable with `[history] disabled = true`).
`sebab report` appends the last 24 h of status changes and stats, which makes
it an incident report you can paste into a bug ticket.

## sebab overview

```sh
sebab overview --json                 # last 24 h, 24 hourly segments, 50 incidents
sebab overview --since 168h --buckets 28 --incidents 20
```

Prints one JSON object built from the local history (no engine needed), used by
the Noctalia panel's dashboard:

```json
{"v":1,"since_s":86400,"buckets":24,"checks":96,"uptime":0.979,
 "monitors":{"payments-api":{"checks":24,"uptime":0.958,"strip":["up","up","down","",...]}},
 "incidents":[{"t":1791800000000,"m":"payments-api","n":"Payments API","from":"up","to":"down",
               "c":"TLS_EXPIRED","sum":"TLS certificate expired 2 days ago","side":"theirs"}]}
```

`uptime` is the share of up or degraded checks among judged ones (null without
data); each strip segment is the worst status seen in that time slice, `""` when
there was no check; incidents are newest first.

## sebab notify-test

Sends one test notification to the desktop (`org.freedesktop.Notifications`)
and to every `[[notify]]` channel (webhook, ntfy, Telegram; see `config.md`),
and prints the result of each. Exit code 1 when any channel failed.

## sebab tui

Interactive terminal view of every monitor (Phase 4): status, latency, last
check and cause; Enter shows hint, side, timings, "down since" and a latency
sparkline. Keys: up/down or j/k, Enter, r (check now), q. Attaches to a running
engine or starts an embedded one. `NO_COLOR=1` disables colours.

## sebab refresh / report / doctor

```sh
sebab refresh [ID]        # ask the running engine to check now
sebab report ID [--fresh] # redacted Markdown diagnosis (Copy diagnosis)
sebab doctor [--json] [--online]
```

`doctor` checks the config, the Secret Service keyring, stored secrets, the
notification daemon, network link and route, VPN interfaces, unprivileged ICMP
(`net.ipv4.ping_group_range`), proxy variables, the engine socket and installed
frontends, with fix hints for Arch, Debian/Ubuntu, Fedora and openSUSE.
`--online` also connects to the canary targets.

## Environment

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` | config at `$XDG_CONFIG_HOME/sebab/config.toml` |
| `XDG_RUNTIME_DIR` | control socket and handoff files under `$XDG_RUNTIME_DIR/sebab` |
| `SEBAB_CONFIG` | use this config file instead |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | respected by HTTP checks |
| `SEBAB_LAB_DNS` | failure lab only: send DNS queries to this address |
