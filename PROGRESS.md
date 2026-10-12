# Sebab build progress

This file is the resume point for any future session. Status values: `todo`, `wip`, `done`, `blocked`, `needs human`.
A story's acceptance criteria are ticked only when a test or a verified manual run proves them.

## Phase 0: foundations

| Item | Status | Commit |
|---|---|---|
| Repo skeleton, go.mod, Makefile, CI, README, LICENSE, DECISIONS, PROGRESS, TESTING | done | aa088ba |
| Event schema v1 as Go types + JSON golden tests | done | 17b01fc |
| Spike: `sebab watch --format jsonl` hello + heartbeat | done | a1efdf8 |
| Spike: Noctalia plugin skeleton shows a live value in the bar | done (bar placement needs human) | 17b46a1 |

## Phase 1: MVP

| Item | Status | Commit |
|---|---|---|
| Config (TOML, validation, XDG paths, atomic writes) | done | 0372380 |
| Probes: local, VPN, DNS, TCP, TLS, HTTP (timings, injectable clock/dialer) | done | 6d7cd09 |
| Diagnosis: every Appendix B cause code, side detection with canary, hints | done | 44ff2f8 |
| Engine: scheduler, state machine, flap control, notify, heartbeat, offline, suspend guard | done | 64d0c8c |
| Auth: bearer/header/basic, keyring, `sebab secret set/rm` via stdin, redaction | wip (store+redaction 748da64, CLI pending) | |
| CLI: check, monitor add/list/edit/rm/pause, watch, doctor | todo | |
| Failure lab: every Phase 1 cause code, one integration test each | done | 65d09a4 |
| Noctalia plugin: service, bar widget, panel | todo | |
| Packaging: AUR PKGBUILDs, goreleaser | todo | |
| Load plugin into local Noctalia as a dev path source and verify | todo | |

### Story acceptance criteria

Story 1: Add an HTTP(S) or TCP monitor (Must)
- [ ] Panel "Add" form accepts name, target, interval, expected status codes
- [ ] Invalid input rejected with a specific message
- [ ] Immediate test check runs and result shown before saving
- [ ] Written to config.toml and appears in all frontends without restarting
- [ ] CLI `sebab monitor add --name ... --target ...`

Story 2: Layered diagnosis (Must)
- [ ] Probes run in order and stop at first failure
- [ ] Every failure maps to exactly one cause code with summary and hint
- [ ] Each completed step's timing included in the event
- [ ] Every Phase 1 cause code reproduced by the failure lab with an automated test

Story 3: Your side vs their side (Must)
- [ ] Canary checked on failure before alerting
- [ ] Canary failure or no default route: all monitors UNKNOWN, exactly one notification
- [ ] Local recovery: one "back online" notification, monitors resume
- [ ] Canary configurable and disable-able (side reported as unknown)

Story 4: Notifications with cause (Must)
- [ ] Down notification only after fail_threshold consecutive failures
- [ ] Title names the monitor, body has cause summary
- [ ] Recovery after recover_threshold successes, includes downtime
- [ ] No repeats while state and cause stay the same; cause change triggers one new notification

Story 5: Bar widget (Must)
- [ ] Aggregate state: all up, degraded, down count, offline, engine missing, engine stalled
- [ ] Tooltip lists non-UP monitors with short cause
- [ ] Left click opens panel; right click forces immediate check
- [ ] Setup hint when binary missing
- [ ] "engine stalled" after 3 heartbeat intervals without heartbeat

Story 6: Panel details (Must)
- [ ] Row per monitor: name, status pill, latency, last check
- [ ] Expanded failed row: cause, hint, side, timings, down since
- [ ] Edit, delete (confirm), pause, resume

Story 7: Authenticated checks (Must)
- [ ] Bearer, custom header, basic auth
- [ ] Masked token field
- [ ] Secret in Secret Service keyring, config holds reference only
- [ ] Secret never in events, logs, notifications, copied diagnoses (automated test)
- [ ] No keyring: user told and offered env-var or 0600 file fallback
- [ ] 401/403 reported as HTTP_AUTH with token hint

Story 8: VPN precondition (Must)
- [ ] `requires_vpn` by interface name
- [ ] Interface missing/down: PAUSED with VPN_DOWN, one notification per VPN
- [ ] Interface returns: monitors resume
- [ ] Sebab never starts/stops/configures the VPN

Story 9: One-shot CLI check (Must)
- [ ] Readable layered output with timings; `--json` prints the event
- [ ] Exit 0 for up, distinct non-zero per failing layer (documented)
- [ ] Works without config or running engine

Story 10: TLS expiry warning (Should)
- [ ] DEGRADED when leaf expires within tls_warn_days (default 14)
- [ ] At most one expiry notification per monitor per day

Story 11: Copy diagnosis (Should)
- [ ] Panel button copies redacted Markdown
- [ ] Redaction covered by secret-safety tests

Story 12: Install and setup (Must)
- [ ] AUR sebab-bin from signed release with checksums (PKGBUILD written, not published)
- [ ] Plugin declares `sebab` dependency and documents network calls, file writes, processes
- [ ] `sebab doctor` reports keyring, notification daemon, ICMP permission (Should)

## Phase 2

| Item | Status | Commit |
|---|---|---|
| Daemon + Unix socket, `watch` attach or embedded fallback, systemd user unit | todo | |
| Waybar module, example config and CSS | todo | |
| D-Bus notifications with frontend dedupe | todo | |
| logind PrepareForSleep handling | todo | |
| nfpm .deb/.rpm config (build only) | todo | |
| `sebab doctor` distro differences | todo | |

## Phase 3 and later

| Item | Status | Commit |
|---|---|---|
| History + sparklines | todo | |
| Incident report export | todo | |
| Assertions (JSON path, body keyword, header) | todo | |
| Quiet hours, per-monitor mute | todo | |
| Webhook / ntfy / Telegram | todo | |

## Needs human

- Phase 1 exit: 30 days of dogfooding with >= 90% diagnosis accuracy.
- Phase 2 exit: catalog submission to noctalia-dev/community-plugins; package install checks in real Debian/Ubuntu containers by the maintainer.
- Plugin `thumbnail.webp` (960x540) must be generated with the Noctalia thumbnail generator from a real screenshot.
- Signed releases: signing key and GitHub release publishing.

## Blocked

(none)

## Next up

CLI commands (check, monitor, secret, watch with control socket, doctor, report), then the full Noctalia plugin.
