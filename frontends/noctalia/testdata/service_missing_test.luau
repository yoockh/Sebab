--!nonstrict
-- Driver: service.luau with no sebab binary installed. The mock host is set up
-- before the entry runs (see PRELUDE in plugin_test.go).

H.assert(#H.streams == 0, "no stream without a binary")
update()
local s = noctalia.state.get("sebab")
H.assert(s.engine == "missing", "engine should be missing, got " .. tostring(s.engine))
H.assert(s.binary == nil, "no binary")
print("service missing ok")
