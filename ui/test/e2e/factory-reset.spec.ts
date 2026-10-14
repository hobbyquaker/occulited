import {expect, test, type Page} from '@playwright/test';

// The factory reset section of the Backup page (task 109, D-106): the warnings about what a wiped
// userfs cannot undo (BidCos-RF devices under an individual key, HmIP devices, the local network
// key, rfd or hmipserver not answering), the typed host name in the shell's dialog, the request
// with the name, the countdown; a set marker with its cancel; the refusal while an update is staged.

const BASE = {hostname: 'lab-ccu', armed: false, container: '', update_staged: false, interfaces: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 0, known: true}}, security_key_set: false, security_key_known: true, hmip_local_key: false};

async function withView(page: Page, view: Record<string, unknown>) {
    await page.route('**/api/system/v1/factory-reset', (r) => (r.request().method() === 'GET' ? r.fulfill({json: {...BASE, ...view}}) : r.fallback()));
}

const section = (page: Page) => page.locator('[data-section="factory-reset"]');

test('the section sits on the Backup page: what is erased, the backup first, the button; quiet when nothing is paired', async ({page}) => {
    await page.goto('/system/backup');
    const s = section(page);
    await expect(page.locator('h2#factory-reset')).toHaveText(/Factory reset/);
    await expect(s.locator('p')).toContainText('Download a backup first');
    await expect(s.locator('[data-action="factory-reset-backup"]')).toBeEnabled();
    await expect(s.locator('[data-action="factory-reset"]')).toBeEnabled();
    await expect(s.locator('[data-notice]')).toHaveCount(0);
});

for (const c of [
    {name: 'BidCos-RF devices under an individual key: the red warning', view: {interfaces: {'BidCos-RF': {devices: 2, known: true}, 'HmIP-RF': {devices: 0, known: true}}, security_key_set: true}, notice: 'factory-reset-bidcos-key', severity: 'error', text: '2 BidCos-RF devices are still paired, and this system uses an individual BidCos security key. After the reset the devices still use that key: without it they cannot be paired again. They should be unpaired from this system first!'},
    {name: 'BidCos-RF devices and a key state that could not be checked: the warning, hedged', view: {interfaces: {'BidCos-RF': {devices: 1, known: true}, 'HmIP-RF': {devices: 0, known: true}}, security_key_set: false, security_key_known: false}, notice: 'factory-reset-bidcos-key', severity: 'error', text: 'could not be checked'},
    {name: 'BidCos-RF devices under the default key: the note', view: {interfaces: {'BidCos-RF': {devices: 3, known: true}, 'HmIP-RF': {devices: 0, known: true}}}, notice: 'factory-reset-bidcos', severity: 'warning', text: '3 BidCos-RF devices are still paired. To pair them again'},
    {name: 'rfd down: unknown, said as such', view: {interfaces: {'BidCos-RF': {devices: 0, known: false, error: 'no answer within 10s'}, 'HmIP-RF': {devices: 0, known: true}}, security_key_set: true}, notice: 'factory-reset-bidcos-unknown', severity: 'warning', text: 'rfd did not answer'},
    {name: 'HmIP devices: reset on the device', view: {interfaces: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 4, known: true}}}, notice: 'factory-reset-hmip', severity: 'warning', text: '4 HmIP devices are still paired'},
    {name: 'hmipserver down: unknown', view: {interfaces: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 0, known: false, error: 'connection refused'}}}, notice: 'factory-reset-hmip-unknown', severity: 'warning', text: 'connection refused'},
    {name: 'local key mode: the network key goes with the reset', view: {hmip_local_key: true}, notice: 'factory-reset-local-key', severity: 'warning', text: 'hmip_user.conf'},
]) {
    test(c.name, async ({page}) => {
        await withView(page, c.view);
        await page.goto('/system/backup');
        const n = section(page).locator(`[data-notice="${c.notice}"]`);
        await expect(n).toBeVisible();
        await expect(n).toHaveAttribute('data-severity', c.severity);
        await expect(n).toContainText(c.text);
        await expect(section(page).locator('[data-notice]')).toHaveCount(1);
        // the key itself is nowhere on the page
        await expect(page.locator('body')).not.toContainText(/[0-9A-F]{32}/);
    });
}

