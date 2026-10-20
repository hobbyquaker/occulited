/*
 * Task 57: the system pages and the menu that reaches them. Every system page lives under
 * /system/<page> (maintainer, 2026-09-12), so the URL says where the reader is and every page can be
 * linked to; the paths the pages had before stand for the new ones and are rewritten by the router
 * (`systemAlias`), query string included, so bookmarks, the Status page's warnings and the links of
 * other pages keep working.
 *
 * The menu (D-68): one flat list in a fixed logical order - the network first, what protects it, who
 * may use the box, what runs on it, what it wrote, what to keep, then the parts of the hardware.
 * A new system page is slotted in by topic. Firmware moved in from the tab bar (D-80), after Backup;
 * Interfaces and Metadata moved in from it too (task 131, D-86), at the top.
 *
 * The filter matches an entry's label in both languages and a few keywords per entry - what a reader
 * knows the matter by rather than what the page is called: ACME finds Certificate, OIDC finds
 * Users, systemd finds Services. The keywords are not shown, so they live here, not in the
 * catalogue; the labels are the catalogue's keys.
 *
 * Pure: no Svelte, no DOM, so vitest runs it (systemmenu.test.ts).
 */
import type {IconName} from './Icon.svelte';

export const SYSTEM_BASE = '/system';

export interface SystemPage {
    /** the page's name in the menu's state and in the warning dots */
    id: string;
    /** its path, `/system/<id>` */
    path: string;
    /** the catalogue key of its label */
    label: string;
    icon: IconName;
    /** what the filter finds it by besides the label, per language; never shown */
    keywords: {de: string[]; en: string[]};
    /** the paths the page had before task 57 (or 131); the router rewrites them */
    old: string[];
}

