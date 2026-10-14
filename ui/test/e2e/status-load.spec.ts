import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 37: the system's load and memory over time on the Status page, like the duty cycle
// and the carrier sense - the load as a figure with the CPU count it is read against (no ring: it
// has no ceiling) and a graph with that count as its mark; the memory's ring gets its graph.

test('the load tile: the figure, the CPU count, the graph with the count as its mark', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-load-card]');
    await expect(card).toBeVisible();
    await expect(card.locator('[data-load-now]')).toHaveText('0.14');
    await expect(card.locator('[data-load-cpus]')).toHaveText('1-minute average, 4 CPUs');
    const chart = card.locator('svg[role="img"][aria-label="load history"]');
    await expect(chart).toBeVisible();
    // the band reaches the CPU count: the dashed mark is drawn
    await expect(chart.locator('line.mark')).toHaveCount(1);
    // 24 h: the reboot's hole breaks the line into two runs, and the busy stretch is the span's peak
    await card.getByRole('button', {name: '24 h'}).click();
    await expect(chart.locator('polyline')).toHaveCount(2);
    await expect(card).toContainText('max 3.60 (24 h)');
    // no ring around the load
    await expect(card.locator('.ring')).toHaveCount(0);
    expect(await fitsWindow(page)).toBe(true);
});

test('the memory ring gets its graph', async ({page}) => {
    await page.goto('/');
    const mem = page.locator('.ol-gauge', {hasText: 'Memory'}).first();
    await expect(mem.locator('svg[role="img"][aria-label="memory history"]')).toBeVisible();
    await expect(mem).toContainText('max 34 % (1 h)');
});

test('German, and a system without the sampler shows no load tile', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/');
    const card = page.locator('[data-load-card]');
    await expect(card.locator('.ol-card-title')).toHaveText('Last');
    await expect(card.locator('[data-load-now]')).toHaveText('0,14');
    await expect(card.locator('[data-load-cpus]')).toHaveText('Mittel über 1 Minute, 4 CPUs');
    await page.route('**/api/system/v1/status/history', (r) => r.fulfill({status: 501, json: {error: 'unsupported'}}));
    await page.reload();
    await expect(page.locator('.ol-gauge', {hasText: 'Speicher'}).first()).toBeVisible();
    await expect(page.locator('[data-load-card]')).toHaveCount(0);
});
