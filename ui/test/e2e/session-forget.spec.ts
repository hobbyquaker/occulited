import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 127: the shell drops its stored session id when the session ends - at the logout,
// and on a 401 that ends it - so it does not keep sending a dead id as its Bearer. B-123's rule stays
// the daemon's: in the public mode a stale session id is the guest; the shell just no longer sends it.

const SID = 'A1b2C3d4E5'; // the stub's session id (GET /api/auth/v1/state)
const GUEST = {setup_required: false, authenticated: true, user: 'guest', role: 'user', level: 'operate', account_id: '0badf00d', public: true, must_change_password: false, scopes: ['logs:read', 'meta:read', 'rpc:operate', 'system:read']};

/** every request's Bearer, from the moment `from()` is true */
function bearers(page: Page, from: () => boolean): string[] {
    const out: string[] = [];
    page.on('request', (r) => {
        if (!from()) return;
        const h = r.headers()['authorization'];
        if (h) out.push(`${new URL(r.url()).pathname} ${h}`);
    });
    return out;
}

const stored = (page: Page) => page.evaluate(() => sessionStorage.getItem('ol.sid'));

test('the logout forgets the session id; the public mode then shows the App without it', async ({page, baseURL}) => {
    let out = false;
    await page.route('**/api/auth/v1/state', (route) => (out ? route.fulfill({json: GUEST}) : route.fallback()));
    await page.route('**/api/auth/v1/logout', async (route) => {
        out = true;
        // the public mode is on: the daemon answers the credential-less browser as the guest
        await page.context().addCookies([{name: 'stub-public', value: '1', url: baseURL!}]);
        await route.fulfill({json: {ok: true}});
    });
    const sent = bearers(page, () => out);
    await page.goto('/account');
    await expect.poll(() => stored(page)).toBe(SID);
    await page.getByRole('button', {name: 'Logout'}).click();
    await expect.poll(() => out).toBe(true);
    await expect.poll(() => stored(page)).toBeNull();
    await page.goto('/app');
    await expect(page.locator('[data-app-title]')).toBeVisible();
    await page.waitForTimeout(1000);
    await expect(page.getByRole('heading', {level: 1, name: 'Login'})).toHaveCount(0);
    expect(sent).toEqual([]);
});

test('a 401 that ends the session forgets its id: the next requests carry no Bearer', async ({page, baseURL}) => {
    let ended = false;
    await page.route('**/api/auth/v1/state', (route) => (ended ? route.fulfill({json: GUEST}) : route.fallback()));
    await page.goto('/app');
    await expect(page.locator('[data-app-title]')).toBeVisible();
    await expect.poll(() => stored(page)).toBe(SID);
    // the session ends on the daemon (expired, revoked elsewhere): its id is answered 401 from now on,
    // and the public mode is on
    await page.context().addCookies([{name: 'stub-public', value: '1', url: baseURL!}]);
    await page.route('**/api/**', (route) => {
        if (route.request().headers()['authorization'] === `Bearer ${SID}`) {
            ended = true;
            return route.fulfill({status: 401, json: {error: 'unauthenticated', message: 'session expired'}});
        }
        return route.fallback();
    });
    let reloaded = false;
    const sent = bearers(page, () => reloaded);
    // the shell asks the daemon something with the old id: the Status page's data
    await page.getByRole('link', {name: 'Status', exact: true}).first().click();
    await expect.poll(() => ended).toBe(true);
    await expect.poll(() => stored(page)).toBeNull();
    // the kiosk reloads: the App as the guest, and the old id is not sent again
    reloaded = true;
    await page.goto('/app');
    await expect(page.locator('[data-app-title]')).toBeVisible();
    await page.waitForTimeout(1000);
    await expect(page.getByRole('heading', {level: 1, name: 'Login'})).toHaveCount(0);
    expect(sent).toEqual([]);
});
