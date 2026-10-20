import {expect, test, type Page, type TestInfo} from '@playwright/test';
import {fitsWindow} from './scroll';

// task 81: the Status page's warnings - directly under the hostname card, errors before warnings;
// an administrator's silence with the period picked (D-65), the silenced line, Show and Unsilence;
// the security-key warning's way to the Interfaces page's Set key panel, and nothing for a user
// (D-64); B-92's Fix ownership; German; the edges in both themes. The stub plants the warnings by
// cookie and keeps the silences per browser (stub-warnings=<id>), so the projects do not meet.

const USER = {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'};

async function plant(page: Page, baseURL: string, info: TestInfo, cookies: Record<string, string> = {}) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies(Object.entries({...cookies, 'stub-warnings': id}).map(([name, value]) => ({name, value, url: baseURL})));
}

test('the warnings stand directly under the hostname card, errors before warnings', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-warn': '1', 'stub-key': 'default'});
    await page.goto('/');
    const block = page.locator('[data-warnings]');
    await expect(block.locator('[data-notice="security-key"]')).toBeVisible();
    // nothing between the heading and the card, the warnings right after it, Utilisation after them
    expect(await page.locator('h1').evaluate((h) => h.nextElementSibling?.classList.contains('ol-hero'))).toBe(true);
    expect(await page.locator('.ol-hero').evaluate((el) => el.nextElementSibling?.hasAttribute('data-warnings'))).toBe(true);
    const hero = (await page.locator('.ol-hero').boundingBox())!;
    const first = (await block.locator('[data-notice]').first().boundingBox())!;
    expect(first.y).toBeGreaterThanOrEqual(hero.y + hero.height);
    const last = (await block.locator('[data-notice]').last().boundingBox())!;
    const utilisation = (await page.getByRole('heading', {level: 2, name: 'Utilisation'}).boundingBox())!;
    expect(utilisation.y).toBeGreaterThan(last.y);
    const ids = await block.locator('[data-notice]').evaluateAll((els) => els.map((e) => `${e.getAttribute('data-severity')}:${e.getAttribute('data-notice')}`));
    expect(ids).toEqual(['error:rega', 'error:arch', 'error:meta', 'error:unclean', 'error:backup-target', 'error:certificate', 'warning:storage', 'warning:security-key']);
    expect(await fitsWindow(page)).toBe(true);
});

test('Silence hides a warning for the period picked; Show and Unsilence bring it back', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-key': 'default'});
    await page.goto('/');
    const notice = page.locator('[data-warnings] [data-notice="security-key"]');
    await expect(notice).toBeVisible();
    const bell = notice.getByRole('button', {name: 'Silence…'});
    await expect(bell).toHaveAttribute('title', 'Silence…');
    await bell.click();
    const menu = notice.getByRole('menu');
    await expect(menu.getByRole('menuitem')).toHaveText(['Silence for 1 day', 'Silence for 7 days', 'Silence for 90 days']);
    await expect(menu).toContainText('It shows again earlier if the warning clears and comes back.');
    const box = (await menu.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
    // Escape closes it, a second click opens it again
    await page.keyboard.press('Escape');
    await expect(menu).toHaveCount(0);
    await bell.click();
    const posted = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/warnings/silence'));
    await menu.getByRole('menuitem', {name: 'Silence for 7 days'}).click();
    expect((await posted).postDataJSON()).toEqual({id: 'security-key', variant: 'default', days: 7});
    await expect(page.locator('[data-notice="security-key"]')).toHaveCount(0);
    const line = page.locator('[data-silenced]');
    await expect(line).toContainText('1 silenced warning');
    await line.getByRole('button', {name: 'Show'}).click();
    const item = page.locator('[data-silenced-warning="security-key"]');
    await expect(item).toContainText('BidCos-RF uses the default security key.');
    await expect(item).toContainText(/silenced on .+ by admin, until .+/);
    // the silence is the box's, not the browser's: a reload keeps it
    await page.reload();
    await expect(page.locator('[data-silenced]')).toContainText('1 silenced warning');
    await expect(page.locator('[data-notice="security-key"]')).toHaveCount(0);
    await page.locator('[data-silenced]').getByRole('button', {name: 'Show'}).click();
    await page.locator('[data-silenced-warning="security-key"]').getByRole('button', {name: 'Unsilence'}).click();
    await expect(page.locator('[data-warnings] [data-notice="security-key"]')).toBeVisible();
    await expect(page.locator('[data-silenced]')).toHaveCount(0);
    expect(await fitsWindow(page)).toBe(true);
});

