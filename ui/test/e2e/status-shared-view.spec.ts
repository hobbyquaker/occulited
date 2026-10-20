import {expect, test} from './fixtures';

// occulited task 83 (the maintainer): the subscriber lists of the component panels open and close
// together, and one time range holds for every history chart of the Status page - both remembered
// in this browser

test('a subscriber list opens and closes in every component panel', async ({page}) => {
    await page.goto('/');
    const subs = page.locator('.ol-components .ol-if-subs');
    const lists = page.locator('.ol-components .ol-if-clients');
    // the stub's two interface cards with subscribers: BidCos-RF and HmIP-RF
    await expect(subs).toHaveCount(2);
    await expect(lists).toHaveCount(0);
    const card = (n: string) => page.locator(`.ol-components [data-component="${n}"]`);
    const before = await card('HmIP-RF').locator('.ol-if-subs').boundingBox();
    await card('BidCos-RF').locator('.ol-if-subs').click();
    await expect(lists).toHaveCount(2);
    for (const n of ['BidCos-RF', 'HmIP-RF']) {
        await expect(card(n).locator('.ol-if-subs')).toHaveAttribute('aria-expanded', 'true');
        await expect(card(n).locator('.ol-if-clients li').first()).toBeVisible();
    }
    // the no-resize rule: the toggle keeps its box when its list opens
    const after = await card('HmIP-RF').locator('.ol-if-subs').boundingBox();
    expect([after!.width, after!.height]).toEqual([before!.width, before!.height]);
    // remembered: a reload shows them open
    await page.reload();
    await expect(lists).toHaveCount(2);
    // closing one closes all
    await card('HmIP-RF').locator('.ol-if-subs').click();
    await expect(lists).toHaveCount(0);
    await expect(subs.first()).toHaveAttribute('aria-expanded', 'false');
});

test('one time range for every chart of the page', async ({page}) => {
    await page.goto('/');
    const charts = page.locator('.chart:has(.spans)');
    // memory, load, duty cycle, carrier sense, and the event rates of the components
    await expect.poll(() => charts.count()).toBeGreaterThanOrEqual(5);
    const pressed = (label: string) => page.locator('.chart .spans button[aria-pressed="true"]', {hasText: label});
    const n = await charts.count();
    // task 81: all open on 24 h
    await expect(pressed('24 h')).toHaveCount(n);
    // a range picked in a component's event-rate chart switches the gauges' charts too
    await page.locator('.ol-components [data-component="HmIP-RF"] .chart').getByRole('button', {name: '1 h'}).click();
    await expect(pressed('1 h')).toHaveCount(n);
    await expect(page.locator('.ol-gauge', {hasText: 'Duty cycle'}).locator('.peaktext')).toHaveText(/\(1 h\)$/);
    // and one picked in the memory chart switches the components' charts
    await page.locator('.ol-gauge', {hasText: 'Memory'}).getByRole('button', {name: '6 h'}).click();
    await expect(pressed('6 h')).toHaveCount(n);
    // remembered across a reload
    await page.reload();
    await expect(pressed('6 h')).toHaveCount(n);
});
