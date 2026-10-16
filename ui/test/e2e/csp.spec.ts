import {expect, test} from '@playwright/test';

// openccu-lite task 259 (D-78's F-5): the shell runs under a Content-Security-Policy. lighttpd sets
// it on the shell's paths (deploy/lighttpd/occulited.conf); the stub reads the same line and sets it
// on the shell and its assets, and on nothing under /addons/. Every page is opened here under that
// header, and a violation - the browser reports one on the console as "Content Security Policy" -
// or an uncaught error fails the run. Chromium is the reporter; the two desktop projects and the
// phone walk the pages, so light and dark and both widths are seen once each.
const PAGES = [
    '/', '/app', '/addons', '/catalog', '/settings', '/account', '/licenses', '/welcome',
    '/system/services', '/system/interfaces', '/system/network', '/system/lan-devices', '/system/users',
    '/system/keys', '/system/certificates', '/system/trust', '/system/remote-access', '/system/firewall',
    '/system/storage', '/system/backup', '/system/updates', '/system/log', '/system/hardware',
    // an addon frame inside the shell (the shell's route, no trailing slash): the frame's page is
    // under /addons/ and gets no policy, the shell around it does
    '/nav/red', '/nav/mh', '/addon-settings/mosquitto',
];

test('every page loads without a CSP violation, under the header the system sends', async ({page, request}) => {
    test.setTimeout(180_000); // a walk over every page
    const violations: string[] = [];
    page.on('console', (m) => {
        if (/Content.Security.Policy|Refused to/.test(m.text())) violations.push(`${page.url()}: ${m.text()}`);
    });
    page.on('pageerror', (e) => violations.push(`${page.url()}: ${e.message}`));
    const home = await page.goto('/');
    const csp = home?.headers()['content-security-policy'] ?? '';
    expect(csp, 'the shell answers with a policy').toContain("default-src 'self'");
    expect(csp).toContain("script-src 'self'");
    expect(csp).toContain("frame-ancestors 'self'");
    expect(csp).toContain("object-src 'none'");
    expect(csp, 'scripts never inline').not.toMatch(/script-src[^;]*unsafe-inline/);
    expect(home?.headers()['permissions-policy'] ?? '').toContain('camera=(self)');
    for (const p of PAGES) {
        await page.goto(p);
        await page.locator('#app > *').first().waitFor();
        // a moment for the page's own requests and scripts (not networkidle: the shell's stream
        // worker's script, occulited B-53, never ends for Playwright)
        await page.waitForTimeout(500);
    }
    // the shell's assets carry it too (a script or style loaded under a policy), an addon page does not
    const asset = await request.get('/app.webmanifest');
    expect(asset.headers()['content-security-policy']).toBe(csp);
    const addon = await request.get('/addons/hmm/settings.cgi');
    expect(addon.headers()['content-security-policy']).toBeUndefined();
    expect(violations, violations.join('\n')).toEqual([]);
});

test('a login and the login page under the policy', async ({page}) => {
    const violations: string[] = [];
    page.on('console', (m) => {
        if (/Content.Security.Policy|Refused to/.test(m.text())) violations.push(m.text());
    });
    page.on('pageerror', (e) => violations.push(e.message));
    await page.context().addCookies([{name: 'stub-token', value: 'out', url: page.url() || 'http://127.0.0.1'}]).catch(() => undefined);
    await page.goto('/login');
    await page.locator('#app > *').first().waitFor();
    expect(violations, violations.join('\n')).toEqual([]);
});
