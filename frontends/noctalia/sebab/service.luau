--!nonstrict
-- Sebab service: runs `sebab watch` (which attaches to a running sebab engine or
-- starts an embedded one), parses its JSON lines and publishes a plain snapshot
-- under noctalia.state "sebab" for the bar widget and the panel.
--
-- The engine decides when to notify (notify = true in an event); this service
-- only displays those notifications, and drops repeats of an alert it already
-- showed in this session (for example after the stream restarts).
--
-- Processes spawned: `sebab watch`, `sebab kill-session` (stall recovery),
-- `sebab refresh` (IPC "refresh"). No network calls, no file writes.

local common = require("./lib/common.luau")

local TICK_MS = 1000
local DEFAULT_HEARTBEAT_MS = 15000

local binary = nil
local session = nil
local engine = "starting" -- starting | running | missing | stalled
local version = ""
local source = ""
local heartbeatMs = DEFAULT_HEARTBEAT_MS
local lastBeatMs = 0
local restartAfterMs = 0
local backoffMs = 5000
local dirty = true

local order = {} -- monitor ids in config order
local monitors = {} -- id -> monitor info + latest check
local summary = nil
local lastError = nil
local offline = false
local offlineCause = nil

-- Session-level dedupe of displayed notifications.
local seenIds = {}
local seenCount = 0
local lastAlert = {} -- monitor id -> cause code of the last "down"/"degraded" alert shown
local offlineShown = false
local vpnShown = {} -- interface -> true while its "down" notice is shown

local function now(): number
  return noctalia.nowMs()
end

local function markDirty()
  dirty = true
end

local function remember(id: string): boolean
  if seenIds[id] then
    return false
  end
  if seenCount > 500 then
    seenIds = {}
    seenCount = 0
  end
  seenIds[id] = true
  seenCount += 1
  return true
end

local function show(n)
  if type(n) ~= "table" or type(n.id) ~= "string" then
    return
  end
  if not remember(n.id) or noctalia.getConfig("notifications") == false then
    return
  end
  if n.urgency == "critical" then
    noctalia.notifyError(n.title or "Sebab", n.body)
  else
    noctalia.notify(n.title or "Sebab", n.body)
  end
end

-- Returns false when a check notification repeats an alert already shown.
local function freshCheckAlert(ev): boolean
  local id = ev.monitor and ev.monitor.id
  if id == nil then
    return true
  end
  local code = ev.cause and ev.cause.code
  local nid = ev.notification and ev.notification.id or ""
  if string.find(nid, ":tls-expiry:", 1, true) then
    return true -- the engine already limits these to one per day
  end
  if ev.status == "up" then
    lastAlert[id] = nil
    return true
  end
  if code ~= nil and lastAlert[id] == code then
    return false
  end
  lastAlert[id] = code
  return true
end

local function freshNotice(ev): boolean
  if ev.kind == "offline" then
    if offlineShown then
      return false
    end
    offlineShown = true
  elseif ev.kind == "online" then
    if not offlineShown then
      return false
    end
    offlineShown = false
  elseif ev.kind == "vpn_down" or ev.kind == "vpn_up" then
    local iface = ev.notification and string.match(ev.notification.title or "", "^VPN (%S+)") or "?"
    local down = ev.kind == "vpn_down"
    if (vpnShown[iface] == true) == down then
      return false
    end
    vpnShown[iface] = down or nil
  end
  return true
end

local function applyMonitors(list)
  local next = {}
  order = {}
  for _, info in ipairs(list or {}) do
    local m = monitors[info.id] or {}
    m.id, m.name, m.kind, m.target = info.id, info.name, info.kind, info.target
    m.intervalS, m.paused, m.vpn, m.auth, m.insecure = info.interval_s, info.paused, info.requires_vpn, info.auth, info.insecure_tls
    next[info.id] = m
    table.insert(order, info.id)
  end
  monitors = next
end

local function applyCheck(ev)
  local id = ev.monitor and ev.monitor.id
  if id == nil then
    return
  end
  local m = monitors[id]
  if m == nil then
    m = { id = id, name = ev.monitor.name, kind = ev.monitor.kind, target = ev.monitor.target }
    monitors[id] = m
    table.insert(order, id)
  end
  m.status, m.observed, m.side = ev.status, ev.observed, ev.side
  m.cause = ev.cause -- { layer, code, summary, hint } or nil
  m.timings = ev.timings_ms
  m.since = common.parseTime(ev.since)
  m.checkedAt = common.parseTime(ev.ts)
  m.insecure = ev.monitor.insecure_tls == true or m.insecure
  m.httpStatus = ev.details and ev.details.http_status
  m.tlsNotAfter = ev.details and common.parseTime(ev.details.tls_not_after)
  m.recent = ev.recent_ms -- latest totals in ms, -1 = failed (sparkline)
