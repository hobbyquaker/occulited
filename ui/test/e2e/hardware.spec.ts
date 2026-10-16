import {type Page, type TestInfo} from '@playwright/test';
import {expect, test} from './fixtures';

// openccu-lite task 340: System → Hardware, the former Status LED page, with its HDMI section
// (off unless switched on), and task 58's headings and panels. The stub keeps a test's HDMI
// switch per stub-hdmi cookie; stub-hdmi=none is a system that is not a Pi.

async function plant(page: Page, baseURL: string, info: TestInfo, cookies: Record<string, string> = {}) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies(Object.entries({'stub-hdmi': id, 'stub-led': id, 'stub-button': id, ...cookies}).map(([name, value]) => ({name, value, url: baseURL})));
}

test('the Hardware page: the LED sections in their panels, then HDMI, off by default and switched on at once', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await expect(page.getByRole('heading', {level: 1, name: 'System › Hardware'})).toBeVisible();
    // task 58: the section headings in their order, each block in its panel
    await expect(page.getByRole('heading', {level: 2})).toHaveText(['RPI-RF-MOD LED', 'Raspberry Pi LEDs', 'Radio module button', 'HDMI']);
    const panels = page.locator('[data-panel]');
    await expect(panels).toHaveCount(7);
    expect(await panels.evaluateAll((els) => els.map((e) => e.getAttribute('data-panel')))).toEqual(['led-settings', 'led-show', 'led-states', 'led-legend', 'pi-leds', 'button', 'hdmi']);
    await expect(page.locator('[data-panel="led-settings"] .ol-card-title')).toHaveText('Settings');
    await expect(page.locator('[data-panel="led-show"] .ol-card-title')).toHaveText('Show on the LED');
    await expect(page.locator('[data-panel="led-states"] summary')).toHaveText('Customise states and order');
    await expect(page.locator('[data-panel="led-legend"] summary')).toHaveText('What the colours mean');
    await expect(page.locator('[data-panel="led-settings"] [data-led="enabled"]')).toBeVisible();
    await expect(page.locator('[data-panel="led-show"] [data-led-simple]').first()).toBeVisible();

    const hdmi = page.locator('[data-panel="hdmi"]');
    const box = hdmi.getByRole('checkbox', {name: 'HDMI output on'});
    await expect(box).not.toBeChecked();
    await expect(hdmi.locator('[data-hdmi-state]')).toHaveText('The output is off. A monitor shows the boot and the recovery system.');
    await expect(hdmi.locator('[data-hdmi-connectors] li')).toHaveText(['HDMI-A-1 no output', 'HDMI-A-2 no output']);
    const put = page.waitForRequest((r) => r.url().endsWith('/api/system/v1/hardware/hdmi') && r.method() === 'PUT');
    await box.check();
    expect((await put).postDataJSON()).toEqual({enabled: true});
    await expect(hdmi.locator('[data-hdmi-state]')).toHaveText('The output is on.');
    await expect(hdmi.locator('[data-hdmi-connectors] li').first()).toHaveText('HDMI-A-1 output active');
    await page.reload();
    await expect(page.locator('[data-panel="hdmi"]').getByRole('checkbox', {name: 'HDMI output on'})).toBeChecked();
});

test('the old Status LED path opens the Hardware page', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/led');
    await expect(page).toHaveURL(/\/system\/hardware$/);
    await expect(page.getByRole('heading', {level: 2, name: 'RPI-RF-MOD LED'})).toBeVisible();
});

test('a system that is not a Pi has no HDMI section', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-hdmi': 'none'});
    await page.goto('/system/hardware');
    await expect(page.getByRole('heading', {level: 2, name: 'RPI-RF-MOD LED'})).toBeVisible();
    await expect(page.locator('[data-panel="hdmi"]')).toHaveCount(0);
    await expect(page.getByRole('heading', {level: 2, name: 'HDMI'})).toHaveCount(0);
});

test('a failed HDMI switch says so and the switch goes back', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-hdmi-fail': '1'});
    await page.goto('/system/hardware');
    const hdmi = page.locator('[data-panel="hdmi"]');
    const box = hdmi.getByRole('checkbox', {name: 'HDMI output on'});
    await box.click();
    await expect(hdmi.locator('.ol-notice.error')).toContainText('permission denied');
    await expect(box).not.toBeChecked();
});

