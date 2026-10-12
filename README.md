# Sebab

> Don't just know it's down. Know why.

Sebab ("cause" in Indonesian) is a local endpoint monitor for Linux desktops. A small Go engine checks your HTTP(S)
and TCP endpoints layer by layer (local network, VPN, DNS, TCP, TLS, HTTP), names the layer that failed, and tells
you whether the problem is on your side or theirs:

> **Payments API is down:** TLS certificate expired 2 days ago. Renew the certificate on the server. DNS and TCP
> are fine.

Results show up in your bar (Noctalia plugin, Waybar module), as desktop notifications, and in the terminal.

```
$ sebab check https://app.example.com/health
https://app.example.com/health
  [ ok ] local link up, route ok
  [ ok ] dns   12 ms
  [ ok ] tcp   38 ms 192.0.2.10:443 (ipv4)
  [FAIL] tls   TLS certificate expired 2 days ago
DOWN  TLS_EXPIRED  (side: theirs)
Hint: Renew the certificate on the server. DNS and TCP are fine.
total 51 ms
```

## What it does

- **Layered diagnosis** with 30 cause codes (`DNS_NXDOMAIN`, `TCP_REFUSED`, `TLS_EXPIRED`, `HTTP_BAD_GATEWAY`,
  `HTTP_AUTH`, ...), a plain-language summary, a hint and a per-step timing breakdown.
- **Your side vs. their side:** after a failure Sebab checks an internet canary. If your own connection is down
  every monitor goes `unknown` and you get exactly one "You are offline" notification instead of an alert storm.
- **Flap control:** alert after N consecutive failures, recover after M successes, one notification per incident
  and one more only when the cause changes.
- **Authenticated checks** (bearer, custom header, basic) with secrets in the Secret Service keyring. Secrets never
  appear in config, events, logs, notifications, process arguments or copied diagnoses (tested).
- **VPN preconditions:** monitors that need `wg0` pause (`VPN_DOWN`) while it is down. Sebab never touches the VPN.
- **TLS expiry warnings**, Retry-After backoff for 429, suspend/resume handling (logind and wall-clock jumps).
- **History** with sparklines and Markdown incident reports for bug tickets; **assertions** on JSON, body and
  headers (catches captive portals); **quiet hours** and per-monitor mute; **webhook, ntfy and Telegram** channels.
- **One engine, many frontends:** `sebab daemon` (systemd user service) or the engine embedded in the first
  frontend serves a Unix socket; Noctalia, Waybar and the CLI share it and nothing is probed twice.

## Install

- Arch: `sebab-bin` / `sebab-git` from the AUR (PKGBUILDs in `packaging/aur`), or build from source.
- Debian, Ubuntu, Fedora: `.deb` / `.rpm` from the releases (built with nfpm, see `packaging/nfpm`).
- From source: `make build` (Go 1.26+) puts a static binary at `bin/sebab`.

Then:

```sh
sebab doctor                                    # what is missing (keyring, notifications, ...)
sebab monitor add --name "Web" --target https://example.com/health
sebab watch                                     # JSON event stream
systemctl --user enable --now sebab.service     # optional shared daemon
```

Frontends:

- **Noctalia:** the `yoockh/sebab` plugin in `frontends/noctalia/sebab` (bar widget, panel, notifications).
- **Waybar:** `frontends/waybar` (`sebab waybar` with `return-type: json`).
- **Terminal:** `sebab tui`.

## Documentation

- [`docs/cli.md`](docs/cli.md): every command, flags and exit codes
- [`docs/config.md`](docs/config.md): `config.toml` reference
- [`docs/event-schema.md`](docs/event-schema.md): event stream v1, cause codes, socket protocol
- [`docs/security.md`](docs/security.md): secret handling review and threat model
- [`TESTING.md`](TESTING.md): build, install, manual test checklist, failure lab
- [`DECISIONS.md`](DECISIONS.md): design decisions and their reasons
- [`PROGRESS.md`](PROGRESS.md): what is done, per PRD story

## Privacy and security

No telemetry. The engine contacts only the endpoints you configure, the canary targets (configurable, or disabled
with `canary = []`) after a failure, and the notification channels you add. It runs as your user and never needs
root. Release checksums are GPG-signed.

## Development

```sh
make check          # gofmt, go vet, go test -race ./...
make lab            # bin/sebab-lab: the failure lab for manual sessions
make plugin-test    # Noctalia plugin scripts in a real Luau VM (needs luau or SEBAB_LUAU)
```

## License

MIT, see `LICENSE`.
