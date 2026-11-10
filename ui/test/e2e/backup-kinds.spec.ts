import {expect, test, type Page} from '@playwright/test';
import {report, THIS, EMPTY_RADIO} from './backupreport';

// occulited task 142 (hobbyquaker/openccu-lite#19, D-125): an uploaded backup is explained before
// anything is done with it - the kind, where it is from, what it holds - and then offers the one
// action that fits: the import for an (Open)CCU or CCU3 backup, with the groups the user chooses
// from, the restore for an openccu-lite backup. One passphrase check, no third button, the script's
// output behind a disclosure.

const OUTPUT = '1) Checking sbk backup file consistency:\n   generated on 3.89.11.20260919, applying to 3.89.11.20260919, OK\n   Config check processing only, exiting.';
const CHECK = {ok: true, output: OUTPUT, backup_version: '3.89.11.20260919', running_version: '3.89.11.20260919', needs_key: true, has_rega: true, backup_key: true, system_key: false, key_index: 2};
const RADIO = {
    bidcos_rf: {devices: 5, address: '0xFF1234', serial: '1709ADFA00', has_key: true, gateways: 0},
    hmip: {devices: 26, identity_sgtin: '3014F711A0001F0000000A03', local_key: false, device_key_map: false},
    bidcos_wired: {devices: 0, gateways: 0},
    key_index: 2,
    files: ['ids'],
};
const FREE = {paired: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 0, known: true}}, devices: 0, unknown: [], user_key: false, importable: true};
const PAIRED = {paired: {'BidCos-RF': {devices: 2, known: true}, 'HmIP-RF': {devices: 1, known: true}}, devices: 3, unknown: [], user_key: false, importable: false};
const ADDONS = [
    {id: 'hm-watchdog', name: 'HM Watchdog', version: '0.9', files: 12, bytes: 4000, program: true},
    {id: 'redmatic', name: 'RedMatic', files: 0, bytes: 0, program: false},
    {id: 'cuxd', name: 'CUx-Daemon', files: 3, bytes: 100, program: true, here: true},
];
const GROUPS = [
    {id: 'addons', default: true, addons: ADDONS},
    {id: 'hb_rf_eth', value: '192.168.1.50', default: true},
    {id: 'hostname', value: 'old-ccu', current: 'lab-ova', default: false},
    {id: 'loghost', value: 'loghost.lan:5514', current: 'here.lan', default: true},
    {id: 'classic_rpc', default: false, sources: ['192.168.1.20', 'local networks']},
];
const CCU = report('ccu', RADIO, {addons: ADDONS, settings: {hb_rf_eth: {value: '192.168.1.50', valid: true}, hostname: {value: 'old-ccu', valid: true}, loghost: {value: 'loghost.lan:5514', valid: true}}, left_behind: ['rega', 'users', 'network', 'firewall', 'addon_extras', 'other']});

async function stub(page: Page, o: {backup: object | null; check?: object; target?: object; groups?: object[]; kind?: string; devices?: boolean}) {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-x.sbk', check: o.check ?? CHECK, encryption: {encrypted: false}, backup: o.backup, system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => o.devices === false
        ? r.fulfill({status: 422, json: {error: 'not_a_backup', message: 'not a CCU backup'}})
        : r.fulfill({json: {backup: RADIO, target: o.target ?? FREE, module_changed: false, non_default_key: true, kind: o.kind ?? 'ccu', groups: o.groups ?? GROUPS, left_behind: ['rega', 'users', 'network', 'firewall', 'addon_extras', 'other']}}));
    await page.route('**/api/system/v1/restore/verify-key', (r) => r.fulfill({json: {backup: JSON.parse(r.request().postData() ?? '{}').key ? 'match' : 'skipped', system: 'none', key_index: 2}}));
    await page.route('**/api/system/v1/boot-expect**', (r) => r.fulfill({json: {kind: 'restore', product: 'ova', expect: {down: 5, http: 20, ui: 5, ready: 10}, default: {down: 5, http: 20, ui: 5, ready: 10}, measured: {down: 0, http: 0, ui: 0, ready: 0}}}));
}

