import {expect, test} from '@playwright/test';

// Task 35: the Certificate page renders the self-signed certificate, flips to ACME, shows the
// provider's fields for DNS-01, and saving is one PUT of the settings with the secrets as typed.
test('the page flips to ACME and saves the settings', async ({page}) => {
    await page.goto('/system/certificates');
    await expect(page.getByRole('heading', {level: 1, name: 'Certificate'})).toBeVisible();
    // the current certificate: self-signed, its names and expiry
    const cards = page.locator('.ol-cards').first();
    await expect(cards).toContainText('self-signed');
    await expect(cards).toContainText('openccu');
    await expect(cards).toContainText('192.0.2.119');
    await expect(cards).toContainText('days left');
    // the ACME form is not there until the mode is chosen
    // (by its id: its placeholder is the suggestion from the host name and the domain)
    await expect(page.locator('#ol-cert-names')).toHaveCount(0);
    await page.getByRole('radio', {name: 'ACME'}).check();
    await expect(page.locator('#ol-cert-names')).toBeVisible();
    await page.locator('#ol-cert-names').fill('ccu.example.org, ccu.lan');
    await page.getByPlaceholder('admin@example.org').fill('me@example.org');
    // DNS-01 shows the provider list; a provider its fields, the secret as a password field
    await page.getByRole('radio', {name: 'DNS-01', exact: true}).check();
    // the challenge radios keep a radio's size, each with its name on one line beside it (the
    // form's text-field width once stretched them into big circles with the name broken in two)
    const modeRadio = (await page.getByRole('radio', {name: 'ACME'}).boundingBox())!;
    for (const name of ['HTTP-01', 'DNS-01']) {
        const radio = page.getByRole('radio', {name, exact: true});
        const box = (await radio.boundingBox())!;
        expect(box.width, `${name} radio width`).toBeLessThanOrEqual(modeRadio.width + 1);
        const label = (await radio.locator('xpath=ancestor::label[1]').boundingBox())!;
        expect(label.height, `${name} label on one line`).toBeLessThan(28);
        expect(Math.abs(box.y + box.height / 2 - (label.y + label.height / 2)), `${name} radio beside its name`).toBeLessThan(6);
    }
    await page.locator('select').nth(1).selectOption('cloudflare');
    const token = page.locator('input[type="password"]').last();
    await expect(token).toBeVisible();
    await token.fill('cf-token-123');
    // task 51: the provider's note is behind the ? of the provider field
    await expect(page.getByText('not the global API key')).toHaveCount(0);
    await page.locator('label[for="ol-cert-provider"]').getByRole('button', {name: 'Help'}).click();
    await expect(page.getByRole('tooltip')).toContainText('not the global API key');

    const [req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/system/v1/certificate/settings')),
        page.locator('.cert-actions').getByRole('button', {name: 'Save'}).click(),
    ]);
    const body = req.postDataJSON();
    expect(body.mode).toBe('acme');
    expect(body.directory).toBe('letsencrypt');
    expect(body.names).toEqual(['ccu.example.org', 'ccu.lan']);
    expect(body.email).toBe('me@example.org');
    expect(body.challenge).toBe('dns-01');
    expect(body.dns_provider).toBe('cloudflare');
    expect(body.dns_credentials).toEqual({api_token: 'cf-token-123'});
    await expect(page.getByText('Saved.')).toBeVisible();
    // the answer says the secret is set and the field is empty again
    await expect(page.locator('input[type="password"]').last()).toHaveValue('');
    await expect(page.locator('input[type="password"]').last()).toHaveAttribute('placeholder', /leave empty to keep/);
    // the actions are there now, Renew not (nothing issued yet)
    await expect(page.getByRole('button', {name: 'Test'})).toBeVisible();
    await expect(page.getByRole('button', {name: 'Issue now'})).toBeVisible();
    await expect(page.getByRole('button', {name: 'Renew now'})).toHaveCount(0);
});

