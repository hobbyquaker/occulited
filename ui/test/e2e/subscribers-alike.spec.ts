import {expect, test} from './fixtures';

// occulited task 84 (the maintainer): the subscriber rows alike on the Status and Interfaces pages -
// external clients on both, the same badges, the clients' names - and the Status page's component
// cards titled with the process that serves the interface

test('the component cards carry their process in the title', async ({page}) => {
    await page.goto('/');
    const title = (n: string) => page.locator(`.ol-components [data-component="${n}"] .ol-card-title`);
    await expect(title('BidCos-RF')).toHaveText('BidCos-RF · rfd');
    await expect(title('HmIP-RF')).toHaveText('HmIP-RF · hmipserver');
    await expect(title('HmIP-RF').locator('[data-process-name]')).toHaveText('· hmipserver');
    // muted, on the title's line
    const [tc, pc] = await Promise.all([
        title('HmIP-RF').evaluate((e) => getComputedStyle(e).color),
        title('HmIP-RF').locator('[data-process-name]').evaluate((e) => getComputedStyle(e).color),
    ]);
    expect(pc).not.toBe(tc);
    const box = (await title('HmIP-RF').boundingBox())!;
    const lh = await title('HmIP-RF').evaluate((e) => parseFloat(getComputedStyle(e).lineHeight) || parseFloat(getComputedStyle(e).fontSize) * 1.6);
    expect(box.height).toBeLessThanOrEqual(lh + 1);
});

// occulited B-93: the first /radio/health answer without the units (they come with the next poll): the
// title reads "BidCos-RF · rfd" from the start, and its box does not change when the units arrive
test('the process title keeps its box when the units arrive a poll later (B-93)', async ({page}) => {
    await page.clock.install();
    let calls = 0;
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const body = await response.json();
        if (calls++ === 0) delete body.units;
        await route.fulfill({response, json: body});
    });
    await page.goto('/');
    const title = (n: string) => page.locator(`.ol-components [data-component="${n}"] .ol-card-title`);
    await expect(title('BidCos-RF')).toHaveText('BidCos-RF · rfd');
    await expect(title('HmIP-RF')).toHaveText('HmIP-RF · hmipserver');
    const before = [(await title('BidCos-RF').boundingBox())!, (await title('HmIP-RF').boundingBox())!];
    expect(calls).toBe(1);
    await page.clock.runFor(10_500);
    await expect.poll(() => calls).toBeGreaterThan(1);
    await expect(title('BidCos-RF')).toHaveText('BidCos-RF · rfd');
    await expect(title('HmIP-RF')).toHaveText('HmIP-RF · hmipserver');
    expect([(await title('BidCos-RF').boundingBox())!, (await title('HmIP-RF').boundingBox())!]).toEqual(before);
});

test('the process title stays on one line on a phone', async ({page}) => {
    await page.setViewportSize({width: 320, height: 700});
    await page.goto('/');
    const title = page.locator('.ol-components [data-component="HmIP-RF"] .ol-card-title');
    await expect(title).toHaveText('HmIP-RF · hmipserver');
    expect(await title.evaluate((e) => getComputedStyle(e).whiteSpace)).toBe('nowrap');
    expect(await title.evaluate((e) => e.scrollWidth <= e.parentElement!.clientWidth + 1 || getComputedStyle(e).textOverflow === 'ellipsis')).toBe(true);
});

