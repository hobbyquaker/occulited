import {expect, test} from '@playwright/test';

// openccu-lite task 399: the Backup page's panel "Addon data in the backup" - every addon whose
// manifest declares a snapshot, with its switch (on for a small one, off for a large one until the
// user switches it on), the size hint and the last run; a failed run is red, and the panel carries
// the backup-addon warning's edge.

test('the declared addons with their switches, sizes and last runs', async ({page}) => {
    await page.goto('/system/backup');
    const panel = page.locator('[data-section="backup-addons"]');
    await expect(panel).toBeVisible();
    await expect(panel).toHaveAttribute('data-warn-for', /backup-addon/);
    const battery = panel.locator('[data-backup-addon="battery"]');
    const history = panel.locator('[data-backup-addon="history"]');
    await expect(battery).toHaveAttribute('data-enabled', 'true');
    await expect(battery.locator('input[type="checkbox"]')).toBeChecked();
    await expect(battery).toContainText('Batteries');
    await expect(battery).toContainText('about 20 MB');
    await expect(battery.locator('[data-last="ok"]')).toContainText('Last snapshot:');
    await expect(battery.locator('[data-last="ok"]')).toContainText('4 MB');
    await expect(battery.locator('.ol-badge')).toHaveCount(0);
    await expect(history).toHaveAttribute('data-enabled', 'false');
    await expect(history.locator('input[type="checkbox"]')).not.toBeChecked();
    await expect(history.locator('.ol-badge')).toHaveText('large');
    await expect(history.locator('[data-last="failed"]')).toContainText('Last snapshot failed');
    await expect(history.locator('[data-last="failed"]')).toContainText('the hook exited 1');
});

test('switching an addon on sends PUT and shows the answer', async ({page}) => {
    let sent: unknown = null;
    await page.route('**/api/system/v1/backup/addons/history', async (r) => {
        sent = r.request().postDataJSON();
        await r.fulfill({json: {supported: true, large_mb: 100, addons: [
            {id: 'history', name: 'History', hook: 'bin/snap', staging: 'var/backup', timeout: 600, size_hint_mb: 800, large: true, enabled: true, chosen: true},
        ]}});
    });
    await page.goto('/system/backup');
    const history = page.locator('[data-backup-addon="history"]');
    await history.locator('input[type="checkbox"]').check();
    await expect(history).toHaveAttribute('data-enabled', 'true');
    expect(sent).toEqual({enabled: true});
    await expect(history.locator('[data-last="none"]')).toHaveText('No snapshot yet');
});

test('German, and a system without the hooks', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    const panel = page.locator('[data-section="backup-addons"]');
    await expect(panel.locator('[data-backup-addon="battery"]')).toContainText('Batterien');
    await expect(panel.locator('[data-backup-addon="battery"]')).toContainText('etwa 20 MB');
    await expect(panel.locator('[data-backup-addon="history"] .ol-badge')).toHaveText('groß');
    await page.route('**/api/system/v1/backup/addons', (r) => r.fulfill({json: {supported: false, large_mb: 100, addons: []}}));
    await page.goto('/system/backup');
    await expect(panel).toContainText('kein systemd');
});
