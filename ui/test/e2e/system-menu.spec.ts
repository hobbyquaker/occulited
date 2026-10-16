import {expect, test, type Page} from '@playwright/test';

/*
 * Task 57 (D-68, D-80): the System menu. A popover under the System tab - an iOS Settings list in a
 * fixed order with a filter, warning dots, a checkmark on the current page - that grows out of the
 * tab and shrinks back; a bottom sheet on a phone; the same menu under a system page's title; the
 * keyboard: Ctrl/⌘+K, the arrow keys, Escape's focus return.
 */
const ORDER_EN = ['Interfaces', 'LAN devices', 'Keys', 'Network', 'Firewall', 'Remote access', 'Trust stores', 'Certificate', 'Users', 'Services', 'Log', 'Storage', 'Backup', 'Updates', 'Hardware'];
const ORDER_DE = ['Schnittstellen', 'LAN-Geräte', 'Schlüssel', 'Netzwerk', 'Firewall', 'Fernzugriff', 'Vertrauensspeicher', 'Zertifikat', 'Benutzer', 'Dienste', 'Protokoll', 'Speicher', 'Sicherung', 'Updates', 'Hardware'];

const tab = (page: Page) => page.locator('.ol-systab');
const menu = (page: Page) => page.locator('.ol-sysmenu');
const rows = (page: Page) => menu(page).getByRole('menuitem');
const filter = (page: Page) => menu(page).getByRole('textbox', {name: 'Filter the System menu'});

async function open(page: Page) {
    await tab(page).click();
    await expect(menu(page)).toBeVisible();
    await expect.poll(() => menu(page).getAttribute('data-phase')).toBe('open');
}

type Rect = {left: number; top: number; width: number; height: number};
const rectOf = (page: Page, selector: string): Promise<Rect> =>
    page.locator(selector).evaluate((el) => {
        const r = el.getBoundingClientRect();
        return {left: r.left, top: r.top, width: r.width, height: r.height};
    });
function near(a: Rect, b: Rect, tolerance = 1.5) {
    for (const k of ['left', 'top', 'width', 'height'] as const) expect(Math.abs(a[k] - b[k]), k).toBeLessThanOrEqual(tolerance);
}

test('the tab opens the menu, a click on an entry navigates and closes it, the tab is active on every system route', async ({page}) => {
    await page.goto('/');
    await expect(tab(page)).not.toHaveClass(/active/);
    await open(page);
    await expect(tab(page)).toHaveAttribute('aria-expanded', 'true');
    await rows(page).filter({hasText: 'Firewall'}).click();
    await expect(page).toHaveURL(/\/system\/firewall$/);
    await expect(page.getByRole('heading', {level: 1, name: 'Firewall'})).toBeVisible();
    await expect(menu(page)).toHaveCount(0);
    await expect(tab(page)).toHaveClass(/active/);
    await expect(tab(page)).toHaveAttribute('aria-expanded', 'false');
    // the current page is checked, and only it
    await open(page);
    await expect(rows(page).filter({hasText: 'Firewall'})).toHaveAttribute('aria-current', 'page');
    await expect(menu(page).locator('[aria-current="page"]')).toHaveCount(1);
    await expect(rows(page).filter({hasText: 'Firewall'}).locator('.ol-syscheck')).toBeVisible();
    await expect(rows(page).filter({hasText: 'Network'}).locator('.ol-syscheck')).toHaveCount(0);
});

test('the entries are one flat list in the fixed order, in English and in German, 44 px each with its square in the one accent', async ({page}) => {
    await page.goto('/');
    await open(page);
    await expect(rows(page)).toHaveText(ORDER_EN);
    await expect(menu(page).locator('[role="menu"]')).toHaveAttribute('role', 'menu');
    for (const r of await rows(page).all()) expect((await r.boundingBox())?.height).toBeGreaterThanOrEqual(44);
    await expect(menu(page).locator('.ol-systile')).toHaveCount(ORDER_EN.length);
    // task 210: every square has the same pastel accent (menu-look.spec.ts checks its token and contrast)
    const colours = await menu(page).locator('.ol-systile').evaluateAll((els) => els.map((e) => getComputedStyle(e).backgroundColor));
    expect(new Set(colours).size).toBe(1);
    // the addon pages stay in the addon dropdown, not here
    await expect(rows(page).filter({hasText: 'Installed addons'})).toHaveCount(0);

    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/');
    await open(page);
    await expect(rows(page)).toHaveText(ORDER_DE);
    await expect(menu(page)).toHaveAttribute('aria-label', 'Systemmenü');
});

