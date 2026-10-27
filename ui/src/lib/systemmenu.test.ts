import {describe, expect, it} from 'vitest';
import {addonsDot, SYSTEM_PAGES, fold, matchPages, opensFilter, pageDots, sectionAlias, shortcutLabel, step, systemAlias, systemPageAt, systemPageOf, worstDot} from './systemmenu';

// task 57: the system pages under /system/<page>, the old paths as aliases, the menu's filter and
// the warning dots

const labels = (p: {label: string}) => [p.label, {Interfaces: 'Schnittstellen', Network: 'Netzwerk', Certificate: 'Zertifikat', Users: 'Benutzer', Services: 'Dienste', Log: 'Protokoll', Backup: 'Sicherung', 'Status LED': 'Statusleuchte'}[p.label] ?? p.label];

describe('the system pages', () => {
    it('are one flat list in the fixed order of D-68 with Firmware after Backup (D-80), Interfaces first (D-86), no Security (D-87), Keys after Interfaces (task 183), no Metadata (task 193)', () => {
        // openccu-lite task 222: LAN devices beside Interfaces
        expect(SYSTEM_PAGES.map((p) => p.id)).toEqual(['interfaces', 'lan-devices', 'keys', 'network', 'firewall', 'remote-access', 'trust', 'certificates', 'users', 'services', 'log', 'storage', 'backup', 'updates', 'hardware']);
    });
    it('each live under /system/<id>', () => {
        for (const p of SYSTEM_PAGES) expect(p.path).toBe(`/system/${p.id}`);
    });
    it('carry keywords in both languages', () => {
        for (const p of SYSTEM_PAGES) {
            expect(p.keywords.de.length, p.id).toBeGreaterThan(0);
            expect(p.keywords.en.length, p.id).toBeGreaterThan(0);
        }
    });
    it.each([
        ['/system/log', 'log'],
        ['/system/log/', 'log'],
        ['/system/certificates', 'certificates'],
        ['/system/hardware', 'hardware'],
        ['/system/interfaces', 'interfaces'],
        ['/system/metadata', undefined],
        ['/system/security', undefined],
        ['/system', undefined],
        ['/system/logs', undefined],
        ['/log', undefined],
        ['/', undefined],
    ])('%s is the page %s', (path, id) => {
        expect(systemPageAt(path)?.id).toBe(id);
    });
});

describe('systemAlias', () => {
    it.each([
        ['/services', '/system/services'],
        ['/users', '/system/users'],
        ['/log', '/system/log'],
        ['/log/', '/system/log/'],
        ['/network', '/system/network'],
        ['/certificate', '/system/certificates'],
        // task 132: the Security page's paths are the Users page
        ['/security', '/system/users'],
        ['/system/security', '/system/users'],
        ['/system/security/', '/system/users/'],
        ['/securityx', ''],
        ['/system/securityx', ''],
        ['/backup', '/system/backup'],
        ['/firmware', '/system/updates'],
        ['/system/firmware', '/system/updates'],
        ['/led', '/system/hardware'],
        ['/system/led', '/system/hardware'],
        // task 131: the two tabs that became system pages
        ['/radio', '/system/interfaces'],
        // task 193: the Metadata page is gone; its paths are the router's (they open the App), not the menu's
        ['/metadata', ''],
        ['/names', ''],
        ['/radio/', '/system/interfaces/'],
        ['/radios', ''],
        ['/metadata-x', ''],
        ['/namespace', ''],
        ['/system/interfaces', ''],
        ['/system/metadata', ''],
        // B-63's lesson: the path itself and what is below it, never a longer word
        ['/login', ''],
        ['/logs', ''],
        ['/services-x', ''],
        ['/system/services', ''],
        ['/addons', ''],
        ['/', ''],
    ])('%s stands for %j', (path, want) => {
        expect(systemAlias(path)).toBe(want);
    });
    it('/system alone is the last system page of this browser, Services without one', () => {
        expect(systemAlias('/system')).toBe('/system/services');
        expect(systemAlias('/system/')).toBe('/system/services');
        expect(systemAlias('/system', '/system/backup')).toBe('/system/backup');
        expect(systemAlias('/system', '/nowhere')).toBe('/system/services');
        // a page that went: the page its path stands for now
        expect(systemAlias('/system', '/system/security')).toBe('/system/users');
    });
});

