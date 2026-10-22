import {expect, shellStreamInWindow, test} from './fixtures';

// task 193, phase 2: the App - a tab beside Status; the drawer generated from the metadata tree
// (favorites, the enums as foldable sections, service messages with a count, settings for an
// account that may configure); a leaf page lists the assigned channels as tiles; on a phone the
// drawer hides behind a floating button.
test('the App tab, the drawer from the tree, a room page with its channels', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone: the phone test below');
    await page.goto('/app');
    await expect(page.getByRole('navigation', {name: 'App menu'})).toBeVisible();
    const drawer = page.locator('[data-app-drawer]');
    await expect(drawer.locator('[data-app-entry="favorites"]')).toHaveText('Favorites');
    await expect(drawer.locator('[data-app-section="rooms"]')).toContainText('Rooms');
    await expect(drawer.locator('[data-app-section="functions"]')).toContainText('Functions');
    await expect(drawer.locator('[data-app-node="rooms/wohnzimmer"]')).toHaveText('Wohnzimmer');
    // the tree is indented and folded per node: Obergeschoss holds Bad
    await expect(drawer.locator('[data-app-node="rooms/og/bad"]')).toHaveText('Bad');
    // task 98: no Servicemeldungen entry (the corners and the Status page have them)
    await expect(drawer.locator('[data-app-entry="messages"]')).toHaveCount(0);
    // an account that may configure has the unassigned channels as an icon button at the foot; no Settings section
    await expect(drawer.locator('[data-app-entry="unassigned"]')).toBeVisible();
    await expect(drawer.locator('[data-app-section="settings"]')).toHaveCount(0);
    // favorites: this account has none in the stub
    await expect(page.locator('[data-app-title]')).toHaveText('Favorites');
    await expect(page.locator('[data-app-empty]')).toContainText('No favorites yet');
    // a room: its channels, alphabetically, the subtree included
    await drawer.locator('[data-app-node="rooms/wohnzimmer"]').click();
    await expect(page).toHaveURL(/\/app\/e\/rooms\/wohnzimmer$/);
    await expect(page.locator('[data-app-title]')).toHaveText('Wohnzimmer');
    const tiles = page.locator('[data-app-tile]');
    await expect(tiles).toHaveCount(4);
    // task 101: the room's own name is left out of the tiles' names
    await expect(tiles.nth(0)).toContainText('Deckenlicht');
    await expect(tiles.nth(0).locator('.app-tile-name')).toHaveText('Deckenlicht');
    await expect(tiles.nth(3)).toContainText('Zentrale Taste 1');
    await drawer.locator('[data-app-node="rooms/og"]').click();
    await expect(page.locator('[data-app-title]')).toHaveText('Obergeschoss');
    await expect(page.locator('[data-app-tile]')).toHaveCount(8);
    await expect(page.locator('[data-app-tile]').first()).toContainText('Bad Spiegellampe');
    // folding a section is remembered
    await drawer.locator('[data-app-section="functions"]').click();
    await expect(drawer.locator('[data-app-node="functions/licht"]')).toHaveCount(0);
    await page.reload();
    await expect(drawer.locator('[data-app-node="functions/licht"]')).toHaveCount(0);
    await drawer.locator('[data-app-section="functions"]').click();
    await expect(drawer.locator('[data-app-node="functions/licht"]')).toBeVisible();
    // the unassigned list
    await drawer.locator('[data-app-entry="unassigned"]').click();
    await expect(page.locator('[data-app-title]')).toHaveText('Unassigned channels');
});

test('an operate account has no unassigned button', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'kid', role: 'user', level: 'operate', account_id: 'deadbeef', must_change_password: false, sid: 'K1'}}));
    await page.goto('/app');
    await expect(page.locator('[data-app-entry="favorites"]')).toBeVisible();
    await expect(page.locator('[data-app-entry="unassigned"]')).toHaveCount(0);
});

test('on a phone the drawer is behind the floating button and closes on a pick', async ({page, isMobile}) => {
    test.skip(!isMobile, 'the phone project only');
    await page.goto('/app');
    const fab = page.locator('[data-app-fab]');
    await expect(fab).toBeVisible();
    await expect(page.locator('[data-app-drawer="closed"]')).toHaveCount(1);
    await fab.click();
    await expect(page.locator('[data-app-drawer="open"]')).toHaveCount(1);
    await page.locator('[data-app-node="rooms/kueche"]').click();
    await expect(page.locator('[data-app-drawer="closed"]')).toHaveCount(1);
    await expect(page.locator('[data-app-title]')).toHaveText('Küche');
    // while it is open the button is gone; Escape, the scrim and a swipe to the left close it
    await fab.click();
    await expect(page.locator('[data-app-drawer="open"]')).toHaveCount(1);
    await expect(fab).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(page.locator('[data-app-drawer="closed"]')).toHaveCount(1);
    await fab.click();
    await page.mouse.click(400, 300); // right of the drawer: the scrim
    await expect(page.locator('[data-app-drawer="closed"]')).toHaveCount(1);
    await fab.click();
    await expect(page.locator('[data-app-drawer="open"]')).toHaveCount(1);
    // the drawer slides in: swipe once it has arrived, or the swipe's end lies off-screen
    await expect.poll(async () => (await page.locator('[data-app-drawer]').boundingBox())!.x).toBeGreaterThanOrEqual(0);
    const box = (await page.locator('[data-app-drawer]').boundingBox())!;
    await page.mouse.move(box.x + box.width - 20, box.y + 300);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width - 120, box.y + 305, {steps: 6});
    await page.mouse.up();
    await expect(page.locator('[data-app-drawer="closed"]')).toHaveCount(1);
});

test('a room page shows values over lite-rpc, and the circle switches', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    await expect(lamp).toHaveAttribute('data-app-widget', 'dimmer');
    await expect(lamp).toHaveAttribute('data-app-on', '1');
    await expect(lamp.locator('[data-app-state]')).toHaveText('On · 60 %');
    const thermo = page.locator('[data-app-tile="HmIP-RF.000ABC:1"]');
    await expect(thermo).toHaveAttribute('data-app-widget', 'thermostat');
    // task 92 (T2E): Ist and Soll side by side; no valve on this one
    await expect(thermo.locator('[data-app-actual]')).toHaveText('20.2°');
    await expect(thermo.locator('[data-app-set]')).toHaveText('21.5°');
    await expect(thermo.locator('[data-app-thermo]')).toHaveAttribute('title', 'set 21.5 °C · 20.2 °C');
    await expect(thermo.locator('[data-app-valve]')).toHaveCount(0);
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/rpc/v1/json/HmIP-RF') && (r.postData() ?? '').includes('setValue')),
        lamp.getByRole('button', {name: 'Switch Bad Spiegellampe'}).click(),
    ]);
    expect(req.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000DD8:3', 'LEVEL', 0]});
    await expect(lamp).toHaveAttribute('data-app-on', '0');
    await expect(lamp.locator('[data-app-state]')).toHaveText('Off');
});

// task 75: the state icon in the circle - the KNX User Forum set's, swapped on a state change in a
// circle that keeps its box (task 92: a disc of 46 px below 900 px, 56 px from there), and the tile keeps its box too
test('the circle carries the state icon; a state change swaps it in the same box', async ({page, baseURL, isMobile}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    const circle = lamp.locator('.app-circle');
    await expect(circle).toHaveAttribute('data-icon', 'light_light_dim_60');
    await expect(circle).toHaveClass(/\bon\b/);
    await expect(circle.locator('svg')).toHaveCount(1);
    const size = isMobile ? 46 : 56;
    const before = (await circle.boundingBox())!;
    const tileBefore = (await lamp.boundingBox())!;
    expect(Math.round(before.width)).toBe(size);
    expect(Math.round(before.height)).toBe(size);
    // the icon fills about 84 % of the disc
    const ico = (await circle.locator('svg').boundingBox())!;
    expect(ico.width / size).toBeGreaterThan(0.8);
    expect(ico.width / size).toBeLessThan(0.88);
    await circle.click();
    await expect(circle).toHaveAttribute('data-icon', 'light_light_dim_00');
    await expect(circle).not.toHaveClass(/\bon\b/);
    expect(await circle.boundingBox()).toEqual(before);
    expect(await lamp.boundingBox()).toEqual(tileBefore);
    // the thermostat: a filled thermometer, from the room temperature since task 97 (20.2 °C of 12..25 °C)
    await expect(page.locator('[data-app-tile="HmIP-RF.000ABC:1"] .app-circle')).toHaveAttribute('data-icon', 'thermometer:63');
    // the smoke detector, quiet: the neutral colour
    const smoke = page.locator('[data-app-tile="HmIP-RF.000F01:1"] .app-circle');
    if (await smoke.count()) await expect(smoke).toHaveAttribute('data-icon', 'secur_smoke_detector');
});

test('Settings: the start page and the App without the top bar, kept with the account', async ({page, baseURL, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-prefs', value: `prefs-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/settings');
    const start = page.locator('[data-setting="start-page"]').getByRole('combobox');
    await expect(start).toHaveValue('status');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/preferences')),
        start.selectOption('app'),
    ]);
    expect(req.postDataJSON()).toMatchObject({start_page: 'app'});
    // the system's own address opens the App now
    await page.goto('/');
    await expect(page).toHaveURL(/\/app$/);
    await expect(page.getByRole('navigation', {name: 'App menu'})).toBeVisible();
    // Status stays reachable from the tab bar
    await page.getByRole('link', {name: 'Status', exact: true}).first().click();
    await expect(page).toHaveURL(/\/$/);
    // the App as the whole window: no header on /app, the drawer's foot leads back
    await page.goto('/settings');
    const full = page.locator('[data-setting="app-fullscreen"]').getByRole('checkbox');
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/preferences')),
        full.check(),
    ]);
    expect(req2.postDataJSON()).toMatchObject({start_page: 'app', app_fullscreen: true});
    await expect(page.locator('header.ol-header')).toBeVisible();
    await page.goto('/app');
    await expect(page.locator('header.ol-header')).toHaveCount(0);
    await expect(page.locator('[data-app-foot]').getByRole('link', {name: 'Leave to Status'})).toHaveAttribute('href', '/');
    await expect(page.locator('[data-app-foot]').getByRole('link', {name: 'Account'})).toHaveAttribute('href', '/account');
    await page.locator('[data-app-foot]').getByRole('link', {name: 'Settings'}).click();
    await expect(page).toHaveURL(/\/settings$/);
    await expect(page.locator('header.ol-header')).toBeVisible();
    await expect(full).toBeChecked();
});

test('a virtual key: a tap presses short, a hold presses long', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    const key = page.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"]');
    await expect(key).toHaveAttribute('data-app-widget', 'button');
    await expect(key.locator('[data-app-state]')).toHaveText('');
    const circle = key.getByRole('button', {name: /^Press /}); // by role, not by name: another project may be renaming it
    const [short] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/rpc/v1/json/BidCos-RF') && (r.postData() ?? '').includes('setValue')),
        circle.click(),
    ]);
    expect(short.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['BidCoS-RF:2', 'PRESS_SHORT', true]});
    await expect(key.locator('[data-app-state]')).toHaveText('short press');
    await expect(key.locator('[data-app-state]')).toHaveText('', {timeout: 3000});
    // a hold of 700 ms: one long press, and the click after it is not a short one
    const box = (await circle.boundingBox())!;
    const requests: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST' && (r.postData() ?? '').includes('setValue')) requests.push(r.postData() ?? ''); });
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.waitForTimeout(700);
    await page.mouse.up();
    await expect(key.locator('[data-app-state]')).toHaveText('long press');
    await page.waitForTimeout(300);
    expect(requests).toHaveLength(1);
    expect(requests[0]).toContain('PRESS_LONG');
});

test('the sheet: a dimmer\'s presets and slider, a thermostat\'s setpoint, a key opens none', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    await expect(lamp).toHaveAttribute('data-app-on', '1');
    await page.getByRole('button', {name: 'Open Bad Spiegellampe'}).click();
    const sheet = page.getByRole('dialog');
    await expect(sheet).toContainText('Bad Spiegellampe');
    await expect(sheet.getByRole('slider', {name: 'Brightness'})).toHaveValue('60');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('setValue')),
        sheet.locator('[data-sheet="presets"]').getByRole('button', {name: '25 %'}).click(),
    ]);
    expect(req.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000DD8:3', 'LEVEL', 0.25]});
    await expect(sheet.getByRole('slider', {name: 'Brightness'})).toHaveValue('25');
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('setValue')),
        sheet.getByRole('slider', {name: 'Brightness'}).fill('80'),
    ]);
    expect(req2.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000DD8:3', 'LEVEL', 0.8]});
    // the head is the tile's: the circle switches, the name and the state beside it
    await expect(sheet.locator('[data-sheet-state]')).toHaveText('On · 80 %');
    await expect(sheet.locator('[data-sheet="values"]')).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(lamp.locator('[data-app-state]')).toHaveText('On · 80 %');
    // the thermostat: the setpoint by half a degree
    await page.getByRole('button', {name: 'Open Bad Thermostat'}).click();
    const [req3] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('setValue')),
        page.getByRole('dialog').getByRole('button', {name: 'Raise'}).click(),
    ]);
    expect(req3.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000ABC:1', 'SET_POINT_TEMPERATURE', 22]});
    await expect(page.getByRole('dialog').locator('[data-sheet="setpoint"]')).toContainText('22.0 °C');
    // the mode: auto is on; manual writes SET_POINT_MODE 1, boost BOOST_MODE true
    const modes = page.getByRole('dialog').locator('[data-sheet="mode"]');
    await expect(modes.getByRole('button', {name: 'Auto'})).toHaveAttribute('aria-pressed', 'true');
    const [req4] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('setValue')),
        modes.getByRole('button', {name: 'Manual'}).click(),
    ]);
    expect(req4.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000ABC:1', 'SET_POINT_MODE', 1]});
    await expect(modes.getByRole('button', {name: 'Manual'})).toHaveAttribute('aria-pressed', 'true');
    const [req5] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('setValue')),
        modes.getByRole('button', {name: 'Boost'}).click(),
    ]);
    expect(req5.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000ABC:1', 'BOOST_MODE', true]});
    await expect(modes.getByRole('button', {name: 'Boost'})).toHaveAttribute('aria-pressed', 'true');
    await page.keyboard.press('Escape');
    // a key acts only: no sheet
    await expect(page.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"]')).not.toHaveAttribute('role', 'button');
});

