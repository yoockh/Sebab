--!nonstrict
-- Sebab panel: monitor list with per-monitor details, add / edit / delete /
-- pause, a live test before saving, and "Copy diagnosis".
--
-- Everything is done through the sebab CLI (argv form, no shell):
--   sebab monitor add|edit|show|rm|pause|resume, sebab check --json,
--   sebab secret handoff, sebab report, sebab refresh
-- A token typed into the masked field is written to a one-shot 0600 file that
-- the engine created ("sebab secret handoff"); the engine reads and deletes it.
-- The token is never put in process arguments. Cancelling or closing the panel
-- deletes the file.

local common = require("./lib/common.luau")

local snapshot = noctalia.state.get(common.STATE_KEY)
local view = "list" -- list | form | confirm
local expanded = {}
local form = nil
local formSerial = 0
local formErrors = {}
local testResult = nil
local busy = nil
local toast = nil
local handoffPath = nil
local confirmId = nil
local highlightId, highlightUntil = nil, 0 -- row to highlight after a save (D28)
local toastUntil = 0

-- How long the "Saved" line and the row highlight stay (ms).
local FLASH_MS = 5000

local AUTH_TYPES = { "", "bearer", "header", "basic" }

local function tr(key, subst)
  return noctalia.tr(key, subst)
end

local function trim(s)
  return noctalia.string.trim(s or "")
end

local function binary()
  return snapshot and snapshot.binary or common.resolveBinary()
end

local function findMonitor(id)
  for _, m in ipairs(snapshot and snapshot.monitors or {}) do
    if m.id == id then
      return m
    end
  end
  return nil
end

local render -- forward declaration

-- run executes the CLI with argv and calls cb(result) on completion.
local function run(argv, label, timeoutMs, cb)
  local bin = binary()
  if bin == nil then
    toast = tr("panel.missing_binary")
    render()
    return
  end
  table.insert(argv, 1, bin)
  busy = label
  render()
  local ok = noctalia.runAsync(argv, function(res)
    busy = nil
    cb(res)
    render()
  end, timeoutMs or 15000)
  if not ok then
    busy = nil
    toast = tr("panel.command_failed")
    render()
  end
end

local function decode(s)
  local v = noctalia.json.decode(trim(s))
  if type(v) == "table" then
    return v
  end
  return nil
end

-- firstLine returns the first non-empty line of CLI output without the
-- "sebab: " prefix.
local function firstLine(text)
  for line in string.gmatch(text or "", "[^\n]+") do
    line = trim(line)
    if line ~= "" then
      return (string.gsub(line, "^sebab: ", ""))
    end
  end
  return ""
end

-- failure explains a failed command: a short human reason, then the CLI's
-- own first line (validation errors from --json output, else stderr).
local function failure(res, reasonKey)
  if res.timedOut then
    return { tr("form.timed_out") }
  end
  local out = decode(res.stdout)
  local detail = ""
  if out ~= nil and type(out.errors) == "table" and #out.errors > 0 then
    detail = tostring(out.errors[1])
  else
    detail = firstLine(res.stderr)
  end
  if detail == "" then
    detail = tr("panel.exit_code", { code = res.exitCode })
  end
  return { tr(reasonKey), detail }
end

