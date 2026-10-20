import {expect, test, type Page} from '@playwright/test';

// Task 125 (D-77): the session's legacy alias in addon URLs, on by default for the addons that do not
// read the session header, with the switches and the warning; the downloads without the session.
// The stub keeps the switches per browser under the cookie stub-legacy=<id>, and plants the Status
// warning only for such a browser.
const ALIAS = 'sid=@LeGaCy0001@';
const kept = (page: Page, prefix: string) => page.locator(`iframe.ol-kept-frame[src^="${prefix}"]`);
const own = async (page: Page, baseURL: string | undefined, info: {project: {name: string}; title: string}) => {
    await page.context().addCookies([{name: 'stub-legacy', value: `${info.project.name}-${info.title}`.replace(/[^\w-]/g, '_'), url: baseURL}]);
};
const row = (page: Page, name: string) => page.locator('.ad-card').filter({has: page.locator('.ad-name', {hasText: new RegExp(`^\\W*${name}$`)})}).first();

test('the Addons page marks the addons that get the alias, and the switch in the ⋯ menu takes it away', async ({page, baseURL}, info) => {
    await own(page, baseURL, info);
    await page.context().addCookies([{name: 'stub-session-header', value: '1', url: baseURL}]);
    await page.goto('/addons');
    const mosquitto = row(page, 'Mosquitto');
    const redmatic = row(page, 'RedMatic');
    await expect(mosquitto.locator('.ad-legacy')).toHaveText('session in URL');
    await expect(redmatic.locator('.ad-legacy')).toHaveCount(0);
    // the badge explains itself as a popup (task 51)
    await mosquitto.locator('.ad-legacy').click();
    await expect(page.getByRole('tooltip')).toContainText('does not read the session header');
    await page.keyboard.press('Escape');
    // the settings page opens with the alias, never the session id
    await page.goto('/addon-settings/mosquitto');
    await expect(kept(page, '/addons/mosquitto/settings.cgi')).toHaveAttribute('src', new RegExp(`^/addons/mosquitto/settings\\.cgi\\?${ALIAS}&theme=\\w+&lang=\\w+$`));
    // the switch, off
    await page.goto('/addons');
    await row(page, 'Mosquitto').getByRole('button', {name: 'More actions'}).click();
    const item = page.getByRole('menuitemcheckbox', {name: /Pass the session in the URL/});
    await expect(item).toHaveAttribute('aria-checked', 'true');
    await item.click();
    await expect(row(page, 'Mosquitto').locator('.ad-legacy-off')).toHaveText('no session in URL');
    await expect(row(page, 'Mosquitto').locator('.ad-legacy')).toHaveCount(0);
    await page.goto('/addon-settings/mosquitto');
    await expect(kept(page, '/addons/mosquitto/settings.cgi')).toHaveAttribute('src', /^\/addons\/mosquitto\/settings\.cgi\?theme=\w+&lang=\w+$/);
    // and on again
    await page.goto('/addons');
    await row(page, 'Mosquitto').getByRole('button', {name: 'More actions'}).click();
    await expect(page.getByRole('menuitemcheckbox', {name: /Pass the session in the URL/})).toHaveAttribute('aria-checked', 'false');
    await page.getByRole('menuitemcheckbox', {name: /Pass the session in the URL/}).click();
    await expect(row(page, 'Mosquitto').locator('.ad-legacy')).toHaveText('session in URL');
});

test('the Addons page switches the legacy session off for every addon', async ({page, baseURL}, info) => {
    await own(page, baseURL, info);
    await page.goto('/addons');
    await expect(page.getByRole('heading', {level: 2, name: 'Addon sessions'})).toBeVisible();
    const sw = page.getByRole('checkbox', {name: /Pass the session in addon URLs/});
    await expect(sw).toBeChecked();
    const save = page.locator('h2:has-text("Addon sessions") ~ .ol-actions').first().getByRole('button', {name: 'Save'});
    await expect(save).toBeDisabled();
    await sw.uncheck();
    await expect(page.getByText('will refuse to open')).toBeVisible();
    await save.click();
    await expect(page.locator('.ol-notice', {hasText: 'Saved.'}).first()).toBeVisible();
    // no addon gets the alias now: the frame, the new-tab link and the settings page are plain
    await page.goto('/nav/red');
    await expect(kept(page, '/addons/red/')).toHaveAttribute('src', /^\/addons\/red\/\?theme=\w+&lang=\w+$/);
    await page.goto('/addon-settings/mosquitto');
    await expect(kept(page, '/addons/mosquitto/settings.cgi')).toHaveAttribute('src', /^\/addons\/mosquitto\/settings\.cgi\?theme=\w+&lang=\w+$/);
    // the Addons page says so, and the per-addon item is off with the reason
    await page.goto('/addons');
    await expect(row(page, 'Mosquitto').locator('.ad-legacy-off')).toHaveText('no session in URL');
    await row(page, 'Mosquitto').getByRole('button', {name: 'More actions'}).click();
    await expect(page.getByRole('menuitemcheckbox', {name: /Pass the session in the URL/})).toBeDisabled();
    await expect(page.getByRole('menuitemcheckbox', {name: /Pass the session in the URL/})).toContainText('off for all addons, see Addon sessions below');
});

