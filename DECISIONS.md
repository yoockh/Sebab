# Decisions

Each entry: what was decided, why, and the alternatives considered. Newest at the bottom.

## D1. Name, license, module path

- **What:** Name "Sebab", MIT license, module `github.com/yoockh/sebab`.
- **Why:** Defaults given by the maintainer for PRD Open Questions 1 and 7. MIT matches the Noctalia catalog default.
- **Alternatives:** Apache-2.0 (patent grant, but longer and not the catalog default).

## D2. Noctalia plugin API level pinned to 32

- **What:** `plugin_api = 32` in `plugin.toml`.
- **Why:** The installed Noctalia is v5.2.1, whose `docs/plugin-api.json` tops out at level 32. The upstream
  `noctalia.d.luau` on `main` already declares level 33 (`revealKey` on `ui.scroll`), which v5.2.1 cannot load.
  Sebab uses argv `runAsync` (24), `require` (22), closures (9), `dismiss_on_outside_click` (8) and container
  tooltips (32), so 32 is the oldest level that covers everything.
- **Alternatives:** 33 (would refuse to enable on the maintainer's machine); a lower level without container tooltips.

## D3. Plugin directory layout: `frontends/noctalia/sebab/`

- **What:** The plugin lives in `frontends/noctalia/sebab/` (not directly in `frontends/noctalia/`), and the dev path
  source points at `frontends/noctalia`.
- **Why:** Noctalia source repos hold one plugin per subdirectory named after the id suffix (`yoockh/sebab` lives in
  `sebab/`), and the community catalog CI enforces it. A path source is scanned the same way. Mirroring that layout
  means the directory can be copied verbatim into a community-plugins PR.
- **Alternatives:** Appendix C's flat `frontends/noctalia/plugin.toml` (does not match how sources are scanned).

## D4. Secret handoff from the plugin: one-shot 0600 file, never argv

- **What:** The panel asks the engine for a handoff file (`sebab secret handoff`). The engine creates a private 0700
  directory and an empty 0600 file inside it and prints the path. The panel writes the token into that file with
  `noctalia.writeFile` (truncating an existing file keeps its 0600 mode), then calls a `sebab` command with
  `--secret-file <path>`. The engine verifies owner, mode and location, reads the secret, and deletes the file. Stale
  handoff files older than 10 minutes are removed by every `sebab secret handoff` call.
  The directory is `$XDG_RUNTIME_DIR/sebab/handoff` when available (tmpfs, never written to disk), otherwise the
  directory passed by the plugin (its `pluginDataDir()`).
- **Why:** Verified in Noctalia v5.2.1 source (`src/scripting/luau_host.cpp`, `src/core/process.cpp`):
  `runAsync`/`runStream` connect the child's stdin to `/dev/null`, so stdin handoff is impossible. `writeFile` is a
  plain `std::ofstream`, which would create files with the umask mode (usually 0644), so the engine must create the
  file first. Preferring the runtime dir keeps the secret off persistent storage even if the panel crashes mid-flow.
- **Alternatives:** stdin (not supported by the host); argv or environment (visible in `/proc`); the plugin data dir
  only (persistent disk; kept as the fallback).

## D5. Secret Service availability on the maintainer machine

- **What:** No Secret Service provider is running on the maintainer's Arch + Hyprland session (`busctl --user list`
  shows no `org.freedesktop.secrets`). Sebab therefore treats the fallback path as a first-class flow: `sebab secret
  set` reports which backend stored the secret, `sebab doctor` explains how to install gnome-keyring or KeePassXC,
  and the secrets file fallback (`$XDG_CONFIG_HOME/sebab/secrets.toml`, 0600) prints a visible warning.
- **Why:** PRD Open Question 4, answered by inspection.

## D6. VPN precondition is detection only

- **What:** Sebab checks that a declared interface exists and is up (`IFF_UP` and `IFF_RUNNING`). It never connects,
  configures or switches VPNs. Interfaces are read through Go's `net.Interfaces`, which on Linux is a netlink
  `RTM_GETLINK` dump, so no extra netlink dependency is needed.
- **Why:** Maintainer default for Open Question 5; avoids a dependency.
- **Alternatives:** `vishvananda/netlink` (more surface than needed).

## D7. Canary defaults

- **What:** Default TCP canaries `tcp://1.1.1.1:443` and `tcp://9.9.9.9:443` plus a local default-route check.
  Configurable via `[defaults] canary = [...]`; `canary = []` disables them. A DNS canary (`dns_canary`, default
  `["example.com"]`) is resolved only when a monitor fails at the DNS layer, to tell `LOCAL_DNS_BROKEN` apart from a
  remote DNS problem. Tests never use these defaults: they inject loopback targets.
