import {expect, test} from './fixtures';

// openccu-lite task 319: the welcome page says what the update packages' EULA preamble says, without
// the licence text - openccu-lite is not OpenCCU; support only in its own issue tracker, not in
// OpenCCU's forum or tracker; no donations, a GitHub star instead - and links the licence.

test('the about box: not OpenCCU, its own issue tracker, no donations, the licence linked', async ({page}) => {
    await page.goto('/welcome');
    const about = page.locator('[data-welcome-about]');
    await expect(about.getByRole('heading', {name: 'About openccu-lite'})).toBeVisible();
    await expect(about).toContainText('it is not OpenCCU');
    await expect(about).toContainText('only in its issue tracker - not in the OpenCCU forum and not in OpenCCU\'s issue tracker');
    await expect(about).toContainText('does not accept donations');
    await expect(about).toContainText('give it a star on GitHub');
    await expect(about.locator('[data-about="issues"]')).toHaveAttribute('href', 'https://github.com/openccu-lite/openccu-lite/issues');
    await expect(about.locator('[data-about="star"]')).toHaveAttribute('href', 'https://github.com/openccu-lite/openccu-lite');
    await expect(about.locator('[data-about="license"]')).toHaveAttribute('href', 'https://github.com/openccu-lite/openccu-lite/blob/main/LICENSE');
    await expect(about.locator('[data-about="licenses"]')).toHaveAttribute('href', '/licenses');
    // the external links open apart from the system, never with its referrer
    for (const a of ['issues', 'star', 'license']) {
        await expect(about.locator(`[data-about="${a}"]`)).toHaveAttribute('rel', /noopener/);
    }
    // no licence text on the page, and the box comes before the first decision
    await expect(page.getByText('TERMS AND CONDITIONS')).toHaveCount(0);
    const aboutY = (await about.boundingBox())!.y;
    const firstY = (await page.getByRole('heading', {name: '2 · Automatic checks'}).boundingBox())!.y;
    expect(aboutY).toBeLessThan(firstY);
});

test('the about box in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/welcome');
    const about = page.locator('[data-welcome-about]');
    await expect(about.getByRole('heading', {name: 'Über openccu-lite'})).toBeVisible();
    await expect(about).toContainText('aber nicht OpenCCU');
    await expect(about).toContainText('nicht im OpenCCU-Forum');
    await expect(about).toContainText('nimmt keine Spenden an');
    await expect(about).toContainText('Das System steht unter der Apache-Lizenz 2.0');
    await expect(about.locator('[data-about="license"]')).toHaveText('Die Lizenz');
});