// a backup target's variant is a path: the DELETE carries it escaped
test('a warning whose variant is a path is silenced and lifted', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/');
    const notice = page.locator('[data-notice="backup-userfs"]');
    await notice.getByRole('button', {name: 'Silence…'}).click();
    await notice.getByRole('menuitem', {name: 'Silence for 1 day'}).click();
    await expect(page.locator('[data-notice="backup-userfs"]')).toHaveCount(0);
    await page.locator('[data-silenced]').getByRole('button', {name: 'Show'}).click();
    const lifted = page.waitForRequest((r) => r.method() === 'DELETE');
    await page.locator('[data-silenced-warning="backup-userfs"]').getByRole('button', {name: 'Unsilence'}).click();
    expect((await lifted).url()).toMatch(/\/api\/system\/v1\/warnings\/silence\/backup-userfs\/%2Fmedia%2Fusb0%2Fbackup$/);
    await expect(page.locator('[data-notice="backup-userfs"]')).toBeVisible();
});

test('the security-key warning opens the Set key panel, the key field focused', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-key': 'default'});
    await page.goto('/');
    await page.locator('[data-notice="security-key"]').getByRole('link', {name: 'Set security key', exact: true}).click();
    await expect(page).toHaveURL(/\/system\/keys#security-key$/);
    await expect(page.getByRole('heading', {level: 3, name: 'Security key'})).toBeVisible();
    await expect(page.locator('#ol-sec-key')).toBeFocused();
    await expect(page.locator('#security-key')).toBeInViewport();
});

test('a user sees the warnings with nothing to silence, and the Keys page opens nothing', async ({page, baseURL}, info) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: USER}));
    await plant(page, baseURL!, info, {'stub-key': 'default'});
    await page.goto('/');
    const notice = page.locator('[data-notice="security-key"]');
    await expect(notice).toBeVisible();
    await expect(page.getByRole('button', {name: 'Silence…'})).toHaveCount(0);
    await notice.getByRole('link', {name: 'Set security key', exact: true}).click();
    await expect(page).toHaveURL(/\/system\/keys#security-key$/);
    await expect(page.locator('#security-key')).toBeVisible();
    await expect(page.locator('[data-note="security-key-admin"]')).toHaveText('An administrator sets the key.');
    await expect(page.locator('#ol-sec-key')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Set key'})).toHaveCount(0);
});

test('Fix ownership gives the addon its files back, and the warning goes', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-ownership': '1'});
    await page.goto('/');
    const notice = page.locator('[data-notice="addon-ownership"]');
    await expect(notice).toContainText('Files of hm2mqtt belong to root, but the addon runs as its own user');
    await expect(notice).toContainText('/usr/local/addons/hm2mqtt/var/hm2mqtt.pid');
    await expect(notice.getByRole('link', {name: 'Services', exact: true})).toHaveAttribute('href', '/services');
    const fixed = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/addons/hm2mqtt/ownership'));
    await notice.getByRole('button', {name: 'Fix ownership'}).click();
    await fixed;
    await expect(page.locator('[data-notice="addon-ownership"]')).toHaveCount(0);
    await expect(page.locator('[data-notice="warnings-result"]')).toContainText('The files of hm2mqtt belong to the addon again.');
    await expect(page.locator('[data-notice="warnings-result"]')).not.toContainText('had failed');
});

// task 81 (D-67): an addon whose unit had failed is started once after the fix, and the result says so
test('Fix ownership on an addon whose unit had failed says it was started', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-ownership': 'failed'});
    await page.goto('/');
    await page.locator('[data-notice="addon-ownership"]').getByRole('button', {name: 'Fix ownership'}).click();
    const result = page.locator('[data-notice="warnings-result"]');
    await expect(result).toHaveText(/hm2mqtt had failed: its files belong to the addon again, and it was started\./);
    await expect(result).not.toContainText('An addon that did not start');
    await expect(page.locator('[data-notice="addon-ownership"]')).toHaveCount(0);
});

test('in German: Fix ownership on a failed addon', async ({page, baseURL}, info) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await plant(page, baseURL!, info, {'stub-ownership': 'failed'});
    await page.goto('/');
    await page.locator('[data-notice="addon-ownership"]').getByRole('button', {name: 'Besitzrechte korrigieren'}).click();
    await expect(page.locator('[data-notice="warnings-result"]')).toContainText('hm2mqtt war ausgefallen: Die Dateien gehören wieder dem Addon, und es wurde gestartet.');
});