-- configPathDisplay is where saves go, with the home directory shown as ~.
local function configPathDisplay()
  local home = noctalia.getenv("HOME") or ""
  local path = noctalia.getenv("SEBAB_CONFIG")
  if path == nil or path == "" then
    local base = noctalia.getenv("XDG_CONFIG_HOME")
    if base == nil or base == "" then
      base = home .. "/.config"
    end
    path = base .. "/sebab/config.toml"
  end
  if home ~= "" and string.sub(path, 1, #home) == home then
    path = "~" .. string.sub(path, #home + 1)
  end
  return path
end

local function cliErrors(res)
  local out = decode(res.stdout)
  if out ~= nil and type(out.errors) == "table" and #out.errors > 0 then
    return out.errors
  end
  local msg = trim(res.stderr)
  if msg == "" then
    msg = tr("panel.command_failed")
  end
  return { (string.gsub(msg, "^sebab: ", "")) }
end

-- Handoff file lifecycle -------------------------------------------------------

local function dropHandoff()
  if handoffPath ~= nil then
    noctalia.removeFile(handoffPath)
    handoffPath = nil
  end
end

-- withSecret writes the typed token into an engine-created handoff file and
-- calls cb(path). With no token typed it calls cb(nil).
local function withSecret(cb)
  if form == nil or form.token == "" then
    cb(nil)
    return
  end
  if handoffPath ~= nil and noctalia.fileExists(handoffPath) then
    local ok, err = noctalia.writeFile(handoffPath, form.token)
    if ok then
      cb(handoffPath)
    else
      formErrors = { tostring(err) }
      render()
    end
    return
  end
  local dataDir = noctalia.pluginDataDir() or ""
  run({ "secret", "handoff", "--fallback-dir", dataDir }, tr("panel.busy_secret"), 5000, function(res)
    if res.exitCode ~= 0 then
      formErrors = cliErrors(res)
      return
    end
    handoffPath = trim(res.stdout)
    local ok, err = noctalia.writeFile(handoffPath, form.token)
    if not ok then
      formErrors = { tostring(err) }
      dropHandoff()
      return
    end
    cb(handoffPath)
  end)
end

-- Form -------------------------------------------------------------------------

local function blankForm()
  formSerial += 1
  return {
    serial = formSerial, mode = "add", id = nil, name = "", target = "", interval = "60", expect = "",
    authIndex = 0, header = "X-API-Key", username = "", token = "", vpn = "", insecure = false,
    hadAuthIndex = 0, secretRef = nil,
  }
end

local function validTarget(t)
  if string.match(t, "^https?://[^/%s]+") then
    return true
  end
  return string.match(t, "^[%w%.%-_]+:%d+$") ~= nil or string.match(t, "^%[[%x:]+%]:%d+$") ~= nil
    or string.match(t, "^tcp://[%w%.%-_]+:%d+$") ~= nil
end

local function validate()
  local errs = {}
  if trim(form.name) == "" then
    table.insert(errs, tr("form.err_name"))
  end
  local t = trim(form.target)
  if t == "" then
    table.insert(errs, tr("form.err_target"))
  elseif not validTarget(t) then
    if string.find(t, "://", 1, true) then
      table.insert(errs, tr("form.err_url"))
    else
      table.insert(errs, tr("form.err_port"))
    end
  end
  local iv = tonumber(trim(form.interval))
  if iv == nil or iv < 30 then
    table.insert(errs, tr("form.err_interval"))
  end
  for code in string.gmatch(form.expect, "[^,%s]+") do
    local n = tonumber(code)
    if n == nil or n < 100 or n > 599 then
      table.insert(errs, tr("form.err_expect", { code = code }))
    end
  end
  local auth = AUTH_TYPES[form.authIndex + 1]
  if auth == "header" and trim(form.header) == "" then
    table.insert(errs, tr("form.err_header"))
  end
  if auth == "basic" and trim(form.username) == "" then
    table.insert(errs, tr("form.err_username"))
  end
  if auth ~= "" and form.token == "" and (form.secretRef == nil or form.authIndex ~= form.hadAuthIndex) then
    table.insert(errs, tr("form.err_token"))
  end
  return errs
end

-- authArgs renders the auth flags. forTest keeps the handoff file for the save.
local function authArgs(argv, secretPath, forTest)
  local auth = AUTH_TYPES[form.authIndex + 1]
  if auth == "" then
    if form.mode == "edit" and form.hadAuthIndex ~= 0 then
      table.insert(argv, "--auth=none")
    end
    return
  end
  table.insert(argv, "--auth")
  table.insert(argv, auth)
  if auth == "header" then
    table.insert(argv, "--header-name")
    table.insert(argv, trim(form.header))
  elseif auth == "basic" then
    table.insert(argv, "--username")
    table.insert(argv, trim(form.username))
  end
  if secretPath ~= nil then
    table.insert(argv, "--secret-file")
    table.insert(argv, secretPath)
    if forTest then
      table.insert(argv, "--keep-secret-file")
    end
  elseif form.secretRef ~= nil then
    table.insert(argv, "--secret")
    table.insert(argv, form.secretRef)
  end
end

local function commonArgs(argv)
  if trim(form.expect) ~= "" then
    table.insert(argv, "--expect")
    table.insert(argv, (string.gsub(form.expect, "%s", "")))
  end
  table.insert(argv, "--vpn=" .. trim(form.vpn))
  table.insert(argv, "--insecure=" .. tostring(form.insecure))
end

local function runTest()
  local errs = validate()
  if #errs > 0 then
    formErrors = errs
    render()
    return
  end
  formErrors = {}
  withSecret(function(path)
    local argv = { "check", "--json", "--name", trim(form.name) }
    commonArgs(argv)
    authArgs(argv, path, true)
    table.insert(argv, trim(form.target))
    run(argv, tr("panel.busy_test"), 30000, function(res)
      local ev = decode(res.stdout)
      if ev == nil or ev.type ~= "check" then
        formErrors = failure(res, "form.test_failed")
        testResult = nil
        return
      end
      testResult = ev
    end)
  end)
end

local function save()
  local errs = validate()
  if #errs > 0 then
    formErrors = errs
    render()
    return
  end
  if testResult == nil then
    -- Story 1: run a test and show the result before saving.
    runTest()
    return
  end
  withSecret(function(path)
    local argv
    if form.mode == "add" then
      argv = { "monitor", "add", "--json", "--no-test" }
    else
      argv = { "monitor", "edit", form.id, "--json", "--no-test" }
    end
    table.insert(argv, "--name")
    table.insert(argv, trim(form.name))
    table.insert(argv, "--target")
    table.insert(argv, trim(form.target))
    table.insert(argv, "--interval")
    table.insert(argv, trim(form.interval) .. "s")
    commonArgs(argv)
    authArgs(argv, path, false)
    run(argv, tr("panel.busy_save"), 15000, function(res)
      local out = decode(res.stdout)
      if res.exitCode ~= 0 or out == nil or out.ok ~= true then
        formErrors = failure(res, "form.save_failed")
        return
      end
      if path ~= nil then
        handoffPath = nil -- consumed and deleted by the engine
      end
      toast = tr("panel.saved", { name = trim(form.name) }) .. "\n" .. tr("panel.saved_to", { path = configPathDisplay() })
      toastUntil = noctalia.nowMs() + FLASH_MS
      highlightId, highlightUntil = out.id or form.id, noctalia.nowMs() + FLASH_MS
      panel.setWantsSecondTicks(true)
      if out.warning ~= nil then
        toast = toast .. "\n" .. out.warning
      end
      form, testResult, formErrors, view = nil, nil, {}, "list"
    end)
  end)
end

local function cancelForm()
  dropHandoff()
  form, testResult, formErrors, view = nil, nil, {}, "list"
  render()
end

local function openAdd()
  form, testResult, formErrors, view = blankForm(), nil, {}, "form"
  render()
end

local function openEdit(id)
  run({ "monitor", "show", id, "--raw" }, tr("panel.busy_load"), 5000, function(res)
    local v = decode(res.stdout)
    if res.exitCode ~= 0 or v == nil then
      toast = cliErrors(res)[1]
      return
    end
    local f = blankForm()
    f.mode, f.id, f.name, f.target = "edit", v.id, v.name or "", v.target or ""
    f.interval = tostring(v.interval_s or 60)
    local codes = {}
    for _, c in ipairs(v.expect_status or {}) do
      table.insert(codes, tostring(c))
    end
    f.expect = table.concat(codes, ",")
    for i, t in ipairs(AUTH_TYPES) do
      if t == (v.auth or "") then
        f.authIndex, f.hadAuthIndex = i - 1, i - 1
      end
    end
    f.header = v.auth_header or f.header
    f.username = v.auth_username or ""
    f.secretRef = v.auth_secret
    f.vpn = v.vpn or ""
    f.insecure = v.insecure_tls == true
    form, testResult, formErrors, view = f, nil, {}, "form"
  end)
end

-- Monitor actions --------------------------------------------------------------

local function simple(argv, label, okToast)
  run(argv, label, 15000, function(res)
    if res.exitCode ~= 0 then
      toast = cliErrors(res)[1]
    elseif okToast ~= nil then
      toast = okToast
    end
  end)
end

local function copyDiagnosis(id)
  run({ "report", id }, tr("panel.busy_copy"), 30000, function(res)
    if res.exitCode ~= 0 or trim(res.stdout) == "" then
      toast = cliErrors(res)[1]
      return
    end
    noctalia.copyToClipboard(res.stdout, "text/plain")
    toast = tr("panel.copied")
  end)
end

-- Rendering --------------------------------------------------------------------

local function pill(status)
  local style = common.statusStyle(status)
  return ui.row({ fill = style.color .. "/0.16", radius = 8, paddingH = 8, paddingV = 2, align = "center" }, {
    ui.label({ text = common.statusLabel(status), color = style.color, fontSize = 11, fontWeight = "semibold" }),
  })
end

local function muted(text, extra)
  local props = { text = text, color = "on_surface_variant", fontSize = 12 }
  for k, v in pairs(extra or {}) do
    props[k] = v
  end
  return ui.label(props)
end

local function sideText(side)
  return tr("side." .. (side or "unknown"))
end

local function details(m)
  local rows = {}
  local nowS = noctalia.nowMs() / 1000
  if m.cause ~= nil then
    table.insert(rows, ui.label({ text = m.cause.summary or "", fontWeight = "semibold", maxLines = 3 }))
    table.insert(rows, muted(m.cause.hint or "", { maxLines = 4 }))
    table.insert(rows, muted(tr("panel.cause_line", { code = m.cause.code or "", layer = m.cause.layer or "",
      side = sideText(m.side) })))
  end
  local t = common.timings(m.timings, m.cause and m.cause.layer)
  if t ~= "" then
    table.insert(rows, muted(tr("panel.timings", { timings = t })))
  end
  local values, failures = common.sparkline(m.recent)
  if values ~= nil then
    table.insert(rows, muted(tr("panel.recent", { n = #values })))
    table.insert(rows, ui.graph({ key = "spark-" .. m.id, values = values, values2 = failures, color = "primary",
      color2 = "error", lineWidth = 1.5, fillOpacity = 0.15, height = 36 }))
  end
  if m.since ~= nil and m.status ~= "up" then
    table.insert(rows, muted(tr("panel.since", { status = common.statusLabel(m.status), time = common.clock(m.since),
      ago = common.ago(nowS - m.since) })))
  end
  table.insert(rows, muted(m.target or "", { maxLines = 2 }))
  if m.insecure then
    table.insert(rows, ui.label({ text = tr("panel.insecure"), color = "error", fontSize = 12 }))
  end
  local id = m.id
  local paused = m.paused == true
  local muted = m.muted == true
  table.insert(rows, ui.row({ gap = 6 }, {
    ui.button({ text = tr("panel.check_now"), glyph = "refresh", controlSize = "sm", variant = "secondary",
      onClick = function() simple({ "refresh", id }, tr("panel.busy_refresh"), tr("panel.refresh_sent")) end }),
    ui.button({ text = tr("panel.copy"), glyph = "copy", controlSize = "sm", variant = "secondary",
      onClick = function() copyDiagnosis(id) end }),
    ui.button({ glyph = "pencil", controlSize = "sm", variant = "ghost", tooltip = tr("panel.edit"),
      onClick = function() openEdit(id) end }),
    ui.button({ glyph = paused and "player-play" or "player-pause", controlSize = "sm", variant = "ghost",
      tooltip = paused and tr("panel.resume") or tr("panel.pause"),
      onClick = function()
        simple({ "monitor", paused and "resume" or "pause", id, "--json" }, tr("panel.busy_save"), nil)
      end }),
    ui.button({ glyph = muted and "bell" or "bell-off", controlSize = "sm", variant = "ghost",
      tooltip = muted and tr("panel.unmute") or tr("panel.mute"),
      onClick = function()
        simple({ "monitor", muted and "unmute" or "mute", id, "--json" }, tr("panel.busy_save"), nil)
      end }),
    ui.button({ glyph = "trash", controlSize = "sm", variant = "ghost", tooltip = tr("panel.delete"),
      onClick = function() confirmId, view = id, "confirm"; render() end }),
  }))
  return ui.column({ key = "details-" .. m.id, gap = 4, paddingH = 8, paddingV = 6, radius = 8,
    fill = "surface_variant/0.5" }, rows)
end

local function monitorRow(m)
  local latency = m.timings and m.timings.total
  local nowS = noctalia.nowMs() / 1000
  local open = expanded[m.id] == true
  local id = m.id
  -- No ui.spacer placeholders: a spacer grows and steals space (D26).
  local cells = { pill(m.status), ui.label({ text = m.name or m.id, flexGrow = 1, maxLines = 1 }) }
  if m.insecure then
    table.insert(cells, ui.glyph({ name = "shield-off", size = 14, color = "error" }))
  end
  if m.muted then
    table.insert(cells, ui.glyph({ name = "bell-off", size = 14, color = "on_surface_variant" }))
  end
  local showLatency = type(latency) == "number" and m.status ~= "down"
  table.insert(cells, muted(showLatency and `{math.floor(latency)} ms` or "-"))
  table.insert(cells, muted(m.checkedAt and common.ago(nowS - m.checkedAt) or tr("time.never"), { width = 72, textAlign = "end" }))
  table.insert(cells, ui.glyph({ name = open and "chevron-down" or "chevron-right", size = 14, color = "on_surface_variant" }))
  local flash = highlightId == m.id and noctalia.nowMs() < highlightUntil
  local header = ui.row({ key = "row-" .. m.id, gap = 8, align = "center", paddingV = 4, paddingH = 4, radius = 6,
    fill = flash and "primary/0.18" or "surface/0", onClick = function() expanded[id] = not expanded[id]; render() end }, cells)
  if not open then
    return header
  end
  return ui.column({ key = "item-" .. m.id, gap = 4 }, { header, details(m) })
end

local function banner(glyph, color, text, action)
  local children = {
    ui.glyph({ name = glyph, size = 16, color = color }),
    ui.label({ text = text, flexGrow = 1, maxLines = 4 }),
  }
  if action ~= nil then
    table.insert(children, action)
  end
  return ui.row({ gap = 8, align = "center", padding = 8, radius = 8, fill = color .. "/0.12" }, children)
end

local function summaryText(s)
  local sum = s and s.summary
  if sum == nil then
    return ""
  end
  local parts = {}
  for _, k in ipairs({ "up", "degraded", "down", "unknown", "paused" }) do
    if (sum[k] or 0) > 0 then
      table.insert(parts, `{sum[k]} {common.statusLabel(k)}`)
    end
  end
  return table.concat(parts, " · ")
end

local function header(title, extra)
  local items = { ui.label({ text = title, fontSize = 16, fontWeight = "bold", flexGrow = 1 }) }
  for _, b in ipairs(extra or {}) do
    table.insert(items, b)
  end
  table.insert(items, ui.button({ glyph = "close", variant = "ghost", onClick = function() panel.close() end }))
  return ui.row({ gap = 6, align = "center" }, items)
end

-- statusLine is the transient busy/toast line, or nil.
local function statusLine()
  local text = busy or toast
  if text == nil then
    return nil
  end
  return muted(text, { key = "status-line", maxLines = 3, color = busy and "on_surface_variant" or "primary" })
end

-- frame is the shared panel layout (D26): the header pinned at the top, the
-- body scrolling in all the remaining height, and the pinned parts (cards,
-- action buttons, status line) at the bottom. nil entries are skipped.
local function frame(headerNode, scrollKey, body, pinned)
  local children = { headerNode, ui.scroll({ key = scrollKey, flexGrow = 1, gap = 10, align = "stretch" }, body) }
  for i = 1, pinned.n or #pinned do
    if pinned[i] ~= nil then
      table.insert(children, pinned[i])
    end
  end
  return ui.column({ key = "frame-" .. scrollKey, flexGrow = 1, gap = 10 }, children)
end

local function listView()
  local s = snapshot
  local body = { muted(summaryText(s)) }
  if s == nil or s.engine == "missing" then
    table.insert(body, banner("alert-triangle", "tertiary", tr("panel.missing_binary"),
      ui.button({ text = tr("panel.settings"), controlSize = "sm", onClick = function() noctalia.openSettings() end })))
  elseif s.engine == "stalled" then
    table.insert(body, banner("plug-connected-x", "error", tr("panel.stalled"),
      ui.button({ text = tr("panel.restart"), controlSize = "sm",
        onClick = function() noctalia.state.set("sebab.cmd", { cmd = "restart", at = noctalia.nowMs() }) end })))
  elseif s.offline then
    table.insert(body, banner("wifi-off", "error", tr("panel.offline", { cause = s.offlineCause or "" })))
  end
  if s ~= nil and s.lastError ~= nil and s.lastError.code ~= "SECRET_MISSING" then
    table.insert(body, banner("alert-circle", "error", s.lastError.message or s.lastError.code))
  end
  local rows = 0
  for _, m in ipairs(s and s.monitors or {}) do
    table.insert(body, monitorRow(m))
    rows += 1
  end
  if rows == 0 and s ~= nil and s.engine ~= "missing" then
    table.insert(body, muted(tr("panel.empty"), { maxLines = 3 }))
  end
  return frame(header(tr("panel.title"), {
    ui.button({ glyph = "refresh", variant = "ghost", tooltip = tr("panel.check_all"),
      onClick = function() simple({ "refresh" }, tr("panel.busy_refresh"), tr("panel.refresh_sent")) end }),
    ui.button({ glyph = "plus", variant = "primary", tooltip = tr("panel.add"), onClick = openAdd }),
  }), "list-scroll", body, { statusLine(), n = 1 })
end

local function field(label, control)
  return ui.column({ gap = 4 }, { muted(label), control })
end

local function input(name, value, placeholder, onChange, password)
  return ui.input({ key = `f{form.serial}-{name}`, value = value, placeholder = placeholder, password = password,
    controlSize = "sm", onChange = function(v)
      onChange(v)
      testResult = nil
    end })
end

-- Result card (D27): always shown above the form's buttons, never inside the
-- scroll. States: placeholder, testing, result, error.
local LAYERS = {
  { id = "local", label = "Local" }, { id = "vpn", label = "VPN" }, { id = "dns", label = "DNS", timing = "dns" },
  { id = "tcp", label = "TCP", timing = "tcp" }, { id = "tls", label = "TLS", timing = "tls" },
  { id = "http", label = "HTTP", timing = "ttfb" },
}

local function layerChips(ev)
  local failed = ev.cause and ev.cause.layer
  local degraded = ev.status == "degraded"
  local reachedFailure = false
  local isHTTPS = string.match(ev.monitor and ev.monitor.target or form.target or "", "^https://") ~= nil
  local isTCP = (ev.monitor and ev.monitor.kind) == "tcp"
  local chips = {}
  for _, l in ipairs(LAYERS) do
    local state, value = "ok", ""
    local skipped = (l.id == "vpn" and trim(form.vpn) == "") or (l.id == "tls" and not isHTTPS)
      or (isTCP and (l.id == "tls" or l.id == "http"))
    local ms = l.timing and ev.timings_ms and ev.timings_ms[l.timing]
    if skipped then
      state = "skip"
    elseif reachedFailure then
      state = "none"
    elseif failed == l.id and not degraded then
      state, reachedFailure = "fail", true
    end
    if state == "ok" and type(ms) == "number" then
      value = `{math.floor(ms)} ms`
    elseif state == "ok" and l.id == "dns" and ms == nil then
      value = tr("card.no_dns") -- literal IP: no lookup
    else
      value = tr("card.layer_" .. state)
    end
    local color = ({ ok = "primary", fail = "error", none = "on_surface/0.18", skip = "on_surface/0.08" })[state]
    if degraded and failed == l.id then
      color = "tertiary"
    end
    table.insert(chips, ui.column({ key = "chip-" .. l.id, flexGrow = 1, gap = 3 }, {
      ui.label({ text = l.label, fontSize = 11, fontWeight = "semibold", color = "on_surface_variant" }),
      ui.box({ height = 6, radius = 3, fill = color }),
      ui.label({ text = value, fontSize = 11, color = state == "fail" and "error" or "on_surface_variant", maxLines = 1 }),
    }))
  end
  return ui.row({ key = "card-chips", gap = 6 }, chips)
end

local function cardShell(color, children)
  return ui.column({ key = "result-card", gap = 6, padding = 10, radius = 10, fill = color .. "/0.10",
    border = color .. "/0.35", borderWidth = 1 }, children)
end

local function resultCard()
  if #formErrors > 0 then
    local rows = { ui.row({ gap = 8, align = "center" }, {
      ui.glyph({ name = "alert-circle", size = 16, color = "error" }),
      ui.label({ text = tr("card.error_title"), fontWeight = "semibold", flexGrow = 1 }) }) }
    for _, e in ipairs(formErrors) do
      table.insert(rows, ui.label({ text = e, color = "error", fontSize = 12, maxLines = 4 }))
    end
    return cardShell("error", rows)
  end
  if busy ~= nil and testResult == nil then
    return cardShell("on_surface", { ui.row({ gap = 8, align = "center" }, {
      ui.glyph({ name = "loader-2", size = 16, color = "primary" }),
      ui.label({ text = busy, flexGrow = 1 }) }) })
  end
  if testResult == nil then
    return cardShell("on_surface", { ui.row({ gap = 8, align = "center" }, {
      ui.glyph({ name = "flask", size = 16, color = "on_surface_variant" }),
      ui.label({ text = tr("card.placeholder"), color = "on_surface_variant", flexGrow = 1, maxLines = 2 }) }) })
  end
  local ev = testResult
  local status = ev.status or "unknown"
  local headline, hint = tr("card.all_good"), tr("card.all_good_hint")
  if ev.cause ~= nil then
    headline = ev.cause.summary or ""
    headline = string.upper(string.sub(headline, 1, 1)) .. string.sub(headline, 2)
    hint = ev.cause.hint or ""
  end
  local side = ev.side
  local sideText = side == "yours" and tr("card.side_yours") or side == "theirs" and tr("card.side_theirs")
    or tr("card.side_unclear")
  local top = { pill(status), ui.label({ text = tr("form.test_result"), fontWeight = "semibold", flexGrow = 1 }) }
  if ev.cause ~= nil then
    table.insert(top, muted(sideText))
  end
  local total = ev.timings_ms and ev.timings_ms.total
  if type(total) == "number" then
    table.insert(top, muted(`{math.floor(total)} ms`))
  end
  return cardShell(common.statusStyle(status).color, {
    ui.row({ gap = 8, align = "center" }, top),
    ui.label({ text = headline, fontWeight = "semibold", maxLines = 3 }),
    muted(hint, { maxLines = 4 }),
    layerChips(ev),
  })
end

local function formView()
  local f = form
  local auth = AUTH_TYPES[f.authIndex + 1]
  local fields = {
    field(tr("form.name"), input("name", f.name, tr("form.name_hint"), function(v) f.name = v end)),
    field(tr("form.target"), input("target", f.target, tr("form.target_hint"), function(v) f.target = v end)),
    ui.row({ gap = 8 }, {
      ui.column({ flexGrow = 1, gap = 4 }, { muted(tr("form.interval")),
        input("interval", f.interval, "60", function(v) f.interval = v end) }),
      ui.column({ flexGrow = 1, gap = 4 }, { muted(tr("form.expect")),
        input("expect", f.expect, tr("form.expect_hint"), function(v) f.expect = v end) }),
    }),
    field(tr("form.auth"), ui.select({ options = { tr("form.auth_none"), tr("form.auth_bearer"), tr("form.auth_header"),
      tr("form.auth_basic") }, selectedIndex = f.authIndex, controlSize = "sm", onChange = function(index)
        f.authIndex = tonumber(index) or 0
        testResult = nil
        render()
      end })),
  }
  if auth == "header" then
    table.insert(fields, field(tr("form.header_name"), input("header", f.header, "X-API-Key", function(v) f.header = v end)))
  elseif auth == "basic" then
    table.insert(fields, field(tr("form.username"), input("username", f.username, "", function(v) f.username = v end)))
  end
  if auth ~= "" then
    local hint = tr("form.token_hint")
    if f.secretRef ~= nil and f.authIndex == f.hadAuthIndex then
      hint = tr("form.token_keep", { name = f.secretRef })
    end
    table.insert(fields, field(tr("form.token"), input("token", "", hint, function(v) f.token = v end, true)))
  end
  table.insert(fields, field(tr("form.vpn"), input("vpn", f.vpn, tr("form.vpn_hint"), function(v) f.vpn = v end)))
  table.insert(fields, ui.row({ gap = 8, align = "center" }, {
    ui.toggle({ checked = f.insecure, onChange = function(v) f.insecure = v == "true"; testResult = nil; render() end }),
    ui.label({ text = tr("form.insecure"), flexGrow = 1, maxLines = 2, color = f.insecure and "error" or "on_surface" }),
  }))
  local title = f.mode == "add" and tr("form.add_title") or tr("form.edit_title", { name = f.name })
  local buttons = ui.row({ key = "form-actions", gap = 8, justify = "end" }, {
    ui.button({ text = tr("form.cancel"), variant = "ghost", onClick = cancelForm }),
    ui.button({ text = tr("form.test"), glyph = "flask", variant = "secondary", enabled = busy == nil, onClick = runTest }),
    ui.button({ text = testResult and tr("form.save") or tr("form.test_and_save"), glyph = "device-floppy",
      variant = "primary", enabled = busy == nil, onClick = save }),
  })
  -- The result card replaces the busy line while the form is open (D27).
  return frame(header(title), "form-scroll", fields, { resultCard(), buttons, n = 2 })
end

local function confirmView()
  local m = findMonitor(confirmId)
  local name = m and m.name or confirmId or ""
  return ui.column({ flexGrow = 1, gap = 12 }, {
    header(tr("confirm.title")),
    ui.label({ text = tr("confirm.body", { name = name }), maxLines = 4 }),
    muted(tr("confirm.secret_kept"), { maxLines = 3 }),
    ui.spacer({ flexGrow = 1 }),
    ui.row({ gap = 8, justify = "end" }, {
      ui.button({ text = tr("form.cancel"), variant = "ghost", onClick = function() view = "list"; render() end }),
      ui.button({ text = tr("confirm.delete"), glyph = "trash", variant = "destructive", onClick = function()
        local id = confirmId
        view, confirmId = "list", nil
        simple({ "monitor", "rm", id, "--json" }, tr("panel.busy_save"), tr("panel.deleted", { name = name }))
      end }),
    }),
  })
end

render = function()
  if view == "form" and form ~= nil then
    panel.render(formView())
  elseif view == "confirm" then
    panel.render(confirmView())
  else
    panel.render(listView())
  end
end

noctalia.state.watch(common.STATE_KEY, function(value)
  snapshot = value
  if view == "list" or view == "confirm" then
    render()
  end
end)

function onOpen(context)
  snapshot = noctalia.state.get(common.STATE_KEY)
  toast = nil
  -- A draft kept from an outside-click close reopens as it was (D29).
  if context == "add" and not (view == "form" and form ~= nil and form.mode == "add") then
    openAdd()
    return
  end
  render()
end

-- Panel IPC, for keybinds and scripted checks (the panel must be open):
--   noctalia msg plugin yoockh/sebab:panel all add [url-or-host:port]
--   noctalia msg plugin yoockh/sebab:panel all test | save | cancel
-- `noctalia msg panel-open yoockh/sebab:panel add` opens the add form directly.
function onIpc(event, payload)
  if event == "add" then
    openAdd()
    if payload ~= nil and trim(payload) ~= "" then
      form.target = trim(payload)
      form.name = string.match(form.target, "^%a+://([^/:]+)") or string.match(form.target, "^([^:/]+)") or form.target
      formSerial += 1
      form.serial = formSerial -- fresh input keys so the seeded values show
      render()
    end
  elseif view == "form" and form ~= nil and event == "test" then
    runTest()
  elseif view == "form" and form ~= nil and event == "save" then
    save()
  elseif event == "cancel" or event == "list" then
    cancelForm()
  end
end

-- update runs every second while a "Saved" line or row highlight is showing.
function update()
  local now = noctalia.nowMs()
  local changed = false
  if toast ~= nil and toastUntil > 0 and now >= toastUntil then
    toast, toastUntil, changed = nil, 0, true
  end
  if highlightId ~= nil and now >= highlightUntil then
    highlightId, changed = nil, true
  end
  if toastUntil == 0 and highlightId == nil then
    panel.setWantsSecondTicks(false)
  end
  if changed then
    render()
  end
end

-- The panel closes on outside clicks (D29), so a stray click must not lose the
-- draft: everything typed is kept, except the secret. The token, its handoff
-- file and a test result that used it are always discarded on close, and the
-- inputs get fresh keys so the masked field is rebuilt empty.
function onClose()
  dropHandoff()
  if form ~= nil then
    form.token = ""
    formSerial += 1
    form.serial = formSerial
    if AUTH_TYPES[form.authIndex + 1] ~= "" then
      testResult = nil
    end
  end
  formErrors = {}
end

function onExit(_signal, _reason)
  dropHandoff()
end
