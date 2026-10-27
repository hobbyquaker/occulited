import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// openccu-lite task 248 (the maintainer: "zusatzsoftware in main menu should have reserved space for
// a colored dot and a triangle just like system menu entry has. if an update is available ... the
// yellow dot on main menu and submenu entry and also a warning on the status page"): the Addons tab
// keeps a dot's place and a caret as System does; an addon-update warning puts the yellow dot on the
// tab and on *Manage addons*, and the Status page shows the warning with its link.

const UPDATE = {id: 'addon-update', variant: 'mosquitto@2.1.2,redmatic@9.7.4', severity: 'warning', href: '/addons', since: new Date().toISOString(), params: {count: 2, addons: [{id: 'mosquitto', name: 'Mosquitto', installed: '2.1.0', available: '2.1.2'}, {id: 'redmatic', name: 'RedMatic', installed: '9.4.0', available: '9.7.4'}]}};

async function withWarning(page: Page, warning: object | null) {
    await page.route('**/api/system/v1/warnings', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []).filter((w: {id: string}) => w.id !== 'addon-update'), ...(warning ? [warning] : [])];
        await route.fulfill({response, json: j});
    });
}

const tab = (page: Page) => page.locator('.ol-addonsbtn');

test('the Addons tab keeps the dot and the caret like System, and does not change its width', async ({page}) => {
    await withWarning(page, null);
    await page.goto('/');
    await expect(tab(page).locator('.ol-caret')).toBeVisible();
    const dot = tab(page).locator('.ol-sysdot');
    await expect(dot).toHaveClass(/ol-sysdot-none/);
    const without = (await tab(page).boundingBox())!;
    // the same markup as System's tab: text, dot, caret in one row
    const sys = page.locator('.ol-systab');
    await expect(sys.locator('.ol-sysdot')).toHaveCount(1);
    await page.unroute('**/api/system/v1/warnings');
    await withWarning(page, UPDATE);
    await page.reload();
    await expect(tab(page).locator('[data-addon-dot="warning"]')).toBeVisible();
    const withDot = (await tab(page).boundingBox())!;
    expect(Math.abs(withDot.width - without.width)).toBeLessThanOrEqual(1);
    expect(await fitsWindow(page)).toBe(true);
});

test('an addon update: the yellow dot on the tab and on Manage addons, the Status warning and its link', async ({page}) => {
    await withWarning(page, UPDATE);
    await page.goto('/');
    const dot = tab(page).locator('.ol-sysdot');
    await expect(dot).toHaveClass(/warning/);
    await expect(dot).toHaveAttribute('aria-label', 'Warning');
    // yellow: the warning colour, as System's
    const colour = await dot.evaluate((e) => getComputedStyle(e).backgroundColor);
    const sysWarn = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--hmm-warn').trim());
    expect(colour).not.toBe('rgba(0, 0, 0, 0)');
    expect(sysWarn).not.toBe('');
    await tab(page).click();
    const manage = page.getByRole('menuitem', {name: /Manage addons/});
    await expect(manage.locator('[data-addon-dot="warning"]')).toBeVisible();
    await page.keyboard.press('Escape');
    // the Status page
    await page.goto('/status');
    const w = page.locator('[data-warnings] [data-notice="addon-update"]');
    await expect(w).toContainText('Updates are available for 2 addons: Mosquitto 2.1.2, RedMatic 9.7.4.');
    const link = w.getByRole('link', {name: 'Manage addons'});
    await expect(link).toHaveAttribute('href', '/addons');
});

test('one addon, in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await withWarning(page, {...UPDATE, variant: 'mosquitto@2.1.2', params: {count: 1, addons: [UPDATE.params.addons[0]]}});
    await page.goto('/status');
    await expect(page.locator('[data-warnings] [data-notice="addon-update"]')).toContainText('Für Mosquitto 2.1.2 ist ein Update verfügbar.');
    await expect(tab(page).locator('[data-addon-dot="warning"]')).toBeVisible();
});

// the maintainer's decision: red only for a failed or ended addon, yellow only for an update, and
// nothing for the other warnings that link to /addons
const FAILED = {id: 'addon-failed', variant: 'mosquitto', severity: 'error', href: '/addons', since: new Date().toISOString(), params: {addons: [{id: 'mosquitto', name: 'Mosquitto', enabled: true}]}};
const REGA = {id: 'rega', variant: 'redmatic', severity: 'error', href: '/addons', since: new Date().toISOString(), params: {addons: [{id: 'redmatic', name: 'RedMatic', enabled: false}]}};

