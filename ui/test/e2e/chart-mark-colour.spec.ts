import {expect, test} from './fixtures';

// occulited B-89: the carrier-sense chart's "10 %" mark label is in the mark's colour, not the
// muted tick colour, in both themes

for (const scheme of ['light', 'dark'] as const) {
    test(`the mark label wears the mark's colour (${scheme})`, async ({page}) => {
        await page.emulateMedia({colorScheme: scheme});
        await page.goto('/');
        const chart = page.locator('.ol-gauge', {hasText: 'Carrier sense'}).locator('.chart');
        const label = chart.locator('text.marktick');
        await expect(label).toHaveText('10 %');
        const [fill, line, tick] = await Promise.all([
            label.evaluate((e) => getComputedStyle(e).fill),
            chart.locator('line.mark').evaluate((e) => getComputedStyle(e).stroke),
            chart.locator('text.tick:not(.marktick)').first().evaluate((e) => getComputedStyle(e).fill),
        ]);
        expect(fill).toBe(line);
        expect(fill).not.toBe(tick);
    });
}
