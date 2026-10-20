import {expect, test, type Page} from '@playwright/test';

// Task 86: the Backup page's targets - a card per target with its state, the last backup, the
// mount, the SSH key and the server's key; adding an SSH target (the key line, getting and trusting
// the server's key); the test's answers; switching encryption off asks; removing asks; the backups
// on a target and Restore this; a container; German.

const card = (page: Page, id: string) => page.locator(`[data-target="${id}"]`);

async function plantTargets(page: Page, change: (v: Record<string, any>) => void) {
    const res = await page.request.get('/api/system/v1/backup/targets');
    const view = await res.json();
    change(view);
    await page.route('**/api/system/v1/backup/targets', (r) => (r.request().method() === 'GET' ? r.fulfill({json: view}) : r.fallback()));
    return view;
}

test('the cards: kinds, states, the mount, the key line, the directory on the system itself', async ({page}) => {
    await page.goto('/system/backup');
    await expect(page.locator('h2#targets')).toHaveText(/Backup targets/);
    await expect(page.locator('[data-action="nightly"]')).toBeChecked();
    const usb = card(page, 'directory');
    await expect(usb).toContainText('USB / directory');
    await expect(usb.locator('[data-state]')).toHaveAttribute('data-state', 'idle');
    await expect(usb.locator('[data-notice="directory-userfs"]')).toContainText('on the system itself');
    await expect(usb.locator('[data-last]')).toContainText('Last backup');
    const ssh = card(page, 'tssh0001');
    await expect(ssh.locator('[data-state]')).toHaveText('writable');
    await expect(ssh.locator('[data-public-key]')).toHaveText(/^restrict ssh-ed25519 AAAA/);
    await expect(ssh.locator('[data-host-key]')).toContainText('SHA256:3b9Qp0F6');
    await expect(ssh.locator('[data-action="mount"]')).toHaveCount(0);
    // task 228: a share target - the share of System → Storage and its folder
    const nas = card(page, 'tnas0001');
    await expect(nas).toContainText('Network share');
    await expect(nas.locator('.ol-card-sub')).toHaveText('share nas · openccu');
    await expect(nas.locator('[data-mounted]')).toContainText('//nas.lan/backup');
    await expect(nas.locator('[data-action="unmount"]')).toBeVisible();
    // occulited task 88: no recovery key yet, and no inline notice about it - the Encryption panel's
    // badge and edge and the Status warning say it
    await expect(page.locator('[data-notice="targets-plain"]')).toHaveCount(0);
});

