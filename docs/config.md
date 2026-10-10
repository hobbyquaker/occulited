# occulited.json

`occulited`'s own configuration: one JSON file, `/usr/local/etc/occulite/occulited.json`
(`--config` overrides the path). Every key has a default that makes an unconfigured system work;
the file may be absent. The daemon rewrites it atomically when a switch is flipped in the UI
(the firmware download switch), so keep it valid JSON with no comments.

```json
{
  "listen": "127.0.0.1:8183",
  "state_dir": "/usr/local/etc/occulite",
  "log_level": "info",
  "firmware": { "enabled": false, "dir": "/etc/config/firmware" },
  "catalog": { "enabled": true, "urls": ["https://raw.githubusercontent.com/hobbyquaker/occulited/master/catalog/catalog.json", "file:///etc/occulite/catalog.json"], "daily": false },
  "mqtt": { "enabled": false, "broker": "127.0.0.1:1883", "username": "", "password": "", "prefix": "occulite" },
  "hmipserver": { "diagrams": false },
  "system_update": { "enabled": false, "feed": "https://api.github.com/repos/hobbyquaker/openccu-lite/releases?per_page=20" },
  "addons": { "default_mode": "confined", "legacy_session": true, "legacy_session_off": [], "early_start": true, "early_start_off": [] },
  "auth": {
    "mode": "local",
    "oidc": {
      "enabled": false,
      "name": "authentik",
      "issuer": "https://auth.example.org/application/o/openccu-lite/",
      "client_id": "…",
      "client_secret": "…",
      "username_claim": "preferred_username",
      "scopes": "openid profile email",
      "password_login": true
    }
  }
}
```

