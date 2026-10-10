import {expect, test} from './fixtures';

// occulited B-57: a system that runs a newer version than the newest published release (a round
// not yet on GitHub, a local build) says so - not "runs the newest release".

test('installed newer than the newest published release', async ({page}) => {
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed = {...j.feed, available: {version: '1.0.0-dev.42', name: 'openccu-lite-x86_64-ova-1.0.0-dev.42.zip', size: 1, newer: false}, installed_newer: true};
        await route.fulfill({response, json: j});
    });
    await page.goto('/system/updates');
    await expect(page.getByText('This system runs a newer version than the newest published release (1.0.0-dev.42).')).toBeVisible();
    await expect(page.getByText('This system runs the newest release.')).toHaveCount(0);
});

test('the newest published release is installed', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed = {...j.feed, available: {version: '1.0.0-alpha.0', name: 'openccu-lite-x86_64-ova-1.0.0-alpha.0.zip', size: 1, newer: false}};
        await route.fulfill({response, json: j});
    });
    await page.goto('/system/updates');
    await expect(page.getByText('Dieses System läuft mit dem neuesten Release.')).toBeVisible();
});