describe('sectionAlias', () => {
    it.each([
        ['/system/security', '#https', {path: '/system/certificates', hash: '#https'}],
        ['/security', '#https', {path: '/system/certificates', hash: '#https'}],
        ['/system/security/', '#https', {path: '/system/certificates', hash: '#https'}],
        ['/system/security', '#hsts', {path: '/system/certificates', hash: '#https'}],
        ['/system/security', '#HSTS', {path: '/system/certificates', hash: '#https'}],
        ['/security', '#redirect', {path: '/system/certificates', hash: '#https'}],
        // the other sections stay with the path's alias, the Users page
        // the maintainer, 2026-09-19: the API tokens moved to the Remote access page
        ['/system/security', '#api-tokens', {path: '/system/remote-access', hash: '#api-tokens'}],
        ['/system/users', '#api-tokens', {path: '/system/remote-access', hash: '#api-tokens'}],
        // openccu-lite task 223: Control without a login is the Settings page's; the page's other anchors stay
        ['/system/remote-access', '#public', {path: '/settings', hash: '#public'}],
        ['/system/remote-access', '#ssh', undefined],
        ['/system/security', '', undefined],
        ['/system/users', '#https', undefined],
        ['/system/certificates', '#https', undefined],
        ['/security/x', '#https', undefined],
        // the follow-up of 2026-09-16: the addon sessions are on the Addons page
        ['/system/users', '#addon-sessions', {path: '/addons', hash: '#addon-sessions'}],
        ['/system/users/', '#addon-sessions', {path: '/addons', hash: '#addon-sessions'}],
        ['/users', '#addon-sessions', {path: '/addons', hash: '#addon-sessions'}],
        ['/system/security', '#addon-sessions', {path: '/addons', hash: '#addon-sessions'}],
        ['/security', '#Addon-Sessions', {path: '/addons', hash: '#addon-sessions'}],
        ['/system/users', '#authentication', undefined],
        ['/addons', '#addon-sessions', undefined],
        ['/system/certificates', '#addon-sessions', undefined],
        // task 183: the key sections are the Keys page's; the rest of the Interfaces page stays
        ['/radio', '#security-key', {path: '/system/keys', hash: '#security-key'}],
        ['/system/interfaces', '#security-key', {path: '/system/keys', hash: '#security-key'}],
        ['/system/interfaces', '#local-key', {path: '/system/keys', hash: '#local-key'}],
        ['/system/interfaces', '#device-keys', {path: '/system/keys', hash: '#device-keys'}],
        ['/system/interfaces', '#connections', undefined],
        // openccu-lite task 222: the gateways, the access points and the LAN devices have a page of their own
        ['/system/interfaces', '#gateways', {path: '/system/lan-devices', hash: '#gateways'}],
        ['/radio', '#access-points', {path: '/system/lan-devices', hash: '#access-points'}],
        ['/system/interfaces', '#lan-devices', {path: '/system/lan-devices', hash: '#lan-devices'}],
        ['/system/keys', '#security-key', undefined],
        // openccu-lite task 224: the RPC trace is the Remote access page's, last under lite-rpc
        ['/system/interfaces', '#rpc-trace', {path: '/system/remote-access', hash: '#rpc-trace'}],
        ['/radio', '#RPC-Trace', {path: '/system/remote-access', hash: '#rpc-trace'}],
        ['/system/remote-access', '#rpc-trace', undefined],
    ])('%s%s is %j', (path, hash, want) => {
        expect(sectionAlias(path, hash)).toEqual(want);
    });
});

