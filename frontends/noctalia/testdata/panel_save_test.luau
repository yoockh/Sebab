--!nonstrict
-- Driver: panel.luau save flow (DECISIONS: item 7). Every piece of feedback must
-- be visible without scrolling, and a failed save must say why.

local seq = 0
local function json(t)
  seq += 1
  local key = "json#" .. seq
  H.decoded[key] = t
  return key
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

local function primary()
  -- The primary action is the last button of the pinned footer.
  local buttons = H.find(H.rendered, function(n) return n.type == "button" and n.props.variant == "primary" end)
  H.assert(#buttons >= 1, "no primary button")
  local b = buttons[#buttons]
  b.props.onClick()
end

-- First run: no config yet.
noctalia.state.set("sebab", { engine = "running", binary = "sebab", monitors = {},
  lastError = { code = "CONFIG_MISSING", message = "no config yet" } })
onOpen("add")
H.contains(H.text(H.rendered), "form.add_title")

-- A validation problem must be visible without scrolling.
type_("name", "Example")
type_("target", "example.com")
primary()
H.contains(H.visibleText(H.rendered), "form.err_port", "validation error must be visible")

-- The test result must be visible without scrolling.
type_("target", "https://example.com/health")
primary()
local argv = H.reply(12, json({ v = 1, type = "check", status = "down", side = "theirs",
  cause = { layer = "dns", code = "DNS_NXDOMAIN", summary = "domain does not resolve (NXDOMAIN)", hint = "Check the name." },
  timings_ms = { total = 80 } }))
H.assert(argv[2] == "check", "first click tests")
H.contains(H.visibleText(H.rendered), "Domain does not resolve (NXDOMAIN)", "test result must be visible")

-- A failed save stays on the form and shows a short reason plus the CLI's first stderr line.
primary()
argv = H.reply(3, "", "sebab: store secret: permission denied\nsecond line")
H.assert(argv[2] == "monitor" and argv[3] == "add", "second click saves")
local vis = H.visibleText(H.rendered)
H.contains(vis, "form.save_failed", "short human reason")
H.contains(vis, "store secret: permission denied", "first stderr line")
H.assert(string.find(vis, "second line", 1, true) == nil, "only the first stderr line")
H.contains(H.text(H.rendered), "form.add_title", "a failed save stays on the form")

-- A timed-out command is reported, never silent.
primary()
local p = table.remove(H.pendingAsync, 1)
p.cb({ exitCode = -1, stdout = "", stderr = "", timedOut = true, stdoutTruncated = false, stderrTruncated = false })
H.contains(H.visibleText(H.rendered), "form.timed_out", "timeout is surfaced")

-- A successful save returns to the list with the config path.
primary()
H.reply(0, json({ ok = true, id = "example" }))
vis = H.visibleText(H.rendered)
H.contains(vis, "panel.saved_to|path=~/.config/sebab/config.toml", "saved confirmation names the file")
H.assert(string.find(H.text(H.rendered), "form.add_title", 1, true) == nil, "back to the list")

-- The list shows the engine's state, not panel memory: the new row appears when the engine reports it.
noctalia.state.set("sebab", { engine = "running", binary = "sebab",
  monitors = { { id = "example", name = "Example", kind = "http", target = "https://example.com/health", status = "down" } } })
H.contains(H.text(H.rendered), "Example", "new monitor listed from engine state")

print("panel save ok")

-- IPC drives the same path as the buttons.
onIpc("add", "http://127.0.0.1:9/health")
H.assert(inputNamed("target").props.value == "http://127.0.0.1:9/health", "ipc add prefills the target")
H.assert(inputNamed("name").props.value == "127.0.0.1", "ipc add derives a name")
onIpc("test", nil)
local a = H.reply(13, json({ v = 1, type = "check", status = "down", cause = { layer = "tcp", code = "TCP_REFUSED",
  summary = "nothing listening", hint = "h" }, timings_ms = {} }))
H.assert(a[2] == "check", "ipc test runs check")
onIpc("cancel", nil)
H.assert(string.find(H.text(H.rendered), "form.add_title", 1, true) == nil, "ipc cancel returns to the list")
print("panel ipc ok")
