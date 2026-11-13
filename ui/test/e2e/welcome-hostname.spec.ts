import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';

// openccu-lite task 327 (openccu-lite/openccu-lite#4): a fresh install is called
// openccu-lite-<the end of its MAC>, and the welcome page offers the rename at once, as its first
// step, through the Network page's path - a hostname-only POST of the settings, which renews the
// DHCP lease under the new name. The same characters as the daemon takes; the lease's outcome and
// the router hint as on the Network page; a static address says there is nothing to tell; a
// container (the host names it) and a pending network change get no step.

type Net = Record<string, unknown> & {settings: Record<string, unknown>; current: Record<string, unknown>};

/** the stub's network view patched, and the POST bodies with the answer reply() makes */
async function network(page: Page, patch: (j: Net) => void, reply: (b: Record<string, unknown>) => object) {
    const posts: Record<string, unknown>[] = [];
    await page.route('**/api/system/v1/network', async (route: Route) => {
        if (route.request().method() === 'POST') {
            const b = route.request().postDataJSON() as Record<string, unknown>;
            posts.push(b);
            return route.fulfill({json: {ok: true, ...reply(b)}});
        }
        const response = await route.fetch();
        const j = (await response.json()) as Net;
        patch(j);
        await route.fulfill({response, json: j});
    });
    return posts;
}

const dhcp = (name: string) => (j: Net) => {
    j.settings = {hostname: name, mode: 'dhcp', dns: []};
    j.current = {hostname: name, mode: 'dhcp', address: '192.0.2.119', netmask: '255.255.255.0', gateway: '192.0.2.1', dns: []};
};
const renamed = (lease: Record<string, unknown>) => (b: Record<string, unknown>) => ({
    applied: true, pending: null,
    rename: {hostname: b.hostname, previous: 'openccu-lite-3f2a', lease},
    certificate: {names: ['openccu-lite-3f2a', '192.0.2.119'], fits: false, mode: 'self-signed', known: true},
});

test('DHCP: the current name first, a wrong one refused, the rename sent alone and the lease said', async ({page}) => {
    const posts = await network(page, dhcp('openccu-lite-3f2a'), renamed({renewed: true, address: '192.0.2.119', at: '2026-10-06T19:30:00Z'}));
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hostname]');
    await expect(step.getByRole('heading', {name: "1 · The system's name"})).toBeVisible();
    await expect(page.getByRole('heading', {name: '2 · Automatic checks'})).toBeVisible();
    const input = step.locator('[data-welcome-hostname-input]');
    await expect(input).toHaveValue('openccu-lite-3f2a');
    const btn = step.locator('[data-action="welcome-rename"]');
    await expect(btn).toBeDisabled(); // unchanged
    await input.fill('küche-');
    await expect(btn).toBeDisabled();
    await expect(step.locator('[data-welcome-hostname-invalid]')).toHaveText('Letters, digits and hyphens, at most 63 characters; no hyphen at the start or the end.');
    await input.fill('kitchen-ccu');
    await expect(step.locator('[data-welcome-hostname-invalid]')).toHaveCount(0);
    await btn.click();
    const note = step.locator('[data-notice="welcome-rename"]');
    await expect(note).toContainText('Hostname set to kitchen-ccu. The DHCP server was told at');
    await expect(note).toContainText('(192.0.2.119)');
    await expect(note).toContainText('Some routers take a new name only with a new lease and show the old one until the lease runs out.');
    await expect(step.locator('[data-notice="welcome-rename-certificate"]')).toContainText("The system's own certificate still names openccu-lite-3f2a.");
    // the Network page's hostname-only change: the settings with the new name, nothing else
    expect(posts).toEqual([{hostname: 'kitchen-ccu', mode: 'dhcp', dns: []}]);
    await expect(btn).toBeDisabled(); // the new name is the current one now
    // the step does not hold Done back
    await expect(page.getByRole('button', {name: 'Done'})).toBeEnabled();
});

