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
    await expect(drawer.locator('[data-app-entry="messages"]')).toContainText('Service messages');
    await expect(page.locator('[data-app-messages]')).toHaveText('2');
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
    await expect(tiles.nth(0)).toContainText('Wohnzimmer Deckenlicht');
    await expect(tiles.nth(3)).toContainText('Zentrale Taste 1');
    await drawer.locator('[data-app-node="rooms/og"]').click();
    await expect(page.locator('[data-app-title]')).toHaveText('Obergeschoss');
    await expect(page.locator('[data-app-tile]')).toHaveCount(7);
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
    await expect(thermo.locator('[data-app-state]')).toHaveText('set 21.5 °C · 20.2 °C');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/rpc/v1/json/HmIP-RF') && (r.postData() ?? '').includes('setValue')),
        lamp.getByRole('button', {name: 'Switch Bad Spiegellampe'}).click(),
    ]);
    expect(req.postDataJSON()[0]).toMatchObject({method: 'setValue', params: ['000DD8:3', 'LEVEL', 0]});
    await expect(lamp).toHaveAttribute('data-app-on', '0');
    await expect(lamp.locator('[data-app-state]')).toHaveText('Off');
});

// task 75: the state icon in the circle - the KNX User Forum set's, swapped on a state change in a
// circle that keeps its box (40 px below 900 px, 52 px from there), and the tile keeps its box too
test('the circle carries the state icon; a state change swaps it in the same box', async ({page, baseURL, isMobile}) => {
    await page.context().addCookies([{name: 'stub-app', value: `app-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    const circle = lamp.locator('.app-circle');
    await expect(circle).toHaveAttribute('data-icon', 'light_light_dim_60');
    await expect(circle).toHaveClass(/\bon\b/);
    await expect(circle.locator('svg')).toHaveCount(1);
    const size = isMobile ? 40 : 52;
    const before = (await circle.boundingBox())!;
    const tileBefore = (await lamp.boundingBox())!;
    expect(Math.round(before.width)).toBe(size);
    expect(Math.round(before.height)).toBe(size);
    // the icon fills about 90 % of the circle
    const ico = (await circle.locator('svg').boundingBox())!;
    expect(ico.width / size).toBeGreaterThan(0.8);
    await circle.click();
    await expect(circle).toHaveAttribute('data-icon', 'light_light_dim_00');
    await expect(circle).not.toHaveClass(/\bon\b/);
    expect(await circle.boundingBox()).toEqual(before);
    expect(await lamp.boundingBox()).toEqual(tileBefore);
    // the thermostat without a valve: the setpoint as a filled thermometer (21.5 °C of 12..25 °C)
    await expect(page.locator('[data-app-tile="HmIP-RF.000ABC:1"] .app-circle')).toHaveAttribute('data-icon', 'thermometer:73');
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
    expect(await save.boundingBox()).toEqual(saveBox); // the button keeps its box
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

test('the service messages entry is not in the drawer when there are none', async ({page, baseURL, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    await page.context().addCookies([{name: 'stub-servicemsg', value: 'none', url: baseURL!}]);
    await page.goto('/app');
    await expect(page.locator('[data-app-entry="favorites"]')).toBeVisible();
    await expect(page.locator('[data-app-entry="messages"]')).toHaveCount(0);
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

test('badges: the maintenance messages on the tile, at most three plus more, and the rollup in the drawer', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    const msgs = ['UNREACH', 'LOW_BAT', 'CONFIG_PENDING', 'UPDATE_PENDING'].map((key) => ({interface: 'HmIP-RF', address: '000DD8', channel: '0', key, value: true, since: '2026-09-22T12:00:00Z', seen: 'event', type: 'HmIP-PDT'}));
    await page.route('**/api/system/v1/service-messages', (r) => r.fulfill({json: {count: msgs.length, messages: msgs, swept: '2026-09-22T12:00:00Z', errors: {}}}));
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    await page.goto('/app/e/rooms/og/bad');
    const lamp = page.locator('[data-app-tile="HmIP-RF.000DD8:3"]');
    const badges = lamp.locator('[data-app-badges]');
    await expect(badges).toHaveAttribute('data-app-badges', '4');
    await expect(badges.locator('.app-badge')).toHaveCount(4); // three icons and the +1
    await expect(badges.locator('.app-badge-more')).toHaveText('+1');
    // occulited task 43: the message's own icon and colour - UNREACH the filled red broken link
    await expect(badges.locator('.app-badge-solid.app-badge-red[data-app-badge="UNREACH"][data-icon="unlink"]')).toHaveCount(1);
    await expect(badges.locator('.app-badge-orange[data-app-badge="LOW_BAT"][data-icon="battery"]')).toHaveCount(1);
    await expect(badges.locator('.app-badge-blue[data-app-badge="CONFIG_PENDING"][data-icon="settings"]')).toHaveCount(1);
    // no badge on the key next to it (another device)
    await expect(page.locator('[data-app-tile="BidCos-RF.BidCoS-RF:2"] [data-app-badges]')).toHaveCount(0);
    // the rollup: Bad and Obergeschoss carry the dot, Wohnzimmer not
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(1);
    await expect(page.locator('[data-app-node="rooms/og"] [data-app-problem]')).toHaveCount(1);
    await expect(page.locator('[data-app-node="rooms/wohnzimmer"] [data-app-problem]')).toHaveCount(0);
    await expect(page.locator('[data-app-title] .app-dot')).toHaveCount(1);
    // the badges name themselves
    await badges.click();
    await expect(page.getByRole('dialog')).toContainText('not reachable');
    await expect(page.getByRole('dialog')).toContainText('Low battery');
    await page.keyboard.press('Escape');
});

// occulited B-46: a device that answers again but whose outage nobody acknowledged (STICKY_UNREACH
// without UNREACH) is not shown as unreachable: a mild badge, not the filled one, no dot in the drawer - and
// the event that clears UNREACH turns the filled badge into the mild one on an open page
test('badges: STICKY_UNREACH alone is a mild "was unreachable", not "not reachable"', async ({page, isMobile}) => {
    test.skip(!!isMobile, 'the drawer is behind the button on a phone');
    const msg = (key: string) => ({interface: 'HmIP-RF', address: '000DD8', channel: '0', key, value: true, since: '2026-09-22T12:00:00Z', seen: 'event', type: 'HmIP-PDT'});
    let msgs = [msg('STICKY_UNREACH')];
    await page.route('**/api/system/v1/service-messages', (r) => r.fulfill({json: {count: msgs.length, messages: msgs, swept: '2026-09-22T12:00:00Z', errors: {}}}));
    await shellStreamInWindow(page);
    await page.route('**/api/system/v1/stream?*', (r) => r.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: ': stub\n\n'}));
    await page.goto('/app/e/rooms/og/bad');
    const badges = page.locator('[data-app-tile="HmIP-RF.000DD8:3"] [data-app-badges]');
    await expect(badges).toHaveAttribute('data-app-badges', '1');
    await expect(badges.locator('.app-badge-mild.app-badge-grey[data-app-badge="STICKY_UNREACH"][data-icon="history"]')).toHaveCount(1);
    await expect(badges.locator('.app-badge-solid')).toHaveCount(0);
    await expect(badges).toHaveAttribute('title', 'was unreachable');
    // nothing is wrong now: no dot on the room, its parent or the title
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(0);
    await expect(page.locator('[data-app-node="rooms/og"] [data-app-problem]')).toHaveCount(0);
    await expect(page.locator('[data-app-title] .app-dot')).toHaveCount(0);
    await badges.click();
    await expect(page.getByRole('dialog')).toContainText('was unreachable');
    await expect(page.getByRole('dialog')).not.toContainText('not reachable');
    await page.keyboard.press('Escape');
    // in German
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    // both set: the device is away now - the filled badge alone, and the dot
    msgs = [msg('UNREACH'), msg('STICKY_UNREACH')];
    await page.reload();
    await expect(badges).toHaveAttribute('data-app-badges', '1');
    await expect(badges.locator('.app-badge-solid[data-app-badge="UNREACH"]')).toHaveCount(1);
    await expect(badges).toHaveAttribute('title', 'nicht erreichbar');
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(1);
    // it answers again, the acknowledgement is still missing
    msgs = [msg('STICKY_UNREACH')];
    await page.reload();
    await expect(badges).toHaveAttribute('title', 'war nicht erreichbar');
    await expect(badges.locator('.app-badge-solid')).toHaveCount(0);
    await expect(page.locator('[data-app-node="rooms/og/bad"] [data-app-problem]')).toHaveCount(0);
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
    expect(await button.boundingBox()).toEqual(before);
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
