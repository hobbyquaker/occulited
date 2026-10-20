import {expect, test, type Page} from '@playwright/test';
import fs from 'node:fs';
import {Box, buildBinary, removeBinary} from './realbox';

// openccu-lite task 262, occulited task 14: passkeys, end to end against the real occulited with
// Chromium's virtual authenticator (CDP WebAuthn.addVirtualAuthenticator: a CTAP2 authenticator with
// resident keys and user verification). The page is opened on http://localhost:<port> - the RP ID
// must be a name, and localhost is the one a development daemon accepts - through the real login
// page: a passkey is added behind the password confirmation, it signs in without name and password,
// the password alone still signs in (a passkey is never a second factor), and it is removed. A key
// that cannot make a passkey (no resident credential, no PIN) is not registered; one stored as a
// second factor before task 14 is marked as unable to sign in. On an address (127.0.0.1) the
// account page says a key cannot be made there.
//
// The virtual authenticator answers every ceremony at once - the login page's conditional passkey
// offer included, which a real browser completes only when the user picks a credential. So the
// passkey authenticator is detached (its credentials kept through CDP) while a page should stay on
// the login form, and attached again for the button.

test.beforeAll(async ({}, workerInfo) => buildBinary(workerInfo));
test.afterAll(removeBinary);
test.skip(({browserName}) => browserName !== 'chromium', 'the virtual authenticator is Chromium\'s');

let box: Box;
const PASSWORD = 'webauthn-pass-1';

test.beforeEach(async () => {
    box = new Box();
    await box.start();
    const setup = await fetch(`${box.url}/api/auth/v1/setup`, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({username: 'admin', password: PASSWORD})});
    const body = await setup.text();
    expect(setup.status, body).toBe(200);
    await fetch(`${box.url}/api/auth/v1/logout`, {method: 'POST', headers: {Cookie: `occulite_session=${JSON.parse(body).sid}`, 'X-Occulite-Request': '1'}});
});

test.afterEach(async () => {
    await box.stop('SIGKILL');
    fs.rmSync(box.dir, {recursive: true, force: true});
});

function byName() {
    return box.url.replace('127.0.0.1', 'localhost');
}

interface Authenticator {
    /** removes the authenticator, keeping its credentials for attach */
    detach(): Promise<void>;
    /** adds an authenticator with the saved credentials */
    attach(): Promise<void>;
}

/** passkey: a platform authenticator with resident credentials and user verification; else a plain U2F-style key without either */
function options(passkey: boolean) {
    return passkey
        ? {protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true}
        : {protocol: 'ctap2', transport: 'usb', hasResidentKey: false, hasUserVerification: false, automaticPresenceSimulation: true};
}

async function virtualAuthenticator(page: Page, passkey = true): Promise<Authenticator> {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    const AUTHENTICATOR = options(passkey);
    let id = ((await cdp.send('WebAuthn.addVirtualAuthenticator', {options: AUTHENTICATOR})) as {authenticatorId: string}).authenticatorId;
    let saved: unknown[] = [];
    return {
        async detach() {
            if (!id) return;
            saved = ((await cdp.send('WebAuthn.getCredentials', {authenticatorId: id})) as {credentials: unknown[]}).credentials;
            await cdp.send('WebAuthn.removeVirtualAuthenticator', {authenticatorId: id});
            id = '';
        },
        async attach() {
            if (id) return;
            id = ((await cdp.send('WebAuthn.addVirtualAuthenticator', {options: AUTHENTICATOR})) as {authenticatorId: string}).authenticatorId;
            for (const c of saved) await cdp.send('WebAuthn.addCredential', {authenticatorId: id, credential: c});
        },
    };
}

async function logoutByFetch(page: Page) {
    await page.evaluate(() => fetch('/api/auth/v1/logout', {method: 'POST', headers: {'X-Occulite-Request': '1', 'Content-Type': 'application/json'}, body: '{}'}));
}

async function loginWithPassword(page: Page, base: string) {
    await page.goto(`${base}/login`);
    const form = page.locator('form.ol-card');
    await expect(form).toBeVisible();
    await page.locator('input:not([type=password])').first().fill('admin');
    await page.locator('input[type=password]').fill(PASSWORD);
    await page.getByRole('button', {name: /^(Login|Anmelden)$/}).click();
}

async function expectLoggedIn(page: Page) {
    await expect(page.locator('[data-user-menu], .ol-user, header')).toBeVisible();
    await expect.poll(async () => (await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).authenticated).toBe(true);
}

async function addKey(page: Page, name: string, expectCount = 1) {
    await page.goto(`${byName()}/account`);
    const card = page.locator('[data-section="security-keys"]');
    await expect(card).toBeVisible();
    await card.locator('[data-add-key]').click();
    // the name
    const dialog = page.locator('[role=dialog]');
    await dialog.locator('input').fill(name);
    await dialog.getByRole('button', {name: /Continue|Weiter/}).click();
    // the confirmation with the password
    await dialog.locator('input[type=password]').fill(PASSWORD);
    await dialog.getByRole('button', {name: /Confirm|Bestätigen/}).click();
    await expect(card.locator('tbody tr')).toHaveCount(expectCount);
    await expect(card.locator('tbody tr', {hasText: name})).toHaveCount(1);
}

