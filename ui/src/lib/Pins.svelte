<script lang="ts">
    /*
     * openccu-lite task 232 (the maintainer, 2026-10-03: "only OIDC and ACME ... per pin, the admin
     * chooses whether the pin is checked in addition to the CA validation (the default) or instead
     * of it"): the pinned public keys of one purpose - the OAuth / OIDC or the ACME store - below
     * the store's certificates on the Trust stores page, and under the OIDC settings' trusted
     * certificates. The list: the certificate the pin was taken from (or the bare hash of a backup
     * pin), the key hash, the mode, when and by whom; Remove asks. Pin the current certificate
     * fetches the chain the configured server presents (unverified, for the comparison) and grows
     * into the panel: the chain's certificates to choose from, the leaf first, each with its
     * SHA-256 and key hash; the mode, pin only in red; Pin asks once more with the fingerprint.
     * Add a backup pin takes the hash of a key not in use yet. Re-pin (the page's pin failure
     * panel) opens the same panel with the stale pins to be replaced.
     */
    import {tick} from 'svelte';
    import {api} from './api';
    import {auth} from './auth.svelte';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Disclosure from './Disclosure.svelte';
    import SectionHead from './SectionHead.svelte';
    import {commonName, looksLikeSPKI, pinModeText, pinName, shortFingerprint, shortSPKI, storeTitle, type PeerAnswer, type PeerCert, type Pin, type PinFailure, type PinMode, type PinPurpose} from './trust';

    interface Props {
        purpose: PinPurpose;
        pins: Pin[];
        /** reloads the pins (and whatever else the caller shows) after a change */
        reload: () => Promise<void>;
        /** the server's URL when the form's is not saved yet (the OIDC settings' issuer); else the configured one */
        target?: string;
        /** the page's Re-pin: opens the panel to replace the pins with what this host presents now */
        repin?: PinFailure | null;
        /** h2 on the Trust stores page, h3 under the OIDC settings */
        level?: 2 | 3;
    }
    let {purpose, pins, reload, target = '', repin = null, level = 3}: Props = $props();
    const admin = $derived(auth.role === 'admin');
    const title = $derived(storeTitle(purpose, t));
    let open = $state(false);
    let trigger = $state<HTMLButtonElement | null>(null);
    let pinButton = $state<HTMLButtonElement | null>(null);
    let backupButton = $state<HTMLButtonElement | null>(null);
    /** peer: the fetched chain to pick from; hash: a typed key hash */
    let kind = $state<'peer' | 'hash'>('peer');
    let peer = $state<PeerAnswer | null>(null);
    let chosen = $state('');
    let hash = $state('');
    let mode = $state<PinMode>('with-ca');
    let replace = $state(false);
    let busy = $state('');
    let error = $state('');
    let notice = $state('');

    const day = (s: string) => new Date(s).toLocaleDateString();
    const chosenCert = $derived(peer?.chain.find((c) => c.fingerprint === chosen) ?? null);
    const canPin = $derived(kind === 'peer' ? !!chosenCert && !chosenCert.pinned : looksLikeSPKI(hash));

    const HELP: Record<PinPurpose, () => string> = {
        oidc: () => t('A pin is the hash of the identity provider\'s public key: the login only accepts a connection whose certificate chain carries one of the pinned keys, whatever the certificate authorities say. A pin survives a renewal with the same key; a backup pin for the next key makes a key rollover seamless. Per pin: checked in addition to the CA validation, or - pin only - instead of it, for a self-signed provider.'),
        acme: () => t('A pin is the hash of the ACME directory\'s public key: the certificate flow only accepts a connection whose certificate chain carries one of the pinned keys. Pins fit a private directory (step-ca, a company CA) whose key is yours to know; Let\'s Encrypt\'s keys change, and its Test goes to the staging directory. Per pin: checked in addition to the CA validation, or - pin only - instead of it.'),
    };

    /** the panel from the fetched chain; `replacing` for Re-pin */
    export async function pinCurrent(replacing = false): Promise<void> {
        busy = 'fetch';
        error = notice = '';
        try {
            const r = await api.post<PeerAnswer>(`/api/system/v1/trust/${purpose}/pins/peer`, target ? {url: target} : {});
            peer = r;
            kind = 'peer';
            chosen = r.chain[0]?.fingerprint ?? '';
            mode = replacing && pins.length && pins.every((p) => p.mode === 'only') ? 'only' : 'with-ca';
            replace = replacing;
            open = true;
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    function addBackup() {
        peer = null;
        kind = 'hash';
        hash = '';
        mode = 'with-ca';
        replace = false;
        error = notice = '';
        open = true;
    }
    async function pin() {
        const subject = kind === 'peer' ? commonName(chosenCert?.subject ?? '') : t('the key hash');
        const fingerprint = kind === 'peer' ? (chosenCert?.fingerprint ?? '') : '';
        const spki = kind === 'peer' ? (chosenCert?.spki ?? '') : hash.trim();
        const lines = [
            replace ? t('The {n} pins of the {store} store are replaced by this one.', {n: pins.length, store: title}) : t('Pin {subject} for the {store} store?', {subject, store: title}),
            mode === 'only' ? t('Pin only: the CA validation and the name check are skipped for a connection that presents this key. Only the key vouches for the server - compare the hashes with what the server\'s administrator shows you before you confirm.') : t('Compare the hashes with what the server\'s administrator shows you before you confirm. The CA validation still applies as well.'),
            fingerprint ? t('SHA-256 of the certificate: {fingerprint}', {fingerprint}) : '',
            t('SHA-256 of the public key: {spki}', {spki}),
        ].filter(Boolean);
        if (!(await ask({title: replace ? t('Re-pin') : t('Pin the public key'), message: lines.join('\n\n'), confirm: replace ? t('Re-pin') : t('Pin'), danger: mode === 'only'}))) return;
        busy = 'pin';
        error = notice = '';
        try {
            const body: Record<string, unknown> = kind === 'peer' ? {pem: chosenCert?.pem, mode, replace} : {spki, mode, replace};
            const r = await api.post<{pin: Pin; pins: Pin[]}>(`/api/system/v1/trust/${purpose}/pins`, body);
            notice = replace ? t('The {store} store pins {subject} now; the earlier pins are gone.', {store: title, subject: pinName(r.pin, t)}) : t('{subject} is pinned for the {store} store ({mode}).', {subject: pinName(r.pin, t), store: title, mode: pinModeText(r.pin.mode, t)});
            open = false;
            await reload();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    async function remove(p: Pin) {
        const last = pins.length === 1;
        const message = [
            t('{subject} ({spki}) is no longer pinned for the {store} store.', {subject: pinName(p, t), spki: shortSPKI(p.spki), store: title}),
            last ? t('It is the last pin: the CA validation alone applies again.') : '',
        ]
            .filter(Boolean)
            .join('\n\n');
        if (!(await ask({title: t('Remove the pin'), message, confirm: t('Remove'), danger: !last && p.mode === 'only'}))) return;
        busy = p.id;
        error = notice = '';
        try {
            await api.del(`/api/system/v1/trust/${purpose}/pins/${encodeURIComponent(p.id)}`);
            await reload();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    // the page's Re-pin: a failure handed in opens the panel for its host
    let lastRepin = $state<PinFailure | null>(null);
    $effect(() => {
        if (repin && repin !== lastRepin) {
            lastRepin = repin;
            trigger = pinButton;
            void tick().then(() => pinCurrent(true));
        }
    });
</script>

{#snippet actions()}
    <button type="button" class="hmm-button" disabled={busy !== ''} onclick={() => { trigger = pinButton; void pinCurrent(false); }} bind:this={pinButton} data-pin-current={purpose}>{t('Pin the current certificate…')}</button>
    <button type="button" class="hmm-button" disabled={busy !== ''} onclick={() => { trigger = backupButton; addBackup(); }} bind:this={backupButton} data-pin-backup={purpose}>{t('Add a backup pin…')}</button>
{/snippet}
<section class="pins" data-pins={purpose}>
    <SectionHead {level} id={`${purpose}-pins`} title={t('Pinned keys')} help={HELP[purpose]()} actions={admin ? actions : undefined} />
    {#if error}<div class="ol-notice error" data-notice="pins-error">{error}</div>{/if}
    {#if notice}<div class="ol-notice" data-notice="pins-{purpose}">{notice}</div>{/if}
    {#if busy === 'fetch'}<p class="ol-muted" data-pins-fetching>{t('Fetching the certificate the server presents…')}</p>{/if}
    <div class="pins-panelrow">
        <Disclosure title={replace ? t('Re-pin: the key the server presents now') : kind === 'peer' ? t('Pin the current certificate') : t('Add a backup pin')} bind:open {trigger}>
            {#snippet help()}{kind === 'peer' ? t('The chain the server presents, read without trusting it. Compare the hashes with what the server\'s administrator shows you; the leaf is the server\'s own certificate, a CA above it pins every certificate it issues.') : t('The SHA-256 of the next key\'s public key (SubjectPublicKeyInfo), base64 or hex - from the server\'s administrator, or from its next certificate with openssl. In place before the key is used, it makes the rollover seamless.')}{/snippet}
            <div class="ol-form pins-form">
                {#if kind === 'peer' && peer}
                    <p class={peer.verified ? 'ol-muted pins-state' : 'ol-warn pins-state'} data-pins-peer-state>
                        {#if peer.verified}
                            {peer.pin_only ? t('{host}: the connection verifies now - a pin alone vouches.', {host: peer.host}) : t('{host}: the connection verifies now.', {host: peer.host})}
                        {:else}
                            {t('{host}: the connection does not verify now: {error}', {host: peer.host, error: peer.error ?? ''})}
                        {/if}
                    </p>
                    <div class="pins-chain" role="radiogroup" aria-label={t('Certificate to pin')}>
                        {#each peer.chain as c, i (c.fingerprint)}
                            <label class="pins-cert" class:pins-pinned={c.pinned} data-pin-peer={i}>
                                <input type="radio" name="pins-chosen-{purpose}" value={c.fingerprint} bind:group={chosen} disabled={c.pinned} />
                                <span class="pins-certbody">
                                    <strong>{commonName(c.subject)}</strong>
                                    <span class="ol-badge">{i === 0 ? t('server certificate') : c.ca ? t('certificate authority') : t('intermediate')}</span>
                                    {#if c.pinned}<span class="ol-badge good" data-badge="pinned">{t('pinned')}</span>{/if}
                                    <span class="pins-line"><span class="ol-muted">{t('Valid until')}</span> {day(c.not_after)}{#if c.expired}<span class="ol-badge bad">{t('expired')}</span>{:else if c.expires_soon}<span class="ol-badge warn">{t('expires within 30 days')}</span>{/if}</span>
                                    <span class="pins-line hmm-mono" data-peer-fingerprint><span class="ol-muted">SHA-256</span> {c.fingerprint}</span>
                                    <span class="pins-line hmm-mono" data-peer-spki><span class="ol-muted">{t('Public key')}</span> {c.spki}</span>
                                </span>
                            </label>
                        {/each}
                    </div>
                {:else if kind === 'hash'}
                    <label class="pins-hash">
                        <span>{t('SHA-256 of the public key (base64 or hex)')}</span>
                        <input class="hmm-input hmm-mono" bind:value={hash} placeholder="sha256//…" spellcheck="false" autocomplete="off" data-pin-hash />
                    </label>
                {/if}
                <fieldset class="pins-mode">
                    <legend>{t('Checked')}</legend>
                    <label><input type="radio" name="pins-mode-{purpose}" value="with-ca" bind:group={mode} /> <strong>{t('In addition to the CA validation')}</strong> <span class="ol-muted">{t('(the default: the key must match and the chain must verify)')}</span></label>
                    <label><input type="radio" name="pins-mode-{purpose}" value="only" bind:group={mode} data-pin-mode-only /> <strong>{t('Pin only - instead of the CA validation')}</strong> <span class="ol-muted">{t('(a self-signed server, a CA nothing trusts; only the key vouches)')}</span></label>
                </fieldset>
                {#if pins.length}
                    <label class="pins-replace"><input type="checkbox" bind:checked={replace} data-pin-replace /> {t('Replace the {n} existing pins', {n: pins.length})}</label>
                {/if}
                <div class="ol-form-buttons">
                    <button type="button" class="hmm-button" class:primary={mode !== 'only'} class:danger={mode === 'only'} onclick={pin} disabled={busy !== '' || !canPin} data-action="pin">{replace ? t('Re-pin') : t('Pin')}</button>
                </div>
            </div>
        </Disclosure>
    </div>
    {#if pins.length === 0}
        <p class="ol-muted" data-pins-empty>{t('No pin: the CA validation alone applies.')}</p>
    {:else}
        <table class="ol-table ol-stack pins-table">
            <thead>
                <tr>
                    <th>{t('Certificate')}</th>
                    <th>{t('Public key')}</th>
                    <th>{t('Checked')}</th>
                    <th>{t('Added')}</th>
                    {#if admin}<th aria-label={t('Actions')}></th>{/if}
                </tr>
            </thead>
            <tbody>
                {#each pins as p (p.id)}
                    <tr data-pin={p.id}>
                        <td class="ol-stack-line">
                            <strong title={p.subject}>{pinName(p, t)}</strong>
                            {#if p.not_after}<span class="ol-muted pins-until">{t('until {day}', {day: day(p.not_after)})}</span>{/if}
                            {#if p.fingerprint}<span class="ol-muted hmm-mono pins-fp" title={p.fingerprint}>{shortFingerprint(p.fingerprint)}</span>{/if}
                        </td>
                        <td class="hmm-mono" title={p.spki} data-pin-spki>{shortSPKI(p.spki)}</td>
                        <td><span class="ol-badge" class:warn={p.mode === 'only'} data-pin-mode={p.mode}>{pinModeText(p.mode, t)}</span></td>
                        <td>{day(p.added)}{#if p.added_by}{' · '}{p.added_by}{/if}</td>
                        {#if admin}<td class="ol-actions"><button type="button" class="hmm-button" onclick={() => remove(p)} disabled={busy !== ''} data-action="pin-remove">{t('Remove')}</button></td>{/if}
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
</section>

<style>
    .pins { margin: 18px 0 8px; }
    .pins-panelrow { margin-bottom: 8px; }
    .pins-panelrow :global(.ol-disclosure) { max-width: 760px; }
    .pins-state { margin: 0 0 6px; overflow-wrap: anywhere; }
    .pins-chain { display: flex; flex-direction: column; gap: 8px; }
    .pins-cert { display: flex; gap: 10px; align-items: flex-start; border: 1px solid var(--hmm-border-muted); border-radius: 6px; padding: 8px 10px; cursor: pointer; }
    .pins-cert input { margin-top: 3px; }
    .pins-pinned { color: var(--hmm-fg-muted); cursor: default; }
    .pins-certbody { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
    .pins-line { font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    .pins-hash { display: flex; flex-direction: column; gap: var(--ol-label-gap, 4px); }
    .pins-hash input { width: 100%; max-width: 520px; box-sizing: border-box; }
    .pins-mode { border: 0; padding: 0; margin: 4px 0 0; display: flex; flex-direction: column; gap: 6px; }
    .pins-mode legend { padding: 0; margin-bottom: 4px; color: var(--hmm-fg-muted); }
    .pins-replace { display: block; margin-top: 4px; }
    .pins-table td { vertical-align: baseline; overflow-wrap: anywhere; }
    .pins-until, .pins-fp { margin-left: 8px; font-size: var(--hmm-font-size-small); }
    .pins-fp { display: inline-block; }
    @media (max-width: 700px) {
        .pins-fp { display: none; }
    }
</style>
