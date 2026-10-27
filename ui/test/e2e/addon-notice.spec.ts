import {expect, test} from './fixtures';

// occulited task 119: an addon's own notice on the Status page - its words in the page's language,
// its name in front, the button to the addon's settings page; silenced as the system's warnings.
const notice = {id: 'addon-notice', variant: 'battery/low', severity: 'warning', href: '/addon-settings/battery', params: {addon: 'battery', title: {de: 'Batterie schwach', en: 'Battery low'}, text: {de: 'Bei 3 Geräten ist die Batterie bald leer.', en: '3 devices will need a new battery soon.'}}};

async function withNotice(page: import('@playwright/test').Page) {
    await page.route('**/api/system/v1/warnings', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []), notice];
        await route.fulfill({response, json: j});
    });
}

test('an addon notice on the Status page, with its button', async ({page}) => {
    await withNotice(page);
    await page.goto('/');
    const row = page.getByText('Battery low. 3 devices will need a new battery soon.');
    await expect(row).toBeVisible();
    await expect(page.getByRole('link', {name: 'Open the addon'})).toHaveAttribute('href', '/addon-settings/battery');
});

test('in German, the addon\'s German words', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await withNotice(page);
    await page.goto('/');
    await expect(page.getByText('Batterie schwach. Bei 3 Geräten ist die Batterie bald leer.')).toBeVisible();
    await expect(page.getByRole('link', {name: 'Addon öffnen'})).toBeVisible();
});
