<script lang="ts">
    import {dateOnly} from './datefmt';
    /*
     * openccu-lite task 230 (the maintainer: "shouldnt we have possibility to upload/paste pem for
     * trusting authentik server?"): the certificates trusted for the identity provider besides the
     * system's own - a private CA, a chain, or the provider's own certificate pinned - added by
     * upload or paste, shown with subject, issuer, validity and fingerprint, removable; a test of
     * the connection with them; and the issuer's own chain fetched for a confirmation by
     * fingerprint (trust on first use, explicit). They count for the login through the provider
     * only; nothing else trusts them. Every action applies at once, apart from the form's Save.
     */
    import {onMount} from 'svelte';
    import {api} from './api';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import Pins from './Pins.svelte';
    import {link} from './router.svelte';
    import {pinModeText, type Pin, type TrustView} from './trust';

    interface Anchor {
        id: string;
        purposes: string[];
        subject: string;
        issuer: string;
        not_before: string;
        not_after: string;
        fingerprint: string;
        ca: boolean;
        self_signed: boolean;
        names?: string[];
        expired?: boolean;
        expires_soon?: boolean;
        added?: string;
        added_by?: string;
    }
    interface PeerCert extends Anchor { pem: string; trusted: boolean }
    interface TestAnswer {
        ok: boolean;
        error?: string;
        tls: boolean;
        issuer?: string;
        issuer_mismatch?: boolean;
        token_endpoint?: string;
        verified_by?: Anchor & {trusted_here: boolean};
        /** openccu-lite task 232: what the server presented, the pin that matched */
        leaf?: Anchor;
        pinned?: Pin;
        pin_only?: boolean;
    }

    let {issuer = ''}: {issuer?: string} = $props();
    let anchors = $state<Anchor[] | null>(null);
    let error = $state('');
    let notice = $state('');
    let busy = $state('');
    let text = $state('');
    let test = $state<TestAnswer | null>(null);
    let chain = $state<{chain: PeerCert[]; verified: boolean; error: string} | null>(null);

    // openccu-lite task 232: the OIDC store's pins, from the Trust stores API (admins)
    let pins = $state<Pin[]>([]);
    let pinsRef = $state<Pins | null>(null);
    async function loadPins() {
        try {
            const v = await api.get<TrustView>('/api/system/v1/trust');
            pins = v.stores.find((s) => s.id === 'oidc')?.pins ?? [];
        } catch {
            pins = [];
        }
    }
    async function load() {
        try {
            anchors = (await api.get<{anchors: Anchor[]}>('/api/auth/v1/oidc/trust')).anchors;
        } catch (e) {
            error = (e as Error).message;
        }
        await loadPins();
    }
    onMount(load);

    const day = (s: string) => dateOnly(s);
    // a stored server certificate is pinned; one of the issuer's chain is only offered
    function kind(a: Anchor, stored = true): string {
        if (a.ca) return t('Certificate authority');
        if (!stored) return a.self_signed ? t('Server certificate, self-signed') : t('Server certificate');
        return a.self_signed ? t('Server certificate, self-signed (pinned)') : t('Server certificate (pinned)');
    }

    async function add(pem: string, fingerprint = '') {
        busy = 'add';
        error = notice = '';
        try {
            const r = await api.post<{anchors: Anchor[]; added: Anchor[]}>('/api/auth/v1/oidc/trust', fingerprint ? {pem, fingerprint} : {pem});
            anchors = r.anchors;
            notice = r.added.length === 1 ? t('The certificate is trusted for the identity provider.') : t('{n} certificates are trusted for the identity provider.', {n: r.added.length});
            text = '';
            test = null;
            if (chain) void fetchChain();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
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
        text = await f.text();
        input.value = '';
    }
    async function remove(a: Anchor) {
        if (!(await ask({title: t('Remove the certificate'), message: t('The identity provider\'s connection no longer trusts {subject} ({fingerprint}). A login through the provider fails if nothing else vouches for its certificate.', {subject: a.subject, fingerprint: a.fingerprint}), confirm: t('Remove'), danger: true}))) return;
        busy = 'remove';
        error = notice = '';
        try {
            anchors = (await api.del<{anchors: Anchor[]}>(`/api/auth/v1/oidc/trust/${encodeURIComponent(a.id)}`)).anchors;
            test = null;
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    async function runTest() {
        busy = 'test';
        error = '';
        test = null;
        try {
            test = await api.post<TestAnswer>('/api/auth/v1/oidc/test', {issuer});
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    async function fetchChain() {
        busy = 'fetch';
        error = '';
        try {
            chain = await api.post<{chain: PeerCert[]; verified: boolean; error: string}>('/api/auth/v1/oidc/peer-chain', {issuer});
        } catch (e) {
            chain = null;
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    async function trustPeer(c: PeerCert) {
        const message = [
            t('Trust {subject} for the identity provider?', {subject: c.subject}),
            t('Compare its SHA-256 fingerprint with the one your provider shows (in authentik: System → Certificates) before you trust it:'),
            c.fingerprint,
        ].join('\n\n');
        if (!(await ask({title: t('Trust this certificate'), message, confirm: t('Trust')}))) return;
        await add(c.pem, c.fingerprint);
    }
</script>

{#snippet certLines(a: Anchor)}
    <dl class="ol-kv ot-kv">
        <dt>{t('Subject')}</dt><dd class="hmm-mono">{a.subject || '–'}</dd>
        {#if !a.self_signed}<dt>{t('Issuer')}</dt><dd class="hmm-mono">{a.issuer}</dd>{/if}
        <dt>{t('Valid')}</dt>
        <dd>
            {t('{from} to {until}', {from: day(a.not_before), until: day(a.not_after)})}
            {#if a.expired}<span class="ol-badge bad">{t('expired')}</span>{:else if a.expires_soon}<span class="ol-badge warn">{t('expires within 30 days')}</span>{/if}
        </dd>
        <dt>SHA-256</dt><dd class="hmm-mono ot-fp" data-fingerprint>{a.fingerprint}</dd>
    </dl>
{/snippet}

<div class="ot" data-oidc-trust>
    <h3 id="oidc-trust">{t('Trusted certificates')}<Help>{t('A provider on the local network often has a certificate from a private CA or a self-signed one, which this system does not know. Add the CA, the chain or the provider\'s own certificate here: it is trusted for the login through the provider only, nothing else. Never a private key.')}</Help></h3>
    {#if error}<div class="ol-notice error" data-notice="oidc-trust-error">{error}</div>{/if}
    {#if notice}<div class="ol-notice" data-notice="oidc-trust">{notice}</div>{/if}
    <!-- openccu-lite task 231: the same list is the OAuth / OIDC store of the Trust stores page -->
    <p class="ol-muted ot-link">{t('The same list is the OAuth / OIDC store on')} <a href="/system/trust#oidc" use:link>{t('Trust stores')}</a>.</p>
    {#if anchors}
        {#if anchors.length === 0}
            <p class="ol-muted" data-oidc-trust-empty>{t('None: the provider\'s certificate must be one the system trusts by itself.')}</p>
        {:else}
            <ul class="ot-list">
                {#each anchors as a (a.id)}
                    <li class="ot-item" data-anchor={a.id}>
                        <div class="ot-head"><strong>{kind(a)}</strong><button type="button" class="hmm-button" onclick={() => remove(a)} disabled={busy !== ''}>{t('Remove')}</button></div>
                        {@render certLines(a)}
                    </li>
                {/each}
            </ul>
        {/if}
    {/if}

    <label class="ot-paste">
        <span>{t('Add a certificate: paste the PEM text or choose a file')}</span>
        <textarea class="hmm-input hmm-mono ol-pem" rows="30" bind:value={text} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false" data-oidc-trust-pem></textarea>
    </label>
    <div class="ol-form-buttons ot-buttons">
        <input type="file" accept=".pem,.crt,.cer,application/x-pem-file,application/x-x509-ca-cert" onchange={onFile} aria-label={t('Certificate file')} data-oidc-trust-file />
        <button type="button" class="hmm-button primary" onclick={() => add(text)} disabled={busy !== '' || !text.trim()}>{t('Trust')}</button>
    </div>

    <div class="ol-form-buttons ot-buttons">
        <button type="button" class="hmm-button" onclick={runTest} disabled={busy !== '' || !issuer} data-action="oidc-test">{t('Test the connection')}</button>
        <button type="button" class="hmm-button" onclick={fetchChain} disabled={busy !== '' || !issuer.startsWith('https://')} data-action="oidc-fetch">{t('Fetch the issuer\'s certificate')}</button>
        {#if busy === 'test' || busy === 'fetch'}<span class="ol-muted">{t('Asking {issuer} …', {issuer})}</span>{/if}
    </div>

    {#if test}
        <div class="ol-notice" class:error={!test.ok} data-oidc-test={test.ok ? 'ok' : 'failed'}>
            {#if test.ok}
                <strong>{t('The provider answers and announces its endpoints.')}</strong>
                {#if test.verified_by}
                    {test.verified_by.trusted_here ? t('TLS verified by {subject}, trusted here.', {subject: test.verified_by.subject}) : t('TLS verified by {subject} from the system\'s own trust store.', {subject: test.verified_by.subject})}
                {:else if !test.tls}
                    {t('Plain http: no certificate is checked, and the client secret travels in clear text.')}
                {/if}
                {#if test.pinned}
                    <span data-oidc-test-pinned>{test.pin_only ? t('The pinned key matched; it alone vouches for the connection (pin only).') : t('The pinned key matched ({mode}).', {mode: pinModeText(test.pinned.mode, t)})}</span>
                {:else if test.tls && test.leaf}
                    <!-- openccu-lite task 232: Pin the current certificate after a successful check -->
                    <button type="button" class="ol-textbutton" onclick={() => pinsRef?.pinCurrent(false)} data-action="oidc-pin-current">{t('Pin the current certificate…')}</button>
                {/if}
                {#if test.issuer_mismatch}<div class="ol-warn">{t('The provider calls itself {issuer}: use exactly that as the Issuer.', {issuer: test.issuer ?? ''})}</div>{/if}
            {:else}
                <strong>{t('The test failed:')}</strong> <span class="hmm-mono">{test.error}</span>
            {/if}
        </div>
    {/if}

    <!-- openccu-lite task 232: the pinned keys of the OIDC store, the same list as on the Trust stores page -->
    <Pins purpose="oidc" {pins} reload={loadPins} target={issuer} bind:this={pinsRef} />

    {#if chain}
        <div class="ot-chain" data-oidc-chain>
            <p class={chain.verified ? 'ol-muted' : 'ol-warn'}>{chain.verified ? t('The chain the issuer presents is trusted already.') : t('The chain the issuer presents is not trusted: {error}', {error: chain.error})}</p>
            <ul class="ot-list">
                {#each chain.chain as c (c.fingerprint)}
                    <li class="ot-item" data-peer={c.id}>
                        <div class="ot-head">
                            <strong>{kind(c, c.trusted)}</strong>
                            {#if c.trusted}<span class="ol-badge good">{t('trusted here')}</span>{:else}<button type="button" class="hmm-button" onclick={() => trustPeer(c)} disabled={busy !== ''}>{t('Trust this certificate…')}</button>{/if}
                        </div>
                        {@render certLines(c)}
                    </li>
                {/each}
            </ul>
        </div>
    {/if}
</div>

<style>
    .ot { margin: 16px 0 4px; max-width: 760px; }
    .ot-list { list-style: none; padding: 0; margin: 0 0 12px; display: flex; flex-direction: column; gap: 8px; }
    .ot-item { border: 1px solid var(--hmm-border-muted); border-radius: 6px; padding: 8px 10px; }
    .ot-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 4px; flex-wrap: wrap; }
    .ot-kv { gap: 2px 14px; margin: 0; }
    .ot-kv dd { overflow-wrap: anywhere; min-width: 0; }
    .ot-fp { font-size: var(--hmm-font-size-small); }
    .ot-paste { display: flex; flex-direction: column; gap: var(--ol-label-gap, 4px); }
    /* task 235: the ACME CA root field's size - its 30 rows (app.css's textarea.ol-pem), and the
       width of a form's field column: 520 px like the OIDC fields above it, where the ACME field is
       what its 640 px form leaves beside the label column (518 px in English) */
    .ot-paste textarea { width: 100%; max-width: 520px; box-sizing: border-box; }
    .ot-buttons { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; margin: 8px 0; }
    .ot-buttons input[type='file'] { max-width: 100%; }
    .ot-chain { margin-top: 8px; }
    .ot-link { margin: 0 0 8px; font-size: var(--hmm-font-size-small); }
    .ol-badge { margin-left: 8px; }
</style>
