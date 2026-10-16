<script lang="ts">
    /*
     * Backup encryption (openccu-lite task 91; D-64, D-65, D-80): the Backup page's first section.
     * The state (encrypted or not, the recovery key's fingerprint and date, earlier keys), the
     * setup wizard - what it does, the key shown once with Copy / kit / print, the tick that the
     * key is stored elsewhere, then the confirmation that saves the recipient - and afterwards
     * "Test my recovery key" (with the kit again for a key that matches), a new key, and the switch.
     *
     * The key is made in this browser (lib/recoverykey.ts) and only its public recipient goes to
     * the system; on plain HTTP, where WebCrypto is missing, the system makes it and the page says
     * so. The system never has the key afterwards, so nothing here can show it again.
     */
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {ask} from './dialog.svelte';
    import {i18n, t} from './i18n.svelte';
    import Help from './Help.svelte';
    import {warnEdge, warnFor} from './systemmenu.svelte';
    import Notice from './Notice.svelte';
    import EmergencyKit from './EmergencyKit.svelte';
    import {scrollToAnchor} from './anchor';
    import {canGenerateLocally, emergencyKit, generateRecoveryKey, isoDate, kitFileName, typedKind} from './recoverykey';
    import {recoveryKeyError} from './keyerror';

    interface KeyView { fingerprint: string; created: string; retired?: string }
    export interface EncryptionView { enabled: boolean; recovery: KeyView | null; previous: KeyView[]; box: KeyView | null }

    interface Props {
        /** the page learns whether downloads are encrypted */
        onchange?: (view: EncryptionView) => void;
    }
    let {onchange}: Props = $props();

    const BASE = '/api/system/v1/backup/encryption';

    let view = $state<EncryptionView | null>(null);
    let loadError = $state('');
    let unsupported = $state(false);
    let busy = $state('');
    let error = $state('');
    let hostname = $state('');

    async function load() {
        try {
            view = await api.get<EncryptionView>(BASE);
            loadError = '';
            onchange?.(view);
        } catch (e) {
            if (e instanceof ApiError && e.status === 501) unsupported = true;
            else loadError = (e as Error).message;
        }
    }
    onMount(() => {
        void load();
        void api.get<{hostname: string}>('/api/system/v1/status').then((s) => (hostname = s.hostname || location.hostname)).catch(() => (hostname = location.hostname));
        scrollToAnchor('encryption');
    });

    // --- the wizard ---
    type Step = 'intro' | 'key' | 'confirm';
    interface Wizard {
        step: Step;
        rotation: boolean;
        code: string;
        identity: string;
        fingerprint: string;
        pendingId: string;
        /** the system made the key (no WebCrypto here): it crossed the network once */
        madeOnSystem: boolean;
        stored: boolean;
        copied: boolean;
    }
    let wizard = $state<Wizard | null>(null);

    function startWizard(rotation: boolean) {
        error = '';
        wizard = {step: 'intro', rotation, code: '', identity: '', fingerprint: '', pendingId: '', madeOnSystem: false, stored: false, copied: false};
    }

    async function makeKey() {
        if (!wizard) return;
        busy = 'key';
        error = '';
        try {
            if (canGenerateLocally()) {
                const k = await generateRecoveryKey();
                const r = await api.post<{pending_id: string; fingerprint: string}>(BASE + '/recovery', {recipient: k.recipient});
                wizard = {...wizard, step: 'key', code: k.code, identity: k.identity, fingerprint: r.fingerprint, pendingId: r.pending_id};
            } else {
                const r = await api.post<{pending_id: string; fingerprint: string; recovery_key: string}>(BASE + '/recovery', {generate: true});
                // the identity for the kit's PC section: the system derives it; the code is already there
                let identity = '';
                try {
                    identity = (await api.post<{identity: string}>(BASE + '/test', {recovery_key: r.recovery_key})).identity;
                } catch {
                    /* the kit then has no PC section */
                }
                wizard = {...wizard, step: 'key', code: r.recovery_key, identity, fingerprint: r.fingerprint, pendingId: r.pending_id, madeOnSystem: true};
            }
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function confirm() {
        if (!wizard || !wizard.stored) return;
        busy = 'confirm';
        error = '';
        try {
            view = await api.post<EncryptionView>(BASE + '/confirm', {pending_id: wizard.pendingId});
            onchange?.(view);
            wizard = null;
            done = true;
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    let done = $state(false);

    function abort() {
        wizard = null;
        error = '';
    }

    // --- the kit: text, download, print, copy ---
    function kitText(code: string, identity: string, fingerprint: string): string {
        return emergencyKit({host: hostname || location.hostname, url: location.origin + '/', created: isoDate(), fingerprint, code, identity, lang: i18n.language});
    }
    function downloadKit(code: string, identity: string, fingerprint: string) {
        const blob = new Blob([kitText(code, identity, fingerprint)], {type: 'text/plain;charset=utf-8'});
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = kitFileName(hostname || location.hostname, isoDate());
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
    }
    let sheet = $state<{text: string; code: string} | null>(null);
    function printKit(code: string, identity: string, fingerprint: string) {
        sheet = {text: kitText(code, identity, fingerprint), code};
    }
    async function copy(code: string) {
        try {
            await navigator.clipboard.writeText(code);
            if (wizard) wizard.copied = true;
        } catch {
            error = t('Copying failed here; select the key and copy it by hand.');
        }
    }

    // --- test my recovery key ---
    interface TestResult { matches: 'current' | 'previous' | 'none'; fingerprint: string; created?: string; retired?: string; identity: string; recovery_key: string }
    let testing = $state(false);
    let typed = $state('');
    let tested = $state<TestResult | null>(null);
    let testMsg = $state('');
    async function runTest() {
        tested = null;
        testMsg = '';
        const kind = typedKind(typed);
        if (kind === 'recipient') {
            testMsg = t('This is the public part (age1…); the recovery key is the code from the emergency kit or the line starting with AGE-SECRET-KEY-1.');
            return;
        }
        if (kind === 'pq') {
            testMsg = t('A post-quantum or plugin age identity is not supported here.');
            return;
        }
        busy = 'test';
        try {
            tested = await api.post<TestResult>(BASE + '/test', {recovery_key: typed});
        } catch (e) {
            testMsg = recoveryKeyError(e, t);
        } finally {
            busy = '';
        }
    }
    // --- the switch ---
    async function turnOff() {
        const ok = await ask({
            title: t('Turn off encryption'),
            message: t('New backups are then handed out unencrypted and hold every key of this system in plain. Backups made so far stay encrypted and still need the recovery key.'),
            confirm: t('Turn off'),
            danger: true,
        });
        if (!ok) return;
        await setEnabled(false);
    }
    async function setEnabled(on: boolean) {
        busy = 'switch';
        error = '';
        try {
            view = await api.put<EncryptionView>(BASE, {enabled: on});
            onchange?.(view);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    const when = (iso?: string) => (iso ? new Date(iso).toLocaleDateString() : '');
</script>

<h2 id="encryption">{t('Encryption')}<Help>{t("Backups hold every key of this system: the radio keys, the users' password hashes, tokens, certificates. Encrypted, a copy on a stick, a share or in a download folder is unreadable without a key. This system keeps a key of its own for its own backups; the recovery key is yours, for a restore on new hardware, and it is never stored here.")}</Help></h2>
<!-- occulited task 59: the section's panel, with the edge of the warning that leads here -->
<div class="ol-enc ol-panel {warnEdge(['backup-unencrypted'])}" data-section="encryption" {...warnFor(['backup-unencrypted'])}>
    {#if unsupported}
        <div class="ol-notice">{t('Backup encryption is not available on this system.')}</div>
    {:else if loadError}
        <div class="ol-notice err">{loadError}</div>
    {:else if view}
        {#if error}<div class="ol-notice err" role="alert">{error}</div>{/if}
        {#if wizard}
            <div class="ol-subblock ol-wizard" data-wizard={wizard.step}>
                {#if wizard.step === 'intro'}
                    <h3>{wizard.rotation ? t('Create a new recovery key') : t('Set up encryption')}</h3>
                    {#if wizard.rotation}
                        <p class="ol-warn">{t('Backups made so far need the current recovery key. Keep it until they are gone; the earlier key is remembered here by its fingerprint, so a restore can say which key a backup needs.')}</p>
                    {/if}
                    <ul>
                        <li>{t('Backups on sticks, shares and in downloads can only be read with a key.')}</li>
                        <li>{t("This system keeps its own key, so it restores its own backups without asking.")}</li>
                        <li>{t("On any other system, or once this system's storage is lost, only the recovery key opens them.")}</li>
                        <li>{t('The recovery key is made in this browser now and shown once. Store it in a password manager or print the emergency kit; the system never has it.')}</li>
                    </ul>
                    {#if !canGenerateLocally()}
                        <Notice kind="warning" id="encryption-http">{t('This page is not loaded over HTTPS, so the browser cannot make the key itself: the system makes it and shows it to you once, over this connection.')}</Notice>
                    {/if}
                    <div class="ol-actions">
                        <button type="button" class="hmm-button primary" data-action="encryption-make-key" disabled={busy !== ''} onclick={makeKey}>{t('Make the recovery key')}</button>
                        <button type="button" class="hmm-button" data-action="encryption-abort" onclick={abort}>{t('Cancel')}</button>
                    </div>
                {:else if wizard.step === 'key'}
                    <h3>{t('Your recovery key')}</h3>
                    <div class="ol-code" data-recovery-key><code>{wizard.code}</code></div>
                    <p class="ol-muted">{t('Fingerprint')}: <span class="hmm-mono">{wizard.fingerprint}</span></p>
                    <div class="ol-actions">
                        <button type="button" class="hmm-button" data-action="encryption-copy" onclick={() => copy(wizard!.code)}>{wizard.copied ? t('Copied') : t('Copy')}</button>
                        <button type="button" class="hmm-button" data-action="encryption-kit" onclick={() => downloadKit(wizard!.code, wizard!.identity, wizard!.fingerprint)}>{t('Download emergency kit')}</button>
                        <button type="button" class="hmm-button" data-action="encryption-print" onclick={() => printKit(wizard!.code, wizard!.identity, wizard!.fingerprint)}>{t('Print…')}</button>
                    </div>
                    <Notice kind="error" id="encryption-warning">{t("Without this recovery key, encrypted backups cannot be restored on another system, or on this one once its storage is lost. Nobody can recover the key - not openccu-lite, not the forum. Store it in a password manager or print the emergency kit.")}</Notice>
                    {#if wizard.madeOnSystem}
                        <Notice kind="warning" id="encryption-made-on-system">{t('The system made this key and sent it over this connection once; it forgets it when you confirm.')}</Notice>
                    {/if}
                    <div class="ol-actions">
                        <button type="button" class="hmm-button primary" data-action="encryption-next" onclick={() => (wizard!.step = 'confirm')}>{t('Continue')}</button>
                        <button type="button" class="hmm-button" data-action="encryption-abort" onclick={abort}>{t('Cancel')}</button>
                    </div>
                {:else}
                    <h3>{t('Confirmation')}</h3>
                    <label class="ol-tick"><input type="checkbox" bind:checked={wizard.stored} data-action="encryption-stored" /> {t('I have stored the recovery key outside this system')}</label>
                    <div class="ol-actions">
                        <button type="button" class="hmm-button primary" data-action="encryption-confirm" disabled={!wizard.stored || busy !== ''} onclick={confirm}>{wizard.rotation ? t('Use the new recovery key') : t('Turn on encryption')}</button>
                        <button type="button" class="hmm-button" data-action="encryption-back" onclick={() => (wizard!.step = 'key')}>{t('Back')}</button>
                        <button type="button" class="hmm-button" data-action="encryption-abort" onclick={abort}>{t('Cancel')}</button>
                    </div>
                    <p class="ol-muted">{t('Only now is the key saved on the system - its public half. Cancelling here leaves nothing behind.')}</p>
                {/if}
            </div>
        {:else}
            <p class="ol-state" data-encryption-state={view.enabled ? 'on' : view.recovery ? 'off' : 'none'}>
                {#if view.enabled && view.recovery}
                    <span class="ol-pill ok">{t('Encrypted')}</span> {t('recovery key {fp} from {date}', {fp: view.recovery.fingerprint, date: when(view.recovery.created)})}
                {:else if view.recovery}
                    <span class="ol-pill warn">{t('Not encrypted')}</span> {t('switched off; the recovery key {fp} from {date} is kept', {fp: view.recovery.fingerprint, date: when(view.recovery.created)})}
                {:else}
                    <span class="ol-pill warn">{t('Not encrypted')}</span> {t('Backups are handed out in plain until a recovery key is set up.')}
                {/if}
            </p>
            {#if done}<div class="ol-notice" role="status" data-notice="encryption-done">{t('Encryption is on. Downloads are .sbk.age files from now on; this system opens them with its own key, any other system asks for the recovery key.')}</div>{/if}
            {#if view.previous.length}
                <p class="ol-muted">{t('Earlier recovery keys, still needed for the backups made with them:')}
                    {#each view.previous as p (p.fingerprint)}<span class="hmm-mono ol-prev">{p.fingerprint} ({when(p.created)} – {when(p.retired)})</span>{/each}
                </p>
            {/if}
            <div class="ol-actions">
                {#if !view.recovery}
                    <button type="button" class="hmm-button primary" data-action="encryption-setup" disabled={busy !== ''} onclick={() => startWizard(false)}>{t('Set up encryption')}</button>
                {:else}
                    <button type="button" class="hmm-button" data-action="encryption-test" onclick={() => { testing = !testing; tested = null; testMsg = ''; }}>{t('Test my recovery key')}</button>
                    <button type="button" class="hmm-button" data-action="encryption-rotate" disabled={busy !== ''} onclick={() => startWizard(true)}>{t('Create a new recovery key')}</button>
                    {#if view.enabled}
                        <button type="button" class="hmm-button danger" data-action="encryption-off" disabled={busy !== ''} onclick={turnOff}>{t('Turn off encryption')}</button>
                    {:else}
                        <button type="button" class="hmm-button primary" data-action="encryption-on" disabled={busy !== ''} onclick={() => setEnabled(true)}>{t('Turn on encryption')}</button>
                    {/if}
                {/if}
            </div>
            {#if testing}
                <div class="ol-subblock ol-test" data-encryption-test>
                    <label>{t('Recovery key')} <input class="hmm-input hmm-mono ol-keyinput" bind:value={typed} autocomplete="off" spellcheck="false" placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX" data-input="recovery-key" /></label>
                    <div class="ol-actions">
                        <button type="button" class="hmm-button" data-action="encryption-run-test" disabled={busy !== '' || !typed.trim()} onclick={runTest}>{t('Check')}</button>
                    </div>
                    {#if testMsg}<div class="ol-notice err" role="alert" data-test-result="error">{testMsg}</div>{/if}
                    {#if tested}
                        <div class="ol-notice" role="status" data-test-result={tested.matches}>
                            {#if tested.matches === 'current'}
                                {t('This is the current recovery key ({fp}).', {fp: tested.fingerprint})}
                            {:else if tested.matches === 'previous'}
                                {t('This is an earlier recovery key ({fp}, {from} – {to}): backups from that time still need it; new ones do not.', {fp: tested.fingerprint, from: when(tested.created), to: when(tested.retired)})}
                            {:else}
                                {t('This key is not one this system knows ({fp}). It opens no backup made here.', {fp: tested.fingerprint})}
                            {/if}
                        </div>
                        {#if tested.matches !== 'none'}
                            <div class="ol-actions">
                                <button type="button" class="hmm-button" data-action="encryption-kit-again" onclick={() => downloadKit(tested!.recovery_key || typed.trim(), tested!.identity, tested!.fingerprint)}>{t('Download emergency kit')}</button>
                                <button type="button" class="hmm-button" data-action="encryption-print-again" onclick={() => printKit(tested!.recovery_key || typed.trim(), tested!.identity, tested!.fingerprint)}>{t('Print…')}</button>
                            </div>
                        {/if}
                    {/if}
                </div>
            {/if}
        {/if}
    {/if}
</div>

{#if sheet}
    <EmergencyKit text={sheet.text} code={sheet.code} onclose={() => (sheet = null)} />
{/if}

<style>
    .ol-enc p { max-width: 720px; }
    .ol-enc .ol-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
    .ol-enc h3 { margin: 12px 0 6px; font-size: 1.05em; }
    .ol-enc ul { max-width: 720px; padding-left: 1.2em; }
    .ol-enc li { margin: 4px 0; }
    .ol-code { margin: 8px 0; padding: 12px 14px; border-radius: var(--hmm-radius-card); background: var(--hmm-card-bg); border: 1px solid var(--hmm-border-muted); font-size: 1.25em; overflow-wrap: anywhere; }
    .ol-code code { font-family: ui-monospace, monospace; letter-spacing: 0.06em; }
    .ol-pill { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 0.85em; font-weight: 600; margin-right: 6px; }
    .ol-pill.ok { background: color-mix(in srgb, var(--hmm-ok) 20%, transparent); color: var(--hmm-ok); }
    .ol-pill.warn { background: color-mix(in srgb, var(--hmm-warn) 25%, transparent); color: var(--hmm-warn); }
    .ol-tick { display: block; margin: 8px 0; }
    .ol-keyinput { width: min(100%, 26em); }
    .ol-prev { display: inline-block; margin-right: 8px; }
    .ol-notice.err { border-left: 3px solid var(--hmm-error); }
    .ol-test label { display: block; }
</style>
