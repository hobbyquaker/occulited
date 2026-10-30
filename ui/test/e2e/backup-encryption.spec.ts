import {expect, test, type Page} from '@playwright/test';

// Task 91: the Backup page's Encryption section - the state, the wizard (the key shown once, the
// tick before "Turn on encryption", Back, aborting saves nothing), the kit's download and print
// view, "Test my recovery key" with its answers, the switch, the download buttons, and the
// restore's recovery-key prompt with the hints for a current, an earlier and an unknown key, a
// typo, the wrong key, and the security key afterwards. Both languages.

const ON = {enabled: true, recovery: {fingerprint: 'ea77-3877-2a7b-31f8', created: '2026-09-23T12:00:00Z'}, previous: [{fingerprint: '0000-1111-2222-3333', created: '2026-03-01T12:00:00Z', retired: '2026-09-23T12:00:00Z'}], box: {fingerprint: '1f3a-9c2e-7b40-d15e', created: '2026-09-23T12:00:00Z'}};
const CODE = '0024-H36H-2NCS-VRH6-DAQF-6DVV-QZN3';

async function withView(page: Page, view: Record<string, unknown>) {
    await page.route('**/api/system/v1/backup/encryption', (r) => (r.request().method() === 'GET' ? r.fulfill({json: view}) : r.fallback()));
}

const section = (page: Page) => page.locator('[data-section="encryption"]');

test('not set up: the badge, the amber edge, the plain download as a button, no inline notice', async ({page}) => {
    await page.goto('/system/backup');
    const s = section(page);
    await expect(page.locator('h2#encryption')).toHaveText(/Encryption/);
    await expect(s.locator('[data-encryption-state]')).toHaveAttribute('data-encryption-state', 'none');
    // occulited task 88 (maintainer): the badge alone, no sentence next to it; the panel's amber edge
    await expect(s.locator('[data-encryption-state]')).toHaveText('Not encrypted');
    await expect(s.locator('[data-encryption-state] .ol-pill.warn')).toBeVisible();
    await expect(s).toHaveClass(/\bwarn\b/);
    expect(await s.evaluate((el) => getComputedStyle(el).borderLeftWidth)).toBe('4px');
    await expect(s.locator('[data-action="encryption-setup"]')).toBeEnabled();
    // a normal button in the panel's flow, not a link sitting on a notice
    const dl = page.getByRole('button', {name: 'Download backup', exact: true});
    await expect(dl).toHaveAttribute('data-action', 'download-backup');
    await expect(page.locator('[data-action="download-plain"]')).toHaveCount(0);
    const panel = (await page.locator('[data-section="create"]').boundingBox())!;
    const b = (await dl.boundingBox())!;
    expect(b.y).toBeGreaterThanOrEqual(panel.y);
    expect(b.y + b.height).toBeLessThanOrEqual(panel.y + panel.height);
    const backUp = (await page.locator('[data-action="run-all"]').boundingBox())!;
    expect(b.height).toBe(backUp.height);
    // none of the three inline notices about it, in English and German
    await expect(page.locator('[data-notice="backup-plain"], [data-notice="targets-plain"]')).toHaveCount(0);
    await expect(page.locator('main')).not.toContainText('handed out in plain');
    await expect(page.locator('main')).not.toContainText('whoever can read the file');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(section(page).locator('[data-encryption-state]')).toHaveText('Nicht verschlüsselt');
    await expect(page.locator('main')).not.toContainText('unverschlüsselt herausgegeben');
    await expect(page.locator('main')).not.toContainText('Wer die Datei lesen kann');
    await expect(page.locator('main')).not.toContainText('Die Verschlüsselung ist noch nicht eingerichtet');
});