test('a very-long press on a card opens the assignment sheet; a room is toggled, a display name set', async ({page, isMobile, baseURL, browser}, info) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    const me = `dn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}, {name: 'stub-dn', value: me, url: baseURL!}]);
    // the projects share the stub's object: each toggles a room of its own
    const room = info.project.name.includes('dark') ? 'rooms/wohnzimmer' : 'rooms/kueche';
    const display = `Taste ${info.project.name}`;
    await page.goto('/app/e/rooms/og/bad');
    const key = page.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"]');
    const longPress = async () => {
        const box = (await key.boundingBox())!;
        await page.mouse.move(box.x + box.width - 20, box.y + box.height - 20);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
    };
    await longPress();
    const sheet = page.getByRole('dialog');
    await expect(sheet).toContainText('Zentrale Taste 2');
    await expect(sheet.locator('[data-assign-node="rooms/og/bad"]').getByRole('checkbox')).toBeChecked();
    await expect(sheet.locator(`[data-assign-node="${room}"]`).getByRole('checkbox')).not.toBeChecked();
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/objects/BidCos-RF.BidCoS-RF%3A2')),
        sheet.locator(`[data-assign-node="${room}"]`).getByRole('checkbox').check(),
    ]);
    expect(req.postDataJSON().enums).toEqual(expect.arrayContaining(['rooms/og/bad', room]));
    await expect(sheet.locator('[data-assign-saved]')).toBeVisible();
    await sheet.locator(`[data-assign-node="${room}"]`).getByRole('checkbox').uncheck();
    // occulited task 73: the metadata name is read-only, there is no renaming from Control
    const nameField = sheet.locator('[data-assign-name]');
    await expect(nameField).toHaveValue('Zentrale Taste 2');
    await expect(nameField).toHaveAttribute('readonly', '');
    await expect(sheet.getByRole('button', {name: 'Rename', exact: true})).toHaveCount(0);
    // the display name: the account's own, never a write to the metadata
    const metaWrites: string[] = [];
    page.on('request', (r) => { if (r.method() === 'PATCH' && (r.postData() ?? '').includes('name')) metaWrites.push(r.url()); });
    const save = sheet.locator('[data-assign-display-save]');
    const saveBox = await save.boundingBox();
    await expect(save).toBeDisabled();
    const [put] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/display-names/BidCos-RF.BidCoS-RF%3A2')),
        sheet.locator('[data-assign-display]').fill(display).then(() => save.click()),
    ]);
    expect(put.postDataJSON()).toEqual({name: display});
    // the button keeps its box (its size: the sheet scrolls between the room rows and the field since task 106's picker made it taller)
    expect(await save.boundingBox()).toMatchObject({width: saveBox!.width, height: saveBox!.height});
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(key.locator('.app-tile-name')).toHaveText(display);
    await expect(key.locator('.app-tile-name')).toHaveAttribute('title', 'actually: Zentrale Taste 2');
    expect(metaWrites).toEqual([]);
    // the metadata keeps its name
    const obj = await (await page.request.get('/api/meta/v1/snapshot')).json();
    expect(obj.objects['BidCos-RF.BidCoS-RF:2'].name).toBe('Zentrale Taste 2');
    // another account sees the real name
    const other = await browser.newContext({baseURL});
    await other.addCookies([{name: 'stub-dn', value: `${me}-other`, url: baseURL!}]);
    const p2 = await other.newPage();
    await p2.goto('/app/e/rooms/og/bad');
    await expect(p2.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"] .app-tile-name')).toHaveText('Zentrale Taste 2');
    await other.close();
    // the name stays after a reload; clearing it brings the metadata name back
    await page.reload();
    await expect(key.locator('.app-tile-name')).toHaveText(display);
    await longPress();
    const [del] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'DELETE' && r.url().endsWith('/api/auth/v1/me/display-names/BidCos-RF.BidCoS-RF%3A2')),
        sheet.locator('[data-assign-display]').fill('').then(() => save.click()),
    ]);
    expect(del.method()).toBe('DELETE');
    await page.keyboard.press('Escape');
    await expect(key.locator('.app-tile-name')).toHaveText('Zentrale Taste 2');
    await expect(key.locator('.app-tile-name')).not.toHaveAttribute('title', /actually/);
});

test('a very-long press on a drawer entry: released in place it renames, moved it sorts', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.goto('/app');
    const drawer = page.locator('[data-app-drawer]');
    const sicherheit = drawer.locator('[data-app-node="functions/sicherheit"]');
    let box = (await sicherheit.boundingBox())!;
    await page.mouse.move(box.x + 20, box.y + box.height / 2);
    await page.mouse.down();
    await page.waitForTimeout(1400);
    await page.mouse.up();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Rename Sicherheit');
    await dialog.getByLabel('Name').fill('Sicherheit & Alarm');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/enums/functions/nodes/sicherheit')),
        dialog.getByRole('button', {name: 'Rename'}).click(),
    ]);
    expect(req.postDataJSON()).toEqual({name: 'Sicherheit & Alarm'});
    await expect(drawer.locator('[data-app-node="functions/sicherheit"]')).toHaveText('Sicherheit & Alarm');
    await page.request.patch('/api/meta/v1/enums/functions/nodes/sicherheit', {data: {name: 'Sicherheit'}});
    // sorting: Licht dragged below Sicherheit while the finger stays down
    await page.reload();
    const licht = drawer.locator('[data-app-node="functions/licht"]');
    box = (await licht.boundingBox())!;
    const below = (await drawer.locator('[data-app-node="functions/sicherheit"]').boundingBox())!;
    await page.mouse.move(box.x + 20, box.y + box.height / 2);
    await page.mouse.down();
    await page.waitForTimeout(1400);
    await expect(page.locator('[data-app-sorting="1"]')).toHaveCount(1);
    await page.mouse.move(box.x + 20, below.y + below.height - 2, {steps: 8});
    await expect(drawer.locator('[data-app-sib="functions/"]').first()).toContainText('Sicherheit');
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/enums/functions/nodes/licht')),
        page.mouse.up(),
    ]);
    expect(req2.postDataJSON()).toEqual({position: 1});
    await page.request.patch('/api/meta/v1/enums/functions/nodes/licht', {data: {position: 0}});
});

test('an enum without nodes is not in the drawer', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.route('**/api/meta/v1/snapshot', async (route) => {
        const r = await route.fetch();
        const snap = await r.json();
        snap.enums.leer = {name: {de: 'Leer', en: 'Empty'}, tree: []};
        await route.fulfill({response: r, json: snap});
    });
    await page.goto('/app');
    await expect(page.locator('[data-app-section="rooms"]')).toBeVisible();
    await expect(page.locator('[data-app-section="leer"]')).toHaveCount(0);
});

test('the App opens where it was left: a room stays the page after a trip to Status', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.goto('/app/e/rooms/kueche');
    await expect(page.locator('[data-app-title]')).toHaveText('Küche');
    await page.getByRole('link', {name: 'Status', exact: true}).first().click();
    await expect(page).toHaveURL(/\/$/);
    await page.getByRole('link', {name: 'Control', exact: true}).click();
    await expect(page).toHaveURL(/\/app\/e\/rooms\/kueche$/);
    await expect(page.locator('[data-app-title]')).toHaveText('Küche');
    // a reload of the bare /app lands there too; favorites, once picked, are remembered the same way
    await page.goto('/app');
    await expect(page).toHaveURL(/\/app\/e\/rooms\/kueche$/);
    await page.locator('[data-app-entry="favorites"]').click();
    await page.goto('/app');
    await expect(page).toHaveURL(/\/app\/favorites$/);
});

// occulited task 98: Favoriten, Räume and Gewerke start at one x - the triangle (drawn, rounded) is at
// the row's right end; expanding turns it and changes no row's box; the Servicemeldungen entry is gone,
// and its old address leads to the Status page's service messages
test('the drawer: the labels aligned, the triangles on the right, no service messages entry (task 98)', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.goto('/app');
    const drawer = page.locator('[data-app-drawer]');
    await expect(drawer.locator('[data-app-entry="favorites"]')).toBeVisible();
    await expect(drawer.locator('[data-app-entry="messages"]')).toHaveCount(0);
    // the label's left edge: the first text's position in the row
    const textX = (l: import('@playwright/test').Locator) => l.evaluate((e) => {
        const w = document.createTreeWalker(e, NodeFilter.SHOW_TEXT, {acceptNode: (n) => (n.textContent!.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT)});
        const r = document.createRange();
        r.selectNodeContents(w.nextNode()!);
        return Math.round(r.getBoundingClientRect().left);
    });
    const fav = await textX(drawer.locator('[data-app-entry="favorites"]'));
    expect(await textX(drawer.locator('[data-app-section="rooms"]'))).toBe(fav);
    expect(await textX(drawer.locator('[data-app-section="functions"]'))).toBe(fav);
    // a child one level in from its parent's label
    expect(await textX(drawer.locator('[data-app-node="rooms/wohnzimmer"]'))).toBeGreaterThan(fav);
    // the triangle at the right end of the row, drawn, turned when open
    const rooms = drawer.locator('[data-app-section="rooms"]');
    const caret = rooms.locator('svg.app-caret');
    await expect(caret).toHaveAttribute('data-app-caret', 'open');
    const rb = (await rooms.boundingBox())!;
    const cb = (await caret.boundingBox())!;
    expect(cb.x + cb.width).toBeGreaterThan(rb.x + rb.width - 20);
    await expect(caret).toHaveCSS('stroke-linejoin', 'round');
    // folding and unfolding: the row keeps its box; the nested node's triangle is on the right too
    const og = drawer.locator('[data-app-node="rooms/og"]').locator('xpath=..');
    const ogBox = (await og.boundingBox())!;
    const ogFold = og.locator('.app-fold-node');
    await expect(ogFold.locator('svg.app-caret')).toBeVisible();
    const ogCaret = (await ogFold.locator('svg.app-caret').boundingBox())!;
    expect(Math.abs(ogCaret.x + ogCaret.width - (cb.x + cb.width))).toBeLessThanOrEqual(1); // one column of triangles
    expect((await ogFold.boundingBox())!.x).toBeGreaterThan((await drawer.locator('[data-app-node="rooms/og"]').boundingBox())!.x);
    const wasOpen = (await ogFold.getAttribute('aria-expanded')) === 'true';
    await ogFold.click();
    await expect(ogFold).toHaveAttribute('aria-expanded', String(!wasOpen));
    expect(await og.boundingBox()).toEqual(ogBox);
    const before = (await rooms.boundingBox())!;
    await rooms.click();
    await expect(caret).toHaveAttribute('data-app-caret', 'closed');
    expect(await rooms.boundingBox()).toEqual(before);
    await rooms.click();
    // the old address of the entry: the Status page's list
    await page.goto('/app/messages');
    await expect(page).toHaveURL(/\/#service-messages$/);
});

// the favorites of this account, with ranks, laid over the stub's snapshot and state
async function withFavorites(page: import('@playwright/test').Page) {
    await page.route('**/api/auth/v1/state', async (route) => {
        const r = await route.fetch();
        await route.fulfill({response: r, json: {...(await r.json()), level: 'administer', account_id: 'stub0001'}});
    });
    await page.route('**/api/meta/v1/snapshot', async (route) => {
        const r = await route.fetch();
        const snap = await r.json();
        snap.enums.favorite = {name: {de: 'Favoriten', en: 'Favorites'}, tree: [{id: 'stub0001', name: 'admin'}]};
        const add = (ref: string, rank?: number) => {
            const o = snap.objects[ref];
            o.enums = [...o.enums, 'favorite/stub0001'];
            if (rank !== undefined) o.meta = {...o.meta, occulite: {order: {'favorite/stub0001': rank}}};
        };
        add('NEQ0123456:1', 1000);
        add('NEQ0123456:2', 2000);
        add('MEQ0987654:2', 3000);
        await route.fulfill({response: r, json: snap});
    });
}

test('favorites: a very-long press sorts by dragging, released in place it opens the assignment', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await withFavorites(page);
    await page.goto('/app/favorites');
    const tiles = page.locator('[data-app-tile]');
    await expect(tiles).toHaveCount(3);
    await expect(tiles.nth(0)).toContainText('Wohnzimmer Deckenlicht');
    // the third card dragged onto the first: it takes the place before it, the midpoint below 1000
    const third = (await tiles.nth(2).boundingBox())!;
    const first = (await tiles.nth(0).boundingBox())!;
    await page.mouse.move(third.x + third.width - 20, third.y + third.height - 15);
    await page.mouse.down();
    await page.waitForTimeout(1400);
    await expect(page.locator('[data-app-sorting="1"]')).toHaveCount(1);
    await page.mouse.move(first.x + 20, first.y + first.height - 15, {steps: 8});
    await expect(page.locator('[data-app-tile]').first()).toContainText('Küche Licht');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/objects/MEQ0987654%3A2')),
        page.mouse.up(),
    ]);
    expect(req.postDataJSON()).toEqual({meta: {occulite: {order: {'favorite/stub0001': 500}}}});
    // released in place: the assignment sheet, with the favorite ticked
    const box = (await tiles.nth(1).boundingBox())!;
    await page.mouse.move(box.x + box.width - 20, box.y + box.height - 15);
    await page.mouse.down();
    await page.waitForTimeout(1400);
    await page.mouse.up();
    await expect(page.getByRole('dialog').locator('[data-assign="favorite"]').getByRole('checkbox')).toBeChecked();
    await page.keyboard.press('Escape');
});

test('rooms and functions: added, renamed and deleted from the sheet at the drawer\'s foot', async ({page, isMobile}, info) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    const name = `Neu ${info.project.name}`;
    const id = `neu-${info.project.name}`;
    await page.goto('/app');
    await page.locator('[data-app-entry="taxonomy"]').click();
    const sheet = page.getByRole('dialog');
    await expect(sheet).toContainText('Rooms and functions');
    await expect(sheet.locator('[data-taxonomy-node="rooms/og/bad"]')).toContainText('Bad');
    const rooms = sheet.locator('[data-taxonomy="rooms"]');
    await rooms.getByLabel('New entry in Rooms').fill(name);
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/meta/v1/enums/rooms/nodes')),
        rooms.getByRole('button', {name: 'Add', exact: true}).click(),
    ]);
    expect(req.postDataJSON()).toEqual({parent: null, id, name});
    const row = sheet.locator(`[data-taxonomy-node="rooms/${id}"]`);
    await expect(row).toContainText(name);
    // renamed through its row
    await row.getByRole('button', {name: `Rename ${name}`}).click();
    await page.getByRole('dialog').last().getByLabel('Name').fill(`${name} 2`);
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith(`/api/meta/v1/enums/rooms/nodes/${id}`)),
        page.getByRole('dialog').last().getByRole('button', {name: 'Rename'}).click(),
    ]);
    expect(req2.postDataJSON()).toEqual({name: `${name} 2`});
    await expect(row).toContainText(`${name} 2`);
    // the drawer follows
    await expect(page.locator(`[data-app-node="rooms/${id}"]`)).toContainText(`${name} 2`);
    // deleted, after the question, detaching the channels
    await row.getByRole('button', {name: `Delete ${name} 2`}).click();
    await expect(page.getByRole('dialog').last()).toContainText('No channel is assigned here.');
    const [req3] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'DELETE' && r.url().includes(`/api/meta/v1/enums/rooms/nodes/${id}`)),
        page.getByRole('dialog').last().getByRole('button', {name: 'Delete'}).click(),
    ]);
    expect(req3.url()).toContain('members=detach');
    await expect(row).toHaveCount(0);
});

// occulited task 92 (S6): the corner shows the most severe message's Lucide symbol only, always the
// same size; every message is in its tooltip and in the sheet
test('the service-message corner: the most severe message only, all of them in the tooltip and the sheet, and the rollup in the drawer', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    const msgs = ['LOW_BAT', 'CONFIG_PENDING', 'UPDATE_PENDING', 'UNREACH'].map((key) => ({interface: 'HmIP-RF', address: '000DD8', channel: '0', key, value: true, since: '2026-09-22T12:00:00Z', seen: 'event', type: 'HmIP-PDT'}));
    await page.route('**/api/system/v1/service-messages', (r) => r.fulfill({json: {count: msgs.length, messages: msgs, swept: '2026-09-22T12:00:00Z', errors: {}}}));
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    const svc = lamp.locator('[data-app-svc]');
    await expect(svc).toHaveAttribute('data-app-svc', 'UNREACH');
    await expect(svc).toHaveAttribute('data-app-svc-kind', 'unreach');
    await expect(svc).toHaveAttribute('data-app-svc-count', '4');
    await expect(svc).toHaveAttribute('title', 'not reachable, Low battery, Configuration pending, Update pending');
    await expect(svc.locator('svg')).toHaveCount(1);
    await expect(svc.locator('svg')).toHaveAttribute('stroke-width', '2.5');
    // a fixed 34 px corner at the tile's top right, its symbol 12 px
    const tb = (await lamp.boundingBox())!;
    const sb = (await svc.boundingBox())!;
    expect(Math.round(sb.width)).toBe(34);
    expect(Math.round(sb.height)).toBe(34);
    expect(Math.round(sb.x + sb.width)).toBe(Math.round(tb.x + tb.width) - 1);
    expect(Math.round(sb.y)).toBe(Math.round(tb.y) + 1);
    expect(Math.round((await svc.locator('svg').boundingBox())!.width)).toBe(12);
    // no corner on the key next to it (another device)
    await expect(page.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"] [data-app-svc]')).toHaveCount(0);
    // the rollup: Bad and Obergeschoss carry the dot, Wohnzimmer not
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(1);
    await expect(page.locator('[data-app-node="rooms/og"] [data-app-problem]')).toHaveCount(1);
    await expect(page.locator('[data-app-node="rooms/wohnzimmer"] [data-app-problem]')).toHaveCount(0);
    await expect(page.locator('[data-app-title] .app-dot')).toHaveCount(1);
    // the corner opens the sheet, which lists every message, the most severe first
    await svc.click();
    const sheet = page.locator('.ol-modal.app-sheet');
    await expect(sheet.locator('[data-sheet-message]')).toHaveCount(4);
    await expect(sheet.locator('[data-sheet-message]').first()).toHaveAttribute('data-sheet-message', 'UNREACH');
    await expect(sheet.locator('[data-sheet="messages"]')).toContainText('Low battery');
    await page.keyboard.press('Escape');
    // the most severe one goes: the corner takes the next, in the same box
    msgs.splice(3, 1);
    await page.reload();
    await expect(svc).toHaveAttribute('data-app-svc', 'LOW_BAT');
    await expect(svc).toHaveAttribute('data-app-svc-kind', 'lowbat');
    expect(await svc.boundingBox()).toEqual(sb);
});

// occulited B-46: a device that answers again but whose outage nobody acknowledged (STICKY_UNREACH
// without UNREACH) is not shown as unreachable: a mild badge, not the filled one, no dot in the drawer - and
// the event that clears UNREACH turns the filled badge into the mild one on an open page
test('STICKY_UNREACH is not shown in Control: no corner, no dot; UNREACH is', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    const msg = (key: string) => ({interface: 'HmIP-RF', address: '000DD8', channel: '0', key, value: true, since: '2026-09-22T12:00:00Z', seen: 'event', type: 'HmIP-PDT'});
    let msgs = [msg('STICKY_UNREACH')];
    await page.route('**/api/system/v1/service-messages', (r) => r.fulfill({json: {count: msgs.length, messages: msgs, swept: '2026-09-22T12:00:00Z', errors: {}}}));
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    const svc = lamp.locator('[data-app-svc]');
    await expect(lamp).toHaveAttribute('data-app-widget', 'dimmer');
    await expect(svc).toHaveCount(0);
    // nothing is wrong now: no dot on the room, its parent or the title
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(0);
    await expect(page.locator('[data-app-node="rooms/og"] [data-app-problem]')).toHaveCount(0);
    await expect(page.locator('[data-app-title] .app-dot')).toHaveCount(0);
    // and not in the sheet
    await lamp.locator('.app-tile-name').click();
    await expect(page.locator('.ol-modal.app-sheet')).toBeVisible();
    await expect(page.locator('.ol-modal.app-sheet [data-sheet="messages"]')).toHaveCount(0);
    await page.keyboard.press('Escape');
    // in German; both set: the device is away now - UNREACH alone, and the dot
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    msgs = [msg('UNREACH'), msg('STICKY_UNREACH')];
    await page.reload();
    await expect(svc).toHaveAttribute('data-app-svc', 'UNREACH');
    await expect(svc).toHaveAttribute('data-app-svc-count', '1');
    await expect(svc).toHaveAttribute('title', 'nicht erreichbar');
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(1);
    // it answers again, the acknowledgement is still missing: nothing in Control
    msgs = [msg('STICKY_UNREACH')];
    await page.reload();
    await expect(lamp).toHaveAttribute('data-app-widget', 'dimmer');
    await expect(svc).toHaveCount(0);
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(0);
});

// occulited task 92: every tile has the same box - across kinds and states, with and without the
// corner, with a long name and with a tinted state - on the desktop and on a phone
test('the tiles: one fixed box for every tile, whatever it shows', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    let msgs: object[] = [];
    await page.route('**/api/system/v1/service-messages', (r) => r.fulfill({json: {count: msgs.length, messages: msgs, swept: '2026-09-22T12:00:00Z', errors: {}}}));
    let longName = false;
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        if (longName) json.objects['HmIP-RF.000DD8:3'].name = 'Bad Spiegellampe über dem Waschbecken links neben der Tür zum Flur';
        await r.fulfill({response: res, json});
    });
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    const boxes = async () => page.locator('[data-app-tile]').evaluateAll((els) => els.map((e) => {
        const b = e.getBoundingClientRect();
        return {ref: e.getAttribute('data-app-tile'), w: Math.round(b.width * 10) / 10, h: Math.round(b.height * 10) / 10};
    }));
    // the kinds of Obergeschoss: smoke detectors, a siren, an RGBW light, a meter - one height, one width
    await page.goto('/app/e/rooms/og');
    await expect(page.locator('[data-app-tile="HmIP-RF.000F04:6"]')).toContainText('12.5 W');
    const og = await boxes();
    expect(og.length).toBeGreaterThan(4);
    for (const b of og) expect(b, b.ref!).toMatchObject({w: og[0]!.w, h: 118});
    // Bad: a dimmer, a thermostat (T2E), a key; and the dimmer through its states
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    await expect(lamp).toHaveAttribute('data-app-tone', 'on');
    const bad = await boxes();
    for (const b of bad) expect(b, b.ref!).toMatchObject({w: og[0]!.w, h: 118});
    const before = (await lamp.boundingBox())!;
    const name = (await lamp.locator('.app-tile-name').boundingBox())!;
    await lamp.locator('.app-circle').click();
    await expect(lamp).toHaveAttribute('data-app-tone', '');
    expect(await lamp.boundingBox()).toEqual(before);
    expect(await lamp.locator('.app-tile-name').boundingBox()).toEqual(name);
    // with the corner: the same box, the text kept clear of it
    msgs = [{interface: 'HmIP-RF', address: '000DD8', channel: '0', key: 'LOW_BAT', value: true, since: '2026-09-22T12:00:00Z', seen: 'event', type: 'HmIP-PDT'}];
    longName = true;
    await page.reload();
    await expect(lamp.locator('[data-app-svc]')).toHaveAttribute('data-app-svc', 'LOW_BAT');
    await expect(lamp.locator('.app-tile-name')).toContainText('Waschbecken');
    expect(await lamp.boundingBox()).toEqual(before);
    for (const b of await boxes()) expect(b, b.ref!).toMatchObject({w: og[0]!.w, h: 118});
    const text = (await lamp.locator('.app-tile-text').boundingBox())!;
    const corner = (await lamp.locator('[data-app-svc]').boundingBox())!;
    const nameBox = (await lamp.locator('.app-tile-name').boundingBox())!;
    expect(nameBox.x + nameBox.width).toBeLessThanOrEqual(text.x + text.width - 15);
    expect(nameBox.x + nameBox.width).toBeLessThan(corner.x + corner.width);
    // B-100: no ellipsis - the long name shrinks to its minimum (10 px) and only then is cut, the whole one in its tooltip
    expect(await lamp.locator('.app-tile-name').evaluate((e) => e.scrollWidth > e.clientWidth)).toBe(true);
    await expect(lamp.locator('.app-tile-name')).toHaveCSS('font-size', '10px');
    await expect(lamp.locator('.app-tile-name')).toHaveCSS('text-overflow', 'clip');
    expect(await lamp.locator('.app-tile-name').boundingBox()).toMatchObject({height: name.height});
    await expect(lamp.locator('.app-tile-name')).toHaveAttribute('title', /Waschbecken links neben der Tür zum Flur$/);
    // the thermostat (T2E) with a valve: the bar and its percent keep their places from 0 to 100 %, the tile its box
    const thermo = page.locator('[data-app-tile="HmIP-RF.000ABC:1"]');
    const thermoBox = (await thermo.boundingBox())!;
    const level = (v: number) => page.request.post('/api/rpc/v1/json/HmIP-RF', {data: [{jsonrpc: '2.0', method: 'setValue', params: ['000ABC:1', 'LEVEL', v], id: 1}]});
    await level(0);
    await expect(thermo.locator('[data-app-valve]')).toHaveText('0 %');
    await expect(thermo).toHaveAttribute('data-app-tone', '');
    const bar0 = (await thermo.locator('.app-vbar').boundingBox())!;
    const pct0 = (await thermo.locator('[data-app-valve]').boundingBox())!;
    await level(1);
    await expect(thermo.locator('[data-app-valve]')).toHaveText('100 %');
    await expect(thermo).toHaveAttribute('data-app-tone', 'on');
    expect(await thermo.locator('.app-vbar').boundingBox()).toEqual(bar0);
    expect(await thermo.locator('[data-app-valve]').boundingBox()).toEqual(pct0);
    expect(await thermo.boundingBox()).toEqual(thermoBox);
    // task 97: with a valve too, the thermometer (filled from the room's 20.2 °C), no gauge - on the tile and in the sheet
    await expect(thermo.locator('.app-circle')).toHaveAttribute('data-icon', 'thermometer:63');
    await level(0);
    await expect(thermo.locator('.app-circle')).toHaveAttribute('data-icon', 'thermometer:63');
    await thermo.locator('.app-tile-name').click();
    await expect(page.locator('.sh-circle')).toHaveAttribute('data-icon', 'thermometer:63');
});

// occulited B-100: a line that does not fit shrinks its font instead of an ellipsis, keeping its
// height; the thermostat's Ist/Soll pair is fitted the same way; a thermostat without a valve and a
// key without a state line have their content centred
test('the tiles fit their text: smaller type, never an ellipsis, the boxes kept (B-100)', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    // task 103: the stub's wall thermostat without boost joins Bad - the centred pair is its (a thermostat that can boost keeps a row)
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['HmIP-RF.000ABD:1'].enums = ['rooms/og/bad'];
        await r.fulfill({response: res, json});
    });
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    await expect(lamp.locator('[data-app-state]')).toHaveText('On · 60 %');
    // every line of every tile fits: no overflow, no ellipsis
    const lines = page.locator('[data-app-tile] .app-tile-name, [data-app-tile] [data-app-state], [data-app-tile] [data-app-thermo]');
    for (const el of await lines.all()) {
        if ((await el.textContent())?.length && !(await el.getAttribute('title'))?.startsWith('actually')) {
            expect(await el.evaluate((e) => e.scrollWidth <= e.clientWidth + 0.5), await el.textContent() ?? '').toBe(true);
            expect(await el.evaluate((e) => getComputedStyle(e).textOverflow)).toBe('clip');
        }
    }
    // the state's line keeps its height whatever its font size
    await expect(lamp.locator('[data-app-state]')).toHaveCSS('height', '25px');
    // the thermostat: the Soll value stays inside the tile, clear of its right padding
    const boostable = page.locator('[data-app-tile="HmIP-RF.000ABC:1"]');
    const bb = (await boostable.boundingBox())!;
    const bset = (await boostable.locator('[data-app-set]').boundingBox())!;
    expect(bset.x + bset.width).toBeLessThanOrEqual(bb.x + bb.width - 10);
    // task 103 ("reserve the row"): a thermostat that can boost keeps an empty row where the countdown will be
    await expect(boostable.locator('[data-app-th-row][data-app-th-reserved]')).toHaveCount(1);
    // B-100 point 3: without a valve (and a window), and without boost, there is no row, the pair is centred.
    // occulited task 108: this wall thermostat and the valve above share the channel type name
    // HEATING_CLIMATECONTROL_TRANSCEIVER; the descriptions come per device model from the system's cache, so the
    // wall thermostat keeps its own (no BOOST_MODE) beside the valve's on the same page
    const thermo = page.locator('[data-app-tile="HmIP-RF.000ABD:1"]');
    await expect(thermo).toHaveAttribute('data-app-widget', 'thermostat');
    const tb = (await thermo.boundingBox())!;
    await expect(thermo.locator('[data-app-th-row]')).toHaveCount(0);
    await expect(thermo.locator('.app-circle-act')).toHaveCount(0); // no boost: no button, no hover
    const text = (await thermo.locator('.app-tile-text').boundingBox())!;
    const nameTop = (await thermo.locator('.app-tile-name').boundingBox())!.y;
    const pairBottom = await thermo.locator('[data-app-thermo]').evaluate((e) => e.getBoundingClientRect().bottom);
    expect(Math.abs(nameTop - text.y)).toBeLessThanOrEqual(0.5);
    expect(Math.abs(pairBottom - (text.y + text.height))).toBeLessThanOrEqual(0.5);
    expect(Math.abs(text.y + text.height / 2 - (tb.y + tb.height / 2))).toBeLessThanOrEqual(1.5);
    // B-100 point 4: a key has no state line, its name is centred and stays where it is on a press
    const key = page.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"]');
    const kb = (await key.boundingBox())!;
    const kn = (await key.locator('.app-tile-name').boundingBox())!;
    expect(Math.abs(kn.y + kn.height / 2 - (kb.y + kb.height / 2))).toBeLessThanOrEqual(1.5);
    await key.locator('.app-circle').click();
    await expect(key.locator('[data-app-state]')).toHaveText('short press');
    expect(await key.locator('.app-tile-name').boundingBox()).toEqual(kn);
    expect(await key.boundingBox()).toEqual(kb);
    // a narrow tile (a phone held upright, 360 px): the dimmer's state shrinks to fit, the box stays
    await page.setViewportSize({width: 360, height: 800});
    await expect.poll(() => lamp.locator('[data-app-state]').evaluate((e) => e.scrollWidth <= e.clientWidth + 0.5)).toBe(true);
    await expect(lamp).toHaveCSS('height', '118px');
    await expect(lamp.locator('[data-app-state]')).toHaveCSS('height', '25px');
    await expect(lamp.locator('[data-app-state]')).not.toHaveAttribute('title');
});

// occulited task 95: contacts and motion detectors say how long they are in their state - a small
// line under the state, reserved on every tile; nothing while the time is not known; recomputed on
// the full minute without fetching; a change over the stream starts it anew; the sheet has the exact time
test('contacts and motion detectors: how long, live every minute (task 95)', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['HmIP-RF.000C01:1'] = {name: 'Fenster Bad', enums: ['rooms/og/bad'], meta: {}};
        json.objects['HmIP-RF.000C02:1'] = {name: 'Tür Bad', enums: ['rooms/og/bad'], meta: {}};
        json.objects['HmIP-RF.000C03:1'] = {name: 'Bewegung Bad', enums: ['rooms/og/bad'], meta: {}};
        json.objects['HmIP-RF.000C04:1'] = {name: 'Bewegung Flur vor dem Bad und der Treppe', enums: ['rooms/og/bad'], meta: {}};
        await r.fulfill({response: res, json});
    });
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    // the page's clock half a minute ahead of the stub's, so the minutes are not on an edge
    await page.clock.install({time: Date.now() + 30_000});
    await page.goto('/app/e/rooms/og/bad');
    const tile = (a: string) => page.locator(`[data-app-tile="HmIP-RF.${a}"]`);
    await expect(tile('000C01:1').locator('[data-app-state]')).toHaveText('Open');
    await expect(tile('000C01:1').locator('.app-tile-dur')).toHaveText('for 3 h 12 min');
    await expect(tile('000C03:1').locator('[data-app-state]')).toHaveText('No motion');
    await expect(tile('000C03:1').locator('.app-tile-dur')).toHaveText('last 14 min ago');
    await expect(tile('000C04:1').locator('[data-app-state]')).toHaveText('Motion');
    await expect(tile('000C04:1').locator('.app-tile-dur')).toHaveText('for 2 min');
    // unknown: nothing, not a guess
    await expect(tile('000C02:1').locator('[data-app-state]')).toHaveText('Closed');
    await expect(tile('000C02:1').locator('.app-tile-dur')).toHaveText('');
    // the line is reserved on every tile with a state, so the states align; every box is the same
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    await expect(lamp.locator('.app-tile-dur')).toHaveText('');
    const y = async (l: import('@playwright/test').Locator) => (await l.boundingBox())!.y - (await l.locator('xpath=ancestor::*[@data-app-tile]').boundingBox())!.y;
    expect(await y(tile('000C01:1').locator('[data-app-state]'))).toBe(await y(lamp.locator('[data-app-state]')));
    expect(await y(tile('000C02:1').locator('[data-app-state]'))).toBe(await y(lamp.locator('[data-app-state]')));
    for (const a of ['000C01:1', '000C02:1', '000C03:1', '000C04:1']) {
        await expect(tile(a)).toHaveCSS('height', '118px');
        await expect(tile(a).locator('.app-tile-dur')).toHaveCSS('height', '17px');
        expect(await tile(a).locator('.app-tile-dur, .app-tile-name, [data-app-state]').evaluateAll((els) => els.filter((e) => !(e.scrollWidth <= e.clientWidth + 0.5 || e.hasAttribute('title'))).map((e) => `${e.className} ${e.textContent} ${e.scrollWidth}/${e.clientWidth} ${getComputedStyle(e).fontSize}`))).toEqual([]);
    }
    const box = (await tile('000C01:1').boundingBox())!;
    const stateBox = (await tile('000C01:1').locator('[data-app-state]').boundingBox())!;
    // the full minute: recomputed without a request
    let reads = 0;
    // (the stream may reconnect when the jump outruns its heartbeat; values are not read)
    page.on('request', (r) => { if (/\/api\/rpc\/v1\/(state|json)/.test(r.url())) reads++; });
    await page.clock.runFor(60_000);
    await expect(tile('000C01:1').locator('.app-tile-dur')).toHaveText('for 3 h 13 min');
    await expect(tile('000C03:1').locator('.app-tile-dur')).toHaveText('last 15 min ago');
    expect(reads).toBe(0);
    expect(await tile('000C01:1').boundingBox()).toEqual(box);
    expect(await tile('000C01:1').locator('[data-app-state]').boundingBox()).toEqual(stateBox);
    // the sheet: the duration and the exact time
    await tile('000C01:1').click();
    await expect(page.locator('[data-sheet-since]')).toHaveText(/^for 3 h 13 min · since \d\d\/\d\d, \d\d:\d\d$/);
    await page.keyboard.press('Escape');
    await expect(page.locator('[data-sheet-since]')).toHaveCount(0);
    // a change over the stream: the time starts anew, and the tile keeps its box
    await page.clock.setSystemTime(Date.now());
    await page.clock.resume();
    await page.request.post('/api/rpc/v1/json/HmIP-RF', {data: [{jsonrpc: '2.0', method: 'setValue', params: ['000C02:1', 'STATE', 1], id: 1}]});
    await expect(tile('000C02:1').locator('[data-app-state]')).toHaveText('Open');
    await expect(tile('000C02:1').locator('.app-tile-dur')).toHaveText('for under 1 min');
    await expect(tile('000C02:1')).toHaveCSS('height', '118px');
});

// occulited task 96: the device sheet's gear (left of close) opens the same assignment sheet as the
// very-long press, on top of it: closing it returns to the device sheet; only for an account that may
test('the device sheet\'s gear opens the assignment sheet; a saved name shows on the tile (task 96)', async ({page, baseURL}) => {
    const me = `dn-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}, {name: 'stub-dn', value: me, url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    // the tile opens its sheet once its channel is loaded (a slow runner clicked before)
    await expect(lamp).toHaveAttribute('data-app-widget', 'dimmer');
    await lamp.locator('.app-tile-name').click();
    const sheet = page.locator('.ol-modal.app-sheet').first();
    await expect(page.getByRole('dialog')).toHaveCount(1);
    const gear = page.locator('[data-sheet-assign]');
    await expect(gear).toHaveAttribute('aria-label', 'Name, rooms and functions');
    // left of the close button, its size (once the sheet has grown out of the tile)
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'));
    const close = page.getByRole('dialog').getByRole('button', {name: 'Close'});
    const g = (await gear.boundingBox())!;
    const c = (await close.boundingBox())!;
    expect(g.x + g.width).toBeLessThanOrEqual(c.x);
    expect(Math.abs(g.y - c.y)).toBeLessThanOrEqual(1);
    expect({w: g.width, h: g.height}).toEqual({w: c.width, h: c.height});
    // the name stays clear of the gear
    const nm = (await page.locator('.sh-name').boundingBox())!;
    expect(nm.x + nm.width).toBeLessThanOrEqual(g.x);
    await gear.focus();
    await page.keyboard.press('Enter');
    const assign = page.locator('.ol-modal.app-assign');
    await expect(assign).toContainText('Bad Spiegellampe');
    const display = `Spiegel ${me}`;
    await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/display-names/HmIP-RF.000DD8%3A3')),
        assign.locator('[data-assign-display]').fill(display).then(() => assign.locator('[data-assign-display-save]').click()),
    ]);
    // closing it returns to the device sheet
    await assign.getByRole('button', {name: 'Close'}).click();
    await expect(assign).toHaveCount(0);
    await expect(sheet).toBeVisible();
    await expect(page.locator('[data-sheet-assign]')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(lamp.locator('.app-tile-name')).toHaveText(display);
    // an operate account: no gear, the header as before
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'kid', role: 'user', level: 'operate', account_id: 'deadbeef', must_change_password: false, sid: 'K1'}}));
    await page.reload();
    await expect(lamp).toHaveAttribute('data-app-widget', 'dimmer');
    await lamp.locator('[data-app-state]').click();
    await expect(page.getByRole('dialog')).toHaveCount(1);
    await expect(page.locator('[data-sheet-assign]')).toHaveCount(0);
});

