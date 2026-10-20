import {expect, test, type Page} from '@playwright/test';

// Task 104: the Log page uses the whole page. The log runs from the window's left edge to its right edge
// and down to its bottom - no panel around it, no radius, no margin - and is the one thing that scrolls,
// so its scroll bar is the rightmost thing on the screen. The filters and buttons sit in a card panel
// above it: the first row the text filter, growing, with Boot, Download and the gear; the second row the
// other filters, folded behind a Filters toggle that counts the filters set below 700 px. The text filter
// (lib/SearchInput.svelte) shows a magnifier while empty and an ✕ once filled; ✕ and Escape clear it and
// reload; it filters while typing, once the typing pauses.

async function openLog(page: Page, path = '/system/log') {
    await page.goto(path);
    await expect(page.locator('.lg-head')).toBeVisible();
}

/** the panel's second row, unfolded where a phone folds it */
async function showFilters(page: Page) {
    const toggle = page.locator('.lg-filters-toggle');
    if ((await toggle.count()) > 0 && (await toggle.getAttribute('aria-expanded')) !== 'true') await toggle.click();
    await expect(page.locator('#lg-filters')).toBeVisible();
}

/** the text filter's requests: the q of every GET /log */
function watchQueries(page: Page): string[] {
    const asked: string[] = [];
    page.on('request', (r) => {
        const u = new URL(r.url());
        if (r.method() === 'GET' && u.pathname === '/api/system/v1/log') asked.push(u.searchParams.get('q') ?? '');
    });
    return asked;
}

test('the log runs edge to edge under a card panel, and its scroll bar is the only one', async ({page, isMobile}) => {
    test.skip(isMobile, 'a desktop window; the phone is below');
    // a low window, so the stub's lines overflow the log
    await page.setViewportSize({width: 1280, height: 420});
    await openLog(page);
    await expect(page.locator('.ol-line').first()).toBeVisible();
    const box = page.locator('.lg-box');
    const m = await box.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        const root = document.documentElement;
        return {
            left: r.left,
            right: r.right,
            bottom: r.bottom,
            radius: s.borderTopLeftRadius,
            margin: [s.marginLeft, s.marginRight, s.marginBottom],
            sides: [s.borderLeftWidth, s.borderRightWidth, s.borderBottomWidth],
            window: root.clientWidth,
            height: window.innerHeight,
            pageScrolls: root.scrollHeight > root.clientHeight,
            boxScrolls: el.scrollHeight > el.clientHeight,
        };
    });
    expect(m.left, 'from the window\'s left edge').toBe(0);
    expect(Math.abs(m.right - m.window), 'to its right edge').toBeLessThanOrEqual(1);
    expect(Math.abs(m.bottom - m.height), 'down to its bottom').toBeLessThanOrEqual(1);
    expect(m.radius).toBe('0px');
    expect(m.margin).toEqual(['0px', '0px', '0px']);
    expect(m.sides, 'no frame around the log').toEqual(['0px', '0px', '0px']);
    expect(m.boxScrolls, 'the log scrolls').toBe(true);
    expect(m.pageScrolls, 'the page does not').toBe(false);

    // the panel wears the card's surface, shadow and radius
    const look = await page.locator('.lg-panel').evaluate((el) => {
        const s = getComputedStyle(el);
        const card = document.createElement('div');
        card.className = 'ol-card';
        document.body.append(card);
        const c = getComputedStyle(card);
        const out = {bg: s.backgroundColor, cardBg: c.backgroundColor, shadow: s.boxShadow, cardShadow: c.boxShadow, radius: s.borderTopLeftRadius, cardRadius: c.borderTopLeftRadius};
        card.remove();
        return out;
    });
    expect(look.bg).toBe(look.cardBg);
    expect(look.shadow).toBe(look.cardShadow);
    expect(look.radius).toBe(look.cardRadius);

    // wider than 700 px the second row is simply there
    await expect(page.locator('.lg-filters-toggle')).toHaveCount(0);
    await expect(page.locator('#lg-filters')).toBeVisible();

    // the text filter takes what the first row leaves
    const fill = await page.locator('.lg-head').evaluate((row) => {
        const search = row.querySelector('.ol-search')!;
        const kids = Array.from(row.children).filter((c) => c !== search && c.getClientRects().length > 0);
        const gap = parseFloat(getComputedStyle(row).columnGap) || 0;
        const others = kids.reduce((w, c) => w + c.getBoundingClientRect().width, 0) + gap * kids.length;
        return {search: search.getBoundingClientRect().width, free: row.clientWidth - others};
    });
    expect(Math.abs(fill.search - fill.free), `the filter ${fill.search} px of ${fill.free} px free`).toBeLessThanOrEqual(2);
});

