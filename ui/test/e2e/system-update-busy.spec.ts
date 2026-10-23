import {expect, test, type Page} from './fixtures';

// occulited B-111: while a system update downloads, uploads or installs, every control that would
// start, restart or change one is disabled - in every tab, from the API's own `busy`, not only in
// the tab that clicked - the running step has its status line and a download its Cancel, and the
// controls come back after a failure or a cancel. In the install phase the page shows the reboot
// notice only.

const NAME = 'openccu-lite-x86_64-ova-1.0.0.zip';
const available = {version: '1.0.0', name: NAME, size: 300 << 20, newer: true};
const stagedOld = {file: 'openccu-lite-x86_64-ova-0.9.0.zip', size: 297_847_385, kind: 'zip', version: '0.9.0', board: 'x86_64-ova', recovery_armed: false};

interface View { busy: {phase: string; file?: string} | null; staged: typeof stagedOld | null; error?: string }

/** GET /system-update as the stub answers it, with the test's view laid over it. */
async function serve(page: Page, view: View) {
    await page.route('**/api/system/v1/system-update', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed.available = available;
        if (view.error) j.feed.download_error = view.error;
        else delete j.feed.download_error;
        j.staged = view.staged;
        j.busy = view.busy;
        await route.fulfill({response, json: j});
    });
}

const controls = (page: Page) => ({
    download: page.locator('[data-update-download]'),
    file: page.locator('[data-update-file]'),
    upload: page.locator('[data-update-upload]'),
    check: page.locator('[data-check-daily="system-update"] [data-check-now]'),
    install: page.locator('[data-update-install]'),
    discard: page.locator('[data-update-discard]'),
    cancel: page.locator('[data-update-cancel]'),
    busy: page.locator('[data-update-busy]'),
});

test('a download another tab started: every control disabled, the status line, Cancel brings them back', async ({page}) => {
    const view: View = {busy: {phase: 'download', file: NAME}, staged: stagedOld};
    await serve(page, view);
    let deleted = 0;
    await page.route('**/api/system/v1/system-update', (route) => {
        if (route.request().method() !== 'DELETE') return route.fallback();
        deleted++;
        view.busy = null;
        return route.fulfill({json: {staged: stagedOld, cancelled: true}});
    });
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(c.busy).toHaveAttribute('data-update-busy', 'download');
    await expect(c.busy).toContainText(`Downloading ${NAME} (300 MB)…`);
    for (const l of [c.download, c.file, c.upload, c.check, c.install, c.discard]) await expect(l).toBeDisabled();
    await expect(c.cancel).toBeEnabled();
    // the download button keeps its size across the states (no resize; the notice above it may shrink)
    const before = await c.download.boundingBox();
    await c.cancel.click();
    await expect(c.busy).toHaveCount(0);
    expect(deleted).toBe(1);
    await expect(page.getByText('The download was cancelled.', {exact: true})).toBeVisible();
    // Upload stays disabled until a file is chosen; the file input is the control that came back
    for (const l of [c.download, c.file, c.check, c.install, c.discard]) await expect(l).toBeEnabled();
    const after = await c.download.boundingBox();
    expect([after?.width, after?.height]).toEqual([before?.width, before?.height]);
});

test('the tab that clicks: the status line at once, Cancel once the API reports the download, the cancelled answer', async ({page}) => {
    const view: View = {busy: null, staged: null};
    await serve(page, view);
    let release: ((r: {status: number; json: unknown}) => void) | undefined;
    const held = new Promise<{status: number; json: unknown}>((resolve) => (release = resolve));
    await page.route('**/api/system/v1/system-update/download', async (route) => {
        view.busy = {phase: 'download', file: NAME};
        const answer = await held;
        await route.fulfill({status: answer.status, json: answer.json});
    });
    await page.route('**/api/system/v1/system-update', (route) => {
        if (route.request().method() !== 'DELETE') return route.fallback();
        view.busy = null;
        release?.({status: 409, json: {error: 'download-cancelled', message: 'the download was cancelled'}});
        return route.fulfill({json: {staged: null, cancelled: true}});
    });
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(c.download).toBeEnabled();
    await c.download.click();
    await expect(c.busy).toHaveAttribute('data-update-busy', 'download');
    await expect(c.download).toBeDisabled();
    await expect(c.upload).toBeDisabled();
    await expect(c.check).toBeDisabled();
    // the page polls while its own request runs and the API's busy arrives within 2 s
    await expect(c.cancel).toBeEnabled({timeout: 5000});
    await c.cancel.click();
    await expect(page.getByText('The download was cancelled.', {exact: true})).toBeVisible();
    await expect(c.busy).toHaveCount(0);
    await expect(c.download).toBeEnabled();
});

