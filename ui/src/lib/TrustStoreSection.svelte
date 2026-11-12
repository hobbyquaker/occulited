<script lang="ts">
    import {dateOnly} from './datefmt';
    /*
     * openccu-lite task 231: one trust store of the Trust stores page - its heading with the
     * explanation of who trusts it, Add certificate below the heading (task 222's toolbar row),
     * which grows into the page's panel (task 241's Disclosure) with a PEM field the size of the
     * ACME page's (task 235) and a file input for PEM or DER, a filter once the list is long (the
     * system bundle has some 150 entries), and the table: subject, issuer, validity with its
     * badges, source, a short fingerprint. Per row: Copy to… (the other stores - a copy, the stores
     * independent afterwards: the maintainer), Remove (in red where it can break TLS: the system
     * store, occulited's base set) or Trust again for a removed image certificate.
     */
    import {api} from './api';
    import {auth} from './auth.svelte';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Disclosure from './Disclosure.svelte';
    import Pins from './Pins.svelte';
    import SearchInput from './SearchInput.svelte';
    import SectionHead from './SectionHead.svelte';
    import {commonName, copyTargets, isPinPurpose, matches, removalIsDanger, shortFingerprint, sourceText, storeTitle, uploadBody, type PinFailure, type StoreID, type TrustCert, type TrustStore} from './trust';

    interface Props {
        store: TrustStore;
        stores: TrustStore[];
        /** reloads the whole view after a change (a copy changes another store too) */
        reload: () => Promise<void>;
        /** openccu-lite task 232: the page's Re-pin for this store, handed down to its pins */
        repin?: PinFailure | null;
    }
    let {store, stores, reload, repin = null}: Props = $props();
    const admin = $derived(auth.role === 'admin');
    const title = $derived(storeTitle(store.id, t));
    let addOpen = $state(false);
    let addButton = $state<HTMLButtonElement | null>(null);
    let text = $state('');
    let der = $state<{name: string; body: {der: string}} | null>(null);
    let filter = $state('');
    let busy = $state('');
    let notice = $state('');
    let error = $state('');

    const day = (s: string) => dateOnly(s);
    const targets = $derived(copyTargets(store.id, stores));
    const shown = $derived(store.certificates.filter((c) => matches(c, filter, sourceText(c, t, day))));
    const filterable = $derived(store.certificates.length > 8);

    const HELP: Record<StoreID, () => string> = {
        system: () => t('Every program on the system - the addons included - verifies servers against this bundle: the certificate authorities of the image plus what is added here. Removing one of the image\'s distrusts it. Both the additions and the removals are kept on the system and applied again at every start and after an update.'),
        occulited: () => t('occulited\'s own downloads - the addon catalogue and releases from GitHub, eQ-3\'s device firmware, the ACME directory - trust this store alone: the authorities those servers use today, shipped with the image, plus what is added here. When a server presents an authority this store lacks, the call fails and the Status page offers the copy from the System store.'),
        oidc: () => t('The certificates trusted for the login through the identity provider, besides the System store - the same list as under the OIDC settings on the Users page.'),
        acme: () => t('The certificates trusted for the ACME directory of a private CA (step-ca, a company CA), besides the occulited store - the CA root of the Certificate page is kept here.'),
    };
    const EMPTY: Record<StoreID, () => string> = {
        system: () => t('The bundle is empty or could not be read.'),
        occulited: () => t('No certificate: none of occulited\'s own downloads can verify its server.'),
        oidc: () => t('None: the provider\'s certificate must be one the system trusts by itself.'),
        acme: () => t('None: the ACME directory\'s certificate must be one the occulited store trusts by itself.'),
    };

    function fail(e: unknown) {
        error = (e as Error).message;
    }
    async function onFile(e: Event) {
        const input = e.currentTarget as HTMLInputElement;
        const f = input.files?.[0];
        if (!f) return;
        if (f.size > 256 * 1024) {
            error = t('The file is larger than a certificate file can be.');
            input.value = '';
            return;
        }
        const body = uploadBody(new Uint8Array(await f.arrayBuffer()));
        if ('pem' in body) {
            text = body.pem;
            der = null;
        } else {
            text = '';
            der = {name: f.name, body};
        }
        input.value = '';
    }
    async function add() {
        const body = text.trim() ? {pem: text} : der?.body;
        if (!body) return;
        busy = 'add';
        error = notice = '';
        try {
            const r = await api.post<{added: TrustCert[]}>(`/api/system/v1/trust/${store.id}`, body);
            notice = r.added.length === 1 ? t('{subject} is trusted in the {store} store.', {subject: commonName(r.added[0]?.subject ?? ''), store: title}) : t('{n} certificates are trusted in the {store} store.', {n: r.added.length, store: title});
            text = '';
            der = null;
            addOpen = false;
            await reload();
        } catch (e) {
            fail(e);
        } finally {
            busy = '';
        }
    }
    async function remove(c: TrustCert) {
        const danger = removalIsDanger(store.id, c);
        const message =
            store.id === 'system'
                ? t('Every program on the system stops trusting {subject} ({fingerprint}). Servers whose certificates chain to it can no longer be verified - by addons too.', {subject: commonName(c.subject), fingerprint: shortFingerprint(c.fingerprint)})
                : store.id === 'occulited'
                  ? t('occulited\'s own downloads stop trusting {subject} ({fingerprint}). A server that chains to it - GitHub, eQ-3 - cannot be reached until it is trusted again.', {subject: commonName(c.subject), fingerprint: shortFingerprint(c.fingerprint)})
                  : t('{subject} ({fingerprint}) is no longer trusted in the {store} store.', {subject: commonName(c.subject), fingerprint: shortFingerprint(c.fingerprint), store: title});
        if (!(await ask({title: t('Remove the certificate'), message, confirm: t('Remove'), danger}))) return;
        busy = c.id;
        error = notice = '';
        try {
            await api.del(`/api/system/v1/trust/${store.id}/${encodeURIComponent(c.id)}`);
            await reload();
        } catch (e) {
            fail(e);
        } finally {
            busy = '';
        }
    }
    async function restore(c: TrustCert) {
        busy = c.id;
        error = notice = '';
        try {
            await api.post(`/api/system/v1/trust/${store.id}/${encodeURIComponent(c.id)}/restore`);
            notice = t('{subject} is trusted again.', {subject: commonName(c.subject)});
            await reload();
        } catch (e) {
            fail(e);
        } finally {
            busy = '';
        }
    }
    async function copy(c: TrustCert, e: Event) {
        const sel = e.currentTarget as HTMLSelectElement;
        const to = sel.value as StoreID;
        sel.value = '';
        if (!to) return;
        const target = storeTitle(to, t);
        if (!(await ask({title: t('Copy the certificate'), message: t('{subject} ({fingerprint}) is copied into the {store} store. The stores stay independent: a later removal in one leaves the other.', {subject: commonName(c.subject), fingerprint: shortFingerprint(c.fingerprint), store: target}), confirm: t('Copy')}))) return;
        busy = c.id;
        error = notice = '';
        try {
            await api.post(`/api/system/v1/trust/${store.id}/${encodeURIComponent(c.id)}/copy`, {to});
            notice = t('{subject} is trusted in the {store} store.', {subject: commonName(c.subject), store: target});
            await reload();
        } catch (e) {
            fail(e);
        } finally {
            busy = '';
        }
    }
