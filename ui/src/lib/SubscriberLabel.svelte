<script lang="ts">
    /*
     * occulited task 84 (the maintainer): one subscriber row head for the Status page's component
     * cards and the Interfaces and Remote access pages' registered clients - the id (or a stream's
     * holder), the client's name where the id tells it ("Homematic Manager", "hmipserver",
     * "occulited"), and the same badges: internal or external (external in the accent colour, task
     * 82) and the system's own registration. The text truncates with its tooltip; the badges keep
     * their box. occulited task 121: a lite-rpc stream carries a "lite-rpc" pill (`stream` is its
     * tooltip: the kind and the transport) instead of "(stream via occulited)" after its name.
     */
    import {t} from './i18n.svelte';

    let {id, label = '', internal, own = false, title, stream = ''}: {id: string; label?: string; internal: boolean; own?: boolean; title?: string; stream?: string} = $props();
</script>

<span class="ol-sublabel">
    <span class="ol-sublabel-text" title={title ?? (label ? `${id} · ${label}` : id)}><span class="hmm-mono">{id}</span>{#if label}<span class="ol-muted ol-sublabel-name">{' · '}{label}</span>{/if}</span>
    {#if stream}<span class="ol-badge" data-client-literpc title={stream}>lite-rpc</span>{/if}
    <span class="ol-badge" class:ol-badge-accent={!internal} data-client-kind={internal ? 'internal' : 'external'}>{internal ? t('internal') : t('external')}</span>
    {#if own}<span class="ol-badge" data-client-own title={t('The system\'s own subscriber: it collects the service messages and the radio module\'s levels, and registers itself again on its own.')}>{t('own')}</span>{/if}
</span>

<style>
    .ol-sublabel { display: flex; align-items: center; min-width: 0; }
    .ol-sublabel-text { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    /* one size on every page, whatever the row's text size: the badge is the same box on both */
    .ol-sublabel .ol-badge { flex: 0 0 auto; font-size: calc(var(--hmm-font-size-small) - 1px); }
</style>
