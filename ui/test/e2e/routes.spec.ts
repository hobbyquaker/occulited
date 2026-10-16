import {expect, test, type Page} from '@playwright/test';

// B-81: an addon's settings page is the shell's /addon-settings/<id>. It was /addons/<id>, which on a
// box is lighttpd's - the addons' www directories: typed, bookmarked or reloaded, that path got
// lighttpd's 403 and never reached the shell. The stub answers /addons/ itself in the same way.
// the frame on screen: kept pages of other addons may be there too, hidden (task 39)
const frame = (page: Page) => page.locator('iframe.ol-frame:visible');
const menuButton = (page: Page) => page.locator('.ol-menu').first().locator('.ol-menubtn');

/** Every link on the page that names the old settings path, /addons/<id> without anything after it. */
const oldSettingsLinks = (page: Page) =>
    page.evaluate(() =>
        Array.from(document.querySelectorAll('a[href]'))
            .map((a) => a.getAttribute('href') ?? '')
            .filter((h) => /^\/addons\/[^/?#]+$/.test(h)),
    );

test('a direct load and a reload of /addon-settings/<id> are the shell with the settings page in its frame', async ({page}) => {
    await page.goto('/addon-settings/mosquitto');
    await expect(frame(page)).toHaveAttribute('src', /^\/addons\/mosquitto\/settings\.cgi\?/);
    await expect(menuButton(page)).toHaveClass(/active/); // task 59: the plain Addons tab is marked, no breadcrumb
    await page.reload();
    await expect(frame(page)).toHaveAttribute('src', /^\/addons\/mosquitto\/settings\.cgi\?/);
});

test('/addons/<id> in the app stands for /addon-settings/<id> and leaves no history entry of its own', async ({page}) => {
    await page.goto('/system/services');
    await expect(page.getByRole('heading', {level: 1, name: 'Services'})).toBeVisible();
    await page.evaluate(() => {
        history.pushState(null, '', '/addons/hm2mqtt');
        dispatchEvent(new PopStateEvent('popstate'));
    });
    await expect(page).toHaveURL(/\/addon-settings\/hm2mqtt$/);
    await expect(frame(page)).toHaveAttribute('src', /^\/addons\/hm2mqtt\/settings\.cgi\?/);
    await expect(page.getByRole('heading', {level: 1, name: 'Addons'})).toHaveCount(0);
    await page.goBack();
    await expect(page).toHaveURL(/\/system\/services$/);
});

test('no link of the shell names /addons/<id> any more: not Installed addons, not the addon dropdown', async ({page}) => {
    await page.goto('/addons');
    await expect(page.locator('#addon-row-mosquitto')).toBeAttached();
    await expect(page.locator('a[href="/addon-settings/mosquitto"]').first()).toBeAttached();
    expect(await oldSettingsLinks(page)).toEqual([]);
    await menuButton(page).click();
    await expect(page.getByRole('link', {name: 'Settings for Mosquitto'})).toHaveAttribute('href', '/addon-settings/mosquitto');
    expect(await oldSettingsLinks(page)).toEqual([]);
});

// Task 57: every system page lives under /system/<page>. The paths the pages had before stand for
// the new ones - on load, on navigation and through Back and Forward, the query string kept - so a
// bookmark, the Status page's warnings and another page's link work before and after the move.
const SYSTEM_ALIASES: [string, string][] = [
    ['/services', '/system/services'],
    ['/users', '/system/users'],
    ['/log?unit=rfd', '/system/log?unit=rfd'],
    ['/network', '/system/network'],
    ['/certificate', '/system/certificates'],
    // task 132: the Security page went; its paths are the Users page
    ['/security', '/system/users'],
    ['/system/security', '/system/users'],
    ['/backup', '/system/backup'],
    ['/firmware', '/system/updates'],
    ['/system/firmware', '/system/updates'],
    ['/led', '/system/hardware'],
    // openccu-lite task 340: the Status LED page is the Hardware page
    ['/system/led', '/system/hardware'],
];

for (const [old, now] of SYSTEM_ALIASES) {
    test(`${old} loaded directly is ${now}`, async ({page}) => {
        await page.goto(old);
        await expect(page).toHaveURL(new RegExp(`${now.replace(/[?]/g, '\\?')}$`));
        await expect(page.locator('.ol-systab')).toHaveClass(/active/);
    });
}

test('an old system path reached by Back is replaced by its /system path, the query kept', async ({page}) => {
    await page.goto('/system/users');
    await expect(page.getByRole('heading', {level: 1, name: 'Users'})).toBeVisible();
    await page.evaluate(() => {
        history.pushState(null, '', '/log?unit=rfd');
        dispatchEvent(new PopStateEvent('popstate'));
    });
    await expect(page).toHaveURL(/\/system\/log\?unit=rfd$/);
    await expect(page.getByRole('heading', {level: 1, name: 'Log'})).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/system\/users$/);
    await page.goForward();
    await expect(page).toHaveURL(/\/system\/log\?unit=rfd$/);
});

test('/system alone opens the system page this browser showed last, Services on a fresh browser', async ({page}) => {
    await page.goto('/system');
    await expect(page).toHaveURL(/\/system\/services$/);
    await page.goto('/system/backup');
    await expect(page.getByRole('heading', {level: 1, name: 'Backup'})).toBeVisible();
    await page.goto('/system');
    await expect(page).toHaveURL(/\/system\/backup$/);
});

test('every /system/<page> loads directly and after a reload, with the right title', async ({page}) => {
    for (const [path, title] of [['/system/firewall', 'Firewall'], ['/system/certificates', 'Certificate'], ['/system/hardware', 'Hardware']]) {
        await page.goto(path);
        await expect(page.locator('.ol-systitle-btn')).toHaveText(new RegExp(`^${title}`));
        await page.reload();
        await expect(page.locator('.ol-systitle-btn')).toHaveText(new RegExp(`^${title}`));
    }
});

test('a deep link opens the page in its state: /system/certificates?mode=acme, /system/services?edit=<unit>', async ({page}) => {
    await page.goto('/system/certificates?mode=acme');
    await expect(page.getByRole('radio', {name: 'ACME'})).toBeChecked();
    // the mode chosen on the page goes into the query string without a history entry
    const depth = await page.evaluate(() => history.length);
    await page.getByRole('radio', {name: 'Manual'}).check();
    await expect(page).toHaveURL(/\/system\/certificates\?mode=manual$/);
    expect(await page.evaluate(() => history.length)).toBe(depth);

    await page.goto('/system/services?edit=rfd');
    await expect(page.getByRole('dialog', {name: /Edit unit/})).toBeVisible();
    await page.locator('.ol-modal-close').click();
    await expect(page).toHaveURL(/\/system\/services$/);
});

test('a filter typed on the Log page adds no history entry', async ({page}) => {
    await page.goto('/system/log');
    await expect(page.locator('.ol-line').first()).toBeVisible();
    const depth = await page.evaluate(() => history.length);
    await page.getByRole('textbox', {name: 'Text filter'}).fill('rfd');
    await page.keyboard.press('Enter');
    await expect(page.locator('.ol-line').first()).toBeVisible();
    expect(await page.evaluate(() => history.length)).toBe(depth);
});
