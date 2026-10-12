--!nonstrict
-- Sebab bar widget: one aggregate state for all monitors.
--   all up / degraded / N down / offline / engine missing / engine stalled
-- Left click opens the panel; right click asks the engine to check everything now.
-- Processes spawned: `sebab refresh` on right click.

local common = require("./lib/common.luau")

local snapshot = noctalia.state.get(common.STATE_KEY)
local compact = noctalia.getConfig("compact") == true

local function count(list, status)
  local n = 0
  for _, m in ipairs(list) do
    if m.status == status then
      n += 1
    end
  end
  return n
end

-- aggregate returns glyph, text, color role and the tooltip (a string or rows).
local function aggregate(s)
  if s == nil or s.engine == "starting" and (s.monitors == nil or #s.monitors == 0) then
    return "loader-2", "", "on_surface_variant", noctalia.tr("widget.starting")
  end
  if s.engine == "missing" then
    return "alert-triangle", compact and "" or noctalia.tr("widget.setup"), "tertiary", {
      { noctalia.tr("widget.missing_title"), noctalia.tr("widget.missing_hint") },
    }
  end
  if s.engine == "stalled" then
    return "plug-connected-x", compact and "" or noctalia.tr("widget.stalled"), "error", {
      { noctalia.tr("widget.stalled_title"), noctalia.tr("widget.stalled_hint") },
    }
  end
  local list = s.monitors or {}
  if #list == 0 then
    return "heart-rate-monitor", "", "on_surface_variant", noctalia.tr("widget.no_monitors")
  end
  if s.offline then
    return "wifi-off", compact and "" or noctalia.tr("widget.offline"), "error", {
      { noctalia.tr("widget.offline"), noctalia.tr("widget.offline_hint") },
    }
  end
  local rows = {}
  for _, m in ipairs(list) do
    if m.status ~= "up" and m.status ~= nil then
      local why = m.cause and m.cause.summary or ""
      table.insert(rows, { m.name or m.id, common.statusLabel(m.status) .. (why ~= "" and (": " .. why) or "") })
    end
  end
  local down, degraded = count(list, "down"), count(list, "degraded")
  if down > 0 then
    return "alert-circle", tostring(down), "error", rows
  end
  if degraded > 0 then
    return "alert-triangle", compact and "" or tostring(degraded), "tertiary", rows
  end
  if #rows == 0 then
    return "heart-rate-monitor", "", "primary", noctalia.trp("widget.all_up", #list)
  end
  return "heart-rate-monitor", "", "primary", rows
end

local function render(s)
  snapshot = s
  local glyph, text, color, rows = aggregate(s)
  barWidget.setGlyph(glyph)
  barWidget.setGlyphColor(color)
  barWidget.setText(text)
  barWidget.setColor(color)
  barWidget.setTooltip(rows)
end

noctalia.state.watch(common.STATE_KEY, render)
render(snapshot)

function onClick()
  noctalia.togglePanel(common.PANEL_ID)
end

function onRightClick()
  local binary = snapshot and snapshot.binary or common.resolveBinary()
  if binary ~= nil then
    noctalia.runAsync({ binary, "refresh" })
  end
end