test('with a recovery key the edge goes and the badge says encrypted', async ({page}) => {
    await page.route('**/api/system/v1/backup/encryption', (r) => r.request().method() === 'GET' ? r.fulfill({json: {enabled: true, recovery: {fingerprint: 'ea77-3877-2a7b-31f8', created: '2026-09-23T12:00:00Z'}, previous: [], box: {fingerprint: '1f3a-9c2e-7b40-d15e', created: '2026-09-23T12:00:00Z'}}}) : r.fallback());
    await page.goto('/system/backup');
    const s = section(page);
    await expect(s.locator('[data-encryption-state]')).toHaveAttribute('data-encryption-state', 'on');
    await expect(s.locator('.ol-pill.ok')).toHaveText('Encrypted');
    await expect(s).not.toHaveClass(/\bwarn\b/);
    await expect(page.locator('[data-action="download-backup"]')).toHaveText('Download encrypted backup');
});

test('the wizard: the key once, Copy / kit / print, the tick gates the confirmation, Back, then on', async ({page}) => {
    const posts: {path: string; body: unknown}[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().includes('/backup/encryption/')) posts.push({path: new URL(r.url()).pathname, body: JSON.parse(r.postData() ?? '{}')});
    });
    await page.goto('/system/backup');
    const s = section(page);
    await s.locator('[data-action="encryption-setup"]').click();
    await expect(s.locator('[data-wizard="intro"]')).toContainText('This system keeps its own key');
    await s.locator('[data-action="encryption-make-key"]').click();
    const key = s.locator('[data-recovery-key] code');
    await expect(key).toBeVisible();
    const code = (await key.textContent()) ?? '';
    // the browser made it: seven groups of four, and only the recipient went to the system
    expect(code).toMatch(/^([0-9A-HJKMNP-TV-Z]{4}-){6}[0-9A-HJKMNP-TV-Z]{4}$/);
    expect(posts).toHaveLength(1);
    expect(posts[0].path).toBe('/api/system/v1/backup/encryption/recovery');
    const sent = posts[0].body as {recipient?: string; generate?: boolean};
    expect(sent.recipient).toMatch(/^age1[a-z0-9]{58}$/);
    expect(sent.generate).toBeUndefined();
    await expect(s.locator('[data-notice="encryption-warning"]')).toContainText('Nobody can recover the key');
    await expect(s.locator('[data-notice="encryption-made-on-system"]')).toHaveCount(0);
    // the kit downloads as a text file that holds the code once
    const [dl] = await Promise.all([page.waitForEvent('download'), s.locator('[data-action="encryption-kit"]').click()]);
    expect(dl.suggestedFilename()).toMatch(/^openccu-lite-recovery-key-.*\.txt$/);
    const text = await (await dl.createReadStream()).toArray().then((c) => Buffer.concat(c).toString());
    expect(text.split(code).length).toBe(2);
    expect(text).toContain('age -d -i key.txt');
    expect(text).toContain('Backup emergency kit');
    // the print view shows the same text and a QR code
    await s.locator('[data-action="encryption-print"]').click();
    const sheet = page.locator('[data-sheet="kit"]');
    await expect(sheet).toBeVisible();
    await expect(sheet.locator('pre')).toContainText(code);
    await expect(sheet.locator('svg path')).toHaveCount(1);
    await sheet.locator('[data-sheet="close"]').click();
    await expect(sheet).toHaveCount(0);
    // the confirmation: disabled until ticked; Back returns to the key
    await s.locator('[data-action="encryption-next"]').click();
    await expect(s.locator('[data-action="encryption-confirm"]')).toBeDisabled();
    await s.locator('[data-action="encryption-back"]').click();
    await expect(s.locator('[data-recovery-key] code')).toHaveText(code);
    await s.locator('[data-action="encryption-next"]').click();
    await s.locator('[data-action="encryption-stored"]').check();
    await expect(s.locator('[data-action="encryption-confirm"]')).toBeEnabled();
    await s.locator('[data-action="encryption-confirm"]').click();
    await expect(s.locator('[data-encryption-state]')).toHaveAttribute('data-encryption-state', 'on');
    await expect(s.locator('[data-notice="encryption-done"]')).toContainText('Encryption is on');
    expect(posts.map((p) => p.path)).toEqual(['/api/system/v1/backup/encryption/recovery', '/api/system/v1/backup/encryption/confirm']);
    expect(posts[1].body).toEqual({pending_id: 'stub-pending'});
    // the key is nowhere on the page any more
    await expect(page.locator('body')).not.toContainText(code);
    // the downloads changed
    await expect(page.locator('[data-action="download-backup"]')).toHaveText('Download encrypted backup');
    await expect(page.locator('[data-action="download-plain"]')).toBeVisible();
});