test('the text filter: a magnifier, the ✕, one request for quick typing, ✕ and Escape clear and reload', async ({page}) => {
    const asked = watchQueries(page);
    await openLog(page);
    await expect(page.locator('.ol-line').first()).toBeVisible();
    // the busybox log polls every five seconds: that would be a request the typing did not make
    await showFilters(page);
    const auto = page.getByLabel('Auto refresh');
    if ((await auto.count()) > 0) await auto.uncheck();
    const input = page.getByRole('textbox', {name: 'Text filter'});
    const clear = page.getByRole('button', {name: 'Clear filter'});
    await expect(page.locator('.ol-search-icon')).toBeVisible();
    await expect(clear).toHaveCount(0);

    asked.length = 0;
    await input.pressSequentially('listBidcos', {delay: 25});
    await expect(page.locator('.ol-search-icon')).toHaveCount(0);
    await expect(clear).toBeVisible();
    await expect.poll(() => asked.length).toBe(1);
    await page.waitForTimeout(600); // the typing's pause is over: nothing more comes
    expect(asked, 'one request, with the whole text').toEqual(['listBidcos']);

    await clear.click();
    await expect(input).toHaveValue('');
    await expect(input).toBeFocused();
    await expect.poll(() => asked.at(-1)).toBe('');
    await expect(page.locator('.ol-search-icon')).toBeVisible();

    // Enter applies at once; Escape clears
    await input.fill('rfd');
    await input.press('Enter');
    await expect.poll(() => asked.at(-1)).toBe('rfd');
    await input.press('Escape');
    await expect(input).toHaveValue('');
    await expect(input).toBeFocused();
    await expect.poll(() => asked.at(-1)).toBe('');
});

test('follow keeps the newest line in view, oldest first and newest first', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-log-churn', value: '1', url: baseURL!}]);
    await openLog(page);
    await expect(page.getByText(/churn line/).first()).toBeVisible();
    const box = page.locator('.lg-box');
    await expect.poll(() => box.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)).toBeLessThan(2);
    await expect(page.locator('.ol-line').last()).toContainText(/churn line/);

    await showFilters(page);
    await page.getByLabel('Order').selectOption('newest');
    await expect.poll(() => box.evaluate((el) => el.scrollTop)).toBeLessThan(2);
    const top = await page.locator('.ol-line').first().textContent();
    await expect.poll(() => page.locator('.ol-line').first().textContent(), {timeout: 10_000}).not.toBe(top);
    await expect(page.locator('.ol-line').first()).toContainText(/churn line/);
    expect(await box.evaluate((el) => el.scrollTop), 'still at the newest line').toBeLessThan(2);
});

test('below 700 px the second row folds behind Filters, which counts the filters set', async ({page, isMobile}) => {
    test.skip(!isMobile, 'the phone project');
    await openLog(page, '/system/log?unit=rfd');
    const toggle = page.locator('.lg-filters-toggle');
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await expect(page.locator('#lg-filters')).toHaveCount(0);
    await expect(toggle.locator('[data-filter-count]'), 'the unit is set').toHaveText('1');
    // the text filter stays in the first row, outside the fold
    await expect(page.getByRole('textbox', {name: 'Text filter'})).toBeVisible();
    await toggle.click();
    await expect(page.locator('#lg-filters')).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await page.locator('#lg-filters').getByLabel('Severity').selectOption('warning');
    await expect(toggle.locator('[data-filter-count]')).toHaveText('2');
    await toggle.click();
    await expect(page.locator('#lg-filters')).toHaveCount(0);
    // the log below the panel still reaches the window's edges
    const edge = await page.locator('.lg-box, .lg-state').first().evaluate((el) => ({left: el.getBoundingClientRect().left, right: el.getBoundingClientRect().right, window: document.documentElement.clientWidth}));
    expect(edge.left).toBe(0);
    expect(Math.abs(edge.right - edge.window)).toBeLessThanOrEqual(1);
});