test('in German: the warning, its button and the silence menu', async ({page, baseURL}, info) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await plant(page, baseURL!, info, {'stub-key': 'default'});
    await page.goto('/');
    const notice = page.locator('[data-notice="security-key"]');
    await expect(notice).toContainText('BidCos-RF verwendet den Standard-Sicherheitsschlüssel.');
    await expect(notice.getByRole('link', {name: 'Sicherheitsschlüssel setzen', exact: true})).toBeVisible();
    await notice.getByRole('button', {name: 'Stummschalten…'}).click();
    await expect(notice.getByRole('menuitem')).toHaveText(['1 Tag stummschalten', '7 Tage stummschalten', '90 Tage stummschalten']);
    await page.mouse.click(2, 2);
    await expect(notice.getByRole('menu')).toHaveCount(0);
});

test('a warning has the amber edge and an error the red one, in either theme', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-warn': '1', 'stub-key': 'default'});
    await page.goto('/');
    await expect(page.locator('[data-notice="security-key"]')).toBeVisible();
    const token = (name: string) =>
        page.evaluate((v) => {
            const d = document.createElement('div');
            d.style.color = `var(${v})`;
            document.body.append(d);
            const c = getComputedStyle(d).color;
            d.remove();
            return c;
        }, name);
    // task 203: the edge is the card's fat left one, the other three sides the panel's hairline
    const edge = (id: string) =>
        page.locator(`[data-notice="${id}"]`).evaluate((e) => {
            const cs = getComputedStyle(e);
            return {colour: cs.borderLeftColor, width: cs.borderLeftWidth, rest: [cs.borderTopWidth, cs.borderRightWidth, cs.borderBottomWidth]};
        });
    const warn = await edge('security-key');
    const err = await edge('certificate');
    expect(warn.colour).toBe(await token('--hmm-warn'));
    expect(err.colour).toBe(await token('--hmm-error'));
    expect([warn.width, err.width]).toEqual(['4px', '4px']);
    expect([warn.rest, err.rest]).toEqual([
        ['1px', '1px', '1px'],
        ['1px', '1px', '1px'],
    ]);
    expect(await token('--hmm-warn')).not.toBe(await token('--hmm-error'));
});

// task 173: classic RPC on without a login warns, links to Remote access, and says which ports
test('classic RPC without a login: the warning and its link', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-rpc': 'plain,tls'});
    await page.goto('/');
    const w = page.locator('[data-warnings] [data-notice="classic-rpc-open"]');
    await expect(w).toContainText('Classic RPC is on without a login (the plain and the TLS ports): anyone the firewall lets in controls every device.');
    await expect(w.getByRole('link').first()).toHaveAttribute('href', '/system/remote-access');
});

// task 201 (decided in task 154's Q&A): a pairing hmipserver declined because the device's key in
// the system's key list is wrong. Until this, such a device simply never appeared and nothing said
// why. The warning names the device and its button goes to the key list.
test('a declined pairing names the device, marks the System tab and leads to the device keys', async ({page, baseURL}, info) => {
    const sgtin = '3014F711A0001F0000000A04';
    await plant(page, baseURL!, info, {'stub-declined': sgtin});
    await page.goto('/');
    const notice = page.locator('[data-warnings] [data-notice="hmip-key-declined"]');
    await expect(notice).toBeVisible();
    await expect(notice).toHaveAttribute('data-severity', 'warning');
    await expect(notice.locator('.ol-notice-text')).toContainText(`Pairing ${sgtin} was declined`);
    // the sentence says what to do and names no page: the button does
    await expect(notice.locator('.ol-notice-text')).not.toContainText(/\bpage\b/);
    // the Keys entry of the System menu carries the warning's dot
    await page.locator('.ol-systab').click();
    const menu = page.locator('.ol-sysmenu');
    await expect(menu).toBeVisible();
    await expect.poll(() => menu.getAttribute('data-phase')).toBe('open');
    const keys = menu.getByRole('menuitem').filter({hasText: 'Keys'});
    await expect(keys.locator('.ol-sysdot')).toHaveAttribute('aria-label', 'Warning');
    await page.keyboard.press('Escape');
    await notice.getByRole('link', {name: 'Device keys', exact: true}).click();
    await expect(page).toHaveURL(/\/system\/keys/);
    await expect(page.getByRole('heading', {level: 1, name: 'Keys'})).toBeVisible();
});