test('aborting the wizard saves nothing', async ({page}) => {
    const posts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().includes('/backup/encryption/')) posts.push(new URL(r.url()).pathname);
    });
    await page.goto('/system/backup');
    const s = section(page);
    await s.locator('[data-action="encryption-setup"]').click();
    await s.locator('[data-action="encryption-make-key"]').click();
    await expect(s.locator('[data-recovery-key] code')).toBeVisible();
    await s.locator('[data-action="encryption-abort"]').click();
    await expect(s.locator('[data-wizard]')).toHaveCount(0);
    await expect(s.locator('[data-encryption-state]')).toHaveAttribute('data-encryption-state', 'none');
    expect(posts).toEqual(['/api/system/v1/backup/encryption/recovery']);
});

test('encrypted: the state with earlier keys, the test with its answers, the kit again, the switch off', async ({page}) => {
    await withView(page, ON);
    await page.goto('/system/backup');
    const s = section(page);
    await expect(s.locator('[data-encryption-state]')).toHaveAttribute('data-encryption-state', 'on');
    await expect(s.locator('[data-encryption-state]')).toContainText('recovery key ea77-3877-2a7b-31f8 from');
    await expect(s.locator('.ol-prev')).toContainText('0000-1111-2222-3333');
    await expect(s.locator('[data-action="encryption-setup"]')).toHaveCount(0);
    await s.locator('[data-action="encryption-test"]').click();
    const input = s.locator('[data-input="recovery-key"]');
    for (const c of [
        {typed: CODE.toLowerCase(), result: 'current', text: 'This is the current recovery key (ea77-3877-2a7b-31f8).'},
        {typed: '0000-0000-0000-0000-0000-0000-0000', result: 'previous', text: 'an earlier recovery key'},
        {typed: 'AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA', result: 'none', text: 'not one this system knows'},
        {typed: 'age14zwu3lsf95tuncdt5qzgp3a7jy7gjdsjmujl7d5n5tdv6qlpfu6qd7fvkk', result: 'error', text: 'the public part'},
        {typed: 'age-secret-key-pq-1qqq', result: 'error', text: 'not supported'},
        {typed: '0024-H36H', result: 'error', text: 'typo'},
    ]) {
        await input.fill(c.typed);
        await s.locator('[data-action="encryption-run-test"]').click();
        const r = s.locator(`[data-test-result="${c.result}"]`);
        await expect(r).toBeVisible();
        await expect(r).toContainText(c.text);
    }
    // a matching key gives the kit again
    await input.fill(CODE);
    await s.locator('[data-action="encryption-run-test"]').click();
    await expect(s.locator('[data-test-result="current"]')).toBeVisible();
    const [dl] = await Promise.all([page.waitForEvent('download'), s.locator('[data-action="encryption-kit-again"]').click()]);
    const text = await (await dl.createReadStream()).toArray().then((c) => Buffer.concat(c).toString());
    expect(text).toContain(CODE);
    expect(text).toContain('AGE-SECRET-KEY-173SCPSZRZXMZDYVU3QVPS6W8EF4LQWXGD4K06XZJJWMA0MQWA99S0V84MH');
    // off through the shell's dialog
    const puts: unknown[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/backup/encryption')) puts.push(JSON.parse(r.postData() ?? '{}'));
    });
    await s.locator('[data-action="encryption-off"]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Backups made so far stay encrypted');
    await dialog.getByRole('button', {name: 'Turn off'}).click();
    await expect(s.locator('[data-encryption-state]')).toHaveAttribute('data-encryption-state', 'off');
    expect(puts).toEqual([{enabled: false}]);
    await expect(s.locator('[data-action="encryption-on"]')).toBeVisible();
});

