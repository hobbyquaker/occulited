import {type Page, type TestInfo} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// task 95: System → Status LED against the stub (test/stub/led.mjs). The simple view: what the LED
// shows now, the switch that asks before it darkens the LED, the six rows and Save; the advanced
// view: rows moved by their handles (task 162), fixed rows without, Test and Locate; a box without a status
// LED; German on a phone; a user who reads but does not change. The stub keeps the configuration
// per browser (stub-led=<id>), so the three projects never meet.

const USER = {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'};

async function plant(page: Page, baseURL: string, info: TestInfo, cookies: Record<string, string> = {}) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies(Object.entries({...cookies, 'stub-led': id}).map(([name, value]) => ({name, value, url: baseURL})));
}

function watchErrors(page: Page): string[] {
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    page.on('console', (m) => {
        if (m.type() === 'error') errors.push(`console: ${m.text()}`);
    });
    return errors;
}

test('the page says what the LED shows, why, and offers the switches of the simple view', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-active': 'service-failed'});
    const errors = watchErrors(page);
    await page.goto('/system/hardware');
    await expect(page.getByRole('heading', {level: 1, name: 'System › Hardware'})).toBeVisible();
    const now = page.locator('[data-led-now]');
    await expect(now).toHaveAttribute('data-led-now', 'service-failed');
    await expect(now).toContainText('Red, slow blink');
    await expect(now).toContainText('A service has failed: ssdpd');
    await expect(now.getByRole('link', {name: 'Open page'})).toHaveAttribute('href', '/system/services');
    await expect(page.locator('[data-led-simple]')).toHaveCount(5);
    await expect(page.locator('[data-led-simple="no-network"]')).toContainText('Yellow, fast blink');
    await expect(page.locator('[data-led-advanced]')).not.toHaveAttribute('open', '');
    // the legend is generated from the configuration, the recovery system included
    await page.locator('[data-led-legend] summary').click();
    await expect(page.locator('[data-led-legend]')).toContainText('Recovery system active');
    await expect(page.locator('[data-led-legend]')).toContainText('Magenta, fast blink');
    expect(await fitsWindow(page)).toBe(true);
    expect(errors, errors.join('\n')).toEqual([]);
});

test('a switch and Save send the configuration', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    const save = page.locator('.ol-led-save').getByRole('button', {name: 'Save'});
    await expect(save).toBeDisabled();
    await page.locator('[data-led-simple="no-network"] input[type="checkbox"]').uncheck();
    await page.getByRole('radio', {name: 'Green'}).click();
    await expect(save).toBeEnabled();
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await save.click();
    const body = (await put).postDataJSON();
    expect(body.states.find((s: {id: string}) => s.id === 'no-network').enabled).toBe(false);
    expect(body.normal).toEqual({color: 'green', pattern: 'solid'});
    await expect(save).toBeDisabled();
    await page.reload();
    await expect(page.locator('[data-led-simple="no-network"] input[type="checkbox"]')).not.toBeChecked();
});

test('switching the LED off asks first', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    const box = page.locator('[data-led="enabled"]');
    await box.uncheck();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('The LED will stay dark, errors included.');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(box).toBeChecked();
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    await box.uncheck();
    await page.getByRole('dialog').getByRole('button', {name: 'Switch off'}).click();
    await expect(box).not.toBeChecked();
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeEnabled();
});

