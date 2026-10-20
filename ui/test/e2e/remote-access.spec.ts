import {expect, test, type Page} from '@playwright/test';

// task 143 (D-95): System -> Remote access, classic CCU RPC. Each test keeps its own settings
// (stub-ra=<id>), so the projects that share the stub never see each other's changes.
async function own(page: Page, baseURL: string | undefined) {
    await page.context().addCookies([{name: 'stub-ra', value: `ra-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
}

test('the page: both switches off, the ports with their interfaces, lite-rpc off', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/remote-access');
    await expect(page.getByRole('heading', {level: 2, name: 'Classic RPC (as on a CCU)'})).toBeVisible();
    const plain = page.locator('[data-switch="plain"]');
    await expect(plain.getByRole('checkbox')).not.toBeChecked();
    await expect(plain.locator('tr[data-port="2010"]')).toContainText('HmIP-RF');
    await expect(plain.locator('tr[data-port="2010"]')).toContainText('running');
    await expect(page.locator('[data-switch="tls"] tr[data-port="49292"]')).toContainText('not running');
    await expect(plain).toContainText('2000 (BidCos-Wired) only where hs485d runs.');
    await expect(page.getByRole('radio', {name: 'None'})).toBeChecked();
    // task 223: the firewall panel says there is nothing to let in
    const fw = page.locator('[data-ra-hint]');
    await expect(fw).toHaveAttribute('data-ra-hint', 'closed');
    await expect(fw.locator('.fw-port')).toHaveCount(0);
    await expect(fw).toContainText('No classic port is switched on, so there is nothing for the firewall to let in.');
    await expect(page.locator('table.ra-rules')).toHaveCount(0);
    await expect(page.locator('[data-switch="lite"]')).toContainText('always on');
    await expect(page.getByRole('button', {name: 'Save'})).toBeDisabled();
});

test('plain on without a login: the warning, then the owned rules', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/remote-access');
    await page.locator('[data-switch="plain"]').getByRole('checkbox').check();
    await expect(page.locator('[data-notice="ra-noauth"]')).toHaveText('Classic RPC without authentication: anyone the firewall lets in controls every device.');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/remote-access')),
        page.getByRole('button', {name: 'Save'}).click(),
    ]);
    expect(req.postDataJSON()).toEqual({classic: {plain: true, tls: false, auth: 'none'}});
    await expect(page.locator('[data-notice="ra-notice"]')).toHaveText('Saved; lighttpd restarts in a moment to open or close the ports - the page may not answer for a second.');
    // task 223: the access points' panel instead of the rule table - each open port's verdict
    const fw = page.locator('[data-ra-hint]');
    await expect(fw).toHaveAttribute('data-ra-hint', 'open');
    await expect(fw.locator('.fw-port')).toHaveText(['tcp 2001 ACCEPT from local networks', 'tcp 2010 ACCEPT from local networks', 'tcp 9292 ACCEPT from local networks']);
    await expect(fw).toContainText('Every open port is accepted from the source its rule names.');
    await expect(fw).not.toHaveClass(/error/);
    await expect(fw.getByRole('link', {name: 'Firewall'})).toHaveAttribute('href', '/system/firewall');
});

test('a typed pair: the rules, then TLS with the login, the plain-port warning', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/remote-access');
    await page.getByRole('radio', {name: 'User name and password'}).check();
    await expect(page.getByText('Set the user name and password first.')).toBeVisible();
    await expect(page.getByRole('button', {name: 'Save'})).toBeDisabled();
    const form = page.locator('[data-pair-form]');
    await form.getByLabel('User name').fill('iobroker');
    await form.getByLabel('Password', {exact: true}).fill('short');
    await expect(form.getByRole('button', {name: 'Set password'})).toBeDisabled();
    await form.getByLabel('Password', {exact: true}).fill('a long enough password');
    await form.getByLabel('Repeat').fill('a long enough passwort');
    await expect(form.getByText('The passwords differ.')).toBeVisible();
    await form.getByLabel('Repeat').fill('a long enough password');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/remote-access/classic-password')),
        form.getByRole('button', {name: 'Set password'}).click(),
    ]);
    expect(req.postDataJSON()).toEqual({user: 'iobroker', password: 'a long enough password'});
    await expect(page.locator('[data-pair]')).toContainText('iobroker');
    await expect(page.locator('[data-pair]')).toContainText('password set');
    await expect(page.locator('[data-notice="ra-generated"]')).toHaveCount(0);
    await page.locator('[data-switch="tls"]').getByRole('checkbox').check();
    await page.locator('[data-switch="plain"]').getByRole('checkbox').check();
    await expect(page.locator('[data-notice="ra-plainpw"]')).toContainText('the password travels in clear text');
    await expect(page.locator('[data-notice="ra-noauth"]')).toHaveCount(0);
    await page.getByRole('button', {name: 'Save'}).click();
    await expect(page.locator('[data-ra-hint] .fw-port')).toHaveCount(6);
});

test('a generated password is shown once, with a copy button', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/remote-access');
    await page.getByRole('radio', {name: 'User name and password'}).check();
    await page.locator('[data-pair-form]').getByRole('button', {name: 'Generate'}).click();
    const shown = page.locator('[data-notice="ra-generated"]');
    await expect(shown).toContainText('The generated password (shown only now):');
    await expect(shown.locator('code')).toHaveText('Gen3rat3dPassw0rdOnlyShownOnce2x');
    await expect(shown.getByRole('button', {name: 'Copy'})).toBeVisible();
    await page.reload();
    await expect(page.locator('[data-notice="ra-generated"]')).toHaveCount(0);
    await expect(page.locator('[data-pair]')).toContainText('ccu');
    // Change opens the form again, Cancel closes it
    await page.locator('[data-pair]').getByRole('button', {name: 'Change'}).click();
    await expect(page.locator('[data-pair-form]')).toBeVisible();
    await page.locator('[data-pair-form]').getByRole('button', {name: 'Cancel'}).click();
    await expect(page.locator('[data-pair-form]')).toHaveCount(0);
});

test('in German', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/remote-access');
    await expect(page.getByRole('heading', {level: 2, name: 'Klassisches RPC (wie auf einer CCU)'})).toBeVisible();
    await expect(page.locator('[data-switch="plain"]')).toContainText('Klartext-Ports');
    await expect(page.getByRole('radio', {name: 'Benutzername und Passwort'})).toBeVisible();
});

test('a user reads it and changes nothing', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.goto('/system/remote-access');
    await expect(page.locator('[data-switch="plain"]').getByRole('checkbox')).toBeDisabled();
    await expect(page.getByRole('button', {name: 'Save'})).toHaveCount(0);
});

// task 223: a port that is switched on and not accepted - the error look and the hint
test('the firewall panel: an open port a rule rejects', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.context().addCookies([{name: 'stub-ra-fw', value: 'blocked', url: baseURL!}]);
    await page.goto('/system/remote-access');
    await page.locator('[data-switch="plain"]').getByRole('checkbox').check();
    await page.getByRole('button', {name: 'Save'}).click();
    const fw = page.locator('[data-ra-hint]');
    await expect(fw).toHaveAttribute('data-ra-hint', 'blocked');
    await expect(fw).toHaveClass(/error/);
    await expect(fw.locator('.fw-port[data-port="2010"]')).toHaveText('tcp 2010 REJECT from anywhere');
    await expect(fw).toContainText('A port that is switched on is not accepted: clients cannot reach it.');
});

// task 223 (the maintainer, 2026-09-24): three headings - Classic RPC (switches, authentication,
// firewall, registered clients), lite-rpc (its panel over the full width, the streams, the API
// tokens), SSH (three panels) - and Control without a login no longer here
test('the order: Classic RPC, lite-rpc, SSH, each with its parts', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/remote-access');
    await page.locator('[data-switch="plain"]').getByRole('checkbox').check();
    await page.getByRole('button', {name: 'Save'}).click();
    await expect(page.locator('h2#subscribers, h3#subscribers')).toBeVisible();
    await expect(page.locator('main h2')).toHaveText(['Classic RPC (as on a CCU)', 'lite-rpc', 'SSH']);
    await expect(page.locator('h2#public')).toHaveCount(0);
    // the parts in document order, by their marks
    const marks = ['#classic', '[data-panel="classic-auth"]', '#classic-firewall', '[data-ra-hint]', '#subscribers', '#lite-rpc', '[data-switch="lite"]', '#streams', '[data-panel="api-tokens"]', '[data-panel="rpc-trace"]', '#ssh', '[data-panel="ssh-access"]', '[data-panel="ssh-keys"]', '[data-panel="ssh-password"]'];
    const tops: number[] = [];
    for (const m of marks) tops.push((await page.locator(m).first().boundingBox())!.y);
    for (let i = 1; i < tops.length; i++) expect(tops[i], `${marks[i]} below ${marks[i - 1]}`).toBeGreaterThan(tops[i - 1]!);
    // the registered clients and the tokens are parts of their sections: h3
    await expect(page.locator('h3#subscribers')).toHaveText('Registered clients');
    await expect(page.locator('h3#streams')).toHaveText('Open streams');
    await expect(page.locator('h3#api-tokens')).toHaveText('API tokens');
    // task 224: the RPC trace, the lite-rpc section's last panel
    await expect(page.locator('[data-panel="rpc-trace"] h3#rpc-trace')).toHaveText('RPC trace');
    await expect(page.locator('[data-panel="ssh-access"] h3')).toHaveText('Access');
    await expect(page.locator('[data-panel="ssh-keys"] h3')).toHaveText('Keys');
    await expect(page.locator('[data-panel="ssh-password"] h3')).toHaveText('Root password');
    // lite-rpc's panel over the page's width: as wide as the authentication panel
    const lite = (await page.locator('[data-switch="lite"]').boundingBox())!;
    const authPanel = (await page.locator('[data-panel="classic-auth"]').boundingBox())!;
    expect(Math.abs(lite.width - authPanel.width)).toBeLessThan(1);
    expect(Math.abs(lite.x - authPanel.x)).toBeLessThan(1);
    // task 224: the trace's panel as wide as lite-rpc's
    const trace = (await page.locator('[data-panel="rpc-trace"]').boundingBox())!;
    expect(Math.abs(trace.width - lite.width)).toBeLessThan(1);
    expect(Math.abs(trace.x - lite.x)).toBeLessThan(1);
});

// task 193: the Control app's public mode - since task 223 on the Settings page, in a panel of the
// settings' width; its plain words, the PUT of its own, and the Status page's warning while it is on
test('Control without a login: on the Settings page - the switch, the account, the warning', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/settings');
    const card = page.locator('[data-switch="public"]');
    await expect(page.locator('h2#public')).toHaveText('Control without a login');
    const box = (await card.boundingBox())!;
    // two of the settings' cards wide: its edges are those of the Control cards above it
    const start = (await page.locator('[data-setting="start-page"]').boundingBox())!;
    const full = (await page.locator('[data-setting="app-fullscreen"]').boundingBox())!;
    expect(Math.abs(box.x - start.x)).toBeLessThan(1);
    if (full.y === start.y) expect(Math.abs(box.x + box.width - (full.x + full.width))).toBeLessThan(1);
    await expect(card.getByRole('checkbox')).not.toBeChecked();
    await expect(card.locator('[data-notice="ra-public"]')).toHaveCount(0);
    await expect(card.locator('[data-public-save]')).toBeDisabled();
    await card.getByRole('checkbox').check();
    await expect(card.locator('[data-notice="ra-public"]')).toContainText('operable by anyone on the network');
    await card.getByRole('textbox').fill('panel');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/remote-access')),
        card.locator('[data-public-save]').click(),
    ]);
    expect(req.postDataJSON()).toEqual({public: {enabled: true, account: 'panel'}});
    await expect(card.locator('[data-notice="ra-public-saved"]')).toContainText('in force at once');
    await expect(card.locator('[data-public-save]')).toBeDisabled();
    // an administrator is refused by the system
    await card.getByRole('textbox').fill('admin');
    await card.locator('[data-public-save]').click();
    await expect(card.locator('.ol-notice.error').last()).toContainText('must be one that reads or operates');
});

test('Control without a login: in German, and not for a user', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/settings');
    await expect(page.locator('h2#public')).toHaveText('Bedienung ohne Anmeldung');
    // occulited task 76 (the maintainer): the text under the switch is its first sentence only; the
    // home-network advice is in the (?) Help
    await expect(page.locator('[data-switch="public"] .pc-text')).toHaveText('Wer den Webport erreicht, bedient das Haus: jedes Gerät in jedem Raum, ohne Anmeldung.');
    await page.locator('h2#public').getByRole('button', {name: 'Hilfe'}).click();
    await expect(page.getByText('Nur in einem Heimnetz verwenden: den Webport auf der Firewall-Seite')).toBeVisible();
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.reload();
    await expect(page.getByRole('heading', {level: 1})).toBeVisible();
    await expect(page.locator('[data-switch="public"]')).toHaveCount(0);
});

test('the old #public on Remote access leads to the Settings page', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/remote-access#public');
    await expect(page).toHaveURL(/\/settings#public$/);
    await expect(page.locator('[data-switch="public"]')).toBeVisible();
});

test('the warning on Status while Control is public leads to the switch', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-public-warn', value: '1', url: baseURL!}]);
    await page.goto('/');
    const notice = page.locator('[data-notice="app-public"]');
    await expect(notice).toContainText('Control is public: anyone who reaches the web port operates the house, as guest, without a login.');
    await notice.getByRole('link', {name: 'Settings', exact: true}).click();
    await expect(page).toHaveURL(/\/settings#public$/);
    await expect(page.locator('[data-switch="public"]')).toBeVisible();
});
