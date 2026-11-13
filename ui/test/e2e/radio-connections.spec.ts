import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// task 129 phase 3 (D-81, D-82, D-98): the connection of each interface process on the Interfaces
// page - what runs on what, the choices the detected hardware allows, the preview in the shell's
// dialog, the paired-BidCos warning before BidCos-RF loses its local radio, and the change itself.
// task 150: the panels on the modules' track with the dropdowns at their foot, "Automatic: …" as
// what each uses now, one HmIP entry per path to a module, multimacd below the two it serves.

async function ownConn(page: Page, baseURL: string | undefined) {
    await page.context().addCookies([{name: 'stub-conn', value: `conn-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
}

test('the panels say what each process runs on, and the choices are the detected modules and paths', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    await page.goto('/radio');
    await expect(page.locator('h2#connections')).toHaveText('Connections');
    const hmip = page.locator('[data-process="hmipserver"]');
    const rfd = page.locator('[data-process="rfd"]');
    const mmd = page.locator('[data-process="multimacd"]');
    await expect(hmip.locator('.conn-now')).toHaveText('Automatic: HMIP-RFUSB 0000000A02, shared with BidCos-RF through multimacd');
    await expect(hmip).toContainText('HmIP-HAPs and DRAPs can route through this module.');
    await expect(rfd.locator('.conn-now')).toHaveText('Automatic: HMIP-RFUSB 0000000A02 through multimacd + LAN gateways');
    // multimacd: the nodes it provides on top, the one it opens at the foot
    await expect(mmd.locator('.conn-mmd-provides dt')).toHaveText(['/dev/mmd_bidcos', '/dev/mmd_hmip']);
    await expect(mmd.locator('.conn-mmd-provides dd')).toHaveText(['↑ rfd (BidCos-RF)', '↑ hmipserver (HmIP-RF)']);
    await expect(mmd.locator('.conn-mmd-uses')).toHaveText('connected to /dev/raw-uart · HMIP-RFUSB 0000000A02');
    // one entry per path; both have "none" - HmIP's runs HMServer.jar (occulited task 147)
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveText(['Automatic', 'HMIP-RFUSB · 0000000A02 · /dev/raw-uart', 'HMIP-RFUSB · 0000000A02 · /dev/mmd_hmip through multimacd on /dev/raw-uart', 'None (virtual devices only)']);
    await expect(page.getByLabel('Module for BidCos-RF').locator('option')).toHaveText(['Automatic', 'HMIP-RFUSB · 0000000A02 · /dev/mmd_bidcos through multimacd on /dev/raw-uart', 'No local radio (LAN gateway only)']);
    await expect(page.getByRole('button', {name: 'Apply changes'})).toBeDisabled();
    // the module card names its USB device
    await expect(page.locator('.ol-module-usb').first()).toHaveText('1b1f:c020 · eQ-3 HmIP-RFUSB (Silicon Labs)');
});

test('the layout: two panels side by side, multimacd spanning them below, the dropdowns in line', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    await page.setViewportSize({width: 1280, height: 900});
    await page.goto('/radio');
    const box = async (sel: string) => (await page.locator(sel).boundingBox())!;
    const h = await box('[data-process="hmipserver"]');
    const r = await box('[data-process="rfd"]');
    const m = await box('[data-process="multimacd"]');
    // three would fit in a row: each at least 340 px
    expect(h.width).toBeGreaterThanOrEqual(340);
    expect(Math.abs(h.y - r.y)).toBeLessThan(1);
    // each panel under its module card: BidCos-RF left, as the stub's modules are ordered
    expect(h.x).toBeGreaterThan(r.x + r.width);
    const mb = await box('#ol-module-BidCos-RF');
    const mh = await box('#ol-module-HmIP-RF');
    expect(Math.abs(mb.x - r.x)).toBeLessThan(1);
    expect(Math.abs(mh.x - h.x)).toBeLessThan(1);
    expect(m.y).toBeGreaterThan(h.y + h.height);
    expect(Math.abs(m.x - r.x)).toBeLessThan(1);
    expect(Math.abs(m.x + m.width - (h.x + h.width))).toBeLessThan(1);
    // the dropdowns at the panels' feet, on one line
    const s1 = (await page.getByLabel('Module for HmIP-RF').boundingBox())!;
    const s2 = (await page.getByLabel('Module for BidCos-RF').boundingBox())!;
    expect(Math.abs(s1.y - s2.y)).toBeLessThan(1);
    // the button keeps its distance from the panels
    const b = (await page.getByRole('button', {name: 'Apply changes'}).boundingBox())!;
    expect(b.y - (m.y + m.height)).toBeGreaterThanOrEqual(12);
    // at phone width, one column, in the same order
    await page.setViewportSize({width: 390, height: 844});
    const h2 = await box('[data-process="hmipserver"]');
    const r2 = await box('[data-process="rfd"]');
    expect(h2.y).toBeGreaterThan(r2.y + r2.height);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test('HmIP-direct: the dialog lists the paired BidCos devices, then the change runs', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/connections')) puts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    // the LAN gateway shortcut picks "none"
    await page.locator('[data-process="rfd"]').getByRole('button', {name: 'Use the LAN gateways only'}).click();
    await expect(page.getByLabel('Module for BidCos-RF')).toHaveValue('none');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Change the radio connections?');
    await expect(dialog).toContainText('HmIP-RF: Automatic');
    await expect(dialog).toContainText('BidCos-RF: No local radio (LAN gateway only)');
    await expect(dialog).toContainText('hmipserver: HMIP-RFUSB 0000000A02, directly on /dev/raw-uart');
    await expect(dialog).toContainText('multimacd: not needed');
    await expect(dialog).toContainText('multimacd, rfd, hmipserver are stopped and started again');
    await expect(dialog).toContainText('BidCos-RF loses its local radio.');
    await expect(dialog).toContainText('Paired BidCos-RF devices (2):');
    await expect(dialog).toContainText('JEQ9000001 · HM-CC-TC');
    // a warning: the cancel button has the focus, cancel sends nothing
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toBeFocused();
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(puts).toHaveLength(0);
    await page.getByRole('button', {name: 'Apply changes'}).click();
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toBeHidden();
    expect(puts).toHaveLength(1);
    expect(JSON.parse(puts[0]!)).toEqual({hmip: '', hmip_path: '', bidcos: 'none', confirm: true});
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toHaveText('Automatic: HMIP-RFUSB 0000000A02, directly on /dev/raw-uart');
    await expect(page.locator('[data-process="rfd"] .conn-now')).toHaveText('Chosen: LAN gateways');
    await expect(page.locator('[data-process="multimacd"] .conn-now')).toHaveText('not needed');
    await expect(page.locator('.conn-last summary')).toContainText('succeeded');
    await expect(page.getByRole('button', {name: 'Apply changes'})).toBeDisabled();
});

test('a box whose module cards list HmIP-RF first puts the HmIP-RF panel first', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    await page.route('**/api/system/v1/radio', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {modules: unknown[]};
        body.modules.reverse();
        await route.fulfill({response, json: body});
    });
    await page.goto('/radio');
    await expect(page.locator('.conn-grid > .ol-card').first()).toHaveAttribute('data-process', 'hmipserver');
    await expect(page.locator('.ol-cards-radio .ol-card').first()).toHaveAttribute('id', 'ol-module-HmIP-RF');
});

test('HmIP through multimacd by choice, and a path the box refuses', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/connections')) puts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    const pick = page.getByLabel('Module for HmIP-RF');
    // directly while BidCos-RF shares the module through multimacd: the box says why not
    await pick.selectOption('0000000A02|direct');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    // the connections' own error line (the device descriptions below carry an .ol-warn of their own, task 97)
    await expect(page.locator('div.ol-warn')).toContainText('HmIP cannot open the module directly while BidCos-RF uses it through the multiplexer');
    await expect(page.getByRole('dialog')).toBeHidden();
    // BidCos-RF off, HmIP through multimacd
    await pick.selectOption('0000000A02|multimacd');
    await page.getByLabel('Module for BidCos-RF').selectOption('none');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('HmIP-RF: HMIP-RFUSB · 0000000A02 · /dev/mmd_hmip through multimacd on /dev/raw-uart');
    await expect(dialog).toContainText('hmipserver: HMIP-RFUSB 0000000A02, through multimacd on /dev/raw-uart');
    await expect(dialog).toContainText('multimacd: runs on /dev/raw-uart');
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toBeHidden();
    expect(JSON.parse(puts[0]!)).toEqual({hmip: '0000000A02', hmip_path: 'multimacd', bidcos: 'none', confirm: true});
    const mmd = page.locator('[data-process="multimacd"]');
    await expect(mmd.locator('.conn-mmd-provides dd')).toHaveText(['not used', '↑ hmipserver (HmIP-RF)']);
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toHaveText('Chosen: HMIP-RFUSB 0000000A02, through multimacd on /dev/raw-uart');
    await expect(pick).toHaveValue('0000000A02|multimacd');
});

test('a pinned module that is missing is named on the Interfaces and the Status page', async ({page, baseURL}) => {
    await page.context().addCookies([
        {name: 'stub-conn', value: `conn-${Math.random().toString(36).slice(2)}`, url: baseURL!},
        {name: 'stub-conn-missing', value: '0001TK0001', url: baseURL!},
    ]);
    await page.goto('/radio');
    await expect(page.locator('[data-notice="missing-hmip"]')).toContainText('The module chosen for HmIP-RF (0001TK0001) is missing.');
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toHaveText('Chosen: the chosen module 0001TK0001 is missing: hmipserver cannot start');
    // the pin stays selectable, so it can be set back to Automatic
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveText(['Automatic', '0001TK0001 · missing', 'None (virtual devices only)']);
    await page.goto('/');
    const card = page.locator('[data-component="HmIP-RF-module"]');
    await expect(card).toContainText('chosen module missing');
    await expect(card).toContainText('0001TK0001');
    await card.click();
    await expect(page).toHaveURL(/\/system\/interfaces#connections$/);
});

// task 155 (D-106): the one rejection line, two causes; the retry for a key server that was not
// reached, the guided fresh start for a module it refused, and the marker from before the causes
test('the key server was not reached: the notice names the network, and Try again restarts HmIP-RF', async ({page, baseURL}) => {
    const id = `conn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-conn', value: id, url: baseURL!}, {name: 'stub-conn-fatal', value: 'unreachable', url: baseURL!}]);
    const posts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().includes('/radio/hmip/exchange/')) posts.push(r.url().split('/exchange/')[1]!);
    });
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-fatal"]');
    await expect(notice).toHaveAttribute('data-cause', 'unreachable');
    await expect(notice).toContainText("could not reach eQ-3's key server");
    await expect(notice).toContainText('Check the network connection and the DNS settings');
    await expect(notice).not.toContainText('Start fresh');
    await notice.getByRole('button', {name: 'Try again'}).click();
    expect(posts).toEqual(['retry']);
    await expect(page.locator('[data-notice="hmip-exchange-done"]')).toContainText('tries the move to this module again');
    await expect(notice).toBeHidden();
});

