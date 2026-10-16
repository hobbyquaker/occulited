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
/** task 51: a row's further lines are in its expanded part - opens the row, answers that block */
async function details(page: Page, type: string): Promise<Locator> {
    const expand = row(page, type).locator('[data-expand]');
    if ((await expand.getAttribute('aria-expanded')) !== 'true') await expand.click();
    return page.locator(`table.fw-types tr.fw-row[data-type="${type}"] + tr.fw-devices-row [data-details]`);
}
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
    // task 51: one line per collapsed row; how many devices are behind is in the badge's tooltip and the expanded part
    await expect(row(page, 'HMIP-WRC2').locator('[data-behind]')).toHaveCount(0);
    await expect(row(page, 'HMIP-WRC2').locator('[data-badge]')).toHaveAttribute('title', /for 1 of 2 devices/);
    await expect((await details(page, 'HMIP-WRC2')).locator('[data-behind]')).toHaveText('for 1 of 2 devices');
    await expect(row(page, 'HMIP-WRC2')).toContainText('2 devices');
    await expect(row(page, 'HmIP-PDT').locator('[data-badge]')).toHaveText('Up to date');
    // the summary counts the statuses; the hidden types are named under the table
    await expect(page.locator('[data-sum="downloaded"]')).toHaveText('1 ready to install');
    await expect(page.locator('[data-sum="available"]')).toHaveText('1 available at eQ-3');
    await expect(page.locator('[data-sum="current"]')).toHaveText('2 up to date');
    await expect(page.locator('[data-fw-hidden]')).toContainText('1 device type that cannot be updated is hidden.');
    // occulited task 60: HmIP devices only - the BidCos-RF, BidCos-Wired and virtual devices are not listed
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
    await expect(rows(page)).toHaveCount(5);
    await expect(row(page, 'HmIP-STHO').locator('[data-badge]')).toHaveText('No firmware at eQ-3');
    // occulited task 60: not even with every device shown
    for (const type of ['HM-CC-TC', 'HMW-IO-12-Sw7-DR', 'HmIP-HEATING']) await expect(row(page, type)).toHaveCount(0);
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
    await expect(page.locator('[data-devices]')).toContainText('Thermometer cellar');
    // by address, in any case
    await search(page).fill('00160000000c01');
    await expect(rows(page)).toHaveCount(1);
    await expect(row(page, 'HmIPW-DRS8')).toBeVisible();
    // a BidCos device is not found: it is not listed (occulited task 60)
    await search(page).fill('JEQ9000001');
    await expect(rows(page)).toHaveCount(0);
    await search(page).fill('');
    await expect(rows(page)).toHaveCount(5);
});

test('a downloaded bundle: the version from the file name, marked; its texts open in the modal', async ({page}, info) => {
    await page.goto('/system/updates');
    const drs8 = row(page, 'HmIPW-DRS8');
    await expect(drs8.locator('[data-version]')).toHaveText('1.2.6');
    await expect(drs8.locator('[data-version]')).toHaveClass(/ol-fromname/);
    await expect(drs8.locator('[data-version]')).toHaveAttribute('title', 'The bundle states no version; this one is read from the name of its update file.');
    const fromName = (await details(page, 'HmIPW-DRS8')).locator('[data-from-name]');
    await expect(fromName).toHaveText('from the file name · 2022-09-28');
    await expect(fromName).toHaveAttribute('title', 'The bundle states no version; this one is read from the name of its update file.');
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
    // task 51: the failed download is an icon in the collapsed row, the text in the expanded part
    await expect(drs8.locator('[data-failed-icon]')).toHaveAttribute('title', 'Last download failed: HTTP 404');
    const more = await details(page, 'HmIPW-DRS8');
    await expect(more.locator('[data-older-download]')).toHaveText('1.2.6 is downloaded');
    await expect(more.locator('[data-failed]')).toHaveText('Last download failed: HTTP 404');
});

// B-225: a device the interface calls not updatable (rfd's UPDATABLE 0) is hidden, and says why
// when all are shown
test('not updatable over the air, in English and German', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.devices = st.devices.map((d) => (d.type === 'HmIP-STHO' ? {...d, not_listed: false, updatable: false} : d));
    });
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    await page.locator('[data-show-all]').check();
    await expect(row(page, 'HmIP-STHO').locator('[data-badge]')).toHaveText('Not updatable over the air');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await page.locator('[data-show-all]').check();
    await expect(row(page, 'HmIP-STHO').locator('[data-badge]')).toHaveText('Nicht über Funk aktualisierbar');
    await expect(page.getByRole('heading', {name: 'Gerätefirmware'})).toBeVisible();
    await expect(page.locator('[data-sum="downloaded"]')).toHaveText('1 bereit zur Installation');
});

