import {expect, test} from '@playwright/test';
import {fitsWindow} from './scroll';

// Task 179: the § in the top bar opens the Licences page - one flat list from the SBOM - also
// before the login.

test('the § opens the list, logged out too', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {authenticated: false, setup_required: false}}));
    await page.goto('/');
    await expect(page.locator('form.ol-card')).toBeVisible();
    await page.locator('a.ol-licences').click();
    await expect(page).toHaveURL(/\/licenses$/);
    await expect(page.locator('.lic-table tbody tr').first()).toBeVisible();
    await expect(page.locator('form.ol-card')).toHaveCount(0);
    await expect(page.locator('a.ol-licences')).toHaveClass(/active/);
});

// the front groups first, then the rest by name - and the page says nothing about the order
test('the front groups first, then the rest by name', async ({page}) => {
    await page.goto('/licenses');
    const names = page.locator('.lic-table tbody td.lic-name');
    await expect(names.first()).toBeVisible();
    const all = await names.allTextContents();
    // task 116: the KNX User Forum's icon set right below hmcfgusb
    expect(all.slice(0, 10)).toEqual(['occulited', 'OpenCCU', 'detect_radio_module', 'generic_raw_uart', 'github.com/mdzio/go-hmccu', 'hmlangw', 'hmcfgusb', 'knx-uf-iconset', 'HMIPServer.jar', 'rfd']);
    await expect(page.locator('tbody tr', {has: page.locator('td.lic-name', {hasText: /^occulited$/})}).locator('td.lic-author')).toHaveText('Sebastian Raff');
    // the line about the GPL sources is gone (task 236, the maintainer)
    await expect(page.getByText('The sources of the GPL')).toHaveCount(0);
    expect(all).toContain('io.netty/netty-codec');
    await expect(page.locator('.lic-search .ol-muted')).toHaveText('19 components');
});

test('the search finds every kind of row; back returns to the list with the search kept', async ({page}) => {
    await page.goto('/licenses');
    const search = page.getByRole('searchbox');
    const rows = page.locator('.lic-table tbody tr');
    for (const [q, name] of [
        ['busybox', 'busybox'],
        ['go-hmccu', 'github.com/mdzio/go-hmccu'],
        ['generic_raw_uart', 'generic_raw_uart'],
        ['hmcfgusb', 'hmcfgusb'],
        ['netty', 'io.netty/netty-codec'],
    ]) {
        await search.fill(q);
        await expect(rows).toHaveCount(1);
        await expect(rows.locator('td.lic-name')).toHaveText(name);
    }
    await search.fill('busybox');
    await rows.locator('td.lic-name a').click();
    await expect(page).toHaveURL(/\/licenses\/c\/busybox%401\.37\.0$/);
    await expect(page.locator('h2.lic-title')).toContainText('busybox');
    await expect(page.locator('pre.lic-text')).toContainText('GNU GENERAL PUBLIC LICENSE');
    await expect(page.locator('.lic-facts')).toContainText('Erik Andersen');
    await page.goBack();
    await expect(page).toHaveURL(/\/licenses$/);
    await expect(page.getByRole('searchbox')).toHaveValue('busybox');
    await expect(rows).toHaveCount(1);
});

// the maintainer, 2026-09-19: the back link took two clicks - the router's link handler went to the
// list, then the page's own stepped back to the component
test('the back link returns to the list with one click, the search kept, also after a step to a part', async ({page}) => {
    await page.goto('/licenses');
    const search = page.getByRole('searchbox');
    const rows = page.locator('.lic-table tbody tr');
    await search.fill('busybox');
    await rows.locator('td.lic-name a').click();
    await expect(page.locator('h2.lic-title')).toContainText('busybox');
    await page.locator('.lic-back a').click();
    await expect(page).toHaveURL(/\/licenses$/);
    await expect(search).toHaveValue('busybox');
    await expect(rows).toHaveCount(1);

    // the list, a JAR, one of its libraries: back goes to the list, not to the JAR
    await search.fill('');
    await rows.filter({has: page.locator('td.lic-name', {hasText: /^HMIPServer\.jar$/})}).locator('td.lic-name a').click();
    await page.locator('table[data-parts] td.lic-name a', {hasText: 'io.netty/netty-codec'}).click();
    await expect(page.locator('h2.lic-title')).toContainText('io.netty/netty-codec');
    await page.locator('.lic-back a').click();
    await expect(page).toHaveURL(/\/licenses$/);

    // the browser's Back from the library to the JAR, then the link: still the list
    await rows.filter({has: page.locator('td.lic-name', {hasText: /^HMIPServer\.jar$/})}).locator('td.lic-name a').click();
    await page.locator('table[data-parts] td.lic-name a', {hasText: 'io.netty/netty-codec'}).click();
    await page.goBack();
    await expect(page.locator('h2.lic-title')).toContainText('HMIPServer.jar');
    await page.locator('.lic-back a').click();
    await expect(page).toHaveURL(/\/licenses$/);
    // the entries are the browser's as ever: Forward is the JAR again
    await page.goForward();
    await expect(page.locator('h2.lic-title')).toContainText('HMIPServer.jar');
});