// occulited task 99: a switch's icon - a keyword of its name by default, chosen in the assignment sheet's
// "Symbol" picker, kept in the metadata (control namespace) across a reload; "Automatic" brings the default back
test('a switch tile: the keyword icon, a chosen one from the picker, kept in the metadata (task 99)', async ({page, isMobile}, info) => {
    test.skip(!!isMobile, 'the very-long press with the mouse');
    const addr = {'desktop-light': '000E01:3', 'desktop-dark': '000E02:3'}[info.project.name] ?? '000E03:3';
    const ref = `HmIP-RF.${addr}`;
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects[ref] = {meta: {}, ...json.objects[ref], name: 'Steckdosen Werkstatt', enums: ['rooms/og/bad']};
        await r.fulfill({response: res, json});
    });
    // a clean start: no icon chosen by an earlier run against the same stub
    await page.request.patch(`/api/meta/v1/objects/${encodeURIComponent(ref)}`, {data: {meta: {control: null}}});
    await page.goto('/app/e/rooms/og/bad');
    const tile = page.locator(`[data-app-tile="${ref}"]`);
    // the keyword: a socket
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', /^message_socket/);
    const box = (await tile.boundingBox())!;
    const longPress = async () => {
        const b = (await tile.boundingBox())!;
        await page.mouse.move(b.x + b.width - 20, b.y + b.height - 20);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
    };
    await longPress();
    const sheet = page.locator('.ol-modal.app-assign');
    const picker = sheet.locator('[data-assign="icon"]');
    // task 110: the icon section starts folded, its row showing the icons in use (off, on: the socket's pair)
    const fold = sheet.locator('[data-assign-icons-fold]');
    await expect(fold).toHaveAttribute('data-assign-icons-fold', 'closed');
    await expect(fold).toHaveAttribute('aria-expanded', 'false');
    await expect(picker).toHaveCount(0);
    await expect(fold.locator('[data-assign-current-icon]')).toHaveCount(2);
    await expect(fold.locator('[data-assign-current-icon]').first()).toHaveAttribute('data-assign-current-icon', 'message_socket');
    await expect(fold.locator('[data-assign-current-icon]').last()).toHaveAttribute('data-assign-current-icon', 'message_socket_on_off');
    await fold.click();
    await expect(fold).toHaveAttribute('aria-expanded', 'true');
    // task 110: two states, Off left and On right; Off chosen first
    await expect(picker.locator('[data-assign-states]')).toHaveAttribute('data-assign-states', '2');
    const offTab = picker.locator('[data-assign-state="off"]');
    const onTab = picker.locator('[data-assign-state="on"]');
    expect((await offTab.boundingBox())!.x).toBeLessThan((await onTab.boundingBox())!.x);
    await expect(offTab).toHaveAttribute('aria-selected', 'true');
    await expect(picker.locator('[data-icon-cell=""]')).toHaveAttribute('aria-checked', 'true');
    // task 102: the 33 suggestions and "Automatisch" on top
    await expect(picker.locator('[data-icon-group="suggested"] [data-icon-cell]')).toHaveCount(34);
    // the picture for the tile's state now
    const state = (await tile.getAttribute('data-app-on')) === '1' ? 'on' : 'off';
    await picker.locator(`[data-assign-state="${state}"]`).click();
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith(`/api/meta/v1/objects/${encodeURIComponent(ref)}`)),
        picker.locator('[data-icon-cell="vent_ventilation"]').click(),
    ]);
    expect(req.postDataJSON()).toEqual({meta: {control: {icons: {[state]: 'vent_ventilation'}}}});
    await expect(picker.locator('[data-icon-cell="vent_ventilation"]')).toHaveAttribute('aria-checked', 'true');
    await expect(picker.locator(`[data-assign-state="${state}"]`)).toHaveAttribute('data-assign-state-icon', 'vent_ventilation');
    await page.keyboard.press('Escape');
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'vent_ventilation');
    expect(await tile.boundingBox()).toEqual(box);
    // kept across a reload
    await page.reload();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'vent_ventilation');
    // the other state keeps its automatic picture
    await tile.locator('.app-circle').click();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', /^message_socket/);
    await tile.locator('.app-circle').click();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'vent_ventilation');
    // task 99's single icon (an older choice) is the picture of both states
    await page.request.patch(`/api/meta/v1/objects/${encodeURIComponent(ref)}`, {data: {meta: {control: {icon: 'sani_pump'}}}});
    await page.reload();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'sani_pump');
    await tile.locator('.app-circle').click();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'sani_pump');
    // back to automatic
    await page.request.patch(`/api/meta/v1/objects/${encodeURIComponent(ref)}`, {data: {meta: {control: null}}});
    await page.reload();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', /^message_socket/);
});

