<script lang="ts">
    import {dateTime} from './datefmt';
    /*
     * The lite-rpc streams (task 77; on the Remote access page, under lite-rpc's heading since
     * openccu-lite task 223): the clients reading the interface processes' events over the web port - SSE or WebSocket - with who they are (a token by name, or a session's user),
     * where from, since when, what they filter, how much they got, and an x that ends one. The
     * list is runtime state, asked every 10 s while the page is open.
     */
    import {onMount} from 'svelte';
    import {pageLife} from './pagelife.svelte';
    import {api, type LiteStreams, type LiteStream} from './api';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import SectionHead from './SectionHead.svelte';
    import Icon from './Icon.svelte';

    let {admin = false}: {admin?: boolean} = $props();
    let data = $state<LiteStreams | null>(null);
    let error = $state('');
    let closing = $state('');
    const life = pageLife();

    async function load() {
        try {
            data = await api.get<LiteStreams>('/api/rpc/v1/streams');
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(() => {
        void load();
        const poll = setInterval(() => life.active && load(), 10000);
        const stopReturn = life.onReturn(() => void load());
        return () => {
            clearInterval(poll);
            stopReturn();
        };
    });

    function who(s: LiteStream): string {
        return s.subject.kind === 'token' ? t('token {name}', {name: s.subject.name}) : t('session of {name}', {name: s.subject.name});
    }
    function filterText(s: LiteStream): string {
        const parts: string[] = [];
        for (const [k, v] of Object.entries(s.filter ?? {})) if (v && v.length) parts.push(`${k}=${v.join(',')}`);
        return parts.join(' ');
    }
    function sinceText(s: LiteStream): string {
        return dateTime(s.since);
    }
    function close(s: LiteStream) {
        void ask({
            title: t('End this stream?'),
            message: [t('The connection of {who} is closed. A running client reconnects on its own and resumes from its last event id; ending it here does not stop the client.', {who: who(s)}), `${s.transport === 'sse' ? 'SSE' : 'WebSocket'} · ${s.remote ?? ''} · ${sinceText(s)}`].join('\n\n'),
            confirm: t('End stream'),
            danger: true,
            focusCancel: true,
            run: async () => {
                closing = s.id;
                try {
                    await api.del(`/api/rpc/v1/streams/${encodeURIComponent(s.id)}`);
                    await load();
                    return null;
                } finally {
                    closing = '';
                }
            },
        });
    }
</script>

<SectionHead level={3} id="streams" title={t('Open streams')} help={t('The clients reading the interface processes\' events over the web port with lite-rpc, instead of a callback address the processes would have to reach. Runtime state: empty until a client connects.')} />
{#if error}<div class="ol-notice error">{error}</div>{/if}
{#if data}
    {#if data.streams.length === 0}
        <p class="ol-muted sc-none" data-streams="none">{t('No stream open ({per} per token or session, {total} in total).', {per: String(data.limits.per_token), total: String(data.limits.total)})}</p>
    {:else}
        <div class="ol-card sc-card" data-streams={data.streams.length}>
            <ul class="ol-subs">
                {#each data.streams as s (s.id)}
                    <li class="ol-sub" data-stream={s.id}>
                        <div class="ol-sub-main">
                            <div class="ol-sub-id"><strong>{who(s)}</strong><span class="ol-muted">{' · '}{s.transport === 'sse' ? 'SSE' : 'WebSocket'}{#if s.remote}{' · '}<span class="hmm-mono">{s.remote}</span>{/if}</span></div>
                            <div class="ol-muted ol-sub-meta">{t('since {when}', {when: sinceText(s)})} · {t('{n} messages', {n: String(s.sent)})}{#if filterText(s)}{' · '}<span class="hmm-mono">{filterText(s)}</span>{/if}{#if s.last_resync}<span class="ol-badge warn" title={t('The client had to resync: {reason}. It missed events it must read again itself.', {reason: s.last_resync})}>resync: {s.last_resync}</span>{/if}</div>
                        </div>
                        {#if admin}
                            <button type="button" class="hmm-button sc-remove" aria-label={t('End stream')} title={t('End stream')} disabled={closing !== ''} onclick={() => close(s)}><Icon name="x" size={14} /></button>
                        {/if}
                    </li>
                {/each}
            </ul>
        </div>
    {/if}
{/if}

<style>
    .sc-none { margin: 0 0 14px; }
    .sc-card { margin-bottom: 14px; }
    .ol-subs { list-style: none; margin: 0; padding: 0; min-width: 0; }
    .ol-sub { display: flex; align-items: center; gap: 8px; padding: 6px 0; border-top: 1px solid var(--hmm-border-muted); min-width: 0; }
    .ol-sub:first-child { border-top: 0; padding-top: 0; }
    .ol-sub-main { flex: 1 1 auto; min-width: 0; }
    .ol-sub-id { overflow-wrap: anywhere; }
    .ol-sub-meta { font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    .sc-remove { flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; width: 26px; height: 26px; padding: 0; }
    .sc-remove:not(:disabled):hover { color: var(--hmm-error); border-color: var(--hmm-error); }
    .ol-badge { margin-left: 6px; }
</style>
