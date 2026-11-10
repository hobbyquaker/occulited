import {expect, test} from './fixtures';

// occulited task 139 (the maintainer, 2026-11-10): the Backup page shows, beside the targets' last
// runs, the newest backup of each way it left the system - automatic, Back up now, downloaded in
// the browser, fetched by a program (occulite-backup) - "never" where a way did not happen; the
// rows are there from the start, so the panel keeps its height when the answer comes.

test.use({timezoneId: 'UTC', locale: 'en-US'});

const way = (w: string) => `[data-section="create"] [data-last-way="${w}"]`;

test('each way on its row: time, size, encrypted or not, who; "never" where none', async ({page}) => {
    await page.route('**/api/system/v1/backup/last', (route) => route.fulfill({json: {
        automatic: {time: '2026-11-09T23:07:12Z', name: 'openccu-1.2.0-2026-11-10-0007.sbk.age', size: 512_000_000, encrypted: true, instance: 'nightly', targets: ['directory', 'tssh0001']},
        download: {time: '2026-11-08T18:30:00Z', name: 'openccu-1.2.0-2026-11-08.sbk', size: 1_250_000_000, encrypted: false, by: 'admin'},
        client: {time: '2026-11-10T03:15:00Z', name: 'openccu-1.2.0-2026-11-10.sbk.age', size: 512_000_000, encrypted: true, by: 'occulite-backup on nas'},
        earlier: {time: '2026-10-01T00:07:00Z', name: 'old.sbk', size: 1, encrypted: false},
    }}));
    await page.goto('/system/backup');
    await expect(page.locator(way('automatic'))).toHaveText('11/9/2026, 11:07:12 PM · 512 MB · encrypted · to USB, Build host');
    await expect(page.locator(way('run'))).toHaveText('never');
    await expect(page.locator(way('download'))).toHaveText('11/8/2026, 6:30:00 PM · 1.3 GB · unencrypted · by admin');
    await expect(page.locator(way('client'))).toHaveText('11/10/2026, 3:15:00 AM · 512 MB · encrypted · occulite-backup on nas');
    // an entry from before the way was recorded shows only when it is the newest
    await expect(page.locator(way('earlier'))).toHaveCount(0);
});

test('a system that recorded only before the update shows its earlier backup', async ({page}) => {
    await page.route('**/api/system/v1/backup/last', (route) => route.fulfill({json: {earlier: {time: '2026-10-01T00:07:00Z', name: 'old.sbk', size: 4_718_592, encrypted: false}}}));
    await page.goto('/system/backup');
    for (const w of ['automatic', 'run', 'download', 'client']) await expect(page.locator(way(w))).toHaveText('never');
    await expect(page.locator(way('earlier'))).toHaveText('10/1/2026, 12:07:00 AM · 5 MB · unencrypted');
});

test('the rows keep their height while the answer is on its way', async ({page}) => {
    let release: () => void = () => {};
    const held = new Promise<void>((r) => (release = r));
    await page.route('**/api/system/v1/backup/last', async (route) => {
        await held;
        await route.fulfill({json: {}});
    });
    await page.goto('/system/backup');
    const rows = page.locator('[data-last-backups]');
    await expect(rows.locator('[data-last-way]')).toHaveCount(4);
    const before = (await rows.boundingBox())!.height;
    release();
    await expect(page.locator(way('client'))).toHaveText('never');
    expect((await rows.boundingBox())!.height).toBe(before);
});

test('German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    const panel = page.locator('[data-section="create"]');
    await expect(panel).toContainText('Automatische Sicherung');
    await expect(panel).toContainText('Im Browser heruntergeladen');
    await expect(panel).toContainText('Von einem Programm abgeholt');
    await expect(page.locator(way('client'))).toHaveText('nie');
});
