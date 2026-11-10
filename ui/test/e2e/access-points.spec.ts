import {expect, test} from '@playwright/test';

// openccu-lite task 217 (the maintainer, 2026-09-24): "a list of paired access points in occulited,
// on the Interfaces page beside the HmIP interface: each HAP/DRAP with name, type, SGTIN, firmware
// (and an available update), reachability, duty cycle; and the firewall state of UDP 43438 /
// TCP 9293/9294 next to it". The hint's cases are task 181's (D-110), answered by the API.

const section = (page: import('@playwright/test').Page) => page.locator('h2#access-points');

test('the access points stand between the gateways and the LAN devices, one card each', async ({page}) => {
    await page.goto('/system/lan-devices');
    await expect(page.locator('main h2')).toHaveText(['BidCoS Gateways', 'Network radio board (HB-RF-ETH)', 'HmIP Access Points', 'LAN devices']);
    await expect(section(page)).toBeVisible();

    const hap = page.locator('.ol-card[data-access-point="00030000000A13"]');
    await expect(hap.locator('.ol-card-title')).toHaveText('Access point cellar');
    await expect(hap.locator('.ol-card-sub')).toHaveText('HmIP-RF · HmIP-HAP-B1');
    await expect(hap.locator('dt')).toHaveText(['State', 'SGTIN', 'Address', 'IP address', 'Firmware', 'Duty cycle', 'Carrier sense']);
    await expect(hap.locator('dd').nth(0)).toHaveText('reachable');
    await expect(hap.locator('dd').nth(1)).toHaveText('30150377DC00030000000A13');
    // task 220: the mode from the LAN find beside the address
    await expect(hap.locator('dd').nth(3)).toHaveText('192.0.2.155DHCP');
    // B-195's data: eQ-3 lists a newer version, the badge leads to the device firmware
    const badge = hap.locator('[data-ap-firmware] a');
    await expect(badge).toHaveText('update available: 2.2.20');
    await expect(badge).toHaveAttribute('href', '/system/updates#device-firmware');
    await expect(hap.locator('[data-ap-duty]')).toHaveText('2.5 %');

    // the wired DRAP has no radio budget, and no name: its type is the title
    const drap = page.locator('.ol-card[data-access-point="00170000000A08"]');
    await expect(drap.locator('.ol-card-title')).toHaveText('HmIPW-DRAP');
    await expect(drap.locator('dt')).toHaveText(['State', 'SGTIN', 'Address', 'IP address', 'Firmware']);
    await expect(drap.locator('[data-ap-firmware] a')).toHaveCount(0);

    // the firewall: the three ports, accepted, and "keep them"
    const fw = page.locator('[data-ap-hint]');
    await expect(fw).toHaveAttribute('data-ap-hint', 'keep');
    // B-134: every source a port is accepted from, as on the Remote access page
    await expect(fw.locator('.fw-port')).toHaveText(['tcp 9293 ACCEPT from anywhere', 'tcp 9294 ACCEPT from anywhere', 'udp 43438 ACCEPT from 192.0.2.0/24, anywhere']);
    await expect(fw).toContainText('An access point is paired: keep these rules on ACCEPT.');
    await expect(fw).not.toHaveClass(/error/);
    await expect(fw.getByRole('link', {name: 'Firewall'})).toHaveAttribute('href', '/system/firewall');
});

for (const [mode, hint, text, error] of [
    ['blocked', 'blocked', 'A port the access points need is not accepted: they cannot reach the system.', true],
    ['none', 'unused', 'No HmIP access point is paired: these rules can be set to REJECT on the Firewall page.', false],
    ['reopen', 'reopen', 'Set these rules to ACCEPT again before pairing an HmIP access point', false],
    ['silent', 'unknown', 'HmIP-RF does not answer, so whether an access point is paired is not known.', false],
] as const) {
    test(`the firewall hint: ${hint}`, async ({page, context, baseURL}) => {
        await context.addCookies([{name: 'stub-aps', value: mode, url: baseURL!}]);
        await page.goto('/system/lan-devices');
        const fw = page.locator('[data-ap-hint]');
        await expect(fw).toHaveAttribute('data-ap-hint', hint);
        await expect(fw).toContainText(text);
        if (error) await expect(fw).toHaveClass(/error/);
        else await expect(fw).not.toHaveClass(/error/);
        if (mode === 'blocked') {
            await expect(fw.locator('.fw-port[data-port="9294"]')).toHaveText('tcp 9294 REJECT from anywhere');
            await expect(page.locator('.ol-card[data-access-point="00170000000A08"] [data-ap-state]')).toHaveText('unreachableconfiguration pending');
        }
        if (mode === 'none' || mode === 'reopen') await expect(page.locator('[data-ap-none]')).toHaveText('No HmIP access point is paired.');
        if (mode === 'silent') await expect(page.locator('[data-ap-error]')).toContainText('HmIP-RF does not answer: dial tcp');
    });
}

test('without HmIP-RF there is no access point section', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-aps', value: 'off', url: baseURL!}]);
    await page.goto('/system/lan-devices');
    await expect(page.locator('h2#gateways')).toBeVisible();
    await expect(page.locator('h2#access-points')).toHaveCount(0);
});

test('in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/lan-devices');
    await expect(page.locator('h2#access-points')).toHaveText('HmIP-Access-Points');
    const hap = page.locator('.ol-card[data-access-point="00030000000A13"]');
    await expect(hap.locator('dt')).toHaveText(['Zustand', 'SGTIN', 'Adresse', 'IP-Adresse', 'Firmware', 'Duty Cycle', 'Carrier Sense']);
    await expect(hap.locator('dd').nth(0)).toHaveText('erreichbar');
    // iptables' words stay as iptables says them
    await expect(page.locator('[data-ap-hint]')).toContainText('Ein Access Point ist angelernt: diese Regeln auf ACCEPT lassen.');
    await expect(page.locator('.fw-port').first()).toHaveText('tcp 9293 ACCEPT von anywhere');
});