// occulited task 100: a switch tile opens a sheet (the circle still switches at once) with two large
// buttons of one size, the state's lit; the service messages and the gear as on every sheet
test('a switch tile opens its sheet: big on and off, the tile follows (task 100)', async ({page, baseURL}, info) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    const addr = {'desktop-light': '000E01:3', 'desktop-dark': '000E02:3'}[info.project.name] ?? '000E03:3';
    const ref = `HmIP-RF.${addr}`;
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects[ref] = {meta: {}, ...json.objects[ref], name: 'Licht Werkstatt', enums: ['rooms/og/bad']};
        await r.fulfill({response: res, json});
    });
    await page.route('**/api/system/v1/service-messages', (r) => r.fulfill({json: {count: 1, messages: [{interface: 'HmIP-RF', address: addr.split(':')[0], channel: '0', key: 'LOW_BAT', value: true, since: '2026-09-22T12:00:00Z', seen: 'event', type: 'HmIP-BSM'}], swept: '2026-09-22T12:00:00Z', errors: {}}}));
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    await page.goto('/app/e/rooms/og/bad');
    const tile = page.locator(`[data-app-tile="${ref}"]`);
    await expect(tile).toHaveAttribute('data-app-widget', 'switch');
    const was = await tile.getAttribute('data-app-on');
    // the circle switches directly, no sheet
    await tile.locator('.app-circle').click();
    await expect(tile).toHaveAttribute('data-app-on', was === '1' ? '0' : '1');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await tile.locator('.app-circle').click();
    await expect(tile).toHaveAttribute('data-app-on', was ?? '0');
    // the tile opens the sheet
    await tile.locator('.app-tile-name').click();
    const sheet = page.getByRole('dialog');
    await expect(sheet.locator('[data-sheet="switch"]')).toBeVisible();
    await expect(sheet.locator('[data-sheet-message="LOW_BAT"]')).toBeVisible();
    await expect(sheet.locator('[data-sheet-assign]')).toBeVisible();
    const onBtn = sheet.locator('[data-sheet-on]');
    const offBtn = sheet.locator('[data-sheet-off]');
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'));
    await offBtn.click();
    await expect(offBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(tile).toHaveAttribute('data-app-on', '0');
    const onBox = (await onBtn.boundingBox())!;
    const offBox = (await offBtn.boundingBox())!;
    expect(onBox.height).toBeGreaterThanOrEqual(64);
    expect(onBox.width).toBe(offBox.width);
    // task 109 (the maintainer): Off left, On right, and the keyboard goes Off, then On
    expect(offBox.x).toBeLessThan(onBox.x);
    await offBtn.focus();
    await page.keyboard.press('Tab');
    await expect(onBtn).toBeFocused();
    // the keyboard: Space on the focused button
    await onBtn.focus();
    await page.keyboard.press('Space');
    await expect(onBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(tile).toHaveAttribute('data-app-on', '1');
    await expect(sheet.locator('[data-sheet-state]')).toHaveText('On');
    // the buttons keep their boxes across the states
    expect(await onBtn.boundingBox()).toEqual(onBox);
    expect(await offBtn.boundingBox()).toEqual(offBox);
    // a write that fails says so in the sheet
    await page.route('**/api/rpc/v1/json/HmIP-RF', (r) => r.fulfill({json: [{jsonrpc: '2.0', error: {code: -1, message: 'device not reachable'}, id: 1}]}));
    await offBtn.click();
    await expect(sheet.locator('.ol-notice.error')).toContainText('device not reachable');
});

