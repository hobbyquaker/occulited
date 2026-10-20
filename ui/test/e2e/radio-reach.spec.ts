import {type Page} from '@playwright/test';
import {expect, test} from './fixtures';

// Task 76's follow-up (D-64): when a page with the registered clients opens it asks the system once
// whether each subscriber's callback accepts a TCP connection (GET /radio/subscribers/reachability),
// and a row whose callback did not is marked "not reachable" - a hint, nothing acts on it. The stub's
// verdict: the remote mb_BidCos_RF does not answer, everything else does. refusedToo() makes the
// second of Homematic Manager's VirtualDevices callbacks refuse the connection as well. The clients
// on this system are the Interfaces page's, those on the network the Remote access page's while
// classic RPC is on (classicOn).

const REACH = '/api/system/v1/radio/subscribers/reachability';
const REMOVE = '**/api/system/v1/radio/subscribers/remove';

function countProbes(page: Page) {
    const seen: string[] = [];
    page.on('request', (r) => {
        if (new URL(r.url()).pathname === REACH) seen.push(r.method());
    });
    return seen;
}

async function classicOn(page: Page) {
    await page.route('**/api/system/v1/remote-access', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const body = (await response.json()) as {classic: {plain: boolean}};
        body.classic.plain = true;
        await route.fulfill({response, json: body});
    });
}

async function refusedToo(page: Page) {
    await page.route(`**${REACH}`, async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {subscribers: {url: string; reachable?: boolean; reason?: string}[]};
        Object.assign(body.subscribers.find((s) => s.url === 'xmlrpc://127.0.0.1:2141')!, {reachable: false, reason: 'refused'});
        await route.fulfill({response, json: body});
    });
}

test('a client on the network whose callback did not accept a connection is marked', async ({page}) => {
    const probes = countProbes(page);
    await classicOn(page);
    await page.goto('/system/remote-access');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    const gone = rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'});
    await expect(gone.locator('.ol-sub-reach')).toHaveText('not reachable');
    await expect(gone.locator('.ol-sub-reach')).toHaveAttribute('title', 'This system could not connect to the address when the page was opened (no answer within a second). The client may be gone, or a firewall is in the way. Nothing is changed because of it.');
    await expect(page.locator('.ol-sub-reach')).toHaveCount(1);
    expect(probes).toEqual(['GET']);
    // a card with the hint stays inside its track, on a phone too
    const overflow = await page.locator('.ol-process').evaluateAll((els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1).length);
    expect(overflow).toBe(0);
});

test('a client on this system whose callback refused is marked, the others are not', async ({page}) => {
    const probes = countProbes(page);
    await refusedToo(page);
    await page.goto('/system/interfaces');
    const vd = page.locator('.ol-process[data-interface="VirtualDevices"] li.ol-sub');
    await expect(vd.nth(1).locator('.ol-sub-reach')).toHaveAttribute('title', /\(connection refused\)/);
    await expect(vd.nth(0).locator('.ol-sub-reach')).toHaveCount(0);
    // the duplicate badge stays beside it
    await expect(vd.nth(1).locator('.ol-badge')).toHaveText(['internal', 'duplicate registration', 'not reachable']);
    // occulited task 84: the external client the stub's probe times out on is on this page too
    await expect(page.locator('.ol-sub-reach')).toHaveCount(2);
    await expect(page.locator('.ol-process[data-interface="VirtualDevices"] .ol-sub-reach')).toHaveCount(1);
    expect(probes).toEqual(['GET']);
    const overflow = await page.locator('.ol-process').evaluateAll((els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1).length);
    expect(overflow).toBe(0);
});

test('the removal dialog names the hint, and a removal does not probe again', async ({page}) => {
    const probes = countProbes(page);
    await classicOn(page);
    await page.route(REMOVE, (route) => route.fulfill({json: {removed: true, subscribers: [
        {id: '1007', url: 'xmlrpc_bin://127.0.0.1:31999', local: true},
        {id: 'BidCos-RF_java', url: 'http://127.0.0.1:39292/bidcos', local: true},
        {id: 'nr_Ab1Cd2_BidCos-RF', url: 'http://127.0.0.1:2048', local: true},
    ]}}));
    await page.goto('/system/remote-access');
    const rf = page.locator('.ol-process[data-interface="BidCos-RF"]');
    const row = rf.locator('li.ol-sub', {hasText: 'mb_BidCos_RF'});
    await expect(row.locator('.ol-sub-reach')).toBeVisible();
    await row.getByRole('button', {name: 'Remove subscription'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Not reachable when the page was opened (no answer within a second).');
    await dialog.getByRole('button', {name: 'Remove subscription'}).click();
    await expect(dialog).toBeHidden();
    await expect(row).toHaveCount(0);
    await expect(rf.locator('.ol-sub-count')).toHaveText('0 subscribers');
    expect(probes).toEqual(['GET']);

    // a reachable entry's dialog says nothing of the kind
    await page.goto('/system/interfaces');
    await page.locator('.ol-process[data-interface="BidCos-RF"] li.ol-sub', {hasText: 'nr_Ab1Cd2_BidCos-RF'}).getByRole('button', {name: 'Remove subscription'}).click();
    await expect(dialog).toBeVisible();
    await expect(dialog).not.toContainText('Not reachable');
    await page.keyboard.press('Escape');
});

test('a probe that fails leaves the page as it is', async ({page}) => {
    await page.route(`**${REACH}`, (route) => route.fulfill({status: 500, json: {error: 'internal', message: 'boom'}}));
    await page.goto('/system/interfaces');
    // occulited task 84: the external client is listed too
    await expect(page.locator('.ol-process[data-interface="BidCos-RF"] li.ol-sub:not(.ol-sub-stream)')).toHaveCount(4);
    await expect(page.locator('.ol-sub-reach')).toHaveCount(0);
    await expect(page.locator('.ol-subscribers')).not.toContainText('boom');
});

test('the hint in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await classicOn(page);
    await page.goto('/system/remote-access');
    const gone = page.locator('.ol-process[data-interface="BidCos-RF"] li.ol-sub', {hasText: 'mb_BidCos_RF'});
    await expect(gone.locator('.ol-sub-reach')).toHaveText('nicht erreichbar');
    await expect(gone.locator('.ol-sub-reach')).toHaveAttribute('title', 'Dieses System konnte beim Öffnen der Seite keine Verbindung zu der Adresse aufbauen (keine Antwort innerhalb einer Sekunde). Der Client ist womöglich nicht mehr da, oder eine Firewall ist im Weg. Deswegen wird nichts verändert.');
    await gone.getByRole('button', {name: 'Abonnement entfernen'}).click();
    await expect(page.getByRole('dialog')).toContainText('Beim Öffnen der Seite nicht erreichbar (keine Antwort innerhalb einer Sekunde).');
    await page.keyboard.press('Escape');
});
