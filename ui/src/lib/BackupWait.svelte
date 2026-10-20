<script lang="ts">
    /*
     * occulited B-90: the line under a backup download button while the system prepares the
     * backup - "Creating the backup… 2:15". Its line is always there (empty when idle), so the
     * panel does not grow when a download starts.
     */
    import {onDestroy} from 'svelte';
    import {t} from './i18n.svelte';
    import {elapsed} from './backupjob';

    let {since}: {since: number | null} = $props();
    let now = $state(Date.now());
    const timer = setInterval(() => (now = Date.now()), 1000);
    onDestroy(() => clearInterval(timer));
</script>

<div class="ol-muted backup-wait" aria-live="polite" data-backup-wait={since !== null ? 'running' : undefined}>{since !== null ? t('Creating the backup… {time}', {time: elapsed((now - since) / 1000)}) : ''}</div>

<style>
    .backup-wait { min-height: 1.5em; margin: 8px 0 0; font-variant-numeric: tabular-nums; }
</style>