// the widget gaps (task 193's parity checklist): colour, the meter's readings, the smoke detector's
// commands and the siren's signal, each over lite-rpc
async function values(page: import('@playwright/test').Page, addr: string): Promise<Record<string, unknown>> {
    const r = await page.request.post('/api/rpc/v1/json/HmIP-RF', {data: [{jsonrpc: '2.0', method: 'getParamset', params: [addr, 'VALUES'], id: 1}]});
    return ((await r.json()) as {result: Record<string, unknown>}[])[0].result;
}

test('an RGBW light: hue, saturation and colour temperature in the sheet, each a setValue', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og');
    const tile = page.locator('[data-app-tile="HmIP-RF.000F03:1"]');
    await expect(tile).toHaveAttribute('data-app-widget', 'color');
    await expect(tile).toContainText('On · 50 %');
    await tile.locator('.app-tile-name').click();
    const sheet = page.locator('.ol-modal.app-sheet');
    await expect(sheet).toBeVisible();
    await expect(sheet.getByLabel('Brightness')).toHaveValue('50');
    await expect(sheet.getByLabel('Hue')).toHaveValue('120');
    await expect(sheet.getByLabel('Saturation')).toHaveValue('100');
    await expect(sheet.getByLabel('Colour temperature')).toHaveValue('3000');
    await sheet.getByLabel('Hue').fill('240');
    await sheet.getByLabel('Colour temperature').fill('4000');
    await expect.poll(() => values(page, '000F03:1')).toMatchObject({HUE: 240, COLOR_TEMPERATURE: 4000});
    await page.keyboard.press('Escape');
});

test('a meter: the readings in the sheet, energy in kWh', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og');
    const tile = page.locator('[data-app-tile="HmIP-RF.000F04:6"]');
    await expect(tile).toHaveAttribute('data-app-widget', 'power');
    await expect(tile).toContainText('12.5 W');
    await tile.click();
    const sheet = page.locator('.ol-modal.app-sheet');
    await expect(sheet.locator('[data-reading="ENERGY_COUNTER"]')).toHaveText('12.35 kWh');
    await expect(sheet.locator('[data-reading="VOLTAGE"]')).toHaveText('230.1 V');
    await expect(sheet.locator('[data-reading="CURRENT"]')).toHaveText('54 mA');
    await expect(sheet.locator('[data-reading="FREQUENCY"]')).toHaveText('50.02 Hz');
    await page.keyboard.press('Escape');
});

test('a smoke detector: quiet on the tile, the alarm test and silence in the sheet', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og');
    const tile = page.locator('[data-app-tile="HmIP-RF.000F01:1"]');
    await expect(tile).toHaveAttribute('data-app-widget', 'smoke');
    await expect(tile).toContainText('Quiet');
    await tile.click();
    const sheet = page.locator('.ol-modal.app-sheet');
    await sheet.getByRole('button', {name: 'Alarm test', exact: true}).click();
    await expect.poll(() => values(page, '000F01:1')).toMatchObject({SMOKE_DETECTOR_COMMAND: 3});
    await sheet.getByRole('button', {name: 'Silence', exact: true}).click();
    await expect.poll(() => values(page, '000F01:1')).toMatchObject({SMOKE_DETECTOR_COMMAND: 1});
    await page.keyboard.press('Escape');
});

test('a BidCos smoke detector (HM-Sec-SD-2, DANGER.STATE only): the smoke tile, quiet, the smoke icon (B-98)', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og');
    const tile = page.locator('[data-app-tile="BidCos-RF.NEQ0123459:1"]');
    await expect(tile).toHaveAttribute('data-app-widget', 'smoke');
    await expect(tile).toContainText('Quiet');
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'secur_smoke_detector');
    await expect(tile.locator('.app-circle')).not.toHaveClass(/\balarm\b/);
});

test('a siren: sound, light and duration go out as one putParamset; Stop sends the off values', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og');
    const tile = page.locator('[data-app-tile="HmIP-RF.000F02:3"]');
    await expect(tile).toHaveAttribute('data-app-widget', 'signal');
    await expect(tile).toContainText('Quiet');
    await tile.click();
    const sheet = page.locator('.ol-modal.app-sheet');
    await sheet.getByLabel('Sound').selectOption({label: 'Falling'});
    await sheet.getByLabel('Light').selectOption({label: 'Double flashing'});
    await sheet.getByLabel('Duration').selectOption({label: '2 min'});
    await sheet.getByRole('button', {name: 'Trigger', exact: true}).click();
    await expect.poll(() => values(page, '000F02:3')).toMatchObject({ACOUSTIC_ALARM_SELECTION: 2, OPTICAL_ALARM_SELECTION: 3, DURATION_VALUE: 2, DURATION_UNIT: 1});
    await sheet.getByRole('button', {name: 'Stop', exact: true}).click();
    await expect.poll(() => values(page, '000F02:3')).toMatchObject({ACOUSTIC_ALARM_SELECTION: 0, OPTICAL_ALARM_SELECTION: 0, DURATION_VALUE: 0, DURATION_UNIT: 0});
    await page.keyboard.press('Escape');
});

