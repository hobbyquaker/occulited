<script lang="ts" module>
    // occulited task 139: the newest backup per way it left the system (GET /backup/last)
    export interface LastBackup { time: string; name: string; size: number; encrypted: boolean; by?: string; instance?: string; targets?: string[] }
    export interface LastBackups { automatic?: LastBackup; run?: LastBackup; download?: LastBackup; client?: LastBackup; earlier?: LastBackup }
    export const LAST_POLL_MS = 15_000;
</script>

<script lang="ts">
    import {dateTime} from './datefmt';
    /*
     * occulited task 139 (the maintainer, 2026-11-10): beside the targets' last runs, the last
     * backup made by hand and the last one a program fetched - each way on its own row, "never"
     * when it did not happen, so the panel keeps its rows. Read again every 15 s while the tab is
     * visible: a download in the browser or a Back up now run ends without telling this page.
     */
    import {onDestroy, onMount} from 'svelte';
    import {api} from './api';
    import {t} from './i18n.svelte';

    let last = $state<LastBackups | null>(null);
    let names = $state<Record<string, string>>({});

    async function load() {
        try { last = await api.get<LastBackups>('/api/system/v1/backup/last'); } catch { /* the rows keep what they showed */ }
    }
    async function loadNames() {
        try {
            const r = await api.get<{targets: {id: string; name: string}[]}>('/api/system/v1/backup/targets');
            names = Object.fromEntries(r.targets.map((x) => [x.id, x.name]));
        } catch { /* ids then */ }
    }
    let timer: ReturnType<typeof setInterval> | null = null;
    onMount(() => {
        void load();
        void loadNames();
        timer = setInterval(() => { if (document.visibilityState !== 'hidden') void load(); }, LAST_POLL_MS);
    });
    onDestroy(() => { if (timer) clearInterval(timer); });

    const size = (n: number) => (n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : `${Math.max(1, Math.round(n / 1e6))} MB`);
    const targetNames = (b: LastBackup) => (b.targets ?? []).map((id) => names[id] ?? id).join(', ');
    function line(b: LastBackup, who: string): string {
        const parts = [dateTime(b.time), size(b.size), b.encrypted ? t('encrypted') : t('unencrypted')];
        if (who) parts.push(who);
        return parts.join(' · ');
    }
    // the rows: [way, label, who]; "earlier" shows only when it is newer than every recorded way
    const rows = $derived.by(() => {
        const l = last ?? {};
        const out: {way: string; label: string; b?: LastBackup; who: string}[] = [
            {way: 'automatic', label: t('Automatic backup'), b: l.automatic, who: l.automatic ? t('to {targets}', {targets: targetNames(l.automatic)}) : ''},
            {way: 'run', label: t('Back up now'), b: l.run, who: l.run ? t('to {targets}', {targets: targetNames(l.run)}) : ''},
            {way: 'download', label: t('Downloaded in the browser'), b: l.download, who: l.download?.by ? t('by {name}', {name: l.download.by}) : ''},
            {way: 'client', label: t('Fetched by a program'), b: l.client, who: l.client?.by ?? ''},
        ];
        const newest = Math.max(0, ...out.map((r) => (r.b ? new Date(r.b.time).getTime() : 0)));
        if (l.earlier && new Date(l.earlier.time).getTime() > newest) out.push({way: 'earlier', label: t('Earlier backup'), b: l.earlier, who: ''});
        return out;
    });
</script>

<div class="ol-fields last-backups" data-last-backups>
    {#each rows as r (r.way)}
        <span class="ol-field-label">{r.label}</span>
        <div class="ol-field-value" data-last-way={r.way}>{#if r.b}<span class="hmm-mono-num">{line(r.b, r.who)}</span>{:else}<span class="ol-muted">{last ? t('never') : '\u00a0'}</span>{/if}</div>
    {/each}
</div>

<style>
    .last-backups { margin-top: 16px; }
    .hmm-mono-num { font-variant-numeric: tabular-nums; }
</style>
