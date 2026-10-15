import {expect, test} from './fixtures';

// occulited task 50 (the maintainer, 2026-10-14): the Updates page's texts are short - what the
// user needs visible, how it works behind the (?) Help.

test('a container: one line, the install guide, the Proxmox steps behind the Help', async ({page}) => {
    await page.route('**/api/system/v1/status', async (route) => {
        const response = await route.fetch();
        await route.fulfill({response, json: {...(await response.json()), container: 'lxc'}});
    });
    await page.goto('/system/updates');
    const note = page.locator('[data-container-update]');
    await expect(note).toContainText('This system is a container: update it by replacing the container on the host.');
    await expect(note).not.toContainText('Proxmox');
    await expect(note.getByRole('link', {name: 'Install guide'})).toHaveAttribute('href', 'https://github.com/hobbyquaker/openccu-lite/blob/main/docs/install-lxc.md');
    await note.getByRole('button', {name: 'Help'}).click();
    await expect(page.getByText('On Proxmox: create a new container from the template')).toBeVisible();
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(page.locator('[data-container-update]')).toContainText('Dieses System ist ein Container: Aktualisieren Sie es, indem Sie den Container auf dem Host ersetzen.');
    await expect(page.locator('[data-container-update]').getByRole('link', {name: 'Installationsanleitung'})).toBeVisible();
});
