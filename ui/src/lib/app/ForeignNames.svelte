<script lang="ts">
    /*
     * occulited B-84 (the maintainer, 2026-10-20: "never automatic"): the names in the metadata of
     * interfaces this system does not list right now (InterfacesList.xml) - an addon that is not
     * installed (CUxD), or a radio module that did not answer at the last start. Nothing removes
     * them at start any more (beta.10 had, and lost every HmIP name of a system whose stick missed
     * one boot); this panel under Control's unassigned channels lists them per interface with the
     * counts, and each interface goes only by its button, after the shell's question.
     */
    import {onMount} from 'svelte';
    import {api} from '../api';
    import {t} from '../i18n.svelte';
    import {ask} from '../dialog.svelte';
    import Help from '../Help.svelte';
    import {foreignSummary, type ForeignAnswer, type ForeignInterface} from './foreign';

    let {onchanged}: {onchanged?: () => void} = $props();
    let list = $state<ForeignInterface[]>([]);
    let busy = $state('');

    async function load() {
        try {
            const r = await api.get<ForeignAnswer>('/api/meta/v1/foreign');
            list = r.list_read ? r.interfaces : [];
        } catch {
            list = []; // an older system without the route, or no meta:read: nothing to offer
        }
    }
    onMount(() => void load());

    async function remove(f: ForeignInterface) {
        busy = f.interface;
        try {
            await ask({
                title: t('Remove the names of {iface}?', {iface: f.interface}),
                message: t('{summary} are removed from the metadata, with their rooms, functions and favorites.', {summary: foreignSummary(f, t)}) + '\n\n' + t('This system does not list {iface} right now. If its radio module or addon is only missing for the moment, keep them: they come back with it. A removal is undone only by a backup.', {iface: f.interface}),
                confirm: t('Remove'),
                danger: true,
                focusCancel: true,
                run: async () => {
                    await api.post('/api/meta/v1/foreign:remove', {interfaces: [f.interface]});
                    await load();
                    onchanged?.();
                },
            });
        } finally {
            busy = '';
        }
    }
</script>

{#if list.length}
    <section class="ol-panel app-foreign" data-app-foreign={list.length}>
        <h2 class="app-foreign-title">{t('Names without an interface')}<Help>{t('These names belong to interfaces this system does not list right now: an addon that is not installed (CUxD), or a radio module that did not answer at the last start. They are kept until they are removed here, and their channels are no tiles meanwhile.')}</Help></h2>
        <ul class="app-foreign-list">
            {#each list as f (f.interface)}
                <li class="app-foreign-row" data-app-foreign-iface={f.interface}>
                    <span class="app-foreign-what"><span class="app-foreign-iface" title={f.interface}>{f.interface}</span> <span class="ol-muted app-foreign-count" data-app-foreign-count={f.objects}>{foreignSummary(f, t)}</span></span>
                    <button type="button" class="hmm-button danger app-foreign-remove" disabled={busy !== ''} onclick={() => void remove(f)} data-action="foreign-remove">{t('Remove…')}</button>
                </li>
            {/each}
        </ul>
    </section>
{/if}

<style>
    .app-foreign { margin-top: 24px; }
    .app-foreign-title { font-size: 1.05em; display: flex; align-items: center; gap: 6px; }
    .app-foreign-list { list-style: none; margin: 0; padding: 0; }
    /* the name and the counts wrap in their column; the button keeps its place at the right */
    .app-foreign-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 12px; min-height: 44px; padding: 6px 0; border-top: 1px solid var(--hmm-border-muted); }
    .app-foreign-row:first-child { border-top: 0; }
    .app-foreign-what { min-width: 0; overflow-wrap: anywhere; }
    .app-foreign-iface { font-weight: 600; margin-right: 8px; }
</style>