test('the filter narrows the list by label and by keyword, Enter opens the first match, an empty result says so', async ({page}) => {
    await page.goto('/');
    await open(page);
    await filter(page).fill('acme');
    await expect(rows(page)).toHaveText(['Certificate']);
    await filter(page).fill('oidc');
    await expect(rows(page)).toHaveText(['Users']);
    // task 132: HSTS finds the Certificate page, where the HTTPS switches are now
    await filter(page).fill('hsts');
    await expect(rows(page)).toHaveText(['Certificate']);
    // task 143: a CCU client's name finds Remote access
    await filter(page).fill('iobroker');
    await expect(rows(page)).toHaveText(['Remote access']);
    await filter(page).fill('xyzzy');
    await expect(rows(page)).toHaveCount(0);
    await expect(menu(page).getByText('Nothing found')).toBeVisible();
    // ✕ empties it (lib/SearchInput.svelte), and the list is whole again
    await menu(page).getByRole('button', {name: 'Clear filter'}).click();
    await expect(rows(page)).toHaveText(ORDER_EN);
    await filter(page).fill('fire');
    await expect(rows(page)).toHaveText(['Firewall']);
    await filter(page).press('Enter');
    await expect(page).toHaveURL(/\/system\/firewall$/);
    await expect(menu(page)).toHaveCount(0);
});

test('a warning dot appears on an entry and on the tab with a warning, and goes when the warning goes', async ({page, context, baseURL}) => {
    // B-92's root-owned files of hm2mqtt: a warning that leads to Services
    await context.addCookies([{name: 'stub-ownership', value: '1', url: baseURL!}]);
    await page.goto('/');
    await open(page);
    const services = rows(page).filter({hasText: 'Services'});
    await expect(services.locator('.ol-sysdot')).toHaveAttribute('aria-label', 'Warning');
    await expect(services.locator('.ol-sysdot')).toHaveClass(/warning/);
    // the stub's default schedule backs up onto the box itself: an error that leads to Backup, so
    // the tab shows the worst of them
    await expect(rows(page).filter({hasText: 'Backup'}).locator('.ol-sysdot')).toHaveAttribute('aria-label', 'Error');
    await expect(tab(page).locator('.ol-sysdot')).toHaveAttribute('aria-label', 'Error');
    await expect(rows(page).filter({hasText: 'Network'}).locator('.ol-sysdot')).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(menu(page)).toHaveCount(0);

    // the warning is gone: the menu reads the list again when it opens
    await context.clearCookies();
    await open(page);
    await expect(services.locator('.ol-sysdot')).toHaveCount(0);
});