test('the unencrypted download asks first, then for the password, and follows with both tickets', async ({page}) => {
    await withView(page, ON);
    await page.route('**/api/auth/v1/confirm**', (r) => r.fulfill({json: {method: 'password'}}));
    const tickets: unknown[] = [];
    await page.route('**/api/auth/v1/ticket', (r) => {
        const b = JSON.parse(r.request().postData() ?? '{}');
        tickets.push(b);
        return r.fulfill({json: {ticket: b.confirm ? 'confirmed-1' : 'download-1', expires_in: 60}});
    });
    let downloadUrl = '';
    await page.route('**/api/system/v1/backup?**', (r) => {
        downloadUrl = r.request().url();
        return r.fulfill({status: 200, headers: {'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="x.sbk"'}, body: 'sbk'});
    });
    await page.goto('/system/backup');
    await page.locator('[data-action="download-plain"]').click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('every key of this system in plain');
    await dialog.getByRole('button', {name: 'Continue'}).click();
    await expect(dialog).toContainText('is confirmed every time. Confirm with your login password.');
    await dialog.locator('input[type="password"]').fill('secret');
    const [dl] = await Promise.all([page.waitForEvent('download'), dialog.getByRole('button', {name: 'Confirm'}).click()]);
    expect(dl.suggestedFilename()).toBe('x.sbk');
    const u = new URL(downloadUrl);
    expect(u.searchParams.get('prepared')).toBe('stub-backup');
    expect(u.searchParams.get('encrypted')).toBe('false');
    expect(u.searchParams.get('confirm')).toBe('confirmed-1');
    expect(u.searchParams.get('ticket')).toBe('download-1');
    expect(tickets).toEqual([{path: '/api/system/v1/backup/unencrypted', confirm: true, password: 'secret'}, {path: '/api/system/v1/backup'}]);
});

// B-95: an account of the identity provider confirms there; the page comes back with the ticket in
// the fragment and downloads the backup it prepared before it left
async function viaProvider(page: Page, back: string) {
    await page.route('**/api/auth/v1/confirm**', (r) => {
        const u = new URL(r.request().url());
        return r.fulfill({json: {method: 'oidc', url: `/api/auth/v1/oidc/confirm?path=${encodeURIComponent(u.searchParams.get('path') ?? '')}&return=${encodeURIComponent(u.searchParams.get('return') ?? '')}`}});
    });
    const asked: string[] = [];
    await page.route('**/api/auth/v1/oidc/confirm**', (r) => {
        const u = new URL(r.request().url());
        asked.push(`${u.searchParams.get('path')} ${u.searchParams.get('return')}`);
        return r.fulfill({status: 302, headers: {Location: `${u.searchParams.get('return')}${back}`}});
    });
    return asked;
}

test('the unencrypted download, confirmed at the identity provider: back on the page it follows with both tickets', async ({page}) => {
    await withView(page, ON);
    const asked = await viaProvider(page, '#confirm=confirmed-oidc');
    const tickets: unknown[] = [];
    await page.route('**/api/auth/v1/ticket', (r) => {
        const b = JSON.parse(r.request().postData() ?? '{}');
        tickets.push(b);
        return r.fulfill({json: {ticket: 'download-1', expires_in: 60}});
    });
    const polled: string[] = [];
    await page.route('**/api/system/v1/backup/prepare/*', (r) => {
        polled.push(new URL(r.request().url()).pathname);
        return r.fulfill({json: {id: 'stub-backup', state: 'ready', started: '2026-10-29T12:00:00Z', size: 3}});
    });
    let downloadUrl = '';
    await page.route('**/api/system/v1/backup?**', (r) => {
        downloadUrl = r.request().url();
        return r.fulfill({status: 200, headers: {'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="x.sbk"'}, body: 'sbk'});
    });
    await page.goto('/system/backup');
    await page.locator('[data-action="download-plain"]').click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', {name: 'Continue'}).click();
    await expect(dialog).toContainText('you sign in at the identity provider once more and come back here');
    const [dl] = await Promise.all([page.waitForEvent('download'), dialog.getByRole('button', {name: 'Continue'}).click()]);
    expect(dl.suggestedFilename()).toBe('x.sbk');
    expect(asked).toEqual(['/api/system/v1/backup/unencrypted /system/backup']);
    expect(polled).toEqual(['/api/system/v1/backup/prepare/stub-backup']);
    const u = new URL(downloadUrl);
    expect(u.searchParams.get('prepared')).toBe('stub-backup');
    expect(u.searchParams.get('encrypted')).toBe('false');
    expect(u.searchParams.get('confirm')).toBe('confirmed-oidc');
    expect(u.searchParams.get('ticket')).toBe('download-1');
    expect(tickets).toEqual([{path: '/api/system/v1/backup'}]);
    // the ticket is gone from the address bar, and a reload downloads nothing
    expect(new URL(page.url()).hash).toBe('');
    expect(await page.evaluate(() => sessionStorage.getItem('ol.backup.plain-pending'))).toBeNull();
});

