import {expect, test} from './fixtures';
import type {Page, Request} from '@playwright/test';

// occulited B-47: the App's live event stream. Over plain http://<address>/ a page is no secure
// context: the browser sends no Sec-Fetch-Site, and no Origin on a same-origin GET, so the system
// cannot tell the App's EventSource from a request another origin made with the session cookie -
// it refused the stream ("a browser session refused" in the journal at every page open) and the
// tiles never followed an event. The App reads the stream with fetch() now, with the header
// credential a page of another origin cannot send.
//
// 127.0.0.1 is a secure context, as HTTPS is; the stub under another name is not. The names are
// mapped in the browser itself, so nothing here asks a name server.
test.use({launchOptions: {args: ['--host-resolver-rules=MAP occulite.test 127.0.0.1, MAP elsewhere.test 127.0.0.1']}});

const STREAM = '/api/rpc/v1/events';
const LAMP = 'HmIP-RF.000DD8:3';
const isStream = (r: Request) => new URL(r.url()).pathname === STREAM;
const lanOf = (baseURL: string) => `http://occulite.test:${new URL(baseURL).port}`;
const lampState = (page: Page) => page.locator(`[data-app-tile="${LAMP}"] [data-app-state]`);

/** A cookie stub-app of this test's own, for the page's origin: the stub keeps its values and streams apart by it. */
async function ownStub(page: Page, origin: string, more: Record<string, string> = {}): Promise<string> {
    const app = `stream-${Math.random().toString(36).slice(2)}`;
    await page.context().addCookies(Object.entries({'stub-app': app, ...more}).map(([name, value]) => ({name, value, url: origin})));
    return app;
}

/** Another client's setValue: the stub sends it on as the interface's event to this test's streams. */
async function switchFromElsewhere(page: Page, baseURL: string, app: string, level: number) {
    const r = await page.request.post(`${baseURL}/api/rpc/v1/json/HmIP-RF`, {
        headers: {'X-Occulite-Request': '1', Cookie: `stub-app=${app}`},
        data: [{jsonrpc: '2.0', method: 'setValue', params: ['000DD8:3', 'LEVEL', level], id: 1}],
    });
    expect(r.status()).toBe(200);
}

for (const where of ['plain http, no secure context', 'a secure context'] as const) {
    test(`${where}: the stream opens and a tile follows an event`, async ({page, baseURL}) => {
        const secure = where === 'a secure context';
        const origin = secure ? baseURL! : lanOf(baseURL!);
        const app = await ownStub(page, origin);
        const calls: string[] = [];
        page.on('request', (r) => r.method() === 'POST' && r.url().includes('/api/rpc/v1/json/') && calls.push(r.postData() ?? ''));
        const [req, res] = await Promise.all([page.waitForRequest(isStream), page.waitForResponse((r) => isStream(r.request())), page.goto(`${origin}/app/e/rooms/og/bad`)]);
        expect(await page.evaluate(() => window.isSecureContext)).toBe(secure);

        // what the browser says about the request's origin, and what the App adds
        const h = await req.allHeaders();
        expect(h['sec-fetch-site']).toBe(secure ? 'same-origin' : undefined);
        expect(h.origin).toBeUndefined();
        expect(h['x-occulite-request']).toBe('1');
        expect(h.authorization).toBe('Bearer A1b2C3d4E5');
        expect(req.resourceType()).toBe('fetch');
        // no credential in the address: it would be in every log on the way
        expect(new URL(req.url()).search).toBe('?interface=BidCos-RF&interface=HmIP-RF&type=event,state');
        expect(res.status()).toBe(200);

        // the first values are the state store's: no interface process is asked for them
        await expect(lampState(page)).toHaveText('On · 60 %');
        expect(calls.filter((c) => c.includes('getParamset"'))).toEqual([]);
        // and the stream starts where that read was
        expect(h['last-event-id']).toMatch(/^stub-\d+$/);

        await switchFromElsewhere(page, baseURL!, app, 0.25);
        await expect(lampState(page)).toHaveText('On · 25 %');
        await switchFromElsewhere(page, baseURL!, app, 0);
        await expect(lampState(page)).toHaveText('Off');
    });
}

