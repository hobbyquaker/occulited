import type {Locator, Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 27: the service messages by kind - a symbol and a severity colour per kind, one
// row per device ("war gestört" folded into its "gestört"), at most eight rows before "show all",
// filter chips with counts kept per browser, sorted by severity and then newest first.

test.beforeEach(async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'many', url: baseURL!}]);
});

// occulited task 39: collapsed at the start - only the pills with their counts; a pill shows its
// devices, several pills at once, "Show all" turns every one on and "Hide all" off again
test('collapsed at the start: the pills with their counts, no list', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await expect(card).toHaveAttribute('data-open', '0');
    await expect(card.locator('[data-filter]')).toHaveCount(6); // every kind present (no "other" in the stub)
    await expect(card.locator('[data-filter="lowbat"]')).toHaveText(/Low battery · 3/);
    await expect(card.locator('[data-filter="lowbat"]')).toHaveAttribute('aria-pressed', 'false');
    await expect(card.locator('.sm-item')).toHaveCount(0);
    await expect(card.locator('[data-all]')).toHaveText('Show all');
    // the pills are a comfortable touch target, all of one height
    const heights = await card.locator('[data-filter], [data-all]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().height)));
    expect(Math.min(...heights)).toBeGreaterThanOrEqual(32);
    expect(new Set(heights).size).toBe(1);
    expect(await fitsWindow(page)).toBe(true);
});

test('a pill opens its devices, two pills add up, the last one off collapses again', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    const low = card.locator('[data-filter="lowbat"]');
    await low.click();
    await expect(low).toHaveAttribute('aria-pressed', 'true');
    await expect(card).toHaveAttribute('data-open', '1');
    await expect(card.locator('.sm-item')).toHaveCount(3);
    const config = card.locator('[data-filter="config"]');
    await config.click();
    // B02 has both kinds: one row; B08 adds one
    await expect(card.locator('.sm-item')).toHaveCount(4);
    await low.click();
    await expect(card.locator('.sm-item')).toHaveCount(2);
    await config.click();
    await expect(card.locator('.sm-item')).toHaveCount(0);
    await expect(card).toHaveAttribute('data-open', '0');
    // not kept: a reload starts collapsed again
    await low.click();
    await page.reload();
    await expect(page.locator('[data-service-messages="13"] .sm-item')).toHaveCount(0);
});

test('Show all: every pill on, one row per device, sticky folded, red first, capped at eight; Hide all', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await card.locator('[data-all]').click();
    for (const p of await card.locator('[data-filter]').all()) await expect(p).toHaveAttribute('aria-pressed', 'true');
    await expect(card.locator('[data-all]')).toHaveText('Hide all');
    const items = card.locator('.sm-item');
    await expect(items).toHaveCount(8);
    await expect(items.nth(0)).toHaveAttribute('data-device', 'HmIP-RF:00010000000B01');
    await expect(items.nth(1)).toHaveAttribute('data-device', 'HmIP-RF:00010000000B09');
    const bad = card.locator('[data-device="HmIP-RF:00010000000B01"]');
    await expect(bad.locator('[data-kind]')).toHaveCount(1); // "war gestört" folded into "gestört"
    await expect(bad.locator('[data-kind="unreach"]')).toHaveClass(/sm-red/);
    const kitchen = card.locator('[data-device="HmIP-RF:00010000000B02"]');
    await expect(kitchen.locator('[data-kind]')).toHaveCount(2);
    await expect(kitchen.locator('[data-kind="lowbat"]')).toHaveClass(/sm-orange/);
    await expect(kitchen.locator('[data-kind="config"]')).toHaveClass(/sm-blue/);
    await card.locator('[data-more]').click();
    await expect(items).toHaveCount(11);
    await expect(card.locator('[data-device="HmIP-RF:00010000000B03"] [data-kind="sticky"]')).toHaveClass(/sm-grey/);
    // occulited task 43: a message's chip has the message's own icon - the faults told apart
    for (const [device, icon] of [['B01', 'unlink'], ['B02', 'battery'], ['B03', 'history'], ['B04', 'arrow-up'], ['B05', 'shield'], ['B08', 'settings'], ['B10', 'alert']]) {
        await expect(card.locator(`[data-device="HmIP-RF:00010000000${device}"] [data-message]`).first()).toHaveAttribute('data-icon', icon);
    }
    await expect(card.locator('[data-device="HmIP-RF:00010000000B05"] [data-kind="fault"]')).toHaveClass(/sm-red/);
    await expect(items.last()).toHaveAttribute('data-device', 'HmIP-RF:00010000000B03'); // grey last
    expect(await fitsWindow(page)).toBe(true);
    await card.locator('[data-all]').click();
    await expect(items).toHaveCount(0);
    await expect(card).toHaveAttribute('data-open', '0');
});