test('the module was refused: the fresh start asks for the host name, moves the identity aside and switches to local key mode', async ({page, baseURL}) => {
    const id = `conn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-conn', value: id, url: baseURL!}, {name: 'stub-conn-fatal', value: 'refused', url: baseURL!}, {name: 'stub-lk', value: id, url: baseURL!}]);
    const posts: {path: string; body: unknown}[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().includes('/radio/hmip/exchange/')) posts.push({path: r.url().split('/exchange/')[1]!, body: JSON.parse(r.postData() ?? '{}')});
    });
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-fatal"]');
    await expect(notice).toHaveAttribute('data-cause', 'refused');
    // task 301: the server refused and does not say why - no "unknown module", no "used once"; the ways out
    await expect(notice).toContainText("eQ-3's key server refused to move the HmIP network of this system to the module 3014F711A000040000000A02. It does not say why");
    await expect(notice).not.toContainText('does not know the module');
    await expect(notice).toContainText('The network belongs to the previous module 3014F711A0001F0000000A04.');
    await expect(notice).toContainText('The ways out: the module the network is on now, where it is still at hand');
    await expect(notice).toContainText('the saved files of an earlier module');
    await expect(notice).not.toContainText('Try again');
    // the local record of exchanges, newest first, with the key server's part and the outcome
    const record = notice.locator('[data-exchanges="2"]');
    await expect(record.locator('summary')).toHaveText('Adapter exchanges recorded on this system (2)');
    await record.locator('summary').click();
    const rows = record.locator('li');
    await expect(rows.nth(0)).toContainText('3014F711A0001F0000000A04 → 3014F711A000040000000A02 · 0xA0B1C2 · through eQ-3\'s key server · refused by the key server');
    await expect(rows.nth(1)).toContainText('3014F711A0001F5F000000AF → 3014F711A0001F0000000A04 · 0xA0B1C2 · through eQ-3\'s key server · accepted');
    await expect(notice.locator('[data-local-swap]')).toHaveCount(0);
    const local = notice.getByLabel(/Switch to local key mode in the same step/);
    await expect(local).toBeChecked();
    await notice.getByRole('button', {name: 'Start fresh with this module…'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Start fresh with this module?');
    await expect(dialog).toContainText('The HmIP identity of the previous module 3014F711A0001F0000000A04 is moved aside and kept, nothing is deleted.');
    await expect(dialog).toContainText('every HmIP device has to be reset and paired again');
    await expect(dialog).toContainText('Local key mode is switched on in the same step');
    await expect(dialog).toContainText('Type the host name lab-ccu to confirm.');
    // the input has the focus (the dialog's rule with a field); a wrong name sends nothing
    await expect(dialog.getByLabel('Host name')).toBeFocused();
    await dialog.getByLabel('Host name').fill('other-ccu');
    await dialog.getByRole('button', {name: 'Start fresh'}).click();
    await expect(notice.locator('.ol-warn')).toContainText("The name typed is not this system's host name; nothing happened.");
    expect(posts).toHaveLength(0);
    await notice.getByRole('button', {name: 'Start fresh with this module…'}).click();
    await dialog.getByLabel('Host name').fill(' LAB-CCU ');
    await dialog.getByRole('button', {name: 'Start fresh'}).click();
    await expect(dialog).toBeHidden();
    // the dialog trims what was typed; the case is the server's to ignore
    expect(posts).toEqual([{path: 'fresh-start', body: {confirm: true, hostname: 'LAB-CCU', local_key: true}}]);
    const done = page.locator('[data-notice="hmip-exchange-done"]');
    await expect(done).toContainText('HmIP-RF is starting with an empty network.');
    await done.getByRole('link', {name: 'HmIP device keys'}).click();
    await expect(page).toHaveURL(/\/system\/keys#device-keys$/);
    // the Keys page: local key mode on, and the previous module's identity kept as a fresh-start snapshot
    await expect(page.locator('[data-state="on"]')).toContainText('the HmIP network key is kept on this system');
    const snap = page.locator('.lk-snapshots li[data-sgtin="3014F711A0001F0000000A04"]');
    await expect(snap).toHaveAttribute('data-kind', 'fresh-start');
    await expect(snap).toContainText('the previous module, moved aside by the fresh start');
});

test('the fresh start without local key mode, and a marker from before the causes', async ({page, baseURL}) => {
    const id = `conn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-conn', value: id, url: baseURL!}, {name: 'stub-conn-fatal', value: 'refused', url: baseURL!}, {name: 'stub-lk', value: id, url: baseURL!}]);
    const posts: unknown[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().endsWith('/exchange/fresh-start')) posts.push(JSON.parse(r.postData() ?? '{}'));
    });
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-fatal"]');
    await notice.getByLabel(/Switch to local key mode in the same step/).uncheck();
    await notice.getByRole('button', {name: 'Start fresh with this module…'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).not.toContainText('Local key mode is switched on');
    await dialog.getByLabel('Host name').fill('lab-ccu');
    await dialog.getByRole('button', {name: 'Start fresh'}).click();
    expect(posts).toEqual([{confirm: true, hostname: 'lab-ccu', local_key: false}]);

    // a marker without a cause: the sentence that names both ways, no action
    await page.context().addCookies([{name: 'stub-conn', value: `${id}-2`, url: baseURL!}, {name: 'stub-conn-fatal', value: 'plain', url: baseURL!}]);
    await page.goto('/radio');
    await expect(notice).toHaveAttribute('data-cause', 'unknown');
    await expect(notice).toContainText("eQ-3's key server rejected the adapter exchange to 3014F711A000040000000A02");
    await expect(notice.getByRole('button')).toHaveCount(0);
});

