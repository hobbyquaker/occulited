import {type Page, type Route} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited B-52 (openccu-lite #7): a fresh system showed an Addons page without any catalogue and
// without a word why. Now a first visit is told that the catalogue comes from GitHub and gets a
// button that loads it, the image's own list is shown meanwhile and marked as such, a failed check
// says so with the reason and a retry, and a card that cannot render does not blank the list.

const BUNDLED_DATE = '2026-10-04T05:37:00Z';
const bundledEntries = [
    {git: 'https://github.com/hobbyquaker/homematic-manager', manifest_path: 'apps/ccu-addon/files/openccu-lite.json'},
    {git: 'https://github.com/rdmtc/RedMatic', manifest_path: 'addon_files/openccu-lite.json'},
    {git: 'https://github.com/mdzio/ccu-jack', manifest_path: 'catalog/manifests/ccu-jack.json', untested: true, adapter: true, format: 1, id: 'ccu-jack', name: {de: 'CCU-Jack', en: 'CCU-Jack'}, release: {github: 'mdzio/ccu-jack', asset: 'ccu-jack-{version}.tar.gz'}},
];
const checkedEntries = [
    {git: 'https://github.com/hobbyquaker/homematic-manager', manifest_path: 'apps/ccu-addon/files/openccu-lite.json', format: 1, id: 'hmm', name: {de: 'Homematic Manager', en: 'Homematic Manager'}, description: {de: 'Geräte anlernen', en: 'Pair devices'}, release: {github: 'hobbyquaker/homematic-manager', asset: 'hmm-{version}.tar.gz'}, latest: {version: '3.1.3'}, stars: 195},
    {git: 'https://github.com/rdmtc/RedMatic', manifest_path: 'addon_files/openccu-lite.json', format: 1, id: 'redmatic', name: {de: 'RedMatic', en: 'RedMatic'}, release: {github: 'rdmtc/RedMatic', asset: 'redmatic-{version}.tar.gz'}, latest: {version: '9.12.3'}, stars: 528},
    bundledEntries[2],
];

type View = Record<string, unknown>;
const bundledView = (extra: View = {}): View => ({format: 1, addons: bundledEntries, source: 'bundled', bundled_date: BUNDLED_DATE, ...extra});
const failure = {at: '2026-10-06T14:32:01Z', message: 'raw.githubusercontent.com: dial tcp: lookup raw.githubusercontent.com: no such host', host: 'raw.githubusercontent.com', failures: 1, since: '2026-10-06T14:32:01Z'};

/** The catalogue as a fresh system answers it: no installed addon, the given view before and after a check. */
async function freshSystem(page: Page, before: View, after: View | ((route: Route) => Promise<void>)): Promise<void> {
    await page.route('**/api/system/v1/addons', (route) => route.fulfill({json: {addons: []}}));
    await page.route('**/api/system/v1/firewall/addons', (route) => route.fulfill({json: []}));
    await page.route(/\/api\/system\/v1\/catalog(\?refresh=1)?$/, async (route) => {
        if (route.request().url().endsWith('?refresh=1')) {
            if (typeof after === 'function') return after(route);
            before = after; // the system holds what the check found
        }
        await route.fulfill({json: {catalog: before, installed: {}, arch: 'x86_64', daily: false}});
    });
}

test('a first visit: the built-in list, marked, and a button that loads the catalogue', async ({page}) => {
    let release: () => void = () => {};
    const held = new Promise<void>((r) => (release = r));
    await freshSystem(page, bundledView(), async (route) => {
        await held; // the spinner is seen while the check runs
        await route.fulfill({json: {catalog: {format: 1, addons: checkedEntries, source: 'published', bundled_date: BUNDLED_DATE, checked: '2026-10-06T14:35:00Z'}, installed: {}, arch: 'x86_64', daily: false}});
    });
    await page.goto('/addons');
    const notice = page.locator('[data-catalog-load="first"]');
    await expect(notice).toContainText('The addon catalogue is loaded from GitHub.');
    await expect(notice).toContainText('Until then this page shows the built-in list of');
    await expect(page.locator('[data-catalog-meta]')).toContainText('built-in list of');
    // the built-in entries are there meanwhile
    await expect(page.locator('[data-addon-card]')).toHaveCount(3);
    const button = notice.locator('[data-catalog-load-now]');
    await expect(button).toHaveText('Load the catalogue now');
    await button.click();
    await expect(button).toHaveText('Loading the catalogue…');
    await expect(button).toHaveAttribute('aria-busy', 'true');
    await expect(button.locator('.ad-spinner')).toBeVisible();
    release();
    // loaded: the notice is gone, the cards carry the manifests
    await expect(page.locator('[data-catalog-load]')).toHaveCount(0);
    await expect(page.locator('#addon-row-hmm')).toContainText('3.1.3');
    await expect(page.locator('[data-catalog-meta]')).toContainText('checked');
    expect(await fitsWindow(page)).toBe(true);
});

test('German: Katalog jetzt laden, the built-in list of its date', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await freshSystem(page, bundledView(), bundledView());
    await page.goto('/addons');
    const notice = page.locator('[data-catalog-load="first"]');
    await expect(notice).toContainText('Der Katalog der Zusatzsoftware wird von GitHub geladen.');
    await expect(notice).toContainText('mitgelieferte Liste vom');
    await expect(notice.locator('[data-catalog-load-now]')).toHaveText('Katalog jetzt laden');
    await expect(page.locator('[data-catalog-meta]')).toContainText('mitgelieferte Liste vom');
});