async function removeKey(page: Page, name: string) {
    const card = page.locator('[data-section="security-keys"]');
    await card.locator('tbody tr', {hasText: name}).getByRole('button', {name: /^(Remove|Entfernen)$/}).click();
    const dialog = page.locator('[role=dialog]');
    await dialog.getByRole('button', {name: /^(Remove|Entfernen)$/}).click();
    await dialog.locator('input[type=password]').fill(PASSWORD);
    await dialog.getByRole('button', {name: /Confirm|Bestätigen/}).click();
    await expect(card.locator('tbody tr', {hasText: name})).toHaveCount(0);
}

test('a passkey is added, signs in without name and password, leaves the password login alone, and is removed', async ({page}) => {
    const phone = await virtualAuthenticator(page, true);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    await addKey(page, 'phone');
    const card = page.locator('[data-section="security-keys"]');
    await expect(card.locator('tbody tr', {hasText: 'phone'}).locator('[data-key-kind]')).toHaveText(/^(passkey|Passkey)$/);
    await expect(card, 'one passkey: the hint to add a second').toContainText(/second one|zweiten/);
    await expect(card.locator('[data-keys-recovery]')).toContainText('occulited admin reset-auth');

    // the password alone signs in as before: no key step, the session at once
    await logoutByFetch(page);
    await phone.detach();
    await page.goto(`${byName()}/login`);
    await expect(page.locator('[data-passkey]')).toBeVisible();
    await page.locator('input:not([type=password])').first().fill('admin');
    await page.locator('input[type=password]').fill(PASSWORD);
    const [passwordAnswer] = await Promise.all([
        page.waitForResponse((r) => r.url().endsWith('/api/auth/v1/login') && r.request().method() === 'POST'),
        page.getByRole('button', {name: /^(Login|Anmelden)$/}).click(),
    ]);
    expect(passwordAnswer.status()).toBe(200);
    expect(await passwordAnswer.json()).toHaveProperty('sid');
    await expectLoggedIn(page);
    expect((await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).method).toBe('password');

    // the passkey signs in without name and password, through the button: the login page is
    // opened without the authenticator (its offer in the name field would sign in by itself with
    // one), then the phone comes and the button is pressed
    await logoutByFetch(page);
    await page.goto(`${byName()}/login`);
    await expect(page.locator('[data-passkey]')).toBeVisible();
    await phone.attach();
    if (await page.locator('[data-passkey]').isVisible()) await page.locator('[data-passkey]').click();
    await expectLoggedIn(page);
    expect((await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).method).toBe('passkey');
    // its last use is on the card
    await page.goto(`${byName()}/account`);
    await expect(card.locator('tbody tr').first()).not.toContainText(/never|nie/);

    // removal asks for the password again; without a passkey the login page has no button
    await removeKey(page, 'phone');
    await expect(card).toContainText(/No passkey yet|Noch kein Passkey/);
    await logoutByFetch(page);
    await phone.detach();
    await page.goto(`${byName()}/login`);
    await expect(page.locator('form.ol-card')).toBeVisible();
    await expect(page.locator('[data-passkey]')).toHaveCount(0);
});

test('a key that cannot make a passkey is not registered', async ({page}) => {
    // a U2F-style key: no resident credential, no PIN - the browser cannot meet "required"
    await virtualAuthenticator(page, false);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    await page.goto(`${byName()}/account`);
    const card = page.locator('[data-section="security-keys"]');
    await card.locator('[data-add-key]').click();
    const dialog = page.locator('[role=dialog]');
    await dialog.locator('input').fill('blue key');
    await dialog.getByRole('button', {name: /Continue|Weiter/}).click();
    await dialog.locator('input[type=password]').fill(PASSWORD);
    await dialog.getByRole('button', {name: /Confirm|Bestätigen/}).click();
    await expect(card.locator('.ol-notice.error')).toBeVisible();
    await expect(card.locator('tbody tr')).toHaveCount(0);
    expect((await page.evaluate(() => fetch('/api/auth/v1/me/webauthn').then((r) => r.json()))).keys).toEqual([]);
});

