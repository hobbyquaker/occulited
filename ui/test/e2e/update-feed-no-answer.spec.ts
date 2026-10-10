import {expect, test} from './fixtures';

// occulited B-56: a release feed that accepts and never answers ends the check after the feed
// timeout, and the Updates page names the host and the wait - not a raw "context deadline
// exceeded" after 30 minutes.

const failed = {
    error: 'feed-unreachable',
    message: 'api.github.com did not answer within 45s',
    detail: {host: 'api.github.com', timeout: 45},
};

test('Check now against a feed that does not answer: host and wait, in the notice and the status line', async ({page}) => {
    let checked = false;
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        if (checked) j.feed = {...j.feed, error: failed.message, error_host: 'api.github.com', error_timeout: 45, available: null};
        await route.fulfill({response, json: j});
    });
    await page.route('**/api/system/v1/system-update/check', (route) => {
        checked = true;
        return route.fulfill({status: 502, json: {...failed, feed: {enabled: true, error: failed.message, error_host: 'api.github.com', error_timeout: 45, available: null}}});
    });
    await page.goto('/system/updates');
    await page.locator('[data-check-daily="system-update"]').getByRole('button', {name: 'Check now'}).click();
    const text = 'api.github.com does not answer: no answer within 45 seconds. Try again later.';
    await expect(page.getByText(text, {exact: true})).toBeVisible();
    await expect(page.getByText('Release check failed: ' + text)).toBeVisible();
});

test('the failed check in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed = {...j.feed, error: failed.message, error_host: 'api.github.com', error_timeout: 45, available: null};
        await route.fulfill({response, json: j});
    });
    await page.goto('/system/updates');
    await expect(page.getByText('Release-Prüfung fehlgeschlagen: api.github.com ist nicht erreichbar: keine Antwort innerhalb von 45 Sekunden. Bitte später erneut versuchen.')).toBeVisible();
});
