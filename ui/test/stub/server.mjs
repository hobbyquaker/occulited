// Throwaway static server + API stub for looking at the occulited SPA in a browser.
// Serves internal/ui/dist, the addon logos copied off the lab box, and enough of
// /api/system/v1 + /api/auth/v1 + /api/meta/v1 for the shell to render as a logged-in admin.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {X509Certificate, createPrivateKey, createPublicKey, createHash} from 'node:crypto';
import {CERT, KEY, CA} from './testcert.mjs';
import {ledRoute} from './led.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
// the built UI (npm run build); the stub is the test rig of task 14 - every page, no box
const DIST = path.resolve(HERE, '../../../internal/ui/dist');
const WWW = path.join(HERE, 'www');

// openccu-lite task 259: the shell's Content-Security-Policy and Permissions-Policy, read from the
// file occulited embeds and sets on the shell's answers (internal/ui/csp.txt, header lines), so the
// suite runs every page under exactly the policy the system sends (csp.spec.ts fails on a
// violation). The addon pages under /addons/ get none, as on a system.
const SHELL_HEADERS = Object.fromEntries(
    fs.readFileSync(path.resolve(HERE, '../../../internal/ui/csp.txt'), 'utf8').split('\n').filter((l) => l.includes(': ')).map((l) => l.split(/: (.*)/s).slice(0, 2)),
);
if (!SHELL_HEADERS['Content-Security-Policy']) throw new Error('internal/ui/csp.txt has no Content-Security-Policy line');

// task 259: the header credential. The daemon answers 403 request-header to a state-changing call
// whose only credential is the session cookie and that lacks X-Occulite-Request. The stub knows no
// sessions; a browser's request - Sec-Fetch-Site is the browser's own statement, no program sends
// it - with a non-safe method to a guarded /api/ route stands in for "on the cookie alone", so
// every fetch() of the shell is pinned to the header here, the raw ones included. The open routes
// (login, setup, the ticket redeem, a pairing request) need none on the daemon either.
const OPEN_API = new Set(['/api/auth/v1/login', '/api/auth/v1/setup', '/api/auth/v1/ticket/redeem', '/api/auth/v1/pairing/request']);
function refusedWithoutHeader(req, pathname) {
    if (!pathname.startsWith('/api/') || ['GET', 'HEAD', 'OPTIONS'].includes(req.method)) return false;
    if (!req.headers['sec-fetch-site'] || OPEN_API.has(pathname) || pathname.startsWith('/api/auth/v1/pairing/request/')) return false;
    return !req.headers['x-occulite-request'];
}
const PORT = Number(process.env.PORT || 8799);

const now = new Date().toISOString();

// Info: lines copied verbatim off the lab box (192.0.2.119), 2026-09-07.
const INFO = {
    redmatic:
        '<div><a target="_blank" href="https://github.com/rdmtc/RedMatic"><img src="/addons/redmatic/redmatic5-wide.png" height="48"/></a></div>',
    mosquitto:
        '<div><a target="_blank" href="https://github.com/homematic-community/ccu-addon-mosquitto"><img src="/addons/mosquitto/mosquitto-text-side-28.png" width="240"/></a></div>',
    hm2mqtt:
        '<div>Homematic to MQTT bridge - <a target="_blank" href="https://github.com/hobbyquaker/hm2mqtt.js">github.com/hobbyquaker/hm2mqtt.js</a></div>',
    'jp-hb-devices-addon':
        "<center><b>JP HB Devices Support Addon</b></center><br /><center><img src='../addons/jp-hb-devices-addon/jp-hb-devices-addon.png'></img></center><br/><a target='_blank' href='https://github.com/jp112sdl/jp-hb-devices-addon'>https://github.com/jp112sdl/jp-hb-devices-addon</a>",
    mh: '<div>Homematic-Manager &mdash; <a target="_blank" href="https://github.com/hobbyquaker/homematic-manager">github.com/hobbyquaker/homematic-manager</a></div>',
};

const IOB_IMAGES = {icon: '/api/system/v1/addons/iobroker/images/icon?v=1.2.0', 'icon-dark': '/api/system/v1/addons/iobroker/images/icon-dark?v=1.2.0', logo: '/api/system/v1/addons/iobroker/images/logo?v=1.2.0', 'logo-dark': '/api/system/v1/addons/iobroker/images/logo-dark?v=1.2.0'};
const ADDONS = [
    {id: 'redmatic', name: 'RedMatic', version: '9.4.0', info: INFO.redmatic, config_url: '/addons/redmatic/settings.cgi', update: '/addons/redmatic/update_check.cgi', operations: ['restart', 'uninstall'], running: true, pid: 1841, enabled: true, policy_mode: 'root', policy_source: 'migrated', undeclared: true, remount_refused: true},
    // openccu-lite task 100: the images a manifest declares, kind → URL on this origin (imageRoute
    // below serves them). Mosquitto declares a logo alone (the icon falls back to it); ioBroker
    // (ADDON_MORE) all four; the others none, so the favicon and the letter stay tested. A declared
    // image that is not in the tree (404 → the letter) is a spec's own route: a 404 is a console
    // error, and pages.spec.ts wants none on a page load.
    {id: 'mosquitto', name: 'Mosquitto', version: '2.1.2+3', info: INFO.mosquitto, config_url: '/addons/mosquitto/settings.cgi', update: '/addons/mosquitto/update_check.cgi', operations: ['restart', 'uninstall'], running: true, pid: 1902, enabled: true, policy_mode: 'confined', policy_source: 'catalog', images: {logo: '/api/system/v1/addons/mosquitto/images/logo?v=2.1.2%2B3'}},
    {id: 'hm2mqtt', name: 'hm2mqtt', version: '3.6.0-beta', info: INFO.hm2mqtt, config_url: '/addons/hm2mqtt/settings.cgi', update: '/addons/hm2mqtt/update_check.cgi', operations: ['restart', 'uninstall'], running: true, pid: 1955, enabled: true, policy_mode: 'confined', policy_source: 'default'},
    {id: 'jp-hb-devices-addon', oneshot: true, result: 'success', name: 'JP HB Devices', version: '6.1', info: INFO['jp-hb-devices-addon'], config_url: '/addons/jp-hb-devices-addon/settings.cgi', update: '/addons/jp-hb-devices-addon/update-check.cgi', operations: ['uninstall'], running: false, enabled: true, policy_mode: 'confined', policy_source: 'migrated'},
    // B-134: its settings page is settings.cgi?cmd=config - the plain settings.cgi is the CCU's
    // hand-over into the app (302 to the frontend), which the catalogue's runtime.settings_url
    // steers the box's config_url away from
    {id: 'mh', name: 'Homematic-Manager', version: '3.0.0', info: INFO.mh, config_url: '/addons/mh/settings.cgi?cmd=config', operations: ['restart', 'uninstall'], running: true, pid: 2011, enabled: true, policy_mode: 'confined', policy_source: 'catalog', api_scopes: ['meta:write']},
];
// task 55: an addon that is switched off, as the NEO Server is on the lab Pi - it needs the ReGa,
// has no settings page and no Name: of its own - the API names it from its built-in list. Only with the cookie `stub-addon-off=1`, so the addon list the
// other pages are tested against stays as it was.
const ADDON_OFF = {id: '97NeoServer', name: 'NEO Server', version: '', operations: ['uninstall'], running: false, enabled: false, rega_dependent: true, rega_reason: 'needs ReGaHSS, which this system does not have'};
// occulited task 23: an addon whose lighttpd fragment the check refused, only with the cookie
// `stub-addon-rejected=1` - the card carries the verdict with the line
const ADDON_REJECTED = {id: 'fragtest', name: 'Fragment Test', version: '0.3.1', operations: ['restart', 'uninstall'], running: true, pid: 2201, enabled: true, policy_mode: 'confined', policy_source: 'manifest', lighttpd_rejected: {reason: 'proxy.server may point at this system only, not at "10.0.0.1"', line: 3, statement: 'proxy.server = ( "" => ( ( "host" => "10.0.0.1", "port" => 8088 ) ))'}};
// task 59: a third addon with a frontend of its own, only with the cookie `stub-addon-more=1` - for
// three pinned tabs, and for the pin of an addon that is gone (the cookie dropped, the shell reloaded)
const ADDON_MORE = {id: 'iobroker', name: 'ioBroker', version: '1.2.0', config_url: '/addons/iobroker/settings.cgi', operations: ['restart', 'uninstall'], running: true, pid: 2100, enabled: true, policy_mode: 'confined', policy_source: 'catalog', images: IOB_IMAGES};
// the maintainer's follow-up to task 131: a nav.d page that is no addon and opens in the shell's frame
// (stub-nav-page=1), beside the CCU WebUI link that opens a new tab
const NAV_PAGE = {id: 'manual', label: {de: 'Handbuch', en: 'Manual'}, href: '/stub-manual/', target: 'iframe', order: 800, source: 'nav.d'};
const NAV_MORE = {id: 'iobroker', addon: 'iobroker', label: {de: 'ioBroker', en: 'ioBroker'}, href: '/addons/iobroker/', target: 'iframe', order: 500, source: 'addon'};
// Node-RED's favicon for RedMatic's frontend: a 16 px PNG, served as favicon.ico like Node-RED does
const NODE_RED_ICON = 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAM0lEQVR4nGPoZ2BgoARjE/xPAOM1gJBmDEPI0YxiCLma4YaMGjCsDKA4IVElKVMlM5GMAXmUOmRhirPBAAAAAElFTkSuQmCC';

// A nav entry exists only for an addon with a *frontend* - a lighttpd drop-in mapping a path, in
// directory form - as occulited synthesises them (internal/system/nav.go). As on the lab Pi: RedMatic
// has Node-RED at /addons/red/ and Homematic-Manager its own; the others have a settings page only.
const FRONTENDS = {redmatic: '/addons/red/', mh: '/addons/mh/'};
const NAV = [
    // task 88: the route id is the proxied path's segment (/nav/red), the addon id is `addon`
    ...ADDONS.filter((a) => FRONTENDS[a.id]).map((a) => ({id: FRONTENDS[a.id].split('/')[2], addon: a.id, label: {de: a.name, en: a.name}, href: FRONTENDS[a.id], target: 'iframe', order: 500, source: 'addon'})),
    {id: 'ccu-webui', label: {de: 'CCU WebUI', en: 'CCU WebUI'}, href: 'http://192.0.2.119/', target: 'blank', order: 900, source: 'nav.d'},
];

