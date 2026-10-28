-- openccu-lite: lighttpd's answer while occulited does not answer (at boot, during a restart,
-- after a crash). Attached with magnet.attract-response-start-to to the requests conf.d/occulited.conf
-- proxies to occulited, it runs when the response starts.
--
-- It changes only lighttpd's own proxy error: a 502, 503 or 504 without a Content-Length and with
-- no Content-Type other than HTML, which is what mod_proxy produces when the connection to
-- 127.0.0.1:8183 is refused. Every answer occulited gives itself - its own 503s included - carries
-- a length or a JSON type and passes through unchanged.
--
-- Browsers (an Accept with text/html, outside /api/) get the waiting page: /var/etc/occulite-starting.html,
-- which lighttpd's start fills in from /etc/lighttpd/occulite-starting.html. It polls the health route
-- and reloads the URL the user asked for, since it is served under that URL. The API gets
-- {"error":"starting",...}, anything else a line of text. Always 503, Retry-After: 5 and
-- Cache-Control: no-store, so no browser or proxy keeps the page.
--
-- The API's answer carries occulited's unit state as "unit" - the small
-- file systemd's hooks on occulited.service keep in /run (deploy/systemd/occulited-unit-state):
-- starting, restarting after a crash, a crash loop, stopped on purpose. The waiting page polls
-- the health route and says which it is. The file is passed on only when it is what the script
-- writes: one short line of a JSON object with nothing but plain characters in it.
--
-- occulited B-121: the script also gives every answer of occulited's and the addons' blocks
-- (conf.d/occulited.conf: /api/, /addons/, the shell) the base setenv.conf's Cache-Control - but
-- only where the answer has none of its own. setenv.conf sets it on every answer and replaced an
-- addon's (and the shell's) `public, max-age=31536000, immutable` on its content-hashed assets and
-- occulited's own no-store; occulited.conf takes Cache-Control out of setenv for those blocks, and
-- the default arrives here: images "private, must-revalidate, no-transform, max-age=120", the rest
-- "private, no-cache, must-revalidate, no-transform, max-age=0", setenv.conf's two values on its
-- same match (the path holds .jpg, .jpeg, .png, .svg, .ico or .gif). Under /addons/ that is all
-- this script does: an addon's own error pages stay its own.
local r = lighty.r
local path = r.req_attr["uri.path"] or "/"

if not r.resp_header["Cache-Control"] then
  local image = false
  for _, ext in ipairs({ ".jpg", ".jpeg", ".png", ".svg", ".ico", ".gif" }) do
    if string.find(path, ext, 2, true) then
      image = true
      break
    end
  end
  if image then
    r.resp_header["Cache-Control"] = "private, must-revalidate, no-transform, max-age=120"
  else
    r.resp_header["Cache-Control"] = "private, no-cache, must-revalidate, no-transform, max-age=0"
  end
end
if string.find(path, "^/addons/") then
  return 0
end

local status = r.req_item.http_status
if status ~= 502 and status ~= 503 and status ~= 504 then
  return 0
end
if r.resp_header["Content-Length"] then
  return 0
end
local ct = r.resp_header["Content-Type"]
if ct and not string.find(ct, "^text/html") then
  return 0
end

local accept = r.req_header["Accept"] or ""
r.resp_header["Cache-Control"] = "no-store"
r.resp_header["Retry-After"] = "5"

if string.find(path, "^/api/") then
  local unit = "null"
  local f = io.open("/run/occulite/occulited-state.json", "rb")
  if f then
    local s = f:read(1024) or ""
    f:close()
    s = string.gsub(s, "%s+$", "")
    if #s < 1024 and string.find(s, '^{"state":"[a-z-]+"[%w",:_-]*}$') then
      unit = s
    end
  end
  r.resp_header["Content-Type"] = "application/json"
  r.resp_body.set({ '{"error":"starting","message":"occulited is not answering yet","unit":' .. unit .. '}\n' })
  return 503
end
if not string.find(accept, "text/html", 1, true) then
  r.resp_header["Content-Type"] = "text/plain; charset=utf-8"
  r.resp_body.set({ "occulited is not answering yet\n" })
  return 503
end

local body
for _, file in ipairs({ "/var/etc/occulite-starting.html", "/etc/lighttpd/occulite-starting.html" }) do
  local f = io.open(file, "rb")
  if f then
    body = f:read("*a")
    f:close()
    if body and body ~= "" then
      break
    end
  end
end
if not body or body == "" then
  body = '<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="10">'
    .. '<title>openccu-lite</title><p>openccu-lite is starting …</p>'
end
r.resp_header["Content-Type"] = "text/html; charset=utf-8"
r.resp_body.set({ body })
return 503
