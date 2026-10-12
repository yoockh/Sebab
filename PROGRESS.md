# Sebab build progress

## Summary (end of the autonomous build, 2026-10-12)

Phases 0, 1 and 2 are done and green; the Phase 3 items you listed are done; Phase 4's TUI is done; from Phase 5,
the secret-handling review (`docs/security.md`, which found and fixed URL credentials echoed in validation errors)
and a reproducible-build check are done. `make check` (gofmt, vet, `go test -race ./...`) passes, the plugin
scripts pass in a real Luau VM, `noctalia plugins lint` passes, and the community-plugins validator reports only the
missing thumbnail. Nothing was pushed. Start with TESTING.md; what needs you is listed under "Needs human".

Phase 5 status: event/config schema v1 is documented and versioned but not frozen (no migration needed yet);
docs site, Indonesian translation (Noctalia Translate), signed releases and 1.0 need a human.

This file is the resume point for any future session. Status values: `todo`, `wip`, `done`, `blocked`, `needs human`.
A story's acceptance criteria are ticked only when a test or a verified manual run proves them (the proof is named).

## Panel UX and reliability pass (owner feedback, 2026-10-12)

| Item | Status | Proof | Commit |
|---|---|---|---|
| 7. Save does not persist (root cause D23) | done | `panel_save_test.luau` (failed before the fix), cli TestPanelSavePathPersistsAcrossDaemonRestart, real shell: saved via panel, file 0600, survived plugin disable/enable | a2d4098 |
| 1. Layout: pinned header/footer, no dead space (D26) | done | `panel_layout_test.luau`, grim screenshots of list and form | 21fdc22 |
| 2. Result card always visible (D27) | done | `panel_card_test.luau`, grim screenshots of placeholder, result and error | d5b4f5f |
| 3. Save flow: back to list, flash row, clear form (D28) | done | `panel_save_test.luau` (highlight, expiry, cancel), grim screenshot | a277fc6 |
| 4. Dismiss on outside click, wipe secrets on close (D29) | done; the click itself needs a human | `panel_close_test.luau` (token gone from files, argv and form) | 4cba03a |
| 5. Help view and field hints (D30) | done | `panel_help_test.luau`, grim screenshot | b613df3 |
| 6. Overview card, strips, sparklines, incidents tab (D31) | done | `panel_dashboard_test.luau`, history TestOverview, cli TestOverviewCommand, grim screenshots | 2367706 |

## Phase 0: foundations

| Item | Status | Commit |
|---|---|---|
| Repo skeleton, go.mod, Makefile, CI, README, LICENSE, DECISIONS, PROGRESS, TESTING | done | aa088ba |
| Event schema v1 as Go types + JSON golden tests | done | 17b01fc |
| Spike: `sebab watch --format jsonl` hello + heartbeat | done | a1efdf8 |
| Spike: Noctalia plugin skeleton shows a live value in the bar | done; bar placement needs human (see Needs human) | 17b46a1 |

## Phase 1: MVP

| Item | Status | Commit |
|---|---|---|
| Config (TOML, validation, XDG paths, atomic writes) | done | 0372380 |
| Probes: local, VPN, DNS, TCP, TLS, HTTP (timings, injectable clock/dialer) | done | 6d7cd09, abd955c |
| Diagnosis: every Appendix B cause code, side detection with canary, hints | done | 44ff2f8 |
| Engine: scheduler, state machine, flap control, notify, heartbeat, offline, suspend guard | done | 64d0c8c |
| Auth: bearer/header/basic, keyring, `sebab secret set/rm` via stdin, redaction | done | 748da64, 334aa49 |
| CLI: check, monitor add/list/edit/rm/pause, watch, doctor | done | 334aa49, c5f0f9e |
| Failure lab: every Phase 1 cause code, one integration test each | done | 65d09a4 |
| Noctalia plugin: service, bar widget, panel | done | 83b0bc9 |
| Packaging: AUR PKGBUILDs, goreleaser | done (not published) | eda4f11 |
| Load plugin into local Noctalia as a dev path source and verify | done | 83b0bc9 |

