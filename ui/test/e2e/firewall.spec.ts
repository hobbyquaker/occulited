import {expect, test, type Page} from '@playwright/test';

// task 157 (D-105): the firewall as one ordered list of INPUT rules. Each test keeps its own box
// (stub-fw=<id>), so the projects that share the stub never see each other's changes.
async function own(page: Page, baseURL: string | undefined) {
    await page.context().addCookies([{name: 'stub-fw', value: `fw-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
}
const row = (page: Page, port: number, n = 0) => page.locator(`table.fw-rules tr[data-port="${port}"]`).nth(n);

test('the list: the conversion notice, the owners, the frame, the policy row', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const notice = page.locator('[data-notice="fw-migrated"]');
    await expect(notice).toContainText('converted from the old configuration');
    await expect(notice).toContainText('Service XMLRPC (full access): 1 rule(s) from the local networks for ports 2001, as before.');
    await expect(notice).toContainText('Automatic rules: addon: mosquitto, Network discovery, HmIP access points, SSH, Web server.');
    await expect(notice).toContainText('Service NEOSERVER (full access): not converted');
    await expect(page.locator('table.fw-rules tr[data-rule]')).toHaveCount(18);
    // an owned rule: its owner shown, its port and protocol fixed, its source editable
    await expect(row(page, 80).locator('[data-owner="web"]')).toHaveText('Web server');
    await expect(row(page, 80).getByLabel('Port', {exact: true})).toBeDisabled();
    await expect(row(page, 80).getByLabel('Source', {exact: true})).toBeEnabled();
    await expect(row(page, 80).getByLabel('Source', {exact: true})).toHaveValue('local networks');
    await expect(row(page, 1883).locator('[data-owner]')).toHaveText('addon: mosquitto');
    await expect(row(page, 2001).getByLabel('Port', {exact: true})).toBeEnabled();
    // the frame, read-only, behind its disclosure; the policy row below the rules
    await page.getByRole('button', {name: 'Always allowed, before the rules'}).click();
    await expect(page.locator('.fw-frame pre').first()).toContainText('-A INPUT -i lo -j ACCEPT');
    // task 249: nothing to drop in a read-only view - its button says Close
    await expect(page.getByRole('group', {name: 'Always allowed'}).getByRole('button', {name: 'Close'})).toBeVisible();
    await expect(page.getByLabel('Policy IPv4')).toHaveValue('DROP');
    await expect(page.getByRole('button', {name: 'Apply'})).toBeDisabled();
    // the notice goes when dismissed, and stays gone
    await notice.getByRole('button', {name: 'Dismiss'}).click();
    await expect(notice).toHaveCount(0);
    await page.reload();
    await expect(page.locator('[data-notice="fw-migrated"]')).toHaveCount(0);
});

test('in German: the conversion notice in the viewer\'s language', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/firewall');
    const notice = page.locator('[data-notice="fw-migrated"]');
    await expect(notice).toContainText('Modus RESTRICTIVE: Die Policy ist DROP für IPv4 und IPv6.');
    await expect(notice).toContainText('Automatische Regeln: Zusatzsoftware: mosquitto, Netzwerkerkennung, HmIP-Zugangspunkte, SSH, Webserver.');
    await expect(notice).toContainText('Freigegebener Port „5353/udp“ war keine Portnummer');
    // iptables' own words stay as iptables has them (maintainer)
    await expect(page.locator('table.fw-rules thead')).toContainText('Source');
    await expect(page.locator('table.fw-rules thead')).toContainText('Destination');
    await expect(page.locator('table.fw-rules thead')).toContainText('Target');
    await expect(row(page, 80).getByLabel('Source', {exact: true})).toHaveValue('local networks');
});

test('edit, apply, confirm within the window', async ({page, baseURL}) => {
    await own(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/api/system/v1/firewall')) puts.push(r.postData() ?? '');
    });
    await page.goto('/system/firewall');
    // an IPv4 source limits the rule to IPv4: the radio follows and the others lock
    const r2001 = row(page, 2001);
    await r2001.getByLabel('Source', {exact: true}).fill('192.168.1.0/24');
    await expect(r2001.getByLabel('Family')).toHaveValue('ipv4');
    await expect(r2001.getByLabel('Family').locator('option[value="ipv6"]')).toBeDisabled();
    await r2001.getByLabel('Target').selectOption('REJECT');
    await expect(page.locator('[data-diff]')).toContainText('Changed: 2001/tcp from 192.168.1.0/24 (IPv4) → REJECT');
    await page.getByRole('button', {name: 'Apply'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Apply the firewall rules?');
    await expect(dialog).toContainText('Confirm them within 60 seconds');
    await dialog.getByRole('button', {name: 'Apply'}).click();
    const pending = page.locator('[data-notice="fw-pending"]');
    await expect(pending).toContainText('The new rules are loaded.');
    await expect(pending).toContainText(/within (59|60) s/);
    const sent = JSON.parse(puts[0]!);
    expect(sent.rules.find((r: {port: number}) => r.port === 2001)).toMatchObject({source: '192.168.1.0/24', family: 'ipv4', target: 'REJECT'});
    // locked while the window is open
    await expect(row(page, 2001).getByLabel('Target')).toBeDisabled();
    await pending.getByRole('button', {name: 'Confirm'}).click();
    await expect(page.locator('[data-notice="fw-notice"]')).toHaveText('The firewall rules are confirmed.');
    await expect(pending).toHaveCount(0);
    await page.reload();
    await expect(row(page, 2001).getByLabel('Target')).toHaveValue('REJECT');
    await expect(page.getByRole('button', {name: 'Apply'})).toBeDisabled();
});

test('duplicate below, move, delete, add - and revert the window', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    // SSH's rule duplicated: the copy directly below, with the same owner
    await row(page, 22).getByRole('button', {name: 'Duplicate'}).click();
    await expect(row(page, 22, 1).locator('[data-owner="ssh"]')).toHaveText('SSH');
    const ports = page.locator('table.fw-rules tr[data-rule]');
    await expect(ports.nth(3)).toHaveAttribute('data-port', '22');
    await row(page, 22, 1).getByLabel('Source', {exact: true}).fill('fe80::/10');
    await expect(row(page, 22, 1).getByLabel('Family')).toHaveValue('ipv6');
    // move the hand-made 2001 up (by its handle, task 162), delete the web server's 443, add one
    await row(page, 2001).locator('button.ol-grip').press('ArrowUp');
    await expect(ports.nth(15)).toHaveAttribute('data-port', '2001');
    await row(page, 443).getByRole('button', {name: 'Delete'}).click();
    await page.getByRole('button', {name: 'Add rule'}).click();
    await ports.last().getByLabel('Port', {exact: true}).fill('8123');
    const diff = page.locator('[data-diff]');
    await expect(diff).toContainText('New: 22/tcp from fe80::/10 (IPv6) → ACCEPT');
    await expect(diff).toContainText('Removed: 443/tcp from local networks (IPv4 and IPv6) → ACCEPT');
    await expect(diff).toContainText('New: 8123/tcp from anywhere (IPv4 and IPv6) → ACCEPT');
    await expect(diff).toContainText('The order of the rules changed');
    await page.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    await page.locator('[data-notice="fw-pending"]').getByRole('button', {name: 'Revert now'}).click();
    await expect(page.locator('[data-notice="fw-notice"]')).toContainText('the change stays as a draft');
    // the draft is still there to apply again, the confirmed list unchanged
    await expect(page.getByRole('button', {name: 'Discard changes'})).toBeVisible();
    await page.getByRole('button', {name: 'Discard changes'}).click();
    await expect(page.locator('table.fw-rules tr[data-rule]')).toHaveCount(18);
});

// the network discovery (maintainer, 2026-09-18): what libfirewall allowed for finding and being
// found, in the list as owned rules - a destination, a source port, igmp without ports
test('the network discovery rules: multicast, SSDP and the discovery replies, editable', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const disc = page.locator('table.fw-rules tr[data-rule]:has([data-owner="discovery"])');
    // with the eQ-3 discovery (B-221) and mDNS from the local networks (B-219)
    await expect(disc).toHaveCount(10);
    await expect(page.locator('tr[data-rule="f1b2c3d8"]').getByLabel('Source port')).toHaveValue('5353');
    await expect(page.locator('tr[data-rule="f1b2c3d9"]').getByLabel('Source', {exact: true})).toHaveValue('local networks');
    const mcast = page.locator('tr[data-rule="f1b2c3d1"]');
    await expect(mcast.locator('[data-owner]')).toHaveText('Network discovery');
    await expect(mcast.getByLabel('Destination')).toHaveValue('224.0.0.0/24');
    await expect(mcast.getByLabel('Destination')).toBeDisabled();
    await expect(mcast.getByLabel('Port', {exact: true})).toHaveValue('');
    await expect(mcast.getByLabel('Family').locator('option[value="ipv6"]')).toBeDisabled();
    const igmp = page.locator('tr[data-rule="f1b2c3d2"]');
    await expect(igmp.getByLabel('Protocol')).toHaveValue('igmp');
    await expect(page.locator('tr[data-rule="f1b2c3d5"]').getByLabel('Source port')).toHaveValue('43439');
    // editable: the target of the SSDP rule, and a hand-made rule with a destination and a source port
    await page.locator('tr[data-rule="f1b2c3d4"]').getByLabel('Target').selectOption('DROP');
    await page.getByRole('button', {name: 'Add rule'}).click();
    const added = page.locator('table.fw-rules tr[data-rule]').last();
    await added.getByLabel('Protocol').selectOption('udp');
    await added.getByLabel('Source port').fill('5353');
    await added.getByLabel('Destination').fill('ff02::fb');
    await expect(added.getByLabel('Family')).toHaveValue('ipv6');
    const diff = page.locator('[data-diff]');
    await expect(diff).toContainText('Changed: 1900/udp from anywhere (IPv4 and IPv6) → DROP');
    await expect(diff).toContainText('New: udp from anywhere, source port 5353, destination ff02::fb (IPv6) → ACCEPT');
    // igmp has no ports
    await added.getByLabel('Protocol').selectOption('igmp');
    await expect(added.getByLabel('Source port')).toBeDisabled();
    await expect(added.getByLabel('Source port')).toHaveValue('');
});

// maintainer: sortable and filterable; the order of # stays the order the rules are checked in
test('the rules sorted and filtered: moving only in the order of #', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const rows = page.locator('table.fw-rules tr[data-rule]');
    await page.getByPlaceholder('Filter the rules').fill('hmipserver');
    await expect(rows).toHaveCount(3);
    await expect(page.getByText('3 of 18 rules shown')).toBeVisible();
    // no handle while the list is not in the order of # (task 162)
    await expect(rows.first().locator('button.ol-grip')).toHaveCount(0);
    await expect(page.locator('[data-notice="fw-order"]')).toBeVisible();
    // the # column still says where a rule is checked
    await expect(rows.first().locator('td').first()).toHaveText('4');
    await page.getByPlaceholder('Filter the rules').fill('');
    await page.locator('table.fw-rules th', {hasText: /^Port/}).click();
    await expect(rows.first()).toHaveAttribute('data-port', '0');
    await expect(rows.last()).toHaveAttribute('data-port', '43439');
    await page.locator('table.fw-rules th', {hasText: /^Port/}).click();
    await expect(rows.first()).toHaveAttribute('data-port', '43439');
    await page.locator('table.fw-rules th', {hasText: /^#/}).click();
    await expect(rows.first()).toHaveAttribute('data-port', '80');
    await expect(page.locator('[data-notice="fw-order"]')).toHaveCount(0);
});

// the source menu: local networks, anywhere (0/0) and every source another rule uses
test('the source menu offers the sources in use', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const r2001 = row(page, 2001);
    await r2001.getByLabel('Source', {exact: true}).fill('192.168.7.0/24');
    const r22 = row(page, 22);
    await r22.getByRole('button', {name: 'Choose a source'}).click();
    const menu = page.getByRole('menu');
    await expect(menu.getByRole('menuitem')).toHaveText(['local networks', 'anywhere', '192.168.7.0/24']);
    // as wide as its entries, not the window
    expect((await menu.boundingBox())!.width).toBeLessThan(260);
    await menu.getByRole('menuitem', {name: '192.168.7.0/24'}).click();
    await expect(menu).toHaveCount(0);
    await expect(r22.getByLabel('Source', {exact: true})).toHaveValue('192.168.7.0/24');
    await expect(r22.getByLabel('Family')).toHaveValue('ipv4');
    // anywhere stands for 0/0; typing 0/0 works too, and the file keeps 0/0
    await r22.getByRole('button', {name: 'Choose a source'}).click();
    await page.getByRole('menu').getByRole('menuitem', {name: 'anywhere'}).click();
    await expect(r22.getByLabel('Source', {exact: true})).toHaveValue('anywhere');
    await expect(r22.getByLabel('Family').locator('option[value="ipv6"]')).toBeEnabled();
    // Escape closes it
    await r2001.getByRole('button', {name: 'Choose a source'}).click();
    await expect(page.getByRole('menu')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('menu')).toHaveCount(0);
});

// task 168: a logged rule leads to its lines on the Log page - once it is loaded
test('a logged rule links to its lines in the kernel log', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const logged = row(page, 9294);
    await expect(row(page, 9293).getByRole('link', {name: 'lines'})).toHaveCount(0);
    // switched on in the draft only: no link yet
    await row(page, 9293).getByLabel('Log', {exact: true}).check();
    await expect(row(page, 9293).getByRole('link', {name: 'lines'})).toHaveCount(0);
    const a = logged.getByRole('link', {name: 'lines'});
    await expect(a).toHaveAttribute('href', '/system/log?source=kernel&q=fw%20c1b2c3d5');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.url().includes('/api/system/v1/log') && r.url().includes('q=fw')),
        a.click(),
    ]);
    expect(new URL(req.url()).searchParams.get('q')).toBe('fw c1b2c3d5');
    await expect(page.getByRole('searchbox', {name: 'Text filter'}).or(page.getByLabel('Text filter'))).toHaveValue('fw c1b2c3d5');
});

test('the policy ACCEPT asks first and warns', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const v6 = page.getByLabel('Policy IPv6');
    await v6.selectOption('ACCEPT');
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Accept everything not dropped?');
    await expect(dialog.getByRole('button', {name: 'Cancel'})).toBeFocused();
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(v6).toHaveValue('DROP');
    await expect(page.locator('[data-notice="fw-accept"]')).toHaveCount(0);
    await v6.selectOption('ACCEPT');
    await dialog.getByRole('button', {name: 'Set ACCEPT'}).click();
    await expect(v6).toHaveValue('ACCEPT');
    await expect(page.locator('[data-notice="fw-accept"]')).toHaveText('The policy is ACCEPT for IPv6: everything no rule drops is reachable.');
    await expect(page.locator('[data-diff]')).toContainText('Policy IPv6: DROP → ACCEPT');
});

test('the listeners: process, rule or policy per family, and Add ACCEPT rule', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const l = (key: string) => page.locator(`table.fw-listeners tr[data-listener="${key}"]`);
    await expect(l('tcp/0.0.0.0/80')).toContainText('lighttpd (412, lighttpd.service)');
    await expect(l('tcp/0.0.0.0/80').locator('.fw-cover')).toHaveText('IPv4: #1 ACCEPT from local networks · Web server');
    await expect(l('tcp/0.0.0.0/1883').locator('.fw-cover')).toHaveText('IPv4: #8 ACCEPT from anywhere · addon: mosquitto');
    // sshd on :: serves both families, and one rule covers both: one line
    await expect(l('tcp6/::/22').locator('.fw-cover div')).toHaveCount(1);
    await expect(l('tcp/127.0.0.1/8183').locator('.fw-cover')).toHaveText('loopback only');
    // B-219: an mDNS responder is reached from the local networks by the network discovery's rule
    await expect(l('udp/0.0.0.0/5353').locator('.fw-cover')).toHaveText('IPv4: #18 ACCEPT from local networks · Network discovery');
    const red = l('tcp/0.0.0.0/1880');
    await expect(red.locator('.fw-cover')).toHaveText('IPv4: (no rule, default: DROP)');
    await red.getByRole('button', {name: 'Add ACCEPT rule'}).click();
    const added = page.locator('table.fw-rules tr[data-rule]').last();
    await expect(added).toHaveAttribute('data-port', '1880');
    await expect(added.getByLabel('Protocol')).toHaveValue('tcp');
    await expect(added.getByLabel('Source', {exact: true})).toHaveValue('anywhere');
    await expect(page.locator('[data-diff]')).toContainText('New: 1880/tcp from anywhere (IPv4 and IPv6) → ACCEPT');
});

// task 169: the listeners sorted and filtered like the rules
test('the listeners sort and filter', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    const rows = page.locator('table.fw-listeners tbody tr[data-listener]');
    const total = await rows.count();
    // by port, ascending first
    await expect(rows.first()).toHaveAttribute('data-listener', /\/22$/);
    await page.locator('table.fw-listeners th', {hasText: /^Port/}).click();
    await expect(rows.last()).toHaveAttribute('data-listener', /\/22$/);
    // by process
    await page.locator('table.fw-listeners th', {hasText: /^Process/}).click();
    const procs = await rows.locator('td:nth-child(4)').allInnerTexts();
    const names = procs.map((p) => p.split(' ')[0]!);
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)));
    // a filter by unit
    await page.getByPlaceholder('Filter the listeners').fill('mosquitto');
    await expect(rows).not.toHaveCount(total);
    for (const r of await rows.all()) await expect(r).toContainText('mosquitto');
    await expect(page.getByText(/of \d+ listeners shown/)).toBeVisible();
});

// task 171: a range in the port field
test('a port range: typed, shown in the diff, sent as port and port_to; a bad one blocks Apply', async ({page, baseURL}) => {
    await own(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/api/system/v1/firewall')) puts.push(r.postData() ?? '');
    });
    await page.goto('/system/firewall');
    await page.getByRole('button', {name: 'Add rule'}).click();
    const added = page.locator('table.fw-rules tr[data-rule]').last();
    const port = added.getByLabel('Port', {exact: true});
    await port.fill('1890-1880');
    await expect(port).toHaveClass(/fw-invalid/);
    await expect(page.getByRole('button', {name: 'Apply'})).toBeDisabled();
    await expect(page.locator('[data-notice="fw-badport"]')).toBeVisible();
    await port.fill('1880-1890');
    await expect(port).not.toHaveClass(/fw-invalid/);
    await expect(page.locator('[data-diff]')).toContainText('New: 1880-1890/tcp from anywhere (IPv4 and IPv6) → ACCEPT');
    await page.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    await expect(page.locator('[data-notice="fw-pending"]')).toBeVisible();
    const sent = JSON.parse(puts[0]!);
    expect(sent.rules.at(-1)).toMatchObject({port: 1880, port_to: 1890, proto: 'tcp'});
});

// task 170: a rule switched off stays in the list, dimmed, and is sent as disabled
test('a rule switched off without deleting it', async ({page, baseURL}) => {
    await own(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/api/system/v1/firewall')) puts.push(r.postData() ?? '');
    });
    await page.goto('/system/firewall');
    const r22 = row(page, 22);
    await r22.getByLabel('Rule on').uncheck();
    await expect(r22).toHaveClass(/fw-off/);
    await expect(page.locator('[data-diff]')).toContainText('Switched off: 22/tcp from local networks (IPv4 and IPv6) → ACCEPT');
    await expect(page.locator('table.fw-rules tr[data-rule]')).toHaveCount(18);
    await page.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    await expect(page.locator('[data-notice="fw-pending"]')).toBeVisible();
    expect(JSON.parse(puts[0]!).rules.find((r: {port: number}) => r.port === 22)).toMatchObject({disabled: true, owner: 'ssh'});
    await page.locator('[data-notice="fw-pending"]').getByRole('button', {name: 'Confirm'}).click();
    await page.reload();
    await expect(row(page, 22)).toHaveClass(/fw-off/);
    await row(page, 22).getByLabel('Rule on').check();
    await expect(page.locator('[data-diff]')).toContainText('Switched on: 22/tcp');
});

// task 167: packets per rule and for the policy, sortable, and a reset
test('the hit counters: per rule, for the policy, sortable, reset', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/system/firewall');
    await expect(row(page, 2001).locator('td.fw-pkts')).toHaveText('2001');
    await expect(row(page, 2001).locator('td.fw-pkts')).toHaveAttribute('title', '20010 bytes');
    await expect(row(page, 43438).locator('td.fw-pkts')).toHaveText('43k');
    await expect(page.locator('[data-policy-pkts]')).toHaveText('IPv4 42 · IPv6 7 pkts');
    await page.locator('table.fw-rules th', {hasText: /^pkts/}).click();
    await page.locator('table.fw-rules th', {hasText: /^pkts/}).click();
    await expect(page.locator('table.fw-rules tr[data-rule]').first()).toHaveAttribute('data-port', '43439');
    await page.getByRole('button', {name: 'Reset counters'}).click();
    await expect(row(page, 2001).locator('td.fw-pkts')).toHaveText('0');
    await expect(page.locator('[data-counters-since]')).toContainText('pkts since');
});

test('a user reads the list and changes nothing', async ({page}) => {
    await page.route('**/api/auth/v1/state', (r) => r.fulfill({json: {setup_required: false, authenticated: true, user: 'monitor', role: 'user', must_change_password: false, sid: 'U1'}}));
    await page.goto('/system/firewall');
    await expect(page.locator('table.fw-rules tr[data-rule]')).toHaveCount(18);
    await expect(row(page, 2001).getByLabel('Source', {exact: true})).toBeDisabled();
    await expect(page.getByRole('button', {name: 'Apply'})).toHaveCount(0);
    await expect(page.getByRole('button', {name: 'Add ACCEPT rule'})).toHaveCount(0);
});

// D-47 on the Addons page since task 157: one switch per declared port, a PUT of the addon's open
// list, applied at once
test('an addon port opens with one switch on the Addons page', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.goto('/addons');
    await expect(page.getByRole('heading', {level: 2, name: 'Addon ports'})).toBeVisible();
    const table = page.locator('table.ol-addon-ports');
    const plain = table.locator('tbody tr', {hasText: '1883'});
    const tls = table.locator('tbody tr', {hasText: '8883'});
    await expect(plain).toContainText('Mosquitto');
    await expect(plain.getByRole('checkbox')).toBeChecked();
    await expect(tls).toContainText('MQTT over TLS');
    await expect(tls.getByRole('checkbox')).not.toBeChecked();
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/system/v1/firewall/addons/mosquitto/ports')),
        tls.getByRole('checkbox').check(),
    ]);
    expect(req.postDataJSON()).toEqual({open: [1883, 8883]});
    await expect(tls.getByRole('checkbox')).toBeChecked();
    await expect(page.getByText('Port 8883 of Mosquitto opened.')).toBeVisible();
    // openccu-lite task 105: a declared range is one switch, shown as the range, opened by its first port
    const range = table.locator('tbody tr', {hasText: '51800-51899'});
    await expect(range).toContainText('HomeKit bridges (HAP); every bridge one port in this range');
    await expect(range).toContainText('listening');
    const [req2] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/system/v1/firewall/addons/redmatic/ports')),
        range.getByRole('checkbox').check(),
    ]);
    expect(req2.postDataJSON()).toEqual({open: [51800]});
    await expect(page.getByText('Port 51800-51899 of RedMatic opened.')).toBeVisible();
    // the firewall shows the owned rules, the range as one rule
    await page.goto('/system/firewall');
    await expect(row(page, 8883).locator('[data-owner]')).toHaveText('addon: mosquitto');
    await expect(row(page, 51800).locator('[data-owner]')).toHaveText('addon: redmatic');
    await expect(row(page, 51800).getByLabel('Port', {exact: true})).toHaveValue('51800-51899');
});

// B-73: every column has its heading, and the switch is one control beside its state word
test('the addon ports have a heading over every column and the switch beside its state', async ({page}) => {
    await page.goto('/addons');
    const table = page.locator('table.ol-addon-ports');
    await expect(table.locator('thead th')).toHaveText(['Addon', 'Port', 'Protocol', 'Description', 'Listening', 'Access']);
    for (const r of await table.locator('tbody tr').all()) await expect(r.locator('td')).toHaveCount(6);
    const sw = table.locator('tbody tr', {hasText: '8883'}).locator('td').last().locator('label.ol-port-switch');
    await expect(sw).toHaveText('closed');
    const box = (await sw.getByRole('checkbox').boundingBox())!;
    const label = (await sw.boundingBox())!;
    expect(label.x + label.width - (box.x + box.width)).toBeLessThan(80);
});

// occulited B-31: a declared port held by another process says so, by name, and one whose
// socket's owner is unknown says that - in both languages
test('an addon port held by another process is named, not listening', async ({page}) => {
    const answer = [{id: 'openccu-loom', name: 'OpenCCU-Loom', mode: 'confined', ports: [
        {port: 5540, proto: 'udp', label: {de: 'Matter', en: 'Matter'}, listening: false, open: false, held_by: {process: 'node-red', unit: 'addon-redmatic.service'}},
        {port: 8088, proto: 'tcp', label: {de: 'Web', en: 'Web'}, listening: true, open: false},
        {port: 8089, proto: 'tcp', label: {de: 'API', en: 'API'}, listening: true, open: false, owner_unknown: true},
        {port: 8090, proto: 'tcp', label: {de: 'MCP', en: 'MCP'}, listening: false, open: false},
    ]}];
    await page.route('**/api/system/v1/firewall/addons', (r) => r.fulfill({json: answer}));
    await page.addInitScript(() => localStorage.setItem('ol.language', 'en'));
    await page.goto('/addons');
    const table = page.locator('table.ol-addon-ports');
    const cell = (port: number) => table.locator('tbody tr', {hasText: String(port)}).locator('td[data-listening]');
    await expect(cell(5540)).toHaveText('held by another process (node-red)');
    await expect(cell(5540)).toHaveAttribute('title', 'addon-redmatic.service');
    await expect(cell(5540).locator('.ol-dot')).toHaveClass(/warn/);
    await expect(cell(8088)).toHaveText('listening');
    await expect(cell(8089)).toHaveText('a socket is open (owner unknown)');
    await expect(cell(8090)).toHaveText('not listening');
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(cell(5540)).toHaveText('von einem anderen Prozess belegt (node-red)');
    await expect(cell(8089)).toHaveText('ein Socket ist offen (Besitzer unbekannt)');
    await expect(cell(8088)).toHaveText('lauscht');
});

// openccu-lite B-339: "local networks" is shown with what it stands for, and the IPv6 networks the
// user adds (another VLAN's /64, the delegated prefix) go with the draft, are applied and kept
test('the IPv6 local networks: the automatic list, the user\'s networks applied and kept', async ({page, baseURL}) => {
    await own(page, baseURL);
    const puts: string[] = [];
    page.on('request', (r) => {
        if (r.method() === 'PUT' && r.url().endsWith('/api/system/v1/firewall')) puts.push(r.postData() ?? '');
    });
    await page.goto('/system/firewall');
    const box = page.locator('[data-localv6]');
    await expect(box).toContainText('"local networks" stands for: 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10; ::1/128, fc00::/7, fe80::/10');
    const input = box.getByLabel('local networks (IPv6)');
    await expect(input).toHaveValue('');
    // an entry that is no IPv6 network holds Apply
    await input.fill('2001:db8:1::/56 nonsense');
    await expect(page.locator('[data-notice="fw-badlocal"]')).toBeVisible();
    await expect(page.getByRole('button', {name: 'Apply'})).toBeDisabled();
    await input.fill('2001:db8:1::/56, 2001:db8:2::/64');
    await expect(page.locator('[data-notice="fw-badlocal"]')).toHaveCount(0);
    await expect(page.locator('[data-diff]')).toContainText('local networks (IPv6): – → 2001:db8:1::/56, 2001:db8:2::/64');
    await page.getByRole('button', {name: 'Apply'}).click();
    await page.getByRole('dialog').getByRole('button', {name: 'Apply'}).click();
    const pending = page.locator('[data-notice="fw-pending"]');
    await expect(pending).toContainText('The new rules are loaded.');
    expect(JSON.parse(puts[0]!).local_ipv6).toEqual(['2001:db8:1::/56', '2001:db8:2::/64']);
    await pending.getByRole('button', {name: 'Confirm'}).click();
    await expect(page.locator('[data-notice="fw-notice"]')).toHaveText('The firewall rules are confirmed.');
    await page.reload();
    await expect(page.locator('[data-localv6]').getByLabel('local networks (IPv6)')).toHaveValue('2001:db8:1::/56 2001:db8:2::/64');
    await expect(page.locator('[data-localv6]')).toContainText('fe80::/10, 2001:db8:1::/56, 2001:db8:2::/64');
    await expect(page.getByRole('button', {name: 'Apply'})).toBeDisabled();
    // in German, the iptables words stay
    await page.evaluate(() => localStorage.setItem('ol.language', 'de'));
    await page.reload();
    await expect(page.locator('[data-localv6]')).toContainText('„local networks“ steht für:');
    await expect(page.locator('[data-localv6]')).toContainText('IPv6, zusätzlich');
});
