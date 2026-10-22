# The metadata store: file format

Normative. Two implementations exist — `occulited` (Go) and homematic-manager's `local`
provider (TypeScript) — and both run the fixtures in [`occulited/fixtures/`](../fixtures/). Anything not
specified here is undefined and must not be relied on.

## Purpose and scope

Names and taxonomy for Homematic devices and channels. Not values, not paramsets, not a device
registry, not a time series. The store never invents objects: it holds metadata *about*
addresses the interface processes report.

## The file

One JSON document, UTF-8, `meta.json`. The writer replaces it atomically: write to a temporary
file in the same directory, `fsync`, rename over the old file, and keep the previous document as
`meta.json.bak`. A reader that finds `meta.json` unparsable must fall back to `meta.json.bak` and
report that it did, never start empty.

```json
{
  "format": 1,
  "revision": 412,
  "objects": { "<ref>": <object>, ... },
  "enums":   { "<enum-id>": <enum>, ... }
}
```

| Field | Type | Rules |
| --- | --- | --- |
| `format` | integer | `1`. A reader must refuse a higher number. |
| `revision` | integer ≥ 0 | Increases by exactly 1 on every successful write, never decreases, never resets. `0` is an empty store. |
| `objects` | map | Keyed by `ref`. May be empty. |
| `enums` | map | Keyed by enum id. May be empty; a fresh store contains the two defaults below. |

Key order is not significant. Writers should emit keys sorted for stable diffs.

## Refs

A `ref` identifies a device or a channel: `<interface>.<address>`.

- `interface` is the interface's name as the CCU knows it — `BidCos-RF`, `BidCos-Wired`, `HmIP-RF`,
  `VirtualDevices`, `CUxD` — or any user-defined interface name. It never contains `.`.
- `address` is the device serial (`ABC1234567`, `000A1B2C3D4E5F`) or a channel `SERIAL:N`.
- The separator is the first `.`; everything after it is the address.
- Refs are case-sensitive and compared byte-wise.

Examples: `BidCos-RF.JEQ9000001`, `BidCos-RF.JEQ9000001:1`, `HmIP-RF.000A1B2C3D4E5F:4`.

## Objects

```json
{
  "name": "Deckenlampe",
  "enums": ["room/eg/wohnzimmer", "function/licht"],
  "meta": { "ccu-jack": { "hidden": false } },
  "orphaned": true
}
```

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Required. Non-empty after trimming; **no control characters** (U+0000–U+001F and U+007F, which includes line breaks and tabs); valid UTF-8; ≤ 255 bytes. A name travels into `meta.json`, through the change stream and into every consumer, and a control character corrupts or truncates something on that way — a NUL most of all, which `utf8.ValidString` accepts. (It also used to travel into an HM-Script export, removed 2026-09-08; the rule stands on its own without it.) |
| `enums` | array of node paths | Optional, default `[]`. Each path must exist. No duplicates. Order not significant. |
| `meta` | object | Optional. One key per consumer namespace (`[a-z0-9][a-z0-9-]*`), any JSON value beneath. The store does not interpret it. ≤ 16 KiB serialised per object. The namespaces in use are listed under *Known namespaces* below. |
| `orphaned` | boolean | Optional, default `false`. Set by the owning process when the address is no longer reported by any interface; cleared when it reappears. Never set by clients. |

### Known namespaces

The store treats every namespace alike; these are the ones a consumer reads, so that another one does not reuse them
for something else:

| Namespace | Owner | Value |
| --- | --- | --- |
| `control` | occulited's Control app | `{"icon": "<name>", "icons": {"closed": "<name>", "open": "<name>", "tilted": "<name>", "on": "<name>", "off": "<name>"}}` — the icons a channel's tile shows, each any name of the KNX User Forum icon set (such as `light_floor_lamp`, `message_socket`, `vent_ventilation`, `message_bell_door`, `fts_window_2w_open`). `icon` is the one picture of a switch or key channel; `icons` the pictures per state, each key optional: a door/window contact's `closed`, `open` and `tilted` (`tilted` only for a sensor that reports it), a switch's `on` and `off` (occulited task 110). For a switch, `icon` stays the fallback for a state without its own (its "on" picture where the set has one, e.g. the light's and the socket's). Written by an account that may change the metadata, from Control's assignment sheet; an absent key means *automatic* (for a switch a keyword in the name, the device type, else the light; for a key the switch symbol; for a contact the window or door pictures). Control ignores a name it does not know, and a key it does not know is left as it is - a later addition (a picture family, say) sits beside these. |

An object with a name and nothing else is valid. An entry is never deleted by the store on its own;
`orphaned` is how "gone" is expressed, so that a replaced device keeps its room assignment.

## Enums

An enum is a named taxonomy holding a tree of nodes.

```json
{
  "name": { "de": "Räume", "en": "Rooms" },
  "tree": [
    { "id": "eg", "name": "Erdgeschoss", "children": [
      { "id": "wohnzimmer", "name": "Wohnzimmer", "icon": "sofa" }
    ]},
    { "id": "og", "name": "Obergeschoss" }
  ]
}
```

| Field | Type | Rules |
| --- | --- | --- |
| enum id (the map key) | string | `[a-z0-9][a-z0-9-]*`, ≤ 32 chars. |
| `name` | object | Localised display names, at least `en`; keys are BCP-47 primary tags (`de`, `en`). |
| `tree` | array of nodes | Ordered. Order is display order and is significant. |

### Nodes

| Field | Type | Rules |
| --- | --- | --- |
| `id` | string | `[a-z0-9][a-z0-9-]*`, ≤ 32 chars, **unique among its siblings**. Stable: renaming a node never changes its id. |
| `name` | string | Required, non-empty, ≤ 255 bytes. |
| `icon` | string | Optional, `[a-z0-9-]+`, an icon name the UI maps; unknown names are ignored, not errors. |
| `children` | array of nodes | Optional, default `[]`. Ordered. |

Depth is limited to 8 levels below the enum.

### Paths

A node is addressed by its path: `<enum-id>/<id>/<id>/...`, e.g. `room/eg/wohnzimmer`. An enum
itself is not a valid membership target — an object belongs to nodes, not to enums.

A **subtree query** for `room/eg` returns every object whose `enums` contains `room/eg` or any path
that starts with `room/eg/`. That is the point of the tree: "everything on the ground floor" is one
query.

### The two defaults

A fresh store contains, with empty trees, what a CCU has:

| id | name.de | name.en |
| --- | --- | --- |
| `room` | Räume | Rooms |
| `function` | Gewerke | Functions |

They are ordinary enums: a user may add nodes, rename them, or delete them entirely. Consumers that
map to ReGa's fixed rooms and functions use `room` and `function` and flatten the tree.

A floor needs no taxonomy of its own: it is a room with rooms below it (`room/eg/wohnzimmer`).
Stores created before 2026-09-11 started with a third default, `floor` (Etagen/Floors); they keep
it, the format version did not change, and a consumer must treat enum ids as data.

## Invariants a writer must uphold

1. Every path in any object's `enums` resolves to an existing node.
2. Node ids are unique among siblings; enum ids are unique.
3. `revision` increases by exactly 1 per write, and a write that changes nothing does not happen.
4. The document on disk is always complete and parsable (atomic replace).

## What changes revision

Any successful mutation: object create/update, enum create/update/delete, node create/rename/move/
delete, membership change, an import. Setting `orphaned` by the owning process is a mutation too.
Reads never do.

## YAML

The same document may be exported as YAML and imported from it (`/api/meta/v1/export?format=yaml`,
`PUT /api/meta/v1/import`). YAML is a transport, not the store: an import is validated as a whole,
applied as one revision, and rejected entirely on the first error. Comments do not survive a
round trip and clients must not expect them to.