// task 193: the public principal - the Control app without a login, and nothing else of the shell
test('public mode: the App without a login, the Control tab alone, the login page for everything else', async ({page, baseURL, isMobile}) => {
    await page.context().addCookies([{name: 'stub-public', value: '1', url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    await expect(page.locator('[data-app-title]')).toHaveText('Bad');
    await expect(page.locator('[data-app-tile="HmIP-RF.000DD8:3"]')).toContainText('On · 60 %');
    // the bar: Control, the way to sign in; no Status, no Addons, no System, no settings
    const labels = await page.locator('nav.ol-nav > a').allInnerTexts();
    expect(labels.map((l) => l.trim())).toEqual(['Control']);
    await expect(page.locator('nav.ol-nav .ol-systab')).toHaveCount(0);
    await expect(page.locator('[data-public-login]')).toHaveAttribute('href', '/login');
    await expect(page.locator('header a[href="/settings"]')).toHaveCount(0);
    if (!isMobile) {
        // an operator: no unassigned channels, no rooms-and-functions editor at the drawer's foot
        await expect(page.locator('[data-app-entry="unassigned"]')).toHaveCount(0);
    }
    // Status is the login page, with the way back to Control
    await page.goto('/');
    await expect(page.getByRole('heading', {level: 1, name: 'Login'})).toBeVisible();
    await expect(page.locator('[data-login-public] a')).toHaveAttribute('href', '/app');
    await page.goto('/system/users');
    await expect(page.getByRole('heading', {level: 1, name: 'Login'})).toBeVisible();
});

// occulited task 18 (the maintainer, 2026-10-04): rooms always come first in the drawer, functions
// second, in either language and whatever order the snapshot brings the enums in - the box's own
// ids are "room" and "function", which Go's sorted map keys put the other way round; another
// enum follows them
for (const lang of ['en', 'de'] as const) {
    test(`the drawer lists rooms before functions (${lang})`, async ({page, isMobile}) => {
        test.skip(!!isMobile, 'the drawer is behind the button on a phone');
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        await page.route('**/api/meta/v1/snapshot', async (route) => {
            const r = await route.fetch();
            const snap = await r.json();
            const {rooms, functions} = snap.enums;
            snap.enums = {etagen: {name: {de: 'Etagen', en: 'Floors'}, tree: [{id: 'eg', name: 'EG'}]}, function: functions, room: rooms};
            await route.fulfill({response: r, json: snap});
        });
        await page.goto('/app');
        const sections = page.locator('[data-app-drawer] [data-app-section]');
        await expect(sections).toHaveText(lang === 'de' ? [/Räume/, /Gewerke/, /Etagen/] : [/Rooms/, /Functions/, /Floors/]);
        await expect(sections.nth(0)).toHaveAttribute('data-app-section', 'room');
        await expect(sections.nth(1)).toHaveAttribute('data-app-section', 'function');
    });
}

// occulited B-84 (the maintainer's system: "System: no such interface: System" over the unassigned
// channels): objects of ReGa's System pseudo-interface, and of an interface lite-rpc does not have,
// are no tiles and no error; the other channels are there as before
test('the unassigned channels: no tile and no error for System and an interface the system does not have', async ({page}) => {
    await page.route('**/api/meta/v1/snapshot', async (route) => {
        const response = await route.fetch();
        const snap = await response.json();
        snap.objects['System.BidCoS-RF:1'] = {name: 'Zentrale (ReGa)', enums: [], meta: {}};
        snap.objects['CUxD.CUX2801001:1'] = {name: 'CUxD Exec', enums: [], meta: {}};
        snap.objects['HmIP-RF.000F05:3'] = {name: 'Abstellraum Licht', enums: [], meta: {}};
        await route.fulfill({response, json: snap});
    });
    const asked: string[] = [];
    await page.route('**/api/rpc/v1/json/**', async (route) => {
        const iface = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop()!);
        asked.push(iface);
        if (iface === 'CUxD' || iface === 'System') return route.fulfill({status: 404, json: {error: 'unknown-interface', message: `no such interface: ${iface}`}});
        return route.fallback();
    });
    await page.goto('/app/unassigned');
    await expect(page.locator('[data-app-title]')).toHaveText('Unassigned channels');
    await expect(page.locator('[data-app-tile]', {hasText: 'Abstellraum Licht'})).toBeVisible();
    await expect(page.locator('[data-app-tile]', {hasText: 'CUxD Exec'})).toHaveCount(0);
    await expect(page.locator('[data-app-tile]', {hasText: 'Zentrale (ReGa)'})).toHaveCount(0);
    await expect(page.locator('.ol-notice.error')).toHaveCount(0);
    expect(asked).not.toContain('System');
});

// occulited B-84 (the maintainer, 2026-10-20: "never automatic"): under the unassigned channels the
// names of interfaces the system does not list, per interface with the counts; each goes only by
// its button, after the shell's question (danger), and Cancel removes nothing. The button keeps
// its box while the question is open.
test('the unassigned channels: names of interfaces the system does not list go only by the button, after asking', async ({page}) => {
    let foreign = [
        {interface: 'CUxD', objects: 3, devices: 1, channels: 2},
        {interface: 'HmIP-RF', objects: 68, devices: 12, channels: 56},
    ];
    const removed: string[][] = [];
    await page.route('**/api/meta/v1/foreign', (route) => route.fulfill({json: {revision: 42, list_read: true, listed: ['BidCos-RF'], interfaces: foreign}}));
    await page.route('**/api/meta/v1/foreign:remove', async (route) => {
        const body = route.request().postDataJSON() as {interfaces: string[]};
        removed.push(body.interfaces);
        foreign = foreign.filter((f) => !body.interfaces.includes(f.interface));
        await route.fulfill({json: {revision: 43, changed: true, removed: 3}});
    });
    await page.goto('/app/unassigned');
    const panel = page.locator('[data-app-foreign]');
    await expect(panel).toBeVisible();
    await expect(panel.locator('[data-app-foreign-iface]')).toHaveCount(2);
    const cux = panel.locator('[data-app-foreign-iface="CUxD"]');
    await expect(cux).toContainText('3 names (1 device, 2 channels)');
    await expect(panel.locator('[data-app-foreign-iface="HmIP-RF"]')).toContainText('68 names (12 devices, 56 channels)');
    const button = cux.locator('[data-action="foreign-remove"]');
    const before = await button.boundingBox();

    // Cancel: nothing is removed
    await button.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Remove the names of CUxD?');
    await expect(dialog).toContainText('3 names (1 device, 2 channels) are removed from the metadata');
    // the button keeps its box and its place at the right; the dialog's scroll lock may round a scrolled phone page by a pixel
    const during = (await button.boundingBox())!;
    expect(during).toMatchObject({x: before!.x, width: before!.width, height: before!.height});
    expect(Math.abs(during.y - before!.y)).toBeLessThanOrEqual(1);
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(dialog).toHaveCount(0);
    expect(removed).toEqual([]);

    // confirmed: exactly CUxD goes, HmIP-RF stays
    await button.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Remove', exact: true}).click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    expect(removed).toEqual([['CUxD']]);
    await expect(panel.locator('[data-app-foreign-iface]')).toHaveCount(1);
    await expect(panel.locator('[data-app-foreign-iface="HmIP-RF"]')).toBeVisible();
});

test('the unassigned channels: no panel when every interface is listed', async ({page}) => {
    await page.goto('/app/unassigned');
    await expect(page.locator('[data-app-title]')).toHaveText('Unassigned channels');
    await expect(page.locator('[data-app-foreign]')).toHaveCount(0);
});

// occulited task 101: on a room's or function's page its own name is left out of each tile's name;
// an option of the account, on by default; favorites, the tooltip, the aria label and the sheet keep
// the full name
test('name shortening: a room or function page leaves its own name out of the tiles (task 101)', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-prefs', value: `prefs-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/kueche');
    const window = page.locator('[data-app-tile="MEQ0987654:1"] .app-tile-name');
    const light = page.locator('[data-app-tile="MEQ0987654:2"] .app-tile-name');
    await expect(window).toHaveText('Fenster');
    await expect(window).toHaveAttribute('data-app-short', '1');
    await expect(window).toHaveAttribute('title', 'Küche Fenster'); // the tooltip: the full name
    await expect(light).toHaveText('Licht');
    // the aria labels keep the full name, and the sheet shows it (a channel the stub answers over lite-rpc)
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    await expect(lamp).toHaveAttribute('data-app-widget', /./);
    await expect(lamp.locator('.app-tile-name')).toHaveText('Spiegellampe');
    await expect(lamp.getByRole('button', {name: 'Switch Bad Spiegellampe'})).toBeVisible();
    await page.getByRole('button', {name: 'Open Bad Spiegellampe'}).click();
    const sheet = page.getByRole('dialog');
    await expect(sheet).toContainText('Bad Spiegellampe');
    await page.keyboard.press('Escape');
    await expect(sheet).toHaveCount(0);
    // the function page: the function's name goes, a compound keeps its word
    await page.goto('/app/e/functions/licht');
    await expect(light).toHaveText('Küche');
    const living = page.locator('[data-app-tile="NEQ0123456:1"] .app-tile-name');
    await expect(living).toHaveText('Wohnzimmer Deckenlicht');
    await expect(living).not.toHaveAttribute('data-app-short', '1');
    // the option, kept with the account: off → the full names
    await page.goto('/settings');
    const box = page.locator('[data-setting="app-short-names"]').getByRole('checkbox');
    await expect(box).toBeChecked();
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/preferences')),
        box.uncheck(),
    ]);
    expect(req.postDataJSON()).toMatchObject({app_full_names: true});
    await page.goto('/app/e/rooms/kueche');
    await expect(window).toHaveText('Küche Fenster');
    await expect(window).not.toHaveAttribute('data-app-short', '1');
    await expect(light).toHaveText('Küche Licht');
    // on again: the field goes from the preferences
    await page.goto('/settings');
    await expect(box).not.toBeChecked();
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/preferences')),
        box.check(),
    ]);
    expect(req2.postDataJSON().app_full_names).toBeUndefined();
    await page.goto('/app/e/rooms/kueche');
    await expect(window).toHaveText('Fenster');
});

test('name shortening: favorites keep the full name (task 101)', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the favorites fixture is the desktop one');
    await withFavorites(page);
    await page.goto('/app/favorites');
    await expect(page.locator('[data-app-tile="MEQ0987654:2"] .app-tile-name')).toHaveText('Küche Licht');
    await expect(page.locator('[data-app-tile="MEQ0987654:2"] .app-tile-name')).not.toHaveAttribute('data-app-short', '1');
});

// occulited task 102: the picker searches the whole knx-uf set, which is a separate asset fetched only when
// the picker opens; a chosen icon outside the bundle shows on the tile, after a reload too
test('the icon picker: the whole set loaded only when it opens, a search in German, an icon outside the bundle on the tile (task 102)', async ({page, isMobile}, info) => {
    test.skip(!!isMobile, 'the very-long press with the mouse');
    // switch channels of this spec's own: task 99's spec resets the icons of 000E01-03 while it runs beside this one
    const addr = {'desktop-light': '000E04:3', 'desktop-dark': '000E05:3'}[info.project.name] ?? '000E06:3';
    const ref = `HmIP-RF.${addr}`;
    const setLoads: string[] = [];
    page.on('request', (r) => { if (/\/assets\/icons\.json-[^/]*\.gz$/.test(r.url())) setLoads.push(r.url()); }); // Vite names it icons.json-<hash>.gz
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects[ref] = {meta: {}, ...json.objects[ref], name: 'Aquarium Werkstatt', enums: ['rooms/og/bad']};
        await r.fulfill({response: res, json});
    });
    await page.request.patch(`/api/meta/v1/objects/${encodeURIComponent(ref)}`, {data: {meta: {control: null}}});
    await page.goto('/app/e/rooms/og/bad');
    const tile = page.locator(`[data-app-tile="${ref}"]`);
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', /^light_light_dim/);
    await expect(page.locator('[data-app-tiles]')).toBeVisible();
    expect(setLoads, 'nothing loads the set before the picker opens').toEqual([]);
    const tileBox = (await tile.boundingBox())!;
    const longPress = async () => {
        const b = (await tile.boundingBox())!;
        await page.mouse.move(b.x + b.width - 20, b.y + b.height - 20);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
    };
    await longPress();
    const picker = page.locator('.ol-modal.app-assign [data-icon-picker]');
    // task 110: folded first; the set is loaded only when the picker itself opens
    await expect(picker).toHaveCount(0);
    expect(setLoads).toEqual([]);
    await page.locator('.ol-modal.app-assign [data-assign-icons-fold]').click();
    await expect(picker).toBeVisible();
    // the set comes in once the picker is open, grouped by category
    await expect(picker.locator('[data-icon-group="all"]')).toBeVisible({timeout: 15_000});
    expect(setLoads.length).toBe(1);
    await expect(picker.locator('[data-icon-category="light"]')).toBeVisible();
    expect(await picker.locator('[data-icon-group="all"] [data-icon-cell]').count()).toBeGreaterThan(800);
    // the search: a German category word finds the fans; a name finds one icon outside the suggestions
    const search = picker.locator('[data-icon-search]');
    await search.fill('Lüfter');
    await expect(picker.locator('[data-icon-group="suggested"] [data-icon-cell="vent_ventilation"]')).toBeVisible();
    await expect(picker.locator('[data-icon-category="vent"]')).toBeVisible();
    await expect(picker.locator('[data-icon-category="light"]')).toHaveCount(0);
    await search.fill('aquarium');
    const cell = picker.locator('[data-icon-cell="scene_aquarium"]');
    await expect(cell).toBeVisible();
    await expect(picker.locator('[data-icon-group="all"] [data-icon-cell]')).toHaveCount(1);
    const cellBox = (await cell.boundingBox())!;
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith(`/api/meta/v1/objects/${encodeURIComponent(ref)}`)),
        cell.click(),
    ]);
    expect(req.postDataJSON()).toEqual({meta: {control: {icons: {off: 'scene_aquarium'}}}}); // task 110: the switch is off
    await expect(cell).toHaveAttribute('aria-checked', 'true');
    expect(await cell.boundingBox()).toEqual(cellBox); // the cell keeps its box when chosen
    // the keyboard: the arrow keys move between the cells
    await cell.focus();
    await page.keyboard.press('ArrowLeft');
    await expect(picker.locator('[data-icon-cell=""]')).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect(cell).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'scene_aquarium');
    expect(await tile.boundingBox()).toEqual(tileBox);
    // after a reload the tile loads the set on its own, since its icon is outside the bundle
    setLoads.length = 0;
    await page.reload();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'scene_aquarium', {timeout: 15_000});
    expect(setLoads.length).toBe(1);
    // nothing found: the note; back to automatic
    await longPress();
    await page.locator('.ol-modal.app-assign [data-assign-icons-fold]').click();
    await picker.locator('[data-icon-search]').fill('xyzzy');
    await expect(picker.locator('[data-icon-nothing]')).toBeVisible();
    await picker.locator('[data-icon-search]').fill('');
    await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && (r.postData() ?? '').includes('"control":null')),
        picker.locator('[data-icon-cell=""]').click(),
    ]);
    await page.keyboard.press('Escape');
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', /^light_light_dim/);
});