const routes = {
    // task 109: the factory reset's view - nothing paired and the default key, so the Backup page stays quiet
    // task 91: backup encryption - not set up on the stub's system; the specs plant the other states
    'GET /api/system/v1/backup/encryption': {enabled: false, recovery: null, previous: [], box: null},
    'GET /api/system/v1/factory-reset': {hostname: 'ccu-vm-1', armed: false, container: '', update_staged: false, interfaces: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 0, known: true}}, security_key_set: false, security_key_known: true, hmip_local_key: false},
    'GET /api/system/v1/health': {ok: true, version: 'stub', release: '1.0.0-alpha.0', base: '3.89.8.20260719', uptime_s: 12345, meta: {revision: 9, recovered: false}},
    // task 19: password_login follows the stub-oidc cookie (variant below), the group mapping is gone
    'GET /api/auth/v1/config': {mode: 'oidc', modes: ['local','oidc','off'], name: 'authentik', issuer: 'https://auth.example.org/application/o/openccu-lite/', client_id: 'abc123', client_secret_set: true, username_claim: 'preferred_username', scopes: 'openid profile email', password_login: true, running: 'local', restart_required: true, session_idle: '30m0s', session_max: '12h0m0s'},
    // task 85: the VM - persistent by default and mounted, nothing left in RAM after the flush
    // task 214: occulited's database file - the stub is the VM, persistent by default and open
    'GET /api/system/v1/datastore': {mode: '', location: '', default_location: 'userfs:etc/occulite/data', sync_interval: '', default_mode: 'persistent', default_sync_interval: '1h', sync_intervals: ['15min', '1h', '6h', '24h'], platform: 'ova', effective: 'persistent', open: true, path: '/usr/local/etc/occulite/data/occulited.db', size: 65_536, rows_per_series: 500, last_sync: '2026-09-24T18:00:00Z', next_sync: '2026-09-24T19:00:00Z', kept: {state: 34}},
    'GET /api/system/v1/datastore/history': {datapoints: {ACTUAL_TEMPERATURE: 'sampled', HUMIDITY: 'sampled', LEVEL: 'sampled', MOTION: 'event', PRESS_SHORT: 'event', STATE: 'event'}, defaults: ['ACTUAL_TEMPERATURE', 'HUMIDITY', 'LEVEL', 'MOTION', 'PRESS_SHORT', 'STATE'], add: [], remove: [], series: 7, capped: 0, rows_per_series: 500, max_series: 1000},
    'GET /api/system/v1/journal': {storage: '', target: 'userfs', runtime_max_use: '', system_max_use: '', system_max_file: '', rate_limit_burst: '', sync_interval: '', target_max_use: '', target_max_age: '', persist: '', platform: 'ova', default_storage: 'persistent', default_persistent: true, persistent: true, effective: 'persistent', reboot_pending: false, ram_usage: 0, target_usage: 23_400_000, target_free: 27_100_000_000, target_ok: true, usage: 'Archived and active journals take up 32.4M in the file system.', last_sync: null, last_sync_copied: 0, next_sync: null},
    // a box whose syslog file has no LOGLEVEL_HMIP: hmipserver's default, WARN since task 94
    'GET /api/system/v1/loglevels': {rfd: 5, hs485d: 5, multimacd: 2, hmip: 'WARN', loghost: '', lighttpd: {request_handling: false, condition_handling: false, file_not_found: false, access_log: false}, occulited: {level: 'info', debug_areas: []}, restart: [], applied: []},
    // task 101: the radio stack restarted in order, as a new multimacd level needs it
    'POST /api/system/v1/radio/restart': {stopped: ['hmipserver', 'rfd', 'multimacd'], started: ['multimacd', 'rfd', 'hmipserver']},
    // D-66: rfd's device descriptions in the writable layer - the image's 129 entries, 87 links an
    // addon added, one image file it replaced; the reset answers the state afterwards
    'GET /api/system/v1/radio/device-descriptions': {path: '/firmware/rftypes', available: true, mode: 'overlay', entries: 216, image: 129, added: 87, replaced: 1, removed: 0, state: '/usr/local/etc/config/extensions/rftypes'},
    'POST /api/system/v1/radio/device-descriptions/reset': {path: '/firmware/rftypes', available: true, mode: 'overlay', entries: 216, image: 129, added: 87, replaced: 0, removed: 0, state: '/usr/local/etc/config/extensions/rftypes', restarted: true},
    'GET /api/system/v1/services/rfd/unit': {
        unit: 'rfd.service',
        effective: '# /usr/lib/systemd/system/rfd.service\n[Unit]\nDescription=BidCos-RF interface process\nAfter=multimacd.service\n\n[Service]\nExecStart=/bin/rfd -f /etc/rfd.conf\nRestart=on-failure\n\n[Install]\nWantedBy=multi-user.target\n\n# /run/systemd/system/rfd.service.d/50-occulite.conf\n[Service]\nEnvironment=RFD_DEBUG=1\n',
        override: '[Service]\nEnvironment=RFD_DEBUG=1\n',
        path: '/run/systemd/system/rfd.service.d/50-occulite.conf',
    },
    'GET /api/auth/v1/state': {setup_required: false, authenticated: true, user: 'admin', role: 'admin', must_change_password: false, sid: 'A1b2C3d4E5', method: 'password'},
    'GET /api/auth/v1/users': {
        users: [
            {name: 'admin', role: 'admin', level: 'administer', created: '2026-08-14T09:12:00Z', password_set: true, webauthn_keys: 2, webauthn_unusable: 1},
            {name: 'sebastian', role: 'admin', level: 'administer', created: '2026-08-20T18:41:00Z', password_set: true, last_provider_login: '2026-09-14T21:12:00Z', webauthn_keys: 0},
            {name: 'monitor', role: 'user', level: 'operate', created: '2026-09-01T07:03:00Z', must_change_password: true, password_set: true},
        ],
    },
    // task 262: the caller's security keys (the account page), the feature route, an account's keys
    'GET /api/auth/v1/webauthn': {passkeys: true, registered: false, name: 'ccu.example.home'},
    'GET /api/auth/v1/me/webauthn': {name: 'ccu.example.home', keys: [
        {id: 'a1b2c3d4e5f6g7h8', name: 'YubiKey blue', created: '2026-09-20T10:00:00Z', last_used: '2026-09-27T18:30:00Z', passkey: false, transports: ['usb', 'nfc']},
        {id: 'h8g7f6e5d4c3b2a1', name: 'iPhone', created: '2026-09-21T09:00:00Z', passkey: true, transports: ['internal', 'hybrid']},
    ]},
    'GET /api/auth/v1/users/admin/webauthn': {keys: [{id: 'a1b2c3d4e5f6g7h8', name: 'YubiKey blue', created: '2026-09-20T10:00:00Z', passkey: false}, {id: 'h8g7f6e5d4c3b2a1', name: 'iPhone', created: '2026-09-21T09:00:00Z', passkey: true}]},
    'GET /api/auth/v1/tokens': {
        tokens: [
            {name: 'hm2mqtt', scopes: ['meta:read'], prefix: '7f3ac1', created: '2026-08-22T11:00:00Z', last_used: '2026-09-07T18:22:00Z'},
            {name: 'ci-nightly', scopes: ['*'], prefix: 'b90e42', created: '2026-09-02T06:00:00Z', expires: '2027-01-01T00:00:00Z', ips: ['192.168.0.0/24']},
        ],
        // task 66: the scopes a token can be given, in the box's order
        scopes: ['meta:read', 'meta:write', 'system:read', 'logs:read', 'system:write', 'addons:write', 'power', 'backup', 'led', 'auth:admin', 'rpc:read', 'rpc:operate', 'rpc:configure', 'rpc:admin'],
        // openccu-lite task 307: the installed addons' ingress scopes
        addons: [{id: 'redmatic', name: 'RedMatic', scope: 'addon:redmatic'}],
    },
    'GET /api/auth/v1/sessions': {
        current: 'A1b2C3d4E5',
        sessions: [
            {id: 'A1b2C3d4E5', user: 'admin', role: 'admin', created: now, last_seen: now, remote: '192.168.0.24', agent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36'},
            {id: 'Z9y8X7w6V5', user: 'sebastian', role: 'admin', created: '2026-09-06T20:10:00Z', last_seen: '2026-09-07T07:44:00Z', remote: '192.168.0.31', agent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_2 like Mac OS X)'},
        ],
    },
    'GET /api/system/v1/nav': {entries: NAV},
    // task 36: the redirect and HSTS, off; the certificate is what certStatus() says (below)
    'GET /api/system/v1/https': {redirect_https: false, hsts: false, hsts_max_age_days: 7, hsts_clearing: false, certificate: {self_signed: true, managed: false, mode: 'self-signed'}, redirect_fqdn: false, redirect_fqdn_host: 'ccu', redirect_fqdn_target: 'ccu.example.org', redirect_fqdn_state: 'unavailable', redirect_fqdn_reason: 'not-covered'},
    'GET /api/system/v1/addons': {addons: ADDONS},
    'GET /api/system/v1/status': {
        hostname: 'openccu',
        version: {version: '3.89.8.20260719', product: 'ova', platform: 'ova', variant: 'lite', lite: '0-beta.2'},
        occulited_version: '1df08bb0101038ac6eb7c08c3f3144a8a91840a1-hot',
        occulited_commit: '1df08bb0101038ac6eb7c08c3f3144a8a91840a1',
        uptime_s: 268431,
        load: [0.14, 0.21, 0.18],
        mem_total_kb: 2033120,
        mem_available_kb: 1355404,
        disks: [
            {mount: '/', total_kb: 1015808, used_kb: 743424},
            {mount: '/usr/local', total_kb: 6291456, used_kb: 1187840},
        ],
        time: now,
        // the zone name and, beside it, the POSIX string of /etc/config/TZ (B-66)
        timezone: 'Europe/Berlin',
        tz: 'CET-1CEST-2,M3.5.0/02:00:00,M10.5.0/03:00:00',
        hm_mode: 'HmIP-RFUSB',
    },
    // task 41: the OVA box's RFUSB with the shipped 4.4.18, an uploaded 4.4.22 and an older
    // 4.2.14; no flash yet
    'GET /api/system/v1/radio/firmware': {
        modules: [{
            protocols: ['BidCos-RF', 'HmIP-RF'], device: 'HMIP-RFUSB', device_node: '/dev/raw-uart', device_type: 'eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1', family: 'hmip', dir: 'HmIP-RFUSB',
            running_version: '4.4.18', newest: 'dualcopro_update_blhmip-4.4.22.eq3', verdict: 'newer-available', flashable: true,
            files: [
                {name: 'dualcopro_update_blhmip-4.4.18.eq3', path: '/firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3', source: 'shipped', version: '4.4.18', size: 104616, sha256: '3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b8550', direction: 'same'},
                {name: 'dualcopro_update_blhmip-4.4.22.eq3', path: '/usr/local/etc/config/radio-firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.22.eq3', source: 'uploaded', version: '4.4.22', size: 104700, sha256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855', direction: 'upgrade'},
                {name: 'dualcopro_update_blhmip-4.2.14.eq3', path: '/usr/local/etc/config/radio-firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.2.14.eq3', source: 'uploaded', version: '4.2.14', size: 135296, sha256: 'a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5061728394a5b6c7d8e9f0', direction: 'downgrade'},
            ],
        }],
        force_no_update: false, forced_version: '', firmware_staged: false, radio_busy: false, upload_dir: '/usr/local/etc/config/radio-firmware', running: null, last: null,
    },
    // task 42: the OVA box's bus - the passed-through RFUSB on a root hub's port 1, plus two
    // empty controllers - as sysfs shows it
    'GET /api/system/v1/usb': {devices: [
        {path: 'usb1', parent: '', bus: 1, dev: 1, vendor: '1d6b', product: '0001', vendor_name: 'Linux Foundation', product_name: 'UHCI Host Controller', manufacturer: 'Linux 6.18.48 uhci_hcd', serial: '0000:00:01.2', speed: '12', class: '09', hub: true, driver: 'hub', nodes: [], radio: null},
        {path: 'usb2', parent: '', bus: 2, dev: 1, vendor: '1d6b', product: '0002', vendor_name: 'Linux Foundation', product_name: 'xHCI Host Controller', manufacturer: 'Linux 6.18.48 xhci-hcd', serial: '0000:02:1b.0', speed: '480', class: '09', hub: true, driver: 'hub', nodes: [], radio: null},
        {path: '2-1', parent: 'usb2', bus: 2, dev: 2, vendor: '1b1f', product: 'c020', vendor_name: 'eQ-3', product_name: 'eQ-3 HmIP-RFUSB', manufacturer: 'Silicon Labs', serial: '3014F711A061A70000000A07', speed: '12', class: '00', hub: false, driver: 'hb_rf_usb_2', nodes: ['/dev/raw-uart'], radio: {device_node: '/dev/raw-uart', device_type: 'eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1', protocols: ['BidCos-RF', 'HmIP-RF']}},
        {path: 'usb3', parent: '', bus: 3, dev: 1, vendor: '1d6b', product: '0003', vendor_name: 'Linux Foundation', product_name: 'xHCI Host Controller', manufacturer: 'Linux 6.18.48 xhci-hcd', serial: '0000:02:1b.0', speed: '5000', class: '09', hub: true, driver: 'hub', nodes: [], radio: null},
    ]},
    // task 76's follow-up (D-64): the reachability probe's verdicts on the subscribers below - the
    // remote mb_BidCos_RF does not answer (radio-reach.spec.ts adds a refused one of its own)
    'GET /api/system/v1/radio/subscribers/reachability': {
        subscribers: [
            {interface: 'BidCos-RF', id: '1007', url: 'xmlrpc_bin://127.0.0.1:31999', reachable: true},
            {interface: 'BidCos-RF', id: 'BidCos-RF_java', url: 'http://127.0.0.1:39292/bidcos', reachable: true},
            {interface: 'BidCos-RF', id: 'mb_BidCos_RF', url: 'http://198.51.100.9:2049', reachable: false, reason: 'timeout'},
            {interface: 'BidCos-RF', id: 'nr_Ab1Cd2_BidCos-RF', url: 'http://127.0.0.1:2048', reachable: true},
            {interface: 'HmIP-RF', id: 'HmIP-RF_java', url: 'http://127.0.0.1:39292/bidcos', reachable: true},
            {interface: 'HmIP-RF', id: 'nr_Ab1Cd2_HmIP-RF', url: 'http://127.0.0.1:2048', reachable: true},
            {interface: 'VirtualDevices', id: 'hmm_VirtualDevices', url: 'xmlrpc://127.0.0.1:2140', reachable: true},
            {interface: 'VirtualDevices', id: 'hmm_VirtualDevices', url: 'xmlrpc://127.0.0.1:2141', reachable: true},
        ],
    },
    // openccu-lite B-201: no interface process is held by a listener (radio-stalls.spec.ts plants one)
    'GET /api/system/v1/radio/subscribers/stalls': {stalls: []},
    'GET /api/system/v1/radio': {
        mode: 'HmIP-RFUSB',
        host: 'ova-KVM',
        modules: [
            {protocol: 'BidCos-RF', device: 'HMIP-RFUSB', device_node: '/dev/raw-uart', device_type: 'eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1', serial: '0000000A02', firmware: '4.4.18', address: '0xFF0A06', address_active: '0xFF0A05'},
            {protocol: 'HmIP-RF', device: 'HMIP-RFUSB', device_node: '/dev/raw-uart', device_type: 'eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1', serial: '0000000A02', sgtin: '3014F711A000040000000A02', firmware: '4.4.18', address: '0xBC0A08', address_active: '0xBC0A08'},
        ],
        leds: {HM_LED_GREEN: '', HM_LED_GREEN_MODE1: 'none', HM_LED_GREEN_MODE2: 'heartbeat', HM_LED_RED: ''},
        interfaces: [
            {name: 'BidCos-RF', url: 'xmlrpc_bin://127.0.0.1:2001', info: 'BidCos-RF', subscribers: [{id: '1007', url: 'xmlrpc_bin://127.0.0.1:31999', local: true},{id: 'BidCos-RF_java', url: 'http://127.0.0.1:39292/bidcos', local: true},{id: 'mb_BidCos_RF', url: 'http://198.51.100.9:2049', local: false},{id: 'nr_Ab1Cd2_BidCos-RF', url: 'http://127.0.0.1:2048', local: true}]},
            // task 75: the system's own subscriber, the RPC process, marked and not removable
            {name: 'HmIP-RF', url: 'xmlrpc://127.0.0.1:2010/', info: 'HmIP-RF', subscribers: [{id: 'HmIP-RF_java', url: 'http://127.0.0.1:39292/bidcos', local: true},{id: 'nr_Ab1Cd2_HmIP-RF', url: 'http://127.0.0.1:2048', local: true},{id: 'occulited_HmIP-RF', url: 'http://127.0.0.1:8184/cb/HmIP-RF', local: true, own: true}]},
            // the Charly's (B-80): Homematic Manager's old registration kept beside the new one -
            // one id, two callbacks; the page keyed its rows by the id and threw. Both carry
            // duplicate, as the API marks them (task 76)
            {name: 'VirtualDevices', url: 'xmlrpc://127.0.0.1:9292/groups', info: 'Virtual Devices', subscribers: [{id: 'hmm_VirtualDevices', url: 'xmlrpc://127.0.0.1:2140', local: true, duplicate: true}, {id: 'hmm_VirtualDevices', url: 'xmlrpc://127.0.0.1:2141', local: true, duplicate: true}]},
        ],
        lan_gateways: [
            {index: 1, type: 'HMLGW2', name: 'Garage', serial: 'NEQ1234567', address: '192.168.0.51', has_key: true},
            {index: 2, type: 'Lan Interface', name: 'Dachboden', serial: 'KEQ0987654', address: '192.168.0.52', has_key: true},
        ],
        wired_gateways: [],
        pending_key_changes: ['KEQ0987654'],
        security_key_set: true,
        security_key_known: true,
    },
    // task 75: the service messages as the store answers them, with the metadata store's names
    'GET /api/system/v1/service-messages': {
        count: 2,
        messages: [
            {interface: 'HmIP-RF', address: '00010000000A10', channel: '0', key: 'LOW_BAT', value: true, since: new Date(Date.parse(now) - 3 * 86400000).toISOString(), seen: 'start', type: 'HmIP-WRC2', name: 'Wandtaster Flur', enums: ['room/eg/flur']},
            {interface: 'BidCos-RF', address: 'JEQ9000001', channel: '0', key: 'UNREACH', value: true, since: new Date(Date.parse(now) - 20 * 60000).toISOString(), seen: 'event', type: 'HM-CC-TC'},
        ],
        swept: now,
        errors: {},
        feed: {connected: true, boot_id: 'stub', received: 42, interfaces: [{name: 'BidCos-RF', state: 'up', registered: true, last_activity: new Date(Date.parse(now) - 12000).toISOString(), events: 30}, {name: 'HmIP-RF', state: 'up', registered: true, last_activity: new Date(Date.parse(now) - 95000).toISOString(), events: 12}, {name: 'VirtualDevices', state: 'up', registered: true, last_activity: '0001-01-01T00:00:00Z', events: 0}]},
    },
    'GET /api/system/v1/radio/health': {
        polled: now,
        // task 75: the RPC process's view for the Interfaces page's "last event" per process
        feed: {connected: true, boot_id: 'stub', received: 42, interfaces: [{name: 'BidCos-RF', state: 'up', registered: true, last_activity: new Date(Date.parse(now) - 12000).toISOString(), events: 30}, {name: 'HmIP-RF', state: 'up', registered: true, last_activity: new Date(Date.parse(now) - 95000).toISOString(), events: 12}, {name: 'VirtualDevices', state: 'up', registered: true, last_activity: '0001-01-01T00:00:00Z', events: 0}]},
        interfaces: [
            // task 156: the box names the radio of each entry; both stacks share the HmIP-RFUSB through multimacd
            // occulited task 13: the module and the way to it, for the Components cards' subtitle
            {interface: 'BidCos-RF', address: 'FF0A05', type: 'HM-MOD-UART', connected: true, default: true, firmware: '4.4.18', duty_cycle: 3, radio: 'module:0000000A02', radio_name: 'HMIP-RFUSB 0000000A02', module: 'HmIP-RFUSB', path: 'multimacd'},
            // task 68: the level read from channel 0 of the module's device, as on the HmIP-RFUSB
            {interface: 'HmIP-RF', address: 'BC0A08', type: 'HmIP-RFUSB', connected: true, default: false, firmware: '4.4.18', duty_cycle: 11, carrier_sense: 2, carrier_sense_source: 'device', radio: 'module:0000000A02', radio_name: 'HMIP-RFUSB 0000000A02', module: 'HmIP-RFUSB', path: 'multimacd'},
        ],
        // occulited task 13: the events per minute of each interface process (task 17), a sample a
        // minute like the duty cycle's, the same two polls down; HmIP-RF peaks at 72/min 30 minutes ago
        rates: (() => {
            const down = (i) => i === 70 || i === 71;
            const series = (inn) => Array.from({length: 90}, (_, i) => ({t: new Date(Date.parse(now) - (89 - i) * 60000).toISOString(), in: down(i) ? 0 : inn(i), up: !down(i)}));
            return {
                'BidCos-RF': series((i) => 3 + (i % 4) * 1.2),
                'HmIP-RF': series((i) => (i === 59 ? 72 : 12 + (i % 5) * 0.5)),
            };
        })(),
        // occulited task 13: who is subscribed - internal on the loopback, external from the LAN;
        // B-45: lite-rpc's streams after the callbacks, an addon's on the loopback, a browser's from the LAN
        subscribers: {
            'BidCos-RF': {total: 4, internal: 2, external: 2, clients: [
                {id: 'BidCos-RF_java', url: 'http://127.0.0.1:39292/bidcos', internal: true},
                {id: 'mb_BidCos_RF', url: 'http://198.51.100.9:2049', internal: false},
                {id: 'occulited_BidCos-RF', url: 'http://127.0.0.1:8184/cb/BidCos-RF', internal: true, own: true},
                {id: 'admin', url: '', internal: false, stream: {id: '4', kind: 'session', transport: 'websocket', remote: '198.51.100.20'}},
            ]},
            'HmIP-RF': {total: 3, internal: 2, external: 1, clients: [
                {id: 'mb_HmIP_RF', url: 'http://198.51.100.9:2049', internal: false},
                {id: 'occulited_HmIP-RF', url: 'http://127.0.0.1:8184/cb/HmIP-RF', internal: true, own: true},
                {id: 'addon:openccu-loom', url: '', internal: true, stream: {id: '3', kind: 'token', transport: 'sse', remote: '127.0.0.1'}},
            ]},
            VirtualDevices: {total: 1, internal: 1, external: 0, clients: [{id: 'occulited_VirtualDevices', url: 'http://127.0.0.1:8184/cb/VirtualDevices', internal: true, own: true}]},
        },
        errors: {},
        // tasks 53/54: an hour and a half of polls a minute apart up to now, as the sampler keeps
        // them, and both stacks down for two polls (a restart of the radio stack) - the graph
        // must show that as a gap, not as a ramp across it. The last hour peaks at 21 %; task 152:
        // an hour and a quarter ago a 45 % burst, which only the 6 h span's maximum sees. Task 151:
        // the HmIP stack's carrier sense, 1-5 % with a 14 % burst 40 minutes ago and five polls
        // without a figure (a gap); BidCos-RF reports none
        history: (() => {
            const down = (i) => i === 70 || i === 71;
            const series = (dc, cs) => Array.from({length: 90}, (_, i) => {
                const s = {t: new Date(Date.parse(now) - (89 - i) * 60000).toISOString(), dc: down(i) ? 0 : dc(i), up: !down(i)};
                const c = cs?.(i);
                if (c !== undefined && !down(i)) s.cs = c;
                return s;
            });
            return {
                'BidCos-RF/FF0A05': series((i) => 2 + (i % 7)),
                'HmIP-RF/BC0A08': series((i) => (i === 15 ? 45 : 8 + ((i * 3) % 14)), (i) => (i === 49 ? 14 : i >= 60 && i < 65 ? undefined : 1 + (i % 5))),
            };
        })(),
        busy: false,
        busy_interface: '',
        // task 94: the radio stack's units, all up (stub-starting=1 and =boot plant a boot)
        units: [
            {unit: 'occu-init-rf-hardware', interfaces: [], active_state: 'active', sub_state: 'exited'},
            {unit: 'multimacd', interfaces: [], active_state: 'active', sub_state: 'running'},
            {unit: 'rfd', interfaces: ['BidCos-RF'], active_state: 'active', sub_state: 'running'},
            {unit: 'hmipserver', interfaces: ['HmIP-RF', 'VirtualDevices'], active_state: 'active', sub_state: 'running'},
            {unit: 'hs485d', interfaces: [], active_state: 'inactive', sub_state: 'dead'},
        ],
    },
    'GET /api/system/v1/services': {
        systemd: true,
        // B-83: the interval the box suggests for the next poll, from what its listing costs
        poll_seconds: 5,
        services: [
            {id: 'rfd', kind: 'system', running: true, pid: 1204, enabled: true, unit_file_state: 'enabled', protocol: 'BidCos-RF', port: 2001, managed: true, category: 'core', description: 'BidCos-RF interface process', user: 'root', memory_bytes: 24117248, cpu_seconds: 812, since: '2026-09-04T18:02:00Z'},
            {id: 'hmipserver', kind: 'system', running: true, pid: 1210, enabled: true, unit_file_state: 'enabled', protocol: 'HmIP-RF', port: 2010, managed: true, category: 'core', description: 'HmIP interface process', user: 'root', memory_bytes: 96468992, cpu_seconds: 1440, since: '2026-09-04T18:02:00Z'},
            {id: 'hs485d', kind: 'system', running: false, enabled: false, unit_file_state: 'disabled', protocol: 'HM-Wired', port: 2000, managed: true, category: 'core', description: 'HM-Wired interface process'},
            {id: 'lighttpd', kind: 'system', running: true, pid: 980, enabled: true, unit_file_state: 'enabled', port: 80, managed: true, category: 'core', ui: true, user: 'root'},
            {id: 'occulited', kind: 'system', running: true, pid: 1002, enabled: true, unit_file_state: 'enabled', port: 8081, managed: true, category: 'core', ui: true, user: 'occulite'},
            // task 49: a oneshot addon unit has no MainPID - the pid is the leader of its cgroup,
            // pids_more the rest, procs all of them (leader first)
            {id: 'addon-redmatic', kind: 'addon', running: true, pid: 1841, pids_more: 5, remount_refused: true, procs: [
                {pid: 1841, cmd: '/usr/local/addons/redmatic/bin/node /usr/local/addons/redmatic/lib/loader.js'},
                {pid: 1860, cmd: '/usr/local/addons/redmatic/bin/node --max-old-space-size=512 /usr/local/addons/redmatic/lib/node_modules/node-red/red.js'},
                {pid: 1861, cmd: '/usr/bin/logger -t node-red -p user.info'},
                {pid: 1862, cmd: '/usr/bin/logger -t node-red -p user.warning'},
                {pid: 1863, cmd: '/usr/bin/logger -t node-red -p user.err'},
                {pid: 1870, cmd: '/usr/local/addons/redmatic/bin/node /usr/local/addons/redmatic/lib/watchdog.js'},
            ], enabled: true, unit_file_state: 'generated', managed: true, category: 'addon', policy_mode: 'root', policy_source: 'migrated', undeclared: true, memory_bytes: 138412032, since: '2026-09-04T18:03:00Z'},
            {id: 'addon-mosquitto', kind: 'addon', running: true, pid: 1902, procs: [{pid: 1902, cmd: '/usr/local/addons/mosquitto/bin/mosquitto -c /usr/local/addons/mosquitto/etc/mosquitto.conf'}], enabled: true, unit_file_state: 'generated', port: 1883, managed: true, category: 'addon', policy_mode: 'confined', policy_source: 'catalog', memory_bytes: 6291456, images: {logo: '/api/system/v1/addons/mosquitto/images/logo?v='}},
            {id: 'addon-hm2mqtt', kind: 'addon', running: true, pid: 1955, procs: [{pid: 1955, cmd: 'node /usr/local/addons/hm2mqtt/index.js'}], enabled: true, unit_file_state: 'generated', managed: true, category: 'addon', policy_mode: 'confined', policy_source: 'default', memory_bytes: 51380224},
            {id: 'addon-mh', kind: 'addon', running: true, pid: 2011, procs: [{pid: 2011, cmd: 'node /usr/local/addons/mh/app/dist/cli.js'}], enabled: true, unit_file_state: 'generated', port: 8098, managed: true, category: 'addon', policy_mode: 'confined', policy_source: 'catalog', memory_bytes: 41943040},
        ],
    },
    'GET /api/system/v1/network': {
        writable: true,
        pending: null,
        network: {
            hostname: 'openccu',
            domain: 'home.arpa',
            mode: 'static',
            address: '192.0.2.119',
            netmask: '255.255.255.0',
            gateway: '192.0.2.1',
            dns: ['192.0.2.1', '1.1.1.1'],
            // the interface panels: a Pi on Wi-Fi (the default route) with a second LAN on eth0 at a
            // gigabit, a USB adapter without a cable, and a veth; the API lists them by name
            interfaces: [
                {name: 'eth0', mac: 'dc:a6:32:01:02:03', up: true, operstate: 'up', carrier: true, speed: 1000, duplex: 'full', mtu: 1500, kind: 'ethernet', driver: 'bcmgenet',
                    ipv4: [{address: '10.10.0.2', prefix: 24}],
                    ipv6: [
                        {address: '2001:db8:1::119', prefix: 64, scope: 'global', flags: ['permanent']},
                        {address: '2001:db8:1:0:1234:5678:abcd:ef01', prefix: 64, scope: 'global', flags: ['temporary', 'deprecated']},
                        {address: 'fd00::119', prefix: 64, scope: 'unique-local'},
                        {address: 'fe80::dea6:32ff:fe01:203', prefix: 64, scope: 'link-local', flags: ['permanent']},
                    ],
                    ipv6_enabled: true, ipv6_autoconf: true,
                    statistics: {rx_bytes: 1234567890, tx_bytes: 98765432, rx_errors: 0, tx_errors: 0, rx_dropped: 3, tx_dropped: 0}},
                {name: 'eth1', mac: '00:e0:4c:68:00:01', up: false, operstate: 'down', carrier: false, mtu: 1500, kind: 'ethernet', driver: 'r8152', ipv4: [], ipv6: [], ipv6_enabled: true, ipv6_autoconf: true},
                {name: 'veth0', mac: 'bc:24:11:aa:bb:cc', up: true, operstate: 'up', carrier: true, speed: 10000, duplex: 'full', mtu: 1500, kind: 'veth', ipv4: [], ipv6: []},
                {name: 'wlan0', mac: 'dc:a6:32:01:02:05', up: true, operstate: 'up', carrier: true, mtu: 1500, kind: 'wireless', driver: 'brcmfmac', default_route: true,
                    ipv4: [{address: '192.0.2.119', prefix: 24}],
                    ipv6: [{address: 'fe80::dea6:32ff:fe01:205', prefix: 64, scope: 'link-local', flags: ['permanent']}]},
            ],
            ipv6_state: {available: true, enabled: true, gateway: 'fe80::1', gateway_interface: 'eth0', dns: ['fd00::1']},
        },
        settings: {hostname: 'openccu', mode: 'static', address: '192.0.2.119', netmask: '255.255.255.0', gateway: '192.0.2.1', dns: ['192.0.2.1', '1.1.1.1']},
        current: {hostname: 'openccu', mode: 'static', address: '192.0.2.119', netmask: '255.255.255.0', gateway: '192.0.2.1', dns: ['192.0.2.1', '1.1.1.1']},
    },
    'GET /api/system/v1/time': {tz: 'CEST-2', zone: 'Europe/Berlin', ntp_servers: ['0.de.pool.ntp.org', '1.de.pool.ntp.org'], has_ntp: true, now: now},
    'GET /api/system/v1/time/zones': {zones: [{name: 'Europe/Berlin', country: 'Germany', code: 'DE'}, {name: 'Europe/Vienna', country: 'Austria', code: 'AT'}, {name: 'UTC', country: '', code: ''}]},
    'GET /api/system/v1/leds': {disabled: false, leds: [{name: 'green', path: '/sys/class/leds/green', trigger: 'heartbeat', normal: 'heartbeat'}, {name: 'red', path: '/sys/class/leds/red', trigger: 'none', normal: 'none'}]},
    'GET /api/system/v1/ssh': {enabled: true, running: true},
    'GET /api/system/v1/timers': {
        systemd: true,
        timers: [
            {unit: 'occulite-backup.timer', activates: 'occulite-backup.service', next: '2026-09-08T00:07:00Z', last: '2026-09-07T00:07:00Z', left: '2h 20min', active: true, own: false, enabled: true, unit_file_state: 'enabled'},
            {unit: 'occulite-addon-updates.timer', activates: 'occulite-addon-updates.service', next: '2026-09-08T04:00:00Z', last: '2026-09-07T04:00:00Z', left: '6h', active: true, own: false, enabled: true, unit_file_state: 'enabled'},
        ],
    },
    'GET /api/system/v1/log': {
        source: 'journald',
        lines: Array.from({length: 30}, (_, i) => ({
            time: new Date(Date.now() - (30 - i) * 60000).toISOString(),
            timestamp: new Date(Date.now() - (30 - i) * 60000).toISOString(),
            severity: ['info', 'info', 'notice', 'warning', 'err'][i % 5],
            // the tag is the unit's name, except that every other hmipserver line is tagged
            // java - one unit, two tags, as a real journal has (B-59's spec filters on both)
            tag: i % 8 === 6 ? 'java' : ['occulited', 'rfd', 'hmipserver', 'addon-mosquitto'][i % 4],
            unit: ['occulited', 'rfd', 'hmipserver', 'addon-mosquitto'][i % 4],
            pid: 1000 + i,
            message: `Sample log line ${i}: the interface answered listBidcosInterfaces in ${10 + i} ms`,
            // task 186: some of occulited's lines come from an area's logger (OCCULITED_AREA)
            ...(i % 4 === 0 && i % 8 === 0 ? {area: 'addons'} : i % 4 === 0 && i % 12 === 4 ? {area: 'acme'} : {}),
        })),
    },
    // two deployed bundles: one with a real version, one whose info says eQ-3's 0.0.0 - the API
    // sends that as an empty version - with their text files behind BUNDLE_FILES
    // task 161: one stick mounted and linked as /media/usb0, one read-only
    'GET /api/system/v1/usb/storage': {sticks: [
        {mount: '/media/usb1', path: '/media/usb0', linked: true, device: '/dev/sda1', fstype: 'vfat', label: 'BACKUP', label_id: 'BACKUP', vendor: 'SanDisk', model: 'Cruzer Blade', total_bytes: 15_931_539_456, free_bytes: 12_884_901_888},
        {mount: '/media/usb2', path: '/media/usb2', linked: false, device: '/dev/sdb1', fstype: 'ext4', label: '', vendor: 'Intenso', model: 'Rainbow Line', read_only: true, total_bytes: 7_751_073_792, free_bytes: 1_073_741_824},
    ]},
    'GET /api/system/v1/firmware': {
        enabled: true, running: false, last_run: now, index_size: 2,
        // B-195: an upper-case type eQ-3 lists (its bundle not deployed yet), a current one, and a
        // BidCos type the index does not list
        devices: [
            {interface: 'HmIP-RF', address: '00010000000A10', type: 'HMIP-WRC2', firmware: '1.0.3', available_firmware: '0.0.0', latest: '1.18.2', update_available: true},
            {interface: 'HmIP-RF', address: '0001D0000000B1', type: 'HmIP-PDT', firmware: '2.2.4', available_firmware: '2.2.4', latest: '2.2.4', update_available: false},
            {interface: 'BidCos-RF', address: 'JEQ9000001', type: 'HM-CC-TC', firmware: '2.1', not_listed: true, update_available: false},
        ],
        last_result: [
            {type: 'HmIP-PDT', version: '2.2.4', action: 'deployed'},
            {type: 'HmIPW-DRS8', version: '', version_from_name: '1.2.6', date_from_name: '2022-09-28', action: 'deployed'},
            // a bundle whose file name carries no version either: a dash
            {type: 'HmIP-XYZ', version: '', action: 'deployed'},
        ],
        deployed: [
            {type_code: '310', name: 'HmIP-PDT', version: '2.2.4', files: ['HmIP-PDT_update_V2_2_4_231123.efw', 'changelog.txt', 'info'], info: {TypeCode: '310', Name: 'HmIP-PDT', FirmwareVersion: '2.2.4'}},
            {type_code: '4107', name: 'HmIPW-DRS8', version: '', version_from_name: '1.2.6', date_from_name: '2022-09-28', files: ['HmIPW-DRS8_update_V1_2_6_220928.efw', 'changelog.txt', 'info'], info: {TypeCode: '4107', Name: 'HmIPW-DRS8', FirmwareVersion: '0.0.0'}},
            {type_code: '9001', name: 'HmIP-XYZ', version: '', files: ['firmware.efw', 'info'], info: {TypeCode: '9001', Name: 'HmIP-XYZ'}},
        ],
    },
    // running is the /VERSION record in the API's shape (system.Version); the power menu words its
    // halt question by its platform
    'GET /api/system/v1/system-update': {running: {version: '3.89.8.20260719', product: 'ova', platform: 'ova', variant: 'lite', lite: '1.0.0-alpha.0'}, container: '', staged: null, feed: {enabled: true, feed_url: 'https://api.github.com/repos/hobbyquaker/openccu-lite/releases?per_page=20', checked: now, error: '', downloading: false, available: null}},
    // task 56: stars and repositories as the index and the daily refresh give them - a count in
    // the thousands, a tie broken by name (Homematic-Manager before Mosquitto), an entry without a
    // count - listed out of star order, and two entries that are not installed (the letter tile,
    // Install). update_available is computed per request (catalogFor), as the box does.
    // D-119: the catalogue is three fields per entry, the rest is the addon's manifest fetched on
    // the user's check and flattened into the item; one entry is not checked yet (no manifest, no id)
    'GET /api/system/v1/catalog': {
        catalog: {format: 1, checked: now, addons: [
            {git: 'https://github.com/homematic-community/ccu-addon-mosquitto', manifest_path: 'addon_files/openccu-lite.json', format: 1, id: 'mosquitto', name: {de: 'Mosquitto', en: 'Mosquitto'}, description: {de: 'MQTT-Broker', en: 'MQTT broker'}, stars: 88, release: {github: 'homematic-community/ccu-addon-mosquitto', asset: 'mosquitto-{version}.tar.gz'}, licence: 'EPL-2.0', tag: '2.1.2+3', latest: {version: '2.1.2+3', asset: 'mosquitto-2.1.2+3.tar.gz'}},
            {git: 'https://github.com/TomMajor/SmartHome', manifest_path: 'catalog/manifests/tm-devices.json', untested: true, adapter: true, format: 1, id: 'tm-devices', name: {de: 'TM Devices', en: 'TM Devices'}, description: {de: 'Geräteunterstützung für Eigenbauten', en: 'Device support for home-built sensors'}, release: {github: 'TomMajor/SmartHome', asset: 'tm-devices-{version}.tgz', prerelease: true}, runtime: {note: {de: 'Noch nicht auf openccu-lite ausprobiert.', en: 'Not yet tried on openccu-lite.'}}, latest: {version: '0.9.0-rc.1', asset: 'tm-devices-0.9.0-rc.1.tgz'}, images: {icon: '/api/system/v1/catalog/tm-devices/images/icon?v=0a1b2c3d4e5f'}},
            {git: 'https://github.com/rdmtc/RedMatic', manifest_path: 'addon_files/openccu-lite.json', format: 1, id: 'redmatic', name: {de: 'RedMatic', en: 'RedMatic'}, description: {de: 'Node-RED auf der Zentrale', en: 'Node-RED on the CCU'}, stars: 1234, release: {github: 'rdmtc/RedMatic', asset: 'redmatic-{arch}-{version}.tar.gz'}, requires: {architectures: ['aarch64', 'x86_64']}, licence: 'Apache-2.0', homepage: 'https://github.com/rdmtc/RedMatic/wiki', tag: 'v9.4.1', latest: {version: '9.4.1', asset: 'redmatic-9.4.1.tar.gz'}},
            {git: 'https://github.com/homematic-community/XML-API', manifest_path: 'openccu-lite.json', format: 1, id: 'xml-api', name: {de: 'XML-API', en: 'XML-API'}, description: {de: 'Die XML-Schnittstelle der ReGa', en: 'The ReGa XML interface'}, stars: 70, release: {github: 'homematic-community/XML-API', asset: 'xml-api-{version}.tar.gz'}, licence: 'MIT', ui: {own_updater: true}, latest: {version: '2.3', asset: 'xml-api-2.3.tar.gz'}, images: {logo: '/api/system/v1/catalog/xml-api/images/logo?v=1a2b3c4d5e6f', 'logo-dark': '/api/system/v1/catalog/xml-api/images/logo-dark?v=6f5e4d3c2b1a'}},
            {git: 'https://github.com/hobbyquaker/homematic-manager', manifest_path: 'apps/ccu-addon/files/openccu-lite.json', format: 1, id: 'mh', name: {de: 'Homematic-Manager', en: 'Homematic-Manager'}, description: {de: 'Geräte anlernen und konfigurieren', en: 'Pair and configure devices'}, stars: 88, release: {github: 'hobbyquaker/homematic-manager', asset: 'mh-{version}.tar.gz'}, licence: 'MIT', latest: {version: '3.0.0', asset: 'mh-3.0.0.tar.gz'}},
            {git: 'https://github.com/SukramJ/openccu-loom', manifest_path: 'openccu-lite.json', untested: true},
        ]},
        installed: {mosquitto: '2.1.2+3', redmatic: '9.4.0', hm2mqtt: '3.6.0-beta', mh: '3.0.0', 'jp-hb-devices-addon': '6.1'},
        arch: 'x86_64',
    },
    'GET /api/system/v1/catalog/progress': {progress: null},
    // task 35: the box on its self-signed certificate, the ACME settings never touched, the
    // five providers the binary carries (D-49)
    'GET /api/system/v1/certificate': {
        settings: {mode: 'self-signed', directory: 'letsencrypt', directory_url: '', ca_root: '', email: '', eab_kid: '', eab_hmac_set: false, names: [], challenge: 'http-01', dns_provider: '', dns_credentials: {}, dns_secrets_set: {}},
        // B-58: an issuer DN as a step-ca hands them out, well over 200 characters, for the overflow spec
        current: {subject: 'CN=openccu,OU=ABC1234567,O=HomeMatic,C=DE', issuer: 'CN=step-ca.lan.example.org+Intermediate+CA,OU=lan.example.org,O=an-organisation-with-a-deliberately-long-name-for-the-card,L=somewhere-far-beyond-the-right-edge-of-the-card,ST=state-of-overflow,C=DE,1.2.840.113549.1.9.1=#0c1a63612d61646d696e406c616e2e6578616d706c652e6f7267', issuer_cn: 'step-ca.lan.example.org Intermediate CA', issuer_org: 'an-organisation-with-a-deliberately-long-name-for-the-card', names: ['openccu'], ips: ['192.0.2.119'], not_before: '2026-09-01T10:00:00Z', not_after: '2036-08-29T10:00:00Z', self_signed: true, days_left: 3641, chain: 1, serial: '1b257ea7c5074a53', fingerprint: 'FD:11:B9:FE:93:76:8B:F0:6E:B8:FC:0B:4C:98:E4:E3:3A:74:21:2E:60:6F:7A:5F:54:CC:F6:97:39:BE:FB:FF'},
        managed: false,
        pending: null,
        issued: null,
        live_is_issued: false,
        last: null,
        running: null,
        renew_below_days: 30,
        next_check: new Date(Date.now() + 6 * 3600 * 1000).toISOString(),
        // no names stored: the box proposes its FQDN and its host name
        suggested_names: ['openccu.home.arpa', 'openccu'],
        providers: [
            {id: 'cloudflare', name: 'Cloudflare', fields: [{key: 'api_token', label: 'API token', secret: true}], note: 'An API token with Zone:DNS:Edit (and Zone:Zone:Read) for the zone; not the global API key.'},
            {id: 'hetzner', name: 'Hetzner DNS', fields: [{key: 'api_token', label: 'API token', secret: true}]},
            {id: 'netcup', name: 'netcup', fields: [{key: 'customer', label: 'Customer number'}, {key: 'api_key', label: 'API key', secret: true}, {key: 'api_password', label: 'API password', secret: true}]},
            {id: 'duckdns', name: 'DuckDNS', fields: [{key: 'token', label: 'Token', secret: true}]},
            {id: 'exec', name: 'Script (exec)', fields: [{key: 'program', label: 'Program', placeholder: '/usr/local/etc/config/acme-dns.sh'}, {key: 'mode', label: 'Mode', optional: true, placeholder: '(default) or RAW'}]},
        ],
    },
    'GET /api/meta/v1/version': {revision: 42, implementation: 'occulited'},
    'GET /api/auth/v1/oidc': {enabled: false},
    // task 86: the backup targets - the USB directory (on the system itself, as the schedule says),
    // an SSH server whose key is confirmed, and an NFS share that is mounted; the specs plant the
    // other states themselves
    'GET /api/system/v1/backup/targets': {
        nightly: {enabled: true, time: '00:07'}, container: '', kinds: {directory: '', share: '', nfs: '', cifs: '', sftp: ''}, encryption: false, hostname: 'openccu', needed_bytes: 5190451,
        targets: [
            {id: 'directory', name: 'USB', kind: 'directory', enabled: true, max_backups: 7, subdir: '', encrypt: true, append_only: false, directory: {path: '/media/usb0/backup'},
                state: {state: 'idle', failures: 0, mounted: false}, last_backup: {at: '2026-09-07T00:07:12Z', ok: true, state: 'writable', name: 'openccu-2026-09-07.sbk', size: 4718592, duration_ms: 41000, encrypted: false, last_ok: '2026-09-07T00:07:12Z'}},
            {id: 'tssh0001', name: 'Build host', kind: 'sftp', enabled: true, max_backups: 30, subdir: 'openccu', encrypt: true, append_only: false, created: '2026-09-01T10:00:00Z',
                sftp: {host: 'nas.lab', port: 22, user: 'backup', path: '/srv/backup', public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAgB4Z0cV7d0a4o5Yl1QeJwHkqOq7n5v8u9P0mYt3xZs openccu-lite-backup@openccu', host_key: {type: 'ssh-ed25519', fingerprint: 'SHA256:3b9Qp0F6zW2kq4U0cX8f1Tn7b5yY2rJq9eK1mL3oP8s'}},
                state: {state: 'writable', failures: 0, mounted: false, checked_at: '2026-09-07T08:00:00Z', free_bytes: 812000000000, total_bytes: 2000000000000}, last_backup: {at: '2026-09-07T00:09:01Z', ok: true, state: 'writable', name: 'openccu-2026-09-07-0007.sbk', size: 4718592, duration_ms: 12000, encrypted: false, last_ok: '2026-09-07T00:09:01Z'}},
            // task 228: an NFS/SMB target is a share target now - the share nas of System → Storage and a folder
            {id: 'tnas0001', name: 'TrueNAS', kind: 'share', enabled: true, max_backups: 30, subdir: '', encrypt: true, append_only: false, created: '2026-09-01T10:00:00Z',
                share: {id: 'nas', folder: 'openccu'},
                state: {state: 'idle', failures: 0, mounted: true, source: '//nas.lan/backup', options: 'rw,vers=3.1.1,soft'}},
        ],
    },
    'GET /api/system/v1/backup/targets/directory/backups': {backups: [{name: 'openccu-2026-09-07.sbk', size: 4718592, time: '2026-09-07T00:07:12Z', encrypted: false}, {name: 'openccu-2026-09-06.sbk', size: 4710400, time: '2026-09-06T00:07:09Z', encrypted: false}]},
    'GET /api/system/v1/backup/schedule': {enabled: true, path: '/media/usb0/backup', max_backups: 7, path_exists: true, on_userfs: true, real_path: '/usr/local/media/usb0/backup', backups: [{name: 'openccu-2026-09-07.sbk', size: 4718592, time: '2026-09-07T00:07:12Z'}, {name: 'openccu-2026-09-06.sbk', size: 4710400, time: '2026-09-06T00:07:09Z'}]},
    // the daily run of the addons' own Update: checks, in the shape internal/addonupdates serves:
    // RedMatic is also a catalogue update (the Status badge counts it once), hm2mqtt only here
    'GET /api/system/v1/addons/updates': {last: now, available: 2, results: [
        {id: 'hm2mqtt', name: 'hm2mqtt', checked: now, info: {installed: '3.6.0-beta', available: '3.6.1', update_available: true, url: 'https://github.com/hobbyquaker/hm2mqtt.js/releases/tag/v3.6.1'}},
        {id: 'redmatic', name: 'RedMatic', checked: now, info: {installed: '9.4.0', available: '9.4.1', update_available: true, url: ''}},
    ]},
    'GET /api/meta/v1/snapshot': {
        format: 1,
        revision: 42,
        objects: {
            'BidCoS-RF:1': {name: 'Zentrale Taste 1', enums: ['rooms/wohnzimmer'], meta: {}},
            'NEQ0123456:1': {name: 'Wohnzimmer Deckenlicht', enums: ['rooms/wohnzimmer', 'functions/licht'], meta: {}},
            'NEQ0123456:2': {name: 'Wohnzimmer Stehlampe', enums: ['rooms/wohnzimmer', 'functions/licht'], meta: {}},
            'MEQ0987654:1': {name: 'Küche Fenster', enums: ['rooms/kueche', 'functions/sicherheit'], meta: {}},
            // task 43: one object per Playwright project for the editor's write tests (the stub is shared)
            'NEQ0123456:3': {name: 'Wohnzimmer Rollo', enums: ['rooms/wohnzimmer'], meta: {}},
            'MEQ0987654:2': {name: 'Küche Licht', enums: ['rooms/kueche', 'functions/licht'], meta: {}},
            'HmIP-RF.000ABC:1': {name: 'Bad Thermostat', enums: ['rooms/og/bad'], meta: {}},
            // task 193: a dimmer with values over lite-rpc (liteRoute)
            'HmIP-RF.000DD8:3': {name: 'Bad Spiegellampe', enums: ['rooms/og/bad', 'functions/licht'], meta: {}},
            // task 193: a virtual key of the central - pressed short and long over setValue
            'BidCos-RF.BidCoS-RF:2': {name: 'Zentrale Taste 2', enums: ['rooms/og/bad'], meta: {}},
            // task 193, the widget gaps: a smoke detector, a siren, an RGBW light and a meter, in Obergeschoss itself
            'HmIP-RF.000F01:1': {name: 'Flur Rauchmelder', enums: ['rooms/og', 'functions/sicherheit'], meta: {}},
            'HmIP-RF.000F02:3': {name: 'Flur Sirene', enums: ['rooms/og', 'functions/sicherheit'], meta: {}},
            'HmIP-RF.000F03:1': {name: 'Flur Leuchte', enums: ['rooms/og', 'functions/licht'], meta: {}},
            'HmIP-RF.000F04:6': {name: 'Flur Steckdose', enums: ['rooms/og'], meta: {}},
        },
        enums: {
            rooms: {name: {de: 'Räume', en: 'Rooms'}, tree: [{id: 'wohnzimmer', name: 'Wohnzimmer'}, {id: 'kueche', name: 'Küche'}, {id: 'og', name: 'Obergeschoss', children: [{id: 'bad', name: 'Bad'}]}]},
            functions: {name: {de: 'Gewerke', en: 'Functions'}, tree: [{id: 'licht', name: 'Licht'}, {id: 'sicherheit', name: 'Sicherheit'}]},
        },
    },
};

// the text files of the deployed bundles above, as GET /firmware/bundles/{type_code}/files/{name}
// hands them out; a firmware image in the list is refused the way the box refuses it
const BUNDLE_FILES = {
    310: {
        'changelog.txt': 'Version 2.2.4\n- improved the battery reading\n- fixed a rare hang after a reboot of the access point\n',
        info: 'TypeCode=310\nName=HmIP-PDT\nFirmwareVersion=2.2.4\nCCU3FirmwareVersionMin=3.73.9\n',
    },
    4107: {
        'changelog.txt': 'Version 1.2.6 (2022-09-28)\n- Channel 8 now keeps its state across a power failure of the bus, and a very long line follows to show that the text wraps inside the dialog on a phone rather than running off to the right: HmIPW-DRS8_update_V1_2_6_220928.efw\n\nVersion 1.2.4\n- first release\n',
        info: 'TypeCode=4107\nName=HmIPW-DRS8\nFirmwareVersion=0.0.0\nCCU3FirmwareVersionMin=3.61.5\n',
    },
};

// task 38: what the manual routes change between requests (the projects share the stub: a
// pending key set by one test is what another sees, which is fine - the specs assert on their
// own answers)
const certState = {mode: null, current: null, pending: null, lastCN: ''};
function certStatus() {
    const st = structuredClone(routes['GET /api/system/v1/certificate']);
    if (certState.mode) {
        st.settings.mode = certState.mode;
        st.managed = true;
        st.managed_mode = certState.mode;
        st.managed_by = certState.current?.issuer ?? '';
        st.current = certState.current ?? st.current;
    }
    st.pending = certState.pending;
    return st;
}
const STUB_CSR = '-----BEGIN CERTIFICATE REQUEST-----\nMIHiMIGJAgEAMCExHzAdBgNVBAMMFmNjdS5leGFtcGxlLm9yZy5zdHViMFkwEwYHKoZI\nzj0CAQYIKoZIzj0DAQcDQgAEstub0000000000000000000000000000000000000000\n000000000000000000000000000000000000000000000000000000000000oAAwCgYI\nKoZIzj0EAwIDSAAwRQIhAPstub00000000000000000000000000000000000000000\nAiAstub000000000000000000000000000000000000000000000000000000000000\n-----END CERTIFICATE REQUEST-----\n';
function dn(s) {
    return s.split('\n').join(',');
}
// the issuer's common name and first organisation, as certpem.Info carries them beside the DN
function dnPart(s, key) {
    return s.split('\n').find((l) => l.startsWith(`${key}=`))?.slice(key.length + 1) || undefined;
}
function certInfo(x) {
    const names = (x.subjectAltName ?? '').split(',').map((s) => s.trim()).filter((s) => s.startsWith('DNS:')).map((s) => s.slice(4));
    const notAfter = new Date(x.validTo);
    return {subject: dn(x.subject), issuer: dn(x.issuer), issuer_cn: dnPart(x.issuer, 'CN'), issuer_org: dnPart(x.issuer, 'O'), names, not_before: new Date(x.validFrom).toISOString(), not_after: notAfter.toISOString(), self_signed: x.subject === x.issuer, days_left: Math.floor((notAfter - Date.now()) / 86400000), chain: 1, serial: x.serialNumber.toLowerCase(), fingerprint: x.fingerprint256};
}
function inspect(b) {
    const out = {};
    let x = null;
    if (b.certificate?.trim()) {
        try {
            x = new X509Certificate(b.certificate);
            out.certificate = certInfo(x);
            out.chain = 1;
            if (b.chain?.trim()) {
                try {
                    const c = new X509Certificate(b.chain);
                    out.chain = 2;
                    if (!x.checkIssued(c)) out.chain_warning = `the certificate that issued "${x.subject.replace(/^CN=/, '')}" is not in the chain`;
                } catch (e) {
                    out.certificate_error = `chain: ${e.message}`;
                }
            } else if (x.subject !== x.issuer) {
                out.chain_warning = `the certificate that issued "${x.subject.replace(/^CN=/, '')}" (${dn(x.issuer)}) is not in the chain: a browser or an addon that does not already know that intermediate will not trust the system - paste the issuer's certificate as the chain unless every client has it`;
            }
            if (new Date(x.validTo) < new Date()) out.validity = `the certificate expired on ${x.validTo}`;
            if (certState.pending) out.pending_matches = false;
        } catch (e) {
            out.certificate_error = e.message;
        }
    }
    if (b.key?.trim()) {
        if (/ENCRYPTED|Proc-Type: 4,ENCRYPTED/.test(b.key)) {
            out.key_error = 'the private key is encrypted - decrypt it first (openssl pkey -in key.pem -out key-plain.pem) and upload the plain key';
        } else {
            try {
                const k = createPrivateKey(b.key);
                const pub = createPublicKey(k).export({type: 'spki', format: 'der'});
                const fp = createHash('sha256').update(pub).digest('hex').toUpperCase().match(/../g).join(':');
                out.key = {algorithm: k.asymmetricKeyDetails?.namedCurve === 'prime256v1' ? 'P-256' : k.asymmetricKeyType.toUpperCase(), fingerprint: fp};
                if (x) out.matches = x.checkPrivateKey(k);
            } catch (e) {
                out.key_error = e.message;
            }
        }
    }
    return out;
}

// B-59: the lines the churning stream has emitted (see /log/stream), newest last
const churned = [];
let churnSeq = 0; // one counter for every stream, so a churned tag is never seen twice
// B-59: the log filters the stub honours - a tag, a unit, a severity floor (like journalctl -p)
const SEV = ['emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug'];
function logMatches(l, params) {
    const tag = params.get('tag');
    const unit = params.get('unit');
    const sev = params.get('severity');
    const area = params.get('area');
    if (tag && l.tag !== tag) return false;
    if (area && l.area !== area) return false;
    if (unit && l.unit !== unit) return false;
    if (sev && SEV.includes(sev) && SEV.indexOf(l.severity) > SEV.indexOf(sev)) return false;
    return true;
}

// task 93: the boots of the stub's journal, and its kernel's messages of a boot:
// [microseconds since the boot, severity, message]
const STUB_BOOT = '4c1d2a6b0e8f4a2b9c3d5e7f8a1b2c3d';
const STUB_BOOT_PREV = '7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d';
const STUB_BOOT_OLDEST = '1e2b5b5c1d6f4b0e9a1c3f6e7d8a9b0c';
// task 93: GET /boot - the Charly's boot of 2026-09-12 as internal/bootchart reads it, this boot of the stub
const STUB_TIMELINE = JSON.parse(fs.readFileSync(path.join(HERE, 'boot-timeline.json'), 'utf8'));
// task 179: the Licences page's SBOM (a small CycloneDX 1.6 fixture with every kind of row)
const STUB_SBOM = JSON.parse(fs.readFileSync(path.join(HERE, 'sbom.json'), 'utf8'));
// two kept earlier boots: the previous one (in the journal too), hmipserver 12 s faster and without
// RedMatic; an older one the journal no longer holds, 5 s faster
const STUB_BOOT_KEPT = '9f8e7d6c5b4a39281706f5e4d3c2b1a0';
function keptBoot(id, fasterS, without) {
    const t = structuredClone(STUB_TIMELINE);
    t.boot_id = id;
    t.snapshot = true;
    const pivot = t.units.find((u) => u.id === 'hmipserver.service').activating;
    t.units = t.units.filter((u) => u.id !== without);
    for (const u of t.units) {
        if (u.id === 'hmipserver.service') {
            u.duration_ms -= fasterS * 1000;
            u.active -= fasterS;
        } else if (u.activating > pivot) {
            u.activating -= fasterS;
            if (u.active !== undefined) u.active -= fasterS;
            if (u.inactive !== undefined) u.inactive -= fasterS;
        }
    }
    for (const k of ['userspace_ms', 'total_ms', 'multi_user_ms']) t.summary[k] -= fasterS * 1000;
    t.timestamps.finish -= fasterS;
    t.timestamps.multi_user -= fasterS;
    return t;
}
const STUB_TIMELINE_PREV = keptBoot(STUB_BOOT_PREV, 12, 'addon-redmatic.service');
const STUB_TIMELINE_KEPT = keptBoot(STUB_BOOT_KEPT, 5, '');
const keptInfo = (t, hoursAgo) => ({boot_id: t.boot_id, started: new Date(Date.now() - hoursAgo * 3600e3).toISOString(), recorded_ms: Date.now() - (hoursAgo - 0.1) * 3600e3, total_ms: t.summary.total_ms, userspace_ms: t.summary.userspace_ms});
// task 102: what runs wrote into the journal, by OCCULITE_RUN_ID - an ACME test and a flash in flight;
// the pages read them through /log?run=, and any other run is one the journal no longer holds
const STUB_RUN_CERT = '20260912T120000-5eed0001';
const STUB_RUN_FLASH = '20260912T120000-5eed0002';
const STUB_RUNS = {
    [STUB_RUN_CERT]: [
        ['acme', 'info', 'test: ccu.example.org via http-01'],
        ['acme', 'info', '[INFO] [ccu.example.org] acme: Obtaining bundled SAN certificate'],
    ],
    [STUB_RUN_FLASH]: [
        ['radio-firmware', 'info', 'hmipserver stopped'],
        ['radio-firmware', 'info', 'rfd stopped'],
        ['radio-firmware', 'info', 'multimacd stopped'],
        ['radio-firmware', 'info', '/dev/raw-uart is free'],
        ['radio-firmware', 'info', 'coprocessor reports 4.4.18'],
        ['radio-firmware', 'info', 'flashing dualcopro_update_blhmip-4.4.22.eq3 (4.4.22, hmip)'],
        ['radio-firmware', 'info', 'hmip-copro-update: writing 104616 bytes'],
    ],
};
const STUB_KERNEL = [
    [0, 'notice', 'Linux version 6.12.47-v8 (builder@openccu) #1 SMP PREEMPT'],
    [0, 'info', 'Machine model: Raspberry Pi 4 Model B Rev 1.1'],
    [1_234_567, 'warning', 'random: crng init done'],
    [2_100_000, 'info', 'usb 1-1: new high-speed USB device number 2 using xhci_hcd'],
    [3_450_012, 'info', 'EXT4-fs (mmcblk0p3): mounted filesystem with ordered data mode'],
    [12_500_001, 'err', 'mmc0: timeout waiting for hardware interrupt.'],
    [15_000_300, 'info', 'brcmfmac: F1 signature read @0x18000000=0x15264345'],
    [22_700_000, 'crit', 'Under-voltage detected! (0x00050005)'],
    [30_000_100, 'info', 'IPv6: ADDRCONF(NETDEV_CHANGE): eth0: link becomes ready'],
];

// tasks 53, 54, 56: what a spec plants through a cookie. The three Playwright projects share
// this one stub, so a variant lives in the browser that asked for it and nowhere else.
// openccu-lite task 217: the HmIP access points. Default: the maintainer's HAP-B1 (TARGA's SGTIN,
// an update listed by eQ-3) and the Charly's DRAP, the ports accepted. stub-aps=none (none paired),
// blocked (paired, 9294 rejected), reopen (none paired, 9294 rejected), silent (HmIP-RF does not
// answer), off (no HmIP-RF on the system)
function accessPointsView(jar) {
    const mode = jar['stub-aps'] ?? '';
    const port = (p, proto, target, rule = 'r' + p) => ({port: p, proto, target, rule, source: '0/0', owner: 'hmip-ap'});
    const rejected = mode === 'blocked' || mode === 'reopen';
    const firewall = {
        ports: [port(9293, 'tcp', 'ACCEPT'), port(9294, 'tcp', rejected ? 'REJECT' : 'ACCEPT'), port(43438, 'udp', 'ACCEPT')],
        hint: {none: 'unused', blocked: 'blocked', reopen: 'reopen', silent: 'unknown'}[mode] ?? 'keep',
    };
    if (mode === 'off') return {interface: 'HmIP-RF', available: false, access_points: [], firewall: null};
    if (mode === 'silent') return {interface: 'HmIP-RF', available: true, error: 'dial tcp 127.0.0.1:32010: connect: connection refused', access_points: [], firewall};
    const aps = mode === 'none' || mode === 'reopen' ? [] : [
        {address: '00030000000A13', type: 'HmIP-HAP-B1', firmware: '2.2.18', available_firmware: '0.0.0', firmware_update_state: 'UP_TO_DATE', reachable: true, ip_address: '192.0.2.155', duty_cycle: 2.5, carrier_sense: 4, connected: true, sgtin: '30150377DC00030000000A13', name: 'Access point cellar', latest: '2.2.20', update_available: true},
        {address: '00170000000A08', type: 'HmIPW-DRAP', firmware: '3.0.36', available_firmware: '3.0.36', firmware_update_state: 'LIVE_UP_TO_DATE', reachable: mode === 'blocked' ? false : true, ip_address: '192.0.2.209', config_pending: mode === 'blocked', sgtin: '3014F711A000170000000A08'},
    ];
    return {interface: 'HmIP-RF', available: true, access_points: aps, firewall};
}

// openccu-lite task 220: eQ-3's LAN devices as a scan finds them. stub-lan=none: nobody answers
const lanWrites = [];
function readBody(req) {
    return new Promise((resolve) => {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => resolve(body));
    });
}
function lanDevicesView(jar) {
    if (jar['stub-lan'] === 'none') return {scanned: now, devices: []};
    const run = (ip) => ({ip, gateway: '192.0.2.1', netmask: '255.255.255.0', dns1: '192.0.2.1', dns2: '0.0.0.0'});
    const cfg = (ip, name) => ({ip, gateway: '192.168.1.1', netmask: '255.255.255.0', dns1: '192.168.1.1', dns2: '0.0.0.0', dhcp: true, auto_ip: true, crypt: 1, name_max: 16, name});
    return {
        scanned: now,
        devices: [
            {type: 'eQ3-HMIP-HAP-App', serial: '30150377DC00030000000A13', version: '2.2.18', protocol_version: 4, ip: '192.0.2.155', runtime: run('192.0.2.155'), config: cfg('192.168.1.224', 'C00030000000A13'), kind: 'access-point', writable: true, password: 'sticker', paired: true, same_subnet: true},
            {type: 'eQ3-HM-LGW-App', serial: 'KEQ9000006', version: '1.1.5', protocol_version: 2, ip: '192.0.2.124', services: [{protocol: 2, port: 2000}, {protocol: 42, port: 2001}], runtime: run('192.0.2.124'), config: cfg('192.168.1.224', 'KEQ9000006'), kind: 'gateway', writable: true, password: 'sticker', same_subnet: true},
            {type: 'eQ3-HMW-LGW-App', serial: 'LEQ9000004', version: '1.0.5', protocol_version: 2, ip: '192.0.2.116', runtime: run('192.0.2.116'), config: {...cfg('192.0.2.116', 'LEQ9000004'), dhcp: false}, kind: 'gateway', writable: true, password: 'configured', configured: 'wired', name: 'Wired', same_subnet: true},
            {type: 'eQ3-HmIP-CCU3-App', serial: '4110000a1', version: '3.89.8.20260719', protocol_version: 2, ip: '192.0.2.230', kind: 'ccu', writable: false, same_subnet: true, link: 'http://192.0.2.230/'},
        ],
    };
}

function cookieJar(req) {
    const jar = {};
    for (const part of (req.headers.cookie ?? '').split(/;\s*/)) {
        const i = part.indexOf('=');
        if (i > 0) jar[part.slice(0, i)] = decodeURIComponent(part.slice(i + 1));
    }
    return jar;
}
// task 129 phase 3: the radio connections of the stub's box (the HmIP-RFUSB serving both stacks,
// two LAN gateways). A spec's change is kept per stub-conn cookie, so the projects that share the
// stub never see each other's; without the cookie the box is on auto and a change is not kept.
const CONN_MODULE = {id: '0000000A02', hardware: 'HMIP-RFUSB', node: '/dev/raw-uart', device_type: 'eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1', sgtin: '3014F711A000040000000A02', version: '4.4.18'};
// task 150: the HmIP option names the paths hmipserver can take to it; BidCos-RF's has none
const CONN_HMIP = {...CONN_MODULE, paths: ['direct', 'multimacd']};
// openccu-lite B-282: stub-conn-trx=1 - the same stick on the HmIP-only firmware line (HMIP_TRX_App,
// firmware 1.8.3): HmIP directly only, not offered for BidCos-RF, no routing flags
const CONN_TRX = {...CONN_MODULE, version: '1.8.3', paths: ['direct'], application: 'HMIP_TRX_App', hmip_only: true};
// task 150: the conflicts the box refuses at the preview (plan.go's Conflict)
function connConflict(ch) {
    if (ch.hmip_path === 'direct' && ch.bidcos !== 'none') return 'HmIP cannot open the module directly while BidCos-RF uses it through the multiplexer';
    return '';
}
const connState = new Map();
// task 199: `basic` is a module that carries HmIP but cannot route for HmIP-HAPs and DRAPs (an
// HM-MOD-RPI-PCB against an RPI-RF-MOD) - the red mark before the routing sentence
function connPlan(ch, missing, basic, trx) {
    const stick = {hardware: 'HMIP-RFUSB', node: '/dev/raw-uart', device_type: CONN_MODULE.device_type, address: '0xBC0A08', serial: '0000000A02', sgtin: CONN_MODULE.sgtin, version: trx ? '1.8.3' : '4.4.18', module: 0};
    // B-272: the board's module when it is the choice (stub-conn-board=detected)
    const board = {hardware: 'HM-MOD-RPI-PCB', node: '/dev/raw-uart1', device_type: BOARD_MODULE.device_type, address: '0x3D0A01', serial: BOARD_MODULE.id, sgtin: BOARD_MODULE.sgtin, version: '2.8.6', module: 1};
    const hmrf = ch.bidcos === BOARD_MODULE.id ? board : stick;
    const hmip = ch.hmip === BOARD_MODULE.id ? board : stick;
    // B-282: the HmIP-only stick carries no BidCos-RF and takes no multimacd path
    const none = ch.bidcos === 'none' || trx;
    // HmIP through multimacd: with BidCos-RF on the module, or by choice (task 150)
    const mmdHmIP = !missing && !trx && (!none || ch.hmip_path === 'multimacd');
    const mmd = !none || mmdHmIP;
    const mmdNode = none ? hmip.node : hmrf.node;
    return {
        multimacd: mmd ? {run: true, node: mmdNode, reason: `HmIP and BidCos-RF share the module on ${mmdNode}`} : {run: false, reason: 'not required: BidCos-RF has no local module'},
        rfd: none ? {run: true, reason: 'BidCos-RF: a LAN gateway'} : {run: true, node: '/dev/mmd_bidcos', reason: 'BidCos-RF: the module through /dev/mmd_bidcos, a LAN gateway'},
        hmipserver: missing ? {run: true, reason: 'no HmIP module: the VirtualDevices half alone'} : !mmdHmIP ? {run: true, node: hmip.node, reason: `HmIP on ${hmip.node} directly`} : {run: true, node: '/dev/mmd_hmip', reason: `HmIP on ${mmdNode} through the multiplexer`},
        ...(none ? {} : {hmrf}), ...(missing ? {missing_hmip: missing} : {hmip}),
        rfd_local: !none, rfd_usb_adapter: false, rfd_lan_gateway: true, hmip_advanced: !missing && !basic && !trx,
        interfaces: missing ? ['BidCos-RF', 'VirtualDevices'] : ['BidCos-RF', 'VirtualDevices', 'HmIP-RF'], notes: [],
    };
}
// task 155 (D-106): stub-conn-fatal=unreachable|refused|plain is a box whose hmipserver stopped on a
// rejected adapter exchange (plain: a marker from before the causes existed); the exchange routes
// below clear it per stub-conn cookie, so the notice goes once the page reloads
const EX_PREVIOUS = '3014F711A0001F0000000A04';
const exCleared = new Set();
const exCalls = new Map();
function connFatal(jar) {
    const cause = jar['stub-conn-fatal'];
    if (!cause || exCleared.has(jar['stub-conn'])) return undefined;
    return {code: 'adapter-exchange-rejected', line: 'Adapter exchange was rejected by key server.', adapter: CONN_MODULE.sgtin, at: '2026-09-23T10:00:00Z', ...(cause === 'plain' ? {} : {cause})};
}
// openccu-lite B-272: an HB-RF-ETH configured under LAN devices while both processes are pinned
// to the stick - stub-conn-board=detected: the radio hotplug attached it and its HM-MOD-RPI-PCB is
// in the detection with no role; pending: the board does not answer (192.0.2.60); arriving: the
// address just set, the first two reads say it is not detected yet, then it is (per stub-conn cookie)
const BOARD_MODULE = {id: 'MEQ9000005', hardware: 'HM-MOD-RPI-PCB', node: '/dev/raw-uart1', device_type: 'HB-RF-ETH@192.0.2.50', sgtin: '3014F711A061A70000000A05', version: '2.8.6'};
const connBoardReads = new Map();
function connBoard(jar) {
    const mode = jar['stub-conn-board'];
    if (!mode) return null;
    if (mode === 'pending') return {address: '192.0.2.60', connected: false, detected: false};
    if (mode === 'arriving') {
        const n = (connBoardReads.get(jar['stub-conn']) ?? 0) + 1;
        connBoardReads.set(jar['stub-conn'], n);
        if (n <= 2) return {address: '192.0.2.50', connected: n === 2, detected: false};
    }
    return {address: '192.0.2.50', connected: true, detected: true, serial: BOARD_MODULE.id};
}
function connModule(o, roles) {
    const {id, ...rest} = o;
    return {serial: id, ...rest, probe: 'ok', roles};
}
function connStatus(jar) {
    const s = connState.get(jar['stub-conn']) ?? {choices: {hmip: '', bidcos: '', hmip_path: ''}, last: null};
    // stub-conn-missing=<id>: the stick HmIP-RF is pinned to is unplugged
    // openccu-lite task 318: stub-conn-held=<sgtin> - on Automatic, the module that holds the HmIP
    // network is missing; the stick is there, offered, and not taken
    const held = !jar['stub-conn-missing'] && jar['stub-conn-held'] && s.choices.hmip === '' ? jar['stub-conn-held'] : '';
    const missing = jar['stub-conn-missing'] || held;
    const choices = missing && !held ? {...s.choices, hmip: missing} : s.choices;
    const fatal = connFatal(jar);
    const board = connBoard(jar);
    const withBoard = !!board?.detected;
    const trx = jar['stub-conn-trx'] === '1';
    const options = {hmip: missing && !held ? [] : [trx ? CONN_TRX : CONN_HMIP, ...(withBoard ? [{...BOARD_MODULE, paths: ['multimacd']}] : [])], bidcos: (missing && !held) || trx ? [] : [CONN_MODULE, ...(withBoard ? [BOARD_MODULE] : [])]};
    const plan = connPlan(choices, missing, jar['stub-conn-basic'] === '1', trx);
    if (held) plan.hmip_pin = held;
    const rolesOf = (id) => [...(plan.hmrf?.serial === id ? ['BidCos-RF'] : []), ...(plan.hmip?.serial === id ? ['HmIP-RF'] : [])];
    const modules = [...(missing && !held ? [] : [connModule(CONN_MODULE, rolesOf(CONN_MODULE.id))]), ...(withBoard ? [connModule(BOARD_MODULE, rolesOf(BOARD_MODULE.id))] : [])];
    // openccu-lite B-300: stub-conn-header=empty|wrong - a Pi's GPIO header beside the stick. The
    // daemon leaves an empty header (the probe cut at 6 s) out of the modules, and names it as
    // header_silent only while a chosen module is missing (with stub-conn-missing); a module on
    // the header that answered wrongly is a module and keeps its card
    const header = jar['stub-conn-header'];
    if (header === 'wrong') modules.push({serial: '', hardware: '', node: '/dev/raw-uart2', device_type: 'GPIO@fe201000.serial', probe: 'error', detail: 'unexpected answer: 0x00', roles: []});
    const silent = header === 'empty' && missing ? {header_silent: {node: '/dev/raw-uart2', probe: 'timeout', detail: 'no answer within 6s', missing: [missing]}} : {};
    // openccu-lite B-285/B-302: stub-conn-moveback=gap|plain - a module-move snapshot is kept (the
    // way back offered), with or without a security counter gap; stub-conn-gap=1 - after the way
    // back, the gap is still open (the notice)
    const gap = {from: CONN_MODULE.sgtin, to: '3014F5AC9400040000000A09', highest: 8529664, back: 8284417, behind: 245247, until: new Date(Date.now() + 20 * 3600_000 - 60_000).toISOString()};
    const mb = jar['stub-conn-moveback'] ? {hmip_move_back: {previous: gap.to, at: '2026-10-03T10:11:30Z', choices: {hmip: '', bidcos: '', hmip_path: ''}, devices: 2, ...(jar['stub-conn-moveback'] === 'gap' ? {counter_gap: gap} : {})}, hostname: 'openccu'} : {};
    const open = jar['stub-conn-gap'] === '1' ? {hmip_counter_gap: {...gap, at: '2026-10-03T10:12:30Z'}} : {};
    return {available: true, choices, options, plan, mode: 'NORMAL', ...(fatal ? {hmip_fatal: fatal} : {}), modules, ...silent, ...(board ? {hb_rf_eth: board} : {}), ...mb, ...open, running: null, last: s.last};
}
// openccu-lite task 301: the local record of adapter exchanges (stub-conn-exchanges=swap adds the
// local swap onto the previous module that the refused diagnosis names as the cause, B-289, as an
// older record wrote it - accepted; swap-failed as B-289 writes it - rejected, adapter-version)
function exExchanges(jar) {
    const list = [
        {at: '2026-09-27T19:39:12Z', from: '3014F711A0001F5F000000AF', to: EX_PREVIOUS, address: '0xA0B1C2', mode: 'key-server', outcome: 'accepted', line: 'Adapter exchange successful.'},
        {at: '2026-09-30T16:47:30Z', from: EX_PREVIOUS, to: CONN_MODULE.sgtin, address: '0xA0B1C2', mode: 'key-server', outcome: 'rejected', cause: 'refused', line: 'Adapter exchange was rejected by key server.'},
    ];
    if (jar['stub-conn-exchanges'] === 'swap') list[0] = {...list[0], mode: 'local-swap', line: 'Adapter exchange successful.'};
    if (jar['stub-conn-exchanges'] === 'swap-failed') list[0] = {...list[0], to_version: '1.8.3', mode: 'local-swap', outcome: 'rejected', cause: 'adapter-version', line: 'Could not exchange network key, adapter version not supported'};
    return list.reverse();
}
function exView(jar) {
    const lk = lkStateOf(jar);
    const fatal = connFatal(jar);
    return {...(fatal ? {fatal} : {}), module: CONN_MODULE.sgtin, previous: jar['stub-conn-fatal-none'] === '1' ? [] : [EX_PREVIOUS], replaces_snapshots: lk.snapshots.some((x) => x.sgtin === EX_PREVIOUS) ? [EX_PREVIOUS] : undefined, local_key: lk.enabled, exchange_id: false, exchanges: exExchanges(jar), hostname: 'lab-ccu'};
}
const importDismissed = new Set();
const importRetried = new Set();
function exRoute(req, u, res) {
    const jar = cookieJar(req);
    if (req.method === 'GET') return sendJSON(res, exView(jar));
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        if (!connFatal(jar)) return sendJSON(res, {error: 'local-key', message: 'HmIP-RF is not stopped on a rejected adapter exchange'}, 409);
        if (u.pathname.endsWith('/fresh-start')) {
            if (!b.confirm) return sendJSON(res, {error: 'confirm', message: 'confirm: true is required'}, 400);
            if ((b.hostname ?? '').trim().toLowerCase() !== 'lab-ccu') return sendJSON(res, {error: 'hostname', message: "the host name typed does not match this system's"}, 400);
            const lk = lkStateOf(jar);
            if (b.local_key && !lk.enabled) Object.assign(lk, {enabled: true, source: 'generated', keyserver_mode: 'LOCAL'});
            lk.snapshots = [{sgtin: EX_PREVIOUS, at: new Date().toISOString(), files: [`${EX_PREVIOUS}.ap`, `${EX_PREVIOUS}.apkx`, `${EX_PREVIOUS}.bbkx`], kind: 'fresh-start'}, ...lk.snapshots.filter((x) => x.sgtin !== EX_PREVIOUS)];
        }
        exCalls.set(jar['stub-conn'], [...(exCalls.get(jar['stub-conn']) ?? []), {path: u.pathname, body: b}]);
        if (jar['stub-conn']) exCleared.add(jar['stub-conn']);
        return sendJSON(res, exView(jar), 202);
    });
}
// task 157: the firewall's rule list per browser (stub-fw=<id>; without it the box's state is shared
// and a change is not kept). Converted from a firewall.conf once (the notice), the owners' rules, a
// hand-made one; the listeners' coverage is computed from the rules, as the box does.
const FW_OWNERS = {web: 'Web server', ssh: 'SSH', 'hmip-ap': 'HmIP access points', 'addon:mosquitto': 'addon: mosquitto', discovery: 'Network discovery'};
function fwBase() {
    const r = (id, port, proto, source, family, target, owner, extra = {}) => ({id, port, proto, source, family, target, ...(owner ? {owner} : {}), ...extra});
    return {
        config: {
            version: 1,
            policy: {ipv4: 'DROP', ipv6: 'DROP'},
            rules: [
                r('a1b2c3d4', 80, 'tcp', 'local networks', 'both', 'ACCEPT', 'web', {comment: 'lighttpd HTTP, redirects to HTTPS'}),
                r('a1b2c3d5', 443, 'tcp', 'local networks', 'both', 'ACCEPT', 'web', {comment: 'lighttpd HTTPS: web UI and API'}),
                r('b1b2c3d4', 22, 'tcp', 'local networks', 'both', 'ACCEPT', 'ssh', {comment: 'sshd'}),
                r('c1b2c3d4', 9293, 'tcp', '0/0', 'both', 'ACCEPT', 'hmip-ap', {comment: 'hmipserver update server: HmIP-HAP, HmIPW-DRAP'}),
                r('c1b2c3d5', 9294, 'tcp', '0/0', 'both', 'ACCEPT', 'hmip-ap', {comment: 'hmipserver update server: HmIP-HAP2', log: true}),
                r('c1b2c3d6', 43438, 'udp', '0/0', 'both', 'ACCEPT', 'hmip-ap', {comment: 'hmipserver: HmIP access points (HAP, DRAP)'}),
                // openccu-lite B-221: the eQ-3 discovery is the network discovery's on every system
                r('c1b2c3d7', 43439, 'udp', '0/0', 'both', 'ACCEPT', 'discovery', {comment: 'eQ-3 discovery: apps find the system'}),
                r('d1b2c3d4', 1883, 'tcp', '0/0', 'both', 'ACCEPT', 'addon:mosquitto', {comment: 'mosquitto: MQTT'}),
                r('f1b2c3d1', 0, 'udp', '0/0', 'ipv4', 'ACCEPT', 'discovery', {dest: '224.0.0.0/24', comment: 'link-local multicast: mDNS (Matter), discovery'}),
                r('f1b2c3d2', 0, 'igmp', '0/0', 'ipv4', 'ACCEPT', 'discovery', {dest: '224.0.0.0/24', comment: 'IGMP: multicast group membership'}),
                r('f1b2c3d3', 0, 'udp', '0/0', 'ipv6', 'ACCEPT', 'discovery', {dest: 'ff02::/16', comment: 'IPv6 link-local multicast: mDNS (Matter)'}),
                r('f1b2c3d4', 1900, 'udp', '0/0', 'both', 'ACCEPT', 'discovery', {comment: 'ssdpd: SSDP, apps find the system'}),
                r('f1b2c3d5', 0, 'udp', '0/0', 'both', 'ACCEPT', 'discovery', {sport: 43439, comment: 'discovery replies of HmIP access points'}),
                r('f1b2c3d6', 0, 'udp', '0/0', 'both', 'ACCEPT', 'discovery', {sport: 23272, comment: 'discovery replies of LAN gateways'}),
                r('f1b2c3d7', 23272, 'udp', '0/0', 'both', 'ACCEPT', 'discovery', {comment: 'LAN gateway discovery'}),
                r('e1b2c3d4', 2001, 'tcp', 'local networks', 'both', 'ACCEPT', '', {comment: 'XMLRPC rfd (BidCos-RF)'}),
                // B-219, appended as on an existing system: mDNS from the local networks - the answers to the system's queries, and queries to it
                r('f1b2c3d8', 0, 'udp', 'local networks', 'both', 'ACCEPT', 'discovery', {sport: 5353, comment: 'mDNS answers: HB-RF-ETH boards and other devices found'}),
                r('f1b2c3d9', 5353, 'udp', 'local networks', 'both', 'ACCEPT', 'discovery', {comment: 'mDNS queries to this system'}),
            ],
            active: ['addon:mosquitto/1883/tcp', 'discovery/0/igmp|0|224.0.0.0/24', 'discovery/0/udp|0|224.0.0.0/24', 'discovery/0/udp|0|ff02::/16', 'discovery/0/udp|23272|', 'discovery/0/udp|43439|', 'discovery/0/udp|5353|', 'discovery/1900/udp', 'discovery/23272/udp', 'discovery/43439/udp', 'discovery/5353/udp', 'hmip-ap/43438/udp', 'hmip-ap/9293/tcp', 'hmip-ap/9294/tcp', 'ssh/22/tcp', 'web/443/tcp', 'web/80/tcp'],
            migration: {at: now, notes: [
                {text: 'Mode {mode}: the policy is DROP for IPv4 and IPv6.', args: {mode: 'RESTRICTIVE'}},
                {text: 'Automatic rules: {owners}.', args: {owners: 'addon:mosquitto discovery hmip-ap ssh web'}},
                {text: 'Service {id} (full access): {n} rule(s) from the local networks for ports {ports}, as before.', args: {id: 'XMLRPC', n: '1', ports: '2001'}},
                {text: 'Service {id} ({access} access): not converted - the system runs no process behind it, so its ports {ports} stay closed.', args: {id: 'NEOSERVER', access: 'full', ports: '1901 1902 5987 8088 9099 10000 48899 49880'}},
                {text: 'User port "{port}" was not a port number; libfirewall opened nothing for it, and no rule was made.', args: {port: '5353/udp'}},
            ]},
        },
        draft: null,
        pending: null,
        frame: {
            ipv4: ['-A INPUT -i lo -j ACCEPT', '-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT', '-A INPUT -p icmp -m icmp --icmp-type 8 -j ACCEPT', '-A INPUT -p icmp -m icmp --icmp-type 3 -j ACCEPT', '-A INPUT -p icmp -m icmp --icmp-type 11 -j ACCEPT', '-A INPUT -p udp -m udp --sport 67 --dport 68 -j ACCEPT'],
            ipv6: ['-A INPUT -i lo -j ACCEPT', '-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT', '-A INPUT -p ipv6-icmp -m icmp6 --icmpv6-type 135 -j ACCEPT', '-A INPUT -p udp -m udp --sport 547 --dport 546 -j ACCEPT'],
        },
        local_networks: {ipv4: ['127.0.0.0/8', '10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '169.254.0.0/16', '100.64.0.0/10'], ipv6: ['::1/128', 'fc00::/7', 'fe80::/10']},
        owners: FW_OWNERS,
        addons: [
            {id: 'mosquitto', name: 'Mosquitto', mode: 'confined', ports: [
                {port: 1883, proto: 'tcp', label: {de: 'MQTT, unverschlüsselt', en: 'MQTT, plain'}, listening: true, open: true},
                {port: 8883, proto: 'tcp', tls: true, label: {de: 'MQTT über TLS', en: 'MQTT over TLS'}, listening: false, open: false},
            ]},
            {id: 'redmatic', name: 'RedMatic', mode: 'confined', ports: [
                {port: 1880, proto: 'tcp', label: {de: 'Node-RED-Editor und Dashboard (Weboberfläche, unverschlüsselt)', en: 'Node-RED editor and dashboard (web interface, plain)'}, listening: true, open: false},
                {port: 51826, proto: 'tcp', label: {de: 'HomeKit-Zubehörprotokoll (HAP) der Bridge', en: 'HomeKit accessory protocol (HAP) of the bridge'}, listening: false, open: false},
            ]},
        ],
    };
}
// the sockets, as /proc/net and the helper's owners give them
const FW_SOCKETS = [
    {proto: 'tcp', port: 80, address: '0.0.0.0', loopback: false, pid: 412, process: 'lighttpd', unit: 'lighttpd.service'},
    {proto: 'tcp6', port: 22, address: '::', loopback: false, pid: 88, process: 'sshd', unit: 'sshd.service'},
    {proto: 'tcp', port: 1883, address: '0.0.0.0', loopback: false, pid: 1203, process: 'mosquitto', unit: 'addon-mosquitto.service'},
    {proto: 'tcp', port: 2001, address: '0.0.0.0', loopback: false, pid: 977, process: 'rfd', unit: 'rfd.service'},
    {proto: 'tcp', port: 8183, address: '127.0.0.1', loopback: true, pid: 301, process: 'occulited', unit: 'occulited.service'},
    // the Charly's (B-80): one port on several addresses - the page keys rows by protocol, address and port
    {proto: 'tcp', port: 1880, address: '0.0.0.0', loopback: false, pid: 1500, process: 'node-red', unit: 'addon-redmatic.service'},
    {proto: 'tcp', port: 1880, address: '127.0.0.1', loopback: true, pid: 1500, process: 'node-red', unit: 'addon-redmatic.service'},
    {proto: 'tcp6', port: 1880, address: '::', loopback: false, pid: 1500, process: 'node-red', unit: 'addon-redmatic.service'},
    {proto: 'udp', port: 5353, address: '0.0.0.0', loopback: false, pid: 220, process: 'avahi-daemon', unit: 'avahi-daemon.service'},
    {proto: 'udp', port: 5353, address: '224.0.0.251', loopback: false, pid: 220, process: 'avahi-daemon', unit: 'avahi-daemon.service'},
];
const fwStates = new Map();
function fwStateOf(jar) {
    const id = jar['stub-fw'] ?? '';
    if (!id) return fwBase();
    if (!fwStates.has(id)) fwStates.set(id, fwBase());
    return fwStates.get(id);
}
function fwView(st) {
    const {addons, ...v} = st;
    return structuredClone(v);
}
function fwListeners(st) {
    const c = st.config;
    return FW_SOCKETS.map((l) => {
        const fams = !l.proto.endsWith('6') ? ['ipv4'] : l.address === '::' ? ['ipv4', 'ipv6'] : ['ipv6'];
        const base = l.proto.replace('6', '');
        const cover = {};
        for (const f of fams) {
            const r = c.rules.find((x) => x.port === l.port && x.proto === base && (x.family === 'both' || x.family === f));
            cover[f] = r ? {rule: r.id, target: r.target, source: r.source, ...(r.owner ? {owner: r.owner} : {})} : {target: f === 'ipv6' ? c.policy.ipv6 : c.policy.ipv4};
        }
        return {...l, cover};
    });
}
// task 143: System -> Remote access per browser (stub-ra=<id>): the switches, the pair, and classic
// RPC's rules as the box would own them (local networks, the XMLRPC comments)
const RA_PORTS = [
    {port: 2001, tls: false, interface: 'BidCos-RF', process: 'rfd', backend: 32001, running: true},
    {port: 2010, tls: false, interface: 'HmIP-RF', process: 'hmipserver', backend: 32010, running: true},
    {port: 9292, tls: false, interface: 'VirtualDevices', process: 'hmipserver', backend: 39292, running: true},
    {port: 42001, tls: true, interface: 'BidCos-RF', process: 'rfd', backend: 32001, running: true},
    {port: 42010, tls: true, interface: 'HmIP-RF', process: 'hmipserver', backend: 32010, running: true},
    {port: 49292, tls: true, interface: 'VirtualDevices', process: 'hmipserver', backend: 39292, running: false},
];
const RA_COMMENTS = {2001: 'XMLRPC rfd (BidCos-RF)', 2010: 'XMLRPC hmipserver (HmIP-RF)', 9292: 'XMLRPC HMServer (virtual devices, groups)', 42001: 'XMLRPC rfd (BidCos-RF) TLS', 42010: 'XMLRPC hmipserver (HmIP-RF) TLS', 49292: 'XMLRPC HMServer (virtual devices, groups) TLS'};
const raStates = new Map();
function raStateOf(jar) {
    const id = jar['stub-ra'] ?? '';
    const fresh = () => ({plain: false, tls: false, user: '', set: false, pub: {enabled: false, account: 'guest'}, fw: jar['stub-ra-fw'] ?? ''});
    if (!id) return fresh();
    if (!raStates.has(id)) raStates.set(id, fresh());
    return raStates.get(id);
}
function raFirewall(ports, mode) {
    const open = ports.filter((p) => p.open);
    const verdicts = open.map((p) => (mode === 'blocked' && p.port === 2010 ? {port: p.port, proto: 'tcp', target: 'REJECT', rule: 'hand2010', source: '0/0'} : {port: p.port, proto: 'tcp', target: 'ACCEPT', rule: `ra${p.port}`, source: 'local networks', owner: 'rpc'}));
    return {ports: verdicts, hint: open.length === 0 ? 'closed' : verdicts.some((v) => v.target !== 'ACCEPT') ? 'blocked' : 'open'};
}
function raView(st) {
    const ports = RA_PORTS.map((p) => ({...p, open: p.tls ? st.tls : st.plain}));
    return {
        classic: {plain: st.plain, tls: st.tls, auth: st.set ? 'password' : 'none', ...(st.set ? {user: st.user} : {}), password_set: st.set},
        ports, hs485d: false,
        rules: ports.filter((p) => p.open).map((p) => ({id: `ra${p.port}`, port: p.port, proto: 'tcp', source: 'local networks', family: 'both', target: 'ACCEPT', comment: RA_COMMENTS[p.port], owner: 'rpc'})),
        // task 223: what the firewall does with each open port; stub-ra-fw=blocked rejects 2010
        firewall: raFirewall(ports, st.fw),
        // task 77: lite-rpc's switch and its read-only values
        lite: {available: true, streams: {per_token: 2, total: 16, open: 2}, buffer: {seconds: 300, events: 5000}},
        // task 193: the Control app's public mode
        public: {...st.pub, available: true},
    };
}
// task 77: the open lite-rpc streams - two by default, per test (the cookie stub-lite: none = no
// stream, anything else its own list)
const liteStreams = new Map();
function liteStreamsOf(jar) {
    const id = jar['stub-lite'] ?? '';
    if (!liteStreams.has(id)) {
        liteStreams.set(id, [
            {id: '7', transport: 'sse', subject: {kind: 'token', name: 'home-assistant'}, remote: '192.0.2.50', filter: {interface: ['HmIP-RF']}, since: '2026-09-22T12:00:00Z', sent: 1234, dropped: 0},
            {id: '9', transport: 'websocket', subject: {kind: 'session', name: 'admin'}, remote: '192.0.2.7', filter: {}, since: '2026-09-22T12:30:00Z', sent: 12, dropped: 0, last_resync: 'gap'},
        ]);
    }
    return liteStreams.get(id);
}
// task 79: the RPC trace's switch, per test (stub-trace=<id>); stub-trace-sd=1 is an SD-card box
const traceStates = new Map();
function traceRoute(req, u, res) {
    const jar = cookieJar(req);
    const id = jar['stub-trace'] ?? '';
    if (!traceStates.has(id)) traceStates.set(id, {mode: 'off', until: undefined});
    const st = traceStates.get(id);
    const view = () => ({mode: st.mode, ...(st.until ? {until: st.until} : {}), on: st.mode !== 'off', sd_warning: jar['stub-trace-sd'] === '1', available: true});
    if (req.method === 'GET') return sendJSON(res, view());
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        if (!['off', 'until', 'permanent'].includes(b.mode)) return sendJSON(res, {error: 'invalid', message: 'mode is off, until or permanent'}, 422);
        st.mode = b.mode;
        st.until = b.mode === 'until' ? new Date(Date.now() + (b.minutes ?? 60) * 60000).toISOString() : undefined;
        sendJSON(res, view());
    });
}
const TRACE_LINES = [
    'xmlrpc 192.0.2.50 (token home-assistant) → HmIP-RF getValue ["000D0000000A11:3","LEVEL"]',
    '← HmIP-RF 192.0.2.50 (token home-assistant) getValue 0.5',
    'xmlrpc HmIP-RF → occulited event ["occulited_HmIP-RF","000D0000000A11:0","UNREACH",true]',
    '→ sse home-assistant@192.0.2.50 event HmIP-RF 000D0000000A11:0 UNREACH true',
    'stream sse home-assistant@192.0.2.50 connect filter={"interface":["HmIP-RF"]} last_event_id="" devices=true',
    '← HmIP-RF occulited listDevices ' + JSON.stringify(Array.from({length: 12}, (_, i) => ({ADDRESS: `000DD8A99316${String(i).padStart(2, '0')}`, TYPE: 'HmIP-PDT', CHILDREN: [`000DD8A99316${String(i).padStart(2, '0')}:0`], FIRMWARE: '1.6.2'}))),
];
// task 193: lite-rpc's JSON path for the App - the two channels of the store with an interface,
// their types, descriptions and values; setValue is remembered per stub run
const liteValuesBy = new Map(); // per test (the cookie stub-app), so the projects do not see each other's setValue
function liteValuesOf(jar) {
    const id = jar['stub-app'] ?? '';
    if (!liteValuesBy.has(id)) liteValuesBy.set(id, {'000ABC:1': {SET_POINT_TEMPERATURE: 21.5, ACTUAL_TEMPERATURE: 20.2, SET_POINT_MODE: 0, BOOST_MODE: false}, '000DD8:3': {LEVEL: 0.6}, 'BidCoS-RF:2': {LEVEL: 0},
        '000F01:1': {SMOKE_DETECTOR_ALARM_STATUS: 0}, '000F02:3': {ACOUSTIC_ALARM_ACTIVE: false, OPTICAL_ALARM_ACTIVE: false},
        '000F03:1': {LEVEL: 0.5, HUE: 120, SATURATION: 1, COLOR_TEMPERATURE: 3000}, '000F04:6': {POWER: 12.5, ENERGY_COUNTER: 12345.6, VOLTAGE: 230.1, CURRENT: 54, FREQUENCY: 50.02}});
    return liteValuesBy.get(id);
}
const liteTypes = {'000ABC:1': 'HEATING_CLIMATECONTROL_TRANSCEIVER', '000DD8:3': 'DIMMER_VIRTUAL_RECEIVER', 'BidCoS-RF:2': 'VIRTUAL_KEY', '000F01:1': 'SMOKE_DETECTOR', '000F02:3': 'ALARM_SWITCH_VIRTUAL_RECEIVER', '000F03:1': 'UNIVERSAL_LIGHT_RECEIVER', '000F04:6': 'ENERGIE_METER_TRANSMITTER'};
const liteDescs = {
    HEATING_CLIMATECONTROL_TRANSCEIVER: {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7, TYPE: 'FLOAT', MIN: 4.5, MAX: 30.5}, ACTUAL_TEMPERATURE: {CONTROL: 'NONE', OPERATIONS: 5, TYPE: 'FLOAT'}, SET_POINT_MODE: {CONTROL: 'NONE', OPERATIONS: 7, TYPE: 'ENUM', VALUE_LIST: ['AUTO', 'MANUAL']}, BOOST_MODE: {CONTROL: 'NONE', OPERATIONS: 7, TYPE: 'BOOL'}},
    DIMMER_VIRTUAL_RECEIVER: {LEVEL: {CONTROL: 'DIMMER.LEVEL', OPERATIONS: 7, TYPE: 'FLOAT', MIN: 0, MAX: 1}, ON_TIME: {CONTROL: 'NONE', OPERATIONS: 2, TYPE: 'FLOAT'}},
    VIRTUAL_KEY: {PRESS_SHORT: {CONTROL: 'BUTTON.SHORT', OPERATIONS: 6, TYPE: 'ACTION'}, PRESS_LONG: {CONTROL: 'BUTTON.LONG', OPERATIONS: 6, TYPE: 'ACTION'}, LEVEL: {CONTROL: 'NONE', OPERATIONS: 7, TYPE: 'FLOAT'}},
    SMOKE_DETECTOR: {
        SMOKE_DETECTOR_ALARM_STATUS: {CONTROL: 'SMOKE_DETECTOR.SMOKE_DETECTOR_ALARM_STATUS', OPERATIONS: 5, TYPE: 'ENUM', VALUE_LIST: ['IDLE_OFF', 'PRIMARY_ALARM', 'INTRUSION_ALARM', 'SECONDARY_ALARM']},
        SMOKE_DETECTOR_COMMAND: {CONTROL: 'NONE', OPERATIONS: 2, TYPE: 'ENUM', VALUE_LIST: ['RESERVED_ALARM_OFF', 'INTRUSION_ALARM_OFF', 'INTRUSION_ALARM', 'SMOKE_TEST', 'COMMUNICATION_TEST', 'COMMUNICATION_TEST_RECEIVED']},
    },
    ALARM_SWITCH_VIRTUAL_RECEIVER: {
        ACOUSTIC_ALARM_SELECTION: {CONTROL: 'ALARM_SWITCH_VIRTUAL_RECEIVER.ACOUSTIC_ALARM_SELECTION', OPERATIONS: 7, TYPE: 'ENUM', VALUE_LIST: ['DISABLE_ACOUSTIC_SIGNAL', 'FREQUENCY_RISING', 'FREQUENCY_FALLING', 'FREQUENCY_RISING_AND_FALLING', 'FREQUENCY_ALTERNATING_LOW_HIGH', 'LOW_BATTERY', 'DISARMED', 'INTERNALLY_ARMED', 'EXTERNALLY_ARMED', 'EVENT', 'ERROR']},
        OPTICAL_ALARM_SELECTION: {CONTROL: 'ALARM_SWITCH_VIRTUAL_RECEIVER.OPTICAL_ALARM_SELECTION', OPERATIONS: 7, TYPE: 'ENUM', VALUE_LIST: ['DISABLE_OPTICAL_SIGNAL', 'BLINKING_ALTERNATELY_REPEATING', 'BLINKING_BOTH_REPEATING', 'DOUBLE_FLASHING_REPEATING', 'FLASHING_BOTH_REPEATING', 'CONFIRMATION_SIGNAL_0', 'CONFIRMATION_SIGNAL_1', 'CONFIRMATION_SIGNAL_2']},
        DURATION_UNIT: {CONTROL: 'NONE', OPERATIONS: 7, TYPE: 'ENUM', VALUE_LIST: ['S', 'M', 'H']}, DURATION_VALUE: {CONTROL: 'NONE', OPERATIONS: 7, TYPE: 'INTEGER', MIN: 0, MAX: 16343},
        ACOUSTIC_ALARM_ACTIVE: {CONTROL: 'NONE', OPERATIONS: 5, TYPE: 'BOOL'}, OPTICAL_ALARM_ACTIVE: {CONTROL: 'NONE', OPERATIONS: 5, TYPE: 'BOOL'},
    },
    UNIVERSAL_LIGHT_RECEIVER: {
        LEVEL: {CONTROL: 'RGBW_COLOR.LEVEL', OPERATIONS: 7, TYPE: 'FLOAT', MIN: 0, MAX: 1}, HUE: {CONTROL: 'RGBW_COLOR.HUE', OPERATIONS: 7, TYPE: 'INTEGER', MIN: 0, MAX: 360},
        SATURATION: {CONTROL: 'RGBW_COLOR.SATURATION', OPERATIONS: 7, TYPE: 'FLOAT', MIN: 0, MAX: 1}, COLOR_TEMPERATURE: {CONTROL: 'NONE', OPERATIONS: 7, TYPE: 'INTEGER', MIN: 2000, MAX: 6500},
    },
    ENERGIE_METER_TRANSMITTER: {
        POWER: {CONTROL: 'POWERMETER_IGL.POWER', OPERATIONS: 5, TYPE: 'FLOAT', UNIT: 'W'}, ENERGY_COUNTER: {CONTROL: 'POWERMETER_IGL.ENERGY_COUNTER', OPERATIONS: 5, TYPE: 'FLOAT', UNIT: 'Wh'},
        VOLTAGE: {CONTROL: 'POWERMETER_IGL.VOLTAGE', OPERATIONS: 5, TYPE: 'FLOAT', UNIT: 'V'}, CURRENT: {CONTROL: 'POWERMETER_IGL.CURRENT', OPERATIONS: 5, TYPE: 'FLOAT', UNIT: 'mA'}, FREQUENCY: {CONTROL: 'POWERMETER_IGL.FREQUENCY', OPERATIONS: 5, TYPE: 'FLOAT', UNIT: 'Hz'},
    },
};
// occulited B-47: the browser rule of lite-rpc's reads, as the daemon's originRefusal has it
// (internal/httpapi/literpc.go) - the stub knows no sessions, so an Authorization header stands for
// the session as Bearer. Sec-Fetch-Site and Origin decide where the browser sends them; where it
// sends neither (a page over plain http://<name>/, no secure context) the header credential does,
// and an EventSource, which can set no header, is refused there as on a system.
function liteOriginRefusal(req) {
    if (req.headers.authorization) return '';
    const sfs = req.headers['sec-fetch-site'];
    if (sfs === 'same-origin' || sfs === 'none') return '';
    if (sfs) return `the stream is opened from this system's own pages only (Sec-Fetch-Site: ${sfs})`;
    const origin = req.headers.origin;
    if (origin) {
        let host = '';
        try {
            host = new URL(origin).host;
        } catch {
            /* not an origin */
        }
        if (host && host.toLowerCase() === (req.headers.host ?? '').toLowerCase()) return '';
        return `the stream is opened from this system's own pages only (Origin: ${origin})`;
    }
    if (req.headers['x-occulite-request']) return '';
    return 'the stream needs the session in the Authorization header, or a browser that says its origin (over plain HTTP: the header X-Occulite-Request)';
}
// the open event streams of one test (the cookie stub-app): a setValue of that test goes out on
// them as the interface's event, the way a device answers a command. Without the cookie nothing is
// sent, so the specs that share the stub's default values never see each other's writes.
const liteOpen = new Map(); // stub-app -> Set of {res, ifaces}
// occulited B-53: the shell's streams opened per spec (the cookie stub-shellstream): [{topics, open}]
const shellStreams = new Map();
// the open lite-rpc streams per stub-lite-limit id
const liteLimited = new Map();
let liteSeq = 0;
function liteEmit(app, iface, address, key, value) {
    if (!app) return;
    liteSeq++;
    const data = JSON.stringify({interface: iface, address, key, value, ts: new Date().toISOString()});
    for (const st of liteOpen.get(app) ?? []) {
        if (st.ifaces.length === 0 || st.ifaces.includes(iface)) st.res.write(`id: stub-${liteSeq}\nevent: event\ndata: ${data}\n\n`);
    }
}
// what reached the stream's gate with ?probe=<id>, for the spec that tries the ways in from another
// origin: a request the browser never sent (a refused preflight) is not in it
const liteProbes = [];
function liteEventsRoute(req, u, res) {
    const why = liteOriginRefusal(req);
    if (u.searchParams.get('probe')) liteProbes.push({probe: u.searchParams.get('probe'), how: u.searchParams.get('how') ?? '', refused: why !== '', header: !!req.headers['x-occulite-request'] || !!req.headers.authorization});
    if (why) {
        console.error(`stub: GET ${u.pathname} refused: ${why}`);
        return sendJSON(res, {error: 'forbidden', message: why}, 403);
    }
    // the account's stream limit (rpc.streams_per_session) for the specs that set stub-lite-limit=<id>.<n>
    // (occulited B-53): the stream over the limit is answered 429, as on the system
    const limit = /^(.+)\.(\d+)$/.exec(cookieJar(req)['stub-lite-limit'] ?? '');
    if (limit) {
        const n = liteLimited.get(limit[1]) ?? 0;
        if (n >= Number(limit[2])) return sendJSON(res, {error: 'too-many-streams', message: `the session has ${n} streams open`}, 429);
        liteLimited.set(limit[1], n + 1);
        req.on('close', () => liteLimited.set(limit[1], (liteLimited.get(limit[1]) ?? 1) - 1));
    }
    res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', 'X-Accel-Buffering': 'no'});
    res.write(': connected\n\n');
    res.write(`id: stub-${liteSeq}\nevent: hello\ndata: ${JSON.stringify({boot_id: 'stub', seq: liteSeq, interfaces: [], buffer: {seconds: 300, events: 5000}})}\n\n`);
    const app = cookieJar(req)['stub-app'];
    const st = {res, ifaces: u.searchParams.getAll('interface').flatMap((v) => v.split(','))};
    if (app) {
        if (!liteOpen.has(app)) liteOpen.set(app, new Set());
        liteOpen.get(app).add(st);
    }
    // the daemon's heartbeat: a client takes 45 s of silence for a dead connection
    const ping = setInterval(() => res.write(': ping\n\n'), 15_000);
    req.on('close', () => {
        clearInterval(ping);
        liteOpen.get(app)?.delete(st);
        res.end();
    });
}
// the state store (GET /state): the last value of the datapoints of the chosen set - the list of
// internal/devstate/keys.go, as far as the stub's channels have them; a command key the specs
// write (a siren's selection, the smoke detector's command) is not kept, as on a system
const LITE_STATE_KEYS = new Set(['STATE', 'LEVEL', 'LEVEL_2', 'COLOR', 'COLOR_TEMPERATURE', 'HUE', 'SATURATION', 'SET_POINT_TEMPERATURE', 'SET_TEMPERATURE', 'SETPOINT', 'ACTUAL_TEMPERATURE', 'SET_POINT_MODE', 'CONTROL_MODE', 'BOOST_MODE', 'WINDOW_STATE',
    'MOTION', 'SMOKE_DETECTOR_ALARM_STATUS', 'ACOUSTIC_ALARM_ACTIVE', 'OPTICAL_ALARM_ACTIVE', 'POWER', 'ENERGY_COUNTER', 'VOLTAGE', 'CURRENT', 'FREQUENCY', 'TEMPERATURE', 'HUMIDITY', 'ILLUMINATION', 'UNREACH', 'STICKY_UNREACH', 'LOW_BAT', 'CONFIG_PENDING']);
