<script lang="ts" module>
    // openccu-lite task 399: the addons that snapshot their data for the backup (GET /backup/addons)
    export interface BackupHookResult { id: string; at: string; ok: boolean; exit?: number; error?: string; output?: string; timed_out?: boolean; bytes: number; files: number; duration_ms: number }
    export interface BackupHookAddon { id: string; name: string | {de?: string; en?: string}; hook: string; staging: string; restore?: string; timeout: number; size_hint_mb: number; large: boolean; enabled: boolean; chosen?: boolean; last?: BackupHookResult }
    export interface BackupAddonsView { supported: boolean; large_mb: number; addons: BackupHookAddon[] }
</script>

<script lang="ts">
    /*
     * openccu-lite task 399 (the maintainer, 2026-10-28): an addon whose manifest declares a backup
     * block has its snapshot program run before every backup, as the addon; the panel shows each
     * such addon with its switch - on by default, off for a large one - and what the last run did.
     * A failed run is also the Status warning backup-addon, whose edge is on this panel.
     */
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {i18n, t} from './i18n.svelte';
    import {dateTime} from './datefmt';
    import Help from './Help.svelte';
    import {warnEdge, warnFor} from './systemmenu.svelte';

    let view = $state<BackupAddonsView | null>(null);
    let busy = $state('');
    let error = $state('');

    async function load() {
        try { view = await api.get<BackupAddonsView>('/api/system/v1/backup/addons'); } catch { /* the panel keeps what it showed */ }
    }
    onMount(() => { void load(); });

    const name = (a: BackupHookAddon) => (typeof a.name === 'string' ? a.name : (a.name[i18n.language as 'de' | 'en'] ?? a.name.en ?? a.name.de ?? a.id));
    const size = (n: number) => (n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : n >= 1e6 ? `${Math.round(n / 1e6)} MB` : `${Math.max(1, Math.round(n / 1e3))} kB`);

    async function toggle(a: BackupHookAddon, on: boolean) {
        busy = a.id;
        error = '';
        try {
            view = await api.put<BackupAddonsView>(`/api/system/v1/backup/addons/${encodeURIComponent(a.id)}`, {enabled: on});
        } catch (e) {
            error = t('The snapshot could not be switched: {error}', {error: e instanceof ApiError ? e.message : String(e)});
            await load();
        } finally {
            busy = '';
        }
    }
</script>

<h2 id="addons">{t('Addon data in the backup')}<Help>{t("Addons that keep their data outside /usr/local or in a database declare a snapshot: before every backup the system runs the addon's snapshot program as the addon, a consistent copy lands in the addon's directory and goes into the backup with everything else. Switch an addon off here to leave its snapshot out; a large one is off until you switch it on.")}</Help></h2>
<div class="ol-panel {warnEdge(['backup-addon'])}" data-section="backup-addons" {...warnFor(['backup-addon'])}>
    {#if error}<div class="ol-notice err">{error}</div>{/if}
    {#if view && !view.supported}
        <p class="ol-muted">{t('Snapshots are not available on this system (no systemd).')}</p>
    {:else if view && view.addons.length === 0}
        <p class="ol-muted">{t('No installed addon declares a snapshot for the backup.')}</p>
    {:else if view}
        <ul class="ol-list backup-addons">
            {#each view.addons as a (a.id)}
                <li data-backup-addon={a.id} data-enabled={a.enabled}>
                    <!-- the row keeps its shape across states: the switch, the name with the size beside it, the last run below -->
                    <label class="ol-tick"><input type="checkbox" checked={a.enabled} disabled={busy !== ''} onchange={(e) => void toggle(a, (e.currentTarget as HTMLInputElement).checked)} data-action="backup-addon" /> <strong>{name(a)}</strong>
                        {#if a.size_hint_mb > 0}{' '}<span class="ol-muted">· {t('about {mb} MB', {mb: a.size_hint_mb})}</span>{/if}
                        {#if a.large}{' '}<span class="ol-badge warn">{t('large')}</span>{/if}
                    </label>
                    <div class="ol-muted last" data-last={a.last ? (a.last.ok ? 'ok' : 'failed') : 'none'}>
                        {#if !a.last}{t('No snapshot yet')}
                        {:else if a.last.ok}{t('Last snapshot: {when}, {size}', {when: dateTime(a.last.at), size: size(a.last.bytes)})}
                        {:else}<span class="ol-warn">{t('Last snapshot failed ({when}): {error}', {when: dateTime(a.last.at), error: a.last.error ?? ''})}</span>{/if}
                    </div>
                </li>
            {/each}
        </ul>
    {/if}
</div>

<style>
    .backup-addons { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
    .backup-addons .last { margin-left: 26px; font-size: 0.92em; overflow-wrap: anywhere; }
</style>
