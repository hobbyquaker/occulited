import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';

// openccu-lite B-241 (D-90): the welcome page asks once about the daily checks, one checkbox per
// destination, each named with where it connects, both unticked on a fresh system. GitHub is one
// box for two settings (the release check, the catalogue's daily check with the addons' update
// checks); eQ-3 is the device firmware check and download. The old text that named eQ-3 as "the
// only thing it calls out for" is gone.

/** the stub's answer with the setting patched, and the PUT bodies of a settings path */
async function setting(page: Page, get: string, patch: (j: Record<string, unknown>) => void, put: string, reply: (b: Record<string, unknown>) => object) {
    const bodies: unknown[] = [];
    await page.route(get, async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        patch(j);
        await route.fulfill({response, json: j});
    });
    await page.route(put, async (route: Route) => {
        const b = route.request().postDataJSON() as Record<string, unknown>;
        bodies.push(b);
        await route.fulfill({json: {ok: true, ...reply(b)}});
    });
    return bodies;
}

// B-292: the routed answers awaited before a check that reads them, and the routes ended with the
// test - a check that held before the patched answer was in ended the test while route.fetch was
// still running, which failed the run ("Response has been disposed") and judged the defaults.
function answered(page: Page, path: string) {
    return page.waitForResponse((r) => r.request().method() === 'GET' && new URL(r.url()).pathname === path);
}

async function fresh(page: Page) {
    const fw = await setting(page, '**/api/system/v1/firmware', (j) => (j.enabled = false), '**/api/system/v1/firmware/settings', (b) => ({enabled: b.enabled}));
    const rel = await setting(page, '**/api/system/v1/system-update', (j) => ((j.feed as Record<string, unknown>).enabled = false), '**/api/system/v1/system-update/settings', (b) => ({enabled: b.enabled}));
    const cat = await setting(page, '**/api/system/v1/catalog', (j) => (j.daily = false), '**/api/system/v1/catalog/settings', (b) => ({daily: b.daily}));
    return {fw, rel, cat};
}

test('a fresh system: both destinations named, both unticked, each box writes its settings', async ({page}) => {
    const puts = await fresh(page);
    const calls: string[] = [];
    page.on('request', (r) => calls.push(`${r.method()} ${new URL(r.url()).pathname}${new URL(r.url()).search}`));
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '2 · Automatic checks'})).toBeVisible();
    await expect(page.getByText('This system connects to the internet only when you ask it to.', {exact: false})).toBeVisible();
    const step = page.locator('[data-welcome-outbound]');
    const gh = step.locator('[data-outbound="github"]');
    const eq3 = step.locator('[data-outbound="eq3"]');
    await expect(gh).not.toBeChecked();
    await expect(eq3).not.toBeChecked();
    await expect(step).toContainText('GitHub (api.github.com, raw.githubusercontent.com)');
    await expect(step).toContainText('eQ-3 (ccu3-update.homematic.com)');
    await expect(step).toContainText('new system release');
    await expect(step).toContainText('addon catalogue');
    await expect(step).toContainText('firmware of the paired device types');
    await expect(page.getByText('the only thing it calls out for')).toHaveCount(0);
    await expect(page.getByText('Download automatically')).toHaveCount(0);
    // the page reads the catalogue as it is held, never the check
    expect(calls.filter((c) => c.includes('/catalog?refresh'))).toEqual([]);

    await gh.check();
    await expect.poll(() => puts.rel).toEqual([{enabled: true}]);
    await expect.poll(() => puts.cat).toEqual([{daily: true}]);
    await expect(gh).toBeChecked();
    expect(puts.fw).toEqual([]);

    await eq3.check();
    await expect.poll(() => puts.fw).toEqual([{enabled: true}]);
    await expect(eq3).toBeChecked();

    await gh.uncheck();
    await expect.poll(() => puts.rel).toEqual([{enabled: true}, {enabled: false}]);
    await expect.poll(() => puts.cat).toEqual([{daily: true}, {daily: false}]);
    await expect(gh).not.toBeChecked();

    // the other steps are still there, and Done leaves the page
    await expect(page.getByRole('heading', {name: '3 · Devices from a CCU or OpenCCU'})).toBeVisible();
    await page.getByRole('button', {name: 'Done'}).click();
    await expect(page).toHaveURL(/\/$/);
});

test('a system in use shows what it runs with: GitHub ticked only when both its settings are on', async ({page}) => {
    await setting(page, '**/api/system/v1/firmware', (j) => (j.enabled = true), '**/api/system/v1/firmware/settings', (b) => ({enabled: b.enabled}));
    await setting(page, '**/api/system/v1/system-update', (j) => ((j.feed as Record<string, unknown>).enabled = true), '**/api/system/v1/system-update/settings', (b) => ({enabled: b.enabled}));
    await setting(page, '**/api/system/v1/catalog', (j) => (j.daily = false), '**/api/system/v1/catalog/settings', (b) => ({daily: b.daily}));
    const answers = ['/api/system/v1/firmware', '/api/system/v1/system-update', '/api/system/v1/catalog'].map((p) => answered(page, p));
    await page.goto('/welcome');
    await Promise.all(answers);
    const step = page.locator('[data-welcome-outbound]');
    await expect(step.locator('[data-outbound="eq3"]')).toBeChecked();
    await expect(step.locator('[data-outbound="github"]')).not.toBeChecked();
});

