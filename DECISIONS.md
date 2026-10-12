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

## D21. Plugin finds a development binary

- **What:** when `sebab` is not on `PATH` and the `binary` setting is not an existing path, the plugin looks for
  `../../../bin/sebab` relative to its own directory, which is `bin/sebab` in a checkout of this repository.
- **Why:** lets the maintainer run the plugin from the repo (dev path source) without installing anything
  system-wide. In a catalog install the path does not exist and the widget shows the setup hint.