test('a second start is refused: the 409 of the API is worded', async ({page}) => {
    const view: View = {busy: null, staged: null};
    await serve(page, view);
    await page.route('**/api/system/v1/system-update/download', (route) => route.fulfill({status: 409, json: {error: 'busy', message: 'an upload is running: x.zip', detail: {phase: 'upload', file: 'x.zip'}}}));
    await page.goto('/system/updates');
    await controls(page).download.click();
    await expect(page.getByText('Another update is in progress: an upload of x.zip', {exact: true})).toBeVisible();
});

test('a failed download: the reason from the API, the controls back', async ({page}) => {
    await serve(page, {busy: null, staged: null, error: 'download: HTTP 500'});
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(page.locator('[data-download-error]')).toHaveText('The last download failed: download: HTTP 500');
    await expect(c.busy).toHaveCount(0);
    for (const l of [c.download, c.file, c.check]) await expect(l).toBeEnabled();
});

test('an upload another tab runs: the status line without Cancel, everything disabled', async ({page}) => {
    await serve(page, {busy: {phase: 'upload', file: 'OpenCCU-3.89.8-ova.zip'}, staged: stagedOld});
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(c.busy).toContainText('Uploading OpenCCU-3.89.8-ova.zip…');
    await expect(c.cancel).toHaveCount(0);
    for (const l of [c.download, c.file, c.upload, c.check, c.install, c.discard]) await expect(l).toBeDisabled();
});

test('the install phase from the API: the reboot notice only', async ({page}) => {
    await serve(page, {busy: {phase: 'install', file: stagedOld.file}, staged: {...stagedOld, recovery_armed: true}});
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(page.getByText('Rebooting into the recovery system…', {exact: true})).toBeVisible();
    for (const l of [c.download, c.file, c.upload, c.install, c.discard, c.busy]) await expect(l).toHaveCount(0);
    await expect(page.locator('[data-check-daily="system-update"]')).toHaveCount(0);
});

test('done: the staged file with its buttons usable', async ({page}) => {
    await serve(page, {busy: null, staged: stagedOld});
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(page.locator('[data-staged]')).toContainText(stagedOld.file);
    for (const l of [c.install, c.discard, c.download, c.file, c.check]) await expect(l).toBeEnabled();
});

test('the texts in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    const view: View = {busy: {phase: 'download', file: NAME}, staged: null};
    await serve(page, view);
    await page.route('**/api/system/v1/system-update', (route) => {
        if (route.request().method() !== 'DELETE') return route.fallback();
        view.busy = null;
        view.error = 'download: HTTP 500';
        return route.fulfill({json: {staged: null, cancelled: true}});
    });
    await page.goto('/system/updates');
    const c = controls(page);
    await expect(c.busy).toContainText(`${NAME} (300 MB) wird heruntergeladen…`);
    await expect(c.cancel).toHaveText('Abbrechen');
    await c.cancel.click();
    await expect(page.getByText('Der Download wurde abgebrochen.', {exact: true})).toBeVisible();
    // the notice of this tab's own cancel comes first; the API's remembered failure shows once it is gone
    await page.reload();
    await expect(page.locator('[data-download-error]')).toHaveText('Der letzte Download ist fehlgeschlagen: download: HTTP 500');
});
