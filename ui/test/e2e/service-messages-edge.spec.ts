import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 53 (the maintainer, 2026-10-15): the service messages panel has the thick left
// edge of the warning panels, in the colour of its most severe message - red for communication
// disturbed or a fault, orange for a low battery, blue for configuration or update pending; a grey
// "was disturbed" alone and no message at all leave it without one.

async function show(page: Page, baseURL: string, keys: string) {
    await page.context().addCookies([{name: 'stub-servicemsg', value: `keys:${keys}`, url: baseURL}]);
    await page.goto('/');
}

// the colour a CSS variable resolves to on this page, as the browser computes it
async function colourOf(page: Page, variable: string): Promise<string> {
    return page.evaluate((v) => {
        const probe = document.createElement('div');
        probe.style.color = `var(${v})`;
        document.body.appendChild(probe);
        const c = getComputedStyle(probe).color;
        probe.remove();
        return c;
    }, variable);
}

async function edgeOf(page: Page) {
    const card = page.locator('[data-service-messages]');
    await expect(card).toBeVisible();
    return card.evaluate((e) => {
        const s = getComputedStyle(e);
        return {width: s.borderLeftWidth, colour: s.borderLeftColor, top: s.borderTopWidth};
    });
}

for (const [keys, edge, variable] of [
    ['CONFIG_PENDING,LOW_BAT,UNREACH', 'err', '--hmm-error'],
    ['UPDATE_PENDING,SABOTAGE', 'err', '--hmm-error'],
    ['CONFIG_PENDING,LOW_BAT', 'warn', '--hmm-warn'],
    ['UPDATE_PENDING,STICKY_UNREACH', 'info', '--hmm-accent'],
] as const) {
    test(`${keys}: the ${edge} edge`, async ({page, baseURL}) => {
        await show(page, baseURL!, keys);
        await expect(page.locator('[data-service-messages]')).toHaveAttribute('data-edge', edge);
        const got = await edgeOf(page);
        expect(got.width).toBe('4px');
        expect(got.colour).toBe(await colourOf(page, variable));
        // the same edge as the warning panels above it (lib/Notice.svelte)
        if (edge !== 'info') {
            const same = await page.evaluate((cls) => {
                const n = document.createElement('div');
                n.className = `ol-panel ${cls}`;
                document.body.appendChild(n);
                const s = getComputedStyle(n);
                const r = {width: s.borderLeftWidth, colour: s.borderLeftColor};
                n.remove();
                return r;
            }, edge);
            expect(got.width).toBe(same.width);
            expect(got.colour).toBe(same.colour);
        }
    });
}

test('a grey message alone: no coloured edge', async ({page, baseURL}) => {
    await show(page, baseURL!, 'STICKY_UNREACH');
    const card = page.locator('[data-service-messages]');
    await expect(card).toHaveAttribute('data-service-messages', '1');
    await expect(card).not.toHaveAttribute('data-edge', /.+/);
    const got = await edgeOf(page);
    expect(got.width).toBe(got.top);
});

test('the edge follows the messages as they change', async ({page, baseURL}) => {
    await page.clock.install();
    await show(page, baseURL!, 'CONFIG_PENDING');
    const card = page.locator('[data-service-messages]');
    await expect(card).toHaveAttribute('data-edge', 'info');
    // the next answer brings a low battery, then a disturbed one; the page asks every 30 s
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'keys:CONFIG_PENDING,LOW_BAT', url: baseURL!}]);
    await page.clock.runFor(31_000);
    await expect(card).toHaveAttribute('data-edge', 'warn');
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'keys:UNREACH', url: baseURL!}]);
    await page.clock.runFor(31_000);
    await expect(card).toHaveAttribute('data-edge', 'err');
});