- **Why:** Maintainer default for Open Question 2. `example.com` is an IANA-reserved name that always exists, so it
  works as a DNS liveness probe without naming any real service.
- **Alternatives:** gateway-only (cannot detect "Wi-Fi up but no internet").

## D8. Notification ownership

- **What:** The engine decides (`notify: true` plus a `notification` object with `id`, `title`, `body`, `urgency`).
  Frontends display it and dedupe by `notification.id`. Engine-level messages (offline, back online, VPN down) use a
  new `notice` event type. Both are additive to schema v1, which already requires frontends to ignore unknown types
  and fields.
- **Why:** Maintainer default for Open Question 10. Text generated once in Go is tested once (including redaction).

## D9. DNS through Go's resolver with RCODE sniffing

- **What:** Names are resolved with Go's pure DNS client (`PreferGo`), which reads `/etc/hosts` and
  `/etc/resolv.conf`, so systemd-resolved and VPN split DNS apply. A wrapper around the resolver's connections
  reads each response header to recover the RCODE (`net.DNSError` merges NXDOMAIN and "no address").
- **Why:** `DNS_NXDOMAIN`, `DNS_SERVFAIL` and `DNS_NO_ADDRESS` need the RCODE; a separate raw query would double the
  traffic and could disagree with the system path.
- **Alternatives:** cgo resolver (honours all of nsswitch, including mDNS, but hides the RCODE); a full DNS library.
- **Known gap:** names that only resolve through nsswitch modules other than `files`/`dns` (mDNS `.local`) are not
  supported.

## D10. Local step skips the route check for loopback and VPN-bound monitors

- **What:** `LOCAL_NO_LINK` needs no non-loopback interface up and running. `LOCAL_NO_ROUTE` (no default route in
  the main table) is skipped for loopback targets and for monitors with `requires_vpn`.
- **Why:** split-tunnel VPNs and policy routing can reach targets without a main-table default route; reporting
  those as offline would be a false alert.

## D11. Side when the canary is disabled

- **What:** with `canary = []`, DNS and TCP failures get side `unknown` (Story 3). TLS and HTTP failures keep their
  taxonomy side, because a server answered, which already proves the local network works.

## D12. Flap control details

- **What:** the first healthy check confirms `up` immediately. A failing first check is shown at once (the widget
  must not claim "up" for a service that is down) but notifies only after `fail_threshold` consecutive failures.
  A confirmed `down` that changes cause notifies once ("new cause"). Recovery notifies only from `down`, with the
  downtime; `degraded -> up` is silent. `TLS_EXPIRING_SOON` notifies at most once per monitor per 24 h.
- **Why:** Story 4 plus least surprise.

## D13. Additional cause code TLS_EXPIRING_SOON

- **What:** Story 10 needs a cause for "certificate expires within `tls_warn_days`"; Appendix B has none, so
  `TLS_EXPIRING_SOON` (tls layer, their side, degraded) was added. With `insecure_tls = true` verification is
  skipped but the expiry warning still applies.

## D14. `sebab check` exit codes

- **What:** 0 up, 1 degraded, 2 usage error, 3 internal error, 10 local, 11 VPN, 12 DNS, 13 TCP, 14 TLS, 15 HTTP.
- **Why:** distinct codes per failing layer (Story 9) that never collide with the conventional 1/2 meanings.

## D15. Control socket in Phase 1

- **What:** `sebab watch` serves `$XDG_RUNTIME_DIR/sebab/sebab.sock` (0600 in a 0700 dir, SO_PEERCRED checked) and
  attaches to an existing one. The Phase 2 daemon reuses the same server.
- **Why:** the widget's right click ("check now") and live config reloads need a control channel; Noctalia gives the
  stream no stdin. Building it once avoids a throwaway mechanism.

## D16. HTTP probe uses a fresh HTTP/1.1 transport per check

- **What:** no keep-alive, no HTTP/2, at most 64 KiB of body read, proxies from the environment respected (DNS and
  TCP steps then describe the proxy, noted in the summary facts).
- **Why:** every check measures a cold path (DNS, TCP, TLS timings stay meaningful) and stays polite.

## D17. Config rewrites lose comments

- **What:** `sebab monitor ...` and the panel rewrite `config.toml` atomically with mode 0600 and keep the previous
  file as `config.toml.bak`. Comments are not preserved; a header comment says so.
- **Alternatives:** a comment-preserving TOML editor (no maintained Go library does round-trips reliably).

## D18. Failure lab simulates TEST-NET routing in tests

- **What:** tests use a lab dialer that only dials loopback, blackholes 192.0.2.0/24 (TEST-NET-1) until the
  context ends and reports 198.51.100.0/24 (TEST-NET-2) unreachable. The manual lab (`sebab-lab`) uses the real
  network stack, where 192.0.2.1 times out or is unreachable depending on the host.
