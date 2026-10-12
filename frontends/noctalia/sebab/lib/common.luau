--!nonstrict
-- Helpers shared by the Sebab service, bar widget and panel.
-- All diagnosis logic lives in the sebab engine; this file only formats data.

local M = {}

M.STATE_KEY = "sebab"
M.PANEL_ID = "yoockh/sebab:panel"

-- Quote a value for /bin/sh (only used for runStream, which takes a string).
function M.shellQuote(s: string): string
  return "'" .. string.gsub(s, "'", "'\\''") .. "'"
end

-- Find the engine binary: the configured name or path, then a development
-- checkout (frontends/noctalia/sebab -> ../../../bin/sebab). Returns nil when
-- nothing is found, so the widget can show a setup hint instead of an error.
function M.resolveBinary(): string?
  local configured = noctalia.getConfig("binary")
  if type(configured) ~= "string" or configured == "" then
    configured = "sebab"
  end
  local expanded = noctalia.expandPath(configured)
  if string.find(expanded, "/", 1, true) then
    if noctalia.fileExists(expanded) then
      return expanded
    end
  elseif noctalia.commandExists(configured) then
    return configured
  end
  local dir = noctalia.pluginDir()
  if dir ~= nil then
    local dev = dir .. "/../../../bin/sebab"
    if noctalia.fileExists(dev) then
      return dev
    end
  end
  return nil
end

-- Parse "2026-10-11T13:01:00.123Z" to Unix seconds (UTC). Returns nil on failure.
function M.parseTime(ts: string?): number?
  if type(ts) ~= "string" then
    return nil
  end
  local y, mo, d, h, mi, s = string.match(ts, "^(%d+)-(%d+)-(%d+)T(%d+):(%d+):(%d+)")
  if y == nil then
    return nil
  end
  y, mo, d = tonumber(y), tonumber(mo), tonumber(d)
  -- Days from civil (Howard Hinnant), proleptic Gregorian calendar.
  if mo <= 2 then
    y -= 1
  end
  local era = math.floor(y / 400)
  local yoe = y - era * 400
  local mp = (mo + 9) % 12
  local doy = math.floor((153 * mp + 2) / 5) + d - 1
  local doe = yoe * 365 + math.floor(yoe / 4) - math.floor(yoe / 100) + doy
  local days = era * 146097 + doe - 719468
  return days * 86400 + tonumber(h) * 3600 + tonumber(mi) * 60 + tonumber(s)
end

-- "12 s ago", "3 min ago", "2 h ago".
function M.ago(seconds: number?): string
  if seconds == nil then
    return noctalia.tr("time.never")
  end
  if seconds < 0 then
    seconds = 0
  end
  if seconds < 60 then
    return noctalia.tr("time.seconds_ago", { n = math.floor(seconds) })
  elseif seconds < 3600 then
    return noctalia.tr("time.minutes_ago", { n = math.floor(seconds / 60) })
  elseif seconds < 86400 then
    return noctalia.tr("time.hours_ago", { n = math.floor(seconds / 3600) })
  end
  return noctalia.tr("time.days_ago", { n = math.floor(seconds / 86400) })
end

-- Local clock time for a Unix timestamp, in the user's shell time format.
function M.clock(unix: number?): string
  if unix == nil then
    return ""
  end
  return noctalia.formatTime(noctalia.timeFormat(), unix)
end

-- Palette role and glyph per monitor status.
M.STATUS_STYLE = {
  up = { color = "primary", glyph = "circle-check" },
  degraded = { color = "tertiary", glyph = "alert-triangle" },
  down = { color = "error", glyph = "alert-circle" },
  unknown = { color = "on_surface_variant", glyph = "help-circle" },
  paused = { color = "on_surface_variant", glyph = "player-pause" },
}

function M.statusStyle(status: string?)
  return M.STATUS_STYLE[status or ""] or M.STATUS_STYLE.unknown
end

function M.statusLabel(status: string?): string
  return noctalia.tr("status." .. (status or "unknown"))
end

-- "DNS 12 ms, TCP 38 ms, TLS failed".
function M.timings(t, failedLayer: string?): string
  if type(t) ~= "table" then
    return ""
  end
  local parts = {}
  local function add(name, value, layer)
    if type(value) == "number" then
      table.insert(parts, `{name} {math.floor(value)} ms`)
    elseif failedLayer == layer then
      table.insert(parts, name .. " " .. noctalia.tr("panel.failed"))
    end
  end
  add("DNS", t.dns, "dns")
  add("TCP", t.tcp, "tcp")
  add("TLS", t.tls, "tls")
  add("TTFB", t.ttfb, "http")
  return table.concat(parts, ", ")
end

return M
