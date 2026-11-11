import {expect, test, type Page} from '@playwright/test';

// occulited B-141: a backup larger than the system takes, or than its free space holds, is refused
// before the upload runs, with the sizes; lighttpd's own 413 page is said in words, not as a JSON
// parse error. Before, a 4.55 GB backup went over the network and came back as "could not untar".

async function pick(page: Page, bytes: number) {
    await page.goto('/system/backup');
    // the size is what the page compares: a small buffer stands in, File.size is overridden
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'big.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('ustar')});
    await page.evaluate((n) => Object.defineProperty(File.prototype, 'size', {get: () => n, configurable: true}), bytes);
}

test('no room on the system: refused before the upload, with the sizes', async ({page}) => {
    let posted = 0;
    await page.route('**/api/system/v1/restore/space', (r) => r.fulfill({json: {max_bytes: 16 * 2 ** 30, reserve_bytes: 128 * 2 ** 20, free_bytes: 6e9}}));
    await page.route('**/api/system/v1/restore/check', (r) => {
        posted++;
        return r.fulfill({status: 500, json: {message: 'must not be posted'}});
    });
    await pick(page, 3.3e9);
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    const notice = page.locator('.ol-notice.err');
    await expect(notice).toContainText('Not enough free space on the system for this backup');
    await expect(notice).toContainText('it needs 6.7 GB (twice the 3.3 GB file - the upload and the copy its check unpacks - and 134 MB that stay free), 6.0 GB are free.');
    expect(posted).toBe(0);
});

test('larger than the system takes: refused before the upload', async ({page}) => {
    let posted = 0;
    await page.route('**/api/system/v1/restore/space', (r) => r.fulfill({json: {max_bytes: 16 * 2 ** 30, reserve_bytes: 128 * 2 ** 20, free_bytes: 100e9}}));
    await page.route('**/api/system/v1/restore/check', (r) => {
        posted++;
        return r.fulfill({status: 500, json: {message: 'must not be posted'}});
    });
    await pick(page, 20 * 2 ** 30);
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    await expect(page.locator('.ol-notice.err')).toContainText('The backup is 21 GB, larger than the 17 GB an upload may have.');
    expect(posted).toBe(0);
});

test("lighttpd's 413 page is said in words", async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({status: 413, contentType: 'text/html', body: '<html><body>413 Payload Too Large</body></html>'}));
    await pick(page, 1000);
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    await expect(page.locator('.ol-notice.err')).toContainText('The system refused the upload (413');
});

test('the server says it after all (the room changed meanwhile): its message is shown', async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({status: 507, json: {error: 'no-space', message: 'not enough free space on the system for this backup: it needs 4683 MB'}}));
    await pick(page, 1000);
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    await expect(page.locator('.ol-notice.err')).toContainText('not enough free space on the system for this backup: it needs 4683 MB');
});