export const SYSTEM_PAGES: readonly SystemPage[] = [
    // task 131 (D-86): the page that was a tab, first - after Status it is what the box is opened
    // for most (proposed for the maintainer's look). Its old tab path stays an alias, so the Status
    // page's /radio#security-key and every bookmark keep working, anchor and query included. The
    // Metadata page that stood beside it went with task 193: the App is the editor of names,
    // rooms, functions and favorites, and /metadata, /names and /system/metadata lead there.
    {id: 'interfaces', path: '/system/interfaces', label: 'Interfaces', icon: 'radio', keywords: {en: ['radio', 'HmIP', 'BidCos', 'Wired', 'duty cycle', 'coprocessor', 'radio firmware', 'USB', 'subscribers', 'rfd', 'hmipserver'], de: ['Funk', 'HmIP', 'BidCos', 'Wired', 'Duty Cycle', 'Funkmodul', 'Funkfirmware', 'USB', 'Abonnenten', 'rfd', 'hmipserver']}, old: ['/radio']},
    // openccu-lite task 222 (the maintainer, 2026-09-24): "move lan-devices to their own page, including
    // bidcos-gateways and hmip-access-points" - beside Interfaces, the devices this system reaches its
    // radio through over the network
    {id: 'lan-devices', path: '/system/lan-devices', label: 'LAN devices', icon: 'router', keywords: {en: ['LAN gateway', 'BidCoS gateway', 'HM-LGW', 'HMW-LGW', 'HB-RF-ETH', 'network radio board', 'access point', 'HAP', 'DRAP', 'NetFinder', 'eQ-3 discovery'], de: ['LAN-Gateway', 'BidCoS-Gateway', 'HM-LGW', 'HMW-LGW', 'HB-RF-ETH', 'Netzwerk-Funkplatine', 'Access Point', 'HAP', 'DRAP', 'NetFinder', 'eQ-3-Discovery']}, old: []},
    // task 183: the radio keys, off the Interfaces page - rfd's security key, the HmIP network key
    // and the HmIP devices' keys
    {id: 'keys', path: '/system/keys', label: 'Keys', icon: 'key', keywords: {en: ['security key', 'AES', 'BidCos', 'HmIP', 'local key mode', 'network key', 'key server', 'device keys', 'SGTIN', 'QR code', 'key sheet', 'sgtin.map'], de: ['Sicherheitsschlüssel', 'Zentralenschlüssel', 'AES', 'BidCos', 'HmIP', 'lokaler Schlüssel', 'Netzwerkschlüssel', 'Schlüsselserver', 'Geräteschlüssel', 'SGTIN', 'QR-Code', 'Schlüsselblatt', 'sgtin.map']}, old: []},
    {id: 'network', path: '/system/network', label: 'Network', icon: 'network', keywords: {en: ['IP', 'DNS', 'hostname', 'DHCP', 'static', 'interfaces', 'Ethernet', 'Wi-Fi', 'time', 'NTP', 'timezone'], de: ['IP', 'DNS', 'Hostname', 'DHCP', 'statisch', 'Schnittstellen', 'Ethernet', 'WLAN', 'Zeit', 'NTP', 'Zeitzone']}, old: ['/network']},
    {id: 'firewall', path: '/system/firewall', label: 'Firewall', icon: 'shield', keywords: {en: ['ports', 'iptables', 'policy', 'rules', 'source', 'listening'], de: ['Ports', 'iptables', 'Policy', 'Regeln', 'Source', 'lauschend']}, old: []},
    // task 143 (D-95): the door a CCU's clients use, after what guards it
    {id: 'remote-access', path: '/system/remote-access', label: 'Remote access', icon: 'globe', keywords: {en: ['SSH', 'shell', 'root', 'authorized_keys', 'key', 'session', 'tokens', 'API', 'API tokens', 'RPC', 'XML-RPC', 'classic RPC', 'CCU', 'ports', '2001', '2010', 'TLS', 'ioBroker', 'Home Assistant', 'Node-RED', 'lite-rpc'], de: ['SSH', 'Shell', 'root', 'authorized_keys', 'Schlüssel', 'Sitzung', 'Tokens', 'API', 'API-Tokens', 'RPC', 'XML-RPC', 'klassisches RPC', 'CCU', 'Ports', '2001', '2010', 'TLS', 'ioBroker', 'Home Assistant', 'Node-RED', 'lite-rpc']}, old: []},
    // task 132 (D-87): the HTTPS redirect, HSTS and the bare-host redirect are on this page now
    // openccu-lite task 231: the four trust stores, after the certificate they verify against
    {id: 'trust', path: '/system/trust', label: 'Trust stores', icon: 'lock', keywords: {en: ['CA', 'certificate authority', 'trust store', 'root certificate', 'PEM', 'DER', 'GitHub', 'eQ-3', 'bundle', 'distrust'], de: ['CA', 'Zertifizierungsstelle', 'Vertrauensspeicher', 'Stammzertifikat', 'PEM', 'DER', 'GitHub', 'eQ-3', 'Bundle', 'Misstrauen']}, old: []},
    {id: 'certificates', path: '/system/certificates', label: 'Certificate', icon: 'award', keywords: {en: ['ACME', 'TLS', 'HTTPS', "Let's Encrypt", 'CA', 'renewal', 'self-signed', 'HSTS', 'HTTPS redirect', 'redirect', 'domain name'], de: ['ACME', 'TLS', 'HTTPS', "Let's Encrypt", 'CA', 'Verlängerung', 'selbstsigniert', 'HSTS', 'HTTPS-Umleitung', 'Umleitung', 'Domänenname']}, old: ['/certificate']},
    // task 132 (D-87): the login settings and the API tokens are on this page now; the Security
    // page's paths stand for it (its HTTPS anchor for the Certificate page's, and the addon sessions'
    // anchor for the Addons page's since the maintainer's follow-up of 2026-09-16)
    {id: 'users', path: '/system/users', label: 'Users', icon: 'users', keywords: {en: ['accounts', 'roles', 'administrator', 'password', 'login', 'authentication', 'OIDC', 'OpenID Connect', 'SSO', 'password login', 'sessions', 'security'], de: ['Konten', 'Rollen', 'Administrator', 'Passwort', 'Anmeldung', 'Authentifizierung', 'OIDC', 'OpenID Connect', 'SSO', 'Passwort-Anmeldung', 'Sitzungen', 'Sicherheit']}, old: ['/users', '/security', '/system/security']},
    {id: 'services', path: '/system/services', label: 'Services', icon: 'server', keywords: {en: ['units', 'timers', 'systemd', 'daemons', 'boot', 'restart'], de: ['Units', 'Timer', 'systemd', 'Daemons', 'Start', 'Neustart']}, old: ['/services']},
    {id: 'log', path: '/system/log', label: 'Log', icon: 'log', keywords: {en: ['journal', 'kernel', 'dmesg', 'messages', 'syslog', 'levels'], de: ['Journal', 'Kernel', 'dmesg', 'Meldungen', 'Syslog', 'Stufen']}, old: ['/log']},
    // openccu-lite task 228: the USB sticks (Format, Safely remove), later the shares - before Backup
    {id: 'storage', path: '/system/storage', label: 'Storage', icon: 'disk', keywords: {en: ['USB stick', 'format', 'exFAT', 'ext4', 'eject', 'safely remove', 'drive', 'share'], de: ['USB-Stick', 'formatieren', 'exFAT', 'ext4', 'auswerfen', 'sicher entfernen', 'Laufwerk', 'Freigabe']}, old: []},
    {id: 'backup', path: '/system/backup', label: 'Backup', icon: 'archive', keywords: {en: ['restore', 'sbk', 'nightly', 'target', 'USB', 'download'], de: ['Wiederherstellen', 'sbk', 'nächtlich', 'Ziel', 'USB', 'Herunterladen']}, old: ['/backup']},
    {id: 'updates', path: '/system/updates', label: 'Updates', icon: 'firmware', keywords: {en: ['system update', 'release', 'firmware', 'device firmware', 'eQ-3', 'install'], de: ['Systemupdate', 'Release', 'Firmware', 'Gerätefirmware', 'eQ-3', 'installieren']}, old: ['/firmware', '/system/firmware']},
    // openccu-lite task 340 (the maintainer, 2026-10-15): the Status LED page became the Hardware
    // page - the status LED, the HDMI output, later WLAN and Bluetooth; the LED's paths stand for it
    {id: 'hardware', path: '/system/hardware', label: 'Hardware', icon: 'memory', keywords: {en: ['Status LED', 'LED', 'light', 'colour', 'RGB', 'locate', 'night', 'HDMI', 'monitor', 'display', 'board LEDs'], de: ['Statusleuchte', 'LED', 'Leuchte', 'Farbe', 'RGB', 'Finden', 'Nacht', 'HDMI', 'Monitor', 'Bildschirm', 'Platinen-LEDs']}, old: ['/led', '/system/led']},
];

