--!nonstrict
-- Mock Noctalia host for plugin tests (plugin_api 32 surface used by Sebab).
-- Props are validated against the exhaustive tables in noctalia.d.luau: the
-- real host logs and skips unknown props, so an unknown prop is a bug here.

H = {
  logs = {},
  notifications = {},
  clipboard = nil,
  runs = {}, -- argv tables passed to runAsync
  streams = {}, -- { cmd = string, onLine = function }
  written = {}, -- path -> contents
  removed = {},
  toggled = {},
  nowMs = 1760187600000,
  config = { binary = "sebab", notifications = true, compact = false },
  files = { sebab = true },
  commands = { sebab = true },
  asyncReplies = {}, -- queue of { exitCode, stdout, stderr }
  pendingAsync = {},
  rendered = nil,
  bar = {},
  decoded = {}, -- json line -> table (filled by the Go harness)
  updateInterval = nil,
}

local function fail(msg)
  error("MOCK: " .. msg, 2)
end

local stateValues, stateWatchers = {}, {}

local function deepcopy(v)
  if type(v) ~= "table" then
    return v
  end
  local out = {}
  for k, x in pairs(v) do
    if type(x) == "function" then
      fail("state values must be plain data, found a function at key " .. tostring(k))
    end
    out[k] = deepcopy(x)
  end
  return out
end

noctalia = {
  log = function(msg)
    table.insert(H.logs, msg)
  end,
  runAsync = function(argv, cb, timeout)
    if type(argv) ~= "table" and type(argv) ~= "string" then
      fail("runAsync expects argv table or string")
    end
    if type(argv) == "table" then
      for i, a in ipairs(argv) do
        if type(a) ~= "string" then
          fail("argv[" .. i .. "] is " .. type(a))
        end
      end
    end
    table.insert(H.runs, argv)
    if cb ~= nil then
      table.insert(H.pendingAsync, { argv = argv, cb = cb })
    end
    return true
  end,
  runStream = function(cmd, onLine)
    if type(cmd) ~= "string" or type(onLine) ~= "function" then
      fail("runStream(cmd: string, onLine: function)")
    end
    table.insert(H.streams, { cmd = cmd, onLine = onLine })
    return true
  end,
  commandExists = function(name)
    return H.commands[name] == true
  end,
  fileExists = function(path)
    return H.files[path] == true or H.written[path] ~= nil
  end,
  expandPath = function(p)
    return (string.gsub(p, "^~", "/home/test"))
  end,
  pluginDir = function()
    return "/plugins/sebab"
  end,
  pluginDataDir = function()
    return "/state/sebab"
  end,
  writeFile = function(path, contents)
    H.written[path] = contents
    return true
  end,
  removeFile = function(path)
    table.insert(H.removed, path)
    H.written[path] = nil
    return true
  end,
  notify = function(title, body)
    table.insert(H.notifications, { kind = "info", title = title, body = body })
  end,
  notifyError = function(title, body)
    table.insert(H.notifications, { kind = "error", title = title, body = body })
  end,
  copyToClipboard = function(text, mime)
    if mime ~= "text/plain" then
      fail("unexpected mime " .. tostring(mime))
    end
    H.clipboard = text
    return true
  end,
  togglePanel = function(id)
    table.insert(H.toggled, id)
  end,
  openSettings = function() end,
  nowMs = function()
    return H.nowMs
  end,
  formatTime = function(pattern, unix)
    if type(pattern) ~= "string" then
      fail("formatTime pattern")
    end
    return "T" .. tostring(unix)
  end,
  timeFormat = function()
    return "{:%H:%M}"
  end,
  getConfig = function(key)
    if H.config[key] == nil then
      fail("undeclared config key " .. key)
    end
    return H.config[key]
  end,
  getSetting = function(_path)
    return true
  end,
  setUpdateInterval = function(ms)
    H.updateInterval = ms
  end,
  tr = function(key, subst)
    local s = key
    for k, v in pairs(subst or {}) do
      s = s .. "|" .. k .. "=" .. tostring(v)
    end
    return s
  end,
  trp = function(key, count, subst)
    return key .. "#" .. tostring(count)
  end,
  json = {
    decode = function(line)
      local v = H.decoded[line]
      if v == nil then
        return nil, "not json"
      end
      return deepcopy(v), nil
    end,
    encode = function(_v)
      return "{}", nil
    end,
  },
  string = {
    trim = function(s)
      return (string.gsub(s, "^%s*(.-)%s*$", "%1"))
    end,
  },
  state = {
    set = function(key, value)
      stateValues[key] = deepcopy(value)
      for _, fn in ipairs(stateWatchers[key] or {}) do
        fn(deepcopy(value))
      end
    end,
    get = function(key)
      return deepcopy(stateValues[key])
    end,
    watch = function(key, fn)
      stateWatchers[key] = stateWatchers[key] or {}
      table.insert(stateWatchers[key], fn)
    end,
  },
}

-- Declarative UI with prop validation ------------------------------------------

local common = { key = 1, width = 1, height = 1, flexGrow = 1, opacity = 1, visible = 1 }
local function with(extra)
  local t = {}
  for k in pairs(common) do
    t[k] = 1
  end
  for _, k in ipairs(extra) do
    t[k] = 1
  end
  return t
end

local flex = { "gap", "padding", "paddingH", "paddingV", "align", "justify", "fill", "radius", "border", "borderWidth",
  "minWidth", "minHeight", "onClick", "onHover", "tooltip" }