test("a view at the log's end stays there when the panel above changes the log's height", async ({page, isMobile}) => {
    test.skip(!isMobile, 'the phone project: its fold changes the height');
    await openLog(page);
    await expect(page.locator('.ol-line').first()).toBeVisible();
    const box = page.locator('.lg-box');
    const gap = () => box.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
    const still = () => expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
    await expect.poll(() => box.evaluate((el) => el.scrollHeight - el.clientHeight), 'the log overflows').toBeGreaterThan(100);
    await expect.poll(gap).toBeLessThanOrEqual(2);
    // the fold opens: the log gets shorter, the view stays on the newest line
    const toggle = page.locator('.lg-filters-toggle');
    await toggle.click();
    await expect(page.locator('#lg-filters')).toBeVisible();
    await still();
    await expect.poll(gap).toBeLessThanOrEqual(2);
    // scrolled up into the history, a change of height leaves the view alone
    await box.evaluate((el) => el.scrollTo({top: 0}));
    await expect.poll(() => box.evaluate((el) => el.scrollTop)).toBe(0);
    await toggle.click();
    await expect(page.locator('#lg-filters')).toHaveCount(0);
    await still();
    expect(await box.evaluate((el) => el.scrollTop)).toBe(0);
});

// occulited B-92: what a loaded machine did to the test above, made on purpose - the page's own
// scroll towards the end and the box's shrink by the whole fold land in one frame, so the scroll's
// event sees the shrink's gap. Only a scroll away from the end may stop the following.
test("a scroll towards the end whose event sees the fold's shrink still follows", async ({page, isMobile}) => {
    test.skip(!isMobile, 'the phone project: its fold changes the height');
    await page.emulateMedia({reducedMotion: 'reduce'}); // the fold opens in one step, as under load
    await openLog(page);
    await expect(page.locator('.ol-line').first()).toBeVisible();
    const box = page.locator('.lg-box');
    const gap = () => box.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
    await expect.poll(() => box.evaluate((el) => el.scrollHeight - el.clientHeight), 'the log overflows').toBeGreaterThan(100);
    await expect.poll(gap).toBeLessThanOrEqual(2);
    // a little up, still within the end's 40 px: the view still follows
    await box.evaluate((el) => el.scrollBy({top: -30}));
    await expect.poll(gap).toBeGreaterThan(20);
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    // back to the end and the fold open, in one task: the scroll's event comes after the shrink
    await box.evaluate((el) => {
        el.scrollTop = el.scrollHeight;
        (document.querySelector('.lg-filters-toggle') as HTMLElement).click();
    });
    await expect(page.locator('#lg-filters')).toBeVisible();
    await expect.poll(gap).toBeLessThanOrEqual(2);
});

test('screenshots: the Log page with a text filter typed', async ({page, isMobile}, info) => {
    if (!isMobile) await page.setViewportSize({width: 1280, height: 720});
    await openLog(page);
    await expect(page.locator('.ol-line').first()).toBeVisible();
    await page.getByRole('textbox', {name: 'Text filter'}).fill('r');
    await expect(page.getByRole('button', {name: 'Clear filter'})).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
    await page.evaluate(() => document.fonts.ready);
    const body = await page.screenshot();
    await info.attach(`log-${info.project.name}`, {body, contentType: 'image/png'});
    const dir = process.env.T104_SHOTS;
    if (dir) await page.screenshot({path: `${dir}/log-${info.project.name}.png`});
});

test('under reduced motion the fold opens at once; German names the toggle and the ✕', async ({page, isMobile}) => {
    test.skip(!isMobile, 'the phone project');
    await page.emulateMedia({reducedMotion: 'reduce'});
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await openLog(page);
    const toggle = page.locator('.lg-filters-toggle');
    await expect(toggle).toContainText('Filter');
    await toggle.click();
    // no frame in between: the row is there when the click returns, and nothing animates it
    expect(await page.locator('#lg-filters').count()).toBe(1);
    expect(await page.locator('#lg-filters').evaluate((el) => el.getAnimations().length)).toBe(0);
    const input = page.getByRole('textbox', {name: 'Textfilter'});
    await input.fill('rfd');
    await expect(page.getByRole('button', {name: 'Filter löschen'})).toBeVisible();
});
