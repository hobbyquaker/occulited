import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 118 (D-122): the update channel - Stable, Beta or Dev - beside the release check
// on the Updates page and in the welcome page's automatic checks. A change is stored at once
// (PUT /system-update/settings {channel}) and asks no server: the page says to check now.

async function channelRoutes(page: Page, feed: Record<string, unknown> = {}) {
    const bodies: unknown[] = [];
    let checks = 0;
    await page.route('**/api/system/v1/system-update', async (route: Route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const j = await response.json();
        j.feed = {...j.feed, ...feed};
        await route.fulfill({response, json: j});
    });
    await page.route('**/api/system/v1/system-update/settings', async (route: Route) => {
        const b = route.request().postDataJSON() as {channel?: string};
        bodies.push(b);
        await route.fulfill({json: {ok: true, enabled: true, channel: b.channel}});
    });
    await page.route('**/api/system/v1/system-update/check', async (route: Route) => {
        checks++;
        await route.fulfill({json: {feed: {enabled: true, channel: 'beta', checked: new Date().toISOString(), available: {version: '1.1.0-beta.1', name: 'openccu-lite-x86_64-ova-1.1.0-beta.1.zip', size: 1, newer: true}}}});
    });
    return {bodies, checks: () => checks};
}

test('the Updates page: the channel is shown, stored on change, and the check stays the user\'s', async ({page}) => {
    const r = await channelRoutes(page, {available: {version: '1.0.0', name: 'openccu-lite-x86_64-ova-1.0.0.zip', size: 1, newer: false}});
    await page.goto('/system/updates');
    const select = page.locator('[data-update-channel]');
    await expect(select).toHaveValue('stable');
    await expect(select.locator('option')).toHaveText(['Stable: releases only', 'Beta: betas and release candidates too', 'Dev: every build']);
    const before = (await select.boundingBox())!;
    await select.selectOption('beta');
    await expect.poll(() => r.bodies).toEqual([{channel: 'beta'}]);
    await expect(page.getByText('Channel changed: check now for its newest release.')).toBeVisible();
    expect(r.checks()).toBe(0);
    // the select keeps its box across the change (no resize on a state change)
    const after = (await select.boundingBox())!;
    expect(Math.round(after.width)).toBe(Math.round(before.width));
    await page.locator('[data-check-daily="system-update"]').getByRole('button', {name: 'Check now'}).click();
    await expect(page.locator('[data-release-available]')).toContainText('Release 1.1.0-beta.1 is available.');
    expect(await fitsWindow(page)).toBe(true);
});

test('Stable before the first release: no release in the channel yet, in German too', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await channelRoutes(page, {available: null, none_in_channel: true, channel: 'stable'});
    await page.goto('/system/updates');
    await expect(page.locator('[data-release-none]')).toHaveText('Im Kanal Stabil gibt es noch kein Release.');
    await expect(page.locator('[data-update-channel] option')).toHaveText(['Stabil: nur Releases', 'Beta: auch Betas und Release Candidates', 'Dev: jeder Build']);
});

test('a refused change shows the error and the stored channel again', async ({page}) => {
    await channelRoutes(page, {channel: 'dev'});
    await page.route('**/api/system/v1/system-update/settings', (route) => route.fulfill({status: 400, json: {error: 'invalid', message: 'channel: stable, beta or dev'}}));
    await page.goto('/system/updates');
    const select = page.locator('[data-update-channel]');
    await expect(select).toHaveValue('dev');
    await select.selectOption('stable');
    await expect(page.locator('[data-notice="update-channel"]')).toContainText('channel: stable, beta or dev');
    await expect(select).toHaveValue('dev');
});

test('the welcome page: the channel beside the automatic checks', async ({page}) => {
    const r = await channelRoutes(page);
    await page.goto('/welcome');
    const box = page.locator('[data-welcome-outbound]');
    await expect(box.locator('[data-update-channel]')).toHaveValue('stable');
    await box.locator('[data-update-channel]').selectOption('dev');
    await expect.poll(() => r.bodies).toEqual([{channel: 'dev'}]);
    expect(r.checks()).toBe(0);
    expect(await fitsWindow(page)).toBe(true);
});
