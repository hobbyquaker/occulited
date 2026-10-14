import {type Locator, type Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {PORT, fitsWindow} from './scroll';

// Task 40 (the maintainer, 2026-10-13: three panels for the device firmware were confusing): one
// row per paired device type with one status - downloaded and ready to install, available at eQ-3,
// up to date - the types that cannot be updated hidden until Show all devices, a search over type,
// device name and address, the bundles no paired type takes under Other downloaded firmware, and a
// bundle's text files in the shell's modal.

function rows(page: Page): Locator {
    return page.locator('table.fw-types tbody tr.fw-row');
}
const row = (page: Page, type: string) => page.locator(`table.fw-types tr.fw-row[data-type="${type}"]`);
const search = (page: Page) => page.getByRole('textbox', {name: 'Search the device firmware'});

/** The relative luminance of a computed CSS colour, 0 black to 1 white. */
function luminance(css: string): number {
    const [r, g, b] = (css.match(/[\d.]+/g) ?? ['0', '0', '0']).slice(0, 3).map(Number);
    return (0.2126 * r! + 0.7152 * g! + 0.0722 * b!) / 255;
}

type Firmware = {devices: Record<string, unknown>[]; deployed: Record<string, unknown>[]; last_result: Record<string, unknown>[]};

/** GET /firmware with the stub's answer changed by `change`. */
async function patchFirmware(page: Page, change: (st: Firmware) => void) {
    await page.route('**/api/system/v1/firmware', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const st = await response.json();
        change(st);
        await route.fulfill({response, json: st});
    });
}

test('one row per updatable type, the ones with something to do first, each with one status', async ({page}) => {
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    expect(await rows(page).evaluateAll((trs) => trs.map((tr) => [tr.getAttribute('data-type'), tr.getAttribute('data-status')]))).toEqual([
        ['HmIPW-DRS8', 'downloaded'],
        ['HMIP-WRC2', 'available'],
        ['HmIP-PDT', 'current'],
        ['HmIP-SWDO-2', 'current'],
    ]);
    await expect(row(page, 'HmIPW-DRS8').locator('[data-badge]')).toHaveText('Downloaded, ready to install');
    await expect(row(page, 'HMIP-WRC2').locator('[data-badge]')).toHaveText('Available at eQ-3');
    await expect(row(page, 'HMIP-WRC2').locator('[data-version]')).toHaveText('1.18.2');
    await expect(row(page, 'HMIP-WRC2').locator('[data-fw-installed]')).toHaveText('1.0.3, 1.18.2');
    await expect(row(page, 'HMIP-WRC2').locator('[data-behind]')).toHaveText('for 1 of 2 devices');
    await expect(row(page, 'HMIP-WRC2')).toContainText('2 devices');
    await expect(row(page, 'HmIP-PDT').locator('[data-badge]')).toHaveText('Up to date');
    // the summary counts the statuses; the hidden types are named under the table
    await expect(page.locator('[data-sum="downloaded"]')).toHaveText('1 ready to install');
    await expect(page.locator('[data-sum="available"]')).toHaveText('1 available at eQ-3');
    await expect(page.locator('[data-sum="current"]')).toHaveText('2 up to date');
    await expect(page.locator('[data-fw-hidden]')).toContainText('2 device types that cannot be updated are hidden.');
    await expect(rows(page).filter({hasText: 'HM-CC-TC'})).toHaveCount(0);
    // the interface's 0.0.0 is never shown as a version
    await expect(page.locator('[data-panel="device-firmware"]')).not.toContainText('0.0.0');
    expect(await fitsWindow(page)).toBe(true);
});

