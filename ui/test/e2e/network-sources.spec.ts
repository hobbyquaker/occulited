import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 138 (the maintainer, 2026-11-10): DNS and NTP each with a select, "taken from DHCP"
// or "manual". From DHCP the servers field stays in its row, read-only, so nothing below moves;
// manual with nothing entered is refused on the page. DNS from DHCP is no override (dns: []) and
// follows IPv4: static has no lease, so the select shows manual and is disabled. NTP manual sends
// ntp_source "manual" with the servers, from DHCP ntp_source "dhcp" alone.

const time = (over: Record<string, unknown> = {}) => ({tz: 'CEST-2', zone: 'Europe/Berlin', ntp_servers: ['0.de.pool.ntp.org', '1.de.pool.ntp.org'], ntp_source: 'dhcp', ntp_servers_active: ['192.0.2.1', '0.de.pool.ntp.org'], has_ntp: true, now: new Date().toISOString(), ...over});

// the stub's network with IPv4 by DHCP and the given override
async function dhcpNetwork(page: Page, dns: string[], posts: unknown[] = []) {
    await page.route('**/api/system/v1/network', async (route) => {
        if (route.request().method() === 'POST') {
            posts.push(route.request().postDataJSON());
            return route.fulfill({json: {applied: false, pending: {token: 't', deadline: new Date(Date.now() + 90_000).toISOString(), settings: {hostname: 'openccu', mode: 'dhcp', dns: []}, previous: {hostname: 'openccu', mode: 'dhcp', dns}}}});
        }
        const res = await route.fetch();
        const n = await res.json();
        const conf = {hostname: 'openccu', mode: 'dhcp', dns};
        await route.fulfill({json: {...n, settings: conf, current: {...conf, dns: dns.length ? dns : ['192.0.2.53']}, network: {...n.network, mode: 'dhcp'}}});
    });
}

async function answerTime(page: Page, first: Record<string, unknown>, puts: Record<string, unknown>[] = []) {
    await page.route('**/api/system/v1/time', async (route) => {
        if (route.request().method() === 'PUT') {
            const b = route.request().postDataJSON();
            puts.push(b);
            const manual = b.ntp_source === 'manual';
            return route.fulfill({json: time({ntp_source: b.ntp_source, ntp_servers: manual ? b.ntp_servers : ['0.de.pool.ntp.org'], ntp_servers_active: manual ? b.ntp_servers : ['192.0.2.1', '0.de.pool.ntp.org']})});
        }
        await route.fulfill({json: time(first)});
    });
}

const height = async (page: Page, sel: string) => (await page.locator(sel).boundingBox())!.height;
// the hints reserve two lines; on a phone the longer one may wrap to a third
const keepsHeight = (project: string) => project !== 'phone';

test('DNS from DHCP: no override, the field read-only in its row; manual needs a server', async ({page}, info) => {
    const posts: unknown[] = [];
    await dhcpNetwork(page, [], posts);
    await page.goto('/system/network');
    const panel = page.locator('[data-panel="name-resolution"]');
    const select = panel.locator('[data-dns-source]');
    const field = panel.locator('#net-dns-override');
    await expect(select).toHaveValue('dhcp');
    await expect(select).toBeEnabled();
    await expect(field).toBeDisabled();
    await expect(field).toHaveValue('');
    await expect(panel.locator('[data-dns-hint]')).toHaveText('The servers the DHCP lease names are used.');
    const before = await height(page, '[data-panel="name-resolution"]');

    await select.selectOption('manual');
    await expect(field).toBeEnabled();
    // nothing entered: nothing to apply
    await expect(panel.locator('[data-action="save-dns"]')).toBeDisabled();
    if (keepsHeight(info.project.name)) expect(await height(page, '[data-panel="name-resolution"]')).toBe(before);
    await field.fill('192.0.2.53');
    await panel.locator('[data-action="save-dns"]').click();
    await expect.poll(() => posts.length).toBe(1);
    expect((posts[0] as {dns: string[]}).dns).toEqual(['192.0.2.53']);
});