test('advanced: a row moves by its handle, fixed rows have none, Test plays a row', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const order = () => page.locator('[data-led-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-led-row')));
    expect((await order()).slice(0, 3)).toEqual(['radio-down', 'no-network', 'service-failed']);
    await page.getByRole('button', {name: 'Move No network'}).press('ArrowDown');
    await expect.poll(async () => (await order()).slice(0, 3)).toEqual(['radio-down', 'service-failed', 'no-network']);
    // the first row goes no higher
    await page.getByRole('button', {name: 'Move Radio or interface down'}).press('ArrowUp');
    expect((await order()).slice(0, 3)).toEqual(['radio-down', 'service-failed', 'no-network']);
    for (const id of ['shutdown', 'booting', 'locate', 'normal']) {
        await expect(page.locator(`[data-led-fixed="${id}"]`).getByRole('button', {name: /^Move /})).toHaveCount(0);
    }
    // task 315: a row's look is edited in the form its Change… opens - task 56: inline, under the row,
    // the button saying it is open; a colour, a pattern that needs a second colour, and Test sends
    // exactly that look
    const row = page.locator('[data-led-row="status-warning"]');
    await row.getByRole('button', {name: 'Change…'}).click();
    const panel = page.locator('[data-led-panel="status-warning"]');
    await expect(panel).toBeVisible();
    await expect(row.getByRole('button', {name: 'Change…'})).toHaveAttribute('aria-expanded', 'true');
    await panel.getByLabel('Colour of Status page warnings').selectOption('yellow');
    await panel.getByLabel('Pattern of Status page warnings').selectOption('alternate');
    await expect(panel.getByLabel('Second colour of Status page warnings')).toHaveValue('blue');
    await expect(row.locator('[data-led-look]')).toHaveText('Yellow and blue, alternating');
    const preview = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/led/preview'));
    await panel.getByRole('button', {name: 'Test'}).click();
    expect((await preview).postDataJSON()).toEqual({color: 'yellow', pattern: 'alternate', color2: 'blue'});
    await expect(page.locator('[data-led-now]')).toHaveAttribute('data-led-now', 'preview');
    // Cancel puts the row's look back and closes the form; the locate row's form takes the minutes
    await row.locator('[data-led-form-cancel]').click();
    await expect(row.locator('[data-led-form]')).toHaveCount(0);
    await expect(row.getByRole('button', {name: 'Change…'})).toHaveAttribute('aria-expanded', 'false');
    await expect(row.locator('[data-led-look]')).not.toHaveText('Yellow and blue, alternating');
    await page.locator('[data-led-fixed="locate"]').getByRole('button', {name: 'Change…'}).click();
    await page.locator('[data-led-panel="locate"]').getByLabel('Minutes').fill('10');
    await page.locator('[data-led-panel="locate"]').getByLabel('Minutes').blur();
    await expect(page.locator('[data-led-fixed="locate"]')).toContainText('for 10 minutes');
    // the disclosure is remembered per browser; Reset brings the defaults back into the draft
    await page.reload();
    await expect(page.locator('[data-led-advanced]')).toHaveAttribute('open', '');
    await page.getByRole('button', {name: 'Move No network'}).press('ArrowDown');
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeEnabled();
    await page.getByRole('button', {name: 'Reset to defaults'}).click();
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
});