Plugin verification on the maintainer machine (Noctalia 5.2.1): `noctalia msg plugins source add dev path
<repo>/frontends/noctalia`, `noctalia msg plugins enable yoockh/sebab`; log shows "loaded plugin 'yoockh/sebab'
(3 entries)", the service runs `bin/sebab watch --session ...`, `noctalia msg plugin yoockh/sebab:engine all dump`
shows `engine=running` and the `CONFIG_MISSING` hint, `noctalia msg panel-open yoockh/sebab:panel add` renders the
list and the add form with no Luau warnings. `noctalia plugins lint` and the community-plugins validator pass
(except the missing thumbnail).

### Story acceptance criteria

Story 1: Add an HTTP(S) or TCP monitor (Must)
- [x] Panel "Add" form accepts name, target, interval, expected status codes (frontends/noctalia panel_test)
- [x] Invalid input rejected with a specific message (panel_test: form.err_port/err_interval; cli TestMonitorCRUD)
- [x] Immediate test check runs and result shown before saving (panel_test "Test, then save"; `monitor add` prints "Test check:")
- [x] Written to config.toml and appears in all frontends without restarting (cli TestWatchRunsEngineServesSocketAndReloads)
- [x] CLI `sebab monitor add --name ... --target ...` (cli TestMonitorCRUD)

Story 2: Layered diagnosis (Must)
- [x] Probes run in order and stop at first failure (lab tests: later timings are null after the failing step)
- [x] Every failure maps to exactly one cause code with summary and hint (diagnose TestEveryCodeHasSummaryAndHint)
- [x] Each completed step's timing included in the event (lab TestLabTCPRefused, TestLabTLSCodes; golden check event)
- [x] Every Phase 1 cause code reproduced by the failure lab with an automated test (lab/integration_test.go)

Story 3: Your side vs their side (Must)
- [x] Canary checked on failure before alerting (engine observe; lab env.check)
- [x] Canary failure or no default route: all monitors UNKNOWN, exactly one notification (engine TestLocalOutageOneNotification, TestNoRouteGoesOfflineDirectly)
- [x] Local recovery: one "back online" notification, monitors resume (engine TestLocalOutageOneNotification)
- [x] Canary configurable and disable-able, side reported as unknown (config TestCanaryDefaultsAndDisable, engine TestCanaryDisabledReportsUnknownSide)

Story 4: Notifications with cause (Must)
- [x] Down notification only after fail_threshold consecutive failures (engine TestFlapControlAndRecovery)
- [x] Title names the monitor, body has cause summary (same)
- [x] Recovery after recover_threshold successes, includes downtime (same: "3 minutes")
- [x] No repeats while state and cause stay the same; cause change triggers one new notification (engine TestCauseChangeNotifiesOnce; plugin service_test dedupe)

Story 5: Bar widget (Must)
- [x] Aggregate state: all up, degraded, down count, offline, engine missing, engine stalled (plugin widget_test)
- [x] Tooltip lists non-UP monitors with short cause (widget_test)
- [x] Left click opens panel; right click forces immediate check (widget_test; cli `refresh` test)
- [x] Setup hint when binary missing (widget_test, service_missing_test)
- [x] "engine stalled" after 3 heartbeat intervals without heartbeat (service_test stall section)

Story 6: Panel details (Must)
- [x] Row per monitor: name, status pill, latency, last check (panel_test)
- [x] Expanded failed row: cause, hint, side, timings, down since (panel_test)
- [x] Edit, delete (confirm), pause, resume (panel_test)

Story 7: Authenticated checks (Must)
- [x] Bearer, custom header, basic auth (lab TestLabAuthSuccess; cli TestCheckAuthSecretSources)
- [x] Masked token field (panel_test asserts password = true)
- [x] Secret in Secret Service keyring, config holds reference only (cli TestPanelFlowWithHandoffFile)
- [x] Secret never in events, logs, notifications, copied diagnoses (cli TestSecretNeverLeaksThroughStream, assertNoLeak in CLI tests; panel_test argv check; secret TestRedactorForms)
- [x] No keyring: user told and offered env-var or 0600 file fallback (cli TestSecretCommandsAndFileFallback, TestDoctorNoKeyringOnArch)
- [x] 401/403 reported as HTTP_AUTH with token hint (lab TestLabHTTPCodes; cli TestCheckAuthSecretSources)

