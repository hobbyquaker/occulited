import {expect, test, type Page} from '@playwright/test';
import {PORT, fitsWindow, scrollBy, scrollTo, scrollY} from './scroll';

// Task 99: "the bar should stay fixed, no matter if i scroll page down". B-129 made that a fixed-height
// shell: the top bar is a row of it that does not scroll, opaque, with its hairline under it, and
// `.ol-scrollport` - the box around `main` - is what scrolls, so the page's scroll bar begins at the
// bar's lower edge. An anchor lands below the bar (the port's scroll-padding-top) and a page's table
// heads stick to the port's top. App.svelte still names the bar's measured height --ol-header-h. What
// opens from the bar stays attached to it, a dialog stays above it and moves nothing, and on a phone
// it stays too - small enough there to leave the page most of the screen.

type Rect = {top: number; bottom: number; right: number; height: number};

function bar(page: Page): Promise<Rect> {
    return page.evaluate(() => {
        const r = document.querySelector('.ol-header')!.getBoundingClientRect();
        return {top: r.top, bottom: r.bottom, right: r.right, height: r.height};
    });
}

function rect(page: Page, selector: string): Promise<Rect> {
    return page.evaluate((s) => {
        const el = document.querySelector(s);
        if (!el) throw new Error(`no ${s}`);
        const r = el.getBoundingClientRect();
        return {top: r.top, bottom: r.bottom, right: r.right, height: r.height};
    }, selector);
}

function rfdRow(page: Page) {
    return page.locator('table.ol-table').first().locator('tbody tr').filter({has: page.locator('.sv-id', {hasText: /^rfd$/})});
}

// every service listed: the page is well longer than the window
async function openServices(page: Page, size?: {width: number; height: number}) {
    await page.addInitScript(() => {
        localStorage.setItem('ol.services.hideSystem', '0');
        localStorage.setItem('ol.services.hideOccu', '0');
    });
    if (size) await page.setViewportSize(size);
    await page.goto('/system/services');
    await expect(rfdRow(page)).toHaveCount(1);
}

