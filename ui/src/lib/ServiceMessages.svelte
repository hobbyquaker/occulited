<script lang="ts">
    /*
     * The service messages on the Status page (task 75). What occulited's store holds - the maintenance datapoints active on a device,
     * UNREACH, LOWBAT, CONFIG_PENDING, UPDATE_PENDING, the ERROR codes - with the device's name
     * from the metadata store and since when. A stream pushes the list to an open page the moment
     * it changes; without it the page asks every 30 s. The subscriber that feeds the store is
     * occulited's own (D-115), so there is no "not running" state to show.
     *
     * openccu-lite task 82 (maintainer, 2026-10-06): a sticky message whose cause is gone ("was
     * unreachable" without "communication disturbed" beside it) is confirmed here, as the CCU's
     * "Bestätigen": false is written to its STICKY_* datapoint over lite-rpc (rpc:operate). The
     * interface process sends no event for that; occulited reads the channel back a second later
     * (B-46) and the stream takes the message away. Nothing else offers a button: a low battery or a
     * device still away cannot be acknowledged. There is no mute (Homematic Manager has one).
     */
    import {onMount} from 'svelte';
    import {api, type ServiceMessagesView, type ServiceMessage} from './api';
    import {pageLife} from './pagelife.svelte';
    import {subscribeShell} from './shellstream/client';
    import {auth} from './auth.svelte';
    import {ask} from './dialog.svelte';
    import {setValue} from './app/rpc';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import Icon, {type IconName} from './Icon.svelte';
    import {grow as growList} from './grow';
    import {CAP, KINDS, SEVERITY, allOn, confirmable, counts, kindOf as kindOfKey, messageLook, panelEdge, rows, toggleAll, toggleKind, visibleRows, type DeviceRow, type Kind} from './servicemessages';

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
    // task 27: a symbol per kind on the filter pills, the colour by severity (colour is never the
    // only signal); a message's own chip has the message's icon (task 43, messageLook)
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
    // occulited B-86: the heading and the card count devices, as the pills do - not datapoints (one
    // device can carry UNREACH and STICKY_UNREACH, LOWBAT, CONFIG_PENDING at once)
    const all = $derived(view ? rows(view.messages) : []);
    const perKind = $derived(counts(all));
    // task 53: the panel's edge in the colour of the most severe message, live with the list
    const edge = $derived(panelEdge(all));
    // occulited task 39 (the maintainer, 2026-10-13): collapsed at every page load - only the type
    // pills; each pill shows its messages, several at once; "Show all" turns them all on (or off).
    // Not kept across page loads on purpose: the panel starts collapsed, as asked.
    let on = $state<Kind[]>([]);
    let expanded = $state(false);
    const present = $derived(KINDS.filter((k) => perKind.has(k)));
    const filtered = $derived(visibleRows(all, on));
    const shown = $derived(expanded ? filtered : filtered.slice(0, CAP));
    const everyOn = $derived(allOn(on, present));
    // the list grows and shrinks; not for a reader who asked for less motion
    const reduced = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
    const grow = {duration: reduced ? 0 : 200};
    // task 82: confirming needs lite-rpc's operate tier
    const canConfirm = $derived(auth.level === 'operate' || auth.level === 'configure' || auth.level === 'administer');
    // per message: busy while the write is on its way, then waiting for the read-back, or the fault
    let confirming = $state<Record<string, {busy?: boolean; error?: string}>>({});
    async function confirm(m: ServiceMessage, reload = true) {
        const id = `${m.interface}:${m.address}:${m.channel}:${m.key}`;
        confirming[id] = {busy: true};
        try {
            await setValue(`${m.interface}.${m.address}:${m.channel}`, m.key, false);
            confirming[id] = {};
            // the stream brings the list without it; the read-back is a second after the write
            if (reload) setTimeout(() => void load(), 2000);
        } catch (e) {
            confirming[id] = {error: (e as Error).message || String(e)};
        }
    }
    // occulited task 115: every confirmable sticky message at once - the same write as one by one, one after the other;
    // a failure keeps its message and its error in the list (its kind is shown), the rest go on
    const stickies = $derived(view ? view.messages.filter((m) => confirmable(m, view!.messages.filter((o) => o.interface === m.interface && o.address === m.address))) : []);
    let confirmingAll = $state(false);
    let allFailed = $state(0);
    async function confirmAll() {
        const list = [...stickies];
        if (!list.length || confirmingAll) return;
        if (!(await ask({title: t('Confirm all'), message: list.length === 1 ? t('Confirm 1 message?') : t('Confirm {n} messages?', {n: list.length}), confirm: t('Confirm all')}))) return;
        confirmingAll = true;
        allFailed = 0;
        try {
            for (const m of list) await confirm(m, false);
        } finally {
            confirmingAll = false;
        }
        const failed = list.filter((m) => confirming[`${m.interface}:${m.address}:${m.channel}:${m.key}`]?.error);
        allFailed = failed.length;
        for (const m of failed) if (!on.includes(kindOfKey(m.key))) on = [...on, kindOfKey(m.key)];
        setTimeout(() => void load(), 2000);
    }
    function pick(k: Kind) {
        on = toggleKind(on, k);
    }
    function pickAll() {
        on = toggleAll(on, present);
    }
    // a picture that does not load leaves the placeholder
    let broken = $state<Record<string, boolean>>({});
