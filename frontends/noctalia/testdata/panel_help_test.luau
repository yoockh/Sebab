--!nonstrict
-- Driver: panel.luau help view and field hints (D30).

local function inputNamed(name)
  return H.find(H.rendered, function(n)
    return n.type == "input" and type(n.props.key) == "string" and string.match(n.props.key, "%-" .. name .. "$") ~= nil
  end)[1]
end
local function clickKey(key)
  H.click(H.rendered, function(n) return n.props.key == key end)
end
local function helpText()
  local md = H.find(H.rendered, function(n) return n.type == "markdown" end)
  H.assert(#md == 1, "help markdown present")
  return md[1].props.text
end

noctalia.state.set("sebab", { engine = "running", binary = "sebab", monitors = {} })
onOpen(nil)
clickKey("help-button")
local t = helpText()
for _, k in ipairs({ "intro", "add", "layers", "side", "tokens" }) do
  H.contains(t, "help." .. k .. "_title")
  H.contains(t, "help." .. k .. "_body")
end
H.contains(t, "help.config_body|path=~/.config/sebab/config.toml", "config path shown generically")
H.assert(#H.find(H.rendered, function(n) return n.props.key == "help-button" end) == 0, "no help button inside help")
clickKey("help-back")
H.contains(H.text(H.rendered), "panel.title", "back to the list")

-- From the form: the draft survives a trip to help.
onOpen("add")
inputNamed("name").props.onChange("Payments API")
local fieldHelp = H.text(H.rendered)
for _, k in ipairs({ "name", "target", "expect", "interval", "auth", "vpn" }) do
  H.contains(fieldHelp, "fieldhelp." .. k, "hint for " .. k)
end
H.find(H.rendered, function(n) return n.type == "select" end)[1].props.onChange("2", "header")
fieldHelp = H.text(H.rendered)
H.contains(fieldHelp, "fieldhelp.header")
H.contains(fieldHelp, "fieldhelp.token")
onIpc("help", nil)
H.assert(#H.find(H.rendered, function(n) return n.type == "markdown" end) == 1, "ipc help opens help")
clickKey("help-back")
H.assert(inputNamed("name").props.value == "Payments API", "draft kept after help")
print("panel help ok")
