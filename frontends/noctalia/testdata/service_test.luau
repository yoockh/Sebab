--!nonstrict
-- Driver: service.luau (appended after the mock and the entry script).

local seq = 0
local function line(t)
  seq += 1
  local key = "json#" .. seq
  H.decoded[key] = t
  return key
end

H.assert(#H.streams == 1, "service should start one stream")
local stream = H.streams[1]
H.contains(stream.cmd, "'sebab' watch --format jsonl --session noctalia-", "stream command")
H.assert(H.updateInterval == 1000, "service ticks every second")

local function feed(t)
  stream.onLine(line(t))
end

feed({ v = 1, type = "hello", ts = "2026-10-11T13:00:00.000Z", engine = "0.1.0", monitors = 2, heartbeat_ms = 15000, source = "embedded" })
feed({ v = 1, type = "monitors", ts = "2026-10-11T13:00:00.000Z", monitors = {
  { id = "api", name = "API", kind = "http", target = "https://api.example.com/", interval_s = 60, paused = false },
  { id = "db", name = "DB", kind = "tcp", target = "tcp://db.example:5432", interval_s = 60, paused = false },
} })
local down = { v = 1, type = "check", ts = "2026-10-11T13:01:00.000Z",
  monitor = { id = "api", name = "API", kind = "http", target = "https://api.example.com/" },
  status = "down", observed = "down", side = "theirs",
  cause = { layer = "tls", code = "TLS_EXPIRED", summary = "TLS certificate expired 2 days ago", hint = "Renew it." },
  timings_ms = { dns = 12, tcp = 38, total = 51 }, since = "2026-10-11T12:59:00.000Z", notify = true,
  notification = { id = "api:down:1", title = "API is down", body = "TLS certificate expired 2 days ago.", urgency = "critical" } }
feed(down)
feed({ v = 1, type = "summary", ts = "2026-10-11T13:01:00.000Z", up = 1, degraded = 0, down = 1, unknown = 0, paused = 0, offline = false })
feed({ v = 1, type = "unknown_future_type", ts = "2026-10-11T13:01:00.000Z" })
stream.onLine("this is not json")

update()
local s = noctalia.state.get("sebab")
H.assert(s.engine == "running", "engine running, got " .. tostring(s.engine))
H.assert(#s.monitors == 2 and s.monitors[1].id == "api" and s.monitors[1].status == "down", "monitor list")
H.assert(s.monitors[1].cause.code == "TLS_EXPIRED", "cause kept")
H.assert(s.monitors[1].since == 1791723540, "since parsed to unix seconds, got " .. tostring(s.monitors[1].since))
H.assert(s.summary.down == 1, "summary kept")
H.assert(#H.notifications == 1 and H.notifications[1].kind == "error" and H.notifications[1].title == "API is down",
  "critical notification shown once")

-- Same notification id again (replay): shown once only.
feed(down)
-- Same alert after a stream restart (new id, same cause): suppressed.
local again = table.clone(down)
again.notification = table.clone(down.notification)
again.notification.id = "api:down:2"
feed(again)
H.assert(#H.notifications == 1, "repeated alert must be suppressed, got " .. #H.notifications)

-- Cause change: new alert.
local changed = table.clone(again)
changed.cause = { layer = "http", code = "HTTP_BAD_GATEWAY", summary = "gateway returned 502", hint = "" }
changed.notification = { id = "api:cause:3", title = "API: new cause", body = "502", urgency = "critical" }
feed(changed)
H.assert(#H.notifications == 2, "cause change should notify")

-- Offline notice twice: one notification; online once.
local offline = { v = 1, type = "notice", ts = "x", kind = "offline", message = "You are offline", notify = true,
  notification = { id = "offline:1", title = "You are offline", body = "No default route. 2 monitors paused.", urgency = "critical" } }
feed(offline)
local offline2 = table.clone(offline)
offline2.notification = table.clone(offline.notification)
offline2.notification.id = "offline:2"
feed(offline2)
feed({ v = 1, type = "notice", ts = "x", kind = "online", message = "Back online", notify = true,
  notification = { id = "online:1", title = "Back online", body = "Checks resumed.", urgency = "normal" } })
H.assert(#H.notifications == 4, "offline + online exactly once each, got " .. #H.notifications)
H.assert(H.notifications[4].kind == "info", "normal urgency uses notify")

-- Error events are surfaced.
feed({ v = 1, type = "error", ts = "x", code = "CONFIG_INVALID", message = "monitor 'x': interval below 30s" })
update()
H.assert(noctalia.state.get("sebab").lastError.code == "CONFIG_INVALID", "error kept")

-- Notifications can be turned off.
H.config.notifications = false
feed({ v = 1, type = "notice", ts = "x", kind = "vpn_down", message = "m", notify = true,
  notification = { id = "vpn:wg0:down:1", title = "VPN wg0 is down", body = "b", urgency = "normal" } })
H.assert(#H.notifications == 4, "disabled notifications must not show")
H.config.notifications = true

-- Stall: no heartbeat for 3 intervals -> stalled, kill-session, restart with backoff.
H.nowMs += 46000
update()
s = noctalia.state.get("sebab")
H.assert(s.engine == "stalled", "engine should be stalled, got " .. tostring(s.engine))
local killed = false
for _, argv in ipairs(H.runs) do
  if type(argv) == "table" and argv[2] == "kill-session" then
    killed = true
  end
end
H.assert(killed, "stalled stream should be killed")
H.nowMs += 6000
update()
H.assert(#H.streams == 2, "a new stream should start after the backoff")
H.streams[2].onLine(line({ v = 1, type = "heartbeat", ts = "x" }))
update()
H.assert(noctalia.state.get("sebab").engine == "stalled" or noctalia.state.get("sebab").engine == "starting"
  or noctalia.state.get("sebab").engine == "running", "state after restart")

-- Restart from the panel.
noctalia.state.set("sebab.cmd", { cmd = "restart", at = 1 })
H.assert(noctalia.state.get("sebab") ~= nil, "still publishing")

-- IPC refresh runs the CLI with argv.
onIpc("refresh", nil)
local last = H.runs[#H.runs]
H.assert(type(last) == "table" and last[1] == "sebab" and last[2] == "refresh", "refresh argv")

print("service ok")
