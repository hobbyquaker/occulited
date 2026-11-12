import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 143: the welcome page's last step says that the system manages no devices by
// itself and that this needs an addon; Homematic Manager installs from there with one click -
// the catalogue's own install route and progress, no dialog - and the catalogue is the button
// beside it. The button keeps its box through every state (install, installing, installed).
// stub-no-hm=1 makes the stub's Homematic Manager (id mh there) not installed, stub-session keeps
// this browser's install run apart from the other projects'.

async function freshStub(page: Page, name: string) {
    await page.context().addCookies([
        {name: 'stub-no-hm', value: '1', url: test.info().project.use.baseURL!},
        {name: 'stub-session', value: `welcome-hm-${name}-${test.info().project.name}-${Date.now()}`, url: test.info().project.use.baseURL!},
    ]);
}

const box = async (page: Page) => (await page.locator('[data-action="welcome-hm"]').boundingBox())!;

test('install from the welcome page: one click, the progress, then installed and opened', async ({page}) => {
    await freshStub(page, 'install');
    const dialogs: string[] = [];
    page.on('dialog', (d) => { dialogs.push(d.message()); void d.dismiss(); });
    const posts: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes('/catalog/')) posts.push(new URL(r.url()).pathname); });
    await page.goto('/welcome');
    const sec = page.locator('[data-welcome-frontend]');
    await expect(sec.getByRole('heading', {name: /Device management$/})).toBeVisible();
    await expect(sec).toContainText('This system manages no devices by itself');
    await expect(sec).toContainText('need an addon');
    const button = sec.locator('[data-action="welcome-hm"]');
    await expect(sec).toHaveAttribute('data-state', 'ready');
    await expect(button.locator('.wf-live')).toHaveText('Install Homematic Manager');
    await expect(sec.locator('[data-action="welcome-catalog"]')).toHaveText('More addons in the catalogue');
    await expect(sec.locator('[data-welcome-hm-status]')).toContainText('from GitHub (github.com)');
    const before = await box(page);

    await button.click();
    // no question in between: the run starts at once, through the catalogue's route
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(sec.locator('.ol-install-progress')).toBeVisible();
    await expect.poll(() => posts).toEqual(['/api/system/v1/catalog/mh/install']);
    await expect(sec).toHaveAttribute('data-state', 'installing');
    await expect(button.locator('.wf-live')).toHaveText('Installing Homematic Manager…');
    await expect(button).toBeDisabled();
    const during = await box(page);
    expect(during.width).toBeCloseTo(before.width, 0);
    expect(during.height).toBeCloseTo(before.height, 0);

    await expect(sec).toHaveAttribute('data-state', 'installed', {timeout: 15_000});
    await expect(button.locator('.wf-live')).toHaveText('Open Homematic Manager');
    await expect(sec.locator('[data-welcome-hm-status]')).toContainText('Homematic Manager is installed.');
    const after = await box(page);
    expect(after.width).toBeCloseTo(before.width, 0);
    expect(after.height).toBeCloseTo(before.height, 0);
    expect(dialogs).toEqual([]);
    expect(await fitsWindow(page)).toBe(true);

    await button.click();
    await expect(page).toHaveURL(/\/nav\/mh$/);
    expect(await page.evaluate(() => localStorage.getItem('ol.welcomed'))).toBe('1');
});

test('already installed (a restored backup): shown as installed, nothing to install', async ({page}) => {
    const posts: string[] = [];
    page.on('request', (r) => { if (r.method() === 'POST') posts.push(r.url()); });
    await page.goto('/welcome');
    const sec = page.locator('[data-welcome-frontend]');
    await expect(sec).toHaveAttribute('data-state', 'installed');
    await expect(sec.locator('[data-action="welcome-hm"] .wf-live')).toHaveText('Open Homematic Manager');
    await expect(sec.locator('[data-welcome-hm-status]')).toContainText('Homematic Manager is installed.');
    expect(posts).toEqual([]);
});

