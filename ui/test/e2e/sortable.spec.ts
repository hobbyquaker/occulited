import {expect, test, type Locator, type Page} from '@playwright/test';
import {PORT} from './scroll';

// Task 162: every list ordered by hand has a drag handle and no ↑/↓ buttons - the firewall's rules
// and the LED states here, the addon dropdown in addon-pins.spec.ts. The handle is dragged with a
// mouse or a finger (touch events through the devtools protocol on the phone project), moved with
// ↑/↓ when it has the focus, and Alt+↑/↓ moves the row from any other control in it. Escape ends a
// drag with nothing moved; near the edge of the scrolling area the page scrolls along. Each test
// keeps its own firewall (stub-fw) and LED configuration (stub-led).

let n = 0;
async function own(page: Page, baseURL: string) {
    const id = `sort-${process.pid}-${Date.now()}-${n++}`;
    await page.context().addCookies([
        {name: 'stub-fw', value: id, url: baseURL},
        {name: 'stub-led', value: id, url: baseURL},
    ]);
}
const ruleRows = (page: Page) => page.locator('table.fw-rules tr[data-rule]');
const ruleIds = (page: Page) => ruleRows(page).evaluateAll((els) => els.map((e) => e.getAttribute('data-rule')!));
const ledIds = (page: Page) => page.locator('[data-led-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-led-row')!));
const status = (page: Page) => page.getByRole('status').filter({hasText: /position/});

/** a drag of `grip` to just inside the top (or bottom) of `target`, by a finger on a phone, else by the mouse */
async function dragTo(page: Page, grip: Locator, target: Locator, where: 'above' | 'below', isMobile: boolean) {
    await grip.scrollIntoViewIfNeeded();
    const g = (await grip.boundingBox())!;
    const r = (await target.boundingBox())!;
    const x = g.x + g.width / 2;
    const y0 = g.y + g.height / 2;
    const y1 = where === 'above' ? r.y + 4 : r.y + r.height - 4;
    if (isMobile) {
        const cdp = await page.context().newCDPSession(page);
        await cdp.send('Input.dispatchTouchEvent', {type: 'touchStart', touchPoints: [{x, y: y0}]});
        for (let i = 1; i <= 8; i++) await cdp.send('Input.dispatchTouchEvent', {type: 'touchMove', touchPoints: [{x, y: y0 + ((y1 - y0) * i) / 8}]});
        await cdp.send('Input.dispatchTouchEvent', {type: 'touchEnd', touchPoints: []});
        return;
    }
    await page.mouse.move(x, y0);
    await page.mouse.down();
    await page.mouse.move(x, y0 + (y1 > y0 ? 4 : -4));
    await page.mouse.move(x, y1, {steps: 8});
    await page.mouse.up();
}

test('no ↑/↓ buttons are left: the firewall and the LED states have handles', async ({page, baseURL}) => {
    await own(page, baseURL!);
    await page.goto('/system/firewall');
    await expect(ruleRows(page)).toHaveCount(18);
    await expect(ruleRows(page).locator('button.ol-grip')).toHaveCount(18);
    await expect(page.locator('table.fw-rules button').filter({hasText: /^[↑↓]$/})).toHaveCount(0);
    await expect(page.getByRole('button', {name: /^Move (up|down)$/})).toHaveCount(0);
    await expect(ruleRows(page).first().getByRole('button', {name: 'Move Rule 1'})).toBeVisible();

    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const rows = page.locator('[data-led-row]');
    await expect(rows.first()).toBeVisible();
    await expect(rows.locator('button.ol-grip')).toHaveCount(await rows.count());
    await expect(page.locator('[data-led-advanced] button').filter({hasText: /^[↑↓]$/})).toHaveCount(0);
    await expect(page.locator('[data-led-fixed] .ol-grip')).toHaveCount(0);
});

test('a firewall rule dragged by its handle: the drop line, the new order, the diff', async ({page, baseURL, isMobile}) => {
    await own(page, baseURL!);
    await page.goto('/system/firewall');
    await expect(ruleRows(page)).toHaveCount(18);
    const before = await ruleIds(page);
    if (!isMobile) {
        // the drop line while the mouse holds the third rule over the first
        const g = (await ruleRows(page).nth(2).locator('.ol-grip').boundingBox())!;
        const r = (await ruleRows(page).first().boundingBox())!;
        await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
        await page.mouse.down();
        await page.mouse.move(g.x + g.width / 2, g.y);
        await page.mouse.move(g.x + g.width / 2, r.y + 4, {steps: 6});
        await expect(ruleRows(page).first()).toHaveClass(/ol-drop-before/);
        await expect(ruleRows(page).nth(2)).toHaveClass(/ol-dragging/);
        await page.mouse.up();
    } else {
        await dragTo(page, ruleRows(page).nth(2).locator('.ol-grip'), ruleRows(page).first(), 'above', true);
    }
    await expect.poll(() => ruleIds(page)).toEqual([before[2], before[0], before[1], ...before.slice(3)]);
    await expect(ruleRows(page).nth(2)).not.toHaveClass(/ol-dragging/);
    await expect(page.locator('[data-diff]')).toContainText('The order of the rules changed');
    await expect(status(page)).toHaveText('Rule 3 is now at position 1 of 18');
    // and down again, below the third
    await dragTo(page, ruleRows(page).first().locator('.ol-grip'), ruleRows(page).nth(2), 'below', isMobile);
    await expect.poll(() => ruleIds(page)).toEqual(before);
});