async function upload(page: Page, check = 'Check') {
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'x.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: check, exact: true}).click();
}

test('an OpenCCU backup: the panel says what it is, the import offers its groups, the selection travels, one key check and no restore', async ({page}) => {
    await stub(page, {backup: CCU});
    const posts: Record<string, unknown>[] = [];
    await page.route('**/api/system/v1/restore/import-devices', (r) => {
        posts.push(JSON.parse(r.request().postData() ?? '{}'));
        return r.fulfill({json: {ok: true, rebooting: true, names: {ok: true, objects: 31, rooms: 4, functions: 3}, key_check: 'match', groups: {
            hb_rf_eth: {ok: true, value: '192.168.1.50'},
            loghost: {ok: false, error: 'not writable'},
            classic_rpc: {ok: true, rules: 16},
            addons: {ok: true, addons: [{id: 'hm-watchdog', name: 'HM Watchdog', imported: true, enabled: false, disabled: 'binary'}, {id: 'redmatic', name: 'RedMatic', imported: true, enabled: false, disabled: 'program'}, {id: 'cuxd', name: 'CUx-Daemon', imported: false, enabled: false, error: 'exists'}]},
        }}});
    });
    await upload(page);
    const file = page.locator('[data-restore="file"]');
    await expect(file).toHaveAttribute('data-kind', 'ccu');
    await expect(file.locator('[data-file="kind"]')).toContainText('A backup of OpenCCU - it can be imported');
    await expect(file.locator('[data-file="kind"]')).toContainText('It cannot be restored here.');
    await expect(file.locator('[data-file="from"]')).toHaveText('old-ccu · OpenCCU 3.89.11.20260919');
    // what this system is, by its own name - not the OpenCCU base it runs on alone
    await expect(file.locator('[data-file="system"]')).toHaveText('openccu-lite 1.2.0 (on OpenCCU 3.89.11.20260919)');
    await expect(file.locator('[data-file="plain"]')).toContainText('not encrypted');
    await expect(file.locator('[data-devices="bidcos-rf"]')).toContainText('5 devices · address 0xFF1234 · an individual security key');
    await expect(file.locator('[data-devices="hmip"]')).toContainText('26 devices');
    await expect(file.locator('[data-file="names"]')).toContainText('a ReGa database: names, rooms and functions');
    await expect(file.locator('[data-file="addons"]')).toHaveText('HM Watchdog 0.9, RedMatic (settings only), CUx-Daemon');
    await expect(file.locator('[data-file="settings"]')).toHaveText('HB-RF-ETH address 192.168.1.50 · syslog server loghost.lan:5514');
    // the script's output is there for support, not the first thing shown
    const details = file.locator('[data-file="details"]');
    await expect(details).not.toHaveAttribute('open', '');
    await expect(details.locator('pre')).toBeHidden();
    await details.locator('summary').click();
    await expect(details.locator('pre')).toContainText('Config check processing only');

    const imp = page.locator('[data-restore="import"]');
    await expect(imp.locator('[data-import="always"]')).toContainText('Always taken: the paired devices of all three radios');
    const box = (id: string) => imp.locator(`[data-input="group-${id}"]`);
    for (const [id, on] of [['addons', true], ['hb_rf_eth', true], ['hostname', false], ['loghost', true], ['classic_rpc', false]] as const) {
        if (on) await expect(box(id)).toBeChecked(); else await expect(box(id)).not.toBeChecked();
    }
    await expect(imp.locator('[data-group="addons"]')).toContainText('The addons with their settings: HM Watchdog, RedMatic (settings only)');
    await expect(imp.locator('[data-group="addons"]')).toContainText('Installed here already and left as they are: CUx-Daemon.');
    await expect(imp.locator('[data-group="hostname"]')).toContainText('The host name: old-ccu');
    await expect(imp.locator('[data-group="hostname"]')).toContainText('This system is called lab-ova.');
    await expect(imp.locator('[data-group="loghost"]')).toContainText('This system sends its log to here.lan');
    await expect(imp.locator('[data-group="classic_rpc"]')).toContainText('open without a login from 192.168.1.20, the local networks');
    await expect(imp.locator('[data-group="classic_rpc"]')).toContainText('anyone the firewall lets in controls every device');
    await imp.locator('[data-import="left-behind"] summary').click();
    await expect(imp.locator('[data-left="rega"]')).toContainText('programs, system variables and scripts of the ReGa');
    await expect(imp.locator('[data-left]')).toHaveCount(6);
    // one passphrase check on the page, no restore, no third button
    await expect(page.locator('[data-keycheck]')).toHaveCount(1);
    await expect(page.locator('[data-keycheck="import"]')).toBeVisible();
    await expect(page.locator('[data-action="restore-apply"]')).toHaveCount(0);
    await expect(page.locator('[data-restore="restore"]')).toHaveCount(0);
    await expect(page.locator('[data-action="import-names"]')).toHaveCount(0);

    // a checkbox changes nothing else on the page: the button keeps its box
    const button = imp.locator('[data-action="import-devices"]');
    // (its place below the panel's top: the page scrolls when a checkbox is brought into view)
    const boxOf = () => button.evaluate((el) => { const r = el.getBoundingClientRect(); const top = el.closest('[data-restore="import"]')!.getBoundingClientRect().y; return {x: r.x, y: Math.round(r.y - top), w: r.width, h: r.height}; });
    const before = await boxOf();
    await box('classic_rpc').check();
    await box('addons').uncheck();
    await box('addons').check();
    expect(await boxOf()).toEqual(before);
    await page.locator('[data-action="keycheck-skip"]').click();
    await button.click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText('Also taken from the backup:');
    await expect(dlg).toContainText('- The addons with their settings: HM Watchdog, RedMatic (settings only)');
    await expect(dlg).toContainText('- The syslog server (LOGHOST): loghost.lan:5514');
    await expect(dlg).not.toContainText('The host name: old-ccu');
    await expect(dlg).toContainText('Classic RPC is switched on without a login, as on the old system: anyone the firewall lets in controls every device.');
    await dlg.getByRole('button', {name: 'Import anyway and reboot'}).click();
    await expect(page.locator('.ol-restorenotice')).toContainText('Importing — the system reboots now.');
    expect(posts).toEqual([{file: 'restore-x.sbk', replace_key: false, key: '', confirm: true, groups: ['addons', 'hb_rf_eth', 'loghost', 'classic_rpc']}]);
    const result = page.locator('.ol-restorenotice pre');
    await expect(result).toContainText('Taken from the backup (hb_rf_eth): 192.168.1.50');
    await expect(result).toContainText('Not imported (loghost): not writable');
    await expect(result).toContainText('Classic RPC is switched on: 16 firewall rules.');
    await expect(result).toContainText('Addon HM Watchdog: imported, disabled (its programs are for another processor).');
    await expect(result).toContainText('Addon RedMatic: imported, disabled (the backup holds its settings, not its program: install it again).');
    await expect(result).toContainText('Addon CUx-Daemon: not imported (exists).');
});