// occulited task 103: a thermostat's boost - the disc starts it and ends it, the countdown ticks every second
// in the valve row's place, V4's ring drains around the disc, boost has its own tint; V0's humidity row; the
// tile keeps its box through all of it
test('a thermostat: the disc boosts, the countdown and the ring, the humidity row; the boxes stay (task 103)', async ({page, baseURL, isMobile}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    // the BidCos thermostats of the stub join Bad for this spec
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['BidCos-RF.KEQ0012345:2'].enums = ['rooms/og/bad'];
        json.objects['BidCos-RF.KEQ0012346:4'].enums = ['rooms/og/bad'];
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/og/bad');
    const hmip = page.locator('[data-app-tile="HmIP-RF.000ABC:1"]');
    const wall = page.locator('[data-app-tile="BidCos-RF.KEQ0012345:2"]');
    const rt = page.locator('[data-app-tile="BidCos-RF.KEQ0012346:4"]');
    await expect(hmip).toHaveAttribute('data-app-widget', 'thermostat');
    await expect(rt.locator('[data-app-valve]')).toHaveText('30 %');
    // V0: the humidity row where the channel has one, none where it has not; every tile 118 px, 4 px inside
    await expect(wall.locator('[data-app-hum]')).toHaveText('54 % humidity');
    await expect(hmip.locator('[data-app-hum]')).toHaveCount(0);
    await expect(rt.locator('[data-app-hum]')).toHaveCount(0);
    await expect(hmip).toHaveCSS('height', '118px');
    await expect(hmip).toHaveCSS('padding-top', '4px');
    await expect(hmip).toHaveCSS('padding-bottom', '4px');
    // the tile, the disc and the name keep their boxes through every state: a thermostat that can boost keeps its row
    // (empty without a valve) where the countdown goes, so nothing moves when a boost starts ("reserve the row")
    await expect(hmip.locator('[data-app-th-row][data-app-th-reserved]')).toHaveCount(1);
    await expect(wall.locator('[data-app-th-row][data-app-th-reserved]')).toHaveCount(1);
    const boxes = async () => Promise.all([hmip, wall, rt].map(async (l) => ({tile: await l.boundingBox(), disc: await l.locator('.app-circle').boundingBox(), name: await l.locator('.app-tile-name').boundingBox(), pair: await l.locator('[data-app-thermo]').boundingBox()})));
    const before = await boxes();
    // HmIP: the disc is the boost button; a click writes BOOST_MODE true, the device answers with BOOST_TIME
    const disc = hmip.locator('[data-app-boost-btn]');
    await expect(disc).toHaveAttribute('data-app-boost-btn', 'off');
    await expect(disc).toHaveAttribute('title', 'Start boost');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')),
        disc.click(),
    ]);
    expect(req.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000ABC:1', 'BOOST_MODE', true]});
    await expect(disc).toHaveAttribute('data-app-boost-btn', 'on');
    await expect(disc).toHaveAttribute('title', 'End boost');
    await expect(hmip).toHaveAttribute('data-app-tone', 'boost');
    // the countdown in the valve row's place, from the reported 300 s, ticking down
    const row = hmip.locator('[data-app-th-row]');
    await expect(row).toHaveText(/^Boost: 5 min 00 s$|^Boost: 4 min 5\d s$/);
    const first = Number(await row.getAttribute('data-app-boost'));
    await expect.poll(async () => Number(await row.getAttribute('data-app-boost')), {timeout: 4000}).toBeLessThan(first);
    await expect(row).toHaveText(/^Boost: 4 min 5\d s$/);
    // V4's ring, nearly full, draining
    const ring = disc.locator('.app-ring');
    await expect(ring).toHaveCount(1);
    expect(Number(await ring.getAttribute('data-app-ring'))).toBeGreaterThan(95);
    // nothing moved: the tile, the disc and the name keep their boxes
    expect(await boxes()).toEqual(before);
    // the sheet shows the countdown and its disc ends the boost
    await hmip.locator('.app-tile-name').click();
    const sheet = page.getByRole('dialog');
    await expect(sheet.locator('[data-sheet-boost]')).toHaveText(/^Boost: 4 min \d\d s$/);
    await expect(sheet.locator('[data-sheet="mode"]').getByRole('button', {name: 'Boost'})).toHaveAttribute('aria-pressed', 'true');
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')),
        sheet.locator('[data-sheet-boost-btn]').click(),
    ]);
    expect(req2.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000ABC:1', 'BOOST_MODE', false]});
    await expect(sheet.locator('[data-sheet-boost]')).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(disc).toHaveAttribute('data-app-boost-btn', 'off');
    await expect(hmip).toHaveAttribute('data-app-tone', '');
    await expect(hmip.locator('[data-app-th-row][data-app-th-reserved]')).toHaveCount(1); // no valve: the empty row again
    await expect(ring).toHaveCount(0);
    expect(await boxes()).toEqual(before);
    // BidCos: the radiator thermostat in manual mode - the disc writes BOOST_MODE, the device answers CONTROL_MODE 3
    // and the minutes left; the countdown takes the valve row's place; ending it goes back to MANU_MODE with the setpoint
    const rtDisc = rt.locator('[data-app-boost-btn]');
    const [req3] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')),
        rtDisc.click(),
    ]);
    expect(req3.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['KEQ0012346:4', 'BOOST_MODE', true]});
    await expect(rt).toHaveAttribute('data-app-tone', 'boost');
    await expect(rt.locator('[data-app-th-row]')).toHaveText(/^Boost: (5 min 00|4 min 5\d) s$/);
    await expect(rt.locator('[data-app-valve]')).toHaveCount(0);
    const [req4] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('MANU_MODE')),
        rtDisc.click(),
    ]);
    expect(req4.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['KEQ0012346:4', 'MANU_MODE', 22]});
    await expect(rt).toHaveAttribute('data-app-tone', 'on'); // its valve is open (task 97's tint), no longer boost
    await expect(rt.locator('[data-app-valve]')).toHaveText('30 %');
    // the wall thermostat in auto: boost, then back to AUTO_MODE; the humidity row stays through it
    const wallDisc = wall.locator('[data-app-boost-btn]');
    await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')), wallDisc.click()]);
    await expect(wall).toHaveAttribute('data-app-tone', 'boost');
    await expect(wall.locator('[data-app-hum]')).toHaveText('54 % humidity');
    const [req5] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('AUTO_MODE')),
        wallDisc.click(),
    ]);
    expect(req5.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['KEQ0012345:2', 'AUTO_MODE', true]});
    await expect(wall).toHaveAttribute('data-app-tone', '');
    expect(await boxes()).toEqual(before);
    if (!isMobile) {
        // a disc without boost (a channel whose BOOST_MODE is not writable) is no button: the dimmer's is, the thermostat's hover is the button's
        await expect(page.locator('[data-app-tile="HmIP-RF.000DD8:3"] .app-circle-act')).toHaveCount(1);
    }
});

test('a thermostat: in German the countdown says Min (task 103)', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/app/e/rooms/og/bad');
    const hmip = page.locator('[data-app-tile="HmIP-RF.000ABC:1"]');
    await expect(hmip).toHaveAttribute('data-app-widget', 'thermostat');
    await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')), hmip.locator('[data-app-boost-btn]').click()]);
    await expect(hmip.locator('[data-app-th-row]')).toHaveText(/^Boost: (5 Min 00|4 Min 5\d) s$/);
    await expect(hmip.locator('[data-app-boost-btn]')).toHaveAttribute('title', 'Boost beenden');
});

// occulited task 110: the assignment sheet's icon section is folded on landing, so the rooms and functions are in view
// without scrolling - on a phone (400 x 700) too; the row keeps its box when it shows one icon or two
test('the assignment sheet lands with the icons folded and the rooms in view, on a phone too (task 110)', async ({page, isMobile}, info) => {
    test.skip(!!isMobile, 'the very-long press with the mouse; the phone size is set below');
    await page.setViewportSize({width: 400, height: 700});
    const addr = {'desktop-light': '000E04:3', 'desktop-dark': '000E05:3'}[info.project.name] ?? '000E06:3';
    const ref = `HmIP-RF.${addr}`;
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects[ref] = {meta: {}, ...json.objects[ref], name: 'Licht Keller', enums: ['rooms/og/bad']};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/og/bad');
    const tile = page.locator(`[data-app-tile="${ref}"]`);
    const b = (await tile.boundingBox())!;
    await page.mouse.move(b.x + b.width - 20, b.y + b.height - 20);
    await page.mouse.down();
    await page.waitForTimeout(1400);
    await page.mouse.up();
    const sheet = page.locator('.ol-modal.app-assign');
    const fold = sheet.locator('[data-assign-icons-fold]');
    await expect(fold).toHaveAttribute('aria-expanded', 'false');
    await expect(fold.locator('.as-caret')).toBeVisible();
    // the first room row is inside the window without scrolling
    const room = sheet.locator('[data-assign-node]').first();
    await expect(room).toBeInViewport();
    const rb = (await room.boundingBox())!;
    expect(rb.y + rb.height).toBeLessThanOrEqual(700);
    // open and closed again: the row keeps its box
    const fb = (await fold.boundingBox())!;
    await fold.click();
    await expect(sheet.locator('[data-icon-picker]')).toBeVisible();
    await fold.click();
    await expect(sheet.locator('[data-icon-picker]')).toHaveCount(0);
    expect(await fold.boundingBox()).toEqual(fb);
});

// occulited task 109: a switching actuator with a power meter is one tile - the switch's, "On · 12.5 W"; the meter's
// own tile is left out where the switch's is shown, and the switch's sheet has the meter's values below its buttons
test('a switch with a power meter on its device is one tile with the power; the sheet shows the meter (task 109)', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['HmIP-RF.000F04:3'] = {name: 'Flur Steckdose Schalter', enums: ['rooms/og'], meta: {}};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/og');
    const sw = page.locator('[data-app-tile="HmIP-RF.000F04:3"]');
    await expect(sw).toHaveAttribute('data-app-widget', 'switch');
    await expect(sw.locator('[data-app-state]')).toHaveText('On · 12.5 W');
    await expect(page.locator('[data-app-tile="HmIP-RF.000F04:6"]')).toHaveCount(0);
    // the room had eight tiles; the switch came, the meter went
    await expect(page.locator('[data-app-tiles]')).toHaveAttribute('data-app-tiles', '8');
    // one line, the height of any other tile's state line
    const other = page.locator('[data-app-tile="HmIP-RF.000F03:1"] [data-app-state]');
    expect((await sw.locator('[data-app-state]').boundingBox())!.height).toBe((await other.boundingBox())!.height);
    // the circle switches; the power stays in the line
    await sw.locator('.app-circle').click();
    await expect(sw.locator('[data-app-state]')).toHaveText('Off · 12.5 W');
    // the sheet: Off left, On right, the meter's values below
    await sw.locator('.app-tile-name').click();
    const sheet = page.getByRole('dialog');
    await expect(sheet.locator('[data-sheet="switch"]')).toBeVisible();
    const readings = sheet.locator('[data-sheet="readings"]');
    await expect(readings).toContainText('Power');
    await expect(readings).toContainText('12.5 W');
    await expect(readings).toContainText('12.35 kWh');
    await expect(readings).toContainText('230.1 V');
    await expect(readings).toContainText('50.02 Hz');
    expect((await sheet.locator('[data-sheet-off]').boundingBox())!.x).toBeLessThan((await sheet.locator('[data-sheet-on]').boundingBox())!.x);
});
test('a power meter whose switch is not in the view keeps its own tile; German decimals (task 109)', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/app/e/rooms/og');
    await expect(page.locator('[data-app-tile="HmIP-RF.000F04:6"] [data-app-state]')).toHaveText('12,5 W');
    await expect(page.locator('[data-app-tiles]')).toHaveAttribute('data-app-tiles', '8');
});
test('the switch tile with a meter in German: "An · 12,5 W" (task 109)', async ({page, baseURL}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['HmIP-RF.000F04:3'] = {name: 'Flur Steckdose Schalter', enums: ['rooms/og'], meta: {}};
        await r.fulfill({response: res, json});
    });
    await page.goto('/app/e/rooms/og');
    await expect(page.locator('[data-app-tile="HmIP-RF.000F04:3"] [data-app-state]')).toHaveText('An · 12,5 W');
});