test('the public mode over plain http: no session to send, the header credential opens the stream', async ({page, baseURL}) => {
    const origin = lanOf(baseURL!);
    const app = await ownStub(page, origin, {'stub-public': '1'});
    const [req, res] = await Promise.all([page.waitForRequest(isStream), page.waitForResponse((r) => isStream(r.request())), page.goto(`${origin}/app/e/rooms/og/bad`)]);
    const h = await req.allHeaders();
    expect(h.authorization).toBeUndefined();
    expect(h['sec-fetch-site']).toBeUndefined();
    expect(h['x-occulite-request']).toBe('1');
    expect(res.status()).toBe(200);
    await expect(lampState(page)).toHaveText('On · 60 %');
    await switchFromElsewhere(page, baseURL!, app, 0.25);
    await expect(lampState(page)).toHaveText('On · 25 %');
});

test('what the rule still refuses over plain http: an EventSource, and every way in from another origin', async ({page, baseURL}) => {
    const origin = lanOf(baseURL!);
    const probe = `probe-${Math.random().toString(36).slice(2)}`;
    const target = `${STREAM}?probe=${probe}`;
    const eventSource = (url: string, withCredentials: boolean) =>
        page.evaluate(
            ([u, c]) =>
                new Promise<string>((resolve) => {
                    const es = new EventSource(u as string, {withCredentials: c as boolean});
                    es.onopen = () => resolve('open');
                    es.onerror = () => resolve(`error, readyState ${es.readyState}`);
                }),
            [url, withCredentials],
        );

    // this system's own page, but the cookie alone: an EventSource can set no header
    await page.goto(`${origin}/app`);
    expect(await eventSource(`${target}&how=own-eventsource`, false)).toBe('error, readyState 2'); // closed for good: the answer was no stream

    // a page of another origin: the same stub under another name, so the requests are cross-site -
    // its addon stand-in, which carries no Content-Security-Policy that would stop them in the page
    await page.goto(`http://elsewhere.test:${new URL(baseURL!).port}/addons/elsewhere/`);
    expect(await eventSource(`${origin}${target}&how=eventsource`, true)).toBe('error, readyState 2');
    const out = await page.evaluate(async (url) => {
        const tried = (how: string, init: RequestInit) =>
            fetch(`${url}&how=${how}`, {credentials: 'include', ...init}).then(
                (r) => (r.type === 'opaque' ? 'unreadable' : `answered ${r.status}`),
                () => 'failed',
            );
        return {
            // a header of the page's own from elsewhere: the browser asks first (a preflight), and nothing answers that
            header: await tried('header', {headers: {'X-Occulite-Request': '1'}}),
            bearer: await tried('bearer', {headers: {Authorization: 'Bearer A1b2C3d4E5'}}),
            // without one the request goes out, saying nothing
            plain: await tried('no-cors', {mode: 'no-cors'}),
        };
    }, `${origin}${target}`);
    expect(out.header).toBe('failed');
    expect(out.bearer).toBe('failed');
    expect(out.plain).not.toMatch(/^answered/);

    // what reached the system: the three without a header, each refused; the two with one never left the browser
    const reached = (await (await page.request.get(`${baseURL}/__stub/lite-probes?probe=${probe}`)).json()) as {how: string; refused: boolean; header: boolean}[];
    expect(reached.sort((a, b) => a.how.localeCompare(b.how))).toEqual([
        {how: 'eventsource', refused: true, header: false},
        {how: 'no-cors', refused: true, header: false},
        {how: 'own-eventsource', refused: true, header: false},
    ]);
});