test('an openccu-lite backup: the panel says so, the restore names the users it replaces, no import', async ({page}) => {
    await stub(page, {backup: report('openccu-lite', RADIO, {lite_version: '1.2.0', hostname: 'lab-ova', users: 3, tokens: 2, addons: [{id: 'hmm', name: 'Homematic Manager', version: '3.1.0', files: 9, bytes: 1, program: true}]}), kind: 'openccu-lite', groups: []});
    const posts: Record<string, unknown>[] = [];
    await page.route('**/api/system/v1/restore/apply', (r) => {
        posts.push(JSON.parse(r.request().postData() ?? '{}'));
        return r.fulfill({json: {ok: true, output: '4) Scheduling backup restore for next boot cycle, OK', rebooting: true}});
    });
    await upload(page);
    const file = page.locator('[data-restore="file"]');
    await expect(file).toHaveAttribute('data-kind', 'openccu-lite');
    await expect(file.locator('[data-file="kind"]')).toContainText('A backup of openccu-lite - it can be restored');
    await expect(file.locator('[data-file="from"]')).toHaveText('lab-ova · openccu-lite 1.2.0 (on OpenCCU 3.89.11.20260919)');
    await expect(file.locator('[data-file="accounts"]')).toHaveText('3 users, 2 API tokens');
    await expect(file.locator('[data-file="addons"]')).toHaveText('Homematic Manager 3.1.0');

    await expect(page.locator('[data-restore="import"]')).toHaveCount(0);
    await expect(page.locator('[data-action="import-devices"]')).toHaveCount(0);
    await expect(page.locator('[data-action="import-names"]')).toHaveCount(0);
    await expect(page.locator('[data-keycheck]')).toHaveCount(1);
    await expect(page.locator('[data-keycheck="restore"]')).toBeVisible();
    const block = page.locator('[data-restore="restore"]');
    await expect(block.locator('[data-restore="replaces"]')).toContainText('and the users and API tokens');
    await page.locator('[data-input="keycheck-pass"]').fill('Right_Pass142');
    await page.locator('[data-action="keycheck-check"]').click();
    await expect(page.locator('[data-keycheck-verdict="match"]')).toBeVisible();
    await block.locator('[data-action="restore-apply"]').click();
    const dlg = page.getByRole('dialog');
    await expect(dlg).toContainText("This system's users and API tokens are replaced by the backup's (3 users, 2 tokens): after the reboot you sign in with an account of the backup.");
    await dlg.getByRole('button', {name: 'Restore and reboot'}).click();
    await expect(page.locator('.ol-restorenotice')).toContainText('Restoring — the system reboots now.');
    expect(posts).toEqual([{file: 'restore-x.sbk', key: 'Right_Pass142', force: false, confirm: true}]);
});

