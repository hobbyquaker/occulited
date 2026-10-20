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
    // occulited task 81: on 24 h by default, the last hour one click away
    await expect(mem.getByRole('button', {name: '24 h'})).toHaveAttribute('aria-pressed', 'true');
    await expect(mem).toContainText(/max \d+ % \(24 h\)/);
    await mem.getByRole('button', {name: '1 h'}).click();
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

// occulited task 91: the memory ring warns from 90 % in use (was 85 %), red from 95 %
for (const [pct, cls] of [[89, ''], [90, 'warn'], [95, 'err']] as const) {
    test(`the memory ring at ${pct} %: ${cls || 'no warning'}`, async ({page}) => {
        const total = 2_000_000;
        await page.route('**/api/system/v1/status', async (r) => {
            const res = await r.fetch();
            const body = await res.json();
            await r.fulfill({response: res, json: {...body, mem_total_kb: total, mem_available_kb: total * (100 - pct) / 100}});
        });
        await page.goto('/');
        const mem = page.locator('.ol-gauge', {hasText: 'Memory'}).first();
        await expect(mem).toContainText(`${pct} %`);
        for (const c of ['warn', 'err']) {
            if (c === cls) await expect(mem).toHaveClass(new RegExp(`\\b${c}\\b`));
            else await expect(mem).not.toHaveClass(new RegExp(`(^|\\s)${c}(\\s|$)`));
        }
    });
}
