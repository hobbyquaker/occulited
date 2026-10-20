import {expect, test} from './fixtures';

// task 19 (D-53, D-54): an account here for every provider login, matched by username; password
// login beside the provider as a switch; the console as the way back. The stub's cookies:
// stub-oidc=on (a provider, password login on), stub-oidc=only (the switch off),
// stub-session-method=oidc (this session came through the provider).
const SIGNED_OUT = {authenticated: false, setup_required: false};

test('with a provider the login page has its button under Login', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'on', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: SIGNED_OUT}));
    await page.goto('/login');
    const login = page.getByRole('button', {name: 'Login'});
    const sso = page.getByRole('link', {name: 'Sign in with authentik'});
    await expect(page.locator('input[type=password]')).toBeVisible();
    await expect(login).toBeVisible();
    await expect(sso).toBeVisible();
    await expect(sso).toHaveAttribute('href', '/api/auth/v1/oidc/start');
    const a = await login.boundingBox();
    const b = await sso.boundingBox();
    expect(b!.y).toBeGreaterThanOrEqual(a!.y + a!.height);
    // the return target travels with the provider's link
    await page.goto('/login?return=%2Faddons%2Fred%2F');
    await expect(page.getByRole('link', {name: 'Sign in with authentik'})).toHaveAttribute('href', '/api/auth/v1/oidc/start?return=%2Faddons%2Fred%2F');
});

test('with password login off the form is gone and the provider is the way in', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'only', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: SIGNED_OUT}));
    await page.goto('/login');
    await expect(page.getByRole('link', {name: 'Sign in with authentik'})).toBeVisible();
    await expect(page.locator('input[type=password]')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Login'})).toHaveCount(0);
    await expect(page.locator('form.ol-card')).toContainText('Password login is switched off on this system: sign in through authentik.');
});

test('the first-boot setup takes a password whatever the switch says', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'only', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {authenticated: false, setup_required: true}}));
    await page.goto('/');
    await expect(page.locator('input[type=password]')).toHaveCount(2);
    await expect(page.getByRole('button', {name: 'Create administrator'})).toBeVisible();
    await expect(page.getByRole('link', {name: 'Sign in with authentik'})).toHaveCount(0);
});

test("the callback's refusal names the account that is missing", async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'on', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: SIGNED_OUT}));
    await page.goto('/login?error=no-account&user=alice');
    await expect(page.locator('form.ol-card .ol-notice.error')).toHaveText('There is no account alice on this system — an administrator has to create it first.');
    // the code and the name are taken off the address; a provider's own error text stays raw
    await expect(page).toHaveURL(/\/login$/);
    await page.goto('/login?error=access_denied%3A%20the%20user%20said%20no');
    await expect(page.locator('form.ol-card .ol-notice.error')).toHaveText('access_denied: the user said no');
});

test('the Users page says how each account signs in, and the password of a new one is optional', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'on', url: baseURL!}]);
    await page.goto('/users');
    // occulited task 69: a phone stacks the accounts' cells and hides the head - the column is in the table
    await expect(page.locator('[data-section="accounts"] thead th', {hasText: 'Sign-in'})).toHaveCount(1);
    const guest = page.locator('table.ol-table').first().locator('tbody tr', {hasText: 'guest'});
    await expect(guest).toContainText('provider only');
    await expect(guest).toContainText('last through the provider:');
    await expect(guest.getByRole('button', {name: 'Set password'})).toBeVisible();
    const admin = page.locator('table.ol-table').first().locator('tbody tr', {hasText: 'admin'}).first();
    await expect(admin.locator('td').nth(2)).toContainText('password');
    await expect(admin.getByRole('button', {name: 'Reset password'})).toBeVisible();
    await page.getByRole('button', {name: 'Add user'}).click();
    await expect(page.getByPlaceholder('Password (optional: empty = provider only)')).toBeVisible();
    await expect(page.getByText('The name must be exactly what authentik sends as the user name claim')).toBeVisible();
    const create = page.getByRole('button', {name: 'Create user'}).last();
    await expect(create).toBeDisabled();
    await page.locator('[data-section="accounts"]').getByPlaceholder('Username', {exact: true}).fill('alice');
    await expect(create).toBeEnabled();
    // a password, once typed, has to be one
    await page.getByPlaceholder('Password (optional: empty = provider only)').fill('short');
    await expect(create).toBeDisabled();
    // the created account goes out without a password
    await page.getByPlaceholder('Password (optional: empty = provider only)').fill('');
    const [req] = await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && r.url().includes('/api/auth/v1/users')), create.click()]);
    expect(req.postDataJSON()).toEqual({username: 'alice', password: '', level: 'operate'});
});

