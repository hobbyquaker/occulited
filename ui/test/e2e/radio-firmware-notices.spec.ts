import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// openccu-lite task 137 (D-89: the boot never flashes the radio coprocessor): a newer firmware on the
// system is a notice on Status; an HmIP-RFUSB the detection found but could not read is a warning on
// Status and on Interfaces, and the radio firmware section offers its flash. Nothing flashes on its own.

const newer = {id: 'radio-firmware', variant: 'RPI-RF-MOD@4.4.22', severity: 'warning', href: '/system/updates#radio-firmware', params: {device: 'RPI-RF-MOD', running: '4.2.14', newest: '4.4.22'}};
const adapter = {id: 'radio-firmware', variant: 'HM-CFG-USB-2@0.967', severity: 'warning', href: '/system/updates#radio-firmware', params: {device: 'HM-CFG-USB-2', running: '0.956', newest: '0.967', drops_off: true}};
const unusable = {id: 'radio-module-unusable', variant: '/dev/raw-uart1', severity: 'warning', href: '/system/updates#radio-firmware', params: {device: 'HMIP-RFUSB', node: '/dev/raw-uart1', newest: '4.4.18'}};

async function plant(page: Page, list: object[]) {
    await page.route('**/api/system/v1/warnings', (route) => route.fulfill({json: {warnings: list, periods: [1, 7, 90]}}));
}

async function unusableStick(page: Page) {
    await page.route('**/api/system/v1/radio/firmware', async (route) => {
        const res = await route.fetch();
        const r = await res.json();
        r.modules.push({protocols: [], device: 'HMIP-RFUSB', device_node: '/dev/raw-uart1', device_type: 'eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3', family: 'hmip', dir: 'HmIP-RFUSB', running_version: '', files: [{name: 'dualcopro_update_blhmip-4.4.18.eq3', path: '/firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3', source: 'shipped', version: '4.4.18', size: 104616, sha256: 'ab'.repeat(32), direction: 'unknown'}], newest: 'dualcopro_update_blhmip-4.4.18.eq3', verdict: 'unusable', note: 'the radio detection found the stick but could not read its firmware; it carries no radio until it is flashed', flashable: true});
        await route.fulfill({response: res, json: r});
    });
}

test('Status: a newer firmware is a notice with the versions; the adapter below 0.967 is told to flash; an unusable stick is a warning', async ({page}) => {
    await plant(page, [newer, adapter, unusable]);
    await page.goto('/');
    const notices = page.locator('[data-warnings]');
    await expect(notices.locator('[data-notice="radio-firmware"]').first()).toContainText('A newer radio firmware is on the system for the RPI-RF-MOD: it runs 4.2.14, 4.4.22 is available. Nothing is flashed on its own');
    // task 50: short - the USB-drop reason is gone
    await expect(notices.locator('[data-notice="radio-firmware"]').nth(1)).toContainText('is on the system: flash it in the radio firmware section.');
    await expect(notices.locator('[data-notice="radio-firmware"]').nth(1)).not.toContainText('USB bus');
    await expect(notices.locator('[data-notice="radio-module-unusable"]')).toContainText('The radio module HMIP-RFUSB at /dev/raw-uart1 does not answer with a usable firmware');
    await notices.locator('[data-notice="radio-module-unusable"]').getByRole('link', {name: 'Radio firmware'}).click();
    await expect(page).toHaveURL(/\/system\/updates#radio-firmware$/);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
    expect(overflow).toBe(false);
});

test('Interfaces names the unusable stick; the radio firmware section offers its flash', async ({page}) => {
    await unusableStick(page);
    await page.goto('/system/interfaces');
    const n = page.locator('[data-module-unusable="/dev/raw-uart1"]');
    await expect(n).toContainText('does not answer with a usable firmware');
    await n.getByRole('link', {name: 'Radio firmware'}).click();
    await expect(page).toHaveURL(/\/system\/updates#radio-firmware$/);
    const section = page.locator('[data-panel="radio-firmware"]');
    await expect(section.locator('[data-verdict="unusable"]')).toHaveText('no usable firmware: the system has no radio on it until it is flashed');
    await expect(section.locator('[data-upload="/dev/raw-uart1"]')).toHaveCount(1);
});

test('German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await plant(page, [newer, unusable]);
    await page.goto('/');
    await expect(page.locator('[data-warnings] [data-notice="radio-firmware"]')).toContainText('liegt eine neuere Funk-Firmware auf dem System: Es läuft 4.2.14, 4.4.22 ist verfügbar');
    await expect(page.locator('[data-warnings] [data-notice="radio-module-unusable"]')).toContainText('antwortet nicht mit einer nutzbaren Firmware');
});
