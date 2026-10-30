import {expect, test} from './fixtures';

// openccu-lite B-247: a system update that would not fit where the recovery unpacks it is refused
// before the reboot, and the Updates page says why - the free and the needed space, and what to do.

const noSpace = {error: 'no-space', message: 'not enough space', detail: {free: 2_298_372_096, required: 2_487_167_352}};

test('a download that does not fit: free and needed space, and the hint to remove old backups', async ({page}) => {
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed.available = {version: '1.0.0', name: 'openccu-lite-x86_64-ova-1.0.0.zip', size: 300 << 20, newer: true};
        await route.fulfill({response, json: j});
    });
    await page.route('**/api/system/v1/system-update/download', (route) => route.fulfill({status: 422, json: noSpace}));
    await page.goto('/system/updates');
    await page.getByRole('button', {name: 'Download and stage'}).click();
    await expect(page.getByText('Not enough space for this update: 2.3 GB free on the system, 2.5 GB needed to unpack it. Remove old backups on the Backup page, or keep them on a USB stick or a share.')).toBeVisible();
});

// B-99: a download refused before a byte is written: the file and its unpacking are named
test('a download refused before it starts: the room to store and to unpack it', async ({page}) => {
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed.available = {version: '1.0.0', name: 'openccu-lite-x86_64-ova-1.0.0.zip', size: 1_300_000_000, newer: true};
        await route.fulfill({response, json: j});
    });
    const before = {...noSpace, detail: {free: 1_400_000_000, required: 2_667_108_864, download: true}};
    await page.route('**/api/system/v1/system-update/download', (route) => route.fulfill({status: 422, json: before}));
    await page.goto('/system/updates');
    await page.getByRole('button', {name: 'Download and stage'}).click();
    await expect(page.getByText('Not enough space for this update: 1.4 GB free on the system, 2.7 GB needed to store and unpack it. Remove old backups on the Backup page, or keep them on a USB stick or a share.')).toBeVisible();
});

test('an install refused for space, in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.staged = {file: 'openccu-lite-x86_64-ova-1.0.0.zip', size: 297_847_385, kind: 'zip', version: '1.0.0', board: 'x86_64-ova', recovery_armed: false};
        await route.fulfill({response, json: j});
    });
    await page.route('**/api/system/v1/system-update/install', (route) => route.fulfill({status: 422, json: noSpace}));
    await page.goto('/system/updates');
    await page.getByRole('button', {name: 'Neu starten und installieren'}).click();
    await page.getByRole('dialog').getByRole('button').last().click();
    await expect(page.getByText('Nicht genug Platz für dieses Update: 2.3 GB frei auf dem System, 2.5 GB nötig zum Entpacken.', {exact: false})).toBeVisible();
});

test('Status: a nightly backup skipped to keep room for an update says so', async ({page}) => {
    const skipped = {id: 'backup-delivery', variant: 'directory:update-room', severity: 'error', href: '/backup#targets', params: {name: 'USB / directory', cause: 'update-room', detail: '…'}};
    await page.route('**/api/system/v1/warnings', (route) => route.fulfill({json: {warnings: [skipped], periods: [1, 7, 90]}}));
    await page.goto('/');
    await expect(page.locator('[data-warnings] [data-notice="backup-delivery"]')).toContainText('The backup to USB / directory was skipped: on the system itself it would leave too little room for a system update, even with the older backups removed. Use a USB stick or a share.');
});