| key | meaning |
| --- | --- |
| `listen` | Loopback only, enforced. lighttpd proxies to it. `127.0.0.1:8183` by default (the port ReGaHSS had; this firmware has no ReGaHSS); until 2026-09-22 it was `127.0.0.1:2121`, CCU-Jack's port, and a file that still carries that old default is moved to the new one at load — a port chosen on purpose stays. |
| `state_dir` | `meta.json`, `users.json`, `local-token` (the system's token with `meta:read` alone, `0644`; security.md), the firmware/catalogue caches, `firewall.json` (the user's own firewall ports — the file's `USERPORTS` is that list plus the addons' opened ports, see system-api.md `/firewall`), `acme/` and `tls/`, `warnings.json` (the Status page's silences) (below). On `/usr/local`, so it is in every backup — except `sessions/`, the session store (below), which no backup contains. |
| `log_level` | `debug`, `info` (default), `warn`, `error`. `debug` logs every request. Where the log goes is the `--log` flag: `auto` (syslog unless stderr is a terminal — the init script's case), `stderr`, `syslog`. **Set on the Log page's settings** (`PUT /loglevels` `occulited`): applied at once, without a restart, and written here, so it survives a restart and is part of the backup. The privilege helper (`occulited helper`) reads it at its own start (`--config`, the same default path) and takes every later change from occulited. A value that is not one of the four logs at `info` with a warning at start. |
| `log_debug_areas` | Optional list: the parts of occulited whose lines are written from debug up whatever `log_level` says — `acme`, `radio-firmware`, `addons`, `metadata`, `http` (one line per request), `auth` (local logins, never a password), `led`. For a debug hunt in one corner without every request line of the others; at `debug` it says nothing more. Absent or empty = none. Debug lines cost RAM journal space, and SD card writes with a persistent journal or ram-sync. |
| `firmware.enabled` | The daily device-firmware check and download from eQ-3 (*Check daily* on the Updates page). **Off by default** (D-90, B-241): the welcome page asks once, naming eQ-3. Off = *Check now* and manual upload only. |
| `firmware.dir` | Where bundles go; the interface processes read `/etc/config/firmware/<type>/`. |
| `firmware.base` | Override the eQ-3 update server (tests). |
| `catalog.urls` | Catalogue files, merged in order (first occurrence of a repository wins); `file://` allowed. The default is the published file first, then the copy the image carries in `/etc/occulite/catalog.json`. **A published file is fetched only by the user's check and by the daily check** (B-240): the Addons page, the start of the service and an install read the copy the last check left in `<state>/catalog-cache.json` (`catalogs`, by URL) and the bundled copy — a system that never ran a check has its bundled catalogue and nothing goes out. An unreachable URL is skipped as long as another delivered entries; a published file that fails to fetch keeps its last copy. |
| `catalog.daily` | The daily check behind the Addons page's *Check daily*: the catalogue files again, the latest releases of the addons already known (conditional GitHub calls), and the installed addons' own update checks. **Absent = off** (D-90, B-241; it meant on until then — see *The outbound switches* below). |
| `mqtt` | Mirror the metadata store to a broker as retained messages (meta-api.md → MQTT); the mosquitto addon on the system is the usual target. |
| `auth.mode` | `local` (default): the users in `users.json`. `oidc`: those plus the external login below. `off`: **no login at all** — every caller, on the API, the addon pages and through lighttpd's gate, is an anonymous administrator; for a system that is alone on a trusted network and nothing else. Set on the Settings page; takes effect at the next start of occulited. |
| `auth.oidc` | External login through OpenID Connect, below; `mode: oidc` switches it on. |
| `auth.session_idle`, `auth.session_max` | The login sessions' idle timeout and absolute lifetime (openccu-lite task 262), Go durations such as `"30m"` and `"12h"`. **Absent = the defaults, 30 minutes and 12 hours** — ASVS Level 2's numbers for a system that controls a home; the values before, `"24h"` and `"720h"` (30 days), are what a household may set back. Bounds: idle 5 min–30 d, lifetime 1 h–90 d, idle ≤ lifetime; a value that is not a duration or out of bounds is logged at start and the default stays. Set on System → Users → Authentication → Sessions (`PUT /api/auth/v1/config`); in force at once, running sessions end by the new limits at their next request. |
| `addons.default_mode` | systemd products only: how a newly installed addon runs. **`confined` is the default**: its own user `addon-<id>`, `ProtectSystem=strict`, no capabilities, write access to its own directories plus whatever the catalogue entry's `runtime` block declares; an entry with `runtime.root: true` stays root. `root` flips the whole system back to the rc.d ABI as it always was. Anything else, including an empty or misspelt value, means `confined` — a default fails towards the closed side. Each addon can be switched on the Services page (root is the *unsafe* opt-out and needs `"unsafe": true`, see system-api.md); the choice is stored on the userfs and survives updates. Addons that were already installed when a system updates into this default are pinned to `root` once per system and shown as such; the switch is per addon and manual. |
| `addons.legacy_session`, `addons.legacy_session_off` | Whether the shell passes the session's legacy alias as `?sid=@…@` in the URLs of the addons that live by the CCU convention — those without `runtime.session.header_since` for their installed version, and every addon outside the catalogue. `legacy_session` absent or `true` means on (the default); `legacy_session_off` names the addons it is switched off for. Written by the Addons page: its section Addon sessions (`PUT /legacy-session`) and the ⋯ menu of an addon's row (`PUT /addons/{id}/legacy-session`). Off, such an addon's pages get no session in their URL and refuse to open until the addon reads `X-Occulite-Session`. |
| `addons.early_start`, `addons.early_start_off` | Whether an addon whose catalogue entry declares `runtime.start: "early"` starts before the radio interfaces are ready. `early_start` absent or `true` means on (the default); `early_start_off` names the addons it is switched off for. Written by the Addons page: its section Addon start (`PUT /early-start`) and the ⋯ menu of a declaring addon's row (`PUT /addons/{id}/early-start`, or `start_early` in `PUT /addons/{id}/policy`). occulited writes `addon-policy/<id>.start` from the declarations and these switches; the change takes effect at the next boot. |
| `hmipserver` | `diagrams` (occulited task 33, **off by default**): whether hmipserver's diagram data (`/var/hmipserver/measurement`, the jar's measurement service) is carried between its tmpfs and the stick (`/media/usb0/measurement`) at its start and stop, as the CCU did. openccu-lite has no WebUI to configure or show a diagram, and the service cannot be switched off in the jar; it records only configured diagrams, so on a system that never was a CCU nothing is written. **With it off** there is no copy at start or stop, and a migrated CCU's diagram data - the stick's `measurement/` and the WebUI's copy `/usr/local/etc/config/measurement` - is removed once, each with its path and size in the journal (`diagram data: … removed`), recorded in `/usr/local/var/lib/occulite/measurement-removed.json`; the marker waits until `/media/usb0` exists, so a stick mounted after the first start is still found. The CCU backup from before the switch is the way back. `true` restores the copy (on a FAT stick without owners and xattrs, B-62). `GET`/`PUT /hmipserver/settings`; read by the radio steps, so a change applies at hmipserver's next start. No page shows it yet. |
| `system_update` | The release feed check: `enabled` (**off by default**, D-90, B-241) makes one outbound call a day to `feed` (GitHub's release list; a system on a prerelease follows prereleases, see system-api.md's *System firmware update*; the earlier default `…/releases/latest` is moved to the new one when the file is read) and shows the newest release for this product on the Status page; the download and the install only ever happen on request. On the LXC products the check runs and names the newer template — `openccu-lite-lxc-<arch>-<version>.tar.xz` — but nothing is downloaded or installed from inside: the template is swapped on the host and `/usr/local` survives ([install-lxc.md](https://github.com/hobbyquaker/openccu-lite/blob/main/docs/install-lxc.md)). |
| `rpc.streams_per_session` | lite-rpc's open event streams per token or login session (`GET /api/rpc/v1/events` and `/events/ws` together), occulited task 19. **Absent = 3** (2 until then): the Control app keeps one stream per window, so a wall tablet, a phone and a desktop under one account stay live; a window over the limit gets `429 too-many-streams`, tries again on its own and says *not live* meanwhile. 1 to 16 (16 is the total for all clients); a value out of bounds is logged at start and the default stands. Read at the start of occulited. Only the shown App windows hold a stream (occulited B-53). Keep it at 4 or below for browsers: over https lighttpd's HTTP/2 allows eight streams at once per connection, and one browser's App windows beyond that would hold back its other requests. |

### The outbound switches

`firmware.enabled`, `system_update.enabled` and `catalog.daily` are the three comfort functions that call the internet
without a button press (D-90). **A fresh system has all three off**; its welcome page asks once, one checkbox per
destination (GitHub: the release check, the catalogue and the addons' update checks; eQ-3: the device firmware), and
each check keeps its *Check daily* beside its *Check now*. At its first start this version **writes the three keys
explicitly** when the file lacks them (`config.SettleOutbound`): on a system whose setup is done (an administrator
exists, or `auth.mode` is `off`) with the values it has been running with — on, the defaults before B-241 — so nothing
changes under a user who chose or accepted them; on a fresh system with off, so a restart after the setup cannot turn
them on. A file written by an earlier version always carries the two booleans and lacks `catalog.daily` unless the
user switched it, so on an upgrade only `catalog.daily: true` is added. The decision is taken once: a file that
carries all three is never changed by it, whatever the setup's state. The log says which keys were written.

## The certificate state: `<state_dir>/acme/`

Nothing in `occulited.json`: the ACME configuration is set on System → Certificate and kept in
its own directory, `0700`, every file `0600` to the `occulite` user, on the userfs and so in
every backup.

| file | content |
| --- | --- |
| `settings.json` | The settings as system-api.md's `PUT /certificate/settings` takes them, **secrets included** (the EAB HMAC, the DNS provider's tokens): `{"mode", "directory", "directory_url", "ca_root", "email", "eab_kid", "eab_hmac", "names", "challenge", "dns_provider", "dns_credentials"}`. Absent = self-signed, Let's Encrypt and HTTP-01 pre-selected. occulited rewrites `names` after a hostname change while they still equal the old host's default `<host>.<domain>, <host>` or are empty; names set by hand are never touched. A domain change (another DHCP lease, the host name unchanged) rewrites `names` the same way while they equal `<host>.<old domain>, <host>`; an empty list stays empty. |
| `domain.json` | `{"domain"}`: the DNS domain (from `/etc/resolv.conf`) occulited saw last, lower case — the "old domain" of the rule above. Looked at when occulited starts, on every `GET /network` (the Network page polls it) and `GET /certificate`, before a rename is applied, and at every renewal check. The first domain seen is only written; an empty domain (resolv.conf without the lease yet, at boot) is neither written nor acted on. Absent = nothing seen yet. |
| `account.key` | The ACME account key (P-256, PEM), one for every directory; made on the first attempt. |
| `account.json` | What each directory answered when that key registered, by directory URL — so a renewal does not register again, and a system that lost the file finds its account by the key. |
| `cert.pem`, `key.pem` | The last issued chain (leaf first) and its private key. The live file `/etc/config/server.pem` is the two joined — chain, then key — written by the privilege helper as `root:certs 0640`. |
| `last.json` | The last attempt, without its lines: those are in the journal under the attempt's `run_id` (system-api.md `GET /certificate` → `last`). |

The renewal timer is in the daemon (twice a day, renew when fewer than 30 days or less than half
the lifetime remain, **in mode `acme` only**); there is no unit or cron entry. S50lighttpd's own
`check_certificate` leaves the file alone while the marker `/etc/config/server.pem.managed`
(one line, `<mode> <issuer>`, written by the helper with the file, removed on the switch
back; the `/etc/config/server.pem.acme` with the issuer alone is written beside it until
the fork's script reads the new name) is there — without it a LAN CA's 24-hour certificate
failed the script's one-day check on the reload that installed it.

## The manual certificate state: `<state_dir>/tls/`

Mode `manual` — the user's own certificate, or one a CA signed for a request the system made. The
directory is `0700`, every file `0600` to the `occulite` user, on the userfs and in every backup.

| file | content |
| --- | --- |
| `key.pem` | The private key, PKCS#8 PEM: of the last request the system generated, or of the last certificate installed by hand (kept so a later request can use the same key). **Never handed out** by any route. |
| `csr.pem`, `csr.json` | The pending request (PEM) and its description (`{algorithm, fingerprint, cn, sans, org, created}`); both removed once a certificate for that key is installed. `GET /certificate/csr` downloads the PEM. |
| `cert.pem` | The chain last installed by hand, leaf first — what went into the live file with `key.pem`. |

The mode itself is `acme/settings.json`'s `mode` (`manual`); the live file is the same
`/etc/config/server.pem`, written by the helper with the marker naming the mode.

## The sessions: `<state_dir>/sessions/`

Login sessions survive a restart of occulited and a reboot, and end after 30 minutes idle or 12 hours after
the login (the defaults since openccu-lite task 262; `auth.session_idle`, `auth.session_max` above). The directory is occulited's
own, `0700` to the `occulite` user, and carries an empty `.nobackup`: the firmware's backup tars
`/usr/local` with `--exclude-tag=.nobackup`, which leaves out a directory holding the tag — so the
store has a directory of its own, and neither a backup nor a restore carries a session.

| file | content |
| --- | --- |
| `sessions.json` | `0600`. `{"version": 1, "sessions": [{"hash", "user", "role", "method", "created", "last_seen", "idle_expires", "expires", "remote", "agent"}]}`: `hash` is the SHA-256 of the session id, lower-case hex — **the id itself is never written** — `method` how the session was opened (`password`, `oidc`), `expires` the login time plus 30 days, `idle_expires` `last_seen` plus 24 hours, times RFC 3339. Written atomically at a login, a logout, *sign out everywhere*, a session ended from the list, a password change, a user deleted or given another role, an expiry and a clean shutdown; a request writes `last_seen` only when the stored one is at least ten minutes old. Auth-off's anonymous session is never in it. |
| `.nobackup` | Empty; written again before any write of the store if it went missing. |

At start occulited restores the sessions whose hash, user (still in `users.json`, and with a
password for a `password` session; an `oidc` session fits any account), login method (one the running `auth.mode`
offers: `password` in `local` and `oidc`, `oidc` where the provider is configured, none in `off`)
and both expiries still fit, with the role `users.json` gives the user now; it rewrites the store
without the rest and rebuilds the gate's mirror `/run/occulite/sessions` (the `--session-dir`
flag: one file per session named by the same hash, containing the user name, `0644` in a `0711`
directory) before it serves. A store that cannot be read or is not valid JSON restores nothing,
logs a warning and never stops the start; a corrupt one is replaced at once, an unreadable one at
the next login; so does a store of another `version`. Deleting the directory ends every session at
the next start. A password set with `occulited passwd <user>` removes that account's entries (the
command never creates the store or its directory), and a running occulited follows a changed
`users.json` at the next session check: the sessions of a removed account or a changed password
end, a changed role is taken over.

## Backup encryption: `<state_dir>/backup-encryption.json` and `<state_dir>/backup-key/`

Encrypted backups (the routes in system-api.md under *Backup and restore*): the `.sbk`
unchanged inside one age v1 stream (`filippo.io/age`, pure Go) for two X25519 recipients — the
system's own identity and the user's recovery key. Two places, deliberately apart:

| path | in a backup? | content |
| --- | --- | --- |
| `backup-encryption.json` | **yes** | `0600`. `{enabled, recovery: {recipient, fingerprint, created}, previous: [{recipient, fingerprint, created, retired}]}` — the recovery key's **public** half (`age1…`) and its predecessors, and the switch. In every backup on purpose: a system restored from it keeps encrypting to the same recovery key (Home Assistant behaves the same way), and a restore can say which key an older backup needs. The API shows fingerprints only. |
| `backup-key/` | **no** | `0700`, carries an empty `.nobackup` — written before the identity, and again before every use if it went missing — so `createBackup.sh`'s `--exclude-tag=.nobackup` (and the recovery system's `create_backup.cgi`) leaves it out of every archive. `box-identity.txt` (`0600`) is the system's own age identity (`AGE-SECRET-KEY-1…`): it decrypts this system's backups without asking, which is safe because it only opens what this system holds in plain anyway. `created.json` lists the last 500 backups handed out (`{sha256, name, size, time, encrypted}`), so `/restore/check` can say `created_here`. |

**The recovery key** is never on the system: a 28-symbol Crockford base32 code (128 random bits and a
10-bit checksum, `XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX`) made in the browser, from which the age identity
is derived with HKDF-SHA256 (salt `openccu-lite backup recovery key`, info `age X25519 identity v1` —
mirrored in `internal/backupcrypt/code.go` and `ui/src/lib/recoverykey.ts`, pinned by a vector in both
test suites). The emergency kit prints the code and the derived identity, so `age -d -i key.txt` on a PC
opens a backup without openccu-lite.

**Across a restore:** `/restore/apply` copies the identity to `/usr/local/tmp/.occulite-box-identity`
(`0600`, occulited's) first. The restore at boot deletes `/usr/local` except `tmp`; `S06InitSystem` empties
`/usr/local/tmp` but keeps its dotfiles; no backup holds that directory. At the next start occulited adopts
the file when `backup-key/` came back empty and removes it either way. A factory reset makes the userfs
anew and carries nothing — intended: those backups then need the recovery key.

## Shares: `<state_dir>/shares.json` and `<state_dir>/shares/`

The SMB and NFS shares of System → Storage, mounted on demand at `/media/net/<name>` by PID 1 (the
helper renders `media-net-<name>.{mount,automount}` into `/run/systemd/system` at every start of
occulited). In every backup, like the backup targets: a restored system mounts its shares again
without anyone typing the password.

| File | What |
| --- | --- |
| `shares.json` | `0600`. `{shares: [{id, kind: nfs\|cifs, server, path, version, seal?, read_only, user?, domain?, created}]}` - no secret, no derived field. `id` is the name: `^[a-z][a-z0-9]{0,15}$`. |
| `shares/<id>/` | `0700`, occulite's. `cifs.cred` (`0600`) - an SMB share's `username=`, `password=`, `domain=`, the file the rendered mount unit names (the helper fixes the path by the id and a flag; the daemon never names it). Always there for an SMB share (an empty password for a guest account). |

## Backup targets: `<state_dir>/backup-targets.json` and `<state_dir>/backup-targets/`

Where the nightly backup goes besides a download (the routes in system-api.md under
*Backup and restore*). Both are **in every backup**: a restored system reconnects to its targets
without anyone typing a password or adding a key again - which also means the target's credentials
travel onto the very target they open, hence the page's advice of a dedicated NAS account.

| path | content |
| --- | --- |
| `backup-targets.json` | `0600`. `{targets: [Target without secrets and derived fields], directory?: {location?, disabled?, encrypt?, append_only?, name?}}` - `location` is the directory target as the location picker chose it (`usb:<label>/<folder>`, `userfs:backup…`), from which the path is derived; a target of kind `share` carries `share: {id, folder}` (the `nfs`/`cifs` targets are migrated to it at start). The USB directory target is not in `targets`: its path and count are upstream's markers `/etc/config/CronBackupPath` and `CronBackupMaxBackups` (so `GET/PUT /backup/schedule` and a restored OpenCCU backup keep working); `directory` holds its own switches. `/etc/config/NoCronBackup` is the nightly switch for every target. |
| `backup-targets/<id>/` | `0700`, occulite's. `id_ed25519` (`0600`, OpenSSH format) and `id_ed25519.pub` - an SFTP target's own key; `host_key` - the server's key the user confirmed (an authorized_keys line); `cifs.cred` (`0600`) - mount.cifs's credentials file (`username=`, `password=`, `domain=`), the path the rendered mount unit names, fixed by the id; `last.json` - the last delivery's result (`{at, instance, ok, state, step?, error?, name, size, duration_ms, sha256, encrypted, removed?, last_ok?}`), written by the process that delivered (root for the directory and the mounts, then chowned to occulite; occulite for SFTP). |

**At runtime, not in the state directory:** the mount units `/run/systemd/system/media-net-<id>.{mount,automount}`
(rendered by the helper from the checked fields, again at every start of occulited - `/run` is empty after
a boot), the mount point `/media/net/<id>`, and the run's staging directory `/usr/local/tmp/occulite-backup/`
(`0700` occulite: `run.json` and the `.sbk`/`.sbk.age` between the create and the deliver unit; emptied
by the deliver unit, and by the next boot).

## The warnings' silences: `<state_dir>/warnings.json`

The Status page's silences (system-api.md → *Status page warnings*):
`{"silences": [{"id", "variant", "by", "at", "until"}]}` — the warning's id and variant, the
administrator who set it, when, and when it ends (RFC 3339, UTC). Written atomically by occulited
itself, `0600` to the `occulite` user: the state directory is the daemon's own, so no helper is
involved, as for `acme/` and `storage-writes.json`. Rewritten whenever a silence is set, lifted,
runs out or is spent because its warning cleared. On the userfs, so it survives a reboot and is in
every backup. Absent = no silences; a file that is not valid JSON is logged, read as none and
replaced at the next write.

## The status LED: `<state_dir>/led.json`

What `PUT /api/system/v1/led` stores (system-api.md → *Status LED*). Written atomically by
occulited itself, `0600` to the `occulite` user, in its own state directory (no helper), so it
survives a reboot and is in every backup. These are the defaults:

```json
{
  "enabled": true,
  "brightness": 100,
  "fade": true,
  "normal": {"color": "blue", "pattern": "solid"},
  "night": {"enabled": false, "from": "22:00", "to": "06:30", "show": "errors", "dim": 30},
  "states": [
    {"id": "radio-down", "enabled": true, "color": "red", "pattern": "solid"},
    {"id": "no-network", "enabled": true, "color": "yellow", "pattern": "fast"},
    {"id": "service-failed", "enabled": true, "color": "red", "pattern": "slow"},
    {"id": "storage-replace", "enabled": true, "color": "red", "pattern": "double"},
    {"id": "interfaces-starting", "enabled": true, "color": "magenta", "pattern": "double", "over_normal": true},
    {"id": "radio-duty-cycle", "enabled": false},
    {"id": "radio-carrier-sense", "enabled": false},
    {"id": "external", "enabled": true},
    {"id": "status-warning", "enabled": true, "color": "yellow", "pattern": "slow"},
    {"id": "system-update", "enabled": true, "color": "cyan", "pattern": "slow"},
    {"id": "addon-update", "enabled": false, "color": "cyan", "pattern": "double"},
    {"id": "no-internet", "enabled": false, "color": "blue", "pattern": "fast"}
  ],
  "warnings_off": [],
  "addon_units": false,
  "locate": {"color": "white", "pattern": "fast", "duration_s": 300},
  "external": {"max_duration_s": 3600, "allow_until_cleared": true},
  "pwr_error_light": false,
  "radio": {
    "duty_cycle": [
      {"enabled": false, "threshold": 25, "color": "yellow", "pattern": "breathe", "over_normal": true},
      {"enabled": false, "threshold": 50, "color": "#ff4000", "pattern": "slow", "over_normal": true},
      {"enabled": false, "threshold": 75, "color": "red", "pattern": "fast"}
    ],
    "carrier_sense": [
      {"enabled": false, "threshold": 5, "color": "yellow", "pattern": "breathe", "over_normal": true},
      {"enabled": false, "threshold": 10, "color": "#ff4000", "pattern": "slow", "over_normal": true},
      {"enabled": false, "threshold": 20, "color": "red", "pattern": "fast"}
    ]
  }
}
```

- **`radio`** (openccu-lite task 316): the radio load's levels behind the `radio-duty-cycle` and
  `radio-carrier-sense` rows - three per kind, thresholds rising within 1–99, each level with its
  switch and a look that lights (`over_normal` = the page's *overlay*: the blink's dark phase shows
  the normal colour; off = the level replaces it). The rows carry the place and the switch, the
  levels the rest; a file without the block gets the defaults above. The hysteresis is fixed at 5
  points.

- **Colours** `red`, `green`, `blue`, `yellow`, `cyan`, `magenta`, `white`, `off` - or, since
  openccu-lite task 315, any `#rrggbb` (lower-cased; a value that is exactly a name's becomes the name,
  black becomes `off`), mixed on an RPI-RF-MOD driven over PWM and shown as the nearest of the seven on
  an on/off LED; **patterns** `solid`, `slow` (500/500 ms), `fast` (100/100 ms), `flash` (100/1900 ms),
  `double` (two 150 ms flashes every 2 s), `breathe` (a 2 s pulse from dark to the colour and back;
  `slow` on an on/off LED), `alternate` (with `color2`, a colour on other channels: yellow and blue,
  red and blue, …). `off` is always solid.
- **`brightness`** (10–100, default 100) is the LED's level in percent over PWM; **`fade`** (default
  true) cross-fades a change of look over 300 ms; **`night.dim`** (1–100, default 30) is the level of
  what the night still shows, and **`night.show: dimmed`** shows everything at that level. A file
  from before these fields reads as the defaults. An on/off LED ignores all three.
- **`over_normal`** (optional, per state; absent = off, and not written while off): the
  state blinks over the `normal` colour instead of over dark — for `slow`, `fast`, `flash` and
  `double` only, dropped for every other pattern and on `normal`, `locate` and the `external` row. With
  `normal` off, in the night window, with the LED switched off, or in normal's own colour it blinks
  over dark as without it (system-api.md → *Status LED*, *Blinking over the normal colour*). Example:
  `{"id": "status-warning", "enabled": true, "color": "yellow", "pattern": "slow", "over_normal": true}`.
  A file written before the field existed loads unchanged. The one default that has it on is
  `interfaces-starting`: a double magenta blink over the normal colour.
- **`states`** is the priority list, the first active and enabled row wins; `external` is where the
  API's overrides rank. A state the file lacks (one a later version adds) is put in at its default
  place. `booting`, `shutdown`, the page's test and `locate` are above the list and not in it.
- **`warnings_off`**: Status page warning ids `status-warning` ignores. **`addon_units`**: a failed
  addon unit counts as `service-failed`. **`night.show`**: `errors` (only `radio-down`,
  `service-failed`, `storage-replace`), `off` or `dimmed`. **`external.max_duration_s`**: 60 to 86 400.
  **`locate.duration_s`**: 10 to 3600. **`pwr_error_light`**: a system without an RGB LED blinks the
  Pi's red power LED while an error state is active.
- **Absent** = the defaults — switched off (`enabled: false`) while `hss_led`'s old
  `/etc/config/disableLED` is on the userfs, until a configuration is saved. A file that is not
  valid JSON or not a valid configuration is logged and the defaults apply.

## External login (OpenID Connect)

Any provider with a discovery document works — authentik, Keycloak, Authelia, Zitadel. The
flow is authorization code with PKCE; claims are read from the userinfo endpoint.

**An account here for every provider login, matched by username** (since
2026-09-15): the value of `username_claim` (default `preferred_username`) must equal the name of
an account in `users.json` — exactly, as a string: no case folding, no characters replaced, no
fall-back to the e-mail claim. Any account matches, with or without a password. No such account:
no session, nothing written, the login page says *There is no account {name} on this system — an
administrator has to create it first*, and the journal carries the name and the remote address.
**Nothing is ever created from a provider identity.** The provider is trusted with the name —
that is the design: whoever signs in at the provider as `alice` is `alice` here, and a rename at
the provider means a new account here. **The role is the account's**, set on the Users
page like any account's; the keys `groups_claim` and `admin_groups` of the first shape (a
group-to-role mapping, applied on every login) are gone — a file that still carries them loads,
occulited logs one line at start that they are ignored, and the next save of the file drops
them. The `external` and `subject` fields of `users.json` (the provider's name and its `sub`,
bound on first sight) are not read any more either; an account created by that first shape stays
— same name, no password — and is matched by name like every other.

