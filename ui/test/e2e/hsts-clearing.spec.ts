import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// Task 96 (D-64): HSTS switched off sends max-age=0 for a while - a browser forgets its entry only when
// it sees that - and the pages ask the user to open the box once by its name in every browser: in
// System → Certificate's HTTPS section (System → Security before task 132), before a switch to
// self-signed on the same page, and when a system update
// that may be the way back to OpenCCU is staged. The default period is a week.
//
// The box by name: the projects are Chromium, which maps openccu.test to the stub's loopback.

const PORT = Number(process.env.PORT || 8799);
const NAME = `http://openccu.test:${PORT}`;
const ADDRESS = 'http://192.0.2.119/';
const UNTIL = '2026-10-12T20:00:00Z';

test.use({launchOptions: {args: ['--host-resolver-rules=MAP openccu.test 127.0.0.1']}});

const certificate = {self_signed: false, managed: true, mode: 'acme'};

/** GET /https answers the view; a PUT changes it the way the daemon does - off after on clears. */
async function httpsRoute(page: Page, initial: Record<string, unknown>) {
    let view: Record<string, unknown> = {
        redirect_https: true, hsts: false, hsts_max_age_days: 7, hsts_clearing: false, certificate,
        redirect_fqdn: false, redirect_fqdn_host: 'ccu', redirect_fqdn_target: 'ccu.example.org', redirect_fqdn_state: 'off',
        ...initial,
    };
    const puts: Record<string, unknown>[] = [];
    await page.route('**/api/system/v1/https', (r) => {
        if (r.request().method() === 'PUT') {
            const b = r.request().postDataJSON();
            puts.push(b);
            const clearing = !b.hsts && !!(view.hsts || view.hsts_clearing);
            view = {...view, redirect_https: b.redirect_https, hsts: b.hsts, hsts_max_age_days: b.hsts ? (b.hsts_max_age_days ?? 7) : 7, hsts_clearing: clearing, hsts_clearing_until: clearing ? (view.hsts_clearing_until ?? UNTIL) : undefined};
        }
        return r.fulfill({json: view});
    });
    return puts;
}

const save = (page: Page) => page.locator('[data-section="https"] button.primary', {hasText: 'Save'}).click();

test.describe("System → Certificate's HTTPS section", () => {
    test('switching HSTS off says what it does, asks nothing, and then names the deadline and the names to visit', async ({page}) => {
        const puts = await httpsRoute(page, {hsts: true, hsts_max_age_days: 180});
        await page.goto('/system/certificates');
        const hsts = page.getByRole('checkbox', {name: /^HSTS/});
        await expect(hsts).toBeChecked();
        await hsts.uncheck();
        await expect(page.getByText('Saving switches HSTS off: the system then sends max-age=0 for as long as the period was, at most 30 days')).toBeVisible();
        await save(page);
        await expect.poll(() => puts.length).toBe(1);
        expect(puts[0]).toEqual({redirect_https: true, hsts: false, hsts_max_age_days: 180});
        await expect(page.getByRole('dialog')).toHaveCount(0);
        const notice = page.locator('.ol-checks .ol-notice');
        await expect(notice).toContainText('HSTS is off. Browsers that visit until');
        await expect(notice).toContainText('forget HSTS for this system: it sends max-age=0 until then.');
        await expect(notice).toContainText('Open the system once by its name in every browser you use:');
        // the stub is opened by address, so the names are <host>.<domain> and the bare host name
        await expect(notice.locator('a')).toHaveCount(2);
        await expect(notice.getByRole('link', {name: 'ccu.example.org'})).toHaveAttribute('href', 'https://ccu.example.org/');
        await expect(notice.getByRole('link', {name: 'ccu', exact: true})).toHaveAttribute('href', 'https://ccu/');
        await expect(page.getByText('Saving switches HSTS off')).toHaveCount(0);
    });

    test('a week is the default period, and the question says it', async ({page}) => {
        const puts = await httpsRoute(page, {});
        await page.goto('/system/certificates');
        await page.getByRole('checkbox', {name: /^HSTS/}).check();
        await expect(page.locator('[data-section="https"] input[type="number"]')).toHaveValue('7');
        await save(page);
        const dialog = page.getByRole('dialog');
        await expect(dialog).toContainText('insist on https:// for 7 days');
        await expect(dialog).toContainText('Switching HSTS off later sends max-age=0 for a while');
        await dialog.getByRole('button', {name: 'Switch HSTS on'}).click();
        await expect.poll(() => puts.length).toBe(1);
        expect(puts[0]).toEqual({redirect_https: true, hsts: true, hsts_max_age_days: 7});
    });

    test('a clearing without a deadline says so without a date', async ({page}) => {
        await httpsRoute(page, {hsts_clearing: true});
        await page.goto('/system/certificates');
        await expect(page.locator('.ol-checks .ol-notice')).toContainText('HSTS is off. The system sends max-age=0, so browsers that visit forget HSTS for it.');
    });

    test('in German, by name: the name the page came by first', async ({page}) => {
        await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
        await httpsRoute(page, {hsts_clearing: true, hsts_clearing_until: UNTIL});
        await page.goto(`${NAME}/system/certificates`);
        const notice = page.locator('.ol-checks .ol-notice');
        await expect(notice).toContainText('HSTS ist aus. Browser, die bis');
        await expect(notice).toContainText('vergessen HSTS für dieses System: es sendet bis dahin max-age=0.');
        await expect(notice).toContainText('Das System in jedem Browser, den Sie nutzen, einmal über seinen Namen öffnen:');
        await expect(notice.locator('a')).toHaveCount(3);
        await expect(notice.locator('a').first()).toHaveAttribute('href', 'https://openccu.test/');
    });
});

