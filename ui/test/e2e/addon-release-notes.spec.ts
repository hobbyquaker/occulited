import {expect, test} from './fixtures';

// occulited task 26: beside an offered addon update, a "Release notes" link to the notes of the
// offered version - the release's page from the catalogue's check, or the manifest's changelog -
// opening in a new tab; no link when neither is known.

async function catalogWith(page: import('@playwright/test').Page, change: (e: Record<string, unknown>) => void) {
    await page.route('**/api/system/v1/catalog', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        for (const e of j.catalog.addons as Record<string, unknown>[]) if (e.id === 'redmatic') change(e);
        await route.fulfill({response, json: j});
    });
}

test('the release page of the offered version beside the Update button', async ({page}) => {
    await catalogWith(page, (e) => (e.release_notes = 'https://github.com/rdmtc/RedMatic/releases/tag/v9.4.1'));
    await page.goto('/addons');
    const row = page.locator('#addon-row-redmatic');
    await expect(row.locator('[data-addon-update]')).toBeVisible();
    const notes = row.locator('[data-addon-notes]');
    await expect(notes).toHaveText('Release notes');
    await expect(notes).toHaveAttribute('href', 'https://github.com/rdmtc/RedMatic/releases/tag/v9.4.1');
    await expect(notes).toHaveAttribute('target', '_blank');
    await expect(notes).toHaveAttribute('rel', 'noopener');
});

test("the manifest's changelog, in German", async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await catalogWith(page, (e) => (e.release_notes = 'https://example.org/CHANGELOG.md#941'));
    await page.goto('/addons');
    const notes = page.locator('#addon-row-redmatic [data-addon-notes]');
    await expect(notes).toHaveAttribute('href', 'https://example.org/CHANGELOG.md#941');
    await expect(notes).toHaveText('Release-Notizen');
});

test('no notes known: no link', async ({page}) => {
    await catalogWith(page, (e) => delete e.release_notes);
    await page.goto('/addons');
    const row = page.locator('#addon-row-redmatic');
    await expect(row.locator('[data-addon-update]')).toBeVisible();
    await expect(row.locator('[data-addon-notes]')).toHaveCount(0);
});