test('a failed check says so - in the header and with the reason - and offers a retry', async ({page}) => {
    await freshSystem(page, bundledView(), bundledView({check_error: failure}));
    await page.goto('/addons');
    await page.locator('[data-catalog-load-now]').click();
    const notice = page.locator('[data-catalog-load="failed"]');
    await expect(notice).toContainText('The addon catalogue is loaded from GitHub.');
    await expect(notice.locator('[data-catalog-error]')).toContainText('Loading the catalogue failed: raw.githubusercontent.com: dial tcp: lookup raw.githubusercontent.com: no such host');
    await expect(notice.locator('[data-catalog-load-now]')).toHaveText('Try again');
    await expect(page.locator('[data-check-failed]')).toContainText('check failed');
    // the built-in list stays
    await expect(page.locator('[data-addon-card]')).toHaveCount(3);
    expect(await fitsWindow(page)).toBe(true);
});

test('a check that keeps failing after one that worked: the count, and the last good check', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    const view = {format: 1, addons: checkedEntries, source: 'published', bundled_date: BUNDLED_DATE, checked: '2026-10-05T09:00:00Z', check_error: {...failure, failures: 3, since: '2026-10-05T10:00:00Z'}};
    await freshSystem(page, view, view);
    await page.goto('/addons');
    const notice = page.locator('[data-catalog-load="failed"]');
    await expect(notice).not.toContainText('wird von GitHub geladen');
    await expect(notice.locator('[data-catalog-error]')).toContainText('Laden des Katalogs fehlgeschlagen: raw.githubusercontent.com');
    await expect(notice.locator('[data-catalog-error]')).toContainText('(3 Prüfungen in Folge seit');
    await expect(notice.locator('[data-catalog-load-now]')).toHaveText('Erneut versuchen');
    await expect(page.locator('[data-catalog-meta]')).toContainText('Prüfung fehlgeschlagen');
    await expect(page.locator('[data-catalog-meta]')).toContainText('zuletzt geprüft');
});

test('a check the system refuses (502) is not swallowed', async ({page}) => {
    await freshSystem(page, bundledView(), (route) => route.fulfill({status: 502, json: {error: 'catalog-unreachable', message: 'https://raw.githubusercontent.com/openccu-lite/occulited/master/catalog/catalog.json: HTTP 503'}}));
    await page.goto('/addons');
    await page.locator('[data-catalog-load-now]').click();
    await expect(page.locator('[data-catalog-error]')).toContainText('Loading the catalogue failed: https://raw.githubusercontent.com/openccu-lite/occulited/master/catalog/catalog.json: HTTP 503');
    await expect(page.locator('[data-addon-card]')).toHaveCount(3);
});

test('an empty catalogue the system cannot read says why, with the installed addons alone', async ({page}) => {
    await page.route('**/api/system/v1/addons', (route) => route.fulfill({json: {addons: []}}));
    await page.route(/\/api\/system\/v1\/catalog$/, (route) => route.fulfill({status: 502, json: {error: 'catalog-unreachable', message: 'file:///etc/occulite/catalog.json: open /etc/occulite/catalog.json: no such file or directory'}}));
    await page.goto('/addons');
    await expect(page.locator('[data-catalog-error]')).toContainText('The catalogue could not be read from the system: file:///etc/occulite/catalog.json');
    await expect(page.locator('[data-catalog-load-now]')).toHaveText('Try again');
    await expect(page.getByText('Nothing matches the filter.')).toBeVisible();
});

test('two entries naming one addon still show a list', async ({page}) => {
    const twice = [checkedEntries[0], {...checkedEntries[1], id: 'hmm'}, checkedEntries[2]];
    const view = {format: 1, addons: twice, source: 'published', checked: '2026-10-06T14:35:00Z'};
    await freshSystem(page, view, view);
    await page.goto('/addons');
    await expect(page.locator('[data-addon-card]')).toHaveCount(2);
    await expect(page.locator('#addon-row-hmm')).toBeVisible();
});

test('a card that cannot render does not blank the list without a word', async ({page}) => {
    const broken = [{...checkedEntries[0], images: {logo: 5}}, checkedEntries[1]];
    const view = {format: 1, addons: broken, source: 'published', checked: '2026-10-06T14:35:00Z'};
    await freshSystem(page, view, view);
    await page.goto('/addons');
    await expect(page.locator('[data-cards-error]')).toContainText('The list of addons could not be shown:');
    // the rest of the page is there
    await expect(page.locator('[data-addons-toolbar]')).toBeVisible();
});

test('German: a check cut off by its time limit is said in German', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    const view = bundledView({check_error: {at: '2026-10-06T17:11:52Z', message: 'the check did not finish in time', failures: 1, since: '2026-10-06T17:11:52Z'}});
    await freshSystem(page, view, view);
    await page.goto('/addons');
    await expect(page.locator('[data-catalog-error]')).toContainText('Laden des Katalogs fehlgeschlagen: die Prüfung wurde nicht rechtzeitig fertig');
    await expect(page.locator('[data-catalog-meta]')).toContainText('Prüfung fehlgeschlagen');
});
