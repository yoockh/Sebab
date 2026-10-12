# Testing Sebab

Everything below was run on the maintainer machine (Arch Linux, Hyprland, Noctalia 5.2.1, Go 1.27) unless a step
says it needs you. No step needs root except where marked **(sudo)**, and those are optional.

## 1. Build and automated tests

Prerequisites (Arch): `go` (1.26 or newer), `git`, `make`. Optional: `docker` (Debian/Fedora package checks and
the containerized lab), `luau` (`sudo pacman -S luau`, for the plugin script tests), `dbus` (`dbus-daemon`, used by
the D-Bus tests on a private bus).

```sh
cd ~/Documents/Sebab
make check            # gofmt check, go vet, go test -race ./...  (under a minute)
make build lab        # bin/sebab and bin/sebab-lab
SEBAB_LUAU=$(command -v luau) make plugin-test   # plugin scripts in a real Luau VM
noctalia plugins lint frontends/noctalia/sebab
```

Expected: every package `ok`; `plugin-test` prints PASS for TestServiceScript, TestServiceScriptWithoutBinary,
TestWidgetScript, TestPanelScript, TestTranslationsComplete; the linter prints `0 errors, 0 warnings`. Without
`luau` the plugin script tests are skipped (CI downloads luau and runs them).

What the automated tests cover, briefly:

| Area | Where |
|---|---|
| Every cause code, layer and side (failure lab, loopback only) | `lab/integration_test.go` |
| Flap control, offline handling, VPN pause, suspend guard, quiet hours, mute, history | `internal/engine` |
| CLI commands, exit codes, secret handoff, daemon, notification ownership, channels | `internal/cli` |
| Secret never in events, notifications, reports, argv | `internal/cli` (`assertNoLeak`), `internal/secret`, `frontends/noctalia/testdata/panel_test.luau` |
| Plugin service, widget, panel against a mock host that validates every `ui.*` prop | `frontends/noctalia` |
| D-Bus notifications and logind PrepareForSleep on a private bus | `internal/platform/linux` |

## 2. Install on Arch

Pick one.

**a. Run from the checkout (no install).** `make build` and use `./bin/sebab`. The Noctalia plugin finds
`bin/sebab` automatically when it is loaded from this checkout.

**b. Local package from the checkout (sebab-git).** The PKGBUILD clones from GitHub, which only works after you
push. To build from the local checkout instead:

```sh
mkdir -p /tmp/sebab-pkg && cd /tmp/sebab-pkg
sed 's#git+${url}.git#git+file://'"$HOME"'/Documents/Sebab#' ~/Documents/Sebab/packaging/aur/sebab-git/PKGBUILD > PKGBUILD
makepkg -si      # (sudo) installs /usr/bin/sebab, the systemd user unit and docs
```

Verified: `makepkg` builds and runs `check()` (the full test suite) successfully.

**c. sebab-bin** needs a signed GitHub release first (see "Releasing" below).

Debian/Ubuntu/Fedora packages: `make build`, then
`VERSION=0.1.0 ARCH=amd64 go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0 pkg --config packaging/nfpm/nfpm.yaml --packager deb --target dist/`
(and `--packager rpm`). Verified to install, run `sebab doctor` with the right distro hints, and uninstall in
`debian:12`, `ubuntu:24.04` and `fedora:42` containers with `--network none`.

## 3. Enable the Noctalia plugin

Already done on this machine during the build:

```sh
noctalia msg plugins source add dev path ~/Documents/Sebab/frontends/noctalia
noctalia msg plugins enable yoockh/sebab
noctalia msg plugins list | grep sebab     # yoockh/sebab [dev] 0.1.0 enabled requires sebab
```

**You still need to place the widget** (I did not edit your bar layout): Settings → Bar → add widget → "Sebab"
(`yoockh/sebab:status`). Open the panel with a left click, or:

```sh
noctalia msg panel-toggle yoockh/sebab:panel
noctalia msg panel-open yoockh/sebab:panel add      # straight to the add form
```

Plugin settings (Settings → Plugins → Sebab): `binary` (default `sebab`; leave it when running from the checkout),
`notifications`, and per widget `compact`.

