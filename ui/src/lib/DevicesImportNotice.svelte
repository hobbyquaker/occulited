<script lang="ts">
    import {dateTime} from './datefmt';
    /**
     * openccu-lite task 275: after the device import from a backup whose HmIP identity belongs to
     * another radio module, hmipserver takes the identity over onto this system's module at its
     * start - the adapter exchange. This notice on the Interfaces page says how that went, from
     * the record the import left and hmipserver's files: pending (the previous module's identity
     * file is still there), done (this module's identity is written), rejected (HmIPExchange's
     * notice says why and what to do; this one steps back), no module. It offers the retry - a
     * restart of hmipserver, which attempts the exchange at every start - and the hint for battery
     * devices, which are re-keyed only when they wake up. The BidCos line says whether rfd runs
     * with the imported identity. Task 278: the record also says that a non-default BidCos key
     * came along, and task 296 whether its passphrase was confirmed at the import - the warning
     * again when it was not. Dismiss removes the record.
     */
    import {api, ApiError} from './api';
    import {t} from './i18n.svelte';
    import {onMount} from 'svelte';
    import {pageLife} from './pagelife.svelte';

    interface Record_ {
        at: string; file: string; version?: string;
        hmip: {from_sgtin?: string; to_sgtin?: string; to_module?: string; module_changed: boolean; local_key: boolean; devices: number};
        // task 296: key_check - the verdict on the backup's passphrase at the import
        bidcos_rf: {address?: string; serial?: string; devices: number; non_default_key: boolean; key_index: number; target_key_replaced: boolean; module?: string; key_check?: 'none' | 'match' | 'mismatch' | 'skipped'};
    }
    interface Outcome {
        hmip: {state: 'none' | 'pending' | 'done' | 'rejected' | 'no-module' | 'unknown'; cause?: string; module_now?: string; line?: string};
        bidcos_rf: {took?: boolean; interface?: string; connected: boolean; error?: string};
    }
    interface View { imported: boolean; record?: Record_; outcome?: Outcome; switching?: string; error?: string }

    let {admin = false, fatal = false, onretry}: {admin?: boolean; fatal?: boolean; onretry?: () => void} = $props();
    let view = $state<View | null>(null);
    let busy = $state('');
    let msg = $state('');
    let poll: ReturnType<typeof setTimeout> | undefined;
    const life = pageLife();

    async function load() {
        try {
            view = await api.get<View>('/api/system/v1/radio/import');
        } catch {
            view = null;
        }
        clearTimeout(poll);
        // the move takes a moment after the reboot; while it is pending or a restart runs, look again
        if (view?.imported && (view.outcome?.hmip.state === 'pending' || view.switching)) poll = setTimeout(() => life.active && void load(), 5000);
    }
    async function retry() {
        busy = 'retry';
        msg = '';
        try {
            await api.post('/api/system/v1/radio/import/retry', {});
            onretry?.();
            await load();
        } catch (e) {
            msg = e instanceof ApiError ? e.message : String(e);
        } finally {
            busy = '';
        }
    }
    async function dismiss() {
        busy = 'dismiss';
        msg = '';
        try {
            await api.del('/api/system/v1/radio/import');
            view = {imported: false};
        } catch (e) {
            msg = e instanceof ApiError ? e.message : String(e);
        } finally {
            busy = '';
        }
    }
    onMount(() => {
        void load();
        return () => clearTimeout(poll);
    });
    const when = (iso: string) => dateTime(iso);
</script>

