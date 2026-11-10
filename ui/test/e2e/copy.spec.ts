import {expect, test, type Locator, type Page} from '@playwright/test';

// B-135 (#14): the Copy buttons over plain HTTP. `navigator.clipboard` exists only in a secure
// context, and the seven buttons that called it by hand did nothing and said nothing there - with an
// API token and a generated password that are shown once. Every copy goes through lib/clipboard.ts
// now (the Clipboard API, then a selected field and the browser's copy command) and says beside the
// button what became of it; where neither way copies, the text on the page is selected.
//
// The suite's page is http://127.0.0.1, a secure context, so the insecure one is made: the property
// is taken off the page before its scripts run. The real clipboard object is kept aside to read
// back what the copy command wrote.

type Mode = 'no-api' | 'no-copy';

/** `no-api`: the page as over plain HTTP. `no-copy`: a browser whose copy command refuses as well. */
async function insecure(page: Page, mode: Mode) {
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']).catch(() => undefined);
    await page.addInitScript((m) => {
        const w = window as unknown as {__clip?: Clipboard};
        w.__clip = navigator.clipboard;
        Object.defineProperty(Navigator.prototype, 'clipboard', {get: () => undefined, configurable: true});
        if (m === 'no-copy') document.execCommand = () => false;
    }, mode);
}
const clipboardText = (page: Page) => page.evaluate(() => (window as unknown as {__clip?: Clipboard}).__clip?.readText().catch(() => null) ?? null);
const selectedText = (page: Page) => page.evaluate(() => {
    const a = document.activeElement;
    if (a instanceof HTMLTextAreaElement) return a.value.slice(a.selectionStart, a.selectionEnd);
    return document.getSelection()?.toString() ?? '';
});
const said = (scope: Locator) => scope.locator('.ol-copy-said');
const box = async (l: Locator) => JSON.stringify(await l.boundingBox());

/** The boxes of `still` before the click are their boxes after the words came. */
async function copyKeepsBoxes(button: Locator, words: Locator, text: string, still: Locator[]) {
    await expect(words.getByRole('status')).toHaveText('');
    await button.scrollIntoViewIfNeeded();
    const before = await Promise.all(still.map(box));
    await button.click();
    await expect(words.getByRole('status')).toHaveText(text);
    expect(await Promise.all(still.map(box))).toEqual(before);
}

async function createToken(page: Page): Promise<{notice: Locator; secret: string}> {
    await page.goto('/system/remote-access');
    await page.getByRole('button', {name: 'Create token'}).click();
    await page.getByPlaceholder('Token name (e.g. hm2mqtt)').fill('copy-spec');
    await page.getByRole('checkbox', {name: /Names and rooms: read/}).check();
    await page.locator('.ol-tokform').getByRole('button', {name: 'Create token'}).click();
    const notice = page.locator('[data-notice="new-token"]');
    await expect(notice.locator('code')).not.toHaveText('');
    return {notice, secret: (await notice.locator('code').textContent()) ?? ''};
}