// occulited B-103: a heating group (VirtualDevices) says BOOST_MODE true with BOOST_TIME 0 while its members boost
// (production, 2026-10-23: the group 0, its HmIP-WTH-2 297 s). The tile counts down from its members' time, which
// the heating groups API names; the members are followed, not shown. Without a member time: "Boost", the ring going round.
async function groupInBad(page: import('@playwright/test').Page, baseURL: string) {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL}]);
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['VirtualDevices.INT0000011:1'] = {name: 'Heizgruppe Bad', enums: ['rooms/og/bad'], meta: {}};
        json.objects['HmIP-RF.000A9D:1'] = {name: 'Wandthermostat Bad', enums: [], meta: {}};
        await r.fulfill({response: res, json});
    });
}
test('a heating group: boost counts down from its members\' time, also after a reload (B-103)', async ({page, baseURL}) => {
    await groupInBad(page, baseURL!);
    await page.goto('/app/e/rooms/og/bad');
    const group = page.locator('[data-app-tile="VirtualDevices.INT0000011:1"]');
    await expect(group).toHaveAttribute('data-app-widget', 'thermostat');
    // the member is followed, not a tile
    await expect(page.locator('[data-app-tile="HmIP-RF.000A9D:1"]')).toHaveCount(0);
    const disc = group.locator('[data-app-boost-btn]');
    const box = async () => ({tile: await group.boundingBox(), disc: await disc.boundingBox(), name: await group.locator('.app-tile-name').boundingBox(), pair: await group.locator('[data-app-thermo]').boundingBox()});
    const before = await box();
    const [req] = await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')), disc.click()]);
    expect(req.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['INT0000011:1', 'BOOST_MODE', true]});
    // boosting from the group's BOOST_MODE; the time from its member's 297 s, ticking
    await expect(group).toHaveAttribute('data-app-tone', 'boost');
    const row = group.locator('[data-app-th-row]');
    await expect(row).toHaveText(/^Boost: 4 min 5\d s$/);
    const first = Number(await row.getAttribute('data-app-boost'));
    await expect.poll(async () => Number(await row.getAttribute('data-app-boost')), {timeout: 4000}).toBeLessThan(first);
    const ring = disc.locator('.app-ring');
    expect(Number(await ring.getAttribute('data-app-ring'))).toBeGreaterThan(95);
    expect(await box()).toEqual(before);
    // the sheet says it too
    await group.locator('.app-tile-name').click();
    await expect(page.getByRole('dialog').locator('[data-sheet-boost]')).toHaveText(/^Boost: 4 min \d\d s$/);
    await page.keyboard.press('Escape');
    // a reload during the boost: the member's time comes from the state store, counted from when it was seen
    await page.reload();
    await expect(row).toHaveText(/^Boost: 4 min [3-5]\d s$/);
    await expect(group).toHaveAttribute('data-app-tone', 'boost');
    // ending it: the group's BOOST_MODE false
    await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')), disc.click()]);
    await expect(group).toHaveAttribute('data-app-tone', '');
    await expect(ring).toHaveCount(0);
    await expect(group.locator('[data-app-th-row][data-app-th-reserved]')).toHaveCount(1);
});
test('a heating group whose members cannot be read: "Boost" and an indeterminate ring (B-103)', async ({page, baseURL}) => {
    await groupInBad(page, baseURL!);
    await page.route('**/api/system/v1/groups', (r) => r.fulfill({status: 403, json: {error: 'forbidden', message: 'the token lacks system:read'}}));
    await page.goto('/app/e/rooms/og/bad');
    const group = page.locator('[data-app-tile="VirtualDevices.INT0000011:1"]');
    const disc = group.locator('[data-app-boost-btn]');
    await expect(disc).toHaveAttribute('data-app-boost-btn', 'off');
    const before = await group.boundingBox();
    await Promise.all([page.waitForRequest((r) => r.method() === 'POST' && (r.postData() ?? '').includes('BOOST_MODE')), disc.click()]);
    await expect(group).toHaveAttribute('data-app-tone', 'boost');
    await expect(group.locator('[data-app-th-row]')).toHaveText('Boost');
    await expect(disc.locator('.app-ring')).toHaveAttribute('data-app-ring', 'indeterminate');
    await expect(disc.locator('.app-ring')).toHaveClass(/indet/);
    expect(await group.boundingBox()).toEqual(before);
});

// occulited task 104: a channel the interface describes without a widget gets no tile - in the unassigned view, and
// in a room; the channels with a UI stay (the smoke detectors among them)
test('no tile for a channel without a UI, in the unassigned view and in a room (task 104)', async ({page}) => {
    await page.goto('/app/unassigned');
    await expect(page.locator('[data-app-title]')).toHaveText('Unassigned channels');
    // the BidCos wall thermostat (a UI) is a tile; the SRH's alarm channel (an empty paramset) is none
    await expect(page.locator('[data-app-tile="BidCos-RF.KEQ0012345:2"]')).toHaveAttribute('data-app-widget', 'thermostat');
    await expect(page.locator('[data-app-tile="HmIP-RF.000C01:2"]')).toHaveCount(0);
    await expect(page.locator('[data-app-tile]', {hasText: 'Fenstergriff Bad Alarm'})).toHaveCount(0);
    // the same channel assigned to a room: still no tile there; the room's smoke detectors keep theirs
    await page.route('**/api/meta/v1/snapshot', async (route) => {
        const response = await route.fetch();
        const snap = await response.json();
        snap.objects['HmIP-RF.000C01:2'].enums = ['rooms/og'];
        await route.fulfill({response, json: snap});
    });
    await page.goto('/app/e/rooms/og');
    await expect(page.locator('[data-app-tile="HmIP-RF.000F01:1"] .app-circle')).toHaveAttribute('data-icon', 'secur_smoke_detector');
    await expect(page.locator('[data-app-tile="BidCos-RF.NEQ0123459:1"]')).toHaveAttribute('data-app-widget', 'smoke');
    await expect(page.locator('[data-app-tile="HmIP-RF.000C01:2"]')).toHaveCount(0);
    await expect(page.locator('[data-app-tiles]')).toHaveAttribute('data-app-tiles', '8');
});

// occulited task 106: a key channel gets a chosen icon through the picker (its own suggestions: switches, bells,
// scenes), kept in the metadata; the press lights the chosen icon; "Automatisch" is the switch symbol
test('a key tile: a chosen icon from the picker, lit on a press, kept across a reload (task 106)', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the very-long press with the mouse');
    const ref = 'BidCos-RF.BidCoS-RF:2';
    await page.request.patch(`/api/meta/v1/objects/${encodeURIComponent(ref)}`, {data: {meta: {control: null}}});
    await page.goto('/app/e/rooms/og/bad');
    const tile = page.locator(`[data-app-tile="${ref}"]`);
    await expect(tile).toHaveAttribute('data-app-widget', 'button');
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'control_switch_1');
    const box = (await tile.boundingBox())!;
    const longPress = async () => {
        const b = (await tile.boundingBox())!;
        await page.mouse.move(b.x + b.width - 20, b.y + b.height - 20);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
    };
    await longPress();
    const sheet = page.locator('.ol-modal.app-assign');
    const picker = sheet.locator('[data-assign="icon"]');
    await sheet.locator('[data-assign-icons-fold]').click(); // task 110: folded first
    await expect(picker).toHaveAttribute('data-assign-icon-kind', 'key');
    await expect(picker.locator('[data-icon-cell=""]')).toHaveAttribute('aria-checked', 'true');
    // the key's own suggestions: the doorbell among them, the socket not
    await expect(picker.locator('[data-icon-group="suggested"] [data-icon-cell="message_bell_door"]')).toBeVisible();
    await expect(picker.locator('[data-icon-group="suggested"] [data-icon-cell="message_socket"]')).toHaveCount(0);
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith(`/api/meta/v1/objects/${encodeURIComponent(ref)}`)),
        picker.locator('[data-icon-cell="message_bell_door"]').click(),
    ]);
    expect(req.postDataJSON()).toEqual({meta: {control: {icon: 'message_bell_door'}}});
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'message_bell_door');
    expect(await tile.boundingBox()).toEqual(box);
    // a press lights the chosen icon and keeps it
    await tile.locator('.app-circle').click();
    await expect(tile.locator('[data-app-state]')).toHaveText('short press');
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'message_bell_door');
    // kept across a reload; then back to automatic
    await page.reload();
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'message_bell_door');
    await longPress();
    await sheet.locator('[data-assign-icons-fold]').click();
    await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && (r.postData() ?? '').includes('"control":null')),
        picker.locator('[data-icon-cell=""]').click(),
    ]);
    await page.keyboard.press('Escape');
    await expect(tile.locator('.app-circle')).toHaveAttribute('data-icon', 'control_switch_1');
});

// occulited task 105: a door/window contact gets a picture per state - closed, open, and tilted for a three-state
// sensor - through one picker with the state chosen above it; kept in the metadata's control namespace (icons), shown
// on the tile for the state the sensor is in
test('a contact: pictures per state from the picker, the tilted state only on a three-state sensor, kept across a reload (task 105)', async ({page, isMobile, baseURL}) => {
    test.skip(!!isMobile, 'the very-long press with the mouse');
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]); // the stub's events reach this page
    // the stub's window handle (three states, open) and door contact (two states, closed) join Bad
    await page.route('**/api/meta/v1/snapshot', async (r) => {
        const res = await r.fetch();
        const json = await res.json();
        json.objects['HmIP-RF.000C01:1'] = {meta: {}, ...json.objects['HmIP-RF.000C01:1'], name: 'Fenster Bad', enums: ['rooms/og/bad']};
        json.objects['HmIP-RF.000C02:1'] = {meta: {}, ...json.objects['HmIP-RF.000C02:1'], name: 'Tür Bad', enums: ['rooms/og/bad']};
        await r.fulfill({response: res, json});
    });
    for (const ref of ['HmIP-RF.000C01:1', 'HmIP-RF.000C02:1']) await page.request.patch(`/api/meta/v1/objects/${encodeURIComponent(ref)}`, {data: {meta: {control: null}}});
    await page.goto('/app/e/rooms/og/bad');
    const handle = page.locator('[data-app-tile="HmIP-RF.000C01:1"]');
    const door = page.locator('[data-app-tile="HmIP-RF.000C02:1"]');
    await expect(handle.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_window_1w_open');
    await expect(door.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_door');
    const box = (await handle.boundingBox())!;
    const longPress = async (tile: typeof handle) => {
        const b = (await tile.boundingBox())!;
        await page.mouse.move(b.x + b.width - 20, b.y + b.height - 20);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
    };
    await longPress(handle);
    const sheet = page.locator('.ol-modal.app-assign');
    const picker = sheet.locator('[data-assign="icon"]');
    await sheet.locator('[data-assign-icons-fold]').click(); // task 110: folded first
    await expect(picker).toHaveAttribute('data-assign-icon-kind', 'contact');
    // three states, each button showing the state's current picture; closed is the state chosen at first
    await expect(picker.locator('[data-assign-states]')).toHaveAttribute('data-assign-states', '3');
    await expect(picker.locator('[data-assign-state="closed"]')).toHaveAttribute('aria-selected', 'true');
    await expect(picker.locator('[data-assign-state="open"]')).toHaveAttribute('data-assign-state-icon', 'fts_window_1w_open');
    await expect(picker.locator('[data-assign-state="tilted"]')).toHaveAttribute('data-assign-state-icon', 'fts_window_1w_tilt');
    await expect(picker.locator('[data-icon-group="suggested"] [data-icon-cell="fts_window_2w_open"]')).toBeVisible();
    // the open state gets the double window: the namespace written whole, the tile (open) shows it
    const stateBox = (await picker.locator('[data-assign-state="open"]').boundingBox())!;
    await picker.locator('[data-assign-state="open"]').click();
    await expect(picker.locator('[data-assign-state="open"]')).toHaveAttribute('aria-selected', 'true');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/objects/HmIP-RF.000C01%3A1')),
        picker.locator('[data-icon-cell="fts_window_2w_open"]').click(),
    ]);
    expect(req.postDataJSON()).toEqual({meta: {control: {icons: {open: 'fts_window_2w_open'}}}});
    await expect(picker.locator('[data-assign-state="open"]')).toHaveAttribute('data-assign-state-icon', 'fts_window_2w_open');
    expect(await picker.locator('[data-assign-state="open"]').boundingBox()).toEqual(stateBox); // the state button keeps its box
    // the tilted state too: both kept in one write
    await picker.locator('[data-assign-state="tilted"]').click();
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/objects/HmIP-RF.000C01%3A1')),
        picker.locator('[data-icon-cell="fts_window_2w_tilt"]').click(),
    ]);
    expect(req2.postDataJSON()).toEqual({meta: {control: {icons: {open: 'fts_window_2w_open', tilted: 'fts_window_2w_tilt'}}}});
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(handle.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_window_2w_open');
    expect(await handle.boundingBox()).toEqual(box);
    // the sensor tilts: the tilted picture; closed: the default, unchanged
    await page.request.post('/api/rpc/v1/json/HmIP-RF', {data: [{jsonrpc: '2.0', method: 'setValue', params: ['000C01:1', 'STATE', 1], id: 1}]});
    await expect(handle.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_window_2w_tilt');
    await page.request.post('/api/rpc/v1/json/HmIP-RF', {data: [{jsonrpc: '2.0', method: 'setValue', params: ['000C01:1', 'STATE', 0], id: 2}]});
    await expect(handle.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_window_1w');
    // kept across a reload
    await page.reload();
    await expect(handle.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_window_1w');
    await page.request.post('/api/rpc/v1/json/HmIP-RF', {data: [{jsonrpc: '2.0', method: 'setValue', params: ['000C01:1', 'STATE', 2], id: 3}]});
    await expect(handle.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_window_2w_open');
    // the door contact: two states only; its closed picture; Automatisch removes the namespace
    await longPress(door);
    await sheet.locator('[data-assign-icons-fold]').click(); // folded again in a new sheet
    await expect(picker).toHaveAttribute('data-assign-icon-kind', 'contact');
    await expect(picker.locator('[data-assign-states]')).toHaveAttribute('data-assign-states', '2');
    await expect(picker.locator('[data-assign-state="tilted"]')).toHaveCount(0);
    const [req3] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && r.url().endsWith('/api/meta/v1/objects/HmIP-RF.000C02%3A1')),
        picker.locator('[data-icon-cell="fts_frontdoor"]').click(),
    ]);
    expect(req3.postDataJSON()).toEqual({meta: {control: {icons: {closed: 'fts_frontdoor'}}}});
    await expect(door.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_frontdoor');
    await Promise.all([
        page.waitForRequest((r) => r.method() === 'PATCH' && (r.postData() ?? '').includes('"control":null')),
        picker.locator('[data-icon-cell=""]').click(),
    ]);
    await page.keyboard.press('Escape');
    await expect(door.locator('.app-circle')).toHaveAttribute('data-icon', 'fts_door');
    // the handle's choice is taken back for the next run
    await page.request.patch('/api/meta/v1/objects/HmIP-RF.000C01%3A1', {data: {meta: {control: null}}});
});