// task 134: a blinking state can blink over the normal colour instead of over dark, opt-in per row
test('advanced: blinking over the normal colour is offered for the four patterns, off by default but for interfaces-starting, saved and reloaded', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    const errors = watchErrors(page);
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    // task 315: the option sits in the row's look panel
    const open = async (id: string) => {
        await page.locator(`[data-led-row="${id}"]`).getByRole('button', {name: 'Change…'}).click();
        await expect(page.locator(`[data-led-panel="${id}"]`)).toBeVisible();
    };
    const over = (id: string) => page.locator(`[data-led-panel="${id}"] [data-led-over="${id}"]`);
    const box = (id: string) => over(id).getByRole('checkbox', {name: 'Blink over the normal colour'});
    // off by default wherever it is offered; not offered for solid, nor for the external row
    for (const id of ['no-network', 'service-failed', 'storage-replace', 'status-warning', 'system-update', 'addon-update']) {
        await open(id);
        await expect(box(id)).not.toBeChecked();
    }
    // task 158: interfaces-starting is the one default that blinks over the normal colour -
    // double magenta over blue, below the errors and above the warnings
    await open('interfaces-starting');
    await expect(box('interfaces-starting')).toBeChecked();
    const row = page.locator('[data-led-row="interfaces-starting"]');
    await expect(row).toContainText('Interfaces starting');
    const panel = page.locator('[data-led-panel="interfaces-starting"]');
    await expect(panel.getByLabel('Colour of Interfaces starting')).toHaveValue('magenta');
    await expect(panel.getByLabel('Pattern of Interfaces starting')).toHaveValue('double');
    const ids = await page.locator('[data-led-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-led-row')));
    expect(ids.indexOf('interfaces-starting')).toBe(ids.indexOf('storage-replace') + 1);
    expect(ids.indexOf('interfaces-starting')).toBeLessThan(ids.indexOf('status-warning'));
    await open('radio-down');
    await expect(over('radio-down')).toHaveCount(0);
    await expect(page.locator('[data-led-row="external"]').getByRole('button', {name: 'Change…'})).toHaveCount(0);
    // the four patterns only (breathe would be a fifth on a dimmable LED)
    const radio = page.locator('[data-led-panel="radio-down"]');
    for (const p of ['slow', 'fast', 'flash', 'double']) {
        await radio.getByLabel('Pattern of Radio or interface down').selectOption(p);
        await expect(box('radio-down')).toBeVisible();
    }
    await radio.getByLabel('Pattern of Radio or interface down').selectOption('alternate');
    await expect(over('radio-down')).toHaveCount(0);
    await radio.getByLabel('Pattern of Radio or interface down').selectOption('solid');
    await expect(over('radio-down')).toHaveCount(0);
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    // blue fast over blue would not be seen: the page says so instead of offering it
    await open('no-internet');
    await expect(over('no-internet')).toHaveText('Blinking over the normal colour is not offered: it is the same colour, the blink would not be seen.');
    await expect(over('no-internet').getByRole('checkbox')).toHaveCount(0);
    // on, the texts and Test say it
    await open('status-warning');
    await box('status-warning').check();
    await expect(page.locator('[data-led-simple="status-warning"]')).toContainText('Yellow, slow blink over blue');
    await expect(page.locator('[data-led-simple="status-warning"] .ol-led-swatch')).toHaveAttribute('data-background', 'blue');
    const preview = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/led/preview'));
    await page.locator('[data-led-row="status-warning"] > .ol-led-controls').getByRole('button', {name: 'Test'}).click();
    expect((await preview).postDataJSON()).toEqual({color: 'yellow', pattern: 'slow', over_normal: true});
    await expect(page.locator('[data-led-now]')).toContainText('Yellow, slow blink over blue');
    // saved with the row alone, and there after a reload
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await page.locator('.ol-led-save').getByRole('button', {name: 'Save'}).click();
    const states = (await put).postDataJSON().states as {id: string; over_normal?: boolean}[];
    expect(states.filter((s) => s.over_normal).map((s) => s.id)).toEqual(['interfaces-starting', 'status-warning']);
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    await page.reload();
    await open('status-warning');
    await expect(box('status-warning')).toBeChecked();
    await open('system-update');
    await expect(box('system-update')).not.toBeChecked();
    // a pattern without a dark phase drops it; switched off, the draft equals the defaults again
    await open('status-warning');
    await page.locator('[data-led-panel="status-warning"]').getByLabel('Pattern of Status page warnings').selectOption('solid');
    await expect(over('status-warning')).toHaveCount(0);
    await page.locator('[data-led-panel="status-warning"]').getByLabel('Pattern of Status page warnings').selectOption('slow');
    await expect(box('status-warning')).not.toBeChecked();
    await page.locator('.ol-led-save').getByRole('button', {name: 'Save'}).click();
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    await page.getByRole('button', {name: 'Reset to defaults'}).click();
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    // normal off: still offered (it takes effect once normal lights again), and the text says plain blinking
    await page.getByRole('radio', {name: 'Off'}).click();
    await open('system-update');
    await box('system-update').check();
    await expect(page.locator('[data-led-simple="system-update"]')).toContainText('Cyan, slow blink ·');
    expect(await fitsWindow(page)).toBe(true);
    expect(errors, errors.join('\n')).toEqual([]);
});

// task 95 (D-67): the no-internet state connects to a host the box talks to anyway, and says which
test('the no-internet row names the host its check connects to', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const line = page.locator('[data-led-row="no-internet"] [data-led-internet]');
    await expect(line).toHaveText('the system has an address but reaches none of the hosts it talks to anyway: catalogue.example.org');
    await expect(page.locator('[data-led-advanced]')).not.toContainText('google');
    expect(await fitsWindow(page)).toBe(true);
});