test('DNS manual back to DHCP sends no override', async ({page}) => {
    const posts: unknown[] = [];
    await dhcpNetwork(page, ['192.0.2.53'], posts);
    await page.goto('/system/network');
    const panel = page.locator('[data-panel="name-resolution"]');
    await expect(panel.locator('[data-dns-source]')).toHaveValue('manual');
    await expect(panel.locator('#net-dns-override')).toHaveValue('192.0.2.53');
    await panel.locator('[data-dns-source]').selectOption('dhcp');
    await expect(panel.locator('#net-dns-override')).toBeDisabled();
    await panel.locator('[data-action="save-dns"]').click();
    await expect.poll(() => posts.length).toBe(1);
    expect((posts[0] as {dns: string[]}).dns).toEqual([]);
});

test('DNS with IPv4 static: manual, the select disabled, the field the static servers', async ({page}) => {
    await page.goto('/system/network');
    const panel = page.locator('[data-panel="name-resolution"]');
    await expect(panel.locator('[data-dns-source]')).toHaveValue('manual');
    await expect(panel.locator('[data-dns-source]')).toBeDisabled();
    await expect(panel.locator('#net-dns-override')).toBeEnabled();
    await expect(panel.locator('#net-dns-override')).toHaveValue('192.0.2.1, 1.1.1.1');
});

test('NTP: from DHCP shows the servers in use and the fallback; manual sends only the entered servers', async ({page}, info) => {
    const puts: Record<string, unknown>[] = [];
    await answerTime(page, {}, puts);
    await page.goto('/system/network');
    const panel = page.locator('[data-panel="time"]');
    const select = panel.locator('[data-ntp-source]');
    const field = panel.locator('#net-ntp');
    await expect(select).toHaveValue('dhcp');
    await expect(field).toBeDisabled();
    await expect(panel.locator('[data-ntp-current]')).toHaveText('192.0.2.1 0.de.pool.ntp.org');
    await expect(panel.locator('[data-ntp-hint]')).toHaveText('The servers the DHCP lease names; without any there: 0.de.pool.ntp.org 1.de.pool.ntp.org.');
    const before = await height(page, '[data-panel="time"]');

    await select.selectOption('manual');
    await expect(field).toBeEnabled();
    await expect(panel.locator('[data-action="save-time"]')).toBeDisabled();
    if (keepsHeight(info.project.name)) expect(await height(page, '[data-panel="time"]')).toBe(before);
    await field.fill('192.0.2.123 ptbtime1.ptb.de');
    await panel.locator('[data-action="save-time"]').click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0]).toMatchObject({ntp_source: 'manual', ntp_servers: ['192.0.2.123', 'ptbtime1.ptb.de']});
    await expect(panel.locator('[data-ntp-current]')).toHaveText('192.0.2.123 ptbtime1.ptb.de');

    await select.selectOption('dhcp');
    await panel.locator('[data-action="save-time"]').click();
    await expect.poll(() => puts.length).toBe(2);
    expect(puts[1].ntp_source).toBe('dhcp');
    expect(puts[1]).not.toHaveProperty('ntp_servers');
    await expect(field).toHaveValue('');
});

test('NTP: a system with servers of its own shows manual with them', async ({page}) => {
    await answerTime(page, {ntp_source: 'manual', ntp_servers: ['192.0.2.123'], ntp_servers_active: ['192.0.2.1', '192.0.2.123']});
    await page.goto('/system/network');
    const panel = page.locator('[data-panel="time"]');
    await expect(panel.locator('[data-ntp-source]')).toHaveValue('manual');
    await expect(panel.locator('#net-ntp')).toHaveValue('192.0.2.123');
    await expect(panel.locator('[data-ntp-hint]')).toHaveText('Only these servers are asked, separated by spaces.');
});

test('in German: the maintainer\'s words', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await answerTime(page, {});
    await dhcpNetwork(page, []);
    await page.goto('/system/network');
    await expect(page.locator('[data-ntp-source] option[value="dhcp"]')).toHaveText('von DHCP übernommen');
    await expect(page.locator('[data-ntp-source] option[value="manual"]')).toHaveText('manuell');
    await expect(page.locator('[data-dns-source] option[value="dhcp"]')).toHaveText('von DHCP übernommen');
});
