<script lang="ts">
    /*
     * The service messages on the Status page (task 75; D-80: read-only, acknowledging is the
     * WebUI's). What occulited's store holds - the maintenance datapoints active on a device,
     * UNREACH, LOWBAT, CONFIG_PENDING, UPDATE_PENDING, the ERROR codes - with the device's name
     * from the metadata store and since when. A stream pushes the list to an open page the moment
     * it changes; without it the page asks every 30 s. The subscriber that feeds the store is
     * occulited's own (D-115), so there is no "not running" state to show.
     */
    import {onMount} from 'svelte';
    import {api, type ServiceMessagesView, type ServiceMessage} from './api';
    import {pageLife} from './pagelife.svelte';
    import {subscribeShell} from './shellstream/client';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import Icon, {type IconName} from './Icon.svelte';
    import {CAP, KINDS, SEVERITY, counts, kindOf as kindOfKey, loadFilter, rows, saveFilter, type DeviceRow, type Kind} from './servicemessages';

    let view = $state<ServiceMessagesView | null>(null);
    // 501: no store on this system (development, or an image without the RPC process) - the
    // section says nothing rather than something wrong
    let unsupported = $state(false);
    const life = pageLife();

    async function load() {
        try {
            view = await api.get<ServiceMessagesView>('/api/system/v1/service-messages');
        } catch (e) {
            if ((e as {status?: number}).status === 501) unsupported = true;
        }
    }
    // the topic service-messages of the shell's stream (occulited B-53: one stream for every window
    // of the browser), while the page is shown; the poll below covers a gap
    $effect(() => {
        if (!life.active) return;
        return subscribeShell('service-messages', (data) => {
            try {
                view = JSON.parse(data);
            } catch {
                /* a torn line: the next one or the poll repairs it */
            }
        });
    });
    onMount(() => {
        void load();
        const poll = setInterval(() => life.active && load(), 30000);
        const stopReturn = life.onReturn(() => void load());
        return () => {
            clearInterval(poll);
            stopReturn();
        };
    });

    // the CCU WebUI's words for the common ones; anything else is the datapoint's name
    const WORDS: Record<string, string> = {
        UNREACH: 'Communication disturbed',
        STICKY_UNREACH: 'Communication was disturbed',
        LOWBAT: 'Low battery',
        LOW_BAT: 'Low battery',
        CONFIG_PENDING: 'Configuration pending',
        UPDATE_PENDING: 'Update pending',
        SABOTAGE: 'Sabotage',
        DUTY_CYCLE: 'Duty cycle exceeded',
        ERROR_OVERHEAT: 'Overheated',
        ERROR_UNDERVOLTAGE: 'Undervoltage',
        ERROR_POWER_FAILURE: 'Power failure',
        FAULT_REPORTING: 'Fault',
    };
    function messageText(m: ServiceMessage): string {
        const w = WORDS[m.key];
        const base = w ? t(w) : m.key;
        // an enum or a number: the value is part of the message (a fault code)
        return typeof m.value === 'boolean' ? base : `${base} (${String(m.value)})`;
    }
    function deviceName(r: DeviceRow): string {
        return r.name || (r.type ? `${r.type} ${r.address}` : r.address);
    }
    function sinceText(r: DeviceRow): string {
        const newest = r.messages.find((m) => m.since === r.since) ?? r.messages[0];
        const when = new Date(r.since).toLocaleString();
        return newest?.seen === 'start' ? t('at least since {when}', {when}) : t('since {when}', {when});
    }
    // task 27: a symbol per kind, the colour by severity (colour is never the only signal)
    const ICON: Record<Kind, IconName> = {unreach: 'unlink', fault: 'alert', lowbat: 'battery', other: 'alert', config: 'settings', update: 'arrow-up', sticky: 'history'};
    const KIND_WORD: Record<Kind, string> = {
        unreach: 'Communication disturbed',
        fault: 'Fault',
        lowbat: 'Low battery',
        other: 'Other messages',
        config: 'Configuration pending',
        update: 'Update pending',
        sticky: 'Communication was disturbed',
    };
    const all = $derived(view ? rows(view.messages) : []);
    const perKind = $derived(counts(all));
    let filter = $state<Kind | ''>(loadFilter());
    let expanded = $state(false);
    const filtered = $derived(filter ? all.filter((r) => r.kinds.includes(filter as Kind)) : all);
    const shown = $derived(expanded ? filtered : filtered.slice(0, CAP));
    function pick(k: Kind) {
        filter = filter === k ? '' : k;
        saveFilter(filter);
    }
