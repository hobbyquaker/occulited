import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 79 (the maintainer, corrected 2026-10-16): the duty-cycle chart draws the
// radio-load warning's threshold as a dotted line in the warning colour - 25 %, 75 % while a device
// firmware transfer runs on the radio. The band's top is at least 25 %, during a transfer at least
// the current value + 25 points; a 75 % line above that is said at the top edge, not stretched for.

/** the duty cycle's history flattened to `v` %, so the floor decides the band */
async function quiet(page: Page, v: number) {
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {history: Record<string, {t: string; dc: number}[]>};
        for (const s of Object.values(body.history)) for (const x of s) x.dc = v;
        await route.fulfill({response, json: body});
    });
}
const dutyChart = (page: Page) => page.locator('.ol-gauge', {hasText: 'Duty cycle'}).locator('.chart');

test('no transfer: the band reaches 25 %, the line at 25 % in the warning colour', async ({page}) => {
    await quiet(page, 1);
    await page.goto('/');
    const chart = dutyChart(page);
    await expect(chart.locator('text.tick').first()).toHaveText('25 %');
    const line = chart.locator('line.thr');
    await expect(line).toHaveAttribute('data-threshold', '25');
    await expect(chart.locator('[data-threshold-label="line"]')).toHaveText('25 %');
    const [stroke, warn] = await line.evaluate((e) => [getComputedStyle(e).stroke, getComputedStyle(document.body).getPropertyValue('--hmm-warn').trim()]);
    expect(stroke).not.toBe('none');
    expect(warn).not.toBe('');
    // the label in the warning colour too, not the ticks' muted one
    const [fill, tickFill] = await Promise.all([chart.locator('[data-threshold-label]').evaluate((e) => getComputedStyle(e).fill), chart.locator('text.tick').first().evaluate((e) => getComputedStyle(e).fill)]);
    expect(fill).toBe(stroke);
    expect(fill).not.toBe(tickFill);
});

test('a transfer at 11 %: the band to 36 %, the 75 % threshold said at the top edge', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-dc-transfer', value: '1', url: baseURL!}]);
    await quiet(page, 1);
    await page.goto('/');
    const chart = dutyChart(page);
    await expect(chart.locator('text.tick').first()).toHaveText('36 %');
    await expect(chart.locator('line.thr')).toHaveCount(0);
    await expect(chart.locator('[data-threshold-label="above"]')).toHaveText('Threshold 75 %');
});

test('a transfer at 60 %: the band to 85 %, the line at 75 % with its label; German', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-dc-transfer', value: '1', url: baseURL!}, {name: 'stub-duty', value: '60', url: baseURL!}]);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await quiet(page, 1);
    await page.goto('/');
    const chart = page.locator('.ol-gauge', {hasText: 'Duty Cycle'}).locator('.chart');
    await expect(chart.locator('text.tick').first()).toHaveText('85 %');
    await expect(chart.locator('line.thr')).toHaveAttribute('data-threshold', '75');
    await expect(chart.locator('[data-threshold-label="line"]')).toHaveText('75 % · Übertragung');
});

test('data above the floor: the band follows the data, the line stays inside', async ({page}) => {
    await page.goto('/');
    const chart = dutyChart(page);
    await expect(chart.locator('text.tick').first()).toHaveText('100 %'); // the stub's 24 h hold a 45 % burst
    await expect(chart.locator('line.thr')).toHaveAttribute('data-threshold', '25');
});