- **Why:** deterministic `TCP_TIMEOUT`/`TCP_UNREACHABLE` tests that can never send a packet off the machine.

## D19. Plugin translations are nested objects

- **What:** `translations/en.json` uses nested objects (`{"panel": {"title": ...}}`). Noctalia flattens them to the
  dotted keys passed to `noctalia.tr`; the community catalog CI rejects dotted keys.

## D20. Panel: live test before save, two clicks

- **What:** the first "Test, then save" click runs `sebab check --json` with the form values and shows the result;
  the second click saves. Changing any field invalidates the test. The panel does not dismiss on outside clicks
  (`dismiss_on_outside_click = false`, as the Noctalia docs recommend for panels with secret inputs).
- **Update:** the `dismiss_on_outside_click = false` part is reversed by D29; the two-click test-then-save part is
  kept and re-confirmed in D28.

## D21. Plugin finds a development binary

- **What:** when `sebab` is not on `PATH` and the `binary` setting is not an existing path, the plugin looks for
  `../../../bin/sebab` relative to its own directory, which is `bin/sebab` in a checkout of this repository.
- **Why:** lets the maintainer run the plugin from the repo (dev path source) without installing anything
  system-wide. In a catalog install the path does not exist and the widget shows the setup hint.

## D22. History in daily JSONL files, not SQLite

- **What:** checks are appended as compact JSON lines to `$XDG_STATE_HOME/sebab/history/checks-YYYY-MM-DD.jsonl`
  (one file per UTC day, 0600, pruned after `[history] keep_days`, default 7) and status changes to
  `incidents.jsonl` (kept 10x longer). The engine keeps the last 30 totals per monitor in memory, seeded from disk,
  and sends them as `recent_ms` on check events for sparklines.
- **Why:** the PRD suggests SQLite, but the only CGO-free driver (modernc.org/sqlite) roughly triples the static
  binary and its build time for a dataset that is append-only, small (about 1.5 MB per day for 20 monitors at 60 s)
  and only read sequentially. JSONL needs no dependency and stays greppable.
- **Alternatives:** modernc.org/sqlite; mattn/go-sqlite3 (needs CGO, breaks the static binary); bbolt.

## D23. Save in the panel: root cause and fix