test('the unencrypted download: a refusal at the provider, and a prepared backup gone meanwhile, say so (German too)', async ({page}) => {
    await withView(page, ON);
    await viaProvider(page, `#confirm-error=${encodeURIComponent('the provider did not ask for the password again')}`);
    let downloads = 0;
    await page.route('**/api/system/v1/backup?**', (r) => {
        downloads++;
        return r.fulfill({status: 200, body: 'sbk'});
    });
    await page.goto('/system/backup');
    await page.locator('[data-action="download-plain"]').click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', {name: 'Continue'}).click();
    await dialog.getByRole('button', {name: 'Continue'}).click();
    await expect(page.getByText('The confirmation at the identity provider was refused: the provider did not ask for the password again')).toBeVisible();
    expect(new URL(page.url()).hash).toBe('');

    // confirmed, but the prepared backup is gone (it waits 30 minutes)
    await page.unrouteAll({behavior: 'ignoreErrors'});
    await withView(page, ON);
    await viaProvider(page, '#confirm=confirmed-oidc');
    await page.route('**/api/system/v1/backup/prepare/*', (r) => r.fulfill({status: 404, json: {error: 'unknown-backup', message: 'no such prepared backup'}}));
    await page.route('**/api/system/v1/backup?**', (r) => {
        downloads++;
        return r.fulfill({status: 200, body: 'sbk'});
    });
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    await page.locator('[data-action="download-plain"]').click();
    await dialog.getByRole('button', {name: 'Weiter'}).click();
    await dialog.getByRole('button', {name: 'Weiter'}).click();
    await expect(page.getByText('Die vorbereitete Sicherung ist nicht mehr da. Starten Sie den Download erneut.')).toBeVisible();
    expect(downloads).toBe(0);
});

const CHECK = {ok: true, output: 'generated on 3.89.9, applying to 3.89.9', backup_version: '3.89.9', running_version: '3.89.9', needs_key: false, has_rega: false};

async function upload(page: Page) {
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk.age', mimeType: 'application/octet-stream', buffer: Buffer.from('age-encryption.org/v1\n')});
    await page.getByRole('button', {name: 'Check', exact: true}).click();
}

