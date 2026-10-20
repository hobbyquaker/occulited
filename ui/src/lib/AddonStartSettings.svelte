<script lang="ts">
    /*
     * Task 119 (D-75): the global switch of the early addon start, on the Addons page under the list,
     * next to the per-addon switches in the list's ⋯ menus. An addon whose catalogue entry declares
     * runtime.start "early" copes with radio interfaces that are not ready yet, so the box starts it
     * before them; this switch turns that off for all of them. A change takes effect at the next
     * boot, and nothing is restarted. `view` is the page's copy of the switches, as in
     * AddonSessionSettings; `onsaved` lets the page read its addon list again.
     *
     * openccu-lite task 369: the start delay per addon, below the switch - the seconds an addon
     * waits at boot after the radio interfaces answer, so the addons do not all hit the HmIP server
     * in the same second. `addons` are the page's installed addons (id and name).
     */
    import {onMount} from 'svelte';
    import {api} from './api';
    import {t} from './i18n.svelte';
    import Loading from './Loading.svelte';
    import Help from './Help.svelte';
    import {scrollToAnchor} from './anchor';

    interface EarlyView { enabled: boolean; off: string[]; delays?: Record<string, number>; max_delay?: number }
    let {view: early = $bindable(null), onsaved, addons = []}: {view?: EarlyView | null; onsaved?: () => void; addons?: {id: string; name: string}[]} = $props();
    // task 369: the delays as typed, per addon id; empty or 0 is none
    let delays = $state<Record<string, number | string | null>>({});
    let delayNotice = $state('');
    let delayBusy = $state(false);
    const maxDelay = $derived(early?.max_delay ?? 600);
    function fillDelays() {
        const d: Record<string, number | null> = {};
        for (const a of addons) d[a.id] = early?.delays?.[a.id] ?? null;
        delays = d;
    }
    const secondsOf = (v: number | string | null | undefined) => (v === undefined || v === null || String(v).trim() === '' ? 0 : Number(v));
    const delayValid = (v: number | string | null | undefined) => {
        const n = secondsOf(v);
        return Number.isInteger(n) && n >= 0 && n <= maxDelay;
    };
    const delayChanged = $derived(addons.filter((a) => secondsOf(delays[a.id]) !== (early?.delays?.[a.id] ?? 0)));
    const delaysOk = $derived(addons.every((a) => delayValid(delays[a.id])));
    async function saveDelays() {
        delayBusy = true;
        delayNotice = '';
        try {
            for (const a of delayChanged) {
                early = await api.put<EarlyView>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/start-delay`, {seconds: secondsOf(delays[a.id])});
            }
            fillDelays();
            delayNotice = t('Saved. Takes effect at the next boot.');
            onsaved?.();
        } catch (e) {
            delayNotice = (e as Error).message;
        } finally {
            delayBusy = false;
        }
    }
    $effect(() => {
        // the addon list arrives after the view, or changes: fill what is not typed yet
        for (const a of addons) if (delays[a.id] === undefined) delays[a.id] = early?.delays?.[a.id] ?? null;
    });
    let earlyOn = $state(true);
    let error = $state('');
    let notice = $state('');
    let busy = $state(false);
    let unavailable = $state(false);
    async function loadEarly() {
        try {
            early = await api.get<EarlyView>('/api/system/v1/early-start');
            earlyOn = early.enabled;
            fillDelays();
        } catch (e) {
            const msg = (e as Error).message;
            if (msg.includes('501') || msg.includes('configuration file')) unavailable = true;
            else error = msg;
        }
    }
    async function save() {
        busy = true;
        notice = '';
        try {
            early = await api.put<EarlyView>('/api/system/v1/early-start', {enabled: earlyOn});
            earlyOn = early.enabled;
            notice = t('Saved. Takes effect at the next boot.');
            onsaved?.();
        } catch (e) {
            notice = (e as Error).message;
        } finally {
            busy = false;
        }
    }

    onMount(async () => {
        await loadEarly();
        scrollToAnchor('addon-start');
    });
</script>

<section class="ol-section" data-section="addon-start">
    <h2 id="addon-start">{t('Addon start')}</h2>
    {#if unavailable}
        <p class="ol-muted">{t('This daemon runs without a configuration file; the switch cannot be changed here.')}</p>
    {:else if !early}
        <Loading error={error} />
    {:else}
        {#if notice}<div class="ol-notice ad-early-notice">{notice}</div>{/if}
        <div class="ol-checks">
            <label><input type="checkbox" class="ad-early-global" bind:checked={earlyOn} /> <strong>{t('Start addons early when they support it')}</strong><Help><p>{t('Such an addon has declared that it copes with radio interfaces that are not ready yet: it retries within seconds and logs no errors while it waits. The system starts it before the radio interfaces, so it is ready sooner after a reboot. Addons without that declaration always wait for the interfaces.')}</p><p>{t('The ⋯ menu of such an addon switches it off for that addon alone.')}</p></Help></label>
            <div class="ol-muted sub">{t('Such addons start before the radio interfaces are ready and connect when they are.')}</div>
            {#if !earlyOn}
                <div class="ol-muted sub">{t('Every addon waits for the radio interfaces.')}</div>
            {:else if early.off.length > 0}
                <div class="ol-muted sub">{t('Switched off for: {list}', {list: early.off.join(', ')})}</div>
            {/if}
            <div class="ol-muted sub ad-early-nextboot">{t('A change takes effect at the next boot.')}</div>
        </div>
        <div class="ol-actions" style="margin-top:10px">
            <button class="hmm-button primary ad-early-save" onclick={save} disabled={busy || earlyOn === early.enabled}>{t('Save')}</button>
        </div>
        {#if early.delays !== undefined && addons.length > 0}
            <h3 class="ad-delay-head">{t('Start delay')}<Help><p>{t('After a reboot every addon connects to the radio interfaces in the same second they are ready. On a large installation the HmIP server then needs seconds for all of them at once. A start delay staggers them: such an addon waits for the radio interfaces, then the given seconds, then starts.')}</p><p>{t('An addon with a delay never starts early. The delay applies at boot only; a start or restart later on does not wait.')}</p></Help></h3>
            {#if delayNotice}<div class="ol-notice ad-delay-notice">{delayNotice}</div>{/if}
            <table class="ad-delays">
                <tbody>
                    {#each addons as a (a.id)}
                        <tr data-addon={a.id}>
                            <th scope="row">{a.name || a.id}</th>
                            <td>
                                <input type="number" class="hmm-input ad-delay-input" min="0" max={maxDelay} step="1" placeholder="0" aria-label={t('Start delay of {name} in seconds', {name: a.name || a.id})} aria-invalid={!delayValid(delays[a.id])} bind:value={delays[a.id]} />
                                <span class="ol-muted">{t('s after the radio interfaces')}</span>
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
            {#if !delaysOk}<div class="ol-muted ol-warn ad-delay-invalid">{t('A start delay is 0 to {max} seconds.', {max: maxDelay})}</div>{/if}
            <div class="ol-muted sub-plain">{t('A change takes effect at the next boot.')}</div>
            <div class="ol-actions" style="margin-top:10px">
                <button class="hmm-button primary ad-delay-save" onclick={saveDelays} disabled={delayBusy || !delaysOk || delayChanged.length === 0}>{t('Save delays')}</button>
            </div>
        {/if}
    {/if}
</section>

<style>
    .ol-checks { display: flex; flex-direction: column; gap: 8px; margin: 8px 0 4px; max-width: 760px; }
    .ol-checks label { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .ol-checks .sub { margin-left: 26px; font-size: var(--hmm-font-size-small); }
    .ad-delay-head { margin: 18px 0 6px; font-size: var(--hmm-font-size); }
    .ad-delays { border-collapse: collapse; }
    .ad-delays th { text-align: left; font-weight: normal; padding: 3px 16px 3px 0; }
    .ad-delays td { padding: 3px 0; }
    .ad-delay-input { width: 6em; }
    .sub-plain { font-size: var(--hmm-font-size-small); margin-top: 4px; }
</style>
