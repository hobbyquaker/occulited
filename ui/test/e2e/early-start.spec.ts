import {expect, test, type Page} from '@playwright/test';

// Task 119 (D-75): the early addon start - the global switch under the list, the per-addon switch in
// the ⋯ menu of an addon that declares runtime.start "early" (only such an addon has one), and the
// note that a change takes effect at the next boot, with no restart offered. The stub keeps the
// switches per browser under the cookie stub-early=<id>; Homematic-Manager declares the early start.
const own = async (page: Page, baseURL: string | undefined, info: {project: {name: string}; title: string}) => {
    await page.context().addCookies([{name: 'stub-early', value: `${info.project.name}-${info.title}`.replace(/[^\w-]/g, '_'), url: baseURL}]);
};
const row = (page: Page, name: string) => page.locator('.ad-card').filter({has: page.locator('.ad-name', {hasText: new RegExp(`^\\W*${name}$`)})}).first();

test('only an addon that declares the early start has the switch, and it takes effect at the next boot', async ({page, baseURL}, info) => {
    await own(page, baseURL, info);
    const restarts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && /\/(restart|start|stop)$/.test(new URL(r.url()).pathname)) restarts.push(r.url());
    });
    await page.goto('/addons');
    const hmm = row(page, 'Homematic-Manager');
    await expect(hmm.locator('.ad-early')).toHaveText('starts early');
    // the badge explains itself as a popup (task 51)
    await hmm.locator('.ad-early').click();
    await expect(page.getByRole('tooltip')).toContainText('before them');
    await page.keyboard.press('Escape');
    // an addon without the declaration: no badge, no item
    const mosquitto = row(page, 'Mosquitto');
    await expect(mosquitto.locator('.ad-early, .ad-early-off')).toHaveCount(0);
    await mosquitto.getByRole('button', {name: 'More actions'}).click();
    await expect(page.getByRole('menuitemcheckbox', {name: /Start early/})).toHaveCount(0);
    await page.keyboard.press('Escape');
    await page.locator('h1').first().click();

    // switched off for hmm
    await hmm.getByRole('button', {name: 'More actions'}).click();
    const item = page.getByRole('menuitemcheckbox', {name: /Start early/});
    await expect(item).toHaveAttribute('aria-checked', 'true');
    await expect(item).toContainText('from the next boot');
    await item.click();
    await expect(page.locator('.ol-notice', {hasText: 'waits for the radio interfaces from the next boot on'})).toBeVisible();
    await expect(hmm.locator('.ad-early-off')).toHaveText('early start off');
    await expect(hmm.locator('.ad-early')).toHaveCount(0);
    await expect(page.locator('h2:has-text("Addon start") ~ .ol-checks')).toContainText('Switched off for: mh');
    // and on again
    await hmm.getByRole('button', {name: 'More actions'}).click();
    await expect(page.getByRole('menuitemcheckbox', {name: /Start early/})).toHaveAttribute('aria-checked', 'false');
    await page.getByRole('menuitemcheckbox', {name: /Start early/}).click();
    await expect(page.locator('.ol-notice', {hasText: 'starts early from the next boot on'})).toBeVisible();
    await expect(hmm.locator('.ad-early')).toHaveText('starts early');
    // nothing was restarted, and nothing offers to
    expect(restarts).toEqual([]);
    await expect(page.getByRole('button', {name: /Reboot now|Restart now/})).toHaveCount(0);
});

