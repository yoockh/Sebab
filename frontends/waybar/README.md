# Sebab for Waybar

1. Install `sebab` (AUR `sebab-bin`, `.deb`/`.rpm` from the releases, or `make build`).
2. Merge `config.jsonc` into your Waybar config and add `"custom/sebab"` to a module list.
3. Merge `style.css` into your Waybar stylesheet.
4. Optional: `systemctl --user enable --now sebab.service` so Waybar, Noctalia and the CLI share one engine.

The module shows a count of monitors that are down (or degraded), `offline`
when your own connection is down, and `stalled` when the engine stops sending
heartbeats. Hover for the causes; click to check everything now.

Waybar does not display Sebab's notifications, so the engine sends them through
`org.freedesktop.Notifications` (mako, dunst, swaync, ...). When Noctalia is
attached to the same engine it displays them instead; you never get one twice.

Output format (`return-type: json`):

```json
{"text":"1","alt":"down","class":["down"],"tooltip":"Payments API: down, TLS certificate expired 2 days ago","percentage":80}
```

Text and tooltip are escaped for Pango markup; keep `"escape"` unset (false).
