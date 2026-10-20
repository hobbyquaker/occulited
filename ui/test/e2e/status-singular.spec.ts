import {expect, test} from './fixtures';

// occulited B-88: one of a thing is singular on the Status page's cards - "1 update available", not
// "1 updates available" - in both languages

async function one(page: import('@playwright/test').Page) {
    await page.route('**/api/system/v1/firmware', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {devices: {update_available: boolean}[]};
        let first = true;
        for (const d of body.devices) {
            d.update_available = d.update_available && first;
            if (d.update_available) first = false;
        }
        if (first && body.devices[0]) body.devices[0].update_available = true;
        await route.fulfill({response, json: body});
    });
    await page.route('**/api/system/v1/certificate', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {current: {days_left: number}; warning?: string};
        body.current.days_left = 1;
        body.warning = 'expiring';
        await route.fulfill({response, json: body});
    });
}

for (const [lang, update, day] of [
    ['en', '1 update available', '1 day left'],
    ['de', '1 Update verfügbar', 'noch 1 Tag'],
] as const) {
    test(`one update, one day: singular (${lang})`, async ({page}) => {
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        await one(page);
        await page.goto('/');
        await expect(page.locator('[data-component="firmware"] .ol-card-body')).toHaveText(update);
        await expect(page.locator('[data-component="certificate"] .ol-badge')).toHaveText(day);
    });
}