// homematic-manager task 93's rule: the version has a slot of its own, so the status label starts at
// the same x in every row
test('the status labels line up', async ({page}, info) => {
    test.skip(info.project.name === 'phone', 'the phone stacks the cells');
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    const xs = await rows(page).locator('[data-badge]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().left)));
    expect(new Set(xs).size).toBe(1);
});

test('Show all devices brings back what cannot be updated, each with its reason', async ({page}) => {
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    await page.locator('[data-fw-hidden]').getByRole('button', {name: 'Show all devices'}).click();
    await expect(page.locator('[data-show-all]')).toBeChecked();
    await expect(rows(page)).toHaveCount(6);
    await expect(row(page, 'HM-CC-TC').locator('[data-badge]')).toHaveText('No firmware at eQ-3');
    await expect(row(page, 'HMW-IO-12-Sw7-DR').locator('[data-badge]')).toHaveText('Only with the system image');
    await expect(page.locator('[data-fw-hidden]')).toHaveCount(0);
    await page.locator('[data-show-all]').uncheck();
    await expect(rows(page)).toHaveCount(4);
    expect(await fitsWindow(page)).toBe(true);
});

test('a row opens to its devices: name, address, firmware, the ones behind marked', async ({page}) => {
    await page.goto('/system/updates');
    const expand = row(page, 'HMIP-WRC2').getByRole('button', {name: /HMIP-WRC2/});
    await expect(expand).toHaveAttribute('aria-expanded', 'false');
    await expand.click();
    await expect(expand).toHaveAttribute('aria-expanded', 'true');
    const list = page.locator('[data-devices]');
    await expect(list.locator('li')).toHaveCount(2);
    const named = list.locator('li[data-device="00010000000A10"]');
    await expect(named).toContainText('Remote hallway');
    await expect(named).toContainText('1.0.3 → 1.18.2');
    const unnamed = list.locator('li[data-device="00010000000A11"]');
    await expect(unnamed).toContainText('unnamed');
    await expect(unnamed.locator('.fw-arrow')).toHaveCount(0);
    expect(await fitsWindow(page)).toBe(true);
    await expand.click();
    await expect(page.locator('[data-devices]')).toHaveCount(0);
});

test('the search finds a type, a device name and an address; a device match opens its row', async ({page}) => {
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    await search(page).fill('swdo');
    await expect(rows(page)).toHaveCount(1);
    await expect(page.locator('[data-devices]')).toHaveCount(0);
    // by name: the rows open, the devices are seen
    await search(page).fill('kitchen');
    await expect(rows(page)).toHaveCount(2);
    await expect(page.locator('[data-devices] li', {hasText: 'Plug kitchen'})).toBeVisible();
    await expect(page.locator('[data-devices] li', {hasText: 'Window kitchen'})).toBeVisible();
    // a hidden type that matches is counted
    await search(page).fill('cellar');
    await expect(rows(page)).toHaveCount(0);
    await expect(page.locator('[data-fw-nomatch]')).toHaveText('Nothing matches the search.');
    await expect(page.locator('[data-fw-hidden]')).toContainText('1 device type that cannot be updated is hidden.');
    await page.locator('[data-show-all]').check();
    await expect(rows(page)).toHaveCount(1);
    await expect(page.locator('[data-devices]')).toContainText('Thermostat cellar');
    // by address, in any case
    await search(page).fill('00160000000c01');
    await expect(rows(page)).toHaveCount(1);
    await expect(row(page, 'HmIPW-DRS8')).toBeVisible();
    await search(page).fill('');
    await expect(rows(page)).toHaveCount(6);
});

test('a downloaded bundle: the version from the file name, marked; its texts open in the modal', async ({page}, info) => {
    await page.goto('/system/updates');
    const drs8 = row(page, 'HmIPW-DRS8');
    await expect(drs8.locator('[data-version]')).toHaveText('1.2.6');
    await expect(drs8.locator('[data-version]')).toHaveClass(/ol-fromname/);
    await expect(drs8.locator('[data-from-name]')).toHaveText('from the file name · 2022-09-28');
    await expect(drs8.locator('[data-from-name]')).toHaveAttribute('title', 'The bundle states no version; this one is read from the name of its update file.');
    const opener = drs8.getByRole('button', {name: 'changelog.txt'});
    await opener.click();
    const dialog = page.getByRole('dialog', {name: 'changelog.txt'});
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('.ol-modal-sub')).toHaveText('HmIPW-DRS8');
    const text = dialog.locator('pre');
    await expect(text).toContainText('Version 1.2.6 (2022-09-28)');
    const style = await text.evaluate((e) => ({font: getComputedStyle(e).fontFamily, ws: getComputedStyle(e).whiteSpace}));
    expect(style.font).toMatch(/mono/i);
    expect(style.ws).toBe('pre-wrap');
    // the page behind does not scroll while the dialog is open
    expect(await page.evaluate((sel) => document.querySelector<HTMLElement>(sel)!.style.overflow, PORT)).toBe('hidden');
    // the dialog's surface follows the theme
    const bg = luminance(await dialog.evaluate((e) => getComputedStyle(e).backgroundColor));
    if (info.project.name.includes('dark')) expect(bg).toBeLessThan(0.4);
    else expect(bg).toBeGreaterThan(0.6);
    // the long line wraps inside the dialog: nothing runs off the viewport, not on a phone either
    const box = (await dialog.boundingBox())!;
    const viewport = page.viewportSize()!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
    expect(await text.evaluate((e) => e.scrollWidth <= e.clientWidth + 1)).toBe(true);
    await dialog.getByRole('button', {name: 'Close'}).click();
    await expect(dialog).toBeHidden();
    await expect(opener).toBeFocused();

    await drs8.getByRole('button', {name: 'info', exact: true}).click();
    const infoDialog = page.getByRole('dialog', {name: 'info'});
    await expect(infoDialog.locator('pre')).toContainText('FirmwareVersion=0.0.0');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toBeHidden();
    expect(await page.evaluate((sel) => document.querySelector<HTMLElement>(sel)!.style.overflow, PORT)).toBe('');
});

