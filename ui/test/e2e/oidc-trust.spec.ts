import {expect, test, type Page} from '@playwright/test';

// openccu-lite task 230 (the maintainer: "shouldnt we have possibility to upload/paste pem for
// trusting authentik server?"): in the OIDC settings, the certificates trusted for the identity
// provider - paste or upload a PEM, see subject, validity and fingerprint, remove it; test the
// connection; fetch the issuer's chain and trust one by its fingerprint. A key is refused.

const PEM = (n: string) => `-----BEGIN CERTIFICATE-----\nMIIB${n}\n-----END CERTIFICATE-----\n`;

async function own(page: Page, baseURL: string | undefined) {
    await page.context().addCookies([{name: 'stub-trust', value: `tr-${Math.random().toString(36).slice(2)}`, url: baseURL!}]);
    await page.goto('/system/users');
    await expect(page.locator('[data-oidc-trust]')).toBeVisible();
}
const section = (page: Page) => page.locator('[data-oidc-trust]');

test('paste a CA, the test passes, remove it, the test fails', async ({page, baseURL}) => {
    await own(page, baseURL);
    const s = section(page);
    await expect(s.getByRole('heading', {name: 'Trusted certificates'})).toBeVisible();
    await expect(s.locator('[data-oidc-trust-empty]')).toBeVisible();
    // the test before: the provider's certificate is unknown
    await s.locator('[data-action="oidc-test"]').click();
    await expect(s.locator('[data-oidc-test="failed"]')).toContainText('certificate signed by unknown authority');
    await s.locator('[data-oidc-trust-pem]').fill(PEM('one'));
    await s.getByRole('button', {name: 'Trust', exact: true}).click();
    await expect(s.locator('[data-notice="oidc-trust"]')).toHaveText('The certificate is trusted for the identity provider.');
    const item = s.locator('[data-anchor]');
    await expect(item).toHaveCount(1);
    await expect(item).toContainText('Certificate authority');
    await expect(item).toContainText('CN=Pasted 1');
    await expect(item.locator('[data-fingerprint]')).toContainText('CC:01:');
    await expect(s.locator('[data-oidc-trust-pem]')).toHaveValue('');
    await s.locator('[data-action="oidc-test"]').click();
    await expect(s.locator('[data-oidc-test="ok"]')).toContainText('TLS verified by CN=Pasted 1, trusted here.');
    // remove: asks first
    await item.getByRole('button', {name: 'Remove'}).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('no longer trusts CN=Pasted 1');
    await dialog.getByRole('button', {name: 'Remove'}).click();
    await expect(s.locator('[data-anchor]')).toHaveCount(0);
    await s.locator('[data-action="oidc-test"]').click();
    await expect(s.locator('[data-oidc-test="failed"]')).toBeVisible();
});

test('upload a chain file: two anchors, the one that expires soon is marked', async ({page, baseURL}) => {
    await own(page, baseURL);
    const s = section(page);
    await s.locator('[data-oidc-trust-file]').setInputFiles({name: 'chain.pem', mimeType: 'application/x-pem-file', buffer: Buffer.from(PEM('a') + PEM('b'))});
    await expect(s.locator('[data-oidc-trust-pem]')).toHaveValue(/BEGIN CERTIFICATE[\s\S]*BEGIN CERTIFICATE/);
    await s.getByRole('button', {name: 'Trust', exact: true}).click();
    await expect(s.locator('[data-notice="oidc-trust"]')).toHaveText('2 certificates are trusted for the identity provider.');
    await expect(s.locator('[data-anchor]')).toHaveCount(2);
    await expect(s.locator('[data-anchor]').nth(1)).toContainText('expires within 30 days');
});

test('a private key is refused', async ({page, baseURL}) => {
    await own(page, baseURL);
    const s = section(page);
    await s.locator('[data-oidc-trust-pem]').fill(PEM('x') + '-----BEGIN EC PRIVATE KEY-----\nMHc\n-----END EC PRIVATE KEY-----\n');
    await s.getByRole('button', {name: 'Trust', exact: true}).click();
    await expect(s.locator('[data-notice="oidc-trust-error"]')).toContainText('private key');
    await expect(s.locator('[data-anchor]')).toHaveCount(0);
});