test('a stream that ends is opened again where it stopped; a resync reads the values again', async ({page, baseURL}) => {
    await ownStub(page, baseURL!);
    const resumed: (string | undefined)[] = [];
    const sse = (body: string) => ({status: 200, headers: {'Content-Type': 'text/event-stream'}, body});
    await page.route(`**${STREAM}*`, async (route) => {
        resumed.push((await route.request().allHeaders())['last-event-id']);
        // the first stream brings one event and ends, as at a restart of occulited
        if (resumed.length === 1) {
            return route.fulfill(sse(`id: boot-7\nevent: hello\ndata: {}\n\nid: boot-8\nevent: event\ndata: ${JSON.stringify({interface: 'HmIP-RF', address: '000DD8:3', key: 'LEVEL', value: 0.25})}\n\n`));
        }
        // the second says the system could not replay what was missed
        if (resumed.length === 2) return route.fulfill(sse('id: boot-9\nevent: hello\ndata: {}\n\nevent: resync\ndata: {"reason":"gap"}\n\n'));
        return route.continue();
    });
    let stateReads = 0;
    page.on('request', (r) => new URL(r.url()).pathname === '/api/rpc/v1/state' && stateReads++);
    await page.goto('/app/e/rooms/og/bad');
    await expect(lampState(page)).toHaveText('On · 25 %');
    expect(stateReads).toBe(1);
    // the resync: the values are the state store's again (the stub's lamp is at 60 %)
    await expect(lampState(page)).toHaveText('On · 60 %');
    expect(stateReads).toBe(2);
    await expect.poll(() => resumed.length).toBe(3);
    expect(resumed[0]).toMatch(/^stub-\d+$/); // from the first read of the values
    expect(resumed.slice(1)).toEqual(['boot-8', 'boot-9']);
});

test('the 30 s refresh of the tree neither reads the values again nor opens the stream anew', async ({page}) => {
    await page.clock.install();
    const seen: string[] = [];
    page.on('request', (r) => {
        const p = new URL(r.url()).pathname;
        if (p.startsWith('/api/rpc/v1/')) seen.push(`${r.method()} ${p}${(r.postData() ?? '').includes('getParamset"') ? ' getParamset' : ''}`);
    });
    await Promise.all([page.waitForResponse((r) => isStream(r.request())), page.goto('/app/e/rooms/og/bad')]);
    await expect(lampState(page)).toHaveText('On · 60 %');
    const atOpen = [...seen];
    expect(atOpen.filter((s) => s === `GET ${STREAM}`)).toHaveLength(1);
    expect(atOpen.filter((s) => s === 'GET /api/rpc/v1/state')).toHaveLength(1);
    // one refresh (the page's clock alone runs: the stub's pings do not, so not past the stream's 45 s of silence)
    await Promise.all([page.waitForResponse((r) => r.url().endsWith('/api/meta/v1/snapshot')), page.clock.runFor(30_000)]);
    await expect(lampState(page)).toHaveText('On · 60 %');
    await page.clock.runFor(5_000);
    expect(seen).toEqual(atOpen);
});

test('a system without the state store: the values are read from the interface processes, as before', async ({page}) => {
    await page.route('**/api/rpc/v1/state*', (r) => r.fulfill({status: 501, json: {error: 'unsupported', message: 'the state store is not available on this system'}}));
    const [read] = await Promise.all([
        page.waitForRequest((r) => r.url().endsWith('/api/rpc/v1/json/HmIP-RF') && (r.postData() ?? '').includes('getParamset"')),
        page.goto('/app/e/rooms/og/bad'),
    ]);
    expect((read.postDataJSON() as {params: unknown[]}[]).map((c) => c.params[0])).toEqual(['000DD8:3', '000ABC:1']);
    await expect(lampState(page)).toHaveText('On · 60 %');
    await expect(page.locator('[data-app-tile="HmIP-RF.000ABC:1"] [data-app-state]')).toHaveText('set 21.5 °C · 20.2 °C');
});

// occulited task 19: while the App's stream is not open its title says so, quietly and with the
// reason - another window holds the account's streams (429), or the connection is made again - and
// the mark goes when the stream is live again. A reconnect quicker than two seconds says nothing.
const notLive = (page: Page) => page.locator('[data-app-title] [data-app-notlive]');

