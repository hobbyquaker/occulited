import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// task 76: the subscribers as one card per interface process, with a removal per row behind the
// shell's dialog. The maintainer, 2026-09-19: the clients on this system on the Interfaces page, those
// on the network on the Remote access page while classic RPC is on (classicOn), each page linking to
// the other. The removal route is answered here, per test, so the shared stub stays as it is.

const REMOVE = '**/api/system/v1/radio/subscribers/remove';
async function classicOn(page: Page) {
    await page.route('**/api/system/v1/remote-access', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const body = (await response.json()) as {classic: {plain: boolean}};
        body.classic.plain = true;
        await route.fulfill({response, json: body});
    });
}

const USER = {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'};


// task 183 (the maintainer): the Interfaces page has no process cards any more; the processes and
// their subscribers are the Remote access page's, and each connection panel carries its process's
// state as the Services page's dot
test('the Interfaces page has no process cards; the connection panels carry the process dots', async ({page}) => {
    // multimacd as a unit a condition kept off (no module on the header): the hollow ring
    await page.route('**/api/system/v1/services', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {services: Record<string, unknown>[]};
        body.services.push({id: 'multimacd', kind: 'system', running: false, skipped: true, enabled: true, unit_file_state: 'enabled', managed: true});
        await route.fulfill({response, json: body});
    });
    await page.goto('/radio');
    await expect(page.locator('[data-process="rfd"]')).toBeVisible();
    await expect(page.getByRole('heading', {level: 2, name: 'Interface processes'})).toHaveCount(0);
    // the only cards of the processes are the registered clients' (2026-09-19), not the old panels
    await expect(page.locator('.ol-process:not(.ol-subscribers .ol-process)')).toHaveCount(0);
    await expect(page.locator('[data-process="rfd"] .ol-card-sub .ol-dot')).toHaveAttribute('data-state', 'running');
    await expect(page.locator('[data-process="rfd"] .ol-card-sub .ol-dot')).toHaveClass(/\bok\b/);
    await expect(page.locator('[data-process="rfd"] .ol-card-sub')).toHaveText('rfd');
    await expect(page.locator('[data-process="hmipserver"] .ol-card-sub .ol-dot')).toHaveAttribute('title', 'Running');
    await expect(page.locator('[data-process="multimacd"] .ol-card-sub')).toHaveText('shares one module between rfd and hmipserver');
    await expect(page.locator('[data-process="multimacd"] .ol-card-sub .ol-dot')).toHaveAttribute('data-state', 'skipped');
    await expect(page.locator('[data-process="multimacd"] .ol-card-sub .ol-dot')).toHaveAttribute('title', 'Skipped');
    // the ring is app.css's now, not the Services page's alone
    const ring = await page.locator('[data-process="multimacd"] .ol-card-sub .ol-dot').evaluate((e) => getComputedStyle(e).borderTopWidth);
    expect(ring).toBe('2px');
    expect(await fitsWindow(page)).toBe(true);
});

// occulited task 84 (the maintainer): the Interfaces page lists the external clients too, with the
// Status page's badges and the clients' names
test('the Interfaces page lists every client, after the connections', async ({page}) => {
    await page.goto('/system/interfaces');
    const heading = page.getByRole('heading', {level: 2, name: 'Registered clients'});
    await expect(heading).toBeVisible();
    // after the connection panels
    const after = await heading.evaluate((h) => !!(document.querySelector('[data-process="rfd"]')!.compareDocumentPosition(h) & Node.DOCUMENT_POSITION_FOLLOWING));
    expect(after).toBe(true);
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    // four callbacks and one lite-rpc stream (occulited task 87)
    await expect(rf.locator('.ol-sub-count')).toHaveText('5 subscribers');
    await expect(rf.locator('li.ol-sub', {hasText: 'nr_Ab1Cd2_BidCos-RF'})).toContainText('node-red-contrib-ccu');
    await expect(rf.locator('li.ol-sub', {hasText: 'nr_Ab1Cd2_BidCos-RF'}).locator('[data-client-kind]')).toHaveText('internal');
    await expect(rf.locator('li.ol-sub', {hasText: '1007'})).toContainText('ReGa');
    await expect(rf.locator('li.ol-sub', {hasText: 'BidCos-RF_java'})).toContainText('hmipserver');
    const ext = rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'}).locator('[data-client-kind]');
    await expect(ext).toHaveText('external');
    await expect(ext).toHaveAttribute('data-client-kind', 'external');
    await expect(ext).toHaveClass(/\bol-badge-accent\b/);
    await expect(rf.locator('.ol-sub-url').first()).toHaveAttribute('title', 'xmlrpc_bin://127.0.0.1:31999');
    // every client is here, so no link to the network's
    await expect(page.locator('[data-subscribers-link]')).toHaveCount(0);
});