Useful while testing:

```sh
noctalia msg plugin yoockh/sebab:engine all dump      # logs the state the widget sees
noctalia msg plugin yoockh/sebab:engine all restart   # restarts the engine stream
grep -E 'sebab|yoockh' ~/.cache/noctalia/noctalia.log | tail
```

To remove it again: `noctalia msg plugins disable yoockh/sebab` and `noctalia msg plugins source remove dev`.

## 4. Failure lab

The lab serves every failure mode on loopback (127.0.0.1) with its own DNS server for `*.lab.test` and its own CA.
It never touches the internet.

```sh
make lab
./bin/sebab-lab -out lab/out          # prints the commands below; Ctrl+C to stop
```

In another terminal:

```sh
export SEBAB_LAB_DNS=127.0.0.1:15353 SEBAB_CONFIG=$PWD/lab/out/config.toml
./bin/sebab check http://app.lab.test:18080/status/502     # exit 15, HTTP_BAD_GATEWAY
./bin/sebab watch --embedded | jq -c 'select(.type=="check") | [.monitor.id,.status,.cause.code,.side]'
```

`lab/out/config.toml` has one monitor per failure mode. Expected results (first check):

| Monitor | Expected cause | Layer | Side | Status |
|---|---|---|---|---|
| ok | none | | | up |
| nxdomain | DNS_NXDOMAIN | dns | theirs | down |
| servfail | DNS_SERVFAIL | dns | theirs | down |
| dns-timeout | DNS_TIMEOUT | dns | theirs (other names resolve) | down |
| noaddr | DNS_NO_ADDRESS | dns | theirs | down |
| refused | TCP_REFUSED | tcp | theirs | down |
| blackhole (192.0.2.1) | TCP_TIMEOUT, or TCP_UNREACHABLE if your router rejects TEST-NET | tcp | theirs / unknown | down |
| expired | TLS_EXPIRED | tls | theirs | down |
| notyet | TLS_NOT_YET_VALID | tls | unknown | down |
| wronghost | TLS_HOSTNAME_MISMATCH | tls | theirs | down |
| selfsigned | TLS_UNTRUSTED ("self-signed") | tls | theirs | down |
| expiring | TLS_EXPIRING_SOON | tls | theirs | degraded |
| handshake | TLS_HANDSHAKE_FAILED | tls | theirs | down |
| http-401 | HTTP_AUTH | http | yours | down |
| http-502 | HTTP_BAD_GATEWAY | http | theirs | down |
| http-503 | HTTP_UNAVAILABLE | http | theirs | down |
| http-500 | HTTP_SERVER_ERROR | http | theirs | down |
| http-404 | HTTP_CLIENT_ERROR | http | theirs | down |
| http-429 | HTTP_RATE_LIMITED (retry after 2 min) | http | theirs | degraded |
| slow | HTTP_SLOW (3 s, threshold 2 s) | http | theirs | degraded |
| hang | HTTP_TIMEOUT | http | theirs | down |
| wrong-status | HTTP_UNEXPECTED_STATUS (200, expected 204) | http | theirs | down |
| redirect-away | HTTP_UNEXPECTED_REDIRECT | http | theirs | down |
| redirect-loop | HTTP_REDIRECT_LOOP | http | theirs | down |
| captive | HTTP_ASSERTION_FAILED | http | theirs | down |
| vpn | VPN_DOWN (interface sebab0 missing) | vpn | yours | paused |

Verified on this machine with the real binary: every row produced exactly this code, layer and side.

The local-side codes need your network:

- **LOCAL_OFFLINE / "You are offline":** turn Wi-Fi off (`noctalia msg wifi-disable`) while a watch with a real
  config runs. Expect every monitor `unknown` and one notification; turning Wi-Fi back on gives one "Back online".
- **LOCAL_NO_LINK / LOCAL_NO_ROUTE:** same, depending on whether the interface disappears or only the route.
- **LOCAL_DNS_BROKEN:** covered by `TestLabLocalDNSBroken`; manually, point `/etc/resolv.conf` at a dead
  resolver **(sudo)**.
