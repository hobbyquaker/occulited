import {expect, test} from './fixtures';

// occulited task 123: a newer release in the feed's last check is a warning on the Status page,
// with the button to the Updates page; there the system update panel carries the warning's edge,
// and the Updates entry of the System menu its dot. Light and dark are the two desktop projects.
const warning = {id: 'system-update-available', variant: '1.0.1', severity: 'warning', href: '/system/updates#system-update', params: {version: '1.0.1', channel: 'stable', running: '1.0.0', url: 'https://example.invalid/releases/v1.0.1'}};

async function withWarning(page: import('@playwright/test').Page) {
    await page.route('**/api/system/v1/warnings', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.warnings = [...(j.warnings ?? []), warning];
        await route.fulfill({response, json: j});
    });
}

test('the Status page names the available system update and leads to the Updates page', async ({page}) => {
    await withWarning(page);
    await page.goto('/');
    await expect(page.getByText('A system update is available: 1.0.1.')).toBeVisible();
    const link = page.getByRole('link', {name: 'Updates', exact: true}).last();
    await expect(link).toHaveAttribute('href', '/system/updates#system-update');
    await link.click();
    await expect(page).toHaveURL(/\/system\/updates#system-update$/);
    const panel = page.locator('[data-panel="system-update"]');
    await expect(panel).toHaveAttribute('data-warn-edge', 'warn');
    const [want, got] = await Promise.all([
        page.evaluate(() => {
            const p = document.createElement('span');
            p.style.color = 'var(--hmm-warn)';
            document.body.append(p);
            const c = getComputedStyle(p).color;
            p.remove();
            return c;
        }),
        panel.evaluate((e) => ({w: getComputedStyle(e).borderLeftWidth, c: getComputedStyle(e).borderLeftColor})),
    ]);
    expect(got.w).toBe('4px');
    expect(got.c).toBe(want);
});

test('in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await withWarning(page);
    await page.goto('/');
    await expect(page.getByText('Ein Systemupdate ist verfügbar: 1.0.1.')).toBeVisible();
});

test('without the warning the panel has no edge', async ({page}) => {
    await page.goto('/system/updates');
    await expect(page.locator('[data-panel="system-update"]')).toBeVisible();
    await expect(page.locator('[data-panel="system-update"]')).not.toHaveAttribute('data-warn-edge', /./);
});
