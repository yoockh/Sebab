--!nonstrict
-- Driver: panel.luau dashboard (D31): overview card, row strips and sparklines, incidents tab.

local seq = 0
local function json(t)
  seq += 1
  H.decoded["json#" .. seq] = t
  return "json#" .. seq
end
local function byKey(key)
  return H.find(H.rendered, function(n) return n.props.key == key end)[1]
end

noctalia.state.set("sebab", { engine = "running", binary = "sebab",
  summary = { up = 2, degraded = 0, down = 1, unknown = 0, paused = 0 },
  monitors = {
    { id = "api", name = "API", status = "down", recent = { 90, -1, -1 }, checkedAt = 1 },
    { id = "web", name = "Web", status = "up", recent = { 20, 22 }, checkedAt = 1 },
    { id = "db", name = "DB", status = "up", checkedAt = 1 },
  } })
onOpen(nil)
local last = H.runs[#H.runs]
H.assert(last[2] == "overview" and last[3] == "--json", "overview loaded on open")

-- Before the history arrives: graceful empty states.
H.contains(H.text(byKey("overview-card")), "panel.uptime_none")
H.assert(byKey("ov-bar") ~= nil, "segmented count bar from the live summary")
H.assert(byKey("ov-up").props.flexGrow == 2 and byKey("ov-down").props.flexGrow == 1, "segments sized by count")
H.assert(byKey("trend-db") == nil, "no trend line without history or recent checks")
H.contains(H.text(byKey("trend-web")), "panel.no_history", "sparkline shown, strip waits for history")

local strip = {}
for i = 1, 24 do
  strip[i] = i == 24 and "down" or (i > 20 and "up" or "")
end
H.assert(H.replyOverview(json({ v = 1, since_s = 86400, buckets = 24, uptime = 0.875,
  monitors = { api = { checks = 8, uptime = 0.75, strip = strip }, web = { checks = 4, uptime = 1, strip = strip } },
  incidents = { { t = 1791800000000, m = "api", n = "API", from = "up", to = "down", c = "HTTP_BAD_GATEWAY",
    sum = "gateway returned 502", side = "theirs" } } })) == 1, "one overview load pending")
H.contains(H.text(byKey("overview-card")), "panel.uptime|pct=87.5", "24 h uptime")
local apiStrip = byKey("strip-api")
H.assert(#apiStrip.children == 24, "24 hourly segments")
H.assert(apiStrip.children[24].props.fill == "error" and apiStrip.children[22].props.fill == "primary"
  and apiStrip.children[1].props.fill == "on_surface/0.08", "segment colours by status, empty when no check")
H.assert(apiStrip.props.tooltip == "panel.strip_uptime|pct=75.0", "per-monitor uptime in the strip tooltip")
local spark = byKey("spark-row-api")
H.assert(spark.type == "graph" and spark.props.values2[2] == 1, "row sparkline marks failures")

-- Incidents tab.
byKey("tab-incidents").props.onClick()
local inc = H.text(H.rendered)
H.contains(inc, "API")
H.contains(inc, "HTTP_BAD_GATEWAY")
H.contains(inc, "status.down")
H.contains(inc, "card.side_theirs")
H.assert(byKey("overview-card") == nil, "overview card only on the monitors tab")
-- Empty incidents.
H.replyOverview(json({ v = 1, monitors = {}, incidents = {} }))
onOpen(nil)
H.replyOverview(json({ v = 1, monitors = {}, incidents = {} }))
H.contains(H.text(H.rendered), "panel.no_incidents")
byKey("tab-monitors").props.onClick()
H.contains(H.text(H.rendered), "panel.overview")
print("panel dashboard ok")