**Accounts without a password** (`hash` empty in `users.json`): while a provider is configured,
`POST /users` and the Users page take the password as optional; such an account signs in through
the provider only, and the Users page shows it as *provider only* with the last provider login
(`last_provider_login`). *Set password* on the Users page (an administrator) or `occulited passwd
<user>` on the console gives it one. `must_change_password` is a password's flag: a session the
provider opened is not sent to change one.

**`password_login`** (default `true`) is the switch for the password form beside the provider's
button: with `false` the login page shows only *Sign in with {name}*, `POST /login` answers
`403 password_login_disabled` for every account, `POST /password` the same, and the Users and
Account pages offer no passwords. The switch holds only in mode `oidc` — in `local` and `off` it
reads `true`, so switching the provider off switches the password back on — and it can only be
turned off on System → Users (Authentication) from a session that came through the provider, so the
administrator who turns it off has just proven the provider signs them in. It takes effect at
once: occulited reads the file again before every password login whenever it changed (inode,
size, mtime). Sessions that are open when it goes off stay open. The first-boot setup always takes
a password: without an account there is nothing to match.

**The break-glass is the console.** With password login off, a provider outage locks the web
interface, and the way back is SSH or the console, as root: `occulited auth password-login on
[--config PATH]` writes the switch back (the running daemon follows without a restart; the file
keeps its owner), and `occulited passwd <user>` sets or resets a password (a provider-only
account gets one). `occulited auth password-login off` exists too, for scripts and tests, and is
refused outside mode `oidc`.

