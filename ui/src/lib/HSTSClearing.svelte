<script lang="ts">
    import {dateOnly} from './datefmt';
    /*
     * Task 96 (D-64): while HSTS is off but the box still sends max-age=0, what that does and what the
     * user has to do - open the box once by its name in every browser, since a browser forgets the
     * entry only when it sees max-age=0. The names are links that open in a new tab. Used by the
     * Security page, the Certificate page before a switch to self-signed and the Status page's
     * system update before the way back.
     */
    import type {HTTPSView} from './api';
    import {t} from './i18n.svelte';
    import {clearingUntil, hstsNames} from './hsts';

    let {view, lead = ''}: {view: HTTPSView; lead?: string} = $props();
    const until = $derived(clearingUntil(view));
    const names = $derived(hstsNames(typeof location !== 'undefined' ? location.hostname : '', view));
</script>

<span class="ol-hsts-clearing">
    {#if lead}{lead}{' '}{/if}{until ? t('Browsers that visit until {date} forget HSTS for this system: it sends max-age=0 until then.', {date: dateOnly(until)}) : t('The system sends max-age=0, so browsers that visit forget HSTS for it.')}
    {#if names.length}{' '}{t('Open the system once by its name in every browser you use:')}{' '}{#each names as n, i (n)}{i ? ', ' : ''}<a class="hmm-mono" href={`https://${n}/`} target="_blank" rel="noopener">{n}</a>{/each}{/if}
</span>

<style>
    .ol-hsts-clearing a { overflow-wrap: anywhere; }
</style>
