import {expect, test} from './fixtures';
import {fitsWindow, pageWidth} from './scroll';

// task 53: the Status page - the duty cycle graph with its time axis and top tick, the cards of
// one row at one height, the code under a component's name, the addon update count, and every
// warning with a working link to the page where it is handled (planted by the stub-warn cookie)

test('the duty cycle graph shows its start and end time, its top tick, the gap and a sample on hover', async ({page}) => {
    await page.goto('/');
    const gauge = page.locator('.ol-gauge', {hasText: 'Duty cycle'});
    const chart = gauge.locator('.chart');
    await expect(chart.locator('svg')).toBeVisible();
    // occulited task 81: every chart opens on 24 h; the last hour is one click away
    await expect(chart.getByRole('button', {name: '24 h'})).toHaveAttribute('aria-pressed', 'true');
    await expect(gauge.locator('.peaktext')).toHaveText('max 45 % (24 h)');
    await chart.getByRole('button', {name: '1 h'}).click();
    // the times the chart must print, from the stub's own samples, in the browser's zone
    const want = await page.evaluate(async () => {
        const h = (await (await fetch('/api/system/v1/radio/health')).json()) as {history: Record<string, {t: string}[]>};
        const s = h.history['HmIP-RF/BC0A08']!;
        const last = Date.parse(s[s.length - 1]!.t);
        const f = (ms: number) => new Date(ms).toLocaleTimeString('en', {hour: '2-digit', minute: '2-digit', hourCycle: 'h23'});
        return {start: f(last - 3600_000), end: f(last), first: f(Date.parse(s[0]!.t))};
    });
    // (ends-with: across midnight the day stands in front of the time)
    await expect(chart.locator('.time.start')).toHaveText(new RegExp(`${want.start}$`));
    await expect(chart.locator('.time.end')).toHaveText(new RegExp(`${want.end}$`));
    // the last hour peaks at 21 %: the top is the next readable step, and its tick says so
    await expect(chart.locator('text.tick')).toHaveText('50 %');
    // task 152: the span's maximum beside the current value, in words and as the lighter arc
    await expect(gauge.locator('.peaktext')).toHaveText('max 21 % (1 h)');
    await expect(gauge.locator('circle.peak')).toHaveCount(1);
    // both stacks down for two polls: two runs of line, not one ramp across the gap
    await expect(chart.locator('polyline.line')).toHaveCount(2);
    // a sample's time and value under the pointer (a tap on the phone)
    const box = (await chart.locator('svg').boundingBox())!;
    await chart.locator('svg').click({position: {x: box.width - 1, y: box.height / 2}});
    await expect(chart.locator('text.value')).toHaveText(new RegExp(`^${want.end} · \\d+ %$`));
    // the span selector: 6 h reaches back to the first sample the stub keeps
    await chart.getByRole('button', {name: '6 h'}).click();
    await expect(chart.locator('.time.start')).toHaveText(new RegExp(`${want.first}$`));
    // the maximum follows the span: the 45 % burst lies outside the last hour
    await expect(gauge.locator('.peaktext')).toHaveText('max 45 % (6 h)');
});

test('the cards of a row share its height, the code sits under the name, nothing overflows', async ({page}) => {
    await page.goto('/');
    // the Addons card counts RedMatic (a catalogue update and its own check) once, and hm2mqtt
    await expect(page.locator('[data-component="addons"]')).toContainText('2 updates');
    await expect(page.locator('[data-component="certificate"]')).toBeVisible();
    await expect(page.locator('.ol-gauge', {hasText: 'Duty cycle'}).locator('svg[role="img"]')).toBeVisible();
    for (const grid of ['.ol-gauges', '.ol-components']) {
        const boxes = await page.locator(`${grid} > .ol-card`).evaluateAll((els) => els.map((e) => {
            const r = e.getBoundingClientRect();
            return {top: Math.round(r.top), height: Math.round(r.height)};
        }));
        expect(boxes.length).toBeGreaterThan(1);
        const rows = new Map<number, number[]>();
        for (const b of boxes) rows.set(b.top, [...(rows.get(b.top) ?? []), b.height]);
        for (const hs of rows.values()) expect(new Set(hs).size, `${grid}: ${hs.join(', ')}`).toBe(1);
    }
    for (const name of ['BidCos-RF', 'HmIP-RF']) {
        const card = page.locator(`.ol-components [data-component="${name}"]`);
        const title = (await card.locator('.ol-card-title').boundingBox())!;
        const sub = (await card.locator('.ol-card-sub').boundingBox())!;
        expect(sub.y, `${name}: the code under the name`).toBeGreaterThanOrEqual(title.y + title.height - 1);
    }
    expect(await fitsWindow(page)).toBe(true);
});

