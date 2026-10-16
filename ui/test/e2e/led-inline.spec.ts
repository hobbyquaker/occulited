import {type Locator, type Page, type TestInfo} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 56 and B-73 (the maintainer, 2026-10-15): every Change… of the LED section opens its
// form inline, directly under the row - or the Settings line - it belongs to, inside the same panel;
// one at a time, Esc and Cancel put the look back, Save saves, another row with changes asks first.

async function plant(page: Page, baseURL: string, info: TestInfo) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies([{name: 'stub-led', value: id, url: baseURL}]);
}

/** the form's top sits just under the button's bottom, and the form inside the row and the panel */
async function under(form: Locator, button: Locator, row: Locator, panel: Locator) {
    const [f, b, r, p] = await Promise.all([form.boundingBox(), button.boundingBox(), row.boundingBox(), panel.boundingBox()]);
    expect(f && b && r && p).toBeTruthy();
    expect(f!.y).toBeGreaterThanOrEqual(b!.y + b!.height - 1);
    expect(f!.y - (b!.y + b!.height)).toBeLessThan(48);
    expect(f!.y + f!.height).toBeLessThanOrEqual(r!.y + r!.height + 1);
    expect(f!.y).toBeGreaterThanOrEqual(p!.y);
    expect(f!.y + f!.height).toBeLessThanOrEqual(p!.y + p!.height + 1);
}

test('Change… opens the form under its own row: the 2nd row and the last, one at a time', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const panel = page.locator('[data-panel="led-states"]');
    const rows = panel.locator('[data-led-row]');
    const second = rows.nth(1);
    const id = await second.getAttribute('data-led-row');
    const btn = second.getByRole('button', {name: /^(Change|Levels)…$/});
    await expect(btn).toHaveAttribute('aria-expanded', 'false');
    await btn.click();
    const form = second.locator(`[data-led-form="${id}"]`);
    await expect(form).toBeVisible();
    await expect(btn).toHaveAttribute('aria-expanded', 'true');
    await under(form, btn, second, panel);
    await expect(form.locator('select, input').first()).toBeFocused();
    // the last row: Everything fine
    const last = panel.locator('[data-led-fixed="normal"]');
    const lastBtn = last.getByRole('button', {name: 'Change…'});
    await lastBtn.click();
    await expect(page.locator('[data-led-form]')).toHaveCount(1);
    await expect(form).toHaveCount(0);
    await expect(btn).toHaveAttribute('aria-expanded', 'false');
    const lastForm = last.locator('[data-led-form="normal-row"]');
    await expect(lastForm).toBeVisible();
    await under(lastForm, lastBtn, last, panel);
    // its own button closes it again, the focus stays on the button
    await lastBtn.click();
    await expect(page.locator('[data-led-form]')).toHaveCount(0);
    await expect(lastBtn).toBeFocused();
});

test('B-73: Everything fine\'s Change… in Settings opens under that line, inside the Settings panel', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    const settings = page.locator('[data-panel="led-settings"]');
    const btn = page.locator('[data-action="edit-normal"]');
    await btn.click();
    const form = settings.locator('[data-led-form="normal"]');
    await expect(form).toBeVisible();
    await under(form, btn, settings, settings);
    // nothing opened in the states panel, which is still closed
    await expect(page.locator('[data-panel="led-states"] [data-led-form]')).toHaveCount(0);
    await expect(page.locator('[data-led-advanced]')).not.toHaveAttribute('open', '');
});

test('Esc and Cancel put the look back; another row with changes asks first; Save saves and closes', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/system/hardware');
    await page.locator('[data-led-advanced] summary').click();
    const row = page.locator('[data-led-row="status-warning"]');
    const look = row.locator('[data-led-look]');
    const was = await look.textContent();
    const btn = row.getByRole('button', {name: 'Change…'});
    // Esc: back to what it was, the form gone, the focus on the button
    await btn.click();
    const form = row.locator('[data-led-form]');
    await form.getByLabel('Colour of Status page warnings', {exact: true}).selectOption('red');
    await expect(look).not.toHaveText(was!);
    await form.getByLabel('Colour of Status page warnings', {exact: true}).press('Escape');
    await expect(form).toHaveCount(0);
    await expect(look).toHaveText(was!);
    await expect(btn).toBeFocused();
    // Cancel: the same
    await btn.click();
    await form.getByLabel('Pattern of Status page warnings').selectOption('fast');
    await form.locator('[data-led-form-cancel]').click();
    await expect(form).toHaveCount(0);
    await expect(look).toHaveText(was!);
    // another row while this one has changes: the shell's dialog, never window.confirm
    await btn.click();
    await form.getByLabel('Pattern of Status page warnings').selectOption('fast');
    await page.locator('[data-led-row="system-update"]').getByRole('button', {name: 'Change…'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Discard the changes to Status page warnings?');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(form).toBeVisible();
    await expect(page.locator('[data-led-row="system-update"] [data-led-form]')).toHaveCount(0);
    await page.locator('[data-led-row="system-update"]').getByRole('button', {name: 'Change…'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Discard'}).click();
    await expect(page.locator('[data-led-row="system-update"] [data-led-form]')).toBeVisible();
    await expect(form).toHaveCount(0);
    await expect(look).toHaveText(was!);
    // Save: the configuration as before, the form closed
    const other = page.locator('[data-led-row="system-update"]');
    await other.locator('[data-led-form]').getByLabel('Pattern of System update available').selectOption('fast');
    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/led'));
    await other.locator('[data-led-form-save]').click();
    const body = (await put).postDataJSON();
    expect(body.states.find((s: {id: string}) => s.id === 'system-update').pattern).toBe('fast');
    await expect(other.locator('[data-led-form]')).toHaveCount(0);
    await expect(page.locator('.ol-led-save').getByRole('button', {name: 'Save'})).toBeDisabled();
    await page.reload();
    await expect(page.locator('[data-led-row="system-update"] [data-led-look]')).toHaveText('Cyan, fast blink');
});
