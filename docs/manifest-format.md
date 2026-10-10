# The addon manifest: `openccu-lite.json`

An addon tells openccu-lite what it is and what it needs in **one file, `openccu-lite.json`**, at the **root of its
package tarball** (beside `update_script`) and at a path of its repository. The CCU3 and OpenCCU ignore the file.
This document is normative; [`manifest.schema.json`](manifest.schema.json) is the same in JSON Schema for an editor
and a pull-request check. The catalogue ([catalog-format.md](catalog-format.md)) only says where the manifests are.

**Two rules decide everything else** (D-119):

1. **The package's manifest is authoritative.** At install and update the system reads `openccu-lite.json` out of
   the archive in Go, before the addon's `update_script` runs, and applies it as declared — the runtime block becomes
   the addon's policy, the UI facts drive the shell. The accepted copy is kept beside the policy, root-owned; the
   copy in the addon's directory is never read again (a confined addon owns that directory). The repository's copy
   at the latest release tag is only what the Addons page shows before an install (from the default branch while
   the latest release does not carry the file yet, and for a repository without releases).
2. **The addon declares, the system trusts.** There is no grant, no request/grant split and no consent dialog:
   `root: true` runs the addon as root, `capabilities`, `groups`, `paths`, `api_scopes` are applied. The guard rails
   that protect the system itself stay: a declared port is a switch in the firewall and **closed** until the user
   opens it (D-29), `data_dirs` are fenced to the addon's own directories under `/usr/local/` (D-52), no root addon
   has `CAP_SYS_ADMIN` unless it declares it (D-66), and what an addon writes into `/firmware/rftypes` lands in the
   writable extension directory (task 97). `untested` in the catalogue is a label and grants nothing.

## The file

```json
{
  "format": 1,
  "id": "mosquitto",
  "version": "2.1.2+3",
  "name": "Mosquitto",
  "description": {
    "de": "Der MQTT-Broker — die Brücke zu Home Assistant, ioBroker und allem anderen.",
    "en": "The MQTT broker — the bridge to Home Assistant, ioBroker and everything else."
  },
  "homepage": "https://github.com/homematic-community/ccu-addon-mosquitto",
  "licence": "EPL-2.0",
  "release": {
    "github": "homematic-community/ccu-addon-mosquitto",
    "asset": "mosquitto-{arch}-{version}.tar.gz",
    "fallback_asset": "mosquitto-{version}.tar.gz"
  },
  "requires": { "architectures": ["armv7l", "aarch64", "x86_64"] },
  "ui": { "icon": "mosquitto/www/icon.svg", "session_header": true },
  "runtime": {
    "needs": [],
    "ports": [1883, 8883],
    "port_info": {
      "1883": { "proto": "tcp", "label": { "de": "MQTT, unverschlüsselt", "en": "MQTT, plain" } },
      "8883": { "proto": "tcp", "tls": true, "label": { "de": "MQTT über TLS", "en": "MQTT over TLS" } }
    },
    "note": {
      "de": "Beide Ports sind in der Firewall geschlossen, bis sie auf der Seite Zusatzsoftware (Ports der Zusatzsoftware) geöffnet werden.",
      "en": "Both ports stay closed in the firewall until opened on the Addons page (Addon ports)."
    }
  }
}
```

