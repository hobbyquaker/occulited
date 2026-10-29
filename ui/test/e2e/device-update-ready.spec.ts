import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 126 (the maintainer, 2026-10-28: "if an update is ready to install that should
// create a status page warning and link (ideally to the already expanded view of the device under
// updates where users then can click)"): device-update-ready names the HmIP devices whose firmware is
// transferred and waits for the install; its button opens the Updates page with their rows expanded,
// the device in view with its Install button, and the rows carry the warning's edge.

const PLUG = '0001D0000000B1';
const one = {id: 'device-update-ready', variant: `${PLUG}@2.2.6`, severity: 'warning', href: `/system/updates?device=${PLUG}#device-firmware`, since: new Date().toISOString(), params: {count: 1, updates: [{address: PLUG, name: 'Plug kitchen', type: 'HmIP-PDT', version: '2.2.6'}]}};

async function withWarning(page: Page, warning: object | null) {
    await page.route('**/api/system/v1/warnings', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []).filter((w: {id: string}) => w.id !== 'device-update-ready'), ...(warning ? [warning] : [])];
        await route.fulfill({response, json: j});
    });
}

/** the stub's HmIP-PDT in the kitchen transferred and ready, and enough other rows to scroll */
async function readyPlug(page: Page) {
    await page.route('**/api/system/v1/firmware', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const st = await response.json();
        st.devices = st.devices.map((d: {address: string}) => (d.address === PLUG ? {...d, firmware: '2.2.4', available_firmware: '2.2.6', latest: '2.2.6', update_available: true, update_state: 'READY_FOR_UPDATE'} : d));
        st.deployed = [...st.deployed, {type_code: '310', name: 'HmIP-PDT', version: '2.2.6', files: ['HmIP-PDT_update_V2_2_6.efw', 'info']}];
        await route.fulfill({response, json: st});
    });
}

const typeRow = (page: Page) => page.locator('table.fw-types tr.fw-row[data-type="HmIP-PDT"]');
const deviceRow = (page: Page) => page.locator(`tr[data-device-row][data-device="${PLUG}"]`);

test('the Status warning names the device and its version, and its button opens the device on the Updates page', async ({page}) => {
    await withWarning(page, one);
    await readyPlug(page);
    await page.goto('/status');
    const w = page.locator('[data-warnings] [data-notice="device-update-ready"]');
    await expect(w).toContainText('Firmware update ready to install: Plug kitchen (2.2.6).');
    const link = w.getByRole('link', {name: 'Updates'});
    await expect(link).toHaveAttribute('href', `/system/updates?device=${PLUG}#device-firmware`);
    await link.click();
    // the parameter is handled once and leaves the URL (B-126), the anchor stays
    await expect(page).toHaveURL(/\/system\/updates#device-firmware$/);
    // the type's row is open, the device in view with its Install button
    await expect(typeRow(page).locator('[data-expand]')).toHaveAttribute('aria-expanded', 'true');
    await expect(deviceRow(page)).toBeInViewport();
    await expect(deviceRow(page).locator('[data-install]')).toBeVisible();
    // the warning's edge: the panel, the type's row and the device's row
    await expect(page.locator('[data-panel="device-firmware"]')).toHaveAttribute('data-warn-edge', 'warn');
    await expect(typeRow(page)).toHaveAttribute('data-warn-edge', 'warn');
    await expect(deviceRow(page)).toHaveAttribute('data-warn-edge', 'warn');
    const want = await page.evaluate(() => {
        const p = document.createElement('span');
        p.style.color = 'var(--hmm-warn)';
        document.body.append(p);
        const c = getComputedStyle(p).color;
        p.remove();
        return c;
    });
    expect(await deviceRow(page).evaluate((e) => getComputedStyle(e.firstElementChild!).boxShadow)).toContain(want);
    // the other devices' rows carry none
    await expect(page.locator('tr[data-device-row][data-warn-edge]')).toHaveCount(1);
    expect(await fitsWindow(page)).toBe(true);
});

test('arriving by the link a second time opens the row again, a poll does not reopen one closed by hand', async ({page}) => {
    await withWarning(page, one);
    await readyPlug(page);
    await page.goto(`/system/updates?device=${PLUG}#device-firmware`);
    const expand = typeRow(page).locator('[data-expand]');
    await expect(expand).toHaveAttribute('aria-expanded', 'true');
    await expand.click();
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    // the page polls every 5 s: the row stays closed
    await page.waitForTimeout(5500);
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    // back through the Status page's link (the kept page, not a reload): open again
    await page.getByRole('link', {name: 'Status', exact: true}).first().click();
    await page.locator('[data-warnings] [data-notice="device-update-ready"]').getByRole('link', {name: 'Updates'}).click();
    await expect(expand).toHaveAttribute('aria-expanded', 'true');
    await expect(deviceRow(page)).toBeInViewport();
});

test('a row closed by hand stays closed on a reload, Back and a return through the menu (B-126)', async ({page}) => {
    await withWarning(page, one);
    await readyPlug(page);
    await page.goto('/status');
    await page.locator('[data-warnings] [data-notice="device-update-ready"]').getByRole('link', {name: 'Updates'}).click();
    const expand = typeRow(page).locator('[data-expand]');
    await expect(expand).toHaveAttribute('aria-expanded', 'true');
    await expect(page).toHaveURL(/\/system\/updates#device-firmware$/);
    await expand.click();
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    // to the Status page by the menu and Back: the kept page keeps the row closed
    await page.getByRole('link', {name: 'Status', exact: true}).first().click();
    await expect(page.locator('[data-warnings]')).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/system\/updates#device-firmware$/);
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    // a reload: the URL no longer names the device
    await page.reload();
    await expect(typeRow(page)).toBeVisible();
    await expect(typeRow(page).locator('[data-expand]')).toHaveAttribute('aria-expanded', 'false');
});

test('several devices, in German: one warning with the count and every device', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await withWarning(page, {...one, variant: `${PLUG}@2.2.6,00160000000C01@1.4.8`, href: `/system/updates?device=${PLUG},00160000000C01#device-firmware`,
        params: {count: 2, updates: [{address: PLUG, name: 'Plug kitchen', type: 'HmIP-PDT', version: '2.2.6'}, {address: '00160000000C01', name: '', type: 'HmIP-WRC2', version: ''}]}});
    await page.goto('/status');
    const w = page.locator('[data-warnings] [data-notice="device-update-ready"]');
    await expect(w).toContainText('Firmware-Updates für 2 Geräte bereit zur Installation: Plug kitchen (2.2.6), HmIP-WRC2 00160000000C01.');
    await expect(w.getByRole('link', {name: 'Updates'})).toHaveAttribute('href', `/system/updates?device=${PLUG},00160000000C01#device-firmware`);
});

test('without the warning: no edge, the rows closed', async ({page}) => {
    await withWarning(page, null);
    await readyPlug(page);
    await page.goto('/system/updates');
    await expect(typeRow(page).locator('[data-expand]')).toHaveAttribute('aria-expanded', 'false');
    await expect(page.locator('[data-panel="device-firmware"]')).not.toHaveAttribute('data-warn-edge', /./);
    await expect(typeRow(page)).not.toHaveAttribute('data-warn-edge', /./);
});
