import {expect, test} from './fixtures';

// occulited task 28: a script that came along from the CCU and fails here says so on the Addons
// page - "came from the CCU", failed, its last log line - and one click removes its rc.d entry;
// the file its link led to is named and removed only when chosen.

const hack = {
    id: 'hdmi-wlan-disable', name: 'hdmi-wlan-disable', version: '', operations: ['start', 'stop'], running: false, enabled: true, failed: true,
    policy_mode: 'root', policy_source: 'migrated', from_ccu: true, failed_log: 'rmmod: Module brcmfmac is not currently loaded', rc_target: '/usr/local/bin/hdmi-wlan-disable.sh',
};

test('a failed script from the CCU: the hint, and removing its rc.d entry with the target chosen', async ({page}) => {
    await page.route('**/api/system/v1/addons', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.addons.push(hack);
        await route.fulfill({response, json: j});
    });
    let body: unknown = null;
    await page.route('**/api/system/v1/addons/hdmi-wlan-disable/remove-rc-entry', (route) => {
        body = route.request().postDataJSON();
        return route.fulfill({json: {ok: true, removed: ['/usr/local/etc/config/rc.d/hdmi-wlan-disable', '/usr/local/etc/config/rc.d/hdmi-wlan-disable.script', '/usr/local/bin/hdmi-wlan-disable.sh'], target: hack.rc_target}});
    });
    await page.goto('/addons');
    const hint = page.locator('[data-from-ccu]');
    await expect(hint).toContainText('Came from the CCU');
    await expect(hint).toContainText('rmmod: Module brcmfmac is not currently loaded');
    await hint.locator('[data-remove-rc]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Remove the rc.d entry of hdmi-wlan-disable?');
    await expect(dialog).toContainText('/usr/local/bin/hdmi-wlan-disable.sh');
    await dialog.getByText('The rc.d entry and /usr/local/bin/hdmi-wlan-disable.sh').click();
    await dialog.getByRole('button', {name: 'Remove', exact: true}).click();
    await expect.poll(() => body).toEqual({target: true});
    await expect(page.getByText(/Removed: .*hdmi-wlan-disable\.sh/)).toBeVisible();
});

test('only the entry by default, in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/system/v1/addons', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.addons.push(hack);
        await route.fulfill({response, json: j});
    });
    let body: unknown = null;
    await page.route('**/api/system/v1/addons/hdmi-wlan-disable/remove-rc-entry', (route) => {
        body = route.request().postDataJSON();
        return route.fulfill({json: {ok: true, removed: ['/usr/local/etc/config/rc.d/hdmi-wlan-disable']}});
    });
    await page.goto('/addons');
    const hint = page.locator('[data-from-ccu]');
    await expect(hint).toContainText('Von der CCU übernommen');
    await hint.getByRole('button', {name: 'rc.d-Eintrag entfernen'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Entfernen', exact: true}).click();
    await expect.poll(() => body).toEqual({target: false});
});

test('an addon not from the CCU has no such hint', async ({page}) => {
    await page.goto('/addons');
    await expect(page.locator('[data-addon-state]').first()).toBeVisible();
    await expect(page.locator('[data-from-ccu]')).toHaveCount(0);
});