async function generatePassword(page: Page, baseURL: string | undefined): Promise<Locator> {
    await page.context().addCookies([{name: 'stub-ra', value: `ra-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/system/remote-access');
    await page.getByRole('radio', {name: 'User name and password'}).check();
    await page.locator('[data-pair-form]').getByRole('button', {name: 'Generate'}).click();
    const notice = page.locator('[data-notice="ra-generated"]');
    await expect(notice.locator('code')).toHaveText('Gen3rat3dPassw0rdOnlyShownOnce2x');
    return notice;
}

test('the new token without a Clipboard API: copied by the copy command, and the button says so', async ({page}) => {
    await insecure(page, 'no-api');
    const {notice, secret} = await createToken(page);
    expect(await page.evaluate(() => 'clipboard' in navigator && navigator.clipboard !== undefined)).toBe(false);
    const copy = notice.getByRole('button', {name: 'Copy'});
    await copyKeepsBoxes(copy, said(notice), 'Copied.', [copy, notice.getByRole('button', {name: 'Hide'}), notice.locator('code'), notice]);
    await expect(said(notice)).toHaveAttribute('data-copy-said', 'selection');
    // what the command wrote, where the browser lets the test read it
    const got = await clipboardText(page);
    if (got !== null) expect(got).toBe(secret);
    // the helper's field is gone
    await expect(page.locator('textarea[aria-hidden="true"]')).toHaveCount(0);
    // the words go again; their place stays
    await expect(said(notice).getByRole('status')).toHaveText('', {timeout: 8000});
});

test('the new token where the browser copies neither way: the token is selected, to be copied by hand', async ({page}) => {
    await insecure(page, 'no-copy');
    const {notice, secret} = await createToken(page);
    const copy = notice.getByRole('button', {name: 'Copy'});
    await copyKeepsBoxes(copy, said(notice), 'Select the text and copy it by hand.', [copy, notice.getByRole('button', {name: 'Hide'}), notice.locator('code'), notice]);
    await expect(said(notice)).toHaveAttribute('data-copy-said', 'failed');
    expect(await selectedText(page)).toBe(secret);
    // these words stay: the user still has something to do
    await page.waitForTimeout(4500);
    await expect(said(notice).getByRole('status')).toHaveText('Select the text and copy it by hand.');
});

test('the new token in a secure context: the Clipboard API', async ({page, context}) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']).catch(() => undefined);
    const {notice, secret} = await createToken(page);
    await notice.getByRole('button', {name: 'Copy'}).click();
    await expect(said(notice).getByRole('status')).toHaveText('Copied.');
    await expect(said(notice)).toHaveAttribute('data-copy-said', 'clipboard');
    const got = await page.evaluate(() => navigator.clipboard.readText().catch(() => null));
    if (got !== null) expect(got).toBe(secret);
});

test('the generated password without a Clipboard API', async ({page, baseURL}) => {
    await insecure(page, 'no-api');
    const notice = await generatePassword(page, baseURL);
    const copy = notice.getByRole('button', {name: 'Copy'});
    await copyKeepsBoxes(copy, said(notice), 'Copied.', [copy, notice.getByRole('button', {name: 'Hide'}), notice]);
    await expect(said(notice)).toHaveAttribute('data-copy-said', 'selection');
    const got = await clipboardText(page);
    if (got !== null) expect(got).toBe('Gen3rat3dPassw0rdOnlyShownOnce2x');
});

test('the generated password where the browser copies neither way: selected, the words in German', async ({page, baseURL}) => {
    await insecure(page, 'no-copy');
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.context().addCookies([{name: 'stub-ra', value: `ra-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/system/remote-access');
    await page.getByRole('radio', {name: 'Benutzername und Passwort'}).check();
    await page.locator('[data-pair-form]').getByRole('button', {name: 'Erzeugen'}).click();
    const notice = page.locator('[data-notice="ra-generated"]');
    const copy = notice.getByRole('button', {name: 'Kopieren'});
    await copyKeepsBoxes(copy, said(notice), 'Den Text markieren und von Hand kopieren.', [copy, notice.getByRole('button', {name: 'Ausblenden'}), notice]);
    expect(await selectedText(page)).toBe('Gen3rat3dPassw0rdOnlyShownOnce2x');
});

test('a trace line of the Log page: a check mark in the button, or the line opened and selected', async ({page}) => {
    await insecure(page, 'no-api');
    await page.goto('/system/log?tag=rpc-trace&severity=debug');
    const lines = page.locator('.ol-line.lg-trace');
    await expect(lines).toHaveCount(6);
    const copy = lines.nth(1).getByRole('button', {name: 'Copy line'});
    const before = [await box(copy), await box(lines.nth(1)), await box(lines.nth(2))];
    await copy.click();
    await expect(copy).toHaveAttribute('data-copy-said', 'selection');
    await expect(copy).toHaveText('✓');
    await expect(copy).toHaveAttribute('title', 'Copied.');
    expect([await box(copy), await box(lines.nth(1)), await box(lines.nth(2))]).toEqual(before);
    const got = await clipboardText(page);
    if (got !== null) expect(got).toBe('← HmIP-RF 192.0.2.50 (token home-assistant) getValue 0.5');
});

test('a folded trace line where the browser copies neither way: opened, selected whole, a cross', async ({page}) => {
    await insecure(page, 'no-copy');
    await page.goto('/system/log?tag=rpc-trace&severity=debug');
    const long = page.locator('.ol-line.lg-trace').nth(5);
    await expect(long.locator('.lg-trace-msg')).toHaveClass(/lg-trace-folded/);
    const copy = long.getByRole('button', {name: 'Copy line'});
    await copy.click();
    await expect(copy).toHaveAttribute('data-copy-said', 'failed');
    await expect(copy).toHaveText('✗');
    await expect(copy).toHaveAttribute('title', 'Select the text and copy it by hand.');
    await expect(long.locator('.lg-trace-msg')).not.toHaveClass(/lg-trace-folded/);
    const selected = await selectedText(page);
    expect(selected).toContain('"FIRMWARE":"1.6.2"}]');
    expect(selected).not.toContain('…');
});

test("the SSH target's key line: the words at the row's end, New key stays beside Copy", async ({page}) => {
    await insecure(page, 'no-copy');
    await page.goto('/system/backup');
    const ssh = page.locator('[data-ssh]').first();
    const copy = ssh.locator('[data-action="copy-key"]');
    const newKey = ssh.locator('[data-action="new-key"]');
    const key = (await ssh.locator('[data-public-key]').textContent()) ?? '';
    await copyKeepsBoxes(copy, said(ssh), 'Select the text and copy it by hand.', [copy, newKey, ssh]);
    expect(await selectedText(page)).toBe(key);
    // New key directly after Copy, the words after both
    const [c, n, w] = [await copy.boundingBox(), await newKey.boundingBox(), await said(ssh).boundingBox()];
    expect(n!.x - (c!.x + c!.width)).toBeLessThan(12);
    if (w!.y < c!.y + c!.height) expect(w!.x).toBeGreaterThan(n!.x + n!.width);
});

test('a licence text: the words before the button at the right end', async ({page}) => {
    await insecure(page, 'no-api');
    await page.goto('/licenses/c/busybox%401.37.0');
    const head = page.locator('.lic-texthead').first();
    const copy = head.getByRole('button', {name: 'Copy'});
    await copyKeepsBoxes(copy, said(head), 'Copied.', [copy, head, page.locator('pre.lic-text').first()]);
    const got = await clipboardText(page);
    if (got !== null) expect(got).toBe(await page.locator('pre.lic-text').first().textContent());
    // the focus is back on the button (a tap focuses no button, so the phone has nothing to give back)
    await expect(page.locator('textarea[aria-hidden="true"]')).toHaveCount(0);
    if (test.info().project.name !== 'phone') await expect(copy).toBeFocused();
});

test('the recovery key of the backup encryption: selected where the browser copies neither way', async ({page}) => {
    await insecure(page, 'no-copy');
    await page.goto('/system/backup');
    const s = page.locator('[data-section="encryption"]');
    await s.locator('[data-action="encryption-setup"]').click();
    await s.locator('[data-action="encryption-make-key"]').click();
    const key = s.locator('[data-recovery-key] code');
    await expect(key).toBeVisible();
    const copy = s.locator('[data-action="encryption-copy"]');
    await copyKeepsBoxes(copy, said(s), 'Select the text and copy it by hand.', [copy, s.locator('[data-action="encryption-kit"]'), s.locator('[data-action="encryption-print"]')]);
    expect(await selectedText(page)).toBe(await key.textContent());
});

test('the certificate request: copied without a Clipboard API; where nothing copies, its text opens selected', async ({page}) => {
    await insecure(page, 'no-copy');
    await page.goto('/system/certificates');
    await page.getByRole('radio', {name: 'Manual'}).check();
    // the same request as certificate-manual.spec.ts makes: the projects share the stub's pending key
    await page.getByPlaceholder('ccu.example.org').last().fill('ccu.example.org');
    await page.getByPlaceholder('ccu.lan, 192.0.2.119').fill('ccu.lan');
    await page.locator('select').last().selectOption('p384');
    await page.getByRole('button', {name: /Generate (a new )?key and request/}).click();
    const dlg = page.getByRole('dialog');
    if (await dlg.waitFor({timeout: 800}).then(() => true, () => false)) await dlg.getByRole('button', {name: 'Generate anyway'}).click();
    const pend = page.locator('.pend');
    await expect(pend).toContainText('Pending request');
    const text = page.getByLabel('Certificate request (PEM)');
    const group = page.getByRole('group', {name: 'The request as text'});
    if (await group.isVisible()) await group.getByRole('button', {name: 'Close'}).click();
    await expect(text).toHaveCount(0);
    await pend.locator('[data-action="copy-csr"]').click();
    await expect(said(pend).getByRole('status')).toHaveText('Select the text and copy it by hand.');
    await expect(text).toBeVisible();
    expect(await selectedText(page)).toMatch(/^-----BEGIN CERTIFICATE REQUEST-----[\s\S]*-----END CERTIFICATE REQUEST-----\s*$/);
});