/**
 * Task 132 (D-87): sections that moved to another page than the one their old path now stands
 * for. The Security page's paths are the Users page - its login settings and API tokens are there -
 * but its HTTPS section is the Certificate page's, so `#https` (and an HSTS or redirect anchor) on
 * those paths goes there. The addon sessions' switch was on Users (task 132) and is on the Addons
 * page since the maintainer's follow-up (2026-09-16), so `#addon-sessions` on the Users page's and
 * the Security page's paths goes to `/addons#addon-sessions`. Matched on the old path itself only.
 */
const MOVED_SECTIONS: readonly {from: readonly string[]; anchors: Readonly<Record<string, string>>; to: string}[] = [
    {from: ['/security', '/system/security'], anchors: {'#https': '#https', '#hsts': '#https', '#redirect': '#https'}, to: '/system/certificates'},
    {from: ['/security', '/system/security', '/users', '/system/users'], anchors: {'#addon-sessions': '#addon-sessions'}, to: '/addons'},
    // task 183: the keys left the Interfaces page (and its old path /radio, the security-key
    // warning's link since task 81)
    {from: ['/radio', '/system/interfaces'], anchors: {'#security-key': '#security-key', '#local-key': '#local-key', '#device-keys': '#device-keys'}, to: '/system/keys'},
    // task 185: SSH left the Network page (and its old path /network)
    {from: ['/network', '/system/network'], anchors: {'#ssh': '#ssh'}, to: '/system/remote-access'},
    // the maintainer, 2026-09-19: the API tokens left the Users page (and the Security page's paths)
    {from: ['/users', '/system/users', '/security', '/system/security'], anchors: {'#api-tokens': '#api-tokens'}, to: '/system/remote-access'},
    // openccu-lite task 222: the gateways, the access points and the LAN devices left the Interfaces page
    {from: ['/radio', '/system/interfaces'], anchors: {'#gateways': '#gateways', '#access-points': '#access-points', '#lan-devices': '#lan-devices', '#hb-rf-eth': '#hb-rf-eth'}, to: '/system/lan-devices'},
    // openccu-lite task 223: Control without a login left the Remote access page for the Settings page
    {from: ['/system/remote-access'], anchors: {'#public': '#public'}, to: '/settings'},
    // openccu-lite task 224: the RPC trace left the Interfaces page for the lite-rpc section
    {from: ['/radio', '/system/interfaces'], anchors: {'#rpc-trace': '#rpc-trace'}, to: '/system/remote-access'},
];

