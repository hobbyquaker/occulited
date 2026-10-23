import {expect, test} from './fixtures';

// occulited task 112 (the maintainer's M3): the thermostat tile shows its mode as a small round badge at the bottom
// right of its disc - clock Auto, hand Manual, case Holiday/Party. HmIP reads SET_POINT_MODE, BidCos CONTROL_MODE;
// during a boost the badge keeps the mode that applies after it. Display only; no badge without a mode datapoint.

async function bad(page: import('@playwright/test').Page, baseURL: string) {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL}]);
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['BidCos-RF.KEQ0012345:2'].enums = ['rooms/og/bad'];
        json.objects['BidCos-RF.KEQ0012346:4'].enums = ['rooms/og/bad'];
        json.objects['VirtualDevices.INT0000011:1'] = {name: 'Heizgruppe Bad', enums: ['rooms/og/bad'], meta: {}};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/og/bad');
}

test('the mode per thermostat, a boost keeps the mode after it, the badge moves nothing', async ({page, baseURL}) => {
    await bad(page, baseURL!);
    const hmip = page.locator('[data-app-tile="HmIP-RF.000ABC:1"]');
    const wall = page.locator('[data-app-tile="BidCos-RF.KEQ0012345:2"]');
    const rt = page.locator('[data-app-tile="BidCos-RF.KEQ0012346:4"]');
    const group = page.locator('[data-app-tile="VirtualDevices.INT0000011:1"]');
    await expect(hmip.locator('[data-app-mode]')).toHaveAttribute('data-app-mode', 'auto');
    await expect(hmip.locator('[data-app-mode]')).toHaveAttribute('title', 'Auto');
    await expect(hmip.locator('[data-app-mode]')).toHaveAttribute('aria-label', 'Mode: Auto');
    await expect(wall.locator('[data-app-mode]')).toHaveAttribute('data-app-mode', 'auto');
    await expect(group.locator('[data-app-mode]')).toHaveAttribute('data-app-mode', 'auto');
    const manual = rt.locator('[data-app-mode]');
    await expect(manual).toHaveAttribute('data-app-mode', 'manual');
    await expect(manual).toHaveAttribute('title', 'Manual');
    // Manual in its own violet, Auto in the muted colour
    const colour = (l: import('@playwright/test').Locator) => l.evaluate((e) => getComputedStyle(e).color);
    const token = await page.evaluate(() => {
        const p = document.createElement('span');
        p.style.color = 'var(--ol-manual)';
        document.body.append(p);
        const c = getComputedStyle(p).color;
        p.remove();
        return c;
    });
    expect(await colour(manual)).toBe(token);
    expect(await colour(hmip.locator('[data-app-mode]'))).not.toBe(token);
    // the badge sits at the disc's bottom right, overlapping its edge, inside the disc's box but for a few pixels
    for (const tile of [hmip, rt]) {
        const disc = (await tile.locator('.app-circle').boundingBox())!;
        const b = (await tile.locator('[data-app-mode]').boundingBox())!;
        expect(b.x + b.width).toBeGreaterThan(disc.x + disc.width - 1);
        expect(b.x + b.width).toBeLessThanOrEqual(disc.x + disc.width + 4);
        expect(b.y + b.height).toBeGreaterThan(disc.y + disc.height - 1);
        expect(b.y + b.height).toBeLessThanOrEqual(disc.y + disc.height + 4);
        expect(b.width).toBe(b.height);
    }
    // the badge moves nothing: the disc, the name and the pair keep their boxes beside a tile without one
    const boxes = async () => ({tile: await rt.boundingBox(), disc: await rt.locator('.app-circle').boundingBox(), badge: await manual.boundingBox(), name: await rt.locator('.app-tile-name').boundingBox(), pair: await rt.locator('[data-app-thermo]').boundingBox()});
    const before = await boxes();
    // a BidCos boost: CONTROL_MODE 3, and the badge still says the manual mode it returns to
    const [req] = await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')), rt.locator('[data-app-boost-btn]').click()]);
    expect(req.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['KEQ0012346:4', 'BOOST_MODE', true]});
    await expect(rt).toHaveAttribute('data-app-tone', 'boost');
    await expect(manual).toHaveAttribute('data-app-mode', 'manual');
    expect(await boxes()).toEqual(before);
    // display only: a click on the badge is the disc's or the card's, never a mode write
    await expect(manual).toHaveCSS('pointer-events', 'none');
});

test('in German', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await bad(page, baseURL!);
    const m = page.locator('[data-app-tile="BidCos-RF.KEQ0012346:4"] [data-app-mode]');
    await expect(m).toHaveAttribute('title', 'Manuell');
    await expect(m).toHaveAttribute('aria-label', 'Modus: Manuell');
});

test('no badge on a tile without a mode', async ({page, baseURL}) => {
    await bad(page, baseURL!);
    // the four thermostats of Bad, once their channels are in
    await expect(page.locator('[data-app-mode]')).toHaveCount(4);
    await expect(page.locator('[data-app-tile][data-app-widget=""]')).toHaveCount(0);
    for (const tile of await page.locator('[data-app-tile]').all()) {
        if ((await tile.getAttribute('data-app-widget')) !== 'thermostat') await expect(tile.locator('[data-app-mode]')).toHaveCount(0);
    }
});