test('a failed addon: the red dot, and the Status warning', async ({page}) => {
    await withWarning(page, FAILED);
    await page.goto('/status');
    await expect(tab(page).locator('[data-addon-dot="error"]')).toBeVisible();
    await expect(page.locator('[data-warnings] [data-notice="addon-failed"]')).toContainText('Addons that failed: Mosquitto.');
});

test('an update and a failed addon: red wins', async ({page}) => {
    await page.route('**/api/system/v1/warnings', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []).filter((w: {href?: string}) => w.href !== '/addons'), UPDATE, FAILED];
        await route.fulfill({response, json: j});
    });
    await page.goto('/');
    await expect(tab(page).locator('[data-addon-dot="error"]')).toBeVisible();
});

test('only a rega warning: no dot on the Addons tab, the warning stays on Status', async ({page}) => {
    await page.route('**/api/system/v1/warnings', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []).filter((w: {href?: string}) => w.href !== '/addons'), REGA];
        await route.fulfill({response, json: j});
    });
    await page.goto('/status');
    await expect(page.locator('[data-warnings] [data-notice="rega"]')).toBeVisible();
    await expect(tab(page).locator('.ol-sysdot')).toHaveClass(/ol-sysdot-none/);
    await tab(page).click();
    await expect(page.getByRole('menuitem', {name: /Manage addons/}).locator('[data-addon-dot]')).toHaveCount(0);
});

// openccu-lite B-267: after a restore, an addon without its program files is a Status warning per
// addon - installed before the restore, not started, reinstall it - in German with "Sie", and the
// link goes to the Addons page's reinstall section
const PAYLOAD = {id: 'addon-payload', variant: 'mosquitto', severity: 'error', href: '/addons#reinstall', since: new Date().toISOString(), params: {addons: [{id: 'mosquitto', name: 'Mosquitto', enabled: true}, {id: 'hmm', name: 'Homematic Manager', enabled: true}]}};

test('after a restore: the Status warning names the addons to reinstall', async ({page}) => {
    await withWarning(page, PAYLOAD);
    await page.goto('/status');
    const w = page.locator('[data-warnings] [data-notice="addon-payload"]');
    await expect(w).toContainText('Addons installed before the restore, now without their program files: Mosquitto, Homematic Manager. They are not started; reinstall them on the Addons page.');
});

test('after a restore, in German: the Status warning with "Sie"', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await withWarning(page, PAYLOAD);
    await page.goto('/status');
    await expect(page.locator('[data-warnings] [data-notice="addon-payload"]')).toContainText('Vor der Wiederherstellung installierte Addons, denen jetzt ihre Programmdateien fehlen: Mosquitto, Homematic Manager. Sie werden nicht gestartet; installieren Sie sie auf der Addons-Seite neu.');
});

// occulited task 119 (the maintainer, 2026-10-27, amending task 248 by this one case): an addon's own
// notice of severity warning lights the Addons tab yellow, an error red; an info lights nothing
for (const [severity, dot] of [['warning', 'warning'], ['error', 'error'], ['info', null]] as const) {
    test(`an addon notice (${severity}): ${dot ? `the ${dot} dot` : 'no dot'} on the Addons tab`, async ({page}) => {
        const notice = {id: 'addon-notice', variant: 'battery/low', severity, href: '/addon-settings/battery', params: {addon: 'battery', title: {de: 'Batterie schwach', en: 'Battery low'}}};
        await page.route('**/api/system/v1/warnings', async (route: Route) => {
            if (route.request().method() !== 'GET') return route.fallback();
            const response = await route.fetch();
            const j = await response.json();
            j.warnings = [...(j.warnings ?? []).filter((w: {id: string; href?: string}) => w.href !== '/addons' && w.id !== 'addon-notice'), notice];
            await route.fulfill({response, json: j});
        });
        await page.goto('/status');
        await expect(page.getByText('Battery low')).toBeVisible();
        if (dot) await expect(tab(page).locator(`[data-addon-dot="${dot}"]`)).toBeVisible();
        else await expect(tab(page).locator('.ol-sysdot')).toHaveClass(/ol-sysdot-none/);
    });
}