test.describe('the motion', () => {
    test('the panel grows out of the tab and closes at once', async ({page, isMobile}) => {
        test.skip(!!isMobile, 'a phone gets the sheet');
        // every animation is held at its start, so its first and last frames can be measured; "the last"
        // is a hair before the end - at the end itself an animation without a fill is over and the
        // element is drawn in its own place again
        await page.addInitScript(() => {
            const w = window as unknown as {__anims: Animation[]};
            w.__anims = [];
            const orig = Element.prototype.animate;
            Element.prototype.animate = function (this: Element, ...args: Parameters<Element['animate']>) {
                const a = orig.apply(this, args);
                a.pause();
                w.__anims.push(a);
                return a;
            };
        });
        const anims = () => page.evaluate(() => (window as unknown as {__anims: Animation[]}).__anims.length);
        const seek = (t: number | 'end') =>
            page.evaluate((t) => {
                for (const a of (window as unknown as {__anims: Animation[]}).__anims) {
                    const total = Number(a.effect?.getComputedTiming().duration ?? 0);
                    a.currentTime = t === 'end' ? total - 0.5 : t;
                }
            }, t);
        const finish = () => page.evaluate(() => (window as unknown as {__anims: Animation[]}).__anims.splice(0).forEach((a) => a.finish()));

        await page.goto('/');
        const before = await rectOf(page, '.ol-systab');
        await tab(page).click();
        await expect(menu(page)).toHaveAttribute('data-phase', 'opening');
        await expect.poll(anims).toBeGreaterThan(0);
        await seek(0);
        near(await rectOf(page, '.ol-sysmenu'), before);
        await seek('end');
        const grown = await rectOf(page, '.ol-sysmenu');
        expect(grown.width).toBeGreaterThan(before.width * 2);
        expect(grown.height).toBeGreaterThan(before.height * 4);
        expect(grown.top).toBeGreaterThanOrEqual(before.top + before.height);
        await finish();
        await expect(menu(page)).toHaveAttribute('data-phase', 'open');
        near(await rectOf(page, '.ol-sysmenu'), grown);

        // task 210: only the opening moves - the close starts no animation, the panel is gone at once
        await page.keyboard.press('Escape');
        await expect(menu(page)).toHaveCount(0);
        // (the page's own load bar may fade meanwhile; only the menu's animations count)
        const menuAnims = await page.evaluate(() =>
            (window as unknown as {__anims: Animation[]}).__anims.filter((a) => ((a.effect as KeyframeEffect | null)?.target as Element | null)?.closest('.ol-sysmenu, .ol-sysbackdrop')).length,
        );
        expect(menuAnims).toBe(0);
        await expect(tab(page)).toBeFocused();
    });

    test('nothing moves under reduced motion', async ({page}) => {
        await page.emulateMedia({reducedMotion: 'reduce'});
        await page.goto('/');
        await tab(page).click();
        await expect(menu(page)).toHaveAttribute('data-phase', 'open');
        expect(await page.evaluate(() => document.getAnimations().length)).toBe(0);
        await page.keyboard.press('Escape');
        await expect(menu(page)).toHaveCount(0);
    });
});

test.describe('the bottom sheet on a phone', () => {
    test.skip(({isMobile}) => !isMobile, 'the phone project');

    test('slides up with a handle over a dimmed page; Escape, a tap outside and a swipe down close it', async ({page}) => {
        await page.goto('/');
        await open(page);
        const sheet = page.locator('.ol-syssheet');
        await expect(sheet).toBeVisible();
        await expect(page.locator('.ol-sysbackdrop')).toBeVisible();
        await expect(sheet.locator('.ol-syshandle')).toBeVisible();
        const box = (await sheet.boundingBox())!;
        const view = page.viewportSize()!;
        expect(box.y + box.height).toBeGreaterThanOrEqual(view.height - 1);
        expect(box.width).toBe(view.width);
        // the rows are 44 px, the filter at the sheet's top
        for (const r of await rows(page).all()) expect((await r.boundingBox())?.height).toBeGreaterThanOrEqual(44);
        expect((await filter(page).boundingBox())!.y).toBeLessThan((await rows(page).first().boundingBox())!.y);
        await page.keyboard.press('Escape');
        await expect(menu(page)).toHaveCount(0);

        // task 188: a real tap does not focus the filter - the phone's keyboard would cover the
        // sheet it just opened - and the sheet says nothing about a shortcut there is no key for
        await tab(page).tap();
        await expect(menu(page)).toBeVisible();
        await expect.poll(() => menu(page).getAttribute('data-phase')).toBe('open');
        await expect(filter(page)).not.toBeFocused();
        await expect(menu(page).locator('[data-shortcut]')).toHaveCount(0);
        await page.keyboard.press('Escape');
        await expect(menu(page)).toHaveCount(0);

        await open(page);
        await page.mouse.click(view.width / 2, 40);
        await expect(menu(page)).toHaveCount(0);

        await open(page);
        const handle = (await sheet.locator('.ol-syshandle-row').boundingBox())!;
        const x = handle.x + handle.width / 2;
        const y = handle.y + handle.height / 2;
        await page.mouse.move(x, y);
        await page.mouse.down();
        await page.mouse.move(x, y + 60, {steps: 4});
        // not far enough yet: the sheet follows the finger and stays
        expect((await sheet.boundingBox())!.y).toBeGreaterThan(box.y + 30);
        await page.mouse.move(x, y + 160, {steps: 4});
        await page.mouse.up();
        await expect(menu(page)).toHaveCount(0);
    });

    test('the title switcher opens the sheet too, with the page checked', async ({page}, info) => {
        await page.goto('/system/backup');
        await page.locator('.ol-systitle-btn').click();
        await expect(page.locator('.ol-syssheet')).toBeVisible();
        await expect(rows(page).filter({hasText: 'Backup'})).toHaveAttribute('aria-current', 'page');
        await page.screenshot({path: info.outputPath('sheet.png')});
    });
});