test('restore: a file this system opens is checked at once; one for the recovery key asks, with the hints; a typo, the wrong key, then the security key', async ({page}) => {
    await withView(page, ON);
    let answer: Record<string, unknown> = {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: true, format: 'age-v1', box_fingerprint: '1f3a-9c2e-7b40-d15e', recovery_fingerprint: 'ea77-3877-2a7b-31f8', known: 'current', opened_with: 'box', needs_recovery_key: false, created_here: true}};
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: answer}));
    await page.goto('/system/backup');
    await upload(page);
    await expect(page.locator('[data-restore="opened-with"]')).toContainText("opened with this system's key");
    await expect(page.locator('[data-restore="opened-with"]')).toContainText('made by this system');
    await expect(page.locator('[data-restore="needs-recovery-key"]')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Restore and reboot'})).toBeEnabled();

    // the current key's file from another system: the prompt names the current key
    answer = {file: 'restore-ccu.sbk.age', check: null, encryption: {encrypted: true, format: 'age-v1', recovery_fingerprint: 'ea77-3877-2a7b-31f8', known: 'current', needs_recovery_key: true, created_here: false}};
    await upload(page);
    const prompt = page.locator('[data-restore="needs-recovery-key"]');
    await expect(prompt.locator('[data-notice="restore-recovery-key"]')).toContainText('encrypted to your current recovery key (ea77-3877-2a7b-31f8)');
    await expect(page.getByRole('button', {name: 'Restore and reboot'})).toHaveCount(0);
    await expect(page.locator('[data-action="restore-decrypt"]')).toBeDisabled();

    // an earlier key, an unknown one, a passphrase file
    answer = {...answer, encryption: {...(answer.encryption as object), known: 'previous', recovery_fingerprint: '0000-1111-2222-3333', key_created: '2026-03-01T12:00:00Z'}};
    await upload(page);
    await expect(prompt.locator('[data-notice="restore-recovery-key"]')).toContainText('earlier recovery key 0000-1111-2222-3333 from');
    answer = {...answer, encryption: {...(answer.encryption as object), known: 'unknown', recovery_fingerprint: ''}};
    await upload(page);
    await expect(prompt.locator('[data-notice="restore-recovery-key"]')).toContainText('not to a key this system knows. Enter the recovery key from the emergency kit');
    answer = {...answer, encryption: {...(answer.encryption as object), passphrase: true}};
    await upload(page);
    await expect(prompt.locator('[data-notice="restore-passphrase"]')).toContainText('passphrase');
    await expect(prompt.locator('[data-input="restore-recovery-key"]')).toHaveCount(0);

    // the decryption: a typo, the wrong key, then the right one - and the security key afterwards
    answer = {file: 'restore-ccu.sbk.age', check: null, encryption: {encrypted: true, format: 'age-v1', recovery_fingerprint: 'ea77-3877-2a7b-31f8', known: 'current', needs_recovery_key: true, created_here: false}};
    await upload(page);
    const decrypts: unknown[] = [];
    await page.route('**/api/system/v1/restore/decrypt', (r) => {
        const b = JSON.parse(r.request().postData() ?? '{}');
        decrypts.push(b);
        if (b.recovery_key === 'typo') return r.fulfill({status: 422, json: {error: 'invalid-key', message: 'checksum'}});
        if (b.recovery_key === 'AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA') return r.fulfill({status: 422, json: {error: 'wrong-key', message: 'no'}});
        if (b.recovery_key === 'BBBB-BBBB-BBBB-BBBB-BBBB-BBBB-BBBB') return r.fulfill({status: 422, json: {error: 'corrupt', message: 'chunk', detail: {upload_removed: true}}});
        return r.fulfill({json: {file: 'restore-ccu.sbk', check: {...CHECK, needs_key: true, output: 'backup and/or system protected by security key'}, encryption: {encrypted: true, format: 'age-v1', recovery_fingerprint: 'ea77-3877-2a7b-31f8', known: 'current', opened_with: 'recovery', needs_recovery_key: false, created_here: false}}});
    });
    const input = prompt.locator('[data-input="restore-recovery-key"]');
    await input.fill('typo');
    await page.locator('[data-action="restore-decrypt"]').click();
    await expect(page.locator('.ol-notice.err').first()).toContainText('There is a typo in the recovery key');
    await input.fill('AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA');
    await page.locator('[data-action="restore-decrypt"]').click();
    await expect(page.locator('.ol-notice.err').first()).toContainText('not encrypted to that key');
    // the prompt stays for the next try (the upload waits on the system) ...
    await expect(prompt).toBeVisible();
    // ... but a damaged upload is gone with the refusal (B-194): back to the file picker
    await input.fill('BBBB-BBBB-BBBB-BBBB-BBBB-BBBB-BBBB');
    await page.locator('[data-action="restore-decrypt"]').click();
    await expect(page.locator('.ol-notice.err').first()).toContainText('damaged or was tampered with; nothing of it was kept');
    await expect(prompt).toHaveCount(0);
    await upload(page);
    await expect(prompt).toBeVisible();
    await input.fill(CODE);
    await page.locator('[data-action="restore-decrypt"]').click();
    await expect(page.locator('[data-restore="opened-with"]')).toContainText('decrypted with the recovery key');
    await expect(page.locator('[data-restore="opened-with"]')).toContainText('did not create this file');
    await expect(page.locator('[data-restore="needs-recovery-key"]')).toHaveCount(0);
    // the security key comes next, with the two keys told apart
    await expect(page.locator('label', {hasText: 'Security key'})).toBeVisible();
    await expect(page.locator('text=The security key protects the radio link')).toBeVisible();
    await expect(page.getByRole('button', {name: 'Restore and reboot'})).toBeDisabled();
    expect(decrypts).toEqual([{file: 'restore-ccu.sbk.age', recovery_key: 'typo'}, {file: 'restore-ccu.sbk.age', recovery_key: 'AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA'}, {file: 'restore-ccu.sbk.age', recovery_key: 'BBBB-BBBB-BBBB-BBBB-BBBB-BBBB-BBBB'}, {file: 'restore-ccu.sbk.age', recovery_key: CODE}]);
});