const liteIfaceOf = (address) => (address.startsWith('BidCoS-RF') ? 'BidCos-RF' : 'HmIP-RF');
function liteStateRoute(req, u, res) {
    const why = liteOriginRefusal(req);
    if (why) {
        console.error(`stub: GET ${u.pathname} refused: ${why}`);
        return sendJSON(res, {error: 'forbidden', message: why}, 403);
    }
    const list = (name) => u.searchParams.getAll(name).flatMap((v) => v.split(',')).filter(Boolean);
    const ifaces = list('interface');
    const addresses = list('address');
    const entries = [];
    for (const [address, values] of Object.entries(liteValuesOf(cookieJar(req)))) {
        const iface = liteIfaceOf(address);
        if (ifaces.length && !ifaces.includes(iface)) continue;
        if (addresses.length && !addresses.some((a) => address === a || address.startsWith(a + ':'))) continue;
        for (const [datapoint, value] of Object.entries(values)) {
            if (LITE_STATE_KEYS.has(datapoint)) entries.push({interface: iface, address, datapoint, value, ts: now, lc: now, confirmed: true, source: 'event'});
        }
    }
    sendJSON(res, {entries, total: entries.length, unconfirmed: 0, event_id: `stub-${liteSeq}`, sweeps: {}, datapoints: [...LITE_STATE_KEYS]});
}
function liteRoute(req, u, res) {
    const iface = decodeURIComponent(u.pathname.split('/').pop());
    const app = cookieJar(req)['stub-app'];
    const liteValues = liteValuesOf(cookieJar(req));
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        const one = (r) => {
            const [addr, key, value] = r.params ?? [];
            const err = (code, message) => ({jsonrpc: '2.0', error: {code, message}, id: r.id});
            if (iface !== 'HmIP-RF' && iface !== 'BidCos-RF') return err(-32000, `the interface process does not answer: ${iface}`);
            switch (r.method) {
                case 'getDeviceDescription': return liteTypes[addr] ? {jsonrpc: '2.0', result: {ADDRESS: addr, TYPE: liteTypes[addr], PARENT: addr.split(':')[0]}, id: r.id} : err(-2, 'Unknown instance');
                case 'getParamsetDescription': return liteTypes[addr] ? {jsonrpc: '2.0', result: liteDescs[liteTypes[addr]], id: r.id} : err(-2, 'Unknown instance');
                case 'getParamset': return liteValues[addr] ? {jsonrpc: '2.0', result: liteValues[addr], id: r.id} : err(-2, 'Unknown instance');
                case 'setValue':
                    if (!liteValues[addr]) return err(-2, 'Unknown instance');
                    if (addr === '000ABC:1' && key === 'SET_POINT_TEMPERATURE' && value > 30) return err(-5, 'Invalid parameter or value');
                    liteValues[addr] = {...liteValues[addr], [key]: value};
                    liteEmit(app, iface, addr, key, value);
                    return {jsonrpc: '2.0', result: '', id: r.id};
                case 'putParamset': {
                    // [address, 'VALUES', {key: value}]: several datapoints in one write (the siren)
                    const [a, set, vals] = r.params ?? [];
                    if (!liteValues[a]) return err(-2, 'Unknown instance');
                    if (set !== 'VALUES') return err(-32601, `not permitted: ${r.method} on ${set} needs rpc:configure`);
                    liteValues[a] = {...liteValues[a], ...vals};
                    for (const [k, v] of Object.entries(vals ?? {})) liteEmit(app, iface, a, k, v);
                    return {jsonrpc: '2.0', result: '', id: r.id};
                }
            }
            return err(-32601, `not permitted: ${r.method} needs rpc:admin`);
        };
        sendJSON(res, Array.isArray(b) ? b.map(one) : one(b));
    });
}
function liteStreamsRoute(req, u, res) {
    const jar = cookieJar(req);
    const mode = jar['stub-lite'] ?? '';
    const list = liteStreamsOf(jar);
    if (req.method === 'GET') {
        return sendJSON(res, {streams: mode === 'none' ? [] : list, limits: {per_token: 2, total: 16}});
    }
    if (req.method === 'DELETE') {
        const id = u.pathname.split('/').pop();
        const i = list.findIndex((s) => s.id === id);
        if (i < 0) return sendJSON(res, {error: 'not-found', message: 'no such stream (any more)'}, 404);
        list.splice(i, 1);
        res.writeHead(204);
        return res.end();
    }
    sendJSON(res, {error: 'not-found', message: 'no such route'}, 404);
}
function raRoute(req, u, res) {
    const st = raStateOf(cookieJar(req));
    const path = u.pathname.replace('/api/system/v1/remote-access', '');
    if (req.method === 'GET' && path === '') return sendJSON(res, raView(st));
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        if (req.method === 'PUT' && path === '') {
            if (b.public) {
                const account = b.public.account || 'guest';
                if (!/^[a-z0-9][a-z0-9_.-]{1,31}$/.test(account)) return sendJSON(res, {error: 'invalid', message: "the public account's name has 1 to 32 letters, digits, dots, underscores or dashes"}, 422);
                if (account === 'admin') return sendJSON(res, {error: 'invalid', message: 'the account admin may administer: the public account must be one that reads or operates'}, 422);
                st.pub = {enabled: !!b.public.enabled, account};
                if (!b.classic) return sendJSON(res, raView(st));
            }
            const c = b.classic;
            if (!c) return sendJSON(res, {error: 'invalid', message: 'classic or public is missing'}, 422);
            if (c.auth === 'password' && !st.set) return sendJSON(res, {error: 'invalid', message: 'invalid classic RPC settings: set a user name and password first'}, 422);
            st.plain = !!c.plain;
            st.tls = !!c.tls;
            if (c.auth === 'none') {
                st.set = false;
                st.user = '';
            }
            return sendJSON(res, raView(st));
        }
        if (req.method === 'PUT' && path === '/classic-password') {
            if (!/^[A-Za-z0-9._-]{1,32}$/.test(b.user ?? '')) return sendJSON(res, {error: 'invalid', message: 'invalid classic RPC settings: the user name has 1 to 32 letters, digits, dots, underscores or hyphens'}, 422);
            if (!b.generate && (b.password ?? '').length < 12) return sendJSON(res, {error: 'invalid', message: 'invalid classic RPC settings: the password has at least 12 characters, on one line'}, 422);
            st.user = b.user;
            st.set = true;
            return sendJSON(res, {...raView(st), ...(b.generate ? {password: 'Gen3rat3dPassw0rdOnlyShownOnce2x'} : {})});
        }
        sendJSON(res, {error: 'not-found', message: 'no such route'}, 404);
    });
}

