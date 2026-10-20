import {expect, test} from './fixtures';
import {pageWidth} from './scroll';

// occulited task 13: the interface process cards under Components - the radio and its way as the
// subtitle (a link to the connections), the events per minute with their history (task 17: no
// outgoing rate, "Events" in both languages), and the subscribers as one number, split and named on
// hover or tap - lite-rpc's streams among them, under occulited's own registration (B-45)

test('an interface card names its radio, its events per minute, and its subscribers', async ({page}) => {
    await page.goto('/');
    const card = page.locator('.ol-components [data-component="HmIP-RF"]');
    await expect(card.locator('.ol-card-body')).toHaveText('up');
    const sub = card.locator('.ol-card-sub a');
    await expect(sub).toHaveText('HmIP-RFUSB · via multimacd');
    await expect(card.locator('.ol-card-detail')).toHaveText('BC0A08');
    // the stub's last poll: 12 + (89 % 5) * 0.5 events a minute; nothing about sending (task 17)
    await expect(card.locator('[data-rate="in"]')).toHaveText('Events 14/min');
    await expect(card.locator('[data-rate="out"]')).toHaveCount(0);
    await expect(card).not.toContainText('sent');
    // the history: one line, broken by the two polls the stack was down
    const chart = card.locator('.chart');
    await expect(chart.locator('polyline.line')).toHaveCount(2);
    await expect(chart.locator('polyline.line2')).toHaveCount(0);
    // the last hour peaks at 72/min: the top is the next rate step with room
    await expect(chart.locator('text.tick')).toHaveText('100 /min');
    // a tap shows the poll's rate per minute
    const box = (await chart.locator('svg').boundingBox())!;
    await chart.locator('svg').click({position: {x: box.width - 1, y: box.height / 2}});
    await expect(chart.locator('text.value')).toHaveText(/ · 14 \/min$/);
    // subscribers: one number, the split and the names behind the pointer and behind a tap; the
    // addon's lite-rpc stream is one of them, listed under occulited's own registration (B-45)
    const subs = card.locator('.ol-if-subs');
    await expect(subs).toHaveText('3 subscribers');
    await expect(subs).toHaveAttribute('title', /^1 external, 2 internal\nmb_HmIP_RF - external\nocculited_HmIP-RF · occulited - internal\n {2}↳ addon:openccu-loom \(stream via occulited\) - internal$/);
    await subs.click();
    await expect(card.locator('.ol-if-clients li')).toHaveText(['mb_HmIP_RF external', 'occulited_HmIP-RF · occulited internal own', 'addon:openccu-loom (stream via occulited) internal']);
    await expect(card.locator('.ol-if-clients li.via')).toHaveAttribute('data-stream', '3');
    // occulited task 82: the external badge in the accent colour, the internal ones neutral, one box
    const ext = card.locator('.ol-if-clients [data-client-kind="external"]');
    const int = card.locator('.ol-if-clients [data-client-kind="internal"]').first();
    await expect(ext).toHaveClass(/\bol-badge-accent\b/);
    await expect(int).not.toHaveClass(/\bol-badge-accent\b/);
    const [ec, ic] = await Promise.all([ext.evaluate((e) => getComputedStyle(e).color), int.evaluate((e) => getComputedStyle(e).color)]);
    expect(ec).not.toBe(ic);
    const [eb, ib] = await Promise.all([ext.boundingBox(), int.boundingBox()]);
    expect(eb!.height).toBe(ib!.height);
    await expect(card.locator('.ol-if-clients li.via .ol-sublabel-text')).toHaveAttribute('title', 'addon:openccu-loom (stream via occulited) · SSE · 127.0.0.1');
    // the subtitle leads to the connections
    await sub.click();
    await expect(page).toHaveURL(/\/system\/interfaces#connections$/);
});

// occulited B-79: a lone burst of 500 a minute - an occulited restart before the fix - does not
// scale the band: the top stays at the real values' step, the burst is clipped at the top with a
// mark there, and the pointer still reads its value
test('an event rate chart scales past a lone burst', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-rate-spike', value: '1', url: baseURL!}]);
    await page.goto('/');
    const chart = page.locator('.ol-components [data-component="BidCos-RF"] .chart');
    // the stub's BidCos-RF line: 3 to 6.6 a minute - the top is 10, not 1000
    await expect(chart.locator('text.tick')).toHaveText('10 /min');
    const clip = chart.locator('polygon.clip');
    await expect(clip).toHaveCount(1);
    const box = (await chart.locator('svg').boundingBox())!;
    const at = (await clip.boundingBox())!;
    await chart.locator('svg').click({position: {x: at.x + at.width / 2 - box.x, y: box.height / 2}});
    await expect(chart.locator('text.value')).toHaveText(/ · 500 \/min$/);
    // the HmIP-RF line's 72 among 12 to 14 is no lone outlier: its band reaches it
    await expect(page.locator('.ol-components [data-component="HmIP-RF"] .chart text.tick')).toHaveText('100 /min');
    await expect(page.locator('.ol-components [data-component="HmIP-RF"] .chart polygon.clip')).toHaveCount(0);
});

