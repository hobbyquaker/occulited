import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// openccu-lite task 411: the unassigned list hides the virtual channels and keys behind two checkboxes, both off by
// default. An HmIP output: its transmitter (000D40:1) and the 2nd and 3rd receiver are out, the 1st stays; a device
// whose receivers have no transmitter in front (000D41) does not follow the pattern and stays whole.
const FIRST = 'HmIP-RF.000D40:2';
const OTHERS = ['HmIP-RF.000D40:1', 'HmIP-RF.000D40:3', 'HmIP-RF.000D40:4'];
const NO_PATTERN = ['HmIP-RF.000D41:1', 'HmIP-RF.000D41:2'];
const KEYS = ['BidCos-RF.BidCoS-RF:2', 'HmIP-RF.HmIP-RCV-1:1'];

async function open(page: Page, baseURL: string) {
    await page.context().addCookies([{name: 'stub-app', value: `show-${Math.random().toString(36).slice(2)}`, url: baseURL}]);
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        for (const ref of [FIRST, ...OTHERS, ...NO_PATTERN, ...KEYS]) json.objects[ref] = {name: ref, enums: [], meta: {}};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/unassigned');
    await expect(page.locator(`[data-app-tile="${FIRST}"]`)).toBeVisible();
}
const tile = (page: Page, ref: string) => page.locator(`[data-app-tile="${ref}"]`);

test('both are off by default: the first receiver stays, and what does not follow the pattern', async ({page, baseURL}) => {
    await open(page, baseURL!);
    await expect(page.locator('[data-app-show-virtual-channels]')).not.toBeChecked();
    await expect(page.locator('[data-app-show-virtual-keys]')).not.toBeChecked();
    for (const r of [...OTHERS, ...KEYS]) await expect(tile(page, r)).toHaveCount(0);
    for (const r of NO_PATTERN) await expect(tile(page, r)).toBeVisible();
});

test('each checkbox shows its own, keeps its box, and is remembered', async ({page, baseURL}) => {
    await open(page, baseURL!);
    const channels = page.locator('[data-app-show-virtual-channels]');
    const keys = page.locator('[data-app-show-virtual-keys]');
    const before = await page.locator('[data-app-unassigned-show]').boundingBox();
    await channels.check();
    for (const r of OTHERS) await expect(tile(page, r)).toBeVisible();
    for (const r of KEYS) await expect(tile(page, r)).toHaveCount(0);
    await keys.check();
    for (const r of KEYS) await expect(tile(page, r)).toBeVisible();
    expect(await page.locator('[data-app-unassigned-show]').boundingBox()).toEqual(before);
    await page.reload();
    await expect(page.locator('[data-app-show-virtual-channels]')).toBeChecked();
    await expect(page.locator('[data-app-show-virtual-keys]')).toBeChecked();
    await channels.uncheck();
    for (const r of OTHERS) await expect(tile(page, r)).toHaveCount(0);
});

test('no checkboxes on a room, and German', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await open(page, baseURL!);
    await expect(page.locator('[data-app-unassigned-show]')).toContainText('Zeige virtuelle Kanäle');
    await page.goto('/app/e/rooms/wohnzimmer');
    await expect(page.locator('[data-app-unassigned-show]')).toHaveCount(0);
});
