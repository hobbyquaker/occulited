<script lang="ts" module>
    /** the catalogue entry of Homematic Manager, by its repository - known before the first check fetched its manifest */
    export const HM_GIT = /^https:\/\/github\.com\/hobbyquaker\/homematic-manager\/?$/i;
    /** its manifest id, for a catalogue whose entry carries none yet (a fresh system, nothing checked) */
    export const HM_ID = 'hmm';
</script>

<script lang="ts">
    /*
     * occulited task 143: the welcome page's last step. The system manages no devices by itself;
     * pairing, parameters, direct links and names need an addon. Homematic Manager installs from
     * here with one click - the catalogue's own route (POST /catalog/<id>/install) and its progress
     * (InstallProgress, as on the Addons page) - and the catalogue is the button beside it.
     *
     * A fresh system holds only the catalogue's bundled list, without the manifests, so the click
     * first runs the catalogue check (GET /catalog?refresh=1), the user's own request to GitHub
     * (D-90): the daily-check setting a step earlier does not gate it. When GitHub cannot be
     * reached, the reason is said and the button tries again.
     */
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {t} from './i18n.svelte';
    import {installError} from './install';
    import {addonsChanged} from './addonsync';
    import InstallProgress, {type InstallBusy, type Progress} from './InstallProgress.svelte';

    interface Entry { git: string; id?: string; latest?: {version: string}; requires?: {architectures?: string[]; lite?: string}; lite_unmet?: boolean }
    interface View {
        catalog: {addons: Entry[]; check_error?: {message: string}; releases_error?: {message: string}};
        installed: Record<string, string>;
        arch: string;
    }
    interface Props {
        /** the step's number in the page's count */
        step: number;
        /** leaves the welcome page for a path (the page's own done(): its open choices first) */
        onopen: (path: string) => void;
        /** the page's open choices block leaving it */
        blocked?: boolean;
    }
    let {step, onopen, blocked = false}: Props = $props();

    // loading: the first answer is not in; none: this system has no catalogue (or no such entry);
    // ready: one click installs; checking: the catalogue check runs; installing: the run is on;
    // installed: it is there; failed: the run or its start failed; unreachable: the check failed;
    // unavailable: not for this system (architecture, openccu-lite version)
    type State = 'loading' | 'none' | 'ready' | 'checking' | 'installing' | 'installed' | 'failed' | 'unreachable' | 'unavailable';
    let st = $state<State>('loading');
    let reason = $state('');
    let id = $state(HM_ID);
    let slotBusy = $state(false);
    let job = $state<InstallBusy | null>(null);
    let progressView: InstallProgress | undefined = $state();

    const archOk = (e: Entry, arch: string) => {
        const a = e.requires?.architectures ?? [];
        return a.length === 0 || a.includes(arch) || a.includes('any');
    };

    /** the state the catalogue's answer says; `checked` is after the user's check */
    function judge(v: View, checked: boolean): State {
        const e = v.catalog.addons.find((x) => HM_GIT.test(x.git));
        if (e?.id) id = e.id;
        if (v.installed[id]) return 'installed';
        if (!e) return 'none';
        if (!e.id || !e.latest) {
            if (!checked) return 'ready';
            reason = v.catalog.check_error?.message ?? v.catalog.releases_error?.message ?? t('The catalogue has no release of Homematic Manager.');
            return v.catalog.check_error || !e.id ? 'unreachable' : 'failed';
        }
        if (!archOk(e, v.arch)) {
            reason = t('Homematic Manager is not available for {arch}.', {arch: v.arch});
            return 'unavailable';
        }
        if (e.lite_unmet) {
            reason = t('Homematic Manager {version} needs openccu-lite {lite} or later; update the system first.', {version: e.latest.version, lite: e.requires?.lite ?? '?'});
            return 'unavailable';
        }
        return 'ready';
    }

    onMount(async () => {
        try {
            st = judge(await api.get<View>('/api/system/v1/catalog'), false);
        } catch {
            // 501: no catalogue on this system - or no answer: the Addons page says which
            st = 'none';
        }
    });

    // a run of this addon already on (the page reloaded during it): followed, not started again
    $effect(() => {
        if (job?.kind === 'catalog' && job.addon_id === id && st !== 'installing' && st !== 'installed') st = 'installing';
    });

    async function install() {
        reason = '';
        if (st !== 'ready' && st !== 'failed' && st !== 'unreachable') return;
        let v: View | null = null;
        try {
            v = await api.get<View>('/api/system/v1/catalog');
        } catch { /* the check below asks again */ }
        let s = v ? judge(v, false) : 'ready';
        const e = v?.catalog.addons.find((x) => HM_GIT.test(x.git));
        if (s === 'ready' && (!e?.id || !e.latest)) {
            st = 'checking';
            try {
                s = judge(await api.get<View>('/api/system/v1/catalog?refresh=1'), true);
            } catch (err) {
                reason = err instanceof ApiError ? err.message : String(err);
                st = 'unreachable';
                return;
            }
        }
        if (s !== 'ready') {
            st = s;
            return;
        }
        st = 'installing';
        try {
            await api.post(`/api/system/v1/catalog/${encodeURIComponent(id)}/install`);
        } catch (err) {
            reason = installError(err);
            st = 'failed';
            return;
        }
        await progressView?.poll();
    }

    async function finished(p: Progress) {
        if (p.addon_id !== id) return;
        // "done" is the run's end, not its verdict: an install script that failed ends done too -
        // what the system lists as installed decides
        let there = false;
        try {
            there = !!(await api.get<View>('/api/system/v1/catalog')).installed[id];
        } catch { /* not known: said as failed, the button tries again */ }
        if (there) {
            st = 'installed';
            addonsChanged(); // the menu shows the new frontend at once
        } else {
            st = 'failed'; // the progress notice says why
        }
    }

    const busy = $derived(st === 'loading' || st === 'checking' || st === 'installing');
    // the one install slot is held by another run (an upload, an uninstall, another addon)
    const otherRun = $derived(slotBusy && st !== 'installing' && !(job?.kind === 'catalog' && job.addon_id === id));
    const label = $derived(
        st === 'installed' ? t('Open Homematic Manager')
        : st === 'checking' ? t('Loading the catalogue…')
        : st === 'installing' ? t('Installing Homematic Manager…')
        : st === 'failed' || st === 'unreachable' ? t('Try again')
        : t('Install Homematic Manager'),
    );
    // every label the button can carry, so its box is the widest of them in every state
    const LABELS = ['Install Homematic Manager', 'Open Homematic Manager', 'Loading the catalogue…', 'Installing Homematic Manager…', 'Try again'];
    const disabled = $derived(st === 'installed' ? blocked : busy || otherRun || st === 'unavailable');

    function main() {
        if (st === 'installed') onopen(`/nav/${encodeURIComponent(id)}`);
        else void install();
    }
