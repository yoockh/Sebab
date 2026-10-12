--!nonstrict
-- Spike: show the number of events received from the engine.

local function render(v)
  barWidget.setGlyph("heart-rate-monitor")
  if v == nil then
    barWidget.setText("...")
  else
    barWidget.setText(tostring(v.beats))
    barWidget.setTooltip("last " .. tostring(v.type) .. " at " .. tostring(v.ts))
  end
end

noctalia.state.watch("spike", render)
render(noctalia.state.get("spike"))
