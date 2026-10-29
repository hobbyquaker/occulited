<script lang="ts" module>
    /** a catalogue install run as /catalog/progress answers it */
    import type {InstallOutcome} from './install';
    export interface Progress { addon_id: string; phase: string; message?: string; bytes?: number; total?: number; percent?: number; started: string; finished?: string; result?: InstallOutcome & {meaning?: string}; error?: string; retry_minutes?: number }
    /** occulited B-113: what the one install slot holds now, `busy` of the same answer - a catalogue run, an uploaded archive's job or an uninstall */
    export interface InstallBusy { kind: 'catalog' | 'upload' | 'uninstall'; addon_id?: string; job?: string; started: string; bytes?: number }
</script>

<script lang="ts">
    /*
     * The catalogue installer's progress (30.2, shared since task 56): one notice with one bar
     * for the whole run, polled from /catalog/progress every 1.5 s. The server paces the bar
     * (bytes for the download, a learned duration for the install); this only draws percent.
     *
     * It is one component for the Catalogue and the Installed addons page, because an update
     * can be started on either: whichever page is open shows the same run, and `onfinished`
     * fires once when it ends so the page reloads its list. `busy` is bindable - the pages
     * disable their Install and Update buttons while a run is on - and `poll()` is exported so a
     * page that has just started a run shows it without waiting for the next tick.
     *
     * occulited B-113: `busy` is the API's own word (`busy` of /catalog/progress), so it covers an
     * uploaded archive's job and an uninstall as well as a catalogue run, in every tab; `job` is
     * the detail, bindable, so the Addons page follows an upload it did not start. An upload or an
     * uninstall another tab started has a status line of its own here.
     */
    import {onMount} from 'svelte';
    import {api} from './api';
    import {t} from './i18n.svelte';
    import {askStartStopped} from './install';
    import {releasesProblemKind} from './catalog';
    import ReleasesProblem from './ReleasesProblem.svelte';

    interface Props {
        /** true while the install slot is held - a catalogue run, an upload's job or an uninstall; the pages read it */
        busy?: boolean;
        /** what holds it (B-113), null when nothing runs */
        job?: InstallBusy | null;
        /**
         * whether a run that had already ended when the page opened is shown. The Catalogue
         * shows the last run as it always did; the Installed addons page shows only what
         * happens while it is open, so an install of last week is not a notice on every visit.
         */
        showEnded?: boolean;
        /** called once when a run has ended (done or failed) since the page opened */
        onfinished?: (p: Progress) => void;
    }
    let {busy = $bindable(false), job = $bindable(null), showEnded = true, onfinished}: Props = $props();

    let progress = $state<Progress | null>(null);
    const keyOf = (p: Progress | null) => (p ? `${p.addon_id}@${p.started}` : '');
    // the run whose end was already reported, so a finished run that stays in the answer (it
    // does, until the next one starts) fires `onfinished` once and not on every poll
    let reported = '';
    // the run that had ended before the page opened
    let endedAtMount = $state('');

    interface Answer { progress: Progress | null; busy?: InstallBusy | null }
    // the poll's time, so an upload's or an uninstall's status line counts its seconds
    let now = $state(Date.now());
    function take(r: Answer): Progress | null {
        const p = r.progress ?? null;
        progress = p;
        job = r.busy ?? null;
        busy = !!job || (!!p && !p.finished);
        now = Date.now();
        return p;
    }
    export async function poll(): Promise<void> {
        try {
            const p = take(await api.get<Answer>('/api/system/v1/catalog/progress'));
            const key = p?.finished ? keyOf(p) : '';
            if (key && key !== reported) {
                reported = key;
                onfinished?.(p!);
                // B-98 (D-67): an addon the failed or reboot-asking install left stopped - ask to start it
                if (p!.result?.stopped_addons?.length && (await askStartStopped(p!.result)).length) onfinished?.(p!);
            }
        } catch {
            /* the box is between two answers; the next poll tells */
        }
    }
    onMount(() => {
        // a run that ended before this page opened is history, not news: it is not reported
        void (async () => {
            try {
                const p = take(await api.get<Answer>('/api/system/v1/catalog/progress'));
                if (p?.finished) reported = endedAtMount = keyOf(p);
            } catch {
                /* the interval tries again */
            }
        })();
        const id = setInterval(poll, 1500);
        return () => clearInterval(id);
    });
    // B-21: a run refused because the release list could not be read says so in the reader's language
    const refused = $derived(progress?.phase === 'failed' && releasesProblemKind(progress.error, progress.retry_minutes) !== '');
    const pct = $derived(progress?.percent ?? (progress?.total ? Math.round(((progress.bytes ?? 0) / progress.total) * 100) : 0));
    const visible = $derived(!!progress && (showEnded || !progress.finished || keyOf(progress) !== endedAtMount));
    // B-113: an upload's job or an uninstall holds the slot - shown here, so a tab that did not
    // start it sees why the install controls are disabled (a catalogue run has the bar below)
    const other = $derived(job && job.kind !== 'catalog' ? job : null);
    const seconds = $derived(other ? Math.max(0, Math.round((now - new Date(other.started).getTime()) / 1000)) : 0);
</script>

{#if other}
    <div class="ol-notice ol-install-progress" data-install-busy={other.kind} role="status">
        {#if other.kind === 'upload'}
            {t('An uploaded addon archive is being installed ({mb} MB, {s} s)…', {mb: Math.max(1, Math.round((other.bytes ?? 0) / 1048576)), s: seconds})}
        {:else}
            {t('Uninstalling {id} ({s} s)…', {id: other.addon_id ?? '', s: seconds})}
        {/if}
    </div>
{/if}
{#if progress && visible}
    <div class="ol-notice ol-install-progress" class:error={progress.phase === 'failed'} data-phase={progress.phase} role="status">
        {#if refused}
            <strong>{progress.addon_id}</strong>: {t(progress.phase)} — <span data-refused={progress.error}><ReleasesProblem code={progress.error} minutes={progress.retry_minutes} /> {t('Nothing was installed.')}</span>
        {:else}
            <strong>{progress.addon_id}</strong>: {t(progress.phase)}{progress.message ? ` — ${progress.message}` : ''}
        {/if}
        {#if busy && progress.phase === 'downloading' && progress.total}{' '}<span class="ol-muted">{Math.round((progress.bytes ?? 0) / 1048576)} / {Math.round(progress.total / 1048576)} MB</span>{/if}
        {#if progress.result?.reboot_required}{' '}<strong>{t('An addon asked for a reboot to finish its installation.')}</strong>{/if}
        {#if busy || progress.phase === 'done'}
            <div class="ol-progress" class:failed={progress.phase === 'failed'} role="progressbar" aria-valuenow={pct} aria-valuemin="0" aria-valuemax="100"><div style={`width:${pct}%`}></div></div>
            <span class="ol-muted">{pct} %</span>
        {/if}
    </div>
{/if}
