import {expect, test, type Page} from '@playwright/test';
import {PORT} from './scroll';

// B-105 and B-108: no page of the shell is wider than a phone or a tablet window. At 412 px (the
// phone project's Pixel 7) the Services page was 527 px, Installed addons 987, Network 646 and
// Users 432, Security 458 in German; at 768 px Installed addons was still 992 (German 1061). The
// whole page scrolled sideways, the phone's browser zoomed it out, and taps landed beside what they
// aimed at. A table may scroll inside its own box, fold its columns or stack its rows; the document
// itself never scrolls sideways. Every page is opened in both languages - the German labels are
// longer - with the warnings the stub plants, and measured once its data is on screen.

type Check = {path: string; ready: string; then?: (page: Page) => Promise<void>};

const PAGES: Check[] = [
    {path: '/', ready: '.ol-gauge'},
    // task 98: then with the USB list open, in its card look (task 199: its button is in the
    // Modules heading row)
    {
        path: '/radio',
        ready: '[data-process="rfd"]',
        then: async (page) => {
            // the Modules heading row's own button, so the German run finds it too
            await page.locator('.ol-headrow', {has: page.locator('h2#modules')}).getByRole('button').click();
            await expect(page.locator('table.ol-usb')).toBeVisible();
            await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
        },
    },
    // task 183: the keys, with the security key's panel open
    {
        path: '/system/keys',
        ready: 'h2#device-keys',
        then: async (page) => {
            await page.locator('#security-key ~ .ol-disclosure-slot .ol-disclosure-trigger button').first().click();
            await expect(page.locator('#ol-sec-key')).toBeVisible();
            await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
        },
    },
    {path: '/system/updates', ready: 'table.fw-types tbody tr'},
    {path: '/system/services', ready: 'table.ol-timers tbody tr'},
    // the boot order graph below the timers, open in its default view (the list on a phone, the
    // chart elsewhere), then in the other one
    {
        path: '/system/services#boot',
        ready: 'section#boot [data-figure="total"] .v',
        then: async (page) => {
            const view = page.locator('section#boot select:has(option[value="chart"])');
            const other = (await view.inputValue()) === 'chart' ? 'list' : 'chart';
            await view.selectOption(other);
            await expect(page.locator(other === 'chart' ? 'section#boot svg.bt-bars' : 'section#boot table.bt-list')).toBeVisible();
        },
    },
    {path: '/system/users', ready: 'table.ol-stack tbody tr'},
    {path: '/system/log', ready: '.ol-line'},
    {path: '/system/log?source=kernel', ready: '.ol-line'},
    // task 101: the settings sheet on its levels tab, with occulited's and multimacd's rows
    {path: '/system/log?settings=levels', ready: '#ol-lv-multimacd'},
    {path: '/system/network', ready: '.ol-syspanels [data-panel="time"]'},
    // task 95: the status LED, then with its advanced settings and the colour legend open
    {
        path: '/system/led',
        ready: '[data-led-simple]',
        then: async (page) => {
            for (const part of ['[data-led-advanced]', '[data-led-legend]']) {
                await page.locator(`${part} summary`).click();
                await expect(page.locator(part)).toHaveAttribute('open', '');
            }
        },
    },
    {path: '/system/certificates', ready: '.ol-card .v'},
    // openccu-lite task 231: the four stores' tables, the system one with its filter
    {path: '/system/trust', ready: '[data-trust-store="system"] table tbody tr'},
    {path: '/system/users#authentication', ready: '.ol-secform input'},
    {path: '/system/backup', ready: 'h2'},
    {path: '/addons', ready: '.ad-card'},
    {path: '/settings', ready: 'select'},
    {path: '/account', ready: 'h2'},
];

/**
 * The page's overflow, and the outermost elements that reach past the window where nothing clips them.
 *
 * B-129 made `.ol-scrollport` - the box around `main` - the scrolling element, so the page's width is
 * the port's wherever the port carries the overflow: a scroll container keeps its horizontal overflow
 * to itself and the document would answer "the window's width" on every page. For the same reason the
 * walk that asks whether an element is clipped stops at the port: everything a page draws is inside
 * it, and counting the port as a clipper would report no culprit ever.
 */
async function overflow(page: Page) {
    return page.evaluate((s) => {
        const doc = document.documentElement;
        const vw = doc.clientWidth;
        const port = document.querySelector(s);
        const culprits: string[] = [];
        for (const el of Array.from(document.querySelectorAll('body *'))) {
            if (el === port) continue;
            const r = el.getBoundingClientRect();
            if (r.width === 0 || r.right <= vw + 1) continue;
            let clipped = false;
            for (let a = el.parentElement; a && a !== document.body && a !== port; a = a.parentElement) {
                if (getComputedStyle(a).overflowX !== 'visible') {
                    clipped = true;
                    break;
                }
            }
            if (clipped) continue;
            const parent = el.parentElement!.getBoundingClientRect();
            if (parent.right > vw + 1) continue;
            culprits.push(`${el.tagName.toLowerCase()}.${String(el.getAttribute('class') ?? '').trim().replace(/\s+/g, '.')} ${Math.round(r.width)} px to ${Math.round(r.right)}`);
        }
        return {page: Math.max(doc.scrollWidth, port?.scrollWidth ?? 0), window: vw, culprits: culprits.slice(0, 8)};
    }, PORT);
}

