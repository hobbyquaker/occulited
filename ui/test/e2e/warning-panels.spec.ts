import type {Locator, Page, Route} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 12 (the maintainer, 2026-10-03): every warning of the Status page is reflected on
// the page its button leads to - the panel there carries the warning's 4 px left edge in its
// severity colour while the warning is active (lib/warnedge.ts, lib/WarnEdge.svelte, app.css). The
// table holds every warning id the system raises (internal/httpapi/warnings.go) with its href; a
// warning whose target is a log view filtered to the moment, or the Control app, has no panel and is
// listed under NO_PANEL with the reason. Light and dark are the two desktop projects.

type Sev = 'error' | 'warning';
interface Case {id: string; severity: Sev; href: string; variant?: string; params?: object}

const CASES: Case[] = [
    {id: 'addon-payload', severity: 'error', href: '/addons#reinstall', variant: 'mosquitto', params: {addons: [{id: 'mosquitto', name: 'Mosquitto', enabled: true}]}},
    {id: 'addon-ended', severity: 'warning', href: '/addons', variant: 'hm2mqtt', params: {addons: [{id: 'hm2mqtt', enabled: true}]}},
    {id: 'addon-failed', severity: 'error', href: '/addons', variant: 'mh', params: {addons: [{id: 'mh', enabled: true}]}},
    {id: 'rega', severity: 'error', href: '/addons', variant: 'mosquitto', params: {addons: [{id: 'mosquitto', enabled: false}]}},
    {id: 'arch', severity: 'error', href: '/catalog', variant: 'hm2mqtt', params: {addons: [{id: 'hm2mqtt', enabled: false}]}},
    {id: 'legacy-session', severity: 'warning', href: '/addons', variant: 'redmatic', params: {addons: [{id: 'redmatic', enabled: true}]}},
    {id: 'addon-ownership', severity: 'warning', href: '/services', variant: 'mosquitto', params: {addons: [{id: 'mosquitto', path: '/usr/local/addons/mosquitto/x'}]}},
    {id: 'crash-loop', severity: 'error', href: '/system/services', variant: 'rfd', params: {units: [{unit: 'rfd', restarts: 4, total: 4, since: new Date().toISOString()}]}},
    {id: 'backup-target', severity: 'error', href: '/backup', variant: '/media/usb1', params: {path: '/media/usb1'}},
    {id: 'backup-userfs', severity: 'error', href: '/backup', variant: '/usr/local/backup', params: {path: '/usr/local/backup'}},
    {id: 'backup-unencrypted', severity: 'warning', href: '/backup#encryption', variant: 'plain'},
    {id: 'backup-delivery', severity: 'warning', href: '/backup#targets', variant: 'usb:too-old', params: {name: 'USB', cause: 'too-old'}},
    {id: 'certificate', severity: 'error', href: '/certificate', variant: 'expiring', params: {days: 5}},
    {id: 'storage', severity: 'warning', href: '#storage', variant: 'watch', params: {verdict: 'watch', reasons: []}},
    {id: 'security-key', severity: 'warning', href: '/system/keys#security-key', variant: 'default'},
    {id: 'hmip-local-key', severity: 'error', href: '/system/keys#local-key', variant: '2026-10-03T00:00:00Z'},
    {id: 'hmip-key-declined', severity: 'warning', href: '/system/keys#device-keys', variant: '3014F5AC9400040000000A09@x', params: {sgtin: '3014F5AC9400040000000A09'}},
    {id: 'hmip-adapter', severity: 'error', href: '/system/interfaces#connections', variant: 'adapter-exchange-rejected', params: {adapter: 'X', cause: 'refused'}},
    {id: 'hmip-local-swap', severity: 'error', href: '/system/interfaces#connections', variant: 'x@y', params: {module: 'X', from: 'Y', version: '2.0.0', minimum: '2.8.0'}},
    {id: 'hmip-module-missing', severity: 'error', href: '/system/interfaces#connections', variant: 'X', params: {module: 'X'}},
    {id: 'devices-import', severity: 'warning', href: '/system/interfaces#connections', variant: 'pending', params: {from: 'X', module: 'Y', count: 3}},
    {id: 'hmip-security-counter', severity: 'error', href: '/system/interfaces#connections', variant: 'behind'},
    {id: 'rpc-stalled', severity: 'error', href: '/system/interfaces#stalls', variant: 'BidCos-RF', params: {interface: 'BidCos-RF', kind: 'delivery', checked: false, listeners: []}},
    {id: 'radio-load', severity: 'warning', href: '/radio', variant: 'duty_cycle', params: {duty_cycle: 60}},
    {id: 'firewall', severity: 'error', href: '/system/firewall', variant: 'ipv4', params: {families: ['ipv4']}},
    {id: 'hmip-port-open', severity: 'warning', href: '/system/firewall', variant: '2010', params: {port: 2010, addresses: '0.0.0.0'}},
    {id: 'classic-rpc-open', severity: 'warning', href: '/system/remote-access', variant: 'plain'},
    {id: 'app-public', severity: 'warning', href: '/settings#public', variant: 'guest', params: {account: 'guest'}},
    {id: 'radio-firmware', severity: 'warning', href: '/system/updates#radio-firmware', variant: 'x@1', params: {device: 'HmIP-RFUSB', running: '1', newest: '2'}},
    {id: 'radio-module-unusable', severity: 'warning', href: '/system/updates#radio-firmware', variant: '/dev/ttyUSB0', params: {device: 'HmIP-RFUSB', node: '/dev/ttyUSB0'}},
    {id: 'hmip-clock-hold', severity: 'error', href: '/system/network', variant: 'unsynced', params: {sgtin: 'X', reason: 'wrapped', clock_state: 'unsynced'}},
    {id: 'hb-rf-eth', severity: 'warning', href: '/system/lan-devices#hb-rf-eth', variant: '192.0.2.10', params: {address: '192.0.2.10'}},
    {id: 'journal-target', severity: 'warning', href: '/log?settings=journal', variant: 'usb', params: {label: 'STICK'}},
    {id: 'journal-sync', severity: 'warning', href: '/log?settings=journal', variant: 'failed', params: {target: 'usb', reason: 'x'}},
    {id: 'store-target', severity: 'warning', href: '/log?settings=history', variant: 'usb', params: {label: 'STICK'}},
    {id: 'trust-ca', severity: 'error', href: '/system/trust#', variant: 'api.github.com', params: {hosts: ['api.github.com'], issuer: 'X', store: 'occulited'}},
];