/**
 * Where an old path with an anchor leads when the anchor's section moved on its own: the new path
 * and anchor, or undefined when the anchor is not one of those (the path's alias applies then).
 */
export function sectionAlias(path: string, hash: string): {path: string; hash: string} | undefined {
    const bare = path.length > 1 && path.endsWith('/') ? path.slice(0, -1) : path;
    for (const m of MOVED_SECTIONS) {
        const to = m.anchors[hash.toLowerCase()];
        if (to && m.from.includes(bare)) return {path: m.to, hash: to};
    }
    return undefined;
}

/** the page at `path`, or the one `path` is below; undefined off the system pages */
export function systemPageAt(path: string): SystemPage | undefined {
    return SYSTEM_PAGES.find((p) => path === p.path || path.startsWith(`${p.path}/`));
}

/**
 * The path an old system path stands for: `/services` is `/system/services`, `/certificate` is
 * `/system/certificates`, `/system` alone the page this browser showed last (`last`, or Services).
 * '' for every other path. Only the path itself and the paths below it (`/log/` too), never a longer
 * word: `/login` is not `/log` (B-63), `/logs` neither.
 */
export function systemAlias(path: string, last = ''): string {
    if (path === SYSTEM_BASE || path === `${SYSTEM_BASE}/`) return systemPageAt(last)?.path ?? (last && last !== path ? systemPageAt(systemAlias(last))?.path : undefined) ?? '/system/services';
    for (const p of SYSTEM_PAGES) {
        for (const o of p.old) {
            if (path === o) return p.path;
            if (path.startsWith(`${o}/`)) return p.path + path.slice(o.length);
        }
    }
    return '';
}

/**
 * The system page a warning's link leads to (task 81's `href`: `/backup`, `/log?since=…`,
 * `/certificate`, `/radio#security-key`), for the dots in the menu; undefined for a
 * link elsewhere (`#storage`, `/addons`). The old paths count, so nothing on the server changes.
 */
