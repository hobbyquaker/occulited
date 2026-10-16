import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// task 150: the radio load is the Status page's alone - the duty cycle ring against the 1 % budget
// with its history (tasks 53, 54), and carrier sense in a panel of its own, only when a radio
// reports it (task 68 named its source). The Interfaces page has no radio load section any more.

test('the Interfaces page has no radio load section', async ({page}) => {
    await page.goto('/radio');
    const heads = page.locator('h2');
    await expect(heads.nth(0)).toHaveText('Modules');
    // the gateways, the HmIP access points and the LAN devices (tasks 199, 217, 220) are System ->
    // LAN devices since openccu-lite task 222
    await expect(heads.nth(1)).toHaveText('Connections');
    // openccu-lite task 218's network radio board is the LAN devices page's since task 239
    // the clients on this system after the connections (2026-09-19)
    await expect(heads.nth(2)).toHaveText('Registered clients');
    // task 79's trace switch is the Remote access page's since openccu-lite task 224
    // task 97: rfd's device descriptions and their writable layer, when the image has the layer
    await expect(heads.nth(3)).toHaveText('Device descriptions');
    // the radio firmware is the Updates page's since 2026-09-20
    await expect(heads).toHaveCount(4);
    await expect(page.getByRole('heading', {name: 'Radio firmware'})).toHaveCount(0);
    await expect(page.locator('.ol-gauge')).toHaveCount(0);
});

test('Status: the duty cycle alone in its ring, carrier sense in a panel of its own', async ({page}) => {
    await page.goto('/');
    const dc = page.locator('.ol-gauge', {hasText: 'Duty cycle'});
    await expect(dc.locator('.num')).toHaveText('11 %');
    await expect(dc.locator('.det')).toHaveText('of the 1 % budget');
    await expect(dc).not.toContainText('arrier sense');
    await expect(dc.locator('.ol-card-foot text.tick')).toHaveText('50 %');
    const cs = page.locator('.ol-gauge', {hasText: 'Carrier sense'});
    await expect(cs.locator('.num')).toHaveText('2 %');
    // occulited B-76: carrier sense is HmIP's, the subtitle names HmIP-RF alone
    await expect(cs.locator('.ol-card-sub')).toHaveText('HMIP-RFUSB 0000000A02 · HmIP-RF');
    await expect(cs.locator('.det')).toHaveText('of the time the channel was heard busy');
    // task 68: the tooltip names where the figure came from - here channel 0 of the module's device
    await expect(cs.locator('.det')).toHaveAttribute('title', "Read from channel 0 of the radio module's own device.");
    await expect(cs).not.toHaveClass(/\b(warn|err)\b/);
    // task 151: its history in the foot, with the 10 % line; task 152: the last hour's 14 % burst
    // as a lighter red arc behind the plain 2 %
    await expect(cs.locator('.ol-card-foot svg')).toBeVisible();
    await expect(cs.locator('.ol-card-foot line.mark')).toHaveCount(1);
    await expect(cs.locator('.ol-card-foot text.tick').first()).toHaveText('20 %');
    // the five polls without a figure break the line, and so do the two polls the stacks were down
    await expect(cs.locator('.ol-card-foot polyline.line')).toHaveCount(3);
    await expect(cs.locator('.peaktext')).toHaveText('max 14 % (1 h)');
    await expect(cs).toHaveClass(/\bpeakerr\b/);
    expect(await fitsWindow(page)).toBe(true);
});

test('Status: carrier sense is red above 10 % and plain at 10 %', async ({page}) => {
    for (const [v, red] of [[11, true], [10, false]] as const) {
        await page.unroute('**/api/system/v1/radio/health');
        await page.route('**/api/system/v1/radio/health', async (route) => {
            const response = await route.fetch();
            const body = (await response.json()) as {interfaces: {interface: string; carrier_sense?: number}[]};
            body.interfaces.find((i) => i.interface === 'HmIP-RF')!.carrier_sense = v;
            await route.fulfill({response, json: body});
        });
        await page.goto('/');
        const cs = page.locator('.ol-gauge', {hasText: 'Carrier sense'});
        await expect(cs.locator('.num')).toHaveText(`${v} %`);
        if (red) {
            await expect(cs).toHaveClass(/\berr\b/);
            await expect(cs.locator('.note')).toHaveText("the channel is busy: interference or a neighbour's traffic");
        } else {
            await expect(cs).not.toHaveClass(/\berr\b/);
            await expect(cs.locator('.note')).toHaveCount(0);
        }
    }
});