**The console's account commands** (occulited task 14), root only: `occulited admin list` shows
the accounts (level, whether a password is set or must be changed, how many passkeys, keys that
cannot sign in), and `occulited admin reset-auth <user>` resets one account's access — its
passkeys removed, a new one-time password printed (to be changed at the next login,
`must_change_password`), its sessions ended. Each reset writes `auth: access reset on the console`
(Warn, `user`, `passkeys_removed`) to the journal and `console_reset` into the account in
`users.json`, and the Status page shows the notice `console-reset` for a week. Both take
`--state-dir DIR` (default `/usr/local/etc/occulite`).

Redirect URI to register at the provider: `http(s)://<host>/api/auth/v1/oidc/callback` — the
scheme follows how the browser reached the system (lighttpd forwards it), so register both if you
use both.

**authentik**: create an OAuth2/OpenID provider (confidential client, redirect URI as above,
signing key any), an application bound to it, and note the *OpenID Configuration Issuer*
(`https://auth.example.org/application/o/<slug>/`). With the default scope mapping
`preferred_username` is the user's login name in authentik; create the account here under
exactly that name, without a password if it should sign in through authentik only.

The button on the login page reads `name`.

## The system update from the console: `occulited update`

`occulited update` (occulited task 22) updates the system from the command line - over SSH, from
a script or a timer - as the Updates page does it. Root only. It is a client of the running
occulited: it calls the same routes as the page (`docs/system-api.md`, *System firmware update*)
with **the console's credential**, an API token minted at every start of occulited whose secret
is in `/run/occulite/console-token` (`0600`, root; accepted from the loopback only; the journal
names its calls `token:console`). No user token is needed; occulited has to be running.