test('each row shows the device picture, a placeholder where there is none or it does not load', async ({page}) => {
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await card.locator('[data-all]').click();
    await card.locator('[data-more]').click();
    const kitchen = card.locator('[data-device="HmIP-RF:00010000000B02"] img[data-device-image]');
    await expect(kitchen).toHaveAttribute('src', '/config/img/devices/50/112_hmip-wrc2_thumb.png');
    await expect.poll(() => kitchen.evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(40);
    const box = await kitchen.boundingBox();
    expect(Math.round(box!.width)).toBe(40);
    // no picture known, and one that answers 404
    await expect(card.locator('[data-device="HmIP-RF:00010000000B06"] [data-device-image-none]')).toBeVisible();
    await expect(card.locator('[data-device="HmIP-RF:00010000000B05"] [data-device-image-none]')).toBeVisible();
});

// occulited task 46: eQ-3's dark line drawings are inverted in dark mode - by the system's preference
// and by the explicit dark theme - and stay as drawn in light mode; the placeholder follows the theme
test('the device pictures are inverted in dark mode, both ways of choosing it', async ({page}) => {
    await page.emulateMedia({colorScheme: 'light'});
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await card.locator('[data-all]').click();
    const pic = card.locator('[data-device="HmIP-RF:00010000000B02"] img[data-device-image]');
    await expect(pic).toHaveClass(/ol-device-picture/);
    const look = () => pic.evaluate((i) => {
        const cs = getComputedStyle(i);
        return `${cs.filter} | ${cs.mixBlendMode} | ${cs.backgroundColor}`;
    });
    const placeholder = card.locator('[data-device="HmIP-RF:00010000000B06"] [data-device-image-none]');
    const placeholderColour = () => placeholder.evaluate((e) => getComputedStyle(e).backgroundColor);
    expect(await look()).toBe('none | multiply | rgba(0, 0, 0, 0)');
    const lightPlaceholder = await placeholderColour();
    await page.emulateMedia({colorScheme: 'dark'});
    expect(await look()).toBe('invert(1) hue-rotate(180deg) | screen | rgba(0, 0, 0, 0)');
    expect(await placeholderColour()).not.toBe(lightPlaceholder);
    // the explicit theme wins over the system's preference, either way
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    expect(await look()).toBe('none | multiply | rgba(0, 0, 0, 0)');
    await page.emulateMedia({colorScheme: 'light'});
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    expect(await look()).toBe('invert(1) hue-rotate(180deg) | screen | rgba(0, 0, 0, 0)');
});

// occulited task 52: some of eQ-3's pictures have an opaque white background, others a transparent one.
// Blended with the card - screen in dark mode, multiply in light - neither shows a square of its own: the
// background is the card's colour, the lines are light on dark and dark on light. Measured on the screen
// (a screenshot of the row, read back through a canvas), since a computed style cannot tell a black square.
type RGB = [number, number, number];
async function samples(page: Page, img: Locator) {
    await expect.poll(() => img.evaluate((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth)).toBe(40);
    await img.scrollIntoViewIfNeeded();
    const box = (await img.boundingBox())!;
    // the clip starts 8px left of the picture: x 2 is the card, x 8.. the picture
    const shot = await page.screenshot({clip: {x: box.x - 8, y: box.y, width: 48, height: 40}, scale: 'css', animations: 'disabled'});
    const at = await page.evaluate(async (b64) => {
        const im = new Image();
        im.src = `data:image/png;base64,${b64}`;
        await im.decode();
        const c = document.createElement('canvas');
        c.width = im.width;
        c.height = im.height;
        const g = c.getContext('2d')!;
        g.drawImage(im, 0, 0);
        const px = (x: number, y: number) => Array.from(g.getImageData(x, y, 1, 1).data.slice(0, 3));
        // the stub's drawing: a frame from 6 to 34 (4px wide) and a bar across rows 18..21, white or clear around it
        return {card: px(2, 20), background: px(8 + 2, 2), inner: px(8 + 14, 13), frame: px(8 + 7, 25), bar: px(8 + 20, 19)};
    }, shot.toString('base64'));
    return at as Record<'card' | 'background' | 'inner' | 'frame' | 'bar', RGB>;
}
const near = (a: RGB, b: RGB) => a.every((v, i) => Math.abs(v - b[i]) <= 8);
const light = (c: RGB) => Math.min(...c) >= 200;
const dark = (c: RGB) => Math.max(...c) <= 60;

for (const [scheme, theme] of [['dark', null], ['light', 'dark'], ['light', null], ['dark', 'light']] as const) {
    const mode = (theme ?? scheme) as 'dark' | 'light';
    test(`opaque and transparent device pictures show no square of their own: ${theme ? `theme ${theme} over a ${scheme} preference` : `${scheme} preference`}`, async ({page}) => {
        await page.emulateMedia({colorScheme: scheme});
        await page.goto('/');
        if (theme) await page.evaluate((th) => document.documentElement.setAttribute('data-theme', th), theme);
        const card = page.locator('[data-service-messages="13"]');
        await card.locator('[data-all]').click();
        await card.locator('[data-more]').click();
        // light mode: a tinted card, so that an opaque white background would show as a white square
        if (mode === 'light') await card.evaluate((c) => (c.style.background = 'rgb(208, 224, 240)'));
        const opaque = card.locator('[data-device="HmIP-RF:00010000000B07"] img[data-device-image]');
        const clear = card.locator('[data-device="HmIP-RF:00010000000B08"] img[data-device-image]');
        await expect(opaque).toHaveAttribute('src', /stub_opaque/);
        await expect(clear).toHaveAttribute('src', /stub_transparent/);
        if (process.env.OL_SHOTS) await card.locator('.sm-list').screenshot({path: `${process.env.OL_SHOTS}/task52-${test.info().project.name}-${scheme}${theme ? `-theme-${theme}` : ''}.png`});
        for (const img of [opaque, clear]) {
            const s = await samples(page, img);
            if (mode === 'light') expect(s.card).toEqual([208, 224, 240]);
            else expect(dark(s.card)).toBe(true);
            // no square: around and inside the frame the picture shows the card
            expect(near(s.background, s.card), `background ${s.background} vs card ${s.card}`).toBe(true);
            expect(near(s.inner, s.card), `inside ${s.inner} vs card ${s.card}`).toBe(true);
            // the lines: light on the dark card, dark on the light one
            for (const line of [s.frame, s.bar]) expect(mode === 'dark' ? light(line) : dark(line), `line ${line}`).toBe(true);
        }
    });
}

test('the pills in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await expect(card.locator('[data-filter="unreach"]')).toHaveText(/Kommunikation gestört · 2/);
    await expect(card.locator('[data-all]')).toHaveText('Alle anzeigen');
    await card.locator('[data-all]').click();
    await expect(card.locator('[data-all]')).toHaveText('Alle ausblenden');
    await expect(card.locator('[data-more]')).toHaveText('Alle Geräte zeigen (11)');
});

// openccu-lite task 82: a sticky message whose cause is gone is confirmed on the device, as the
// CCU's "Bestätigen" does - setValue(<device>:0, STICKY_UNREACH, false) over lite-rpc - and goes
test('a sticky message whose cause is gone is confirmed, nothing else offers a button', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `confirm-${test.info().testId}`, url: baseURL!}]);
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await card.locator('[data-all]').click();
    await card.locator('[data-more]').click();
    // one button on the whole list: "war gestört" alone; the folded one beside "gestört" has none
    await expect(card.locator('[data-confirm]')).toHaveCount(1);
    const write = page.waitForRequest((r) => r.url().includes('/api/rpc/v1/json/HmIP-RF') && r.method() === 'POST');
    await card.locator('[data-confirm="00010000000B03:0:STICKY_UNREACH"]').click();
    const body = (await write).postDataJSON();
    expect([body].flat()[0]).toMatchObject({method: 'setValue', params: ['00010000000B03:0', 'STICKY_UNREACH', false]});
    await expect(page.locator('[data-service-messages="12"]')).toBeVisible();
    await expect(page.locator('[data-device="HmIP-RF:00010000000B03"]')).toHaveCount(0);
});