test('an external subscriber is on both pages, with the same badge and the clients named', async ({page}) => {
    await page.goto('/');
    const card = page.locator('.ol-components [data-component="BidCos-RF"]');
    await card.locator('.ol-if-subs').click();
    const statusExt = card.locator('.ol-if-clients li', {hasText: 'mb_BidCos_RF'}).locator('[data-client-kind]');
    await expect(statusExt).toHaveText('external');
    await expect(statusExt).toHaveClass(/\bol-badge-accent\b/);
    // the Interfaces page's names on the Status page: hmipserver behind BidCos-RF_java, occulited for the own
    await expect(card.locator('.ol-if-clients li', {hasText: 'BidCos-RF_java'})).toHaveText('BidCos-RF_java · hmipserver internal');
    await expect(card.locator('.ol-if-clients li', {hasText: 'occulited_BidCos-RF'})).toHaveText('occulited_BidCos-RF · occulited internal own');

    await page.goto('/system/interfaces');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    const ifExt = rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'}).locator('[data-client-kind]');
    await expect(ifExt).toHaveText('external');
    await expect(ifExt).toHaveClass(/\bol-badge-accent\b/);
    // one row head, one look: the badge has the same box on both pages
    const ib = (await ifExt.boundingBox())!;
    await page.goto('/');
    await card.locator('.ol-if-clients').waitFor();
    const sb = (await statusExt.boundingBox())!;
    expect([Math.round(sb.width), Math.round(sb.height)]).toEqual([Math.round(ib.width), Math.round(ib.height)]);
});

test('the badges and names in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/interfaces');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    await expect(rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'}).locator('[data-client-kind]')).toHaveText('extern');
    await expect(rf.locator('li.ol-sub', {hasText: '1007'}).locator('.ol-sub-id')).toHaveText('1007 · ReGa intern');
    await expect(page.locator('[data-subscribers-link]')).toHaveCount(0);
});

// occulited task 87: the Interfaces page lists lite-rpc's event streams too, in the Status page's
// rows - under occulited's registration, which they come through, or at the end without one
test("the Interfaces page lists lite-rpc's streams as the Status page does", async ({page}) => {
    await page.goto('/system/interfaces');
    const ip = page.locator('.ol-process[data-interface="HmIP-RF"]');
    const rows = ip.locator('li.ol-sub');
    await expect(rows).toHaveCount(4);
    await expect(ip.locator('[data-sub-count]')).toHaveText('4 subscribers');
    // right after occulited's own registration, marked as coming through it
    await expect(rows.nth(2)).toContainText('occulited_HmIP-RF');
    const stream = rows.nth(3);
    await expect(stream).toHaveAttribute('data-stream', '3');
    await expect(stream).toHaveClass(/\bvia\b/);
    await expect(stream.locator('.ol-sub-id')).toHaveText('addon:openccu-loom lite-rpc internal');
    await expect(stream.locator('.ol-sub-url')).toHaveText('SSE · 127.0.0.1');
    await expect(stream.locator('.ol-sub-remove')).toHaveCount(0);
    // BidCos-RF has no registration of occulited in the list: the stream at the end, not indented
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    const admin = rf.locator('li.ol-sub[data-stream="4"]');
    await expect(admin.locator('.ol-sub-id')).toHaveText('session of admin lite-rpc external');
    await expect(admin.locator('.ol-sub-url')).toHaveText('WebSocket · 198.51.100.20');
    await expect(admin).not.toHaveClass(/\bvia\b/);
    await expect(rf.locator('li.ol-sub').last()).toHaveAttribute('data-stream', '4');
    const ib = (await stream.locator('[data-client-kind]').boundingBox())!;
    // occulited task 121: the lite-rpc pill is a badge of the same height as the kind's
    const lb = (await stream.locator('[data-client-literpc]').boundingBox())!;
    expect(Math.round(lb.height)).toBe(Math.round(ib.height));

    // the same row head on the Status page
    await page.goto('/');
    const card = page.locator('.ol-components [data-component="HmIP-RF"]');
    await card.locator('.ol-if-subs').click();
    const st = card.locator('.ol-if-clients li[data-stream="3"]');
    await expect(st).toHaveText('addon:openccu-loom lite-rpc internal');
    const sb = (await st.locator('[data-client-kind]').boundingBox())!;
    expect([Math.round(sb.width), Math.round(sb.height)]).toEqual([Math.round(ib.width), Math.round(ib.height)]);
});

test("lite-rpc's streams on the Interfaces page in German", async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/interfaces');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    await expect(rf.locator('li.ol-sub[data-stream="4"] .ol-sub-id')).toHaveText('Sitzung von admin lite-rpc extern');
});