test('the page title switcher opens the same menu under the title with the page checked', async ({page, isMobile}, info) => {
    test.skip(!!isMobile, 'the sheet is covered above');
    await page.goto('/system/certificates');
    const title = page.getByRole('heading', {level: 1});
    await expect(title).toContainText('System › Certificate');
    const button = page.locator('.ol-systitle-btn');
    await button.click();
    await expect(menu(page)).toBeVisible();
    await expect.poll(() => menu(page).getAttribute('data-phase')).toBe('open');
    await expect(button).toHaveAttribute('aria-expanded', 'true');
    await expect(tab(page)).toHaveAttribute('aria-expanded', 'false');
    const b = await rectOf(page, '.ol-systitle-btn');
    const m = await rectOf(page, '.ol-sysmenu');
    expect(m.top).toBeGreaterThanOrEqual(b.top + b.height);
    expect(m.top).toBeLessThanOrEqual(b.top + b.height + 12);
    expect(Math.abs(m.left - b.left)).toBeLessThanOrEqual(1);
    await expect(rows(page).filter({hasText: 'Certificate'})).toHaveAttribute('aria-current', 'page');
    await page.screenshot({path: info.outputPath('title-menu.png')});
    await rows(page).filter({hasText: 'Users'}).click();
    await expect(page).toHaveURL(/\/system\/users$/);
    await expect(page.getByRole('heading', {level: 1})).toContainText('System › Users');
    await expect(menu(page)).toHaveCount(0);
});

test('Ctrl+K opens the menu with the filter focused, the arrow keys walk the rows, Escape gives the focus back', async ({page}) => {
    await page.goto('/');
    await page.keyboard.press('Control+k');
    await expect(menu(page)).toBeVisible();
    await expect(filter(page)).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(rows(page).nth(0)).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(rows(page).nth(1)).toBeFocused();
    await page.keyboard.press('End');
    await expect(rows(page).nth(ORDER_EN.length - 1)).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(rows(page).nth(0)).toBeFocused();
    await page.keyboard.press('Home');
    await expect(rows(page).nth(0)).toBeFocused();
    // up from the first row returns to the filter; a letter typed on a row goes into it
    await page.keyboard.press('ArrowUp');
    await expect(filter(page)).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.type('led');
    await expect(filter(page)).toBeFocused();
    await expect(filter(page)).toHaveValue('led');
    await expect(rows(page)).toHaveText(['Hardware']);
    await page.keyboard.press('Escape'); // the field's Escape clears it
    await expect(filter(page)).toHaveValue('');
    await expect(rows(page)).toHaveText(ORDER_EN);
    await page.keyboard.press('Escape'); // the menu's Escape closes it and the tab gets the focus back
    await expect(menu(page)).toHaveCount(0);
    await expect(tab(page)).toBeFocused();
    // Enter on a row opens its page: the third row, Keys (after LAN devices since openccu-lite task 222)
    await page.keyboard.press('Control+k');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/system\/keys$/);
});