test('a key stored as a second factor before is marked, and the password signs in alone', async ({page}) => {
    const phone = await virtualAuthenticator(page, true);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    await addKey(page, 'old key');
    await phone.detach();
    // what an upgrade finds: a key users.json holds as a non-resident credential (a second
    // factor of task 262) - the daemon reads the changed file at the next request
    const file = `${box.state}/users.json`;
    const doc = JSON.parse(fs.readFileSync(file, 'utf8'));
    for (const u of doc.users) for (const k of u.webauthn ?? []) k.key.extensions = {rk: false};
    fs.writeFileSync(file, JSON.stringify(doc, null, 2));
    await page.goto(`${byName()}/account`);
    const card = page.locator('[data-section="security-keys"]');
    await expect(card.locator('tbody tr', {hasText: 'old key'}).locator('[data-key-kind]')).toHaveText(/cannot be used to sign in|kann nicht zur Anmeldung genutzt werden/);
    await expect(card.locator('[data-keys-unusable]')).toBeVisible();
    // no passkey anywhere: no button; and the password alone opens the session
    await logoutByFetch(page);
    await page.goto(`${byName()}/login`);
    await expect(page.locator('form.ol-card')).toBeVisible();
    await expect(page.locator('[data-passkey]')).toHaveCount(0);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    // and it is removed with the same offer
    await page.goto(`${byName()}/account`);
    await removeKey(page, 'old key');
    await expect(card.locator('[data-keys-unusable]')).toHaveCount(0);
});

test('the passkey signs in through the browser\'s offer in the name field', async ({page}) => {
    const key = await virtualAuthenticator(page);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    await addKey(page, 'phone');
    await logoutByFetch(page);
    // the authenticator stays: the conditional offer completes as a user picking the passkey would
    void key;
    await page.goto(`${byName()}/login`);
    await expectLoggedIn(page);
    expect((await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).method).toBe('passkey');
});

test('on an address the account page says a key cannot be made there', async ({page}) => {
    await loginWithPassword(page, box.url); // 127.0.0.1
    await expectLoggedIn(page);
    await page.goto(`${box.url}/account`);
    const card = page.locator('[data-section="security-keys"]');
    await expect(card.locator('[data-keys-wrong-place]')).toBeVisible();
    await expect(card.locator('[data-add-key]')).toHaveCount(0);
    // and the API refuses a registration on the address (409 webauthn-needs-name)
    const r = await page.evaluate(async () => {
        const t = await fetch('/api/auth/v1/ticket', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-Occulite-Request': '1'}, body: JSON.stringify({path: '/api/auth/v1/me/webauthn', confirm: true, password: 'webauthn-pass-1'})}).then((x) => x.json());
        const b = await fetch('/api/auth/v1/me/webauthn/begin', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-Occulite-Request': '1', 'X-Occulite-Confirm': t.ticket}, body: JSON.stringify({name: 'x'})});
        return {status: b.status, body: await b.json()};
    });
    expect(r.status).toBe(409);
    expect(r.body.error).toBe('webauthn-needs-name');
});

// occulited task 70 (the maintainer: "cant a passkey be used automatically if one is setup? ... this
// should also be configurable"): auth.passkey_offer. "prompt" opens the passkey dialog when the
// login page opens - with Chromium's virtual authenticator that is a sign-in without a click - once
// per tab; "off" offers nothing but the button.
async function setOffer(page: Page, offer: string) {
    const st = await page.evaluate(async (o) => {
        const r = await fetch('/api/auth/v1/config', {method: 'PUT', headers: {'X-Occulite-Request': '1', 'Content-Type': 'application/json'}, body: JSON.stringify({mode: 'local', passkey_offer: o})});
        return r.status;
    }, offer);
    expect(st).toBe(200);
}

test('the passkey offer "prompt": the dialog at once signs in, once per tab', async ({page}) => {
    const key = await virtualAuthenticator(page);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    await addKey(page, 'phone');
    await setOffer(page, 'prompt');
    expect((await page.evaluate(() => fetch('/api/auth/v1/webauthn').then((r) => r.json()))).offer).toBe('prompt');
    await logoutByFetch(page);
    await page.evaluate(() => sessionStorage.clear());
    await page.goto(`${byName()}/login`);
    await expectLoggedIn(page);
    expect((await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).method).toBe('passkey');
    // the tab was asked once: signed out again, the page does not ask again by itself
    await logoutByFetch(page);
    await key.detach();
    await page.goto(`${byName()}/login`);
    await expect(page.locator('input[type=password]')).toBeVisible();
    await key.attach();
    await page.waitForTimeout(1500);
    expect((await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).authenticated).toBe(false);
    // the button still signs in
    await page.locator('[data-passkey]').click();
    await expectLoggedIn(page);
});

test('the passkey offer "off": neither the dialog nor the autofill, the button only', async ({page}) => {
    const key = await virtualAuthenticator(page);
    await loginWithPassword(page, byName());
    await expectLoggedIn(page);
    await addKey(page, 'phone');
    await setOffer(page, 'off');
    await logoutByFetch(page);
    void key;
    await page.goto(`${byName()}/login`);
    await expect(page.locator('[data-passkey]')).toBeVisible();
    await page.waitForTimeout(1500);
    // with "autofill" the virtual authenticator answers the offer at once (the test above it); here nothing asks
    expect((await page.evaluate(() => fetch('/api/auth/v1/state').then((r) => r.json()))).authenticated).toBe(false);
    await page.locator('[data-passkey]').click();
    await expectLoggedIn(page);
});
