import {expect, test} from './fixtures';

// occulited B-76 (the maintainer: "there is no carrier sense for bidcos"): the Carrier sense panel of
// a module that carries both stacks names HmIP-RF alone; the Duty cycle panel keeps both.

const card = (page: import('@playwright/test').Page, title: string) => page.locator('.ol-card', {has: page.locator('.ol-card-title', {hasText: title})});

test('a dual-stack module: Carrier sense names HmIP-RF, Duty cycle both', async ({page}) => {
    await page.goto('/');
    await expect(card(page, 'Carrier sense').locator('.ol-card-sub')).toHaveText('HMIP-RFUSB 0000000A02 · HmIP-RF');
    await expect(card(page, 'Duty cycle').locator('.ol-card-sub')).toHaveText('HMIP-RFUSB 0000000A02 · BidCos-RF, HmIP-RF');
});

test('even when rfd reports a carrier level, the panel is HmIP-RF\'s', async ({page}) => {
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const h = await response.json();
        for (const i of h.interfaces) if (i.interface === 'BidCos-RF') i.carrier_sense = 30;
        await route.fulfill({response, json: h});
    });
    await page.goto('/');
    const cs = card(page, 'Carrier sense');
    await expect(cs.locator('.ol-card-sub')).toHaveText('HMIP-RFUSB 0000000A02 · HmIP-RF');
    // the value is HmIP-RF's 2 %, not BidCos-RF's 30 %
    await expect(cs).toContainText('2 %');
    await expect(cs).not.toContainText('30 %');
});