// the bundles no paired type takes stay visible, collapsed; a second bundle of a shared TypeCode lies
// in <TypeCode>-<Name>, and its files are read from that directory; a BidCos bundle is read by rfd
test('other downloaded firmware: collapsed, its texts, the directory, the interface', async ({page}) => {
    await patchFirmware(page, (st) => {
        st.deployed = [
            ...st.deployed,
            {type_code: '261', dir: '261-HmIP-BDT', name: 'HmIP-BDT', version: '1.1.0', files: ['HmIP-BDT_update_V1_1_0_171122.eq3', 'changelog.txt', 'info'], info: {}},
            // occulited task 60: a BidCos bundle is not listed here either
            {type_code: '262', dir: '262-HM-LC-Dim1T-DR', name: 'HM-LC-Dim1T-DR', version: '1.1.0', files: ['HM-LC-Dim1T-DR_update_V1_1_0_171122.eq3', 'info'], info: {}},
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
    await expect(others.locator('tr[data-bundle="HM-LC-Dim1T-DR"]')).toHaveCount(0);
    const dim = others.locator('tr[data-bundle="HmIP-BDT"]');
    await expect(dim).toContainText('HmIP-RF');
    await dim.getByRole('button', {name: 'changelog.txt'}).click();
    await expect(page.getByRole('dialog', {name: 'changelog.txt'})).toContainText('dimmer');
    expect(asked).toBe('/api/system/v1/firmware/bundles/261-HmIP-BDT/files/changelog.txt');
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
    expect(await drs.locator('[data-progress] > span').evaluate((el) => el.style.width)).toBe('42%');
    await expect(drs.locator('[data-badge]')).toHaveAttribute('title', /Last progress: 3 h ago/);
    await expect((await details(page, 'HmIPW-DRS8')).locator('[data-last-progress]')).toHaveText('Last progress: 3 h ago');
    const wrc = row(page, 'HMIP-WRC2');
    await expect(wrc.locator('[data-badge]')).toHaveText('Transfer interrupted · 26 %');
    await expect((await details(page, 'HMIP-WRC2')).locator('[data-last-progress]')).toHaveText('Last progress: 3 h ago · waiting for the device');
    await expect(row(page, 'HmIP-SWDO-2').locator('[data-badge]')).toHaveText('Queued for transfer');
    await expect(row(page, 'HmIP-SWDO-2').locator('[data-progress]')).toHaveCount(0);
    await expect(row(page, 'HmIP-PDT').locator('[data-badge]')).toHaveText('Transferred, ready to install');
    // the summary: three on their way (running, interrupted, queued), one ready to install (the transferred one)
    await expect(page.locator('[data-sum="transferring"]')).toHaveText('3 transferring');
    await expect(page.locator('[data-sum="downloaded"]')).toHaveText('1 ready to install');
    await expect(page.locator('[data-fw-transfer-help]')).toContainText('8 to 42 hours');
    // the devices of a type: each with its own state; the one that is up to date without (the row is open since the last progress above)
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
    await expect((await details(page, 'HmIPW-DRS8')).locator('[data-last-progress]')).toContainText('Last progress:');
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
    // the stub's one, HmIP-FROLL; the BidCos bundle is not listed (occulited task 60)
    await expect(others.locator('summary')).toContainText('· 2');
    await search(page).fill('froll');
    await expect(rows(page)).toHaveCount(0);
    await expect(others.locator('summary')).toContainText('· 1/2');
    await others.locator('summary').click();
    await expect(others.locator('tbody tr')).toHaveCount(1);
    await expect(others.locator('tbody tr')).toContainText('HmIP-FROLL');
    await search(page).fill('zzz');
    await expect(others.locator('[data-fw-others-nomatch]')).toBeVisible();
    await search(page).fill('');
    await expect(others.locator('tbody tr')).toHaveCount(2);
});

// task 45: holding a firmware back (the bundle moves beside the firmware directory, the
// interfaces stop transferring); task 48: downloaded, not delivered - a held firmware reaches the
// devices only through Deliver to devices, which asks first; Deploy automatically is off by
// default; the duty cycle shows beside running transfers
test('Hold back and Deliver to devices ask first; Deploy automatically off by default; the duty cycle', async ({page}) => {
    const updated = new Date(Date.now() - 20 * 60 * 1000).toISOString();
    let held = false;
    const transferCalls: {dir: string; body: unknown}[] = [];
    const settingsBodies: unknown[] = [];
    await page.route('**/api/system/v1/firmware/bundles/*/transfer', async (route) => {
        const dir = decodeURIComponent(new URL(route.request().url()).pathname.split('/').at(-2)!);
        const body = route.request().postDataJSON() as {transfer: boolean};
        transferCalls.push({dir, body});
        if (dir === '4107' || dir === '4107-HmIPW-DRS8-1.2.6') held = !body.transfer;
        await route.fulfill({json: {ok: true, transfer: body.transfer, bundle: {type_code: '4107', dir: held ? '4107-HmIPW-DRS8-1.2.6' : '4107', name: 'HmIPW-DRS8', version: '1.2.6', held}}});
    });
    await page.route('**/api/system/v1/firmware/settings', async (route) => {
        settingsBodies.push(route.request().postDataJSON());
        await route.fulfill({json: {ok: true, enabled: true, auto_deploy: true}});
    });
    await page.route('**/api/system/v1/radio/health', (route) => route.fulfill({json: {interfaces: [{interface: 'HmIP-RF', duty_cycle: 12.5}, {interface: 'BidCos-RF', duty_cycle: 3}]}}));
    await patchFirmware(page, (st) => {
        (st as unknown as {auto_deploy: boolean}).auto_deploy = settingsBodies.length > 0;
        st.deployed = st.deployed.map((b) => (b.name === 'HmIPW-DRS8' ? {...b, held, dir: held ? '4107-HmIPW-DRS8-1.2.6' : '4107'} : b));
        // a second HmIP type with a held download; a BidCos type with one is not listed (occulited task 60)
        st.devices.push({interface: 'HmIP-RF', address: '001A0000000A01', type: 'HmIP-WRC6', firmware: '1.0.12', latest: '1.0.18', update_available: true});
        st.deployed.push({type_code: '403', dir: '403-HmIP-WRC6-1.0.18', name: 'HmIP-WRC6', version: '1.0.18', held: true, files: ['HmIP-WRC6_update_V1_0_18.efw', 'info']});
        st.devices.push({interface: 'BidCos-RF', address: 'NEQ1234567', type: 'HM-LC-Sw1-Pl-DN-R1', firmware: '1.9', latest: '2.8', update_available: true});
        st.deployed.push({type_code: '216', dir: '216-HM-LC-Sw1-Pl-DN-R1-2.8', name: 'HM-LC-Sw1-Pl-DN-R1', version: '2.8', held: true, files: ['hm_lc_sw1_pl_dn_r1_update_V2_8.eq3', 'info']});
        st.devices = st.devices.map((d) => (d.address === '00160000000C01' && !held ? {...d, update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'running', percent: 42, since: updated, updated}} : d.address === '00160000000C01' ? {...d, update_state: 'UP_TO_DATE', available_firmware: '0.0.0'} : d));
    });
    await page.goto('/system/updates');
    const drs = row(page, 'HmIPW-DRS8');
    await expect(drs.locator('[data-badge]')).toHaveText('Transferring to the device · 42 %');
    // the transfer is HmIP's: its duty cycle alone (occulited task 60)
    await expect(page.locator('[data-duty-cycle]')).toHaveText('Duty cycle HmIP-RF: 12.5 %');
    await expect(drs.locator('[data-deliver]')).toHaveCount(0);
    // holding back a running transfer asks first; Cancel leaves everything as it is
    const hold = drs.locator('[data-hold-back]');
    await hold.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Hold this firmware back?');
    await expect(dialog).toContainText('starts over from the beginning');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(transferCalls).toHaveLength(0);
    // Hold back: the bundle is held, the row says so and offers the delivery
    await hold.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Hold back'}).click();
    await expect(drs.locator('[data-badge]')).toHaveText('Downloaded, not delivered');
    expect(transferCalls).toEqual([{dir: '4107', body: {transfer: false}}]);
    await expect(page.locator('[data-sum="held"]')).toHaveText('2 not delivered');
    await expect(page.locator('[data-fw-held-note]')).toHaveText('Other apps show firmware that is not delivered as up to date.');
    await expect(page.locator('[data-duty-cycle]')).toHaveCount(0);
    // Deliver to devices asks what it starts: for HmIP the background transfer of hours
    const deliver = drs.locator('[data-deliver]');
    await deliver.click();
    await expect(page.getByRole('dialog')).toContainText('Deliver 1.2.6 to the devices?');
    await expect(page.getByRole('dialog')).toContainText('This can take hours');
    await page.getByRole('dialog').getByRole('button', {name: 'Cancel'}).click();
    expect(transferCalls).toHaveLength(1);
    await deliver.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Deliver', exact: true}).click();
    await expect(drs.locator('[data-hold-back]')).toBeVisible();
    expect(transferCalls[1]).toEqual({dir: '4107-HmIPW-DRS8-1.2.6', body: {transfer: true}});
    // the BidCos type with its held download is not listed (occulited task 60)
    await expect(row(page, 'HM-LC-Sw1-Pl-DN-R1')).toHaveCount(0);
    await expect(row(page, 'HmIP-WRC6').locator('[data-badge]')).toHaveText('Downloaded, not delivered');
    // the global switch: off by default
    const auto = page.locator('[data-auto-deploy]');
    await expect(auto).not.toBeChecked();
    await auto.click();
    await expect(auto).toBeChecked();
    expect(settingsBodies).toEqual([{auto_deploy: true}]);
    // a type whose bundle is not on the system has neither action
    await expect(row(page, 'HMIP-WRC2').locator('[data-deliver], [data-hold-back]')).toHaveCount(0);
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(page.getByText('Automatisch bereitstellen')).toBeVisible();
    await expect(row(page, 'HmIP-WRC6').locator('[data-deliver]')).toHaveText('An die Geräte ausliefern');
    await expect(row(page, 'HmIP-WRC6').locator('[data-badge]')).toHaveText('Heruntergeladen, nicht ausgeliefert');
});

// task 49: several versions per type - the Versions dialog lists what is on the system and the older
// ones of the OpenCCU firmware archive; a download lands held, a delivery of a version older than
// the devices run warns with the risks; BidCos types and access points have no Versions
test('Versions: the archive, a download, an older version delivered only after the warning', async ({page}) => {
    let fetched = false;
    const fetches: unknown[] = [];
    const transfers: {dir: string; body: unknown}[] = [];
    await page.route('**/api/system/v1/firmware/archive?*', (route) => route.fulfill({json: {type: 'HmIP-PDT', source: 'https://github.com/OpenCCU/HMDeviceFirmware', versions: [
        {type: 'HmIP-PDT', version: '2.2.4', date: '2023-11-23', path: 'HmIP/HmIP-PDT_update_V2_2_4_231123.tgz', size: 98883, on_system: true},
        {type: 'HmIP-PDT', version: '1.4.8', date: '2017-07-25', path: 'HmIP/HmIP-PDT_update_V1_4_8_170725.tgz', size: 100801, on_system: fetched},
    ]}}));
    await page.route('**/api/system/v1/firmware/archive/fetch', async (route) => {
        fetches.push(route.request().postDataJSON());
        fetched = true;
        await route.fulfill({json: {ok: true, bundle: {type_code: '310', dir: '310-HmIP-PDT-1.4.8', name: 'HmIP-PDT', version: '1.4.8', held: true, files: []}}});
    });
    await page.route('**/api/system/v1/firmware/bundles/*/transfer', async (route) => {
        transfers.push({dir: decodeURIComponent(new URL(route.request().url()).pathname.split('/').at(-2)!), body: route.request().postDataJSON()});
        await route.fulfill({json: {ok: true, transfer: true, bundle: {}}});
    });
    await patchFirmware(page, (st) => {
        st.deployed = st.deployed.map((b) => (b.name === 'HmIP-PDT' ? {...b, dir: '310'} : b));
        if (fetched) st.deployed.push({type_code: '310', dir: '310-HmIP-PDT-1.4.8', name: 'HmIP-PDT', version: '1.4.8', held: true, files: ['info']});
    });
    await page.goto('/system/updates');
    await page.locator('[data-show-all]').check();
    // BidCos and BidCos-Wired types have no Versions
    await expect(row(page, 'HM-CC-TC').locator('[data-versions]')).toHaveCount(0);
    await row(page, 'HmIP-PDT').locator('[data-versions]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Firmware versions');
    await expect(dialog).toContainText('Which version the devices of this type receive. The newest is the default.');
    await expect(dialog.locator('[data-versions-system] tr')).toHaveCount(1);
    await expect(dialog.locator('[data-versions-system] tr').first()).toContainText('2.2.4');
    await expect(dialog.locator('[data-versions-system] tr').first()).toContainText('Delivered');
    const older = dialog.locator('[data-archive-version="1.4.8"]');
    await expect(older).toContainText('2017-07-25');
    await expect(dialog.locator('[data-archive-version="2.2.4"]')).toContainText('On the system');
    await older.locator('[data-fetch]').click();
    await expect(dialog.locator('[data-versions-system] tr')).toHaveCount(2);
    expect(fetches).toEqual([{path: 'HmIP/HmIP-PDT_update_V1_4_8_170725.tgz'}]);
    await expect(older).toContainText('On the system');
    // delivering 1.4.8 to a device on 2.2.4: the warning names the risks; Cancel sends nothing
    await dialog.locator('[data-versions-system] tr[data-version="1.4.8"] [data-deliver-version]').click();
    const warn = page.getByRole('dialog').filter({hasText: 'Deliver the older version 1.4.8?'});
    await expect(warn).toContainText('eQ-3 does not support going back');
    await warn.getByRole('button', {name: 'Cancel'}).click();
    expect(transfers).toHaveLength(0);
    await dialog.locator('[data-versions-system] tr[data-version="1.4.8"] [data-deliver-version]').click();
    await page.getByRole('dialog').filter({hasText: 'Deliver the older version 1.4.8?'}).getByRole('button', {name: 'Deliver older version'}).click();
    await expect.poll(() => transfers).toEqual([{dir: '310-HmIP-PDT-1.4.8', body: {transfer: true}}]);
});

test('Versions: the archive not reachable says so; German', async ({page}) => {
    await page.route('**/api/system/v1/firmware/archive?*', (route) => route.fulfill({status: 502, json: {error: 'archive-failed', message: 'firmware archive: HTTP 403'}}));
    await page.goto('/system/updates');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await row(page, 'HmIP-PDT').locator('[data-versions]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Firmware-Versionen');
    await expect(dialog).toContainText('Ältere Versionen');
    await expect(dialog.locator('[data-archive-error]')).toContainText('HTTP 403');
});

// task 51 (the maintainer, 2026-10-15: "the right column with the buttons better aligned, and it should
// not break on collapsed rows"): many types in mixed states - transferring and interrupted with a
// percent, queued, ready, downloaded, held for 1 of 2 devices, available, up to date, with and without
// the text icons - their version, badge, icons and Versions at one x each, every collapsed row one line
async function mixedFirmware(page: Page) {
    const updated = new Date(Date.now() - 3 * 3600 * 1000).toISOString();
    await page.route('**/api/system/v1/firmware', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const st = (await response.json()) as Firmware;
        st.devices = st.devices.map((d) => {
            switch (d.address) {
                case '0001D0000000B1':
                    return {...d, firmware: '2.2.4', available_firmware: '2.2.6', update_state: 'READY_FOR_UPDATE'};
                case '00120000000D01':
                    return {...d, firmware: '1.2.0', available_firmware: '1.4.0', update_state: 'NEW_FIRMWARE_AVAILABLE'};
                default:
                    return d;
            }
        });
        st.devices.push(
            {interface: 'HmIP-RF', address: '00180000000E01', type: 'HmIP-WTH-2', firmware: '1.6.2', available_firmware: '1.8.6', latest: '1.8.6', update_available: true, name: 'Thermostat living room', update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'running', percent: 63, since: updated, updated}},
            {interface: 'HmIP-RF', address: '00180000000E02', type: 'HmIP-WTH-2', firmware: '1.8.6', available_firmware: '1.8.6', latest: '1.8.6', update_available: false, name: 'Thermostat bath'},
            {interface: 'HmIP-RF', address: '00190000000F01', type: 'HmIP-BWTH', firmware: '1.4.6', available_firmware: '1.4.8', latest: '1.4.8', update_available: true, update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: {state: 'interrupted', percent: 26, since: updated, updated, reason: 'unreachable'}},
            {interface: 'BidCos-RF', address: 'KEQ9000003', type: 'HM-TC-IT-WM-W-EU', firmware: '1.4', latest: '1.5', update_available: true, name: 'Wall thermostat office'},
            {interface: 'BidCos-RF', address: 'KEQ9000006', type: 'HM-TC-IT-WM-W-EU', firmware: '1.5', latest: '1.5', update_available: false},
            {interface: 'HmIP-RF', address: '001A0000000A01', type: 'HmIP-WRC6', firmware: '1.0.12', available_firmware: '0.0.0', latest: '1.0.18', update_available: true, name: 'Remote bedroom'},
            {interface: 'HmIP-RF', address: '001A0000000A02', type: 'HmIP-WRC6', firmware: '1.0.18', available_firmware: '0.0.0', latest: '1.0.18', update_available: false},
            {interface: 'HmIP-RF', address: '001B0000000A01', type: 'HmIP-SMI', firmware: '1.4.0', available_firmware: '0.0.0', latest: '1.4.0', update_available: false},
        );
        st.deployed = [
            ...st.deployed,
            {type_code: '310', name: 'HmIP-PDT', version: '2.2.6', files: ['HmIP-PDT_update_V2_2_6.efw', 'info']},
            {type_code: '264', name: 'HmIP-SWDO-2', version: '1.4.0', files: ['x.efw']},
            {type_code: '400', name: 'HmIP-WTH-2', version: '1.8.6', files: ['HmIP-WTH-2_update_V1_8_6.efw', 'changelog.txt', 'info']},
            {type_code: '401', name: 'HmIP-BWTH', version: '1.4.8', files: ['HmIP-BWTH_update_V1_4_8.efw', 'info']},
            {type_code: '402', dir: '402-HM-TC-IT-WM-W-EU-1.5', name: 'HM-TC-IT-WM-W-EU', version: '1.5', held: true, files: ['hm_tc_it_wm_w_eu_update_V1_5.eq3', 'changelog.txt', 'info']},
            {type_code: '403', dir: '403-HmIP-WRC6-1.0.18', name: 'HmIP-WRC6', version: '1.0.18', held: true, files: ['HmIP-WRC6_update_V1_0_18.efw', 'info']},
        ];
        await route.fulfill({response, json: st});
    });
}