</script>

{#snippet addAction()}
    <!-- task 268: the button becomes the panel - hidden while it is open, back when it closes -->
    <button type="button" class="hmm-button" aria-expanded={addOpen} disabled={busy !== ''} onclick={() => (addOpen = true)} bind:this={addButton} data-trust-add={store.id}>{t('Add certificate')}</button>
{/snippet}
<section class="ts" data-trust-store={store.id}>
    <SectionHead id={store.id} title={title} help={HELP[store.id]()} actions={admin && store.editable ? addAction : undefined} />
    {#if error}<div class="ol-notice error" data-notice="trust-error">{error}</div>{/if}
    {#if notice}<div class="ol-notice" data-notice="trust-{store.id}">{notice}</div>{/if}
    <div class="ts-panelrow">
        <Disclosure title={t('Add a certificate to the {store} store', {store: title})} bind:open={addOpen} trigger={addButton}>
            {#snippet help()}{t('Paste the PEM text - one certificate or a chain - or choose a file: PEM (.pem, .crt) or DER (.der, .cer). A private key is refused; never paste one.')}{/snippet}
            <div class="ol-form ts-form">
                <label class="ts-paste">
                    <span>{t('PEM text')}</span>
                    <textarea class="hmm-input hmm-mono ol-pem" rows="30" bind:value={text} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false" data-trust-pem oninput={() => (der = null)}></textarea>
                </label>
                <div class="ol-form-buttons ts-buttons">
                    <input type="file" accept=".pem,.crt,.cer,.der,application/x-pem-file,application/x-x509-ca-cert,application/pkix-cert" onchange={onFile} aria-label={t('Certificate file')} data-trust-file />
                    {#if der}<span class="ol-muted" data-trust-der>{t('DER file {name}', {name: der.name})}</span>{/if}
                </div>
                <div class="ol-form-buttons">
                    <button type="button" class="hmm-button primary" onclick={add} disabled={busy !== '' || (!text.trim() && !der)} data-action="trust-add">{t('Add')}</button>
                </div>
            </div>
        </Disclosure>
    </div>
    {#if store.certificates.length === 0}
        <p class="ol-muted" data-trust-empty>{EMPTY[store.id]()}</p>
    {:else}
        {#if filterable}
            <div class="ol-toolbar ts-filter">
                <SearchInput bind:value={filter} placeholder={t('Filter')} label={t('Filter the {store} store', {store: title})} delay={0} />
                <span class="ol-muted">{filter.trim() ? t('{n} of {total} match', {n: shown.length, total: store.certificates.length}) : t('{n} certificates', {n: store.certificates.length})}</span>
            </div>
        {/if}
        <!-- task 267: one column layout for all four stores - the same widths whichever tab is open -->
        <table class="ol-table ol-stack ts-table" class:ts-admin={admin}>
            <thead>
                <tr>
                    <th class="ts-subject-h">{t('Subject')}</th>
                    <th class="ts-issuer-h">{t('Issuer')}</th>
                    <th class="ts-valid-h">{t('Valid until')}</th>
                    <th class="ts-source-h">{t('Source')}</th>
                    <th class="ts-fp-h">SHA-256</th>
                    {#if admin}<th class="ts-actions-h" aria-label={t('Actions')}></th>{/if}
                </tr>
            </thead>
            <tbody>
                {#each shown as c (c.id)}
                    <tr data-cert={c.id} class:ts-off={c.distrusted}>
                        <td class="ts-subject ol-stack-line">
                            <span class="ts-name" title={c.subject}>{commonName(c.subject)}</span>
                            {#if c.distrusted}<span class="ol-badge bad" data-badge="distrusted">{t('distrusted')}</span>{/if}
                            {#if !c.ca}<span class="ol-badge">{t('server certificate')}</span>{/if}
                        </td>
                        <td class="ts-issuer"><span class="ts-k">{t('Issuer')}:</span> <span class="ts-name" title={c.issuer}>{c.self_signed ? t('self-signed') : commonName(c.issuer)}</span></td>
                        <td class="ts-valid">
                            <span class="ts-k">{t('Valid until')}:</span>
                            {day(c.not_after)}
                            {#if c.expired}<span class="ol-badge bad">{t('expired')}</span>{:else if c.expires_soon}<span class="ol-badge warn">{t('expires within 30 days')}</span>{/if}
                        </td>
                        <td class="ts-source"><span class="ts-name" title={sourceText(c, t, day)}>{sourceText(c, t, day)}</span></td>
                        <td class="hmm-mono ts-fp" title={c.fingerprint} data-fingerprint>{shortFingerprint(c.fingerprint)}</td>
                        {#if admin}
                            <td class="ol-actions ts-actions">
                                <!-- B-246: one line at desktop widths - the row never wraps there, the dropdown gives way first -->
                                <div class="ts-actrow">
                                {#if targets.length && !c.distrusted}
                                    <select class="hmm-select ts-copy" aria-label={t('Copy {subject} to', {subject: commonName(c.subject)})} value="" onchange={(e) => copy(c, e)} disabled={busy !== ''} data-trust-copy>
                                        <option value="" disabled selected>{t('Copy to…')}</option>
                                        {#each targets as s (s.id)}<option value={s.id}>{storeTitle(s.id, t)}</option>{/each}
                                    </select>
                                {/if}
                                {#if c.distrusted}
                                    <button type="button" class="hmm-button" onclick={() => restore(c)} disabled={busy !== ''} data-action="trust-restore">{t('Trust again')}</button>
                                {:else if c.removable && store.editable}
                                    <button type="button" class="hmm-button" onclick={() => remove(c)} disabled={busy !== ''} data-action="trust-remove">{t('Remove')}</button>
                                {/if}
                                </div>
                            </td>
                        {/if}
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
    {#if isPinPurpose(store.id)}
        <!-- openccu-lite task 232: the purpose's pinned public keys, below its certificates -->
        <Pins purpose={store.id} pins={store.pins ?? []} {reload} {repin} level={2} />
    {/if}
</section>

<style>
    .ts { margin-bottom: 26px; }
    /* the panel that grows from the heading's button spans the table's width (task 199's row) */
    .ts-panelrow { margin-bottom: 8px; }
    .ts-panelrow :global(.ol-disclosure) { max-width: 760px; }
    .ts-paste { display: flex; flex-direction: column; gap: var(--ol-label-gap, 4px); }
    .ts-paste textarea { width: 100%; max-width: 520px; box-sizing: border-box; }
    .ts-buttons { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
    .ts-buttons input[type='file'] { max-width: 100%; }
    .ts-filter { margin: 4px 0 8px; }
    .ts-filter :global(.ol-search) { flex: 1 1 240px; max-width: 360px; }
    .ts-table td { vertical-align: baseline; overflow-wrap: anywhere; }
    /* task 267: fixed widths, the same in every store; a long name ends in an ellipsis, the whole
       name in its tooltip (the fingerprint's short form has its full one there already) */
    @media (min-width: 701px) {
        .ts-table { table-layout: fixed; }
        .ts-table th.ts-subject-h { width: 24%; }
        .ts-table th.ts-issuer-h { width: 16%; }
        .ts-table th.ts-valid-h { width: 14%; }
        .ts-table th.ts-source-h { width: 15%; }
        .ts-table th.ts-fp-h { width: 13em; }
        /* B-246: room for Copy to… and Remove side by side in both languages (the German pair
           measures 218 px at the table's 12 px font); 15em broke them onto two lines */
        .ts-table.ts-admin th.ts-actions-h { width: 20em; }
        .ts-table .ts-name { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .ts-table td.ts-fp { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
        .ts-actrow { display: flex; flex-wrap: nowrap; gap: 6px; align-items: center; white-space: nowrap; }
        .ts-actrow .ts-copy { flex: 0 1 auto; min-width: 5em; }
        .ts-actrow .hmm-button { flex: none; }
    }
    .ts-subject .ts-name { font-weight: 600; }
    .ts-off .ts-subject .ts-name, .ts-off .ts-issuer, .ts-off .ts-valid, .ts-off .ts-fp { color: var(--hmm-fg-muted); }
    .ts-fp { font-size: var(--hmm-font-size-small); }
    /* a table cell stays a cell on a wide window (app.css, td.ol-actions); the controls sit in a
       row of their own inside it, which the stacked phone row lets wrap */
    .ts-copy { max-width: 150px; }
    /* the stacked row's labels show only below 700 px, where the header row is gone */
    .ts-k { display: none; color: var(--hmm-fg-muted); }
    /* a tablet window (768 px) has no room for the fingerprint column beside the actions; the
       whole fingerprint stays in the row's title and in the copy and remove questions */
    @media (max-width: 900px) {
        .ts-table th.ts-fp-h, .ts-table td.ts-fp { display: none; }
    }
    @media (max-width: 700px) {
        .ts-k { display: inline; }
        .ts-table td.ts-fp { display: block; }
        .ts-actrow { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; width: 100%; }
    }
</style>
