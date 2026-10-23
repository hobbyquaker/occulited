import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited B-106 (the maintainer, 2026-10-23): "Release 1.0.0-beta.14 ist verfügbar.Release-Notizen" -
// Svelte trims the leading whitespace of an {#if} block, so a space written at its start is lost.
// The separators on the Updates page are expressions ({' '}), and the rendered text keeps them.

async function release(page: Page) {
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed = {...j.feed, available: {version: '1.0.0-beta.14', name: 'openccu-lite-x86_64-ova-1.0.0-beta.14.zip', size: 1, newer: true, notes_url: 'https://example.invalid/releases/v1.0.0-beta.14'}};
        await route.fulfill({response, json: j});
    });
    await page.route('**/api/system/v1/firmware', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.next_run = j.last_run;
        await route.fulfill({response, json: j});
    });
}

for (const [lang, sentence, link, next] of [
    ['en', 'Release 1.0.0-beta.14 is available.', 'Release notes', 'Next run'],
    ['de', 'Release 1.0.0-beta.14 ist verfügbar.', 'Release-Notizen', 'Nächster Lauf'],
] as const) {
    test(`a space before the release notes link and around the next run's dot (${lang})`, async ({page}) => {
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        await release(page);
        await page.goto('/system/updates');
        const line = page.locator('[data-release-available]');
        await expect(line.getByRole('link', {name: link})).toBeVisible();
        expect(await line.textContent()).toBe(`${sentence} ${link}`);
        const check = page.locator('[data-last-check]');
        await expect(check).toContainText(next);
        expect(await check.textContent()).toMatch(new RegExp(`\\S · ${next}: `));
    });
}