for (const [width, oneLine] of [[1440, true], [1024, false]] as const) {
    test(`the status column's tracks line up at ${width} px${oneLine ? ', a collapsed row is one line' : ''}`, async ({page}, info) => {
        test.skip(info.project.name === 'phone', 'the phone stacks the cells');
        await page.setViewportSize({width, height: 1000});
        await mixedFirmware(page);
        await page.goto('/system/updates');
        // nine types, the BidCos thermostat not listed (occulited task 60)
        await expect(rows(page)).toHaveCount(8);
        const lefts = (sel: string) => rows(page).locator(sel).evaluateAll((els) => [...new Set(els.map((e) => Math.round(e.getBoundingClientRect().left)))]);
        const rights = (sel: string) => rows(page).locator(sel).evaluateAll((els) => [...new Set(els.map((e) => Math.round(e.getBoundingClientRect().right)))]);
        expect(await lefts('[data-version]')).toHaveLength(1);
        expect(await lefts('[data-badge]')).toHaveLength(1);
        expect(await lefts('.fw-texts')).toHaveLength(1);
        expect(await rights('.fw-vslot')).toHaveLength(1);
        // Deliver to devices at one x in every row that has it
        expect(await rights('[data-deliver]')).toHaveLength(1);
        // the transfer's bar sits inside its badge
        const bar = await row(page, 'HmIP-WTH-2').locator('[data-progress]').boundingBox();
        const badge = await row(page, 'HmIP-WTH-2').locator('[data-badge]').boundingBox();
        expect(bar!.y + bar!.height).toBeLessThanOrEqual(badge!.y + badge!.height + 0.5);
        expect(bar!.y).toBeGreaterThanOrEqual(badge!.y);
        if (oneLine) {
            const heights = await rows(page).evaluateAll((trs) => [...new Set(trs.map((tr) => Math.round(tr.getBoundingClientRect().height)))]);
            expect(heights).toHaveLength(1);
            // the version on the line of the other cells
            const mid = (sel: string) => row(page, 'HmIP-WRC6').locator(sel).evaluate((e) => { const r = e.getBoundingClientRect(); return Math.round(r.top + r.height / 2); });
            expect(Math.abs((await mid('[data-version]')) - (await mid('[data-fw-installed]')))).toBeLessThanOrEqual(1);
        }
        expect(await fitsWindow(page)).toBe(true);
    });
}
