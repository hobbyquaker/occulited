# The metadata API

Normative. Served by `occulited` under `/api/meta/v1/`, reached through lighttpd on the system;
homematic-manager's `local` provider implements the same operations in-process. Wire format is JSON,
UTF-8. The file format and validation rules are in [meta-format.md](meta-format.md); this document
does not repeat them.

## Conventions

- Every response carries `revision` (the store's current revision after the request).
- Every mutating response returns the new revision; a mutation that changes nothing returns `304`
  and does not bump it. A `304` has no body (HTTP allows none): the unchanged revision is in its
  `ETag` header, as it is on every mutation's answer.
- Conditional writes: a mutating request may send `If-Match: <revision>`; if the store's revision
  differs the request fails with `409` `revision-conflict` and nothing changes.
- Errors: `{"error": "<code>", "message": "<human text>", "detail": {...}}`. Codes are stable and
  listed at the end; `message` is not.
- Node paths in URLs are literal (`/enums/room/nodes/eg/wohnzimmer`); refs are percent-encoded
  where they appear in a path segment (`BidCos-RF.JEQ9000001%3A1`).
- All endpoints require a credential except `/version`: a session id (cookie
  `occulite_session` from a login over HTTP or `__Secure-occulite_session` from one over HTTPS,
  both accepted on either scheme, scoped to `Path=/api`; `Authorization: Bearer <sid>`; or `?sid=<sid>` — with or without the CCU's
  `@` wrapping) or an **API token**. **A mutation that rides on the cookie alone also carries the header
  `X-Occulite-Request`** (any value; `403 request-header` without it — openccu-lite task 259, the rule in
  [system-api.md → `/api/auth/v1`](system-api.md#apiauthv1)); a Bearer or a token needs none, a read neither (`olt_<32 hex>`, same three places; created on System → Users (API tokens)
  or `POST /api/auth/v1/tokens`; the system's own token is in `<state>/local-token` for
  programs running on it). **Scopes** (the model in
  [system-api.md → Scopes](system-api.md#scopes)): every read below needs
  **`meta:read`** — `/snapshot`, `/objects`, `/objects/{ref}`, `/enums`, `/enums/{enum}/tree`,
  `/export`, `/events/sse` — and every mutation and import **`meta:write`**, which includes the
  read; `/version` is open. An account session has the scopes of its level on the ladder
  (system-api.md → Scopes): `read` and `operate` have `meta:read`, `configure` and `administer`
  have `meta:write` — so names, rooms, functions and favorites are a `configure` account's to
  change, not an administrator's alone (occulited B-55); the local token has `meta:read` and
  nothing else, so a program on the system reads names and rooms with it and needs a token with
  `meta:write` (or the user's session) to change anything. A `403` names the missing scope in its
  `scope` field.
  The `orphaned` flag is set only by the owning process, never through the API.

## The page

occulited's own web UI edits the store on its **Metadata** page (2026-09-10): every
taxonomy a row with its nodes indented under it, a toolbar (new taxonomy, add, add below, rename,
move, delete, refresh), and the objects with rename, "assign to a node" and their memberships. It
is the editor homematic-manager has (`packages/ui/src/lib/util/metaTree.ts` is shared code), on
these routes: `POST/PATCH/DELETE /enums[/{enum}]`, `POST/PATCH/DELETE /enums/{enum}/nodes[/{path}]`
with `?members=detach` after the page has listed what is assigned, and `PATCH /objects/{ref}` for
`name` and `enums`. A new node's id is the slug of its name, free among its siblings (`-2`, `-3`
on a collision). The page follows the change stream below and re-reads the snapshot on every
event, so an edit made by an addon or by the Manager shows up as it happens; nothing is drawn from
a write's own answer. Writes need a session with `meta:write` — an account of the level `configure`
or `administer`; a `read` or `operate` account sees the store read-only.

## Feature detection

`GET /api/meta/v1/version`

```json
{ "api": "meta", "version": 1, "format": 1, "revision": 412, "implementation": "occulited fa42dfde1e31fb074df53220dd573ceb92642ff0",
  "commit": "fa42dfde1e31fb074df53220dd573ceb92642ff0",
  "hmip": { "keyserver_mode": "LOCAL", "device_keys": 12, "offline_pairing": false },
  "capabilities": { "pairing": true, "state": true, "history": true, "apis": { "meta": 1, "rpc": 1, "system": 1, "auth": 1 },
    "transports": ["sse", "websocket"], "limits": { "streams_per_token": 2, "streams_total": 16, "buffer_seconds": 300,
    "buffer_events": 5000 }, "json_double": true } }
```

A consumer that gets this answers "openccu-lite"; one that gets a 404 or a non-JSON body keeps
using whatever it used before (ReGa, typically). No authentication.

`implementation` is the server's name and version: for occulited the commit it was built from,
the full hash (`-dirty` or `-hot` after it for a build by hand), and **`commit`** the bare hash,
absent when the build carries none (occulited's `docs/system-api.md`, *occulited's version*).
`1.0.0-dev.38` to `1.0.0-dev.40` reported the image version there instead (occulited task 9, taken
back by task 16).

The interfaces the system runs are not in this open answer — they say which radio hardware the
system has. A client asks `GET /api/rpc/v1/interfaces` with a credential that holds `rpc:read`
(occulited's `docs/system-api.md`, lite-rpc): each interface's `InterfacesList.xml` name, the name
`/api/rpc/v1/xmlrpc/{interface}` and `/api/rpc/v1/json/{interface}` take, and whether it runs.

**`hmip`** (2026-09-23) is what a pairing client needs to know about the HmIP network,
readable without a system or radio scope — the one radio fact in this API, because this is where a
client already looks. It says which of the three ways to admit an HmIP device can work; it never
carries a key or an SGTIN (the keys themselves stay behind `radio:keys` in the system API).

| Field | Meaning |
| --- | --- |
| `keyserver_mode` | `KeyServer.Mode` of `hmip_user.conf`: `LOCAL` (eQ-3's key server is never asked), `KEYSERVER` (the key server only) or `KEYSERVER_LOCAL` (the shipped default, and what an unset mode answers: the system's own device keys first, the key server after). |
| `device_keys` | How many device keys the system holds in `sgtin.map` — a count, never the list. hmipserver consults them under `LOCAL` and `KEYSERVER_LOCAL` only, at its start. |
| `offline_pairing` | `false` on `LOCAL`: a device whose key is not on the system — stored, or scanned from its sticker by the client — cannot be paired at all; `true` in the other two modes, where the key server stands in (with internet). |

**`capabilities`** (2026-09-25) is what a client can use on this system beyond the
metadata: `{pairing, state, history, apis, transports, limits, json_double}` — `pairing` whether a program may ask for access (the client pairing,
`POST /api/auth/v1/pairing/request`; `false` when an administrator switched it off), `state` the state store
(`GET /api/rpc/v1/state`), `history` the datapoint history (`GET /api/rpc/v1/history`); `apis` the API majors (`{meta: 1, rpc: 1, system: 1,
auth: 1}` — a client refuses a higher major than it speaks); `transports` the event stream's (`["sse", "websocket"]`);
`limits` `{streams_per_token, streams_total, buffer_seconds, buffer_events}`; `json_double` that lite-rpc's JSON path
takes `{"double": n}`. A system from before answers no
`capabilities` at all: a client treats every one as absent.

For a pairing dialog: with the SGTIN **and** the printed key a device pairs in every mode; with the
SGTIN alone (the key server fetches the key) only when `offline_pairing` is `true`; "any device, no
SGTIN" pairs on `LOCAL` only a device counted in `device_keys`. **A system from before this field
answers no `hmip` at all** — `undefined`, not an empty object — so a client that finds it missing
shows every pairing mode, as it did before. The object is read live from the two files on every
call: a mode switch or a scanned key shows in the next answer.

## Snapshot

`GET /api/meta/v1/snapshot` → the entire document as on disk (`format`, `revision`, `objects`,
`enums`). This is the normal startup call for a consumer; everything after it comes from the change
stream.

## Objects

| | |
| --- | --- |
| `GET /objects` | All objects. `?enum=<path>` filters to the subtree of that node (see format spec). `?orphaned=true|false` filters on the flag. Response: `{"revision": N, "objects": {"<ref>": <object>}}`. |
| `GET /objects/{ref}` | One object, or `404` `unknown-object`. |
| `PUT /objects/{ref}` | Create or replace. Body: `{"name", "enums"?, "meta"?}`. Missing optional fields are reset to their defaults. |
| `PATCH /objects/{ref}` | Create or update. Body: any subset of `name`, `enums`, `meta`. Absent fields are left alone. `meta` is merged **per namespace**: `{"meta": {"ccu-jack": null}}` removes that namespace, any other value replaces it whole. |
| `DELETE /objects/{ref}` | Removes the entry. Normally unnecessary — `orphaned` is how "gone" is expressed — but a user may want to forget a device for good. |
| `POST /objects:bulk` | `{"set": {"<ref>": <patch body>}, "delete": ["<ref>"]}`. Applied as one revision, all-or-nothing. |

Object bodies are validated per the format spec: `name` non-empty, every path in `enums` must
exist (`422` `unknown-path`), no duplicates.

## Enums and nodes

| | |
| --- | --- |
| `GET /enums` | `{"revision", "enums": {...}}` — every enum with its full tree. |
| `POST /enums` | `{"id", "name": {"en": "...", ...}}` → `201`. `409` `duplicate-id` if it exists. |
| `PATCH /enums/{enum}` | `{"name"}` only. |
| `DELETE /enums/{enum}?members=detach` | Deletes the enum and every node. Refused with `409` `has-members` (detail lists the refs) unless `members=detach`, in which case the paths are removed from every object in the same revision. |
| `GET /enums/{enum}/tree` | The ordered tree. |
| `POST /enums/{enum}/nodes` | `{"parent": "<path>" \| null, "id", "name", "icon"?, "position"?}` → `201`. `parent: null` creates a root node. `position` is the index among the new siblings; default: append. |
| `PATCH /enums/{enum}/nodes/{path...}` | Any of `name`, `icon`, `parent` (move; `null` = to root), `position` (reorder among siblings). Moving a node under its own descendant is `422` `invalid-move`. Moving keeps the id, so member paths are rewritten in the same revision and consumers see one `node.moved` event carrying `from` and `to`. |
| `DELETE /enums/{enum}/nodes/{path...}?members=detach` | Deletes the node and its subtree. Same `has-members` rule as for enums. |

## Import and export

| | |
| --- | --- |
| `GET /export?format=json\|yaml` | The whole document; `Content-Disposition: attachment; filename=meta.<ext>`. **`hmscript` was removed on 2026-09-08.** It produced an HM-Script that recreated the rooms, functions and names on a CCU, and existed to carry the metadata store back after switching away from openccu-lite. That is no longer the way back: a system returns to OpenCCU by restoring the backup taken **before** the migration (`docs/switching.md`), and an export that moved names but not programs or system variables made a half-migration look reversible when it is not. |
| `PUT /import` | Body is a whole document in JSON or YAML (by `Content-Type`). Validated completely; applied as **one** revision; rejected entirely on the first error with `422` and the location of the error in `detail`. `?mode=replace` (default) replaces the store; `?mode=merge` keeps existing objects and enums not mentioned in the import and overwrites the ones that are. An import whose result equals the current document is not a revision (`changed: false`). |
| `POST /import/regadom` | The other half of the ReGa bridge, for a system that has a ReGa database rather than a reachable CCU: JSON `{"source"?: "box"\|"restore:<file>", "mode"?: "merge"\|"replace", "dry_run"?: false}` — `box` (the default) reads `/etc/config/homematic.regadom` through the privilege helper (root-only), `restore:<file>` a `.sbk` already uploaded to `POST /api/system/v1/restore/check` — or multipart `file` (a regadom or an `.sbk`, up to 2 GiB) with `?mode=` and `?dry_run=true`. Converts exactly as `/import/ccu` does and answers the same shape, plus the favorites: a regadom's favorite pages become nodes of the `favorite` enum — a `_USER<id>` page under that user's name, any other under its own, empty pages left out — and the result carries `favorites` (pages) and `favorites_for` (their names); after the import the favorites sync hands a page to the account of its name. `422 regadom` when the file is not one. Admin only. |
| `POST /import/ccu` | The ReGa bridge from a running CCU / RaspberryMatic / OpenCCU: `{"host", "port"?: 8181, "tls"?: false, "mode"?: "merge"\|"replace", "dry_run"?: false}`. Runs one HM-Script over the CCU's remote script port (the CCU's firewall must allow this system: REGA *full* or the address listed), converts: refs from interface name and address; rooms and functions become flat nodes with slugged ids (umlauts transcribed, collisions get `-2` and are reported in `renamed`); a room or function whose ReGa name is exactly one of the CCU's built-in translation keys (`roomBathroom` or `${roomBathroom}`, the 21 of the WebUI's `translate.lang.extension.js`) gets the WebUI's German name instead (`Badezimmer`, id `badezimmer`), and `occulited` renames nodes still named by such a key at start through ordinary node updates; objects still carrying the CCU's default `<type> <address>` name are counted in `unnamed` and left out, unless they are members of a room or function — then they are kept with the address as name; objects of ReGa's `System` pseudo-interface (the CCU's own objects, no interface process behind them) and of any interface this system's `InterfacesList.xml` does not list are counted in `foreign` and left out, memberships included (occulited B-84; with no interface list read, only `System`; the automatic first-boot import leaves out only `System`, as a radio module may not have answered yet). `occulited` removes at start only the `System.*` objects an earlier import brought in (one revision, logged with the count; a second start finds none); the objects of an interface `InterfacesList.xml` does not list are never removed automatically — `GET /foreign` lists them and `POST /foreign:remove` removes them on the user's word (the maintainer, 2026-10-20: "never automatic"). Answer: `{"result": {"devices", "channels", "rooms", "functions", "skipped", "renamed", "unnamed", "foreign"}, "objects", "mode", "dry_run", "revision"?, "changed"?}`. `502 ccu-unreachable` when the CCU does not answer or returns no devices. Admin only. |
| `GET /foreign` | The objects of interfaces this system does not have (occulited B-84): `{"revision", "list_read", "listed": [<the interfaces InterfacesList.xml lists now>], "interfaces": [{"interface", "objects", "devices", "channels"}]}`, sorted by interface, `System` included while there are any. With no interface list read, `list_read` is `false` and `interfaces` is empty: a missing file offers nothing. Read only; nothing is removed by it or at start. An interface listed here may be one whose radio module or addon is missing only for the moment. |
| `POST /foreign:remove` | Removes the objects of the named interfaces, memberships with them, as one revision: `{"interfaces": ["CUxD", …]}`, honouring `If-Match`. Every name must be one `GET /foreign` lists now: `409 not-foreign` (with `detail.interface`) for one that is listed in `InterfacesList.xml` again or has no objects, `409 no-interface-list` when no list could be read, `422 invalid-body` for an empty list; a refusal removes nothing. Answer: `{"revision", "changed", "removed"}`. The UI asks first (Control, the unassigned channels). |

## The change stream

`GET /api/meta/v1/events/sse` — Server-Sent Events, one JSON event per message. A WebSocket
variant at `GET /api/meta/v1/events` was described here and in the porting kit before it existed;
it is **not implemented** and 404s. SSE is the change stream; whether the WebSocket is
worth adding is on the backlog. The events:

```json
{ "revision": 413, "kind": "object.updated", "ref": "BidCos-RF.JEQ9000001:1", "value": { ...object... } }
{ "revision": 414, "kind": "node.moved", "enum": "room", "from": "room/og/bad", "to": "room/eg/bad" }
{ "revision": 415, "kind": "import", "objects": 212, "enums": 3 }
```

| kind | fields | when |
| --- | --- | --- |
| `object.updated` | `ref`, `value` | create, update, `orphaned` change |
| `object.deleted` | `ref` | delete |
| `enum.created` / `enum.updated` / `enum.deleted` | `enum`, `value` (not on delete) | |
| `node.created` / `node.updated` / `node.deleted` | `enum`, `path`, `value` (not on delete) | rename and icon are `node.updated` |
| `node.moved` | `enum`, `from`, `to` | move or reorder |
| `import` | `objects`, `enums` | after a bulk import; consumers should re-snapshot |

Every event carries the revision it produced. A consumer that observes a gap (`revision` jumped by
more than 1 since its last event) or connects late sends `?since=<revision>`: the server replays
the events it still has (at least the last 1000) or answers with a single `{"kind": "resync",
"revision": N}` telling the consumer to fetch the snapshot. The stream opens with the comment
`: connected` and sends the comment `: ping` every 30 s as its heartbeat (SSE only; there is no
WebSocket here). Its messages carry `data:` alone — no `id:`, no `event:`; resuming is `?since=`.

## Optional MQTT publication

Off by default; `mqtt: {"enabled": true, "broker": "127.0.0.1:1883", "username"?, "password"?,
"prefix"?: "occulite"}` in `occulited.json` turns it on. `occulited` then publishes retained
messages in [mqtt-smarthome](https://github.com/mqtt-smarthome/mqtt-smarthome) style so a consumer
without an HTTP client still gets the names — the whole store on every (re)connect, the revision
last, then every change as it happens:

```
occulite/meta/revision                      412
occulite/meta/object/BidCos-RF.JEQ9000001:1 {"name": "...", "enums": [...]}
occulite/meta/enum/room                     { ...enum... }
```

Deleted objects and enums are published as an empty retained payload; a moved or deleted node and
an import republish the whole store (member paths change). This is a mirror of the store, never an
input to it. The client is MQTT 3.1.1, QoS 0, plain TCP (the broker on the system); TLS and
WebSockets are not offered.

## Error codes

| code | HTTP | meaning |
| --- | --- | --- |
| `invalid-ref` | 422 | ref does not match `<interface>.<address>` |
| `invalid-name` | 422 | empty, too long, or contains line breaks |
| `invalid-id` | 422 | enum or node id violates `[a-z0-9][a-z0-9-]*` / length |
| `unknown-object` | 404 | |
| `unknown-enum` | 404 | |
| `unknown-path` | 422 | node path does not resolve — in a body, in a query (`GET /objects?enum=`) and in a URL (`/enums/{enum}/nodes/{path}`) alike |
| `duplicate-id` | 409 | enum id or sibling node id already taken |
| `duplicate-path` | 422 | the same path twice in an object's `enums` |
| `has-members` | 409 | delete refused; `detail.refs` lists the members |
| `invalid-move` | 422 | node moved under itself or a descendant |
| `too-deep` | 422 | tree depth > 8 |
| `format-unsupported` | 422 | import with `format` > 1 |
| `revision-conflict` | 409 | `If-Match` did not match |
| `not-foreign` | 409 | `POST /foreign:remove`: the interface is listed in `InterfacesList.xml` or has no objects; `detail.interface` names it |
| `no-interface-list` | 409 | `POST /foreign:remove`: `InterfacesList.xml` could not be read, so nothing is removed |
| `unauthenticated` | 401 | no or invalid credential (session, token, or the `?sid=` of an addon page) |
| `forbidden` | 403 | the session or token lacks the route's scope; `scope` names it (`meta:read` or `meta:write`) |
