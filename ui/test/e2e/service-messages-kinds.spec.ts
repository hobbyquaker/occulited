import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 27: the service messages by kind - a symbol and a severity colour per kind, one
// row per device ("war gestört" folded into its "gestört"), at most eight rows before "show all",
// filter chips with counts kept per browser, sorted by severity and then newest first.

test.beforeEach(async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'many', url: baseURL!}]);
});

test('one row per device, sticky folded, red first, capped at eight', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    const items = card.locator('.sm-item');
    await expect(items).toHaveCount(8);
    // the two red devices first, the newer of them on top
    await expect(items.nth(0)).toHaveAttribute('data-device', 'HmIP-RF:00010000000B01');
    await expect(items.nth(1)).toHaveAttribute('data-device', 'HmIP-RF:00010000000B09');
    const bad = card.locator('[data-device="HmIP-RF:00010000000B01"]');
    await expect(bad.locator('[data-kind]')).toHaveCount(1); // "war gestört" folded into "gestört"
    await expect(bad.locator('[data-kind="unreach"]')).toHaveClass(/sm-red/);
    // two kinds of one device side by side
    const kitchen = card.locator('[data-device="HmIP-RF:00010000000B02"]');
    await expect(kitchen.locator('[data-kind]')).toHaveCount(2);
    await expect(kitchen.locator('[data-kind="lowbat"]')).toHaveClass(/sm-orange/);
    await expect(kitchen.locator('[data-kind="config"]')).toHaveClass(/sm-blue/);
    // show all: eleven devices
    await card.locator('[data-show-all]').click();
    await expect(items).toHaveCount(11);
    await expect(card.locator('[data-device="HmIP-RF:00010000000B03"] [data-kind="sticky"]')).toHaveClass(/sm-grey/);
    await expect(items.last()).toHaveAttribute('data-device', 'HmIP-RF:00010000000B03'); // grey last
    expect(await fitsWindow(page)).toBe(true);
});

test('a filter chip filters, a second click clears, and the choice is kept', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    const low = card.locator('[data-filter="lowbat"]');
    await expect(low).toHaveText(/Low battery · 3/);
    await low.click();
    await expect(low).toHaveAttribute('aria-pressed', 'true');
    await expect(card.locator('.sm-item')).toHaveCount(3);
    await page.reload();
    await expect(page.locator('[data-service-messages="13"] .sm-item')).toHaveCount(3);
    await page.locator('[data-filter="lowbat"]').click();
    await expect(page.locator('[data-service-messages="13"] .sm-item')).toHaveCount(8);
});

test('the chips in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await expect(card.locator('[data-filter="unreach"]')).toHaveText(/Kommunikation gestört · 2/);
    await expect(card.locator('[data-show-all]')).toHaveText('Alle anzeigen (11)');
});