test('a user sees the diagnosis, not the actions, and the view is not asked for', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-conn', value: `conn-${Math.random().toString(36).slice(2)}`, url: baseURL!}, {name: 'stub-conn-fatal', value: 'refused', url: baseURL!}]);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.route('**/api/system/v1/radio/firmware', (r) => r.fulfill({status: 403, json: {error: 'forbidden', message: 'administrator role required'}}));
    const calls: string[] = [];
    page.on('request', (r) => calls.push(new URL(r.url()).pathname));
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-fatal"]');
    await expect(notice).toContainText("eQ-3's key server refused to move the HmIP network of this system to the module");
    await expect(notice).toContainText('The ways out: the module the network is on now');
    await expect(notice.getByRole('button')).toHaveCount(0);
    await expect(notice.getByRole('checkbox')).toHaveCount(0);
    // the record comes with the admin's view, which a user is not asked for
    await expect(notice.locator('[data-exchanges]')).toHaveCount(0);
    expect(calls).not.toContain('/api/system/v1/radio/hmip/exchange');
});

// openccu-lite task 301 / B-289: the record shows that the previous module took the network over
// without the key server (a local swap onto a module whose firmware cannot take the network key) -
// the one cause of a refusal the page can name; the Status warning says the ways out, no "unknown module"
test('refused, and the record names the local swap onto the previous module as the cause; the Status warning', async ({page, baseURL}) => {
    const id = `conn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-conn', value: id, url: baseURL!}, {name: 'stub-conn-fatal', value: 'refused', url: baseURL!}, {name: 'stub-conn-exchanges', value: 'swap', url: baseURL!}]);
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-fatal"]');
    const swap = notice.locator('[data-local-swap="3014F711A0001F0000000A04"]');
    await expect(swap).toContainText('3014F711A0001F0000000A04 took the network over on');
    await expect(swap).toContainText('without the key server: its firmware could not take the network key, so the key server cannot move the network on from it');
    await notice.locator('[data-exchanges] summary').click();
    await expect(notice.locator('[data-exchanges] li').nth(1)).toContainText("local swap without the key server (the module's firmware cannot take the network key) · accepted");
    // the Status page's warning for the same marker
    await page.route('**/api/system/v1/warnings', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []), {id: 'hmip-adapter', variant: 'adapter-exchange-rejected', severity: 'error', href: '/system/interfaces#connections', since: new Date().toISOString(), params: {adapter: '3014F711A000040000000A02', line: 'Adapter exchange was rejected by key server.', cause: 'refused'}}];
        await route.fulfill({response, json: j});
    });
    await page.goto('/');
    const warning = page.locator('[data-notice="hmip-adapter"]');
    await expect(warning).toContainText("eQ-3's key server refused to move the HmIP network of this system to the module 3014F711A000040000000A02. The Interfaces page names the ways out");
    await expect(warning).not.toContainText('does not know');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/radio');
    await expect(notice).toContainText('hat es abgelehnt, das HmIP-Netz dieses Systems auf das Modul 3014F711A000040000000A02 zu übertragen. Einen Grund nennt er nicht');
    await expect(notice).toContainText('Die Auswege: das Modul, auf dem das Netz jetzt liegt');
});

// openccu-lite B-289: the record as B-289 writes a local swap - a failed move with the module's
// firmware; the diagnosis names it and the update, and the Status page's warning for a network
// stranded on such a module
test('refused after a local swap: the firmware, the update, the failed entry; the hmip-local-swap warning', async ({page, baseURL}) => {
    const id = `conn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-conn', value: id, url: baseURL!}, {name: 'stub-conn-fatal', value: 'refused', url: baseURL!}, {name: 'stub-conn-exchanges', value: 'swap-failed', url: baseURL!}]);
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-fatal"]');
    await expect(notice.locator('[data-local-swap="3014F711A0001F0000000A04"]')).toContainText('its application firmware 1.8.3 is below 2.8.0 and could not take the network key');
    await expect(notice.locator('[data-local-swap-update]')).toContainText('Update the firmware of module 3014F711A0001F0000000A04 (Updates page, radio firmware)');
    await notice.locator('[data-exchanges] summary').click();
    await expect(notice.locator('[data-exchanges] li').nth(1)).toContainText("firmware 1.8.3 · local swap without the key server (the module's firmware cannot take the network key) · failed: the module's firmware cannot take the network key");
    await page.route('**/api/system/v1/warnings', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []), {id: 'hmip-local-swap', variant: '3014F711A000040000000A02@2026-10-01T16:36:00Z', severity: 'error', href: '/system/interfaces#connections', since: new Date().toISOString(), params: {module: '3014F711A000040000000A02', from: '3014F711A0001F0000000A04', version: '1.8.3', minimum: '2.8.0'}}];
        await route.fulfill({response, json: j});
    });
    await page.goto('/');
    const warning = page.locator('[data-notice="hmip-local-swap"]');
    await expect(warning).toContainText("HmIP-RF runs on module 3014F711A000040000000A02 without the HmIP network's key");
    await expect(warning).toContainText('its application firmware 1.8.3 is below 2.8.0');
    await expect(warning).toContainText('The way out is the saved files of 3014F711A0001F0000000A04');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/radio');
    await expect(notice.locator('[data-local-swap="3014F711A0001F0000000A04"]')).toContainText('Anwendungsfirmware 1.8.3 liegt unter 2.8.0');
});

