import {type Locator, type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 113 (the maintainer, 2026-10-23): a blind's and a lock's tile show the action that runs - an arrow
// (a lock: its shackle) at the disc's bottom right and a spinner ring around it, the state line in words; the end
// restores the tile; reduced motion stops the ring; nothing moves. The lock sheet's third button reads "Öffnen".
// occulited task 114: the blind's disc stops a moving blind, opens one below 50 % and closes one from 50 %.
// The stub's blinds move until they are stopped, its locks run for 1.5 s (test/stub/server.mjs).

const HMIP_BLIND = 'HmIP-RF.000B10:4';
const BIDCOS_BLIND = 'BidCos-RF.LEQ0000001:1';
const DLD = 'HmIP-RF.000B20:1';
const KEYMATIC = 'BidCos-RF.KEQ9999999:1';

async function kitchen(page: Page, baseURL: string) {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL}]);
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects[HMIP_BLIND] = {name: 'Rollladen Küche', enums: ['rooms/kueche'], meta: {}};
        json.objects[BIDCOS_BLIND] = {name: 'Jalousie Küche', enums: ['rooms/kueche'], meta: {}};
        json.objects[DLD] = {name: 'Haustür', enums: ['rooms/kueche'], meta: {}};
        json.objects[KEYMATIC] = {name: 'Kellertür', enums: ['rooms/kueche'], meta: {}};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/kueche');
}

function setValue(page: Page, address: string, key: string) {
    return page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('"setValue"') && (r.postData() ?? '').includes(address) && (r.postData() ?? '').includes(`"${key}"`));
}

async function boxes(tile: Locator) {
    return {tile: await tile.boundingBox(), disc: await tile.locator('.app-circle').boundingBox(), name: await tile.locator('.app-tile-name').boundingBox(), state: await tile.locator('[data-app-state]').boundingBox()};
}

