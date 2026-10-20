import {expect, test, type Page} from '@playwright/test';

// openccu-lite task 227 (the maintainer: "for IPv6: shouldnt the panel also offer a selection
// static, dhcp, slaac ?"): the IPv6 panel's mode - off, SLAAC, DHCPv6, static - applied live, kept
// only when confirmed within the window, rolled back by itself otherwise; IPv4 untouched.

async function own(page: Page, baseURL: string | undefined, window?: number) {
    await page.context().addCookies([
        {name: 'stub-v6', value: `v6-${Math.random().toString(36).slice(2)}`, url: baseURL!},
        ...(window ? [{name: 'stub-v6-window', value: String(window), url: baseURL!}] : []),
    ]);
    await page.goto('/system/network');
}
const panel = (page: Page, name: string) => page.locator(`[data-panel="ipv6"][data-of="${name}"]`);

test('static on eth0: the fields, the question, the countdown, Keep', async ({page, baseURL}) => {
    await own(page, baseURL);
    const p = panel(page, 'eth0');
    const mode = p.locator('[data-v6-mode="eth0"]');
    await expect(mode).toHaveValue('slaac');
    await expect(p.getByRole('button', {name: 'Apply'})).toBeDisabled();
    await mode.selectOption('static');
    await p.getByLabel('Address').fill('2001:db8::20');
    await p.getByLabel('Prefix length').fill('64');
    await p.getByLabel('Gateway').fill('fe80::1');
    await p.getByLabel('DNS').fill('2001:db8::53');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/network/ipv6')),
        (async () => {
            await p.getByRole('button', {name: 'Apply'}).click();
            const dialog = page.getByRole('dialog');
            await expect(dialog).toContainText('Keep it within 60 seconds');
            await expect(dialog).toContainText('IPv4 is not touched');
            await dialog.getByRole('button', {name: 'Apply'}).click();
        })(),
    ]);
    expect(req.postDataJSON()).toEqual({interface: 'eth0', mode: 'static', address: '2001:db8::20', prefix: 64, gateway: 'fe80::1', dns: ['2001:db8::53']});
    const pending = page.locator('[data-v6-pending]');
    await expect(pending).toContainText(/IPv6 on eth0: keep it\? (60|59|58) s left/);
    await expect(pending).toContainText('static 2001:db8::20/64 — previously SLAAC');
    await expect(p.getByRole('button', {name: 'Apply'})).toBeDisabled();
    await pending.getByRole('button', {name: 'Keep'}).click();
    await expect(pending).toHaveCount(0);
    await expect(page.locator('.ol-notice', {hasText: 'IPv6 on eth0 is kept.'})).toBeVisible();
    await expect(mode).toHaveValue('static');
});

test('not confirmed in time: the system rolls back by itself', async ({page, baseURL}) => {
    await own(page, baseURL, 2);
    const p = panel(page, 'eth0');
    await p.locator('[data-v6-mode="eth0"]').selectOption('off');
    await p.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    await expect(page.locator('[data-v6-pending]')).toBeVisible();
    await expect(page.locator('[data-v6-pending]')).toHaveCount(0, {timeout: 8000});
    await expect(p.locator('[data-v6-mode="eth0"]')).toHaveValue('slaac');
});

test('Revert now, and a bad address', async ({page, baseURL}) => {
    await own(page, baseURL);
    const p = panel(page, 'eth0');
    await p.locator('[data-v6-mode="eth0"]').selectOption('dhcpv6');
    await expect(p).toContainText('An address from a DHCPv6 server');
    await p.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    await page.locator('[data-v6-pending]').getByRole('button', {name: 'Revert now'}).click();
    await expect(page.locator('[data-v6-pending]')).toHaveCount(0);
    await expect(page.locator('.ol-notice', {hasText: 'IPv6 on eth0 is back to SLAAC.'})).toBeVisible();
    await expect(p.locator('[data-v6-mode="eth0"]')).toHaveValue('slaac');
    await p.locator('[data-v6-mode="eth0"]').selectOption('static');
    await p.getByLabel('Address').fill('192.0.2.1');
    await p.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    await expect(page.locator('.ol-notice.error, .ol-notice.err')).toContainText('not an IPv6 address');
});

