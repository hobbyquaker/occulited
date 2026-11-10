import {expect, test, type Page} from '@playwright/test';
import {report, THIS} from './backupreport';

// openccu-lite task 296 (the maintainer: "restoring a backup that has a non-default bidcos aes-key
// should ask the user for the key, just to check if user has it ... it should not prevent
// restoring, just warn very clear what consequences are when not knowing the aes key"): the
// restore and the device import ask for the backup's passphrase as a check - match, mismatch,
// skip - and never block; going on without a match asks a danger question with the warning, and
// the result repeats it.

const PASS = 'Right_Pass296';
const CHECK = {ok: true, output: '1) Checking sbk backup file consistency:\n   backup and/or system protected by security key...\n   backup protected with a key, the passphrase is asked for below.', backup_version: '3.89.11', running_version: '3.89.11.20260919', needs_key: true, has_rega: false, backup_key: true, system_key: false, key_index: 2};
const EXPECT = {kind: 'restore', product: 'ova', expect: {down: 5, http: 20, ui: 5, ready: 10}, default: {down: 5, http: 20, ui: 5, ready: 10}, measured: {down: 0, http: 0, ui: 0, ready: 0}};
const BACKUP = {
    bidcos_rf: {devices: 1, address: '0xFF1234', serial: '1709ADFA00', has_key: true, gateways: 0},
    hmip: {devices: 0, local_key: false, device_key_map: false},
    bidcos_wired: {devices: 0, gateways: 0},
    key_index: 2,
    files: ['ids', 'keys', 'rfd/JEQ9000001.dev'],
};
const FREE = {paired: {'BidCos-RF': {devices: 0, known: true}}, devices: 0, unknown: [], user_key: false, importable: true};

interface Rig { verifies: {file: string; key: string}[]; applies: {file: string; key: string; force: boolean}[]; imports: {file: string; key: string; replace_key: boolean}[] }

