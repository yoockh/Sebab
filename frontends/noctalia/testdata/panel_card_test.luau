--!nonstrict
-- Driver: panel.luau result card (D27).

local seq = 0
local function json(t)
  seq += 1
  H.decoded["json#" .. seq] = t
  return "json#" .. seq
end
local function card()
  local c = H.find(H.rendered, function(n) return n.props.key == "result-card" end)
  H.assert(#c == 1, "exactly one result card")
  return H.text(c[1])
end
local function chip(id)
  return H.text(H.find(H.rendered, function(n) return n.props.key == "chip-" .. id end)[1])
end

noctalia.state.set("sebab", { engine = "running", binary = "sebab", monitors = {} })
onIpc("add", "https://api.example.com/health")
onOpen("add")
onIpc("add", "https://api.example.com/health")
H.contains(card(), "card.placeholder", "placeholder before any test")
H.contains(H.visibleText(H.rendered), "card.placeholder", "card visible without scrolling")

onIpc("test", nil)
H.contains(card(), "panel.busy_test", "testing state")
H.reply(14, json({ v = 1, type = "check", status = "down", side = "theirs",
  monitor = { kind = "http", target = "https://api.example.com/health" },
  cause = { layer = "tls", code = "TLS_EXPIRED", summary = "tLS certificate expired 2 days ago", hint = "Renew it." },
  timings_ms = { dns = 12, tcp = 38, total = 51 } }))
local c = card()
H.contains(c, "status.down", "status pill")
H.contains(c, "TLS certificate expired 2 days ago", "headline in plain words, capitalised")
H.contains(c, "Renew it.", "hint")
H.contains(c, "card.side_theirs", "side")
H.contains(c, "51 ms", "total")
H.contains(chip("local"), "card.layer_ok")
H.contains(chip("vpn"), "card.layer_skip")
H.contains(chip("dns"), "12 ms")
H.contains(chip("tcp"), "38 ms")
H.contains(chip("tls"), "card.layer_fail")
H.contains(chip("http"), "card.layer_none")

-- Healthy plain-HTTP result with a VPN: TLS skipped, VPN ok, everything timed.
onIpc("add", "http://intranet.example:8080/")
H.find(H.rendered, function(n) return n.type == "input" and string.match(n.props.key or "", "%-vpn$") ~= nil end)[1].props.onChange("wg0")
onIpc("test", nil)
H.reply(0, json({ v = 1, type = "check", status = "up", monitor = { kind = "http", target = "http://intranet.example:8080/" },
  timings_ms = { dns = 2, tcp = 3, ttfb = 40, total = 50 } }))
c = card()
H.contains(c, "card.all_good")
H.assert(string.find(c, "card.side_", 1, true) == nil, "no side for a healthy result")
H.contains(chip("vpn"), "card.layer_ok")
H.contains(chip("tls"), "card.layer_skip")
H.contains(chip("http"), "40 ms")

-- Errors use the same card.
onIpc("add", "nope")
onIpc("test", nil)
H.contains(card(), "card.error_title")
H.contains(card(), "form.err_port")
print("panel card ok")
