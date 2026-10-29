import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited B-74 (the maintainer's screenshot of a Pi 4 on beta.6, a 2280 px window): the Status
// page's history charts drew out of their panels to the window's edge, their washes in blocks.
// The series held three samples stamped 2067 from a boot with a wrong clock; the chart placed them
// decades to the right of its end. Now the span ends at the last sample written, anything after it
// falls out, and the chart is clipped to its own box. Every chart's line and wash lie inside its
// panel, at 1440 and 2280 px; the two colour schemes are the desktop projects.

async function outside(page: Page): Promise<string[]> {
    return page.locator('.ol-card:has(.chart svg)').evaluateAll((cards) => {
        const out: string[] = [];
        for (const card of cards) {
            const c = card.getBoundingClientRect();
            const name = card.querySelector('.ol-card-title')?.textContent ?? '?';
            for (const el of card.querySelectorAll('.chart svg, .chart polyline, .chart polygon')) {
                // an SVG shape's client rect is its geometry, clipped or not - what it would draw
                const r = el.getBoundingClientRect();
                if (r.left < c.left - 1 || r.right > c.right + 1 || r.top < c.top - 1 || r.bottom > c.bottom + 1) {
                    out.push(`${name}: ${el.tagName} ${Math.round(r.left)}..${Math.round(r.right)} outside ${Math.round(c.left)}..${Math.round(c.right)}`);
                }
            }
        }
        return out;
    });
}

for (const width of [1440, 2280]) {
    for (const clock of ['', '2067']) {
        test(`at ${width} px${clock ? ', samples from a wrong clock in the series' : ''}: every chart inside its panel`, async ({page, baseURL}) => {
            test.skip(test.info().project.name === 'phone', 'the wide windows are the desktop projects');
            if (clock) await page.context().addCookies([{name: 'stub-clock', value: clock, url: baseURL!}]);
            await page.setViewportSize({width, height: 1200});
            await page.goto('/');
            // memory and load, the duty cycle and carrier sense of the radio, the events of both stacks
            await expect(page.locator('.ol-card .chart svg')).toHaveCount(6);
            expect(await outside(page)).toEqual([]);
            // the 2067 samples are not shown: the axis ends at the last sample's minute, as without them
            // (a chart whose span reaches back past midnight writes the date before the time - "10/29 01:35"
            // beside "01:35" in the first hours of a day - so the minute is compared, not the label)
            const ends = await page.locator('.chart .time.end').allTextContents();
            expect(new Set(ends.map((e) => e.trim().split(' ').pop())).size, ends.join(', ')).toBe(1);
            expect(await fitsWindow(page)).toBe(true);
        });
    }
}

test('the wrong-clock samples leave the load chart as it is without them', async ({page, baseURL}) => {
    test.skip(test.info().project.name === 'phone', 'one desktop look is enough');
    await page.setViewportSize({width: 1440, height: 1200});
    await page.goto('/');
    const chart = page.locator('[data-load-card] .chart svg');
    const clean = await chart.locator('polyline').evaluateAll((ls) => ls.map((l) => l.getAttribute('points')));
    await page.context().addCookies([{name: 'stub-clock', value: '2067', url: baseURL!}]);
    await page.reload();
    await expect(chart).toBeVisible();
    expect(await chart.locator('polyline').evaluateAll((ls) => ls.map((l) => l.getAttribute('points')))).toEqual(clean);
});
