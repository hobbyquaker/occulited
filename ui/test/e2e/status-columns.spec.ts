import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 54 (the maintainer, 2026-10-15): "component panels have 1/4 width - make them 1/3
// like the panels above under 'auslastung'". The component cards share the gauges' grid track, so
// a component card is as wide as a gauge and both sections have the same number of columns at
// every width.

async function columns(page: Page, grid: string): Promise<number> {
    return page.locator(grid).first().evaluate((e) => getComputedStyle(e).gridTemplateColumns.split(' ').length);
}

async function widthOf(page: Page, sel: string): Promise<number> {
    return (await page.locator(sel).first().boundingBox())!.width;
}

for (const [width, cols] of [[1440, 3], [900, 2], [560, 1]] as const) {
    test(`at ${width} px: ${cols} columns in both sections, the cards as wide as the gauges`, async ({page}) => {
        await page.setViewportSize({width, height: 1000});
        await page.goto('/');
        await expect(page.locator('[data-component="HmIP-RF"]')).toBeVisible();
        await expect(page.locator('[data-component="certificate"]')).toBeVisible();
        expect(await columns(page, '.ol-gauges')).toBe(cols);
        for (const grid of await page.locator('.ol-components').all()) {
            expect(await grid.evaluate((e) => getComputedStyle(e).gridTemplateColumns.split(' ').length)).toBe(cols);
        }
        const gauge = await widthOf(page, '.ol-gauges > .ol-card');
        for (const name of ['HmIP-RF', 'BidCos-RF', 'firmware', 'certificate', 'addons']) {
            expect(Math.abs((await widthOf(page, `.ol-components [data-component="${name}"]`)) - gauge), name).toBeLessThanOrEqual(1);
        }
        // nothing in a card runs out of it: the content is no wider than the card
        const over = await page.locator('.ol-components > .ol-card').evaluateAll((els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => e.getAttribute('data-component')));
        expect(over).toEqual([]);
        expect(await fitsWindow(page)).toBe(true);
    });
}