test('only the handle starts a drag, and Escape ends one with nothing moved', async ({page, baseURL, isMobile}) => {
    test.skip(isMobile, 'a mouse and a key');
    await own(page, baseURL!);
    await page.goto('/system/firewall');
    await expect(ruleRows(page)).toHaveCount(18);
    const before = await ruleIds(page);
    // a press on a field of the third rule, dragged up: no drag
    const c = (await ruleRows(page).nth(2).getByLabel('Comment').boundingBox())!;
    const top = (await ruleRows(page).first().boundingBox())!;
    await page.mouse.move(c.x + 10, c.y + c.height / 2);
    await page.mouse.down();
    await page.mouse.move(c.x + 10, top.y + 4, {steps: 6});
    await expect(page.locator('table.fw-rules .ol-drop-before')).toHaveCount(0);
    await page.mouse.up();
    expect(await ruleIds(page)).toEqual(before);
    // the handle, dragged up, then Escape before it is let go
    const g = (await ruleRows(page).nth(2).locator('.ol-grip').boundingBox())!;
    await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
    await page.mouse.down();
    await page.mouse.move(g.x + g.width / 2, top.y + 4, {steps: 6});
    await expect(ruleRows(page).first()).toHaveClass(/ol-drop-before/);
    await page.keyboard.press('Escape');
    await expect(page.locator('table.fw-rules .ol-drop-before, table.fw-rules .ol-dragging')).toHaveCount(0);
    await page.mouse.up();
    expect(await ruleIds(page)).toEqual(before);
    await expect(page.locator('[data-diff]')).toHaveCount(0);
});

test('the keyboard: ↑/↓ on the handle, Alt+↑/↓ from a field, a select keeps its own Alt+↓', async ({page, baseURL}) => {
    await own(page, baseURL!);
    await page.goto('/system/firewall');
    await expect(ruleRows(page)).toHaveCount(18);
    const before = await ruleIds(page);
    const grip = ruleRows(page).first().locator('.ol-grip');
    await grip.focus();
    await page.keyboard.press('ArrowDown');
    await expect.poll(() => ruleIds(page)).toEqual([before[1], before[0], ...before.slice(2)]);
    await expect(status(page)).toHaveText('Rule 1 is now at position 2 of 18');
    // the focus stays on the handle, so a second press moves the rule on
    await page.keyboard.press('ArrowDown');
    await expect.poll(() => ruleIds(page)).toEqual([before[1], before[2], before[0], ...before.slice(3)]);
    await expect(page.locator(`tr[data-rule="${before[0]}"] .ol-grip`)).toBeFocused();
    // the top: nothing moves
    await page.locator(`tr[data-rule="${before[1]}"] .ol-grip`).press('ArrowUp');
    expect((await ruleIds(page))[0]).toBe(before[1]);
    // Alt+↑ in the comment field of the moved rule takes it back up; a plain ↑ there does not
    const comment = page.locator(`tr[data-rule="${before[0]}"]`).getByLabel('Comment');
    await comment.press('ArrowUp');
    expect((await ruleIds(page))[2]).toBe(before[0]);
    await comment.press('Alt+ArrowUp');
    await expect.poll(() => ruleIds(page)).toEqual([before[1], before[0], before[2], ...before.slice(3)]);
    await expect(comment).toBeFocused();
    // Alt+↓ on a select is the select's
    await page.locator(`tr[data-rule="${before[0]}"]`).getByLabel('Target').press('Alt+ArrowDown');
    expect((await ruleIds(page))[1]).toBe(before[0]);
    // sorted by another column: no handle, and Alt+↑/↓ moves nothing
    await page.locator('table.fw-rules th', {hasText: /^Port/}).click();
    await expect(ruleRows(page).locator('button.ol-grip')).toHaveCount(0);
    const sorted = await ruleIds(page);
    await ruleRows(page).nth(3).getByLabel('Comment').press('Alt+ArrowUp');
    expect(await ruleIds(page)).toEqual(sorted);
});

test('an LED state dragged by its handle, and moved by the keyboard', async ({page, baseURL, isMobile}) => {
    await own(page, baseURL!);
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const rows = page.locator('[data-led-row]');
    await expect(rows.first()).toBeVisible();
    const before = await ledIds(page);
    await dragTo(page, rows.nth(2).locator('.ol-grip'), rows.first(), 'above', isMobile);
    await expect.poll(() => ledIds(page)).toEqual([before[2], before[0], before[1], ...before.slice(3)]);
    await expect(page.getByRole('button', {name: 'Save'}).first()).toBeEnabled();
    await rows.first().locator('.ol-grip').press('ArrowDown');
    await expect.poll(() => ledIds(page)).toEqual([before[0], before[2], before[1], ...before.slice(3)]);
    await expect(status(page)).toContainText('is now at position 2 of');
});

test('a drag near the edge scrolls the page along', async ({page, baseURL, isMobile}) => {
    test.skip(isMobile, 'the mouse holds still at the edge');
    await own(page, baseURL!);
    await page.setViewportSize({width: 1280, height: 480});
    await page.goto('/system/firewall');
    await expect(ruleRows(page)).toHaveCount(18);
    const before = await ruleIds(page);
    const port = page.locator(PORT);
    const last = ruleRows(page).last();
    await last.scrollIntoViewIfNeeded();
    const start = await port.evaluate((el) => el.scrollTop);
    expect(start).toBeGreaterThan(200);
    const p = (await port.boundingBox())!;
    const g = (await last.locator('.ol-grip').boundingBox())!;
    await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
    await page.mouse.down();
    await page.mouse.move(g.x + g.width / 2, g.y - 20, {steps: 3});
    await page.mouse.move(g.x + g.width / 2, p.y + 4, {steps: 6});
    await expect.poll(() => port.evaluate((el) => el.scrollTop), {timeout: 5000}).toBeLessThan(start - 150);
    await page.mouse.up();
    const after = await ruleIds(page);
    expect(after.indexOf(before[17]!)).toBeLessThan(17);
});