// openccu-lite B-289 (maintainer, 2026-10-02): the preview refuses moving HmIP-RF onto a module whose
// firmware is below 2.8.0 while local key mode is off - the dialog names the firmware and the
// update and offers nothing to confirm; a PUT the daemon refuses anyway gets the same words
test('a move onto a module below firmware 2.8.0 is refused in the preview, and the 422 is said the same', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    const refused = {module: '3014F711A000040000000A02', version: '1.8.3', minimum: '2.8.0'};
    let previewRefuses = true;
    const puts: string[] = [];
    await page.route('**/api/system/v1/radio/connections/preview', async (route) => {
        const response = await route.fetch();
        const j = await response.json();
        j.hmip_move = {from: '3014F711A0001F0000000A04', to: refused.module, local_key: false, snapshot: true, to_version: '1.8.3', ...(previewRefuses ? {refused} : {})};
        await route.fulfill({response, json: j});
    });
    await page.route('**/api/system/v1/radio/connections', async (route) => {
        if (route.request().method() !== 'PUT') return route.fallback();
        puts.push(route.request().postData() ?? '');
        await route.fulfill({status: 422, json: {error: 'hmip-firmware', message: 'module refused', detail: refused}});
    });
    await page.goto('/radio');
    await page.locator('[data-process="rfd"]').getByRole('button', {name: 'Use the LAN gateways only'}).click();
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('HmIP-RF cannot move to this module');
    await expect(dialog).toContainText('HmIP-RF cannot move to module 3014F711A000040000000A02: it runs application firmware 1.8.3, and below 2.8.0');
    await expect(dialog).toContainText("Update the module's firmware first (Updates page, radio firmware). With local key mode on, the move needs no key server and is allowed.");
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toHaveCount(0);
    await expect(dialog.getByRole('button', {name: 'Change'})).toHaveCount(0);
    await dialog.getByRole('button', {name: 'Close', exact: true}).and(dialog.locator('.hmm-button')).click();
    await expect(dialog).toBeHidden();
    expect(puts).toHaveLength(0);
    // the daemon's own refusal, should the preview have let it through
    previewRefuses = false;
    await page.getByRole('button', {name: 'Apply changes'}).click();
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toContainText('HmIP-RF cannot move to module 3014F711A000040000000A02: it runs application firmware 1.8.3');
    expect(puts).toHaveLength(1);
    // German
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    previewRefuses = true;
    await page.goto('/radio');
    await page.locator('[data-process="rfd"]').getByRole('button', {name: 'Nur die LAN-Gateways nutzen'}).click();
    await page.getByRole('button', {name: 'Änderungen übernehmen'}).click();
    await expect(dialog).toContainText('HmIP-RF kann nicht auf das Modul 3014F711A000040000000A02 wechseln: Es hat die Anwendungsfirmware 1.8.3');
});