test('a refused file shows the answer of the system', async ({page}) => {
    await page.route('**/api/system/v1/firmware/bundles/310/files/changelog.txt', (r) =>
        r.fulfill({status: 413, json: {error: 'too-large', message: 'the file is 300000 bytes, more than the 262144 bytes shown here', detail: {size: 300000, limit: 262144}}}),
    );
    await page.goto('/system/updates');
    await row(page, 'HmIP-PDT').getByRole('button', {name: 'changelog.txt'}).click();
    const dialog = page.getByRole('dialog', {name: 'changelog.txt'});
    await expect(dialog).toContainText('the file is 300000 bytes');
    await expect(dialog.locator('pre')).toHaveCount(0);
});

test('Download now starts the check', async ({page}) => {
    let posted = 0;
    await page.route('**/api/system/v1/firmware/check', (r) => {
        posted++;
        return r.fulfill({status: 202, json: {ok: true, message: 'check started'}});
    });
    await page.goto('/system/updates');
    await row(page, 'HMIP-WRC2').getByRole('button', {name: 'Download now'}).click();
    await expect(page.locator('[data-panel="device-firmware"] .ol-notice')).toHaveText('Check started');
    expect(posted).toBe(1);
    // only a type eQ-3 has a newer firmware for offers it
    await expect(page.locator('[data-download]')).toHaveCount(1);
});

test("eQ-3's newer firmware beyond the downloaded one, and a failed download", async ({page}) => {
    await patchFirmware(page, (st) => {
        st.devices = st.devices.map((d) => (d.type === 'HmIPW-DRS8' ? {...d, latest: '1.4.0'} : d));
        st.last_result = [...st.last_result, {type: 'HmIPW-DRS8', version: '1.4.0', action: 'failed', detail: 'HTTP 404'}];
    });
    await page.goto('/system/updates');
    const drs8 = row(page, 'HmIPW-DRS8');
    await expect(drs8).toHaveAttribute('data-status', 'available');
    await expect(drs8.locator('[data-version]')).toHaveText('1.4.0');
    await expect(drs8.locator('[data-older-download]')).toHaveText('1.2.6 is downloaded');
    await expect(drs8.locator('[data-failed]')).toHaveText('Last download failed: HTTP 404');
});

// B-225: a device the interface calls not updatable (rfd's UPDATABLE 0) is hidden, and says why
// when all are shown
test('not updatable over the air, in English and German', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.devices = st.devices.map((d) => (d.type === 'HM-CC-TC' ? {...d, not_listed: false, updatable: false} : d));
    });
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    await page.locator('[data-show-all]').check();
    await expect(row(page, 'HM-CC-TC').locator('[data-badge]')).toHaveText('Not updatable over the air');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await page.locator('[data-show-all]').check();
    await expect(row(page, 'HM-CC-TC').locator('[data-badge]')).toHaveText('Nicht über Funk aktualisierbar');
    await expect(page.getByRole('heading', {name: 'Gerätefirmware'})).toBeVisible();
    await expect(page.locator('[data-sum="downloaded"]')).toHaveText('1 bereit zur Installation');
});