test('the question takes the host name: a wrong one changes nothing, the right one is sent with the request and the countdown runs', async ({page}) => {
    await withView(page, {interfaces: {'BidCos-RF': {devices: 2, known: true}, 'HmIP-RF': {devices: 1, known: true}}, security_key_set: true});
    await page.route('**/api/system/v1/boot-expect**', (r) => r.fulfill({json: {kind: 'restore', product: 'ova', expect: {down: 5, http: 20, ui: 5, ready: 10}, default: {down: 5, http: 20, ui: 5, ready: 10}, measured: {down: 0, http: 0, ui: 0, ready: 0}}}));
    const posts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/system/v1/factory-reset') posts.push(r.postData() ?? '');
    });
    await page.goto('/system/backup');
    await section(page).locator('[data-action="factory-reset"]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Type the host name lab-ccu to confirm.');
    const confirm = dialog.getByRole('button', {name: 'Erase and reboot'});
    await dialog.locator('input').fill('lab-ccu2');
    await confirm.click();
    await expect(dialog).toBeHidden();
    await expect(section(page).locator('[role="alert"]')).toContainText("not this system's host name; nothing happened");
    expect(posts).toEqual([]);
    // the right name, in another case: the request carries it, the countdown replaces the buttons
    await section(page).locator('[data-action="factory-reset"]').click();
    await dialog.locator('input').fill(' LAB-CCU ');
    await dialog.getByRole('button', {name: 'Erase and reboot'}).click();
    await expect(section(page).locator('[role="status"]')).toContainText('Factory reset - the system reboots now.');
    await expect(section(page).locator('[data-action="factory-reset"]')).toHaveCount(0);
    expect(posts).toHaveLength(1);
    expect(JSON.parse(posts[0])).toEqual({confirm: true, hostname: 'LAB-CCU'}); // the dialog trims
});

test('a refusal keeps the page: the message is shown and nothing counts down', async ({page}) => {
    await page.route('**/api/system/v1/factory-reset', (r) => (r.request().method() === 'POST' ? r.fulfill({status: 409, json: {error: 'update-staged', message: 'a system update is staged and the recovery system would install it: install or discard it first'}}) : r.fallback()));
    await page.goto('/system/backup');
    await section(page).locator('[data-action="factory-reset"]').click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('input').fill('ccu-vm-1');
    await dialog.getByRole('button', {name: 'Erase and reboot'}).click();
    await expect(section(page).locator('[role="alert"]')).toContainText('install or discard it first');
    await expect(section(page).locator('[role="status"]')).toHaveCount(0);
    await expect(section(page).locator('[data-action="factory-reset"]')).toBeVisible();
});

test('a staged update: the button is off and the notice leads to the Updates page', async ({page}) => {
    await withView(page, {update_staged: true});
    await page.goto('/system/backup');
    const n = section(page).locator('[data-notice="factory-reset-update-staged"]');
    await expect(n).toContainText('install or discard it first');
    await expect(n.locator('a.hmm-button')).toHaveAttribute('href', /\/system\/updates$/);
    await expect(section(page).locator('[data-action="factory-reset"]')).toBeDisabled();
});

test('a marker set already: the notice, its cancel sends the DELETE, and the button is off meanwhile', async ({page}) => {
    let armed = true;
    await page.route('**/api/system/v1/factory-reset', (r) => {
        if (r.request().method() === 'DELETE') {
            armed = false;
            return r.fulfill({json: {ok: true, armed: false}});
        }
        return r.request().method() === 'GET' ? r.fulfill({json: {...BASE, armed}}) : r.fallback();
    });
    await page.goto('/system/backup');
    const n = section(page).locator('[data-notice="factory-reset-armed"]');
    await expect(n).toContainText('set for the next boot');
    await expect(section(page).locator('[data-action="factory-reset"]')).toBeDisabled();
    await n.locator('[data-action="factory-reset-cancel"]').click();
    await expect(n).toHaveCount(0);
    await expect(section(page).locator('[data-action="factory-reset"]')).toBeEnabled();
});

test('the view that cannot be read is said as such, and the button stays', async ({page}) => {
    await page.route('**/api/system/v1/factory-reset', (r) => (r.request().method() === 'GET' ? r.fulfill({status: 500, json: {error: 'internal', message: 'boom'}}) : r.fallback()));
    await page.goto('/system/backup');
    await expect(section(page).locator('[data-notice="factory-reset-unknown"]')).toContainText('could not be checked: boom');
});

test('German', async ({page}) => {
    await withView(page, {interfaces: {'BidCos-RF': {devices: 2, known: true}, 'HmIP-RF': {devices: 0, known: true}}, security_key_set: true});
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    await expect(page.locator('h2#factory-reset')).toContainText('Werkseinstellungen');
    // occulited task 36: the maintainer's wording, the whole notice
    await expect(section(page).locator('[data-notice="factory-reset-bidcos-key"]')).toHaveText('2 BidCos-RF-Geräte sind noch angelernt, und dieses System verwendet einen individuellen BidCos-Sicherheitsschlüssel. Nach dem Zurücksetzen verwenden die Geräte weiterhin diesen Schlüssel: Ohne ihn lassen sie sich nicht wieder anlernen. Sie sollten vorher von diesem System abgelernt werden!');
    await expect(section(page).locator('[data-action="factory-reset"]')).toHaveText('Auf Werkseinstellungen zurücksetzen…');
});