test('static: said before and after; Enter renames', async ({page}) => {
    const posts = await network(page, () => {}, renamed({renewed: false, static: true}));
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hostname]');
    await expect(step.locator('[data-welcome-hostname-static]')).toContainText('there is no DHCP server to tell');
    await step.locator('[data-welcome-hostname-input]').fill('attic');
    await step.locator('[data-welcome-hostname-input]').press('Enter');
    await expect(step.locator('[data-notice="welcome-rename"]')).toHaveText('Hostname set to attic. Static address: no DHCP server to tell.');
    expect(posts).toEqual([{hostname: 'attic', mode: 'static', address: '192.0.2.119', netmask: '255.255.255.0', gateway: '192.0.2.1', dns: ['192.0.2.1', '1.1.1.1']}]);
});

test('a lease that could not be renewed, and an error, are said', async ({page}) => {
    await network(page, dhcp('openccu-lite-3f2a'), renamed({renewed: false, error: 'systemctl reload occu-network: exit 1'}));
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hostname]');
    await step.locator('[data-welcome-hostname-input]').fill('attic');
    await step.locator('[data-action="welcome-rename"]').click();
    await expect(step.locator('[data-notice="welcome-rename"]')).toContainText('could not be renewed under the new name: systemctl reload occu-network: exit 1 Until the next start the server knows the system as openccu-lite-3f2a.');
    await page.unrouteAll({behavior: 'wait'});
    await page.route('**/api/system/v1/network', (r: Route) => (r.request().method() === 'POST' ? r.fulfill({status: 400, json: {error: 'invalid', message: 'invalid hostname'}}) : r.fallback()));
    await step.locator('[data-welcome-hostname-input]').fill('cellar');
    await step.locator('[data-action="welcome-rename"]').click();
    await expect(step.locator('[data-notice="welcome-rename-error"]')).toContainText('invalid hostname');
});

test('a registered passkey: the Network page\'s warning first, and cancelling renames nothing', async ({page}) => {
    const posts = await network(page, dhcp('openccu-lite-3f2a'), renamed({renewed: true}));
    await page.route('**/api/auth/v1/webauthn', (r: Route) => r.fulfill({json: {passkeys: true, registered: true, name: 'ccu.example.home'}}));
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hostname]');
    await step.locator('[data-welcome-hostname-input]').fill('attic');
    await step.locator('[data-action="welcome-rename"]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Passkeys are bound to the system\'s name.');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(step.locator('[data-notice="welcome-rename"]')).toHaveCount(0);
    expect(posts).toEqual([]);
});

test('a container, a pending network change, no administrator: no step, the numbers as before', async ({page}) => {
    for (const patch of [(j: Net) => { j.host_managed = true; j.writable = false; }, (j: Net) => { j.pending = {token: 'x'}; }]) {
        await page.unrouteAll({behavior: 'wait'});
        await network(page, patch, () => ({}));
        await page.goto('/welcome');
        await expect(page.getByRole('heading', {name: '1 · Automatic checks'})).toBeVisible();
        await expect(page.locator('[data-welcome-hostname]')).toHaveCount(0);
    }
    await page.unrouteAll({behavior: 'wait'});
    await page.route('**/api/system/v1/network', (r: Route) => r.fulfill({status: 403, json: {error: 'forbidden', message: 'forbidden'}}));
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '1 · Automatic checks'})).toBeVisible();
    await expect(page.locator('[data-welcome-hostname]')).toHaveCount(0);
});

test('German, and on a phone the field fits the width', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await network(page, dhcp('openccu-lite-3f2a'), renamed({renewed: true}));
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hostname]');
    await expect(step.getByRole('heading', {name: '1 · Der Name des Systems'})).toBeVisible();
    await expect(step).toContainText('Ein neues System heißt openccu-lite und das Ende seiner Netzwerkadresse (MAC).');
    await expect(step.getByRole('button', {name: 'Umbenennen'})).toBeDisabled();
    await step.locator('[data-welcome-hostname-input]').fill('-x');
    await expect(step.locator('[data-welcome-hostname-invalid]')).toContainText('kein Bindestrich am Anfang oder am Ende');
    // nothing wider than the window
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over).toBeLessThanOrEqual(0);
    const box = (await step.locator('[data-welcome-hostname-input]').boundingBox())!;
    expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
});
