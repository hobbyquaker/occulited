import {expect, test, type Page} from './fixtures';

// occulited task 134: the system is older than the manifest's requires.lite - the install and the update are disabled
// and say why; an installed addon the system no longer meets is marked, its buttons otherwise stay.
async function older(page: Page) {
    await page.route('**/api/system/v1/catalog', async (route) => {
        const res = await route.fetch();
        const json = await res.json();
        for (const a of json.catalog.addons) {
            if (a.id === 'xml-api' || a.id === 'redmatic') {
                a.requires = {...a.requires, lite: '9.9.0-dev.1'};
                a.lite_unmet = true;
            }
        }
        await route.fulfill({response: res, json});
    });
}

test('the catalogue: the buttons are disabled and the page says what it needs', async ({page}) => {
    await older(page);
    await page.goto('/addons');
    const install = page.locator('#addon-row-xml-api [data-addon-install]');
    await expect(install).toBeDisabled();
    await expect(install).toHaveAttribute('title', 'Requires openccu-lite 9.9.0-dev.1 or later');
    await expect(page.locator('#addon-row-xml-api [data-addon-lite-unmet]')).toHaveText('Requires openccu-lite 9.9.0-dev.1 or later');
    await expect(page.locator('#addon-row-redmatic [data-addon-update]')).toBeDisabled();
});

test('an installed addon the system no longer meets is marked, in German too', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/system/v1/addons', async (route) => {
        const res = await route.fetch();
        const json = await res.json();
        const list = json.addons ?? json;
        const one = list.find((a: {id: string}) => a.id === 'redmatic');
        one.requires_lite = '9.9.0-dev.1';
        await route.fulfill({response: res, json});
    });
    await page.goto('/addons');
    await expect(page.locator('#addon-row-redmatic [data-addon-lite-unmet]')).toHaveText('Benötigt openccu-lite 9.9.0-dev.1 oder neuer');
});
