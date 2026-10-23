import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited B-108 (the maintainer, 2026-10-23, on production): a click on a locked lock's disc showed "open" for a
// moment, then "Öffnet …" with the spinner. A click that starts an action shows the action at once and never the target
// state before the device reports it reached - whether the device's activity report comes before the write's answer,
// after it, or after an echo of the target state. The tile's state line is recorded on every DOM change.

const HMIP_BLIND = 'HmIP-RF.000B10:4';
const BIDCOS_BLIND = 'BidCos-RF.LEQ0000001:1';
const DLD = 'HmIP-RF.000B20:1';
const KEYMATIC = 'BidCos-RF.KEQ9999999:1';

async function kitchen(page: Page, baseURL: string, cookies: Record<string, string> = {}) {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL}, ...Object.entries(cookies).map(([name, value]) => ({name, value, url: baseURL}))]);
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

/** records every text the selector's element shows from now on, and whether its tile is lit */
async function record(page: Page, ref: string, selector = '[data-app-state]') {
    await page.evaluate(({ref, selector}) => {
        const w = window as unknown as {seen: Record<string, string[]>};
        w.seen ??= {};
        const seen: string[] = (w.seen[ref + selector] = []);
        const tile = document.querySelector(`[data-app-tile="${ref}"]`) ?? document;
        const note = () => {
            const el = (selector.startsWith('[data-sheet') ? document : tile).querySelector(selector);
            const text = `${el?.textContent ?? ''}${tile instanceof Element ? `|${tile.getAttribute('data-app-on')}` : ''}`;
            if (seen[seen.length - 1] !== text) seen.push(text);
        };
        note();
        new MutationObserver(note).observe(document.body, {subtree: true, childList: true, characterData: true, attributes: true});
    }, {ref, selector});
    return async () => page.evaluate((k) => (window as unknown as {seen: Record<string, string[]>}).seen[k], ref + selector);
}

for (const [order, cookies] of [['the report before the answer', {}], ['the report after the answer', {'stub-act-order': 'after'}], ['an echo of the target first', {'stub-echo': '1'}], ['an echo after the answer', {'stub-act-order': 'after', 'stub-echo': '1'}]] as const) {
    test(`a lock shows the action at once, never the target before it is reached (${order})`, async ({page, baseURL}) => {
        await kitchen(page, baseURL!, cookies);
        for (const ref of [KEYMATIC, DLD]) {
            const tile = page.locator(`[data-app-tile="${ref}"]`);
            await expect(tile.locator('[data-app-state]')).toHaveText('Locked');
            const seen = await record(page, ref);
            await tile.locator('[data-app-act]').click();
            await expect(tile.locator('[data-app-spin]')).toBeVisible();
            await expect(tile.locator('[data-app-state]')).toHaveText('Unlocked', {timeout: 5000});
            await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
            // every frame: locked, then the action, then the new state - nothing in between
            expect(await seen()).toEqual(['Locked|0', 'Opening …|0', 'Unlocked|1']);
        }
    });

    test(`a blind shows the action at once, never the target level (${order})`, async ({page, baseURL}) => {
        await kitchen(page, baseURL!, cookies);
        for (const [ref, text, moving] of [[HMIP_BLIND, '30 % open|1', 'Moving up · 30 %|1'], [BIDCOS_BLIND, '80 % open|1', 'Moving down · 80 %|1']] as const) {
            const tile = page.locator(`[data-app-tile="${ref}"]`);
            await expect(tile.locator('[data-app-state]')).toHaveText(text.split('|')[0]!);
            const seen = await record(page, ref);
            await tile.locator('[data-app-act]').click();
            await expect(tile.locator('[data-app-spin]')).toBeVisible();
            await expect(tile.locator('[data-app-state]')).toHaveText(moving.split('|')[0]!);
            await expect(tile.locator('[data-app-act]')).toHaveAttribute('data-app-disc-action', 'stop');
            // STOP ends it and shows the real position
            await tile.locator('[data-app-act]').click();
            await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
            await expect(tile.locator('[data-app-state]')).toHaveText(text.split('|')[0]!);
            expect(await seen()).toEqual([text, moving, text]);
        }
    });
}