// B-69: the addon warnings name each addon by its name and say "disabled" once - they said
// "97NeoServer (disabled). They were disabled."
test('the addon warnings name the addons and say disabled once', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-warn', value: '1', url: baseURL!}]);
    await page.goto('/');
    const rega = page.locator('[data-notice="rega"] .ol-notice-text');
    await expect(rega).toHaveText('Disabled incompatible Addon: Print. Installed, not usable without ReGa, and still enabled: E-Mail.');
    const arch = page.locator('[data-notice="arch"] .ol-notice-text');
    await expect(arch).toHaveText('Built for another architecture, and therefore disabled: CUxD. The repair is a release built for this system.');
    for (const notice of [rega, arch]) {
        const text = (await notice.textContent()) ?? '';
        expect(text.match(/disabled/g)?.length ?? 0, text).toBeLessThanOrEqual(1);
        expect(text).not.toContain('(');
    }
});

// B-66: the zone name, as the Network page shows it; the POSIX string only behind the pointer
test('the timezone is the zone name, with the POSIX string as its tooltip', async ({page}) => {
    await page.goto('/');
    const tz = page.locator('.ol-hero .fig', {hasText: 'Timezone'}).locator('.fv');
    await expect(tz).toHaveText('Europe/Berlin');
    await expect(tz).toHaveAttribute('title', 'CET-1CEST-2,M3.5.0/02:00:00,M10.5.0/03:00:00');
});

test('at 360 px nothing on the Status, Interfaces and Catalogue pages runs out sideways', async ({page}) => {
    await page.setViewportSize({width: 360, height: 780});
    for (const [path, ready] of [['/', '.ol-gauge'], ['/radio', '[data-process="multimacd"]'], ['/addons', '.ad-card']] as const) {
        await page.goto(path);
        await expect(page.locator(ready).first()).toBeVisible();
        expect(await pageWidth(page), path).toBeLessThanOrEqual(360);
    }
});

const WARNINGS: {id: string; link: string; path: string; heading: string; planted: boolean; search?: RegExp}[] = [
    {id: 'rega', link: 'Addons', path: '/addons', heading: 'Addons', planted: true},
    // task 139: the catalogue is the Addons page; the box's /catalog href is an alias of it
    {id: 'arch', link: 'Addons', path: '/addons', heading: 'Addons', planted: true},
    {id: 'meta', link: 'Control', path: '/app', heading: 'Favorites', planted: true},
    {id: 'unclean', link: 'Log', path: '/system/log', heading: 'Log', planted: true, search: /\?boot=-1$/},
    {id: 'backup-target', link: 'Backup', path: '/system/backup', heading: 'Backup', planted: true},
    {id: 'certificate', link: 'Certificate', path: '/system/certificates', heading: 'Certificate', planted: true},
    // the stub's default schedule is the backup on the box itself
    {id: 'backup-userfs', link: 'Backup', path: '/system/backup', heading: 'Backup', planted: false},
];

for (const w of WARNINGS) {
    test(`the ${w.id} warning links to ${w.path}`, async ({page, context, baseURL}) => {
        if (w.planted) await context.addCookies([{name: 'stub-warn', value: '1', url: baseURL!}]);
        await page.goto('/');
        const notice = page.locator(`[data-notice="${w.id}"]`);
        await expect(notice).toBeVisible();
        // the sentence says what is wrong and names no page: the button does
        await expect(notice.locator('.ol-notice-text')).not.toContainText(/\bpage\b/);
        await notice.getByRole('link', {name: w.link, exact: true}).click();
        await expect(page).toHaveURL(new RegExp(`${w.path}(\\?|$)`));
        if (w.search) await expect(page).toHaveURL(w.search);
        await expect(page.getByRole('heading', {level: 1, name: w.heading})).toBeVisible();
    });
}

// occulited task 47 (after tasks 133 and 16): every round tags occulited and the OpenCCU-Base fork
// with the openccu-lite version (D-121), so the system card names openccu-lite and the product only;
// a system that is not openccu-lite keeps occulited's commit (shortened, the full one as tooltip)
// and its own version
test('the system card names openccu-lite and the product, not occulited and OpenCCU', async ({page}) => {
    await page.goto('/');
    const ver = page.locator('.ol-hero .ver');
    await expect(ver).toHaveText('openccu-lite 0-beta.2 · ova');
    await expect(ver.locator('span[title]')).toHaveCount(0);
});

test('a system that is not openccu-lite names occulited and its version', async ({page}) => {
    await page.route('**/api/system/v1/status', async (route) => {
        const response = await route.fetch();
        const j = await response.json();
        delete j.version.lite;
        await route.fulfill({response, json: j});
    });
    await page.goto('/');
    const ver = page.locator('.ol-hero .ver');
    await expect(ver).toHaveText('occulited 1df08bb01-hot · 3.89.8.20260719 · ova');
    await expect(ver.locator('span[title]')).toHaveAttribute('title', '1df08bb0101038ac6eb7c08c3f3144a8a91840a1-hot');
});

