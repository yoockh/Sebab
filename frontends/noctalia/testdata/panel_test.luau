--!nonstrict
-- Driver: panel.luau. Stories 1, 6, 7 and 11 from the panel side.

local TOKEN = "panel-canary-token-55aa"
local seq = 0
local function json(t)
  seq += 1
  local key = "json#" .. seq
  H.decoded[key] = t
  return key
end

local function allArgs()
  local parts = {}
  for _, argv in ipairs(H.runs) do
    if type(argv) == "table" then
      table.insert(parts, table.concat(argv, " "))
    else
      table.insert(parts, argv)
    end
  end
  return table.concat(parts, "\n")
end

local function byKeyPrefix(prefix)
  return H.find(H.rendered, function(n)
    return type(n.props.key) == "string" and string.sub(n.props.key, 1, #prefix) == prefix
  end)
end

local function inputNamed(name)
  local hits = H.find(H.rendered, function(n)
    return n.type == "input" and type(n.props.key) == "string" and string.match(n.props.key, "%-" .. name .. "$") ~= nil
  end)
  H.assert(#hits == 1, "input " .. name .. " not found")
  return hits[1]
end

local function type_(name, value)
  inputNamed(name).props.onChange(value)
end

local function clickText(text)
  H.click(H.rendered, function(n)
    return n.props.text == text
  end)
end

local function clickGlyph(glyph)
  H.click(H.rendered, function(n)
    return n.props.glyph == glyph
  end)
end

noctalia.state.set("sebab", {
  engine = "running", binary = "sebab", summary = { up = 1, degraded = 0, down = 1, unknown = 0, paused = 0 },
  monitors = {
    { id = "api", name = "Payments API", kind = "http", target = "https://api.example.com/health", status = "down",
      side = "theirs", since = 1760187540, checkedAt = 1760187590, insecure = false,
      cause = { layer = "tls", code = "TLS_EXPIRED", summary = "TLS certificate expired 2 days ago",
        hint = "Renew the certificate on the server. DNS and TCP are fine." },
      timings = { dns = 12, tcp = 38, total = 51 } },
    { id = "web", name = "Web", kind = "http", target = "https://www.example.com/", status = "up",
      checkedAt = 1760187595, timings = { dns = 3, tcp = 20, tls = 41, ttfb = 90, total = 95 }, insecure = true },
  },
})

onOpen(nil)
local text = H.text(H.rendered)
H.contains(text, "Payments API", "row name")
H.contains(text, "status.down", "status pill")
H.contains(text, "51 ms", "latency")
H.contains(text, "time.seconds_ago|n=10", "last check")
H.assert(#H.find(H.rendered, function(n) return n.type == "glyph" and n.props.name == "shield-off" end) == 1,
  "insecure monitor is marked")

-- Story 6: expanding a failed row shows cause, hint, side, timings, down since.
H.click(H.rendered, function(n) return n.props.key == "row-api" end)
text = H.text(H.rendered)
H.contains(text, "TLS certificate expired 2 days ago")
H.contains(text, "Renew the certificate on the server.")
H.contains(text, "panel.cause_line|", "cause line")
H.contains(text, "side=side.theirs", "side")
H.contains(text, "timings=DNS 12 ms, TCP 38 ms, TLS panel.failed", "timing breakdown")
H.contains(text, "panel.since|", "down since")

-- Story 11: copy diagnosis through `sebab report`.
clickText("panel.copy")
local argv = H.reply(0, "### Sebab diagnosis: Payments API\n")
H.assert(argv[2] == "report" and argv[3] == "api", "report argv")
H.assert(H.clipboard == "### Sebab diagnosis: Payments API\n", "clipboard set")

-- Pause.
clickGlyph("player-pause")
argv = H.reply(0, json({ ok = true, id = "api" }))
H.assert(argv[2] == "monitor" and argv[3] == "pause" and argv[4] == "api", "pause argv")

-- Story 1: add with validation errors first.
clickGlyph("plus")
H.contains(H.text(H.rendered), "form.add_title")
clickText("form.test_and_save")
text = H.text(H.rendered)
H.contains(text, "form.err_name")
H.contains(text, "form.err_target")

type_("name", "Admin API")
type_("target", "db.example")
type_("interval", "10")
clickText("form.test_and_save")
text = H.text(H.rendered)
H.contains(text, "form.err_port", "missing port message")
H.contains(text, "form.err_interval", "interval message")

type_("target", "https://admin.example.com/api/ping")
type_("interval", "60")
type_("expect", "200, 204")

-- Story 7: bearer token in a masked field.
local sel = H.find(H.rendered, function(n) return n.type == "select" end)[1]
sel.props.onChange("1", "form.auth_bearer")
local token = inputNamed("token")
H.assert(token.props.password == true, "token field must be masked")
type_("token", TOKEN)

-- First click tests (handoff file, then check --json), and shows the result.
clickText("form.test_and_save")
argv = H.reply(0, "/run/user/1000/sebab/handoff/handoff-abc\n")
H.assert(argv[2] == "secret" and argv[3] == "handoff", "asks the engine for a handoff file")
H.assert(H.written["/run/user/1000/sebab/handoff/handoff-abc"] == TOKEN, "token written to the handoff file")
argv = H.reply(15, json({ v = 1, type = "check", status = "down", cause = { layer = "http", code = "HTTP_AUTH",
  summary = "credentials rejected (401)", hint = "Your token may be expired or invalid." }, timings_ms = { dns = 1, tcp = 1 } }))
H.assert(argv[2] == "check" and argv[3] == "--json", "live test uses check --json")
local joined = table.concat(argv, " ")
H.contains(joined, "--secret-file /run/user/1000/sebab/handoff/handoff-abc")
H.contains(joined, "--keep-secret-file")
H.contains(joined, "--expect 200,204")
H.assert(argv[#argv] == "https://admin.example.com/api/ping", "target is the positional argument")
text = H.text(H.rendered)
H.contains(text, "form.test_result")
H.contains(text, "credentials rejected (401)")

-- Second click saves.
clickText("form.save")
argv = H.reply(0, json({ ok = true, id = "admin-api", secret_backend = "keyring" }))
joined = table.concat(argv, " ")
H.assert(argv[2] == "monitor" and argv[3] == "add", "save uses monitor add")
H.contains(joined, "--name Admin API")
H.contains(joined, "--interval 60s")
H.contains(joined, "--auth bearer --secret-file /run/user/1000/sebab/handoff/handoff-abc")
H.assert(string.find(joined, "--keep-secret-file", 1, true) == nil, "save consumes the handoff file")
H.contains(H.text(H.rendered), "panel.saved|name=Admin API", "back to the list with a toast")

-- Edit prefills from `monitor show --raw`.
H.click(H.rendered, function(n) return n.props.key == "row-web" end)
H.click(H.rendered, function(n) return n.props.glyph == "pencil" end)
argv = H.reply(0, json({ id = "web", name = "Web", kind = "http", target = "https://www.example.com/?k=1",
  interval_s = 120, expect_status = { 200 }, auth = "header", auth_secret = "web-key", auth_header = "X-Key",
  insecure_tls = true }))
H.assert(argv[2] == "monitor" and argv[3] == "show" and argv[5] == "--raw", "edit loads the raw monitor")
H.assert(inputNamed("interval").props.value == "120", "interval prefilled")
H.assert(inputNamed("header").props.value == "X-Key", "header prefilled")
H.contains(H.text(H.rendered), "form.token_keep|name=web-key", "empty token keeps the stored secret")
clickText("form.test_and_save")
argv = H.reply(0, json({ v = 1, type = "check", status = "up", timings_ms = {} }))
H.contains(table.concat(argv, " "), "--secret web-key", "test reuses the stored secret reference")
clickText("form.save")
argv = H.reply(0, json({ ok = true, id = "web" }))
H.assert(argv[3] == "edit" and argv[4] == "web", "save uses monitor edit")

-- Delete with confirmation.
clickGlyph("trash")
H.contains(H.text(H.rendered), "confirm.body|name=")
clickText("confirm.delete")
argv = H.reply(0, json({ ok = true, id = "web" }))
H.assert(argv[3] == "rm", "delete uses monitor rm")

-- A typed token that was never saved is removed when the panel closes.
clickGlyph("plus")
type_("name", "X")
type_("target", "https://x.example/")
H.find(H.rendered, function(n) return n.type == "select" end)[1].props.onChange("1", "")
type_("token", TOKEN)
clickText("form.test")
H.reply(0, "/run/user/1000/sebab/handoff/handoff-zzz\n")
H.reply(0, json({ v = 1, type = "check", status = "up", timings_ms = {} }))
onClose()
local removed = false
for _, p in ipairs(H.removed) do
  if p == "/run/user/1000/sebab/handoff/handoff-zzz" then
    removed = true
  end
end
H.assert(removed, "unsaved handoff file must be deleted on close")

-- The token never appears in any process argument.
H.assert(string.find(allArgs(), TOKEN, 1, true) == nil, "token leaked into argv")

-- Engine missing banner.
noctalia.state.set("sebab", { engine = "missing", monitors = {} })
onOpen(nil)
H.contains(H.text(H.rendered), "panel.missing_binary")

print("panel ok")