test.describe('the Certificate page', () => {
    test('back to self-signed while HSTS is on: HSTS goes off first, the certificate stays until the next Save', async ({page}) => {
        const puts = await httpsRoute(page, {hsts: true, hsts_max_age_days: 30});
        await page.route('**/api/system/v1/certificate', async (r) => {
            const response = await r.fetch();
            const body = await response.json();
            body.settings.mode = 'acme';
            await r.fulfill({response, json: body});
        });
        let switched = 0;
        page.on('request', (r) => {
            if (r.method() === 'POST' && r.url().includes('/certificate/self-signed')) switched++;
        });
        await page.goto('/system/certificates');
        await expect(page.getByRole('radio', {name: 'ACME'})).toBeChecked();
        await page.getByRole('radio', {name: 'Self-signed'}).check();
        await page.getByRole('button', {name: 'Save', exact: true}).first().click();
        const first = page.getByRole('dialog', {name: 'Switch HSTS off first'});
        await expect(first).toContainText('HSTS is on: a browser that has seen the header refuses the self-signed certificate under this name for up to 30 days, with no way past.');
        await expect(first).toContainText('Do that in every browser you use, then switch to self-signed.');
        await first.getByRole('button', {name: 'Switch HSTS off now'}).click();
        await expect.poll(() => puts.length).toBe(1);
        expect(puts[0]).toEqual({redirect_https: true, hsts: false, hsts_max_age_days: 30});
        const notice = page.locator('.ol-hsts-off');
        await expect(notice).toContainText('HSTS is off. Browsers that visit until');
        await expect(notice.getByRole('link', {name: 'ccu.example.org'})).toBeVisible();
        await expect(notice).toContainText('Then switch to self-signed.');
        // the HTTPS section below read the box again: HSTS is off there too
        await expect(page.locator('[data-section="https"]').getByRole('checkbox', {name: /^HSTS/})).not.toBeChecked();
        expect(switched).toBe(0);
        // the next Save asks the usual question, with the clearing and the visit by name in it
        await page.getByRole('button', {name: 'Save', exact: true}).first().click();
        const again = page.getByRole('dialog', {name: 'Back to self-signed'});
        await expect(again).toContainText('HSTS is off and the system sends max-age=0 until');
        await expect(again).toContainText('A browser that has not opened the system by its name since HSTS was switched off refuses the self-signed certificate under that name');
        await again.getByRole('button', {name: 'Cancel'}).click();
        await expect(again).toBeHidden();
        expect(switched).toBe(0);
    });
});

