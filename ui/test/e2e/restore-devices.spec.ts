import {expect, test, type Page} from '@playwright/test';
import {report, THIS} from './backupreport';

// openccu-lite task 251 (the maintainer: "import paired devices that fetches everything needed
// that devices pairings (hmip, bidcos-rf and -wired) and possibly keys that are present are
// imported"): on the Backup page, after a checked CCU backup, what it holds of the three radios and
// the import onto a system without paired devices - refused with the reason when devices are
// paired here or an interface did not answer, the danger question, then the POST and the reboot
// notice. Task 275: the notice that HmIP re-keys when the backup's identity belongs to another
// module; task 278 (option B): the notice that a non-default BidCos key comes along, and the word to
// replace this system's own key store; task 296: its passphrase as a check (restore-key.spec.ts has
// the check itself) - not checked, the import still runs after the warning.

const CHECK = {ok: true, output: '1) Checking sbk backup file consistency:\n   generated on 3.89.11.20260919, applying to 3.89.11.20260919, OK\n   backup or system NOT protected by security key, OK', backup_version: '3.89.11.20260919', running_version: '3.89.11.20260919', needs_key: false, has_rega: true};
const BACKUP = {
    bidcos_rf: {devices: 2, address: '0xFF1234', serial: '1709ADFA00', has_key: false, gateways: 1},
    hmip: {devices: 3, identity_sgtin: '3014F711A0001F0000000A03', local_key: true, device_key_map: true},
    bidcos_wired: {devices: 1, gateways: 0},
    key_index: 0,
    files: ['ids', 'rfd/JEQ9000001.dev'],
    version: '3.89.11.20260919',
};
const MODULE = {hardware: 'RPI-RF-MOD', serial: '0000000A01', sgtin: '3014F711A000040000000A01'};
const FREE = {paired: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 0, known: true}}, devices: 0, unknown: [], user_key: false, importable: true, hmip_module: MODULE, bidcos_module: {hardware: 'RPI-RF-MOD', serial: '0000000A01'}};

async function upload(page: Page) {
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: 'Check', exact: true}).click();
}

