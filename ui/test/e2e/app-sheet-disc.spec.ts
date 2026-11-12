import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 94 (the maintainer, 2026-11-11): the opened tile's head takes task 92's soft disc - the
// tile's own style, app.css's .ol-disc - with the tint when on or alarming, for one look across the
// tile and the sheet. Checked side by side: the tile's disc and the sheet's disc carry the same
// colours (a dimmer on and off, a contact open, a smoke detector in alarm, a thermostat), and the
// sheet's disc keeps its 56 px box across the states.

async function stubApp(page: Page, baseURL: string) {
    await page.context().addCookies([{name: 'stub-app', value: `disc-${Math.random().toString(36).slice(2)}`, url: baseURL}]);
}

/** a value on the stub, as a device would report it (the stub keeps it per stub-app) */
async function put(page: Page, address: string, key: string, value: unknown) {
    const r = await page.request.post('/api/rpc/v1/json/HmIP-RF', {headers: {'X-Occulite-Request': '1'}, data: [{jsonrpc: '2.0', id: 1, method: 'setValue', params: [address, key, value]}]});
    expect(r.ok()).toBe(true);
}

const look = (el: import('@playwright/test').Locator) =>
    el.evaluate((e) => {
        const s = getComputedStyle(e);
        return {color: s.color, background: s.backgroundColor, border: s.borderTopWidth, radius: s.borderTopLeftRadius};
    });

/** opens the tile's sheet and compares the two discs; returns the sheet's disc */
async function sameDisc(page: Page, ref: string, tone: '' | 'on' | 'alarm') {
    const tile = page.locator(`[data-app-tile="${ref}"]`);
    const tileDisc = tile.locator('.ol-disc');
    await expect(tileDisc).toHaveCount(1);
    if (tone) await expect(tileDisc).toHaveClass(new RegExp(`\\b${tone}\\b`));
    // the values come in after the first paint, and a disc that becomes a button is a new element: read it once it stands
    await expect.poll(async () => (await look(tileDisc)).radius).toBe('50%');
    const tileLook = await look(tileDisc);
    await tile.locator('[data-app-state], .app-tile-name').first().click();
    const sheetDisc = page.locator('.app-sheet .sh-head .ol-disc');
    await expect(sheetDisc).toBeVisible();
    await expect(sheetDisc).toHaveAttribute('data-icon', (await tileDisc.getAttribute('data-icon'))!);
    if (tone) await expect(sheetDisc).toHaveClass(new RegExp(`\\b${tone}\\b`));
    else await expect(sheetDisc).not.toHaveClass(/\b(on|alarm)\b/);
    const sheetLook = await look(sheetDisc);
    // the same disc: no ring, the same fill and icon colour
    expect(sheetLook).toEqual({...tileLook, border: '0px'});
    expect(tileLook.border).toBe('0px');
    // the sheet grows out of the tile: measured once its animations have ended
    await page.locator('.app-sheet').evaluate((el) => Promise.all(el.getAnimations({subtree: true}).map((a) => a.finished.catch(() => undefined))));
    await expect.poll(async () => Math.round((await sheetDisc.boundingBox())!.width)).toBe(56);
    const box = (await sheetDisc.boundingBox())!;
    expect(Math.round(box.height)).toBe(56);
    const ico = (await sheetDisc.locator('svg').boundingBox())!;
    expect(ico.width / box.width).toBeGreaterThan(0.8);
    expect(ico.width / box.width).toBeLessThan(0.88);
    return sheetDisc;
}

async function closeSheet(page: Page) {
    await page.keyboard.press('Escape');
    await expect(page.locator('.app-sheet')).toHaveCount(0);
}

test('a dimmer: the sheet\'s disc is the tile\'s, tinted on, neutral off, in the same box', async ({page, baseURL}) => {
    await stubApp(page, baseURL!);
    await page.goto('/app/e/rooms/og/bad');
    const ref = 'HmIP-RF.000DD8:3';
    const disc = await sameDisc(page, ref, 'on');
    // the disc's own box (the phone's bottom sheet may settle by a pixel around it)
    const round = async () => { const b = (await disc.boundingBox())!; return [Math.round(b.width), Math.round(b.height)]; };
    const before = await round();
    // the sheet's disc switches it off: neutral now, the same box
    await disc.click();
    await expect(disc).not.toHaveClass(/\bon\b/);
    expect(await round()).toEqual(before);
    await closeSheet(page);
    await sameDisc(page, ref, '');
});

test('a thermostat: the disc in the sheet as on the tile', async ({page, baseURL}) => {
    await stubApp(page, baseURL!);
    await page.goto('/app/e/rooms/og/bad');
    await sameDisc(page, 'HmIP-RF.000ABC:1', '');
});

test('a smoke detector in alarm: the error tint on the tile and in the sheet', async ({page, baseURL}) => {
    await stubApp(page, baseURL!);
    await put(page, '000F01:1', 'SMOKE_DETECTOR_ALARM_STATUS', 1);
    await page.goto('/app/e/rooms/og');
    await sameDisc(page, 'HmIP-RF.000F01:1', 'alarm');
});

test('a contact, open: the on tint on the tile and in the sheet', async ({page, baseURL}) => {
    await stubApp(page, baseURL!);
    await put(page, '000C02:1', 'STATE', 1);
    // the stub's door contact is unassigned: put into the bathroom, as app.spec does
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['HmIP-RF.000C02:1'] = {name: 'Tür Bad', enums: ['rooms/og/bad'], meta: {}};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/og/bad');
    await sameDisc(page, 'HmIP-RF.000C02:1', 'on');
});