async function expectFits(page: Page, what: string) {
    await page.evaluate(() => document.fonts.ready);
    const o = await overflow(page);
    expect(o.page, `${what}: ${o.page} px in a ${o.window} px window - ${o.culprits.join('; ')}`).toBeLessThanOrEqual(o.window);
}

async function open(page: Page, baseURL: string, language: string, path: string) {
    await page.context().addCookies([{name: 'stub-warn', value: '1', url: baseURL}]);
    await page.addInitScript((l) => localStorage.setItem('ol.language', l), language);
    await page.goto(path);
    await expect(page.getByRole('heading', {level: 1})).toBeVisible();
}

function everyPage(label: string) {
    for (const language of ['en', 'de'] as const) {
        for (const p of PAGES) {
            test(`${p.path} in ${language === 'en' ? 'English' : 'German'}`, async ({page, baseURL}) => {
                await open(page, baseURL!, language, p.path);
                await expect(page.locator(p.ready).first()).toBeVisible();
                await expectFits(page, `${label} ${p.path}`);
                if (p.then) {
                    await p.then(page);
                    await expectFits(page, `${label} ${p.path}, the other view`);
                }
            });
        }
    }
}

test.describe('no page is wider than a phone', () => {
    test.skip(({isMobile}) => !isMobile, 'the phone project');
    everyPage('412 px');
});

// a tablet: the phone project widened to 768 px (a touch screen), and a desktop window that narrow
test.describe('no page is wider than a 768 px tablet window', () => {
    test.use({viewport: {width: 768, height: 1024}});
    test.beforeEach(({}, info) => {
        test.skip(info.project.name === 'desktop-dark', 'the light desktop and the phone projects');
    });
    everyPage('768 px');
});

// B-115: the checks above passed with the stub's short values while four pages were wider than a 412 px
// phone on .119 - the Status page's release-check error (GitHub's raw 403 body), the Backup table's
// file names (Firefox), the radio firmware card in German (Firefox) and the Network page's addon ports
// (WebKit). The stub's real-box data (stub-real=1) carries such values: a long error, file names
// without a break, long German port labels. Every project opens the four pages with it at 412 px and
// at 360 px - the narrower phone covers the few pixels by which the engines' fonts and break rules
// differ - in both languages. PW_ENGINES=firefox,webkit runs this file in those engines too
// (playwright.config.ts).
const REAL: Check[] = [
    // the Updates page since 2026-09-19/20: the release feed's error, and the radio firmware below it
    {path: '/system/updates', ready: '.fw-module:has-text("coprocessor_update_hm_only.eq3")'},
    // task 86: the targets' cards (a long SFTP path, the key line, the server key's fingerprint),
    // then the USB directory's backups with .119's file names
    {
        path: '/system/backup',
        ready: '[data-target="directory"] .ol-card-sub',
        then: async (page) => {
            await page.locator('[data-target="directory"] [data-action="backups"]').click();
            await expect(page.locator('table.ol-table td.hmm-mono:has-text("ccu3_backup_before_openccu_lite")').first()).toBeVisible();
        },
    },
    {path: '/radio', ready: '.ol-cards-radio .ol-card'},
    // task 157: the rule list on the Firewall page, the addon ports on the Addons page
    {path: '/system/firewall', ready: 'table.fw-rules tr[data-port="2001"]'},
    {path: '/addons', ready: 'table.ol-addon-ports td:has-text("RedMatic")'},
    // task 143
    {path: '/system/remote-access', ready: '[data-switch="tls"] tr[data-port="42010"]'},
];

test.describe("a real box's data fits a phone", () => {
    for (const width of [412, 360]) {
        for (const language of ['en', 'de'] as const) {
            for (const p of REAL) {
                test(`${p.path} at ${width} px in ${language === 'en' ? 'English' : 'German'}`, async ({page, baseURL}) => {
                    await page.setViewportSize({width, height: 900});
                    await page.context().addCookies([{name: 'stub-real', value: '1', url: baseURL!}]);
                    await page.addInitScript((l) => localStorage.setItem('ol.language', l), language);
                    await page.goto(p.path);
                    await expect(page.locator(p.ready).first()).toBeVisible();
                    await expectFits(page, `${width} px with a real box's data, ${p.path}`);
                    if (p.then) {
                        await p.then(page);
                        await expectFits(page, `${width} px with a real box's data, ${p.path}, the other view`);
                    }
                });
            }
        }
    }
});

// task 139: the Addons page is cards - two columns above 1100 px, one below - and fits at the
// edges, in German
test.describe('the Addons page at the edges of its steps', () => {
    test.beforeEach(({}, info) => {
        test.skip(info.project.name !== 'desktop-light', 'widths of a desktop window');
    });
    for (const width of [701, 900, 1100, 1101]) {
        test(`${width} px in German`, async ({page, baseURL}) => {
            await page.setViewportSize({width, height: 900});
            await open(page, baseURL!, 'de', '/addons');
            const cards = page.locator('.ad-card');
            await expect(cards.first()).toBeVisible();
            await expectFits(page, `/addons at ${width} px`);
            const lefts = new Set(await cards.evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().left))));
            expect(lefts.size).toBe(width <= 1100 ? 1 : 2);
        });
    }
});