test('the interface cards in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/');
    const card = page.locator('.ol-components [data-component="BidCos-RF"]');
    await expect(card.locator('.ol-card-sub a')).toHaveText('HmIP-RFUSB · via multimacd');
    // task 17: "Events" in German too, per minute; 3 + (89 % 4) * 1.2 with the decimal comma
    await expect(card.locator('.ol-if-k')).toHaveText('Events');
    await expect(card.locator('[data-rate="in"]')).toHaveText('Events 4,2/min');
    await expect(card).not.toContainText('Telegramm');
    await expect(card).not.toContainText('gesendet');
    await expect(card.locator('.chart text.tick')).toHaveText(/ \/min$/);
    // B-45: a browser's stream from the LAN is an external subscriber
    await expect(card.locator('.ol-if-subs')).toHaveText('4 Abonnenten');
    await card.locator('.ol-if-subs').click();
    await expect(card.locator('.ol-if-clients > div.ol-muted')).toHaveText('2 extern, 2 intern');
    await expect(card.locator('.ol-if-clients li.via')).toHaveText('Sitzung von admin (Stream über occulited) extern');
});

test('the radio line falls back to the address, and a card without counts has no figures', async ({page}) => {
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {interfaces: Record<string, unknown>[]; rates?: unknown; subscribers?: unknown; answering?: string[]};
        for (const i of body.interfaces) {
            delete i.module;
            delete i.path;
        }
        body.answering = ['CUxD'];
        await route.fulfill({response, json: body});
    });
    await page.goto('/');
    await expect(page.locator('[data-component="HmIP-RF"] .ol-card-sub')).toHaveText('BC0A08');
    await expect(page.locator('[data-component="HmIP-RF"] .ol-card-detail')).toHaveCount(0);
    const cuxd = page.locator('[data-component="CUxD"]');
    await expect(cuxd.locator('.ol-card-body')).toHaveText('up');
    await expect(cuxd.locator('.ol-if-figs, .chart')).toHaveCount(0);
});

test('the cards fit a phone, with the subscriber list open, in both themes', async ({page}) => {
    for (const scheme of ['light', 'dark'] as const) {
        await page.emulateMedia({colorScheme: scheme});
        await page.setViewportSize({width: 360, height: 780});
        await page.goto('/');
        const card = page.locator('[data-component="HmIP-RF"]');
        await expect(card.locator('.chart svg')).toBeVisible();
        // task 83: the open list is remembered, so the second round finds it open
        if (scheme === 'light') await card.locator('.ol-if-subs').click();
        await expect(card.locator('.ol-if-clients li')).toHaveCount(3);
        expect(await pageWidth(page)).toBeLessThanOrEqual(360);
    }
});