// openccu-lite B-272: an HB-RF-ETH added under LAN devices while both processes are pinned to the
// stick. The module cards come from /var/hm_mode (a module in a role), so the board's module had no
// card and, until the radio hotplug had attached it, no dropdown entry either - and nothing said a
// board was on its way.
async function withBoard(page: Page, baseURL: string | undefined, mode: string) {
    await ownConn(page, baseURL);
    await page.context().addCookies([{name: 'stub-conn-board', value: mode, url: baseURL!}]);
}

test("an HB-RF-ETH's module in no role has a card, is offered in both dropdowns and can be chosen", async ({page, baseURL}) => {
    await withBoard(page, baseURL, 'detected');
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/connections')) puts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    const card = page.locator('[data-module-unused="MEQ9000005"]');
    await expect(card.locator('.ol-card-title')).toHaveText('HM-MOD-RPI-PCB');
    await expect(card.locator('.ol-card-sub')).toHaveText('Not used');
    await expect(card.locator('[data-module-via]')).toHaveText('HB-RF-ETH@192.0.2.50');
    await expect(card).toContainText('MEQ9000005');
    await expect(card).toContainText('3014F711A061A70000000A05');
    await expect(card.locator('[data-module-unused-why]')).toHaveText('No interface process uses this module. It can be chosen for HmIP-RF or BidCos-RF under Connections.');
    // the stick's cards keep their place, and nothing says a board is on its way
    await expect(page.locator('#ol-module-BidCos-RF [data-module-device]')).toHaveText('HMIP-RFUSB');
    await expect(page.locator('[data-notice="hb-rf-eth-pending"]')).toHaveCount(0);
    // both dropdowns offer it beside the stick: HmIP through multimacd only (an HM-MOD-RPI-PCB)
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveText(['Automatic', 'HMIP-RFUSB · 0000000A02 · /dev/raw-uart', 'HMIP-RFUSB · 0000000A02 · /dev/mmd_hmip through multimacd on /dev/raw-uart', 'HM-MOD-RPI-PCB · MEQ9000005 · /dev/mmd_hmip through multimacd on /dev/raw-uart1', 'None (virtual devices only)']);
    await expect(page.getByLabel('Module for BidCos-RF').locator('option')).toHaveText(['Automatic', 'HMIP-RFUSB · 0000000A02 · /dev/mmd_bidcos through multimacd on /dev/raw-uart', 'HM-MOD-RPI-PCB · MEQ9000005 · /dev/mmd_bidcos through multimacd on /dev/raw-uart1', 'No local radio (LAN gateway only)']);
    // choosing it: the preview names it, the change carries it
    await page.getByLabel('Module for HmIP-RF').selectOption('MEQ9000005|multimacd');
    await page.getByLabel('Module for BidCos-RF').selectOption('MEQ9000005');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('HmIP-RF: HM-MOD-RPI-PCB · MEQ9000005 · /dev/mmd_hmip through multimacd on /dev/raw-uart1');
    await expect(dialog).toContainText('BidCos-RF: HM-MOD-RPI-PCB · MEQ9000005 · /dev/raw-uart1');
    await expect(dialog).toContainText('hmipserver: HM-MOD-RPI-PCB MEQ9000005, shared with BidCos-RF through multimacd');
    await expect(dialog).toContainText('multimacd: runs on /dev/raw-uart1');
    await expect(dialog).toContainText("HmIP-RF moves from module 3014F711A000040000000A02 to module 3014F711A061A70000000A05. That is an adapter exchange through eQ-3's key server");
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toBeHidden();
    expect(puts).toHaveLength(1);
    // task 317 (D-120): HmIP-RF moves to the board's module - its identity files are rewritten, so
    // the change is confirmed (the daemon refuses it otherwise)
    expect(JSON.parse(puts[0]!)).toEqual({hmip: 'MEQ9000005', hmip_path: 'multimacd', bidcos: 'MEQ9000005', confirm: true});
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toHaveText('Chosen: HM-MOD-RPI-PCB MEQ9000005, shared with BidCos-RF through multimacd');
    await expect(page.locator('[data-process="multimacd"] .conn-mmd-uses')).toHaveText('connected to /dev/raw-uart1 · HM-MOD-RPI-PCB MEQ9000005');
    // in both roles now: no "not used" card for it
    await expect(card).toHaveCount(0);
});

test('a board just added: the page says it is on its way, reads again, and shows its module once the hotplug attached it', async ({page, baseURL}) => {
    await withBoard(page, baseURL, 'arriving');
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hb-rf-eth-pending"]');
    await expect(notice).toHaveAttribute('data-board-address', '192.0.2.50');
    await expect(notice).toContainText('The HB-RF-ETH at 192.0.2.50 is configured but does not answer yet. The system tries it again by itself; its module appears here once the board answers.');
    await expect(notice.locator('a')).toHaveText('LAN devices');
    await expect(page.locator('[data-module-unused]')).toHaveCount(0);
    // the second read: the kernel has the board, its module is being probed - no Try now for that
    await expect(notice).toContainText('The HB-RF-ETH at 192.0.2.50 is connected; its radio module is being detected.', {timeout: 10_000});
    await expect(notice.locator('[data-action="try-board"]')).toHaveCount(0);
    // the third: the module is in the detection - its card, its entries, the notice gone
    await expect(page.locator('[data-module-unused="MEQ9000005"]')).toBeVisible({timeout: 10_000});
    await expect(notice).toHaveCount(0);
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveCount(5);
    await expect(page.getByLabel('Module for BidCos-RF').locator('option', {hasText: 'MEQ9000005'})).toHaveCount(1);
});