// issue #13: OVH - the endpoint is a choice (ovh-eu first), the application key plain, the
// application secret and the consumer key write-only: set after the save, an empty field keeps them
test('OVH: endpoint choice, key and the two secrets', async ({page}) => {
    await page.goto('/system/certificates');
    await page.getByRole('radio', {name: 'ACME'}).check();
    await page.locator('#ol-cert-names').fill('*.example.org, example.org');
    await page.getByRole('radio', {name: 'DNS-01', exact: true}).check();
    await page.locator('#ol-cert-provider').selectOption('ovh');
    const endpoint = page.getByRole('combobox', {name: 'Endpoint'});
    await expect(endpoint).toHaveValue('ovh-eu');
    await expect(endpoint.locator('option')).toHaveText(['ovh-eu', 'ovh-ca', 'ovh-us']);
    await endpoint.selectOption('ovh-ca');
    await page.getByRole('textbox', {name: 'Application key'}).fill('ak-123');
    const secret = page.getByLabel('Application secret');
    const consumer = page.getByLabel('Consumer key');
    await expect(secret).toHaveAttribute('type', 'password');
    await expect(consumer).toHaveAttribute('type', 'password');
    await secret.fill('as-456');
    await consumer.fill('ck-789');
    // the choice is as wide as the text fields beside it
    const eb = (await endpoint.boundingBox())!;
    const sb = (await secret.boundingBox())!;
    expect(Math.abs(eb.width - sb.width)).toBeLessThan(2);
    await page.locator('label[for="ol-cert-provider"]').getByRole('button', {name: 'Help'}).click();
    await expect(page.getByRole('tooltip')).toContainText('/domain/zone/*');

    const save = async () => {
        const [req] = await Promise.all([
            page.waitForRequest((r) => r.method() === 'PUT' && r.url().includes('/api/system/v1/certificate/settings')),
            page.locator('.cert-actions').getByRole('button', {name: 'Save'}).click(),
        ]);
        return req.postDataJSON();
    };
    let body = await save();
    expect(body.dns_provider).toBe('ovh');
    expect(body.names).toEqual(['*.example.org', 'example.org']);
    expect(body.dns_credentials).toEqual({endpoint: 'ovh-ca', application_key: 'ak-123', application_secret: 'as-456', consumer_key: 'ck-789'});
    await expect(page.getByText('Saved.')).toBeVisible();
    // the secrets are set and never shown: the fields are empty with the keep note, the key and the endpoint stay
    for (const f of [secret, consumer]) {
        await expect(f).toHaveValue('');
        await expect(f).toHaveAttribute('placeholder', /leave empty to keep/);
    }
    await expect(endpoint).toHaveValue('ovh-ca');
    await expect(page.getByRole('textbox', {name: 'Application key'})).toHaveValue('ak-123');
    // saving again sends the secrets empty, which keeps them on the system
    body = await save();
    expect(body.dns_credentials).toEqual({endpoint: 'ovh-ca', application_key: 'ak-123', application_secret: '', consumer_key: ''});
    await expect(page.getByRole('button', {name: 'Test'})).toBeVisible();
    await expect(page.getByRole('button', {name: 'Issue now'})).toBeVisible();
});

test('a test run shows its lines while it runs', async ({page}) => {
    await page.goto('/system/certificates');
    await page.getByRole('radio', {name: 'ACME'}).check();
    await page.locator('#ol-cert-names').fill('ccu.example.org');
    // the test saves the settings first, with the stored mode: the box is not switched to ACME
    const [put, req] = await Promise.all([
        page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/system/v1/certificate/settings')),
        page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/system/v1/certificate/test')),
        page.getByRole('button', {name: 'Test'}).click(),
    ]);
    expect(put.postDataJSON().mode).toBe('self-signed');
    expect(put.postDataJSON().names).toEqual(['ccu.example.org']);
    expect(req.method()).toBe('POST');
    // and the form stays on ACME
    await expect(page.getByRole('radio', {name: 'ACME'})).toBeChecked();
    await expect(page.getByRole('heading', {level: 2, name: /Running: Test/})).toBeVisible();
    await expect(page.locator('pre.cert-log')).toContainText('Obtaining bundled SAN certificate');
});

test('the Status page carries the certificate card', async ({page}) => {
    await page.goto('/');
    const card = page.locator('.ol-cert');
    await expect(card).toContainText('Certificate');
    await expect(card).toContainText('self-signed');
    await expect(card.locator('.ol-badge')).toHaveCount(0);
});
