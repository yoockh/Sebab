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