test('a board that does not answer: the notice says the system keeps trying, and Try now asks at once', async ({page, baseURL}) => {
    await withBoard(page, baseURL, 'pending');
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/hb-rf-eth')) puts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hb-rf-eth-pending"]');
    await expect(notice).toContainText('The HB-RF-ETH at 192.0.2.60 is configured but does not answer yet.');
    await expect(notice.locator('a')).toHaveAttribute('href', '/system/lan-devices#hb-rf-eth');
    await notice.locator('[data-action="try-board"]').click();
    await expect.poll(() => puts.length).toBe(1);
    expect(JSON.parse(puts[0]!)).toEqual({address: '192.0.2.60'});
    // still pending: the stick's cards alone, the dropdowns as before
    await expect(page.locator('[data-module-unused]')).toHaveCount(0);
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveCount(4);
});

test('a user sees the pending board without Try now', async ({page, baseURL}) => {
    await withBoard(page, baseURL, 'pending');
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.route('**/api/system/v1/radio/firmware', (r) => r.fulfill({status: 403, json: {error: 'forbidden', message: 'administrator role required'}}));
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hb-rf-eth-pending"]');
    await expect(notice).toContainText('does not answer yet');
    await expect(notice.getByRole('button')).toHaveCount(0);
});

// openccu-lite B-282: an HmIP-RFUSB on the HmIP-only firmware line (HMIP_TRX_App, 1.8.3) is offered
// for HmIP-RF directly only, with the reason in the entry; not for BidCos-RF; the card says where the
// dual firmware is flashed, and that the stick cannot route
test('an HmIP-only stick: HmIP directly, not for BidCos-RF, the reason and the firmware hint', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-conn', value: `conn-${Math.random().toString(36).slice(2)}`, url: baseURL!}, {name: 'stub-conn-trx', value: '1', url: baseURL!}]);
    await page.goto('/radio');
    const hmip = page.locator('[data-process="hmipserver"]');
    await expect(hmip.locator('.conn-now')).toHaveText('Automatic: HMIP-RFUSB 0000000A02, directly on /dev/raw-uart');
    await expect(hmip).toContainText('No routing through HmIP-HAPs or DRAPs');
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveText(['Automatic', 'HMIP-RFUSB · 0000000A02 · /dev/raw-uart · HmIP only - firmware 1.8.3', 'None (virtual devices only)']);
    await expect(page.getByLabel('Module for BidCos-RF').locator('option')).toHaveText(['Automatic', 'No local radio (LAN gateway only)']);
    const why = hmip.locator('[data-hmip-only="0000000A02"]');
    await expect(why).toContainText('HMIP-RFUSB 0000000A02 runs the HmIP-only firmware 1.8.3: BidCos-RF cannot use it, and hmipserver reaches it directly only, not through multimacd. The DualCoPro firmware adds BidCos-RF; the radio firmware section flashes it.');
    await expect(why.getByRole('link', {name: 'Radio firmware'})).toHaveAttribute('href', '/system/updates#radio-firmware');
    await expect(page.locator('[data-process="multimacd"]')).toContainText('not needed');
});

// openccu-lite B-300: a Pi with no module on its GPIO header (rpi4-2: USB sticks only) showed a card
// for the header's UART, "The module did not answer the detection (no answer within 6s)". An empty
// header has no card; a module there that answered wrongly keeps one; a hint only while a chosen
// module is missing.
async function withHeader(page: Page, baseURL: string | undefined, mode: string, missing?: string) {
    await ownConn(page, baseURL);
    await page.context().addCookies([{name: 'stub-conn-header', value: mode, url: baseURL!}, ...(missing ? [{name: 'stub-conn-missing', value: missing, url: baseURL!}] : [])]);
}

test('an empty GPIO header has no module card and no hint', async ({page, baseURL}) => {
    await withHeader(page, baseURL, 'empty');
    await page.goto('/radio');
    await expect(page.locator('#ol-module-BidCos-RF [data-module-device]')).toHaveText('HMIP-RFUSB');
    await expect(page.locator('[data-module-unused]')).toHaveCount(0);
    await expect(page.locator('[data-notice="header-silent"]')).toHaveCount(0);
    await expect(page.getByText('did not answer the detection')).toHaveCount(0);
    // the dropdowns offer the stick alone
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveCount(4);
});

test('a module on the GPIO header that answered wrongly keeps its card', async ({page, baseURL}) => {
    await withHeader(page, baseURL, 'wrong');
    await page.goto('/radio');
    const card = page.locator('[data-module-unused="/dev/raw-uart2"]');
    await expect(card.locator('.ol-card-title')).toHaveText('GPIO@fe201000.serial');
    await expect(card.locator('[data-module-unused-why]')).toHaveText('The module did not answer the detection (unexpected answer: 0x00).');
    await expect(page.locator('[data-notice="header-silent"]')).toHaveCount(0);
});

test('a silent GPIO header while the chosen module is missing: the hint, no card (en and de)', async ({page, baseURL}) => {
    await withHeader(page, baseURL, 'empty', '0000000A03');
    await page.goto('/radio');
    const hint = page.locator('[data-notice="header-silent"]');
    await expect(hint).toHaveText('No radio module answered on the GPIO header (/dev/raw-uart2: no answer within 6s), and the module chosen under Connections (0000000A03) is missing. Check that the module is seated, or choose another one.');
    await expect(page.locator('[data-module-unused]')).toHaveCount(0);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(hint).toHaveText('An der GPIO-Leiste hat kein Funkmodul geantwortet (/dev/raw-uart2: no answer within 6s), und das unter Verbindungen gewählte Modul (0000000A03) fehlt. Prüfen Sie, ob das Modul richtig sitzt, oder wählen Sie ein anderes.');
    await expect(page.locator('[data-module-unused]')).toHaveCount(0);
});

