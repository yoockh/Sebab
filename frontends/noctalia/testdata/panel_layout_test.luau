--!nonstrict
-- Driver: panel.luau layout (D26). Header pinned, one growing scroll, pinned footer,
-- and no ui.spacer anywhere in the list or form (a spacer steals height).

local function checkFrame(where, lastKey)
  local root = H.rendered
  H.assert(root.type == "column" and root.props.flexGrow == 1, where .. ": root must grow")
  H.assert(root.children[1].type == "row", where .. ": header first")
  local scroll = root.children[2]
  H.assert(scroll.type == "scroll" and scroll.props.flexGrow == 1, where .. ": scroll second and growing")
  for i, c in ipairs(root.children) do
    if i ~= 2 then
      H.assert(c.props.flexGrow == nil, where .. ": only the scroll may grow (child " .. i .. ")")
    end
  end
  local spacers = H.find(root, function(n) return n.type == "spacer" end)
  H.assert(#spacers == 0, where .. ": no ui.spacer placeholders")
  if lastKey ~= nil then
    local found = false
    for i = 3, #root.children do
      if root.children[i].props.key == lastKey then
        found = true
      end
    end
    H.assert(found, where .. ": " .. lastKey .. " pinned below the scroll")
  end
end

noctalia.state.set("sebab", { engine = "running", binary = "sebab", monitors = {
  { id = "a", name = "A", status = "up", timings = { total = 12 }, checkedAt = 1 },
  { id = "b", name = "B", status = "down", insecure = true, muted = true, timings = { total = 40 }, checkedAt = 1 },
} })
onOpen(nil)
checkFrame("list", nil)
local rowB = H.find(H.rendered, function(n) return n.props.key == "row-b" end)[1]
H.assert(string.find(H.text(rowB), "40 ms", 1, true) == nil, "a down monitor shows no latency")
onOpen("add")
checkFrame("form", "form-actions")
checkFrame("form", "result-card")
print("panel layout ok")
