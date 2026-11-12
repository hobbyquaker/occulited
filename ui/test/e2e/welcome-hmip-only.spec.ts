import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';

// openccu-lite task 322: HmIP only - BidCos-RF's connection "none". The welcome page asks only where
// rfd runs on a local module with no LAN gateway and the choice is still automatic; nothing is
// preselected and Done waits for the choice. With BidCos devices paired there is nothing to choose,
// one line says BidCos-RF stays on; an HmIP-only stick and a LAN gateway get no step. Done runs the
// connection change first, waits until it has finished, and only then the local key. The Interfaces
// page names "none" by context and the hmipserver card says HmIP only; Status shows it as information.

const STICK = {hardware: 'HMIP-RFUSB', node: '/dev/raw-uart', serial: '0000000A02', sgtin: '3014F711A000040000000A02'};
function dualPlan(lgw = false) {
    return {
        multimacd: {run: true, node: '/dev/raw-uart', reason: 'shared'},
        rfd: {run: true, node: '/dev/mmd_bidcos', reason: 'the module'},
        hmipserver: {run: true, node: '/dev/mmd_hmip', reason: 'through the multiplexer'},
        hmrf: STICK, hmip: STICK,
        rfd_local: true, rfd_usb_adapter: false, rfd_lan_gateway: lgw, hmip_advanced: true,
        interfaces: ['BidCos-RF', 'VirtualDevices', 'HmIP-RF'], notes: [],
    };
}
function hmipOnlyPlan() {
    return {
        multimacd: {run: false, reason: 'not required: BidCos-RF has no local module'},
        rfd: {run: false, reason: 'off by choice'},
        hmipserver: {run: true, node: '/dev/raw-uart', reason: 'directly'},
        hmip: STICK, rfd_local: false, rfd_usb_adapter: false, rfd_lan_gateway: false, hmip_advanced: true,
        interfaces: ['VirtualDevices', 'HmIP-RF'], notes: [],
    };
}
function connView(plan: object, bidcos = '', extra: object = {}) {
    return {available: true, choices: {hmip: '', bidcos}, options: {hmip: [], bidcos: []}, plan, modules: [{serial: STICK.serial, hardware: STICK.hardware, node: STICK.node, roles: ['HmIP-RF', 'BidCos-RF']}], running: null, last: null, ...extra};
}

interface Log { calls: string[] }
/** the welcome page's reads, routed; the PUTs recorded in order */
async function setup(page: Page, o: {plan: object; bidcos?: number; known?: boolean; lk?: boolean; hmipDevices?: number}): Promise<Log> {
    const log: Log = {calls: []};
    let changed = false;
    let polls = 0;
    await page.route('**/api/system/v1/factory-reset', (r: Route) => r.request().method() !== 'GET' ? r.fallback()
        : r.fulfill({json: {hostname: 'ccu-vm-1', interfaces: {'BidCos-RF': {devices: o.bidcos ?? 0, known: o.known ?? true}, 'HmIP-RF': {devices: o.hmipDevices ?? 0, known: true}}}}));
    await page.route('**/api/system/v1/radio/connections', async (r: Route) => {
        const m = r.request().method();
        if (m === 'PUT') {
            log.calls.push(`PUT connections ${JSON.stringify(r.request().postDataJSON())}`);
            changed = true; polls = 0;
            return r.fulfill({status: 202, json: connView(dualPlan(), '', {running: {choices: {hmip: '', bidcos: 'none'}}})});
        }
        if (!changed) return r.fulfill({json: connView(o.plan)});
        polls++;
        // the change runs for two polls, then it has finished
        if (polls <= 2) return r.fulfill({json: connView(dualPlan(), '', {running: {choices: {hmip: '', bidcos: 'none'}}})});
        if (polls === 3) log.calls.push('connections finished');
        return r.fulfill({json: connView(hmipOnlyPlan(), 'none', {last: {ok: true}})});
    });
    await page.route('**/api/system/v1/radio/hmip/local-key**', async (r: Route) => {
        const m = r.request().method();
        if (m === 'PUT') {
            log.calls.push(`PUT local-key ${JSON.stringify(r.request().postDataJSON())}`);
            return r.fulfill({json: {ok: true}});
        }
        if (!o.lk) return r.fulfill({status: 409, json: {error: 'local-key', message: 'no HmIP module'}});
        return r.fulfill({json: {available: true, enabled: false, devices: o.hmipDevices ?? 0}});
    });
    return log;
}

test('no BidCos device: the choice, nothing preselected, Done waits; HmIP only switches BidCos-RF off', async ({page}) => {
    const log = await setup(page, {plan: dualPlan()});
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hmip-only]');
    await expect(step.getByRole('heading', {name: '4 · HmIP only?'})).toBeVisible();
    await expect(step).toContainText('This system has no BidCos device paired.');
    await expect(step).toContainText('without the multiplexer in between');
    await expect(step).toContainText('nothing is lost');
    const only = step.getByRole('radio', {name: 'HmIP only'});
    const keep = step.getByRole('radio', {name: 'Keep BidCos-RF'});
    await expect(only).not.toBeChecked();
    await expect(keep).not.toBeChecked();
    await expect(page.getByText('The administrator exists. Five things worth deciding now')).toBeVisible();
    await expect(page.getByRole('heading', {name: '5 · Device management'})).toBeVisible();
    const doneBtn = page.getByRole('button', {name: 'Done'});
    await expect(doneBtn).toBeDisabled();
    await expect(page.locator('[data-welcome-ho-blocked]')).toBeVisible();
    await only.check();
    await expect(doneBtn).toBeEnabled();
    await doneBtn.click();
    await expect(page).not.toHaveURL(/\/welcome/, {timeout: 15_000});
    expect(log.calls).toEqual(['PUT connections {"hmip":"","bidcos":"none"}', 'connections finished']);
});