- **VPN resume:** **(sudo)** `sudo ip link add sebab0 type dummy && sudo ip link set sebab0 up` makes the `vpn`
  monitor resume with one "VPN sebab0 is back" notification; `sudo ip link del sebab0` pauses it again.

Optional container variant (same ports, binds host loopback only):

```sh
UID=$(id -u) GID=$(id -g) docker compose -f lab/docker-compose.yml up --build
```

### See the lab in your bar

1. Start the lab as above.
2. `noctalia msg plugins disable yoockh/sebab`
3. `SEBAB_CONFIG=$PWD/lab/out/config.toml SEBAB_LAB_DNS=127.0.0.1:15353 ./bin/sebab daemon &`
4. `noctalia msg plugins enable yoockh/sebab`. The plugin's `sebab watch` attaches to that daemon, so the
   widget and panel show all lab monitors. After the second check (30 s) about 20 "is down" notifications appear,
   one per failing monitor, and no repeats afterwards.
5. Stop the daemon (`kill %1`). The plugin falls back to its own embedded engine automatically.

Verified on this machine: the plugin attached (`source: daemon`), showed all 26 lab monitors, Noctalia displayed one
notification per failing monitor, and after the daemon stopped the plugin continued with an embedded engine.

## 5. Manual checklist per story

Use a scratch config so your real one stays clean: `export SEBAB_CONFIG=/tmp/sebab-test.toml`, plus the lab
variables from section 4 when a step uses `*.lab.test`.

**Story 1, add a monitor**
- Panel → + → leave everything empty → "Test, then save": "Enter a name." and "Enter a URL or host:port."
- Target `db.example` → "Missing port: use host:port, or a full URL."; interval `10` → "at least 30 seconds".
- Valid input → first click shows the test result (status pill, cause, timings); second click saves; the row appears.
- CLI: `sebab monitor add --name Web --target http://app.lab.test:18080/ok` prints "Test check:" then "Added".
- A running `sebab watch` in another terminal prints a `monitors` event with the new monitor within a second.

**Story 2, layered diagnosis:** `sebab check --json <lab url>` for a few lab rows: one `cause.code`, a summary and a
hint; `timings_ms` has numbers for completed steps and `null` from the failing step on.

**Story 3, your side vs theirs:** see section 4 (Wi-Fi off/on). With `canary = []` in `[defaults]`, a refused port
reports `side: unknown`.

**Story 4, notifications:** with `fail_threshold = 2` a new failure notifies on the second failing check only;
"<name> is down" with the cause in the body; after recovery "<name> recovered / Back up after N minutes"; no repeat
while the cause stays; switching the lab monitor from `/status/502` to `/status/503` (edit the URL) gives one
"<name>: new cause".

**Story 5, bar widget:** all up → heart icon; a degraded monitor → triangle + count; a down monitor → red count;
Wi-Fi off → "offline"; set the plugin `binary` to `/nonexistent` → "setup" with an install hint;
`kill -STOP $(pgrep -f 'sebab watch')` → "stalled" after about 45 s, then the plugin restarts the stream
(`kill -CONT` the old one or let the restart kill it). Left click toggles the panel; right click triggers a check
(`sebab watch` output shows new check events immediately).

**Story 6, panel details:** click a failed row: summary, hint, "<CODE> on the <layer> layer, <side>", timings
("DNS 1 ms, TCP 0 ms, TLS failed"), "down since HH:MM (N min ago)", a latency sparkline after a few checks. Buttons:
Check now, Copy diagnosis, Edit (prefilled), mute bell, pause/resume, delete (asks for confirmation).

**Story 7, authenticated checks:** add `http://app.lab.test:18080/auth/bearer` with "Bearer token" and the token
`lab-canary-secret-7f3a9c1e5b` → up. `grep -r lab-canary ~/.config/sebab` finds nothing in `config.toml` (only a
`secret = '<id>-token'` reference). Without a keyring the panel shows the warning that the secret went to
`secrets.toml` (0600); `sebab doctor` explains how to add gnome-keyring. While the panel test runs,
`ps aux | grep sebab` never shows the token. A wrong token → HTTP_AUTH "credentials rejected (401)" with the
"token may be expired or invalid" hint.