</script>

{#if !unsupported}
    <h2 id="service-messages">{t('Service messages')}{#if view}{' '}<span class="ol-muted sm-count" data-count={view.count}>· {view.count}</span>{/if}<Help>{t('The maintenance messages the devices report - communication disturbed, low battery, configuration pending, update pending, a fault - collected by the system itself from the interface processes, pushed to this page as they change. Acknowledging a message is done in the frontend addon.')}</Help></h2>
    {#if view}
        {#if view.count === 0}
            <div class="ol-muted sm-none" data-service-messages="none">{t('No service messages.')}</div>
        {:else}
            <div class="ol-card sm-card" data-service-messages={view.count}>
                {#if perKind.size > 1 || filter}
                    <div class="sm-filters" role="group" aria-label={t('Filter by kind')}>
                        {#each KINDS.filter((k) => perKind.has(k) || filter === k) as k (k)}
                            <button type="button" class="sm-chip sm-{SEVERITY[k]}" class:active={filter === k} aria-pressed={filter === k} data-filter={k} onclick={() => pick(k)}>
                                <Icon name={ICON[k]} size={13} />{t(KIND_WORD[k])} · {perKind.get(k) ?? 0}
                            </button>
                        {/each}
                    </div>
                {/if}
                <ul class="sm-list">
                    {#each shown as r (r.key)}
                        <li class="sm-item" data-device={`${r.interface}:${r.address}`}>
                            <div class="sm-main">
                                <div class="sm-device">{deviceName(r)}{#if r.enums?.length}<span class="ol-muted"> · {r.enums.map((e) => e.split('/').slice(-1)[0]).join(', ')}</span>{/if}</div>
                                <div class="sm-kinds">
                                    {#each r.messages as m (`${m.channel}:${m.key}`)}
                                        <span class="sm-chip sm-{SEVERITY[kindOfKey(m.key)]}" data-message={`${m.address}:${m.channel}:${m.key}`} data-kind={kindOfKey(m.key)} title={`${m.interface} ${m.address}:${m.channel}`}><Icon name={ICON[kindOfKey(m.key)]} size={13} /><span class="sm-text">{messageText(m)}</span></span>
                                    {/each}
                                </div>
                                <div class="ol-muted sm-meta"><span class="hmm-mono">{r.interface} {r.address}</span> · {sinceText(r)}</div>
                            </div>
                        </li>
                    {/each}
                </ul>
                {#if filtered.length > CAP}
                    <button type="button" class="hmm-button sm-more" data-show-all onclick={() => (expanded = !expanded)}>{expanded ? t('Show fewer') : t('Show all ({n})', {n: filtered.length})}</button>
                {/if}
            </div>
        {/if}
    {/if}
{/if}

<style>
    .sm-count { font-weight: normal; }
    .sm-none { margin: 0 0 14px; }
    .sm-card { margin-bottom: 14px; }
    .sm-filters { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 10px; }
    .sm-list { list-style: none; margin: 0; padding: 0; }
    .sm-item { padding: 8px 0; border-top: 1px solid var(--hmm-border-muted); }
    .sm-item:first-child { border-top: 0; padding-top: 0; }
    .sm-main { min-width: 0; }
    .sm-device { font-weight: 600; overflow-wrap: anywhere; }
    .sm-kinds { display: flex; flex-wrap: wrap; gap: 6px; margin: 4px 0 2px; }
    .sm-meta { font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    /* the chip: the symbol and the words in the severity's colour, a hairline in it too */
    .sm-chip { display: inline-flex; align-items: center; gap: 5px; padding: 1px 8px; border: 1px solid currentColor; border-radius: 999px; font-size: var(--hmm-font-size-small); line-height: 1.6; background: transparent; font: inherit; font-size: var(--hmm-font-size-small); }
    .sm-chip :global(svg) { flex: 0 0 auto; }
    button.sm-chip { cursor: pointer; }
    button.sm-chip.active { background: color-mix(in srgb, currentColor 16%, transparent); font-weight: 600; }
    .sm-red { color: var(--hmm-error); }
    .sm-orange { color: var(--hmm-warn); }
    .sm-blue { color: var(--hmm-accent); }
    .sm-grey { color: var(--hmm-fg-muted); }
    .sm-text { color: var(--hmm-fg, inherit); }
    .sm-more { margin-top: 8px; }
</style>