test('the backup\'s devices, a free system, the import asks in red and posts the file, then the reboot notice', async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}, backup: report('ccu', BACKUP), system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup: BACKUP, target: FREE, module_changed: true, non_default_key: false}}));
    const posts: unknown[] = [];
    await page.route('**/api/system/v1/restore/import-devices', (r) => {
        posts.push(JSON.parse(r.request().postData() ?? '{}'));
        return r.fulfill({json: {ok: true, imported: {backup: BACKUP, written: ['ids'], aside: '/etc/config/.import-devices-aside/x', non_default_key: false, target_key_replaced: false}, names: {ok: true, objects: 109, rooms: 11, functions: 10, changed: true}, rebooting: true}});
    });
    await page.goto('/system/backup');
    await upload(page);
    const block = page.locator('[data-restore="devices"]');
    await expect(block.getByRole('heading', {level: 3, name: 'Import into this system'})).toBeVisible();
    await expect(block.locator('[data-file="kind"]')).toContainText('A backup of OpenCCU');
    await expect(block.locator('[data-devices="bidcos-rf"]')).toHaveText('2 devices · address 0xFF1234 · 1 LAN gateways');
    await expect(block.locator('[data-devices="hmip"]')).toHaveText('3 devices · identity of module 3014F711A0001F0000000A03 · local key mode · device key map');
    await expect(block.locator('[data-devices="wired"]')).toHaveText('1 devices');
    await expect(block.locator('[data-devices-target="free"]')).toContainText('This system has no paired devices');
    await expect(block.locator('[data-input="devices-key"]')).toHaveCount(0);
    // task 275: the identity belongs to another module - the notice before the import
    const mc = block.locator('[data-notice="module-change"]');
    await expect(mc).toContainText('Another radio module: HmIP re-keys.');
    await expect(mc).toContainText('belongs to module 3014F711A0001F0000000A03; this system runs HmIP-RF on RPI-RF-MOD 0000000A01 (3014F711A000040000000A01)');
    await expect(mc).toContainText('press a button on it if it stays silent');
    await expect(block.locator('[data-notice="bidcos-key"]')).toHaveCount(0);
    await expect(block.locator('[data-input="replace-key"]')).toHaveCount(0);
    const button = block.locator('[data-action="import-devices"]');
    await expect(button).toBeEnabled();
    await button.click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('2 BidCos-RF devices, 3 HmIP devices, 1 BidCos-Wired devices');
    await expect(dlg).toContainText('adapter exchange');
    // task 301: the move said plainly - the backup is in local key mode here, so no key server; and the source system must stop
    await expect(dlg).toContainText('hmipserver moves the HmIP network onto RPI-RF-MOD 0000000A01 (3014F711A000040000000A01) at the start');
    await expect(dlg).toContainText('The backup is in local key mode: no key server is involved in the move.');
    await expect(dlg).toContainText('The system this backup comes from must not keep running this HmIP network');
    // task 281: the names of the same file go first
    await expect(dlg).toContainText('names, rooms and functions of the backup\'s ReGa database are imported first');
    await expect(dlg.getByRole('button', {name: 'Import and reboot'})).toHaveClass(/danger/);
    await dlg.getByRole('button', {name: 'Cancel'}).click();
    expect(posts).toHaveLength(0);
    await button.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Import and reboot'}).click();
    await expect(page.locator('.ol-restorenotice')).toContainText('the system reboots now');
    await expect(page.locator('.ol-restorenotice pre')).toContainText('The paired devices are imported.');
    await expect(page.locator('.ol-restorenotice pre')).toContainText('HmIP: the identity of module 3014F711A0001F0000000A03 is moved onto RPI-RF-MOD 0000000A01');
    await expect(page.locator('.ol-restorenotice pre')).toContainText('Names: 109 named objects, 11 rooms, 10 functions imported from the backup.');
    expect(posts).toEqual([{file: 'restore-ccu.sbk', replace_key: false, key: '', confirm: true, groups: []}]);
});