{#if view?.imported && view.record && view.outcome}
    {@const r = view.record}
    {@const o = view.outcome}
    {@const hmipState = o.hmip.state}
    <div class={`ol-notice di ${hmipState === 'pending' || hmipState === 'unknown' ? 'warn' : ''}`} data-notice="devices-import" data-state={hmipState}>
        <p><strong>{t('Devices imported from a backup')}</strong> · {when(r.at)} · {r.file}</p>
        {#if r.hmip.module_changed}
            {#if hmipState === 'done'}
                <p data-hmip="done">{t('HmIP: hmipserver took the identity of module {from} over onto {to}. The {n} HmIP devices are re-keyed for this module; a battery device only when it wakes up - press a button on it if it stays silent, and give it a few hours.', {from: r.hmip.from_sgtin ?? '', to: o.hmip.module_now ?? r.hmip.to_sgtin ?? '', n: r.hmip.devices})}</p>
            {:else if hmipState === 'pending' || hmipState === 'unknown'}
                <p data-hmip="pending">{t('HmIP: the identity of module {from} is not on {to} yet. hmipserver attempts the move (the adapter exchange) at its start: offline in local key mode, else through eQ-3\'s key server, which needs an internet connection. If it does not come through, try again - that restarts HmIP-RF.', {from: r.hmip.from_sgtin ?? '', to: o.hmip.module_now ?? r.hmip.to_sgtin ?? ''})}</p>
                {#if view.switching}<p class="ol-muted" data-switching={view.switching}><span class="ol-dot starting"></span>{t('HmIP-RF is restarting and tries the move to this module again.')}</p>{/if}
                {#if view.error}<p class="ol-warn">{view.error}</p>{/if}
            {:else if hmipState === 'rejected' && o.hmip.cause === 'adapter-version'}
                <!-- openccu-lite B-289: hmipserver moved the identity without the network key - a failed move -->
                <p class="ol-warn" data-hmip="rejected" data-cause="adapter-version">{t('HmIP: the identity of module {from} was moved onto {to} without the network key - the module\'s firmware is below 2.8.0 and cannot take it. HmIP devices do not answer on this module, and eQ-3\'s key server refuses every move away from it. The way out is the saved files of module {from} (a kept identity or a backup); update the firmware of {to} (Updates page, radio firmware) before the network moves onto it again.', {from: r.hmip.from_sgtin ?? '', to: o.hmip.module_now ?? r.hmip.to_sgtin ?? ''})}</p>
            {:else if hmipState === 'rejected'}
                <p data-hmip="rejected">{t('HmIP: eQ-3\'s key server rejected the move of the identity of module {from} onto this module - see the notice above for the way out.', {from: r.hmip.from_sgtin ?? ''})}</p>
            {:else if hmipState === 'no-module'}
                <p data-hmip="no-module">{t('HmIP: the identity of module {from} is imported; this system has no HmIP module to take it over.', {from: r.hmip.from_sgtin ?? ''})}</p>
            {/if}
            {#if o.hmip.line}<p class="ol-muted ol-mono" data-hmip-line>{o.hmip.line}</p>{/if}
        {/if}
        {#if r.bidcos_rf.serial}
            {#if o.bidcos_rf.took === true}
                <p data-bidcos="took">{t('BidCos-RF: rfd runs with the imported identity (address {a}, serial {s}) on {module}{conn}.', {a: r.bidcos_rf.address ?? '', s: r.bidcos_rf.serial, module: r.bidcos_rf.module || o.bidcos_rf.interface || '', conn: o.bidcos_rf.connected ? '' : ' - ' + t('not connected')})}</p>
            {:else if o.bidcos_rf.took === false}
                <p class="ol-warn" data-bidcos="not-took">{t('BidCos-RF: rfd reports {iface}, not the imported identity (serial {s}). Check the ids file and rfd\'s log.', {iface: o.bidcos_rf.interface ?? '', s: r.bidcos_rf.serial})}</p>
            {:else if o.bidcos_rf.error}
                <p class="ol-muted" data-bidcos="unknown">{t('BidCos-RF: rfd did not answer ({error}).', {error: o.bidcos_rf.error})}</p>
            {/if}
            {#if r.bidcos_rf.non_default_key && (r.bidcos_rf.key_check === 'mismatch' || r.bidcos_rf.key_check === 'skipped')}
                <p class="ol-warn" data-bidcos="key-unconfirmed">{r.bidcos_rf.key_check === 'mismatch'
                    ? t('The backup\'s non-default BidCos security key came along; the passphrase entered at the import did not match it. Find it before you change the key, re-key or re-pair these devices, or restore onto a system with another key: without it, only a factory reset of every such device and pairing it again helps.')
                    : t('The backup\'s non-default BidCos security key came along; its passphrase was skipped at the import. Find it before you change the key, re-key or re-pair these devices, or restore onto a system with another key: without it, only a factory reset of every such device and pairing it again helps.')}</p>
            {:else if r.bidcos_rf.non_default_key}
                <p data-bidcos="key">{t('The backup\'s non-default BidCos security key came along{replaced}. Keep the other system\'s passphrase safe: it is needed to change the key later, or to pair a device that still holds it.', {replaced: r.bidcos_rf.target_key_replaced ? ' ' + t('and replaced this system\'s own key') : ''})}</p>
            {/if}
        {/if}
        {#if admin}
            <div class="ol-actions">
                {#if r.hmip.module_changed && (hmipState === 'pending' || hmipState === 'unknown') && !fatal}
                    <button type="button" class="hmm-button" disabled={!!busy || !!view.switching} onclick={retry} data-action="import-retry">{t('Try again')}</button>
                {/if}
                <button type="button" class="hmm-button" disabled={!!busy} onclick={dismiss} data-action="import-dismiss">{t('Dismiss')}</button>
            </div>
        {/if}
        {#if msg}<p class="ol-warn" data-notice="import-msg">{msg}</p>{/if}
    </div>
{/if}

<style>
    .di p { margin: 4px 0; max-width: 820px; }
    .di .ol-actions { margin-top: 8px; display: flex; gap: 8px; flex-wrap: wrap; }
    .di.warn { border-color: var(--hmm-warn); }
    .ol-mono { font-family: monospace; font-size: 0.9em; }
</style>