// task 154: the HmIP device keys - one state per test (the cookie stub-dk); three paired devices,
// the dimmer's key stored. The export wants the confirmed ticket in X-Occulite-Confirm; the cookie
// stub-confirm says how the session confirms (password, the default; oidc; none)
const DK_PAIRED = [
    {address: '000A1B2C3D4E5F', type: 'HmIP-PDT', name: 'Dimmer Flur'},
    {address: '000B1B2C3D4E5F', type: 'HmIP-eTRV-2', name: 'Heizung Bad'},
    {address: '000D1B2C3D4E5F', type: 'HmIPW-DRAP', name: ''},
];
const DK_CONFIRMED = 'CONFIRMEDAAAAAAAAAAAAAAAAA';
const dkStates = new Map();
function dkBase() {
    return {keys: {'3014F711A0000A1B2C3D4E5F': '0123456789ABCDEF0123456789ABCDEF'}, pending: new Set(), applying: 0};
}
function dkStateOf(jar) {
    const id = jar['stub-dk'] ?? '';
    if (!id) return dkBase();
    if (!dkStates.has(id)) dkStates.set(id, dkBase());
    return dkStates.get(id);
}
function dkView(st) {
    const rows = [];
    let withKey = 0;
    const matched = new Set();
    for (const d of DK_PAIRED) {
        const sg = Object.keys(st.keys).find((k) => k.slice(10) === d.address);
        if (sg) {
            matched.add(sg);
            withKey++;
        }
        rows.push({...(sg ? {sgtin: sg} : {}), address: d.address, type: d.type, ...(d.name ? {name: d.name} : {}), paired: true, has_key: !!sg, ...(sg && st.pending.has(sg) ? {pending: true} : {})});
    }
    for (const sg of Object.keys(st.keys).sort()) if (!matched.has(sg)) rows.push({sgtin: sg, address: sg.slice(10), paired: false, has_key: true, ...(st.pending.has(sg) ? {pending: true} : {})});
    rows.sort((a, b) => (a.paired !== b.paired ? (a.paired ? -1 : 1) : a.has_key !== b.has_key ? (a.has_key ? 1 : -1) : 0));
    return {rows, paired: DK_PAIRED.length, with_key: withKey, stored: Object.keys(st.keys).length, pending: st.pending.size, devices_known: true, ...(st.applying > 0 ? {applying: true} : {})};
}
const DK_PRINTED = '0123456789ABCEFGHJKLMNPQRSTUWXYZ';
function dkParse(b) {
    const sq = (x) => String(x ?? '').replace(/[\s-]/g, '').toUpperCase();
    if (b.code) {
        const m = /EQ01SG([0-9A-F]{24})DLK([0-9A-F]{32})/.exec(sq(b.code));
        return m ? {sgtin: m[1], key: m[2]} : {error: 'not an HmIP device code (EQ01SG…DLK…)'};
    }
    const sg = sq(b.sgtin);
    let key = sq(b.key);
    if (!/^[0-9A-F]{24}$/.test(sg)) return {error: 'the SGTIN is 24 characters, 0-9 and A-F (the dashes do not count)'};
    if (/^[0-9A-Z]{26}$/.test(key) && ![...key].some((c) => !DK_PRINTED.includes(c))) {
        let n = 0n;
        for (const c of key) n = n * 32n + BigInt(DK_PRINTED.indexOf(c));
        key = (n & ((1n << 128n) - 1n)).toString(16).toUpperCase().padStart(32, '0');
    }
    if (!/^[0-9A-F]{32}$/.test(key)) return {error: 'the key is the 26 printed characters or 32 hex digits'};
    return {sgtin: sg, key};
}
// task 185: SSH on the Remote access page - the switch, root's keys (the page's section and a key
// added elsewhere) and two open sessions, one from the page's own address. Adding a key and setting
// root's password want the confirmed ticket, as the device keys' export does. The state is per
// browser with the cookie stub-ssh, fresh on every request without it.
const SSH_LAB = {type: 'ssh-ed25519', bits: 256, comment: 'lab', fingerprint: 'SHA256:labLABlabLABlabLABlabLABlabLABlabLABlabLAB0', options: 'command="uptime"'};
const sshStates = new Map();
function sshBase() {
    return {
        enabled: true,
        running: true,
        managed: [],
        // the same key on two lines, as a file edited by hand can have it
        other: [SSH_LAB, SSH_LAB],
        sessions: [
            {id: 700, user: 'root', tty: 'pts/0', from: '127.0.0.1', port: 40000, since: new Date(Date.now() - 3600e3).toISOString(), method: 'password', own: true},
            {id: 800, user: 'root', from: '192.0.2.114', port: 46139, since: new Date(Date.now() - 600e3).toISOString(), method: 'publickey', key_type: 'ED25519', key_fingerprint: 'SHA256:MBm1zNU3tdJvms19ANAdCN9mhUIvo62COPiBA1ZzK3g'},
        ],
    };
}
function sshStateOf(jar) {
    const id = jar['stub-ssh'] ?? '';
    if (!id) return sshBase();
    if (!sshStates.has(id)) sshStates.set(id, sshBase());
    return sshStates.get(id);
}
function sshParse(line) {
    const f = String(line ?? '').trim().split(/\s+/);
    if (/[\r\n]/.test(String(line ?? '').trim())) return {error: 'one key per line: the paste holds a line break'};
    if (!['ssh-ed25519', 'ecdsa-sha2-nistp256', 'ecdsa-sha2-nistp384', 'ecdsa-sha2-nistp521', 'ssh-rsa'].includes(f[0])) return {error: 'not a public key line: it starts with the key type (ssh-ed25519 …); an options prefix such as from= or command= is not taken here'};
    if (!f[1] || f[1].length < 40) return {error: 'the key is not valid base64'};
    let h = 0;
    for (const c of f[1]) h = (h * 31 + c.charCodeAt(0)) >>> 0;
    return {type: f[0], bits: f[0] === 'ssh-ed25519' ? 256 : 384, comment: f.slice(2).join(' '), fingerprint: 'SHA256:' + h.toString(36).padEnd(43, 'x')};
}
function sshRoute(req, u, res) {
    const jar = cookieJar(req);
    const st = sshStateOf(jar);
    const path = u.pathname.replace('/api/system/v1/ssh', '');
    if (req.method === 'GET' && path === '') return sendJSON(res, {enabled: st.enabled, running: st.running});
    if (req.method === 'GET' && path === '/keys') return sendJSON(res, {managed: st.managed, other: st.other});
    if (req.method === 'GET' && path === '/sessions') return sendJSON(res, {sessions: st.sessions});
    if (req.method === 'DELETE' && path === '/keys') {
        const fp = u.searchParams.get('fingerprint');
        const i = st.managed.findIndex((k) => k.fingerprint === fp);
        if (i < 0) return sendJSON(res, {error: 'not-found', message: 'no key of the page has this fingerprint'}, 404);
        st.managed.splice(i, 1);
        return sendJSON(res, {ok: true});
    }
    const end = /^\/sessions\/(\d+)$/.exec(path);
    if (req.method === 'DELETE' && end) {
        const i = st.sessions.findIndex((x) => String(x.id) === end[1]);
        if (i < 0) return sendJSON(res, {error: 'not-found', message: 'no such SSH session'}, 404);
        st.sessions.splice(i, 1);
        return sendJSON(res, {ok: true});
    }
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'PUT' && path === '') {
            st.enabled = st.running = !!b.enabled;
            return sendJSON(res, {enabled: st.enabled, running: st.running});
        }
        if (req.method === 'POST' && path === '/keys') {
            const k = sshParse(b.key);
            if (k.error) return sendJSON(res, {error: 'invalid', message: k.error}, 422);
            if ([...st.managed, ...st.other].some((x) => x.fingerprint === k.fingerprint)) return sendJSON(res, {error: 'exists', message: 'this key is already there'}, 409);
            if (req.headers['x-occulite-confirm'] !== DK_CONFIRMED) return sendJSON(res, {error: 'confirm-required', message: 'this asks for the password every time'}, 403);
            st.managed.push(k);
            return sendJSON(res, k, 201);
        }
        if (req.method === 'POST' && path === '/password') {
            if (req.headers['x-occulite-confirm'] !== DK_CONFIRMED) return sendJSON(res, {error: 'confirm-required', message: 'this asks for the password every time'}, 403);
            if (String(b.password ?? '').length < 8) return sendJSON(res, {error: 'invalid', message: 'password must be at least 8 characters'}, 400);
            st.rootPassword = b.password;
            return sendJSON(res, {ok: true});
        }
        sendJSON(res, {error: 'not-found', message: 'no such route'}, 404);
    });
    return true;
}
function dkRoute(req, u, res) {
    const jar = cookieJar(req);
    const st = dkStateOf(jar);
    const path = u.pathname.replace('/api/system/v1/radio/hmip/device-keys', '');
    if (req.method === 'GET' && path === '') {
        // HmIP-RF restarts over two reads, then has read the keys
        if (st.applying > 0 && --st.applying === 0) st.pending.clear();
        return sendJSON(res, dkView(st));
    }
    if (req.method === 'GET' && path === '/export') {
        if (req.headers['x-occulite-confirm'] !== DK_CONFIRMED) return sendJSON(res, {error: 'confirm-required', message: 'the key sheet asks for the password every time'}, 403);
        const keys = Object.entries(st.keys).map(([sgtin, key]) => {
            const d = DK_PAIRED.find((x) => x.address === sgtin.slice(10));
            return {sgtin, key, payload: `EQ01SG${sgtin}DLK${key}`, address: sgtin.slice(10), ...(d ? {type: d.type, name: d.name} : {})};
        });
        return sendJSON(res, {keys});
    }
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'POST' && path === '') {
            const k = dkParse(b);
            if (k.error) return sendJSON(res, {error: 'invalid-code', message: k.error}, 422);
            const same = st.keys[k.sgtin] === k.key;
            const replaced = !!st.keys[k.sgtin] && !same;
            st.keys[k.sgtin] = k.key;
            if (!same) st.pending.add(k.sgtin);
            return sendJSON(res, {sgtin: k.sgtin, address: k.sgtin.slice(10), paired: DK_PAIRED.some((d) => d.address === k.sgtin.slice(10)), ...(replaced ? {replaced} : {}), ...(same ? {same} : {})});
        }
        if (req.method === 'POST' && path === '/apply') {
            st.applying = 3;
            return sendJSON(res, dkView(st), 202);
        }
        const del = /^\/([^/]+)$/.exec(path);
        if (req.method === 'DELETE' && del) {
            const sg = decodeURIComponent(del[1]).replace(/-/g, '').toUpperCase();
            if (!st.keys[sg]) return sendJSON(res, {error: 'not-found', message: 'no key is stored for ' + sg}, 404);
            delete st.keys[sg];
            st.pending.add(sg);
            return sendJSON(res, dkView(st));
        }
        sendJSON(res, {error: 'not-found', message: 'no such route'}, 404);
    });
}

// task 89: Wi-Fi - one state per test (the cookie stub-wifi), the onboard chip off to begin with
const wifiStates = new Map();
function wifiBase() {
    return {settings: {enabled: false, iface: 'wlan0', country: 'DE', mode: 'dhcp', preferred: 'eth'}, networks: [], connected: '', confirm: null};
}
function wifiStateOf(jar) {
    const id = jar['stub-wifi'] ?? '';
    if (!id) return wifiBase();
    if (!wifiStates.has(id)) wifiStates.set(id, wifiBase());
    return wifiStates.get(id);
}
function wifiView(st) {
    const on = st.settings.enabled;
    const connected = on && st.connected !== '';
    const state = !on ? 'off' : st.networks.length === 0 ? 'not-configured' : connected ? 'connected' : 'connecting';
    const v = {chips: [{iface: 'wlan0', kind: 'onboard', present: on}], settings: st.settings, state, addresses: connected ? ['192.168.178.57/24'] : [], networks: st.networks.map((n) => ({...n}))};
    if (on) v.status = connected ? {state: 'COMPLETED', ssid: st.connected, bssid: '11:22:33:44:55:66', freq: 5240, key_mgmt: 'WPA2-PSK', ip: '192.168.178.57'} : {state: 'SCANNING'};
    if (connected) v.signal = {rssi: -58, link_speed: 144, freq: 5240};
    if (st.confirm) v.confirm = st.confirm;
    return v;
}
const STUB_SCAN = [
    {ssid: 'FRITZ!Box 7590', bssid: '11:22:33:44:55:66', freq: 5240, signal: -52, security: 'wpa2-wpa3'},
    {ssid: 'Nachbar', bssid: '11:22:33:44:55:67', freq: 2437, signal: -71, security: 'wpa2'},
    {ssid: 'Cafe', bssid: '11:22:33:44:55:68', freq: 2412, signal: -80, security: 'open'},
    {ssid: 'Firma', bssid: '11:22:33:44:55:69', freq: 2462, signal: -66, security: 'enterprise'},
];
function wifiRoute(req, u, res) {
    const st = wifiStateOf(cookieJar(req));
    const path = u.pathname.replace('/api/system/v1/wifi', '');
    if (req.method === 'GET' && path === '') return sendJSON(res, wifiView(st));
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'PUT' && path === '') {
            if (!/^[A-Z]{2}$/.test(b.country ?? '')) return sendJSON(res, {error: 'invalid', message: 'invalid Wi-Fi request: country: two capital letters (ISO 3166)'}, 422);
            st.settings = {...st.settings, ...b};
            if (!st.settings.enabled) st.connected = '';
            else if (st.networks.length) st.connected = st.networks[st.networks.length - 1].ssid;
            return sendJSON(res, wifiView(st));
        }
        if (req.method === 'POST' && path === '/scan') {
            if (!st.settings.enabled) return sendJSON(res, {error: 'invalid', message: 'invalid Wi-Fi request: Wi-Fi is switched off'}, 422);
            return sendJSON(res, {networks: STUB_SCAN, saved: st.networks.map((n) => n.ssid)});
        }
        if (req.method === 'POST' && path === '/networks') {
            if (b.security !== 'open' && (b.password ?? '').length < 8) return sendJSON(res, {error: 'invalid', message: 'invalid Wi-Fi request: password: 8 to 63 characters (or 64 hex digits)'}, 422);
            st.networks = st.networks.filter((n) => n.ssid !== b.ssid);
            st.networks.push({ssid: b.ssid, security: b.security, hidden: !!b.hidden, priority: st.networks.length});
            st.settings.enabled = true;
            st.connected = b.ssid;
            return sendJSON(res, wifiView(st));
        }
        if (req.method === 'DELETE' && path.startsWith('/networks/')) {
            const ssid = decodeURIComponent(path.slice('/networks/'.length));
            st.networks = st.networks.filter((n) => n.ssid !== ssid);
            if (st.connected === ssid) st.connected = '';
            return sendJSON(res, wifiView(st));
        }
        if (req.method === 'POST' && path === '/confirm') {
            const had = !!st.confirm;
            st.confirm = null;
            return sendJSON(res, {confirmed: had});
        }
        sendJSON(res, {error: 'not-found', message: 'no such route'}, 404);
    });
}

function fwRoute(req, u, res) {
    const jar = cookieJar(req);
    const st = fwStateOf(jar);
    const path = u.pathname.replace('/api/system/v1/firewall', '');
    if (req.method === 'GET' && path === '') return sendJSON(res, fwView(st));
    if (req.method === 'GET' && path === '/listeners') return sendJSON(res, {listeners: fwListeners(st), policy: st.config.policy});
    if (req.method === 'GET' && path === '/addons') return sendJSON(res, st.addons);
    // task 167: counters - the port's value as packets, ten bytes each; zero after a reset
    const counts = () => {
        const rules = {};
        for (const r of st.config.rules) if (!r.disabled) rules[r.id] = {packets: st.zeroed ? 0 : r.port, bytes: st.zeroed ? 0 : r.port * 10};
        return {at: new Date().toISOString(), since: st.since ?? now, known: true, rules, policy: {ipv4: {packets: st.zeroed ? 0 : 42, bytes: 4200}, ipv6: {packets: st.zeroed ? 0 : 7, bytes: 700}}};
    };
    if (req.method === 'GET' && path === '/counters') return sendJSON(res, counts());
    if (req.method === 'POST' && path === '/counters/reset') {
        st.zeroed = true;
        st.since = new Date().toISOString();
        return sendJSON(res, counts());
    }
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        const busy = () => sendJSON(res, {error: 'pending', message: 'a firewall change is waiting for its confirmation'}, 409);
        const ports = /^\/addons\/([^/]+)\/ports$/.exec(path);
        if (req.method === 'PUT' && ports) {
            const open = new Set(b.open ?? []);
            const id = decodeURIComponent(ports[1]);
            const a = st.addons.find((x) => x.id === id);
            if (!a) return sendJSON(res, {error: 'invalid', message: `addon "${id}" has no policy`}, 422);
            for (const p of a.ports) {
                if (open.has(p.port) && !p.open) st.config.rules.push({id: Math.random().toString(16).slice(2, 10), port: p.port, proto: 'tcp', source: '0/0', family: 'both', target: 'ACCEPT', owner: 'addon:' + id});
                if (!open.has(p.port) && p.open) st.config.rules = st.config.rules.filter((r) => !(r.owner === 'addon:' + id && r.port === p.port));
                p.open = open.has(p.port);
            }
            st.owners['addon:' + id] = 'addon: ' + id;
            return sendJSON(res, st.addons);
        }
        if (req.method === 'PUT' && path === '') {
            if (st.pending) return busy();
            for (const r of b.rules ?? []) {
                if (!(r.port >= 0 && r.port <= 65535) || (r.proto === 'igmp' && (r.port || r.sport))) return sendJSON(res, {error: 'invalid', message: `port ${r.port}: 1 to 65535, or empty for every port`}, 422);
                if (!r.id) r.id = Math.random().toString(16).slice(2, 10);
            }
            st.draft = {...structuredClone(st.config), policy: b.policy, rules: b.rules};
            return sendJSON(res, fwView(st));
        }
        if (req.method === 'DELETE' && path === '/draft') {
            if (st.pending) return busy();
            st.draft = null;
            return sendJSON(res, fwView(st));
        }
        if (req.method === 'POST' && path === '/apply') {
            if (st.pending) return busy();
            if (!st.draft) return sendJSON(res, {error: 'invalid', message: 'there is no draft to apply'}, 422);
            const t0 = Date.now();
            st.pending = {started: new Date(t0).toISOString(), deadline: new Date(t0 + 60000).toISOString()};
            return sendJSON(res, fwView(st));
        }
        if (req.method === 'POST' && path === '/confirm') {
            if (!st.pending) return sendJSON(res, {error: 'invalid', message: 'no firewall change is waiting for its confirmation'}, 422);
            st.config = st.draft;
            st.draft = null;
            st.pending = null;
            return sendJSON(res, fwView(st));
        }
        if (req.method === 'POST' && path === '/revert') {
            st.pending = null;
            return sendJSON(res, fwView(st));
        }
        if (req.method === 'POST' && path === '/migration/dismiss') {
            if (st.config.migration) st.config.migration.dismissed = true;
            return sendJSON(res, fwView(st));
        }
        return sendJSON(res, {error: 'not-found', message: path}, 404);
    });
}

// task 149: local key mode per browser (stub-lk=<id>); stub-lk-empty=1 is a box with no HmIP device
// paired (the welcome step), stub-lk-wrong=1 an entered key the devices do not answer to
const LK_SGTIN = '3014F711A000040000000A02';
const lkStates = new Map();
function lkStateOf(jar) {
    const id = jar['stub-lk'] ?? '';
    // task 212: `stub-lk-fresh=1` - a fresh start moved the previous module's identity aside, and
    // that module (LK_SGTIN) is in use again; `stub-lk-fresh=other` - it is another module's
    if (!lkStates.has(id)) lkStates.set(id, {enabled: false, source: undefined, keyserver_mode: 'KEYSERVER_LOCAL', override_active: false, check: undefined,
        snapshots: jar['stub-lk-fresh'] ? [{sgtin: jar['stub-lk-fresh'] === 'other' ? '3014F711A0001F0000000A04' : LK_SGTIN, at: '2026-09-23T10:00:00Z', files: ['x.ap', 'x.apkx', 'x.bbkx'], kind: 'fresh-start'}] : []});
    return lkStates.get(id);
}
function lkView(jar, st) {
    const snap = st.snapshots.find((x) => x.sgtin === LK_SGTIN);
    const revert = !st.enabled ? 'local key mode is off' : snap ? undefined : 'there is no snapshot of this module from before the switch (the key was set by hand)';
    return {available: true, sgtin: LK_SGTIN, hostname: 'openccu', enabled: st.enabled, source: st.enabled ? st.source : undefined, keyserver_mode: st.keyserver_mode, exchange_id: false, snapshots: st.snapshots, revert_blocked: revert, override_active: st.override_active, check: st.check};
}
function lkRoute(req, u, res) {
    const jar = cookieJar(req);
    const st = lkStateOf(jar);
    if (req.method === 'GET') {
        const v = lkView(jar, st);
        if (u.searchParams.get('devices') === '1') v.devices = jar['stub-lk-empty'] === '1' ? 0 : 3;
        if (st.switching) {
            v.switching = st.switching;
            delete st.switching;
        }
        return sendJSON(res, v);
    }
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        const shared = !jar['stub-lk'];
        const fail = (message) => sendJSON(res, {error: 'local-key', message}, 409);
        const snapDel = /\/snapshots\/([^/]+)$/.exec(u.pathname);
        if (req.method === 'DELETE' && snapDel) {
            st.snapshots = st.snapshots.filter((x) => x.sgtin !== decodeURIComponent(snapDel[1]));
            return sendJSON(res, lkView(jar, st));
        }
        // task 212: the restore of a fresh-start snapshot - the fresh start's confirmation, the
        // service's refusals, then the snapshot is consumed and HmIP-RF restarts
        const snapRestore = /\/snapshots\/([^/]+)\/restore$/.exec(u.pathname);
        if (req.method === 'POST' && snapRestore) {
            const sgtin = decodeURIComponent(snapRestore[1]);
            if (!b.confirm) return sendJSON(res, {error: 'confirm', message: 'confirm: true is required'}, 400);
            if ((b.hostname ?? '').trim().toLowerCase() !== 'openccu') return sendJSON(res, {error: 'hostname', message: 'the host name typed does not match this system\'s'}, 400);
            const snap = st.snapshots.find((x) => x.sgtin === sgtin);
            if (!snap) return fail(`no snapshot of ${sgtin}`);
            if (snap.kind !== 'fresh-start') return fail(`the snapshot of ${sgtin} is from a switch to local key mode; "Back to eQ-3's key server" restores it`);
            if (st.enabled) return fail('local key mode is on: "Back to eQ-3\'s key server" is the way back to a kept identity');
            if (sgtin !== LK_SGTIN) return fail(`the module ${sgtin} is not in use (${LK_SGTIN} is); put it back first`);
            st.snapshots = st.snapshots.filter((x) => x.sgtin !== sgtin);
            st.switching = 'restore'; // the page's next status read still sees the work running (one read)
            return sendJSON(res, {...lkView(jar, st), switching: 'restore'}, 202);
        }
        if (u.pathname.endsWith('/override')) {
            if (!st.enabled) return fail('local key mode is off');
            st.override_active = !!b.on;
            st.keyserver_mode = b.on ? 'KEYSERVER_LOCAL' : 'LOCAL';
            return sendJSON(res, lkView(jar, st), 202);
        }
        // openccu-lite task 317 (D-120): confirm, and the host name typed for a generated key and
        // for the way back
        const host = 'openccu';
        const unconfirmed = (typed) => {
            if (!b.confirm) return sendJSON(res, {error: 'confirm', message: 'confirm: true is required'}, 400), true;
            if (typed && (b.hostname ?? '').trim().toLowerCase() !== host) return sendJSON(res, {error: 'hostname', message: 'the host name typed does not match this system\'s'}, 400), true;
            return false;
        };
        if (req.method === 'PUT') {
            if (unconfirmed(b.mode === 'generate' && jar['stub-lk-empty'] !== '1')) return;
            if (st.enabled) return fail('local key mode is on already');
            if (b.mode === 'known' && !/^[0-9A-Fa-f]{32}$/.test((b.network_key ?? '').replace(/[\s:-]/g, ''))) return fail('network key: a key is 32 hexadecimal digits (16 bytes), and not the example key 0102…0F10');
            const now = new Date().toISOString();
            const next = {...st, enabled: true, source: b.mode === 'known' ? 'entered' : 'generated', keyserver_mode: 'LOCAL', override_active: false,
                snapshots: st.snapshots.some((x) => x.sgtin === LK_SGTIN) ? st.snapshots : [{sgtin: LK_SGTIN, at: now, files: [`${LK_SGTIN}.ap`, `${LK_SGTIN}.apkx`, `${LK_SGTIN}.bbkx`]}, ...st.snapshots],
                check: jar['stub-lk-empty'] === '1' ? {started: now, finished: now, state: 'skipped', before: 0, total: 0, heard: 0, unreachable: [], quiet: []}
                    : jar['stub-lk-wrong'] === '1' ? {started: now, finished: now, state: 'failed', before: 3, total: 3, heard: 0, unreachable: ['000A1B2C3D4E5F'], quiet: ['000B1B2C3D4E5F', '000C1B2C3D4E5F']}
                    : {started: now, finished: now, state: 'ok', before: 3, total: 3, heard: 3, unreachable: [], quiet: []}};
            if (!shared) Object.assign(st, next);
            return sendJSON(res, lkView(jar, next), 202);
        }
        if (req.method === 'DELETE') {
            if (unconfirmed(true)) return;
            if (!st.enabled || !st.snapshots.some((x) => x.sgtin === LK_SGTIN)) return fail('there is no snapshot of this module from before the switch (the key was set by hand)');
            Object.assign(st, {enabled: false, source: undefined, keyserver_mode: 'KEYSERVER_LOCAL', override_active: false, check: undefined});
            return sendJSON(res, lkView(jar, st), 202);
        }
        return fail('unknown');
    });
}

const connLkDiscarded = new Set();
function connRoute(req, u, res) {
    const jar = cookieJar(req);
    if (req.method === 'GET') return sendJSON(res, connStatus(jar));
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
        const b = JSON.parse(body || '{}');
        const ch = {hmip: b.hmip === 'auto' ? '' : (b.hmip ?? ''), bidcos: b.bidcos === 'auto' ? '' : (b.bidcos ?? ''), hmip_path: b.hmip_path === 'auto' ? '' : (b.hmip_path ?? '')};
        const conflict = connConflict(ch);
        if (conflict) return sendJSON(res, {error: 'invalid', message: conflict}, 422);
        const cur = connStatus(jar).choices;
        const lost = ch.bidcos === 'none' && cur.bidcos !== 'none';
        const devices = [{address: 'JEQ9000001', type: 'HM-CC-TC'}, {address: 'KEQ9000003', type: 'HM-LC-Sw1-FM'}];
        // openccu-lite B-285: HmIP-RF moves when the new plan puts it on another module; task 317
        // (D-120): the daemon refuses such a change without confirm, as it does BidCos-RF's loss
        // task 318: from the held module when it is missing (connStatus's plan names it)
        const curPlan = connStatus(jar).plan;
        const from = curPlan.hmip ?? (curPlan.hmip_pin ? {sgtin: curPlan.hmip_pin} : null), to = connPlan(ch).hmip;
        // openccu-lite B-301: stub-conn-lksnap=1 - a local-key snapshot of the module left is kept
        // until the page discards it (DELETE …/local-key/snapshots/…, per stub-conn cookie)
        const blocked = jar['stub-conn-lksnap'] === '1' && !connLkDiscarded.has(jar['stub-conn']);
        const move = from && to && from.sgtin !== to.sgtin ? {from: from.sgtin, to: to.sgtin, local_key: false, snapshot: true, ...(blocked ? {snapshot_blocked: true} : {}), to_version: to.version} : null;
        if (u.pathname.endsWith('/preview')) {
            const changed = ch.hmip !== cur.hmip || ch.bidcos !== cur.bidcos || ch.hmip_path !== (cur.hmip_path ?? '');
            return sendJSON(res, {choices: ch, plan: connPlan(ch), changed, restarts: changed ? ['multimacd', 'rfd', 'hmipserver'] : [], bidcos_lost: lost, devices: lost ? devices : [], ...(move ? {hmip_move: move} : {})});
        }
        if (lost && !b.confirm) return sendJSON(res, {error: 'confirm-required', message: `${devices.length} paired BidCos-RF devices lose the local radio; confirm the change`, devices}, 409);
        if (move?.snapshot_blocked) return sendJSON(res, {error: 'snapshot-blocked', message: `a snapshot of ${move.from} from before local key mode was switched off is kept; discard it, then move HmIP-RF`, detail: {sgtin: move.from}}, 409);
        if (move && !b.confirm) return sendJSON(res, {error: 'confirm-required', message: `HmIP-RF moves from module ${move.from} to module ${move.to} and its identity files are rewritten; confirm the change`, devices: [], hmip_move: move}, 409);
        const t0 = new Date().toISOString();
        const last = {choices: ch, previous: cur, started: t0, finished: t0, ok: true, lines: ['12:00:00 choices written', '12:00:01 hmipserver stopped', '12:00:02 re-running the radio detection and the plan', '12:00:20 now: ' + (ch.bidcos === 'none' ? 'multimacd off, rfd on, hmipserver on /dev/raw-uart' : 'multimacd on /dev/raw-uart, rfd on /dev/mmd_bidcos, hmipserver on /dev/mmd_hmip')]};
        if (jar['stub-conn']) connState.set(jar['stub-conn'], {choices: ch, last});
        res.writeHead(202, {'Content-Type': 'application/json'});
        res.end(JSON.stringify({...connStatus(jar), ...(jar['stub-conn'] ? {} : {choices: ch, last})}));
    });
}

// B-4: the upload install jobs
const installJobs = new Map();
let installJobSeq = 0;

function sendJSON(res, body, status = 200) {
    res.writeHead(status, {'Content-Type': 'application/json'});
    res.end(JSON.stringify(body));
}
// task 56: the install runs of one browser (stub-session=<id>): the versions its finished runs
// installed, and the run in flight, which moves on a phase with every poll of /catalog/progress
const sessions = new Map();
// task 59: the shell preferences of one browser (stub-prefs=<id>)
const prefsStore = new Map();
function sessionOf(jar) {
    const id = jar['stub-session'];
    if (!id) return null;
    if (!sessions.has(id)) sessions.set(id, {versions: {}, run: null, target: '', polls: 0});
    return sessions.get(id);
}
function catalogFor(s) {
    const c = structuredClone(routes['GET /api/system/v1/catalog']);
    if (s) Object.assign(c.installed, s.versions);
    // update_available as the box sets it: installed, and the newest release is another version
    for (const e of c.catalog.addons) {
        if (e.id && c.installed[e.id] && e.latest && c.installed[e.id] !== e.latest.version) e.update_available = true;
        else delete e.update_available;
    }
    return c;
}
// task 53: the addons behind two of the Status warnings (stub-warn=1)
const WARN_ADDONS = [
    {id: 'hm-print', name: 'Print', version: '1.2', operations: ['uninstall'], running: false, enabled: false, rega_dependent: true, rega_reason: 'calls the ReGa script interface'},
    // B-69: a ReGa addon switched on again by hand - the warning says so in a sentence of its own
    {id: 'email', name: 'E-Mail', version: '1.7.5', operations: ['uninstall'], running: true, enabled: true, rega_dependent: true, rega_reason: 'the E-Mail addon is driven by ReGa scripts and system variables'},
    {id: 'cuxd', name: 'CUxD', version: '2.11', operations: ['uninstall'], running: false, enabled: false, binary_incompatible: true, binary_reason: 'ELF for ARM, this box is x86_64'},
];
// the answer of an addon's own Update: check, the CCU way
const OWN_CHECK = {
    hm2mqtt: {installed: '3.6.0-beta', available: '3.6.1', update_available: true, url: 'https://github.com/hobbyquaker/hm2mqtt.js/releases/tag/v3.6.1'},
};
// task 81: the Status page's warnings as occulited evaluates them, planted by the cookies the
// pages' own answers follow (stub-warn, stub-storage) plus stub-key=default (the default security
// key) and stub-ownership=1 (B-92: root-owned files of hm2mqtt). The silences and the fixes live per
// browser (stub-warnings=<id>), so the three projects never see each other's; the period and the
// clearing rule are applied as occulited applies them.
const UNCLEAN_AT = Date.now() - 3 * 3600 * 1000;
const WARNING_ORDER = ['rega', 'arch', 'meta', 'unclean', 'backup-target', 'backup-userfs', 'journal-target', 'journal-sync', 'certificate', 'storage', 'addon-ownership', 'security-key', 'classic-rpc-open', 'hmip-local-key', 'hmip-key-declined', 'legacy-session', 'console-reset'];

