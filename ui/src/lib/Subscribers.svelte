<script lang="ts">
    /*
     * The XML-RPC clients registered with each interface process (task 76), with whether their
     * callback accepts a connection (D-64) and a removal per row. The maintainer, 2026-09-19: on two
     * pages - the Interfaces page lists the clients on this system (scope internal), the Remote
     * access page those on the network (scope external, while classic RPC is on); each links to the
     * other. occulited task 84 (the maintainer): the Interfaces page lists every client, external ones
     * too (scope all), and every row's head is the Status page's (SubscriberLabel: the client's
     * name, the internal or external badge, the own one).
     */
    import {onMount} from 'svelte';
    import {link} from './router.svelte';
    import {pageLife} from './pagelife.svelte';
    import {api, type Radio, type InterfaceSubscriber, type FeedStatus} from './api';
    import {span} from './units';
    import {clientLabel, processAddress, processOrder, reachKey, streamDetail, streamName, streamPillTitle, streamsOf, type SubClient, type SubscriberReach, type SubSummary} from './subscribers';
    import {listenerName, stallSentence, stuck, type Stall, type StallListener} from './rpcstalls';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import SectionHead from './SectionHead.svelte';
    import Icon from './Icon.svelte';
    import Loading from './Loading.svelte';
    import SubscriberLabel from './SubscriberLabel.svelte';

    // feed: the RPC process's view of each interface (task 75) - when it last spoke, for the
    // card's head; the Interfaces page has it from /radio/health, the Remote access page not
    // level: an h3 on the Remote access page, where the clients are a part of classic RPC (task 223)
    // streams: /radio/health's subscribers, for lite-rpc's event streams (occulited task 87) - on
    // the Interfaces page, as rows of their own under occulited's registration, which they come
    // through, as the Status page lists them
    let {admin = false, scope, feed, level = 2, streams}: {admin?: boolean; scope: 'external' | 'all'; feed?: FeedStatus; level?: 2 | 3; streams?: Record<string, SubSummary>} = $props();
    type Row = {kind: 'sub'; s: InterfaceSubscriber} | {kind: 'stream'; c: SubClient; via: boolean};
    /** the callbacks, with the lite-rpc streams after occulited's own registration (at the end without one) */
    function rowsOf(name: string, subs: InterfaceSubscriber[]): Row[] {
        const st = scope === 'all' ? streamsOf(streams, name) : [];
        const out: Row[] = [];
        let placed = false;
        for (const s of subs) {
            out.push({kind: 'sub', s});
            if (s.own && !placed) {
                out.push(...st.map((c) => ({kind: 'stream' as const, c, via: true})));
                placed = true;
            }
        }
        if (!placed) out.push(...st.map((c) => ({kind: 'stream' as const, c, via: false})));
        return out;
    }
    const rowKey = (r: Row) => (r.kind === 'stream' ? `stream:${r.c.stream!.id}` : `${r.s.id} ${r.s.url}`);
    function lastSpoke(name: string): string {
        const i = feed?.interfaces.find((x) => x.name === name);
        if (!i || !i.last_activity || i.last_activity.startsWith('0001')) return '';
        const secs = Math.max(0, Math.round((Date.now() - Date.parse(i.last_activity)) / 1000));
        return t('last event {span} ago', {span: span(secs)});
    }
    // a client is on this system when its callback address is (the API's local)
    const mine = (s: InterfaceSubscriber) => scope === 'all' || !s.local;
    let radio = $state<Radio | null>(null);
    let error = $state('');
    type ProcessIf = Radio['interfaces'][number];
    const processCards = $derived(processOrder(radio?.interfaces ?? [], (radio?.modules ?? []).map((m) => m.protocol)));

    async function load() {
        try {
            radio = await api.get<Radio>('/api/system/v1/radio');
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    // B-201: an interface process held by a listener that never answers - on the Interfaces page,
    // where the Status page's warning links to (#stalls). The first check runs in the background;
    // the page asks again a few times until it has a result.
    let stalls = $state<Stall[]>([]);
    let stallTimer: ReturnType<typeof setTimeout> | undefined;
    async function loadStalls(check = false, tries = 8) {
        if (scope === 'external') return;
        clearTimeout(stallTimer);
        try {
            const r = await api.get<{stalls: Stall[]}>(`/api/system/v1/radio/subscribers/stalls${check ? '?check=1' : ''}`);
            stalls = r.stalls ?? [];
            if (tries > 0 && stalls.some((s) => !s.checked_at)) stallTimer = setTimeout(() => void loadStalls(false, tries - 1), 3000);
        } catch {
            // only a hint: the list below stands without it
        }
    }
    const life = pageLife();
    onMount(() => {
        void load();
        void loadStalls();
        // task 177: back from another page - who is registered now
        const off = life.onReturn(() => {
            void load();
            void loadStalls();
        });
        return () => {
            clearTimeout(stallTimer);
            off?.();
        };
    });
    let dropping = $state('');
    async function dropListener(s: Stall, l: StallListener) {
        const name = listenerName(l, t);
        const facts = [t('Address: {url}', {url: l.address})];
        if (l.id) facts.push(t('ID: {id}', {id: l.id}));
        if (l.owner) facts.push(t('User: {owner}', {owner: l.owner}));
        facts.push(l.id ? t('It is on {interface}\'s list of clients, and is removed from it as well.', {interface: s.interface}) : t('It is not on {interface}\'s list of clients: its registration never finished.', {interface: s.interface}));
        await ask({
            title: t('End the connection from {interface} to {name}?', {interface: s.interface, name}),
            message: [
                l.verdict === 'blocked'
                    ? t("{interface} keeps this client's events back and writes a warning to its log every second until the connection ends. Ending it makes the pending event fail, as it would for a client that is gone; the other clients are not affected.", {interface: s.interface})
                    : t('{interface} waits for this client\'s answer and does nothing else meanwhile. Ending the connection makes that call fail, as it would for a client that is gone, and {interface} goes on with the others.', {interface: s.interface}),
                t('The client itself is not stopped. If it registers again and still does not answer, the same happens again: then the addon or program behind it needs a look.'),
                facts.join('\n'),
            ].join('\n\n'),
            confirm: t('End connection'),
            danger: true,
            focusCancel: true,
            run: async () => {
                dropping = `${s.interface} ${l.address}`;
                try {
                    await api.post('/api/system/v1/radio/subscribers/drop', {interface: s.interface, address: l.address});
                    await Promise.all([load(), loadStalls(true)]);
                    return null;
                } catch (e) {
                    const status = (e as {status?: number}).status;
                    if (status === 409) return t("This system's image does not let it end another process's connection yet. A restart of the interface process on the Services page ends it too; that interrupts the radio for a moment.");
                    if (status === 422) await loadStalls(true);
                    throw e;
                } finally {
                    dropping = '';
                }
            },
        });
    }

    function setSubscribers(name: string, subs: InterfaceSubscriber[]) {
        if (!radio) return;
        radio = {...radio, interfaces: radio.interfaces.map((x) => (x.name === name ? {...x, subscribers: subs} : x))};
    }
    // task 76's follow-up (D-64): whether each subscriber's callback accepts a TCP connection, asked once
    // per page load - the system connects to every callback address, on the LAN too, so the page never
    // polls it. An entry that appears later is asked about once more; a removal asks nothing. A failed
    // request shows nothing: it is only a hint.
    let reach = $state<Record<string, SubscriberReach>>({});
    const probed = new Set<string>();
    $effect(() => {
        const keys = (radio?.interfaces ?? []).flatMap((i) => (i.subscribers ?? []).filter(mine).map((s) => reachKey(i.name, s.id, s.url)));
        const fresh = keys.filter((k) => !probed.has(k));
        if (fresh.length === 0) return;
        for (const k of fresh) probed.add(k);
        api.get<{subscribers: SubscriberReach[]}>('/api/system/v1/radio/subscribers/reachability').then(
            (r) => {
                const next = {...reach};
                for (const v of r.subscribers ?? []) next[reachKey(v.interface, v.id, v.url)] = v;
                reach = next;
            },
            () => undefined,
        );
    });
    /** the verdict when the callback did not accept the connection; undefined when it did or was not probed */
    function unreachable(iface: string, s: InterfaceSubscriber): SubscriberReach | undefined {
        const v = reach[reachKey(iface, s.id, s.url)];
        return v?.reachable === false ? v : undefined;
    }
    function reasonText(r: SubscriberReach['reason']): string {
        return r === 'refused' ? t('connection refused') : r === 'timeout' ? t('no answer within a second') : t('no route to the address');
    }
    // the removal runs inside the shell's dialog: it stays open when the process keeps the entry
    let removing = $state('');
    async function unsubscribe(i: ProcessIf, s: InterfaceSubscriber) {
        const facts = [t('ID: {id}', {id: s.id}), t('Address: {url}', {url: s.url}), s.local ? t('on this system') : t('on the network')];
        if (s.duplicate) facts.push(t('The same id is also registered with another address; one of the two is likely left over.'));
        const gone = unreachable(i.name, s);
        if (gone) facts.push(t('Not reachable when the page was opened ({reason}).', {reason: reasonText(gone.reason)}));
        await ask({
            title: t('Remove {id} from {interface}?', {id: s.id, interface: i.name}),
            message: [
                t('A subscription is an address {interface} sends every event to. Removing it makes the process stop sending events there.', {interface: i.name}),
                t('This helps when the client is gone, has moved to another address or port, or is listed twice.'),
                t('It does not help against a client that is still running: it registers again at its next start or re-init, and it may receive no events until then.'),
                facts.join('\n'),
            ].join('\n\n'),
            confirm: t('Remove subscription'),
            danger: true,
            focusCancel: true,
            run: async () => {
                removing = `${i.name} ${s.id} ${s.url}`;
                try {
                    const r = await api.post<{removed: boolean; subscribers: InterfaceSubscriber[]}>('/api/system/v1/radio/subscribers/remove', {interface: i.name, id: s.id, url: s.url});
                    setSubscribers(i.name, r.subscribers);
                    return r.removed ? null : t('The entry is still there: {interface} did not drop it. A restart of the interface process on the Services page removes it; that interrupts the radio for a moment and makes every running client register again. A reboot does the same.', {interface: i.name});
                } catch (e) {
                    // 422: the page was stale - show what the system has now, and the reason
                    if ((e as {status?: number}).status === 422) await load().catch(() => undefined);
                    throw e;
                } finally {
                    removing = '';
                }
            },
        });
    }
</script>

<SectionHead {level} id="subscribers" title={t('Registered clients')} help={scope === 'all' ? t('The XML-RPC clients registered with each interface process - addons and occulited on this system, and clients on the network. The event streams of lite-rpc are listed under the registration of occulited, which they come through. The list is runtime state and is empty until something subscribes.') : t('The XML-RPC clients on the network registered with each interface process through classic RPC. The list is runtime state and is empty until something subscribes.')} />
{#if scope === 'external'}<p class="ol-muted ol-subs-other" data-subscribers-link><a href="/system/interfaces#subscribers" use:link>{t('All clients: Interfaces')}</a></p>{/if}
{#if error}<div class="ol-notice error">{error}</div>{/if}
{#if stalls.length > 0}
    <div id="stalls" class="ol-stalls">
        {#each stalls as s (s.interface)}
            {@const held = stuck(s)}
            <div class="ol-notice error ol-stall" data-stall={s.interface}>
                <div>{stallSentence({interface: s.interface, kind: s.kind, checked: !!s.checked_at, stuck: held}, t)}</div>
                {#if held.length > 0}
                    <ul class="ol-stall-list">
                        {#each held as l (l.address || l.id)}
                            <li class="ol-stall-listener" data-listener={l.address || l.id}>
                                <span class="hmm-mono ol-stall-name" title={l.url ?? l.address}>{listenerName(l, t)}</span>
                                {#if l.verdict === 'blocked' && l.blocked_for}<span class="ol-muted ol-stall-held" data-held>{t('Held for {duration}', {duration: span(l.blocked_for)})}</span>{/if}
                                {#if admin && l.address}<button type="button" class="hmm-button ol-stall-drop" disabled={dropping !== ''} onclick={() => dropListener(s, l)}>{t('End connection')}</button>{/if}
                            </li>
                        {/each}
                    </ul>
                {/if}
            </div>
        {/each}
    </div>
{/if}
{#if !radio && !error}
    <Loading />
{:else if radio}
    <div class="ol-cards ol-subscribers">
        {#each processCards as i (i.name)}
            {@const subs = (i.subscribers ?? []).filter(mine)}
            {@const rows = rowsOf(i.name, subs)}
            <div class="ol-card ol-process" data-interface={i.name}>
                <div class="ol-card-head">
                    <span class="ol-card-icon"><Icon name="server" size={14} /></span>
                    <div class="ol-card-titles">
                        <div class="ol-card-title">{i.name}</div>
                        <div class="ol-card-sub" title={i.url}>{processAddress(i.url)}{i.info && i.info !== i.name ? ` · ${i.info}` : ''}</div>
                    </div>
                    <div class="ol-sub-headend"><span class="ol-muted ol-sub-count" data-sub-count={rows.length}>{rows.length === 1 ? t('1 subscriber') : t('{n} subscribers', {n: String(rows.length)})}</span>{#if lastSpoke(i.name)}<span class="ol-muted ol-sub-last" data-last-event={i.name}>· {lastSpoke(i.name)}</span>{/if}</div>
                </div>
                {#if rows.length === 0}
                    <div class="ol-muted ol-subs-empty">{t('no subscribers')}</div>
                {:else}
                    <!-- an interface process keeps a client's old registration beside its new one:
                         the same id twice, with two callbacks. The pair is unique. -->
                    <ul class="ol-subs">
                        {#each rows as r (rowKey(r))}
                            {#if r.kind === 'stream'}
                            <li class="ol-sub ol-sub-stream" class:via={r.via} data-stream={r.c.stream!.id}>
                                <div class="ol-sub-main">
                                    <div class="ol-sub-id"><SubscriberLabel id={streamName(r.c, t)} label="" internal={r.c.internal} own={false} stream={streamPillTitle(r.c, t)} title={[streamName(r.c, t), streamDetail(r.c)].filter(Boolean).join(' · ')} /></div>
                                    <div class="hmm-mono ol-sub-url" title={streamDetail(r.c)}>{streamDetail(r.c)}</div>
                                </div>
                            </li>
                            {:else}
                            {@const s = r.s}
                            {@const gone = unreachable(i.name, s)}
                            <li class="ol-sub">
                                <div class="ol-sub-main">
                                    <div class="ol-sub-id"><SubscriberLabel id={s.id} label={clientLabel(s.id, s.own)} internal={s.local} own={s.own} /></div>
                                    <div class="hmm-mono ol-sub-url" title={s.url}>{s.url}</div>
                                    {#if s.duplicate || gone}<div class="ol-muted ol-sub-meta">{#if s.duplicate}<span class="ol-badge warn" title={t('The same id is also registered with another address; one of the two is likely left over.')}>{t('duplicate registration')}</span>{/if}{#if gone}<span class="ol-badge warn ol-sub-reach" title={t('This system could not connect to the address when the page was opened ({reason}). The client may be gone, or a firewall is in the way. Nothing is changed because of it.', {reason: reasonText(gone.reason)})}>{t('not reachable')}</span>{/if}</div>{/if}
                                </div>
                                {#if admin && !s.own}
                                    <button type="button" class="hmm-button ol-sub-remove" aria-label={t('Remove subscription')} title={t('Remove subscription')} disabled={removing !== ''} onclick={() => unsubscribe(i, s)}><Icon name="x" size={14} /></button>
                                {/if}
                            </li>
                            {/if}
                        {/each}
                    </ul>
                {/if}
            </div>
        {/each}
    </div>
{/if}

<style>
    .ol-subs-other { margin: -4px 0 8px; font-size: var(--hmm-font-size-small); }
    .ol-stalls { display: grid; gap: 8px; margin-bottom: 12px; }
    .ol-stall-list { list-style: none; margin: 8px 0 0; padding: 0; }
    .ol-stall-listener { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 4px 0; min-width: 0; }
    .ol-stall-name { flex: 1 1 12em; min-width: 0; overflow-wrap: anywhere; }
    .ol-stall-drop { flex: 0 0 auto; }
    .ol-subscribers { grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr)); margin-bottom: 14px; }
    .ol-process .ol-card-head { align-items: flex-start; }
    .ol-sub-headend { flex: 0 0 auto; display: flex; align-items: center; white-space: nowrap; font-size: var(--hmm-font-size-small); padding-top: 2px; }
    .ol-sub-last { white-space: nowrap; margin-left: 4px; }
    .ol-subs-empty { margin-top: 10px; }
    .ol-subs { list-style: none; margin: 10px 0 0; padding: 0; min-width: 0; }
    .ol-sub { display: flex; align-items: center; gap: 8px; padding: 6px 0; border-top: 1px solid var(--hmm-border-muted); min-width: 0; }
    .ol-sub-main { flex: 1 1 auto; min-width: 0; }
    .ol-sub-url { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .ol-sub-meta .ol-badge:first-child { margin-left: 0; }
    .ol-sub-url { font-size: var(--hmm-font-size-small); }
    .ol-sub-meta { font-size: var(--hmm-font-size-small); }
    .ol-sub-remove { flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; width: 26px; height: 26px; padding: 0; }
    .ol-sub-remove:not(:disabled):hover { color: var(--hmm-error); border-color: var(--hmm-error); }
    /* occulited task 87: a lite-rpc stream under occulited's registration, as on the Status page */
    .ol-sub-stream.via { padding-left: 14px; position: relative; }
    .ol-sub-stream.via::before { content: '↳'; position: absolute; left: 0; color: var(--hmm-fg-muted); }
</style>