test('German: the Hardware page and its HDMI section', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/hardware');
    await expect(page.getByRole('heading', {level: 1, name: 'System › Hardware'})).toBeVisible();
    await expect(page.locator('[data-panel="led-settings"] .ol-card-title')).toHaveText('Einstellungen');
    await expect(page.locator('[data-panel="led-show"] .ol-card-title')).toHaveText('Auf der LED anzeigen');
    await expect(page.locator('[data-panel="led-states"] summary')).toHaveText('Zustände und Reihenfolge anpassen');
    await expect(page.locator('[data-panel="led-legend"] summary')).toHaveText('Was die Farben bedeuten');
    const hdmi = page.locator('[data-panel="hdmi"]');
    await expect(hdmi.getByRole('checkbox', {name: 'HDMI-Ausgang an'})).not.toBeChecked();
    await expect(hdmi.locator('[data-hdmi-state]')).toHaveText('Der Ausgang ist aus. Ein Monitor zeigt den Start und das Recovery-System.');
});

// openccu-lite task 164: the radio module's button - the defaults (single mutes, double and long
// nothing), the live state, the long press's choices, saved; on an adapter the reason; without
// the module no section
test('the radio module button: the defaults, the live state, the gestures set and saved', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-button-pressed': '1'});
    await page.goto('/system/hardware');
    const panel = page.locator('[data-panel="button"]');
    await expect(panel.locator('[data-button-live]')).toHaveAttribute('data-button-live', 'pressed');
    await expect(panel.locator('[data-button-live]')).toContainText('Pressed');
    await expect(panel.locator('[data-button-last]')).toContainText('Single press');
    await expect(panel.locator('[data-button="single"]')).toHaveValue('mute');
    await expect(panel.locator('[data-button="double"]')).toHaveValue('none');
    await expect(panel.locator('[data-button="long"]')).toHaveValue('none');
    await expect(panel.locator('[data-button="long_s"]')).toHaveValue('5');
    await expect(panel.locator('[data-button="single"] option')).toHaveText(['Nothing', 'Mute the LED warnings', 'Locate']);
    await expect(panel.locator('[data-button="long"] option')).toHaveText(['Nothing', 'Mute the LED warnings', 'Locate', 'Reboot into the recovery system', 'Reboot']);
    await expect(panel.locator('[data-button-poweron]')).toContainText('recovery system starts');
    const save = panel.locator('[data-button-save]');
    await expect(save).toBeDisabled();
    await panel.locator('[data-button="long"]').selectOption('recovery');
    await panel.locator('[data-button="double"]').selectOption('locate');
    await panel.locator('[data-button="long_s"]').fill('3');
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/hardware/button'));
    await save.click();
    expect((await put).postDataJSON()).toEqual({single: 'mute', double: 'locate', long: 'recovery', long_s: 3, mute_minutes: 60});
    await expect(panel.locator('.ol-notice')).toHaveText('Saved.');
    await page.reload();
    await expect(page.locator('[data-panel="button"] [data-button="long"]')).toHaveValue('recovery');
});

test('the button on a radio adapter says why it is not available; without the module there is no section', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info, {'stub-button-reason': 'adapter'});
    await page.goto('/system/hardware');
    await expect(page.locator('[data-panel="button"] [data-button-reason="adapter"]')).toContainText('does not reach the system');
    await expect(page.locator('[data-panel="button"] [data-button="single"]')).toHaveCount(0);
    await plant(page, baseURL!, info, {'stub-button-reason': 'no-module'});
    await page.reload();
    await expect(page.getByRole('heading', {level: 2, name: 'RPI-RF-MOD LED'})).toBeVisible();
    await expect(page.locator('[data-panel="button"]')).toHaveCount(0);
});

test('a mute shows in the LED section, with Unmute', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await expect(page.locator('[data-led-now]')).toBeVisible();
    const r = await page.request.post('/api/system/v1/led/mute', {data: {minutes: 30}, headers: {'X-Occulite-Request': '1'}});
    expect(r.ok()).toBe(true);
    await page.reload();
    const muted = page.locator('[data-led-muted]');
    await expect(muted).toContainText('The LED warnings are muted until');
    await muted.locator('[data-led-unmute]').click();
    await expect(muted).toHaveCount(0);
});

test('German: the button section', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/hardware');
    await expect(page.getByRole('heading', {level: 2, name: 'Taste am Funkmodul'})).toBeVisible();
    const panel = page.locator('[data-panel="button"]');
    await expect(panel.locator('[data-button="single"] option')).toHaveText(['Nichts', 'Warnungen der LED stummschalten', 'Finden']);
    await expect(panel).toContainText('Einfacher Druck');
    await expect(panel).toContainText('Doppelter Druck');
    await expect(panel).toContainText('Langer Druck');
});
