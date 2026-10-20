import {type Page, type Route} from '@playwright/test';
import {expect, shellStreamInWindow, test} from './fixtures';

// openccu-lite B-297: the Addons menu and the pinned tabs follow an uninstall and an install
// without a reload - in the tab that did it (the Addons page says so when its work is done) and in
// every other one (the system's addon revision, the topic addons of GET /api/system/v1/stream). Each spec
// answers the addon routes itself, so nothing depends on the stub's shared state.

const menu = (page: Page) => page.locator('.ol-menu').first();
const button = (page: Page) => menu(page).locator('.ol-menubtn');
const popup = (page: Page) => menu(page).getByRole('menu');
const names = (page: Page) => popup(page).locator('.ol-menurow .ol-addonname');
const pinTab = (page: Page, name: string) => page.locator('nav.ol-nav > a.ol-pintab .ol-pintab-name', {hasText: new RegExp(`^${name}$`)});

let n = 0;
/**
 * The addons as a switch: `state.gone` takes RedMatic out of GET /addons and GET /nav, and the
 * stream answers `state.revision` - once per connection, then it ends, and the browser connects
 * again after 100 ms, so a changed revision arrives as it would from the system.
 */
async function addons(page: Page, baseURL: string, state: {gone: boolean; revision: number; uninstalls?: number; streamed?: number}) {
    await page.context().addCookies([{name: 'stub-prefs', value: `follows-${process.pid}-${Date.now()}-${n++}`, url: baseURL}]);
    const filtered = async (route: Route, key: 'addons' | 'entries') => {
        const res = await route.fetch();
        const body = await res.json();
        if (state.gone) body[key] = body[key].filter((x: {id: string; addon?: string}) => (x.addon ?? x.id) !== 'redmatic');
        await route.fulfill({response: res, json: body});
    };
    await page.route('**/api/system/v1/addons', (r) => filtered(r, 'addons'));
    await page.route('**/api/system/v1/nav', (r) => filtered(r, 'entries'));
    await page.route('**/api/system/v1/addons/redmatic/uninstall', (r) => {
        state.gone = true;
        state.uninstalls = (state.uninstalls ?? 0) + 1;
        return r.fulfill({json: {ok: true, output: '', system_removed: []}});
    });
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', async (r) => {
        await r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: `retry: 100\nevent: addons\ndata: ${JSON.stringify({revision: state.revision})}\n\n`});
        state.streamed = (state.streamed ?? 0) + 1;
    });
}

/**
 * occulited B-67: the shell takes the first revision it is told as the one it knows (lib/addonsync.ts),
 * so a revision moved before the page's stream has told it the old one is no move at all - under a
 * loaded machine the stream connected late and the page never followed. Wait until two answers went
 * out since now: the second connects only after the first ended, so the first was read.
 */
async function streamedTwice(state: {streamed?: number}) {
    const from = state.streamed ?? 0;
    await expect.poll(() => state.streamed ?? 0, {timeout: 15_000}).toBeGreaterThanOrEqual(from + 2);
}

async function pinRedMatic(page: Page) {
    await button(page).click();
    const row = popup(page).locator('.ol-menurow').filter({has: page.locator('.ol-addonname', {hasText: /^RedMatic$/})});
    await row.locator('.ol-pin').click();
    await expect(pinTab(page, 'RedMatic')).toBeVisible();
    await page.keyboard.press('Escape');
}

test('an uninstall on the Addons page takes the entry and its pinned tab out of the bar at once', async ({page, baseURL}) => {
    await page.setViewportSize({width: 1600, height: 800});
    const state = {gone: false, revision: 10};
    await addons(page, baseURL!, state);
    await page.goto('/addons');
    await pinRedMatic(page);
    await button(page).click();
    await expect(names(page)).toContainText(['RedMatic']);
    await page.keyboard.press('Escape');

    const card = page.locator('#addon-row-redmatic');
    await card.getByRole('button', {name: 'More actions'}).click();
    await card.getByRole('menu').getByRole('menuitem', {name: 'Uninstall'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Uninstall'}).click();
    await expect.poll(() => state.uninstalls).toBe(1);

    // no reload, and the revision did not move: the page's own word did it
    await expect(pinTab(page, 'RedMatic')).toHaveCount(0);
    await button(page).click();
    await expect(names(page)).not.toContainText(['RedMatic']);
    await expect(names(page).filter({hasText: /^Homematic-Manager$/})).toHaveCount(1);
});

test('another tab, another device: a moved revision takes the entry out, and an install brings it back', async ({page, baseURL}) => {
    await page.setViewportSize({width: 1600, height: 800});
    const state = {gone: false, revision: 20};
    await addons(page, baseURL!, state);
    await page.goto('/');
    await pinRedMatic(page);
    await streamedTwice(state);

    // uninstalled elsewhere: the next revision says so
    state.gone = true;
    state.revision = 21;
    await expect(pinTab(page, 'RedMatic')).toHaveCount(0);
    await button(page).click();
    await expect(names(page).filter({hasText: /^RedMatic$/})).toHaveCount(0);
    await page.keyboard.press('Escape');

    // installed again elsewhere: the entry, and the pin the account kept, come back
    state.gone = false;
    state.revision = 22;
    await expect(pinTab(page, 'RedMatic')).toBeVisible();
    await button(page).click();
    await expect(names(page).filter({hasText: /^RedMatic$/})).toHaveCount(1);
});

test('the same revision again reads nothing', async ({page, baseURL}) => {
    const state = {gone: false, revision: 30};
    await addons(page, baseURL!, state);
    let navReads = 0;
    page.on('request', (r) => {
        if (new URL(r.url()).pathname === '/api/system/v1/nav') navReads++;
    });
    await page.goto('/');
    await expect(button(page)).toBeVisible();
    // several reconnects with the same revision
    await page.waitForTimeout(800);
    expect(navReads).toBe(1);
});

test('an open addon page whose addon was uninstalled elsewhere says so', async ({page, baseURL}) => {
    const state = {gone: false, revision: 40};
    await addons(page, baseURL!, state);
    await page.goto('/nav/red');
    await expect(page.locator('iframe').first()).toBeAttached();
    await streamedTwice(state);
    state.gone = true;
    state.revision = 41;
    const gone = page.locator('[data-nav-gone]');
    await expect(gone).toHaveText(/This addon is no longer installed\./);
    await expect(gone.getByRole('link', {name: 'Addons'})).toHaveAttribute('href', '/addons');
    // its kept page went with it
    await expect(page.locator('iframe[src*="/addons/red/"]')).toHaveCount(0);
});