// task 203 (the maintainer, 2026-09-22): a warning is a panel like the rest of the UI, its severity
// the card's 4 px left edge - not a sunken box with a coloured line all the way round.
test('the Status warnings are panels with the 4 px left edge of their severity', async ({page, context, baseURL}) => {
    await context.addCookies([
        {name: 'stub-warn', value: '1', url: baseURL!},
        {name: 'stub-key', value: 'default', url: baseURL!},
    ]);
    await page.goto('/');
    for (const [severity, token] of [
        ['error', '--hmm-error'],
        ['warning', '--hmm-warn'],
    ] as const) {
        const notice = page.locator(`.ol-warnings [data-severity="${severity}"]`).first();
        await expect(notice).toBeVisible();
        await expect(notice).toHaveClass(/\bol-panel\b/);
        const box = await notice.evaluate((el, tok) => {
            const cs = getComputedStyle(el);
            // the token's own value, resolved the way the browser resolves the border colour
            const probe = document.createElement('div');
            probe.style.color = cs.getPropertyValue(tok).trim();
            el.append(probe);
            const want = getComputedStyle(probe).color;
            probe.remove();
            return {
                left: cs.borderLeftWidth,
                top: cs.borderTopWidth,
                right: cs.borderRightWidth,
                bottom: cs.borderBottomWidth,
                colour: cs.borderLeftColor,
                want,
                bg: cs.backgroundColor,
                card: getComputedStyle(document.documentElement).getPropertyValue('--hmm-card-bg').trim(),
                // the text starts where a panel without an edge has it: 1 px hairline + 16 px padding
                inset: Number.parseFloat(cs.borderLeftWidth) + Number.parseFloat(cs.paddingLeft),
            };
        }, token);
        expect(box.left, severity).toBe('4px');
        expect([box.top, box.right, box.bottom], severity).toEqual(['1px', '1px', '1px']);
        expect(box.colour, severity).toBe(box.want);
        expect(box.inset, severity).toBe(17);
        // the panel's card surface, not the sunken box a notice used to be
        expect(box.bg, severity).not.toBe('rgba(0, 0, 0, 0)');
    }
    // the link button and the Silence bell still work from inside the panel
    const rega = page.locator('[data-notice="rega"]');
    await expect(rega.getByRole('link', {name: 'Addons', exact: true})).toBeVisible();
    await rega.locator('[data-action="silence"]').click();
    await expect(rega.locator('.ol-silence-pop')).toBeVisible();
    // the menu is not clipped by the panel
    const pop = (await rega.locator('.ol-silence-pop').boundingBox())!;
    const panel = (await rega.boundingBox())!;
    expect(pop.y + pop.height).toBeGreaterThan(panel.y + panel.height);
});

// task 203: the scope is the Notice component. An inline `.ol-notice` - a hint inside a panel, used
// on nearly every page - is still the sunken box with its 1 px border all round.
test('an inline notice on another page is still the sunken box', async ({page, context, baseURL}) => {
    await context.addCookies([{name: 'stub-fw', value: `fw-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/system/firewall');
    const inline = page.locator('[data-notice="fw-migrated"]');
    await expect(inline).toBeVisible();
    await expect(inline).not.toHaveClass(/\bol-panel\b/);
    const box = await inline.evaluate((el) => {
        const cs = getComputedStyle(el);
        const probe = document.createElement('div');
        probe.style.background = cs.getPropertyValue('--hmm-bg-sunken').trim();
        el.append(probe);
        const sunken = getComputedStyle(probe).backgroundColor;
        probe.remove();
        return {widths: [cs.borderTopWidth, cs.borderRightWidth, cs.borderBottomWidth, cs.borderLeftWidth], bg: cs.backgroundColor, sunken};
    });
    expect(box.widths).toEqual(['1px', '1px', '1px', '1px']);
    expect(box.bg).toBe(box.sunken);
});

// hs485d (BidCos-Wired) has no listBidcosInterfaces: it answers with a fault, the sampler lists it
// under answering, and the page shows it up - never "not answering"
test('Status: an interface process without a radio list is up', async ({page}) => {
    await page.route('**/api/system/v1/radio/health', async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as {answering?: string[]};
        body.answering = ['BidCos-Wired'];
        await route.fulfill({response, json: body});
    });
    await page.goto('/');
    const card = page.locator('.ol-card[data-component="BidCos-Wired"]');
    await expect(card).toHaveCount(1);
    await expect(card.locator('.ol-card-body')).toHaveText('up');
    await expect(card.locator('.ol-dot.ok')).toHaveCount(1);
    await expect(page.getByText('not answering')).toHaveCount(0);
});