end

local function onLine(line: string)
  local ev = noctalia.json.decode(line)
  if type(ev) ~= "table" or ev.v ~= 1 then
    return
  end
  local t = ev.type
  if t == "hello" then
    engine, version, source = "running", ev.engine or "", ev.source or ""
    heartbeatMs = tonumber(ev.heartbeat_ms) or DEFAULT_HEARTBEAT_MS
    lastBeatMs = now()
    backoffMs = 5000
  elseif t == "heartbeat" then
    lastBeatMs = now()
    if engine == "stalled" then
      engine = "running"
    end
  elseif t == "monitors" then
    applyMonitors(ev.monitors)
  elseif t == "check" then
    applyCheck(ev)
    if ev.notify and freshCheckAlert(ev) then
      show(ev.notification)
    end
  elseif t == "summary" then
    summary = ev
    offline, offlineCause = ev.offline == true, ev.offline_cause
  elseif t == "notice" then
    if ev.notify and freshNotice(ev) then
      show(ev.notification)
    end
  elseif t == "error" then
    lastError = { code = ev.code, message = ev.message, monitor = ev.monitor }
  else
    return -- unknown types are ignored (schema rule)
  end
  lastBeatMs = math.max(lastBeatMs, now())
  markDirty()
end

local function publish()
  local list = {}
  for _, id in ipairs(order) do
    local m = monitors[id]
    if m ~= nil then
      table.insert(list, m)
    end
  end
  noctalia.state.set(common.STATE_KEY, {
    engine = engine,
    binary = binary,
    version = version,
    source = source,
    heartbeatMs = heartbeatMs,
    lastBeatMs = lastBeatMs,
    summary = summary,
    offline = offline,
    offlineCause = offlineCause,
    lastError = lastError,
    monitors = list,
  })
  dirty = false
end

local function startStream()
  binary = common.resolveBinary()
  if binary == nil then
    if engine ~= "missing" then
      engine = "missing"
      markDirty()
    end
    return
  end
  session = "noctalia-" .. tostring(math.floor(now()))
  local cmd = common.shellQuote(binary) .. " watch --format jsonl --session " .. session
  if noctalia.runStream(cmd, onLine) then
    engine = "starting"
    lastBeatMs = now()
  else
    noctalia.log("sebab: could not start the engine stream")
    engine = "stalled"
    restartAfterMs = now() + backoffMs
  end
  markDirty()
end

-- No heartbeat for 3 intervals: report "engine stalled", stop the old process
-- and start a new stream with backoff.
local function checkStall()
  local t = now()
  if engine == "missing" then
    if t >= restartAfterMs then
      restartAfterMs = t + 10000
      startStream()
    end
    return
  end
  if engine == "stalled" then
    if t >= restartAfterMs then
      startStream()
    end
    return
  end
  if t - lastBeatMs > 3 * heartbeatMs then
    engine = "stalled"
    markDirty()
    if binary ~= nil and session ~= nil then
      noctalia.runAsync({ binary, "kill-session", session })
    end
    restartAfterMs = t + backoffMs
    backoffMs = math.min(backoffMs * 2, 60000)
  end
end

function update()
  checkStall()
  if dirty then
    publish()
  end
end

function onIpc(event, _payload)
  if event == "refresh" then
    if binary ~= nil then
      noctalia.runAsync({ binary, "refresh" })
    end
  elseif event == "restart" then
    if binary ~= nil and session ~= nil then
      noctalia.runAsync({ binary, "kill-session", session })
    end
    engine = "stalled"
    restartAfterMs = now() + 1000
    markDirty()
  elseif event == "dump" then
    noctalia.log("sebab state: " .. tostring(noctalia.json.encode(noctalia.state.get(common.STATE_KEY))))
  end
end

function onExit(_signal, _reason)
  -- runStream children are terminated by the host; kill-session covers a
  -- process that ignores SIGTERM while stuck.
  if binary ~= nil and session ~= nil then
    noctalia.runAsync({ binary, "kill-session", session })
  end
end

-- The panel's "Restart" button.
noctalia.state.watch("sebab.cmd", function(v)
  if type(v) == "table" and v.cmd == "restart" then
    onIpc("restart", nil)
  end
end)

noctalia.setUpdateInterval(TICK_MS)
startStream()
publish()
