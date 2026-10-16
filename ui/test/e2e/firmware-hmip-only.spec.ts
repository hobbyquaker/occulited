import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';

// occulited task 60 (the maintainer, 2026-10-15): "system - updates firmwares should only show HmIP
// devices, hide Bidcos-RF, Wired and VirtualDevices". The device firmware list, its counts, its
// search and Show all devices take hmipserver's devices only (HmIP wired included); so does the
// Status page's Device firmware card. The daemon's downloads are unchanged.

/** the stub's firmware answer plus a BidCos-RF type with an update and a held BidCos bundle */
async function mixed(page: Page) {
    await page.route('**/api/system/v1/firmware', async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        const response = await route.fetch();
        const st = await response.json();
        st.devices.push({interface: 'BidCos-RF', address: 'NEQ1234567', type: 'HM-LC-Sw1-Pl-DN-R1', firmware: '1.9', latest: '2.8', update_available: true, name: 'Plug garage'});
        st.deployed.push({type_code: '216', dir: '216-HM-LC-Sw1-Pl-DN-R1-2.8', name: 'HM-LC-Sw1-Pl-DN-R1', version: '2.8', held: true, files: ['hm_lc_sw1_pl_dn_r1_update_V2_8.eq3', 'info']});
        await route.fulfill({response, json: st});
    });
}

const rows = (page: Page) => page.locator('table.fw-types tbody tr.fw-row');

test('only HmIP types in the list, the counts and the search; HmIP wired stays', async ({page}) => {
    await mixed(page);
    await page.goto('/system/updates');
    await expect(rows(page)).toHaveCount(4);
    await page.locator('[data-show-all]').check();
    const types = await rows(page).evaluateAll((trs) => trs.map((tr) => tr.getAttribute('data-type')));
    expect(types.sort()).toEqual(['HMIP-WRC2', 'HmIP-PDT', 'HmIP-STHO', 'HmIP-SWDO-2', 'HmIPW-DRS8']);
    // every row's interface is HmIP-RF
    expect(new Set(await rows(page).locator('td[data-label="Interface"]').allTextContents())).toEqual(new Set(['HmIP-RF']));
    // the counts: the BidCos update and its held bundle are not among them
    await expect(page.locator('[data-sum="available"]')).toHaveText('1 available at eQ-3');
    await expect(page.locator('[data-sum="held"]')).toHaveCount(0);
    // the search does not find a BidCos device by name, address or type
    for (const q of ['garage', 'NEQ1234567', 'HM-LC-Sw1']) {
        await page.getByRole('textbox', {name: 'Search the device firmware'}).fill(q);
        await expect(rows(page)).toHaveCount(0);
    }
});

test('German: the same rows', async ({page}) => {
    await mixed(page);
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/system/updates');
    await expect(page.getByRole('heading', {name: 'Gerätefirmware'})).toBeVisible();
    await page.locator('[data-show-all]').check();
    await expect(rows(page)).toHaveCount(5);
    await expect(page.locator('table.fw-types tr.fw-row[data-type="HM-LC-Sw1-Pl-DN-R1"]')).toHaveCount(0);
});

test('the Status page counts the HmIP devices with an update only', async ({page}) => {
    await mixed(page);
    await page.goto('/');
    // the WRC2 and the wired DRS8; the BidCos plug is not counted
    await expect(page.locator('[data-component="firmware"] .ol-card-body')).toHaveText('2 updates available');
});