test('in German: no host to check against', async ({page, baseURL}, info) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await plant(page, baseURL!, info, {'stub-led-internet': 'none'});
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    await expect(page.locator('[data-led-row="no-internet"] [data-led-internet]')).toHaveText('nichts zum Prüfen: der Addon-Katalog ist ausgeschaltet oder lokal, und ACME ist nicht eingerichtet');
});

test('Locate calls locate, and stops', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    const post = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/led/locate'));
    await page.getByRole('button', {name: 'Locate'}).click();
    await post;
    await expect(page.locator('[data-led-now]')).toHaveAttribute('data-led-now', 'locate');
    await expect(page.locator('[data-led-now]')).toContainText('White, fast blink');
    const del = page.waitForRequest((r) => r.method() === 'DELETE' && r.url().endsWith('/api/system/v1/led/locate'));
    await page.getByRole('button', {name: 'Stop locating'}).click();
    await del;
    await expect(page.locator('[data-led-now]')).toHaveAttribute('data-led-now', 'normal');
});

test('a box without a status LED offers the power LED and the board LEDs under Raspberry Pi LEDs', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-hw': 'none'});
    await page.goto('/system/hardware');
    // task 58: no RPI-RF-MOD section without the module's LED; the Pi's own LEDs in their panel
    await expect(page.getByRole('heading', {level: 2, name: 'Raspberry Pi LEDs'})).toBeVisible();
    await expect(page.getByRole('heading', {level: 2, name: 'RPI-RF-MOD LED'})).toHaveCount(0);
    await expect(page.locator('[data-notice="no-led"]')).toHaveCount(0);
    await expect(page.locator('[data-led-now]')).toHaveCount(0);
    const panel = page.locator('[data-panel="pi-leds"]');
    const pwr = panel.locator('[data-led="pwr"]');
    await pwr.check();
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await panel.getByRole('button', {name: 'Save'}).click();
    expect((await put).postDataJSON().pwr_error_light).toBe(true);
    await expect(panel.getByRole('button', {name: 'Switch the onboard LEDs off'})).toBeVisible();
});

test('a box with neither the status LED nor the board LEDs says so (task 203: a plain panel)', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-hw': 'none'});
    await page.route('**/api/system/v1/leds', (r) => r.fulfill({json: {disabled: false, leds: []}}));
    await page.route('**/api/system/v1/led', (r) => (r.request().method() === 'GET' ? r.fetch().then(async (resp) => r.fulfill({json: {...(await resp.json()), pwr: false}})) : r.continue()));
    await page.goto('/system/hardware');
    await expect(page.locator('[data-notice="no-led"]')).toContainText('This system has no status LED. The radio module has no status LED.');
    const plain = await page.locator('[data-notice="no-led"]').evaluate((el) => {
        const cs = getComputedStyle(el);
        return {panel: el.classList.contains('ol-panel'), severity: el.getAttribute('data-severity'), widths: [cs.borderTopWidth, cs.borderRightWidth, cs.borderBottomWidth, cs.borderLeftWidth]};
    });
    expect(plain.panel).toBe(true);
    expect(plain.severity).toBe(null);
    expect(plain.widths).toEqual(['1px', '1px', '1px', '1px']);
    await expect(page.getByRole('heading', {level: 2, name: 'Raspberry Pi LEDs'})).toHaveCount(0);
});

test('German on a phone: every text translated, nothing wider than the screen', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-active': 'radio-down'});
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.setViewportSize({width: 390, height: 844});
    await page.goto('/system/hardware');
    await expect(page.getByRole('heading', {level: 1, name: 'System › Hardware'})).toBeVisible();
    await expect(page.locator('[data-led-now]')).toContainText('Rot, dauerhaft');
    await page.locator('[data-led-advanced] summary').click();
    await page.locator('[data-led-legend] summary').click();
    await expect(page.getByRole('button', {name: 'Finden'})).toBeVisible();
    await page.locator('[data-led-row="status-warning"]').getByRole('button', {name: 'Ändern…'}).click();
    await expect(page.locator('[data-led-over="status-warning"]')).toContainText('Über der Normalfarbe blinken');
    await expect(page.locator('[data-led-capability]')).toContainText('Diese LED schaltet');
    expect(await fitsWindow(page)).toBe(true);
});