test("a component's page from a link, and its license text through the stored ref", async ({page}) => {
    await page.goto('/licenses/c/linux%406.18.51');
    await expect(page.locator('pre.lic-text')).toContainText('GNU GENERAL PUBLIC LICENSE');
    await page.locator('.lic-back a').click();
    await expect(page).toHaveURL(/\/licenses$/);
});

// the maintainer, 2026-09-19: licenseinfo.htm is not in the image any more (eQ-3's outdated CCU3 list)
test('without an SBOM the page says so, and nothing links licenseinfo.htm', async ({page}) => {
    await page.route('**/api/system/v1/sbom', (r) => r.fulfill({status: 404, json: {error: 'not-found', message: 'this image carries no SBOM'}}));
    await page.goto('/licenses');
    await expect(page.locator('.lic-page .ol-notice')).toHaveText('This image carries no SBOM yet.');
    await expect(page.locator('a[href*="licenseinfo"]')).toHaveCount(0);
});

test('the list fits the window', async ({page}) => {
    await page.goto('/licenses');
    await expect(page.locator('.lic-table tbody tr').first()).toBeVisible();
    expect(await fitsWindow(page)).toBe(true);
    await page.goto('/licenses/c/busybox%401.37.0');
    await expect(page.locator('pre.lic-text')).toBeVisible();
    expect(await fitsWindow(page)).toBe(true);
});

test('the § is an icon like the GitHub mark, and the icons sit close together', async ({page}, info) => {
    await page.goto('/licenses');
    const para = page.locator('a.ol-licences');
    const gh = page.locator('a.ol-iconlink[href^="https://github.com"]');
    await expect(para.locator('svg')).toBeVisible();
    // no text in the link: nothing to underline
    expect((await para.textContent())?.trim()).toBe('');
    const a = await para.locator('svg').boundingBox();
    const b = await gh.locator('svg').boundingBox();
    expect(a?.width).toBe(b?.width);
    expect(a?.height).toBe(b?.height);
    // the links' boxes are 6 px apart (the bar's 12 px gap less 6); on a phone the bar's own 4 px
    const pa = await para.boundingBox();
    const pb = await gh.boundingBox();
    const gap = (pb?.x ?? 0) - ((pa?.x ?? 0) + (pa?.width ?? 0));
    expect(gap).toBeCloseTo(info.project.name === 'phone' ? 4 : 6, 0);
});