test('Ctrl+K is not taken over while the focus is in a text field', async ({page}) => {
    await page.goto('/system/log');
    const field = page.getByRole('textbox', {name: 'Text filter'});
    await field.focus();
    await page.keyboard.press('Control+k');
    await expect(menu(page)).toHaveCount(0);
    await expect(field).toBeFocused();
});

test('the tab opened by the keyboard focuses the filter; so does a mouse click since task 188', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'a phone has no Tab key to speak of');
    await page.goto('/');
    await tab(page).focus();
    await page.keyboard.press('Enter');
    await expect(menu(page)).toBeVisible();
    await expect(filter(page)).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(menu(page)).toHaveCount(0);
    // task 188: a mouse cannot bring up an on-screen keyboard, so it focuses the filter as well;
    // only a finger does not (the sheet's test above)
    await open(page);
    await expect(filter(page)).toBeFocused();
});

test('at 768 px the menu is a popover, screenshotted in the project\'s theme', async ({page, isMobile}, info) => {
    test.skip(!!isMobile, 'the phone project is 412 px');
    await page.setViewportSize({width: 768, height: 900});
    await page.goto('/system/network');
    await open(page);
    await expect(page.locator('.ol-syspop')).toBeVisible();
    await expect(page.locator('.ol-syssheet')).toHaveCount(0);
    const m = await rectOf(page, '.ol-sysmenu');
    expect(m.left + m.width).toBeLessThanOrEqual(768);
    await page.screenshot({path: info.outputPath('menu-768.png')});
});

// task 188 (the maintainer, 2026-09-19): "when opening system menu the cursor should be placed in
// filter input so user can start typing something right away. and we also need a keyboard shortcut
// for that menu." The shortcut was there since task 57 and nothing in the UI said so.
test.describe('the filter on open and the visible shortcut', () => {
    test.skip(({isMobile}) => !!isMobile, 'the phone keeps the sheet: covered above');

    test('a mouse click on the tab focuses the filter, and typing narrows the list at once', async ({page}) => {
        await page.goto('/');
        await open(page);
        await expect(filter(page)).toBeFocused();
        await page.keyboard.type('led');
        await expect(filter(page)).toHaveValue('led');
        await expect(rows(page)).toHaveText(['Hardware']);
        await page.keyboard.press('Escape');
        await page.keyboard.press('Escape');
        await expect(menu(page)).toHaveCount(0);
    });

    test("a mouse click on a system page's title switcher focuses the filter too", async ({page}) => {
        await page.goto('/system/firewall');
        await page.locator('.ol-systitle-btn').click();
        await expect(menu(page)).toBeVisible();
        await expect.poll(() => menu(page).getAttribute('data-phase')).toBe('open');
        await expect(filter(page)).toBeFocused();
    });

    test('the shortcut is written beside the filter, on the tab and on the title switcher', async ({page}) => {
        await page.goto('/');
        // the tab says it without the menu being open: a tooltip and the ARIA property
        await expect(tab(page)).toHaveAttribute('aria-keyshortcuts', 'Control+K Meta+K');
        await expect(tab(page)).toHaveAttribute('title', 'System menu (Ctrl K)');
        await open(page);
        const kbd = menu(page).locator('[data-shortcut]');
        // the suite runs on Linux, so it is the Ctrl spelling; shortcutLabel's unit test has the Mac one
        await expect(kbd).toHaveText('Ctrl K');
        // beside the filter, not over it, and not read out twice (the tab carries the ARIA property)
        await expect(kbd).toHaveAttribute('aria-hidden', 'true');
        const f = (await filter(page).boundingBox())!;
        const k = (await kbd.boundingBox())!;
        expect(k.x).toBeGreaterThanOrEqual(f.x + f.width - 1);
        await page.keyboard.press('Escape');
        await page.goto('/system/firewall');
        await expect(page.locator('.ol-systitle-btn')).toHaveAttribute('aria-keyshortcuts', 'Control+K Meta+K');
    });

    test('in German the tooltip is translated and the keys are not', async ({page}) => {
        await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
        await page.goto('/');
        await expect(tab(page)).toHaveAttribute('title', 'Systemmenü (Ctrl K)');
    });
});