Texts (`name`, `description`, `note`, a port's `label`) are `{"de": …, "en": …}` or one plain string for both; the UI
shows the user's language and falls back to English, then German. A key this version of occulited does not know is
ignored (the schema is the strict reader), so a manifest written for a newer system still installs on an older one.
The file is at most 256 KiB.

### Identity

| Key | Required | Rules |
| --- | --- | --- |
| `format` | yes | `1`. |
| `id` | yes | `^[a-z0-9][a-z0-9_.-]{0,31}$` — **the addon's rc.d id**, the name `update_script` links into `/usr/local/etc/config/rc.d/`. The system checks it after the install against the rc.d entries the installer created or changed; a manifest whose `id` names none of them is not applied and the install log says so. |
| `version` | no | The package's version, `1.2.3`, `3.0.0-beta.22`, `2.1.2+3`. Informational: the installed version comes from the rc.d script's `info` as it always did. |
| `name` | yes | Text. What the Addons page and the menu show. |
| `description` | no | Text, one or two sentences. |
| `homepage` | no | An `http(s)` URL. |
| `changelog` | no | An `http(s)` URL of the release notes, e.g. a `CHANGELOG.md` (occulited task 26). The Addons page links it as *Release notes* beside an offered update, instead of the GitHub release's page of the offered version; without it that page is linked, and without either no link is shown. |
| `licence` | no | An SPDX identifier. |

### `release` — where the packages are

Read by the update check and by the catalogue install. An addon that is only ever installed by upload leaves it out.

| Key | Rules |
| --- | --- |
| `github` | `owner/repo`. Releases are listed through the GitHub API; drafts are skipped, prereleases too unless `prerelease` is true. |
| `asset` | The asset name with the placeholders `{arch}` (`uname -m`: `armv7l`, `aarch64`, `x86_64`) and `{version}` (the tag without a leading `v`; matches anything, so `3.5.2-beta` resolves). |
| `assets` | A map architecture → pattern, for projects that do not name packages by `uname -m`. Tried before `asset`. |
| `fallback_asset` | Tried when nothing else matches: a package for every architecture (scripts, device descriptions). It must be genuinely architecture-independent. |
| `prerelease` | `true` for a project without a stable release yet. |

A `<asset>.sha256` beside the asset is checked when it exists; without one the download is trusted on HTTPS alone.

### `requires` — compatibility

| Key | Rules |
| --- | --- |
| `lite` | The oldest openccu-lite version the addon runs on (`1.0.0`). The page says so when the system is older; the install is not refused. |
| `rega` | `true` when the addon needs the ReGa, which openccu-lite does not have — the catalogue lists such an addon only as *untested*; the system disables one it finds after an update from OpenCCU, and the Addons page marks it incompatible. **A manifest without it says the addon runs without the ReGa**: it replaces the `openccu-lite.ok` marker of the porting kit, which stays accepted for addons without a manifest. |
| `forms` | The product forms: `sd` (the SD-card images), `ova` (the virtual machine). Empty = all. |
| `architectures` | The `uname -m` names the package exists for; empty = any. Both ARM products of openccu-lite are `aarch64`; `armv7l` matches only eQ-3's own 32-bit CCU3 firmware. |

### `ui` — what the shell shows and how it opens the addon

| Key | Rules |
| --- | --- |
| `icon`, `icon_dark` | A square icon, SVG preferred (else PNG, at least 64×64), as a **path relative to the package root** (`mosquitto/www/icon.svg`); `_dark` for dark backgrounds. Used in the addon menu, the tab bar, the Services rows and the Addons page. |
| `logo`, `logo_dark` | A wide logo (height about 48–96 px), the same way. Used on the addon's card. If only one of icon and logo is given, the other falls back to it. |

**The images** (openccu-lite task 100). The system serves them itself, from its own origin, and shows them through `<img>`
alone - never from the addon's pages, never inlined:

- **Installed:** kept by the install (occulited task 11). The install reads every declared image out of the
  package archive at its **path from the package root** - the archive's root, where `openccu-lite.json` lies - before
  `update_script` runs, and keeps it root-owned beside the stored manifest. So `www/icon.svg` is the archive's
  `www/icon.svg`, wherever `update_script` copies it afterwards (OpenCCU-Loom's goes to the CCU's addon web tree, not
  into `/usr/local/addons/openccu-loom`). Only a regular file is taken (a link is not followed), at most 256 KiB, an
  image by its content; a declared image the package does not carry is named in the install log and the shell shows
  its fallback - the install goes on. The copies follow the package: an update keeps the new package's images and
  drops the ones its manifest no longer declares, a package without a manifest (the catalogue's word standing in)
  and an uninstall drop them.
  - **An addon installed before** the system kept copies, or one whose manifest is the catalogue's, has its images
    read out of its tree as before: the path's first segment is taken as the directory the package carries the
    addon in, the one `update_script` copies to `/usr/local/addons/<id>` (`mosquitto/www/icon.svg` is read at
    `/usr/local/addons/mosquitto/www/icon.svg`), through `os.Root` on the tree, and for a confined addon's closed
    tree through the privilege helper, which resolves the kind from the stored manifest and answers nothing but a
    declared image. A reinstall or the next update moves such an addon to the kept copies.
- **In the catalogue, before an install:** the user's check fetches them from the addon's repository at the tag the
  manifest was read at, **beside the manifest**: the manifest's directory in the repository is taken as the package
  root, so a manifest at `addon_files/openccu-lite.json` that declares `mosquitto/www/icon.svg` has the catalogue read
  `addon_files/mosquitto/www/icon.svg`. Keep the images in the repository where the package takes them from - a file
  the build copies in from elsewhere is not there for the catalogue.
- **The rules,** the same everywhere: at most 256 KiB; the type comes from the **content** (SVG, PNG, JPEG, GIF or
  WebP - an HTML file named `.svg` is refused), never from the name; every answer carries `X-Content-Type-Options:
  nosniff` and `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`, so an SVG with a script or
  an external reference is a picture and nothing more.
- **Light and dark:** the shell shows the `_dark` variant in dark mode and the plain one otherwise; a missing dark
  variant uses the light one and a missing light one the dark one. Declare a dark variant only where the light one
  does not work on a dark background. The fallback order in the shell is the manifest's image → the frontend's
  favicon (the addon menu and the tab bar) or the logo of the `Info:` line (the Addons page) → the first letter of
  the name.

```json
"ui": {
  "icon": "hmm/www/icon.svg",
  "icon_dark": "hmm/www/icon-dark.svg",
  "logo": "hmm/www/logo.svg",
  "logo_dark": "hmm/www/logo-dark.svg",
  "settings_url": "/addons/hmm/settings.cgi?cmd=config",
  "session_header": true
}
```
| `settings_url` | The addon's settings page **where its `Config-Url` is not it**: a path under `/addons/`, with a query. Homematic Manager's `Config-Url` is the CCU's button into the app, its settings page is `/addons/hmm/settings.cgi?cmd=config`. Without it the `Config-Url` is the settings page. |
| `session_header` | `true`: **this version** reads the gate's `X-Occulite-Session` everywhere the shell opens it — its frontend and its settings page — so the shell leaves `?sid=@…@` off those URLs. The manifest is per version, so this is a boolean, not a "since" version. Keep accepting `?sid=` for the CCU3 and OpenCCU. |
| `own_updater` | `true`: the addon still carries an update mechanism of its own, which the system's updates bypass; the page notes it. Addons should not ship one, or hide it on openccu-lite (`grep -q '^LITE=' /VERSION \|\| [ -x /usr/bin/occulited ]`). |
| `fullscreen` | `true`: the addon's frontend brings a header and a menu of its own and **offers its own way back to the system** (a link to `/`), so the shell may show it as the whole window — no top bar, no icons, the frame fills the window. The flag only makes the choice available: the user switches it on per addon on the Settings page (*Addons*), kept with the account like the pins, and it is **off until then**. The shell adds no way back of its own, so declare it only when every page of the frontend has one. The addon's settings page and an addon opened in a new tab are unaffected. (occulited task 24, openccu-lite #11.) |

### `runtime` — what the addon needs to run

Under systemd every addon runs in a generated unit, as its own user `addon-<id>` unless it declares `root`. The block
says what that unit gets; **applied as declared** (rule 2). An addon without a `runtime` block runs confined with
nothing but its own three directories and is shown as *undeclared* on the Addons and Services pages — that marking is
what a missing block looks like, and the block is what removes it. **One exception:** a block that says nothing but
the start order (`needs`, `start`, with or without a `note`) keeps the marking, because the start order is no
statement of what the addon needs to run. Any other key removes it, `daemon: true` and `api_scopes` included — so an
addon that needs nothing beyond its own directories declares `{"daemon": true, "needs": [...], "start": "early"}`, or
an empty block `{}` when it keeps no process running. The busybox products ignore the block.

| Key | Rules |
| --- | --- |
| `root` | `true`: the unit runs as root (talks to hardware, patches the system). Shown as *root (unsafe)*. A root addon still has no `CAP_SYS_ADMIN` (D-66): `mount -o remount,rw /` answers *permission denied* and what it writes into `/firmware/rftypes` lands in the writable extension directory; declare `capabilities: ["CAP_SYS_ADMIN"]` beside `root` only when it truly has to mount, and the page marks it *may mount file systems*. |
| `capabilities` | `CAP_*` names for `AmbientCapabilities=` (`CAP_NET_BIND_SERVICE`, `CAP_NET_RAW`, …). Empty: an empty bounding set. **A confined addon may not declare a root-equivalent capability** (B-251): `CAP_SYS_ADMIN`, `CAP_SYS_MODULE`, `CAP_SYS_RAWIO`, `CAP_SYS_PTRACE`, `CAP_SYS_CHROOT`, `CAP_SYS_BOOT`, `CAP_DAC_OVERRIDE`, `CAP_DAC_READ_SEARCH`, `CAP_FOWNER`, `CAP_CHOWN`, `CAP_SETUID`, `CAP_SETGID`, `CAP_SETPCAP`, `CAP_MKNOD`, `CAP_BPF`, `CAP_MAC_ADMIN`, `CAP_MAC_OVERRIDE`, `CAP_NET_ADMIN` are each root in effect and are **refused at install** and never rendered into the unit. An addon that truly needs one runs as `root` (shown *root (unsafe)*) by the user's choice on the Services page. `CAP_NET_ADMIN` is on the list because it can flush the firewall, sniff the LAN or reroute the box's traffic; a VPN addon that needs it runs as root. |
| `groups` | Supplementary groups (`dialout`, `video`). Every confined addon is in `certs` and may read the system's TLS certificate (D-46) without declaring it. **A confined addon may not declare `occulite`** (the privilege helper's group — its member has every helper operation, so full root by proxy) **or `root`** (B-251); both are refused at install and never rendered. A root addon may declare any group. **`usbstorage`** reads and writes USB sticks with FAT, exFAT or NTFS, which the system mounts at `/media/usb1`…`8` owned by root and that group (umask 0007; openccu-lite B-259); declare `/media` in `paths` as well to write there. It is not granted by default: a stick may hold the system's backups, and an addon that declares the group can read them. **A declared group the system does not know is left out of the unit** (and logged), since systemd would refuse to start it: an addon that declares `usbstorage` still starts on a system whose image predates it. |
| `paths` | Extra writable paths for `ReadWritePaths=` beyond the addon's own directories, `/usr/local/etc/config/rc.d`, `/run`, `/var/log`, `/tmp` and `/var/tmp`. May name a shared directory; never chowned. |
| `data_dirs` | The addon's **own state directories outside its three standard ones** (`/usr/local/addons/<id>`, `/usr/local/etc/config/addons/<id>`, `/usr/local/etc/config/addons/www/<id>`): taken over — chowned to `addon-<id>` and put on `ReadWritePaths=` — when the addon is confined (D-52), created when missing. Absolute paths under `/usr/local/` only. The system adds the convention `/usr/local/<id>` on its own. Guard rails, whatever the manifest says: never `/usr/local` itself or one of its shared trees (`addons`, `etc`, `tmp`, `backup`, `crontabs`, `lost+found`, `var`, `sdcard`, `usb`, dotfiles), never another addon's directory or a path inside one, never a symlink. |
| `ports`, `port_info` | The ports the addon listens on. Each is a switch on the Addons page (*Addon ports*), **closed by default** (D-29, D-47); an opened one is a rule on the Firewall page with the addon as its owner. This holds for any mode — the declaration is about reachability, not confinement. `port_info` is keyed by the port as a string: `proto` (`tcp`/`udp`, informational — the firewall opens both), `tls` (a badge, so a user can open the TLS listener and leave the plain one closed), `label` (Text). A key that names no port in `ports` is refused. |
| `needs` | The interface processes the addon talks to, out of `rfd`, `hmipserver`, `hs485d`: its unit starts after them. **Ordering only** (openccu-lite B-308): the addon's unit never starts an interface process (no `Wants=`) - the radio stack starts the ones its plan runs, and the addon copes with those there are (rfd alone, rfd and hmipserver, hmipserver alone), as on a CCU; one the plan does not run is not waited for. **Absent = undeclared**, the safe default (after rfd and hmipserver). **`[]` = none**: it starts right after the network (a broker, a web page). An id the system does not know makes the declaration unusable (logged; the default order). |
| `start` | `"early"`: the addon **copes with interface processes that do not answer yet** — it retries within seconds and logs no error lines while it waits — so its unit starts before them (D-75), and is not ordered on them at all. The user can switch the early start off, globally and per addon. Any other value is refused. |
| `daemon` | `true`: the addon **keeps a process running** after its rc.d `start` (a broker, a server, Node-RED). Its unit is a oneshot that stays *active* either way, so without this an empty unit reads *Completed* — right for an addon that only prepares things, wrong for a daemon that died. With it, a unit whose processes are all gone shows as **Exited** (red) on the Services and Addons pages, with *Start* offered and a Status warning. The system also learns it: once the addon's unit has held a process after a start, it counts as a daemon until the addon is removed; the field covers the very first start. |
| `api_scopes` | The scopes of the addon's **own API token** (D-85), out of `meta:read`, `meta:write`, `system:read`, `logs:read`, `system:write`, `addons:write`, `led`, `rpc:read`, `rpc:operate`, `rpc:configure`, `rpc:admin`. The system mints a token with exactly these at every start and writes it to `/run/occulite/addon-tokens/<id>.api` (`0600`, the addon's user). **Never granted:** `*`, `auth:admin`, `power`, `backup` — such a name, or one the system does not know, is logged at the mint and left out. Declare only what the addon uses: every addon reads names and rooms with the local token without any declaration. |
| `note` | Text: why the addon needs what it declares, and what it contacts outside the system (D-90). Shown on the Addons page. No internal ids in it. |

## Where the system reads the manifest, and when

| Moment | Source | What is used |
| --- | --- | --- |
| **Install or update** (upload or catalogue) | `openccu-lite.json` at the root of the archive, read in Go before `update_script` runs | everything: the policy from `runtime`, the stored copy for `ui`, `requires`, `release` |
| **A package without a manifest** | the catalogue's adapter manifest for the id (`catalog/manifests/<id>.json`), else the manifest fetched for the page — when it was read at the default branch, or at a release tag that is the installed version (the `Version` of the addon's `info`); the latest release's manifest does not describe an older package | the same |
| **Neither** (an arbitrary CCU addon) | — | the system's default mode, no extras: today's behaviour. An update of an addon that had a manifest loses the stored copy (its `ui`, `requires`, `release`) and the `api_scopes`; the rest of its runtime block and its mode stay in its policy |
| **The Addons page before an install** | `<git>/<manifest>` at the latest release tag, fetched on the user's check and cached | name, description, icons, homepage, `requires`, `release` (the latest version), `untested` from the catalogue |
| **The update check** | the installed addon's stored manifest, `release` | the newest matching release |

