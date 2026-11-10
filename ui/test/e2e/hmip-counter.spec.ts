import {expect, test, type Page, type TestInfo} from '@playwright/test';
import {report, THIS} from './backupreport';

// openccu-lite task 299 (eq-3/occu#134): the HmIP security counter's warnings on the Status page, the
// hold of hmipserver for a trusted clock, the clock gate's untrusted states, and the outlook a backup's
// access point gets before an import. The stub plants the warnings by cookie (stub-counter), per
// browser (stub-warnings=<id>).

async function plant(page: Page, baseURL: string, info: TestInfo, cookies: Record<string, string>) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies(Object.entries({...cookies, 'stub-warnings': id}).map(([name, value]) => ({name, value, url: baseURL})));
}

test('a counter set backwards is an error that names the numbers and the remedy', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-counter': 'backwards'});
    await page.goto('/');
    const n = page.locator('[data-warnings] [data-notice="hmip-security-counter"]');
    await expect(n).toContainText('The HmIP security counter of access point 3014F711A000040000000A02 was set below what the devices have seen (the module holds 1024874713, the start computed 5319842009). Every HmIP device refuses this system as a replay until it is power-cycled');
    await expect(n).toHaveAttribute('data-severity', 'error');
    await expect(n.getByRole('link', {name: 'Interfaces'})).toHaveAttribute('href', '/system/interfaces#connections');
});

test('near the wrap: a warning with the date the protection ends', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-counter': 'near'});
    await page.goto('/');
    const n = page.locator('[data-warnings] [data-notice="hmip-security-counter"]');
    await expect(n).toContainText('is past 2^31 (the start computed 2200000000) and reaches 2^32, where its protection ends, at about');
    await expect(n).toHaveAttribute('data-severity', 'warning');
});

test('held back for a trusted clock: the hold says why and points to the Network page, the wrap beside it', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-counter': 'held'});
    await page.goto('/');
    const hold = page.locator('[data-warnings] [data-notice="hmip-clock-hold"]');
    await expect(hold).toContainText('HmIP-RF is waiting for a trusted clock: the computed security counter has passed 2^32 for access point 3014F711A000040000000A02, and the clock came neither from a real-time clock nor from a time server (timeout).');
    await expect(hold).toContainText('set the time by hand on the Network page');
    await expect(hold.getByRole('link', {name: 'Network'})).toHaveAttribute('href', '/system/network');
    await expect(page.locator('[data-warnings] [data-notice="hmip-security-counter"]')).toContainText("has passed 2^32 (the start computed 5319842009): hmipserver's check against a lower value protects nothing any more.");
});

test('the clock gate refused the time server or the real-time clock: the notice says which', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-clock', value: 'ntp-implausible', url: baseURL!}]);
    await page.goto('/');
    const n = page.locator('[data-notice="clock"]');
    await expect(n).toHaveAttribute('data-clock-state', 'ntp-implausible');
    await expect(n).toContainText("The clock is not synchronised. The time server's time is outside what this image trusts (from its build to 15 years after) and was not taken.");
    await page.context().clearCookies();
    await page.context().addCookies([{name: 'stub-clock', value: 'rtc-implausible', url: baseURL!}]);
    await page.goto('/');
    await expect(page.locator('[data-notice="clock"]')).toContainText(/The real-time clock said .*2041.*, outside what this image trusts/);
});

const CHECK = {ok: true, output: 'OK', backup_version: '3.89.11.20260919', running_version: '3.89.11.20260919', needs_key: false, has_rega: false};
const FREE = {paired: {'BidCos-RF': {devices: 0, known: true}, 'HmIP-RF': {devices: 0, known: true}}, devices: 0, unknown: [], user_key: false, importable: true, hmip_module: {hardware: 'RPI-RF-MOD', serial: '0000000A01', sgtin: '3014F711A000040000000A01'}};
function backup(counter: object) {
    return {bidcos_rf: {devices: 0, has_key: false, gateways: 0}, hmip: {devices: 3, identity_sgtin: '3014F711A000040000000A01', local_key: false, device_key_map: false, security_counter: counter}, bidcos_wired: {devices: 0, gateways: 0}, key_index: 0, files: ['ids'], version: '3.89.11.20260919'};
}

test('a backup whose access point has wrapped: the import panel says so before the import', async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}, backup: report('ccu', backup({first_connect: '2014-06-30T22:00:00Z', offset: 4031374848, calc: 5319842009, verdict: 'wrapped', wraps_at: '2016-12-01T00:00:00Z'})), system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup: backup({first_connect: '2014-06-30T22:00:00Z', offset: 4031374848, calc: 5319842009, verdict: 'wrapped', wraps_at: '2016-12-01T00:00:00Z'}), target: FREE, module_changed: false, non_default_key: false}}));
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    const n = page.locator('[data-restore="devices"] [data-notice="security-counter"]');
    await expect(n).toHaveAttribute('data-verdict', 'wrapped');
    await expect(n).toContainText('with its offset of 4031374848 the counter computed at the first start here is 5319842009, past 2^32.');
    await expect(n).toContainText('This system holds hmipserver back while the clock is not trusted');
});

test('a fine access point raises no notice', async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}, backup: report('ccu', backup({first_connect: '2026-09-03T18:52:45Z', offset: 2829, calc: 7661307, verdict: 'fine', wraps_at: '2067-06-01T00:00:00Z'})), system: THIS}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup: backup({first_connect: '2026-09-03T18:52:45Z', offset: 2829, calc: 7661307, verdict: 'fine', wraps_at: '2067-06-01T00:00:00Z'}), target: FREE, module_changed: false, non_default_key: false}}));
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    await expect(page.locator('[data-restore="devices"] [data-devices="hmip"]')).toContainText('3 devices');
    await expect(page.locator('[data-notice="security-counter"]')).toHaveCount(0);
});
