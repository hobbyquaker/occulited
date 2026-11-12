import {expect, test, type Page, type TestInfo} from '@playwright/test';

// occulited B-133: a date follows the page's language, not the browser's locale. A headless
// browser in en-US with the page in German showed "11/1/2026, 12:18:32 AM" inside a German warning;
// every date now goes through lib/datefmt.ts (German de-DE, English en-GB). Both directions: an
// English browser with the German page, a German browser with the English page.

test.use({timezoneId: 'UTC'});

async function plant(page: Page, baseURL: string, info: TestInfo, cookies: Record<string, string>) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies(Object.entries({...cookies, 'stub-warnings': id}).map(([name, value]) => ({name, value, url: baseURL})));
}

const last = {
    automatic: {time: '2026-11-09T23:07:12Z', name: 'a.sbk.age', size: 512_000_000, encrypted: true, instance: 'nightly', targets: ['directory']},
    download: {time: '2026-11-01T00:18:32Z', name: 'b.sbk', size: 1_250_000_000, encrypted: false, by: 'admin'},
};

const cases = [
    {browser: 'en-US', page: 'de', automatic: '9.11.2026, 23:07:12', download: '1.11.2026, 00:18:32', wraps: '1.4.2033', since: /am \d{1,2}\.\d{1,2}\.\d{4}, \d{2}:\d{2}:\d{2} im Browser/},
    {browser: 'de-DE', page: 'en', automatic: '09/11/2026, 23:07:12', download: '01/11/2026, 00:18:32', wraps: '01/04/2033', since: /on \d{2}\/\d{2}\/\d{4}, \d{2}:\d{2}:\d{2}\./},
];

for (const c of cases) {
    test.describe(`a ${c.browser} browser, the page in ${c.page}`, () => {
        test.use({locale: c.browser});
        test.beforeEach(async ({page}) => {
            await page.addInitScript((l) => localStorage.setItem('ol.language', l), c.page);
        });

        test('the Last backups rows', async ({page}) => {
            await page.route('**/api/system/v1/backup/last', (route) => route.fulfill({json: last}));
            await page.goto('/system/backup');
            const row = (w: string) => page.locator(`[data-section="create"] [data-last-way="${w}"]`);
            await expect(row('automatic')).toContainText(c.automatic);
            await expect(row('download')).toContainText(c.download);
            for (const w of ['automatic', 'download']) await expect(row(w)).not.toContainText(/AM|PM/);
        });

        test('a date and a date with its time inside warning sentences', async ({page, baseURL}, info) => {
            await plant(page, baseURL!, info, {'stub-backup-age': 'warning', 'stub-counter': 'near'});
            await page.goto('/');
            const age = page.locator('[data-warnings] [data-notice="backup-age"] .ol-notice-text');
            await expect(age).toContainText(c.since);
            await expect(age).not.toContainText(/AM|PM/);
            await expect(page.locator('[data-warnings] [data-notice="hmip-security-counter"] .ol-notice-text')).toContainText(c.wraps);
        });
    });
}
