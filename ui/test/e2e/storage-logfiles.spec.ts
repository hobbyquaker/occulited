import {expect, test} from '@playwright/test';
import {fitsWindow} from './scroll';

// task 84: the hint about the files written beside the journal (D-59) - the largest ones with their
// size, the addon, RAM or the userfs and whether they grew; nothing when there are none; no part of
// the verdict. Since task 111 a small block under the Utilisation panels (the storage health panel
// it was in is gone). The stub plants them by cookie (stub-storage-logs=1).

test('the files beside the journal: path, size, addon, where, growing, and how many more', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-storage-logs', value: '1', url: baseURL!}]);
    await page.goto('/');
    const hint = page.locator('[data-storage-extra] [data-storage-logs]');
    await expect(hint).toBeVisible();
    await expect(hint.locator('.ol-storage-line')).toHaveText('Log files beside the journal');
    const items = hint.locator('li[data-logfile]');
    await expect(items).toHaveCount(3);
    await expect(items.nth(0)).toContainText('/usr/local/addons/hm2mqtt/var/hm2mqtt.log.1 · 1.4 MB · addon hm2mqtt · on the userfs');
    await expect(items.nth(0).locator('[data-growing]')).toHaveCount(0);
    await expect(items.nth(1).locator('[data-growing]')).toHaveText('growing, +3.1 kB');
    await expect(items.nth(2)).toContainText('/var/log/hmserver.log · 188.0 kB · in RAM');
    await expect(hint).toContainText('and 4 more log files');
    // a hint: no warning, no verdict of its own
    await expect(page.locator('[data-notice="storage"]')).toHaveCount(0);
    await expect(page.locator('.ol-storage-panel.warn, .ol-storage-panel.err')).toHaveCount(0);
    // the long npm path wraps: the page does not scroll sideways
    expect(await fitsWindow(page)).toBe(true);
});

test('no files beside the journal: no hint', async ({page}) => {
    await page.goto('/');
    await expect(page.locator('.ol-storage-panel').first()).toBeVisible();
    await expect(page.locator('[data-storage-logs]')).toHaveCount(0);
    await expect(page.locator('[data-storage-extra]')).toHaveCount(0);
});
