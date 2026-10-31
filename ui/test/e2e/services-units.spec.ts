import {type Locator, type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// Tasks 49 and 48 on the Services page: the PID column names the leader of a oneshot addon unit's
// cgroup with +N and lists every process in its popup; the Enabled column maps systemd's
// UnitFileState, sorts, and a static unit offers no switch; an addon outside its unit says that
// Restart puts it back, on Services and on Installed addons, and Restart on either page is the
// services restart of its unit.

const STRAY_TEXT = 'Runs outside its unit (started by an installer or by hand). Restart puts it back into the unit.';

// B-108: a tap on the phone, a click elsewhere. The clicks here were forced while the Services page
// was wider than a phone (B-105): the phone emulation zoomed it out, and a tap missed what it aimed at.
async function press(target: Locator) {
    if (test.info().project.use.isMobile) await target.tap();
    else await target.click();
}

type Json = Record<string, unknown> & {services?: Json[]; addons?: Json[]};

// the fixture's answer, changed for this test only - the stub is shared by the three projects
async function patch(page: Page, pattern: string, change: (body: Json) => void) {
    await page.route(pattern, async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const body = (await response.json()) as Json;
        change(body);
        await route.fulfill({response, json: body});
    });
}

function servicesTable(page: Page) {
    return page.locator('table.ol-table').first();
}
// the id is its own element in the first cell: where the window is narrow, kind and description
// stand in a line under it (B-75)
function row(table: Locator, id: string) {
    return table.locator('tbody tr').filter({has: table.page().locator('td:first-child .sv-id', {hasText: new RegExp(`^${id}$`)})});
}
async function cell(table: Locator, id: string, header: string) {
    const headers = (await table.locator('thead th').allTextContents()).map((h) => h.replace(/[▲▼]/g, '').trim());
    const i = headers.indexOf(header);
    expect(i, `column ${header} in ${headers.join(', ')}`).toBeGreaterThanOrEqual(0);
    return row(table, id).locator('td').nth(i);
}