test('the Remote access page lists the clients on the network below classic RPC, only while it is on', async ({page}) => {
    await page.goto('/system/remote-access');
    await expect(page.getByRole('heading', {level: 2, name: 'Classic RPC (as on a CCU)'})).toBeVisible();
    await expect(page.getByRole('heading', {level: 3, name: 'Registered clients'})).toHaveCount(0);

    await classicOn(page);
    await page.goto('/system/remote-access');
    const heading = page.getByRole('heading', {level: 3, name: 'Registered clients'});
    await expect(heading).toBeVisible();
    const below = await heading.evaluate((h) => {
        const rpc = [...document.querySelectorAll('h2')].find((x) => x.textContent?.startsWith('Classic RPC'))!;
        return !!(rpc.compareDocumentPosition(h) & Node.DOCUMENT_POSITION_FOLLOWING);
    });
    expect(below).toBe(true);
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    await expect(rf.locator('.ol-sub-count')).toHaveText('1 subscriber');
    await expect(rf.locator('li.ol-sub')).toContainText('mb_BidCos_RF');
    await expect(rf.locator('li.ol-sub [data-client-kind]')).toHaveText('external');
    await expect(page.locator('.ol-process[data-interface="HmIP-RF"] .ol-subs-empty')).toHaveText('no subscribers');
    const other = page.locator('[data-subscribers-link] a');
    await expect(other).toHaveText('All clients: Interfaces');
    await other.click();
    await expect(page).toHaveURL(/\/system\/interfaces#subscribers$/);
});

test('an interface without subscribers says so', async ({page}) => {
    await page.route('**/api/system/v1/radio', async (route) => {
        const response = await route.fetch();
        const body = await response.json();
        body.interfaces[1].subscribers = [];
        await route.fulfill({response, json: body});
    });
    // and no lite-rpc stream on it either (occulited task 87)
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const body = await response.json();
        delete body.subscribers['HmIP-RF'];
        await route.fulfill({response, json: body});
    });
    await page.goto('/system/interfaces');
    const hmip = page.locator('.ol-process[data-interface="HmIP-RF"]');
    await expect(hmip.locator('.ol-subs-empty')).toHaveText('no subscribers');
    await expect(hmip.locator('li.ol-sub')).toHaveCount(0);
});

test('the removal is offered to administrators only', async ({page}) => {
    await page.goto('/system/interfaces');
    const buttons = page.locator('button.ol-sub-remove');
    await expect(buttons).toHaveCount(8);
    await expect(buttons.first()).toHaveAttribute('aria-label', 'Remove subscription');
    await expect(buttons.first()).toHaveAttribute('title', 'Remove subscription');
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: USER}));
    await page.route('**/api/system/v1/radio/firmware', (r) => r.fulfill({status: 403, json: {error: 'forbidden', message: 'administrator role required'}}));
    await page.goto('/system/interfaces');
    await expect(page.locator('li.ol-sub').first()).toBeVisible();
    await expect(page.locator('button.ol-sub-remove')).toHaveCount(0);
});