test('the Users page without a provider has no sign-in column and needs a password', async ({page}) => {
    await page.goto('/users');
    await expect(page.locator('[data-section="accounts"] thead th', {hasText: 'Username'})).toHaveCount(1);
    await expect(page.locator('[data-section="accounts"] thead th', {hasText: 'Sign-in'})).toHaveCount(0);
    await page.getByRole('button', {name: 'Add user'}).click();
    await expect(page.getByPlaceholder('Password (min. 8)')).toBeVisible();
    await page.locator('[data-section="accounts"]').getByPlaceholder('Username', {exact: true}).fill('alice');
    await expect(page.getByRole('button', {name: 'Create user'}).last()).toBeDisabled();
});

test('with password login off the Users page offers no passwords', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'only', url: baseURL!}]);
    await page.goto('/users');
    await expect(page.getByText('Password login is switched off (Authentication, below): every account signs in through authentik.')).toBeVisible();
    await expect(page.getByRole('button', {name: 'Reset password'})).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Set password'})).toHaveCount(0);
    await page.getByRole('button', {name: 'Add user'}).click();
    // the accounts' part: the provider's client secret further down is no user's password
    await expect(page.locator('[data-section="accounts"] input[type=password]')).toHaveCount(0);
    await page.goto('/account');
    await expect(page.getByText('Password login is switched off (System → Users → Authentication): accounts sign in through authentik.')).toBeVisible();
    await expect(page.getByPlaceholder('Current password')).toHaveCount(0);
});

test('the switch is off limits from a password session, and the group mapping is gone', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'on', url: baseURL!}]);
    await page.goto('/system/users');
    const sw = page.getByRole('checkbox', {name: /^Password login/});
    await expect(sw).toBeChecked();
    await expect(sw).toBeDisabled();
    await expect(page.getByText('Can only be switched off from a session that came through authentik')).toBeVisible();
    await expect(page.getByPlaceholder('ccu-admins')).toHaveCount(0);
    await expect(page.getByPlaceholder('groups')).toHaveCount(0);
    await expect(page.getByText('A login through authentik is the account of exactly that user name here')).toBeVisible();
    // the guide's fourth step names the account instead of the groups
    await page.getByRole('button', {name: 'Setting it up in authentik'}).click();
    await expect(page.getByRole('tooltip').locator('li').nth(3)).toContainText('Create the account here (System → Users)');
});

