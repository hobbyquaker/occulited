<script lang="ts">
    import {dateOnly} from './datefmt';
    // Task 50: the editor of an own timer - the form on one side, the two files it stands for on
    // the other. What is saved is the files' text, so a user who knows systemd writes any directive
    // the form does not have; a file edited by hand stops following the form until "Take from the
    // form" (lib/timers.ts generates the files and reads an opened timer back into the form).
    // The container owns the Save and Close buttons and calls save(); `dirty` tells it whether
    // closing loses anything, so the container asks before it does.
    import {untrack} from 'svelte';
    import {api, type CalendarCheck, type LocalTimer} from './api';
    import {t} from './i18n.svelte';
    import {calendarOf, defaultForm, formFromFiles, serviceText, TIMER_NAME, timerText, WEEKDAYS, type TimerForm} from './timers';
    import Help from './Help.svelte';

    // task 51: the command's label names its input by `for` - with the ? inside it, a label without
    // `for` would name the ? instead
    const commandId = `ol-te-command-${Math.random().toString(36).slice(2, 8)}`;

    let {
        timer = null,
        analyze = true,
        dirty = $bindable(false),
        saving = $bindable(false),
        onsaved,
    }: {
        /** the stored own timer; null for a new one (after its creation the container passes it) */
        timer?: LocalTimer | null;
        /** systemd-analyze is on the box (GET /timers/own), so the files are verified before they apply */
        analyze?: boolean;
        dirty?: boolean;
        saving?: boolean;
        onsaved?: (saved: LocalTimer, created: boolean) => void;
    } = $props();

    // the starting point is read once: the container mounts a fresh editor for another timer
    const initial = untrack(() => timer);
    const start = initial ? formFromFiles(initial.name, initial.timer_file, initial.service_file) : defaultForm();
    const startTimer = initial ? initial.timer_file : timerText(start);
    const startService = initial ? initial.service_file : serviceText(start);
    let form = $state<TimerForm>(start);
    let timerFile = $state(startTimer);
    let serviceFile = $state(startService);
    // what the box has (an opened timer) or the untouched form (a new one): a difference is unsaved
    let base = $state({timer: startTimer, service: startService, name: start.name});

    // A file follows the form while it is still exactly what the form generated last; once edited
    // by hand it keeps the edit. So a stored file with directives the form does not know is never
    // overwritten by touching the form, and a new timer's files fill in as the form is filled in.
    let lastTimer = timerText(start);
    let lastService = serviceText(start);
    $effect(() => {
        const nextTimer = timerText(form);
        const nextService = serviceText(form);
        untrack(() => {
            if (timerFile === lastTimer) timerFile = nextTimer;
            if (serviceFile === lastService) serviceFile = nextService;
        });
        lastTimer = nextTimer;
        lastService = nextService;
    });
    const timerByHand = $derived(timerFile !== timerText(form));
    const serviceByHand = $derived(serviceFile !== serviceText(form));
    $effect(() => {
        dirty = timerFile !== base.timer || serviceFile !== base.service || form.name.trim() !== base.name;
    });

    const nameOk = $derived(TIMER_NAME.test(form.name.trim()));
    const unitBase = $derived(timer ? timer.unit.replace(/\.timer$/, '') : `local-${form.name.trim() || '…'}`);

    // The schedule through systemd-analyze while it is typed: the next three runs under the field,
    // so a typo shows before saving. Debounced; only the newest expression's answer is shown, and
    // an answer for an older expression is never passed off as this one's.
    const expression = $derived(calendarOf(form));
    let check = $state<{expression: string; result?: CalendarCheck; error?: string} | null>(null);
    let checkSeq = 0;
    $effect(() => {
        const expr = expression;
        const seq = ++checkSeq;
        if (!expr) return;
        const id = setTimeout(() => {
            api.post<CalendarCheck>('/api/system/v1/timers/calendar', {expression: expr})
                .then((r) => {
                    if (seq === checkSeq) check = {expression: expr, result: r};
                })
                .catch((e: Error) => {
                    if (seq === checkSeq) check = {expression: expr, error: e.message};
                });
        }, 300);
        return () => clearTimeout(id);
    });

    // Monday was 2024-01-01: the weekday names in the page's language without seven catalogue keys
    const weekdayName = (i: number) => dateOnly(new Date(2024, 0, 1 + i), {weekday: 'long'});

    let error = $state('');
    let notice = $state('');
    function fromForm(which: 'timer' | 'service') {
        if (which === 'timer') timerFile = timerText(form);
        else serviceFile = serviceText(form);
    }
    /** Creates the timer or replaces its files; true when the box took them. */
    export async function save(): Promise<boolean> {
        error = '';
        notice = '';
        const name = form.name.trim();
        if (!timer && !TIMER_NAME.test(name)) {
            error = t('A name is 1 to 32 letters, digits, - and _, starting with a letter or a digit.');
            return false;
        }
        saving = true;
        try {
            const files = {timer: timerFile, service: serviceFile};
            const r = timer
                ? await api.put<LocalTimer>(`/api/system/v1/timers/own/${encodeURIComponent(timer.name)}`, files)
                : await api.post<LocalTimer>('/api/system/v1/timers/own', {name, ...files});
            base = {timer: r.timer_file, service: r.service_file, name: r.name};
            notice = t('Saved.');
            onsaved?.(r, !timer);
            return true;
        } catch (e) {
            // the box's reason - a name that exists, systemd's verdict on a file - stays here, next
            // to the text; the API has written nothing, or taken a refused text back out
            error = (e as Error).message;
            return false;
        } finally {
            saving = false;
        }
    }