test('Keep BidCos-RF: no change at all', async ({page}) => {
    const log = await setup(page, {plan: dualPlan()});
    await page.goto('/welcome');
    await page.locator('[data-welcome-hmip-only]').getByRole('radio', {name: 'Keep BidCos-RF'}).check();
    await page.getByRole('button', {name: 'Done'}).click();
    await expect(page).not.toHaveURL(/\/welcome/);
    expect(log.calls).toEqual([]);
});

test('HmIP only and a local key: the connection change first, the key only after it has finished', async ({page}) => {
    const log = await setup(page, {plan: dualPlan(), lk: true});
    await page.goto('/welcome');
    await expect(page.getByRole('heading', {name: '5 · The HmIP network key'})).toBeVisible();
    await expect(page.getByRole('heading', {name: '6 · Device management'})).toBeVisible();
    await expect(page.getByText('Six things worth deciding now')).toBeVisible();
    await page.locator('[data-welcome-hmip-only]').getByRole('radio', {name: 'HmIP only'}).check();
    await expect(page.getByRole('button', {name: 'Done'})).toBeDisabled(); // the key is still to choose
    await page.getByRole('radio', {name: 'Generate a local key now'}).check();
    await page.getByRole('button', {name: 'Done'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Generate'}).click();
    await expect(page).not.toHaveURL(/\/welcome/, {timeout: 15_000});
    expect(log.calls).toEqual(['PUT connections {"hmip":"","bidcos":"none"}', 'connections finished', 'PUT local-key {"mode":"generate","confirm":true}']);
});

test('BidCos devices paired: no choice, one line, Done free', async ({page}) => {
    const log = await setup(page, {plan: dualPlan(), bidcos: 4});
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hmip-only]');
    await expect(step.locator('[data-welcome-ho-paired]')).toHaveText(/BidCos-RF has 4 paired devices and stays on\. It can be turned off later on the Interfaces page\./);
    await expect(step.getByRole('radio')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Done'})).toBeEnabled();
    await page.getByRole('button', {name: 'Done'}).click();
    await expect(page).not.toHaveURL(/\/welcome/);
    expect(log.calls).toEqual([]);
});

test('count unknown: the choice, still nothing preselected', async ({page}) => {
    await setup(page, {plan: dualPlan(), bidcos: 0, known: false});
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hmip-only]');
    await expect(step.getByRole('radio', {name: 'HmIP only'})).not.toBeChecked();
    await expect(page.getByRole('button', {name: 'Done'})).toBeDisabled();
});

test('a LAN gateway, an HmIP-only stick, a choice made already: no step', async ({page}) => {
    for (const plan of [dualPlan(true), hmipOnlyPlan()]) {
        await page.unrouteAll({behavior: 'ignoreErrors'});
        await setup(page, {plan});
        await page.goto('/welcome');
        await expect(page.getByRole('heading', {name: '4 · Device management'})).toBeVisible();
        await expect(page.locator('[data-welcome-hmip-only]')).toHaveCount(0);
        await expect(page.getByRole('button', {name: 'Done'})).toBeEnabled();
    }
});

test('German: the step', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await setup(page, {plan: dualPlan()});
    await page.goto('/welcome');
    const step = page.locator('[data-welcome-hmip-only]');
    await expect(step.getByRole('heading', {name: '4 · Nur HmIP?'})).toBeVisible();
    await expect(step.getByRole('radio', {name: 'Nur HmIP'})).toBeVisible();
    await expect(step.getByRole('radio', {name: 'BidCos-RF behalten'})).toBeVisible();
    await expect(step).toContainText('nichts geht verloren');
});

test('Interfaces and Status: "Off (HmIP only)" without a LAN gateway, the hmipserver card says HmIP only', async ({page}) => {
    await page.route('**/api/system/v1/radio/connections', (r: Route) => r.request().method() !== 'GET' ? r.fallback() : r.fulfill({json: connView(hmipOnlyPlan(), 'none')}));
    await page.goto('/system/interfaces#connections');
    await expect(page.getByLabel('Module for BidCos-RF').locator('option', {hasText: 'Off (HmIP only)'})).toHaveCount(1);
    await expect(page.getByLabel('Module for BidCos-RF').locator('option', {hasText: 'LAN gateway'})).toHaveCount(0);
    await expect(page.locator('[data-process="hmipserver"] .conn-now')).toContainText('HmIP only: HMIP-RFUSB 0000000A02, directly on /dev/raw-uart');
    await expect(page.locator('[data-process="rfd"] .conn-now')).toContainText('off (HmIP only)');
    await page.goto('/');
    const card = page.locator('[data-component="BidCos-RF"][data-state="off"]');
    await expect(card).toContainText('off (HmIP only)');
    await expect(card).not.toHaveClass(/\b(warn|err)\b/);
});