test('the dialog explains, Escape cancels, Remove calls the route and the row goes', async ({page}) => {
    const calls: unknown[] = [];
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => (release = r));
    await page.route(REMOVE, async (route) => {
        calls.push(route.request().postDataJSON());
        await gate;
        await route.fulfill({json: {removed: true, subscribers: [
            {id: '1007', url: 'xmlrpc_bin://127.0.0.1:31999', local: true},
            {id: 'BidCos-RF_java', url: 'http://127.0.0.1:39292/bidcos', local: true},
            {id: 'nr_Ab1Cd2_BidCos-RF', url: 'http://127.0.0.1:2048', local: true},
        ]}});
    });
    await classicOn(page);
    await page.goto('/system/remote-access');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    const row = rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'});
    await row.getByRole('button', {name: 'Remove subscription'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Remove mb_BidCos_RF from BidCos-RF?');
    await expect(dialog).toContainText('A subscription is an address BidCos-RF sends every event to. Removing it makes the process stop sending events there.');
    await expect(dialog).toContainText('This helps when the client is gone, has moved to another address or port, or is listed twice.');
    await expect(dialog).toContainText('It does not help against a client that is still running: it registers again at its next start or re-init, and it may receive no events until then.');
    await expect(dialog).toContainText('ID: mb_BidCos_RF');
    await expect(dialog).toContainText('Address: http://198.51.100.9:2049');
    await expect(dialog).toContainText('on the network');
    // Cancel is the default: Enter out of habit does not remove anything
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    expect(calls).toEqual([]);

    await row.getByRole('button', {name: 'Remove subscription'}).click();
    await dialog.getByRole('button', {name: 'Remove subscription'}).click();
    // while it runs, the dialog's buttons and the rows' buttons are off
    await expect(dialog.getByRole('button', {name: 'Remove subscription'})).toBeDisabled();
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toBeDisabled();
    await expect(rf.locator('button.ol-sub-remove').first()).toBeDisabled();
    release();
    await expect(dialog).toBeHidden();
    expect(calls).toEqual([{interface: 'BidCos-RF', id: 'mb_BidCos_RF', url: 'http://198.51.100.9:2049'}]);
    await expect(row).toHaveCount(0);
    await expect(rf.locator('.ol-sub-count')).toHaveText('0 subscribers');
    await expect(rf.locator('.ol-subs-empty')).toHaveText('no subscribers');
});

test('an entry the process keeps leaves the dialog open with the reason', async ({page}) => {
    await page.route(REMOVE, (route) => route.fulfill({json: {removed: false, subscribers: [
        {id: 'hmm_VirtualDevices', url: 'xmlrpc://127.0.0.1:2140', local: true, duplicate: true},
        {id: 'hmm_VirtualDevices', url: 'xmlrpc://127.0.0.1:2141', local: true, duplicate: true},
    ]}}));
    await page.goto('/system/interfaces');
    const vd = page.locator('.ol-process[data-interface="VirtualDevices"]');
    await vd.locator('li.ol-sub').first().getByRole('button', {name: 'Remove subscription'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('The same id is also registered with another address; one of the two is likely left over.');
    await dialog.getByRole('button', {name: 'Remove subscription'}).click();
    await expect(dialog.locator('.ol-dialog-outcome')).toHaveText('The entry is still there: VirtualDevices did not drop it. A restart of the interface process on the Services page removes it; that interrupts the radio for a moment and makes every running client register again. A reboot does the same.');
    await expect(dialog.getByRole('button', {name: 'Remove subscription'})).toHaveCount(0);
    await dialog.getByRole('button', {name: 'Close'}).last().click();
    await expect(dialog).toBeHidden();
    await expect(vd.locator('li.ol-sub')).toHaveCount(2);
});

test('a stale page: the refusal is shown in the dialog and the list is read again', async ({page}) => {
    await page.route(REMOVE, (route) => route.fulfill({status: 422, json: {error: 'not-subscribed', message: 'this subscription is not registered with HmIP-RF (any more)'}}));
    await page.goto('/system/interfaces');
    const hmip = page.locator('.ol-process[data-interface="HmIP-RF"]');
    await hmip.locator('li.ol-sub').first().getByRole('button', {name: 'Remove subscription'}).click();
    const dialog = page.getByRole('dialog');
    const reread = page.waitForRequest((r) => r.method() === 'GET' && new URL(r.url()).pathname === '/api/system/v1/radio');
    await dialog.getByRole('button', {name: 'Remove subscription'}).click();
    await expect(dialog.locator('.ol-dialog-outcome.error')).toContainText('not registered with HmIP-RF');
    await reread;
    await expect(dialog.getByRole('button', {name: 'Remove subscription'})).toBeEnabled();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
});

test('the dialog in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await classicOn(page);
    await page.goto('/system/remote-access');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    await expect(rf.locator('.ol-sub-count')).toHaveText('1 Abonnent');
    await expect(page.locator('[data-subscribers-link] a')).toHaveText('Alle Clients: Schnittstellen');
    await rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'}).getByRole('button', {name: 'Abonnement entfernen'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('mb_BidCos_RF von BidCos-RF entfernen?');
    await expect(dialog).toContainText('Ein Abonnement ist eine Adresse, an die BidCos-RF jedes Ereignis schickt. Nach dem Entfernen schickt der Prozess dorthin keine Ereignisse mehr.');
    await expect(dialog).toContainText('Das hilft, wenn der Client nicht mehr existiert, unter eine andere Adresse oder einen anderen Port umgezogen ist oder doppelt eingetragen ist.');
    await expect(dialog).toContainText('Gegen einen Client, der noch läuft, hilft es nicht: Er meldet sich bei seinem nächsten Start oder Re-Init wieder an und bekommt bis dahin womöglich keine Ereignisse.');
    await expect(dialog).toContainText('Adresse: http://198.51.100.9:2049');
    await expect(dialog).toContainText('im Netzwerk');
    await expect(dialog.getByRole('button', {name: 'Abonnement entfernen'})).toBeVisible();
    await expect(dialog.getByRole('button', {name: 'Abbrechen'})).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
});