// openccu-lite task 315: on an RPI-RF-MOD driven over PWM the LED has levels - any colour, breathe,
// brightness, a fade between looks, the night's level; an on/off LED offers none of it
test('a dimmable LED: a free colour, breathe, brightness, fade and the night level, saved as the API spells them', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-hw': 'pwm'});
    const errors = watchErrors(page);
    await page.goto('/system/hardware');
    await expect(page.locator('[data-led-capability]')).toHaveAttribute('data-led-capability', 'pwm');
    const brightness = page.locator('[data-led="brightness"]');
    await expect(brightness).toHaveValue('100');
    await expect(page.locator('[data-led="fade"]')).toBeChecked();
    await brightness.fill('60');
    await expect(page.locator('.ol-led-pct').first()).toHaveText('60 %');
    await page.locator('[data-led="fade"]').uncheck();
    // the night: everything dimmed, to a level
    await page.locator('[data-led="night"]').check();
    await page.getByRole('radio', {name: 'Everything, dimmed'}).click();
    const dim = page.locator('[data-led="night-dim"]');
    await expect(dim).toHaveValue('30');
    await dim.fill('20');
    // a state in a free colour, breathing over the normal colour
    await page.locator('[data-led-advanced] summary').click();
    await page.locator('[data-led-row="status-warning"]').getByRole('button', {name: 'Change…'}).click();
    const panel = page.locator('[data-led-panel="status-warning"]');
    await panel.getByLabel('Pattern of Status page warnings').selectOption('breathe');
    await panel.locator('[data-led-picker]').fill('#ff8000');
    await expect(panel.getByLabel('Colour of Status page warnings', {exact: true})).toHaveValue('custom');
    await expect(page.locator('[data-led-row="status-warning"] [data-led-look]')).toHaveText('#ff8000, breathing');
    await panel.getByRole('checkbox', {name: 'Blink over the normal colour'}).check();
    await expect(page.locator('[data-led-simple="status-warning"]')).toContainText('#ff8000, breathing over blue');
    // the picker back on a preset's value: the name again
    await panel.locator('[data-led-picker]').fill('#ff0000');
    await expect(panel.getByLabel('Colour of Status page warnings', {exact: true})).toHaveValue('red');
    await panel.locator('[data-led-picker]').fill('#ff8000');
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await page.locator('.ol-led-save').getByRole('button', {name: 'Save'}).click();
    const body = (await put).postDataJSON();
    expect(body.brightness).toBe(60);
    expect(body.fade).toBe(false);
    expect(body.night.show).toBe('dimmed');
    expect(body.night.dim).toBe(20);
    expect(body.states.find((s: {id: string}) => s.id === 'status-warning')).toEqual({id: 'status-warning', enabled: true, color: '#ff8000', pattern: 'breathe', over_normal: true});
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    expect(await fitsWindow(page)).toBe(true);
    expect(errors, errors.join('\n')).toEqual([]);
});

