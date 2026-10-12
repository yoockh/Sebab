# Sebab

> Don't just know it's down. Know why.

Sebab is a local endpoint monitor for Linux desktops. A small Go engine probes each endpoint layer by layer
(local network, VPN, DNS, TCP, TLS, HTTP), names the layer that failed, and tells you whether the problem is on
your side or theirs. Results show up in your bar (Noctalia first, Waybar next) and as desktop notifications.

Status: early development. See `PROGRESS.md` for what works today and `TESTING.md` for how to try it.

## License

MIT, see `LICENSE`.
