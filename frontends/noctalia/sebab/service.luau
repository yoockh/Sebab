--!nonstrict
-- Spike: run `sebab watch` and publish the latest heartbeat time to shared state.

local function shellQuote(s)
  return "'" .. string.gsub(s, "'", "'\\''") .. "'"
end

local beats = 0

local function onLine(line)
  local ev = noctalia.json.decode(line)
  if type(ev) ~= "table" then
    return
  end
  if ev.type == "hello" or ev.type == "heartbeat" then
    beats += 1
    noctalia.state.set("spike", { type = ev.type, ts = ev.ts, beats = beats })
  end
end

local function resolveBinary()
  local configured = noctalia.getConfig("binary") or "sebab"
  if noctalia.commandExists(configured) or noctalia.fileExists(configured) then
    return configured
  end
  -- Development checkout: frontends/noctalia/sebab -> ../../../bin/sebab
  local dir = noctalia.pluginDir()
  if dir ~= nil then
    local dev = dir .. "/../../../bin/sebab"
    if noctalia.fileExists(dev) then
      return dev
    end
  end
  return nil
end

local binary = resolveBinary()
noctalia.log("sebab spike: binary = " .. tostring(binary))
if binary == nil then
  return
end
local started = noctalia.runStream(shellQuote(binary) .. " watch --format jsonl --heartbeat 2s", onLine)
noctalia.log("sebab spike: stream started = " .. tostring(started))

function onIpc(event, _payload)
  if event == "dump" then
    local v = noctalia.state.get("spike")
    noctalia.log("sebab spike state: " .. tostring(noctalia.json.encode(v)))
  end
end