| Command | What it does |
| --- | --- |
| `occulited update check [--pre\|--stable]` | Asks the release feed now: the installed version, the channel, the newest release for this system. Exit **100** when it is an update, **0** when the system is up to date. |
| `occulited update install [latest] [--pre\|--stable]` | Installs the newest release of the channel, when it is newer than the installed one (else it says *up to date* and exits 0). |
| `occulited update install <version>` | Installs that published version, from either channel: a newer one, the installed one again, or an older one (a **downgrade**, which warns that settings only the newer version knows may be lost). `v1.0.0-dev.41` and `1.0.0-dev.41` both work. |
| `occulited update install --file <path>` | Installs a release file already on the system; a `<path>.sha256` beside it is checked, a mismatch refuses the file. |
| `occulited update status` | The installed version, the staged file (and whether it installs at the next boot), the last check. |
| `occulited update discard` | Removes a staged file. |

**The channel** is the Updates page's: a system that runs a prerelease (every `-dev` release) follows
prereleases, one on a release sees releases only. `--pre` and `--stable` override it for one call.

**`install`, step by step:** the question - *Install … and reboot?*, a reinstall's or a downgrade's
with its warning - answered `y` on the terminal or by `--yes` (without a terminal and without
`--yes` nothing happens, exit 3); **a backup**: *Back up now* to every enabled backup target
(Backup page), waited for, and the install goes on when at least one target holds the new backup
(`--no-backup` skips it; no enabled target is refused with that hint); the download (the release's
published `.sha256` checked by occulited - a release without one is not installed from the command
line) or the upload of `--file`, staged as the page stages it (a file for another board is
discarded again); then the reboot into the recovery system, which installs it and keeps
`/usr/local`. The SSH session ends with the reboot; `occulited update status` after it shows the
new version. A signed release's signature will be checked on the same path once releases are
signed.

