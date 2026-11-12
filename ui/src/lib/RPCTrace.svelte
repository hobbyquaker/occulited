<script lang="ts">
    import {dateTime} from './datefmt';
    /*
     * The RPC trace's switch (task 79, D-79; on System → Remote access since openccu-lite task 224,
     * the last panel of the lite-rpc section - it was the Interfaces page's): every call between a client
     * or occulited and an interface process, every stream delivery, in the journal under the
     * tag rpc-trace. Off, on for a while (it switches itself off; a restart keeps the deadline),
     * or on permanently - with a warning on an ARM product whose journal persists to the SD
     * card, because a permanent trace writes a lot.
     */
    import {onMount} from 'svelte';
    import {link} from './router.svelte';
    import {pageLife} from './pagelife.svelte';
    import {scrollToAnchor} from './anchor';
    import {api, type RPCTraceView} from './api';
    import {askSelect} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';

    let {admin = false}: {admin?: boolean} = $props();
    let view = $state<RPCTraceView | null>(null);
    let error = $state('');
    let busy = $state(false);
    const life = pageLife();

    async function load() {
        try {
            const first = !view;
            view = await api.get<RPCTraceView>('/api/system/v1/rpc-trace');
            error = '';
            // task 224: /system/interfaces#rpc-trace (an old bookmark) lands here
            if (first) scrollToAnchor('rpc-trace');
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(() => {
        void load();
        const poll = setInterval(() => life.active && load(), 30000);
        const stopReturn = life.onReturn(() => void load());
        return () => {
            clearInterval(poll);
            stopReturn();
        };
    });

    // the choices: off, on for a while (the same steps as the Log page's time range), permanently
    const CHOICES: {value: string; label: string; minutes?: number}[] = [
        {value: 'off', label: 'off'},
        {value: 'm15', label: 'on for 15 minutes', minutes: 15},
        {value: 'h1', label: 'on for 1 hour', minutes: 60},
        {value: 'h6', label: 'on for 6 hours', minutes: 360},
        {value: 'd1', label: 'on for 24 hours', minutes: 1440},
        {value: 'd7', label: 'on for 7 days', minutes: 10080},
        {value: 'permanent', label: 'on permanently'},
    ];
    function stateText(v: RPCTraceView): string {
        if (v.mode === 'permanent') return t('on permanently');
        if (v.mode === 'until' && v.until) return t('on until {when}', {when: dateTime(v.until)});
        return t('off');
    }
    async function change() {
        if (!view) return;
        const current = view.mode === 'until' ? '' : view.mode;
        const parts = [t('Every call between a client or this system and an interface process, every stream delivery and the streams\' life go to the journal under the tag rpc-trace, at debug. Lines are capped at 8 kB; a token is named, never shown.')];
        if (view.sd_warning) parts.push(t('Warning: this system\'s journal persists on the SD card. A permanent trace writes constantly and wears the card; prefer a timed trace.'));
        const picked = await askSelect({
            title: t('RPC trace'),
            message: parts.join('\n\n'),
            select: {label: t('Trace'), options: CHOICES.map((c) => ({value: c.value, label: t(c.label)})), initial: current || 'h1'},
            confirm: t('Apply'),
        });
        if (picked === null) return;
        const c = CHOICES.find((x) => x.value === picked);
        if (!c) return;
        busy = true;
        try {
            view = await api.put<RPCTraceView>('/api/system/v1/rpc-trace', c.minutes ? {mode: 'until', minutes: c.minutes} : {mode: c.value});
            error = '';
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }
</script>

<!-- task 224: a panel of its own, full width like the lite-rpc panel, its controls under its heading -->
<section class="ol-panel" data-panel="rpc-trace">
    <h3 id="rpc-trace">{t('RPC trace')}<Help>{t('Every call between a client or this system and an interface process, every stream delivery and the streams\' life, as lines in the journal under the tag rpc-trace. Switch it on for a while to see why a client gets no events; the Log page shows it.')}</Help></h3>
    {#if error}<div class="ol-notice error">{error}</div>{/if}
    {#if view}
        <div class="rt-row" data-rpc-trace={view.on ? 'on' : 'off'}>
            {#if !view.available}
                <span class="ol-muted">{t('The RPC trace is not available on this system.')}</span>
            {:else}
                <span class="ol-dot" class:ok={view.on}></span><span data-rpc-trace-state>{stateText(view)}</span>
                {#if admin}<button type="button" class="hmm-button" onclick={change} disabled={busy}>{t('Change…')}</button>{/if}
                <a href="/system/log?tag=rpc-trace&severity=debug" use:link>{t('Open the trace in the Log')}</a>
            {/if}
        </div>
    {/if}
</section>

<style>
    .rt-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin: 0; }
</style>
