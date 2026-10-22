import {type Page, type TestInfo} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 62: while the LED's warnings are muted (openccu-lite task 164), the Status page
// says until when, with the way to System → Hardware and, for an administrator, Unmute. The stub
// keeps a test's LED state per stub-led cookie. A mute that ends after midnight names the day as well
// (the suite ran red between 23:30 and midnight).

async function plant(page: Page, baseURL: string, info: TestInfo) {
    const id = `${info.project.name}-${info.title}`.replace(/[^A-Za-z0-9-]/g, '_');
    await page.context().addCookies([{name: 'stub-led', value: id, url: baseURL}]);
}

test('a mute shows on the Status page until Unmute', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.goto('/');
    await expect(page.locator('[data-warnings]')).toBeVisible();
    const notice = page.locator('[data-notice="led-muted"]');
    await expect(notice).toHaveCount(0);
    const r = await page.request.post('/api/system/v1/led/mute', {data: {minutes: 30}, headers: {'X-Occulite-Request': '1'}});
    expect(r.ok()).toBe(true);
    await page.reload();
    await expect(notice).toContainText(/The LED warnings are muted until ([^,]+, )?\d\d:\d\d\./);
    await expect(notice.getByRole('link', {name: 'Hardware'})).toHaveAttribute('href', '/system/hardware');
    await notice.locator('[data-action="unmute"]').click();
    await expect(notice).toHaveCount(0);
    const st = await (await page.request.get('/api/system/v1/led/state')).json();
    expect(st.muted_until).toBeUndefined();
});

test('German: the mute line', async ({page, baseURL}, info) => {
    await plant(page, baseURL!, info);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    const r = await page.request.post('/api/system/v1/led/mute', {data: {minutes: 30}, headers: {'X-Occulite-Request': '1'}});
    expect(r.ok()).toBe(true);
    await page.goto('/');
    const notice = page.locator('[data-notice="led-muted"]');
    await expect(notice).toContainText(/Die Warnungen der LED sind bis ([^,]+, )?\d\d:\d\d stummgeschaltet\./);
    await expect(notice.locator('[data-action="unmute"]')).toHaveText('Stummschaltung aufheben');
});
