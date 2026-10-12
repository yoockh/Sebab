--!nonstrict
-- Driver: panel.luau close behaviour (D29). A close keeps the draft but never the secret.

local TOKEN = "close-canary-token-33cd"
local seq = 0
local function json(t)
  seq += 1
  H.decoded["json#" .. seq] = t
  return "json#" .. seq
end
local function inputNamed(name)
  local hits = H.find(H.rendered, function(n)
    return n.type == "input" and type(n.props.key) == "string" and string.match(n.props.key, "%-" .. name .. "$") ~= nil
  end)
  H.assert(#hits == 1, "input " .. name .. " not found")
  return hits[1]
end
local function anyContains(t, needle)
  for _, v in pairs(t) do
    if type(v) == "string" and string.find(v, needle, 1, true) then
      return true
    end
  end
  return false
end

noctalia.state.set("sebab", { engine = "running", binary = "sebab", monitors = {} })
onOpen("add")
inputNamed("name").props.onChange("Admin API")
inputNamed("target").props.onChange("https://admin.example.com/ping")
H.find(H.rendered, function(n) return n.type == "select" end)[1].props.onChange("1", "bearer")
local tokenKey = inputNamed("token").props.key
inputNamed("token").props.onChange(TOKEN)
onIpc("test", nil)
H.reply(0, "/run/user/1000/sebab/handoff/handoff-close\n")
H.assert(H.written["/run/user/1000/sebab/handoff/handoff-close"] == TOKEN, "token handed off for the test")
H.reply(0, json({ v = 1, type = "check", status = "up", timings_ms = {} }))

-- A stray outside click closes the panel.
onClose()
H.assert(not anyContains(H.written, TOKEN), "no file holds the token after close")
H.assert(H.removed[#H.removed] == "/run/user/1000/sebab/handoff/handoff-close", "handoff file deleted on close")

-- Reopening restores the draft without the token.
onOpen("add")
H.assert(inputNamed("name").props.value == "Admin API", "name kept")
H.assert(inputNamed("target").props.value == "https://admin.example.com/ping", "target kept")
local token = inputNamed("token")
H.assert(token.props.key ~= tokenKey, "masked field rebuilt with a fresh key")
H.assert((token.props.value or "") == "", "token field empty after close")
H.assert(string.find(H.text(H.rendered), "card.placeholder", 1, true) ~= nil, "a token-based test result is discarded")

-- Saving now needs the token again: the old one is gone from panel state.
onIpc("save", nil)
H.contains(H.text(H.rendered), "form.err_token", "token must be re-entered")
for _, argv in ipairs(H.runs) do
  if type(argv) == "table" then
    H.assert(not string.find(table.concat(argv, " "), TOKEN, 1, true), "token never in argv")
  end
end
print("panel close ok")
