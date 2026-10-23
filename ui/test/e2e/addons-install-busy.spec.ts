import {expect, test, type Page} from './fixtures';

// occulited B-113: the Addons page has one state for everything that changes an installation -
// the API's own `busy` of GET /catalog/progress (a catalogue run, an uploaded archive's job, an
// uninstall), in every tab, not only the one that clicked. While it is set every control that
// would start an install or an uninstall is disabled, an upload another tab started has its status
// line and is followed to its result, and a start the system refuses with 409 is worded.

interface Busy { kind: 'catalog' | 'upload' | 'uninstall'; addon_id?: string; job?: string; started: string; bytes?: number }
interface View { busy: Busy | null; progress?: Record<string, unknown> | null }

/** GET /catalog/progress as the test says it; the view is read at every poll, so a test moves it on. */
async function serve(page: Page, view: View) {
    await page.route('**/api/system/v1/catalog/progress', (route) => route.fulfill({json: {progress: view.progress ?? null, busy: view.busy}}));
}

const controls = (page: Page) => ({
    install: page.locator('#addon-row-xml-api [data-addon-install]'),
    update: page.locator('#addon-row-redmatic [data-addon-update]'),
    check: page.locator('[data-check-updates], [data-check-daily="catalog"] [data-check-now]'),
    file: page.locator('[data-install-file]'),
    fileInstall: page.locator('[data-install-file-submit]'),
    busy: page.locator('[data-install-busy]'),
});

test('an upload another tab started: every install control disabled, the status line, the job followed to its result', async ({page}) => {
    const started = new Date(Date.now() - 3000).toISOString();
    const view: View = {busy: {kind: 'upload', job: 'j-other', started, bytes: 5 * 1048576}};
    await serve(page, view);
    let polls = 0;
    await page.route('**/api/system/v1/addons/install?job=j-other', (route) => {
        polls += 1;
        if (polls < 2) return route.fulfill({json: {id: 'j-other', state: 'running', started, bytes: 5 * 1048576}});
        view.busy = null; // the slot is free once the job has ended
        return route.fulfill({json: {id: 'j-other', state: 'done', started, finished: new Date().toISOString(), bytes: 5 * 1048576, result: {exit: 0, meaning: 'installed', reboot_required: false, output: 'update_script: ok', seconds: 4.2}}});
    });
    await page.goto('/addons');
    const c = controls(page);
    await expect(c.busy).toHaveAttribute('data-install-busy', 'upload');
    await expect(c.busy).toContainText(/An uploaded addon archive is being installed \(5 MB, \d+ s\)…/);
    for (const l of [c.install, c.update, c.check, c.file, c.fileInstall]) await expect(l).toBeDisabled();
    // the ⋯ menu's Uninstall too
    const red = page.locator('#addon-row-redmatic');
    await red.getByRole('button', {name: 'More actions'}).click();
    await expect(red.locator('[data-addon-uninstall]')).toBeDisabled();
    await page.keyboard.press('Escape');
    // this tab follows the job as the starting one does: the result arrives here, and the controls come back
    const before = await c.install.boundingBox();
    await expect(page.locator('.ol-notice').filter({hasText: 'Install result'})).toContainText('installed');
    await expect(c.busy).toHaveCount(0);
    for (const l of [c.install, c.update, c.check, c.file]) await expect(l).toBeEnabled();
    // Install from file stays disabled until a file is chosen; the file input is the control that came back
    await expect(c.fileInstall).toBeDisabled();
    const after = await c.install.boundingBox();
    expect([after?.width, after?.height]).toEqual([before?.width, before?.height]);
    expect(polls).toBeGreaterThanOrEqual(2);
});

test('a catalogue run: the file install and the other catalogue buttons are disabled, with the bar', async ({page}) => {
    const started = new Date().toISOString();
    await serve(page, {busy: {kind: 'catalog', addon_id: 'mosquitto', started}, progress: {addon_id: 'mosquitto', phase: 'downloading', percent: 35, bytes: 9 * 1048576, total: 26 * 1048576, started}});
    await page.goto('/addons');
    const c = controls(page);
    await expect(page.locator('.ol-install-progress[data-phase="downloading"]')).toContainText('mosquitto');
    await expect(c.busy).toHaveCount(0); // the catalogue run has the bar, no second line
    for (const l of [c.install, c.update, c.check, c.file, c.fileInstall]) await expect(l).toBeDisabled();
});

test('an uninstall holds the slot: the status line names the addon, the installs are disabled', async ({page}) => {
    await serve(page, {busy: {kind: 'uninstall', addon_id: 'cuxd', started: new Date().toISOString()}});
    await page.goto('/addons');
    const c = controls(page);
    await expect(c.busy).toHaveAttribute('data-install-busy', 'uninstall');
    await expect(c.busy).toContainText(/Uninstalling cuxd \(\d+ s\)…/);
    for (const l of [c.install, c.update, c.file, c.fileInstall]) await expect(l).toBeDisabled();
});

test('a start the system refuses with 409 install-running is worded', async ({page}) => {
    await page.route('**/api/system/v1/catalog/xml-api/install', (route) => route.fulfill({status: 409, json: {error: 'install-running', message: 'an install is already running'}}));
    await page.goto('/addons');
    const c = controls(page);
    await c.install.click();
    await page.getByRole('dialog').getByRole('button', {name: 'Install', exact: true}).click();
    await expect(page.locator('.ol-notice').filter({hasText: 'Another install is still running. Wait until it has finished.'})).toBeVisible();
    // the file install's refusal reads the same, and an uninstall's names the uninstall
    await page.route('**/api/system/v1/addons/install', (route) => route.fulfill({status: 409, json: {error: 'install-running', message: 'an uninstall is already running'}}));
    await c.file.setInputFiles({name: 'mosquitto.tar.gz', mimeType: 'application/gzip', buffer: Buffer.from('x')});
    await c.fileInstall.click();
    await expect(page.locator('.ol-notice').filter({hasText: 'An uninstall is still running. Wait until it has finished.'})).toBeVisible();
    await expect(c.fileInstall).toBeEnabled();
});

test('the texts in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    const view: View = {busy: {kind: 'upload', job: 'j-de', started: new Date().toISOString(), bytes: 2 * 1048576}};
    await serve(page, view);
    await page.route('**/api/system/v1/addons/install?job=j-de', (route) => route.fulfill({json: {id: 'j-de', state: 'running', started: view.busy!.started, bytes: 2 * 1048576}}));
    await page.goto('/addons');
    const c = controls(page);
    await expect(c.busy).toContainText(/Ein hochgeladenes Zusatzsoftware-Archiv wird installiert \(2 MB, \d+ s\)…/);
    await expect(c.fileInstall).toHaveText('Installieren');
    await expect(c.fileInstall).toBeDisabled();
    view.busy = {kind: 'uninstall', addon_id: 'cuxd', started: new Date().toISOString()};
    await expect(c.busy).toContainText(/cuxd wird deinstalliert \(\d+ s\)…/);
});