test('a plain .sbk behaves as before', async ({page}) => {
    await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false, needs_recovery_key: false, created_here: false}}}));
    await page.goto('/system/backup');
    await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('sbk')});
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    await expect(page.locator('dl.ol-kv')).toContainText('valid');
    await expect(page.locator('[data-restore="opened-with"]')).toHaveCount(0);
    await expect(page.locator('[data-restore="needs-recovery-key"]')).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Restore and reboot'})).toBeEnabled();
});

// openccu-lite B-193: /restore/apply answers before the reboot. rebooting true runs the countdown;
// rebooting false (the reboot did not start) says the restore is staged and keeps no countdown.
for (const rebooting of [true, false]) {
    test(`restore and reboot: the answer ${rebooting ? 'reboots' : 'is staged only'}`, async ({page}) => {
        await page.route('**/api/system/v1/restore/check', (r) => r.fulfill({json: {file: 'restore-ccu.sbk', check: CHECK, encryption: {encrypted: false, needs_recovery_key: false, created_here: false}}}));
        await page.route('**/api/system/v1/boot-expect**', (r) => r.fulfill({json: {kind: 'restore', product: 'ova', expect: {down: 5, http: 20, ui: 5, ready: 10}, default: {down: 5, http: 20, ui: 5, ready: 10}, measured: {down: 0, http: 0, ui: 0, ready: 0}}}));
        const posts: string[] = [];
        await page.route('**/api/system/v1/restore/apply', (r) => {
            posts.push(r.request().postData() ?? '');
            return r.fulfill({json: rebooting ? {ok: true, output: '4) Scheduling backup restore for next boot cycle, OK', rebooting: true} : {ok: true, output: '4) OK', rebooting: false, message: 'restore staged, but the reboot did not start: no reboot command'}});
        });
        await page.goto('/system/backup');
        await page.locator('input[type="file"][accept=".sbk,.age"]').setInputFiles({name: 'ccu.sbk', mimeType: 'application/octet-stream', buffer: Buffer.from('sbk')});
        await page.getByRole('button', {name: 'Check', exact: true}).click();
        await page.getByRole('button', {name: 'Restore and reboot'}).click();
        const dialog = page.getByRole('dialog');
        await expect(dialog).toContainText('Restore this backup and reboot?');
        // task 317 (D-120): the restore replaces the HmIP identity with the rest - asked in red
        await expect(dialog).toContainText('pairings, keys, the HmIP identity, addons and their settings');
        await expect(dialog.getByRole('button', {name: 'Restore and reboot'})).toHaveClass(/danger/);
        await dialog.getByRole('button', {name: 'Restore and reboot'}).click();
        // the page reads the uptime and the boot expectation first, then posts
        await expect.poll(() => posts.length).toBe(1);
        expect(JSON.parse(posts[0])).toEqual({file: 'restore-ccu.sbk', key: '', force: false, confirm: true});
        if (rebooting) {
            await expect(page.locator('.ol-restorenotice')).toContainText('Restoring — the system reboots now.');
            await expect(page.locator('.ol-restorenotice pre')).toContainText('Scheduling backup restore');
            await expect(page.locator('.ol-notice.err', {hasText: 'The restore is prepared'})).toHaveCount(0);
        } else {
            // the stub's standing nightly-target notice is an .err too
            await expect(page.locator('.ol-notice.err', {hasText: 'The restore is prepared'})).toHaveText('The restore is prepared but the reboot did not start: restore staged, but the reboot did not start: no reboot command Reboot the system to apply it.');
            await expect(page.locator('.ol-restorenotice')).toHaveCount(0);
        }
    });
}

