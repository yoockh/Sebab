--!nonstrict
-- Driver: widget.luau. Story 5 states: all up, degraded, down count, offline,
-- engine missing, engine stalled.

local function set(s)
  noctalia.state.set("sebab", s)
end

local function mon(id, status, summary)
  return { id = id, name = string.upper(id), status = status, cause = summary and { summary = summary } or nil }
end

H.assert(H.bar.glyph == "loader-2", "starting state before any data")

set({ engine = "missing", monitors = {} })
H.assert(H.bar.glyph == "alert-triangle" and H.bar.text == "widget.setup", "setup hint when binary missing")
H.assert(type(H.bar.tooltip) == "table" and H.bar.tooltip[1][1] == "widget.missing_title", "missing tooltip")

set({ engine = "stalled", binary = "sebab", monitors = { mon("a", "up") } })
H.assert(H.bar.glyph == "plug-connected-x" and H.bar.text == "widget.stalled" and H.bar.color == "error", "stalled")

set({ engine = "running", binary = "sebab", monitors = {} })
H.assert(H.bar.tooltip == "widget.no_monitors", "no monitors hint")

set({ engine = "running", binary = "sebab", monitors = { mon("a", "up"), mon("b", "up") } })
H.assert(H.bar.glyph == "heart-rate-monitor" and H.bar.text == "" and H.bar.color == "primary", "all up")
H.assert(H.bar.tooltip == "widget.all_up#2", "all up tooltip")

set({ engine = "running", binary = "sebab", monitors = { mon("a", "up"), mon("b", "degraded", "response took 4.2 s") } })
H.assert(H.bar.glyph == "alert-triangle" and H.bar.text == "1" and H.bar.color == "tertiary", "degraded")

set({ engine = "running", binary = "sebab", monitors = { mon("a", "down", "TLS certificate expired"),
  mon("b", "down", "gateway returned 502"), mon("c", "degraded", "slow"), mon("d", "up") } })
H.assert(H.bar.glyph == "alert-circle" and H.bar.text == "2" and H.bar.color == "error", "down count")
H.assert(#H.bar.tooltip == 3, "tooltip lists non-UP monitors only, got " .. #H.bar.tooltip)
H.contains(H.bar.tooltip[1][2], "TLS certificate expired", "tooltip has the short cause")

set({ engine = "running", binary = "sebab", offline = true, monitors = { mon("a", "unknown") } })
H.assert(H.bar.glyph == "wifi-off" and H.bar.text == "widget.offline", "offline")

onClick()
H.assert(H.toggled[1] == "yoockh/sebab:panel", "left click opens the panel")
onRightClick()
local argv = H.runs[#H.runs]
H.assert(argv[1] == "sebab" and argv[2] == "refresh", "right click forces a check")

print("widget ok")