The stored copy lives in `/usr/local/etc/config/addon-policy/<id>.manifest.json`, root-owned, written by the install
and removed with the addon's policy. It is rewritten by every install and update, so a release that declares less than
its predecessor loses what it dropped at once — a closed port, a group. A package without a manifest declares nothing:
installed over one with a manifest (a downgrade from a file, a fork's package), it takes the catalogue's word as above,
or else the stored copy goes, so that no `ui.session_header` of the earlier version keeps `?sid=` off a page that
cannot read the header. A user's own choice on the Services page (the
switch to root and back) stands across updates; the runtime block's facts follow the package all the same.

## For addon authors

- Put `openccu-lite.json` where your packaging copies it to the **root of the tarball**, next to `update_script`, and
  name that path in the catalogue entry. The CCU3 and OpenCCU ignore the file.
- Bump nothing for the manifest alone: the system reads it with every install, and `version` is informational.
- Declare only what the addon uses. `needs: []` and `start: "early"` shorten every boot; `ports` give the user a
  switch instead of a closed door; `data_dirs` is what keeps a confined addon writing where it always did.
- Validate with the schema: `npx ajv-cli validate --spec=draft2020 -s manifest.schema.json -d openccu-lite.json`
  (ajv-cli's own default is draft-07, which refuses this 2020-12 schema; `npx ajv` is the library, without a command),
  or any JSON Schema 2020-12 validator. The system's own reader is `internal/manifest` in this repository.
