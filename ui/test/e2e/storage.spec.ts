import {type BrowserContext, type Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {pageWidth, scrollTo} from './scroll';

// task 69, reshaped by task 111 (the maintainer's variant B): each disk's health in its own usage
// panel - a verdict pill, one row of chips (only what the disk reports), its reasons in one line,
// the full facts behind a shared "Details" - and no separate storage health panel any more. The
// stub's health variants (stub-storage) bring the usage panels of their own disks.

async function open(page: Page, context: BrowserContext, baseURL: string | undefined, variant?: string) {
    if (variant) await context.addCookies([{name: 'stub-storage', value: variant, url: baseURL!}]);
    await page.goto('/');
    await expect(page.locator('.ol-gauges')).toBeVisible();
}
const panel = (page: Page, name: string) => page.locator(`.ol-storage-panel[data-storage-device="${name}"]`);
const chips = (page: Page, name: string) => panel(page, name).locator('[data-chip]');

test('the old storage health panel is gone; a Pi card shows its write load alone and no wear or SMART', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'good');
    await expect(page.locator('[data-panel="storage"]')).toHaveCount(0);
    await expect(page.getByRole('heading', {name: 'Storage health'})).toHaveCount(0);
    const sd = panel(page, 'mmcblk0');
    await expect(sd.locator('[data-pill]')).toHaveText('good');
    await expect(sd).not.toHaveClass(/\b(warn|err)\b/);
    // not reported means not shown: no wear chip, no SMART chip, no "not available" text
    expect(await chips(page, 'mmcblk0').evaluateAll((cs) => cs.map((c) => c.getAttribute('data-chip')))).toEqual(['writes']);
    await expect(chips(page, 'mmcblk0')).toHaveText(['Writes 8.2 MB/day']);
    await expect(sd).not.toContainText('not available');
    await expect(sd.locator('[data-reasons]')).toHaveCount(0);
    // Details: the facts that exist for a card, and none of the missing ones
    await expect(sd.locator('[data-details]')).toHaveCount(0);
    await sd.locator('[data-details-toggle]').click();
    const d = sd.locator('[data-details]');
    await expect(d.locator('[data-fact="made"]')).toHaveText('06/2024 (2 years old)');
    await expect(d.locator('[data-fact="wear"], [data-fact="smart"]')).toHaveCount(0);
    await expect(d.locator('[data-fact="errors"]')).toContainText('none in the kernel log since this boot');
    await expect(d.locator('[data-fact="writes"]')).toContainText('8.2 MB a day (over the last 14 days)');
    await expect(d.locator('[data-fact="writes"]')).toContainText('/usr/local: 50.8 GB written since the file system was made');
    await expect(page.locator('[data-notice="storage"]')).toHaveCount(0);
});

test('replace: pill, reasons in one line, red edge and the failing chips; the worn NVMe without a mount gets its own panel', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'replace');
    const sata = panel(page, 'sda');
    await expect(sata.locator('[data-pill]')).toHaveText('replace');
    await expect(sata).toHaveClass(/\berr\b/);
    // the reasons in one line, without the disk's name in front, and how many more there are
    await expect(sata.locator('[data-reasons]')).toHaveAttribute('data-reasons', '2');
    await expect(sata.locator('.sp-reason')).toHaveText("The drive's own health check (SMART) failed.");
    await expect(sata.locator('.sp-more')).toHaveText('+1 more');
    await expect(chips(page, 'sda')).toHaveText(['Wear 88 %', 'SMART failed', 'Writes 95.0 MB/day', '7 errors']);
    await expect(sata.locator('[data-chip="smart"]')).toHaveClass(/\berr\b/);
    await expect(sata.locator('[data-chip="errors"]')).toHaveAttribute('title', /I\/O error, dev sda/);
    await expect(sata.locator('[data-chip="wear"]')).toHaveClass(/\bwarn\b/);
    // the full list and facts behind Details
    await sata.locator('[data-details-toggle]').click();
    await expect(sata.locator('[data-reasons-all] li')).toHaveText(["The drive's own health check (SMART) failed.", '7 storage errors in the kernel log since this boot.']);
    await expect(sata.locator('[data-fact="smart"] .sp-bad')).toHaveText('failed');
    await expect(sata.locator('[data-fact="smart"]')).toContainText('1520 reallocated sectors · 16 pending sectors');
    await expect(sata.locator('[data-fact="errors"] .sp-bad')).toHaveText('7 storage errors in the kernel log since this boot');
    const nvme = panel(page, 'nvme0n1');
    await expect(nvme.locator('[data-unmounted]')).toHaveText('not mounted');
    await expect(nvme.locator('[data-pill]')).toHaveText('watch');
    await expect(nvme).toHaveClass(/\bwarn\b/);
    await expect(nvme.locator('.sp-reason')).toHaveText('83 % of the rated life is used.');
    await expect(page.locator('[data-notice="storage"] .ol-notice-text')).toHaveText(/^A storage device should be replaced soon\. /);
});