// task 125: the legacy session's switches per browser (stub-legacy=<id>; the default state is shared
// and left as it is), the alias the shell asks for, and which addon gets it: one without the header
// (redmatic reads it only with stub-session-header=1) while the switches say so. The warning that
// names them is planted only for a browser with its own state, so the other warning specs see what
// they saw.
const LEGACY_SID = 'LeGaCy0001';
const legacyStates = new Map();
function legacyStateOf(jar) {
    const id = jar['stub-legacy'] ?? '';
    if (!legacyStates.has(id)) legacyStates.set(id, {enabled: true, off: []});
    return legacyStates.get(id);
}
function headerAddon(jar, id) {
    return id === 'redmatic' && jar['stub-session-header'] === '1';
}
function legacyFor(jar, id) {
    const st = legacyStateOf(jar);
    return st.enabled && !st.off.includes(id) && !headerAddon(jar, id);
}
function withLegacyAddons(jar, list) {
    return list.map((a) => (a.config_url && legacyFor(jar, a.id) ? {...a, legacy_session: true} : a));
}
// task 119: the early start's switches per browser (stub-early=<id>, as stub-legacy above), and
// the addon that declares runtime.start "early" - Homematic-Manager - marked in the addon list only
// for such a browser, so the other specs see the list they saw.
const EARLY_ADDONS = ['mh'];
const earlyStates = new Map();
function earlyStateOf(jar) {
    const id = jar['stub-early'] ?? '';
    if (!earlyStates.has(id)) earlyStates.set(id, {enabled: true, off: []});
    return earlyStates.get(id);
}
function withEarlyAddons(jar, list) {
    if (jar['stub-early'] === undefined) return list;
    const st = earlyStateOf(jar);
    return list.map((a) => (EARLY_ADDONS.includes(a.id) ? {...a, start_early_declared: true, ...(st.enabled && !st.off.includes(a.id) ? {start_early: true} : {})} : a));
}
// occulited task 24: the addon whose manifest declares ui.fullscreen - Homematic-Manager - marked in
// the addon list only for a browser with the cookie stub-addon-fullscreen=1, so the other specs see
// the Settings page they saw (no Addons section there without a declaring addon).
const FULLSCREEN_ADDONS = ['mh'];
function withFullscreenAddons(jar, list) {
    if (jar['stub-addon-fullscreen'] !== '1') return list;
    return list.map((a) => (FULLSCREEN_ADDONS.includes(a.id) ? {...a, fullscreen: true} : a));
}
// B-133: a frontend the addon's own server answers behind lighttpd's proxy (FRONTENDS) never gets
// the alias - the box marks only a nav.d page under /addons/ that lives by the CCU convention
function withLegacyNav(jar, entries) {
    return entries.map((e) => (e.addon && !Object.values(FRONTENDS).includes(e.href) && legacyFor(jar, e.addon) ? {...e, legacy_session: true} : e));
}
function legacyWarning(jar) {
    if (jar['stub-legacy'] === undefined) return null;
    const addons = ADDONS.filter((a) => a.config_url && legacyFor(jar, a.id)).map((a) => ({id: a.id, name: a.name, enabled: a.enabled})).sort((a, b) => a.id.localeCompare(b.id));
    if (addons.length === 0) return null;
    return {id: 'legacy-session', variant: addons.map((a) => a.id).join(','), severity: 'warning', href: '/addons', params: {addons}};
}
const warnStates = new Map();
function warnStateOf(jar) {
    const id = jar['stub-warnings'] ?? '';
    if (!warnStates.has(id)) warnStates.set(id, {silences: [], fixed: new Set()});
    return warnStates.get(id);
}
function activeWarnings(jar, warn, st) {
    const list = [];
    const listed = (id, href, pred) => {
        const addons = WARN_ADDONS.filter(pred).map((a) => ({id: a.id, name: a.name, enabled: a.enabled})).sort((a, b) => a.id.localeCompare(b.id));
        return {id, variant: addons.map((a) => a.id).join(','), severity: 'error', href, params: {addons}};
    };
    const at = Math.floor(UNCLEAN_AT / 1000);
    if (warn) {
        list.push(listed('rega', '/addons', (a) => a.rega_dependent));
        list.push(listed('arch', '/catalog', (a) => !a.rega_dependent && a.binary_incompatible));
        list.push({id: 'meta', variant: '1757660000', severity: 'error', href: '/app'});
        // the previous boot's log where the journal holds it; in RAM (stub-journal-ram=1) an hour before the marker
        const href = jar['stub-journal-ram'] === '1' ? `/log?since=@${at - 3600}` : '/log?boot=-1';
        list.push({id: 'unclean', variant: String(at), severity: 'error', href, params: {at: new Date(at * 1000).toISOString()}});
        list.push({id: 'backup-target', variant: '/media/usb0/backup', severity: 'error', href: '/backup', params: {path: '/media/usb0/backup'}});
        list.push({id: 'certificate', variant: 'expiring', severity: 'error', href: '/certificate', params: {days: 9}});
    } else {
        // the stub's default schedule is the backup on the box itself
        list.push({id: 'backup-userfs', variant: '/media/usb0/backup', severity: 'error', href: '/backup', params: {path: '/media/usb0/backup'}});
    }
    // openccu-lite task 231: a server occulited's store could not verify (stub-trust-pending=1)
    if (trustStateOf(jar).pending) {
        list.push({id: 'trust-ca', variant: 'api.github.com', severity: 'error', href: '/system/trust#occulited', params: {store: 'occulited', hosts: ['api.github.com'], issuer: 'CN=DigiCert Global Root G2,O=DigiCert Inc', candidate: true}});
    }
    // openccu-lite task 232: a connection's certificate matched none of its purpose's pins (stub-trust-pin-failure=1)
    for (const f of trustStateOf(jar).pinFailures) {
        list.push({id: 'trust-pin', variant: `${f.purpose}:${f.host}`, severity: 'error', href: `/system/trust#${f.purpose}`, params: {purpose: f.purpose, host: f.host, subject: f.chain[0].subject, fingerprint: f.chain[0].fingerprint, spki: f.chain[0].spki}});
    }
    const rep = STORAGE[jar['stub-storage'] || (warn ? 'watch' : 'good')] ?? STORAGE.good;
    if (rep.verdict !== 'good') {
        list.push({id: 'storage', variant: rep.verdict, severity: rep.verdict === 'replace' ? 'error' : 'warning', href: '#storage', params: {verdict: rep.verdict, reasons: rep.reasons, devices: rep.devices.map((d) => ({name: d.name, kind: d.kind, model: d.model}))}});
    }
    if ((jar['stub-ownership'] === '1' || jar['stub-ownership'] === 'failed') && !st.fixed.has('hm2mqtt')) {
        list.push({id: 'addon-ownership', variant: 'hm2mqtt', severity: 'warning', href: '/services', params: {addons: [{id: 'hm2mqtt', enabled: true, path: '/usr/local/addons/hm2mqtt/var/hm2mqtt.pid'}]}});
    }
    // openccu-lite task 318: the module holding the HmIP network is missing (stub-conn-held)
    if (jar['stub-conn-held']) list.push({id: 'hmip-module-missing', variant: jar['stub-conn-held'], severity: 'error', href: '/system/interfaces#connections', params: {module: jar['stub-conn-held']}});
    if (jar['stub-key'] === 'default') list.push({id: 'security-key', variant: 'default', severity: 'warning', href: '/system/keys#security-key'});
    // openccu-lite task 299: the HmIP security counter (stub-counter=near|wrapped|backwards|held)
    if (jar['stub-counter'] === 'held') {
        list.push({id: 'hmip-clock-hold', variant: 'timeout', severity: 'error', href: '/system/network', params: {sgtin: '3014F711A000040000000A02', since: '2026-09-30T09:10:00Z', reason: 'the computed security counter has passed 2^32', clock_state: 'timeout', calc: 5319842009}});
        list.push({id: 'hmip-security-counter', variant: 'wrapped', severity: 'warning', href: '/system/interfaces#connections', params: {sgtin: '3014F711A000040000000A02', calc: 5319842009, at: '2026-09-30T09:10:00Z', source: 'computed', offset: 4031374848, wraps_at: '2026-10-01T00:00:00Z'}});
    } else if (jar['stub-counter']) {
        const v = jar['stub-counter'];
        list.push({id: 'hmip-security-counter', variant: v, severity: v === 'backwards' ? 'error' : 'warning', href: '/system/interfaces#connections', params: {sgtin: '3014F711A000040000000A02', calc: v === 'near' ? 2200000000 : 5319842009, current: 1024874459, written: v === 'near' ? 2200000000 : 1024874713, at: '2026-09-30T09:00:00Z', source: 'journal', offset: 4031374848, wraps_at: '2033-04-01T00:00:00Z'}});
    }
    // task 173: classic RPC on without a login (stub-rpc=plain|tls|plain,tls)
    if (jar['stub-rpc']) list.push({id: 'classic-rpc-open', variant: jar['stub-rpc'], severity: 'warning', href: '/system/remote-access'});
    // task 193: the Control app public (stub-public-warn=1)
    if (jar['stub-public-warn'] === '1') list.push({id: 'app-public', variant: 'guest', severity: 'warning', href: '/settings#public', params: {account: 'guest'}});
    const legacy = legacyWarning(jar);
    if (legacy) list.push(legacy);
    // task 149: the check after a switch to local key mode found the devices silent (stub-lk-wrong=1)
    const lk = lkStates.get(jar['stub-lk']);
    if (lk?.enabled && lk.check?.state === 'failed') {
        list.push({id: 'hmip-local-key', variant: lk.check.started, severity: 'error', href: '/system/keys#local-key', params: {unreachable: lk.check.total - lk.check.heard, total: lk.check.total}});
    }
    // task 201: hmipserver declined a pairing because the device's key in the map is wrong
    // (stub-declined=<sgtin>)
    if (jar['stub-declined']) {
        const sgtin = jar['stub-declined'];
        list.push({id: 'hmip-key-declined', variant: `${sgtin}@2026-09-22T19:30:00Z`, severity: 'warning', href: '/system/keys#device-keys', params: {sgtin, address: sgtin.slice(10), at: '2026-09-22T19:30:00Z'}});
    }
    // occulited task 14: `occulited admin reset-auth` reset an account on the console
    // (stub-console-reset=<user>)
    if (jar['stub-console-reset']) {
        const user = jar['stub-console-reset'];
        list.push({id: 'console-reset', variant: `${user}@1790000000`, severity: 'warning', href: '/system/users', params: {accounts: [{user, at: '2026-09-21T14:13:20Z'}]}});
    }
    // task 85: stub-journal=fallback (ram-sync could not be set up at boot) or copy-failed
    if (jar['stub-journal'] === 'fallback') {
        list.push({id: 'journal-target', variant: 'ram-sync', severity: 'warning', href: '/log?settings=journal', params: {mode: 'ram-sync', reason: 'the userfs target cannot be mounted, the journal stays in RAM (STORAGE=ram-sync)', path: '/usr/local/var/log/journal'}});
    }
    if (jar['stub-journal'] === 'copy-failed') {
        list.push({id: 'journal-sync', variant: 'failed', severity: 'warning', href: '/log?settings=journal', params: {at: new Date(UNCLEAN_AT).toISOString(), reason: 'copying system@x.journal to /usr/local/var/log/journal failed (the userfs full or read-only?)'}});
    }
    const rank = (w) => (w.severity === 'error' ? 0 : 1);
    return list.sort((a, b) => rank(a) - rank(b) || WARNING_ORDER.indexOf(a.id) - WARNING_ORDER.indexOf(b.id));
}
function warningsView(jar, warn, st) {
    const now = Date.now();
    const active = activeWarnings(jar, warn, st);
    st.silences = st.silences.filter((s) => Date.parse(s.until) > now && active.some((w) => w.id === s.id && w.variant === s.variant));
    const warnings = active.map((w) => {
        const s = st.silences.find((x) => x.id === w.id && x.variant === w.variant);
        return s ? {...w, silenced: s} : w;
    });
    return {warnings, periods: [1, 7, 90]};
}
function warningsRoute(req, u, res, jar, warn) {
    const st = warnStateOf(jar);
    if (req.method === 'GET' && u.pathname === '/api/system/v1/warnings') {
        sendJSON(res, warningsView(jar, warn, st));
        return true;
    }
    const own = /^\/api\/system\/v1\/addons\/([^/]+)\/ownership$/.exec(u.pathname);
    if (req.method === 'POST' && own) {
        const id = decodeURIComponent(own[1]);
        req.resume();
        req.on('end', () => {
            st.fixed.add(id);
            // task 81: stub-ownership=failed has the addon's unit failed, so the fix starts it
            sendJSON(res, {id, root_owned: '', started: jar['stub-ownership'] === 'failed'});
        });
        return true;
    }
    if (req.method === 'POST' && u.pathname === '/api/system/v1/warnings/silence') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            let b = {};
            try {
                b = JSON.parse(body || '{}');
            } catch {
                /* a broken body is refused below */
            }
            if (![1, 7, 90].includes(b.days)) return sendJSON(res, {error: 'invalid-period', message: 'the period must be 1, 7 or 90 days'}, 400);
            if (!warningsView(jar, warn, st).warnings.some((w) => w.id === b.id && w.variant === b.variant)) return sendJSON(res, {error: 'not-active', message: 'no such warning is active'}, 404);
            const at = new Date();
            const s = {id: b.id, variant: b.variant, by: 'admin', at: at.toISOString(), until: new Date(at.getTime() + b.days * 86400 * 1000).toISOString()};
            st.silences = [...st.silences.filter((x) => !(x.id === s.id && x.variant === s.variant)), s];
            sendJSON(res, {...warningsView(jar, warn, st), silence: s});
        });
        return true;
    }
    const del = /^\/api\/system\/v1\/warnings\/silence\/([^/]+)\/(.+)$/.exec(u.pathname);
    if (req.method === 'DELETE' && del) {
        const id = decodeURIComponent(del[1]);
        const variant = decodeURIComponent(del[2]);
        req.resume();
        req.on('end', () => {
            const before = st.silences.length;
            st.silences = st.silences.filter((x) => !(x.id === id && x.variant === variant));
            if (st.silences.length === before) return sendJSON(res, {error: 'not-silenced', message: 'no such silence'}, 404);
            sendJSON(res, warningsView(jar, warn, st));
        });
        return true;
    }
    return false;
}
// B-115: a real box's data (stub-real=1). The stub's own values are short, so every page fitted a
// phone here while four were wider on .119: GitHub's raw 403 body as the release check's error, the
// nightly backups' file names (.119's list of 2026-09-12), a radio module whose file names and German
// labels fill its card, and an addon whose ports carry long German labels.
const REAL_BOX = {
    'GET /api/system/v1/system-update': () => {
        const r = structuredClone(routes['GET /api/system/v1/system-update']);
        r.feed.error = 'feed: HTTP 403 {"message":"API rate limit exceeded for 2003:e5:5f0c:8d00:ba27:ebff:fe4a:1c2d. (But here\'s the good news: Authenticated requests get a higher rate limit. Check out the documentation for more details.)","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}';
        return r;
    },
    // task 86: the targets with a real system's values, and the USB directory's backups as .119 had them
    'GET /api/system/v1/backup/targets': () => {
        const v = structuredClone(routes['GET /api/system/v1/backup/targets']);
        v.hostname = 'ccu-vm-1';
        v.targets[1].name = 'Build host (TrueNAS dataset via SSH)';
        v.targets[1].subdir = 'ccu-vm-1';
        v.targets[1].sftp = {...v.targets[1].sftp, host: 'backup.example.org', path: '/mnt/tank/backups/homematic/openccu-lite-systems'};
        v.targets[2].nfs = {server: 'nas.example.org', export: '/mnt/tank/backups/homematic/openccu-lite', version: '4.2'};
        v.targets[2].subdir = 'ccu-vm-1';
        v.targets[2].state = {...v.targets[2].state, source: 'nas.example.org:/mnt/tank/backups/homematic/openccu-lite'};
        return v;
    },
    'GET /api/system/v1/backup/targets/directory/backups': () => ({backups: REAL_BOX['GET /api/system/v1/backup/schedule']().backups.map((b) => ({...b, encrypted: false}))}),
    'GET /api/system/v1/backup/schedule': () => ({
        enabled: true, path: '/media/usb0/backup', max_backups: 30, path_exists: true, on_userfs: true, real_path: '/usr/local/sdcard/backup',
        backups: [
            ['2026-09-12-0019', 1001420800, '2026-09-12T00:20:37+02:00'], ['2026-09-11-0008', 1001420800, '2026-09-11T00:09:39+02:00'],
            ['2026-09-10-0035', 1001420800, '2026-09-10T00:37:11+02:00'], ['2026-09-09-0027', 1001420800, '2026-09-09T00:29:15+02:00'],
            ['2026-09-08-0020', 1001379840, '2026-09-08T00:21:32+02:00'], ['2026-09-07-0517', 1000263680, '2026-09-07T05:18:51+02:00'],
            ['2026-09-06-0007', 1014312960, '2026-09-06T00:08:44+02:00'], ['2026-09-05-0007', 1014220800, '2026-09-05T00:08:26+02:00'],
            ['2026-09-04-0007', 8396800, '2026-09-04T00:07:02+02:00'], ['2026-09-01-0007', 3481600, '2026-09-01T00:07:01+02:00'],
        ].map(([d, size, time]) => ({name: `openccu-3.89.8.20260719-${d}.sbk`, size, time})).concat(
            // a file copied into the directory by hand: underscores only, which no engine breaks at
            [{name: 'ccu3_backup_before_openccu_lite_20260911_1425.sbk', size: 7802880, time: '2026-09-11T14:25:03+02:00'}],
        ),
    }),
    'GET /api/system/v1/radio/firmware': () => {
        const r = structuredClone(routes['GET /api/system/v1/radio/firmware']);
        // a legacy HM-MOD-RPI-PCB beside the RFUSB: eQ-3's file name has no hyphen, so no engine breaks it
        r.modules.push({
            protocols: ['BidCos-RF'], device: 'HM-MOD-RPI-PCB', device_node: '/dev/mmd_bidcos', device_type: 'eQ-3 HM-MOD-RPI-PCB@platform-3f201000.serial', family: 'legacy', dir: 'HM-MOD-UART',
            running_version: '2.8.6', newest: 'coprocessor_update_hm_only.eq3', verdict: 'up-to-date', flashable: true,
            files: [{name: 'coprocessor_update_hm_only.eq3', path: '/firmware/HM-MOD-UART/coprocessor_update_hm_only.eq3', source: 'shipped', version: '2.8.6', size: 118784, sha256: 'b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c', direction: 'same'}],
        });
        // task 147: an HM-CFG-USB-2 on the old 0.956, no file uploaded yet (the image ships none)
        r.modules.push({protocols: [], device: 'HM-CFG-USB-2', device_node: 'usb:JEQ9000002', device_type: 'USB', family: 'hmcfgusb', dir: 'HM-CFG-USB-2', running_version: '0.956', verdict: 'no-file', flashable: true, files: []});
        return r;
    },
};
// task 75: the service messages as the store answers, or none of them (stub-servicemsg=none)
function serviceMessagesView(jar) {
    const full = routes['GET /api/system/v1/service-messages'];
    if (jar['stub-servicemsg'] === 'many') {
        // occulited task 27: eleven devices, every kind, one with UNREACH and STICKY_UNREACH
        const ago = (min) => new Date(Date.parse(now) - min * 60000).toISOString();
        const msg = (address, key, min, extra = {}) => ({interface: 'HmIP-RF', address, channel: '0', key, value: true, since: ago(min), seen: 'event', type: 'HmIP-SWDO', ...extra});
        const messages = [
            msg('00010000000B01', 'UNREACH', 30, {name: 'Fenster Bad'}), msg('00010000000B01', 'STICKY_UNREACH', 30, {name: 'Fenster Bad'}),
            msg('00010000000B02', 'LOW_BAT', 300, {name: 'Thermostat Küche'}), msg('00010000000B02', 'CONFIG_PENDING', 200, {name: 'Thermostat Küche'}),
            msg('00010000000B03', 'STICKY_UNREACH', 10), msg('00010000000B04', 'UPDATE_PENDING', 50), msg('00010000000B05', 'SABOTAGE', 400),
            msg('00010000000B06', 'LOW_BAT', 20), msg('00010000000B07', 'LOW_BAT', 25), msg('00010000000B08', 'CONFIG_PENDING', 5),
            msg('00010000000B09', 'UNREACH', 60), msg('00010000000B10', 'ERROR_CODE', 70, {value: 3}), msg('00010000000B11', 'UPDATE_PENDING', 80),
        ];
        return {count: messages.length, messages, swept: now, errors: {}, feed: full.feed};
    }
    return jar['stub-servicemsg'] === 'none' ? {count: 0, messages: [], swept: now, errors: {}, feed: full.feed} : full;
}

// openccu-lite task 228: two sticks - a SanDisk with an exFAT partition that carries the journal's
// copies, and a blank one without a filesystem
const USB_DISKS = new Map();
function storageUSBRoute(req, u, res, jar) {
    const id = jar['stub-usb'] ?? '';
    if (!USB_DISKS.has(id)) USB_DISKS.set(id, id === 'none' ? [] : [
        {name: 'sda', vendor: 'SanDisk', model: 'Ultra Fit', serial: '4C530001', size_bytes: 32010928128, removable: true, partitions: [{name: 'sda1', fstype: 'exfat', label: 'LOGSTICK', label_id: 'LOGSTICK', mount: '/media/usb1', size_bytes: 32009879552, total_bytes: 32003000000, free_bytes: 30100000000}], uses: [{kind: 'journal'}, {kind: 'backup', name: 'USB stick', id: 'directory'}]},
        {name: 'sdb', vendor: 'Generic', model: 'Flash Disk', size_bytes: 8053063680, removable: true, partitions: [], uses: []},
    ]);
    const disks = USB_DISKS.get(id);
    const view = () => ({disks, filesystems: ['exfat', 'ext4']});
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'GET') return sendJSON(res, view());
        const m = /^\/api\/system\/v1\/storage\/usb\/([^/]+)\/(format|eject)$/.exec(u.pathname);
        const d = m && disks.find((x) => x.name === m[1]);
        if (!d) return sendJSON(res, {error: 'not_found', message: 'no USB stick'}, 404);
        if (m[2] === 'eject') {
            for (const p of d.partitions) delete p.mount;
            return sendJSON(res, {...view(), ejected: d.name});
        }
        const max = b.fs === 'exfat' ? 15 : 16;
        if (!['exfat', 'ext4'].includes(b.fs) || !/^[A-Za-z0-9_-]+$/.test(b.label ?? '') || b.label.length > max) return sendJSON(res, {error: 'invalid', message: `label: 1 to ${max} letters, digits, - or _`}, 422);
        d.partitions = [{name: d.name + '1', fstype: b.fs, label: b.label, label_id: b.label, mount: '/media/usb2', size_bytes: d.size_bytes - 1048576, total_bytes: d.size_bytes - 2097152, free_bytes: d.size_bytes - 3145728}];
        d.uses = [];
        return sendJSON(res, {...view(), formatted: d.name});
    });
    return true;
}

// openccu-lite task 228, phase 2: the network shares - an SMB share that is mounted and an NFS
// export that is idle; stub-shares=none starts without any, stub-shares=container is a container.
// A server named "down…" is unreachable in the test and the mount.
const SHARES = new Map();
function storageSharesRoute(req, u, res, jar) {
    const id = jar['stub-shares'] ?? '';
    const blank = (s) => ({has_password: false, read_only: false, version: '', where: `/media/net/${s.id}`, state: {state: 'idle', mounted: false}, uses: [], ...s});
    if (!SHARES.has(id)) SHARES.set(id, id === 'none' || id === 'container' ? [] : [
        blank({id: 'nas', kind: 'cifs', server: 'nas.lan', path: 'backup', user: 'ccu', has_password: true, state: {state: 'mounted', mounted: true, source: '//nas.lan/backup', free_bytes: 812000000000, total_bytes: 2000000000000},
            // stub-shares=used: the journal's copies and a backup target on it (phase 3)
            uses: id === 'used' ? [{kind: 'journal'}, {kind: 'backup', name: 'TrueNAS', id: 'tnas0001'}] : []}),
        blank({id: 'media', kind: 'nfs', server: '192.168.1.20', path: '/mnt/tank/media', read_only: true}),
    ]);
    const list = SHARES.get(id);
    const view = () => ({container: id === 'container' ? 'lxc' : '', kinds: id === 'container' ? {nfs: 'container', cifs: 'container'} : {nfs: '', cifs: ''}, shares: list});
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        const m = /^\/api\/system\/v1\/storage\/shares(?:\/([^/]+))?(?:\/(test|mount|unmount))?$/.exec(u.pathname);
        if (!m) return sendJSON(res, {error: 'not_found'}, 404);
        const [, sid, action] = m;
        if (!sid) {
            if (req.method === 'GET') return sendJSON(res, view());
            if (!/^[a-z][a-z0-9]{0,15}$/.test(b.id ?? '')) return sendJSON(res, {error: 'invalid', message: 'invalid: id: a name of letters and digits'}, 400);
            if (list.some((x) => x.id === b.id)) return sendJSON(res, {error: 'exists', message: `the name is taken: a share named ${b.id} exists`}, 409);
            const {password, ...rest} = b;
            const sh = blank({...rest, has_password: !!password});
            list.push(sh);
            return sendJSON(res, {share: sh}, 201);
        }
        const i = list.findIndex((x) => x.id === sid);
        if (i < 0) return sendJSON(res, {error: 'not_found', message: 'no such share'}, 404);
        const sh = list[i];
        if (req.method === 'DELETE') {
            list.splice(i, 1);
            res.writeHead(204);
            return res.end();
        }
        if (req.method === 'PUT') {
            const {password, ...rest} = b;
            list[i] = {...sh, ...rest, has_password: sh.has_password || !!password};
            return sendJSON(res, {share: list[i]});
        }
        const down = sh.server.startsWith('down');
        if (action === 'test') {
            if (down) return sendJSON(res, {ok: false, state: 'unreachable', step: 'mount', error: 'mount error(113): could not connect to ' + sh.server, free_bytes: 0, total_bytes: 0, read_only: sh.read_only});
            if (sh.read_only) return sendJSON(res, {ok: true, state: 'readable', step: 'done', fs_type: 'nfs4', free_bytes: 1e12, total_bytes: 4e12, read_only: true});
            return sendJSON(res, {ok: true, state: 'writable', step: 'done', fs_type: 'cifs', free_bytes: 812000000000, total_bytes: 2000000000000, write_mbps: 38.4});
        }
        if (action === 'mount') {
            sh.state = down ? {state: 'unreachable', mounted: false, detail: 'mount error(113): could not connect to ' + sh.server} : {state: 'mounted', mounted: true, source: sh.kind === 'cifs' ? `//${sh.server}/${sh.path}` : `${sh.server}:${sh.path}`, free_bytes: 1e12, total_bytes: 4e12};
            return sendJSON(res, sh);
        }
        if (action === 'unmount') {
            sh.state = {state: 'idle', mounted: false};
            return sendJSON(res, sh);
        }
        return sendJSON(res, {error: 'not_found'}, 404);
    });
    return true;
}

// openccu-lite task 228, phases 3-4: where a use may keep its files - the system storage, the sticks
// of GET /usb/storage by label, the shares of this browser's stub-shares - with the rules the daemon
// applies (internal/location)
function locationsView(use, jar) {
    const rule = (kind) => ({
        journal: {userfs: {allowed: true, userfs_prefix: 'var/log/journal', fixed: true, default: 'var/log/journal'}, usb: {allowed: true, code: 'ram-sync-only', default: 'journal'}, share: {allowed: true, code: 'ram-sync-only', default: 'journal'}},
        backup: {userfs: {allowed: true, code: 'lost-with-system', userfs_prefix: 'backup', default: 'backup'}, usb: {allowed: true, default: 'backup'}, share: {allowed: true, default: ''}},
        store: {userfs: {allowed: true, userfs_prefix: 'etc/occulite', default: 'etc/occulite/data'}, usb: {allowed: true, code: 'copy-only', reason: 'takes a copy only', default: 'occulited'}, share: {allowed: false, code: 'no-share-for-store', reason: 'the database maps its file into memory and needs file locking and fsync that NFS and SMB do not guarantee'}},
    })[use]?.[kind] ?? {allowed: false};
    const out = [{id: 'userfs', kind: 'userfs', name: 'userfs', detail: '/usr/local', state: 'present', free_bytes: 18_400_000_000, total_bytes: 29_000_000_000, uses: use === 'store' ? [{kind: 'store', folder: 'etc/occulite/data'}] : [], ...rule('userfs')}];
    for (const st of routes['GET /api/system/v1/usb/storage'].sticks) {
        const v = {id: `usb:${st.label_id ?? ''}`, kind: 'usb', name: st.label || `${st.vendor} ${st.model}`, detail: `${st.vendor} ${st.model}`, state: 'present', free_bytes: st.free_bytes, total_bytes: st.total_bytes, read_only: !!st.read_only, uses: st.label_id === 'BACKUP' ? [{kind: 'backup', name: 'USB', id: 'directory', folder: 'backup'}] : [], ...rule('usb')};
        if (v.allowed && st.read_only) Object.assign(v, {allowed: false, code: 'read-only', reason: 'it is mounted read-only'});
        if (!st.label_id) Object.assign(v, {id: 'usb:', allowed: false, code: 'no-label', reason: 'it has no label'});
        out.push(v);
    }
    const shares = SHARES.get(jar['stub-shares'] ?? '') ?? [];
    for (const sh of shares.length || SHARES.has(jar['stub-shares'] ?? '') ? shares : [{id: 'nas', kind: 'cifs', server: 'nas.lan', path: 'backup', read_only: false, state: {state: 'mounted', mounted: true, free_bytes: 812000000000, total_bytes: 2000000000000}}, {id: 'media', kind: 'nfs', server: '192.168.1.20', path: '/mnt/tank/media', read_only: true, state: {state: 'idle', mounted: false}}]) {
        const v = {id: `share:${sh.id}`, kind: 'share', name: sh.id, detail: sh.kind === 'cifs' ? `//${sh.server}/${sh.path}` : `${sh.server}:${sh.path}`, state: sh.state.state, read_only: sh.read_only, uses: sh.id === 'nas' ? [{kind: 'backup', name: 'TrueNAS', id: 'tnas0001', folder: 'openccu'}] : [], ...rule('share')};
        if (sh.state.mounted) Object.assign(v, {free_bytes: sh.state.free_bytes, total_bytes: sh.state.total_bytes});
        if (v.allowed && sh.read_only) Object.assign(v, {allowed: false, code: 'read-only', reason: 'it is mounted read-only'});
        out.push(v);
    }
    return {use, locations: out};
}
// the folders on each location, as GET /storage/dirs completes them
const STUB_DIRS = {
    userfs: ['backup', 'backup/ccu', 'backup/old', 'etc/occulite/data', 'etc/occulite/db2', 'var/log/journal'],
    'usb:BACKUP': ['backup', 'backup/2025', 'journal', 'photos'],
    'share:nas': ['openccu', 'openccu/journal', 'other-system', 'media'],
};
function dirsView(q) {
    const prefix = (q.get('prefix') ?? '').replace(/^\/+/, '');
    const all = STUB_DIRS[q.get('location') ?? ''] ?? [];
    const cut = prefix.lastIndexOf('/');
    const parent = cut < 0 ? '' : prefix.slice(0, cut);
    const partial = cut < 0 ? prefix : prefix.slice(cut + 1);
    const dirs = all.filter((d) => {
        const i = d.lastIndexOf('/');
        return (i < 0 ? '' : d.slice(0, i)) === parent && d.slice(i + 1).toLowerCase().startsWith(partial.toLowerCase());
    });
    return {dirs, exists: all.includes(prefix.replace(/\/+$/, ''))};
}

// openccu-lite task 227: the stub's IPv6 settings and the one pending change
const IPV6 = new Map();
function ipv6Route(req, u, res, jar) {
    const id = jar['stub-v6'] ?? '';
    const window = Number(jar['stub-v6-window'] ?? 60);
    if (!IPV6.has(id)) IPV6.set(id, {interfaces: {eth0: {mode: 'slaac'}, wlan0: {mode: 'slaac'}, eth1: {mode: 'off'}, veth0: {mode: 'slaac'}}, pending: null});
    const st = IPV6.get(id);
    if (st.pending && Date.parse(st.pending.deadline) <= Date.now()) {
        st.interfaces[st.pending.interface] = st.pending.previous;
        st.pending = null;
    }
    const view = () => ({interfaces: st.interfaces, pending: st.pending, window_seconds: window, writable: true});
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'GET') return sendJSON(res, view());
        if (u.pathname.endsWith('/confirm') || u.pathname.endsWith('/revert')) {
            if (!st.pending || st.pending.token !== b.token) return sendJSON(res, {error: 'not_found', message: 'no such pending IPv6 change: it was confirmed, reverted or rolled back already'}, 404);
            if (u.pathname.endsWith('/revert')) st.interfaces[st.pending.interface] = st.pending.previous;
            st.pending = null;
            return sendJSON(res, {...view(), confirmed: u.pathname.endsWith('/confirm')});
        }
        if (st.pending) return sendJSON(res, {error: 'pending', message: 'an IPv6 change is already waiting for confirmation'}, 409);
        const {interface: iface, ...set} = b;
        if (set.mode === 'static' && !/^[0-9a-f:]+$/i.test(set.address ?? '')) return sendJSON(res, {error: 'invalid', message: 'address: not an IPv6 address'}, 422);
        const previous = st.interfaces[iface];
        st.interfaces[iface] = set;
        const now = Date.now();
        st.pending = {token: `v6-${now}`, interface: iface, settings: set, previous, started: new Date(now).toISOString(), deadline: new Date(now + window * 1000).toISOString()};
        return sendJSON(res, {...view(), changed: true});
    });
    return true;
}

// openccu-lite task 230: a provider with a private CA. The stub does not parse certificates: every
// PEM block is one anchor, named by its position; the peer chain is a fixed leaf and its CA.
const OIDC_TRUST = new Map();
const FAKE_PEM = (n) => `-----BEGIN CERTIFICATE-----\nMIIB${n}fake\n-----END CERTIFICATE-----\n`;
// openccu-lite task 232: each with the SHA-256 of its public key (spki), what a pin matches
const SPKI_LEAF = 'LeafKeyHash0000000000000000000000000000000=';
const SPKI_CA = 'LabCAKeyHash000000000000000000000000000000=';
const PEER_CHAIN = [
    {id: 'aaaa000000000001', purposes: [], subject: 'CN=auth.example.org', issuer: 'CN=Lab CA', not_before: '2026-09-01T00:00:00Z', not_after: '2026-12-01T00:00:00Z', fingerprint: 'AA:11:' + '00:'.repeat(29) + '01', spki: SPKI_LEAF, ca: false, self_signed: false, names: ['auth.example.org'], pem: FAKE_PEM('leaf')},
    {id: 'bbbb000000000002', purposes: [], subject: 'CN=Lab CA', issuer: 'CN=Lab CA', not_before: '2026-01-01T00:00:00Z', not_after: '2036-01-01T00:00:00Z', fingerprint: 'BB:22:' + '00:'.repeat(29) + '02', spki: SPKI_CA, ca: true, self_signed: true, pem: FAKE_PEM('ca')},
];
// the ACME directory's chain: a step-ca leaf under its root
const SPKI_ACME = 'StepCALeafKeyHash0000000000000000000000000=';
const ACME_CHAIN = [
    {id: 'cccc000000000003', purposes: [], subject: 'CN=ca.lan', issuer: 'CN=Lab Step CA', not_before: '2026-09-01T00:00:00Z', not_after: '2026-10-20T00:00:00Z', fingerprint: 'CC:33:' + '00:'.repeat(29) + '03', spki: SPKI_ACME, ca: false, self_signed: false, names: ['ca.lan'], pem: FAKE_PEM('acme')},
    {id: 'dddd000000000004', purposes: [], subject: 'CN=Lab Step CA', issuer: 'CN=Lab Step CA', not_before: '2026-01-01T00:00:00Z', not_after: '2036-01-01T00:00:00Z', fingerprint: 'DD:44:' + '00:'.repeat(29) + '04', spki: 'StepCARootKeyHash0000000000000000000000000=', ca: true, self_signed: true, pem: FAKE_PEM('acmeca')},
];
function oidcTrustRoute(req, u, res, jar) {
    const id = jar['stub-trust'] ?? '';
    const list = OIDC_TRUST.get(id) ?? [];
    OIDC_TRUST.set(id, list);
    const view = () => list.map(({pem, ...a}) => a);
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'GET') return sendJSON(res, {anchors: view()});
        if (req.method === 'DELETE') {
            const k = list.findIndex((a) => a.id === decodeURIComponent(u.pathname.split('/').pop()));
            if (k < 0) return sendJSON(res, {error: 'not_found', message: 'no such anchor'}, 404);
            list.splice(k, 1);
            return sendJSON(res, {anchors: view()});
        }
        if (u.pathname.endsWith('/test')) {
            // openccu-lite task 232: a pin only vouches alone; with-ca needs the anchors as well
            const pins = trustStateOf(jar).pins.oidc;
            const match = pins.find((p) => p.spki === PEER_CHAIN[0].spki);
            const {pem: _pem, ...leaf} = PEER_CHAIN[0];
            if (pins.length && !match) return sendJSON(res, {ok: false, tls: true, error: `Get "https://auth.example.org/.well-known/openid-configuration": auth.example.org presents a certificate none of the ${pins.length} pin(s) for oidc match: CN=auth.example.org, public key SHA-256 ${PEER_CHAIN[0].spki}, certificate SHA-256 ${PEER_CHAIN[0].fingerprint}`});
            if (match && match.mode === 'only') return sendJSON(res, {ok: true, tls: true, issuer: 'https://auth.example.org/application/o/openccu-lite/', token_endpoint: 'https://auth.example.org/application/o/token/', leaf, pinned: match, pin_only: true});
            if (!list.length) return sendJSON(res, {ok: false, tls: true, error: 'Get "https://auth.example.org/.well-known/openid-configuration": tls: failed to verify certificate: x509: certificate signed by unknown authority'});
            const root = list[list.length - 1];
            return sendJSON(res, {ok: true, tls: true, issuer: 'https://auth.example.org/application/o/openccu-lite/', token_endpoint: 'https://auth.example.org/application/o/token/', verified_by: {...root, pem: undefined, trusted_here: true}, leaf, pinned: match});
        }
        if (u.pathname.endsWith('/peer-chain')) {
            const chain = PEER_CHAIN.map((c) => ({...c, trusted: list.some((a) => a.fingerprint === c.fingerprint)}));
            const verified = chain.some((c) => c.trusted);
            return sendJSON(res, {chain, verified, error: verified ? '' : 'x509: certificate signed by unknown authority'});
        }
        const pem = String(b.pem ?? '');
        if (pem.includes('PRIVATE KEY')) return sendJSON(res, {error: 'private_key', message: 'the text holds a private key: only certificates are accepted - never paste or upload a key here'}, 422);
        const blocks = pem.match(/-----BEGIN CERTIFICATE-----[\s\S]*?-----END CERTIFICATE-----/g) ?? [];
        if (!blocks.length) return sendJSON(res, {error: 'no_certificate', message: 'no certificate found: paste a PEM block beginning with -----BEGIN CERTIFICATE-----'}, 422);
        const added = [];
        for (const block of blocks) {
            const peer = PEER_CHAIN.find((c) => c.pem.trim() === block.trim());
            if (b.fingerprint && (!peer || peer.fingerprint.replace(/:/g, '') !== String(b.fingerprint).replace(/:/g, '').toUpperCase())) return sendJSON(res, {error: 'fingerprint_mismatch', message: 'the certificate is not the one whose fingerprint was confirmed'}, 422);
            const n = list.length + added.length + 1;
            const a = peer ? {...peer, purposes: ['oidc'], added: '2026-09-25T00:40:00Z', added_by: 'admin'} : {id: `c${String(n).padStart(15, '0')}`, purposes: ['oidc'], subject: `CN=Pasted ${n}`, issuer: `CN=Pasted ${n}`, not_before: '2026-01-01T00:00:00Z', not_after: n === 2 ? '2026-10-10T00:00:00Z' : '2030-01-01T00:00:00Z', fingerprint: `CC:${String(n).padStart(2, '0')}:` + '00:'.repeat(29) + 'FF', ca: true, self_signed: true, expires_soon: n === 2, added: '2026-09-25T00:40:00Z', added_by: 'admin', pem: block};
            if (!list.some((x) => x.id === a.id)) added.push(a);
        }
        list.push(...added);
        return sendJSON(res, {anchors: view(), added: added.map(({pem, ...a}) => a)});
    });
    return true;
}