`--json` prints the answer as JSON on stdout (the progress goes to stderr then); `--config FILE`
names occulited's configuration (default `/usr/local/etc/occulite/occulited.json`), whose `listen`
address is where the command reaches occulited. **Exit codes:** 0 done (or nothing to do), 1
failed, 2 usage, 3 not confirmed, 100 `check`: an update is available.

An unattended update is the user's own job - a timer or a cron entry that runs, say weekly,
`occulited update install --yes`: it installs only what is newer, and only after a successful
backup.

## The journal: the one log, and `/etc/config/journal`

**The journal is the only log on an openccu-lite system**: rfd, hs485d, multimacd, hmipserver
(identifier `hmipserver`, with real priorities), lighttpd (`lighttpd` for errors, `lighttpd-access`
for the access log), occulited, addon installs (`addon-install`, the addon id as `ADDON_ID`), the
addons' units (`addon-<id>`) and the kernel. No shipped configuration names a `.log` file; the fork's
`scripts/lite-log-guard.sh` fails the build when one does, and `scripts/lite-log-inventory.sh` is the
check on a booted system. **The exceptions:** the recovery system and the install phase of a system update,
which runs in it, keep `/tmp/fwinstall.log` (no journald there). Addons that write files of their own
(the third-party ones; homematic-manager, hm2mqtt.js and RedMatic until their own tasks) are reported,
not changed.