**Story 8, VPN:** see section 4 (dummy interface). One "VPN sebab0 is down" notification for all monitors that need
it, never one per monitor.

**Story 9, CLI check:** `sebab check https://example.com` style output; `--json`; exit codes 0/1/10 to 15 per
`docs/cli.md`; works with `SEBAB_CONFIG=/nonexistent`.

**Story 10, TLS expiry:** the lab `expiring` monitor is degraded with "TLS certificate expires in 4 days"; one
"certificate expires soon" notification per day.

**Story 11, copy diagnosis:** panel → Copy diagnosis → paste: a Markdown table with target (query secrets shown as
`[REDACTED]`), status, cause, timings and, when history exists, the last 24 h of status changes.

**Story 12, setup:** `sebab doctor` on this machine reports the missing Secret Service keyring with
`sudo pacman -S gnome-keyring`, notifications handled by noctalia, ICMP allowed, and the engine state.

**Phase 2:** `systemctl --user link ~/Documents/Sebab/packaging/systemd/sebab.service` only after installing the
binary to `/usr/bin` (the unit runs `/usr/bin/sebab daemon`); `sebab waybar` prints JSON lines (see
`frontends/waybar`); with no frontend attached the daemon sends desktop notifications itself; suspend and resume
the laptop: a "system resumed: checks continue in 15s" notice and no false alerts.

**Phase 4:** `sebab tui` in a terminal: arrows move, Enter expands, r checks now, q quits.

**Phase 3:** `sebab history <id>`; `sebab report <id>`; `--assert` (docs/cli.md); `quiet_hours`; `sebab monitor
mute <id>`; `[[notify]]` channels with `sebab notify-test` (point a webhook at `python3 -m http.server` on loopback
to see the POST, or at your own ntfy/Telegram).

## 6. Releasing (needs you)

1. Create/choose a GPG key, `export GPG_FINGERPRINT=...`.
2. `git tag v0.1.0 && goreleaser release --clean` (drafts a GitHub release with tarballs, .deb, .rpm, source,
   `checksums.txt` and `checksums.txt.sig`). `goreleaser release --snapshot --clean --skip=sign,publish` was verified
   locally.
3. `packaging/aur/update-checksums.sh 0.1.0` fills `sebab-bin`'s sums from the signed checksum file; publish the
   AUR packages by hand.
4. For the Noctalia catalog: generate `thumbnail.webp` (960x540) with the thumbnail generator from a real
   screenshot, copy `frontends/noctalia/sebab` into a community-plugins checkout, open the PR.

## 7. Known issues and limits

- **Widget placement and visuals were not checked by a human.** The plugin loads, the service streams, the panel
  renders list and form in the real shell with no warnings, but nobody has looked at the widget on a bar yet.
- **Notification history:** Noctalia shows plugin notifications as toasts but does not write them to
  `notification_history.json`.
- **Plugin `thumbnail.webp` is missing** (required by the catalog CI; the validator reports only this).
- **DNS:** names that only resolve through nsswitch modules other than files/dns (for example mDNS `.local`)
  fail as DNS errors (D9).
- **Proxies:** with `HTTPS_PROXY` set, the DNS and TCP steps describe the proxy, not the target.
- **Config comments** are lost when the CLI or panel rewrites `config.toml` (D17); the previous file is kept as
  `.bak`.
- **History settings** (`[history]`) apply on engine start, not on live reload.
- **Unix socket path limit:** `$XDG_RUNTIME_DIR/sebab/sebab.sock` must stay under 108 bytes; with an unusually long
  runtime dir the socket is skipped (a warning is printed) and frontends cannot share the engine.
- **Not implemented:** the opt-in Globalping "is it down for everyone" check (Phase 3, contacts a third party),
  GNOME and KDE frontends (Phase 4), Phase 5 items.
- **D-Bus notifications from the daemon** use the generic `org.freedesktop.Notifications` API: no actions, no
  replacing of earlier notifications.