// openccu-lite task 231: the four trust stores, per browser (stub-trust, shared with the OIDC
// settings' list so both views agree); stub-trust-pending=1 plants a strict failure with the
// System store's copy as its one-click fix. The stub parses nothing: a PEM block or a DER upload
// is one certificate named by its position.
const TRUST_STORES = new Map();
const TRUST_IDS = ['system', 'occulited', 'oidc', 'acme'];
const FP = (n) => `${n}${n}:` + '00:'.repeat(30) + `${n}${n}`;
const trustCert = (id, cn, o, opts = {}) => ({id, purposes: [], subject: `CN=${cn},O=${o}`, issuer: `CN=${cn},O=${o}`, not_before: '2020-01-01T00:00:00Z', not_after: '2038-01-01T00:00:00Z', fingerprint: FP(id.slice(-1).toUpperCase()), ca: true, self_signed: true, source: 'image', removable: true, ...opts});
function trustStateOf(jar) {
    const id = jar['stub-trust'] ?? '';
    let st = TRUST_STORES.get(id);
    if (!st) {
        st = {
            system: [
                trustCert('s000000000000001', 'ISRG Root X1', 'Internet Security Research Group'),
                trustCert('s000000000000002', 'USERTrust ECC Certification Authority', 'The USERTRUST Network'),
                trustCert('s000000000000003', 'USERTrust RSA Certification Authority', 'The USERTRUST Network'),
                trustCert('s000000000000004', 'DigiCert Global Root G2', 'DigiCert Inc'),
                trustCert('s000000000000005', 'GlobalSign Root CA', 'GlobalSign nv-sa'),
                trustCert('s000000000000006', 'Baltimore CyberTrust Root', 'Baltimore', {distrusted: true, removable: false}),
                trustCert('s000000000000007', 'DigiCert Global Root G3', 'DigiCert Inc'),
                trustCert('s000000000000008', 'Amazon Root CA 1', 'Amazon'),
                trustCert('s000000000000009', 'Lab CA', 'Lab', {source: 'added', origin: 'page', added: '2026-09-25T09:00:00Z', added_by: 'admin', not_after: '2027-01-01T00:00:00Z'}),
            ],
            occulited: [
                trustCert('s000000000000001', 'ISRG Root X1', 'Internet Security Research Group'),
                trustCert('s000000000000002', 'USERTrust ECC Certification Authority', 'The USERTRUST Network'),
                trustCert('s000000000000003', 'USERTrust RSA Certification Authority', 'The USERTRUST Network'),
            ],
            acme: [],
            pending: jar['stub-trust-pending'] === '1',
            n: 0,
            // openccu-lite task 232: the pins per purpose; stub-trust-pin-failure=1 plants a mismatch
            // for OIDC: a stale pin and the chain the provider presents now
            pins: {oidc: jar['stub-trust-pin-failure'] === '1' ? [{id: 'stale0000000001', purpose: 'oidc', spki: 'OldKeyHash00000000000000000000000000000000=', mode: 'with-ca', subject: 'CN=auth.example.org', not_after: '2026-09-30T00:00:00Z', fingerprint: 'EE:55:' + '00:'.repeat(29) + '05', added: '2026-06-01T00:00:00Z', added_by: 'admin'}] : [], acme: []},
            pinFailures: jar['stub-trust-pin-failure'] === '1' ? [{purpose: 'oidc', host: 'auth.example.org', at: '2026-10-03T08:00:00Z', error: `auth.example.org presents a certificate none of the 1 pin(s) for oidc match: CN=auth.example.org, public key SHA-256 ${SPKI_LEAF}, certificate SHA-256 AA:11:${'00:'.repeat(29)}01`, chain: PEER_CHAIN.map(({pem, ...c}) => c)}] : [],
        };
        TRUST_STORES.set(id, st);
    }
    return st;
}
function trustOIDCList(jar) {
    const id = jar['stub-trust'] ?? '';
    if (!OIDC_TRUST.has(id)) OIDC_TRUST.set(id, []);
    return OIDC_TRUST.get(id);
}
function trustView(jar) {
    const st = trustStateOf(jar);
    const oidc = trustOIDCList(jar).map(({pem, ...a}) => ({...a, source: 'added', origin: a.origin ?? 'oidc-settings', removable: true}));
    const stores = [
        {id: 'system', editable: true, certificates: st.system},
        {id: 'occulited', editable: true, certificates: st.occulited},
        {id: 'oidc', editable: true, certificates: oidc, pins: st.pins.oidc},
        {id: 'acme', editable: true, certificates: st.acme, pins: st.pins.acme},
    ];
    const pending = st.pending ? [{host: 'api.github.com', store: 'occulited', at: '2026-09-25T20:00:00Z', error: 'tls: failed to verify certificate: x509: certificate signed by unknown authority', issuer: 'CN=DigiCert Global Root G2,O=DigiCert Inc', chain: [], candidate: st.system.find((c) => c.id === 's000000000000004')}] : [];
    return {stores, pending, pin_failures: st.pinFailures};
}
// openccu-lite task 232: the pin routes of the oidc and acme stores - the peer's chain, pin by
// certificate or bare hash in either mode (replace drops the others), remove. A pin whose key the
// pending mismatch's chain carries clears it; the last pin removed clears the purpose's failures.
function pinRoute(req, u, res, jar, store, parts, body) {
    const st = trustStateOf(jar);
    if (!['oidc', 'acme'].includes(store)) return sendJSON(res, {error: 'not_found', message: 'this store takes no pins: only the OAuth / OIDC and the ACME store do'}, 404);
    const pins = st.pins[store];
    const chainOf = store === 'oidc' ? PEER_CHAIN : ACME_CHAIN;
    const host = store === 'oidc' ? 'auth.example.org' : 'ca.lan';
    const b = body ? JSON.parse(body) : {};
    if (req.method === 'POST' && parts[3] === 'peer') {
        if (b.url && !String(b.url).startsWith('https://')) return sendJSON(res, {error: 'invalid', message: 'the server is an https URL; a plain http one has no certificate to pin'}, 422);
        const chain = chainOf.map((c) => ({...c, pinned: pins.some((p) => p.spki === c.spki)}));
        const match = pins.find((p) => chain.some((c) => c.spki === p.spki));
        const caOK = store === 'oidc' ? trustOIDCList(jar).length > 0 : st.acme.length > 0;
        const verified = pins.length ? !!match && (match.mode === 'only' || caOK) : caOK;
        return sendJSON(res, {url: b.url || `https://${host}/`, host, chain, verified, error: verified ? undefined : pins.length && !match ? `${host} presents a certificate none of the ${pins.length} pin(s) for ${store} match` : 'tls: failed to verify certificate: x509: certificate signed by unknown authority', pin_only: verified && match?.mode === 'only' ? true : undefined});
    }
    if (req.method === 'POST' && !parts[3]) {
        const mode = b.mode || 'with-ca';
        if (!['with-ca', 'only'].includes(mode)) return sendJSON(res, {error: 'invalid', message: 'the mode is with-ca (the CA check as well, the default) or only (the pin alone)'}, 422);
        let pin;
        if (b.pem) {
            const c = chainOf.find((x) => x.pem.trim() === String(b.pem).trim());
            if (!c) return sendJSON(res, {error: 'bad_certificate', message: 'a certificate does not parse'}, 422);
            pin = {id: c.id.replace(/^[a-z]+/, 'k'), purpose: store, spki: c.spki, mode, subject: c.subject, not_after: c.not_after, fingerprint: c.fingerprint, added: '2026-10-03T10:00:00Z', added_by: 'admin'};
        } else {
            const v = String(b.spki ?? '').trim().replace(/^sha256\/\//, '');
            const hex = v.replace(/[:\s-]/g, '');
            if (!/^[A-Za-z0-9+/]{43}=$/.test(v) && !/^[0-9A-Fa-f]{64}$/.test(hex)) return sendJSON(res, {error: 'invalid', message: 'the public key hash is the SHA-256 of a certificate\'s SubjectPublicKeyInfo: 44 base64 characters or 64 hex digits'}, 422);
            const spki = /^[0-9A-Fa-f]{64}$/.test(hex) ? Buffer.from(hex, 'hex').toString('base64') : v;
            pin = {id: 'h' + spki.slice(0, 15).replace(/[^A-Za-z0-9]/g, '0'), purpose: store, spki, mode, added: '2026-10-03T10:00:00Z', added_by: 'admin'};
        }
        if (b.replace) pins.splice(0, pins.length);
        else if (pins.some((p) => p.spki === pin.spki)) return sendJSON(res, {error: 'present', message: 'this public key is pinned already'}, 409);
        pins.push(pin);
        st.pinFailures = st.pinFailures.filter((f) => !(f.purpose === store && (f.chain ?? []).some((c) => c.spki === pin.spki)));
        return sendJSON(res, {pin, pins});
    }
    if (req.method === 'DELETE' && parts[3]) {
        const k = pins.findIndex((p) => p.id === decodeURIComponent(parts[3]));
        if (k < 0) return sendJSON(res, {error: 'not_found', message: 'no such anchor'}, 404);
        pins.splice(k, 1);
        if (!pins.length) st.pinFailures = st.pinFailures.filter((f) => f.purpose !== store);
        return sendJSON(res, {pins});
    }
    return sendJSON(res, {error: 'not_found', message: 'no such route'}, 404);
}
function trustRoute(req, u, res, jar) {
    const st = trustStateOf(jar);
    const listOf = (store) => (store === 'oidc' ? trustOIDCList(jar) : st[store]);
    const parts = u.pathname.split('/').slice(4); // trust, {store}, {id}, {action}
    const store = parts[1];
    const cid = parts[2] ? decodeURIComponent(parts[2]) : '';
    const action = parts[3];
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
        const b = body ? JSON.parse(body) : {};
        if (req.method === 'GET' && !store) return sendJSON(res, trustView(jar));
        if (!TRUST_IDS.includes(store)) return sendJSON(res, {error: 'not_found', message: 'no such trust store'}, 404);
        if (parts[2] === 'pins') return pinRoute(req, u, res, jar, store, parts, body); // openccu-lite task 232
        const storeView = (id) => trustView(jar).stores.find((s) => s.id === id);
        const list = listOf(store);
        if (req.method === 'POST' && !cid) {
            const pem = String(b.pem ?? '');
            if (pem.includes('PRIVATE KEY')) return sendJSON(res, {error: 'private_key', message: 'the text holds a private key: only certificates are accepted - never paste or upload a key here'}, 422);
            const blocks = pem ? (pem.match(/-----BEGIN CERTIFICATE-----[\s\S]*?-----END CERTIFICATE-----/g) ?? []) : b.der ? ['der'] : [];
            if (!blocks.length) return sendJSON(res, {error: 'no_certificate', message: 'no certificate found: paste a PEM block beginning with -----BEGIN CERTIFICATE-----'}, 422);
            const added = [];
            for (const block of blocks) {
                st.n++;
                const cn = block === 'der' ? `DER upload ${st.n}` : `Pasted ${st.n}`;
                const a = trustCert(`p${String(st.n).padStart(15, '0')}`, cn, 'Lab', {source: 'added', origin: 'page', added: '2026-09-25T20:00:00Z', added_by: 'admin', fingerprint: `CC:${String(st.n).padStart(2, '0')}:` + '00:'.repeat(29) + 'FF'});
                if (store === 'oidc') a.purposes = ['oidc'];
                list.push(store === 'oidc' ? {...a, pem: block} : a);
                added.push(a);
            }
            return sendJSON(res, {added, store: storeView(store)});
        }
        const k = list.findIndex((c) => c.id === cid);
        if (k < 0) return sendJSON(res, {error: 'not_found', message: 'no such anchor'}, 404);
        const c = list[k];
        if (req.method === 'DELETE') {
            if (c.distrusted) return sendJSON(res, {error: 'invalid', message: 'this certificate cannot be removed here'}, 422);
            if (c.source === 'image') list[k] = {...c, distrusted: true, removable: false};
            else list.splice(k, 1);
            return sendJSON(res, {store: storeView(store)});
        }
        if (req.method === 'POST' && action === 'restore') {
            if (!c.distrusted) return sendJSON(res, {error: 'not_found', message: 'no such anchor'}, 404);
            list[k] = {...c, distrusted: false, removable: true};
            return sendJSON(res, {store: storeView(store)});
        }
        if (req.method === 'POST' && action === 'copy') {
            const to = String(b.to ?? '');
            if (to === store) return sendJSON(res, {error: 'invalid', message: 'a certificate is copied into another store'}, 422);
            if (!TRUST_IDS.includes(to)) return sendJSON(res, {error: 'not_found', message: 'no such trust store'}, 404);
            const target = listOf(to);
            const copy = {...c, source: 'added', origin: 'copy', added: '2026-09-25T20:00:00Z', added_by: 'admin', distrusted: false, removable: true, purposes: to === 'oidc' ? ['oidc'] : []};
            if (to === 'oidc') copy.pem = FAKE_PEM(c.id);
            const added = [];
            if (!target.some((x) => x.id === c.id)) {
                target.push(copy);
                added.push(copy);
            }
            if (to === 'occulited' && st.pending && c.id === 's000000000000004') st.pending = false;
            return sendJSON(res, {added, store: storeView(to)});
        }
        if (req.method === 'GET' && action === 'pem') {
            res.writeHead(200, {'Content-Type': 'application/x-pem-file'});
            return res.end(FAKE_PEM(c.id));
        }
        return sendJSON(res, {error: 'not_found', message: 'no such route'}, 404);
    });
    return true;
}

function variant(req, u, res) {
    const jar = cookieJar(req);
    const s = sessionOf(jar);
    const warn = jar['stub-warn'] === '1';
    const key = `${req.method} ${u.pathname}`;
    if (jar['stub-real'] === '1' && REAL_BOX[key]) {
        sendJSON(res, REAL_BOX[key]());
        return true;
    }
    // task 95: the status LED, per browser (led.mjs)
    if (ledRoute(req, u, res, jar)) return true;
    // openccu-lite task 228: the Storage page's USB sticks, per browser (stub-usb; stub-usb=none: none)
    if (u.pathname.startsWith('/api/system/v1/storage/usb')) return storageUSBRoute(req, u, res, jar);
    // openccu-lite task 228, phase 2: the network shares, per browser (stub-shares)
    if (u.pathname.startsWith('/api/system/v1/storage/shares')) return storageSharesRoute(req, u, res, jar);
    // openccu-lite task 251: a checked backup's paired devices - the stub knows no backup, so it
    // answers an empty one and a free system (the restore-devices spec mocks the real shapes)
    if (key === 'GET /api/system/v1/restore/devices') {
        sendJSON(res, {backup: {bidcos_rf: {devices: 0, has_key: false, gateways: 0}, hmip: {devices: 0, local_key: false, device_key_map: false}, bidcos_wired: {devices: 0, gateways: 0}, key_index: 0, files: []}, target: {paired: {}, devices: 0, unknown: [], user_key: false, importable: true}});
        return true;
    }
    // openccu-lite task 275: the record of the last device import; stub-import=pending|done|rejected|
    // no-module shows one (the cookie a test sets), retry answers 202, a dismissal 204 and forgets it
    if (u.pathname === '/api/system/v1/radio/import' || u.pathname === '/api/system/v1/radio/import/retry') {
        const jar = cookieJar(req);
        // stub-import=<state>[.<anything>]: the suffix keeps one test's retry and dismissal apart
        // from another's while the projects run in parallel
        const cookie = jar['stub-import'] ?? '';
        const state = cookie.split('.')[0];
        const view = () => {
            if (!state || importDismissed.has(cookie)) return {imported: false};
            const record = {at: '2026-09-27T16:40:00Z', file: 'restore-ccu.sbk', version: '3.89.11',
                hmip: {from_sgtin: '3014F711A0001F5F000000AF', to_sgtin: '3014F711A0001F0000000A03', to_module: 'RPI-RF-MOD 0000000A03', module_changed: true, local_key: false, devices: 2},
                // task 296: stub-import-key=<verdict> is the passphrase's verdict at the import
                bidcos_rf: {address: '0xFF5678', serial: '1709ADFA00', devices: 1, non_default_key: true, key_index: 1, target_key_replaced: false, module: 'RPI-RF-MOD 0000000A03', ...(jar['stub-import-key'] ? {key_check: jar['stub-import-key']} : {})}};
            // openccu-lite B-289: stub-import=local-swap - moved without the network key, a failed move
            const swap = state === 'local-swap';
            const outcome = {hmip: {state: swap ? 'rejected' : state, module_now: state === 'no-module' ? '' : '3014F711A0001F0000000A03', ...(state === 'rejected' ? {cause: 'unreachable'} : {}), ...(swap ? {cause: 'adapter-version', line: 'Could not exchange network key, adapter version not supported'} : {}), ...(state === 'done' ? {line: 'Adapter exchange successful.'} : {})},
                bidcos_rf: {took: true, interface: 'CCU2 1709ADFA00', connected: true}};
            return {imported: true, record, outcome, switching: importRetried.has(cookie) ? 'retry' : ''};
        };
        if (req.method === 'GET') { sendJSON(res, view()); return true; }
        if (req.method === 'DELETE') { importDismissed.add(cookie); res.writeHead(204); res.end(); return true; }
        if (req.method === 'POST') { req.resume(); req.on('end', () => { importRetried.add(cookie); sendJSON(res, view(), 202); }); return true; }
    }
    if (key === 'POST /api/system/v1/restore/import-devices') {
        readBody(req).then((raw) => {
            // task 317 (D-120): not without confirm
            if (!JSON.parse(raw || '{}').confirm) return sendJSON(res, {error: 'confirm', message: 'confirm: true is required'}, 400);
            sendJSON(res, {error: 'nothing_to_import', message: 'the backup holds no paired device and no radio identity'}, 422);
        });
        return true;
    }
    // openccu-lite task 231: the Trust stores page, per browser (stub-trust)
    if (u.pathname === '/api/system/v1/trust' || u.pathname.startsWith('/api/system/v1/trust/')) return trustRoute(req, u, res, jar);
    // phases 3-4: the location picker's list and the folders on a location
    if (key === 'GET /api/system/v1/storage/locations') {
        sendJSON(res, locationsView(u.searchParams.get('use'), jar));
        return true;
    }
    if (key === 'GET /api/system/v1/storage/dirs') {
        sendJSON(res, dirsView(u.searchParams));
        return true;
    }
    // openccu-lite task 227: IPv6 per interface with its rollback window, per browser (stub-v6);
    // stub-v6-window=<s> shortens the window
    if (u.pathname.startsWith('/api/system/v1/network/ipv6')) return ipv6Route(req, u, res, jar);
    // openccu-lite task 230: the certificates trusted for the identity provider, per browser
    if (u.pathname.startsWith('/api/auth/v1/oidc/trust') || u.pathname === '/api/auth/v1/oidc/test' || u.pathname === '/api/auth/v1/oidc/peer-chain') return oidcTrustRoute(req, u, res, jar);
    // task 19: stub-oidc=on is a box with a provider and password login on, stub-oidc=only one with
    // the switch off; stub-session-method=oidc makes this session one that came through the
    // provider (the Security page lets the switch be turned off from such a session only)
    const oidc = jar['stub-oidc'];
    if (key === 'GET /api/auth/v1/oidc' && oidc) {
        sendJSON(res, {enabled: true, name: 'authentik', password_login: oidc !== 'only'});
        return true;
    }
    if (key === 'GET /api/system/v1/sbom') {
        sendJSON(res, STUB_SBOM);
        return true;
    }
    // task 193: the public principal (stub-public=1): the Control app without a login
    if (key === 'GET /api/auth/v1/state' && jar['stub-public'] === '1') {
        sendJSON(res, {setup_required: false, authenticated: true, user: 'guest', role: 'user', level: 'operate', account_id: '0badf00d', public: true, must_change_password: false, scopes: ['logs:read', 'meta:read', 'rpc:operate', 'system:read']});
        return true;
    }
    // B-174: a browser session opened with an API token (stub-token=1): signed in, scopes, no role;
    // the logout ends it (the token itself stays), and the state is then signed out (stub-token=out)
    if (key === 'GET /api/auth/v1/state' && jar['stub-token'] === '1') {
        sendJSON(res, {setup_required: false, authenticated: true, user: 'ci-token', scopes: ['*']});
        return true;
    }
    // task 262: with the cookie stub-webauthn-registered=1 an account has a key, so the Network
    // page warns before a rename
    if (key === 'GET /api/auth/v1/webauthn' && jar['stub-webauthn-registered'] === '1') {
        sendJSON(res, {...routes[key], registered: true});
        return true;
    }
    if (key === 'GET /api/auth/v1/state' && jar['stub-token'] === 'out') {
        sendJSON(res, {setup_required: false, authenticated: false});
        return true;
    }
    if (key === 'POST /api/auth/v1/logout' && jar['stub-token'] === '1') {
        res.writeHead(200, {'Content-Type': 'application/json', 'Set-Cookie': 'stub-token=out; Path=/'});
        res.end(JSON.stringify({ok: true}));
        return true;
    }
    if (key === 'GET /api/auth/v1/state' && jar['stub-session-method']) {
        sendJSON(res, {...routes[key], method: jar['stub-session-method']});
        return true;
    }
    if (key === 'GET /api/auth/v1/config' && oidc) {
        sendJSON(res, {...routes[key], running: 'oidc', restart_required: false, password_login: oidc !== 'only'});
        return true;
    }
    if (key === 'PUT /api/auth/v1/config') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            // openccu-lite B-206: strict like the API - GET's read-only fields are refused
            const writable = ['mode', 'name', 'issuer', 'client_id', 'client_secret', 'username_claim', 'scopes', 'password_login', 'session_idle', 'session_max'];
            const unknown = Object.keys(b).find((k) => !writable.includes(k));
            if (unknown) return sendJSON(res, {error: 'invalid-body', message: `"${unknown}" is not a field PUT /config takes (it may be one of GET's read-only fields); send only ${writable.join(', ')}`}, 422);
            const view = {...routes['GET /api/auth/v1/config'], ...b, client_secret_set: !!b.client_secret || routes['GET /api/auth/v1/config'].client_secret_set, running: oidc ? 'oidc' : 'local', restart_required: (oidc ? 'oidc' : 'local') !== b.mode};
            delete view.client_secret;
            if (b.mode !== 'oidc') view.password_login = true;
            sendJSON(res, view);
        });
        return true;
    }
    if (key === 'GET /api/auth/v1/users' && oidc) {
        // an account created for the provider, without a password
        sendJSON(res, {users: [...routes[key].users, {name: 'guest', role: 'user', created: '2026-09-14T20:00:00Z', password_set: false, last_provider_login: '2026-09-15T08:30:00Z'}]});
        return true;
    }
    if (key === 'GET /api/system/v1/catalog') {
        sendJSON(res, catalogFor(s));
        return true;
    }
    if (key === 'GET /api/system/v1/addons') {
        const list = structuredClone(ADDONS);
        if (s) for (const a of list) if (s.versions[a.id]) a.version = s.versions[a.id];
        sendJSON(res, {addons: withFullscreenAddons(jar, withEarlyAddons(jar, withLegacyAddons(jar, warn ? [...list, ...structuredClone(WARN_ADDONS)] : list)))});
        return true;
    }
    if (key === 'GET /api/system/v1/nav') {
        sendJSON(res, {entries: withLegacyNav(jar, NAV)});
        return true;
    }
    // task 66: a token is created with its scopes; the secret comes back once
    if (key === 'POST /api/auth/v1/tokens') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            res.writeHead(201, {'Content-Type': 'application/json'});
            res.end(JSON.stringify({name: b.name, scopes: b.scopes, token: 'olt_0123456789abcdef0123456789abcdef', expires: b.expires, ips: b.ips}));
        });
        return true;
    }
    // task 125: the alias, the tickets and the switches
    if (key === 'POST /api/auth/v1/legacy-sid') {
        req.resume();
        req.on('end', () => sendJSON(res, {legacy_sid: LEGACY_SID}));
        return true;
    }
    if (key === 'POST /api/auth/v1/ticket') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = body ? JSON.parse(body) : {};
            // task 154: a confirmed ticket needs the password (the stub's is labpass1)
            if (b.confirm) {
                const how = jar['stub-confirm'] || 'password';
                if (how === 'oidc') return sendJSON(res, {error: 'confirm-oidc', message: 'confirm with a fresh login at the identity provider', url: '/api/auth/v1/oidc/confirm?path=' + encodeURIComponent(b.path)}, 409);
                if (how === 'password' && b.password !== 'labpass1') return sendJSON(res, {error: 'invalid-credentials', message: 'invalid username or password'}, 401);
                return sendJSON(res, {ticket: DK_CONFIRMED, expires_in: 60});
            }
            sendJSON(res, {ticket: 'TICKETAAAAAAAAAAAAAAAAAAAA', expires_in: 60});
        });
        return true;
    }
    if (key === 'GET /api/auth/v1/confirm') {
        const how = jar['stub-confirm'] || 'password';
        sendJSON(res, how === 'oidc' ? {method: how, url: '/api/auth/v1/oidc/confirm?path=' + encodeURIComponent(u.searchParams.get('path') ?? '') + '&return=' + encodeURIComponent(u.searchParams.get('return') ?? '')} : {method: how});
        return true;
    }
    // the provider's round trip in one step: straight back with the ticket in the fragment, or
    // refused (the cookie stub-confirm-refuse)
    if (key === 'GET /api/auth/v1/oidc/confirm') {
        const back = u.searchParams.get('return') || '/';
        res.writeHead(302, {Location: back + (jar['stub-confirm-refuse'] ? '#confirm-error=' + encodeURIComponent('the provider did not ask for the password again') : '#confirm=' + DK_CONFIRMED)});
        res.end();
        return true;
    }
    if (key === 'POST /api/auth/v1/ticket/redeem') {
        req.resume();
        req.on('end', () => sendJSON(res, {sid: 'HANDEDOVERAAAAAAAAAAAAAAAA', user: 'admin', role: 'admin', must_change_password: false}));
        return true;
    }
    if (key === 'GET /api/system/v1/legacy-session') {
        sendJSON(res, structuredClone(legacyStateOf(jar)));
        return true;
    }
    if (key === 'PUT /api/system/v1/legacy-session') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const st = legacyStateOf(jar);
            if (typeof b.enabled === 'boolean') st.enabled = b.enabled;
            if (Array.isArray(b.off)) st.off = [...new Set(b.off)].sort();
            sendJSON(res, structuredClone(st));
        });
        return true;
    }
    if (key === 'GET /api/system/v1/early-start') {
        sendJSON(res, {...structuredClone(earlyStateOf(jar)), next_boot: true});
        return true;
    }
    if (key === 'PUT /api/system/v1/early-start') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const st = earlyStateOf(jar);
            if (typeof b.enabled === 'boolean') st.enabled = b.enabled;
            if (Array.isArray(b.off)) st.off = [...new Set(b.off)].sort();
            sendJSON(res, {...structuredClone(st), next_boot: true});
        });
        return true;
    }
    const earlyAddon = req.method === 'PUT' && /^\/api\/system\/v1\/addons\/([^/]+)\/early-start$/.exec(u.pathname);
    if (earlyAddon) {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const st = earlyStateOf(jar);
            const id = decodeURIComponent(earlyAddon[1]);
            st.off = st.off.filter((x) => x !== id);
            if (!b.enabled) st.off = [...st.off, id].sort();
            const declared = EARLY_ADDONS.includes(id);
            sendJSON(res, {...structuredClone(st), next_boot: true, id, addon_enabled: !st.off.includes(id), start_early_declared: declared, start_early: declared && st.enabled && !st.off.includes(id)});
        });
        return true;
    }
    const legacyAddon = req.method === 'PUT' && /^\/api\/system\/v1\/addons\/([^/]+)\/legacy-session$/.exec(u.pathname);
    if (legacyAddon) {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const st = legacyStateOf(jar);
            const id = decodeURIComponent(legacyAddon[1]);
            st.off = st.off.filter((x) => x !== id);
            if (!b.enabled) st.off = [...st.off, id].sort();
            sendJSON(res, {...structuredClone(st), id, addon_enabled: st.enabled && !st.off.includes(id)});
        });
        return true;
    }
    // task 86: the targets' routes answer as the system does; a POST answers the new target with an id
    // and - for SSH - its key, a PUT the target as sent
    const tgt = /^\/api\/system\/v1\/backup\/targets(?:\/([a-z][a-z0-9]{0,15})(?:\/(test|mount|unmount|run|backups|keypair|hostkey))?)?$/.exec(u.pathname);
    if (tgt && key !== 'GET /api/system/v1/backup/targets' && key !== 'GET /api/system/v1/backup/targets/directory/backups') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const [, id, sub] = tgt;
            const base = routes['GET /api/system/v1/backup/targets'].targets.find((x) => x.id === id);
            if (!sub && req.method === 'POST' && !id) {
                const t = {...b, id: 'tnew0001', created: new Date().toISOString(), state: {state: 'idle', failures: 0, mounted: false}};
                if (t.sftp) t.sftp = {port: 22, ...t.sftp, public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINewKeyForTheStubTarget0000000000000000000 openccu-lite-backup@openccu'};
                if (t.cifs) t.cifs = {...t.cifs, has_password: !!t.cifs.password, password: undefined};
                return sendJSON(res, {target: t}, 201);
            }
            if (id && !base && id !== 'tnew0001') return sendJSON(res, {error: 'not_found', message: 'no such backup target'}, 404);
            if (!sub && req.method === 'PUT') return sendJSON(res, {target: {...base, ...b, id}});
            if (!sub && req.method === 'DELETE') { res.writeHead(204); return res.end(); }
            if (sub === 'test') return sendJSON(res, {ok: true, state: 'writable', step: 'done', free_bytes: 812000000000, total_bytes: 2000000000000, needed_bytes: 5190451, write_mbps: 11.4, at: new Date().toISOString()});
            if (sub === 'run') return sendJSON(res, {started: true, instance: id}, 202);
            if (sub === 'mount' || sub === 'unmount') return sendJSON(res, {...base, state: {...base.state, mounted: sub === 'mount'}});
            if (sub === 'backups') return sendJSON(res, {backups: []});
            if (sub === 'keypair') return sendJSON(res, {public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIRenewedKeyForTheStubTarget00000000000000 openccu-lite-backup@openccu'});
            if (sub === 'hostkey' && req.method === 'GET') return sendJSON(res, {type: 'ssh-ed25519', fingerprint: 'SHA256:NewServerKeyOfTheStub0000000000000000000000', trusted: false, changed: false});
            if (sub === 'hostkey' && req.method === 'PUT') return sendJSON(res, {type: 'ssh-ed25519', fingerprint: b.fingerprint, trusted: true, changed: false});
            sendJSON(res, {error: 'not_found', message: 'no such route'}, 404);
        });
        return true;
    }
    if (key === 'POST /api/system/v1/backup/run' || key === 'PUT /api/system/v1/backup/nightly') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            if (key === 'PUT /api/system/v1/backup/nightly') return sendJSON(res, {enabled: !!b.enabled, time: '00:07'});
            sendJSON(res, {started: true, instance: b.target || 'all'}, 202);
        });
        return true;
    }
    // a backup, small, as the download fetches it (with its ticket)
    if (key === 'GET /api/system/v1/backup') {
        res.writeHead(200, {'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="stub.sbk"'});
        res.end('stub backup');
        return true;
    }
    // B-98 (D-67): an uploaded archive; stub-install-stopped=failed|reboot has it leave Mosquitto stopped
    // (in the reboot, with its unit switched off, so the boot would not start it)
    // B-4: the upload is a job the page polls; its first poll answers running, the next the end
    if (key === 'POST /api/system/v1/addons/install') {
        req.resume();
        req.on('end', () => {
            const stopped = jar['stub-install-stopped'];
            let job;
            if (stopped === 'failed') job = {state: 'failed', result: {exit: 1, meaning: 'exit 1', reboot_required: false, output: 'update_script: exit 1', seconds: 1.4, stopped_addons: [{id: 'mosquitto', starts_at_boot: true}]}};
            else if (stopped === 'reboot') job = {state: 'done', result: {exit: 10, meaning: 'installed, reboot required', reboot_required: true, output: '', seconds: 2.1, stopped_addons: [{id: 'mosquitto', starts_at_boot: false}]}};
            else job = {state: 'done', result: {exit: 0, meaning: 'installed', reboot_required: false, output: '', seconds: 1.8}};
            const id = `job${++installJobSeq}`;
            installJobs.set(id, {...job, id, polls: 0, started: new Date().toISOString(), bytes: 1});
            sendJSON(res, {id, state: 'running', started: new Date().toISOString(), bytes: 1}, 202);
        });
        return true;
    }
    if (key === 'GET /api/system/v1/addons/install') {
        const asked = u.searchParams.get('job');
        const id = asked ?? [...installJobs.keys()].at(-1);
        const job = id && installJobs.get(id);
        // occulited B-15: the newest of none is 204, an unknown id 404
        if (!job && !asked) return res.writeHead(204).end(), true;
        if (!job) return sendJSON(res, {error: 'unknown-job', message: 'no such install job'}, 404), true;
        // the newest job, asked for by a page that opens, is history: the stub's jobs are over by
        // then, and that look does not move another test's job along
        if (asked) job.polls += 1;
        const {polls, ...out} = job;
        sendJSON(res, !asked || polls > 1 ? {...out, finished: new Date().toISOString()} : {id: out.id, state: 'running', started: out.started, bytes: out.bytes});
        return true;
    }
    const install = req.method === 'POST' && /^\/api\/system\/v1\/catalog\/([^/]+)\/install$/.exec(u.pathname);
    if (install) {
        const id = decodeURIComponent(install[1]);
        if (s) {
            s.run = {addon_id: id, phase: 'resolving', percent: 0, started: new Date().toISOString()};
            s.target = routes['GET /api/system/v1/catalog'].catalog.addons.find((e) => e.id === id)?.latest?.version ?? '';
            s.polls = 0;
        }
        sendJSON(res, {started: true}, 202);
        return true;
    }
    if (key === 'GET /api/system/v1/catalog/progress') {
        if (s?.run && !s.run.finished) {
            s.polls += 1;
            const phases = [['downloading', 35, {bytes: 9 * 1048576, total: 26 * 1048576}], ['installing', 80, {}], ['done', 100, {}]];
            const [phase, percent, extra] = phases[Math.min(s.polls, phases.length) - 1];
            Object.assign(s.run, {phase, percent, ...extra});
            if (phase === 'done') {
                s.run.finished = new Date().toISOString();
                s.run.result = {exit: 0, meaning: 'installed', reboot_required: false};
                // B-98 (D-67): stub-install-stopped=reboot|failed - the install left the addon stopped
                const stopped = jar['stub-install-stopped'];
                if (stopped === 'reboot') s.run.result = {exit: 10, meaning: 'installed, reboot required', reboot_required: true, stopped_addons: [{id: s.run.addon_id, starts_at_boot: true}]};
                if (stopped === 'failed') s.run.result = {exit: 1, meaning: 'exit 1', reboot_required: false, stopped_addons: [{id: s.run.addon_id, starts_at_boot: true}]};
                if (stopped !== 'failed') s.versions[s.run.addon_id] = s.target;
            }
        }
        sendJSON(res, {progress: s?.run ?? null});
        return true;
    }
    const own = req.method === 'GET' && /^\/api\/system\/v1\/addons\/([^/]+)\/update$/.exec(u.pathname);
    if (own) {
        const id = decodeURIComponent(own[1]);
        const a = ADDONS.find((x) => x.id === id);
        sendJSON(res, OWN_CHECK[id] ?? {installed: a?.version ?? '', available: a?.version ?? '', update_available: false, url: ''});
        return true;
    }
    if ((u.pathname === '/api/system/v1/warnings' || u.pathname.startsWith('/api/system/v1/warnings/') || /^\/api\/system\/v1\/addons\/[^/]+\/ownership$/.test(u.pathname)) && warningsRoute(req, u, res, jar, warn)) {
        return true;
    }
    // task 81: the default security key (stub-key=default)
    if (jar['stub-key'] === 'default' && key === 'GET /api/system/v1/radio') {
        sendJSON(res, {...structuredClone(routes[key]), security_key_set: false});
        return true;
    }
    if (warn && key === 'GET /api/system/v1/status') {
        sendJSON(res, {...structuredClone(routes[key]), meta_recovered: true, unclean_shutdown: {at: new Date(Date.now() - 3 * 3600 * 1000).toISOString()}});
        return true;
    }
    if (warn && key === 'GET /api/system/v1/backup/schedule') {
        sendJSON(res, {...structuredClone(routes[key]), path_exists: false, on_userfs: false, backups: []});
        return true;
    }
    if (warn && key === 'GET /api/system/v1/certificate') {
        const st = structuredClone(routes[key]);
        st.warning = 'expiring';
        st.current.days_left = 9;
        st.current.not_after = new Date(Date.now() + 9 * 86400 * 1000).toISOString();
        sendJSON(res, st);
        return true;
    }
    // task 69: the storage health panel - the Pi 4's card by default, a verdict of choice with
    // stub-storage=<good|watch|replace|old-sd|virtual|container>, and the watch verdict with the other
    // warnings; virtual and container are the ones whose host monitors the disks (task 111)
    if (key === 'GET /api/system/v1/storage') {
        const v = jar['stub-storage'] || (warn ? 'watch' : 'good');
        const rep = {...structuredClone(STORAGE[v] ?? STORAGE.good), checked: new Date().toISOString(), log_files: []};
        // task 84: stub-storage-logs=1 - files written beside the journal, the hint below the disks
        if (jar['stub-storage-logs'] === '1') {
            rep.log_files = [
                {path: '/usr/local/addons/hm2mqtt/var/hm2mqtt.log.1', size: 1_400_000, userfs: true, addon: 'hm2mqtt', growing: false},
                {path: '/usr/local/addons/redmatic/var/npm-cache/_logs/2026-09-12T10_12_44_571Z-debug-0.log', size: 42_000, userfs: true, addon: 'redmatic', growing: true, grown_bytes: 3_100},
                {path: '/var/log/hmserver.log', size: 188_000, userfs: false, growing: true, grown_bytes: 12_000},
            ];
            rep.log_files_more = 4;
        }
        sendJSON(res, rep);
        return true;
    }
    // task 75: stub-servicemsg=none - a system without a service message
    if (key === 'GET /api/system/v1/service-messages') {
        sendJSON(res, serviceMessagesView(jar));
        return true;
    }
    // task 156: BidCos-RF on an HM-CFG-USB-2, HmIP on the stick - two radios (stub-radios=2)
    if (jar['stub-radios'] === '2' && key === 'GET /api/system/v1/radio/health') {
        const h = structuredClone(routes[key]);
        Object.assign(h.interfaces.find((i) => i.interface === 'BidCos-RF'), {radio: 'module:JEQ9000002', radio_name: 'HM-CFG-USB-2 JEQ9000002'});
        sendJSON(res, h);
        return true;
    }
    // task 54: a radio at a given share of its budget (stub-duty=75)
    if (jar['stub-duty'] && key === 'GET /api/system/v1/radio/health') {
        const h = structuredClone(routes[key]);
        h.interfaces.find((i) => i.interface === 'HmIP-RF').duty_cycle = Number(jar['stub-duty']);
        sendJSON(res, h);
        return true;
    }
    // task 94: a boot in progress (stub-starting). `1`: hmipserver's JVM starting for 23 s while rfd
    // answers, its sampler error from the start still there. `boot`: the radio detection still running,
    // /var/hm_mode not written yet, the radio stack queued behind it, the sampler has seen nothing
    const starting = jar['stub-starting'];
    if (starting && key === 'GET /api/system/v1/radio/health') {
        const h = structuredClone(routes[key]);
        const unit = (name) => h.units.find((x) => x.unit === name);
        if (starting === 'boot') {
            Object.assign(h, {interfaces: [], errors: {}, history: {}, rates: {}});
            Object.assign(unit('occu-init-rf-hardware'), {active_state: 'activating', sub_state: 'start', starting: true, starting_s: 4});
            for (const n of ['multimacd', 'rfd', 'hmipserver']) Object.assign(unit(n), {active_state: 'inactive', sub_state: 'dead', starting: true, queued: true});
        } else {
            h.interfaces = h.interfaces.filter((i) => i.interface !== 'HmIP-RF');
            h.errors = {'HmIP-RF': 'dial tcp 127.0.0.1:32010: connect: connection refused'};
            Object.assign(unit('hmipserver'), {active_state: 'activating', sub_state: 'start', starting: true, starting_s: 23});
        }
        sendJSON(res, h);
        return true;
    }
    if (starting === 'boot' && key === 'GET /api/system/v1/radio') {
        sendJSON(res, {...structuredClone(routes[key]), mode: '', modules: []});
        return true;
    }
    if (starting && key === 'GET /api/system/v1/services') {
        const list = structuredClone(routes[key]);
        const hmip = list.services.find((x) => x.id === 'hmipserver');
        Object.assign(hmip, {running: false, starting: true, starting_s: 23});
        for (const k of ['pid', 'memory_bytes', 'cpu_seconds', 'since']) delete hmip[k];
        sendJSON(res, list);
        return true;
    }
    // task 94: the boot's clock gate (stub-clock): timed out with chrony still not synchronised, or
    // timed out and synchronised since
    if (jar['stub-clock'] && key === 'GET /api/system/v1/status') {
        const st = structuredClone(routes[key]);
        // openccu-lite task 299: the untrusted states beyond the timeout (rtc-implausible names the refused time)
        const c = jar['stub-clock'];
        st.clock = c === 'synced' || c === 'timeout' ? {state: 'timeout', synchronised: c === 'synced'} : {state: c, synchronised: false, ...(c === 'rtc-implausible' ? {rtc_implausible: '2041-01-01T00:00:00Z'} : {})};
        sendJSON(res, st);
        return true;
    }
    return false;
}
// task 69: the storage reports. The SD cards as the Pi 4 and the Charly measured them on 2026-09-12,
// the QEMU disk as the OVA box's; the eMMC, the SSDs and the NVMe as the Go fixtures describe them.
const PI4_CARD = {
    name: 'mmcblk0', kind: 'sd', vendor: 'SanDisk', vendor_id: '0x000003', model: 'SN64G', capacity_bytes: 63864569856, manufactured: '2024-06', age_months: 27,
    mounts: ['/', '/boot', '/usr/local'],
    filesystems: [
        {name: 'mmcblk0p2', mounts: ['/'], errors: 0, lifetime_write_bytes: 506121 * 1024},
        {name: 'mmcblk0p3', mounts: ['/usr/local'], errors: 0, lifetime_write_bytes: 49590013 * 1024},
    ],
    io_errors: 0, writes: {since_boot_bytes: 9270272, per_day_bytes: 8200000, per_day_days: 14, per_day_basis: 'samples'}, verdict: 'good', reasons: [],
};
const STORAGE = {
    good: {verdict: 'good', reasons: [], kernel_log: true, health_source: 'device', devices: [PI4_CARD]},
    watch: {
        verdict: 'watch', kernel_log: true,
        reasons: [
            {level: 'watch', code: 'emmc-eol-warning', device: 'mmcblk1'},
            {level: 'watch', code: 'ext4-errors', device: 'mmcblk1', count: 2, filesystem: '/usr/local'},
        ],
        devices: [
            {
                name: 'mmcblk1', kind: 'emmc', vendor: 'SanDisk', vendor_id: '0x000045', model: 'DG4016', capacity_bytes: 15634268160, manufactured: '2022-01', age_months: 56,
                mounts: ['/', '/usr/local'], emmc: {life_time_a: 2, life_time_b: 1, pre_eol: 'warning'},
                filesystems: [
                    {name: 'mmcblk1p2', mounts: ['/'], errors: 0, lifetime_write_bytes: 610000000},
                    {name: 'mmcblk1p3', mounts: ['/usr/local'], errors: 2, first_error: '2026-08-30T08:12:00Z', last_error: '2026-09-10T21:40:00Z', lifetime_write_bytes: 12000000000},
                ],
                io_errors: 0, writes: {since_boot_bytes: 52000000, per_day_bytes: 14000000, per_day_days: 21, per_day_basis: 'samples'}, verdict: 'watch',
                reasons: [
                    {level: 'watch', code: 'emmc-eol-warning', device: 'mmcblk1'},
                    {level: 'watch', code: 'ext4-errors', device: 'mmcblk1', count: 2, filesystem: '/usr/local'},
                ],
            },
            {
                name: 'sda', kind: 'usb', vendor: 'Samsung', model: 'PSSD T7', capacity_bytes: 500107862016, mounts: ['/media/usb0'],
                smart: {available: true, passed: true, model: 'Samsung PSSD T7', power_on_hours: 4211, wear_percent: 3, reallocated_sectors: 0, temperature_c: 35, read: now},
                filesystems: [], io_errors: 0, writes: {since_boot_bytes: 1200000, per_day_bytes: 2400000, per_day_days: 0.5, per_day_basis: 'boot'}, verdict: 'good', reasons: [],
            },
        ],
    },
    replace: {
        verdict: 'replace', kernel_log: true,
        reasons: [
            {level: 'replace', code: 'smart-failed', device: 'sda'},
            {level: 'replace', code: 'io-errors', device: 'sda', count: 7},
        ],
        devices: [
            {
                name: 'sda', kind: 'sata', model: 'KINGSTON SA400S37240G', capacity_bytes: 240057409536, mounts: ['/', '/usr/local'],
                smart: {available: true, passed: false, model: 'KINGSTON SA400S37240G', power_on_hours: 31022, wear_percent: 88, reallocated_sectors: 1520, pending_sectors: 16, temperature_c: 41, read: now},
                filesystems: [{name: 'sda3', mounts: ['/usr/local'], errors: 0, lifetime_write_bytes: 88000000000}],
                io_errors: 7, last_io_error: 'I/O error, dev sda, sector 20480 op 0x1:(WRITE) flags 0x800 phys_seg 1 prio class 0', last_io_error_at: now,
                writes: {since_boot_bytes: 310000000, per_day_bytes: 95000000, per_day_days: 28, per_day_basis: 'samples'}, verdict: 'replace',
                reasons: [
                    {level: 'replace', code: 'smart-failed', device: 'sda'},
                    {level: 'replace', code: 'io-errors', device: 'sda', count: 7},
                ],
            },
            {
                name: 'nvme0n1', kind: 'nvme', model: 'WD Blue SN570 500GB', capacity_bytes: 500107862016, mounts: [],
                smart: {available: true, passed: true, model: 'WD Blue SN570 500GB', power_on_hours: 9120, wear_percent: 83, media_errors: 0, temperature_c: 44, read: now},
                filesystems: [], io_errors: 0, writes: {since_boot_bytes: 0}, verdict: 'watch',
                reasons: [{level: 'watch', code: 'wear', device: 'nvme0n1', percent: 83}],
            },
        ],
    },
    // the Charly's eight-year-old card, if something wrote 620 MB a day to it
    'old-sd': {
        verdict: 'watch', kernel_log: true,
        reasons: [{level: 'watch', code: 'sd-age-writes', device: 'mmcblk0', years: 8, bytes_per_day: 620000000}],
        devices: [{
            name: 'mmcblk0', kind: 'sd', vendor: 'Samsung', vendor_id: '0x00001b', model: 'GB1QT', capacity_bytes: 32010928128, manufactured: '2018-07', age_months: 98,
            mounts: ['/', '/usr/local'],
            filesystems: [{name: 'mmcblk0p3', mounts: ['/usr/local'], errors: 0, lifetime_write_bytes: 43030393 * 1024}],
            io_errors: 0, writes: {since_boot_bytes: 8472576, per_day_bytes: 620000000, per_day_days: 9, per_day_basis: 'samples'}, verdict: 'watch',
            reasons: [{level: 'watch', code: 'sd-age-writes', device: 'mmcblk0', years: 8, bytes_per_day: 620000000}],
        }],
    },
    // task 111: an LXC container lists its host's disks from /sys/block; no SMART is read for them
    container: {
        verdict: 'good', reasons: [], kernel_log: true, health_source: 'host',
        devices: [{
            name: 'sda', kind: 'sata', model: 'Samsung SSD 870 EVO 1TB', capacity_bytes: 1000204886016, mounts: [],
            filesystems: [], io_errors: 0, writes: {since_boot_bytes: 918000000}, verdict: 'good', reasons: [],
        }],
    },
    virtual: {
        verdict: 'good', reasons: [], kernel_log: true, health_source: 'host',
        devices: [{
            name: 'sda', kind: 'virtio', virtual: true, vendor: 'QEMU', model: 'QEMU HARDDISK', capacity_bytes: 34359738368, mounts: ['/', '/boot', '/usr/local'],
            filesystems: [
                {name: 'sda2', mounts: ['/'], errors: 0, lifetime_write_bytes: 484961 * 1024},
                {name: 'sda3', mounts: ['/usr/local'], errors: 0, lifetime_write_bytes: 136273930 * 1024},
            ],
            io_errors: 0, writes: {since_boot_bytes: 5411307520, per_day_bytes: 3510000000, per_day_days: 1.55, per_day_basis: 'boot'}, verdict: 'good', reasons: [],
        }],
    },
};
// task 50: own timers, in memory (the projects share the stub: a test creates its timer under a
// name of its own). GET /timers lists them after the fixture's shipped ones.
const ownTimers = new Map(); // name -> {name, unit, service, timer_file, service_file, enabled}
function timersView() {
    const t = structuredClone(routes['GET /api/system/v1/timers']);
    for (const o of ownTimers.values()) {
        t.timers.push({unit: o.unit, activates: o.service, next: o.enabled ? new Date(Date.now() + 3600000).toISOString() : undefined, last: undefined, left: o.enabled ? '1h' : undefined, active: o.enabled, own: true, enabled: o.enabled, unit_file_state: o.enabled ? 'enabled-runtime' : 'disabled'});
    }
    return t;
}
function timerFilesProblem(timer, service) {
    if (!/^\s*\[Timer\]\s*$/m.test(timer ?? '')) return 'the timer file has no [Timer] section';
    if (!/^\s*\[Service\]\s*$/m.test(service ?? '')) return 'the service file has no [Service] section';
    return '';
}