export function systemPageOf(href: string | undefined): SystemPage | undefined {
    if (!href || href[0] !== '/') return undefined;
    const path = href.split(/[?#]/)[0] ?? '';
    // a section that moved on its own (task 183: /system/interfaces#local-key is the Keys page's)
    const hash = href.includes('#') ? href.slice(href.indexOf('#')) : '';
    const moved = hash ? sectionAlias(path, hash) : undefined;
    return systemPageAt(moved?.path ?? (systemAlias(path) || path));
}

export type Severity = 'error' | 'warning';

/** the dot each page shows: the worst active, unsilenced warning that leads to it (a notice, info, lights none: task 366) */
export function pageDots(warnings: {href?: string; severity: Severity | 'info'; silenced?: unknown}[]): Record<string, Severity> {
    const out: Record<string, Severity> = {};
    for (const w of warnings) {
        if (w.silenced || w.severity === 'info') continue;
        const p = systemPageOf(w.href);
        if (!p) continue;
        if (w.severity === 'error' || !out[p.id]) out[p.id] = w.severity;
    }
    return out;
}

/**
 * Task 248, the maintainer's decision: the Addons tab's dot is red for an addon that failed or
 * ended (`addon-failed`, `addon-ended`), yellow for an available update (`addon-update`), and
 * nothing else - the other warnings that link to /addons (rega, legacy-session, ...) stay on the
 * Status page. A silenced warning lights nothing.
 */
const ADDON_RED = new Set(['addon-failed', 'addon-ended']);
export function addonsDot(warnings: {id: string; silenced?: unknown}[]): Severity | undefined {
    let out: Severity | undefined;
    for (const w of warnings) {
        if (w.silenced) continue;
        if (ADDON_RED.has(w.id)) return 'error';
        if (w.id === 'addon-update') out = 'warning';
    }
    return out;
}

/** the worst of the pages' dots, for the tab; undefined without any */
export function worstDot(dots: Record<string, Severity>): Severity | undefined {
    const all = Object.values(dots);
    return all.includes('error') ? 'error' : all.includes('warning') ? 'warning' : undefined;
}

/** lower case without accents, so `zertifikate` finds Zertifikate and `verlangerung` Verlängerung */
export function fold(s: string): string {
    return s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
}

/**
 * The pages a filter text finds, in the menu's order: every one whose label in either language or
 * whose keywords contain the text. An empty filter finds all of them. `labels` gives the labels of a
 * page in both languages (the catalogue's, so the German label counts for an English reader too).
 */
export function matchPages(query: string, labels: (p: SystemPage) => string[], pages: readonly SystemPage[] = SYSTEM_PAGES): SystemPage[] {
    const q = fold(query.trim());
    if (q === '') return [...pages];
    return pages.filter((p) => [...labels(p), ...p.keywords.de, ...p.keywords.en].some((s) => fold(s).includes(q)));
}

/** where the arrow keys go in a list of `n` entries from `at` (-1: none focused): wrapping at both ends */
export function step(at: number, n: number, key: string): number {
    if (n === 0) return -1;
    switch (key) {
        case 'ArrowDown':
            return at < 0 || at >= n - 1 ? 0 : at + 1;
        case 'ArrowUp':
            return at <= 0 ? n - 1 : at - 1;
        case 'Home':
            return 0;
        case 'End':
            return n - 1;
    }
    return at;
}

/**
 * Task 188: the System menu's keyboard shortcut as this platform writes it - `⌘ K` on a Mac and on
 * iPadOS, `Ctrl K` everywhere else. The binding itself is the same on both (Ctrl or ⌘ plus K,
 * lib/SystemMenu.svelte's onWindowKey); only its name differs, and a hint that names the wrong key
 * is worse than none.
 *
 * The platform string is a parameter so the test can ask for each of them; `navigator.platform` is
 * what every browser still answers for this question (iPadOS says `MacIntel` as well, which is the
 * right answer here - it has a ⌘ key when it has a keyboard at all).
 */
export function shortcutLabel(platform = typeof navigator === 'undefined' ? '' : navigator.platform || navigator.userAgent): string {
    return /mac|iphone|ipad|ipod/i.test(platform) ? '⌘ K' : 'Ctrl K';
}

/**
 * Whether the filter takes the focus when this click opens the menu (task 188; the maintainer,
 * 2026-09-19: *"when opening system menu the cursor should be placed in filter input so user can
 * start typing something right away"*).
 *
 * The question is only ever whether an on-screen keyboard would come up and cover what was just
 * opened, so the input device decides it, not the window's width: a finger never focuses the
 * filter, a mouse, a pen and the keyboard always do. A click made by the keyboard (Enter or Space
 * on the tab or the title) has `detail` 0 and no pointer type of its own; a click whose pointer
 * type is missing altogether - an older browser, a synthetic click - is decided by what the
 * machine has (`pointer: fine`), which is the same question asked of the device instead of the
 * event.
 */
export function opensFilter(ev?: {detail?: number; pointerType?: string}, fine: () => boolean = finePointer): boolean {
    if (!ev || ev.detail === 0) return true;
    if (ev.pointerType === 'touch') return false;
    if (ev.pointerType === 'mouse' || ev.pointerType === 'pen') return true;
    return fine();
}

/** whether this machine has a pointer it can point precisely with - a mouse, a trackpad, a pen */
function finePointer(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(pointer: fine)').matches;
}