test('the sheet: its buttons show the action at once too', async ({page, baseURL}) => {
    await kitchen(page, baseURL!, {'stub-act-order': 'after', 'stub-echo': '1'});
    await expect(page.locator(`[data-app-tile="${KEYMATIC}"] [data-app-state]`)).toHaveText('Locked');
    await page.locator(`[data-app-tile="${KEYMATIC}"]`).click();
    await expect(page.locator('[data-sheet="lock"]')).toBeVisible();
    const seen = await record(page, KEYMATIC, '[data-sheet-state]');
    await page.locator('[data-sheet="lock"] button', {hasText: 'Unlock'}).click();
    await expect(page.locator('[data-sheet-state]')).toHaveText('Opening …');
    await expect(page.locator('[data-sheet-state]')).toHaveText('Unlocked', {timeout: 5000});
    expect((await seen()).map((s) => s.split('|')[0])).toEqual(['Locked', 'Opening …', 'Unlocked']);
    await page.keyboard.press('Escape');
    // a blind's Up in the sheet: the action, not "100 % open"
    await expect(page.locator('[data-sheet="lock"]')).toHaveCount(0);
    await page.locator(`[data-app-tile="${HMIP_BLIND}"]`).click();
    await expect(page.locator('[data-sheet="blind"]')).toBeVisible();
    const blind = await record(page, HMIP_BLIND, '[data-sheet-state]');
    await page.locator('[data-sheet="blind"] button', {hasText: 'Up'}).click();
    await expect(page.locator('[data-sheet-state]')).toHaveText('Moving up · 30 %');
    await page.locator('[data-sheet="blind"] button', {hasText: 'Stop'}).click();
    await expect(page.locator('[data-sheet-state]')).toHaveText('30 % open');
    expect((await blind()).map((s) => s.split('|')[0])).toEqual(['30 % open', 'Moving up · 30 %', '30 % open']);
});

test('a write error clears the action at once and says why', async ({page, baseURL}) => {
    await kitchen(page, baseURL!);
    await page.route('**/api/rpc/v1/json/BidCos-RF', async (route) => {
        if ((route.request().postData() ?? '').includes('"setValue"')) return route.fulfill({json: [{jsonrpc: '2.0', error: {code: -5, message: 'Invalid parameter or value'}, id: 1}]});
        return route.fallback();
    });
    const tile = page.locator(`[data-app-tile="${KEYMATIC}"]`);
    await expect(tile.locator('[data-app-state]')).toHaveText('Locked');
    await tile.locator('[data-app-act]').click();
    await expect(tile.locator('[data-app-state]')).toContainText('Invalid parameter');
    await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
});

test('no report from the device: after the timeout the tile shows its real state again', async ({page, baseURL}) => {
    await page.clock.install();
    await kitchen(page, baseURL!);
    // the write is answered, and the device never reports (no event)
    await page.route('**/api/rpc/v1/json/BidCos-RF', async (route) => {
        if ((route.request().postData() ?? '').includes('"setValue"')) return route.fulfill({json: [{jsonrpc: '2.0', result: '', id: 1}]});
        return route.fallback();
    });
    const tile = page.locator(`[data-app-tile="${KEYMATIC}"]`);
    await expect(tile.locator('[data-app-state]')).toHaveText('Locked');
    await tile.locator('[data-app-act]').click();
    await expect(tile.locator('[data-app-state]')).toHaveText('Opening …');
    await page.clock.runFor(14_000);
    await expect(tile.locator('[data-app-state]')).toHaveText('Opening …');
    await page.clock.runFor(1_500);
    await expect(tile.locator('[data-app-state]')).toHaveText('Locked');
    await expect(tile.locator('[data-app-spin]')).toHaveCount(0);
});
