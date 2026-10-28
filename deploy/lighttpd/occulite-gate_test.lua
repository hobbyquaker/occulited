-- openccu-lite: the session gate's match rules, exercised without lighttpd.
--
-- The gate itself (occulite-gate.lua) needs lighttpd's mod_magnet to run; its patterns do
-- not. Keep the functions below in step with the gate. The second half runs the gate script
-- itself against a stand-in for lighty.r (B-94). Run it with Lua 5.3 or later (the gate's address
-- check uses the integer bit operators; the fork's lighttpd is built with Lua 5.4):
--
--   lua deploy/lighttpd/occulite-gate_test.lua      (the box has /usr/bin/lua, 5.4)
--
-- and syntax-check the gate itself with:  luac -p deploy/lighttpd/occulite-gate.lua

-- the match rules of the gate; `live` stands in for the session directory. The cookie is the gate
-- cookie (openccu-lite task 259): occulite_gate / __Secure-occulite_gate at Path=/addons/; the
-- API's occulite_session is scoped to /api and is no credential here.
local COOKIE_PATTERNS = {
  "[;,%s]__Secure%-occulite_gate=([%w@]+)",
  "[;,%s]occulite_gate=([%w@]+)",
}
-- task 125: a session id is 26 characters of base32, the legacy alias ten alphanumerics
local SID_PATTERN = "^@?(" .. string.rep("[A-Z2-7]", 26) .. ")@?$"
local ALIAS_PATTERN = "^@?(" .. string.rep("%w", 10) .. ")@?$"
local function shape(sid)
  if sid == nil then return nil end
  return sid:match(SID_PATTERN)
end
local function alias_shape(sid)
  if sid == nil then return nil end
  return sid:match(ALIAS_PATTERN)
end
-- cookie_sid is the gate's loop: the first cookie of either name whose sid is live
local function cookie_sid(c, live)
  c = ";" .. c
  for _, pattern in ipairs(COOKIE_PATTERNS) do
    for sid in c:gmatch(pattern) do
      local s = shape(sid)
      if s ~= nil and (live == nil or live[s]) then return s end
    end
  end
  return nil
end
local function query_sid(q)
  return q:match("^sid=([%w@]+)") or q:match("&sid=([%w@]+)")
end
local fails = 0
local function want(got, expect, what)
  if got ~= expect then
    print(string.format("FAIL  %-62s got %s want %s", what, tostring(got), tostring(expect)))
    fails = fails + 1
  else
    print(string.format("ok    %-62s %s", what, tostring(got)))
  end
end
local ID = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
want(cookie_sid("occulite_gate=" .. ID), ID, "HTTP cookie alone")
want(cookie_sid("__Secure-occulite_gate=" .. ID), ID, "HTTPS cookie alone")
want(cookie_sid("theme=dark; occulite_gate=" .. ID), ID, "HTTP cookie after another")
want(cookie_sid("theme=dark; __Secure-occulite_gate=" .. ID), ID, "HTTPS cookie after another")
want(cookie_sid("occulite_gate=@" .. ID .. "@"), ID, "cookie, @-wrapped")
want(cookie_sid("a=1,occulite_gate=" .. ID), ID, "comma-separated cookie")
want(cookie_sid("a=1,__Secure-occulite_gate=" .. ID), ID, "comma-separated HTTPS cookie")
want(cookie_sid("x_occulite_gate=" .. ID), nil, "a cookie merely ending in the name")
want(cookie_sid("evilocculite_gate=" .. ID), nil, "a cookie name glued to it")
want(cookie_sid("evil-occulite_gate=" .. ID), nil, "a cookie name glued to it with a dash")
want(cookie_sid("x__Secure-occulite_gate=" .. ID), nil, "a cookie merely ending in the HTTPS name")
want(cookie_sid("__Secure-occulite_gatex=" .. ID), nil, "a cookie name that only starts with it")
want(cookie_sid("__Secure_occulite_gate=" .. ID), nil, "a look-alike of the HTTPS name")
want(cookie_sid("occulite_gate=SHORT"), nil, "a sid of the wrong length")
want(cookie_sid("occulite_gate=" .. ID .. "A"), nil, "a sid that is too long")
want(cookie_sid("occulite_gate=abcdefghijklmnopqrstuvwxyz"), nil, "a sid in lower case is no base32 id")
want(cookie_sid("occulite_gate=ABCDEFGHIJKLMNOPQRSTUVWXY0"), nil, "a 0 or 1 is no base32 letter")
want(cookie_sid("occulite_gate=ABCDEFGHIJ"), nil, "the ten-character shape is no session id (task 125)")
want(cookie_sid("occulite_gate=@ABCDEFGHIJ@"), nil, "an @-wrapped alias is no cookie either")
want(shape(ID), ID, "the session id's shape")
want(alias_shape(ID), nil, "a session id is no alias")
want(alias_shape("@AbCd012345@"), "AbCd012345", "the alias's shape, @-wrapped")
want(alias_shape("AbCd012345"), "AbCd012345", "the alias's shape, bare")
want(alias_shape("AbCd01234"), nil, "a nine-character alias")
want(alias_shape("AbCd0123456"), nil, "an eleven-character alias")
-- both names, and stale ones in front of a live one
local LIVEHTTP, LIVEHTTPS, STALE = "LIVEHTTPAAAAAAAAAAAAAAAAAA", "LIVEHTTPSAAAAAAAAAAAAAAAAA", "STALEAAAAAAAAAAAAAAAAAAAAA"
local live = {[LIVEHTTPS] = true, [LIVEHTTP] = true}
want(cookie_sid("occulite_gate=" .. LIVEHTTP .. "; __Secure-occulite_gate=" .. LIVEHTTPS, live), LIVEHTTPS, "both live: the HTTPS cookie first")
want(cookie_sid("occulite_gate=" .. STALE .. "; __Secure-occulite_gate=" .. LIVEHTTPS, live), LIVEHTTPS, "a stale HTTP cookie, a live HTTPS one")
want(cookie_sid("__Secure-occulite_gate=" .. STALE .. "; occulite_gate=" .. LIVEHTTP, live), LIVEHTTP, "a stale HTTPS cookie, a live HTTP one")
want(cookie_sid("occulite_gate=" .. STALE .. "; occulite_gate=" .. LIVEHTTP, live), LIVEHTTP, "one name twice, the first stale")
want(cookie_sid("occulite_gate=" .. STALE .. "; __Secure-occulite_gate=STALEBBBBBBBBBBBBBBBBBBBBB", live), nil, "only stale cookies")
want(cookie_sid("x_occulite_gate=" .. LIVEHTTP, live), nil, "a live sid under a look-alike name")
want(cookie_sid("occulite_session=" .. LIVEHTTP, live), nil, "a live sid under the API cookie's name (task 259)")
want(shape(query_sid("sid=@" .. ID .. "@")), ID, "query, first parameter")
want(shape(query_sid("x=1&sid=@" .. ID .. "@")), ID, "query, later parameter")
want(alias_shape(query_sid("x=1&sid=@AbCd012345@")), "AbCd012345", "query, an alias")
want(query_sid("mysid=@" .. ID .. "@"), nil, "a query parameter ending in sid (B-21)")
want(shape("../../etc/passwd"), nil, "a traversal never survives the shape check")
want(alias_shape("../../etc/passwd"), nil, "nor the alias's")

-- The gate script itself (B-94: the session header), run against a stand-in for lighttpd's
-- lighty.r and for the session directories. The stand-in's request headers behave like lighttpd's:
-- names are looked up without regard to case, the first spelling a name arrived in is kept, and a
-- removed header stays as an empty entry that pairs() still returns.
local script_dir = (arg and arg[0] and arg[0]:match("^(.*)/[^/]*$")) or "."
local gate_path = script_dir .. "/occulite-gate.lua"

local function header_table(initial, broken)
  local entries = {} -- { {name=, value=}, ... } in arrival order
  local function find(k)
    local lk = k:lower()
    for _, e in ipairs(entries) do
      if e.name:lower() == lk then return e end
    end
  end
  local t = {}
  local mt = {}
  mt.__index = function(_, k)
    local e = find(k)
    if e == nil or e.value == "" then return nil end
    return e.value
  end
  mt.__newindex = function(_, k, v)
    if broken == "remove" and v == nil then return end
    if broken == "set" and v ~= nil then return end
    local e = find(k)
    if e == nil then
      if v ~= nil then entries[#entries + 1] = { name = k, value = v } end
      return
    end
    e.value = v or ""
  end
  mt.__pairs = function()
    local i = 0
    return function()
      i = i + 1
      local e = entries[i]
      if e then return e.name, e.value end
    end, t, nil
  end
  setmetatable(t, mt)
  for _, h in ipairs(initial) do
    local e = find(h[1])
    if e then e.value = e.value .. ", " .. h[2] else entries[#entries + 1] = { name = h[1], value = h[2] } end
  end
  -- what a backend receives: every entry that still has a value
  local function sent()
    local out = {}
    for _, e in ipairs(entries) do
      if e.value ~= "" then out[#out + 1] = e end
    end
    return out
  end
  return t, sent
end

-- The session directory names a session's file by the SHA-256 of its id (B-102), the alias
-- directory an alias's by the SHA-256 of the alias (task 125). lighttpd's lighty.c.md answers the
-- digest in upper-case hex; stock Lua has no SHA-256, so the stand-in knows the digests of the ids
-- the cases use (printf %s <id> | sha256sum) and answers any other input with a digest no session
-- has.
local LIVEQUERY, FORGED, REVOKED = "LIVEQUERYAAAAAAAAAAAAAAAAA", "FORGEDAAAAAAAAAAAAAAAAAAAA", "REVOKEDAAAAAAAAAAAAAAAAAAA"
local SHA256 = {
  [""] = "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855",
  [LIVEHTTP] = "617F9A61A0DD623E1298BA2F516F8FC6FC0ED0542FBD018FB04755495C4F88AA",
  [LIVEHTTPS] = "F1E9D25F607024FB2E802B55C05FE72C4AC22F20ABBCE935D122B5F04CD0E866",
  [LIVEQUERY] = "907048D2362C800C5EA0869807FB860E42AA61010032348582D017C74F20F7B2",
  [STALE] = "9BC19D9323AAB9DEB0A3977709AFC1628A348F2FC2A145E0F736CAD854514D5B",
  [FORGED] = "EFDFF1AC92EBED3E5FBE145FF959D0DB66DDD37317141896AFC142D6A19CA987",
  [REVOKED] = "7D33505C330FF0F9C6FBAAB330F0F137953B4B8CE272BBAADD36F389DFAF89D2",
  aliasLive1 = "15B07E9F685C0E620D528C86C0C5CDE7ACD607FDE09D7DD375062D912EE63A44",
  aliasGone1 = "2585CC7B33FF91B9237275C6ED5B36F22957CD33C3CB1BE6B5AFCCCF9CCA4634",
  anonymous0 = "CC51685EB0DB2E1429B9C01079E8B34502CB610031BB107548D8520A5E44C1E1",
}
local md_inputs = {}
local function md_stub(algo, data)
  md_inputs[#md_inputs + 1] = algo .. ":" .. data
  if algo ~= "sha256" then return nil end
  return SHA256[data] or string.rep("0", 64)
end
-- key is a session's (or an alias's) file name as occulited writes it
local function key(id) return SHA256[id]:lower() end

-- run_gate runs the gate on one request: headers as {name, value} pairs, the query string, the
-- names of the files in the session directory, lighty.c (nil: one with md_stub, false: none), the
-- names of the files in the alias directory, and the request's path (default /addons/x/).
-- It answers the gate's return value and the session headers a backend would receive, as
-- "name=value" joined by " | " ("-" for none).
-- task 213: NEXT carries a case's method, host, and the tables that catch the answer's headers and
-- the gate's log lines
NEXT = {}
local function run_gate(headers, query, live_files, broken, lighty_c, alias_files, path)
  local req_header, sent = header_table(headers, broken)
  if lighty_c == nil then lighty_c = { md = md_stub } end
  local lighty_stub = {
    r = {
      req_header = req_header,
      req_attr = { ["uri.query"] = query, ["uri.path"] = path or "/addons/x/", ["request.method"] = NEXT.method or "GET", ["uri.authority"] = NEXT.authority or "box.lan", ["request.remote-addr"] = NEXT.remote or "192.168.1.50" },
      resp_header = NEXT.resp_header or {},
      resp_body = { set = function(_, body) NEXT.body = body and body[1] end },
    },
    c = lighty_c or nil,
  }
  -- task 307: the token mirror's files (NEXT.tokens: file name → content), read line by line
  local io_stub = {
    open = function(p)
      local name = p:match("^/var/run/occulite/sessions/(.*)$")
      if name and live_files[name] then return { close = function() end } end
      local alias = p:match("^/var/run/occulite/legacy%-sessions/(.*)$")
      if alias and alias_files and alias_files[alias] then return { close = function() end } end
      local tok = p:match("^/var/run/occulite/gate%-tokens/(.*)$")
      if tok and NEXT.tokens and NEXT.tokens[tok] then
        local content = NEXT.tokens[tok]
        return { close = function() end, lines = function() return (content .. "\n"):gmatch("([^\n]*)\n") end }
      end
      -- occulited task 125: the public views' mirror (NEXT.public: segment → true)
      local pub = p:match("^/var/run/occulite/public%-views/(.*)$")
      if pub and NEXT.public and NEXT.public[pub] then return { close = function() end } end
      return nil
    end,
  }
  local env = setmetatable({ lighty = lighty_stub, io = io_stub, print = NEXT.print or print }, { __index = _G })
  local chunk, err = loadfile(gate_path, "t", env)
  if not chunk then
    print("FAIL  cannot load the gate: " .. tostring(err))
    os.exit(1)
  end
  local rc = chunk()
  local seen = {}
  for _, e in ipairs(sent()) do
    local cgi = (e.name:upper():gsub("[^%w]", "_"))
    -- the session header as before; the identity headers of task 307 only when a case asks (NEXT.identity)
    if cgi == "X_OCCULITE_SESSION" or (NEXT.identity and (cgi == "X_OCCULITE_AUTH" or cgi == "X_OCCULITE_TOKEN")) then seen[#seen + 1] = e.name .. "=" .. e.value end
  end
  return rc, (#seen > 0 and table.concat(seen, " | ") or "-")
end

local LIVE = { [key(LIVEHTTP)] = true, [key(LIVEHTTPS)] = true, [key(LIVEQUERY)] = true }
local ALIASES = { [key("aliasLive1")] = true, [key("anonymous0")] = true }
local function case(what, headers, query, want_rc, want_seen, broken, live, lighty_c, aliases, path)
  local rc, seen = run_gate(headers, query, live or LIVE, broken, lighty_c, aliases or ALIASES, path)
  want(rc, want_rc, what .. ": answer")
  if want_rc == 0 then
    want(seen, want_seen, what .. ": header")
  elseif want_rc ~= 500 then
    want(seen, "-", what .. ": nothing to pass on") -- a rejected request carries none either
  end
end
local FORGED_HDR = { "X-Occulite-Session", FORGED }
case("no credential, a browser", { { "Accept", "text/html" } }, nil, 302)
case("no credential, forged header, a browser", { { "Accept", "text/html" }, FORGED_HDR }, nil, 302)
case("no credential, forged header, an API caller", { FORGED_HDR }, nil, 401)
case("a stale cookie and a forged header", { { "Cookie", "occulite_gate=" .. STALE }, FORGED_HDR }, nil, 401)
-- task 259: the API's session cookie, should a browser still send it here, opens nothing
case("a live id under the API's cookie name", { { "Cookie", "occulite_session=" .. LIVEHTTP } }, nil, 401)
case("a live id under the API's HTTPS cookie name", { { "Cookie", "__Secure-occulite_session=" .. LIVEHTTPS } }, nil, 401)
case("a live id under a look-alike cookie, forged header", { { "Cookie", "x_occulite_gate=" .. LIVEHTTP }, FORGED_HDR }, nil, 401)
case("?sid= of a stale session, forged header", { FORGED_HDR }, "sid=@" .. STALE .. "@", 401)
case("an HTTP cookie", { { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTP)
case("an HTTPS cookie, @-wrapped", { { "Cookie", "theme=dark; __Secure-occulite_gate=@" .. LIVEHTTPS .. "@" } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTPS)
case("a live cookie and a forged header", { FORGED_HDR, { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTP)
case("a live cookie, the forged header in lower case", { { "x-occulite-session", FORGED }, { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 0, "x-occulite-session=" .. LIVEHTTP)
case("a live cookie, forged under both cases", { { "X-OCCULITE-SESSION", FORGED }, { "x-occulite-session", "FORGEDBBBBBBBBBBBBBBBBBBBB" }, { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 0, "X-OCCULITE-SESSION=" .. LIVEHTTP)
case("a live cookie, forged as X_Occulite_Session and x.occulite.session", { { "X_Occulite_Session", FORGED }, { "x.occulite.session", "FORGEDBBBBBBBBBBBBBBBBBBBB" }, { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTP)
case("a header that only resembles it stays", { { "X-Occulite-Sessions", "keep" }, { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTP)
case("?sid=@..@ with the session id", {}, "sid=@" .. LIVEQUERY .. "@", 0, "X-Occulite-Session=" .. LIVEQUERY)
case("?sid= and a forged header", { FORGED_HDR }, "x=1&sid=" .. LIVEQUERY, 0, "X-Occulite-Session=" .. LIVEQUERY)
case("a stale cookie beside a live ?sid=", { { "Cookie", "occulite_gate=" .. STALE }, FORGED_HDR }, "sid=@" .. LIVEQUERY .. "@", 0, "X-Occulite-Session=" .. LIVEQUERY)
case("a stale HTTPS cookie before a live HTTP one", { { "Cookie", "__Secure-occulite_gate=" .. STALE .. "; occulite_gate=" .. LIVEHTTP } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTP)
-- fail closed: a lighttpd that does not remove or does not set the header
case("the removal does not take, with a live cookie", { FORGED_HDR, { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 500, nil, "remove")
case("the removal does not take, no credential", { FORGED_HDR }, nil, 500, nil, "remove")
case("the header cannot be set", { { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, 500, nil, "set")
-- openccu-lite B-230: a client-sent X-Forwarded-For (and the other forwarding headers) never
-- reaches the addon, in any spelling; lighttpd appends its own element after the gate
do
  local function forwarding(headers)
    local req_header, sent = header_table(headers)
    local env = setmetatable({ lighty = { r = { req_header = req_header, req_attr = { ["uri.path"] = "/addons/x/", ["request.method"] = "GET", ["uri.authority"] = "box.lan" }, resp_header = {}, resp_body = { set = function() end } }, c = { md = md_stub } }, io = { open = function() return nil end }, print = print }, { __index = _G })
    local rc = loadfile(gate_path, "t", env)()
    local left = {}
    for _, e in ipairs(sent()) do left[#left + 1] = e.name end
    return rc, table.concat(left, ",")
  end
  local rc, left = forwarding({ { "X-Forwarded-For", "127.0.0.1" }, { "x-forwarded-proto", "https" }, { "X-Forwarded-Host", "evil.example" }, { "Forwarded", "for=127.0.0.1" }, { "X_Forwarded_For", "10.99.1.1" }, { "Accept", "text/html" } })
  want(rc, 302, "forged forwarding headers, no session: answer")
  want(left, "Accept", "forged forwarding headers: all removed, the rest kept")
  rc, left = forwarding({ { "X-Forwarded-Fork", "keep" } })
  want(left, "X-Forwarded-Fork", "a header that only resembles one stays")
end
do -- lighttpd before 1.4.60: no lighty.r
  local chunk = loadfile(gate_path, "t", setmetatable({ lighty = {} }, { __index = _G }))
  want(chunk(), 500, "no lighty.r: the gate fails closed")
end

-- B-102: the session directory names a file by the SHA-256 of the id, never by the id
local HC_LIVE = { { "Cookie", "occulite_gate=" .. LIVEHTTP } }
case("a file named by the id itself (the old mirror) is no session", HC_LIVE, nil, 401, nil, nil, { [LIVEHTTP] = true })
case("a file named by the upper-case digest is no session", HC_LIVE, nil, 401, nil, nil, { [SHA256[LIVEHTTP]] = true })
case("a live session's digest as the cookie", { { "Cookie", "occulite_gate=" .. key(LIVEHTTP) } }, nil, 401)
case("a live session's digest as ?sid=", {}, "sid=@" .. key(LIVEQUERY) .. "@", 401)
case("a forged id", { { "Cookie", "occulite_gate=" .. FORGED }, FORGED_HDR }, nil, 401)
case("a forged id as ?sid=", {}, "sid=@" .. FORGED .. "@", 401)
do -- a session that ends: the same cookie before and after its file goes
  local live = { [key(REVOKED)] = true }
  case("a session before it ends", { { "Cookie", "occulite_gate=" .. REVOKED } }, nil, 0, "X-Occulite-Session=" .. REVOKED, nil, live)
  live[key(REVOKED)] = nil
  case("the same cookie once the session ended", { { "Cookie", "occulite_gate=" .. REVOKED } }, nil, 401, nil, nil, live)
  case("the same id as ?sid= once the session ended", {}, "sid=@" .. REVOKED .. "@", 401, nil, nil, live)
end
do -- the gate hashes the bare id with sha256: not the @-wrapped one, not another algorithm
  md_inputs = {}
  case("an @-wrapped HTTPS cookie", { { "Cookie", "__Secure-occulite_gate=@" .. LIVEHTTPS .. "@" } }, nil, 0, "X-Occulite-Session=" .. LIVEHTTPS)
  local seen = table.concat(md_inputs, " ")
  want(seen:find("sha256:" .. LIVEHTTPS, 1, true) ~= nil and seen:find("@", 1, true) == nil and seen:find("md5", 1, true) == nil, true, "hashed: " .. seen)
end
-- a lighttpd whose lighty.c.md has no sha256 (built without a crypto library), or no lighty.c at all
case("lighty.c.md without sha256: the gate fails closed", HC_LIVE, nil, 500, nil, nil, nil, { md = function() return nil end })
case("no lighty.c: the gate fails closed", HC_LIVE, nil, 500, nil, nil, nil, false)
case("no lighty.c, no credential: the gate fails closed", { { "Accept", "text/html" } }, nil, 500, nil, nil, nil, false)

-- Task 125 (D-77): the legacy alias, ten characters, from ?sid= under /addons/ - looked up by its
-- SHA-256 in the alias directory, never in the session directory, never from a cookie, and not
-- outside /addons/. An accepted alias travels in the session header as the credential the gate
-- validated.
case("an alias as ?sid=@..@", {}, "sid=@aliasLive1@", 0, "X-Occulite-Session=aliasLive1")
case("an alias as bare ?sid=", {}, "x=1&sid=aliasLive1", 0, "X-Occulite-Session=aliasLive1")
case("an alias as ?sid= with a forged header", { FORGED_HDR }, "sid=@aliasLive1@", 0, "X-Occulite-Session=aliasLive1")
case("a stale cookie beside a live alias", { { "Cookie", "occulite_gate=" .. STALE } }, "sid=@aliasLive1@", 0, "X-Occulite-Session=aliasLive1")
case("a live cookie wins over the alias", HC_LIVE, "sid=@aliasLive1@", 0, "X-Occulite-Session=" .. LIVEHTTP)
case("auth off: the fixed alias", {}, "sid=@anonymous0@", 0, "X-Occulite-Session=anonymous0")
case("an alias nobody has", {}, "sid=@aliasGone1@", 401)
case("an alias as the HTTP cookie", { { "Cookie", "occulite_gate=aliasLive1" } }, nil, 401)
case("an alias as the HTTPS cookie, @-wrapped", { { "Cookie", "__Secure-occulite_gate=@aliasLive1@" } }, nil, 401)
case("an alias whose file is in the session directory is no session", {}, "sid=@aliasLive1@", 401, nil, nil, { [key("aliasLive1")] = true }, nil, {})
case("a session id whose file is in the alias directory is no alias", {}, "sid=@" .. LIVEQUERY .. "@", 401, nil, nil, {}, nil, { [key(LIVEQUERY)] = true })
case("a file named by the alias itself is no alias", {}, "sid=@aliasLive1@", 401, nil, nil, nil, nil, { aliasLive1 = true })
case("an alias outside /addons/", {}, "sid=@aliasLive1@", 401, nil, nil, nil, nil, nil, "/api/auth/v1/state")
case("an alias on a path that only starts like /addons/", {}, "sid=@aliasLive1@", 401, nil, nil, nil, nil, nil, "/addonsx/y")
case("the session id outside /addons/ still passes", {}, "sid=@" .. LIVEQUERY .. "@", 0, "X-Occulite-Session=" .. LIVEQUERY, nil, nil, nil, nil, "/api/auth/v1/state")
case("an alias under an addon's CGI path", {}, "sid=@aliasLive1@", 0, "X-Occulite-Session=aliasLive1", nil, nil, nil, nil, "/addons/mosquitto/settings.cgi")
do -- an alias that ends with its session
  local aliases = { [key("aliasLive1")] = true }
  case("the alias before its session ends", {}, "sid=@aliasLive1@", 0, "X-Occulite-Session=aliasLive1", nil, nil, nil, aliases)
  aliases[key("aliasLive1")] = nil
  case("the same alias once the session ended", {}, "sid=@aliasLive1@", 401, nil, nil, nil, nil, aliases)
end

-- openccu-lite task 213: cross-site requests with the cookie. Refused: a state-changing request
-- from another site (Sec-Fetch-Site cross-site or same-site, else an Origin or Referer of another
-- host), and any request from elsewhere but a top-level navigation, which opens the page without
-- its query. Passing: same-origin, none (a typed URL), no header at all on a GET, ?sid=.
local function xs(what, headers, method, query, want_rc, want_loc, want_log)
  local resp, logged = {}, {}
  NEXT = { method = method, authority = "box.lan:443", resp_header = resp, print = function(line) logged[#logged + 1] = line end }
  local h = { { "Cookie", "occulite_gate=" .. LIVEHTTP } }
  for _, x in ipairs(headers) do h[#h + 1] = x end
  local rc = run_gate(h, query, LIVE, nil, nil, ALIASES, "/addons/hmm/settings.cgi")
  NEXT = {}
  want(rc, want_rc, what .. ": answer")
  if want_loc ~= nil then want(resp["Location"], want_loc, what .. ": location") end
  want(#logged > 0 and logged[1]:find(want_log or "\0", 1, true) ~= nil, want_log ~= nil, what .. ": logged")
end
local SFS = function(v) return { "Sec-Fetch-Site", v } end
local NAV = { { "Sec-Fetch-Mode", "navigate" }, { "Sec-Fetch-Dest", "document" } }
xs("a same-origin POST", { SFS("same-origin"), { "Origin", "https://box.lan:443" } }, "POST", nil, 0)
xs("a typed URL (Sec-Fetch-Site none)", { SFS("none") }, "GET", "cmd=config", 0)
xs("a cross-site POST", { SFS("cross-site"), { "Origin", "https://evil.example" } }, "POST", nil, 403, nil, "addon=hmm method=POST")
xs("a same-site POST (another port of this host)", { SFS("same-site") }, "POST", nil, 403, nil, "Sec-Fetch-Site: same-site")
xs("a cross-site image GET", { SFS("cross-site"), { "Sec-Fetch-Mode", "no-cors" }, { "Sec-Fetch-Dest", "image" } }, "GET", "cmd=stop", 403, nil, "cross-site")
xs("a cross-site frame", { SFS("cross-site"), { "Sec-Fetch-Mode", "navigate" }, { "Sec-Fetch-Dest", "iframe" } }, "GET", nil, 403, nil, "cross-site")
xs("a link from another site, no query", { SFS("cross-site"), NAV[1], NAV[2] }, "GET", nil, 0)
xs("a link from another site with a query: the bare page", { SFS("cross-site"), NAV[1], NAV[2] }, "GET", "cmd=config&auth=off", 302, "/addons/hmm/settings.cgi")
xs("a cross-site form POST as a navigation", { SFS("cross-site"), NAV[1], NAV[2] }, "POST", nil, 403, nil, "method=POST")
xs("no Sec-Fetch-Site, a foreign Origin", { { "Origin", "https://evil.example" } }, "POST", nil, 403, nil, "Origin: https://evil.example")
xs("no Sec-Fetch-Site, Origin null", { { "Origin", "null" } }, "POST", nil, 403, nil, "Origin: null")
xs("no Sec-Fetch-Site, this system's Origin", { { "Origin", "https://BOX.lan:443" } }, "POST", nil, 0)
xs("no Sec-Fetch-Site nor Origin, a foreign Referer", { { "Referer", "https://evil.example/x" } }, "DELETE", nil, 403, nil, "Referer")
xs("no Sec-Fetch-Site nor Origin, this system's Referer", { { "Referer", "https://box.lan:443/addons/hmm/" } }, "PUT", nil, 0)
xs("no header at all (curl with the cookie)", {}, "POST", nil, 0)
xs("no header at all, a GET", {}, "GET", "cmd=config", 0)
do -- ?sid= passes from anywhere: another site cannot know it
  local logged = {}
  NEXT = { method = "POST", authority = "box.lan", print = function(l) logged[#logged + 1] = l end }
  local rc = run_gate({ SFS("cross-site") }, "sid=@" .. LIVEQUERY .. "@", LIVE, nil, nil, ALIASES)
  NEXT = {}
  want(rc, 0, "?sid= from another site: answer")
end

-- openccu-lite task 307 (GitHub issue #3): an API token as Authorization: Bearer. The token mirror
-- names a file by the SHA-256 of the secret; the file says which segments under /addons/ the token
-- opens ("*" for Full access), when it expires and from where it is accepted. A Bearer with a
-- token's shape is final: 401 for a token nobody has or an expired one, 403 (logged, with the
-- token's name) for another addon, a path outside /addons/ or an address outside the ranges; an
-- accepted request carries the token in the session header, X-Occulite-Auth: token and
-- X-Occulite-Token: <name>; a session-accepted request carries X-Occulite-Auth: session. Forged
-- copies of the two identity headers are removed like the session header's.
do
  local LOOM, UNKNOWN, FULL, EXPIRED, RANGED = "olt_0123456789abcdef0123456789abcdef", "olt_ffffffffffffffffffffffffffffffff", "olt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "olt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "olt_cccccccccccccccccccccccccccccccc"
  SHA256[LOOM] = "9036A388ED99FC7BB8E0253DEEAC8F7CBD9C06E70F8B0A03764BF00831960175"
  SHA256[UNKNOWN] = "72CBA6180A5035A2046A613047ED64E785432012DDDA817AC569E946437B1A4F"
  SHA256[FULL] = "B304DCBDF421EEA17902A51F265903F05954993E83AE67672A94AB4377A1CB82"
  SHA256[EXPIRED] = "A54A8EC1AC82489AF7E83BDB79D50533AB582303F691D45DBE7A57276C520DDB"
  SHA256[RANGED] = "DDF061AE5AE0107DE4B412EF0204EB8F2852925176AA2E3542A48001E38577A7"
  local TOKENS = {
    [key(LOOM)] = "name homematicip-local-ha\naddons red redmatic\n",
    [key(FULL)] = "name full\naddons *\n",
    [key(EXPIRED)] = "name old\naddons red\nexpires 1000000000\n",
    [key(RANGED)] = "name ranged\naddons hmm\nip 192.0.2.0/24\nip 2001:db8::/32\nip 198.51.100.7/32\n",
  }
  local function tok(what, authz, path, want_rc, want_seen, opts)
    opts = opts or {}
    local logged, resp = {}, {}
    NEXT = { tokens = TOKENS, identity = true, remote = opts.remote, method = opts.method, resp_header = resp, print = function(l) logged[#logged + 1] = l end }
    local headers = {}
    if authz ~= nil then headers[#headers + 1] = { "Authorization", authz } end
    for _, h in ipairs(opts.headers or {}) do headers[#headers + 1] = h end
    local rc, seen = run_gate(headers, opts.query, LIVE, nil, nil, ALIASES, path)
    local body = NEXT.body or ""
    NEXT = {}
    want(rc, want_rc, what .. ": answer")
    if want_rc == 0 then
      want(seen, want_seen, what .. ": headers")
    elseif want_rc ~= 500 then
      want(seen, "-", what .. ": nothing to pass on")
    end
    if opts.log ~= nil then
      want(#logged > 0 and logged[1]:find(opts.log, 1, true) ~= nil, true, what .. ": logged " .. opts.log)
    elseif want_rc == 403 then
      want(#logged, 1, what .. ": one log line")
    else
      want(#logged, 0, what .. ": nothing logged")
    end
    if opts.body ~= nil then want(body:find(opts.body, 1, true) ~= nil, true, what .. ": body says " .. opts.body) end
  end
  local LOOM_HDRS = "X-Occulite-Session=" .. LOOM .. " | X-Occulite-Auth=token | X-Occulite-Token=homematicip-local-ha"
  tok("the token on its proxied segment", "Bearer " .. LOOM, "/addons/red/api/x", 0, LOOM_HDRS)
  tok("the token on the addon's own id", "Bearer " .. LOOM, "/addons/redmatic/settings.cgi", 0, LOOM_HDRS)
  tok("the token on the bare segment without a slash", "Bearer " .. LOOM, "/addons/red", 0, LOOM_HDRS)
  tok("the token with spaces around it", "Bearer   " .. LOOM .. "  ", "/addons/red/", 0, LOOM_HDRS)
  tok("the token on another addon: 403, logged with its name", "Bearer " .. LOOM, "/addons/hmm/", 403, nil, { log = "token=homematicip-local-ha addon=hmm", body = "forbidden" })
  tok("the token on a POST to another addon", "Bearer " .. LOOM, "/addons/hmm/x.cgi", 403, nil, { method = "POST", log = "method=POST" })
  tok("the token outside /addons/", "Bearer " .. LOOM, "/api/auth/v1/state", 403, nil, { log = "addon=-" })
  tok("the token on a path that only starts like /addons/", "Bearer " .. LOOM, "/addonsx/red/", 403, nil, { log = "addon=-" })
  tok("Full access opens every addon", "Bearer " .. FULL, "/addons/hmm/", 0, "X-Occulite-Session=" .. FULL .. " | X-Occulite-Auth=token | X-Occulite-Token=full")
  tok("a token nobody has: 401, not logged", "Bearer " .. UNKNOWN, "/addons/red/", 401, nil, { body = "not known" })
  tok("an expired token: 401", "Bearer " .. EXPIRED, "/addons/red/", 401)
  tok("a Bearer of the wrong length is a token nobody has", "Bearer olt_0123", "/addons/red/", 401)
  tok("a Bearer with the shape and a live cookie: the token's verdict is final", "Bearer " .. UNKNOWN, "/addons/red/", 401, nil, { headers = { { "Cookie", "occulite_gate=" .. LIVEHTTP } } })
  tok("a Bearer that is no token falls through to the cookie", "Bearer " .. LIVEHTTP, "/addons/red/", 0, "X-Occulite-Session=" .. LIVEHTTP .. " | X-Occulite-Auth=session", { headers = { { "Cookie", "occulite_gate=" .. LIVEHTTP } } })
  -- (the stand-in keeps a removed header's slot as lighttpd does, so the forged X-Occulite-Auth's
  -- position comes first; its value is the gate's)
  tok("a forged identity header is removed before the token sets its own", "Bearer " .. LOOM, "/addons/red/", 0, "X-Occulite-Auth=token | X-Occulite-Session=" .. LOOM .. " | X-Occulite-Token=homematicip-local-ha", { headers = { { "X-Occulite-Auth", "session" }, { "x_occulite_token", "admin" }, { "X-Occulite-Session", FORGED } } })
  tok("a forged identity header without a credential: 401 for a program", nil, "/addons/red/", 401, nil, { headers = { { "X-Occulite-Auth", "token" }, { "X-Occulite-Token", "admin" } } })
  -- the address ranges: IPv4, a mapped IPv4, IPv6, and outside
  local RANGED_HDRS = "X-Occulite-Session=" .. RANGED .. " | X-Occulite-Auth=token | X-Occulite-Token=ranged"
  tok("a ranged token from inside its IPv4 range", "Bearer " .. RANGED, "/addons/hmm/", 0, RANGED_HDRS, { remote = "192.0.2.77" })
  tok("a ranged token from the single address", "Bearer " .. RANGED, "/addons/hmm/", 0, RANGED_HDRS, { remote = "198.51.100.7" })
  tok("a ranged token, the address mapped into IPv6", "Bearer " .. RANGED, "/addons/hmm/", 0, RANGED_HDRS, { remote = "::ffff:192.0.2.9" })
  tok("a ranged token from inside its IPv6 range", "Bearer " .. RANGED, "/addons/hmm/", 0, RANGED_HDRS, { remote = "2001:DB8:1::ab%eth0" })
  tok("a ranged token from next to the single address", "Bearer " .. RANGED, "/addons/hmm/", 403, nil, { remote = "198.51.100.8", log = "not accepted from this address" })
  tok("a ranged token from outside both ranges", "Bearer " .. RANGED, "/addons/hmm/", 403, nil, { remote = "203.0.113.5", log = "token=ranged addon=hmm" })
  tok("a ranged token from another IPv6 network", "Bearer " .. RANGED, "/addons/hmm/", 403, nil, { remote = "2001:db9::1" })
  tok("a ranged token without a known address", "Beared " .. RANGED, "/addons/hmm/", 401, nil, { remote = "" })
  tok("a ranged token with an unreadable address", "Bearer " .. RANGED, "/addons/hmm/", 403, nil, { remote = "nowhere" })
  -- the session paths carry the marker too
  NEXT = { identity = true }
  local rc, seen = run_gate({ { "Cookie", "occulite_gate=" .. LIVEHTTP } }, nil, LIVE, nil, nil, ALIASES)
  NEXT = {}
  want(rc, 0, "a cookie session: answer")
  want(seen, "X-Occulite-Session=" .. LIVEHTTP .. " | X-Occulite-Auth=session", "a cookie session: the marker says session")
  NEXT = { identity = true }
  rc, seen = run_gate({}, "sid=@aliasLive1@", LIVE, nil, nil, ALIASES)
  NEXT = {}
  want(seen, "X-Occulite-Session=aliasLive1 | X-Occulite-Auth=session", "an alias: the marker says session")
end

-- occulited task 125: the public Control app's views - an anonymous GET or HEAD under a mirrored
-- segment passes with X-Occulite-Auth: public and no session header; anything else is as before
local function pub(what, opts, want_rc, want_seen)
  NEXT = { identity = true, public = { history = true }, method = opts.method }
  local rc, seen = run_gate(opts.headers or {}, opts.query, LIVE, nil, nil, ALIASES, opts.path)
  NEXT = {}
  want(rc, want_rc, what .. ": answer")
  if want_seen then want(seen, want_seen, what .. ": header") end
end
pub("a public view, anonymous GET", { path = "/addons/history/embed/dashboard/a" }, 0, "X-Occulite-Auth=public")
pub("a public view's asset, anonymous HEAD", { path = "/addons/history/assets/index-AbCd1234.js", method = "HEAD" }, 0, "X-Occulite-Auth=public")
pub("a public view, forged session and identity headers are removed", { path = "/addons/history/", headers = { FORGED_HDR, { "X-Occulite-Auth", "session" }, { "X_Occulite_Token", "admin" } } }, 0, "X-Occulite-Auth=public")
pub("a public view, a POST is refused", { path = "/addons/history/api/v1/dashboards", method = "POST" }, 401)
pub("a public view, a PUT from a browser goes to the login", { path = "/addons/history/x", method = "PUT", headers = { { "Accept", "text/html" } } }, 302)
pub("another addon's segment is not public", { path = "/addons/hmm/" }, 401)
pub("the segment itself without a slash is not public", { path = "/addons/history" }, 401)
pub("a look-alike segment is not public", { path = "/addons/history2/" }, 401)
pub("a segment with capitals is not looked up", { path = "/addons/History/" }, 401)
pub("a live cookie still wins over the public pass", { path = "/addons/history/", headers = { { "Cookie", "occulite_gate=" .. LIVEHTTP } } }, 0, "X-Occulite-Session=" .. LIVEHTTP .. " | X-Occulite-Auth=session")
pub("a stale cookie falls through to the public pass", { path = "/addons/history/", headers = { { "Cookie", "occulite_gate=" .. STALE } } }, 0, "X-Occulite-Auth=public")
pub("a token is final: an unknown one is 401 even on a public segment", { path = "/addons/history/", headers = { { "Authorization", "Bearer olt_" .. string.rep("0", 32) } } }, 401)
NEXT = { identity = true, method = "GET" }
local prc = run_gate({}, nil, LIVE, nil, nil, ALIASES, "/addons/history/")
NEXT = {}
want(prc, 401, "no mirror file (the public mode off): 401")

if fails > 0 then print(fails .. " FAILED") os.exit(1) end
print("all pass")