// the bundles no paired type takes stay visible, collapsed; a second bundle of a shared TypeCode lies
// in <TypeCode>-<Name>, and its files are read from that directory; a BidCos bundle is read by rfd
test('other downloaded firmware: collapsed, its texts, the directory, the interface', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.deployed = [
            ...st.deployed,
            {type_code: '261', dir: '261-HM-LC-Dim1T-DR', name: 'HM-LC-Dim1T-DR', version: '1.1.0', files: ['HM-LC-Dim1T-DR_update_V1_1_0_171122.eq3', 'changelog.txt', 'info'], info: {}},
        ];
    });
    let asked = '';
    await page.route('**/api/system/v1/firmware/bundles/*/files/*', (route) => {
        asked = new URL(route.request().url()).pathname;
        return route.fulfill({contentType: 'text/plain', body: 'V1.1.0\n- dimmer'});
    });
    await page.goto('/system/updates');
    const others = page.locator('[data-fw-others]');
    await expect(others.locator('summary')).toHaveText('Other downloaded firmware · 2');
    await expect(others.locator('table')).toBeHidden();
    await others.locator('summary').click();
    const xyz = others.locator('tr[data-bundle="HmIP-XYZ"]');
    await expect(xyz.locator('td').nth(1)).toHaveText('–');
    await expect(xyz.getByRole('button')).toHaveCount(1);
    await expect(xyz.getByRole('button', {name: 'info', exact: true})).toBeVisible();
    await expect(xyz).toContainText('firmware.efw');
    const dim = others.locator('tr[data-bundle="HM-LC-Dim1T-DR"]');
    await expect(dim).toContainText('BidCos-RF');
    await dim.getByRole('button', {name: 'changelog.txt'}).click();
    await expect(page.getByRole('dialog', {name: 'changelog.txt'})).toContainText('dimmer');
    expect(asked).toBe('/api/system/v1/firmware/bundles/261-HM-LC-Dim1T-DR/files/changelog.txt');
    await page.keyboard.press('Escape');
    // the firmware image is no link
    await expect(dim.getByRole('button', {name: /\.eq3$/})).toHaveCount(0);
    expect(await fitsWindow(page)).toBe(true);
});

test('the upload is a dialog behind a button', async ({page}) => {
    await page.goto('/system/updates');
    const open = page.locator('[data-upload-open]');
    await expect(open).toHaveText('Upload a bundle');
    await open.click();
    const dialog = page.getByRole('dialog', {name: 'Upload a bundle'});
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('for a system without internet access');
    await expect(dialog.getByRole('button', {name: 'Deploy'})).toBeDisabled();
    await dialog.getByLabel('Firmware bundle').setInputFiles({name: 'HmIP-PDT.tgz', mimeType: 'application/gzip', buffer: Buffer.from('x')});
    await expect(dialog.getByRole('button', {name: 'Deploy'})).toBeEnabled();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
});

test('a system that knows no paired device yet says so', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.devices = [];
    });
    await page.goto('/system/updates');
    await expect(page.locator('[data-fw-none]')).toBeVisible();
    await expect(page.locator('table.fw-types')).toHaveCount(0);
    // the deployed bundles are still there
    await expect(page.locator('[data-fw-others] summary')).toHaveText('Other downloaded firmware · 3');
});