// openccu-lite task 316: the radio load's levels - two rows, off by default, each with a panel of three levels
test('the radio levels: rows off by default, a level switched on in the panel, saved; an active level shows its look', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-hw': 'pwm', 'stub-led-active': 'radio-duty-cycle'});
    const errors = watchErrors(page);
    await page.goto('/system/hardware');
    // the row is off in the stub, so the planted state does not show; switch it on first
    await page.locator('[data-led-advanced] summary').click();
    const row = page.locator('[data-led-row="radio-duty-cycle"]');
    await expect(row).toContainText('Radio: duty cycle');
    await expect(row.locator('[data-led-look]')).toHaveText('no level switched on');
    const ids = await page.locator('[data-led-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-led-row')));
    expect(ids.indexOf('radio-duty-cycle')).toBe(ids.indexOf('interfaces-starting') + 1);
    expect(ids.indexOf('radio-carrier-sense')).toBe(ids.indexOf('radio-duty-cycle') + 1);
    expect(ids.indexOf('external')).toBe(ids.indexOf('radio-carrier-sense') + 1);
    await row.getByRole('button', {name: 'Levels…'}).click();
    const panel = page.locator('[data-led-panel="radio-duty-cycle"]');
    await expect(panel.locator('[data-led-level]')).toHaveCount(3);
    const l2 = panel.locator('[data-led-level="2"]');
    await expect(l2.getByLabel('Threshold of level 2 of Radio: duty cycle')).toHaveValue('50');
    await expect(l2.getByLabel('Colour of level 2 of Radio: duty cycle', {exact: true})).toHaveValue('custom');
    await expect(l2.getByRole('radio', {name: 'Overlay'})).toHaveAttribute('aria-checked', 'true');
    await l2.getByLabel('Level 2 of Radio: duty cycle', {exact: true}).check();
    await l2.getByLabel('Threshold of level 2 of Radio: duty cycle').fill('55');
    await l2.getByLabel('Threshold of level 2 of Radio: duty cycle').blur();
    await l2.getByRole('radio', {name: 'Replace'}).click();
    await expect(row.locator('[data-led-look]')).toHaveText('> 55 %: #ff4000, slow blink');
    // a steady light cannot overlay
    const l3 = panel.locator('[data-led-level="3"]');
    await l3.getByLabel('Pattern of level 3 of Radio: duty cycle').selectOption('solid');
    await expect(l3.getByRole('radio', {name: 'Overlay'})).toBeDisabled();
    await row.getByRole('checkbox', {name: 'Radio: duty cycle', exact: true}).check();
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await page.locator('.ol-led-save').getByRole('button', {name: 'Save'}).click();
    const body = (await put).postDataJSON();
    expect(body.radio.duty_cycle[1]).toEqual({enabled: true, threshold: 55, color: '#ff4000', pattern: 'slow'});
    expect(body.radio.duty_cycle[2].pattern).toBe('solid');
    expect(body.states.find((s: {id: string}) => s.id === 'radio-duty-cycle')).toEqual({id: 'radio-duty-cycle', enabled: true});
    // the stub plants level 2 of the row once it is on: the LED shows the level's look, the detail the interface
    await expect(page.locator('[data-led-now]')).toHaveAttribute('data-led-now', 'radio-duty-cycle');
    await expect(page.locator('[data-led-now]')).toContainText('#ff4000, slow blink');
    await expect(page.locator('[data-led-now]')).toContainText('Radio: duty cycle: HmIP-RF 63 %');
    // the legend lists the level
    await page.locator('[data-led-legend] summary').click();
    await expect(page.locator('[data-led-legend]')).toContainText('Radio: duty cycle > 55 %');
    expect(await fitsWindow(page)).toBe(true);
    expect(errors, errors.join('\n')).toEqual([]);
});

test('an on/off LED offers neither a picker nor breathe nor the levels', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await expect(page.locator('[data-led-capability]')).toHaveAttribute('data-led-capability', 'on-off');
    await expect(page.locator('[data-led="brightness"]')).toHaveCount(0);
    await expect(page.locator('[data-led="fade"]')).toHaveCount(0);
    await page.locator('[data-led="night"]').check();
    await expect(page.getByRole('radio', {name: 'Everything, dimmed'})).toHaveCount(0);
    await expect(page.locator('[data-led="night-dim"]')).toHaveCount(0);
    await page.locator('[data-led-advanced] summary').click();
    await page.locator('[data-led-row="status-warning"]').getByRole('button', {name: 'Change…'}).click();
    const panel = page.locator('[data-led-panel="status-warning"]');
    await expect(panel.locator('[data-led-picker]')).toHaveCount(0);
    await expect(panel.getByLabel('Pattern of Status page warnings').locator('option[value="breathe"]')).toHaveCount(0);
});

