import {expect, test} from '@playwright/test';
import {fitsWindow} from './scroll';

// occulited task 59 (the maintainer, 2026-10-15): "system - sicherungen: page should be grouped into
// panels". Every section of the Backup page is one panel under its heading; what stands apart inside
// a section (the paired devices of a restore, the decrypt form, the encryption wizard) is a block set
// off by a hairline, never a panel in a panel. The standalone "Import names from a ReGa database" is
// gone (the maintainer: an update from (Open)CCU keeps the names; a restore's check still offers them).

const HEADINGS = ['Encryption', 'Create a backup', 'Backup targets', 'Restore a backup', 'Factory reset'];

test('each section heading is followed by its panel, no panel in a panel, no standalone ReGa import', async ({page}) => {
    await page.goto('/system/backup');
    await expect(page.locator('[data-section="targets"]')).toBeVisible();
    const h2 = page.locator('h2');
    await expect(h2).toHaveText(HEADINGS);
    for (const el of await h2.all()) {
        const next = await el.evaluate((h) => ({cls: h.nextElementSibling?.className ?? '', text: h.textContent}));
        expect(next.cls, `${next.text}: the panel after it`).toMatch(/\bol-panel\b/);
    }
    expect(await page.locator('.ol-panel .ol-panel').count()).toBe(0);
    await expect(page.getByText('Import names from a ReGa database')).toHaveCount(0);
    await expect(page.locator('#rega-import')).toHaveCount(0);
    // the warning edges are on the panels now: the targets' panel carries their ids
    await expect(page.locator('.ol-panel[data-warn-for~="backup-delivery"]')).toHaveCount(1);
    await expect(page.locator('.ol-panel[data-warn-for~="backup-unencrypted"]')).toHaveCount(1);
    expect(await fitsWindow(page)).toBe(true);
});

test('German: the headings and no ReGa import', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    await expect(page.locator('[data-section="targets"]')).toBeVisible();
    await expect(page.getByText('Namen aus einer ReGa-Datenbank importieren')).toHaveCount(0);
    expect(await page.locator('h2').count()).toBe(HEADINGS.length);
});

test('the restore anchor still lands on its heading, its panel under it', async ({page}) => {
    await page.goto('/system/backup#restore');
    const heading = page.locator('h2#restore');
    await expect(heading).toBeInViewport();
    await expect(page.locator('[data-section="restore"]')).toBeInViewport();
    expect(await heading.evaluate((h) => h.nextElementSibling?.getAttribute('data-section'))).toBe('restore');
});

test('a checked backup: its paired devices are a block inside the restore panel', async ({page}) => {
    const CHECK = {ok: true, output: 'OK', backup_version: '3.89.11', running_version: '3.89.11', needs_key: false, has_rega: true};
    const BACKUP = {bidcos_rf: {devices: 2, address: '0xFF1234', has_key: false, gateways: 0}, hmip: {devices: 0}, bidcos_wired: {devices: 0, gateways: 0}, key_index: 0, files: ['ids'], version: '3.89.11'};
    const FREE = {paired: {}, devices: 0, unknown: [], user_key: false, importable: true};
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false}}}));
    await page.route('**/api/system/v1/restore/devices?**', (r) => r.fulfill({json: {backup: BACKUP, target: FREE, module_changed: false, non_default_key: false}}));
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    const block = page.locator('[data-section="restore"] [data-restore="devices"]');
    await expect(block).toBeVisible();
    await expect(block).toHaveClass(/\bol-subblock\b/);
    await expect(block).not.toHaveClass(/\bol-panel\b/);
    // the names of the backup are still offered by the check
    await expect(page.getByRole('button', {name: 'Import the names from this backup'})).toBeVisible();
    expect(await page.locator('.ol-panel .ol-panel').count()).toBe(0);
});