- **What happened:** the owner pressed the panel's primary button ("Test, then save") and saw nothing. Evidence:
  `~/.config/sebab/` existed (created by the engine's config watcher) but held no `config.toml`, no handoff
  directory existed, and the Noctalia log showed no Luau errors. Replaying the panel's exact argv for `check` and
  `monitor add` in the plugin's environment (same HOME, no XDG_CONFIG_HOME, binary found through the dev path)
  worked and created `config.toml` from nothing, so the CLI, the environment (e) and first-run file creation (b)
  were fine. In Noctalia's v5.2.1 source, input `onChange` fires only on real edits and closure handlers keep
  stable path-derived names, so clicks did reach the script.
- **Root cause:** the form rendered every piece of feedback (the test result, validation errors and CLI errors) at
  the end of the scrollable field list, below the fold. With the two-step flow (D20) the first click only ran a
  test whose result was invisible, and a validation error such as "missing port" was invisible too, so pressing
  the button looked like a no-op and the owner closed the panel, which discards the draft. `monitor add` never ran.
- **Fix:** feedback moved to a pinned area outside the scroll (see D26, D27 for the full layout); failures show a
  short human reason plus the CLI's first stderr line (or the first `--json` error), timeouts are reported, and a
  successful save shows "Saved to ~/.config/sebab/config.toml" (home shown as `~`, honouring SEBAB_CONFIG and
  XDG_CONFIG_HOME). Proof: `frontends/noctalia/testdata/panel_save_test.luau` (failed before the fix) and
  `internal/cli` TestPanelSavePathPersistsAcrossDaemonRestart (daemon on a temp XDG dir, the panel's argv, file
  content and 0600/0700 modes, restart, monitor still listed).
- **Also fixed on the way:** (c) a frontend that attached after engine start never learned about `CONFIG_MISSING`
  or `CONFIG_INVALID` (the error event was emitted once at startup); the engine now replays the current config
  problem in every snapshot. The plugin kept a stale "no config yet" banner after the first save; the service now
  clears config errors when a new `monitors` event arrives. (d) the list was already driven by engine state.

## D24. Panel IPC for keybinds and scripted checks

- **What:** the panel accepts `add [url-or-host:port]` (opens the form, prefilled), `test`, `save` and `cancel`
  over `noctalia msg plugin yoockh/sebab:panel all <event>`, driving exactly the same code as the buttons.
- **Why:** lets a keybind open "add this URL", and lets the real-shell verification reach the result and error
  states: no Wayland input injection tool (wtype, ydotool) is installed and installing one needs root.

## D25. claude-cockpit used for layout ideas only

- **What:** before looking at `nightwatch75/claude-cockpit` (community-plugins) its license was checked:
  `license = "MIT"` in `plugin.toml` and an MIT `LICENSE` file (Copyright (c) 2026 nightwatch75). Only its
  screenshots and the shape of its layout (icon header, pill tab row, rounded cards with a headline value,
  progress bars, keyed `align = "stretch"` scrolls) were studied. No code was copied.

## D26. Panel frame: pinned header and footer, one growing scroll, no spacers

- **What:** every panel view uses one frame: header row (title, actions, close) pinned at the top, a single
  `ui.scroll` with `flexGrow = 1` holding the content, then pinned parts (result card, action buttons, status line)
  at the bottom. The panel is 520x720 (was 480x640) on a 1920x1080 screen.
- **Why:** the form's scroll was cut short with dead space under the buttons. Cause, verified in the real shell:
  `ui.spacer` is a flexible filler, and the empty placeholders (`ui.spacer({ height = 0 })` for "no status line"
  and "no test result") took a share of the free height away from the scroll. Placeholders are now omitted
  instead; `panel_layout_test.luau` asserts no spacer exists and only the scroll grows. Noctalia reads the panel
  size from the manifest when the plugin is (re)enabled, not on `config-reload`.

## D27. Result card pinned above the form buttons

- **What:** the add/edit form always shows one card between the scrolling fields and the buttons. States:
  placeholder ("Press Test to see which layer fails"), testing, result, error. A result shows the status pill,
  the cause as a plain sentence, the next step (hint), whose side it is (your side / their side / side unclear),
  the total time, and one chip per layer (Local, VPN, DNS, TCP, TLS, HTTP): ok with ms where the engine measures
  it (DNS, TCP, TLS, time to first byte for HTTP), failed, not run, or skipped (no VPN configured, no TLS for
  http:// and tcp targets, "no lookup" for literal IPs). Validation and CLI errors use the same card.
- **Why:** the result has to be visible without scrolling (it was the root cause of D23). The event has no local
  or VPN timing, so those chips show state only. The form was not split into two columns because 520 px leaves
  too little room for both.

## D28. Save flow: back to the list, flash the row, keep the two-step test

- **What:** a successful save returns to the list, shows "Saved <name>" and "Saved to <config path>" for 5 s,
  highlights the saved row (`out.id` from `monitor add --json`) for 5 s and clears the form, its result and the
  handoff path. Expiry runs on panel second ticks, which are switched off again when nothing is flashing. A
  failed save stays on the form with the error in the result card (D27). Cancel discards the draft.
- **D20's two-step flow stays:** the first click tests, the second saves. It no longer conflicts with usability
  because the result is now always visible (D27), and it keeps the PRD's "test before save" guarantee (Story 1).

## D29. Panel closes on outside click; drafts survive, secrets never do (reverses part of D20)

- **What:** `dismiss_on_outside_click = true`. Closing (outside click, Escape, X) keeps the non-secret draft in
  panel memory and reopens it; the token, its handoff file and any test result that used the token are discarded
  on every close, and the inputs get fresh keys so the masked field is rebuilt empty. Drafts are never written
  to disk. Cancel still discards the whole draft.
- **Why:** the owner reported that the panel only closed with X and blocked other widgets and windows. Noctalia
  v5.2.1 source (`src/shell/panel/panel_manager.cpp`, `resolvePanelKeyboardPlan`): on a compositor with focus-grab
  support (Hyprland) a panel that does not dismiss on outside clicks keeps an exclusive keyboard grab for as long
  as it is open; with outside dismissal Noctalia creates a focus grab, relaxes the keyboard to on-demand, and
  closes the panel when a click elsewhere clears the grab. D20 chose `false` to protect typed secrets; the
  mitigation above protects them better (they are wiped on close instead of the panel refusing to close).
- **Proof:** `panel_close_test.luau` (token gone from files, argv and the form after close; draft restored).
  The outside click itself must be checked by a human (no input injection is available here, TESTING.md).

## D30. Help view and field hints

- **What:** an "i" button (`info-circle`) next to the close X opens a help view (`ui.markdown`, API 21): what
  Sebab does, how to add a monitor, what each layer means, your side vs their side, how secrets are stored, and
  where the config file lives (path shown with `~`). Back returns to the view the user came from with the form
  draft intact. Every form field has a one-line hint under it (`fieldhelp.*`). Interval and expected status
  became full-width fields so their hints fit. All strings are nested keys in `translations/en.json`; the help
  can also be opened with the panel IPC event `help`.