// a warning whose button leads to a view, not to a panel: nothing there to carry an edge
const NO_PANEL: Record<string, string> = {
    meta: 'the Control app: the recovered store is the whole app, not a panel of it',
    unclean: 'the log at the moment of the unclean shutdown',
    'occulited-crash-loop': "occulited's log at the time of the loop",
    'addon-update': 'the card has its update button; an available update is no edge (the maintainer, 2026-10-03)',
};

async function only(page: Page, warnings: object[]) {
    await page.route('**/api/system/v1/warnings', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        await route.fulfill({json: {warnings, periods: [1, 7, 90]}});
    });
}

// the token's colour as the browser resolves it
const token = (page: Page, name: string) => page.evaluate((n) => {
    const p = document.createElement('span');
    p.style.color = `var(${n})`;
    document.body.append(p);
    const c = getComputedStyle(p).color;
    p.remove();
    return c;
}, name);

async function edged(page: Page, el: Locator, severity: Sev) {
    await expect(el).toHaveAttribute('data-warn-edge', severity === 'error' ? 'err' : 'warn');
    const want = await token(page, severity === 'error' ? '--hmm-error' : '--hmm-warn');
    const got = await el.evaluate((e) => {
        if (e.tagName === 'TR') return {w: '4px', c: getComputedStyle(e.firstElementChild!).boxShadow};
        const s = getComputedStyle(e);
        return {w: s.borderLeftWidth, c: s.borderLeftColor};
    });
    expect(got.w).toBe('4px');
    expect(got.c).toContain(want);
}