test('refused with devices paired here, waiting while an interface is silent, a non-default key is said and never asked, the own key is replaced on the word only', async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}, backup: report('ccu', backup), system: THIS}}));
    let target: Record<string, unknown> = {paired: {'BidCos-RF': {devices: 2, known: true}, 'HmIP-RF': {devices: 1, known: true}}, devices: 3, unknown: [], user_key: false, importable: false};
    let backup: Record<string, unknown> = BACKUP;
    let nonDefaultKey = false;
    const posts: unknown[] = [];
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup, target, module_changed: false, non_default_key: nonDefaultKey}}));
    await page.route('**/api/system/v1/restore/import-devices', (r) => {
        posts.push(JSON.parse(r.request().postData() ?? '{}'));
        return r.fulfill({json: {ok: true, imported: {backup, written: ['ids'], aside: '/etc/config/.import-devices-aside/x', non_default_key: true, target_key_replaced: true}, names: {ok: false, error: 'no ReGa database in the backup'}, rebooting: true}});
    });
    await page.goto('/system/backup');
    await upload(page);
    const block = page.locator('[data-restore="devices"]');
    await expect(block.locator('[data-devices-target="paired"]')).toContainText('This system has 3 devices paired (BidCos-RF: 2, HmIP-RF: 1): the import is refused');
    await expect(block.locator('[data-action="import-devices"]')).toBeDisabled();
    // an interface that did not answer
    target = {paired: {'BidCos-RF': {devices: 0, known: false, error: 'no answer'}}, devices: 0, unknown: ['BidCos-RF'], user_key: false, importable: false};
    await upload(page);
    await expect(block.locator('[data-devices-target="unknown"]')).toContainText('BidCos-RF did not answer');
    await expect(block.locator('[data-action="import-devices"]')).toBeDisabled();
    // nothing to import
    const full = backup;
    backup = {bidcos_rf: {devices: 0, has_key: false, gateways: 0}, hmip: {devices: 0, local_key: false, device_key_map: false}, bidcos_wired: {devices: 0, gateways: 0}, key_index: 0, files: []};
    target = FREE;
    await upload(page);
    await expect(block.locator('[data-devices="none"]')).toContainText('None: the backup holds no paired device');
    await expect(block.locator('[data-action="import-devices"]')).toHaveCount(0);
    backup = full;
    // task 278: a backup with a non-default security key onto a free system - said; task 296: its
    // passphrase asked as a check, never a gate; the same module, so no module-change notice
    target = FREE;
    backup = {...BACKUP, key_index: 2, bidcos_rf: {...BACKUP.bidcos_rf, has_key: true}};
    nonDefaultKey = true;
    await upload(page);
    await expect(block.locator('[data-devices="bidcos-rf"]')).toContainText('an individual security key');
    const keyNotice = block.locator('[data-notice="bidcos-key"]');
    await expect(keyNotice).toContainText('The backup\'s BidCos security key');
    await expect(keyNotice).toContainText('own BidCos security key (key index 2)');
    await expect(keyNotice).toContainText('The import brings it along as it is');
    await expect(keyNotice.locator('[data-input="keycheck-pass"]')).toBeVisible();
    await expect(block.locator('[data-input="devices-key"]')).toHaveCount(0);
    await expect(block.locator('[data-notice="module-change"]')).toHaveCount(0);
    await expect(block.locator('[data-action="import-devices"]')).toBeEnabled();
    // this system has a key store of its own: the button waits for the word to replace it, and the
    // POST carries it; the reboot notice repeats the key hint
    target = {...FREE, user_key: true};
    await upload(page);
    await expect(block.locator('[data-action="import-devices"]')).toBeDisabled();
    const replace = block.locator('[data-input="replace-key"]');
    await expect(replace).toBeVisible();
    await replace.check();
    await expect(block.locator('[data-action="import-devices"]')).toBeEnabled();
    await block.locator('[data-action="import-devices"]').click();
    // the passphrase was not checked: the warning first, the import on the word
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Import without the confirmed passphrase?');
    await expect(dlg).toContainText('The passphrase of the backup\'s BidCos security key is not confirmed.');
    await expect(dlg).toContainText('not the default key');
    await expect(dlg).toContainText('This system\'s own security key is replaced by the backup\'s.');
    await dlg.getByRole('button', {name: 'Import anyway and reboot'}).click();
    await expect(page.locator('[data-notice="restore-key-warning"]')).toContainText('non-default BidCos security key came along without a confirmed passphrase');
    await expect(page.locator('.ol-restorenotice pre')).not.toContainText('Keep the other system');
    // task 281: a names failure is said and does not stop the devices
    await expect(page.locator('.ol-restorenotice pre')).toContainText('The names could not be imported from the backup: no ReGa database in the backup');
    expect(posts).toEqual([{file: 'restore-ccu.sbk', replace_key: true, key: '', confirm: true, groups: []}]);
});

