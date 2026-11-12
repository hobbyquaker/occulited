import {expect, test, type Page} from '@playwright/test';

// task 149 (D-103): local key mode - the HmIP network key kept on the box. The section on the
// Interfaces page beside the BidCos security key, the switch with its dialog and its check, the
// pairing override, the way back from the snapshot; the welcome page's step on an empty network.

async function own(page: Page, baseURL: string | undefined, extra: Record<string, string> = {}) {
    const id = `lk-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-lk', value: id, url: baseURL!}, ...Object.entries(extra).map(([name, value]) => ({name, value, url: baseURL!}))]);
}

test('enter the network key, the check, the override, back to eQ-3 and discard the snapshot', async ({page, baseURL}) => {
    await own(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/hmip/local-key')) puts.push(r.postData() ?? '');
    });
    await page.goto('/system/keys');
    const h = page.locator('h2#local-key');
    await expect(h).toContainText('Local key mode');
    await expect(page.locator('[data-state="off"]')).toContainText("swapping the radio module needs eQ-3's key server once. Pairing a device without its key");

    await page.getByRole('button', {name: 'Switch to local key mode'}).first().click();
    const go = page.locator('.lk-form').getByRole('button', {name: 'Switch to local key mode'});
    // nothing is chosen: the switch waits for a choice
    await expect(go).toBeDisabled();
    await page.getByLabel("Enter the network's key").check();
    await page.getByLabel('Network key', {exact: true}).fill('0011');
    await expect(go).toBeDisabled();
    await page.getByLabel('Network key', {exact: true}).fill('0011 2233 4455 6677 8899 aabb ccdd eeff');
    await expect(go).toBeEnabled();
    await go.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Switch to local key mode?');
    await expect(dialog).toContainText("If it is the network's real key, every HmIP device keeps working");
    await expect(dialog).toContainText('HmIP-RF restarts at once');
    await dialog.getByRole('button', {name: 'Switch', exact: true}).click();
    await expect(dialog).toBeHidden();
    // task 317 (D-120): the identity files change - the danger dialog, and confirm in the request
    expect(JSON.parse(puts[0]!)).toEqual({mode: 'known', network_key: '0011 2233 4455 6677 8899 aabb ccdd eeff', backbone_key: '', confirm: true});
    await expect(page.locator('[data-state="on"]')).toContainText('the HmIP network key is kept on this system (entered).');
    await expect(page.locator('[data-state="on"]')).toContainText("Radio module swaps and pairing work without eQ-3's key server; a device is paired with the key from its QR code.");
    await expect(page.locator('[data-check="ok"]')).toHaveText('All 3 HmIP devices answered after the switch.');
    await expect(page.locator('.lk-snapshots li')).toContainText('3014F711A000040000000A02');
    await expect(page.locator('.lk-snapshots li')).toContainText('this module');

    // the key server for one pairing
    await page.getByLabel('Allow the key server for the next pairing').check();
    await expect(page.locator('[data-state="on"]')).toContainText("eQ-3's key server may be asked for the next pairing, for a device whose key is not known here.");
    await expect(page.locator('.lk-override-note')).toContainText('On until the next install mode has ended');
    await page.getByLabel('Allow the key server for the next pairing').uncheck();
    await expect(page.locator('[data-state="on"]')).toContainText("Radio module swaps and pairing work without eQ-3's key server; a device is paired with the key from its QR code.");

    // back to eQ-3's key server from the snapshot - task 317 (D-120): the files in use are copied
    // first, the host name typed; a wrong one changes nothing. The snapshot stays until discarded
    const dels: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'DELETE' && r.url().endsWith('/radio/hmip/local-key')) dels.push(r.postData() ?? '');
    });
    await page.getByRole('button', {name: "Back to eQ-3's key server"}).click();
    await expect(dialog).toContainText('is put back, and the key lines leave hmip_user.conf');
    await expect(dialog).toContainText('The identity files in use now are copied aside on the system first.');
    await expect(dialog).toContainText('Type the host name openccu to confirm.');
    await expect(dialog.getByLabel('Host name')).toBeFocused();
    await dialog.getByLabel('Host name').fill('other');
    await dialog.getByRole('button', {name: 'Go back'}).click();
    await expect(page.locator('.ol-notice, .ol-warn').filter({hasText: "The name typed is not this system's host name; nothing happened."})).toBeVisible();
    expect(dels).toEqual([]);
    await page.getByRole('button', {name: "Back to eQ-3's key server"}).click();
    await dialog.getByLabel('Host name').fill('OpenCCU');
    await dialog.getByRole('button', {name: 'Go back'}).click();
    await expect.poll(() => dels.map((d) => JSON.parse(d))).toEqual([{confirm: true, hostname: 'OpenCCU'}]);
    await expect(page.locator('[data-state="off"]')).toBeVisible();
    await expect(page.locator('.lk-snapshots li')).toHaveCount(1);
    await page.locator('.lk-snapshots li').getByRole('button', {name: 'Discard'}).click();
    await expect(dialog).toContainText('Without it the way back');
    await dialog.getByRole('button', {name: 'Discard'}).click();
    await expect(page.locator('.lk-snapshots li')).toHaveCount(0);
});

test('generate: the dialog warns of the re-pairing and asks for the host name', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/keys');
    await page.getByRole('button', {name: 'Switch to local key mode'}).first().click();
    await page.getByLabel('Generate a new key').check();
    await expect(page.locator('[data-notice="generate-cost"]')).toContainText('has to be taught in again once');
    await page.locator('.lk-form').getByRole('button', {name: 'Switch to local key mode'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Every HmIP device has to be taught in again once');
    // task 317 (D-120): devices are paired - the host name typed
    await expect(dialog).toContainText('Type the host name openccu to confirm.');
    await expect(dialog.getByLabel('Host name')).toBeFocused();
    await dialog.getByLabel('Host name').fill('openccu');
    await dialog.getByRole('button', {name: 'Switch', exact: true}).click();
    await expect(page.locator('[data-state="on"]')).toContainText('(generated on this system)');
});

test('a wrong key: the devices go silent, the page and the Status page say so, with the way back', async ({page, baseURL}) => {
    await own(page, baseURL, {'stub-lk-wrong': '1'});
    await page.goto('/system/keys');
    await page.getByRole('button', {name: 'Switch to local key mode'}).first().click();
    await page.getByLabel("Enter the network's key").check();
    await page.getByLabel('Network key', {exact: true}).fill('00112233445566778899AABBCCDDEEFF');
    await page.locator('.lk-form').getByRole('button', {name: 'Switch to local key mode'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Switch', exact: true}).click();
    const notice = page.locator('[data-notice="local-key-check"]');
    await expect(notice).toContainText("3 of 3 HmIP devices have not answered since the switch to local key mode: the key was probably not the network's.");
    await expect(notice.getByRole('button', {name: "Back to eQ-3's key server"})).toBeVisible();
    await page.goto('/');
    const w = page.locator('[data-warnings] [data-notice="hmip-local-key"]');
    await expect(w).toContainText('3 of 3 HmIP devices have not answered');
    await expect(w.getByRole('link', {name: 'Local key mode'})).toHaveAttribute('href', '/system/keys#local-key');
    // B-291: the way back is on the Keys page (task 183), not on the Interfaces page
    await expect(w).toContainText("The way back to eQ-3's key server is on the Keys page, under Local key mode.");
    await expect(w).not.toContainText('Interfaces');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(w).toContainText('Der Weg zurück zum Schlüsselserver von eQ-3 ist auf der Seite Schlüssel unter „Lokaler Schlüssel".');
    await expect(w).not.toContainText('Schnittstellen');
});

test('the welcome page: a step on an empty HmIP network, an explicit choice before Done', async ({page, baseURL}) => {
    await own(page, baseURL, {'stub-lk-empty': '1'});
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/radio/hmip/local-key')) puts.push(r.postData() ?? '');
    });
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '4 · The HmIP network key'})).toBeVisible();
    await expect(page.getByRole('heading', {name: '5 · Device management'})).toBeVisible();
    const done = page.getByRole('button', {name: 'Done'});
    await expect(done).toBeDisabled();
    await expect(page.getByRole('button', {name: 'More addons in the catalogue'})).toBeDisabled();
    await expect(page.getByLabel('Generate a local key now')).not.toBeChecked();
    await expect(page.getByLabel("Keep eQ-3's key server")).not.toBeChecked();
    // B-290: local key mode is on the Keys page (task 183), not on the Interfaces page
    const later = page.locator('[data-welcome-lk-later]');
    await expect(later).toContainText('Either way this can be changed later on the Keys page, under Local key mode.');
    await expect(later).not.toContainText('Interfaces');
    await expect(later.getByRole('link', {name: 'Keys'})).toHaveAttribute('href', '/system/keys#local-key');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(later).toContainText('So oder so lässt sich das später auf der Seite Schlüssel unter „Lokaler Schlüssel" ändern.');
    await expect(later.getByRole('link', {name: 'Schlüssel'})).toHaveAttribute('href', '/system/keys#local-key');
    // task 305: "Netzwerkschlüssel", never "Netzschlüssel"
    await expect(page.getByRole('heading', {name: '4 · Der HmIP-Netzwerkschlüssel'})).toBeVisible();
    await expect(page.locator('main, body').first()).not.toContainText('Netzschlüssel');
    await page.evaluate(() => localStorage.setItem('ol.language', 'en'));
    await page.addInitScript(() => localStorage.setItem('ol.language', 'en'));
    await page.reload();
    await expect(later).toContainText('on the Keys page');
    await page.getByLabel('Generate a local key now').check();
    await expect(page.locator('[data-notice="lk-backup"]')).toContainText('protecting the backups becomes crucial');
    await expect(page.locator('[data-notice="lk-backup"]').getByRole('link', {name: 'Backup'})).toHaveAttribute('href', '/system/backup');
    await expect(done).toBeEnabled();
    // task 317 (D-120): the identity files are written under the new key - asked in red first;
    // Cancel stays on the page and sends nothing
    await done.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Generate a local key now?');
    await expect(dialog).toContainText('HmIP-RF writes its identity files under it and restarts at once.');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(page).toHaveURL(/\/welcome$/);
    expect(puts).toEqual([]);
    await done.click();
    await dialog.getByRole('button', {name: 'Generate', exact: true}).click();
    await expect(page).toHaveURL(/\/$/);
    expect(puts.map((p) => JSON.parse(p))).toEqual([{mode: 'generate', confirm: true}]);
});

test('the welcome page without the step: HmIP devices are paired already', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '4 · Device management'})).toBeVisible();
    await expect(page.getByRole('heading', {name: /The HmIP network key/})).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Done'})).toBeEnabled();
});

test('a user sees no local key section and does not ask for it', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.route('**/api/system/v1/radio/firmware', (r) => r.fulfill({status: 403, json: {error: 'forbidden', message: 'administrator role required'}}));
    const calls: string[] = [];
    page.on('request', (r) => calls.push(new URL(r.url()).pathname));
    await page.goto('/system/keys');
    await expect(page.locator('[data-note="hmip-keys-admin"]')).toBeVisible();
    await expect(page.locator('h2#local-key')).toHaveCount(0);
    expect(calls).not.toContain('/api/system/v1/radio/hmip/local-key');
});

// openccu-lite task 212: a fresh-start snapshot goes back without local key mode, once its module
// is in use again - the fresh start's confirmation, the host name typed
test('a fresh-start snapshot of the module in use: Restore with the host name typed; another module\'s waits', async ({page, baseURL}) => {
    await own(page, baseURL, {'stub-lk-fresh': '1'});
    const posts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().includes('/snapshots/')) posts.push(r.url().split('/api')[1] + ' ' + (r.postData() ?? ''));
    });
    await page.goto('/system/keys');
    const li = page.locator('.lk-snapshots li');
    await expect(li).toHaveCount(1);
    await expect(li).toContainText('3014F711A000040000000A02');
    await expect(li).toContainText('this module');
    await expect(li).toContainText('the previous module, moved aside by the fresh start');
    await li.getByRole('button', {name: 'Restore…'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Restore the identity of 3014F711A000040000000A02?');
    await expect(dialog).toContainText('the network and the devices paired under it are known again');
    await expect(dialog).toContainText('Devices paired since the fresh start have to be paired again.');
    await expect(dialog).toContainText('Type the host name openccu to confirm.');
    // a wrong name: nothing happens
    await dialog.getByLabel('Host name').fill('other');
    await dialog.getByRole('button', {name: 'Restore', exact: true}).click();
    await expect(dialog).toBeHidden();
    await expect(page.locator('.ol-warn')).toContainText('The name typed is not this system\'s host name; nothing happened.');
    expect(posts).toEqual([]);
    await expect(li).toHaveCount(1);
    // the right one: the restore posts, the snapshot is consumed, the restart notice shows
    await li.getByRole('button', {name: 'Restore…'}).click();
    await page.getByRole('dialog').getByLabel('Host name').fill(' OpenCCU ');
    await page.getByRole('dialog').getByRole('button', {name: 'Restore', exact: true}).click();
    await expect.poll(() => posts.length).toBe(1);
    expect(posts[0]).toBe('/system/v1/radio/hmip/local-key/snapshots/3014F711A000040000000A02/restore {"confirm":true,"hostname":"OpenCCU"}');
    await expect(page.locator('.ol-notice')).toContainText('HmIP-RF is restarting with the restored identity.');
    await expect(li).toHaveCount(0);
});

test('a fresh-start snapshot of another module: no Restore, the hint; in German', async ({page, baseURL}) => {
    await own(page, baseURL, {'stub-lk-fresh': 'other'});
    await page.goto('/system/keys');
    const li = page.locator('.lk-snapshots li');
    await expect(li).toContainText('3014F711A0001F0000000A04');
    await expect(li.getByRole('button', {name: 'Restore…'})).toHaveCount(0);
    await expect(li).toContainText('restorable once this module is in use again');
    await expect(li.getByRole('button', {name: 'Discard'})).toBeVisible();
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(page.locator('.lk-snapshots li')).toContainText('wiederherstellbar, sobald dieses Modul wieder in Betrieb ist');
});