// openccu-lite task 326: the RPI-RF-MOD on a radio adapter - its LED is on/off whatever the image,
// said so on the page, with the HB-RF-ETH's own brightness setting; no picker, no breathe, no levels
for (const kind of ['hb-rf-usb-2', 'hb-rf-eth'] as const) {
    test(`an LED on the radio adapter (${kind}) is on/off, and the page says so`, async ({page, baseURL}, info) => {
        await plant(page, baseURL!, info);
        await page.route('**/api/system/v1/led', async (route) => {
            if (route.request().method() !== 'GET') return route.fallback();
            const r = await route.fetch();
            const body = await r.json();
            body.hardware = {...body.hardware, kind, brightness: false, max_brightness: 1, patterns: (body.hardware.patterns as string[]).filter((p) => p !== 'breathe' && p !== 'double')};
            await route.fulfill({response: r, json: body});
        });
        await page.goto('/system/hardware');
        const line = page.locator('[data-led-capability]');
        await expect(line).toHaveAttribute('data-led-capability', 'adapter');
        await expect(line).toContainText('on/off LED on the radio adapter');
        await expect(page.locator('[data-led-eth-hint]')).toHaveCount(kind === 'hb-rf-eth' ? 1 : 0);
        await expect(page.locator('[data-led="brightness"]')).toHaveCount(0);
        await page.locator('[data-led-advanced] summary').click();
        await page.locator('[data-led-row="status-warning"]').getByRole('button', {name: 'Change…'}).click();
        const panel = page.locator('[data-led-panel="status-warning"]');
        await expect(panel.locator('[data-led-picker]')).toHaveCount(0);
        await expect(panel.getByLabel('Pattern of Status page warnings').locator('option[value="breathe"]')).toHaveCount(0);
        // in German
        await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
        await page.goto('/system/hardware');
        await expect(line).toContainText('auf dem Funkadapter');
        if (kind === 'hb-rf-eth') await expect(page.locator('[data-led-eth-hint]')).toContainText('Weboberfläche des HB-RF-ETH');
    });
}

test('a user reads the page and changes nothing', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: USER}));
    await page.goto('/system/hardware');
    await expect(page.locator('[data-led-now]')).toBeVisible();
    await expect(page.locator('[data-led="enabled"]')).toBeDisabled();
    await expect(page.getByRole('button', {name: 'Locate'})).toHaveCount(0);
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toHaveCount(0);
});

// occulited task 57: the device firmware's two rows - off by default, below the warnings, listed,
// switched on and saved like the others; an active one shown with its link to the Updates page
test('the firmware rows: transferred and waiting for installation, off by default, switched on and saved', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-led-active': 'firmware-transfer'});
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const ids = await page.locator('[data-led-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-led-row')));
    expect(ids.indexOf('firmware-transfer')).toBe(ids.indexOf('status-warning') + 1);
    expect(ids.indexOf('firmware-ready')).toBe(ids.indexOf('firmware-transfer') + 1);
    const tr = page.locator('[data-led-row="firmware-transfer"]');
    const rd = page.locator('[data-led-row="firmware-ready"]');
    await expect(tr).toContainText('Device firmware being transferred');
    await expect(rd).toContainText('Device firmware waiting for installation');
    await expect(tr.getByRole('checkbox', {name: 'Device firmware being transferred', exact: true})).not.toBeChecked();
    await expect(rd.getByRole('checkbox', {name: 'Device firmware waiting for installation', exact: true})).not.toBeChecked();
    // off: the LED shows normal although a transfer is planted
    await expect(page.locator('[data-led-now]')).toHaveAttribute('data-led-now', 'normal');
    await tr.getByRole('checkbox', {name: 'Device firmware being transferred', exact: true}).check();
    await rd.getByRole('checkbox', {name: 'Device firmware waiting for installation', exact: true}).check();
    await rd.getByRole('button', {name: 'Change…'}).click();
    await rd.locator('[data-led-form]').getByLabel('Pattern of Device firmware waiting for installation').selectOption('flash');
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await page.locator('.ol-led-save').getByRole('button', {name: 'Save'}).click();
    const states = (await put).postDataJSON().states as {id: string; enabled: boolean; pattern?: string}[];
    expect(states.find((s) => s.id === 'firmware-transfer')).toMatchObject({enabled: true, color: 'blue', pattern: 'breathe'});
    expect(states.find((s) => s.id === 'firmware-ready')).toMatchObject({enabled: true, color: 'green', pattern: 'flash'});
    await expect(page.locator('[data-led-now]')).toHaveAttribute('data-led-now', 'firmware-transfer');
    await expect(page.locator('[data-led-now]').getByRole('link', {name: 'Open page'})).toHaveAttribute('href', '/system/updates');
});
