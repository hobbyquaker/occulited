import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited B-80 (the maintainer, 2026-10-16: no progress for a transfer to several devices of one
// type, a time left in hours and minutes, the progress as the pill's fill, more detail when the row
// is open, no resizing on a state change) and task 72 (the duty-cycle pause): two HmIP-BSM devices
// that share hmipserver's multicast handler.

type Firmware = {devices: Record<string, unknown>[]; deployed: Record<string, unknown>[]};
type Tr = Record<string, unknown> | undefined;

const row = (page: Page) => page.locator('table.fw-types tr.fw-row[data-type="HmIP-BSM"]');
const devicesRow = (page: Page) => row(page).locator('xpath=following-sibling::tr[1]');
// B-81: each device is a row of the table
const deviceRows = (page: Page) => page.locator('tr[data-device-row][data-type-of="HmIP-BSM"]');

/** GET /firmware with two BSMs in the given update states and transfers. */
async function bsm(page: Page, a: {state: string; tr: Tr}, b: {state: string; tr: Tr} = a) {
    await page.unrouteAll({behavior: 'wait'});
    await page.route('**/api/system/v1/firmware', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const st = (await response.json()) as Firmware;
        const one = (address: string, name: string, x: {state: string; tr: Tr}) => ({interface: 'HmIP-RF', address, type: 'HmIP-BSM', firmware: '1.18.14', available_firmware: '1.18.24', latest: '1.18.24', update_available: true, name, update_state: x.state, transfer: x.tr});
        st.devices.push(one('00170000000C01', 'Light hallway', a), one('00170000000C02', 'Light stairs', b));
        st.deployed.push({type_code: '4231', name: 'HmIP-BSM', version: '1.18.24', files: ['HmIP-BSM_update_V1_18_24.efw', 'info']});
        await route.fulfill({response, json: st});
    });
}

const since = new Date(Date.now() - 5 * 3600 * 1000).toISOString();
const at = new Date(Date.now() - 20 * 60 * 1000).toISOString();
const running = (percent: number, more: Record<string, unknown> = {}): Tr => ({state: 'running', percent, bytes: Math.round(101588 * percent / 100), size: 101588, since, updated: at, progress_at: at, eta_seconds: 19200, ...more});

test('a multicast transfer: the fill and the number in the pill, the bytes and the time left when open', async ({page}) => {
    await bsm(page, {state: 'DELIVER_FIRMWARE_IMAGE', tr: {...running(44), bytes: 45120}}, {state: 'DELIVER_FIRMWARE_IMAGE', tr: {...running(40), bytes: 40640, eta_seconds: null}});
    await page.goto('/system/updates');
    const badge = row(page).locator('[data-badge]');
    await expect(badge).toHaveText('Transferring · 44 %');
    const fill = badge.locator('[data-progress]');
    await expect(fill).toHaveAttribute('data-progress', '44');
    expect(await fill.evaluate((el) => (el as HTMLElement).style.width)).toBe('44%');
    // the fill lies inside the pill, from its left edge
    const fb = (await fill.boundingBox())!;
    const bb = (await badge.boundingBox())!;
    expect(Math.abs(fb.x - bb.x)).toBeLessThanOrEqual(1.5);
    expect(fb.height).toBeGreaterThanOrEqual(bb.height - 3);
    await expect(badge).toHaveAttribute('title', /about 5 h 20 min left/);
    await expect(badge.locator('[data-pause-mark]')).toHaveCount(0);
    // open: the type's time left, and each device's own numbers
    await row(page).locator('[data-expand]').click();
    await expect(devicesRow(page).locator('[data-details] [data-eta]')).toHaveText('about 5 h 20 min left');
    const devs = deviceRows(page);
    await expect(devs.filter({hasText: '00170000000C01'}).locator('[data-bytes]')).toHaveText('45,120 / 101,588 bytes (44 %)');
    await expect(devs.filter({hasText: '00170000000C01'}).locator('[data-device-progress] [data-eta]')).toHaveText('about 5 h 20 min left');
    await expect(devs.filter({hasText: '00170000000C01'}).locator('[data-device-progress] [data-last-progress]')).toHaveText('Last progress: 20 min ago');
    await expect(devs.filter({hasText: '00170000000C02'}).locator('[data-bytes]')).toHaveText('40,640 / 101,588 bytes (40 %)');
    await expect(devs.filter({hasText: '00170000000C02'}).locator('[data-device-progress] [data-eta]')).toHaveText('time left unknown');
    expect(await fitsWindow(page)).toBe(true);
    // German: the numbers in German format
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(row(page).locator('[data-badge]')).toHaveText('Übertragung · 44 %');
    await row(page).locator('[data-expand]').click();
    await expect(deviceRows(page).filter({hasText: '00170000000C01'}).locator('[data-bytes]')).toHaveText('45.120 / 101.588 Bytes (44 %)');
    await expect(devicesRow(page).locator('[data-details] [data-eta]')).toHaveText('noch ca. 5 Std. 20 Min.');
});

