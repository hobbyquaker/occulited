import {expect, test} from './fixtures';

// occulited task 50 (the maintainer, 2026-10-14): the Updates page's texts are short - what the
// user needs visible, how it works behind the (?) Help.

test('a container: one line without a link, the Proxmox steps behind the Help', async ({page}) => {
    await page.route('**/api/system/v1/status', async (route) => {
        const response = await route.fetch();
        await route.fulfill({response, json: {...(await response.json()), container: 'lxc'}});
    });
    await page.goto('/system/updates');
    const note = page.locator('[data-container-update]');
    await expect(note).toContainText('This system is a container: update it by replacing the container on the host.');
    await expect(note).not.toContainText('Proxmox');
    // openccu-lite task 367: no LXC image is released, so the note links no LXC guide
    await expect(note.getByRole('link')).toHaveCount(0);
    await note.getByRole('button', {name: 'Help'}).click();
    await expect(page.getByText('On Proxmox: create a new container from the template')).toBeVisible();
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(page.locator('[data-container-update]')).toContainText('Dieses System ist ein Container: Aktualisieren Sie es, indem Sie den Container auf dem Host ersetzen.');
    await expect(page.locator('[data-container-update]').getByRole('link')).toHaveCount(0);
});

// occulited B-68: a session that is not admin gets no empty System update panel; an admin keeps it
test('no empty System update panel for a user', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.goto('/system/updates');
    await expect(page.locator('[data-panel="radio-firmware"], .ol-panel').first()).toBeVisible();
    await expect(page.locator('[data-panel="system-update"]')).toHaveCount(0);
    // no panel of the page is empty
    const empty = await page.locator('.ol-panel').evaluateAll((els) => els.filter((e) => !e.textContent?.trim()).length);
    expect(empty).toBe(0);
});

test('an admin has the System update panel', async ({page}) => {
    await page.goto('/system/updates');
    await expect(page.locator('[data-panel="system-update"] h2')).toContainText('System update');
});
