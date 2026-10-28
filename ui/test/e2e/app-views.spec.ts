import {expect, test, type Page} from '@playwright/test';

// occulited task 125: the addons' views in Control - history's dashboards after the rooms and the
// functions, labelled "History" in every language; one dashboard opens directly, several fold out;
// the page frames the view under the App's title with ?embed=app&theme=&lang= and hands it the
// theme and the language by message; hidden per account (Settings); in the public mode too.

async function views(page: Page, baseURL: string | undefined, which: 'one' | 'two' | 'none'): Promise<void> {
    await page.context().addCookies([
        {name: 'stub-appviews', value: which, url: baseURL!},
        {name: 'stub-prefs', value: `views-${Math.random().toString(36).slice(2)}`, url: baseURL!},
    ]);
}

async function openDrawer(page: Page, isMobile: boolean): Promise<void> {
    if (isMobile) await page.locator('[data-app-fab]').click();
}

test('one dashboard: the History entry after Rooms and Functions opens it in a frame', async ({page, baseURL, isMobile}) => {
    await views(page, baseURL, 'one');
    await page.goto('/app');
    await openDrawer(page, !!isMobile);
    const drawer = page.locator('[data-app-drawer]');
    const entry = drawer.locator('[data-app-view="history/lab1"]');
    await expect(entry).toHaveText('History');
    // after the rooms and the functions, in the tree's order
    const order = await drawer.locator('.app-tree > li').evaluateAll((lis) => lis.map((li) => li.querySelector('[data-app-section],[data-app-entry],[data-app-view]')?.getAttribute('data-app-section') ?? li.querySelector('[data-app-view]')?.getAttribute('data-app-view') ?? li.querySelector('[data-app-entry]')?.getAttribute('data-app-entry')));
    expect(order.indexOf('history/lab1')).toBeGreaterThan(order.indexOf('functions'));
    expect(order.indexOf('functions')).toBeGreaterThan(order.indexOf('rooms'));
    // no children: a single view is the entry itself
    await expect(drawer.locator('[data-app-section="addon-views/history"]')).toHaveCount(0);
    await entry.click();
    await expect(page).toHaveURL(/\/app\/view\/history\/lab1$/);
    await expect(page.locator('[data-app-title]')).toHaveText('History');
    const frame = page.locator('iframe.app-view');
    await expect(frame).toHaveAttribute('src', /^\/addons\/history\/embed\/dashboard\/lab1\?embed=app&theme=(system|light|dark)&lang=en$/);
    await expect(entry).toHaveClass(/active/);
    // the frame fills the width of the content area and a good part of the window
    const box = await frame.boundingBox();
    expect(box!.height).toBeGreaterThan(300);
});

test('two dashboards: History folds out to them; the deep link opens one; theme and language follow by message', async ({page, baseURL, isMobile}) => {
    await views(page, baseURL, 'two');
    await page.goto('/app/view/history/energy');
    await expect(page.locator('[data-app-title]')).toHaveText('Energy');
    const frame = page.locator('iframe.app-view');
    await expect(frame).toHaveAttribute('src', /\/addons\/history\/embed\/dashboard\/energy\?embed=app&/);
    await openDrawer(page, !!isMobile);
    const drawer = page.locator('[data-app-drawer]');
    await expect(drawer.locator('[data-app-section="addon-views/history"]')).toHaveText('History');
    await expect(drawer.locator('[data-app-view="history/lab1"]')).toHaveText('Lab');
    await expect(drawer.locator('[data-app-view="history/energy"]')).toHaveText('Energy');
    await expect(drawer.locator('[data-app-view="history/energy"]')).toHaveClass(/active/);
    // the frame got the look at its load; a language change reaches it without a reload
    const inner = page.frameLocator('iframe.app-view');
    await expect(inner.locator('#probe')).toBeAttached();
    const src = await frame.getAttribute('src');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/settings');
    await page.goto('/app/view/history/energy');
    await expect(page.locator('[data-app-title]')).toHaveText('Energie');
    await expect(page.locator('iframe.app-view')).toHaveAttribute('src', /lang=de$/);
    const looks = await page.frames().find((f) => f.url().includes('/addons/history/'))!.evaluate(() => (window as unknown as {__looks: {type?: string; lang?: string}[]}).__looks);
    expect(looks.some((l) => l.type === 'openccu-lite:theme' && l.lang === 'de')).toBe(true);
    expect(src).toContain('lang=en');
});

test('a view that is not registered says so; no views, no entry', async ({page, baseURL}) => {
    await views(page, baseURL, 'none');
    await page.goto('/app/view/history/lab1');
    await expect(page.locator('[data-app-view-missing]')).toBeVisible();
    await expect(page.locator('iframe.app-view')).toHaveCount(0);
    await expect(page.locator('[data-app-views]')).toHaveCount(0);
});

test('hidden per account: Settings takes History out of the drawer', async ({page, baseURL, isMobile}) => {
    await views(page, baseURL, 'one');
    await Promise.all([page.waitForResponse((r) => r.url().endsWith('/api/system/v1/app-views')), page.goto('/settings')]);
    const box = page.locator('[data-setting="app-views"][data-addon="history"]').getByRole('checkbox', {name: 'Show "History" in the menu'});
    await expect(box).toBeChecked();
    const [req] = await Promise.all([page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/auth/v1/me/preferences')), box.uncheck()]);
    expect(req.postDataJSON().app_views_hidden).toEqual(['history']);
    await page.goto('/app');
    await openDrawer(page, !!isMobile);
    await expect(page.locator('[data-app-drawer] [data-app-entry="favorites"]')).toBeVisible();
    await expect(page.locator('[data-app-views="history"]')).toHaveCount(0);
    // the address still opens it
    await page.goto('/app/view/history/lab1');
    await expect(page.locator('iframe.app-view')).toBeAttached();
});

test('the public mode shows the views too', async ({page, baseURL, isMobile}) => {
    await views(page, baseURL, 'one');
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, public: true, user: 'guest', role: 'user', level: 'operate', account_id: 'abcd1234', must_change_password: false, sid: 'P1'}}));
    await page.goto('/app');
    await openDrawer(page, !!isMobile);
    await expect(page.locator('[data-app-view="history/lab1"]')).toHaveText('History');
});