test.describe('the top bar stays at the top while the page scrolls', () => {
    for (const path of ['/system/services', '/radio']) {
        test(`${path} scrolled down: the bar at the top, whole, opaque, its height named, its hairline under it`, async ({page, isMobile}) => {
            // the stub's Services page is short on a desktop: a low window makes it scroll
            if (path === '/system/services') await openServices(page, isMobile ? undefined : {width: 1280, height: 300});
            else {
                if (!isMobile) await page.setViewportSize({width: 1280, height: 500});
                await page.goto(path);
                await expect(page.locator('[data-process="rfd"]')).toBeVisible();
            }
            const before = await bar(page);
            await expect.poll(() => page.evaluate(() => document.documentElement.style.getPropertyValue('--ol-header-h'))).toBe(`${before.height}px`);

            await scrollTo(page, 900);
            await expect.poll(() => scrollY(page)).toBeGreaterThan(100);
            const after = await bar(page);
            expect(after.top).toBe(0);
            expect(after.height).toBe(before.height);
            await expect(page.locator('.ol-header')).toBeInViewport({ratio: 1});
            // the page scrolls under the bar, not over it: at the bar's lower edge the bar is what is hit
            expect(await page.evaluate((y) => !!document.elementFromPoint(4, y)?.closest('.ol-header'), after.height - 2)).toBe(true);
            // the hairline under it, in the border token
            const line = await page.locator('.ol-header').evaluate((el) => {
                const probe = document.createElement('div');
                probe.style.color = 'var(--hmm-border)';
                document.body.append(probe);
                const token = getComputedStyle(probe).color;
                probe.remove();
                const s = getComputedStyle(el);
                return {width: s.borderBottomWidth, color: s.borderBottomColor, token, background: s.backgroundColor};
            });
            expect(line.width).toBe('1px');
            expect(line.color).toBe(line.token);
            expect(line.background, 'an opaque bar').not.toMatch(/rgba\(.*, 0\)|transparent/);
        });
    }

    test("a page's table head sits directly under the bar", async ({page, isMobile}) => {
        test.skip(isMobile, 'below 700 px the Services tables stack their rows and the head does not stick');
        await openServices(page, {width: 1280, height: 300});
        // scroll the services table's head well past the bar
        await page.evaluate((s) => {
            const port = document.querySelector(s)!;
            const table = document.querySelector('table.ol-table')!;
            port.scrollTo({top: port.scrollTop + table.getBoundingClientRect().top - port.getBoundingClientRect().top + 300});
        }, PORT);
        await expect.poll(() => page.evaluate(() => document.querySelector('table.ol-table')!.getBoundingClientRect().top), 'the table starts above the window').toBeLessThan(0);
        const b = await bar(page);
        const th = await rect(page, 'table.ol-table th');
        expect(Math.abs(th.top - b.bottom), `the head at ${th.top}, the bar's bottom at ${b.bottom}`).toBeLessThanOrEqual(0.5);
        // and not under the bar: the head is what is hit just below it
        expect(await page.evaluate((y) => !!document.elementFromPoint(40, y)?.closest('th'), b.bottom + 3)).toBe(true);
    });

    // A table in a box that scrolls on its own keeps its head at that box's top (app.css): an offset of
    // the bar's height there would push the head down over the rows. Every page with a table, at rest.
    const TABLES: {path: string; ready: string; then?: (page: Page) => Promise<void>}[] = [
        {path: '/radio', ready: '[data-process="rfd"]', then: async (page) => {
            // task 199: the USB list opens from the Modules heading row
            // the Modules heading row's own button, so the German run finds it too
            await page.locator('.ol-headrow', {has: page.locator('h2#modules')}).getByRole('button').click();
            await expect(page.locator('table.ol-usb tbody tr').first()).toBeVisible();
        }},
        {path: '/system/services', ready: 'table.ol-timers tbody tr'},
        {path: '/system/services#boot', ready: 'section#boot [data-figure="total"] .v', then: async (page) => {
            const view = page.locator('section#boot select:has(option[value="chart"])');
            await view.selectOption('list');
            await expect(page.locator('section#boot table.bt-list')).toBeVisible();
        }},
        {path: '/system/updates', ready: 'table.fw-types tbody tr'},
        {path: '/system/users', ready: 'table.ol-stack tbody tr'},
        {path: '/system/users#api-tokens', ready: 'table.ol-tokens tbody tr'},
        {path: '/system/network', ready: '.ol-syspanels [data-panel="time"]'},
        {path: '/system/backup', ready: 'h2'},
        {path: '/account', ready: 'table.ol-table'},
        {path: '/addons', ready: '.ad-card'},
    ];
    for (const p of TABLES) {
        test(`${p.path}: no table head is pushed off its row`, async ({page}) => {
            await page.goto(p.path);
            await expect(page.locator(p.ready).first()).toBeVisible();
            if (p.then) await p.then(page);
            // a wheel ends /services#boot's following of its anchor, as it does for a reader
            await page.evaluate(() => window.dispatchEvent(new WheelEvent('wheel')));
            await scrollTo(page, 0);
            await expect.poll(() => scrollY(page)).toBe(0);
            const pushed = await page.evaluate(() =>
                Array.from(document.querySelectorAll('table.ol-table th'))
                    .filter((th) => th.getClientRects().length > 0)
                    .map((th) => ({th: th.textContent?.trim() ?? '', off: th.getBoundingClientRect().top - th.parentElement!.getBoundingClientRect().top}))
                    .filter((x) => Math.abs(x.off) > 0.5),
            );
            expect(pushed).toEqual([]);
        });
    }

    // task 183: the section is the Keys page's first, so it lands without a scroll now
    test('/radio#security-key lands on the Keys page with the heading below the bar', async ({page, baseURL}) => {
        await page.context().addCookies([{name: 'stub-key', value: 'default', url: baseURL!}]);
        await page.goto('/radio#security-key');
        await expect(page.locator('#ol-sec-key')).toBeFocused();
        const b = await bar(page);
        await expect.poll(async () => (await rect(page, '#security-key')).top).toBeGreaterThanOrEqual(b.bottom);
        await expect(page.locator('#security-key')).toBeInViewport({ratio: 1});
    });

    test('/services#boot lands with the section below the bar', async ({page}) => {
        await page.goto('/system/services#boot');
        await expect(page.locator('section#boot [data-figure="total"] .v')).toBeVisible();
        await expect.poll(() => scrollY(page)).toBeGreaterThan(0);
        const b = await bar(page);
        await expect.poll(async () => (await rect(page, 'section#boot')).top).toBeGreaterThanOrEqual(b.bottom);
        await expect.poll(async () => (await rect(page, 'section#boot')).top).toBeLessThan(b.bottom + 40);
    });

    test('after scrolling, the System menu and the power menu open attached to the bar and stay so', async ({page, isMobile}) => {
        // the Interfaces page: long enough in the stub to scroll far on a desktop too
        if (!isMobile) await page.setViewportSize({width: 1280, height: 500});
        await page.goto('/radio');
        await expect(page.locator('[data-process="rfd"]')).toBeVisible();
        await scrollTo(page, 900);
        await expect.poll(() => scrollY(page)).toBeGreaterThan(300);

        // task 57: the System tab's popover (lib/SystemMenu.svelte) is fixed to the window and follows
        // its tab through a scroll; on a phone it is a bottom sheet, which this test is not about
        test.skip(!!isMobile, 'the System menu is a bottom sheet on a phone');
        const system = page.locator('.ol-header .ol-systab');
        await system.click();
        const pop = page.locator('.ol-sysmenu');
        await expect(pop).toBeVisible();
        await expect.poll(() => pop.getAttribute('data-phase')).toBe('open');
        const gap = async () => (await pop.evaluate((el) => el.getBoundingClientRect().top)) - (await system.evaluate((el) => el.getBoundingClientRect().bottom));
        expect(await gap()).toBeGreaterThanOrEqual(0);
        expect(await gap()).toBeLessThanOrEqual(8);
        await expect(pop.getByRole('menuitem').first()).toBeInViewport();
        await scrollBy(page, 150);
        expect(await gap(), 'still under its button after the page moved').toBeLessThanOrEqual(8);
        await page.keyboard.press('Escape');
        await expect(pop).toBeHidden();

        const power = page.locator('.ol-powerbtn');
        await power.click();
        const powerPop = page.locator('.ol-powerpop');
        await expect(powerPop).toBeVisible();
        const powerGap = async () => (await powerPop.evaluate((el) => el.getBoundingClientRect().top)) - (await power.evaluate((el) => el.getBoundingClientRect().bottom));
        expect(await powerGap()).toBeGreaterThanOrEqual(0);
        expect(await powerGap()).toBeLessThanOrEqual(8);
        await scrollBy(page, 150);
        expect(await powerGap()).toBeLessThanOrEqual(8);
        await expect(powerPop.locator('[data-action="reboot"]')).toBeInViewport();
    });

    test('on a phone the bar leaves the page most of the screen, in English and German', async ({page, isMobile}, info) => {
        test.skip(!isMobile, 'the phone project');
        const seen: string[] = [];
        for (const size of [{width: 412, height: 915}, {width: 360, height: 640}]) {
            for (const language of ['en', 'de']) {
                await page.addInitScript((l) => localStorage.setItem('ol.language', l), language);
                await page.setViewportSize(size);
                await page.goto('/system/services');
                await expect(page.locator('table.ol-timers tbody tr').first()).toBeVisible();
                await page.evaluate(() => document.fonts.ready);
                const h = (await bar(page)).height;
                seen.push(`${size.width}×${size.height} ${language}: ${h} px (${((100 * h) / size.height).toFixed(1)} %)`);
                expect(h, seen.at(-1)).toBeLessThanOrEqual(size.height / 5);
            }
        }
        await info.attach('bar heights', {body: seen.join('\n'), contentType: 'text/plain'});
        expect(await fitsWindow(page)).toBe(true);
    });

    // B-129: the document does not scroll any more, so Page Down would reach nothing while the focus
    // sits on the tab in the top bar that was just clicked. App.svelte hands those keys to the port.
    test('the keyboard scrolls the page right after a navigation from the bar', async ({page, isMobile}) => {
        test.skip(isMobile, 'a hardware keyboard');
        await page.setViewportSize({width: 1280, height: 500});
        await page.goto('/');
        await expect(page.locator('main h1')).toBeVisible();
        // task 131: Interfaces is an entry of the System menu now; the chosen row goes with the menu
        await page.locator('.ol-header .ol-systab').click();
        await page.locator('.ol-sysmenu').getByRole('menuitem', {name: 'Interfaces'}).click();
        await expect(page.locator('[data-process="rfd"]')).toBeVisible();
        expect(await scrollY(page), 'at the top, and the focus is not in the page').toBe(0);
        await page.keyboard.press('PageDown');
        await expect.poll(() => scrollY(page)).toBeGreaterThan(100);
        await page.keyboard.press('End');
        const bottom = await scrollY(page);
        expect(bottom).toBeGreaterThan(0);
        await page.keyboard.press('PageUp');
        await expect.poll(() => scrollY(page)).toBeLessThan(bottom);
        await page.keyboard.press('Home');
        await expect.poll(() => scrollY(page)).toBe(0);
    });

    test('screenshots: the Interfaces page scrolled under the bar', async ({page, isMobile}, info) => {
        if (!isMobile) await page.setViewportSize({width: 1280, height: 720});
        await page.goto('/radio');
        await expect(page.locator('[data-process="rfd"]')).toBeVisible();
        await scrollTo(page, 700);
        await expect.poll(() => scrollY(page)).toBeGreaterThan(300);
        await page.evaluate(() => document.fonts.ready);
        const body = await page.screenshot();
        await info.attach(`interfaces-scrolled-${info.project.name}`, {body, contentType: 'image/png'});
        const dir = process.env.T99_SHOTS;
        if (dir) await page.screenshot({path: `${dir}/interfaces-scrolled-${info.project.name}.png`});
    });
});

// the dialog over a scrolled page needs Chromium without --hide-scrollbars for the whole file:
// topbar-sticky-dialog.spec.ts