test('German: the step and both destinations', async ({page}) => {
    await fresh(page);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '2 · Automatische Prüfungen'})).toBeVisible();
    const step = page.locator('[data-welcome-outbound]');
    await expect(step).toContainText('GitHub (api.github.com, raw.githubusercontent.com): täglich nach einer neuen Systemversion suchen');
    await expect(step).toContainText('eQ-3 (ccu3-update.homematic.com): täglich nach neuer Firmware');
    await expect(step).toContainText('Installieren bleibt Ihre Entscheidung.');
    // B-23: Sie throughout the page
    await expect(page.getByText('wenn Sie es darum bitten', {exact: false})).toBeVisible();
    await expect(page.locator('body')).not.toContainText(/\b(du|deine?[nmrs]?|dich|dir)\b/);
});

// occulited task 4: the second step no longer imports names from a running CCU over its ReGa port;
// it leads to the device import from a CCU or OpenCCU backup on the Backup page (openccu-lite task
// 251), says that this is not a restore, and is offered only while no device is paired here.
async function paired(page: Page, counts: Record<string, number> | null) {
    await page.route('**/api/system/v1/factory-reset', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        if (!counts) return route.fulfill({status: 503, json: {error: 'unavailable', message: 'no answer'}});
        const response = await route.fetch();
        const j = await response.json();
        j.interfaces = Object.fromEntries(Object.entries(counts).map(([name, devices]) => [name, {devices, known: true}]));
        await route.fulfill({response, json: j});
    });
}

test('the devices step: no device paired - the import of a CCU\'s backup, and the pointer to the restore of an openccu-lite one', async ({page}) => {
    await paired(page, {'BidCos-RF': 0, 'HmIP-RF': 0});
    const calls: string[] = [];
    page.on('request', (r) => calls.push(new URL(r.url()).pathname));
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '3 · Devices from a CCU or OpenCCU'})).toBeVisible();
    await expect(page.locator('[data-welcome-import]')).toHaveText('Moving from a CCU or OpenCCU: its backup is imported. The paired devices with their keys and names come into this system, and what you choose of its addons and settings. The system reboots afterwards.');
    // occulited task 142 (#19): the pointer to the restore, by its name - the page itself restores nothing
    await expect(page.locator('[data-welcome-restore]')).toContainText('A backup of openccu-lite is restored instead');
    await expect(page.locator('[data-action="welcome-restore"]')).toHaveAttribute('href', '/system/backup#restore');
    // the ReGa import by address is gone, and with it the Names page it pointed to
    await expect(page.getByPlaceholder('CCU address')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Preview'})).toHaveCount(0);
    await expect(page.getByText('Names page')).toHaveCount(0);
    const go = page.getByRole('link', {name: 'Import the backup of a CCU or OpenCCU'});
    await expect(go).toHaveAttribute('href', '/system/backup#restore');
    await go.click();
    await expect(page).toHaveURL(/\/system\/backup#restore$/);
    await expect(page.locator('h2#restore')).toBeVisible();
    expect(calls.filter((c) => c.includes('/import/ccu'))).toEqual([]);
});

test('the devices step: devices paired already - no offer, and why', async ({page}) => {
    await paired(page, {'BidCos-RF': 2, 'HmIP-RF': 1});
    await page.goto('/welcome');
    await expect(page.locator('[data-welcome-devices-paired]')).toHaveText('This system has 3 devices paired already: the import only works on a system without paired devices.');
    await expect(page.locator('[data-action="welcome-import-devices"]')).toHaveCount(0);
});

test('the devices step: the count cannot be read - the offer stays (the Backup page checks again)', async ({page}) => {
    await paired(page, null);
    await page.goto('/welcome');
    await expect(page.locator('[data-action="welcome-import-devices"]')).toBeVisible();
    await expect(page.locator('[data-welcome-devices-paired]')).toHaveCount(0);
});

test('German: the devices step says Sie', async ({page}) => {
    await paired(page, {'BidCos-RF': 0});
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '3 · Geräte von einer CCU oder OpenCCU'})).toBeVisible();
    await expect(page.locator('[data-welcome-import]')).toContainText('Umzug von einer CCU oder von OpenCCU: Deren Sicherung wird importiert.');
    await expect(page.locator('[data-welcome-restore]')).toContainText('Eine Sicherung von openccu-lite wird stattdessen eingespielt');
    await expect(page.getByRole('link', {name: 'Sicherung einer CCU oder von OpenCCU importieren'})).toHaveAttribute('href', '/system/backup#restore');
    await expect(page.getByText('Namen-Seite')).toHaveCount(0);
});
