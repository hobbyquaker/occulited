-- openccu-lite: the session gate in front of /addons/ (D-28, task 6).
--
-- lighttpd has no auth_request; this mod_magnet script does the check per request against the
-- session directory occulited maintains on tmpfs: one file per live session, named by the SHA-256
-- (lower-case hex) of the session id - 26 characters of base32 since task 125 - containing the user
-- name. occulited keeps no session id outside its memory (B-102, D-67), so the gate hashes the id a
-- request carries to find the file; neither the mirror nor occulited's session store holds a usable
-- credential. A request without a valid session is redirected to the shell's login with the
-- original URL to return to. Accepted credentials: the gate cookie - occulite_gate from a login
-- over HTTP, __Secure-occulite_gate from one over HTTPS (openccu-lite task 259, D-78: the same
-- session id as the API's cookie, but scoped to Path=/addons/, while the API's cookie
-- occulite_session is scoped to Path=/api and never reaches an addon; a session cookie that
-- still arrives here, from a browser that has not loaded the shell since the change, is no
-- credential at the gate) - or ?sid= with the session id, with or without the CCU's @ wrapping.
--
-- The legacy alias (task 125, D-77): the CCU's addon convention passes the session as
-- ?sid=@xxxxxxxxxx@, ten alphanumerics, and the addons' CGIs parse exactly that shape. occulited
-- gives a session such an alias when the shell opens an addon that lives by the convention, and
-- mirrors it in a second directory, again by its SHA-256. The gate accepts the alias from ?sid=
-- alone - never from a cookie - and only under /addons/, which is where it runs; occulited's API
-- never takes it. A request accepted by its alias reaches the addon with the alias in the session
-- header, which the API refuses too: an addon that validates the header against the API is one
-- the shell opens without ?sid= (its catalogue entry declares the header), so it sees the id.
--
-- The session header: a request the gate accepts reaches the addon with the credential it
-- validated in X-Occulite-Session - bare, without the @ wrapping - so an addon needs
-- neither the cookie's names nor ?sid=. Before anything else, every request loses any header a
-- client sent under that name, in any case and in any spelling a CGI reads as the same variable
-- (X_Occulite_Session), accepted or not. The header is therefore present only behind a session
-- the gate validated. The requests the gate does not run on lose it in the fork's lite
-- modules.conf, which runs the same removal for every request on every socket.
--
-- Cross-site requests (openccu-lite task 213, task 120's F-2, homematic-manager B-41): the gate
-- cookie is SameSite=Lax, which still carries it on a top-level navigation from another site and on
-- any request from the same site (another port of this host). A request the gate accepts by its
-- cookie is therefore refused with 403 when the browser says it comes from elsewhere:
-- Sec-Fetch-Site cross-site or same-site - except a top-level navigation (GET, mode navigate, dest
-- document), which opens the addon's page but without its query string (a link cannot carry a
-- state change in it: a 302 to the bare path) - and, from a browser without Sec-Fetch-Site, a
-- state-changing method whose Origin (else Referer) is not this system. Sec-Fetch-Site none (a
-- typed URL, a bookmark) and same-origin pass; so does ?sid=, which another site cannot know. Each
-- refusal is a line in lighttpd's error log (the journal) with the addon's id. occulited's API and
-- its addon CGIs apply the same rule (internal/httpapi/crosssite.go).
--
-- Costs one SHA-256 of a few bytes and one open() per credential, no subrequest, and keeps
-- WebSockets and the addons' own proxy drop-ins working because lighttpd still serves them itself.
--
-- Needs lighttpd 1.4.60 or later (lighty.r) and lighty.c.md with sha256 (1.4.65 or later, built
-- with a crypto library; the fork's is built with OpenSSL). Without either the header cannot be
-- removed or no session can be found, and the gate fails closed.

local SESSION_DIR = "/var/run/occulite/sessions/"
local LEGACY_DIR = "/var/run/occulite/legacy-sessions/"
local TOKEN_DIR = "/var/run/occulite/gate-tokens/"
local SESSION_HEADER = "X-Occulite-Session"
-- The identity headers (openccu-lite task 307): X-Occulite-Auth says what the gate accepted -
-- "session" for a session id or its alias, "token" for an API token, "public" for a view of the
-- public Control app (occulited task 125, step 3 below) - and X-Occulite-Token names
-- the token. Like the session header they are removed from every request first, so an addon sees
-- them only behind the gate's own decision.
local AUTH_HEADER = "X-Occulite-Auth"
local TOKEN_HEADER = "X-Occulite-Token"

local r = lighty.r
if r == nil then
    return 500
end
local md = lighty.c and lighty.c.md
if md == nil or md("sha256", "") == nil then
    return 500
end

-- The header's name as a CGI sees it: mod_cgi and occulited's CGI runner pass a request header as
-- HTTP_<NAME>, and mod_cgi turns every character that is not a letter or a digit into "_". So
-- X_Occulite_Session or x.occulite.session would reach a CGI as the same variable as the real
-- header, and are removed with it.
local function is_session_header(name)
    return (name:upper():gsub("[^%w]", "_")) == "X_OCCULITE_SESSION"
end

-- The forwarding headers (openccu-lite B-230): lighttpd's mod_proxy appends the client's address
-- to an X-Forwarded-For the client sent instead of replacing it, so a client-sent one goes here as
-- it does everywhere else in the fork's global script (occulite-session-header.lua): the only
-- element left is lighttpd's own. It appends to a client's Forwarded the same way, and sets
-- X-Forwarded-Proto and X-Forwarded-Host itself; all four go, whatever the spelling (the session
-- header's rule).
local FORWARDING = { X_FORWARDED_FOR = true, X_FORWARDED_PROTO = true, X_FORWARDED_HOST = true, FORWARDED = true, X_OCCULITE_INGRESS = true }
local function is_stripped(name)
    local cgi = (name:upper():gsub("[^%w]", "_"))
    return cgi == "X_OCCULITE_SESSION" or cgi == "X_OCCULITE_AUTH" or cgi == "X_OCCULITE_TOKEN" or FORWARDING[cgi] == true
end

-- Names first, removal after: the table is not changed while pairs() walks it. Then a second walk,
-- which must find none of them left with a value; lighttpd keeps a removed header as an empty
-- entry and forwards no empty header.
local function strip_session_header()
    local names = {}
    for k in pairs(r.req_header) do
        if is_stripped(k) then names[#names + 1] = k end
    end
    for _, k in ipairs(names) do
        r.req_header[k] = nil
    end
    for k, v in pairs(r.req_header) do
        if is_stripped(k) and v ~= nil and v ~= "" then return false end
    end
    return true
end

local function fail_closed()
    r.resp_header["Content-Type"] = "application/json"
    r.resp_body:set({ '{"error":"session-header","message":"the session header could not be set safely"}' })
    return 500
end

if not strip_session_header() then
    return fail_closed()
end
-- openccu-lite task 406: a request that came in on an addon's web ingress socket
-- (/run/lighttpd-addons/<id>/proxy.sock, the block occulited renders) carries the addon's id in
-- X-Occulite-Ingress, set here from the socket the request arrived on (request.server-addr is the
-- socket's path), after a client-sent copy went above; occulited holds the address the addon claimed
-- to that addon's declared ranges. The fork's global script does the same outside /addons/.
do
    local sa = r.req_attr["request.server-addr"] or ""
    local ingress = sa:match("^/run/lighttpd%-addons/([%w_.%-]+)/proxy%.sock$")
    if ingress then
        r.req_header["X-Occulite-Ingress"] = ingress
    end
end

-- The two shapes: a session id is 26 characters of base32 (A-Z, 2-7), the legacy alias ten
-- alphanumerics; each may come wrapped in @. Neither pattern matches the other's length.
local SID_PATTERN = "^@?(" .. string.rep("[A-Z2-7]", 26) .. ")@?$"
local ALIAS_PATTERN = "^@?(" .. string.rep("%w", 10) .. ")@?$"

-- live_file says whether the directory holds a file named by the SHA-256 of the bare id.
-- lighty.c.md answers upper-case hex, occulited names the files in lower case.
local function live_file(dir, id)
    local key = md("sha256", id)
    if key == nil then return false end
    key = key:lower()
    if not key:match("^" .. string.rep("%x", 64) .. "$") then return false end
    local f = io.open(dir .. key, "r")
    if f == nil then return false end
    f:close()
    return true
end

-- live_sid answers the bare id when sid has the shape of a session id and names a live session.
local function live_sid(sid)
    if sid == nil then return nil end
    sid = sid:match(SID_PATTERN)
    if sid == nil then return nil end
    if live_file(SESSION_DIR, sid) then return sid end
    return nil
end

-- live_alias answers the bare alias when sid has the alias's shape and names a live session's
-- alias - under /addons/ only, which is the gate's whole domain; a request the gate runs on
-- elsewhere gets nothing from an alias.
local function live_alias(sid)
    if sid == nil then return nil end
    local path = r.req_attr["uri.path"] or ""
    if path:sub(1, 8) ~= "/addons/" then return nil end
    sid = sid:match(ALIAS_PATTERN)
    if sid == nil then return nil end
    if live_file(LEGACY_DIR, sid) then return sid end
    return nil
end

-- accept passes the request on with the credential it was accepted for: a session id or its alias
-- (X-Occulite-Auth: session), or an API token (X-Occulite-Auth: token, X-Occulite-Token: its name).
local function accept(cred, kind, token_name)
    r.req_header[SESSION_HEADER] = cred
    r.req_header[AUTH_HEADER] = kind
    if token_name ~= nil then r.req_header[TOKEN_HEADER] = token_name end
    if r.req_header[SESSION_HEADER] ~= cred or r.req_header[AUTH_HEADER] ~= kind or (token_name ~= nil and r.req_header[TOKEN_HEADER] ~= token_name) then
        return fail_closed()
    end
    return 0
end

local function urlencode(s)
    return (s:gsub("[^%w%-%._~/]", function(c) return string.format("%%%02X", string.byte(c)) end))
end

-- 0. an API token as Authorization: Bearer (openccu-lite task 307, GitHub issue #3). occulited
-- mirrors every stored token into TOKEN_DIR as the sessions are mirrored: a file named by the
-- SHA-256 of the secret, with lines "name <token>", "addons <segment> …" (the URL segments under
-- /addons/ the token's addon:<id> scopes open, "*" for Full access), "expires <unix>" and
-- "ip <cidr>". A Bearer with a token's shape is final - no fall-through to a cookie: a program that
-- presents a token gets its own answer. No file, or past its expiry: 401. The request's segment not
-- among the token's, or the client's address outside its ranges: 403 with a journal line naming the
-- token and the addon. The token travels to the addon in the session header, as a session does, so
-- an addon's check against /api/auth/v1/state keeps working; X-Occulite-Auth says it is a token.
local TOKEN_PATTERN = "^Bearer%s+(olt_" .. string.rep("%x", 32) .. ")%s*$"

-- token_file reads a token's mirror file: nil when there is none, else {name, addons, expires, ips}
local function token_file(secret)
    local key = md("sha256", secret)
    if key == nil then return nil end
    key = key:lower()
    if not key:match("^" .. string.rep("%x", 64) .. "$") then return nil end
    local f = io.open(TOKEN_DIR .. key, "r")
    if f == nil then return nil end
    local tok = { name = "-", addons = {}, ips = {} }
    for line in f:lines() do
        local k, v = line:match("^(%S+)%s*(.-)%s*$")
        if k == "name" and v ~= "" then
            tok.name = v
        elseif k == "addons" then
            for seg in v:gmatch("%S+") do tok.addons[seg] = true end
        elseif k == "expires" then
            tok.expires = tonumber(v)
        elseif k == "ip" and v ~= "" then
            tok.ips[#tok.ips + 1] = v
        end
    end
    f:close()
    return tok
end

-- parse_ip turns an address into its 16 bytes - IPv4 as the mapped ::ffff:a.b.c.d, so that a
-- client lighttpd reports in either form meets a range of either family the way Go's net does.
-- nil for anything that is no address.
local function parse_ip(s)
    s = s:gsub("%%.*$", "") -- a zone id
    local v4 = { s:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$") }
    if #v4 == 4 then
        local bytes = { 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff }
        for _, n in ipairs(v4) do
            n = tonumber(n)
            if n == nil or n > 255 then return nil end
            bytes[#bytes + 1] = n
        end
        return bytes
    end
    if not s:find(":", 1, true) then return nil end
    local tail = s:match(":(%d+%.%d+%.%d+%.%d+)$")
    if tail then
        local t = parse_ip(tail)
        if t == nil then return nil end
        s = s:sub(1, #s - #tail) .. string.format("%x:%x", t[13] * 256 + t[14], t[15] * 256 + t[16])
    end
    local groups = {}
    local function split(part, into)
        if part == "" then return true end
        for g in (part .. ":"):gmatch("([^:]*):") do
            if not g:match("^%x%x?%x?%x?$") then return false end
            into[#into + 1] = tonumber(g, 16)
        end
        return true
    end
    local head, rest = s:match("^(.-)::(.*)$")
    if head ~= nil then
        local h, t = {}, {}
        if not split(head, h) or not split(rest, t) or #h + #t > 7 or rest:find("::", 1, true) then return nil end
        for _, g in ipairs(h) do groups[#groups + 1] = g end
        for _ = 1, 8 - #h - #t do groups[#groups + 1] = 0 end
        for _, g in ipairs(t) do groups[#groups + 1] = g end
    elseif not split(s, groups) or #groups ~= 8 then
        return nil
    end
    local bytes = {}
    for _, g in ipairs(groups) do
        bytes[#bytes + 1] = g // 256
        bytes[#bytes + 1] = g % 256
    end
    return bytes
end

-- ip_in says whether addr lies in the CIDR range; an IPv4 range lives in the mapped space (+96).
local function ip_in(addr, cidr)
    local net, bits = cidr:match("^(.*)/(%d+)$")
    if net == nil then return false end
    bits = tonumber(bits)
    local a, n = parse_ip(addr), parse_ip(net)
    if a == nil or n == nil then return false end
    if not net:find(":", 1, true) then bits = bits + 96 end
    if bits > 128 then return false end
    for i = 1, 16 do
        local remaining = bits - (i - 1) * 8
        if remaining <= 0 then return true end
        local mask = 0xff
        if remaining < 8 then mask = (0xff << (8 - remaining)) & 0xff end
        if (a[i] & mask) ~= (n[i] & mask) then return false end
    end
    return true
end

local function refuse_token(name, seg, why)
    print(string.format("occulite-gate: token refused: token=%s addon=%s method=%s path=%s (%s)", name, seg, r.req_attr["request.method"] or "?", urlencode(r.req_attr["uri.path"] or "/"), why))
    r.resp_header["Content-Type"] = "application/json"
    r.resp_body:set({ '{"error":"forbidden","message":"the token does not open this addon\'s pages: ' .. why .. '"}' })
    return 403
end

local authz = r.req_header["Authorization"]
if authz ~= nil and authz:match("^Bearer%s+olt_") then
    local secret = authz:match(TOKEN_PATTERN)
    local tok = nil
    if secret ~= nil then tok = token_file(secret) end
    if tok == nil or (tok.expires ~= nil and os.time() >= tok.expires) then
        r.resp_header["Content-Type"] = "application/json"
        r.resp_body:set({ '{"error":"unauthenticated","message":"the token is not known here or has expired"}' })
        return 401
    end
    local path = r.req_attr["uri.path"] or "/"
    local seg = path:match("^/addons/([^/]+)")
    if seg == nil or not (tok.addons["*"] or tok.addons[seg]) then
        return refuse_token(tok.name, seg or "-", "the scope addon:<id> of this addon is required")
    end
    if #tok.ips > 0 then
        local addr = r.req_attr["request.remote-addr"] or ""
        local ok = false
        for _, c in ipairs(tok.ips) do
            if ip_in(addr, c) then ok = true break end
        end
        if not ok then return refuse_token(tok.name, seg, "not accepted from this address") end
    end
    return accept(secret, "token", tok.name)
end

-- 1. the gate cookies: the session id only, never an alias. One name per scheme, because a browser
-- neither sends a Secure cookie over plain HTTP nor lets an HTTP login replace it; over HTTPS it
-- sends both names. Every occurrence of either is tried, so a stale cookie does not hide a live one. Each name is anchored at the start
-- of the header or at a cookie separator, for the same reason the query string is (B-21): an
-- unanchored match reads a cookie that merely *ends* in the name - one an addon page could set -
-- as the gate cookie. Not a hole on its own, because live_sid still has to find the file, but
-- the gate should not depend on that. The header gets a leading ";" so that its start is a
-- separator like any other. The API's own cookie (occulite_session) is not on the list (task 259).
local COOKIE_PATTERNS = {
    "[;,%s]__Secure%-occulite_gate=([%w@]+)",
    "[;,%s]occulite_gate=([%w@]+)",
}
-- task 213: whether a cookie request comes from another site - nil when it may pass, "navigate"
-- for a top-level navigation from elsewhere, else the reason for the refusal
local SAFE = { GET = true, HEAD = true, OPTIONS = true }
local function host_of(v)
    local h = v:match("^%a[%w+.-]*://([^/?#]+)")
    return h and h:lower() or nil
end
local function cross_site()
    local safe = SAFE[r.req_attr["request.method"] or "GET"] == true
    local sfs = r.req_header["Sec-Fetch-Site"]
    if sfs == "same-origin" or sfs == "none" then return nil end
    if sfs == "cross-site" or sfs == "same-site" then
        if safe and r.req_header["Sec-Fetch-Mode"] == "navigate" and r.req_header["Sec-Fetch-Dest"] == "document" then
            return "navigate"
        end
        return "Sec-Fetch-Site: " .. sfs
    end
    if safe then return nil end
    local own = (r.req_attr["uri.authority"] or ""):lower()
    for _, name in ipairs({ "Origin", "Referer" }) do
        local v = r.req_header[name]
        if v ~= nil and v ~= "" then
            if own ~= "" and host_of(v) == own then return nil end
            return name .. ": " .. v
        end
    end
    return nil
end

local function refuse_cross_site(why)
    local path = r.req_attr["uri.path"] or "/"
    local addon = path:match("^/addons/([^/]+)") or "-"
    print(string.format("occulite-gate: cross-site request refused: addon=%s method=%s path=%s (%s)", addon, r.req_attr["request.method"] or "?", urlencode(path), (why:gsub("[%c]", "?"))))
    r.resp_header["Content-Type"] = "application/json"
    r.resp_body:set({ '{"error":"cross-site","message":"a request from another site with this system\'s session is refused"}' })
    return 403
end

local cookie = r.req_header["Cookie"]
if cookie ~= nil then
    cookie = ";" .. cookie
    for _, pattern in ipairs(COOKIE_PATTERNS) do
        for sid in cookie:gmatch(pattern) do
            local live = live_sid(sid)
            if live ~= nil then
                local why = cross_site()
                if why == nil then return accept(live, "session") end
                if why ~= "navigate" then return refuse_cross_site(why) end
                -- a link from elsewhere opens the page, never with a query that could change state
                local query = r.req_attr["uri.query"]
                if query == nil or query == "" then return accept(live, "session") end
                r.resp_header["Location"] = urlencode(r.req_attr["uri.path"] or "/")
                return 302
            end
        end
    end
end

-- 2. ?sid= in the query string: the session id, or the legacy alias the addon pages themselves pass
-- on (?sid=@xxxxxxxxxx@). uri.query carries no leading "?", so the parameter is either the first
-- one or preceded by "&" - anchoring both ways keeps a parameter that merely ends in "sid"
-- (RedMatic's pages carry several) from being read as the session (B-21).
local query = r.req_attr["uri.query"]
if query ~= nil then
    local sid = query:match("^sid=([%w@]+)") or query:match("&sid=([%w@]+)")
    local live = live_sid(sid) or live_alias(sid)
    if live ~= nil then return accept(live, "session") end
end

-- 3. the public Control app (occulited task 125): while the App's public mode is on, occulited
-- mirrors the URL segments of the addons that registered views in the App into PUBLIC_DIR, one
-- empty file per segment. A GET or HEAD under such a segment that carries no live credential
-- passes then, without a session header and with X-Occulite-Auth: public - the frame of a view in
-- the public App. What such a request may see is the addon's decision; an addon that knows
-- nothing of it finds no session and refuses, as before. Nothing but GET and HEAD, and only the
-- segment's shape (a lower-case id), so no path leads out of the directory.
local PUBLIC_DIR = "/var/run/occulite/public-views/"
local method = r.req_attr["request.method"] or ""
if method == "GET" or method == "HEAD" then
    local seg = (r.req_attr["uri.path"] or ""):match("^/addons/([%l%d][%l%d_%.%-]*)/")
    if seg ~= nil and #seg <= 64 then
        local f = io.open(PUBLIC_DIR .. seg, "r")
        if f ~= nil then
            f:close()
            r.req_header[AUTH_HEADER] = "public"
            if r.req_header[AUTH_HEADER] ~= "public" or (r.req_header[SESSION_HEADER] or "") ~= "" then
                return fail_closed()
            end
            return 0
        end
    end
end

-- 4. nothing: send the browser to the shell's login, and API-style callers a 401
--
-- uri.path is the *decoded* path, so it goes back into the Location header percent-encoded: a
-- path carrying CR or LF would otherwise be written into a response header verbatim. lighttpd
-- may well reject such a request before the gate ever runs - the gate should not depend on it
-- (B-21).
local path = r.req_attr["uri.path"] or "/"
local accept_header = r.req_header["Accept"] or ""
if accept_header:find("text/html", 1, true) then
    r.resp_header["Location"] = "/login?return=" .. urlencode(path)
    return 302
end
r.resp_header["Content-Type"] = "application/json"
r.resp_body:set({ '{"error":"unauthenticated","message":"login required"}' })
return 401
