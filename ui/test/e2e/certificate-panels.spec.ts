import {expect, test} from '@playwright/test';
import {fitsWindow} from './scroll';

// occulited task 71 (the maintainer, 2026-10-16: "system - zertifikat: page should be grouped into
// panels"): as the Backup (task 59) and Users (task 69) pages - every section of the Certificate page
// one panel under its heading, never a panel in a panel; the certificate warning's edge on the
// current certificate's panel.

for (const lang of ['en', 'de'] as const) {
    test(`each section heading is followed by its panel, no panel in a panel (${lang})`, async ({page}) => {
        await page.addInitScript((l) => localStorage.setItem('ol.language', l), lang);
        await page.goto('/system/certificates');
        await expect(page.locator('[data-section="https"]')).toBeVisible();
        const h2 = page.locator('main h2');
        expect(await h2.count()).toBeGreaterThanOrEqual(3);
        for (const el of await h2.all()) {
            const next = await el.evaluate((h) => ({cls: h.nextElementSibling?.className ?? '', text: h.textContent}));
            expect(next.cls, `${next.text}: the panel after it`).toMatch(/\bol-panel\b/);
        }
        expect(await page.locator('.ol-panel .ol-panel').count()).toBe(0);
        await expect(page.locator('.ol-panel[data-section="current"]')).toBeVisible();
        await expect(page.locator('.ol-panel[data-section="settings"]')).toBeVisible();
        // the manual mode's request section is a panel of its own too
        await page.getByRole('radio', {name: lang === 'de' ? 'Manuell' : 'Manual'}).check();
        await expect(page.locator('.ol-panel[data-section="csr"]')).toBeVisible();
        for (const el of await h2.all()) {
            expect(await el.evaluate((h) => h.nextElementSibling?.classList.contains('ol-panel'))).toBe(true);
        }
        expect(await fitsWindow(page)).toBe(true);
    });
}

test('the HTTPS anchor still lands on its heading, its panel under it', async ({page}) => {
    await page.goto('/system/certificates#https');
    const heading = page.locator('h2#https');
    await expect(heading).toBeInViewport();
    expect(await heading.evaluate((h) => h.nextElementSibling?.classList.contains('ol-panel'))).toBe(true);
});