test('the PID column shows the leader with +N, the Enabled column the unit file state', async ({page}) => {
    // two states the fixture has no service in: a static unit and one switched off here
    await patch(page, '**/api/system/v1/services', (body) => {
        body.services!.push(
            {id: 'e2e-static', kind: 'system', running: false, oneshot: true, result: 'success', enabled: true, unit_file_state: 'static', managed: true, category: 'core'},
            {id: 'e2e-masked', kind: 'system', running: false, enabled: false, unit_file_state: 'masked-runtime', managed: true, category: 'core'},
        );
    });
    await page.goto('/system/services');
    const table = servicesTable(page);
    await expect(row(table, 'e2e-masked')).toHaveCount(1);

    // a oneshot addon unit: the leader and the count of the rest; the +5 opens every process
    // (task 51: a popup, where it was a title)
    const tip = page.getByRole('tooltip');
    const redmaticPid = await cell(table, 'addon-redmatic', 'PID');
    await expect(redmaticPid).toHaveText('1841 +5');
    await press(redmaticPid.getByRole('button', {name: '+5'}));
    await expect(tip.locator('li')).toHaveCount(6);
    await expect(tip.locator('li').first()).toHaveText(/^1841\s+\/usr\/local\/addons\/redmatic\//);
    await page.keyboard.press('Escape');
    await expect(tip).toHaveCount(0);
    // a single process has no +N - the pid itself opens its command line - and a MainPID without
    // a process list stays plain text
    const mosquittoPid = await cell(table, 'addon-mosquitto', 'PID');
    await expect(mosquittoPid).toHaveText('1902');
    await press(mosquittoPid.getByRole('button', {name: '1902'}));
    await expect(tip.locator('li')).toHaveText(/^1902\s+\/usr\/local\/addons\/mosquitto\/bin\/mosquitto /);
    await page.keyboard.press('Escape');
    await expect(await cell(table, 'rfd', 'PID')).toHaveText('1204');
    await expect((await cell(table, 'rfd', 'PID')).getByRole('button')).toHaveCount(0);
    await expect(await cell(table, 'e2e-static', 'PID')).toHaveText('');

    await expect(await cell(table, 'rfd', 'Enabled')).toHaveText('Yes');
    await expect(await cell(table, 'addon-redmatic', 'Enabled')).toHaveText('Yes · addon');
    // the kind opens what it means for the switch
    await press((await cell(table, 'addon-redmatic', 'Enabled')).getByRole('button', {name: 'addon'}));
    await expect(tip).toContainText('rc.d script');
    await page.keyboard.press('Escape');
    await expect(tip).toHaveCount(0);
    await expect(await cell(table, 'hs485d', 'Enabled')).toHaveText('No');
    await expect(await cell(table, 'e2e-static', 'Enabled')).toHaveText('— · static');
    await expect(await cell(table, 'e2e-masked', 'Enabled')).toHaveText('No · switched off');
    // "· Disabled" has left the Status column
    await expect(await cell(table, 'hs485d', 'Status')).toHaveText('Stopped');

    // sorted by Enabled: on first, switched off last; again, the other way round
    const ids = () => table.locator('tbody tr td:first-child .sv-id').allTextContents();
    await press(table.locator('thead th', {hasText: 'Enabled'}));
    await expect.poll(async () => (await ids()).at(-1)).toBe('e2e-masked');
    expect((await ids()).indexOf('e2e-static')).toBeGreaterThan((await ids()).indexOf('addon-redmatic'));
    await press(table.locator('thead th', {hasText: 'Enabled'}));
    await expect.poll(async () => (await ids())[0]).toBe('e2e-masked');

    // a static unit offers neither Enable nor Disable; a switched-off one offers Enable
    const staticRow = row(table, 'e2e-static');
    await press(staticRow.getByRole('button', {name: 'More actions'}));
    await expect(staticRow.getByRole('menuitem', {name: 'Edit unit…'})).toBeVisible();
    await expect(staticRow.getByRole('menuitem', {name: /^(Enable|Disable)$/})).toHaveCount(0);
    await page.keyboard.press('Escape');
    const maskedRow = row(table, 'e2e-masked');
    await press(maskedRow.getByRole('button', {name: 'More actions'}));
    await expect(maskedRow.getByRole('menuitem', {name: 'Enable', exact: true})).toBeVisible();
});

// B-65: red is for a failure only. A unit a condition keeps off this box is skipped - a neutral
// ring, and the reason behind its popup; a static unit, one a timer starts and a boot-only one are
// plainly stopped (the old rule, `!running && enabled`, made all of them red)
test('the Status column: red for a failure only, skipped where a condition is not met', async ({page}) => {
    await patch(page, '**/api/system/v1/services', (body) => {
        Object.assign(body.services!.find((x) => x.id === 'hs485d')!, {enabled: true, unit_file_state: 'enabled', skipped: true, result: 'exec-condition'});
        body.services!.push(
            {id: 'hmlangw', kind: 'system', running: false, enabled: true, unit_file_state: 'enabled', skipped: true, managed: true, category: 'core'},
            {id: 'e2e-timer-started', kind: 'system', running: false, enabled: true, unit_file_state: 'static', result: 'success', managed: false, category: 'core'},
            {id: 'e2e-boot-only', kind: 'system', running: false, enabled: true, unit_file_state: 'enabled', result: 'success', managed: false, category: 'core'},
            {id: 'e2e-failed', kind: 'system', running: false, enabled: true, unit_file_state: 'enabled', failed: true, result: 'exit-code', managed: true, category: 'core'},
        );
    });
    await page.goto('/system/services');
    const table = servicesTable(page);
    await expect(row(table, 'e2e-failed')).toHaveCount(1);
    const dot = (id: string) => row(table, id).locator('.ol-dot');
    // one red dot on the page: the failure
    await expect(table.locator('.ol-dot.err')).toHaveCount(1);
    await expect(dot('e2e-failed')).toHaveClass(/\berr\b/);
    await expect(await cell(table, 'e2e-failed', 'Status')).toHaveText('Failed');
    for (const id of ['hs485d', 'hmlangw']) {
        await expect(dot(id)).toHaveClass(/\bskipped\b/);
        await expect(await cell(table, id, 'Status')).toHaveText('Skipped · condition not met');
        // a hollow ring, not a filled dot
        expect(await dot(id).evaluate((e) => [getComputedStyle(e).backgroundColor, getComputedStyle(e).borderTopWidth])).toEqual(['rgba(0, 0, 0, 0)', '2px']);
    }
    for (const id of ['e2e-timer-started', 'e2e-boot-only']) {
        await expect(dot(id)).not.toHaveClass(/\b(err|skipped|ok|once|failed)\b/);
        await expect(await cell(table, id, 'Status')).toHaveText('Stopped');
    }
    // the reason opens from the words after Skipped
    const tip = page.getByRole('tooltip');
    await press((await cell(table, 'hmlangw', 'Status')).getByRole('button', {name: 'condition not met'}));
    await expect(tip).toContainText('ConditionPathExists=');
    await page.keyboard.press('Escape');
    await expect(tip).toHaveCount(0);
});

test('a stray addon: the text says Restart puts it back, and Restart is offered on both pages', async ({page}) => {
    await patch(page, '**/api/system/v1/services', (body) => {
        const s = body.services!.find((x) => x.id === 'addon-hm2mqtt')!;
        Object.assign(s, {stray: true, pid: 3050, procs: [{pid: 3050, cmd: 'node /usr/local/addons/hm2mqtt/index.js'}]});
    });
    await patch(page, '**/api/system/v1/addons', (body) => {
        Object.assign(body.addons!.find((x) => x.id === 'hm2mqtt')!, {stray: true, pid: 3050});
    });
    const posted: string[] = [];
    await page.route('**/api/system/v1/services/*/restart', (route) => {
        posted.push(new URL(route.request().url()).pathname);
        return route.fulfill({json: {output: '1 process(es) outside the unit stopped, addon-hm2mqtt.service started in its unit'}});
    });

    await page.goto('/system/services');
    const table = servicesTable(page);
    // task 51: the state opens the text, where it was a title
    const tip = page.getByRole('tooltip');
    const stray = row(table, 'addon-hm2mqtt').getByRole('button', {name: 'outside its unit'});
    await expect(stray).toHaveClass(/ol-warn/);
    await press(stray);
    await expect(tip).toHaveText(STRAY_TEXT);
    await page.keyboard.press('Escape');
    await expect(tip).toHaveCount(0);
    // the pid found outside the unit is marked like the status, and its popup says where it is
    const pid = (await cell(table, 'addon-hm2mqtt', 'PID')).getByRole('button', {name: '3050'});
    await expect(pid).toHaveClass(/ol-warn/);
    await press(pid);
    await expect(tip.locator('p')).toHaveText('outside its unit:');
    await expect(tip.locator('li')).toHaveText(/^3050\s+node /);
    await page.keyboard.press('Escape');
    await expect(tip).toHaveCount(0);
    await press(row(table, 'addon-hm2mqtt').locator('.ol-rowactions button:has(.ol-acttext:text-is("Restart"))'));
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('started again in its unit');
    await press(dialog.getByRole('button', {name: 'OK'}));
    await expect.poll(() => posted).toEqual(['/api/system/v1/services/addon-hm2mqtt/restart']);
    await expect(page.locator('pre.ol-notice')).toContainText('started in its unit');

    await page.goto('/addons');
    const addon = page.locator('#addon-row-hm2mqtt');
    await press(addon.getByRole('button', {name: 'outside its unit'}));
    await expect(tip).toHaveText(STRAY_TEXT);
    await page.keyboard.press('Escape');
    await expect(tip).toHaveCount(0);
    await press(addon.getByRole('button', {name: 'Restart', exact: true}));
    await expect(page.getByRole('dialog')).toContainText('started again in its unit');
    await press(page.getByRole('dialog').getByRole('button', {name: 'OK'}));
    await expect.poll(() => posted).toHaveLength(2);
    expect(posted[1]).toBe('/api/system/v1/services/addon-hm2mqtt/restart');
    // the other addons carry no such button
    await expect(page.locator('#addon-row-mosquitto').getByRole('button', {name: 'Restart', exact: true})).toHaveCount(0);
    // the page reads the addon list again after the restart; the test can end while the patched
    // route still waits for the stub's answer - the shared fixture (./fixtures) waits for it
});

// occulited B-132: a switched-off addon keeps its row: no user, Start and Restart disabled, Enable in the menu
test('a switched-off addon: its row says so and the way back is the menu', async ({page}) => {
    await page.route('**/api/system/v1/services', async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        const m = body.services.find((x: {id: string}) => x.id === 'addon-mosquitto');
        Object.assign(m, {running: false, enabled: false, unit_file_state: 'masked-runtime', pid: 0, procs: undefined, user: undefined});
        await route.fulfill({response: res, json: body});
    });
    await page.goto('/system/services');
    const row = page.locator('[data-service="addon-mosquitto"]');
    await expect(row).toContainText('switched off');
    await expect(row.getByRole('button', {name: 'Start', exact: true})).toBeDisabled();
    await expect(row.getByRole('button', {name: 'Restart', exact: true})).toBeDisabled();
    await row.locator('[aria-haspopup=menu]').click();
    await expect(page.getByRole('menuitem', {name: 'Enable'})).toBeVisible();
    await expect(page.getByRole('menuitem', {name: 'Own user'})).toHaveCount(0);
});