test('from a provider session the switch goes off after the break-glass is explained', async ({page, context, baseURL}) => {
    await context.addCookies([
        {name: 'stub-oidc', value: 'on', url: baseURL!},
        {name: 'stub-session-method', value: 'oidc', url: baseURL!},
    ]);
    await page.goto('/system/users');
    const sw = page.getByRole('checkbox', {name: /^Password login/});
    await expect(sw).toBeEnabled();
    await expect(sw).toBeChecked();
    await sw.uncheck();
    await expect(page.getByText('`occulited auth password-login on` turns this switch back on')).toBeVisible();
    let puts = 0;
    page.on('request', (r) => { if (r.method() === 'PUT' && r.url().includes('/api/auth/v1/config')) puts++; });
    const save = page.locator('button.primary', {hasText: 'Save'}).first();
    await save.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('authentik is the only way into the web interface');
    await expect(dialog).toContainText('occulited auth password-login on');
    await expect(dialog).toContainText('occulited passwd <user>');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(dialog).toBeHidden();
    expect(puts).toBe(0);
    await save.click();
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/auth/v1/config')),
        page.getByRole('dialog').getByRole('button', {name: 'Switch off'}).click(),
    ]);
    const body = req.postDataJSON();
    expect(body.password_login).toBe(false);
    expect(body.mode).toBe('oidc');
    expect(body).not.toHaveProperty('admin_groups');
    await expect(page.getByText('Saved.', {exact: true})).toBeVisible();
    // off now: the warning stays, and the switch can always be turned back on
    await expect(sw).not.toBeChecked();
    await expect(sw).toBeEnabled();
    await expect(page.getByText('`occulited auth password-login on` turns this switch back on')).toBeVisible();
});

test('with the switch off, turning it on is free and needs no dialog', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'only', url: baseURL!}]);
    await page.goto('/system/users');
    const sw = page.getByRole('checkbox', {name: /^Password login/});
    await expect(sw).not.toBeChecked();
    await expect(sw).toBeEnabled();
    await expect(page.getByText('`occulited auth password-login on` turns this switch back on')).toBeVisible();
    await sw.check();
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/auth/v1/config')),
        page.locator('button.primary', {hasText: 'Save'}).first().click(),
    ]);
    expect(req.postDataJSON().password_login).toBe(true);
    await expect(page.getByRole('dialog')).toHaveCount(0);
});

// openccu-lite B-206 (the maintainer: "when trying to save authentik config i get json unknown
// field modes"): the page sent GET's answer back, read-only fields included, and the API refuses
// any field it does not know. The PUT carries the writable fields only; a round trip saves.
test('saving the OIDC settings sends the writable fields only and saves', async ({page}) => {
    await page.goto('/system/users');
    await page.getByLabel('Issuer').fill('https://auth.example.org/application/o/lite/');
    await page.getByLabel('Client ID').fill('lite-client');
    await page.getByLabel('Client secret').fill('s3cret');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/auth/v1/config')),
        page.locator('button.primary', {hasText: 'Save'}).first().click(),
    ]);
    const body = req.postDataJSON();
    // (task 262: the session lengths ride with every save)
    // (occulited task 70: the login page's options ride along too)
    expect(Object.keys(body).sort()).toEqual(['client_id', 'client_secret', 'hide_password_form', 'issuer', 'mode', 'name', 'passkey_offer', 'password_login', 'scopes', 'session_idle', 'session_max', 'username_claim']);
    expect(body).toMatchObject({mode: 'oidc', issuer: 'https://auth.example.org/application/o/lite/', client_id: 'lite-client', client_secret: 's3cret'});
    await expect(page.getByText('Saved. The change takes effect when occulited is restarted.')).toBeVisible();
    await expect(page.getByText(/unknown field|is not a field/)).toHaveCount(0);
    await expect(page.getByLabel('Client secret')).toHaveValue('');
});

// B-82 (the maintainer: "make the oauth login button same height as the other two!"): Login, the
// passkey and the provider's link are one size, with a long provider name too, in both languages
for (const [lang, name] of [['en', 'authentik'], ['de', 'authentik'], ['en', 'The identity provider of the whole house and garden'], ['de', 'Der Identitätsanbieter für Haus, Hof und Garten']] as const) {
    test(`the three login buttons are one size (${lang}, ${name.length > 12 ? 'a long name' : name})`, async ({page, context, baseURL}) => {
        await context.addCookies([{name: 'stub-oidc', value: 'on', url: baseURL!}]);
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: SIGNED_OUT}));
        await page.route('**/api/auth/v1/oidc', (r) => r.fulfill({json: {enabled: true, name, password_login: true}}));
        await page.goto('/login');
        const buttons = [page.locator('[data-login-submit]'), page.locator('[data-passkey]'), page.locator('[data-login-sso]')];
        for (const b of buttons) await expect(b).toBeVisible();
        const boxes = await Promise.all(buttons.map(async (b) => (await b.boundingBox())!));
        for (const b of boxes) {
            expect(Math.abs(b.height - boxes[0]!.height)).toBeLessThanOrEqual(1);
            expect(Math.abs(b.width - boxes[0]!.width)).toBeLessThanOrEqual(1);
        }
        // a long name is cut, its whole text in the tooltip
        await expect(buttons[2]!).toHaveAttribute('title', new RegExp(name));
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });
}