test('a confirm the interface process refuses says so and keeps the message', async ({page, baseURL}) => {
    await page.context().addCookies([
        {name: 'stub-app', value: `confirm-${test.info().testId}`, url: baseURL!},
        {name: 'stub-confirm', value: 'fail', url: baseURL!},
    ]);
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await card.locator('[data-all]').click();
    await card.locator('[data-more]').click();
    await card.locator('[data-confirm="00010000000B03:0:STICKY_UNREACH"]').click();
    await expect(card.locator('[data-confirm-error]')).toContainText('Invalid parameter or value');
    await expect(card.locator('[data-device="HmIP-RF:00010000000B03"]')).toHaveCount(1);
});

test('without lite-rpc operate rights there is no confirm button', async ({page}) => {
    await page.route('**/api/auth/v1/state', async (route) => {
        const r = await route.fetch();
        await route.fulfill({response: r, json: {...(await r.json()), role: undefined, level: 'read'}});
    });
    await page.goto('/');
    const card = page.locator('[data-service-messages="13"]');
    await card.locator('[data-all]').click();
    await card.locator('[data-more]').click();
    await expect(card.locator('[data-device="HmIP-RF:00010000000B03"]')).toHaveCount(1);
    await expect(card.locator('[data-confirm]')).toHaveCount(0);
});