// the maintainer, 2026-09-19: the licence cell leads to the text too, and an Author column says who
// made it where the SBOM knows (the authors or the supplier, else the copyright holder)
test('the license cell opens the text, and the Author column names who made it', async ({page}) => {
    await page.goto('/licenses');
    const row = (name: string) => page.locator('tbody tr', {has: page.locator('td.lic-name', {hasText: new RegExp(`^${name}$`)})});
    await expect(row('rfd').locator('td.lic-author')).toHaveText('eQ-3 AG');
    await expect(row('generic_raw_uart').locator('td.lic-author')).toHaveText('Alexander Reinert');
    await expect(row('busybox').locator('td.lic-author')).toHaveText(/^Erik Andersen/);
    await expect(row('lighttpd').locator('td.lic-author')).toHaveText('');
    await page.getByRole('searchbox').fill('reinert');
    await expect(page.locator('tbody tr')).toHaveCount(2);
    await page.getByRole('searchbox').fill('');
    await row('rfd').locator('td.lic-lic a').click();
    await expect(page).toHaveURL(/\/licenses\/c\//);
    await expect(page.locator('.lic-text').first()).toBeVisible();
});

// the maintainer, 2026-09-19: one component in two places is one row, its origins comma-separated,
// and the other place's deep link opens the same row
test('busybox in the image and in the recovery system is one row', async ({page}) => {
    await page.goto('/licenses');
    await page.getByRole('searchbox').fill('busybox');
    const rows = page.locator('.lic-table tbody tr');
    await expect(rows).toHaveCount(1);
    await expect(rows.locator('td.lic-origin')).toHaveText('buildroot, recovery-system');
    await page.goto('/licenses/c/' + encodeURIComponent('recovery-system/busybox@1.37.0'));
    await expect(page.locator('h2.lic-title')).toContainText('busybox');
    await expect(page.locator('.lic-facts')).toContainText('buildroot, recovery-system');
});

// the maintainer, 2026-09-19: an origin is a link to its repository, its title without the author
test('an origin links to its repository, a nested one to its parent', async ({page}) => {
    await page.goto('/licenses');
    const row = (name: string) => page.locator('tbody tr', {has: page.locator('td.lic-name', {hasText: new RegExp(`^${name.replace(/[./]/g, '\\$&')}$`)})});
    await expect(row('generic_raw_uart').locator('td.lic-origin a')).toHaveAttribute('href', 'https://github.com/alexreinert/piVCCU');
    await expect(row('generic_raw_uart').locator('td.lic-origin')).toHaveText('piVCCU');
    await expect(row('rfd').locator('td.lic-origin')).toHaveText('OpenCCU-Base');
    await row('io.netty/netty-codec').locator('td.lic-origin a').click();
    await expect(page).toHaveURL(/\/licenses\/c\/HMIPServer\.jar/);
    await expect(page.locator('h2.lic-title')).toContainText('HMIPServer.jar');
    // the JAR's page lists its libraries, each to its own page
    const parts = page.locator('table[data-parts] td.lic-name a');
    await expect(parts).toHaveText(['com.google.code.gson/gson', 'io.netty/netty-codec']);
    await parts.last().click();
    await expect(page.locator('h2.lic-title')).toContainText('io.netty/netty-codec');
    await expect(page.locator('table[data-parts]')).toHaveCount(0);
});


// task 236 (the maintainer): openccu-lite's own license and author under the product line, and the
// license's disclaimer of warranty in a panel at the head's right - its bold title the panel's
// title, the text only there (moved, not repeated; no license or author in the panel), as high as
// the left area (taller when the text needs it) and ending at the table's right edge, the whole
// text always shown, never a scrollbar (the maintainer); on a phone the
// first thing on the page, above the heading, and whole. German too, where the disclaimer is OpenCCU's own German wording.
const DISCLAIMER = 'Unless required by applicable law or agreed to in writing, openccu-lite is provided by the Contributors (and each Contributor provides its Contributions) on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND';
const DISCLAIMER_DE = 'openccu-lite wird OHNE JEDE AUSDRÜCKLICHE ODER IMPLIZIERTE GARANTIE bereitgestellt, einschließlich der Garantie zur Benutzung für den vorgesehenen oder einem bestimmten Zweck sowie jeglicher Rechtsverletzung, jedoch nicht darauf beschränkt. IN KEINEM FALL sind die Autoren oder Copyrightinhaber';

test("openccu-lite's license and author, and the disclaimer in the panel", async ({page, request}) => {
    await page.goto('/licenses');
    const lines = page.locator('[data-own-lines]');
    await expect(lines).toHaveText(/License\s*Apache License 2\.0\s*Author\s*Sebastian Raff \(hobbyquaker\)/);
    const link = lines.locator('[data-own-license]');
    await expect(link).toHaveAttribute('href', '/openccu-lite-LICENSE.txt');
    // task 266: the privacy statement, under license and author
    const privacy = lines.locator('[data-own-privacy]');
    await expect(privacy).toHaveText('What the system sends to outside sources');
    await expect(privacy).toHaveAttribute('href', 'https://github.com/hobbyquaker/openccu-lite/blob/main/docs/privacy.md');
    await expect(privacy).toHaveAttribute('target', '_blank');
    const text = await (await request.get('/openccu-lite-LICENSE.txt')).text();
    expect(text).toContain('Apache License');
    expect(text).toContain('Version 2.0, January 2004');
    // licence and author once on the page, under the heading - not again in the panel
    await expect(page.getByText('Sebastian Raff (hobbyquaker)')).toHaveCount(1);
    await expect(page.locator('.lic-top').getByText('Apache License 2.0')).toHaveCount(1);
    const panel = page.locator('[data-own-panel]');
    await expect(panel).not.toContainText('Sebastian Raff');
    await expect(panel).not.toContainText('Apache License 2.0');
    // the bold title is the panel's title, in the panels' h3; the text follows it
    await expect(panel.locator('> h3')).toHaveText('Disclaimer of Warranty');
    await expect(panel).toHaveAttribute('aria-labelledby', 'lic-disclaimer-title');
    const disclaimer = panel.locator('[data-disclaimer]');
    await expect(disclaimer).toContainText(DISCLAIMER);
    await expect(disclaimer).toContainText('You are solely responsible for determining the appropriateness of using or redistributing openccu-lite');
    await expect(disclaimer.locator('strong')).toHaveCount(0);
    // moved, not repeated: the disclaimer exists once, and nothing of it under the search field
    await expect(page.locator('[data-disclaimer]')).toHaveCount(1);
    await expect(page.getByText('Disclaimer of Warranty')).toHaveCount(1);
    await expect(page.locator('[data-lic-left]')).not.toContainText('WITHOUT WARRANTIES');

    const left = (await page.locator('[data-lic-left]').boundingBox())!;
    const box = (await panel.boundingBox())!;
    const table = (await page.locator('.lic-table').boundingBox())!;
    const search = (await page.locator('.lic-search').boundingBox())!;
    const phone = (page.viewportSize()?.width ?? 1280) <= 700;
    if (!phone) {
        // beside the left area, ending at the table's right edge, both from the row's top; as high as
        // the left area, or taller when the text needs it - the left area then stays at the top
        expect(Math.abs(box.x + box.width - (table.x + table.width))).toBeLessThanOrEqual(1);
        expect(Math.abs(box.y - left.y)).toBeLessThanOrEqual(1);
        expect(box.y + box.height).toBeGreaterThanOrEqual(left.y + left.height - 1);
        expect(search.y + search.height).toBeLessThanOrEqual(left.y + left.height + 1);
        expect(box.x).toBeGreaterThan(left.x + left.width);
        // wide: more than half of the row
        expect(box.width).toBeGreaterThan(table.width / 2);
        expect(table.y - (box.y + box.height)).toBeLessThan(30);
    }
    // never a scrollbar and no fade: the whole text shows, inside the panel
    expect(await disclaimer.evaluate((el) => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(0);
    expect(await disclaimer.evaluate((el) => [getComputedStyle(el).overflowY, getComputedStyle(el).maskImage])).toEqual(['visible', 'none']);
    const d = (await disclaimer.boundingBox())!;
    expect(d.y + d.height).toBeLessThanOrEqual(box.y + box.height);
    if (phone) {
        // on a phone the first thing on the page, above the heading, full width, the whole text in the flow
        expect(box.y + box.height).toBeLessThanOrEqual(left.y);
        expect(box.y).toBeLessThan((await page.locator('.lic-page h2').boundingBox())!.y);
        expect(Math.abs(box.x - left.x)).toBeLessThanOrEqual(1);
        expect(Math.abs(box.width - left.width)).toBeLessThanOrEqual(1);
        expect(table.y).toBeGreaterThan(left.y + left.height);
        expect(await fitsWindow(page)).toBe(true);
    }
});

test('the license and the disclaimer panel in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/licenses');
    await expect(page.locator('[data-own-lines]')).toHaveText(/Lizenz\s*Apache License 2\.0\s*Autor\s*Sebastian Raff \(hobbyquaker\)\s*Datenschutz\s*Was das System nach außen sendet/);
    const panel = page.locator('[data-own-panel]');
    await expect(panel.locator('> h3')).toHaveText('Haftungsausschluss');
    await expect(panel).not.toContainText('Sebastian Raff');
    const disclaimer = panel.locator('[data-disclaimer]');
    await expect(disclaimer).toContainText(DISCLAIMER_DE);
    await expect(disclaimer).toContainText('oder sonstiger Verwendung der Software entstanden.');
    await expect(page.locator('[data-disclaimer]')).toHaveCount(1);
});

// the maintainer: the disclaimer never scrolls - the whole text at every desktop width, in both languages
test('the disclaimer panel shows its whole text at 1024 to 1920 px', async ({page}, info) => {
    test.skip(info.project.name !== 'desktop-light', 'the widths are set here');
    for (const lang of ['en', 'de']) {
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        for (const width of [1024, 1280, 1440, 1920]) {
            await page.setViewportSize({width, height: 800});
            await page.goto('/licenses');
            const disclaimer = page.locator('[data-own-panel] [data-disclaimer]');
            await expect(disclaimer).toBeVisible();
            expect(await disclaimer.evaluate((el) => el.scrollHeight - el.clientHeight), `${lang} ${width}`).toBeLessThanOrEqual(0);
            const box = (await page.locator('[data-own-panel]').boundingBox())!;
            const left = (await page.locator('[data-lic-left]').boundingBox())!;
            const table = (await page.locator('.lic-table').boundingBox())!;
            const d = (await disclaimer.boundingBox())!;
            expect(d.y + d.height, `${lang} ${width}`).toBeLessThanOrEqual(box.y + box.height);
            expect(Math.abs(box.x + box.width - (table.x + table.width)), `${lang} ${width}`).toBeLessThanOrEqual(1);
            expect(Math.abs(box.y - left.y), `${lang} ${width}`).toBeLessThanOrEqual(1);
        }
    }
});