test('a system that has devices: the reason, the way to the factory reset, the names alone - and nothing is imported', async ({page}) => {
    await stub(page, {backup: CCU, target: PAIRED});
    let names = 0;
    await page.route('**/api/meta/v1/import/regadom', (r) => { names++; return r.fulfill({json: {objects: 31, result: {rooms: 4, functions: 3, unnamed: 0}}}); });
    await upload(page);
    const n = page.locator('[data-devices-target="paired"]');
    await expect(n).toContainText('This system has 3 devices paired (BidCos-RF: 2, HmIP-RF: 1): the import is refused');
    await expect(n).toContainText('a factory reset empties this one. It deletes everything on it');
    await expect(page.locator('[data-action="import-devices"]')).toBeDisabled();
    // the one case the names alone still have
    await n.locator('[data-action="import-names"]').click();
    await expect(page.locator('[data-import="names-result"]')).toHaveText('Imported: 31 named objects, 4 rooms, 3 functions.');
    expect(names).toBe(1);
    const reset = n.locator('[data-action="to-factory-reset"]');
    await expect(reset).toHaveAttribute('href', '/system/backup#factory-reset');
    await reset.click();
    await expect(page.locator('h2#factory-reset')).toBeInViewport();
});

test('groups that cannot be chosen say why and stay off', async ({page}) => {
    await stub(page, {backup: CCU, groups: [
        {id: 'addons', default: false, unavailable: 'here', addons: [ADDONS[2]]},
        {id: 'hostname', value: 'bad_name', current: 'lab-ova', default: false, unavailable: 'invalid'},
        {id: 'classic_rpc', default: false, unavailable: 'auth', sources: ['local networks']},
    ]});
    await upload(page);
    const imp = page.locator('[data-restore="import"]');
    for (const [id, why] of [['addons', 'Every addon of the backup is installed here already'], ['hostname', 'This system cannot take this value'], ['classic_rpc', 'The old system asked for a login on these ports']] as const) {
        await expect(imp.locator(`[data-input="group-${id}"]`)).toBeDisabled();
        await expect(imp.locator(`[data-input="group-${id}"]`)).not.toBeChecked();
        await expect(imp.locator(`[data-group="${id}"]`)).toContainText(why);
    }
    await expect(imp.locator('[data-group="addons"]')).toContainText('The addons with their settings: CUx-Daemon');
});