test("fetch the issuer's chain and trust its CA by fingerprint", async ({page, baseURL}) => {
    await own(page, baseURL);
    const s = section(page);
    await s.locator('[data-action="oidc-fetch"]').click();
    const chain = s.locator('[data-oidc-chain]');
    await expect(chain).toContainText('The chain the issuer presents is not trusted');
    await expect(chain.locator('[data-peer]')).toHaveCount(2);
    const ca = chain.locator('[data-peer]').nth(1);
    await expect(ca).toContainText('CN=Lab CA');
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/auth/v1/oidc/trust')),
        (async () => {
            await ca.getByRole('button', {name: 'Trust this certificate…'}).click();
            const dialog = page.getByRole('dialog');
            await expect(dialog).toContainText('Compare its SHA-256 fingerprint');
            await expect(dialog).toContainText('BB:22:00');
            await dialog.getByRole('button', {name: 'Trust'}).click();
        })(),
    ]);
    expect(req.postDataJSON().fingerprint).toMatch(/^BB:22:/);
    await expect(s.locator('[data-anchor]')).toHaveCount(1);
    await expect(chain).toContainText('The chain the issuer presents is trusted already.');
    await expect(ca.locator('.ol-badge', {hasText: 'trusted here'})).toBeVisible();
});

test('in German', async ({page, baseURL}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await own(page, baseURL);
    const s = section(page);
    await expect(s.getByRole('heading', {name: 'Vertrauenswürdige Zertifikate'})).toBeVisible();
    await expect(s.getByRole('button', {name: 'Verbindung testen'})).toBeVisible();
    await expect(s.getByRole('button', {name: 'Zertifikat des Issuers abrufen'})).toBeVisible();
});

test('only in mode OpenID Connect', async ({page, baseURL}) => {
    await own(page, baseURL);
    await page.getByRole('radio', {name: /Local users/}).check();
    await expect(page.locator('[data-oidc-trust]')).toHaveCount(0);
});

// task 235 (the maintainer, after task 230 on the Charly: "make the pem input field multiline (same
// size as on acme setup)"): the paste field is the ACME CA root field's twin - 30 rows, monospace,
// the line breaks kept - and a pasted chain arrives with its line breaks and is accepted.
test('the PEM field is multi-line, keeps a pasted chain line by line, and is the ACME field\'s size', async ({page, baseURL}) => {
    await own(page, baseURL);
    const s = section(page);
    const field = s.locator('[data-oidc-trust-pem]');
    await expect(field).toHaveAttribute('rows', '30');
    await expect(field).toHaveClass(/\bol-pem\b/);
    const style = await field.evaluate((e) => {
        const c = getComputedStyle(e);
        return {whiteSpace: c.whiteSpace, lines: e.getBoundingClientRect().height / parseFloat(c.lineHeight), font: c.fontFamily, size: c.fontSize, line: c.lineHeight};
    });
    expect(style.whiteSpace).toBe('pre');
    expect(style.lines).toBeGreaterThanOrEqual(29);
    expect(style.font).toMatch(/mono/i);

    // a chain pasted from the clipboard, as authentik's certificate download has it
    const chain = PEM('leaf') + PEM('intermediate');
    await field.focus();
    // insertText is what the browser does with a paste: the text in one input event, line breaks and all
    await page.keyboard.insertText(chain);
    await expect(field).toHaveValue(chain);
    expect((await field.inputValue()).split('\n').length).toBe(7);
    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/auth/v1/oidc/trust')),
        s.getByRole('button', {name: 'Trust', exact: true}).click(),
    ]);
    expect(req.postDataJSON().pem).toBe(chain);
    await expect(s.locator('[data-notice="oidc-trust"]')).toHaveText('2 certificates are trusted for the identity provider.');
    await expect(s.locator('[data-anchor]')).toHaveCount(2);
    await expect(s.locator('[data-anchor] [data-fingerprint]').first()).toBeVisible();

    // the same box as the ACME setup's CA root field
    const box = (await field.boundingBox())!;
    await page.goto('/system/certificates');
    await page.getByRole('radio', {name: 'ACME'}).check();
    await page.locator('select').first().selectOption('custom');
    const acme = page.locator('#ol-cert-ca-root');
    await expect(acme).toBeVisible();
    const acmeBox = (await acme.boundingBox())!;
    expect(Math.abs(box.height - acmeBox.height)).toBeLessThanOrEqual(1);
    // the ACME field is what its form leaves beside the label column, so its width moves a little
    // with the language; ours is the OIDC form's field width, 520 px on a desktop, full width on a phone
    // (occulited tasks 69, 71: both fields sit in their page's panel)
    expect(Math.abs(box.width - acmeBox.width)).toBeLessThanOrEqual(24);
    const acmeStyle = await acme.evaluate((e) => {
        const c = getComputedStyle(e);
        return {whiteSpace: c.whiteSpace, font: c.fontFamily, size: c.fontSize, line: c.lineHeight};
    });
    expect(acmeStyle).toEqual({whiteSpace: style.whiteSpace, font: style.font, size: style.size, line: style.line});
});