test('an HmIP blind: the disc opens it, shows it moving up, and stops it', async ({page, baseURL}) => {
    await kitchen(page, baseURL!);
    const tile = page.locator(`[data-app-tile="${HMIP_BLIND}"]`);
    const disc = tile.locator('[data-app-act]');
    await expect(tile.locator('[data-app-state]')).toHaveText('30 % open');
    await expect(disc).toHaveAttribute('data-app-disc-action', 'open');
    await expect(disc).toHaveAttribute('aria-label', 'Rollladen Küche: Open');
    await expect(disc).toHaveAttribute('title', 'Open');
    await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
    await expect(tile.locator('[data-app-motion]')).toHaveCount(0);
    const still = await boxes(tile);
    const [up] = await Promise.all([setValue(page, '000B10:4', 'LEVEL'), disc.click()]);
    expect(up.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000B10:4', 'LEVEL', 1]});
    // it moves: the arrow up, the spinner, the words - and the level it stands at, not the one it was sent
    await expect(tile.locator('[data-app-motion]')).toHaveAttribute('data-app-motion', 'up');
    await expect(tile.locator('[data-app-spin]')).toBeVisible();
    await expect(tile.locator('[data-app-state]')).toHaveText('Moving up · 30 %');
    await expect(disc).toHaveAttribute('data-app-disc-action', 'stop');
    await expect(disc).toHaveAttribute('aria-label', 'Rollladen Küche: Stop');
    // the spinner turns, around the disc and in the "on" colour; the badge overlaps the disc's bottom right
    const spin = tile.locator('[data-app-spin]');
    expect(await spin.evaluate((e) => getComputedStyle(e).animationName)).toMatch(/app-spin$/);
    const d = (await tile.locator('.app-circle').boundingBox())!;
    const s = (await spin.boundingBox())!;
    expect(s.x).toBeLessThan(d.x);
    expect(s.x + s.width).toBeGreaterThan(d.x + d.width);
    const b = (await tile.locator('[data-app-motion]').boundingBox())!;
    expect(b.x + b.width).toBeLessThanOrEqual(d.x + d.width + 4);
    expect(b.y + b.height).toBeLessThanOrEqual(d.y + d.height + 4);
    // nothing moved
    expect(await boxes(tile)).toEqual(still);
    // a click while it moves stops it, and the tile is the still one again
    const [stop] = await Promise.all([setValue(page, '000B10:4', 'STOP'), disc.click()]);
    expect(stop.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000B10:4', 'STOP', true]});
    await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
    await expect(tile.locator('[data-app-motion]')).toHaveCount(0);
    await expect(tile.locator('[data-app-state]')).toHaveText('30 % open');
    await expect(disc).toHaveAttribute('data-app-disc-action', 'open');
    expect(await boxes(tile)).toEqual(still);
});

test('a BidCos blind in German: from 80 % the disc closes it, it moves down, and stops', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await kitchen(page, baseURL!);
    const tile = page.locator(`[data-app-tile="${BIDCOS_BLIND}"]`);
    const disc = tile.locator('[data-app-act]');
    await expect(tile.locator('[data-app-state]')).toHaveText('80 % offen');
    await expect(disc).toHaveAttribute('aria-label', 'Jalousie Küche: Schließen');
    await expect(disc).toHaveAttribute('title', 'Schließen');
    const [down] = await Promise.all([setValue(page, 'LEQ0000001:1', 'LEVEL'), disc.click()]);
    expect(down.postDataJSON()[0]).toMatchObject({params: ['LEQ0000001:1', 'LEVEL', 0]});
    await expect(tile.locator('[data-app-motion]')).toHaveAttribute('data-app-motion', 'down');
    await expect(tile.locator('[data-app-state]')).toHaveText('Fährt runter · 80 %');
    await expect(disc).toHaveAttribute('aria-label', 'Jalousie Küche: Stopp');
    const [stop] = await Promise.all([setValue(page, 'LEQ0000001:1', 'STOP'), disc.click()]);
    expect(stop.postDataJSON()[0]).toMatchObject({params: ['LEQ0000001:1', 'STOP', true]});
    await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
    await expect(tile.locator('[data-app-state]')).toHaveText('80 % offen');
    // the sheet's controls are the ones they were; its Up reads Auf (it read "Aktiv ab", the uptime's word)
    await tile.click();
    await expect(page.locator('[data-sheet="blind"] button')).toHaveText(['Auf', 'Stoppen', 'Ab']);
});

test('the locks: unlocking shows the open shackle and "Opening …", then the new state', async ({page, baseURL}) => {
    await kitchen(page, baseURL!);
    for (const [ref, address, key] of [[DLD, '000B20:1', 'LOCK_TARGET_LEVEL'], [KEYMATIC, 'KEQ9999999:1', 'STATE']] as const) {
        const tile = page.locator(`[data-app-tile="${ref}"]`);
        await expect(tile.locator('[data-app-state]')).toHaveText('Locked');
        const still = await boxes(tile);
        await Promise.all([setValue(page, address, key), tile.locator('[data-app-act]').click()]);
        await expect(tile.locator('[data-app-motion]')).toHaveAttribute('data-app-motion', 'up');
        await expect(tile.locator('[data-app-spin]')).toBeVisible();
        await expect(tile.locator('[data-app-state]')).toHaveText('Opening …');
        expect(await boxes(tile)).toEqual(still);
        await expect(tile.locator('[data-app-state]')).toHaveText('Unlocked', {timeout: 5000});
        await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
        await expect(tile.locator('[data-app-motion]')).toHaveCount(0);
        expect(await boxes(tile)).toEqual(still);
        // and back: locking
        await Promise.all([setValue(page, address, key), tile.locator('[data-app-act]').click()]);
        await expect(tile.locator('[data-app-state]')).toHaveText('Locking …');
        await expect(tile.locator('[data-app-motion]')).toHaveAttribute('data-app-motion', 'down');
        await expect(tile.locator('[data-app-state]')).toHaveText('Locked', {timeout: 5000});
    }
});

test('the lock sheet: Öffnen is the third button, and the sheet says the running action', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await kitchen(page, baseURL!);
    for (const ref of [DLD, KEYMATIC]) {
        const tile = page.locator(`[data-app-tile="${ref}"]`);
        await expect(tile.locator('[data-app-state]')).toHaveText('Verriegelt');
        await tile.click();
        await expect(page.locator('[data-sheet="lock"] button')).toHaveText(['Verriegeln', 'Entriegeln', 'Öffnen']);
        await page.locator('[data-sheet="lock"] button', {hasText: 'Öffnen'}).click();
        await expect(page.locator('[data-sheet-state]')).toHaveText('Öffnet …');
        await expect(page.locator('[data-sheet-state]')).toHaveText('Entriegelt', {timeout: 5000});
        await page.keyboard.press('Escape');
        await expect(page.locator('[data-sheet="lock"]')).toHaveCount(0);
    }
});

test('reduced motion: the ring stands still', async ({page, baseURL}) => {
    await page.emulateMedia({reducedMotion: 'reduce'});
    await kitchen(page, baseURL!);
    const tile = page.locator(`[data-app-tile="${HMIP_BLIND}"]`);
    await Promise.all([setValue(page, '000B10:4', 'LEVEL'), tile.locator('[data-app-act]').click()]);
    const spin = tile.locator('[data-app-spin]');
    await expect(spin).toBeVisible();
    await expect(spin).toHaveCSS('animation-name', 'none');
    await expect(tile.locator('[data-app-motion]')).toHaveAttribute('data-app-motion', 'up');
});