test('a file that is no backup: said, the details open, nothing offered', async ({page}) => {
    await stub(page, {backup: null, check: {ok: false, output: 'ERROR: usr_local.tar.gz missing', needs_key: false, has_rega: false}, devices: false});
    await upload(page);
    const file = page.locator('[data-restore="file"]');
    await expect(file).toHaveAttribute('data-kind', 'unknown');
    await expect(file.locator('[data-file="kind"]')).toHaveText('Not a backup this system can read.');
    await expect(file.locator('[data-file="check"]')).toContainText('rejected the file');
    await expect(file.locator('[data-file="details"] pre')).toBeVisible();
    await expect(page.locator('[data-action="import-devices"]')).toHaveCount(0);
    await expect(page.locator('[data-action="restore-apply"]')).toHaveCount(0);
});

test('German: the panel, the groups and the restore\'s question', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await stub(page, {backup: CCU});
    await upload(page, 'Prüfen');
    await expect(page.getByRole('heading', {level: 2, name: 'Sicherung einspielen oder importieren'})).toBeVisible();
    const file = page.locator('[data-restore="file"]');
    await expect(file.getByRole('heading', {level: 3})).toHaveText('Diese Datei');
    await expect(file.locator('[data-file="kind"]')).toContainText('Eine Sicherung von OpenCCU - sie lässt sich importieren');
    await expect(file.locator('[data-file="kind"]')).toContainText('Einspielen lässt sie sich hier nicht.');
    await expect(file.locator('[data-file="system"]')).toHaveText('openccu-lite 1.2.0 (auf OpenCCU 3.89.11.20260919)');
    const imp = page.locator('[data-restore="import"]');
    await expect(imp.getByRole('heading', {level: 3})).toContainText('In dieses System importieren');
    await expect(imp.locator('[data-group="addons"]')).toContainText('Die Zusatzsoftware mit ihren Einstellungen: HM Watchdog, RedMatic (nur Einstellungen)');
    await expect(imp.locator('[data-group="classic_rpc"]')).toContainText('Klassisches RPC wie auf dem alten System');
    await expect(imp.locator('[data-group="classic_rpc"]')).toContainText('192.168.1.20, die lokalen Netze');
    await expect(imp.locator('[data-action="import-devices"]')).toHaveText('Importieren und neu starten');
    // the restore, on an openccu-lite backup
    await page.unroute('**/api/system/v1/restore/check');
    await page.unroute('**/api/system/v1/restore/devices?**');
    await stub(page, {backup: report('openccu-lite', EMPTY_RADIO, {users: 1, tokens: 0}), check: {...CHECK, needs_key: false, backup_key: false}, kind: 'openccu-lite', groups: []});
    await upload(page, 'Prüfen');
    await expect(page.locator('[data-file="kind"]')).toContainText('Eine Sicherung von openccu-lite - sie lässt sich einspielen');
    await expect(page.locator('[data-file="addons"]')).toHaveText('keine');
    await page.locator('[data-action="restore-apply"]').click();
    await expect(page.getByRole('dialog')).toContainText('Die Benutzer und API-Token dieses Systems werden durch die der Sicherung ersetzt (1 Benutzer, 0 Token)');
});