// openccu-lite task 318 (D-120): on Automatic HmIP-RF is kept on the module that holds the HmIP
// network. That module missing, the stick beside it is offered but not taken: the notice says so,
// the Status page carries the error, and the move is the confirmed change.
test('the module holding the HmIP network is missing: kept, said, and moved only on the word (en and de)', async ({page, baseURL}) => {
    const held = '3014F711A0001F0000000A03';
    await ownConn(page, baseURL);
    await page.context().addCookies([{name: 'stub-conn-held', value: held, url: baseURL!}]);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/connections')) puts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    const notice = page.locator('[data-notice="missing-hmip"]');
    await expect(notice).toHaveAttribute('data-pinned', '');
    await expect(notice).toHaveText(`The module that holds the HmIP network (${held}) is missing. hmipserver cannot start without it, so HmIP-RF and the virtual devices are down until it is back; no other module is taken without asking, since the network would have to move. To move HmIP-RF to another module, choose it for HmIP-RF below and confirm the change.`);
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toHaveText(`Automatic: the module ${held}, which holds the HmIP network, is missing: hmipserver cannot start`);
    // the stick is offered; choosing it is a move from the held module, confirmed in red
    await page.getByLabel('Module for HmIP-RF').selectOption('0000000A02|multimacd');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText(`HmIP-RF moves from module ${held} to module 3014F711A000040000000A02.`);
    await expect(dialog.getByRole('button', {name: 'Change'})).toHaveClass(/danger/);
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(puts).toEqual([]);
    // the Status page: the error, the card
    await page.goto('/');
    await expect(page.locator('[data-warnings] [data-notice="hmip-module-missing"]')).toContainText(`HmIP-RF is down: the radio module ${held}, which holds the HmIP network, is missing or did not answer.`);
    await expect(page.locator('[data-component="HmIP-RF-module"] .ol-card-body')).toHaveText('module missing');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/radio');
    await expect(notice).toContainText(`Das Modul, das das HmIP-Netzwerk hält (${held}), fehlt.`);
    await page.goto('/');
    await expect(page.locator('[data-component="HmIP-RF-module"] .ol-card-body')).toHaveText('Modul fehlt');
});

// openccu-lite B-302 step 1: the way back to the previous module says the expected wait of the
// HmIP security counter before the user confirms, and after the way back a notice says until when
async function withMoveBack(page: Page, baseURL: string | undefined, cookies: Record<string, string>) {
    await ownConn(page, baseURL);
    await page.context().addCookies(Object.entries(cookies).map(([name, value]) => ({name, value, url: baseURL!})));
}

test('the way back with a counter gap: the dialog says the wait before the confirmation', async ({page, baseURL}) => {
    await withMoveBack(page, baseURL, {'stub-conn-moveback': 'gap'});
    const posts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().endsWith('/module-move/back')) posts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    await page.locator('[data-notice="hmip-move-back"]').getByRole('button', {name: 'Back to the previous module…'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('The HmIP security counter of module 3014F5AC9400040000000A09 is 245,247 behind what module 3014F711A000040000000A02 has sent.');
    await expect(dialog).toContainText("devices that heard module 3014F711A000040000000A02 may ignore this system's commands (switching, configuration) until about");
    await expect(dialog).toContainText('about 20 hours; their own reports still arrive.');
    await expect(dialog).toContainText('Nothing is written to the identity files for this. To avoid the wait, stay on the module in use.');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(posts).toEqual([]);
    // confirmed: the request as before
    await page.locator('[data-notice="hmip-move-back"]').getByRole('button', {name: 'Back to the previous module…'}).click();
    await dialog.getByLabel('Host name').fill('openccu');
    await dialog.getByRole('button', {name: 'Back to the previous module'}).click();
    await expect.poll(() => posts.map((p) => JSON.parse(p))).toEqual([{confirm: true, hostname: 'openccu'}]);
});

test('the way back without a gap says nothing of the counter', async ({page, baseURL}) => {
    await withMoveBack(page, baseURL, {'stub-conn-moveback': 'plain'});
    await page.goto('/radio');
    await page.locator('[data-notice="hmip-move-back"]').getByRole('button', {name: 'Back to the previous module…'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('HmIP-RF goes back to module 3014F5AC9400040000000A09.');
    await expect(dialog).not.toContainText('security counter');
});

test('after the way back: the notice until the counter has caught up (en and de)', async ({page, baseURL}) => {
    await withMoveBack(page, baseURL, {'stub-conn-gap': '1'});
    await page.goto('/radio');
    const notice = page.locator('[data-notice="hmip-counter-gap"]');
    await expect(notice).toContainText('HmIP-RF is back on module 3014F5AC9400040000000A09, whose HmIP security counter is 245,247 behind what module 3014F711A000040000000A02 sent.');
    await expect(notice).toContainText('the counter catches up at the first start of HmIP-RF after that time, and this notice goes then.');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(notice).toContainText('HmIP-RF ist wieder auf Modul 3014F5AC9400040000000A09, dessen HmIP-Sicherheitszähler');
});

// openccu-lite B-301 (maintainer: the refusal stays): a snapshot of the module in use from before
// local key mode was switched off blocks the move's own snapshot - the page says so before the
// attempt and offers its discard in red; after the discard the change is asked as usual
test('a kept local-key snapshot blocks the move: said, discarded on the word, then the move (en and de)', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    await page.context().addCookies([{name: 'stub-conn-board', value: 'detected', url: baseURL!}, {name: 'stub-conn-lksnap', value: '1', url: baseURL!}]);
    const calls: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/connections')) calls.push('PUT ' + (r.postData() ?? ''));
        if (r.method() === 'DELETE' && r.url().includes('/local-key/snapshots/')) calls.push('DELETE ' + decodeURIComponent(r.url().split('/').pop()!));
    });
    await page.goto('/radio');
    await page.getByLabel('Module for HmIP-RF').selectOption('MEQ9000005|multimacd');
    await page.getByLabel('Module for BidCos-RF').selectOption('MEQ9000005');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('A kept snapshot blocks the move');
    await expect(dialog).toContainText('A snapshot of module 3014F711A000040000000A02 from before local key mode was switched off is still kept.');
    await expect(dialog).toContainText('The identity in use is not touched.');
    await expect(dialog.getByRole('button', {name: 'Discard the snapshot'})).toHaveClass(/danger/);
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toBeFocused();
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    expect(calls).toEqual([]);
    // discarded: the change is asked as usual, and goes out confirmed
    await page.getByRole('button', {name: 'Apply changes'}).click();
    await dialog.getByRole('button', {name: 'Discard the snapshot'}).click();
    await expect(dialog).toContainText('HmIP-RF moves from module 3014F711A000040000000A02 to module 3014F711A061A70000000A05.');
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toBeHidden();
    expect(calls.map((c) => c.split(' ')[0])).toEqual(['DELETE', 'PUT']);
    expect(calls[0]).toBe('DELETE 3014F711A000040000000A02');
    expect(JSON.parse(calls[1]!.slice(4))).toMatchObject({hmip: 'MEQ9000005', confirm: true});
});