// occulited task 70 (the maintainer: "hide user/password login if oidc is used"): the form behind
// the link "Sign in with a password", which is always there and brings it back
test('the password form hidden while the provider is used: the provider, the passkey, the link back', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'hidden', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: SIGNED_OUT}));
    await page.goto('/login?return=%2Faddons%2Fred%2F');
    const sso = page.locator('[data-login-sso]');
    await expect(sso).toBeVisible();
    await expect(sso).toHaveClass(/primary/);
    await expect(page.locator('input[type=password]')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Login'})).toHaveCount(0);
    await expect(page.locator('[data-passkey]')).toBeVisible();
    // not the "switched off" sentence: the password login is there, behind the link
    await expect(page.getByText('Password login is switched off')).toHaveCount(0);
    const link = page.locator('[data-login-password]');
    await expect(link).toHaveText('Sign in with a password');
    await expect(link).toHaveAttribute('href', '/login?password=1&return=%2Faddons%2Fred%2F');
    await link.click();
    await expect(page.locator('input[type=password]')).toBeVisible();
    await expect(page.getByRole('button', {name: 'Login'})).toBeVisible();
    await expect(page.locator('[data-login-sso]')).not.toHaveClass(/primary/);
    await expect(page.locator('[data-login-password]')).toHaveCount(0);
    // German
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/login');
    await expect(page.locator('[data-login-password]')).toHaveText('Mit Passwort anmelden');
});

test('the Users page: the form hidden once the provider has signed someone in, the passkey offer', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-oidc', value: 'on', url: baseURL!}]);
    const bodies: Record<string, unknown>[] = [];
    await page.route('**/api/auth/v1/config', async (route) => {
        if (route.request().method() === 'PUT') bodies.push(route.request().postDataJSON());
        await route.fallback();
    });
    await page.goto('/system/users');
    const hide = page.locator('[data-hide-password-form]');
    await expect(hide).toBeEnabled();
    await expect(hide).not.toBeChecked();
    await hide.check();
    const offer = page.locator('[data-passkey-offer]');
    await expect(offer).toHaveValue('autofill');
    await offer.selectOption('prompt');
    await page.locator('[data-section="authentication"]').getByRole('button', {name: 'Save'}).click();
    await expect.poll(() => bodies.length).toBe(1);
    expect(bodies[0]).toMatchObject({hide_password_form: true, passkey_offer: 'prompt'});
    // before any provider login it cannot be switched on, and says why
    await page.route('**/api/auth/v1/config', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        await route.fulfill({response, json: {...(await response.json()), provider_login_seen: false, hide_password_form: false}});
    });
    await page.reload();
    await expect(page.locator('[data-hide-password-form]')).toBeDisabled();
    await expect(page.locator('[data-hide-password-wait]')).toHaveText('Possible once an account has signed in through authentik.');
});

test('the passkey offer "off": no autofill offer in the name field', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-passkey-offer', value: 'off', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: SIGNED_OUT}));
    await page.goto('/login');
    await expect(page.locator('[data-passkey]')).toBeVisible();
    await expect(page.locator('input:not([type=password])').first()).toHaveAttribute('autocomplete', 'username');
});