</script>

<section class="wf" data-welcome-frontend data-state={st} aria-labelledby="welcome-frontend">
    <h2 id="welcome-frontend">{step} · {t('Device management')}</h2>
    <p>{t('This system manages no devices by itself: pairing devices, setting their parameters, direct links and names need an addon. Homematic Manager does all of this; other addons connect the system to automations, MQTT or smart home platforms.')}</p>
    {#if st !== 'none'}
        <div class="ol-actions wf-row">
            <button class="hmm-button primary wf-main" {disabled} aria-busy={busy} onclick={main} data-action="welcome-hm">
                {#each LABELS as l (l)}<span class="wf-sizer" aria-hidden="true">{t(l)}</span>{/each}
                <span class="wf-live">{label}</span>
            </button>
            <button class="hmm-button wf-catalog" disabled={blocked} onclick={() => onopen('/catalog')} data-action="welcome-catalog">{t('More addons in the catalogue')}</button>
        </div>
    {:else}
        <div class="ol-actions wf-row">
            <button class="hmm-button primary wf-catalog" disabled={blocked} onclick={() => onopen('/addons')} data-action="welcome-catalog">{t('Open the Addons page')}</button>
        </div>
    {/if}
    <InstallProgress bind:this={progressView} bind:busy={slotBusy} bind:job showEnded={false} onfinished={finished} />
    <!-- one line under the buttons, its height reserved: the state changes the words, not the buttons -->
    <p class="ol-muted wf-status" data-welcome-hm-status role="status">
        {#if st === 'installed'}
            {t('Homematic Manager is installed. It opens from the menu as well.')}
        {:else if st === 'unreachable'}
            <span class="ol-warn">{t('The catalogue could not be loaded from GitHub: {e}', {e: reason})}</span> {t('Without an internet connection Homematic Manager can be installed later from the catalogue, or from its archive on the Addons page.')}
        {:else if st === 'failed'}
            <span class="ol-warn">{reason || t('Homematic Manager was not installed; the notice above says why.')}</span>
        {:else if st === 'installing'}
            {t('The installation runs on the system: it goes on when this page is left.')}
        {:else if st === 'unavailable'}
            <span class="ol-warn">{reason}</span>
        {:else if otherRun}
            {t('Another install is still running. Wait until it has finished.')}
        {:else if st === 'none'}
            {t('This system has no addon catalogue: addons are installed from their archive on the Addons page.')}
        {:else}
            {t('One click installs the newest release from GitHub (github.com); the system loads the catalogue from there first.')}
        {/if}
    </p>
</section>

<style>
    .wf { margin-top: 8px; }
    .wf-row { align-items: stretch; flex-wrap: wrap; gap: 8px; }
    /* every label in one cell: the button is as wide as its widest label, whatever it says now */
    .wf-main { display: inline-grid; justify-items: center; align-items: center; }
    .wf-sizer, .wf-live { grid-area: 1 / 1; white-space: nowrap; }
    .wf-sizer { visibility: hidden; pointer-events: none; }
    .wf-status { min-height: 2.8em; margin: 8px 0 0; }
    @media (max-width: 480px) {
        .wf-main, .wf-catalog { width: 100%; }
        .wf-sizer, .wf-live { white-space: normal; }
    }
</style>