test('the blocked move in German', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    await page.context().addCookies([{name: 'stub-conn-board', value: 'detected', url: baseURL!}, {name: 'stub-conn-lksnap', value: '1', url: baseURL!}]);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/radio');
    await page.getByLabel('Modul für HmIP-RF').selectOption('MEQ9000005|multimacd');
    await page.getByLabel('Modul für BidCos-RF').selectOption('MEQ9000005');
    await page.getByRole('button', {name: 'Änderungen übernehmen'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Eine aufbewahrte Sicherung verhindert den Wechsel');
    await expect(dialog.getByRole('button', {name: 'Sicherung verwerfen'})).toBeVisible();
});

// occulited task 147: HmIP none - HMServer.jar with the virtual devices alone, offered with every
// hardware, confirmed in red while HmIP-RF runs on a module; and HMIPServer.jar without a module is
// said, with the way out, never replaced by HMServer.jar
test('HmIP none: the dialog says HmIP goes off, then hmipserver runs HMServer.jar', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/connections')) puts.push(r.postData() ?? '');
    });
    await page.goto('/radio');
    const hmip = page.locator('[data-process="hmipserver"]');
    await expect(hmip.locator('.conn-hmip-none')).toContainText('None: no HmIP at all. hmipserver runs HMServer.jar');
    const before = (await hmip.boundingBox())!;
    await page.getByLabel('Module for HmIP-RF').selectOption('none|');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('HmIP-RF: None (virtual devices only)');
    await expect(dialog).toContainText('hmipserver: HmIP off: HMServer.jar, virtual devices only');
    await expect(dialog).toContainText('rfd: HMIP-RFUSB 0000000A02 through multimacd + LAN gateways');
    await expect(dialog).toContainText('multimacd: runs on /dev/raw-uart');
    await expect(dialog).toContainText('HmIP is switched off: hmipserver runs HMServer.jar with the virtual devices only');
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toBeFocused();
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toBeHidden();
    expect(puts).toHaveLength(1);
    expect(JSON.parse(puts[0]!)).toEqual({hmip: 'none', hmip_path: '', bidcos: '', confirm: true});
    await expect(hmip.locator('.conn-now')).toHaveText('Chosen: HmIP off: HMServer.jar, virtual devices only');
    await expect(hmip.locator('.conn-can')).toHaveCount(0);
    await expect(page.getByLabel('Module for HmIP-RF')).toHaveValue('none|');
    await expect(page.locator('[data-notice="no-hmip-module"]')).toHaveCount(0);
    await expect(page.locator('[data-process="multimacd"] .conn-mmd-uses')).toHaveText('connected to /dev/raw-uart · HMIP-RFUSB 0000000A02');
    await expect(page.locator('[data-process="multimacd"] .conn-mmd-provides dd')).toHaveText(['↑ rfd (BidCos-RF)', 'not used']);
    // the panel keeps its width across the change
    const after = (await hmip.boundingBox())!;
    expect(Math.abs(after.width - before.width)).toBeLessThan(1);
    // back to Automatic: HmIP on its module again, nothing to confirm
    await page.getByLabel('Module for HmIP-RF').selectOption('|');
    await page.getByRole('button', {name: 'Apply changes'}).click();
    await expect(dialog).not.toContainText('HmIP is switched off');
    await dialog.getByRole('button', {name: 'Change'}).click();
    await expect(dialog).toBeHidden();
    expect(JSON.parse(puts[1]!)).toEqual({hmip: '', hmip_path: '', bidcos: '', confirm: false});
    await expect(hmip.locator('.conn-now')).toHaveText('Automatic: HMIP-RFUSB 0000000A02, shared with BidCos-RF through multimacd');
});

test('no HmIP module on Automatic: hmipserver cannot start, and the page names the way out', async ({page, baseURL}) => {
    await ownConn(page, baseURL);
    await page.context().addCookies([{name: 'stub-conn-nohmip', value: '1', url: baseURL!}]);
    await page.goto('/radio');
    await expect(page.locator('[data-notice="no-hmip-module"]')).toHaveText('No HmIP radio module was found. hmipserver (HMIPServer.jar) cannot start without one, so HmIP-RF and the virtual devices are down. Connect the module, or set HmIP-RF to None below: hmipserver then runs HMServer.jar with the virtual devices only.');
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toHaveText('Automatic: no HmIP module: hmipserver cannot start');
    // the dropdown is there without an HmIP module, for None
    await expect(page.getByLabel('Module for HmIP-RF').locator('option')).toHaveText(['Automatic', 'None (virtual devices only)']);
    // the fatal marker's generic notice stays away: the notice above says it
    await expect(page.locator('[data-notice="hmip-fatal"]')).toHaveCount(0);
});