test('watch: the eMMC wears "up to 20 %", the warning button lands on its panel', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'watch');
    const emmc = panel(page, 'mmcblk1');
    await expect(emmc.locator('[data-pill]')).toHaveText('watch');
    await expect(emmc).toHaveClass(/\bwarn\b/);
    await expect(chips(page, 'mmcblk1')).toHaveText(['Wear up to 20 %', 'Writes 14.0 MB/day']);
    const ssd = panel(page, 'sda');
    await expect(ssd.locator('[data-pill]')).toHaveText('good');
    await expect(chips(page, 'sda')).toHaveText(['Wear 3 %', 'SMART ok', 'Writes 2.4 MB/day']);
    await expect(ssd).not.toHaveClass(/\b(warn|err)\b/);
    // the anchor is the affected disk's panel
    await expect(page.locator('#storage')).toHaveAttribute('data-storage-device', 'mmcblk1');
    const notice = page.locator('[data-notice="storage"]');
    await expect(notice.locator('.ol-notice-text')).toHaveText('A storage device should be watched. eMMC DG4016: the eMMC has used 80 % of its reserve blocks. eMMC DG4016: the file system on /usr/local has recorded 2 errors.');
    await scrollTo(page, 0);
    await notice.getByRole('link', {name: 'Storage health', exact: true}).click();
    await expect(page).toHaveURL(/\/#storage$/);
    await expect(emmc).toBeInViewport();
    await emmc.locator('[data-details-toggle]').click();
    await expect(emmc.locator('[data-cells="cell type A"] .sp-emmc-range')).toHaveText('10–20 %');
    await expect(emmc.locator('[data-cells="cell type B"] .sp-emmc-range')).toHaveText('0–10 %');
    await expect(emmc.locator('[data-fact="eol"]')).toHaveText('80 % used');
    await expect(emmc.locator('[data-fs="mmcblk1p3"]')).toContainText('/usr/local: 2 file system errors recorded');
});

test('Details open and close in every disk panel at once, and the browser remembers it', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'watch');
    const toggles = page.locator('.ol-storage-panel [data-details-toggle]');
    await expect(toggles).toHaveCount(2);
    await expect(page.locator('.ol-storage-panel [data-details]')).toHaveCount(0);
    await toggles.nth(1).click();
    await expect(page.locator('.ol-storage-panel [data-details]')).toHaveCount(2);
    await expect(toggles.nth(0)).toHaveAttribute('aria-expanded', 'true');
    await page.reload();
    await expect(page.locator('.ol-storage-panel [data-details]')).toHaveCount(2);
    await toggles.nth(0).click();
    await expect(page.locator('.ol-storage-panel [data-details]')).toHaveCount(0);
    await expect(toggles.nth(1)).toHaveAttribute('aria-expanded', 'false');
    await page.reload();
    await expect(page.locator('.ol-storage-panel')).toHaveCount(2);
    await expect(page.locator('.ol-storage-panel [data-details]')).toHaveCount(0);
});

// the maintainer: closed, a disk panel is as high as the other panels of Utilisation
for (const [label, width] of [['desktop', 1280], ['phone', 390]] as const) {
    test(`closed, the disk panels are as high as the gauges (${label}); open, they may grow`, async ({page, context, baseURL}) => {
        await page.setViewportSize({width, height: 900});
        for (const variant of ['replace', 'watch', 'good', 'old-sd']) {
            await context.clearCookies();
            await open(page, context, baseURL, variant);
            await expect(page.locator('.ol-storage-panel [data-chip]').first()).toBeVisible();
            const gauge = (await page.locator('.ol-gauges > .ol-gauge').first().boundingBox())!;
            for (const p of await page.locator('.ol-storage-panel').all()) {
                const box = (await p.boundingBox())!;
                expect(Math.abs(box.height - gauge.height), `${variant} ${await p.getAttribute('data-storage-device')}`).toBeLessThanOrEqual(1);
                // nothing that belongs in the closed panel is cut off: the foot is inside it
                const foot = (await p.locator('.sp-foot').boundingBox())!;
                expect(foot.y + foot.height, variant).toBeLessThanOrEqual(box.y + box.height);
                const chipRow = (await p.locator('[data-chips]').boundingBox())!;
                expect(chipRow.y + chipRow.height, variant).toBeLessThanOrEqual(foot.y);
            }
            expect(await pageWidth(page), variant).toBeLessThanOrEqual(width);
        }
        await panel(page, 'mmcblk0').locator('[data-details-toggle]').click();
        await expect(panel(page, 'mmcblk0').locator('[data-details]')).toBeVisible();
        const gauge = (await page.locator('.ol-gauges > .ol-gauge').first().boundingBox())!;
        await expect.poll(async () => (await panel(page, 'mmcblk0').boundingBox())!.height).toBeGreaterThan(gauge.height);
    });
}