test('German', async ({page}) => {
    await withView(page, ON);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    await expect(page.locator('h2#encryption')).toContainText('Verschlüsselung');
    await expect(section(page).locator('[data-encryption-state]')).toContainText('Verschlüsselt');
    await expect(section(page).locator('[data-action="encryption-test"]')).toHaveText('Wiederherstellungsschlüssel prüfen');
    await expect(page.locator('[data-action="download-backup"]')).toHaveText('Verschlüsselte Sicherung herunterladen');
    await section(page).locator('[data-action="encryption-rotate"]').click();
    await expect(section(page).locator('[data-wizard="intro"]')).toContainText('Bisherige Sicherungen brauchen den aktuellen Wiederherstellungsschlüssel');
    await section(page).locator('[data-action="encryption-make-key"]').click();
    await expect(section(page).locator('[data-notice="encryption-warning"]')).toContainText('Niemand kann den Schlüssel wiederbeschaffen');
    await section(page).locator('[data-action="encryption-next"]').click();
    await expect(section(page).locator('[data-action="encryption-confirm"]')).toHaveText('Neuen Wiederherstellungsschlüssel verwenden');
});

// occulited B-90: the backup is prepared on the system first - the line counts the time while the
// panel and the button keep their boxes - and then downloaded with ?prepared=<id>
test('the download prepares the backup first, then follows it with ?prepared', async ({page}) => {
    let polls = 0;
    await page.route('**/api/system/v1/backup/prepare', (r) => r.fulfill({status: 202, json: {id: 'job-1', state: 'running', started: new Date().toISOString()}}));
    await page.route('**/api/system/v1/backup/prepare/job-1', (r) => {
        polls++;
        return r.fulfill({json: {id: 'job-1', state: polls < 2 ? 'running' : 'ready', started: new Date().toISOString(), size: 3}});
    });
    let downloadUrl = '';
    await page.route('**/api/system/v1/backup?**', (r) => {
        downloadUrl = r.request().url();
        return r.fulfill({status: 200, headers: {'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="x.sbk"'}, body: 'sbk'});
    });
    await page.goto('/system/backup');
    const panel = page.locator('[data-section="create"]');
    const button = page.locator('[data-action="download-backup"]');
    await expect(button).toBeVisible();
    const before = {panel: await panel.boundingBox(), button: await button.boundingBox()};
    const [dl] = await Promise.all([page.waitForEvent('download'), button.click()]);
    expect(dl.suggestedFilename()).toBe('x.sbk');
    expect(new URL(downloadUrl).searchParams.get('prepared')).toBe('job-1');
    expect(polls).toBe(2);
    expect(await panel.boundingBox()).toEqual(before.panel);
    expect(await button.boundingBox()).toEqual(before.button);
});

test('the preparing line while the backup is made, and a failed one says why', async ({page}) => {
    await page.route('**/api/system/v1/backup/prepare', (r) => r.fulfill({status: 202, json: {id: 'job-2', state: 'running', started: new Date().toISOString()}}));
    let fail = false;
    await page.route('**/api/system/v1/backup/prepare/job-2', (r) => r.fulfill({json: fail ? {id: 'job-2', state: 'failed', started: '', error: 'createBackup.sh: exit status 1'} : {id: 'job-2', state: 'running', started: ''}}));
    await page.goto('/system/backup');
    const panel = page.locator('[data-section="create"]');
    await expect(page.locator('[data-action="download-backup"]')).toBeVisible();
    const before = await panel.boundingBox();
    await page.locator('[data-action="download-backup"]').click();
    await expect(panel.locator('[data-backup-wait="running"]')).toContainText(/Creating the backup… 0:0\d/);
    expect(await panel.boundingBox()).toEqual(before);
    fail = true;
    await expect(page.locator('.ol-notice.err')).toContainText('createBackup.sh: exit status 1', {timeout: 10_000});
    await expect(panel.locator('[data-backup-wait="running"]')).toHaveCount(0);
});
