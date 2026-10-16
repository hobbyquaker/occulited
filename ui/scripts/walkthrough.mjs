// openccu-lite task 274: the screenshots of the fork's docs/walkthrough - every page of the UI,
// German, light theme, taken from the stub server (test/stub/server.mjs) with demo data laid over
// its answers here, so the test suite's data and expectations stay as they are.
//
//   npm run build
//   PORT=8841 node test/stub/server.mjs &
//   env -u WAYLAND_DISPLAY node scripts/walkthrough.mjs http://127.0.0.1:8841 <out-dir> [name ...]
//
// The Homematic Manager shots (hmm-*) need its web host in demo mode - its own MockTransport
// fixture, no CCU - at WALK_HMM_URL (e.g. http://127.0.0.1:8851/): the shell's /addons/mh/ is
// routed there, so the manager is shown in the shell's frame as it is on a system. Without the
// variable those shots are skipped.
//
// WALK_LICENSES_URL (e.g. https://<a lab system>/) takes the Licenses page from a real system instead
// of the stub (the maintainer: it shows the real components): signed out, as the page is public, with
// no API patching and without the author rule - his name as the author is the one thing allowed on
// that image. Without it the page comes from the stub, author rule included.
//
// WALK_BOOT_URL and WALK_BOOT_TOKEN (a read-only API token of that system, deleted afterwards) take the
// Services page's boot timeline of a real boot with the early addon start; without them those shots
// are skipped.
//
// WALK_HMM_ICON and WALK_REDMATIC_ICON: the two addons' own favicon files (from their repositories), served as
// their frontends' icons, so the pinned tabs show them. Homematic Manager and RedMatic are pinned on every
// page with the top bar; the pinning shots start with RedMatic alone.
//
// WALK_RADIO_URL, WALK_RADIO_TOKEN and WALK_RADIO_NAMES ("<nn-name>,<nn-name>") take the Interfaces and
// the LAN devices page of a real system - one radio setup per run. Real pages get REAL_RULES on top:
// every MAC, module serial, radio address and IPv4 address that is not a documentation one is masked.
//
// Two layers of demo data: the stub's JSON answers are patched per endpoint (a system with a plain
// name, no warnings, local logins, no ULA), and before every shot the page's text is swept with
// RULES, which mask what must never be on a published image - device serials, SGTINs, host names
// and addresses that are not documentation ones. The images are PNG; the caller compresses them.
import {chromium} from '@playwright/test';
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const [base, out, ...only] = process.argv.slice(2);
if (!base || !out) {
    console.error('usage: walkthrough.mjs <stub base URL> <out dir> [shot name ...]');
    process.exit(2);
}
fs.mkdirSync(out, {recursive: true});

const HOST = 'openccu-lite';
let head = 'f0775320000000000000000000000000000000000';
try {
    head = execFileSync('git', ['rev-parse', 'HEAD'], {encoding: 'utf8'}).trim();
} catch {
    /* outside a checkout: the placeholder */
}