// task 44: the deployed firmware on its way to HmIP devices - the interface's live state and the
// percent from hmipserver's journal: transferring with its bar, interrupted with the last progress,
// queued, ready to install, installing; the summary counts them; a device's own chip in the list
test('a transfer running, interrupted, queued, ready and installing, with the percent and the last progress', async ({page}) => {
    const updated = new Date(Date.now() - 3 * 3600 * 1000).toISOString();
    await patchFirmware(page, (st) => {
        st.devices = st.devices.map((d) => {
            switch (d.address) {
                case '00160000000C01':
                    return {...d, update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'running', percent: 42, bytes: 41806, size: 99540, since: updated, updated}};
                case '00010000000A10':
                    return {...d, available_firmware: '1.18.2', update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'interrupted', percent: 26, bytes: 14310, size: 55600, since: updated, updated, reason: 'unreachable'}};
                case '0001D0000000B1':
                    return {...d, firmware: '2.2.4', available_firmware: '2.2.6', update_state: 'READY_FOR_UPDATE'};
                case '00120000000D01':
                    return {...d, firmware: '1.2.0', available_firmware: '1.4.0', update_state: 'NEW_FIRMWARE_AVAILABLE'};
                default:
                    return d;
            }
        });
        st.deployed = [...st.deployed, {type_code: '310', name: 'HmIP-PDT', version: '2.2.6', files: ['HmIP-PDT_update_V2_2_6.efw', 'info']}, {type_code: '264', name: 'HmIP-SWDO-2', version: '1.4.0', files: ['x.efw']}];
    });
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    expect(await rows(page).evaluateAll((trs) => trs.map((tr) => [tr.getAttribute('data-type'), tr.getAttribute('data-status')]))).toEqual([
        ['HmIPW-DRS8', 'transferring'],
        ['HMIP-WRC2', 'interrupted'],
        ['HmIP-SWDO-2', 'queued'],
        ['HmIP-PDT', 'ready'],
    ]);
    const drs = row(page, 'HmIPW-DRS8');
    await expect(drs.locator('[data-badge]')).toHaveText('Transferring to the device · 42 %');
    await expect(drs.locator('[data-progress]')).toHaveAttribute('data-progress', '42');
    expect(await drs.locator('[data-progress] > div').evaluate((el) => el.style.width)).toBe('42%');
    await expect(drs.locator('[data-last-progress]')).toHaveText('Last progress: 3 h ago');
    const wrc = row(page, 'HMIP-WRC2');
    await expect(wrc.locator('[data-badge]')).toHaveText('Transfer interrupted · 26 %');
    await expect(wrc.locator('[data-last-progress]')).toHaveText('Last progress: 3 h ago · waiting for the device');
    await expect(row(page, 'HmIP-SWDO-2').locator('[data-badge]')).toHaveText('Queued for transfer');
    await expect(row(page, 'HmIP-SWDO-2').locator('[data-progress]')).toHaveCount(0);
    await expect(row(page, 'HmIP-PDT').locator('[data-badge]')).toHaveText('Transferred, ready to install');
    // the summary: three on their way (running, interrupted, queued), one ready to install (the transferred one)
    await expect(page.locator('[data-sum="transferring"]')).toHaveText('3 transferring');
    await expect(page.locator('[data-sum="downloaded"]')).toHaveText('1 ready to install');
    await expect(page.locator('[data-fw-transfer-help]')).toContainText('8 to 42 hours');
    // the devices of a type: each with its own state; the one that is up to date without
    await wrc.locator('[data-expand]').click();
    const devs = wrc.locator('xpath=following-sibling::tr[1]').locator('[data-devices] li');
    await expect(devs).toHaveCount(2);
    await expect(devs.filter({hasText: '00010000000A10'}).locator('[data-device-state]')).toHaveText('Transfer interrupted · 26 %');
    await expect(devs.filter({hasText: '00010000000A11'}).locator('[data-device-state]')).toHaveCount(0);
    expect(await fitsWindow(page)).toBe(true);
    // installing wins over everything, in German too
    await patchFirmware(page, (st) => {
        st.devices = st.devices.map((d) => (d.address === '00160000000C01' ? {...d, update_state: 'PERFORMING_UPDATE', transfer: undefined} : d));
    });
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(row(page, 'HmIPW-DRS8').locator('[data-badge]')).toHaveText('Wird installiert');
    await expect(page.locator('[data-sum="installing"]')).toHaveText('1 wird installiert');
    await expect(row(page, 'HmIPW-DRS8').locator('[data-progress]')).toHaveCount(0);
    await expect(page.locator('[data-fw-transfer-help]')).toHaveCount(0);
});

// the interface's state without the journal (no progress line yet) is transferring without a percent
test('a transfer the journal has not reported yet shows no percent', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.devices = st.devices.map((d) => (d.address === '00160000000C01' ? {...d, update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'running', percent: -1, since: '2026-10-14T13:15:35Z', updated: '2026-10-14T13:15:35Z'}} : d));
    });
    await page.goto('/system/updates');
    await expect(row(page, 'HmIPW-DRS8').locator('[data-badge]')).toHaveText('Transferring to the device');
    await expect(row(page, 'HmIPW-DRS8').locator('[data-progress]')).toHaveCount(0);
    await expect(row(page, 'HmIPW-DRS8').locator('[data-last-progress]')).toContainText('Last progress:');
});

// task 44: the search runs over Other downloaded firmware as well
test('the search covers the other downloaded firmware', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.deployed = [
            ...st.deployed,
            {type_code: '0044', dir: '0044', name: 'HmIP-FROLL', version: '1.0.2', files: ['HmIP-FROLL_update_V1_0_2.efw', 'info']},
            {type_code: '0083', dir: '0083', name: 'HM-ES-PMSw1-Pl', version: '2.6', files: ['HM-ES-PMSw1-Pl_update_V2_6.eq3']},
        ];
    });
    await page.goto('/system/updates');
    // the stub has one bundle no paired type takes already
    const others = page.locator('[data-fw-others]');
    await expect(others.locator('summary')).toContainText('· 3');
    await search(page).fill('froll');
    await expect(rows(page)).toHaveCount(0);
    await expect(others.locator('summary')).toContainText('· 1/3');
    await others.locator('summary').click();
    await expect(others.locator('tbody tr')).toHaveCount(1);
    await expect(others.locator('tbody tr')).toContainText('HmIP-FROLL');
    await search(page).fill('zzz');
    await expect(others.locator('[data-fw-others-nomatch]')).toBeVisible();
    await search(page).fill('');
    await expect(others.locator('tbody tr')).toHaveCount(3);
});

