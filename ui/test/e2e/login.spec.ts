import {expect, test} from '@playwright/test';
import {fitsWindow} from './scroll';

// The login: a wrong password shows its notice without moving the button or growing the card.
test('a wrong password does not resize the login card', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {authenticated: false, setup_required: false}}));
    await page.route('**/api/auth/v1/login', (r) => r.fulfill({status: 401, json: {error: 'invalid', message: 'wrong user name or password'}}));
    await page.goto('/');
    const card = page.locator('form.ol-card');
    await expect(card).toBeVisible();
    const before = await card.boundingBox();
    await page.locator('input:not([type=password])').first().fill('admin');
    await page.locator('input[type=password]').fill('nope');
    await page.getByRole('button', {name: 'Login'}).click();
    await expect(card.locator('.ol-notice.error')).toContainText('wrong user name or password');
    const after = await card.boundingBox();
    expect(after?.height).toBe(before?.height);
});

// B-112: each label's text stands a few pixels above its field - it was a line break with no space on
// the login page. The gap between the text's box and the field, in English and German, in setup mode
// with its third field, and on the phone project at its width.
async function labelGaps(page: import('@playwright/test').Page, labels: string) {
    return page.locator(labels).evaluateAll((els) =>
        els.map((l) => {
            const text = l.querySelector('span')!.getBoundingClientRect();
            const field = l.querySelector('input, select')!.getBoundingClientRect();
            return {text: l.querySelector('span')!.textContent ?? '', gap: Math.round((field.top - text.bottom) * 10) / 10};
        }),
    );
}

for (const setup of [false, true]) {
    test(`the login's labels stand off their fields${setup ? ' (setup)' : ''}`, async ({page}) => {
        await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {authenticated: false, setup_required: setup}}));
        for (const language of ['en', 'de']) {
            await page.addInitScript((l) => localStorage.setItem('ol.language', l), language);
            await page.goto('/');
            await expect(page.locator('form.ol-card')).toBeVisible();
            await page.evaluate(() => document.fonts.ready);
            const gaps = await labelGaps(page, 'form.ol-card label');
            expect(gaps.length, JSON.stringify(gaps)).toBe(setup ? 3 : 2);
            for (const g of gaps) expect(g.gap, `${language} ${g.text}: ${g.gap} px`).toBeGreaterThanOrEqual(4);
            // nothing wider than the window, the phone included
            expect(await fitsWindow(page)).toBe(true);
        }
    });
}

test('the authentication form stacks its labels above their fields on a phone with the same gap', async ({page, isMobile}) => {
    test.skip(!isMobile, 'the one-column form is below 700 px');
    await page.goto('/system/users');
    const labels = page.locator('.ol-secform label');
    await expect(labels.first()).toBeVisible();
    const gaps = await page.locator('.ol-secform label').evaluateAll((els) =>
        els.map((l) => {
            const text = l.querySelector('span')!.getBoundingClientRect();
            const field = l.querySelector('input, select')!.getBoundingClientRect();
            return {text: l.querySelector('span')!.textContent ?? '', gap: Math.round((field.top - text.bottom) * 10) / 10};
        }),
    );
    expect(gaps.length).toBeGreaterThan(0);
    for (const g of gaps) expect(g.gap, `${g.text}: ${g.gap} px`).toBeGreaterThanOrEqual(4);
});

// B-63: the shell matched pages with startsWith, so a signed-in /login was the Log page - and the
// addon gate's /login?return=<page> for a session it did not see ended there, in the addon's frame.
test('a signed-in /login is not the Log page: without a target it goes on to Status', async ({page}) => {
    await page.goto('/login');
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByRole('heading', {level: 1, name: 'Status'})).toBeVisible();
    await expect(page.locator('.ol-shell')).not.toHaveClass(/ol-fill/);
    // and /log with a query is still the Log page
    await page.goto('/system/log?unit=rfd');
    await expect(page.getByRole('heading', {level: 1, name: 'Log'})).toBeVisible();
});

test('a signed-in /login?return= goes on to its page of this origin', async ({page}) => {
    await page.goto('/login?return=%2Faddons%2Fred%2F');
    await expect(page).toHaveURL(/\/addons\/red\/$/);
    await expect(page.locator('body')).toContainText('Node-RED stub');
});

test('a signed-in /login?return= never leaves the origin', async ({page, baseURL}) => {
    const away: string[] = [];
    await page.route((url) => url.origin !== new URL(baseURL!).origin, (r) => {
        away.push(r.request().url());
        return r.fulfill({status: 200, body: 'away'});
    });
    for (const target of ['https://evil.example/', '//evil.example/', '/\\evil.example/', '/\t/evil.example/', '/.//evil.example/']) {
        await page.goto(`/login?return=${encodeURIComponent(target)}`);
        await expect(page, target).toHaveURL(new RegExp(`^${baseURL}/$`));
        await expect(page.getByRole('heading', {level: 1, name: 'Status'})).toBeVisible();
    }
    expect(away).toEqual([]);
});

test('a return page that sends a signed-in browser straight back is followed once, then explained', async ({page}) => {
    let hits = 0;
    // a gate that does not take this session: every request for the page goes back to the login
    await page.route('**/addons/red/', (r) => {
        hits++;
        return r.fulfill({status: 302, headers: {Location: '/login?return=/addons/red/'}});
    });
    await page.goto('/login?return=%2Faddons%2Fred%2F');
    await expect(page.locator('.ol-notice')).toContainText('/addons/red/ asked for a login again although this browser is signed in');
    await expect(page).toHaveURL(/\/login\?return=/);
    expect(hits).toBe(1);
    await expect(page.locator('.ol-notice').getByRole('link', {name: 'Status'})).toHaveAttribute('href', '/');
});

test('a login with a return target goes there', async ({page}) => {
    let signedIn = false;
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: signedIn ? {authenticated: true, setup_required: false, user: 'admin', role: 'admin', sid: 'A1b2C3d4E5'} : {authenticated: false, setup_required: false}}));
    await page.route('**/api/auth/v1/login', (r) => {
        signedIn = true;
        return r.fulfill({json: {sid: 'A1b2C3d4E5', user: 'admin', role: 'admin', must_change_password: false}});
    });
    await page.goto('/login?return=%2Faddons%2Fred%2F');
    await page.locator('input:not([type=password])').first().fill('admin');
    await page.locator('input[type=password]').fill('secret');
    await page.getByRole('button', {name: 'Login'}).click();
    await expect(page).toHaveURL(/\/addons\/red\/$/);
    await expect(page.locator('body')).toContainText('Node-RED stub');
});