local ALLOWED = {
  column = with(flex),
  row = with(flex),
  scroll = with({ "gap", "padding", "paddingH", "paddingV", "align", "justify", "fill", "radius", "border",
    "borderWidth", "stickToBottom", "scrollToBottomRev", "onScroll" }),
  box = with({ "fill", "radius", "border", "borderWidth", "softness", "onClick", "onHover", "tooltip" }),
  label = with({ "text", "fontSize", "color", "fontWeight", "fontFamily", "baseline", "maxWidth", "maxLines", "textAlign" }),
  glyph = with({ "name", "size", "color" }),
  spacer = with({}),
  separator = with({ "thickness", "color", "spacing", "orientation" }),
  button = with({ "text", "glyph", "fontSize", "glyphSize", "variant", "contentAlign", "controlSize", "tooltip",
    "enabled", "selected", "onClick", "onRightClick", "onHover" }),
  input = with({ "value", "placeholder", "fontSize", "controlSize", "password", "multiline", "submitOnEnter",
    "frameVisible", "focus", "enabled", "onChange", "onSubmit" }),
  select = with({ "options", "selectedIndex", "placeholder", "controlSize", "enabled", "onChange" }),
  toggle = with({ "checked", "enabled", "onChange" }),
}
local CALLBACKS = { onClick = 1, onHover = 1, onChange = 1, onSubmit = 1, onRightClick = 1, onScroll = 1 }
local PARENTS = { column = true, row = true, scroll = true }
local VARIANTS = { default = 1, primary = 1, secondary = 1, destructive = 1, outline = 1, ghost = 1 }

local function node(kind)
  return function(props, children)
    props = props or {}
    for k, v in pairs(props) do
      if ALLOWED[kind][k] == nil then
        fail("ui." .. kind .. " has unknown prop '" .. k .. "'")
      end
      if CALLBACKS[k] and type(v) ~= "function" and type(v) ~= "string" then
        fail("ui." .. kind .. "." .. k .. " must be a function or global name")
      end
    end
    if kind == "button" and props.variant ~= nil and VARIANTS[props.variant] == nil then
      fail("bad button variant " .. tostring(props.variant))
    end
    if kind == "input" and props.password and props.multiline then
      fail("password and multiline are exclusive")
    end
    if children ~= nil and not PARENTS[kind] then
      fail("ui." .. kind .. " cannot have children")
    end
    for i, c in ipairs(children or {}) do
      if type(c) ~= "table" or c.type == nil then
        fail("child " .. i .. " of ui." .. kind .. " is not a node")
      end
    end
    return { type = kind, props = props, children = children or {} }
  end
end

ui = {}
for kind in pairs(ALLOWED) do
  ui[kind] = node(kind)
end

panel = {
  render = function(tree)
    if type(tree) ~= "table" or tree.type == nil then
      fail("panel.render needs a node")
    end
    H.rendered = tree
  end,
  close = function()
    H.closed = true
  end,
}

barWidget = {
  setText = function(t)
    if type(t) ~= "string" then
      fail("setText needs a string")
    end
    H.bar.text = t
  end,
  setGlyph = function(g)
    H.bar.glyph = g
  end,
  setGlyphColor = function(c)
    H.bar.glyphColor = c
  end,
  setColor = function(c)
    H.bar.color = c
  end,
  setTooltip = function(t)
    if type(t) == "table" then
      for i, row in ipairs(t) do
        if type(row) ~= "table" or type(row[1]) ~= "string" or type(row[2]) ~= "string" then
          fail("tooltip row " .. i .. " must be { key, value } strings")
        end
      end
    elseif type(t) ~= "string" and t ~= nil then
      fail("tooltip must be a string or rows")
    end
    H.bar.tooltip = t
  end,
}

-- Test helpers ------------------------------------------------------------------

-- find returns every node in the tree whose props match pred.
function H.find(tree, pred, out)
  out = out or {}
  if tree == nil then
    return out
  end
  if pred(tree) then
    table.insert(out, tree)
  end
  for _, c in ipairs(tree.children or {}) do
    H.find(c, pred, out)
  end
  return out
end

-- text concatenates every label/button text in the tree.
function H.text(tree)
  local parts = {}
  H.find(tree, function(n)
    if n.props.text ~= nil then
      table.insert(parts, tostring(n.props.text))
    end
    if n.props.placeholder ~= nil then
      table.insert(parts, tostring(n.props.placeholder))
    end
    return false
  end)
  return table.concat(parts, "\n")
end

function H.click(tree, pred)
  local hits = H.find(tree, function(n)
    return n.props.onClick ~= nil and pred(n)
  end)
  if #hits == 0 then
    fail("no clickable node matched")
  end
  local cb = hits[1].props.onClick
  if type(cb) == "string" then
    fail("named callbacks are not used by sebab: " .. cb)
  else
    cb()
  end
end

-- reply completes the oldest pending runAsync call.
function H.reply(exitCode, stdout, stderr)
  local p = table.remove(H.pendingAsync, 1)
  if p == nil then
    fail("no pending runAsync call to reply to")
  end
  p.cb({ exitCode = exitCode, stdout = stdout or "", stderr = stderr or "", timedOut = false,
    stdoutTruncated = false, stderrTruncated = false })
  return p.argv
end

function H.assert(cond, msg)
  if not cond then
    error("ASSERT: " .. msg, 2)
  end
end

function H.contains(hay, needle, msg)
  if string.find(hay, needle, 1, true) == nil then
    error("ASSERT: " .. (msg or "") .. " expected to find '" .. needle .. "' in:\n" .. hay, 2)
  end
end

local modules = {}
function H.module(path, fn)
  modules[path] = fn
end
require = function(path)
  local m = modules[path]
  if m == nil then
    fail("unknown module " .. tostring(path))
  end
  if type(m) == "function" then
    modules[path] = m()
    m = modules[path]
  end
  return m
end
