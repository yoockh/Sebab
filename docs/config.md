# config.toml reference

Location: `$XDG_CONFIG_HOME/sebab/config.toml` (usually `~/.config/sebab/config.toml`), or `$SEBAB_CONFIG`.
It is the single source of truth for every frontend. `sebab monitor ...` and the Noctalia panel rewrite it
atomically (mode 0600, previous version kept as `config.toml.bak`); comments are not preserved. Running engines
reload it as soon as it changes. Unknown keys are rejected with the line number.

Secrets never live here: every `secret` field is a reference, either a stored secret name
(`sebab secret set NAME`) or `env:VARIABLE`.

```toml
[defaults]
interval = "60s"            # minimum 30s
timeout = "10s"             # per check; must be below the interval
fail_threshold = 2          # consecutive failures before "down" is notified (1-10)
recover_threshold = 2       # consecutive successes before "recovered" (1-10)
slow_threshold = "2s"       # HTTP_SLOW above this; "0s" disables
tls_warn_days = 14          # TLS_EXPIRING_SOON within this many days; 0 disables
heartbeat = "15s"           # frontends report "stalled" after 3 missed heartbeats
canary = ["tcp://1.1.1.1:443", "tcp://9.9.9.9:443"]   # [] disables side detection
dns_canary = ["example.com"]                         # resolved only after a DNS failure
quiet_hours = "22:00-07:00" # local time, may wrap midnight; no notifications inside

[history]
keep_days = 7               # day files in $XDG_STATE_HOME/sebab/history
# disabled = true

[[notify]]                  # optional external channels (they contact a third party)
type = "webhook"            # POST JSON {title, body, urgency, monitor, status, code, side, kind, time, id}
url = "https://hooks.example.com/sebab"
secret = "hook-token"       # optional, sent as "Authorization: Bearer"

[[notify]]
type = "ntfy"
url = "https://ntfy.example.com/my-topic"
secret = "env:NTFY_TOKEN"   # optional

[[notify]]
type = "telegram"
secret = "telegram-bot-token"
chat_id = "123456789"
# api_url = "https://api.telegram.org"

[[monitor]]
id = "payments-api"         # lowercase letters, digits, . _ - (derived from the name when added by CLI/panel)
name = "Payments API"
url = "https://api.example.com/health"   # or: target = "tcp://db.example.com:5432"
interval = "60s"            # any [defaults] duration/threshold can be overridden per monitor
expect_status = [200, 204]  # default: any 2xx (and 3xx with follow_redirects = false)
method = "GET"              # or HEAD
headers = { Accept = "application/json" }   # never credentials
follow_redirects = true
allowed_redirect_hosts = ["login.example.com"]  # same host and www./apex variants are always allowed
auth = { type = "bearer", secret = "payments-token" }
# auth = { type = "header", header = "X-API-Key", secret = "env:API_KEY" }
# auth = { type = "basic", username = "monitor", secret = "basic-password" }
requires_vpn = { interface = "wg0" }   # paused (VPN_DOWN) while wg0 is missing or down
ca_file = "/etc/ssl/internal-ca.pem"   # trust an internal CA
# insecure_tls = true                  # skip verification (shown as insecure everywhere)
paused = false
muted = false               # checked and shown, never notified
assert = [
  { type = "json", path = "status", value = "ok" },
  { type = "header", name = "Content-Type", value = "json" },
  { type = "body", value = "Maintenance", negate = true },
]
```

Limits: at most 100 monitors, no CIDR ranges or port ranges (Sebab is not a scanner), at most 10 assertions per
monitor.