</script>

<div class="te">
    <div class="te-form">
        <div class="te-field">
            <span class="te-lbl">{t('Name')}</span>
            <span class="te-name">
                <span class="hmm-mono ol-muted">local-</span>
                <input class="hmm-input hmm-mono" bind:value={form.name} aria-label={t('Name')} disabled={!!timer || saving} maxlength="32" autocomplete="off" spellcheck="false" placeholder="backup-share" />
                <span class="hmm-mono ol-muted">.timer</span>
            </span>
            {#if !timer && form.name.trim() !== '' && !nameOk}
                <small class="ol-warn">{t('A name is 1 to 32 letters, digits, - and _, starting with a letter or a digit.')}</small>
            {/if}
        </div>

        <fieldset class="te-field" disabled={saving}>
            <legend class="te-lbl">{t('When')}</legend>
            <div class="te-row">
                <select class="hmm-select" bind:value={form.schedule} aria-label={t('When')}>
                    <option value="hourly">{t('hourly')}</option>
                    <option value="daily">{t('daily at …')}</option>
                    <option value="weekly">{t('weekly on … at …')}</option>
                    <option value="boot">{t('after boot + delay')}</option>
                    <option value="custom">{t('custom (OnCalendar=)')}</option>
                </select>
                {#if form.schedule === 'weekly'}
                    <select class="hmm-select" bind:value={form.weekday} aria-label={t('Weekday')}>
                        {#each WEEKDAYS as d, i (d)}<option value={d}>{weekdayName(i)}</option>{/each}
                    </select>
                {/if}
                {#if form.schedule === 'daily' || form.schedule === 'weekly'}
                    <input class="hmm-input" type="time" bind:value={form.time} aria-label={t('Time')} />
                {:else if form.schedule === 'boot'}
                    <input class="hmm-input hmm-mono te-short" bind:value={form.bootDelay} aria-label={t('Delay after boot')} placeholder="5min" spellcheck="false" autocomplete="off" />
                {/if}
            </div>
            {#if form.schedule === 'custom'}
                <input class="hmm-input hmm-mono te-wide" bind:value={form.calendar} aria-label="OnCalendar=" placeholder="Mon..Fri *-*-* 07:30" spellcheck="false" autocomplete="off" />
            {/if}
            {#if expression}
                <div class="te-check" aria-live="polite">
                    {#if !check || check.expression !== expression}
                        <span class="ol-muted">…</span>
                    {:else if check.error}
                        <div class="ol-warn te-pre">{check.error}</div>
                    {:else if check.result && !check.result.available}
                        <div class="ol-muted">{t('systemd-analyze is not on this system: systemd checks the expression when the timer is saved.')}</div>
                    {:else if check.result?.next?.length}
                        <div class="ol-muted">{t('Next runs')}{#if check.result.normalized}<span class="hmm-mono">{` · ${check.result.normalized}`}</span>{/if}</div>
                        <ul class="te-next hmm-mono">
                            {#each check.result.next as n, i (i)}<li>{n}</li>{/each}
                        </ul>
                    {/if}
                </div>
            {/if}
            <label class="te-checkbox"><input type="checkbox" bind:checked={form.persistent} disabled={form.schedule === 'boot'} /> {t('Catch up after the system was off (Persistent=)')}</label>
            <label class="te-stack">
                <span>{t('Random delay (RandomizedDelaySec=)')}</span>
                <input class="hmm-input hmm-mono te-short" bind:value={form.randomDelay} placeholder="5min" spellcheck="false" autocomplete="off" />
            </label>
        </fieldset>

        <fieldset class="te-field" disabled={saving}>
            <legend class="te-lbl">{t('What')}</legend>
            <label class="te-stack" for={commandId}>
                <span>{t('Command (ExecStart=)')}<Help>{t("Runs without a shell: for pipes or redirections write /bin/sh -c '…'.")}</Help></span>
                <input id={commandId} class="hmm-input hmm-mono te-wide" bind:value={form.command} placeholder="logger hello" spellcheck="false" autocomplete="off" />
            </label>
            <label class="te-stack">
                <span>{t('Runs as (User=)')}</span>
                <input class="hmm-input hmm-mono te-short" bind:value={form.user} placeholder="root" spellcheck="false" autocomplete="off" />
            </label>
        </fieldset>
    </div>

    <!-- task 51: that these two files are what is saved is the dialog's help now (the ? of its title,
         ServicesPage.svelte); "edited by hand" opens what it means -->
    <div class="te-files">
        <div class="te-file">
            <div class="te-lbl">
                <span class="hmm-mono">{unitBase}.timer</span>
                {#if timerByHand}
                    <span class="ol-muted">·</span><Help class="ol-muted">{#snippet trigger()}{t('edited by hand')}{/snippet}{t('The form no longer changes this file.')}</Help>
                    <button type="button" class="hmm-button te-small" disabled={saving} onclick={() => fromForm('timer')}>{t('Take from the form')}</button>
                {/if}
            </div>
            <textarea class="hmm-input hmm-mono te-text" bind:value={timerFile} aria-label={`${unitBase}.timer`} spellcheck="false" disabled={saving}></textarea>
        </div>
        <div class="te-file">
            <div class="te-lbl">
                <span class="hmm-mono">{unitBase}.service</span>
                {#if serviceByHand}
                    <span class="ol-muted">·</span><Help class="ol-muted">{#snippet trigger()}{t('edited by hand')}{/snippet}{t('The form no longer changes this file.')}</Help>
                    <button type="button" class="hmm-button te-small" disabled={saving} onclick={() => fromForm('service')}>{t('Take from the form')}</button>
                {/if}
            </div>
            <textarea class="hmm-input hmm-mono te-text" bind:value={serviceFile} aria-label={`${unitBase}.service`} spellcheck="false" disabled={saving}></textarea>
        </div>
        {#if !analyze}
            <p class="ol-muted te-note">{t('systemd-analyze is not on this system, so the files are not verified before they are applied; a unit systemd cannot load is still refused.')}</p>
        {/if}
    </div>
</div>
{#if error}<div class="ol-warn te-pre te-error" role="alert">{error}</div>{/if}
{#if notice}<div class="ol-muted te-saved" role="status">{notice}</div>{/if}

<style>
    /* the form and the files side by side while there is room for both, one above the other on a
       phone; the files are what is saved, so they get the same width as the form */
    .te { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(360px, 100%), 1fr)); gap: 12px 16px; align-items: start; }
    .te-form, .te-files { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
    .te-field { display: flex; flex-direction: column; gap: 6px; margin: 0; padding: 0; border: 0; min-width: 0; }
    .te-lbl { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; padding: 0; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); }
    .te-name { display: flex; align-items: center; gap: 2px; min-width: 0; }
    .te-name input { flex: 1; min-width: 0; }
    .te-row { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
    .te-stack { display: flex; flex-direction: column; gap: 4px; }
    .te-stack > span { color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); }
    .te-checkbox { display: flex; align-items: center; gap: 6px; }
    .te-wide { width: 100%; box-sizing: border-box; }
    .te-short { width: 10em; max-width: 100%; box-sizing: border-box; }
    .te-check { font-size: var(--hmm-font-size-small); }
    .te-next { margin: 2px 0 0; padding-left: 18px; }
    .te-note { margin: 0; font-size: var(--hmm-font-size-small); }
    .te-text { width: 100%; box-sizing: border-box; min-height: 150px; resize: vertical; font-size: 12px; line-height: 1.4; white-space: pre; }
    .te-small { min-height: 18px; padding: 0 6px; font-size: var(--hmm-font-size-small); line-height: 16px; }
    .te-pre { white-space: pre-wrap; overflow-wrap: anywhere; }
    .te-error, .te-saved { margin-top: 8px; }
</style>