</script>

{#if !unsupported}
    <h2 id="service-messages">{t('Service messages')}{#if view}{' '}<span class="ol-muted sm-count" data-count={all.length}>· {all.length}</span>{/if}<Help>{t('The maintenance messages the devices report - communication disturbed, low battery, configuration pending, update pending, a fault - collected by the system itself from the interface processes, pushed to this page as they change. A message that the device keeps after its cause is gone - communication was disturbed - is confirmed here, as on the CCU. The number counts the devices with a message; a device with messages of several kinds appears under each of them.')}</Help></h2>
    {#if view}
        {#if view.count === 0}
            <div class="ol-muted sm-none" data-service-messages="none">{t('No service messages.')}</div>
        {:else}
            <div class="ol-card sm-card" class:err={edge === 'err'} class:warn={edge === 'warn'} class:info={edge === 'info'} data-service-messages={all.length} data-open={on.length > 0 ? '1' : '0'} data-edge={edge || undefined}>
                <div class="sm-filters" role="group" aria-label={t('Show messages by kind')}>
                    {#each KINDS.filter((k) => perKind.has(k)) as k (k)}
                        <button type="button" class="sm-chip sm-pill sm-{SEVERITY[k]}" class:active={on.includes(k)} aria-pressed={on.includes(k)} data-filter={k} onclick={() => pick(k)}>
                            <Icon name={ICON[k]} size={14} /><span class="sm-steady" data-label={`${t(KIND_WORD[k])} · ${perKind.get(k) ?? 0}`}>{t(KIND_WORD[k])} · {perKind.get(k) ?? 0}</span>
                        </button>
                    {/each}
                    <button type="button" class="hmm-button sm-all" aria-pressed={everyOn} data-all onclick={pickAll}><span class="sm-steady" data-label={t('Show all')} data-alt={t('Hide all')}>{everyOn ? t('Hide all') : t('Show all')}</span></button>
                    <!-- task 115: always in the row, so nothing moves when the last sticky message goes or a new one comes; invisible without one -->
                    {#if canConfirm}
                        <button type="button" class="hmm-button sm-all sm-confirm-all" class:sm-gone={stickies.length === 0} aria-hidden={stickies.length === 0 ? 'true' : undefined} tabindex={stickies.length === 0 ? -1 : undefined} disabled={confirmingAll || stickies.length === 0} title={t('Acknowledge every message whose cause is gone on its device, as the CCU does')} onclick={() => void confirmAll()} data-confirm-all={stickies.length}>{t('Confirm all')}</button>
                    {/if}
                </div>
                {#if allFailed > 0}<div class="sm-error sm-all-failed" role="alert" data-confirm-all-failed={allFailed}>{t('{n} not confirmed; their errors are in the list.', {n: allFailed})}</div>{/if}
                {#if on.length > 0}
                    <!-- B-109: a slide without opacity - an animated opacity made the pictures blend with a transparent group -->
                    <div class="sm-body" transition:growList={grow}>
                        <ul class="sm-list">
                            {#each shown as r (r.key)}
                                <li class="sm-item" data-device={`${r.interface}:${r.address}`}>
                                    <div class="sm-pic" aria-hidden="true">
                                        {#if r.image && !broken[r.key]}
                                            <img class="ol-device-picture" src={r.image} alt="" width="40" height="40" loading="lazy" decoding="async" data-device-image onerror={() => (broken[r.key] = true)} />
                                        {:else}
                                            <span class="sm-nopic" data-device-image-none><Icon name="radio" size={18} /></span>
                                        {/if}
                                    </div>
                                    <div class="sm-main">
                                        <div class="sm-device">{deviceName(r)}{#if r.places.length}{' '}<span class="ol-muted sm-places">· {r.places.join(', ')}</span>{/if}</div>
                                        <div class="sm-kinds">
                                            {#each r.messages as m (`${m.channel}:${m.key}`)}
                                                {@const look = messageLook(m.key)}
                                                <span class="sm-chip sm-{look.tone}" data-message={`${m.address}:${m.channel}:${m.key}`} data-kind={kindOfKey(m.key)} data-icon={look.icon} title={`${m.interface} ${m.address}:${m.channel}`}><Icon name={look.icon} size={13} /><span class="sm-text">{messageText(m)}</span></span>
                                                {#if canConfirm && confirmable(m, r.messages)}
                                                    {@const c = confirming[`${m.interface}:${m.address}:${m.channel}:${m.key}`]}
                                                    <button type="button" class="hmm-button sm-confirm" data-confirm={`${m.address}:${m.channel}:${m.key}`} disabled={c?.busy} onclick={() => void confirm(m)} title={t('Acknowledge the message on the device, as the CCU does')}>{t('Confirm')}</button>
                                                    {#if c?.error}<span class="sm-error" role="alert" data-confirm-error>{t('Not confirmed: {error}', {error: c.error})}</span>{/if}
                                                {/if}
                                            {/each}
                                        </div>
                                        <div class="ol-muted sm-meta"><span class="hmm-mono">{r.interface} {r.address}</span> · {sinceText(r)}</div>
                                    </div>
                                </li>
                            {/each}
                        </ul>
                        {#if filtered.length > CAP}
                            <button type="button" class="hmm-button sm-more" data-more onclick={() => (expanded = !expanded)}>{expanded ? t('Show fewer') : t('Show every device ({n})', {n: filtered.length})}</button>
                        {/if}
                    </div>
                {/if}
            </div>
        {/if}
    {/if}
{/if}

<style>
    .sm-count { font-weight: normal; }
    .sm-none { margin: 0 0 14px; }
    .sm-card { margin-bottom: 14px; }
    .sm-filters { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
    .sm-body { margin-top: 12px; }
    /* task 39: the pills a bit higher - a comfortable touch target, all of one height */
    .sm-pill { min-height: 34px; padding: 4px 12px; gap: 6px; }
    .sm-all { min-height: 34px; padding: 4px 14px; border-radius: 999px; }
    /* task 115: the button keeps its place in the row without a sticky message, unseen */
    .sm-confirm-all.sm-gone { visibility: hidden; }
    .sm-all-failed { margin-top: 8px; }
    .sm-item { display: flex; gap: 12px; align-items: flex-start; }
    .sm-pic { flex: 0 0 40px; width: 40px; height: 40px; }
    .sm-pic img { display: block; width: 40px; height: 40px; object-fit: contain; }
    .sm-nopic { display: flex; width: 40px; height: 40px; align-items: center; justify-content: center; border-radius: 8px; background: var(--hmm-control-bg, transparent); color: var(--hmm-fg-muted); }
    .sm-list { list-style: none; margin: 0; padding: 0; }
    .sm-item { padding: 8px 0; border-top: 1px solid var(--hmm-border-muted); }
    .sm-item:first-child { border-top: 0; padding-top: 0; }
    .sm-main { min-width: 0; flex: 1 1 auto; }
    .sm-device { font-weight: 600; overflow-wrap: anywhere; }
    .sm-kinds { display: flex; flex-wrap: wrap; gap: 6px; margin: 4px 0 2px; }
    .sm-meta { font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    /* the chip: the symbol and the words in the severity's colour, a hairline in it too */
    .sm-chip { display: inline-flex; align-items: center; gap: 5px; padding: 1px 8px; border: 1px solid currentColor; border-radius: 999px; font-size: var(--hmm-font-size-small); line-height: 1.6; background: transparent; font: inherit; font-size: var(--hmm-font-size-small); }
    .sm-chip :global(svg) { flex: 0 0 auto; }
    button.sm-chip { cursor: pointer; }
    button.sm-chip.active { background: color-mix(in srgb, currentColor 16%, transparent); font-weight: 600; }
    /* B-72: a pill keeps its box when it turns on - its label is as wide as its bold self in both
       states (a hidden bold copy, no height), and "Show all" is as wide as "Hide all" */
    .sm-steady { display: inline-grid; }
    .sm-steady::before, .sm-steady::after { order: 1; visibility: hidden; height: 0; overflow: hidden; pointer-events: none; user-select: none; } /* after the label: its baseline stays the line's */
    .sm-steady::after { content: attr(data-label); font-weight: 600; }
    .sm-steady[data-alt]::after { font-weight: inherit; }
    .sm-steady[data-alt]::before { content: attr(data-alt); }
    .sm-red { color: var(--hmm-error); }
    .sm-orange { color: var(--hmm-warn); }
    .sm-blue { color: var(--hmm-accent); }
    .sm-grey { color: var(--hmm-fg-muted); }
    .sm-text { color: var(--hmm-fg, inherit); }
    .sm-more { margin-top: 8px; }
    .sm-confirm { padding: 0 10px; font-size: var(--hmm-font-size-small); line-height: 1.6; }
    .sm-error { color: var(--hmm-error); font-size: var(--hmm-font-size-small); align-self: center; }
</style>