describe('the warning dots', () => {
    it.each([
        ['/backup', 'backup'],
        ['/system/backup', 'backup'],
        ['/certificate', 'certificates'],
        ['/log?since=@1757660000', 'log'],
        ['/log?settings=journal', 'log'],
        ['/services', 'services'],
        ['/security', 'users'],
        ['/app', undefined],
        // task 183: the key sections moved to the Keys page, their old anchors with them
        ['/radio#security-key', 'keys'],
        ['/system/interfaces#security-key', 'keys'],
        ['/system/interfaces#local-key', 'keys'],
        ['/system/keys#device-keys', 'keys'],
        ['/system/interfaces#connections', 'interfaces'],
        ['#storage', undefined],
        ['/addons', undefined],
        ['', undefined],
        [undefined, undefined],
    ])('%s leads to %s', (href, id) => {
        expect(systemPageOf(href)?.id).toBe(id);
    });
    it('shows the worst active, unsilenced warning per page and for the tab', () => {
        const dots = pageDots([
            {href: '/backup', severity: 'warning'},
            {href: '/backup', severity: 'error'},
            {href: '/certificate', severity: 'error', silenced: {by: 'admin'}},
            {href: '/log?since=@1', severity: 'warning'},
            {href: '/app', severity: 'error'},
        ]);
        expect(dots).toEqual({backup: 'error', log: 'warning'});
        expect(worstDot(dots)).toBe('error');
        expect(worstDot({log: 'warning'})).toBe('warning');
        expect(worstDot({})).toBeUndefined();
    });
});

describe('the filter', () => {
    const ids = (q: string) => matchPages(q, labels).map((p) => p.id);
    it('finds every page with nothing typed, in order', () => {
        expect(ids('')).toEqual(SYSTEM_PAGES.map((p) => p.id));
        expect(ids('   ')).toEqual(SYSTEM_PAGES.map((p) => p.id));
    });
    it('matches the label in either language, without regard to case', () => {
        expect(ids('fire')).toEqual(['firewall']);
        expect(ids('DIENSTE')).toEqual(['services']);
        // task 231: 'Zertifizierungsstelle' is a keyword of the Trust stores page
        expect(ids('zerti')).toEqual(['trust', 'certificates']);
        // the radio interfaces by label, the network interfaces by Network's keyword
        expect(ids('schnitt')).toEqual(['interfaces', 'network']);
        expect(ids('metad')).toEqual([]);
    });
    it('matches a keyword: ACME finds Certificate, OIDC Users, systemd Services', () => {
        expect(ids('acme')).toEqual(['certificates']);
        expect(ids('oidc')).toEqual(['users']);
        // task 132: the Security page's keywords went with its sections
        expect(ids('hsts')).toEqual(['certificates']);
        expect(ids('https-umleitung')).toEqual(['certificates']);
        expect(ids('token')).toEqual(['remote-access']);
        expect(ids('passwort-anmeldung')).toEqual(['users']);
        // the addon sessions are the Addons page's since 2026-09-16, not a system page's
        expect(ids('sitzungsubergabe')).toEqual([]);
        expect(ids('addon sessions')).toEqual([]);
        expect(ids('systemd')).toEqual(['services']);
        expect(ids('restore')).toEqual(['backup']);
        expect(ids('duty')).toEqual(['interfaces']);
        expect(ids('sicherheitsschl')).toEqual(['keys']);
        expect(ids('sgtin')).toEqual(['keys']);
        // the BidCos-RF security key is on the Keys page (task 183)
        expect(ids('bidcos')).toEqual(['interfaces', 'lan-devices', 'keys']);
        expect(ids('netfinder')).toEqual(['lan-devices']);
        expect(ids('gewerke')).toEqual([]);
    });
    it('ignores accents', () => {
        expect(ids('verlangerung')).toEqual(['certificates']);
        expect(fold('Zeitzone Ä')).toBe('zeitzone a');
    });
    it('finds nothing for a text no page carries', () => {
        expect(ids('xyzzy')).toEqual([]);
    });
});