test('the multicast handler\'s blocks when a device has no bytes of its own yet', async ({page}) => {
    await bsm(page, {state: 'DELIVER_FIRMWARE_IMAGE', tr: {state: 'running', percent: 24, bytes: 12, size: 50, unit: 'blocks', since, updated: at, progress_at: at, eta_seconds: null}});
    await page.goto('/system/updates');
    await expect(row(page).locator('[data-badge]')).toHaveText('Transferring · 24 %');
    await row(page).locator('[data-expand]').click();
    await expect(deviceRows(page).first().locator('[data-bytes]')).toHaveText('12 / 50 blocks (24 %)');
});

test('paused at the duty-cycle threshold: the pause mark in the pill, since when in the tooltip and the open row', async ({page}) => {
    const paused = {state: 'DELIVER_FIRMWARE_IMAGE', tr: {...running(64), state: 'paused', reason: 'duty-cycle', paused_since: new Date(Date.now() - 10 * 60 * 1000).toISOString(), eta_seconds: null}};
    await bsm(page, paused);
    await page.goto('/system/updates');
    const badge = row(page).locator('[data-badge]');
    await expect(badge).toHaveText('Transferring · 64 %');
    await expect(badge.locator('[data-pause-mark]')).toBeVisible();
    await expect(badge.locator('[data-pause-mark]')).toHaveAttribute('aria-label', 'paused (duty cycle)');
    await expect(badge).toHaveAttribute('title', /Paused \(duty cycle\) since \d\d:\d\d; hmipserver continues by itself\./);
    // the pause mark sits at the pill's right end, inside it
    const mb = (await badge.locator('[data-pause-mark]').boundingBox())!;
    const bb = (await badge.boundingBox())!;
    expect(mb.x + mb.width).toBeLessThanOrEqual(bb.x + bb.width);
    expect(bb.x + bb.width - (mb.x + mb.width)).toBeLessThanOrEqual(10);
    await row(page).locator('[data-expand]').click();
    await expect(devicesRow(page).locator('[data-paused-text]')).toContainText('Paused (duty cycle) since');
    await expect(devicesRow(page).locator('[data-details] [data-eta]')).toHaveText('time left unknown');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(row(page).locator('[data-badge] [data-pause-mark]')).toHaveAttribute('aria-label', 'pausiert (Duty Cycle)');
    await expect(row(page).locator('[data-badge]')).toHaveAttribute('title', /Pausiert \(Duty Cycle\) seit \d\d:\d\d; hmipserver setzt die Übertragung von selbst fort\./);
});

// the maintainer: "resizing elements on changing states are ugly" - the pill keeps its box in every
// live state, a one- to three-digit percent, paused or not, in both languages; nothing is cut
const STATES: [string, {state: string; tr: Tr}][] = [
    ['queued', {state: 'NEW_FIRMWARE_AVAILABLE', tr: undefined}],
    ['no percent yet', {state: 'DELIVER_FIRMWARE_IMAGE', tr: {state: 'running', percent: -1, since, updated: at}}],
    ['7 %', {state: 'DELIVER_FIRMWARE_IMAGE', tr: running(7)}],
    ['64 %', {state: 'DELIVER_FIRMWARE_IMAGE', tr: running(64)}],
    ['paused 64 %', {state: 'DELIVER_FIRMWARE_IMAGE', tr: {...running(64), state: 'paused', reason: 'duty-cycle', paused_since: at}}],
    ['interrupted 100 %', {state: 'DELIVER_FIRMWARE_IMAGE', tr: {...running(100), state: 'interrupted', reason: 'unreachable'}}],
    ['done 100 %', {state: 'DELIVER_FIRMWARE_IMAGE', tr: {...running(100), state: 'done'}}],
    ['ready', {state: 'READY_FOR_UPDATE', tr: undefined}],
];
for (const lang of ['en', 'de'] as const) {
    test(`the pill keeps its box in every state (${lang})`, async ({page}) => {
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        const boxes: string[] = [];
        const devBoxes: string[] = [];
        for (const [, x] of STATES) {
            await bsm(page, x);
            await page.goto('/system/updates');
            const badge = row(page).locator('[data-badge]');
            await expect(badge).toBeVisible();
            const b = (await badge.boundingBox())!;
            // the row moves in the list with its status (ready sorts later): its x and size are what must stay
            boxes.push(`${Math.round(b.x)},${Math.round(b.width)},${Math.round(b.height)}`);
            // a percent is never cut (a long label without one may be, its whole text in the tooltip)
            if ((await badge.textContent())?.includes('%')) expect(await badge.locator('.fw-badge-text').evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
            await row(page).locator('[data-expand]').click();
            const d = (await deviceRows(page).locator('[data-device-state]').first().boundingBox())!;
            devBoxes.push(`${Math.round(d.width)},${Math.round(d.height)}`);
        }
        expect(new Set(boxes).size, boxes.join(' | ')).toBe(1);
        expect(new Set(devBoxes).size, devBoxes.join(' | ')).toBe(1);
    });
}
