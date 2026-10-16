import {expect, test, type Page} from '@playwright/test';

// Every page of the shell renders against the stub: its heading is there, nothing threw in the
// console, and a screenshot lands in the report for the eye. Both themes and the phone viewport
// come from the projects in playwright.config.ts.
const PAGES: {path: string; heading: string}[] = [
    {path: '/', heading: 'Status'},
    {path: '/system/interfaces', heading: 'Interfaces'},
    {path: '/system/updates', heading: 'Updates'},
    {path: '/system/services', heading: 'Services'},
    {path: '/system/users', heading: 'Users'},
    {path: '/system/log', heading: 'Log'},
    {path: '/system/network', heading: 'Network'},
    {path: '/system/firewall', heading: 'Firewall'},
    {path: '/system/hardware', heading: 'Hardware'},
    {path: '/system/certificates', heading: 'Certificate'},
    {path: '/system/trust', heading: 'Trust stores'},
    {path: '/system/backup', heading: 'Backup'},
    {path: '/addons', heading: 'Addons'},
    {path: '/catalog', heading: 'Addons'},
    {path: '/settings', heading: 'Settings'},
    {path: '/account', heading: 'Account'},
];

function collectErrors(page: Page): string[] {
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    page.on('console', (m) => {
        if (m.type() === 'error') errors.push(`console: ${m.text()}`);
    });
    return errors;
}

for (const p of PAGES) {
    test(`${p.path} renders ${p.heading}`, async ({page}, info) => {
        const errors = collectErrors(page);
        await page.goto(p.path);
        await expect(page.getByRole('heading', {level: 1, name: p.heading})).toBeVisible();
        // the shell: the icons, the menus (no release text since task 256 - the Updates page names it)
        await expect(page.locator('a[title="GitHub"]')).toBeVisible();
        await page.screenshot({path: info.outputPath('page.png'), fullPage: true});
        expect(errors, errors.join('\n')).toEqual([]);
    });
}

// task 131 (D-86): Status, the pinned addons (none here), Addons, System - nothing more; the stub's
// nav.d entry, a link the box was configured with, is in the Addons dropdown since the maintainer's
// follow-up (2026-09-16)
test('the tab bar is exactly Status, Control, Addons, System (task 131: Interfaces is in the System menu)', async ({page}) => {
    await page.goto('/');
    await expect(page.locator('nav.ol-nav .ol-systab')).toBeVisible();
    const labels = await page.locator('nav.ol-nav > a, nav.ol-nav .ol-menubtn').allInnerTexts();
    const cleaned = labels.map((l) => l.replace(/[▾\s]+$/g, '').trim());
    expect(cleaned).toEqual(['Status', 'Control', 'Addons', 'System']);
    await expect(page.locator('nav.ol-nav a[href="http://192.0.2.119/"]')).toHaveCount(0);
    await expect(page.locator('nav.ol-nav a[href="/radio"], nav.ol-nav a[href="/metadata"]')).toHaveCount(0);
});

test('the theme switch on Settings changes the document theme and sets the cookie', async ({page, context}) => {
    await page.goto('/settings');
    await page.locator('select').nth(1).selectOption('dark');
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    const cookies = await context.cookies();
    expect(cookies.find((c) => c.name === 'ol-theme')?.value).toBe('dark');
});
