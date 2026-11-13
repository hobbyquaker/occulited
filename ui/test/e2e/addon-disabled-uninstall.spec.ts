import {expect, test} from './fixtures';
import type {Page} from '@playwright/test';

// occulited B-136 (openccu-lite/openccu-lite#15): a disabled addon - hm-watchdog with ARM binaries
// from an OpenCCU backup, which the start disabled - shows its name and version and can be
// uninstalled: from the ⋯ menu and from the "Came from the CCU" notice, which offers "Remove the
// rc.d entry" only where there is no addon directory (the system refuses it otherwise).

const watchdog = {
    id: 'hm-watchdog', name: 'HM Watchdog', version: '0.9', operations: ['uninstall'], running: false, enabled: false, directory: true,
    binary_incompatible: true, binary_reason: 'bin/hm-watchdog is built for arm', policy_mode: 'root', policy_source: 'migrated', from_ccu: true,
};
// a CCU user's own script: an rc.d entry and nothing else
const hack = {id: 'hdmi-wlan-disable', name: '', version: '', operations: [], running: false, enabled: false, policy_mode: 'root', policy_source: 'migrated', from_ccu: true};
// a healthy addon the user switched off with "Disable at boot"
const off = {id: 'history', name: 'History', version: '1.4.0', operations: ['uninstall'], running: false, enabled: false, directory: true, policy_mode: 'confined', policy_source: 'catalog'};

async function withAddons(page: Page, more: object[]) {
    await page.route('**/api/system/v1/addons', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.addons.push(...more);
        await route.fulfill({response, json: j});
    });
}

const card = (page: Page, id: string) => page.locator(`[data-addon-card="${id}"]`);

test('a disabled addon with a directory: name, version, Uninstall in the notice and in the menu, no "Remove the rc.d entry"', async ({page}) => {
    await withAddons(page, [watchdog, hack]);
    let asked = 0;
    await page.route('**/api/system/v1/addons/hm-watchdog/uninstall', (route) => {
        asked++;
        return route.fulfill({json: {ok: true, output: 'cannot stop', forced: true, script_error: 'exit status 3', system_removed: ['/usr/local/etc/config/rc.d/hm-watchdog', '/usr/local/addons/hm-watchdog']}});
    });
    await page.goto('/addons');
    const c = card(page, 'hm-watchdog');
    await expect(c.locator('.ad-name')).toContainText('HM Watchdog');
    await expect(c.locator('.ad-version')).toHaveText('0.9');
    await expect(c.locator('[data-addon-state]')).toContainText('disabled');
    const hint = c.locator('[data-from-ccu]');
    await expect(hint).toContainText('Came from the CCU');
    await expect(hint.locator('[data-remove-rc]')).toHaveCount(0);
    await expect(hint.locator('[data-from-ccu-uninstall]')).toBeVisible();
    // the entry alone, where there is nothing else: the other button, as before
    const h = card(page, 'hdmi-wlan-disable').locator('[data-from-ccu]');
    await expect(h.locator('[data-remove-rc]')).toBeVisible();
    await expect(h.locator('[data-from-ccu-uninstall]')).toHaveCount(0);
    await card(page, 'hdmi-wlan-disable').locator('.ol-morebtn').click();
    await expect(card(page, 'hdmi-wlan-disable').locator('[data-addon-uninstall]')).toHaveCount(0);
    await page.keyboard.press('Escape');

    // the menu offers it too; the question says what happens when the script fails
    await c.locator('.ol-morebtn').click();
    await c.locator('[data-addon-uninstall]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Uninstall HM Watchdog?');
    await expect(dialog).toContainText('The addon is disabled: should its uninstall script fail, the system removes its rc.d entry and its directory /usr/local/addons/hm-watchdog with everything in it.');
    await dialog.getByRole('button', {name: 'Uninstall', exact: true}).click();
    await expect.poll(() => asked).toBe(1);
    const notice = page.getByText(/Uninstalled: HM Watchdog/);
    await expect(notice).toContainText('Its uninstall script failed (exit status 3); the addon was disabled, so the system removed it anyway.');
    await expect(notice).toContainText('/usr/local/addons/hm-watchdog');
});

test('the notice\'s Uninstall, in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await withAddons(page, [watchdog]);
    let asked = 0;
    await page.route('**/api/system/v1/addons/hm-watchdog/uninstall', (route) => {
        asked++;
        return route.fulfill({json: {ok: true, output: '', forced: true, script_error: 'exit status 3', system_removed: ['/usr/local/addons/hm-watchdog']}});
    });
    await page.goto('/addons');
    const hint = card(page, 'hm-watchdog').locator('[data-from-ccu]');
    await expect(hint).toContainText('Von der CCU übernommen');
    await hint.getByRole('button', {name: 'Deinstallieren', exact: true}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Die Zusatzsoftware ist deaktiviert: Schlägt ihr Deinstallationsskript fehl, entfernt das System ihren rc.d-Eintrag und ihr Verzeichnis /usr/local/addons/hm-watchdog mit allem darin.');
    await dialog.getByRole('button', {name: 'Deinstallieren', exact: true}).click();
    await expect.poll(() => asked).toBe(1);
    await expect(page.getByText(/Deinstalliert: HM Watchdog/)).toContainText('Ihr Deinstallationsskript ist fehlgeschlagen (exit status 3); die Zusatzsoftware war deaktiviert, deshalb hat das System sie trotzdem entfernt.');
});

test('an addon the user disabled: Uninstall in the menu, and no word of a failure when its script worked', async ({page}) => {
    await withAddons(page, [off]);
    await page.route('**/api/system/v1/addons/history/uninstall', (route) => route.fulfill({json: {ok: true, output: 'removed', system_removed: ['/usr/local/etc/config/rc.d/history']}}));
    await page.goto('/addons');
    const c = card(page, 'history');
    await expect(c.locator('.ad-version')).toHaveText('1.4.0');
    await expect(c.locator('[data-from-ccu]')).toHaveCount(0);
    await c.locator('.ol-morebtn').click();
    await expect(c.getByRole('menuitem', {name: 'Enable at boot'})).toBeVisible();
    await c.locator('[data-addon-uninstall]').click();
    await page.getByRole('dialog').getByRole('button', {name: 'Uninstall', exact: true}).click();
    const notice = page.getByText(/Uninstalled: History/);
    await expect(notice).toContainText('removed');
    await expect(notice).not.toContainText('failed');
});

test('an enabled addon is asked as before', async ({page}) => {
    await page.goto('/addons');
    const c = card(page, 'mosquitto');
    await c.locator('.ol-morebtn').click();
    await c.locator('[data-addon-uninstall]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Uninstall Mosquitto?');
    await expect(dialog).not.toContainText('disabled');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
});
