# Sebab

Don't just know it's down. Know why. Sebab monitors your HTTP(S) and TCP
endpoints from your own machine, names the layer that failed (local network,
VPN, DNS, TCP, TLS or HTTP) and tells you whether the problem is on your side
or theirs: "Payments API is down: TLS certificate expired 2 days ago".

## Plugin

| Field | Value |
| --- | --- |
| ID | `yoockh/sebab` |
| Entries | Bar widget: `status`; panel: `panel`; service: `engine` |

## Requirements

Install `sebab` (the engine) on `PATH`, for example from the AUR package
`sebab-bin`, or set the full path in the plugin's `binary` setting. All probing
and diagnosis happens in `sebab`; this plugin only displays its event stream.

A Secret Service keyring (GNOME Keyring, KWallet or KeePassXC) is recommended
for authenticated monitors. Without one, `sebab` falls back to environment
variable references or a 0600 secrets file and says so. Run `sebab doctor` to
see what is missing.

## Usage

1. Add the **Sebab** widget (`yoockh/sebab:status`) to a bar.
2. Left click the widget to open the panel, then **+** to add a monitor: a
   name, a URL (`https://api.example.com/health`) or `host:port`, an interval
   (30 s minimum) and optionally expected status codes, authentication and a
   required VPN interface. The first **Test, then save** click runs a live
   check and shows its result; the second click saves.
3. Right click the widget to check every monitor now.

Open the panel from a keybind or the shell:

```sh
noctalia msg panel-toggle yoockh/sebab:panel
```

The widget shows one aggregate state: a heart monitor when everything is up,
the number of monitors that are down, a warning when something is degraded,
"offline" when your own connection is down, "setup" when `sebab` is not
installed, and "stalled" when the engine stops sending heartbeats.

In the panel, click a monitor to see the cause, a hint, whose side the fault is
on, the timing breakdown (DNS, TCP, TLS, time to first byte) and since when it
is down. From there you can check it now, **Copy diagnosis** (a redacted
Markdown snippet for bug reports), edit, pause/resume or delete it.

## Settings

| Setting | Type | Default | Description |
| --- | --- | --- | --- |
| `binary` | `string` | `sebab` | Name on `PATH` or absolute path of the engine. |
| `notifications` | `bool` | `true` | Show the engine's down, recovery, offline and VPN notifications. |
| `compact` (widget) | `bool` | `false` | Show only the icon and the number of monitors that are down. |

Monitors themselves are not plugin settings: they live in
`~/.config/sebab/config.toml`, shared with every other Sebab frontend (CLI,
Waybar), and are edited from the panel.

## IPC

```sh
noctalia msg plugin yoockh/sebab:engine all refresh   # check every monitor now
noctalia msg plugin yoockh/sebab:engine all restart   # restart the engine stream
noctalia msg plugin yoockh/sebab:engine all dump      # log the current state
```

## Notes

- **Processes spawned:** the service runs `sebab watch --format jsonl --session <token>`
  (long-running) and `sebab kill-session <token>` to restart a stalled stream.
  The widget runs `sebab refresh`. The panel runs `sebab monitor add|edit|show|rm|pause|resume`,
  `sebab check --json`, `sebab secret handoff`, `sebab report` and `sebab refresh`.
  All of them use the argument-array form (no shell), except `runStream`,
  which takes a quoted shell string.
- **Network calls:** none from the plugin. The `sebab` engine contacts only the
  endpoints you configure, plus the canary targets (default `tcp://1.1.1.1:443`
  and `tcp://9.9.9.9:443`, configurable or disabled with `canary = []`) after a
  failure to decide whose side it is.
- **Files written:** the panel writes a typed secret into a one-shot handoff
  file that `sebab secret handoff` created (mode 0600 in a 0700 directory under
  `$XDG_RUNTIME_DIR/sebab/handoff`, or in the plugin data directory when
  `XDG_RUNTIME_DIR` is unset). The engine reads and deletes it; the panel
  deletes it on cancel or close. Secrets are never passed as process
  arguments. `sebab` itself writes `~/.config/sebab/config.toml`.
- **Privacy:** no telemetry. Secrets are redacted from every event,
  notification and copied diagnosis.
- The engine (`sebab watch`) attaches to a running `sebab daemon` when there is
  one, so several frontends share one engine and nothing is probed twice.