test('a window over the account\'s stream limit says "not live: too many windows" until a stream is free', async ({page, baseURL}) => {
    const app = await ownStub(page, baseURL!);
    let busy = true;
    let tries = 0;
    await page.route(`**${STREAM}*`, (route) => {
        tries++;
        return busy ? route.fulfill({status: 429, json: {error: 'too-many-streams', message: 'too many streams: 3 per token or session'}}) : route.continue();
    });
    await page.goto('/app/e/rooms/og/bad');
    await expect(lampState(page)).toHaveText('On · 60 %');
    await expect(notLive(page)).toHaveText('not live: too many windows');
    await expect(notLive(page)).toHaveAttribute('data-app-notlive', 'busy');
    await expect(notLive(page)).toHaveAttribute('title', /as many live connections open as the system allows/);
    // the window keeps trying; another one closes
    const before = tries;
    busy = false;
    await expect(notLive(page)).toHaveCount(0, {timeout: 20_000});
    expect(tries).toBeGreaterThan(before);
    await switchFromElsewhere(page, baseURL!, app, 0.25);
    await expect(lampState(page)).toHaveText('On · 25 %');
});

test('in German: a stream that ends and does not come back at once says "nicht live: verbindet neu"', async ({page, baseURL}) => {
    await ownStub(page, baseURL!);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    let down = false;
    let streams = 0;
    await page.route(`**${STREAM}*`, (route) => {
        if (down) return route.fulfill({status: 503, body: 'occulited restarts'});
        streams++;
        // the first stream is open for a moment and ends, as at a restart of occulited
        if (streams === 1) {
            down = true;
            return route.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: 'id: boot-1\nevent: hello\ndata: {}\n\n'});
        }
        return route.continue();
    });
    await page.goto('/app/e/rooms/og/bad');
    await expect(notLive(page)).toHaveText('nicht live: verbindet neu');
    await expect(notLive(page)).toHaveAttribute('data-app-notlive', 'reconnecting');
    down = false;
    await expect(notLive(page)).toHaveCount(0, {timeout: 20_000});
    expect(streams).toBe(2);
});

test('a stream that is live says nothing, and a reconnect within two seconds neither', async ({page, baseURL}) => {
    await ownStub(page, baseURL!);
    let streams = 0;
    await page.route(`**${STREAM}*`, (route) => {
        streams++;
        if (streams === 1) return route.fulfill({status: 200, headers: {'Content-Type': 'text/event-stream'}, body: 'id: boot-1\nevent: hello\ndata: {}\n\n'});
        return route.continue();
    });
    await page.goto('/app/e/rooms/og/bad');
    await expect.poll(() => streams).toBe(2); // the end, and the stream again a second later
    await page.waitForTimeout(2500);
    await expect(notLive(page)).toHaveCount(0);
});

// occulited B-53: the App's stream is the shown App's alone - not while another page of the shell is
// shown (the pages stay mounted) or the window is hidden, after a grace of ten seconds; back on the
// App the values are read again and the stream opened from there.
test('the stream closes a while after another page of the shell is shown, and opens again on the return', async ({page, baseURL, isMobile}) => {
    test.skip(!!isMobile, 'the tabs are in the menu on a phone');
    const app = await ownStub(page, baseURL!);
    await page.clock.install();
    const opened: Request[] = [];
    const ended: Request[] = [];
    page.on('request', (r) => isStream(r) && opened.push(r));
    page.on('requestfailed', (r) => isStream(r) && ended.push(r));
    page.on('requestfinished', (r) => isStream(r) && ended.push(r));
    await page.goto('/app/e/rooms/og/bad');
    await expect(lampState(page)).toHaveText('On · 60 %');
    await expect.poll(() => opened.length).toBe(1);
    await page.locator('.ol-nav > a[href="/"]').click();
    await expect(page).toHaveURL(/\/$/);
    // a quick look elsewhere keeps it
    await page.clock.runFor(5_000);
    expect(ended).toHaveLength(0);
    await page.clock.runFor(6_000);
    await expect.poll(() => ended.length).toBe(1);
    // back: the stream again, live
    await page.locator('.ol-nav > a[href="/app"]').click();
    await expect.poll(() => opened.length).toBe(2);
    await expect(notLive(page)).toHaveCount(0);
    await switchFromElsewhere(page, baseURL!, app, 0.3);
    await expect(lampState(page)).toHaveText('On · 30 %');
});