test('the global switch turns the early start off for every addon', async ({page, baseURL}, info) => {
    await own(page, baseURL, info);
    await page.goto('/addons');
    await expect(page.getByRole('heading', {level: 2, name: 'Addon start'})).toBeVisible();
    const section = page.locator('[data-section="addon-start"]');
    await expect(section).toContainText('start before the radio interfaces are ready and connect when they are');
    await expect(section.locator('.ad-early-nextboot')).toHaveText('A change takes effect at the next boot.');
    const sw = page.getByRole('checkbox', {name: /Start addons early when they support it/});
    await expect(sw).toBeChecked();
    const save = section.getByRole('button', {name: 'Save', exact: true});
    await expect(save).toBeDisabled();
    await sw.uncheck();
    await expect(section).toContainText('Every addon waits for the radio interfaces.');
    await save.click();
    await expect(section.locator('.ad-early-notice')).toHaveText('Saved. Takes effect at the next boot.');
    // the addon says so, and its item is off with the reason
    const hmm = row(page, 'Homematic-Manager');
    await expect(hmm.locator('.ad-early-off')).toHaveText('early start off');
    await hmm.getByRole('button', {name: 'More actions'}).click();
    const item = page.getByRole('menuitemcheckbox', {name: /Start early/});
    await expect(item).toBeDisabled();
    await expect(item).toContainText('off for all addons, see Addon start below');
    await page.keyboard.press('Escape');
    // after a reload the switch is still off; on again
    await page.reload();
    await expect(sw).not.toBeChecked();
    await sw.check();
    await section.getByRole('button', {name: 'Save', exact: true}).click();
    await expect(section.locator('.ad-early-notice')).toHaveText('Saved. Takes effect at the next boot.');
    await expect(row(page, 'Homematic-Manager').locator('.ad-early')).toHaveText('starts early');
});

// openccu-lite task 369: the start delay per addon, under Addon start - set, shown in the list, refused
// out of range, removed with 0; nothing restarts.
test('an addon gets a start delay, shown in the list, from the next boot', async ({page, baseURL}, info) => {
    await own(page, baseURL, info);
    const restarts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && /\/(restart|start|stop)$/.test(new URL(r.url()).pathname)) restarts.push(r.url());
    });
    await page.goto('/addons');
    const section = page.locator('[data-section="addon-start"]');
    await expect(section.getByRole('heading', {level: 3, name: 'Start delay'})).toBeVisible();
    const input = section.getByRole('spinbutton', {name: 'Start delay of Mosquitto in seconds'});
    await expect(input).toHaveValue('');
    const save = section.getByRole('button', {name: 'Save delays'});
    await expect(save).toBeDisabled();
    const box = await input.boundingBox();
    // out of range: refused before it is sent, the field keeps its size
    await input.fill('601');
    await expect(section.locator('.ad-delay-invalid')).toHaveText('A start delay is 0 to 600 seconds.');
    await expect(save).toBeDisabled();
    const after = await input.boundingBox();
    expect([after?.width, after?.height]).toEqual([box?.width, box?.height]);
    await input.fill('30');
    await expect(section.locator('.ad-delay-invalid')).toHaveCount(0);
    await save.click();
    await expect(section.locator('.ad-delay-notice')).toHaveText('Saved. Takes effect at the next boot.');
    const mosquitto = row(page, 'Mosquitto');
    await expect(mosquitto.locator('.ad-delay')).toHaveText('starts 30 s later');
    await mosquitto.locator('.ad-delay').click();
    await expect(page.getByRole('tooltip')).toContainText('waits for the radio interfaces and then 30 more seconds');
    await page.keyboard.press('Escape');
    // an addon that could start early shows the delay instead: with a delay it never starts early
    const hmm = section.getByRole('spinbutton', {name: 'Start delay of Homematic-Manager in seconds'});
    await hmm.fill('60');
    await save.click();
    await expect(row(page, 'Homematic-Manager').locator('.ad-delay')).toHaveText('starts 60 s later');
    await expect(row(page, 'Homematic-Manager').locator('.ad-early')).toHaveCount(0);
    // kept across a reload; 0 removes it
    await page.reload();
    await expect(section.getByRole('spinbutton', {name: 'Start delay of Mosquitto in seconds'})).toHaveValue('30');
    await section.getByRole('spinbutton', {name: 'Start delay of Mosquitto in seconds'}).fill('0');
    await section.locator('.ad-delay-save').click();
    await expect(section.locator('.ad-delay-notice')).toHaveText('Saved. Takes effect at the next boot.');
    await expect(row(page, 'Mosquitto').locator('.ad-delay')).toHaveCount(0);
    expect(restarts).toEqual([]);
});