describe('the arrow keys', () => {
    it('move through the list and wrap at both ends', () => {
        expect(step(-1, 3, 'ArrowDown')).toBe(0);
        expect(step(0, 3, 'ArrowDown')).toBe(1);
        expect(step(2, 3, 'ArrowDown')).toBe(0);
        expect(step(0, 3, 'ArrowUp')).toBe(2);
        expect(step(-1, 3, 'ArrowUp')).toBe(2);
        expect(step(1, 3, 'Home')).toBe(0);
        expect(step(1, 3, 'End')).toBe(2);
        expect(step(1, 3, 'x')).toBe(1);
        expect(step(0, 0, 'ArrowDown')).toBe(-1);
    });
});

// task 188: the shortcut is written out in the UI, and a hint that names the wrong key is worse
// than none
describe('the shortcut label', () => {
    it('is ⌘ K on Apple platforms and Ctrl K elsewhere', () => {
        for (const p of ['MacIntel', 'iPhone', 'iPad', 'Macintosh; Intel Mac OS X 10_15_7']) expect(shortcutLabel(p)).toBe('⌘ K');
        for (const p of ['Win32', 'Linux x86_64', 'Linux armv8l', '']) expect(shortcutLabel(p)).toBe('Ctrl K');
    });
});

// task 188: the filter takes the focus unless an on-screen keyboard would come up over it
describe('whether an open focuses the filter', () => {
    const never = () => false;
    const always = () => true;
    it('the keyboard opens it: a click made by Enter or Space has detail 0', () => {
        expect(opensFilter({detail: 0}, never)).toBe(true);
        expect(opensFilter(undefined, never)).toBe(true); // Ctrl/⌘+K and any other open of our own
    });
    it('a mouse and a pen focus it, a finger never does', () => {
        expect(opensFilter({detail: 1, pointerType: 'mouse'}, never)).toBe(true);
        expect(opensFilter({detail: 1, pointerType: 'pen'}, never)).toBe(true);
        expect(opensFilter({detail: 1, pointerType: 'touch'}, always)).toBe(false);
    });
    it('a click without a pointer type asks the machine instead', () => {
        expect(opensFilter({detail: 1}, always)).toBe(true);
        expect(opensFilter({detail: 1, pointerType: ''}, never)).toBe(false);
    });
});

// task 248: the Addons tab's dot - red for a failed or ended addon, yellow for an update, nothing else
describe('addonsDot', () => {
    it('red for a failed or ended addon, yellow for an update, nothing for the rest', () => {
        expect(addonsDot([])).toBeUndefined();
        expect(addonsDot([{id: 'addon-update'}])).toBe('warning');
        expect(addonsDot([{id: 'addon-update'}, {id: 'addon-failed'}])).toBe('error');
        expect(addonsDot([{id: 'addon-ended'}])).toBe('error');
        expect(addonsDot([{id: 'rega'}, {id: 'legacy-session'}, {id: 'arch'}])).toBeUndefined();
        expect(addonsDot([{id: 'addon-update', silenced: {until: 'x'}}])).toBeUndefined();
        // occulited task 119: an addon's own notice - warning yellow, error red, info nothing, silenced nothing
        expect(addonsDot([{id: 'addon-notice', severity: 'warning'}])).toBe('warning');
        expect(addonsDot([{id: 'addon-notice', severity: 'error'}])).toBe('error');
        expect(addonsDot([{id: 'addon-notice', severity: 'info'}])).toBeUndefined();
        expect(addonsDot([{id: 'addon-notice', severity: 'error', silenced: {until: 'x'}}])).toBeUndefined();
        expect(addonsDot([{id: 'addon-notice', severity: 'info'}, {id: 'addon-update'}])).toBe('warning');
    });
});

describe('a notice lights no dot (openccu-lite task 366)', () => {
    it('leaves the Hardware page without a dot', () => {
        expect(pageDots([{href: '/system/hardware#bootloader', severity: 'info'}])).toEqual({});
    });
});