**lighttpd's access log** is off by default; the Log page's level dialog switches it on
(`PUT /loglevels` `lighttpd.access_log`, the drop-in `/etc/config/lighttpd/occulite-accesslog.conf`).
Every request is then a journal entry, so switch it off again after debugging.

**Where the journal lives** is `/etc/config/journal` on the userfs, read by
`/usr/libexec/occu/lite-journal-persist` at boot and edited by the Journal panel (`GET/PUT /journal`):

| Key | Values | Meaning |
| --- | --- | --- |
| `STORAGE` | `ram`, `ram-sync`, `persistent`, empty | `ram`: `/run/log/journal`, lost at a reboot, at most `RUNTIME_MAX_USE`. `ram-sync`: in RAM, copied to the target every `SYNC_INTERVAL` and at every shutdown and reboot. `persistent`: straight to the target, no RAM journal. Empty: the product's default — `persistent` on `ova`, `oci` and `lxc`, `ram` on the SD-card products (rpi3/Charly, rpi4, rpi5, …). |
| `TARGET` | `userfs`, empty, `usb:<label>/<dir>` | `userfs` or empty: `/usr/local/var/log/journal` (excluded from the backup). `usb:<label>/<dir>` (`ram-sync` only): the copies go to `<dir>` on the USB stick whose filesystem label is `<label>` - udev's `ID_FS_LABEL`, spaces and odd characters made `_` (`GET /usb/storage`'s `label_id`) - wherever it is mounted (`/media/usb1`…`8`); nothing is mounted over `/var/log/journal`. Without the stick the journal stays in RAM (the `journal-target` warning, variant `usb`); `occu-usb-mount@`'s start switches the copies to the stick and copies at once, its stop copies once more before the unmount. A stick with another label is never written. `persistent` with a stick, or anything else, keeps the journal in RAM with the reason. |
| `SYNC_INTERVAL` | `30min`, `6h`, `1d`, empty | `ram-sync`: how often the copy runs, 15 minutes to 7 days; empty = `6h` |
| `TARGET_MAX_USE` | a size, empty | `ram-sync`: the most the copies on the target may take, the oldest going first; empty = `64M` |
| `TARGET_MAX_AGE` | `12h`, `30d`, `2w`, empty | `ram-sync`: copies older than this are removed; empty = no age limit |
| `PERSIST` | `1`, `0` | the older key, read when `STORAGE` is empty; occulited keeps it in step for an image downgrade (`ram-sync` writes `0`) |
| `RUNTIME_MAX_USE` | a size (`16M`) | journald's RuntimeMaxUse, the RAM the journal may take; image default 16M (the system's values are the drop-in `/run/systemd/journald.conf.d/50-openccu-lite-box.conf`, which sorts after the image's `10-openccu-lite.conf` and wins over it). In `ram-sync` it bounds what RAM holds between two copies, and at 80 % of it a copy starts early. |
| `SYSTEM_MAX_USE`, `SYSTEM_MAX_FILE` | sizes | journald's SystemMaxUse (image default 32M) and SystemMaxFileSize |
| `RATE_LIMIT_BURST` | a count | journald's RateLimitBurst |