// a fresh system holds the bundled list only: the entry is known by its repository, without a manifest
const bundled = {format: 1, source: 'bundled', addons: [{git: 'https://github.com/hobbyquaker/homematic-manager', manifest_path: 'apps/ccu-addon/files/openccu-lite.json'}]};
const checked = {format: 1, source: 'published', addons: [{git: 'https://github.com/hobbyquaker/homematic-manager', manifest_path: 'apps/ccu-addon/files/openccu-lite.json', format: 1, id: 'hmm', name: {de: 'Homematic Manager', en: 'Homematic Manager'}, latest: {version: '3.1.3'}}]};

test('catalogue unreachable: the reason and a retry, nothing installed', async ({page}) => {
    let checks = 0;
    const posts: string[] = [];
    await page.route(/\/api\/system\/v1\/catalog(\?refresh=1)?$/, async (route) => {
        if (route.request().url().endsWith('?refresh=1')) {
            checks++;
            return route.fulfill({status: 502, json: {error: 'catalog-unreachable', message: 'raw.githubusercontent.com: no such host'}});
        }
        await route.fulfill({json: {catalog: bundled, installed: {}, arch: 'x86_64', daily: false}});
    });
    await page.route('**/api/system/v1/catalog/*/install', (route) => { posts.push(route.request().url()); return route.fulfill({status: 202, json: {started: true}}); });
    await page.goto('/welcome');
    const sec = page.locator('[data-welcome-frontend]');
    await expect(sec).toHaveAttribute('data-state', 'ready');
    const before = await box(page);
    const button = sec.locator('[data-action="welcome-hm"]');
    await button.click();
    await expect(sec).toHaveAttribute('data-state', 'unreachable');
    const status = sec.locator('[data-welcome-hm-status]');
    await expect(status).toContainText('The catalogue could not be loaded from GitHub: raw.githubusercontent.com: no such host');
    await expect(status).toContainText('from its archive on the Addons page');
    // never a dead click: the button tries again
    await expect(button).toBeEnabled();
    await expect(button.locator('.wf-live')).toHaveText('Try again');
    const after = await box(page);
    expect(after.width).toBeCloseTo(before.width, 0);
    await button.click();
    await expect.poll(() => checks).toBe(2);
    expect(posts).toEqual([]);
    // the catalogue stays one click away
    await expect(sec.locator('[data-action="welcome-catalog"]')).toBeEnabled();
});

test('a fresh system: the click loads the catalogue first, then installs by the manifest id', async ({page}) => {
    let view: object = bundled;
    const posts: string[] = [];
    await page.route(/\/api\/system\/v1\/catalog(\?refresh=1)?$/, async (route) => {
        if (route.request().url().endsWith('?refresh=1')) view = checked;
        await route.fulfill({json: {catalog: view, installed: {}, arch: 'x86_64', daily: false}});
    });
    await page.route('**/api/system/v1/catalog/*/install', (route) => { posts.push(new URL(route.request().url()).pathname); return route.fulfill({status: 202, json: {started: true}}); });
    await page.goto('/welcome');
    const sec = page.locator('[data-welcome-frontend]');
    await sec.locator('[data-action="welcome-hm"]').click();
    await expect.poll(() => posts).toEqual(['/api/system/v1/catalog/hmm/install']);
});

test('the catalogue button leaves the welcome page for the catalogue', async ({page}) => {
    await page.goto('/welcome');
    await page.locator('[data-action="welcome-catalog"]').click();
    await expect(page).toHaveURL(/\/addons$/);
});

test('German: Geräteverwaltung, Homematic Manager installieren, Weitere Addons im Katalog', async ({page}) => {
    await freshStub(page, 'de');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/welcome');
    const sec = page.locator('[data-welcome-frontend]');
    await expect(sec.getByRole('heading', {name: /Geräteverwaltung$/})).toBeVisible();
    await expect(sec).toContainText('Dieses System verwaltet selbst keine Geräte');
    await expect(sec.locator('[data-action="welcome-hm"] .wf-live')).toHaveText('Homematic Manager installieren');
    await expect(sec.locator('[data-action="welcome-catalog"]')).toHaveText('Weitere Addons im Katalog');
});
