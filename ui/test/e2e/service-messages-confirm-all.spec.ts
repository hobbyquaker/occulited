import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 115 (the maintainer, 2026-10-23): "Confirm all" for the sticky service messages - shown while one can
// be confirmed, asking once with the count, confirming each the way one is confirmed (setValue STICKY_*, false over
// lite-rpc, operate rights), a refused one keeps its message and its error, the others go; nothing moves when the button
// comes or goes. The stub's sticky view: three confirmable (B01, B02 STICKY_UNREACH, B03 STICKY_SABOTAGE), a
// STICKY_UNREACH beside its UNREACH (B04, not confirmable), a low battery (B05).

async function sticky(page: Page, baseURL: string, extra: Record<string, string> = {}) {
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'sticky', url: baseURL}, {name: 'stub-app', value: `all-${Math.random().toString(36).slice(2)}`, url: baseURL}, ...Object.entries(extra).map(([name, value]) => ({name, value, url: baseURL}))]);
    await page.goto('/');
}

function writes(page: Page) {
    const seen: unknown[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().includes('/api/rpc/v1/json/')) seen.push(...[r.postDataJSON()].flat().map((c: {params: unknown}) => c.params));
    });
    return seen;
}

test('confirms every confirmable sticky message after one question, and the button goes without moving anything', async ({page, baseURL}) => {
    await sticky(page, baseURL!);
    const card = page.locator('[data-service-messages]');
    const button = card.locator('[data-confirm-all]');
    await expect(button).toHaveText('Confirm all');
    await expect(button).toHaveAttribute('data-confirm-all', '3');
    await expect(button).toBeVisible();
    const sent = writes(page);
    const boxes = async () => ({filters: await card.locator('.sm-filters').boundingBox(), all: await card.locator('[data-all]').boundingBox(), button: await button.boundingBox()});
    const before = await boxes();
    // one question with the count; cancelled, nothing is written
    await button.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Confirm 3 messages?');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(sent).toEqual([]);
    await button.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Confirm all'}).click();
    await expect(card).toHaveAttribute('data-service-messages', '2', {timeout: 5000});
    expect(sent).toEqual([['00010000000B01:0', 'STICKY_UNREACH', false], ['00010000000B02:0', 'STICKY_UNREACH', false], ['00010000000B03:0', 'STICKY_SABOTAGE', false]]);
    // the rest stay: the STICKY_UNREACH beside its cause, the low battery; the button is unseen in its place
    await expect(button).toHaveAttribute('data-confirm-all', '0');
    await expect(button).toBeHidden();
    await expect(button).toBeDisabled();
    // the same messages with the button seen or unseen lay out the same: the card, the pills, Show all and the button's box
    const layout = async () => ({card: await card.boundingBox(), filters: await card.locator('.sm-filters').boundingBox(), all: await card.locator('[data-all]').boundingBox(), button: await button.boundingBox()});
    const hidden = await layout();
    await button.evaluate((b) => b.classList.remove('sm-gone'));
    await expect(button).toBeVisible();
    expect(await layout()).toEqual(hidden);
    expect([hidden.button!.width, hidden.button!.height]).toEqual([before.button!.width, before.button!.height]);
});

test('a refused confirmation keeps its message and its error, the others go on', async ({page, baseURL}) => {
    await sticky(page, baseURL!, {'stub-confirm-fail': '00010000000B02'});
    const card = page.locator('[data-service-messages]');
    await card.locator('[data-confirm-all]').click();
    await page.getByRole('dialog').getByRole('button', {name: 'Confirm all'}).click();
    await expect(card.locator('[data-confirm-all-failed]')).toHaveText('1 not confirmed; their errors are in the list.');
    // its row is shown, with the error beside its own button
    const row = card.locator('[data-device="HmIP-RF:00010000000B02"]');
    await expect(row.locator('[data-confirm-error]')).toContainText('Invalid parameter or value');
    await expect(card).toHaveAttribute('data-service-messages', '3', {timeout: 5000});
    await expect(card.locator('[data-device="HmIP-RF:00010000000B01"]')).toHaveCount(0);
    await expect(card.locator('[data-device="HmIP-RF:00010000000B03"]')).toHaveCount(0);
    await expect(card.locator('[data-confirm-all]')).toHaveAttribute('data-confirm-all', '1');
});

test('in German', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await sticky(page, baseURL!);
    const button = page.locator('[data-confirm-all]');
    await expect(button).toHaveText('Alle bestätigen');
    await button.click();
    await expect(page.getByRole('dialog')).toContainText('3 Meldungen bestätigen?');
    await expect(page.getByRole('dialog').getByRole('button', {name: 'Alle bestätigen'})).toBeVisible();
});

test('no button without a confirmable sticky message, nor without operate rights', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'keys:UNREACH,LOW_BAT', url: baseURL!}]);
    await page.goto('/');
    await expect(page.locator('[data-service-messages="2"]')).toBeVisible();
    await expect(page.locator('[data-confirm-all]')).toBeHidden();
    await expect(page.locator('[data-confirm-all]')).toHaveAttribute('aria-hidden', 'true');
    // read rights: none at all
    await page.route('**/api/auth/v1/state', async (route) => {
        const r = await route.fetch();
        await route.fulfill({response: r, json: {...(await r.json()), role: undefined, level: 'read'}});
    });
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'sticky', url: baseURL!}]);
    await page.reload();
    await expect(page.locator('[data-service-messages="5"]')).toBeVisible();
    await expect(page.locator('[data-confirm-all]')).toHaveCount(0);
});
