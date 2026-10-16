<script lang="ts">
    /*
     * openccu-lite task 164: the Hardware page's section for the RPI-RF-MOD's button - what each
     * gesture does (single, double and long press), the long threshold, the mute's length, the
     * button's live state for checking it, and the fixed power-on gesture. The privileged helper
     * reads the button; this section only sets the actions (GET/PUT /api/system/v1/hardware/button).
     * Shown where the module is on the Pi's header, or explained where it sits on a radio adapter
     * (the maintainer, D-108); hidden on a system without the module.
     */
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import {auth} from '../lib/auth.svelte';
    import Help from '../lib/Help.svelte';

    interface Settings {
        single: string;
        double: string;
        long: string;
        long_s: number;
        mute_minutes: number;
    }
    interface ButtonView {
        available: boolean;
        reason?: string;
        pressed: boolean;
        last?: {kind: string; at: string; action: string; error?: string};
        ignored?: number;
        settings: Settings;
        actions: string[];
        long_actions: string[];
        recovery: boolean;
    }

    let view = $state<ButtonView | null>(null);
    let draft = $state<Settings | null>(null);
    let error = $state('');
    let notice = $state('');
    let busy = $state(false);
    const admin = $derived(auth.role === 'admin');
    const dirty = $derived(!!view && !!draft && JSON.stringify(view.settings) !== JSON.stringify(draft));
    // the section shows where the module is on the header, is being detected, or sits on an adapter
    const shown = $derived(!!view && (view.available || ['detecting', 'adapter', 'no-line', 'line-error'].includes(view.reason ?? '')));

    const life = pageLife();
    onMount(() => {
        void load();
        // the live state, for checking the button: every two seconds while the page shows
        const id = setInterval(() => life.active && view?.available && void load(true), 2000);
        const stop = life.onReturn(() => void load());
        return () => {
            stop();
            clearInterval(id);
        };
    });

    async function load(stateOnly = false) {
        try {
            const v = await api.get<ButtonView>('/api/system/v1/hardware/button');
            view = v;
            if (!stateOnly || !draft || !dirty) draft = {...v.settings};
        } catch {
            if (!stateOnly) view = null;
        }
    }

    async function save() {
        if (!draft) return;
        busy = true;
        error = '';
        notice = '';
        try {
            view = await api.put<ButtonView>('/api/system/v1/hardware/button', draft);
            draft = {...view.settings};
            notice = t('Saved.');
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }

    function actionName(a: string): string {
        switch (a) {
            case 'mute':
                return t('Mute the LED warnings');
            case 'locate':
                return t('Locate');
            case 'recovery':
                return t('Reboot into the recovery system');
            case 'reboot':
                return t('Reboot');
        }
        return t('Nothing');
    }

    function gestureName(g: string): string {
        switch (g) {
            case 'single':
                return t('Single press');
            case 'double':
                return t('Double press');
            case 'long':
                return t('Long press');
        }
        return g;
    }

    function reasonText(r?: string): string {
        switch (r) {
            case 'detecting':
                return t('The radio module is still being detected.');
            case 'adapter':
                return t('The radio module sits on a radio adapter (HB-RF-USB, HB-RF-USB-2, HB-RF-ETH): its button does not reach the system there.');
            case 'no-line':
            case 'line-error':
                return t('The button\'s GPIO line could not be read.');
        }
        return '';
    }

    function longActions(): string[] {
        return (view?.long_actions ?? []).filter((a) => a !== 'recovery' || view?.recovery);
    }

    function timeOf(iso: string): string {
        return new Date(iso).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', second: '2-digit'});
    }
</script>

{#if view && draft && shown}
    <h2 id="button">{t('Radio module button')}<Help>{t('The button on the RPI-RF-MOD radio module. Each gesture can do one thing; holding it while the power is connected always starts the recovery system.')}</Help></h2>
    <section class="ol-card ol-btn-panel" data-panel="button">
        {#if error}<div class="ol-notice error">{error}</div>{/if}
        {#if notice}<div class="ol-notice">{notice}</div>{/if}
        {#if !view.available}
            <p class="ol-muted" data-button-reason={view.reason}>{reasonText(view.reason)}</p>
        {:else}
            <div class="ol-btn-live" data-button-live={view.pressed ? 'pressed' : 'released'}>
                <span class="ol-dot" class:ok={!view.pressed} class:warn={view.pressed}></span>
                {view.pressed ? t('Pressed') : t('Released')}
                {#if view.last}<span class="ol-muted" data-button-last>· {t('last: {gesture} at {time}, {action}', {gesture: gestureName(view.last.kind), time: timeOf(view.last.at), action: actionName(view.last.action)})}{#if view.last.error} – {view.last.error}{/if}</span>{/if}
            </div>
            <fieldset class="ol-btn-fields" disabled={!admin || busy}>
                <label class="ol-btn-row">
                    <span class="ol-btn-key">{t('Single press')}</span>
                    <select class="hmm-select" bind:value={draft.single} data-button="single">
                        {#each view.actions as a (a)}<option value={a}>{actionName(a)}</option>{/each}
                    </select>
                </label>
                <label class="ol-btn-row">
                    <span class="ol-btn-key">{t('Double press')}</span>
                    <select class="hmm-select" bind:value={draft.double} data-button="double">
                        {#each view.actions as a (a)}<option value={a}>{actionName(a)}</option>{/each}
                    </select>
                </label>
                <label class="ol-btn-row">
                    <span class="ol-btn-key">{t('Long press')}</span>
                    <select class="hmm-select" bind:value={draft.long} data-button="long">
                        {#each longActions() as a (a)}<option value={a}>{actionName(a)}</option>{/each}
                    </select>
                </label>
                <label class="ol-btn-row">
                    <span class="ol-btn-key">{t('Long press from')}<Help>{t('From this long on the LED blinks white fast: release now. Held 15 seconds or longer, nothing happens.')}</Help></span>
                    <input class="hmm-input ol-btn-num" type="number" min="3" max="10" bind:value={draft.long_s} data-button="long_s" aria-label={t('Long press from')} />
                    <span>{t('seconds')}</span>
                </label>
                <label class="ol-btn-row">
                    <span class="ol-btn-key">{t('Mute for')}<Help>{t('Muted, the LED shows the normal state; radio and network down still show. A press while muted un-mutes.')}</Help></span>
                    <input class="hmm-input ol-btn-num" type="number" min="5" max="1440" bind:value={draft.mute_minutes} data-button="mute_minutes" aria-label={t('Mute for')} />
                    <span>{t('min')}</span>
                </label>
            </fieldset>
            {#if admin}
                <div class="ol-actions">
                    <button class="hmm-button primary" onclick={save} disabled={!dirty || busy} data-button-save>{t('Save')}</button>
                </div>
            {/if}
        {/if}
        <p class="ol-muted" data-button-poweron>{t('Held while the power is connected: the recovery system starts (fixed, also when the system does not start).')}</p>
    </section>
{/if}

<style>
    .ol-btn-panel { display: flex; flex-direction: column; gap: 10px; }
    .ol-btn-panel > p { margin: 0; }
    .ol-btn-live { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
    .ol-btn-live .ol-dot.warn { background: var(--hmm-warn); }
    .ol-btn-fields { border: 0; padding: 0; margin: 0; min-width: 0; display: flex; flex-direction: column; gap: 8px; }
    .ol-btn-row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
    .ol-btn-key { min-width: 13em; }
    .ol-btn-num { width: 5em; }
    @media (max-width: 640px) {
        .ol-btn-key { min-width: 0; flex-basis: 100%; }
    }
</style>