test('a user sees the mode and changes nothing', async ({page, baseURL}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await own(page, baseURL);
    await expect(panel(page, 'eth0').locator('[data-v6-mode-read]')).toHaveText('SLAAC');
    await expect(page.locator('[data-v6-mode]')).toHaveCount(0);
});

test('in German', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await own(page, baseURL);
    const p = panel(page, 'eth0');
    await p.locator('[data-v6-mode="eth0"]').selectOption('static');
    await expect(p.getByLabel('Präfixlänge')).toBeVisible();
    await p.getByLabel('Adresse').fill('2001:db8::20');
    await p.getByRole('button', {name: 'Anwenden'}).click();
    await expect(page.getByRole('dialog')).toContainText('IPv4 bleibt unberührt');
});

// occulited B-85: after Off was applied and kept, every other mode enables Apply again and is applied
for (const next of ['slaac', 'dhcpv6', 'static'] as const) {
    test(`Off kept, then ${next}: Apply is enabled and applies`, async ({page, baseURL}) => {
        await own(page, baseURL);
        const p = panel(page, 'eth0');
        const mode = p.locator('[data-v6-mode="eth0"]');
        const apply = p.getByRole('button', {name: 'Apply'});
        await mode.selectOption('off');
        await apply.click();
        await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
        await page.locator('[data-v6-pending]').getByRole('button', {name: 'Keep'}).click();
        await expect(page.locator('[data-v6-pending]')).toHaveCount(0);
        await expect(mode).toHaveValue('off');
        await expect(apply).toBeDisabled();
        await mode.selectOption(next);
        if (next === 'static') await p.getByLabel('Address').fill('2001:db8::20');
        await expect(apply).toBeEnabled();
        const [req] = await Promise.all([
            page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/network/ipv6')),
            (async () => {
                await apply.click();
                await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
            })(),
        ]);
        expect(req.postDataJSON()).toMatchObject({interface: 'eth0', mode: next});
        await page.locator('[data-v6-pending]').getByRole('button', {name: 'Keep'}).click();
        await expect(mode).toHaveValue(next);
    });
}

// occulited B-85 (the maintainer's case): the browser reaches the system over IPv6 on eth0 - a
// change cuts it off, Keep cannot arrive, the system goes back after the window. The page says so
// beside Apply and in the question, with the page's IPv4 address to do it from instead.
test('connected over IPv6 on eth0: the hint, the warning, and the rollback said', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-v6-client', value: 'eth0', url: baseURL!}]);
    await own(page, baseURL, 2);
    const p = panel(page, 'eth0');
    const hint = p.locator('[data-v6-cuts="eth0"]');
    await expect(hint).toContainText('connected over IPv6 here');
    await expect(hint.getByRole('link', {name: 'Open over IPv4'})).toHaveAttribute('href', /^http:\/\/[0-9.]+\/system\/network$/);
    await expect(panel(page, 'wlan0').locator('[data-v6-cuts]')).toHaveCount(0);
    await p.locator('[data-v6-mode="eth0"]').selectOption('off');
    await p.getByRole('button', {name: 'Apply'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('This browser is connected to the system over IPv6 on eth0');
    await expect(dialog).toContainText('Open this page over IPv4 for the change: http://');
    await dialog.getByRole('button', {name: 'Apply'}).click();
    // nobody keeps it: back to SLAAC, and the page says why
    await expect(page.locator('.ol-notice', {hasText: 'IPv6 on eth0 is back to SLAAC: the change was not kept in time.'})).toBeVisible({timeout: 8000});
    await expect(p.locator('[data-v6-mode="eth0"]')).toHaveValue('slaac');
});

test('connected over IPv4: no hint, the question as before', async ({page, baseURL}) => {
    await own(page, baseURL);
    const p = panel(page, 'eth0');
    await expect(page.locator('[data-v6-cuts]')).toHaveCount(0);
    await p.locator('[data-v6-mode="eth0"]').selectOption('off');
    await p.getByRole('button', {name: 'Apply'}).click();
    await expect(page.getByRole('dialog')).not.toContainText('connected to the system over IPv6');
});