test('the Status page warns which addons receive the session, and the warning goes with the switch', async ({page, baseURL}, info) => {
    await own(page, baseURL, info);
    await page.goto('/');
    const warning = page.locator('[data-warnings] [data-notice="legacy-session"]');
    await expect(warning).toBeVisible();
    await expect(warning).toContainText('receive your session in their URLs');
    await expect(warning).toContainText('RedMatic');
    await expect(warning).toContainText('Mosquitto');
    await expect(warning.getByRole('link', {name: 'Addons'})).toHaveAttribute('href', '/addons');
    await page.goto('/addons');
    await page.getByRole('checkbox', {name: /Pass the session in addon URLs/}).uncheck();
    await page.locator('h2:has-text("Addon sessions") ~ .ol-actions').first().getByRole('button', {name: 'Save'}).click();
    await expect(page.locator('.ol-notice', {hasText: 'Saved.'}).first()).toBeVisible();
    await page.goto('/');
    await expect(page.locator('[data-warnings]')).toBeVisible();
    await expect(page.locator('[data-warnings] [data-notice="legacy-session"]')).toHaveCount(0);
});

test('the Backup download carries no session: a one-time ticket is fetched on the click', async ({page}) => {
    await page.goto('/backup');
    // occulited task 88: a button now, no href - the ticket's URL is the only one fetched
    const link = page.getByRole('button', {name: 'Download backup', exact: true});
    await expect(link).toBeVisible();
    const [ticket, fetched] = await Promise.all([
        page.waitForRequest((r) => r.url().endsWith('/api/auth/v1/ticket') && r.method() === 'POST'),
        page.waitForRequest((r) => r.url().includes('/api/system/v1/backup?')),
        link.click(),
    ]);
    expect(ticket.postDataJSON()).toEqual({path: '/api/system/v1/backup'});
    const url = new URL(fetched.url());
    expect(url.searchParams.get('ticket')).toBe('TICKETAAAAAAAAAAAAAAAAAAAA');
    expect(url.searchParams.has('sid')).toBe(false);
});

test('the Log download fetches a ticket for its own path', async ({page}) => {
    await page.goto('/log?unit=rfd');
    await page.getByRole('button', {name: 'Download'}).click();
    const [ticket, fetched] = await Promise.all([
        page.waitForRequest((r) => r.url().endsWith('/api/auth/v1/ticket') && r.method() === 'POST'),
        page.waitForRequest((r) => r.url().includes('/api/system/v1/log/download?')),
        page.locator('a[data-format="json"]').click(),
    ]);
    expect(ticket.postDataJSON()).toEqual({path: '/api/system/v1/log/download'});
    const p = new URL(fetched.url()).searchParams;
    expect(p.get('ticket')).toBe('TICKETAAAAAAAAAAAAAAAAAAAA');
    expect(p.get('format')).toBe('json');
    expect(p.has('sid')).toBe(false);
});

test('a ?ticket= in the URL is exchanged for the session before the shell asks anything, and leaves the URL', async ({page}) => {
    const order: string[] = [];
    page.on('request', (r) => {
        const u = new URL(r.url());
        if (u.pathname.startsWith('/api/')) order.push(`${r.method()} ${u.pathname}`);
    });
    const redeem = page.waitForRequest((r) => r.url().endsWith('/api/auth/v1/ticket/redeem'));
    await page.goto('/network?confirm=tok123&ticket=TICKETAAAAAAAAAAAAAAAAAAAA');
    expect((await redeem).postDataJSON()).toEqual({ticket: 'TICKETAAAAAAAAAAAAAAAAAAAA'});
    // the ticket leaves the URL at once (the page then consumes ?confirm= on its own)
    await expect(page).not.toHaveURL(/ticket=/);
    await expect(page).toHaveURL(/\/network/);
    await expect(page.getByRole('heading', {level: 1})).toBeVisible();
    // the redeem comes before anything else, and no request carries the ticket or a session in its URL
    expect(order[0]).toBe('POST /api/auth/v1/ticket/redeem');
    expect(order.filter((o) => o.includes('ticket=') || o.includes('sid='))).toEqual([]);
});