test('add an SSH target: the form, the key line, the server key fetched and trusted', async ({page}) => {
    const sent: {method: string; path: string; body: any}[] = [];
    page.on('request', (r) => {
        if (r.url().includes('/backup/targets') && r.method() !== 'GET') sent.push({method: r.method(), path: new URL(r.url()).pathname, body: JSON.parse(r.postData() || '{}')});
    });
    await page.goto('/system/backup');
    await page.locator('[data-add="sftp"]').click();
    const form = page.locator('[data-form="sftp"]');
    await form.locator('[data-field="name"]').fill('Pi 4');
    await form.locator('[data-field="host"]').fill('ccu-vm-1.lan');
    await form.locator('[data-field="user"]').fill('root');
    await form.locator('[data-field="sshpath"]').fill('/usr/local/sftp-target');
    await expect(form.locator('[data-field="subdir"]')).toHaveValue('openccu');
    // the list after the save shows the new target, its key, and no server key yet
    const view = await plantTargets(page, (v) => {
        v.targets.push({id: 'tnew0001', name: 'Pi 4', kind: 'sftp', enabled: true, max_backups: 30, subdir: 'openccu', encrypt: true, append_only: false,
            sftp: {host: 'ccu-vm-1.lan', port: 22, user: 'root', path: '/usr/local/sftp-target', public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINew openccu-lite-backup@openccu'},
            state: {state: 'host-key-unknown', failures: 0, mounted: false}});
    });
    await form.locator('[data-action="save"]').click();
    const post = sent.find((s) => s.method === 'POST');
    expect(post?.path).toBe('/api/system/v1/backup/targets');
    expect(post?.body).toMatchObject({kind: 'sftp', name: 'Pi 4', subdir: 'openccu', encrypt: true, sftp: {host: 'ccu-vm-1.lan', port: 22, user: 'root', path: '/usr/local/sftp-target'}});
    expect(post?.body.id).toBeUndefined();
    const c = card(page, 'tnew0001');
    await expect(c.locator('[data-public-key]')).toHaveText('restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINew openccu-lite-backup@openccu');
    await expect(c.locator('[data-state]')).toHaveText("server's key not confirmed");
    await c.locator('[data-action="scan-key"]').click();
    await expect(c.locator('[data-scan="new"]')).toContainText('SHA256:NewServerKeyOfTheStub');
    view.targets[3].sftp.host_key = {type: 'ssh-ed25519', fingerprint: 'SHA256:NewServerKeyOfTheStub0000000000000000000000'};
    view.targets[3].state = {state: 'idle', failures: 0, mounted: false};
    await c.locator('[data-action="trust-key"]').click();
    await expect(c.locator('[data-host-key]')).toContainText('SHA256:NewServerKeyOfTheStub');
    const put = sent.find((s) => s.method === 'PUT');
    expect(put?.path).toBe('/api/system/v1/backup/targets/tnew0001/hostkey');
    expect(put?.body).toEqual({fingerprint: 'SHA256:NewServerKeyOfTheStub0000000000000000000000'});
});

test('the test: writable with its speed, and a root-squashed export with the hint', async ({page}) => {
    await page.goto('/system/backup');
    const ssh = card(page, 'tssh0001');
    await ssh.locator('[data-action="test"]').click();
    await expect(ssh.locator('[data-test-result="writable"]')).toContainText('1 MB written, read back and deleted. About 11.4 MB/s.');
    await page.route('**/backup/targets/tnas0001/test', (r) => r.fulfill({json: {ok: false, state: 'read-only', step: 'create', error: 'permission denied', free_bytes: 0, needed_bytes: 0}}));
    const nfs = card(page, 'tnas0001');
    await nfs.locator('[data-action="test"]').click();
    await expect(nfs.locator('[data-test-result="read-only"]')).toContainText('not writable (create): permission denied');
    await expect(nfs.locator('[data-test-result="read-only"]')).toContainText('root_squash');
});

test('switching encryption off asks; removing asks and keeps the files', async ({page}) => {
    await page.goto('/system/backup');
    const nfs = card(page, 'tnas0001');
    await nfs.locator('[data-action="edit"]').click();
    const enc = nfs.locator('[data-field="encrypt"]');
    await expect(enc).toBeChecked();
    await enc.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Unencrypted backups carry every key of this system');
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(enc).toBeChecked();
    await nfs.locator('[data-action="cancel"]').click();
    const deleted: string[] = [];
    page.on('request', (r) => r.method() === 'DELETE' && deleted.push(new URL(r.url()).pathname));
    await nfs.locator('[data-action="remove"]').click();
    await expect(dialog).toContainText('The backups already there stay where they are');
    await dialog.getByRole('button', {name: 'Remove'}).click();
    await expect.poll(() => deleted).toEqual(['/api/system/v1/backup/targets/tnas0001']);
});

test('the backups on a target and Restore this: the check of the file from the target', async ({page}) => {
    let asked: unknown = null;
    await page.route('**/api/system/v1/restore/check', (r) => {
        asked = JSON.parse(r.request().postData() || '{}');
        return r.fulfill({json: {file: 'restore-openccu-2026-09-07.sbk', check: {ok: true, output: 'backup ok', needs_key: false, has_rega: false}, encryption: {encrypted: false, needs_recovery_key: false, created_here: true}}});
    });
    await page.goto('/system/backup');
    const usb = card(page, 'directory');
    await usb.locator('[data-action="backups"]').click();
    await expect(usb.locator('[data-backups] tbody tr')).toHaveCount(2);
    await usb.locator('[data-action="restore-this"]').first().click();
    await page.getByRole('dialog').getByRole('button', {name: 'Check'}).click();
    await expect(page.locator('[data-restore="from-target"]')).toContainText('openccu-2026-09-07.sbk');
    expect(asked).toEqual({target: 'directory', name: 'openccu-2026-09-07.sbk'});
    await expect(page.getByRole('button', {name: 'Restore and reboot'})).toBeEnabled();
});

test('a container: no share, the host notice; a failed target is red with its reason', async ({page}) => {
    await plantTargets(page, (v) => {
        v.container = 'lxc';
        v.kinds = {directory: '', share: 'container', nfs: 'container', cifs: 'container', sftp: ''};
        v.targets[2].state = {state: 'unsupported', unsupported: 'container', failures: 0, mounted: false};
        v.targets[1].state = {state: 'auth-failed', detail: 'ssh: unable to authenticate', failures: 2, mounted: false, next_retry_at: '2026-09-07T09:00:00Z'};
    });
    await page.goto('/system/backup');
    await expect(page.locator('[data-add="share"]')).toBeDisabled();
    await expect(page.locator('[data-add="nfs"]')).toHaveCount(0);
    await expect(page.locator('[data-notice="container"]')).toContainText('This system is a container');
    await expect(card(page, 'tnas0001').locator('[data-notice="unsupported"]')).toContainText('Mount the share on the host');
    const ssh = card(page, 'tssh0001');
    await expect(ssh).toHaveClass(/err/);
    await expect(ssh.locator('[data-state]')).toHaveText('login refused');
    await expect(ssh.locator('[data-detail]')).toContainText('unable to authenticate');
});

test('German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/backup');
    await expect(page.locator('h2#targets')).toHaveText(/Sicherungsziele/);
    await expect(card(page, 'tssh0001').locator('[data-state]')).toHaveText('beschreibbar');
    await expect(page.locator('[data-add="sftp"]')).toHaveText('SSH-Server (SFTP)');
    await expect(card(page, 'directory').locator('[data-action="run"]')).toHaveText('Jetzt sichern');
});