test('in German the declined pairing is translated', async ({page, baseURL}, info) => {
    const sgtin = '3014F711A0001F0000000A04';
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await plant(page, baseURL!, info, {'stub-declined': sgtin});
    await page.goto('/');
    const notice = page.locator('[data-notice="hmip-key-declined"]');
    await expect(notice.locator('.ol-notice-text')).toContainText(`Das Anlernen von ${sgtin} wurde abgelehnt`);
    await expect(notice.getByRole('link', {name: 'Geräteschlüssel', exact: true})).toBeVisible();
});

// occulited task 14: `occulited admin reset-auth` on the console leaves a notice for a week -
// which account, when, and what to do when nobody with root did it; the button goes to Users
test('a console reset of an account is a notice on the Status page, in English and German', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-console-reset': 'admin'});
    await page.goto('/');
    const notice = page.locator('[data-warnings] [data-notice="console-reset"]');
    await expect(notice).toBeVisible();
    await expect(notice).toHaveAttribute('data-severity', 'warning');
    await expect(notice.locator('.ol-notice-text')).toContainText('Access reset on the console: admin');
    await expect(notice.locator('.ol-notice-text')).toContainText('one-time password');
    await notice.getByRole('link', {name: 'Users', exact: true}).click();
    await expect(page).toHaveURL(/\/system\/users/);
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/');
    await expect(page.locator('[data-notice="console-reset"] .ol-notice-text')).toContainText('Zugang per Konsole zurückgesetzt: admin');
    await expect(page.locator('[data-notice="console-reset"]').getByRole('link', {name: 'Benutzer', exact: true})).toBeVisible();
});

// occulited task 88 (maintainer, 2026-10-20): "Backups are not encrypted" moved from the Backup page's
// three inline notices to a Status warning that can be muted, with the way to the Encryption panel
test('backups not encrypted: the Status warning, its link, silence and unsilence', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-plain-backup': '1'});
    await page.goto('/');
    const notice = page.locator('[data-warnings] [data-notice="backup-unencrypted"]');
    await expect(notice).toContainText('Backups are not encrypted: anyone who can read the file has every key of this system.');
    await expect(notice.getByRole('link', {name: 'Set up encryption', exact: true})).toBeVisible();
    const posted = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/warnings/silence'));
    await notice.getByRole('button', {name: 'Silence…'}).click();
    await notice.getByRole('menu').getByRole('menuitem', {name: 'Silence for 90 days'}).click();
    expect((await posted).postDataJSON()).toEqual({id: 'backup-unencrypted', variant: 'plain', days: 90});
    await expect(page.locator('[data-notice="backup-unencrypted"]')).toHaveCount(0);
    await page.locator('[data-silenced]').getByRole('button', {name: 'Show'}).click();
    await page.locator('[data-silenced-warning="backup-unencrypted"]').getByRole('button', {name: 'Unsilence'}).click();
    const back = page.locator('[data-warnings] [data-notice="backup-unencrypted"]');
    await expect(back).toBeVisible();
    // the link leads to the Backup page's Encryption panel
    await back.getByRole('link', {name: 'Set up encryption', exact: true}).click();
    await expect(page).toHaveURL(/\/backup#encryption$/);
    await expect(page.locator('[data-section="encryption"]')).toBeInViewport();
    expect(await fitsWindow(page)).toBe(true);
});

test('in German: backups not encrypted', async ({page, baseURL}, info) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await plant(page, baseURL!, info, {'stub-plain-backup': '1'});
    await page.goto('/');
    const notice = page.locator('[data-warnings] [data-notice="backup-unencrypted"]');
    await expect(notice).toContainText('Die Sicherung ist nicht verschlüsselt: Wer die Datei lesen kann, hat jeden Schlüssel dieses Systems.');
    await expect(notice.getByRole('link', {name: 'Verschlüsselung einrichten', exact: true})).toBeVisible();
});

test('no Status warning about plain backups while none is planted (a recovery key exists)', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-key': 'default'});
    await page.goto('/');
    await expect(page.locator('[data-warnings] [data-notice="security-key"]')).toBeVisible();
    await expect(page.locator('[data-notice="backup-unencrypted"]')).toHaveCount(0);
});