**When a switch applies.** To `persistent`: at once (bind mount and flush). To `ram-sync`: at once
(bind mount, journald restarted into RAM with `Storage=volatile`, the copies started). To `ram`: at the
next boot, because a mounted journal is not unmounted; from `ram-sync` the copies stop at once, after a
last one, and stay readable until then. The sizes and the interval apply at once.

**ram-sync** is the middle between the two costs: RAM only loses the log one wants after
a crash, persistent writes the SD card continuously. journald writes RAM only, and
`occu-journal-sync.service` (`/usr/libexec/occu/lite-journal-sync`) copies: `journalctl --rotate`, every
closed journal file not yet on the userfs copied under a temporary name and renamed, the userfs synced,
only then the copies removed from RAM, and the copies trimmed to `TARGET_MAX_USE`/`TARGET_MAX_AGE`. It
copies when it stops, so at shutdown and at every reboot, the one into the recovery system included;
the Journal panel's *Copy now* restarts it. The card sees one batch per interval; a reboot loses
nothing, a power loss the time since the last copy. **Early copies**: between two copies
journald drops what does not fit in `RUNTIME_MAX_USE`, so the loop also copies once the files under
`/run/log/journal` take 80 % of it (their allocated size, as journald counts; `journalctl --disk-usage`
would count the copies on the userfs too), or of journald's default when it is empty (10 % of `/run`'s
filesystem, at most 4G) — at most one copy every 15 minutes. Every copy records why it ran
(`LAST_REASON` in `sync.state`: `interval`, `early`, `shutdown`, `manual`), and the Journal panel shows
it; the state is also written beside the copies as `.occu-sync.state`, so the copy at shutdown is known
after the reboot. The userfs directory is bind-mounted over
`/var/log/journal` as in `persistent`, so the Log page, its download and the boot menu read the copies
and RAM together. The results are in `/run/occu-journal/`: `fallback` (why `ram-sync` or `persistent`
could not be set up), `sync.state` (the last copy) and `sync.next`; a mode that could not be set up and
a failed copy are warnings on the Status page.