test.describe('the panel a warning leads to carries its edge', () => {
    test.beforeEach(({}, info) => test.skip(info.project.name === 'phone', 'the edge is the same on a phone; light and dark are the two desktop projects'));
    for (const c of CASES) {
        test(`${c.id} → ${c.href}`, async ({page, baseURL}) => {
            await only(page, [{id: c.id, variant: c.variant ?? 'x', severity: c.severity, href: c.href, since: new Date().toISOString(), params: c.params ?? {}}]);
            if (c.id === 'addon-payload') await payloadMissing(page, 'mosquitto');
            // the stub plants a server its store could not verify on this cookie (openccu-lite task 231)
            if (c.id === 'trust-ca') await page.context().addCookies([{name: 'stub-trust', value: `wp-${Math.random().toString(36).slice(2)}`, url: baseURL!}, {name: 'stub-trust-pending', value: '1', url: baseURL!}]);
            await page.goto(c.href.startsWith('#') ? '/' + c.href : c.href);
            const el = page.locator(`[data-warn-for~="${c.id}"][data-warn-edge]`).first();
            await edged(page, el, c.severity);
        });
    }
    test('an available update gives the card no edge', async ({page}) => {
        await only(page, [{id: 'addon-update', variant: 'redmatic', severity: 'warning', href: '/addons', since: new Date().toISOString(), params: {addons: [{id: 'redmatic', name: 'RedMatic', available: '9.4.1'}]}}]);
        await page.goto('/addons');
        await expect(page.locator('#addon-row-redmatic')).toBeVisible();
        await expect(page.locator('#addon-row-redmatic')).not.toHaveAttribute('data-warn-edge', /./);
    });
    test('without the warning, no edge', async ({page}) => {
        await only(page, []);
        await page.goto('/system/keys');
        await expect(page.locator('[data-warn-for~="security-key"]')).toBeVisible();
        await expect(page.locator('[data-warn-edge]')).toHaveCount(0);
    });
    test('every warning id the system raises is in the table or has its reason', () => {
        const known = [...CASES.map((c) => c.id), ...Object.keys(NO_PANEL)];
        expect(new Set(known).size).toBe(known.length);
        // the ids of internal/httpapi/warnings.go and its siblings, as of task 12
        for (const id of ['addon-payload', 'addon-ended', 'addon-failed', 'rega', 'arch', 'legacy-session', 'addon-update', 'addon-ownership', 'crash-loop', 'occulited-crash-loop', 'meta', 'unclean', 'backup-target', 'backup-userfs', 'backup-unencrypted', 'backup-delivery', 'certificate', 'storage', 'security-key', 'hmip-local-key', 'hmip-key-declined', 'hmip-adapter', 'hmip-local-swap', 'hmip-module-missing', 'devices-import', 'hmip-security-counter', 'rpc-stalled', 'radio-load', 'firewall', 'hmip-port-open', 'classic-rpc-open', 'app-public', 'radio-firmware', 'radio-module-unusable', 'hmip-clock-hold', 'hb-rf-eth', 'journal-target', 'journal-sync', 'store-target', 'trust-ca']) {
            expect(known, id).toContain(id);
        }
    });
});

// a restore brought an addon back without its program files (task 146)
async function payloadMissing(page: Page, id: string) {
    await page.route('**/api/system/v1/addons', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        for (const a of j.addons ?? []) if (a.id === id) Object.assign(a, {payload_missing: true, payload_missing_dirs: ['bin']});
        await route.fulfill({response, json: j});
    });
}

test('an addon without its program files says so on its card, and the reinstall panel is red', async ({page}) => {
    await only(page, []);
    await payloadMissing(page, 'mosquitto');
    await page.goto('/addons');
    const card = page.locator('#addon-row-mosquitto');
    await expect(card.locator('[data-addon-reinstall]')).toHaveText('Needs reinstalling');
    await expect(card.locator('[data-addon-state]')).toHaveCount(0);
    await expect(card.locator('[data-addon-reinstall-link]')).toHaveAttribute('href', '#reinstall');
    // red even before the Status page's list was read: the panel is there only while addons need it
    await edged(page, page.locator('#reinstall'), 'error');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(card.locator('[data-addon-reinstall]')).toHaveText('Muss neu installiert werden');
});