// task 45: the per-type switch holds a firmware back (the bundle moves beside the firmware
// directory, the interfaces stop transferring) and offers it again; the global switch decides where
// a new download lands; the duty cycle shows beside running transfers
test('Transfer this firmware: holding back asks and stops, offering again; Deploy automatically; the duty cycle', async ({page}) => {
    const updated = new Date(Date.now() - 20 * 60 * 1000).toISOString();
    let held = false;
    let transferCalls: {dir: string; body: unknown}[] = [];
    let settingsBodies: unknown[] = [];
    await page.route('**/api/system/v1/firmware/bundles/*/transfer', async (route) => {
        const dir = new URL(route.request().url()).pathname.split('/').at(-2)!;
        const body = route.request().postDataJSON() as {transfer: boolean};
        transferCalls.push({dir, body});
        held = !body.transfer;
        await route.fulfill({json: {ok: true, transfer: body.transfer, bundle: {type_code: '266', dir: '266', name: 'HmIPW-DRS8', version: '1.2.6', held}}});
    });
    await page.route('**/api/system/v1/firmware/settings', async (route) => {
        settingsBodies.push(route.request().postDataJSON());
        await route.fulfill({json: {ok: true, enabled: true, auto_deploy: false}});
    });
    await page.route('**/api/system/v1/radio/health', (route) => route.fulfill({json: {interfaces: [{interface: 'HmIP-RF', duty_cycle: 12.5}, {interface: 'BidCos-RF', duty_cycle: 3}]}}));
    await patchFirmware(page, (st) => {
        st.auto_deploy = !settingsBodies.length;
        st.deployed = st.deployed.map((b) => (b.name === 'HmIPW-DRS8' ? {...b, held} : b));
        st.devices = st.devices.map((d) => (d.address === '00160000000C01' && !held ? {...d, update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'running', percent: 42, since: updated, updated}} : d.address === '00160000000C01' ? {...d, update_state: 'UP_TO_DATE', available_firmware: '0.0.0'} : d));
    });
    await page.goto('/system/updates');
    const drs = row(page, 'HmIPW-DRS8');
    await expect(drs.locator('[data-badge]')).toHaveText('Transferring to the device · 42 %');
    await expect(page.locator('[data-duty-cycle]')).toHaveText('Duty cycle HmIP-RF: 12.5 % · BidCos-RF: 3 %');
    const sw = drs.locator('[data-transfer-switch]');
    await expect(sw).toBeChecked();
    // holding back a running transfer asks first; No leaves everything as it is
    await sw.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Hold this firmware back?');
    await expect(dialog).toContainText('starts the transfer over from the beginning');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(transferCalls).toHaveLength(0);
    await expect(sw).toBeChecked();
    // Yes: the bundle is held, the row says so, the switch is off
    await sw.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Hold back'}).click();
    await expect(drs.locator('[data-badge]')).toHaveText('Held back');
    expect(transferCalls).toEqual([{dir: '4107', body: {transfer: false}}]);
    await expect(sw).not.toBeChecked();
    await expect(page.locator('[data-sum="held"]')).toHaveText('1 held back');
    await expect(page.locator('[data-duty-cycle]')).toHaveCount(0);
    // offered again: no question, the bundle is back
    await sw.click();
    await expect(sw).toBeChecked();
    expect(transferCalls).toHaveLength(2);
    expect(transferCalls[1]).toEqual({dir: '4107', body: {transfer: true}});
    // the global switch
    const auto = page.locator('[data-auto-deploy]');
    await expect(auto).toBeChecked();
    await auto.click();
    await expect(auto).not.toBeChecked();
    expect(settingsBodies).toEqual([{auto_deploy: false}]);
    // a type whose bundle is not on the system has no switch; a non-admin sees none
    await expect(row(page, 'HMIP-WRC2').locator('[data-transfer-switch]')).toHaveCount(0);
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(page.locator('[data-auto-deploy]')).toBeVisible();
    await expect(page.getByText('Automatisch bereitstellen')).toBeVisible();
    await expect(row(page, 'HmIPW-DRS8').locator('.fw-switch')).toContainText('Diese Firmware übertragen');
});