test('German: the heading and the refusal', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}, backup: report('ccu', BACKUP), system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup: BACKUP, target: {paired: {'HmIP-RF': {devices: 1, known: true}}, devices: 1, unknown: [], user_key: false, importable: false, hmip_module: MODULE}, module_changed: true, non_default_key: true}}));
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: 'Prüfen', exact: true}).click();
    const block = page.locator('[data-restore="devices"]');
    await expect(block.getByRole('heading', {level: 3, name: 'In dieses System importieren'})).toBeVisible();
    await expect(block.locator('[data-devices="hmip"]')).toContainText('3 Geräte · Identität des Funkmoduls');
    await expect(block.locator('[data-devices-target="paired"]')).toContainText('An diesem System sind 1 Geräte angelernt (HmIP-RF: 1)');
    await expect(block.locator('[data-action="import-devices"]')).toHaveText('Importieren und neu starten');
    await expect(block.locator('[data-notice="module-change"]')).toContainText('Anderes Funkmodul: HmIP schlüsselt um.');
    await expect(block.locator('[data-notice="module-change"]')).toContainText('drücken Sie eine Taste daran');
    await expect(block.locator('[data-notice="bidcos-key"]')).toContainText('Der BidCos-Sicherheitsschlüssel der Sicherung');
    await expect(block.locator('[data-notice="bidcos-key"]')).toContainText('Der Import bringt ihn unverändert mit');
    await expect(block.locator('[data-action="keycheck-skip"]')).toHaveText('Überspringen - ich kenne sie nicht');
});

// openccu-lite B-289 (maintainer, 2026-10-02): the backup's HmIP network would move onto this
// system's module, whose firmware is below 2.8.0, with local key mode off in the backup - the
// import and the restore are refused: the notice names the firmware and the update, both buttons
// are off, and the daemon's 422 for the same says the same
test('a module below firmware 2.8.0: the import and the restore are refused, with the firmware and the update; German', async ({page}) => {
    const refused = {module: '3014F711A000040000000A01', version: '1.8.3', minimum: '2.8.0'};
    const backup = {...BACKUP, hmip: {...BACKUP.hmip, local_key: false}};
    const target = {...FREE, hmip_module: {...MODULE, hardware: 'HMIP-RFUSB', version: '1.8.3'}};
    let kind: 'ccu' | 'openccu-lite' = 'ccu';
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}, backup: report(kind, backup), system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup, target, module_changed: true, non_default_key: false, hmip_firmware: refused, kind}}));
    const posts: string[] = [];
    await page.route('**/api/system/v1/restore/import-devices', (r) => { posts.push('import'); return r.fulfill({status: 422, json: {error: 'hmip-firmware', message: 'refused', detail: refused}}); });
    await page.route('**/api/system/v1/restore/apply', (r) => { posts.push('apply'); return r.fulfill({status: 422, json: {error: 'hmip-firmware', message: 'refused', detail: refused}}); });
    await page.goto('/system/backup');
    await upload(page);
    const block = page.locator('[data-restore="devices"]');
    const n = block.locator('[data-notice="hmip-firmware"]');
    await expect(n).toContainText('HmIP-RF cannot move to this module');
    await expect(n).toContainText('The HmIP network of this backup cannot move onto module 3014F711A000040000000A01: it runs application firmware 1.8.3, and below 2.8.0');
    await expect(n).toContainText("Update the module's firmware first (Updates page, radio firmware). A backup taken in local key mode moves without the key server and is allowed.");
    await expect(block.locator('[data-notice="module-change"]')).toHaveCount(0);
    await expect(block.locator('[data-action="import-devices"]')).toBeDisabled();
    // an (Open)CCU backup has no restore (occulited task 142)
    await expect(page.locator('[data-action="restore-apply"]')).toHaveCount(0);
    expect(posts).toHaveLength(0);
    // an openccu-lite backup with the same radio side: the restore is refused the same way
    kind = 'openccu-lite';
    await upload(page);
    await expect(page.locator('[data-restore="restore"] [data-notice="hmip-firmware"]')).toContainText('HmIP-RF cannot move to this module');
    await expect(page.locator('[data-action="restore-apply"]')).toBeDisabled();
    await expect(page.locator('[data-action="import-devices"]')).toHaveCount(0);
    kind = 'ccu';
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: 'Prüfen', exact: true}).click();
    await expect(n).toContainText('Das HmIP-Netz dieser Sicherung kann nicht auf das Modul 3014F711A000040000000A01 wechseln');
    await expect(n).toContainText('Seite Updates, Funk-Firmware');
});