// ---- the API layer: string replacements on the JSON text, then patches per endpoint ----
const TEXT = [
    [/"openccu"/g, `"${HOST}"`],
    [/"ccu-vm-1"/g, `"${HOST}"`],
    [/openccu\.home\.arpa/g, `${HOST}.home.arpa`],
    [/CN=openccu,/g, `CN=${HOST},`],
    [/@openccu\b(?!-)/g, `@${HOST}`],
    [/"openccu-(\d{4}-)/g, `"${HOST}-$1`],
];
// the Log page's lines: what a quiet system writes, instead of the stub's numbered samples
const DEMO_LOG = [
    ['info', 'occulited', 'listening on 127.0.0.1:8183'],
    ['info', 'rfd', 'XmlRpc: init from 127.0.0.1:31999, interface id 1007'],
    ['info', 'hmipserver', 'HmIP-RF: 14 devices loaded, key server not needed'],
    ['notice', 'hmipserver', 'Access point HmIP-HAP connected via LAN'],
    ['info', 'addon-mosquitto', 'New client connected from 127.0.0.1:50422 as hm2mqtt (p2, c1, k60)'],
    ['info', 'occulited', 'lite-rpc: SSE stream opened for token home-assistant'],
    ['info', 'addon-hm2mqtt', 'mqtt connected mqtt://127.0.0.1:1883'],
    ['warning', 'rfd', 'Device HM-CC-TC unreachable: no answer to 3 attempts'],
    ['info', 'occulited', 'service messages: 2 active (LOW_BAT, UNREACH)'],
    ['info', 'lighttpd', 'server started (lighttpd/1.4.82)'],
    ['info', 'occulited', 'addons: catalogue refreshed, 2 updates available'],
    ['notice', 'occulited', 'backup: nightly backup written to target NAS (4.7 MB, 12 s)'],
    ['info', 'addon-redmatic', 'Node-RED: Started flows'],
    ['err', 'addon-redmatic', 'ccu-connection: HmIP-RF ping timeout, reconnecting'],
    ['info', 'addon-redmatic', 'ccu-connection: HmIP-RF connected'],
];
const PINS = [{id: 'mh', pinned: true}, {id: 'redmatic', pinned: true}];
const PINS_BEFORE = [{id: 'mh', pinned: false}, {id: 'redmatic', pinned: true}];
/** endpoint path (no query) -> function(json, shot) returning the patched json */
const PATCH = {
    '/api/system/v1/health': (j) => ({...j, version: head, release: '1.0.0'}),
    '/api/system/v1/status': (j) => ({...j, occulited_version: head, version: {...j.version, lite: '1.0.0', version: '3.89.9.20260914'}}),
    '/api/system/v1/system-update': (j) => ({...j, running: {...j.running, lite: '1.0.0', version: '3.89.9.20260914'}}),
    '/api/system/v1/warnings': (j) => ({...j, warnings: []}),
    '/api/auth/v1/me/preferences': (j) => ({...j, addons: PINS}),
    '/api/auth/v1/config': (j) => ({...j, mode: 'local', running: 'local', restart_required: false, name: '', issuer: '', client_id: '', client_secret_set: false}),
    '/api/system/v1/network': (j) => {
        for (const i of j.network?.interfaces ?? []) {
            i.ipv6 = (i.ipv6 ?? []).filter((a) => a.scope !== 'unique-local');
            i.ipv4 = (i.ipv4 ?? []).map((a) => (a.address === '10.10.0.2' ? {...a, address: '192.0.2.119'} : a));
            if (i.name === 'wlan0') i.ipv4 = [{address: '192.0.2.120', prefix: 24}];
        }
        if (j.network?.ipv6_state) j.network.ipv6_state.dns = ['2001:db8:1::1'];
        return j;
    },
    '/api/system/v1/firewall': (j) => {
        const drop = (o) => {
            if (o && typeof o === 'object') {
                delete o.migration;
                Object.values(o).forEach(drop);
            }
        };
        drop(j);
        return j;
    },
    '/api/system/v1/backup/targets': (j) => ({
        ...j,
        targets: (j.targets ?? []).filter((t) => t.kind !== 'directory').map((t) => (t.kind === 'sftp' ? {...t, name: 'Server im Keller'} : t.kind === 'share' ? {...t, name: 'NAS'} : t)),
    }),
    '/api/system/v1/log': (j) => (Array.isArray(j.lines) && j.lines.some((l) => /^Sample log line/.test(l.message ?? ''))
        ? {...j, lines: j.lines.slice(-DEMO_LOG.length).map((l, i) => {
            const [severity, tag, message] = DEMO_LOG[i % DEMO_LOG.length];
            return {...l, severity, tag, unit: tag, message, area: undefined};
        })}
        : j),
    '/api/system/v1/ssh/keys': () => ({managed: [{type: 'ssh-ed25519', bits: 256, comment: 'anna@laptop', fingerprint: 'SHA256:Q3v8XmL2pR7tN0bK5sW1yE4hJ6cF9gD2aZ8uV3iO5qT'}], other: []}),
    '/api/system/v1/certificate': (j) => (j.current ? {...j, current: {...j.current, issuer: j.current.subject, issuer_cn: HOST, issuer_org: 'HomeMatic', fingerprint: j.current.fingerprint?.replace(/^FD:/, 'A7:')}} : j),
    '/api/system/v1/https': (j) => ({...j, redirect_fqdn_host: HOST, redirect_fqdn_target: `${HOST}.home.arpa`}),
    '/api/system/v1/services': (j) => ({
        ...j,
        services: [
            ...(j.services ?? []).map((s) => (s.kind === 'addon' && s.policy_mode === 'confined' && !s.user ? {...s, user: s.id} : s)),
            {id: 'occu-etc-writable', kind: 'system', running: true, enabled: true, unit_file_state: 'enabled', managed: true, category: 'core', user: 'root'},
        ],
    }),
    '/api/system/v1/backup/schedule': (j) => ({...j, path: '/media/net/nas/openccu-lite', on_userfs: false, real_path: '/media/net/nas/openccu-lite'}),
};

// ---- the page layer: what the sweep masks before each shot (run in the page) ----
const RULES = [
    ['step-ca\\.lan\\.example\\.org', 'ca.example.org'],
    ['lan\\.example\\.org', 'example.org'],
    ['\\bnas\\.(lan|lab)\\b', 'nas.home.arpa'],
    ['Build host( \\(TrueNAS dataset via SSH\\))?', 'Server im Keller'],
    ['TrueNAS', 'NAS'],
    ['Lab CA', 'Heimnetz-CA'],
    ['labLAB[A-Za-z0-9]*', 'Q3v8XmL2pR7tN0bK5sW1yE4hJ6cF9gD2aZ8uV3iO5qT'],
    ['\\blab\\b', 'laptop'],
    ['\\bLab\\b', 'Heimnetz'],
    ['authentik', 'SSO'],
    ['Access point cellar', 'Access Point Keller'],
    ['\\bsebastian\\b', 'anna'],
    // the author's full name stays off the images (the handle names him)
    ['\\bSebastian R\\w+ \\(hobbyquaker\\)', 'hobbyquaker', 'author'],
    ['\\bSebastian R\\w+', 'hobbyquaker', 'author'],
    ['\\b10\\.10\\.0\\.2\\b', '192.0.2.119'],
    ['\\b192\\.168\\.(0|1)\\.(\\d{1,3})(?![\\d/])', '192.0.2.$2'],
    ['\\b192\\.168\\.0\\.0/24\\b', '192.0.2.0/24'],
    // a real system's own names and addresses, should one show
    ['\\blab-ccu[\\w-]*', 'openccu-lite'],
    ['\\b172\\.16\\.\\d{1,3}\\.\\d{1,3}\\b', '192.0.2.10'],
];
// only on pages of a real system: whatever identifies its hardware
const REAL_RULES = [
    ['\\b([0-9A-Fa-f]{2})[:-]([0-9A-Fa-f]{2})[:-]([0-9A-Fa-f]{2})[:-][0-9A-Fa-f]{2}[:-][0-9A-Fa-f]{2}[:-][0-9A-Fa-f]{2}\\b', '$1:$2:$3:XX:XX:XX'],
    ['\\b0x[0-9A-Fa-f]{6}\\b', '0xXXXXXX'],
    ['\\b(?=[0-9A-F]*[0-9])(?=[0-9A-F]*[A-F])[0-9A-F]{6,12}\\b', 'XXXXXXXXXX'],
    // a CCU's discovery serial (the last nine hex digits of its MAC)
    ['\\b(?=[0-9a-f]*[0-9])(?=[0-9a-f]*[a-f])[0-9a-f]{9}\\b', '0a1b2c3d4'],
    ['\\b(?!127\\.0\\.0\\.1\\b)(?!192\\.0\\.2\\.)(?:\\d{1,3}\\.){3}(\\d{1,3})\\b', '192.0.2.$1'],
];
function sweep(rules) {
    const fixed = rules.map(([re, to]) => [new RegExp(re, 'g'), to]);
    const fn = [
        // SGTINs, plain and in groups of four: masked
        [/\b30[0-9A-F]{2}(-[0-9A-F]{4}){5}\b/gi, () => 'XXXX-XXXX-XXXX-XXXX-XXXX-XXXX'],
        [/\b30[0-9A-F]{22}\b/gi, () => 'XXXXXXXXXXXXXXXXXXXXXXXX'],
        // anything else that starts like an eQ-3 SGTIN (a fixture's address in that shape)
        [/\b3014F7[0-9A-F]*/gi, (m) => 'X'.repeat(m.length)],
        // HmIP device addresses (14 hex digits, 00…) and BidCos serials (three letters, seven digits): masked, so
        // nobody takes them for a real device's
        [/\b00[0-9A-F]{12}\b/g, () => '00XXXXXXXXXXXX'],
        [/\b([A-Z]EQ)(\d{7})\b/g, (m, p) => `${p}XXXXXXX`],
    ];
    const apply = (s) => {
        let r = s;
        for (const [re, to] of fixed) r = r.replace(re, to);
        for (const [re, f] of fn) r = r.replace(re, f);
        return r;
    };
    const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walk.nextNode()) nodes.push(walk.currentNode);
    for (const n of nodes) {
        const r = apply(n.nodeValue);
        if (r !== n.nodeValue) n.nodeValue = r;
    }
    for (const el of document.querySelectorAll('input, textarea')) {
        if (el.value) {
            const r = apply(el.value);
            if (r !== el.value) el.value = r;
        }
        if (el.placeholder) el.placeholder = apply(el.placeholder);
    }
    for (const el of document.querySelectorAll('[title]')) el.title = apply(el.title);
}

// ---- the shots ----
const SETUP = {setup_required: true, authenticated: false};
const SIGNED_OUT = {setup_required: false, authenticated: false};
const fresh = {
    '/api/system/v1/firmware': (j) => ({...j, enabled: false}),
    '/api/system/v1/system-update': (j) => ({...PATCH['/api/system/v1/system-update'](j), feed: {...j.feed, enabled: false}}),
    '/api/system/v1/catalog': (j) => ({...j, daily: false}),
};
const HMM = process.env.WALK_HMM_URL ?? '';
// the boot timeline opened, services only, and scrolled into view: the page is shot from there down
const bootTimeline = async (p) => {
    await p.locator('.bt-toggle').click();
    await p.waitForTimeout(1500);
    await p.getByLabel(/Nur Dienste/).check().catch(() => {});
    await p.waitForTimeout(500);
};
const hmmFrame = (p) => p.frameLocator('iframe[src*="/addons/mh/"]');
const longPress = async (p, sel) => {
    const box = await p.locator(sel).first().boundingBox();
    await p.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await p.mouse.down();
    await p.waitForTimeout(1500);
    await p.mouse.up();
};
const DARK = {dark: true};
// the ACME and OIDC shots: example names, a demo CA, an invented client - nothing real
const ACME_DOMAIN = `${HOST}.example.org`;
const acme = {
    '/api/system/v1/certificate': (j) => {
        const c = PATCH['/api/system/v1/certificate'](j);
        const now = Date.now();
        return {
            ...c,
            settings: {...c.settings, mode: 'acme', directory: 'custom', directory_url: 'https://ca.example.org/acme/acme/directory', ca_root: '', email: 'admin@example.org', names: [ACME_DOMAIN], challenge: 'dns-01', dns_provider: 'cloudflare', dns_credentials: {}, dns_secrets_set: {api_token: true}},
            managed: true,
            current: {...c.current, subject: `CN=${ACME_DOMAIN}`, issuer: 'CN=Beispiel-CA Intermediate', issuer_cn: 'Beispiel-CA Intermediate', issuer_org: 'Beispiel', names: [ACME_DOMAIN], ips: [], self_signed: false, not_before: new Date(now - 30 * 86400e3).toISOString(), not_after: new Date(now + 60 * 86400e3).toISOString(), days_left: 60},
        };
    },
    '/api/system/v1/https': (j) => ({...PATCH['/api/system/v1/https'](j), certificate: {self_signed: false, managed: true, mode: 'acme'}, redirect_https: true, hsts: true, redirect_fqdn_target: ACME_DOMAIN, redirect_fqdn_state: 'available', redirect_fqdn_reason: ''}),
};
const OIDC_CONFIG = {mode: 'oidc', running: 'oidc', restart_required: false, name: 'Keycloak', issuer: 'https://auth.example.org/realms/home', client_id: 'openccu-lite', client_secret_set: true, username_claim: 'preferred_username', scopes: 'openid profile email', password_login: true};
const oidc = {
    '/api/auth/v1/config': (j) => ({...j, ...OIDC_CONFIG}),
    '/api/auth/v1/oidc': () => ({enabled: true, name: 'Keycloak', password_login: true}),
};
const SHOTS = [
    {name: '01-einrichtung', path: '/', state: SETUP},
    {name: '02-willkommen', path: '/welcome', patch: fresh},
    {name: '03-anmeldung', path: '/login', state: SIGNED_OUT},
    {name: '04-status', path: '/'},
    {name: '05-bedienung', path: '/app/e/rooms/og', app: true},
    {name: '06-bedienung-kanal', path: '/app/e/rooms/og', app: true, act: (p) => p.locator('[data-app-tile]', {hasText: 'Bad Thermostat'}).click(), height: 900},
    {name: '07-systemmenue', path: '/', act: (p) => p.locator('.ol-systab').click(), height: 900},
    {name: '08-schnittstellen', path: '/system/interfaces'},
    {name: '09-lan-geraete', path: '/system/lan-devices'},
    {name: '10-schluessel', path: '/system/keys'},
    {name: '11-netzwerk', path: '/system/network'},
    {name: '12-firewall', path: '/system/firewall'},
    {name: '13-fernzugriff', path: '/system/remote-access'},
    {name: '14-vertrauensspeicher', path: '/system/trust'},
    {name: '15-zertifikat', path: '/system/certificates'},
    {name: '16-zertifikat-acme', path: '/system/certificates', patch: acme},
    {name: '17-benutzer', path: '/system/users'},
    {name: '18-benutzer-oidc', path: '/system/users', patch: oidc},
    {name: '19-anmeldung-oidc', path: '/login', state: SIGNED_OUT, patch: oidc},
    {name: '20-dienste', path: '/system/services'},
    {name: '21-protokoll', path: '/system/log', height: 900},
    {name: '22-speicher', path: '/system/storage'},
    {name: '23-sicherung', path: '/system/backup'},
    {name: '24-updates', path: '/system/updates'},
    {name: '25-hardware', path: '/system/hardware'},
    {name: '26-einstellungen', path: '/settings'},
    {name: '27-konto', path: '/account'},
    // a real system lists some hundred components: the head of the list is enough
    {name: '28-lizenzen', path: '/licenses', real: process.env.WALK_LICENSES_URL ?? '', height: process.env.WALK_LICENSES_URL ? 1400 : undefined},
    {name: '29-ausschalten', path: '/', act: (p) => p.locator('.ol-powerbtn').click(), height: 900},
    {name: '30-zusatzsoftware', path: '/addons'},
    {name: '31-zusatzsoftware-menue', path: '/', prefs: true, patch: {'/api/auth/v1/me/preferences': (j) => ({...j, addons: PINS_BEFORE})}, act: (p) => p.locator('.ol-addonsbtn').click(), height: 900},
    {name: '32-zusatzsoftware-angeheftet', path: '/', prefs: true, patch: {'/api/auth/v1/me/preferences': (j) => ({...j, addons: PINS_BEFORE})}, act: async (p) => {
        await p.locator('.ol-addonsbtn').click();
        await p.locator('.ol-addonpop [data-addon="mh"] .ol-pin').click();
        await p.waitForTimeout(600);
        await p.keyboard.press('Escape');
    }, height: 900},
    {name: '33-hmm-geraete', path: '/nav/mh', hmm: '#/BidCos-RF/devices', height: 900, act: async (p) => {
        const f = hmmFrame(p);
        const row = f.locator('[data-row-id="MEQ0123456"]');
        await row.waitFor({state: 'visible'});
        await row.getByRole('button', {name: 'Expand row'}).click();
    }},
    {name: '34-hmm-anlernen', path: '/nav/mh', hmm: '#/HmIP-RF/devices', height: 900, act: async (p) => {
        const f = hmmFrame(p);
        await f.getByTestId('devices-table').waitFor({state: 'visible'});
        await f.getByTestId('devices-add').click();
    }},
    {name: '35-hmm-parameter', path: '/nav/mh', hmm: '#/BidCos-RF/devices', height: 900, act: async (p) => {
        const f = hmmFrame(p);
        const row = f.locator('[data-row-id="MEQ0123456"]');
        await row.waitFor({state: 'visible'});
        await row.getByRole('button', {name: 'Expand row'}).click();
        await f.getByTestId('paramset-MEQ0123456:1-MASTER').click();
        await f.getByTestId('paramset-dialog').waitFor({state: 'visible'});
    }},
    {name: '36-hmm-verknuepfungen', path: '/nav/mh', hmm: '#/HmIP-RF/links', height: 900, act: async (p) => {
        const f = hmmFrame(p);
        const row = f.locator('[data-row-id="0001D8A9B7C6D5:1->000A1B2C3D4E5F:4"]');
        await row.waitFor({state: 'visible'});
        await row.click();
        await f.getByTestId('links-edit').click();
        await f.getByTestId('link-paramset-dialog').waitFor({state: 'visible'});
        await f.getByTestId('link-expert').uncheck();
        await f.getByTestId('link-profile').selectOption('2');
        // a notice of the demo fixture's firmware, not of the link: closed
        await p.waitForTimeout(400);
        for (const b of await f.locator('.hmm-notice-close').all()) await b.click();
    }},
    {name: '37-hmm-servicemeldungen', path: '/nav/mh', hmm: '#/BidCos-RF/messages', height: 900},
    {name: '38-namen-raeume', path: '/app/e/rooms/og', app: true, act: (p) => p.locator('[data-app-entry="taxonomy"]').click(), height: 900},
    {name: '39-namen-kanal', path: '/app/e/rooms/og', app: true, act: (p) => longPress(p, '[data-app-tile]'), height: 900},
    {name: '40-dunkel-status', path: '/', ...DARK},
    {name: '41-dunkel-bedienung', path: '/app/e/rooms/og', app: true, ...DARK},
    {name: '42-dunkel-netzwerk', path: '/system/network', ...DARK},
    {name: '43-dunkel-zusatzsoftware', path: '/addons', ...DARK},
    {name: '46-protokoll-stufen', path: '/system/log', height: 900, patch: {'/api/system/v1/loglevels': (j) => ({...j, loghost: 'syslog.example.org:514'})}, act: (p) => p.locator('.lg-settings').click()},
    {name: '47-protokoll-journal', path: '/system/log', height: 900, act: async (p) => {
        await p.locator('.lg-settings').click();
        await p.getByRole('tab', {name: 'Journal'}).click();
    }},
    {name: '48-protokoll-ziel', path: '/system/log', height: 900, act: async (p) => {
        await p.locator('.lg-settings').click();
        await p.getByRole('tab', {name: 'Journal'}).click();
        await p.getByText('Systemspeicher (userfs)').first().click();
    }},
    // the phone: Status, the App and a channel's sheet, the App as the whole window (Settings → "Bedienung als ganzes
    // Fenster", which the installed web app opens on), and the App in the dark
    {name: '53-handy-status', path: '/', phone: true},
    {name: '54-handy-bedienung', path: '/app/e/rooms/og', app: true, phone: true},
    {name: '55-handy-bedienung-kanal', path: '/app/e/rooms/og', app: true, phone: true, act: (p) => p.locator('[data-app-tile]', {hasText: 'Bad Thermostat'}).click()},
    {name: '56-handy-app-vollbild', path: '/app/e/rooms/og', app: true, phone: true, patch: {'/api/auth/v1/me/preferences': (j) => ({...j, addons: PINS, app_fullscreen: true})}},
    {name: '57-handy-app-menue', path: '/app/e/rooms/og', app: true, phone: true, patch: {'/api/auth/v1/me/preferences': (j) => ({...j, addons: PINS, app_fullscreen: true})}, act: (p) => p.locator('[data-app-fab]').click()},
    {name: '58-handy-dunkel-bedienung', path: '/app/e/rooms/og', app: true, phone: true, ...DARK},
    {name: '44-startablauf', path: '/system/services', real: process.env.WALK_BOOT_URL ?? '', bearer: process.env.WALK_BOOT_TOKEN ?? '', needsReal: true, act: bootTimeline, from: '.bt-head'},
    {name: '45-dunkel-startablauf', path: '/system/services', real: process.env.WALK_BOOT_URL ?? '', bearer: process.env.WALK_BOOT_TOKEN ?? '', needsReal: true, act: bootTimeline, from: '.bt-head', ...DARK},
];

// one real system's radio setup per run (see the header)
if (process.env.WALK_RADIO_URL && process.env.WALK_RADIO_TOKEN && process.env.WALK_RADIO_NAMES) {
    const [a, b] = process.env.WALK_RADIO_NAMES.split(',');
    const real = {real: process.env.WALK_RADIO_URL, bearer: process.env.WALK_RADIO_TOKEN, radio: true};
    if (a) SHOTS.push({name: a, path: '/system/interfaces', ...real});
    if (b) SHOTS.push({name: b, path: '/system/lan-devices', ...real});
}
const browser = await chromium.launch({args: ['--lang=de-DE']});
for (const shot of SHOTS) {
    if (only.length && !only.includes(shot.name)) continue;
    if (shot.needsReal && !(shot.real && shot.bearer)) {
        console.error(`${shot.name}: skipped, WALK_BOOT_URL / WALK_BOOT_TOKEN are not set`);
        continue;
    }
    if (shot.hmm && !HMM) {
        console.error(`${shot.name}: skipped, WALK_HMM_URL is not set`);
        continue;
    }
    const theme = shot.dark ? 'dark' : 'light';
    const origin = shot.real || base;
    const vw = shot.phone ? 390 : 1280;
    const vh = shot.phone ? 844 : 900;
    if (shot.phone && !shot.height) shot.height = vh;
    const ctx = await browser.newContext({...(shot.phone ? {deviceScaleFactor: 2, isMobile: true, hasTouch: true} : {}), ...(shot.bearer ? {extraHTTPHeaders: {Authorization: `Bearer ${shot.bearer}`}} : {}), ignoreHTTPSErrors: !!shot.real, viewport: {width: vw, height: vh}, colorScheme: theme, locale: 'de-DE', timezoneId: 'Europe/Berlin', serviceWorkers: 'block'});
    await ctx.addInitScript((th) => {
        try {
            localStorage.setItem('ol.language', 'de');
            localStorage.setItem('ol.theme', th);
            localStorage.setItem('hmm.theme', th);
        } catch {
            /* no storage: the defaults */
        }
    }, theme);
    if (shot.app) await ctx.addCookies([{name: 'stub-app', value: `walk-${shot.name}`, url: base}]);
    if (shot.real) console.error(`${shot.name}: from ${new URL(shot.real).protocol}//<real system>, signed out, no patching`);
    // the addon pins are per browser in the stub (stub-prefs): a fresh set per shot
    if (shot.prefs) await ctx.addCookies([{name: 'stub-prefs', value: `walk-${shot.name}-${Date.now()}`, url: base}]);
    // a real system's page: only the viewer's own pins are set, as on every other image (nothing on the system)
    if (shot.real && shot.bearer) await ctx.route('**/api/auth/v1/me/preferences', (route) => (route.request().method() === 'GET' ? route.fulfill({json: {addons: [{id: 'hmm', pinned: true}, ...PINS]}}) : route.abort()));
    if (!shot.real) await ctx.route('**/api/**', async (route) => {
        const req = route.request();
        const u = new URL(req.url());
        if (req.method() === 'GET' && u.pathname === '/api/auth/v1/state' && shot.state) return route.fulfill({json: shot.state});
        if (req.method() !== 'GET') return route.fallback();
        let response;
        try {
            response = await route.fetch();
        } catch {
            return route.fallback();
        }
        const type = response.headers()['content-type'] ?? '';
        if (!type.includes('json')) return route.fulfill({response});
        let text = await response.text();
        for (const [re, to] of TEXT) text = text.replace(re, to);
        let json;
        try {
            json = JSON.parse(text);
        } catch {
            return route.fulfill({response, body: text});
        }
        const own = shot.patch?.[u.pathname];
        const p = own ?? PATCH[u.pathname];
        if (p) json = p(json, shot);
        // the manager's frame opens on the page the shot wants, in demo mode
        if (shot.hmm && u.pathname === '/api/system/v1/nav') json = {...json, entries: (json.entries ?? []).map((e) => (e.addon === 'mh' ? {...e, href: '/addons/mh/'} : e))};
        return route.fulfill({response, json});
    });
    // the pinned addons' icons: their frontends' pages name their own favicon files
    const icon = (file) => (file && fs.existsSync(file) ? fs.readFileSync(file) : null);
    const hmmIcon = icon(process.env.WALK_HMM_ICON);
    const redIcon = icon(process.env.WALK_REDMATIC_ICON);
    // (on a real system too: its frontends answer a token session with a login page, so their pages are stood in for)
    const iconsHere = !shot.real || !!shot.bearer;
    if (iconsHere && hmmIcon) {
        // inline: the probe of a separate favicon.ico never reached the route in headless Chromium
        await ctx.route(/\/addons\/(mh|hmm)\/(\?.*)?$/, (route) => route.fulfill({contentType: 'text/html', body: `<!doctype html><html><head><link rel="icon" href="data:image/x-icon;base64,${hmmIcon.toString('base64')}"></head><body></body></html>`}));
    }
    if (iconsHere && redIcon) {
        await ctx.route(/\/addons\/red\/(\?.*)?$/, (route) => route.fulfill({contentType: 'text/html', body: '<!doctype html><html><head><link rel="icon" type="image/png" href="/addons/redmatic/favicon-96x96.png"></head><body></body></html>'}));
        await ctx.route('**/addons/redmatic/favicon-96x96.png', (route) => route.fulfill({contentType: 'image/png', body: redIcon}));
    }
    if (shot.hmm) {
        // registered last, so it wins over the /api/ route for the manager's own paths
        await ctx.route('**/addons/mh/**', async (route) => {
            const u = new URL(route.request().url());
            const rest = u.pathname.replace(/^\/addons\/mh\//, '');
            if (rest === '' || rest === 'index.html') {
                // the frame's document: demo mode, then straight to the shot's page
                const r = await route.fetch({url: new URL(`?demo`, HMM).href});
                let page = await r.text();
                if (hmmIcon) page = page.replace(/<link rel="icon"[^>]*>/, `<link rel="icon" href="data:image/x-icon;base64,${hmmIcon.toString('base64')}">`);
                const html = page.replace('<head>', `<head><script>if (!location.search.includes('demo')) history.replaceState(null, '', '?demo${shot.hmm}'); else if (location.hash !== '${shot.hmm}') location.hash = '${shot.hmm}';</script>`);
                return route.fulfill({status: 200, contentType: 'text/html; charset=utf-8', body: html});
            }
            return route.fulfill({response: await route.fetch({url: new URL(rest + u.search, HMM).href})});
        });
    }
    const page = await ctx.newPage();
    await page.goto(new URL(shot.path, origin).href);
    await page.waitForTimeout(1800);
    if (shot.act) {
        try {
            await shot.act(page);
        } catch (e) {
            console.error(`${shot.name}: ${e.message.split('\n')[0]}`);
        }
        await page.waitForTimeout(900);
    }
    let h = shot.height;
    if (h && h !== vh) {
        await page.setViewportSize({width: vw, height: h});
        await page.waitForTimeout(500);
    }
    if (!h) {
        h = await page.evaluate(() => {
            const s = document.querySelector('.ol-scrollport');
            const hd = document.querySelector('.ol-header');
            return (s ? s.scrollHeight : document.body.scrollHeight) + (hd ? hd.offsetHeight : 0);
        });
        h = Math.min(Math.max(h, 600), 4000);
        await page.setViewportSize({width: vw, height: h});
        await page.waitForTimeout(500);
    }
    // a token's browser session says so above every page; the picture is of the page, not of how it was taken
    if (shot.bearer) await page.evaluate(() => document.querySelectorAll('[data-testid="token-readonly"]').forEach((e) => e.remove()));
    const rules = shot.real ? [...RULES.filter((x) => x[2] !== 'author'), ...(shot.radio ? REAL_RULES : [])] : RULES;
    await page.evaluate(sweep, rules);
    // the addon frames (the manager) are swept the same way
    for (const fr of page.frames()) if (fr !== page.mainFrame()) await fr.evaluate(sweep, rules).catch(() => {});
    // `from`: only the part of the page from that element down (the boot timeline, not the table above it)
    let clip;
    if (shot.from) {
        const box = await page.locator(shot.from).first().boundingBox();
        if (box) clip = {x: 0, y: Math.max(0, box.y - 12), width: vw, height: h - Math.max(0, box.y - 12)};
    }
    await page.screenshot({path: path.join(out, `${shot.name}.png`), ...(clip ? {clip} : {})});
    // WALK_TEXT_DIR: the page's text as shot (inputs included), for the check that nothing slipped through
    if (process.env.WALK_TEXT_DIR) {
        const grab = () => [document.body.innerText, ...[...document.querySelectorAll('input, textarea')].map((e) => `${e.value} ${e.placeholder ?? ''}`), ...[...document.querySelectorAll('[title]')].map((e) => e.title)].join('\n');
        const frames = [];
        for (const fr of page.frames()) if (fr !== page.mainFrame()) frames.push(await fr.evaluate(grab).catch(() => ''));
        const text = frames.join('\n') + '\n' + await page.evaluate(() => [document.body.innerText, ...[...document.querySelectorAll('input, textarea')].map((e) => `${e.value} ${e.placeholder ?? ''}`), ...[...document.querySelectorAll('[title]')].map((e) => e.title)].join('\n'));
        fs.mkdirSync(process.env.WALK_TEXT_DIR, {recursive: true});
        fs.writeFileSync(path.join(process.env.WALK_TEXT_DIR, `${shot.name}.txt`), text);
    }
    console.log(shot.name, h);
    await ctx.close();
}
await browser.close();