Story 8: VPN precondition (Must)
- [x] `requires_vpn` by interface name (config; lab TestLabVPNDown)
- [x] Interface missing/down: PAUSED with VPN_DOWN, one notification per VPN (engine TestVPNDownPausesAndResumes)
- [x] Interface returns: monitors resume (same)
- [x] Sebab never starts/stops/configures the VPN (design; no code path touches VPNs, D6)

Story 9: One-shot CLI check (Must)
- [x] Readable layered output with timings; `--json` prints the event (cli TestCheckHumanOutput, TestCheckExitCodesPerLayer)
- [x] Exit 0 for up, distinct non-zero per failing layer (documented in docs/cli.md) (cli TestCheckExitCodesPerLayer)
- [x] Works without config or running engine (cli TestCheckWorksWithoutConfig)

Story 10: TLS expiry warning (Should)
- [x] DEGRADED when leaf expires within tls_warn_days (default 14) (lab TestLabTLSCodes TLS_EXPIRING_SOON)
- [x] At most one expiry notification per monitor per day (engine TestTLSExpiryWarningOncePerDay)

Story 11: Copy diagnosis (Should)
- [x] Panel button copies redacted Markdown (panel_test copy; cli `report` tests)
- [x] Redaction covered by secret-safety tests (cli TestPanelFlowWithHandoffFile, TestSecretNeverLeaksThroughStream)

Story 12: Install and setup (Must)
- [ ] AUR sebab-bin from signed release with checksums: PKGBUILD + checksum updater written, signed release and AUR publication need human
- [x] Plugin declares `sebab` dependency and documents network calls, file writes, processes (frontends/noctalia/sebab/README.md)
- [x] `sebab doctor` reports keyring, notification daemon, ICMP permission (cli doctor tests; real run on Arch)

## Phase 2

| Item | Status | Commit |
|---|---|---|
| Daemon + Unix socket, `watch` attach or embedded fallback, systemd user unit | done | e123510, 22ad46d |
| Waybar module, example config and CSS | done | 599e3a0 |
| D-Bus notifications with frontend dedupe | done | e123510 |
| logind PrepareForSleep handling | done | 3039143, e123510 |
| nfpm .deb/.rpm config (build only) | done; installs verified in debian:12, ubuntu:24.04, fedora:42 | c9f1548 |
| `sebab doctor` distro differences | done | c5f0f9e, 22ad46d |

## Phase 3 and later

| Item | Status | Commit |
|---|---|---|
| History + sparklines | done (JSONL, D22) | 012909b |
| Incident report export | done (`sebab report` adds the 24 h timeline) | 012909b |
| Assertions (JSON path, body keyword, header) | done (`HTTP_ASSERTION_FAILED`) | 3a9b6cb |
| Quiet hours, per-monitor mute | done | c9541e0 |
| Webhook / ntfy / Telegram | done (`sebab notify-test`) | ae29cab |
| Opt-in Globalping "down for everyone" check | not done: not in the requested Phase 3 list, contacts a third party | |
| Phase 4: TUI (`sebab tui`) | done (smoke-tested under a pty) | e09a432 |
| Phase 4: GNOME / KDE frontends | not planned here (PRD: author or contributors) | |

## Needs human

- Panel outside-click dismissal: click outside the open panel once to confirm it closes (TESTING.md 5b item 4).
- Restarting Noctalia itself with a saved monitor (plugin disable/enable and engine restarts were verified).
- Phase 1 exit: 30 days of dogfooding with >= 90% diagnosis accuracy.
- Phase 3 exit: file a real bug report with an exported incident report.
- Phase 2 exit: catalog submission to noctalia-dev/community-plugins; "same engine drives Noctalia and Waybar at
  the same time" on the real desktop (covered by tests: two frontends attached to one engine).
- Plugin `thumbnail.webp` (960x540) must be generated with the Noctalia thumbnail generator from a real screenshot
  (catalog CI requires it; the validator reports only this).
- Signed releases: GPG key, `GPG_FINGERPRINT`, tagging and publishing GitHub releases; publishing the AUR packages.

## Blocked

(none)

## Next up

Remaining PRD items are Phase 5 (schema freeze and migrations, docs site, i18n, security review, reproducible
and signed releases); most need a human. Handoff docs (TESTING.md, README.md, docs/) are current.