// the page with the check's answer, a verify-key that knows PASS (this system has systemKey), and
// the apply and import that answer like the daemon
async function rig(page: Page, opts: {check?: object; systemKey?: string; devices?: boolean} = {}): Promise<Rig> {
    const r: Rig = {verifies: [], applies: [], imports: []};
    await page.route('**/api/system/v1/restore/check', (x) => x.fulfill({json: {file: 'restore-ccu.sbk', check: opts.check ?? CHECK, encryption: {encrypted: false}, backup: report(opts.devices ? 'ccu' : 'openccu-lite', BACKUP), system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (x) => opts.devices
        ? x.fulfill({json: {backup: BACKUP, target: FREE, module_changed: false, non_default_key: true, kind: 'ccu', groups: [], left_behind: []}})
        : x.fulfill({status: 422, json: {error: 'not_a_backup', message: 'no'}}));
    const verdict = (key: string) => ({
        backup: key === '' ? 'skipped' : key === PASS ? 'match' : 'mismatch',
        system: !opts.systemKey ? 'none' : key === '' ? 'skipped' : key === opts.systemKey ? 'match' : 'mismatch',
        key_index: 2,
    });
    await page.route('**/api/system/v1/restore/verify-key', (x) => {
        const b = JSON.parse(x.request().postData() ?? '{}');
        r.verifies.push(b);
        return x.fulfill({json: verdict(b.key)});
    });
    await page.route('**/api/system/v1/boot-expect**', (x) => x.fulfill({json: EXPECT}));
    await page.route('**/api/system/v1/restore/apply', (x) => {
        const b = JSON.parse(x.request().postData() ?? '{}');
        r.applies.push(b);
        return x.fulfill({json: {ok: true, output: '4) Scheduling backup restore for next boot cycle, OK', rebooting: true, key_check: verdict(b.key)}});
    });
    await page.route('**/api/system/v1/restore/import-devices', (x) => {
        const b = JSON.parse(x.request().postData() ?? '{}');
        r.imports.push(b);
        return x.fulfill({json: {ok: true, imported: {backup: BACKUP, written: ['ids', 'keys'], aside: '/etc/config/.import-devices-aside/x', non_default_key: true, target_key_replaced: false}, key_check: verdict(b.key).backup, rebooting: true}});
    });
    return r;
}

async function upload(page: Page, check = 'Check') {
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: check, exact: true}).click();
}

const panel = (page: Page) => page.locator('[data-restore="key-check"]');

test('restore: the passphrase matches - no warning, the key goes along, no force', async ({page}) => {
    const r = await rig(page);
    await upload(page);
    const p = panel(page);
    await expect(p).toContainText('The backup\'s BidCos security key');
    await expect(p).toContainText('own BidCos security key (key index 2)');
    await expect(p).toContainText('It is only compared, never stored.');
    // the old field and the force box are gone for a backup with its own key
    await expect(page.locator('[data-input="restore-key"]')).toHaveCount(0);
    await expect(page.locator('[data-input="restore-force"]')).toHaveCount(0);
    await expect(p.locator('[data-action="keycheck-check"]')).toBeDisabled();
    await p.locator('[data-input="keycheck-pass"]').fill(PASS);
    await p.locator('[data-action="keycheck-check"]').click();
    await expect(p.locator('[data-keycheck-verdict="match"]')).toContainText('The passphrase matches the backup\'s security key.');
    expect(r.verifies).toEqual([{file: 'restore-ccu.sbk', key: PASS}]);
    await page.locator('[data-action="restore-apply"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Restore this backup and reboot?');
    await expect(dlg).not.toContainText('passphrase');
    await dlg.getByRole('button', {name: 'Restore and reboot'}).click();
    await expect.poll(() => r.applies.length).toBe(1);
    expect(r.applies[0]).toEqual({file: 'restore-ccu.sbk', key: PASS, force: false, confirm: true});
    await expect(page.locator('.ol-restorenotice')).toContainText('the system reboots now');
    await expect(page.locator('[data-notice="restore-key-warning"]')).toHaveCount(0);
});

test('restore: a wrong passphrase - the verdict, the danger question with the warning, cancel posts nothing, then the restore on the word with force', async ({page}) => {
    const r = await rig(page);
    await upload(page);
    const p = panel(page);
    await p.locator('[data-input="keycheck-pass"]').fill('Wrong_Pass296');
    await p.locator('[data-action="keycheck-check"]').click();
    await expect(p.locator('[data-keycheck-verdict="mismatch"]')).toContainText('does not match the backup\'s security key. You can still go on');
    // typing again clears the verdict
    await p.locator('[data-input="keycheck-pass"]').fill('Wrong_Pass2966');
    await expect(p.locator('[data-keycheck-verdict]')).toHaveCount(0);
    await p.locator('[data-input="keycheck-pass"]').fill('Wrong_Pass296');
    await p.locator('[data-action="keycheck-check"]').click();
    await expect(p.locator('[data-keycheck-verdict="mismatch"]')).toBeVisible();
    // the restore button stays usable: never blocked
    const apply = page.locator('[data-action="restore-apply"]');
    await expect(apply).toBeEnabled();
    await apply.click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Restore without the confirmed passphrase?');
    await expect(dlg).toContainText('The passphrase you entered does not match the backup\'s BidCos security key.');
    await expect(dlg).toContainText('The restore itself re-keys no device');
    await expect(dlg).toContainText('to change the system security key; to re-key the devices');
    await expect(dlg).toContainText('to pair these devices with another central');
    await expect(dlg).toContainText('to restore onto a system that has a different key');
    await expect(dlg).toContainText('the only way back is a factory reset of every such device and pairing it again');
    await expect(dlg).toContainText('Find the passphrase before you go on');
    await expect(dlg).toContainText('Restore this backup and reboot?');
    const go = dlg.getByRole('button', {name: 'Restore anyway'});
    await expect(go).toHaveClass(/danger/);
    await dlg.getByRole('button', {name: 'Cancel'}).click();
    expect(r.applies).toHaveLength(0);
    await apply.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Restore anyway'}).click();
    await expect.poll(() => r.applies.length).toBe(1);
    expect(r.applies[0]).toEqual({file: 'restore-ccu.sbk', key: 'Wrong_Pass296', force: true, confirm: true});
    // the result repeats it
    await expect(page.locator('[data-notice="restore-key-warning"]')).toContainText('Restored without a confirmed passphrase for the backup\'s BidCos security key.');
    await expect(page.locator('[data-notice="restore-key-warning"]')).toContainText('only a factory reset of every such device and pairing it again helps');
});

test('restore: skipped - the warning, then the restore on the word with force and no key', async ({page}) => {
    const r = await rig(page);
    await upload(page);
    const p = panel(page);
    await p.locator('[data-input="keycheck-pass"]').fill('half typed');
    await p.locator('[data-action="keycheck-skip"]').click();
    await expect(p.locator('[data-keycheck-verdict="skipped"]')).toContainText('Skipped: the passphrase is not checked.');
    await expect(p.locator('[data-input="keycheck-pass"]')).toHaveValue('');
    expect(r.verifies).toEqual([{file: 'restore-ccu.sbk', key: ''}]);
    await page.locator('[data-action="restore-apply"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('The passphrase of the backup\'s BidCos security key is not confirmed.');
    await dlg.getByRole('button', {name: 'Restore anyway'}).click();
    await expect.poll(() => r.applies.length).toBe(1);
    expect(r.applies[0]).toEqual({file: 'restore-ccu.sbk', key: '', force: true, confirm: true});
});

test('restore: not checked at all - the same warning, force and no key', async ({page}) => {
    const r = await rig(page);
    await upload(page);
    await page.locator('[data-action="restore-apply"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Restore without the confirmed passphrase?');
    await expect(dlg).toContainText('is not confirmed');
    await dlg.getByRole('button', {name: 'Restore anyway'}).click();
    await expect.poll(() => r.applies.length).toBe(1);
    expect(r.applies[0]).toEqual({file: 'restore-ccu.sbk', key: '', force: true, confirm: true});
    expect(r.verifies).toHaveLength(0);
});

test('restore: the backup\'s passphrase matches, this system has another key - said, and the restore forces past it without a warning', async ({page}) => {
    const r = await rig(page, {check: {...CHECK, system_key: true}, systemKey: 'Other_Key296'});
    await upload(page);
    const p = panel(page);
    await expect(p.locator('[data-keycheck-system]')).toContainText('This system has a security key of its own; the restore replaces it with the backup\'s.');
    await p.locator('[data-input="keycheck-pass"]').fill(PASS);
    await p.locator('[data-action="keycheck-check"]').click();
    await expect(p.locator('[data-keycheck-system-verdict="mismatch"]')).toContainText('It is not this system\'s own key, which the restore replaces.');
    await page.locator('[data-action="restore-apply"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).not.toContainText('passphrase');
    await dlg.getByRole('button', {name: 'Restore and reboot'}).click();
    await expect.poll(() => r.applies.length).toBe(1);
    expect(r.applies[0]).toEqual({file: 'restore-ccu.sbk', key: PASS, force: true, confirm: true});
});

test('restore: only this system has a key - the firmware\'s field and force as before, no passphrase check', async ({page}) => {
    await rig(page, {check: {...CHECK, backup_key: false, system_key: true, key_index: 0}});
    await upload(page);
    await expect(panel(page)).toHaveCount(0);
    await expect(page.locator('[data-input="restore-key"]')).toBeVisible();
    await expect(page.locator('[data-input="restore-force"]')).toBeVisible();
    await expect(page.locator('[data-action="restore-apply"]')).toBeDisabled();
    await page.locator('[data-input="restore-force"]').check();
    await expect(page.locator('[data-action="restore-apply"]')).toBeEnabled();
});

test('import: a matching passphrase goes along and the question has no warning; the result keeps the plain hint', async ({page}) => {
    const r = await rig(page, {devices: true});
    await upload(page);
    const k = page.locator('[data-notice="bidcos-key"]');
    await expect(k).toContainText('The import brings it along as it is');
    await k.locator('[data-input="keycheck-pass"]').fill(PASS);
    await k.locator('[data-action="keycheck-check"]').click();
    await expect(k.locator('[data-keycheck-verdict="match"]')).toBeVisible();
    await page.locator('[data-action="import-devices"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Import the paired devices');
    await expect(dlg).not.toContainText('not confirmed');
    await dlg.getByRole('button', {name: 'Import and reboot'}).click();
    await expect.poll(() => r.imports.length).toBe(1);
    expect(r.imports[0]).toEqual({file: 'restore-ccu.sbk', replace_key: false, key: PASS, confirm: true, groups: []});
    await expect(page.locator('.ol-restorenotice pre')).toContainText('Keep the other system\'s passphrase safe');
    await expect(page.locator('[data-notice="restore-key-warning"]')).toHaveCount(0);
});

test('import: skipped - the warning in the danger question, the import on the word, the result repeats it', async ({page}) => {
    const r = await rig(page, {devices: true});
    await upload(page);
    const k = page.locator('[data-notice="bidcos-key"]');
    await k.locator('[data-action="keycheck-skip"]').click();
    await expect(k.locator('[data-keycheck-verdict="skipped"]')).toBeVisible();
    await page.locator('[data-action="import-devices"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Import without the confirmed passphrase?');
    await expect(dlg).toContainText('The import itself re-keys no device');
    await expect(dlg).toContainText('Find the passphrase before you go on');
    await dlg.getByRole('button', {name: 'Import anyway and reboot'}).click();
    await expect.poll(() => r.imports.length).toBe(1);
    expect(r.imports[0]).toEqual({file: 'restore-ccu.sbk', replace_key: false, key: '', confirm: true, groups: []});
    await expect(page.locator('[data-notice="restore-key-warning"]')).toContainText('came along without a confirmed passphrase');
});

test('German: the panel, the verdict and the warning', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    const r = await rig(page);
    await upload(page, 'Prüfen');
    const p = panel(page);
    await expect(p).toContainText('Der BidCos-Sicherheitsschlüssel der Sicherung');
    await expect(p).toContainText('eigenen BidCos-Sicherheitsschlüssel erstellt (Schlüsselindex 2)');
    await expect(p).toContainText('Sie wird nur verglichen, nie gespeichert.');
    await expect(p.locator('[data-action="keycheck-check"]')).toHaveText('Passphrase prüfen');
    await p.locator('[data-input="keycheck-pass"]').fill('Falsch_296');
    await p.locator('[data-action="keycheck-check"]').click();
    await expect(p.locator('[data-keycheck-verdict="mismatch"]')).toContainText('Die Passphrase passt nicht zum Sicherheitsschlüssel der Sicherung.');
    await page.getByRole('button', {name: 'Einspielen und neu starten'}).click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Ohne bestätigte Passphrase wiederherstellen?');
    await expect(dlg).toContainText('Die eingegebene Passphrase passt nicht zum BidCos-Sicherheitsschlüssel der Sicherung.');
    await expect(dlg).toContainText('Die Wiederherstellung selbst schlüsselt kein Gerät um');
    await expect(dlg).toContainText('jedes dieser Geräte auf Werkseinstellungen zurückzusetzen und neu anzulernen');
    await dlg.getByRole('button', {name: 'Trotzdem wiederherstellen'}).click();
    await expect.poll(() => r.applies.length).toBe(1);
    await expect(page.locator('[data-notice="restore-key-warning"]')).toContainText('Ohne bestätigte Passphrase für den BidCos-Sicherheitsschlüssel der Sicherung wiederhergestellt.');
});
