import {expect, test} from './fixtures';
import {pageWidth} from './scroll';

// occulited task 78 (the maintainer): one storage panel per disk - the card, the VM's disk, every
// USB stick - with a ring per partition and a legend in words and figures, the device's size in the
// head, in decimal units (1 GB = 10^9) in this panel alone

test('a Pi card and a USB stick: two panels, rings and legend per partition', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-storage', value: 'sd', url: baseURL!}]);
    await page.goto('/');
    const panels = page.locator('.ol-gauges .ol-storage-panel');
    await expect(panels).toHaveCount(2);
    // the old per-mount gauges are gone
    await expect(page.locator('.ol-gauge', {hasText: '/usr/local'})).toHaveCount(0);
    const sd = page.locator('[data-storage-device="mmcblk0"]');
    await expect(sd.locator('.ol-card-title')).toHaveText('SD card');
    await expect(sd.locator('.ol-card-sub')).toHaveText('mmcblk0');
    // 15 931 539 456 bytes: 16 GB, not 14.8
    await expect(sd.locator('[data-device-size]')).toHaveText('16 GB');
    // the rings outermost first: the userfs, the system, the boot partition
    await expect(sd.locator('circle.arc')).toHaveCount(3);
    expect(await sd.locator('circle.arc').evaluateAll((cs) => cs.map((c) => c.getAttribute('data-ring')))).toEqual(['/usr/local', '/', '/boot']);
    const radii = await sd.locator('circle.arc').evaluateAll((cs) => cs.map((c) => Number(c.getAttribute('r'))));
    expect(radii[0]! > radii[1]! && radii[1]! > radii[2]!).toBe(true);
    // each ring its own hue
    const strokes = await sd.locator('circle.arc').evaluateAll((cs) => cs.map((c) => getComputedStyle(c).stroke));
    expect(new Set(strokes).size).toBe(3);
    // the legend in words and decimal figures
    const row = (m: string) => sd.locator(`li[data-partition="${m}"]`);
    await expect(row('/usr/local')).toContainText('Data');
    await expect(row('/usr/local').locator('[data-figures]')).toHaveText('2.3 of 13 GB');
    await expect(row('/usr/local').locator('[data-pct]')).toHaveText('17 %');
    await expect(row('/').locator('[data-figures]')).toHaveText('1.6 of 2.0 GB');
    await expect(row('/boot')).toContainText('Boot');
    await expect(row('/boot').locator('[data-figures]')).toHaveText('68 of 267 MB');
    // the stick: its name as it reports it, its label, its one mounted partition
    const usb = page.locator('[data-storage-device="sdb"]');
    await expect(usb.locator('.ol-card-title')).toHaveText('SanDisk Ultra Fit');
    await expect(usb.locator('.ol-card-sub')).toHaveText('sdb · BACKUP');
    await expect(usb.locator('[data-device-size]')).toHaveText('31 GB');
    await expect(usb.locator('circle.arc')).toHaveCount(1);
    await expect(usb.locator('li[data-partition="/media/usb1"] [data-figures]')).toHaveText('4.1 of 15 GB');
    // a read-only system at 79 % is no warning
    await expect(sd).not.toHaveClass(/\b(warn|err)\b/);
});

test('the OVA disk by default, and the mount gauges for a daemon without the panels', async ({page, context, baseURL}) => {
    await page.goto('/');
    await expect(page.locator('[data-storage-device="sda"] .ol-card-title')).toHaveText('Disk');
    await expect(page.locator('[data-storage-device="sda"] [data-device-size]')).toHaveText('34 GB');
    await context.addCookies([{name: 'stub-storage', value: 'none', url: baseURL!}]);
    await page.goto('/');
    await expect(page.locator('.ol-storage-panel')).toHaveCount(0);
    await expect(page.locator('.ol-gauge', {hasText: '/usr/local'})).toHaveCount(1);
});

test('a full userfs colours the panel and says so; the figures keep their box', async ({page, context, baseURL}) => {
    await page.goto('/');
    const pct = page.locator('[data-storage-device="sda"] li[data-partition="/usr/local"] [data-pct]');
    await expect(pct).toHaveText('4 %');
    const before = (await pct.boundingBox())!;
    await context.addCookies([{name: 'stub-storage', value: 'full', url: baseURL!}]);
    await page.goto('/');
    const sda = page.locator('[data-storage-device="sda"]');
    await expect(sda).toHaveClass(/\berr\b/);
    await expect(sda).toContainText('nearly full');
    await expect(pct).toHaveText('95 %');
    const after = (await pct.boundingBox())!;
    expect(after.width).toBe(before.width);
});

test('in German, on a phone, in both themes', async ({page, context, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await context.addCookies([{name: 'stub-storage', value: 'sd', url: baseURL!}]);
    for (const scheme of ['light', 'dark'] as const) {
        await page.emulateMedia({colorScheme: scheme});
        await page.setViewportSize({width: 360, height: 780});
        await page.goto('/');
        const sd = page.locator('[data-storage-device="mmcblk0"]');
        await expect(sd.locator('.ol-card-title')).toHaveText('SD-Karte');
        await expect(sd.locator('li[data-partition="/usr/local"]')).toContainText('Daten');
        await expect(sd.locator('li[data-partition="/usr/local"] [data-figures]')).toHaveText('2,3 von 13 GB');
        await expect(sd.locator('li[data-partition="/"] [data-figures]')).toHaveText('1,6 von 2,0 GB');
        expect(await pageWidth(page)).toBeLessThanOrEqual(360);
    }
});
