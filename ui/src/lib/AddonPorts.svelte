<script lang="ts">
    /*
     * D-47 (B-52), on the Addons page since task 157: the ports an installed addon declares
     * (runtime.ports in the catalogue), one switch each, closed by default. Opening one adds an
     * owned rule to the firewall (from 0/0, IPv4 and IPv6); closing it, or uninstalling the addon,
     * removes it. Applied at once - an addon's port is the user's decision here, not a rule edit.
     */
    import {onMount} from 'svelte';
    import {api} from './api';
    import {i18n, t} from './i18n.svelte';
    import Help from './Help.svelte';

    // occulited B-31: listening is the addon's own socket; held_by names another process on the port,
    // owner_unknown says a socket is open whose owner could not be read
    interface AddonPort { port: number; proto?: string; tls?: boolean; label?: Record<string, string>; listening: boolean; open: boolean; held_by?: {process?: string; unit?: string}; owner_unknown?: boolean }
    interface FwAddon { id: string; name: string; mode: string; ports: AddonPort[] }

    let {admin = false}: {admin?: boolean} = $props();
    let addons = $state<FwAddon[] | null>(null);
    let busy = $state(false);
    let notice = $state('');
    let error = $state('');

    async function load() {
        try {
            addons = await api.get<FwAddon[]>('/api/system/v1/firewall/addons');
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(() => void load());

    async function setPort(a: FwAddon, p: AddonPort, open: boolean) {
        busy = true;
        try {
            const want = a.ports.filter((x) => (x.port === p.port ? open : x.open)).map((x) => x.port);
            addons = await api.put<FwAddon[]>(`/api/system/v1/firewall/addons/${encodeURIComponent(a.id)}/ports`, {open: want});
            notice = t(open ? 'Port {port} of {addon} opened.' : 'Port {port} of {addon} closed.', {port: p.port, addon: a.name});
            error = '';
        } catch (e) {
            error = (e as Error).message;
            await load();
        } finally {
            busy = false;
        }
    }
</script>

{#if addons && addons.length > 0}
    <h2 id="addon-ports">{t('Addon ports')}<Help>{t('One switch per port an installed addon declares; off is closed, which is the default. Opening one adds a rule to the firewall that accepts the port from every address; the Firewall page shows it with the addon as its owner.')}</Help></h2>
    {#if error}<div class="ol-notice error">{error}</div>{/if}
    {#if notice}<div class="ol-notice">{notice}</div>{/if}
    <!-- six columns do not fit a phone; the table scrolls inside its own box, the page never sideways -->
    <div class="ol-scroll-x">
        <table class="ol-table ol-addon-ports">
            <thead><tr><th>{t('Addon')}</th><th>{t('Port')}</th><th>{t('Protocol')}</th><th>{t('Description')}</th><th>{t('Listening')}</th><th>{t('Access')}</th></tr></thead>
            <tbody>
                {#each addons as a (a.id)}
                    <!-- an addon may declare one port for TCP and for UDP -->
                    {#each a.ports as p (`${p.proto ?? ''} ${p.port}`)}
                        <tr>
                            <td>{a.name}</td>
                            <td class="hmm-mono">{p.port}</td>
                            <td class="hmm-mono">{p.proto ?? ''}</td>
                            <td>{p.label?.[i18n.language] ?? p.label?.en ?? ''}{#if p.tls}{' '}<span class="ol-badge good">TLS</span>{/if}</td>
                            <td data-listening={p.held_by ? 'held' : p.owner_unknown ? 'unknown' : p.listening ? 'yes' : 'no'} title={p.held_by?.unit ?? ''}>
                                {#if p.held_by}
                                    <span class="ol-dot warn"></span>{t('held by another process ({process})', {process: p.held_by.process || p.held_by.unit || '?'})}
                                {:else if p.owner_unknown}
                                    <span class="ol-dot warn"></span>{t('a socket is open (owner unknown)')}
                                {:else}
                                    <span class="ol-dot" class:ok={p.listening}></span>{p.listening ? t('listening') : t('not listening')}
                                {/if}
                            </td>
                            <td>
                                <label class="ol-port-switch"><input type="checkbox" checked={p.open} disabled={busy || !admin} onchange={(e) => setPort(a, p, (e.currentTarget as HTMLInputElement).checked)} /> {p.open ? t('open') : t('closed')}</label>
                            </td>
                        </tr>
                    {/each}
                {/each}
            </tbody>
        </table>
    </div>
{/if}

<style>
    .ol-port-switch { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    .ol-scroll-x { max-width: 100%; overflow-x: auto; }
    .ol-scroll-x td { white-space: nowrap; }
</style>