test('Status: no second arc without a history', async ({page}) => {
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {history: Record<string, unknown>};
        body.history = {};
        await route.fulfill({response, json: body});
    });
    await page.goto('/');
    const dc = page.locator('.ol-gauge', {hasText: 'Duty cycle'});
    await expect(dc.locator('.num')).toHaveText('11 %');
    await expect(dc.locator('circle.peak')).toHaveCount(0);
    await expect(dc.locator('.peaktext')).toHaveCount(0);
});

test('Status: carrier sense from the interface list, and no panel when no radio reports it', async ({page}) => {
    type Entry = {interface: string; carrier_sense?: number; carrier_sense_source?: string};
    const patchWith = async (patch: (h: Entry) => void) => {
        await page.unroute('**/api/system/v1/radio/health');
        await page.route('**/api/system/v1/radio/health', async (route) => {
            const response = await route.fetch();
            const body = (await response.json()) as {interfaces: Entry[]};
            patch(body.interfaces.find((i) => i.interface === 'HmIP-RF')!);
            await route.fulfill({response, json: body});
        });
    };
    await patchWith((h) => Object.assign(h, {carrier_sense: 4, carrier_sense_source: 'interface'}));
    await page.goto('/');
    const cs = page.locator('.ol-gauge', {hasText: 'Carrier sense'});
    await expect(cs.locator('.num')).toHaveText('4 %');
    await expect(cs.locator('.det')).toHaveAttribute('title', 'Reported by the interface process with its radio interfaces.');
    // 0 % is a figure, not an absence
    await patchWith((h) => Object.assign(h, {carrier_sense: 0}));
    await page.goto('/');
    await expect(cs.locator('.num')).toHaveText('0 %');
    // a module that has not reported it yet, and one that never does: no panel
    for (const patch of [(h: Entry) => delete h.carrier_sense, (h: Entry) => { delete h.carrier_sense; delete h.carrier_sense_source; }]) {
        await patchWith(patch);
        await page.goto('/');
        await expect(page.locator('.ol-gauge', {hasText: 'Duty cycle'})).toBeVisible();
        await expect(cs).toHaveCount(0);
    }
});

test('a radio at 75 % of its budget is amber with the note in words', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-duty', value: '75', url: baseURL!}]);
    await page.goto('/');
    const status = page.locator('.ol-gauge', {hasText: 'Duty cycle'});
    await expect(status.locator('.num')).toHaveText('75 %');
    await expect(status).toHaveClass(/\bwarn\b/);
    await expect(status).toContainText('close to the legal limit');
});

// task 156: one duty cycle panel per physical radio - both stacks through multimacd are one, the
// stacks on two modules are two, each with its own graph, span and maximum
test('Status: one duty cycle panel for a shared module, two for two modules', async ({page, context, baseURL}) => {
    await page.goto('/');
    const dc = page.locator('.ol-gauge', {hasText: 'Duty cycle'});
    await expect(dc).toHaveCount(1);
    await expect(dc.locator('.ol-card-sub')).toHaveText('HMIP-RFUSB 0000000A02 · BidCos-RF, HmIP-RF');
    await expect(dc.locator('.num')).toHaveText('11 %');
    await expect(dc).toContainText('BidCos-RF 3 % · HmIP-RF 11 % — one radio, counted once per stack; the higher is shown');

    await context.addCookies([{name: 'stub-radios', value: '2', url: baseURL!}]);
    await page.goto('/');
    await expect(dc).toHaveCount(2);
    const bidcos = dc.filter({hasText: 'HM-CFG-USB-2 JEQ9000002'});
    const hmip = dc.filter({hasText: 'HMIP-RFUSB 0000000A02'});
    await expect(bidcos.locator('.ol-card-sub')).toHaveText('HM-CFG-USB-2 JEQ9000002 · BidCos-RF');
    await expect(bidcos.locator('.num')).toHaveText('3 %');
    await expect(hmip.locator('.num')).toHaveText('11 %');
    await expect(bidcos).not.toContainText('counted once per stack');
    // each its own maximum and span: the HmIP burst of 45 % is outside the last hour
    await expect(bidcos.locator('.peaktext')).toHaveText('max 8 % (1 h)');
    await expect(hmip.locator('.peaktext')).toHaveText('max 21 % (1 h)');
    await hmip.getByRole('button', {name: '6 h'}).click();
    await expect(hmip.locator('.peaktext')).toHaveText('max 45 % (6 h)');
    await expect(bidcos.locator('.peaktext')).toHaveText('max 8 % (1 h)');
    // carrier sense: only the HmIP stick reports it, so one panel
    await expect(page.locator('.ol-gauge', {hasText: 'Carrier sense'})).toHaveCount(1);
    await expect(page.locator('.ol-gauge', {hasText: 'Carrier sense'}).locator('.ol-card-sub')).toHaveText('HMIP-RFUSB 0000000A02 · HmIP-RF');
});
