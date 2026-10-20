import {expect, test} from '@playwright/test';
import {fitsWindow} from './scroll';

// occulited task 69 (the maintainer, 2026-10-16: "system - benutzer: page should be grouped into
// panels"): as the Backup page (task 59) - every section of the Users page one panel under its
// heading, what stands apart inside a section (the session lengths, the provider's certificates)
// a sub-block set off by a hairline, never a panel in a panel.

for (const [lang, headings] of [['en', ['Accounts', 'Sessions', 'Authentication']], ['de', ['Konten', 'Sitzungen', 'Anmeldung']]] as const) {
    test(`each section heading is followed by its panel, no panel in a panel (${lang})`, async ({page}) => {
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        await page.goto('/system/users');
        await expect(page.locator('[data-section="session-lengths"]')).toBeVisible();
        const h2 = page.locator('main h2');
        await expect(h2).toHaveText([...headings]);
        for (const el of await h2.all()) {
            const next = await el.evaluate((h) => ({cls: h.nextElementSibling?.className ?? '', text: h.textContent}));
            expect(next.cls, `${next.text}: the panel after it`).toMatch(/\bol-panel\b/);
        }
        expect(await page.locator('.ol-panel .ol-panel').count()).toBe(0);
        // the sub-blocks inside the Authentication panel
        await expect(page.locator('[data-section="authentication"] .ol-panel [data-section="session-lengths"].ol-subblock')).toBeVisible();
        await expect(page.locator('[data-section="authentication"] .ol-panel .ol-subblock [data-oidc-trust]')).toBeVisible();
        // the tables are in their panels
        await expect(page.locator('[data-section="accounts"] .ol-panel table tbody tr').first()).toBeVisible();
        await expect(page.locator('[data-section="sessions"] .ol-panel table tbody tr').first()).toBeVisible();
        expect(await fitsWindow(page)).toBe(true);
    });
}

test('the authentication anchor still lands on its heading, its panel under it', async ({page}) => {
    await page.goto('/system/users#authentication');
    const heading = page.locator('h2#authentication');
    await expect(heading).toBeInViewport();
    expect(await heading.evaluate((h) => h.nextElementSibling?.classList.contains('ol-panel'))).toBe(true);
});
