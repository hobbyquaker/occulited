import {expect, test, type Page} from '@playwright/test';

// Task 51: the help behind a ? (lib/Help.svelte) - hover with its delays on a desktop, the pin by
// click, Escape with the focus back on the ?, the outside click, one popup at a time, a tap on a
// phone with the popup inside a 360 px screen, and a ? inside a label that does not tick its box.
// The timing rules themselves are unit-tested in lib/help.test.ts; this is the component in a page.

const help = (page: Page, heading: string) => page.getByRole('heading', {level: 2, name: heading}).getByRole('button', {name: 'Help'});
const tip = (page: Page) => page.getByRole('tooltip');

test.describe('with a mouse', () => {
    test.beforeEach(({}, info) => {
        test.skip(info.project.name === 'phone', 'a phone cannot hover');
    });

    test('hover opens after the delay and closes after leaving', async ({page}) => {
        await page.goto('/system/backup');
        const q = help(page, 'Create a backup');
        await expect(q).toBeVisible();
        const t0 = Date.now();
        await q.hover();
        await expect(tip(page)).toBeVisible();
        expect(Date.now() - t0).toBeGreaterThanOrEqual(280);
        await expect(q).toHaveAttribute('aria-expanded', 'true');
        await expect(q).toHaveAttribute('aria-describedby', /^ol-help-/);
        await expect(tip(page)).toContainText('A CCU-compatible .sbk');
        const t1 = Date.now();
        await page.mouse.move(2, 400);
        await expect(tip(page)).toHaveCount(0);
        expect(Date.now() - t1).toBeGreaterThanOrEqual(150);
        await expect(q).toHaveAttribute('aria-expanded', 'false');
    });

    test('the pointer can travel into the popup and it stays open', async ({page}) => {
        await page.goto('/system/backup');
        await help(page, 'Create a backup').hover();
        await expect(tip(page)).toBeVisible();
        const box = (await tip(page).boundingBox())!;
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, {steps: 6});
        await page.waitForTimeout(600);
        await expect(tip(page)).toBeVisible();
    });

    test('a click pins it: leaving does not close it, a second click does', async ({page}) => {
        await page.goto('/system/backup');
        const q = help(page, 'Create a backup');
        await q.click();
        await expect(tip(page)).toBeVisible();
        await page.mouse.move(2, 400);
        await page.waitForTimeout(600);
        await expect(tip(page)).toBeVisible();
        await q.click();
        await expect(tip(page)).toHaveCount(0);
    });
});

test('Enter opens it, Escape closes it and gives the focus back to the ?', async ({page}) => {
    await page.goto('/system/backup');
    const q = help(page, 'Create a backup');
    await q.focus();
    await page.keyboard.press('Enter');
    await expect(tip(page)).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(tip(page)).toHaveCount(0);
    await expect(q).toBeFocused();
});

test('a click outside closes it, and only one is open at a time', async ({page}) => {
    await page.goto('/system/backup');
    await help(page, 'Create a backup').click();
    await expect(tip(page)).toContainText('A CCU-compatible .sbk');
    await help(page, 'Restore or import a backup').click();
    await expect(tip(page)).toHaveCount(1);
    await expect(tip(page)).toContainText('Two kinds of backup, two ways.');
    await page.getByRole('heading', {level: 1, name: 'Backup'}).click();
    await expect(tip(page)).toHaveCount(0);
});

test('a ? inside a checkbox label opens without ticking the box, and neither does a click on the text', async ({page}) => {
    await page.goto('/system/certificates');
    const redirect = page.getByRole('checkbox', {name: /Redirect HTTP to HTTPS/});
    await expect(redirect).not.toBeChecked();
    await redirect.locator('xpath=ancestor::label').getByRole('button', {name: 'Help'}).click();
    await expect(tip(page)).toContainText('every request on port 80');
    await tip(page).click();
    await expect(tip(page)).toBeVisible();
    await expect(redirect).not.toBeChecked();
});

test.describe('on a 360 px phone', () => {
    test.use({viewport: {width: 360, height: 760}});
    test.beforeEach(({}, info) => {
        test.skip(info.project.name !== 'phone', 'the touch screen');
    });

    // on a page that fits the phone: a page wider than the screen (Network's tables at 360 px) is
    // zoomed out by the mobile browser, and a tap there does not land where Playwright aims
    test('a tap opens it inside the screen, a tap outside closes it', async ({page}) => {
        await page.goto('/system/backup');
        for (const heading of ['Create a backup', 'Restore or import a backup']) {
            const q = help(page, heading);
            await q.scrollIntoViewIfNeeded();
            await q.tap();
            await expect(tip(page)).toBeVisible();
            const box = (await tip(page).boundingBox())!;
            expect(box.x).toBeGreaterThanOrEqual(0);
            expect(box.x + box.width).toBeLessThanOrEqual(360);
            // task 57: the heading is the title switcher; its crumb text is what is outside the tip
            await page.locator('.ol-systitle-crumb').tap();
            await expect(tip(page)).toHaveCount(0);
        }
    });

    // a popup in an open dialog has to land in what is visible: while the Services page was wider
    // than the phone and zoomed out, it was clamped into 0..360 of the page and sat at x -304 of the
    // screen. The page fits the phone since B-105, so the taps are real taps again.
    test('a popup in a dialog on the Services page stays inside the screen', async ({page}) => {
        await page.goto('/system/services');
        await page.getByRole('button', {name: 'New timer…'}).tap();
        const dialog = page.getByRole('dialog', {name: 'New timer'});
        await expect(dialog).toBeVisible();
        await dialog.getByRole('heading').getByRole('button', {name: 'Help'}).tap();
        await expect(tip(page)).toBeVisible();
        const box = (await tip(page).boundingBox())!;
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(360);
        expect(box.y).toBeGreaterThanOrEqual(0);
        expect(box.y + box.height).toBeLessThanOrEqual(760);
    });
});