const MIME = {'.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml', '.ico': 'image/x-icon'};

const sseClients = new Set();
// tasks 45 and 47: every metadata write the stub received, with the x-stub-client header the spec set -
// the projects and tests share this stub, so a spec reads back only its own writes and can
// assert that one drop was exactly one request
const metaWrites = [];
// openccu-lite task 100: the addon images, as the box serves them - the type by content, nosniff,
// the policy that keeps an SVG a picture, an hour of private caching. One SVG per addon and
// variant, told apart by colour (the dark one is light, so it reads on a dark background).
// Each has a viewBox and no width or height, as OpenCCU-Loom's icon.svg: no intrinsic size, which
// a slot that sized the image auto/auto laid out 0×0 (occulited B-43) - keep them so.
const IMAGE_ADDONS = {iobroker: {letter: 'I', light: '#2a6fd6', dark: '#9cc2ff'}, mosquitto: {letter: 'M', light: '#3c5280', dark: '#b8c8ea'}, 'tm-devices': {letter: 'T', light: '#7a3e9d', dark: '#d7b3ec'}, 'xml-api': {letter: 'X', light: '#2e7d32', dark: '#a5d6a7'}};
function imageRoute(req, u, res) {
    const m = /^\/api\/system\/v1\/(addons|catalog)\/([^/]+)\/images\/(icon|icon-dark|logo|logo-dark)$/.exec(u.pathname);
    if (!m || req.method !== 'GET') return false;
    const a = IMAGE_ADDONS[decodeURIComponent(m[2])];
    if (!a || (m[2] === 'mosquitto' && !m[3].startsWith('logo')) || (m[2] === 'xml-api' && !m[3].startsWith('logo')) || (m[2] === 'tm-devices' && m[3] !== 'icon')) {
        sendJSON(res, {error: 'not-found', message: 'the addon declares no such image'}, 404);
        return true;
    }
    const dark = m[3].endsWith('-dark');
    const wide = m[3].startsWith('logo');
    const fill = dark ? a.dark : a.light;
    const text = dark ? '#1b1b1f' : '#ffffff';
    const svg = wide
        ? `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 48"><rect width="120" height="48" rx="8" fill="${fill}"/><text x="60" y="32" font-family="sans-serif" font-size="24" font-weight="700" text-anchor="middle" fill="${text}">${a.letter}${a.letter}${a.letter}</text></svg>`
        : `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="10" fill="${fill}"/><text x="24" y="33" font-family="sans-serif" font-size="26" font-weight="700" text-anchor="middle" fill="${text}">${a.letter}</text></svg>`;
    res.writeHead(200, {'Content-Type': 'image/svg+xml', 'X-Content-Type-Options': 'nosniff', 'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'", 'Cross-Origin-Resource-Policy': 'same-origin', 'Cache-Control': 'private, max-age=3600'});
    res.end(svg);
    return true;
}

const srv = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x');
    const key = `${req.method} ${u.pathname}`;
    if (imageRoute(req, u, res)) return;
    if (refusedWithoutHeader(req, u.pathname)) {
        console.error(`stub: ${key} without X-Occulite-Request (task 259)`);
        res.writeHead(403, {'Content-Type': 'application/json'});
        return res.end(JSON.stringify({error: 'request-header', message: 'a state-changing call with the session cookie alone must carry the header X-Occulite-Request'}));
    }
    if (req.method === 'GET' && u.pathname === '/__stub/meta-writes') {
        const client = u.searchParams.get('client') ?? '';
        res.writeHead(200, {'Content-Type': 'application/json'});
        res.end(JSON.stringify(metaWrites.filter((w) => w.client === client).map(({client: _, ...w}) => w)));
        return;
    }
    // task 43: the metadata writes of the editor. The snapshot is shared by the three projects, so
    // a test creates what it checks under a name of its own; every write bumps the revision and
    // goes out on the change stream, which is how the page learns of it (it re-reads the snapshot).
    const metaMatch = /^\/api\/meta\/v1\/(enums(?:\/([^/]+)(?:\/nodes(?:\/(.+))?)?)?|objects\/(.+))$/.exec(u.pathname);
    if (metaMatch && req.method !== 'GET') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const snap = routes['GET /api/meta/v1/snapshot'];
            const b = body ? JSON.parse(body) : {};
            const client = req.headers['x-stub-client'];
            if (client) metaWrites.push({client, method: req.method, path: decodeURIComponent(u.pathname), query: u.search, body: b});
            const enumId = metaMatch[2] ? decodeURIComponent(metaMatch[2]) : undefined;
            const nodePath = metaMatch[3] ? metaMatch[3].split('/').map(decodeURIComponent) : undefined;
            const ref = metaMatch[4] ? decodeURIComponent(metaMatch[4]) : undefined;
            const find = (ids) => { let nodes = snap.enums[enumId]?.tree ?? []; let n; for (const id of ids) { n = nodes.find((x) => x.id === id); if (!n) return undefined; nodes = n.children ?? []; } return n; };
            const parentList = (parent) => { if (!parent) return snap.enums[enumId].tree; const p = find(parent.split('/').slice(1)); p.children ??= []; return p.children; };
            const detach = (prefix) => { for (const o of Object.values(snap.objects)) o.enums = o.enums.filter((e) => e !== prefix && !e.startsWith(prefix + '/')); };
            let event = null;
            let status = 200;
            try {
                if (ref !== undefined && req.method === 'PATCH') {
                    const o = snap.objects[ref] ?? (snap.objects[ref] = {name: '', enums: [], meta: {}});
                    if (b.name !== undefined) o.name = b.name;
                    // task 193: meta merged per namespace, null removes one (docs/meta-api.md)
                    if (b.meta && typeof b.meta === 'object') for (const [ns, v] of Object.entries(b.meta)) { if (v === null) delete o.meta[ns]; else o.meta[ns] = v; }
                    if (b.enums !== undefined) o.enums = b.enums;
                    event = {kind: 'object.updated', ref, value: o};
                } else if (enumId === undefined && req.method === 'POST') {
                    if (snap.enums[b.id]) { status = 409; event = {error: 'duplicate-id', message: `enum ${b.id} exists`}; }
                    else { snap.enums[b.id] = {name: b.name, tree: []}; event = {kind: 'enum.created', enum: b.id, value: snap.enums[b.id]}; }
                } else if (nodePath === undefined && enumId !== undefined && !u.pathname.endsWith('/nodes')) {
                    if (req.method === 'PATCH') { snap.enums[enumId].name = b.name; event = {kind: 'enum.updated', enum: enumId, value: snap.enums[enumId]}; }
                    else if (req.method === 'DELETE') {
                        const members = Object.keys(snap.objects).filter((r) => snap.objects[r].enums.some((e) => e === enumId || e.startsWith(enumId + '/')));
                        if (members.length && u.searchParams.get('members') !== 'detach') { status = 409; event = {error: 'has-members', message: 'the enum has members', detail: {refs: members}}; }
                        else { detach(enumId); delete snap.enums[enumId]; event = {kind: 'enum.deleted', enum: enumId}; }
                    }
                } else if (nodePath === undefined && req.method === 'POST') {
                    const list = parentList(b.parent);
                    list.push({id: b.id, name: b.name});
                    status = 201;
                    event = {kind: 'node.created', enum: enumId, path: `${b.parent ?? enumId}/${b.id}`, value: {id: b.id, name: b.name}};
                } else if (nodePath !== undefined && req.method === 'PATCH') {
                    const node = find(nodePath);
                    const from = `${enumId}/${nodePath.join('/')}`;
                    if (b.name !== undefined) { node.name = b.name; event = {kind: 'node.updated', enum: enumId, path: from, value: node}; }
                    // task 193: a position among the siblings (the App's drag sorting)
                    if (b.position !== undefined) {
                        const list = nodePath.length > 1 ? find(nodePath.slice(0, -1)).children : snap.enums[enumId].tree;
                        const i = list.indexOf(node);
                        if (i >= 0) { list.splice(i, 1); list.splice(Math.max(0, Math.min(b.position, list.length)), 0, node); }
                        event = {kind: 'node.updated', enum: enumId, path: from, value: node};
                    }
                    if (b.parent !== undefined) {
                        const oldList = parentList(nodePath.length > 1 ? `${enumId}/${nodePath.slice(0, -1).join('/')}` : null);
                        oldList.splice(oldList.indexOf(node), 1);
                        parentList(b.parent).push(node);
                        const to = `${b.parent ?? enumId}/${node.id}`;
                        for (const o of Object.values(snap.objects)) o.enums = o.enums.map((e) => (e === from || e.startsWith(from + '/') ? to + e.slice(from.length) : e));
                        event = {kind: 'node.moved', enum: enumId, from, to};
                    }
                } else if (nodePath !== undefined && req.method === 'DELETE') {
                    const path = `${enumId}/${nodePath.join('/')}`;
                    const members = Object.keys(snap.objects).filter((r) => snap.objects[r].enums.some((e) => e === path || e.startsWith(path + '/')));
                    if (members.length && u.searchParams.get('members') !== 'detach') { status = 409; event = {error: 'has-members', message: 'the node has members', detail: {refs: members}}; }
                    else { const node = find(nodePath); const list = parentList(nodePath.length > 1 ? `${enumId}/${nodePath.slice(0, -1).join('/')}` : null); list.splice(list.indexOf(node), 1); detach(path); event = {kind: 'node.deleted', enum: enumId, path}; }
                }
            } catch (e) {
                status = 500; event = {error: 'internal', message: String(e)};
            }
            if (status < 400 && event) {
                snap.revision += 1;
                event.revision = snap.revision;
                for (const client of sseClients) client.write(`data: ${JSON.stringify(event)}\n\n`);
            }
            res.writeHead(status, {'Content-Type': 'application/json'});
            res.end(JSON.stringify(status < 400 ? {revision: snap.revision, ...(event ?? {})} : event));
        });
        return;
    }
    if (req.method === 'GET' && u.pathname === '/api/meta/v1/events/sse') {
        res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive'});
        res.write(': hello\n\n');
        sseClients.add(res);
        const beat = setInterval(() => res.write(': heartbeat\n\n'), 15000);
        req.on('close', () => { clearInterval(beat); sseClients.delete(res); });
        return;
    }
    // task 64: the log download - the stub's lines through the page's filters (tag, unit, severity,
    // text), oldest first, as the file the box sends: text in journalctl's short-iso shape or JSON
    // lines, the range in the name (the download time without one)
    if (req.method === 'GET' && u.pathname === '/api/system/v1/log/download') {
        const format = u.searchParams.get('format') || 'text';
        if (format !== 'text' && format !== 'json') return sendJSON(res, {error: 'invalid', message: 'format must be text or json'}, 422);
        const needle = (u.searchParams.get('q') ?? '').toLowerCase();
        const lines = structuredClone(routes['GET /api/system/v1/log'].lines)
            .filter((l) => logMatches(l, u.searchParams) && (!needle || l.message.toLowerCase().includes(needle)))
            .sort((a, b) => a.timestamp.localeCompare(b.timestamp));
        const now = new Date();
        const stamp = (d) => d.toISOString().slice(0, 16).replace(':', '');
        const UNIT = {min: 60e3, h: 3600e3, d: 86400e3};
        const at = (v) => {
            if (/^@\d+$/.test(v)) return stamp(new Date(Number(v.slice(1)) * 1000));
            const rel = /^-(\d+)(min|h|d)$/.exec(v);
            if (rel) return stamp(new Date(now.getTime() - Number(rel[1]) * UNIT[rel[2]]));
            return v.replace(/[^A-Za-z0-9.]+/g, '-');
        };
        const since = u.searchParams.get('since') ?? '';
        const until = u.searchParams.get('until') ?? '';
        const span = since && until ? `${at(since)}-${at(until)}` : since ? `${at(since)}-${stamp(now)}` : until ? `until-${at(until)}` : stamp(now);
        const ext = format === 'json' ? 'jsonl' : 'txt';
        const body = lines.map((l) => (format === 'json' ? JSON.stringify(l) : `${l.timestamp.replace(/\.\d+Z$/, '+00:00')} openccu ${l.tag}[${l.pid}]: ${l.message}`) + '\n').join('');
        res.writeHead(200, {
            'Content-Type': format === 'json' ? 'application/x-ndjson' : 'text/plain; charset=utf-8',
            'Content-Disposition': `attachment; filename="openccu-log-${span}.${ext}"`,
            'Cache-Control': 'no-store',
        });
        return res.end(body);
    }
    // task 93: the boot timeline of this boot; no other boot has one here
    if (req.method === 'GET' && u.pathname === '/api/system/v1/boot') {
        const id = (u.searchParams.get('id') ?? '').toLowerCase();
        if (id === STUB_BOOT_PREV) return sendJSON(res, {...STUB_TIMELINE_PREV, started: new Date(Date.now() - 30 * 3600e3).toISOString(), previous: keptInfo(STUB_TIMELINE_KEPT, 70)});
        if (id === STUB_BOOT_KEPT) return sendJSON(res, {...STUB_TIMELINE_KEPT, started: new Date(Date.now() - 70 * 3600e3).toISOString()});
        if (id && id !== '0' && id !== STUB_BOOT) return sendJSON(res, {error: 'no-timeline', message: 'there is no timeline of that boot'}, 404);
        const started = new Date(Date.now() - 9 * 3600e3).toISOString();
        const cookie = req.headers.cookie ?? '';
        // B-153: stub-boot-restarted=1 - rfd, hmipserver and occulited restarted since the boot, served
        // from the kept snapshot with the states of now; stub-boot-lost=1 - no snapshot, so the first
        // read after occulited's restart has lost them and names them
        const again = {'rfd.service': 1308, 'hmipserver.service': 1310, 'occulited.service': 7041};
        if (/(^|;\s*)stub-boot-restarted=1/.test(cookie)) {
            const units = STUB_TIMELINE.units.map((x) => (x.id in again ? {...x, restarted: again[x.id], state: x.id === 'hmipserver.service' ? 'activating' : x.state} : x));
            return sendJSON(res, {...STUB_TIMELINE, units, later: 1, later_units: ['fstrim.service'], started, previous: keptInfo(STUB_TIMELINE_PREV, 30)});
        }
        if (/(^|;\s*)stub-boot-lost=1/.test(cookie)) {
            const units = STUB_TIMELINE.units.filter((x) => !(x.id in again));
            return sendJSON(res, {...STUB_TIMELINE, units, later: 3, later_units: Object.keys(again).sort(), started, previous: keptInfo(STUB_TIMELINE_PREV, 30)});
        }
        return sendJSON(res, {...STUB_TIMELINE, started, previous: keptInfo(STUB_TIMELINE_PREV, 30)});
    }
    // task 93: the journal's boots - this one and two earlier ones; stub-journal-ram=1 is a journal
    // in RAM, which holds this boot alone
    if (req.method === 'GET' && u.pathname === '/api/system/v1/boots') {
        const ram = /(^|;\s*)stub-journal-ram=1/.test(req.headers.cookie ?? '');
        const hour = 3600e3;
        const t0 = Date.now();
        const iso = (ms) => new Date(ms).toISOString();
        const boots = [
            {index: 0, boot_id: STUB_BOOT, first: iso(t0 - 9 * hour), last: iso(t0), current: true, journal: true, snapshot: true, total_ms: STUB_TIMELINE.summary.total_ms},
            {index: -1, boot_id: STUB_BOOT_PREV, first: iso(t0 - 30 * hour), last: iso(t0 - 10 * hour), current: false, journal: true, snapshot: true, total_ms: STUB_TIMELINE_PREV.summary.total_ms},
            {index: -2, boot_id: STUB_BOOT_OLDEST, first: iso(t0 - 60 * hour), last: iso(t0 - 31 * hour), current: false, journal: true},
        ];
        // task 93: kept for the Services page, but no longer in the journal (in RAM, no earlier boot is)
        const keptOnly = [{boot_id: STUB_BOOT_KEPT, first: iso(t0 - 70 * hour), current: false, journal: false, snapshot: true, total_ms: STUB_TIMELINE_KEPT.summary.total_ms}];
        const inRam = [boots[0], {...boots[1], index: undefined, last: undefined, journal: false}];
        return sendJSON(res, {current: STUB_BOOT, source: 'journald', persistent: !ram, boots: ram ? [...inRam, ...keptOnly] : [...boots, ...keptOnly]});
    }
    // task 102: one run's lines, as the journal answers OCCULITE_RUN_ID; nothing of the run is run_missing
    // task 79: the trace's lines under their tag
    if (req.method === 'GET' && u.pathname === '/api/system/v1/log' && u.searchParams.get('tag') === 'rpc-trace') {
        const start = Date.now() - 60000;
        return sendJSON(res, {source: 'journald', lines: TRACE_LINES.map((message, i) => ({time: new Date(start + i * 1000).toISOString(), timestamp: new Date(start + i * 1000).toISOString(), severity: 'debug', tag: 'rpc-trace', unit: 'occulited.service', pid: 4711, message}))});
    }
    if (req.method === 'GET' && u.pathname === '/api/system/v1/log' && u.searchParams.get('run')) {
        const rows = STUB_RUNS[u.searchParams.get('run')];
        if (!rows) return sendJSON(res, {source: 'journald', lines: [], run_missing: true});
        const t0 = Date.now() - 60e3;
        const lines = rows
            .map(([tag, severity, message], i) => {
                const ts = new Date(t0 + i * 1000).toISOString();
                return {time: ts, timestamp: ts, severity, tag, message};
            })
            .filter((l) => logMatches(l, u.searchParams));
        return sendJSON(res, {source: 'journald', lines});
    }
    // task 93: the kernel log (kernel=1), dmesg's lines with their stamps since the boot, and an
    // earlier boot's journal - both told apart from this boot's by their messages
    if (req.method === 'GET' && u.pathname === '/api/system/v1/log') {
        const boot = u.searchParams.get('boot') ?? '';
        const earlier = boot !== '' && boot !== '0' && boot !== STUB_BOOT;
        const kernel = u.searchParams.get('kernel') === '1';
        if (kernel || earlier) {
            const start = Date.now() - (earlier ? 30 : 9) * 3600e3;
            let lines;
            if (kernel) {
                lines = STUB_KERNEL.map(([us, severity, message]) => ({
                    time: new Date(start + us / 1000).toISOString(),
                    timestamp: new Date(start + us / 1000).toISOString(),
                    severity,
                    tag: 'kernel',
                    facility: '0',
                    monotonic_us: us,
                    message: earlier ? `${message} (earlier boot)` : message,
                }));
            } else {
                lines = structuredClone(routes['GET /api/system/v1/log'].lines);
                for (const l of lines) l.message = l.message.replace('Sample log line', 'Earlier boot line');
            }
            const needle = (u.searchParams.get('q') ?? '').toLowerCase();
            lines = lines.filter((l) => logMatches(l, u.searchParams) && (!needle || l.message.toLowerCase().includes(needle)));
            return sendJSON(res, {source: 'journald', lines});
        }
    }
    // B-59: the log honours the tag and unit filters, as journalctl does (-t and -u are ANDed)
    if (req.method === 'GET' && u.pathname === '/api/system/v1/log') {
        const r = structuredClone(routes['GET /api/system/v1/log']);
        // task 178: a long boot - the cookie names its entries - served in pages, each line with
        // a cursor; every seventh line is long enough to wrap, as a real log's are
        const long = Number((/(^|;\s*)stub-log-pages=(\d+)/.exec(req.headers.cookie ?? '') ?? [])[2] ?? 0);
        if (long > 0) {
            const t0 = Date.now() - long * 1000;
            r.lines = Array.from({length: long}, (_, i) => ({
                time: new Date(t0 + i * 1000).toISOString(),
                timestamp: new Date(t0 + i * 1000).toISOString(),
                severity: ['info', 'info', 'notice', 'warning'][i % 4],
                tag: ['occulited', 'rfd', 'hmipserver'][i % 3],
                unit: ['occulited', 'rfd', 'hmipserver'][i % 3],
                pid: 1000 + (i % 3),
                message: `Boot line ${i}` + (i % 7 === 0 ? ': a longer message, the kind that wraps at the window\'s edge and so takes two or three rows of the list, not one like the others' : ''),
            }));
        }
        // the churned lines belong to the log too, so a reload after a while sees their tags
        if (/(^|;\s*)stub-log-churn=1/.test(req.headers.cookie ?? '')) r.lines.push(...churned);
        r.lines.forEach((l, i) => (l.cursor = `c${i}`));
        r.lines = r.lines.filter((l) => logMatches(l, u.searchParams));
        // the page: the tail, before or after a cursor, or the head; and whether the log goes on
        const limit = Number(u.searchParams.get('limit') || 500);
        const before = u.searchParams.get('before');
        const after = u.searchParams.get('after');
        const at = (c) => r.lines.findIndex((l) => l.cursor === c);
        r.older = false;
        r.newer = false;
        if (u.searchParams.get('head') === '1') {
            r.newer = r.lines.length > limit;
            r.lines = r.lines.slice(0, limit);
        } else if (after) {
            const i = at(after);
            r.older = true;
            r.newer = r.lines.length > i + 1 + limit;
            r.lines = r.lines.slice(i + 1, i + 1 + limit);
        } else if (before) {
            const i = at(before);
            r.older = i > limit;
            r.newer = true;
            r.lines = r.lines.slice(Math.max(0, i - limit), i);
        } else {
            r.older = r.lines.length > limit;
            r.lines = r.lines.slice(-limit);
        }
        // a slow journal (the cookie names the milliseconds): a filtered query on a Pi's journal
        // takes seconds, and two answers can arrive in the wrong order
        const delay = Number((/(^|;\s*)stub-log-delay=(\d+)/.exec(req.headers.cookie ?? '') ?? [])[2] ?? 0);
        setTimeout(() => {
            res.writeHead(200, {'Content-Type': 'application/json'});
            res.end(JSON.stringify(r));
        }, delay * (u.searchParams.get('tag') ? 1 : 2));
        return;
    }
    // task 88 (D-67): a box whose RedMatic reads the gate's session header - 9.7.3, the version its
    // catalogue entry declares - so occulited answers session_header for its frontend and its settings
    // page, and the shell leaves ?sid= off both; every other addon keeps it. Only with the cookie
    // `stub-session-header=1`, so the other tests keep RedMatic 9.4.0.
    if (req.method === 'GET' && (u.pathname === '/api/system/v1/nav' || u.pathname === '/api/system/v1/addons') && /(^|;\s*)stub-session-header=1/.test(req.headers.cookie ?? '')) {
        const jar = cookieJar(req);
        res.writeHead(200, {'Content-Type': 'application/json'});
        if (u.pathname === '/api/system/v1/nav') return res.end(JSON.stringify({entries: withLegacyNav(jar, NAV.map((e) => (e.addon === 'redmatic' ? {...e, session_header: true} : e)))}));
        return res.end(JSON.stringify({addons: withLegacyAddons(jar, ADDONS.map((a) => (a.id === 'redmatic' ? {...a, version: '9.7.3', session_header: true} : a)))}));
    }
    // task 55: the addon list with the switched-off NEO Server in it, on request (stub-addon-off=1);
    // task 59: and with a third frontend addon, ioBroker, in the list and in the nav (stub-addon-more=1)
    {
        const jar = cookieJar(req);
        const off = jar['stub-addon-off'] === '1';
        const more = jar['stub-addon-more'] === '1';
        const rejected = jar['stub-addon-rejected'] === '1';
        if (req.method === 'GET' && u.pathname === '/api/system/v1/addons' && (off || more || rejected)) {
            res.writeHead(200, {'Content-Type': 'application/json'});
            return res.end(JSON.stringify({addons: [...ADDONS, ...(more ? [ADDON_MORE] : []), ...(off ? [ADDON_OFF] : []), ...(rejected ? [ADDON_REJECTED] : [])]}));
        }
        const navPage = jar['stub-nav-page'] === '1';
        if (req.method === 'GET' && u.pathname === '/api/system/v1/nav' && (more || navPage)) {
            res.writeHead(200, {'Content-Type': 'application/json'});
            const entries = [...NAV, ...(more ? [NAV_MORE] : []), ...(navPage ? [NAV_PAGE] : [])].sort((a, b) => a.order - b.order);
            return res.end(JSON.stringify({entries}));
        }
        if (req.method === 'GET' && u.pathname === '/stub-manual/') {
            res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
            return res.end('<!doctype html><title>Manual</title><h1>Manual</h1>');
        }
        // task 59: the caller's shell preferences - pins and order of the addon dropdown. Kept per
        // browser (stub-prefs=<id>) so the parallel specs do not see each other's pins; without the
        // cookie a PUT is answered as the box would but not kept.
        if (u.pathname === '/api/auth/v1/me/preferences') {
            const id = jar['stub-prefs'] ?? '';
            if (req.method === 'GET') return sendJSON(res, prefsStore.get(id) ?? {addons: []});
            if (req.method === 'PUT') {
                let body = '';
                req.on('data', (c) => { body += c; });
                req.on('end', () => {
                    let b;
                    try { b = JSON.parse(body); } catch { return sendJSON(res, {error: 'invalid-body', message: 'not JSON'}, 422); }
                    if (!Array.isArray(b?.addons) || b.addons.some((a) => typeof a?.id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(a.id))) return sendJSON(res, {error: 'invalid-body', message: 'addons must be a list of {id, pinned?}'}, 422);
                    if (b.start_page !== undefined && !['', 'app', 'status'].includes(b.start_page)) return sendJSON(res, {error: 'invalid-body', message: 'invalid preferences: start_page is app or status'}, 422);
                    // task 193: the App's choices travel with the pins; occulited task 24: an addon's
                    // whole-window flag rides on its entry
                    const stored = {addons: b.addons.map((a) => ({id: a.id, ...(a.pinned === true ? {pinned: true} : {}), ...(a.fullscreen === true ? {fullscreen: true} : {})})), ...(b.start_page ? {start_page: b.start_page} : {}), ...(b.app_fullscreen === true ? {app_fullscreen: true} : {}), ...(b.app_hidden === true ? {app_hidden: true} : {})};
                    if (id) prefsStore.set(id, stored);
                    sendJSON(res, stored);
                });
                return;
            }
        }
    }
    // task 39: every framed stand-in page counts its loads in the tab's sessionStorage (shared with
    // the shell, same origin), has an input, and keeps what the shell posts to it - so a test can
    // tell a kept frame from a reloaded one
    const FRAME_PROBE = '<input id="probe" aria-label="probe"><script>(function(){var k="stub.loads:"+location.pathname;sessionStorage.setItem(k,String(Number(sessionStorage.getItem(k)||0)+1));window.__looks=[];addEventListener("message",function(e){window.__looks.push(e.data)});})()</script>';
    // B-132: an addon that sends its frame back to the box's own page (homematic-manager's B-32: its
    // session check fails and it redirects to `/`). With the cookie every page under /addons/mh/ - the
    // frontend and the settings page - answers a redirect to the shell, so a test can open the addon
    // and see what the frame then does.
    if (req.method === 'GET' && u.pathname.startsWith('/addons/mh/') && /(^|;\s*)stub-frame-loop=1/.test(req.headers.cookie ?? '')) {
        res.writeHead(302, {Location: '/'});
        return res.end();
    }
    // B-133: homematic-manager's frontend as the box runs it - proxied to the addon's own server,
    // which checks a ?sid= it is handed against the box's API before anything else. The alias is
    // refused there (D-77), so a frame opened with ?sid= comes back as the box's page.
    if (req.method === 'GET' && u.pathname === '/addons/mh/' && u.searchParams.has('sid')) {
        res.writeHead(302, {Location: '/', 'Cache-Control': 'no-store'});
        return res.end();
    }
    // B-134: homematic-manager's settings.cgi - without cmd=config the CCU's hand-over into the app
    // (302 to the frontend, with its token cookie), with it the addon's settings page
    if (req.method === 'GET' && u.pathname === '/addons/mh/settings.cgi') {
        if (u.searchParams.get('cmd') !== 'config') {
            res.writeHead(302, {Location: '/addons/mh/', 'Set-Cookie': 'hmm_token=stub; Path=/addons/mh/; HttpOnly; SameSite=Strict', 'Cache-Control': 'no-store'});
            return res.end();
        }
        res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
        return res.end(`<!doctype html><body style="font:13px system-ui;padding:20px"><h1>Homematic-Manager settings stub</h1><p>Anmeldung / Login</p>${FRAME_PROBE}</body>`);
    }
    // task 55: RedMatic's frontend declares its favicon the way Node-RED's editor template does
    // (`<link rel="icon" type="image/png" href="favicon.ico">`); Homematic-Manager's page is the
    // stand-in below, which declares none, and there is no favicon.ico beside it - a 404, as on a box
    if (req.method === 'GET' && u.pathname === '/addons/red/') {
        res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
        return res.end(`<!doctype html><html><head><title>Node-RED</title><link rel="icon" type="image/png" href="favicon.ico"></head><body style="font:13px system-ui;padding:20px">Node-RED stub${FRAME_PROBE}</body></html>`);
    }
    if (req.method === 'GET' && u.pathname === '/addons/red/favicon.ico') {
        res.writeHead(200, {'Content-Type': 'image/png'});
        return res.end(Buffer.from(NODE_RED_ICON, 'base64'));
    }
    if (/^\/addons\/.+\/favicon\.ico$/.test(u.pathname)) {
        res.writeHead(404, {'Content-Type': 'text/plain'});
        return res.end('not found');
    }
    if (variant(req, u, res)) return;
    // task 50: the timers list with the own timers, their CRUD, run now, the calendar check, and
    // the Services switch on an own timer (the other switches stay the empty ok below)
    const timersMatch = /^\/api\/system\/v1\/timers(?:\/(own|calendar)(?:\/([^/]+)(?:\/(run))?)?)?$/.exec(u.pathname);
    const switchMatch = req.method === 'POST' && /^\/api\/system\/v1\/services\/local-([A-Za-z0-9_-]+)\.timer\/(enable|disable)$/.exec(u.pathname);
    if (timersMatch || switchMatch) {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const send = (status, obj) => {
                res.writeHead(status, {'Content-Type': 'application/json'});
                res.end(JSON.stringify(obj));
            };
            let b = {};
            try {
                b = body ? JSON.parse(body) : {};
            } catch (e) {
                return send(400, {error: 'bad-request', message: String(e)});
            }
            if (switchMatch) {
                const t = ownTimers.get(switchMatch[1]);
                if (!t) return send(422, {error: 'service-control', message: `systemctl ${switchMatch[2]} local-${switchMatch[1]}.timer: exit status 1`, output: ''});
                t.enabled = switchMatch[2] === 'enable';
                return send(200, {ok: true, output: ''});
            }
            const [, kind, id, run] = timersMatch;
            const name = id ? decodeURIComponent(id).replace(/^local-(.+)\.timer$/, '$1') : undefined;
            if (!kind && req.method === 'GET') return send(200, timersView());
            if (kind === 'calendar' && !id && req.method === 'POST') {
                const expr = String(b.expression ?? '').trim();
                if (!expr || /bad|bogus/.test(expr)) return send(422, {error: 'invalid', message: `systemd-analyze calendar: Failed to parse calendar specification '${expr}': Invalid argument`, output: ''});
                const next = [1, 2, 3].map((d) => new Date(Date.now() + d * 86400000).toISOString().replace('T', ' ').slice(0, 19) + ' UTC');
                return send(200, {available: true, expression: expr, normalized: expr, next});
            }
            if (kind === 'own' && !id) {
                if (req.method === 'GET') return send(200, {timers: [...ownTimers.values()], analyze: true});
                if (req.method === 'POST') {
                    const nm = String(b.name ?? '').replace(/^local-(.+)\.timer$/, '$1');
                    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/.test(nm)) return send(422, {error: 'invalid', message: 'a timer name is 1 to 32 letters, digits, - and _, starting with a letter or a digit'});
                    if (ownTimers.has(nm) || routes['GET /api/system/v1/timers'].timers.some((x) => x.unit === `local-${nm}.timer`)) return send(409, {error: 'exists', message: `an own timer named ${nm} exists already`});
                    const problem = timerFilesProblem(b.timer, b.service);
                    if (problem) return send(422, {error: 'invalid', message: problem});
                    const t = {name: nm, unit: `local-${nm}.timer`, service: `local-${nm}.service`, timer_file: b.timer, service_file: b.service, enabled: true};
                    ownTimers.set(nm, t);
                    return send(201, t);
                }
            }
            if (kind === 'own' && id) {
                const t = ownTimers.get(name);
                if (!t) return send(404, {error: 'not-found', message: `no own timer named ${name}`});
                if (run && req.method === 'POST') return send(200, {ok: true, output: ''});
                if (!run && req.method === 'GET') return send(200, t);
                if (!run && req.method === 'PUT') {
                    const problem = timerFilesProblem(b.timer, b.service);
                    if (problem) return send(422, {error: 'invalid', message: problem});
                    t.timer_file = b.timer;
                    t.service_file = b.service;
                    return send(200, t);
                }
                if (!run && req.method === 'DELETE') {
                    ownTimers.delete(name);
                    return send(200, {ok: true});
                }
            }
            return send(405, {error: 'method', message: `${req.method} ${u.pathname} is not in the stub`});
        });
        return;
    }
    // task 85: the Journal panel's save, for looking at the page - the settings over the view, the
    // derived fields recomputed, nothing kept (the specs answer their own saves)
    if (key === 'PUT /api/system/v1/journal') {
        let raw = '';
        req.on('data', (c) => (raw += c));
        req.on('end', () => {
            let b;
            try {
                b = JSON.parse(raw || '{}');
            } catch (e) {
                return sendJSON(res, {error: 'bad-request', message: String(e)}, 400);
            }
            // task 216: a USB stick by its label, ram-sync only; task 228: a network share by its name
            const stick = /^usb:([^/\s]+)\/(.+)$/.exec(b.target ?? '');
            const share = /^share:([a-z][a-z0-9]{0,15})\/(.+)$/.exec(b.target ?? '');
            if (share && b.storage !== 'ram-sync' && b.storage !== 'ram') {
                return sendJSON(res, {error: 'invalid', message: 'persistent on a network share is not possible'}, 422);
            }
            if (b.target && b.target !== 'userfs' && !stick && !share) {
                return sendJSON(res, {error: 'invalid', message: `target "${b.target}" is neither userfs nor a USB stick as usb:<label>/<directory>`}, 422);
            }
            if (stick && b.storage !== 'ram-sync' && b.storage !== 'ram') {
                return sendJSON(res, {error: 'invalid', message: 'persistent on a USB stick is not possible'}, 422);
            }
            const view = {...routes['GET /api/system/v1/journal']};
            if (stick) {
                const on = routes['GET /api/system/v1/usb/storage'].sticks.find((x) => x.label_id === stick[1]);
                Object.assign(view, {target_label: stick[1], target_dir: stick[2], target_path: on ? `${on.mount}/${stick[2]}` : undefined, target_ok: !!on, target_usage: on ? 0 : null, target_free: on ? on.free_bytes : null});
            }
            if (share) Object.assign(view, {target_share: share[1], target_dir: share[2], target_path: `/media/net/${share[1]}/${share[2]}`, target_ok: true, target_usage: null, target_free: null});
            for (const k of ['storage', 'target', 'runtime_max_use', 'system_max_use', 'system_max_file', 'rate_limit_burst', 'sync_interval', 'target_max_use', 'target_max_age']) if (k in b) view[k] = b[k];
            view.target = view.target || 'userfs';
            view.persist = {persistent: '1', ram: '0', 'ram-sync': '0'}[view.storage] ?? '';
            // the stub's box is the VM, persistent now: a switch to ram-sync applies at once, one to RAM at the reboot
            const want = view.storage || view.default_storage;
            if (want === 'ram-sync') view.effective = 'ram-sync';
            view.reboot_pending = want === 'ram' && view.persistent;
            sendJSON(res, view);
        });
        return;
    }
    // task 214: the history's storage - the setting over the view, nothing kept (the specs answer their own)
    // task 219: client pairing - no request waits by default (the specs route one in)
    if (key === 'GET /api/auth/v1/pairing') return sendJSON(res, {enabled: true, requests: []});
    if (key === 'GET /api/auth/v1/pairing/stream') {
        res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store'});
        res.write('event: pairing\ndata: {"enabled":true,"requests":[]}\n\n');
        return; // held open like the real stream
    }
    if (key === 'PUT /api/auth/v1/pairing/settings') {
        let raw = '';
        req.on('data', (c) => (raw += c));
        req.on('end', () => sendJSON(res, {enabled: JSON.parse(raw || '{}').enabled !== false, requests: []}));
        return;
    }
    if (/^POST \/api\/auth\/v1\/pairing\/[^/]+\/(approve|reject)$/.test(key)) return sendJSON(res, {ok: true});
    if (/^PATCH \/api\/auth\/v1\/tokens\/[^/]+$/.test(key)) return sendJSON(res, {});
    // task 218: the network radio board - 192.0.2.99's board is another system's, 192.0.2.60 does not answer
    if (key === 'GET /api/system/v1/radio/hb-rf-eth') return sendJSON(res, hbView(''));
    if (key === 'POST /api/system/v1/radio/hb-rf-eth/find') {
        return sendJSON(res, {boards: [
            {address: '192.0.2.50', name: 'HB-RF-ETH-ABCDEF1234', serial: 'ABCDEF1234', firmware: '1.3.0', module_type: 'HM-MOD-RPI-PCB', module_serial: 'MEQ9000005', bidcos_address: '0x3D0A01', state: 'free'},
            {address: '192.0.2.99', name: 'HB-RF-ETH-9999', serial: '99999', firmware: '1.2.9', module_type: 'RPI-RF-MOD', connected_to: '192.0.2.7', state: 'other'},
        ]});
    }
    if (key === 'PUT /api/system/v1/radio/hb-rf-eth') {
        let raw = '';
        req.on('data', (c) => (raw += c));
        req.on('end', () => {
            const b = JSON.parse(raw || '{}');
            const a = (b.address ?? '').trim();
            if (a !== '' && !/^\d{1,3}(\.\d{1,3}){3}$/.test(a)) return sendJSON(res, {error: 'invalid', message: 'the HB-RF-ETH is addressed by IPv4 only (the kernel module takes no name and no IPv6): four numbers such as 192.168.1.50'}, 422);
            if (a === '192.0.2.99' && !b.take) return sendJSON(res, {error: 'in-use', message: 'the board is connected to 192.0.2.7: taking it takes the radio from that system'}, 409);
            sendJSON(res, hbView(a)); // stateless: parallel specs share the stub
        });
        return;
    }
    // task 195: the datapoint history's list
    if (key === 'PUT /api/system/v1/datastore/history') {
        let raw = '';
        req.on('data', (c) => (raw += c));
        req.on('end', () => {
            const b = JSON.parse(raw || '{}');
            const bad = [...(b.add ?? []), ...(b.remove ?? [])].find((n) => !/^[A-Z0-9_]{1,64}$/.test(n));
            if (bad) return sendJSON(res, {error: 'invalid', message: `"${bad}" is not a datapoint name (A-Z, 0-9, _)`}, 422);
            const view = structuredClone(routes['GET /api/system/v1/datastore/history']);
            view.add = b.add ?? [];
            view.remove = b.remove ?? [];
            for (const n of view.add) view.datapoints[n] ??= 'event';
            for (const n of view.remove) delete view.datapoints[n];
            sendJSON(res, view);
        });
        return;
    }
    if (key === 'PUT /api/system/v1/datastore') {
        let raw = '';
        req.on('data', (c) => (raw += c));
        req.on('end', () => {
            let b;
            try {
                b = JSON.parse(raw || '{}');
            } catch (e) {
                return sendJSON(res, {error: 'invalid-body', message: String(e)}, 422);
            }
            if (!['', 'ram', 'ram-sync', 'persistent'].includes(b.mode ?? '')) return sendJSON(res, {error: 'invalid', message: 'the storage is ram, ram-sync or persistent, or empty for the product\'s default'}, 422);
            if (b.sync_interval && !/^(15min|[1-9][0-9]*h|1d)$/.test(b.sync_interval)) return sendJSON(res, {error: 'invalid', message: 'the write interval is 15min to 1d'}, 422);
            if ((b.location ?? '').startsWith('share:')) return sendJSON(res, {error: 'invalid', message: 'the database cannot live on a network share'}, 422);
            // task 229: a USB stick takes ram-sync's snapshot only
            if ((b.location ?? '').startsWith('usb:') && (b.mode || 'persistent') !== 'ram-sync') return sendJSON(res, {error: 'invalid', message: 'a USB stick takes a copy of the database, made at every write of ram-sync'}, 422);
            const view = {...routes['GET /api/system/v1/datastore'], mode: b.mode ?? '', sync_interval: b.sync_interval ?? '', location: b.location ?? ''};
            if ((b.location ?? '').startsWith('usb:')) {
                const [label, ...folder] = b.location.slice(4).split('/');
                view.copy = {label, folder: folder.join('/'), present: label === 'BACKUP', dir: label === 'BACKUP' ? `/media/usb1/${folder.join('/')}` : undefined, last_copy: label === 'BACKUP' ? '2026-09-24T18:00:00Z' : null};
            }
            view.effective = view.mode || view.default_mode;
            view.open = view.effective !== 'ram';
            view.next_sync = view.effective !== 'ram' ? '2026-09-24T19:00:00Z' : null; // task 194: persistent writes the deferred report times at the interval
            sendJSON(res, view);
        });
        return;
    }
    // task 85: Copy now - only in ram-sync, which the stub's own answer is not; the specs answer it
    if (key === 'POST /api/system/v1/journal/sync') {
        return sendJSON(res, {error: 'not-ram-sync', message: 'the journal is not in RAM with copies to the userfs now'}, 409);
    }
    if (routes[key] !== undefined) {
        res.writeHead(200, {'Content-Type': 'application/json'});
        return res.end(JSON.stringify(routes[key]));
    }
    if (u.pathname.startsWith('/api/system/v1/firewall')) {
        return fwRoute(req, u, res);
    }
    if (u.pathname === '/api/system/v1/wifi' || u.pathname.startsWith('/api/system/v1/wifi/')) {
        return wifiRoute(req, u, res);
    }
    if (u.pathname.startsWith('/api/system/v1/remote-access')) {
        return raRoute(req, u, res);
    }
    if (u.pathname.startsWith('/api/rpc/v1/streams')) {
        return liteStreamsRoute(req, u, res);
    }
    if (u.pathname.startsWith('/api/rpc/v1/json/')) {
        return liteRoute(req, u, res);
    }
    if (key === 'GET /api/rpc/v1/events') {
        return liteEventsRoute(req, u, res);
    }
    if (key === 'GET /__stub/lite-probes') {
        return sendJSON(res, liteProbes.filter((p) => p.probe === u.searchParams.get('probe')).map(({probe: _, ...p}) => p));
    }
    if (key === 'GET /api/rpc/v1/state') {
        return liteStateRoute(req, u, res);
    }
    if (u.pathname === '/api/system/v1/rpc-trace') {
        return traceRoute(req, u, res);
    }
    // task 36: the PUT answers what was sent, with the certificate as GET describes it
    if (req.method === 'PUT' && u.pathname === '/api/system/v1/https') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const st = structuredClone(routes['GET /api/system/v1/https']);
            res.writeHead(200, {'Content-Type': 'application/json'});
            // redirect_fqdn is optional: absent leaves it as it is
            const fqdn = b.redirect_fqdn ?? st.redirect_fqdn;
            const fqdnState = fqdn ? {redirect_fqdn_state: 'active', redirect_fqdn_reason: undefined} : {};
            res.end(JSON.stringify({...st, redirect_https: !!b.redirect_https, hsts: !!b.hsts, hsts_max_age_days: b.hsts_max_age_days ?? 7, redirect_fqdn: !!fqdn, ...fqdnState}));
        });
        return;
    }
    // task 35: the settings PUT answers the whole status with the settings moved, the secrets
    // turned into flags; the actions answer the status with an attempt in flight
    if (req.method === 'PUT' && u.pathname === '/api/system/v1/certificate/settings') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const st = structuredClone(routes['GET /api/system/v1/certificate']);
            const b = JSON.parse(body || '{}');
            const secrets = {};
            const fields = (st.providers.find((p) => p.id === b.dns_provider)?.fields ?? []);
            const creds = {};
            for (const f of fields) {
                if (f.secret) secrets[f.key] = !!(b.dns_credentials?.[f.key]);
                else creds[f.key] = b.dns_credentials?.[f.key] ?? '';
            }
            // an ACME save without names takes the suggested ones, as the box does
            if (b.mode === 'acme' && !(b.names ?? []).some((n) => String(n).trim())) b.names = st.suggested_names;
            st.settings = {...st.settings, ...b, eab_hmac_set: !!b.eab_hmac, dns_credentials: creds, dns_secrets_set: secrets};
            delete st.settings.eab_hmac;
            res.writeHead(200, {'Content-Type': 'application/json'});
            res.end(JSON.stringify(st));
        });
        return;
    }
    // task 38: the manual mode - the preview parses what the page holds with node's own X.509
    // reader, the install answers the status in mode manual, the key route a fixed request, the
    // download the same with the attachment header
    if (req.method === 'POST' && u.pathname === '/api/system/v1/certificate/inspect') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            res.writeHead(200, {'Content-Type': 'application/json'});
            res.end(JSON.stringify(inspect(JSON.parse(body || '{}'))));
        });
        return;
    }
    // the maintainer, 2026-09-20: the clock set by hand or from the browser's time
    if (req.method === 'POST' && u.pathname === '/api/system/v1/time/clock') {
        let body = '';
        req.on('data', (c) => (body += c));
        req.on('end', () => {
            const b = body ? JSON.parse(body) : {};
            sendJSON(res, {...routes['GET /api/system/v1/time'], now: b.time ?? new Date().toISOString()});
        });
        return true;
    }
    if (req.method === 'PUT' && u.pathname === '/api/system/v1/certificate/manual') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const ct = req.headers['content-type'] ?? '';
            // multipart: the parts are not parsed here - a file upload counts as the fixture
            const b = ct.startsWith('multipart/') ? {certificate: CERT, key: KEY} : JSON.parse(body || '{}');
            const ins = inspect(b);
            if (!ins.certificate || (!b.key && !certState.pending) || ins.matches === false) {
                res.writeHead(422, {'Content-Type': 'application/json'});
                return res.end(JSON.stringify({error: 'invalid', message: ins.certificate_error || ins.key_error || 'the private key does not belong to the certificate: the certificate was issued for the key AA:BB…, the key given is CC:DD…'}));
            }
            certState.mode = 'manual';
            certState.current = ins.certificate;
            certState.pending = null;
            const st = certStatus();
            res.writeHead(200, {'Content-Type': 'application/json'});
            res.end(JSON.stringify({status: st, result: {certificate: ins.certificate, restarted_addons: ['mosquitto'], warning: ins.chain_warning ?? '', key_from: b.key ? 'upload' : 'pending'}, restarted_addons: ['mosquitto'], warning: ins.chain_warning ?? ''}));
        });
        return;
    }
    if (req.method === 'POST' && u.pathname === '/api/system/v1/certificate/key') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            if (!b.cn) {
                res.writeHead(422, {'Content-Type': 'application/json'});
                return res.end(JSON.stringify({error: 'invalid', message: 'the common name is empty - the name this system is reached by'}));
            }
            certState.lastCN = b.cn;
            certState.pending = {algorithm: b.algorithm === 'p384' ? 'P-384' : b.algorithm === 'rsa2048' ? 'RSA-2048' : 'P-256', fingerprint: 'AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99', cn: b.cn, sans: b.sans ?? [], org: b.org ?? '', created: new Date().toISOString(), csr: STUB_CSR};
            res.writeHead(200, {'Content-Type': 'application/json'});
            res.end(JSON.stringify({pending: certState.pending, csr: STUB_CSR, status: certStatus()}));
        });
        return;
    }
    if (req.method === 'GET' && u.pathname === '/api/system/v1/certificate/csr') {
        // the projects share the stub: another project's manual install may have cleared the
        // pending request between the click and the download, which Chromium then cancels on
        // the 404 - the last request generated stays downloadable
        const cn = certState.pending?.cn ?? certState.lastCN;
        if (!cn) {
            res.writeHead(404, {'Content-Type': 'application/json'});
            return res.end(JSON.stringify({error: 'not-found', message: 'no certificate request has been generated'}));
        }
        res.writeHead(200, {'Content-Type': 'application/pkcs10', 'Content-Disposition': `attachment; filename="${cn}.csr"`});
        return res.end(STUB_CSR);
    }
    if (req.method === 'GET' && u.pathname === '/api/system/v1/certificate') {
        res.writeHead(200, {'Content-Type': 'application/json'});
        return res.end(JSON.stringify(certStatus()));
    }
    const certAction = req.method === 'POST' && /^\/api\/system\/v1\/certificate\/(test|issue|renew)$/.exec(u.pathname);
    if (certAction) {
        const st = structuredClone(routes['GET /api/system/v1/certificate']);
        // task 102: the attempt names its run; its lines are the journal's (STUB_RUNS)
        st.running = {kind: certAction[1], started: now, ok: false, directory: 'https://acme-staging-v02.api.letsencrypt.org/directory', names: ['ccu.example.org'], run_id: STUB_RUN_CERT, installed: false};
        res.writeHead(202, {'Content-Type': 'application/json'});
        return res.end(JSON.stringify(st));
    }
    // task 91: the wizard's two requests and the switch answer as the system does; the test route
    // knows one key (the vector of recoverykey.test.ts) as the current one
    if (u.pathname === '/api/system/v1/backup/encryption/recovery' && req.method === 'POST') {
        let body = '';
        req.on('data', (c) => (body += c));
        return req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const out = {pending_id: 'stub-pending', fingerprint: 'ea77-3877-2a7b-31f8'};
            if (b.generate) out.recovery_key = '0024-H36H-2NCS-VRH6-DAQF-6DVV-QZN3';
            sendJSON(res, out);
        });
    }
    if (u.pathname === '/api/system/v1/backup/encryption/confirm' && req.method === 'POST') {
        req.resume();
        return req.on('end', () => sendJSON(res, {enabled: true, recovery: {fingerprint: 'ea77-3877-2a7b-31f8', created: '2026-09-23T12:00:00Z'}, previous: [], box: {fingerprint: '1f3a-9c2e-7b40-d15e', created: '2026-09-23T12:00:00Z'}}));
    }
    if (u.pathname === '/api/system/v1/backup/encryption' && req.method === 'PUT') {
        let body = '';
        req.on('data', (c) => (body += c));
        return req.on('end', () => {
            const b = JSON.parse(body || '{}');
            sendJSON(res, {enabled: !!b.enabled, recovery: {fingerprint: 'ea77-3877-2a7b-31f8', created: '2026-09-23T12:00:00Z'}, previous: [], box: {fingerprint: '1f3a-9c2e-7b40-d15e', created: '2026-09-23T12:00:00Z'}});
        });
    }
    if (u.pathname === '/api/system/v1/backup/encryption/test' && req.method === 'POST') {
        let body = '';
        req.on('data', (c) => (body += c));
        return req.on('end', () => {
            const key = String(JSON.parse(body || '{}').recovery_key ?? '').replace(/[\s_-]/g, '').toUpperCase();
            if (key.startsWith('AGE1')) return sendJSON(res, {error: 'is-recipient', message: 'the public part'}, 422);
            if (key.startsWith('AGE-SECRET-KEY-PQ-')) return sendJSON(res, {error: 'unsupported', message: 'post-quantum'}, 422);
            if (!/^[0-9A-HJKMNP-TV-Z]{28}$/.test(key) && !key.startsWith('AGE-SECRET-KEY-1')) return sendJSON(res, {error: 'invalid-key', message: 'typo'}, 422);
            const current = key === '0024H36H2NCSVRH6DAQF6DVVQZN3';
            const previous = key === '0000000000000000000000000000';
            sendJSON(res, {matches: current ? 'current' : previous ? 'previous' : 'none', fingerprint: current ? 'ea77-3877-2a7b-31f8' : previous ? '0000-1111-2222-3333' : 'abcd-abcd-abcd-abcd', created: '2026-09-23T12:00:00Z', ...(previous ? {retired: '2026-09-24T12:00:00Z'} : {}), identity: 'AGE-SECRET-KEY-173SCPSZRZXMZDYVU3QVPS6W8EF4LQWXGD4K06XZJJWMA0MQWA99S0V84MH', recovery_key: '0024-H36H-2NCS-VRH6-DAQF-6DVV-QZN3'});
        });
    }
    // task 109: the factory reset arms and reboots, the cancel disarms (a spec plants a refusal itself)
    if (u.pathname === '/api/system/v1/factory-reset' && (req.method === 'POST' || req.method === 'DELETE')) {
        req.resume();
        return req.on('end', () => sendJSON(res, req.method === 'POST' ? {ok: true, rebooting: true, reset: true} : {ok: true, armed: false}));
    }
    // the power menu's routes answer as the box does before it acts (a spec plants a refusal itself)
    if (req.method === 'POST' && ['/api/system/v1/reboot', '/api/system/v1/halt', '/api/system/v1/reboot/recovery'].includes(u.pathname)) {
        const answer = {'/api/system/v1/reboot': {ok: true, message: 'rebooting'}, '/api/system/v1/halt': {ok: true, halting: true}, '/api/system/v1/reboot/recovery': {ok: true, rebooting: true, recovery: true}}[u.pathname];
        req.resume();
        return req.on('end', () => sendJSON(res, answer));
    }
    // a deployed bundle's text file: the names the bundle lists, text files only
    const bundleFile = req.method === 'GET' && /^\/api\/system\/v1\/firmware\/bundles\/([^/]+)\/files\/([^/]+)$/.exec(u.pathname);
    if (bundleFile) {
        const tc = decodeURIComponent(bundleFile[1]);
        const name = decodeURIComponent(bundleFile[2]);
        const bundle = routes['GET /api/system/v1/firmware'].deployed.find((b) => b.type_code === tc);
        if (!bundle || !bundle.files.includes(name)) return sendJSON(res, {error: 'not-found', message: 'no such file in this bundle'}, 404);
        if (name !== 'info' && !/\.(txt|md|log)$/i.test(name)) return sendJSON(res, {error: 'not-viewable', message: 'not a text file: only info and .txt, .md and .log files are shown'}, 415);
        res.writeHead(200, {'Content-Type': 'text/plain; charset=utf-8', 'X-Content-Type-Options': 'nosniff'});
        return res.end(BUNDLE_FILES[tc]?.[name] ?? '');
    }
    if (u.pathname === '/api/system/v1/radio/connections' || u.pathname === '/api/system/v1/radio/connections/preview') {
        return connRoute(req, u, res);
    }
    // task 237: the GET is the last search's result (the stub's box has searched before, unless the
    // stub-lan cookie says unsearched); only the POST searches
    if (u.pathname === '/api/system/v1/radio/lan-devices') {
        const jar = cookieJar(req);
        if (jar['stub-lan'] === 'unsearched') return sendJSON(res, {devices: []});
        return sendJSON(res, lanDevicesView(jar));
    }
    if (req.method === 'POST' && u.pathname === '/api/system/v1/radio/lan-devices/search') {
        return sendJSON(res, lanDevicesView(cookieJar(req)));
    }
    if (req.method === 'POST' && /^\/api\/system\/v1\/radio\/lan-devices\/[^/]+\/network$/.test(u.pathname)) {
        return readBody(req).then((raw) => {
            const b = JSON.parse(raw || '{}');
            const serial = decodeURIComponent(u.pathname.split('/')[6]);
            if (b.password === 'wrong') return sendJSON(res, {error: 'wrong-password', message: 'the device did not take the password'}, 422);
            if (!b.dhcp && b.ip && !b.ip.startsWith('192.0.2.') && !b.other_subnet) return sendJSON(res, {error: 'other-subnet', message: 'the address is in none of the networks'}, 409);
            if (!b.dhcp && b.ip === '192.0.2.50') return sendJSON(res, {error: 'address-in-use', message: 'in use'}, 409);
            lanWrites.push({serial, ...b});
            const dev = structuredClone(lanDevicesView({}).devices.find((d) => d.serial === serial));
            if (!b.dhcp) dev.runtime.ip = b.ip;
            const out = {written: true, restarts: true, device: dev};
            if (dev.configured) Object.assign(out, {gateway_file: true, restarted: dev.configured === 'wired' ? 'hs485d' : 'rfd'});
            return sendJSON(res, out);
        });
    }
    if (u.pathname === '/api/stub/lan-writes') {
        return sendJSON(res, lanWrites);
    }
    if (u.pathname === '/api/system/v1/radio/hmip/access-points') {
        return sendJSON(res, accessPointsView(cookieJar(req)));
    }
    if (u.pathname.startsWith('/api/system/v1/radio/hmip/local-key')) {
        const jar = cookieJar(req);
        if (req.method === 'DELETE' && /\/snapshots\//.test(u.pathname) && jar['stub-conn-lksnap'] === '1') connLkDiscarded.add(jar['stub-conn']);
        return lkRoute(req, u, res);
    }
    if (req.method === 'POST' && u.pathname === '/api/system/v1/radio/hmip/module-move/back') {
        return readBody(req).then((raw) => {
            const b = JSON.parse(raw || '{}');
            if (!b.confirm) return sendJSON(res, {error: 'confirm', message: 'confirm: true is required'}, 400);
            if ((b.hostname ?? '').trim().toLowerCase() !== 'openccu') return sendJSON(res, {error: 'hostname', message: 'the host name typed does not match this system\'s'}, 400);
            return sendJSON(res, connStatus(cookieJar(req)), 202);
        });
    }
    if (u.pathname.startsWith('/api/system/v1/radio/hmip/exchange')) {
        return exRoute(req, u, res);
    }
    if (u.pathname.startsWith('/api/system/v1/radio/hmip/device-keys')) {
        return dkRoute(req, u, res);
    }
    if (u.pathname === '/api/system/v1/ssh' || u.pathname.startsWith('/api/system/v1/ssh/')) {
        return sshRoute(req, u, res);
    }
    // task 41: a flash answers 202 with the attempt in flight; the page polls the status
    if (req.method === 'POST' && u.pathname === '/api/system/v1/radio/firmware/flash') {
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
            const b = JSON.parse(body || '{}');
            const st = structuredClone(routes['GET /api/system/v1/radio/firmware']);
            // task 102: the attempt names its run; its lines are the journal's (STUB_RUNS)
            st.running = {module: b.module, device_node: '/dev/raw-uart', file: `/usr/local/etc/config/radio-firmware/HmIP-RFUSB/${b.file}`, file_version: '4.4.22', started: now, ok: false, exit: 0, run_id: STUB_RUN_FLASH};
            res.writeHead(202, {'Content-Type': 'application/json'});
            res.end(JSON.stringify(st));
        });
        return;
    }
    if (req.method === 'POST' && u.pathname === '/api/system/v1/radio/firmware/upload') {
        req.on('data', () => {});
        req.on('end', () => {
            res.writeHead(200, {'Content-Type': 'application/json'});
            res.end(JSON.stringify({name: 'dualcopro_update_blhmip-4.4.23.eq3', path: '/usr/local/etc/config/radio-firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.23.eq3', source: 'uploaded', version: '4.4.23', size: 12, sha256: '0000000000000000000000000000000000000000000000000000000000000000', direction: 'upgrade'}));
        });
        return;
    }
    if (u.pathname === '/api/system/v1/stream') {
        // occulited B-53: the shell's stream - each topic's first event (as its own route below sends
        // it), then a heartbeat; held open like the real one. stub-shellstream=<id> records the opens
        // (GET /__stub/shellstream?id=), for the specs that count the browser's connections.
        const topics = [...new Set(u.searchParams.getAll('topics').flatMap((v) => v.split(',')).filter(Boolean))].sort();
        const known = ['addons', 'pairing', 'service-messages'];
        const bad = topics.find((t) => !known.includes(t));
        if (bad || topics.length === 0) return sendJSON(res, {error: 'bad-request', message: `unknown topic ${bad ?? '(none)'}`}, 400);
        const id = cookieJar(req)['stub-shellstream'];
        const rec = id ? {topics: topics.join(','), open: true} : null;
        if (rec) {
            if (!shellStreams.has(id)) shellStreams.set(id, []);
            shellStreams.get(id).push(rec);
        }
        res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', 'X-Accel-Buffering': 'no'});
        for (const t of topics) {
            if (t === 'addons') res.write(`event: addons\ndata: ${JSON.stringify({revision: 1})}\n\n`);
            if (t === 'service-messages') res.write(`event: messages\ndata: ${JSON.stringify(serviceMessagesView(cookieJar(req)))}\n\n`);
            if (t === 'pairing') res.write('event: pairing\ndata: {"enabled":true,"requests":[]}\n\n');
        }
        const hb = setInterval(() => res.write(': ping\n\n'), 30000);
        req.on('close', () => {
            clearInterval(hb);
            if (rec) rec.open = false;
        });
        return;
    }
    if (u.pathname === '/__stub/shellstream') return sendJSON(res, shellStreams.get(u.searchParams.get('id')) ?? []);
    if (u.pathname === '/api/system/v1/addons/stream') {
        // openccu-lite B-297: the addons' revision once, then a heartbeat - nothing changes on the
        // stub; a spec that wants a change answers the route itself (addon-menu-follows.spec.ts)
        res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
        res.write(`event: addons\ndata: ${JSON.stringify({revision: 1})}\n\n`);
        const hb = setInterval(() => res.write(': ping\n\n'), 30000);
        req.on('close', () => clearInterval(hb));
        return;
    }
    if (u.pathname === '/api/system/v1/service-messages/stream') {
        // task 75: the view once, then a heartbeat - a test that wants a change edits the route's
        // answer and waits for the poll, or sets stub-servicemsg=none for an empty list
        res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
        const view = serviceMessagesView(cookieJar(req));
        res.write(`event: messages\ndata: ${JSON.stringify(view)}\n\n`);
        const hb = setInterval(() => res.write(': ping\n\n'), 30000);
        req.on('close', () => clearInterval(hb));
        return;
    }
    if (u.pathname === '/api/system/v1/log/stream') {
        // the Log page opens an EventSource; answer with the right MIME type and no events -
        // unless the browser carries the churn cookie (B-59): then a line every 200 ms with a
        // tag and a unit the page has not seen before, the way a busy box keeps the option
        // lists of the two selects moving; the stream's own filters are honoured like journalctl's
        res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'});
        res.write(': stub\n\n');
        let timer = null;
        if (/(^|;\s*)stub-log-churn=1/.test(req.headers.cookie ?? '')) {
            timer = setInterval(() => {
                const i = ++churnSeq;
                const base = ['occulited', 'rfd', 'hmipserver', 'addon-mosquitto'];
                // three kinds of line in turn: a known tag under its unit, a fresh tag under a
                // known unit, a known tag under a fresh unit - so a page filtered on one field
                // still sees the other field's list grow, and one filtered on both sees a line
                // every twelfth round
                const known = base[i % 4];
                const l = {
                    time: new Date().toISOString(),
                    timestamp: new Date().toISOString(),
                    severity: ['info', 'notice', 'warning'][i % 3],
                    tag: i % 3 === 1 ? `churn-tag-${i}` : known,
                    unit: i % 3 === 2 ? `churn-unit-${i}` : known,
                    pid: 5000 + i,
                    message: `churn line ${i}`,
                };
                churned.push(l);
                if (churned.length > 200) churned.shift();
                if (logMatches(l, u.searchParams)) res.write(`data: ${JSON.stringify(l)}\n\n`);
            }, 200);
        }
        req.on('close', () => {
            if (timer) clearInterval(timer);
            res.end();
        });
        return;
    }
    if (u.pathname.startsWith('/api/')) {
        // every other API call: an empty ok, so nothing 500s in the console
        res.writeHead(200, {'Content-Type': 'application/json'});
        return res.end('{}');
    }
    // addon logos, copied off the lab box
    if (u.pathname.startsWith('/addons/')) {
        const f = path.join(WWW, u.pathname);
        if (f.startsWith(WWW) && fs.existsSync(f) && fs.statSync(f).isFile()) {
            res.writeHead(200, {'Content-Type': MIME[path.extname(f)] ?? 'application/octet-stream'});
            return res.end(fs.readFileSync(f));
        }
        // frames.spec.ts: a framed page of the same origin that speaks Node-RED's theme protocol. It
        // lives under /addons/ like a real addon page, outside the shell's CSP (a srcdoc frame would
        // inherit the shell's policy, which allows no inline script)
        if (u.pathname === '/addons/frames-probe/') {
            res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
            return res.end(`<!doctype html><script>
    window.addEventListener('message', (e) => { parent.__theme = e.data; });
    parent.postMessage({type: 'request-theme'}, '*');
</script>`);
        }
        // an addon settings page in the iframe: a stand-in, so the frame is not a 404
        res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
        return res.end(`<!doctype html><body style="font:13px system-ui;padding:20px">addon page stub: ${u.pathname}${FRAME_PROBE}</body>`);
    }
    let f = path.join(DIST, u.pathname);
    if (!f.startsWith(DIST) || !fs.existsSync(f) || fs.statSync(f).isDirectory()) f = path.join(DIST, 'index.html');
    // the shell and its assets carry the system's CSP (task 259)
    res.writeHead(200, {'Content-Type': MIME[path.extname(f)] ?? 'application/octet-stream', ...SHELL_HEADERS});
    res.end(fs.readFileSync(f));
});
srv.listen(PORT, process.env.HOST || '127.0.0.1', () => console.log(`stub on http://${process.env.HOST || '127.0.0.1'}:${PORT}`));

// task 218: the stub's HB-RF-ETH view of an address
function hbView(hbAddress) {
    if (hbAddress === '') return {address: '', connected: false, module_loaded: false, retrying: false, tries: 0};
    // openccu-lite B-218: a board that was connected and whose link is lost - the kernel's to reconnect
    if (hbAddress === '192.0.2.61') return {address: hbAddress, connected: false, module_loaded: true, retrying: true, reconnecting: true, lost_since: new Date(Date.now() - 40000).toISOString(), tries: 0};
    if (hbAddress === '192.0.2.60') return {address: hbAddress, connected: false, module_loaded: true, retrying: true, tries: 3, board_error: 'the board does not answer: dial tcp 192.0.2.60:80: i/o timeout'};
    return {address: hbAddress, connected: true, module_loaded: true, retrying: false, tries: 0,
        board: {address: hbAddress, serial: 'ABCDEF1234', firmware: '1.3.0', module_type: 'HM-MOD-RPI-PCB', module_serial: 'MEQ9000005', bidcos_address: '0x3D0A01', connected_to: '192.0.2.1', state: 'this'}};
}