// task 161, task 228: the directory target is picked with the location picker - a USB stick by its
// label (a read-only one or one without a label cannot be picked) or the system storage, and a folder
// completed from the ones there; an older path says so; a target whose stick is missing is red.
test('the directory target by location, and a target without its stick', async ({page}) => {
    await plantTargets(page, (v) => {
        const usb = v.targets.find((x: {id: string}) => x.id === 'directory');
        usb.state = {state: 'no-medium', detail: 'no USB stick is mounted for /media/usb0/backup', failures: 1, mounted: false};
    });
    await page.goto('/system/backup');
    const usb = card(page, 'directory');
    await expect(usb.locator('[data-state]')).toHaveText('no USB stick');
    await expect(usb).toHaveClass(/err/);
    await usb.locator('[data-action="edit"]').click();
    const form = page.locator('[data-form="directory"]');
    await expect(form.locator('[data-legacy-path]')).toContainText('/media/usb0/backup');
    await form.locator('[data-location-button]').click();
    const list = form.locator('[data-location-list]');
    await expect(list.locator('[data-location]')).toHaveCount(3);
    await expect(list.locator('[data-location="userfs"]')).toContainText('System storage (userfs)');
    await expect(list.locator('[data-location="userfs"]')).toContainText("A backup on the system's own storage is lost with the system.");
    await expect(list.locator('[data-location="usb:BACKUP"]')).toContainText('In use by: the backup target USB');
    await expect(list.locator('[data-location="usb:"]')).toHaveAttribute('aria-disabled', 'true');
    await expect(list.locator('[data-location="usb:"] [data-why]')).toContainText('It has no label');
    await expect(list.locator('[data-location^="share:"]')).toHaveCount(0);
    await list.locator('[data-location="usb:BACKUP"]').click();
    await expect(list).toHaveCount(0);
    await expect(form.locator('[data-location-button]')).toContainText('BACKUP');
    const folder = form.locator('[data-location-folder]');
    await expect(folder).toHaveValue('backup');
    await expect(form.locator('[data-legacy-path]')).toHaveCount(0);
    await folder.fill('backup/2');
    await expect(form.locator('datalist option')).toHaveCount(1);
    await expect(form.locator('datalist option')).toHaveAttribute('value', 'backup/2025');
    await expect(form.locator('[data-folder-new]')).toContainText('A new folder');
    await folder.fill('bad folder');
    await expect(form.locator('[data-folder-problem]')).toContainText('letters, digits');
    await expect(form.locator('[data-action="save"]')).toBeDisabled();
    await folder.fill('backup/ccu');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/backup/targets/directory')),
        form.locator('[data-action="save"]').click(),
    ]);
    expect(req.postDataJSON().directory).toEqual({path: '', location: 'usb:BACKUP/backup/ccu'});
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

// task 228: a share target - the share picked by its name (a read-only one cannot be), the folder
// this system's own (the host name by default)
test('add a share target: the share by its name and a folder', async ({page}) => {
    await page.goto('/system/backup');
    await page.locator('[data-add="share"]').click();
    const form = page.locator('[data-form="share"]');
    await expect(form.locator('[data-action="save"]')).toBeDisabled();
    await form.locator('[data-location-button]').click();
    const list = form.locator('[data-location-list]');
    await expect(list.locator('[data-location]')).toHaveCount(2);
    await expect(list.locator('[data-location="share:media"]')).toHaveAttribute('aria-disabled', 'true');
    await expect(list.locator('[data-location="share:media"] [data-why]')).toContainText('mounted read-only');
    await list.locator('[data-location="share:nas"]').click();
    await expect(form.locator('[data-location-folder]')).toHaveValue('openccu');
    await form.locator('[data-field="name"]').fill('NAS');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/backup/targets')),
        form.locator('[data-action="save"]').click(),
    ]);
    expect(req.postDataJSON()).toMatchObject({kind: 'share', name: 'NAS', subdir: '', share: {id: 'nas', folder: 'openccu'}});
});