test('a change of the verdict keeps the pill and the panel in their boxes', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'replace');
    const pill = panel(page, 'sda').locator('[data-pill]');
    await expect(pill).toHaveText('replace');
    const before = (await pill.boundingBox())!;
    const panelBefore = (await panel(page, 'sda').boundingBox())!;
    // the same disk, judged good: the stub's report with the verdict changed
    await page.route('**/api/system/v1/storage', async (route) => {
        const response = await route.fetch();
        const j = await response.json();
        for (const d of j.devices) Object.assign(d, {verdict: 'good', reasons: []});
        await route.fulfill({response, json: {...j, verdict: 'good', reasons: []}});
    });
    await page.reload();
    await expect(pill).toHaveText('good');
    const after = (await pill.boundingBox())!;
    expect(after.width).toBe(before.width);
    expect(after.height).toBe(before.height);
    expect((await panel(page, 'sda').boundingBox())!.height).toBe(panelBefore.height);
});

test('an old SD card with heavy writes is one to watch, and says why', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'old-sd');
    const sd = panel(page, 'mmcblk0');
    await expect(sd.locator('.sp-reason')).toHaveText('The card is 8 years old and receives 620.0 MB a day.');
    await expect(sd.locator('.sp-more')).toHaveCount(0);
    await sd.locator('[data-details-toggle]').click();
    await expect(sd.locator('[data-fact="made"]')).toHaveText('07/2018 (8 years old)');
});

// task 111 (earlier part): the host monitors the disks of a VM and a container
test('a VM: the host watches its disk - no pill, no wear or SMART, one quiet line', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'virtual');
    const disk = panel(page, 'sda');
    await expect(disk.locator('[data-pill]')).toHaveCount(0);
    await expect(disk.locator('[data-notes]')).toHaveText('Disk health is monitored by the host');
    await expect(chips(page, 'sda')).toHaveText(['Writes 3.5 GB/day']);
    await disk.locator('[data-details-toggle]').click();
    await expect(disk.locator('[data-fact="smart"], [data-fact="wear"]')).toHaveCount(0);
    await expect(disk).not.toContainText('not available');
});

test('a container without disk panels says once that the host monitors them', async ({page, context, baseURL}) => {
    await open(page, context, baseURL, 'container');
    await expect(page.locator('.ol-storage-panel')).toHaveCount(0);
    await expect(page.locator('[data-storage-extra] [data-storage-host]')).toHaveText('Disk health is monitored by the host');
    await expect(page.locator('[data-notice="storage"]')).toHaveCount(0);
});

test('in German', async ({page, context, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await open(page, context, baseURL, 'replace');
    const sata = panel(page, 'sda');
    await expect(sata.locator('[data-pill]')).toHaveText('ersetzen');
    await expect(chips(page, 'sda')).toHaveText(['Verschleiß 88 %', 'SMART fehlgeschlagen', 'Schreiblast 95.0 MB/Tag', '7 Fehler']);
    await expect(sata.locator('.sp-more')).toHaveText('+1 weitere');
    await expect(sata.locator('[data-details-toggle]')).toHaveText(/Details$/);
    await context.clearCookies();
    await open(page, context, baseURL, 'virtual');
    await expect(panel(page, 'sda').locator('[data-notes]')).toHaveText('Die Datenträger-Gesundheit überwacht der Host');
});

test('the other warnings bring the storage warning along (stub-warn)', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-warn', value: '1', url: baseURL!}]);
    await page.goto('/');
    await expect(page.locator('[data-notice="storage"]')).toBeVisible();
    await expect(panel(page, 'mmcblk1').locator('[data-pill]')).toHaveText('watch');
});

// WCAG contrast of the pill's text against its own background, in whichever theme the project runs
test('the verdict pills are readable in this theme', async ({page, context, baseURL}) => {
    for (const [variant, disk] of [['good', 'mmcblk0'], ['watch', 'mmcblk1'], ['replace', 'sda']] as const) {
        await context.clearCookies();
        await open(page, context, baseURL, variant);
        const ratio = await panel(page, disk).locator('[data-pill]').evaluate((el) => {
            const rgb = (s: string) => (s.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
            const lum = ([r, g, b]: number[]) => {
                const c = [r!, g!, b!].map((v) => {
                    const x = v / 255;
                    return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
                });
                return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!;
            };
            const st = getComputedStyle(el);
            const a = lum(rgb(st.color));
            const b = lum(rgb(st.backgroundColor));
            return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
        });
        expect(ratio, variant).toBeGreaterThanOrEqual(3);
    }
});