test.describe('the system update', () => {
    async function staged(page: Page, wayBack: boolean) {
        await page.route('**/api/system/v1/system-update', async (r) => {
            // route.fetch runs outside the browser, where openccu.test does not resolve: the loopback
            const response = await r.fetch({url: r.request().url().replace('//openccu.test:', '//127.0.0.1:')});
            const body = await response.json();
            body.staged = {file: wayBack ? 'OpenCCU-3.89.8.20260719-ova.zip' : 'openccu-lite-x86_64-ova-1.0.0.zip', size: 300_000_000, kind: 'zip', recovery_armed: false, way_back: wayBack};
            await r.fulfill({response, json: body});
        });
    }

    test('a staged way back while HSTS clears names the deadline and the names; the install question warns', async ({page}) => {
        await httpsRoute(page, {hsts_clearing: true, hsts_clearing_until: UNTIL});
        await staged(page, true);
        await page.goto('/system/updates');
        const note = page.locator('.ol-wayback');
        // task 50: the risk visible, the why behind the ?
        await expect(note).toContainText('Not an openccu-lite release: HSTS is switched off for it.');
        await expect(note.getByRole('button', {name: 'Help'})).toBeVisible();
        await expect(note).toContainText('Browsers that visit until');
        await expect(note.getByRole('link', {name: 'ccu.example.org'})).toHaveAttribute('href', 'https://ccu.example.org/');
        // the Status page's certificate card says it too
        await page.goto('/');
        await expect(page.locator('.ol-cert')).toContainText('HSTS off (max-age=0)');
        await page.goto('/system/updates');
        await page.getByRole('button', {name: 'Reboot and install'}).click();
        const dialog = page.getByRole('dialog');
        await expect(dialog).toContainText("Reboot now and install OpenCCU-3.89.8.20260719-ova.zip? Settings, pairings and addons are kept; the system is unreachable for a few minutes.");
        await expect(dialog).toContainText("If a browser refuses the system's name afterwards, open the system by its IP address.");
        await dialog.getByRole('button', {name: 'Cancel'}).click();
        await expect(dialog).toBeHidden();
    });

    test('a staged way back while HSTS is on points to the HTTPS section of the Certificate page', async ({page}) => {
        await httpsRoute(page, {hsts: true});
        await staged(page, true);
        await page.goto('/system/updates');
        const note = page.locator('.ol-wayback');
        await expect(note).toContainText('Not an openccu-lite release, and HSTS is on');
        await expect(note).toContainText('Switch HSTS off first, or open the system by its IP address afterwards.');
        const link = note.getByRole('link', {name: 'Certificate'});
        await expect(link).toHaveAttribute('href', '/system/certificates#https');
        await link.click();
        await expect(page).toHaveURL(/\/system\/certificates#https$/);
        await expect(page.locator('#https')).toBeInViewport();
    });

    test('an openccu-lite release says nothing about HSTS', async ({page}) => {
        await httpsRoute(page, {hsts: true});
        await staged(page, false);
        await page.goto('/system/updates');
        await expect(page.getByText('openccu-lite-x86_64-ova-1.0.0.zip')).toBeVisible();
        await expect(page.locator('.ol-wayback')).toHaveCount(0);
    });
});

test('the power menu: while HSTS clears, only the address is shown, as with HSTS on', async ({page}) => {
    await httpsRoute(page, {hsts_clearing: true, hsts_clearing_until: UNTIL});
    await page.goto(`${NAME}/`);
    await page.locator('.ol-powerbtn').click();
    await page.locator('.ol-powerpop [data-action="recovery"]').click();
    const dialog = page.getByRole('dialog', {name: 'Reboot into the recovery system', exact: true});
    await expect(dialog).toContainText(ADDRESS);
    await dialog.getByRole('button', {name: 'Reboot into the recovery system', exact: true}).click();
    const state = page.locator('.ol-powerstate');
    await expect(state.getByRole('link', {name: ADDRESS})).toHaveAttribute('href', ADDRESS);
    await expect(state.locator('a')).toHaveCount(1);
});
