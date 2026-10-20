<script lang="ts">
    /*
     * openccu-lite task 340: the Status LED section of System → Hardware (HardwarePage.svelte), the
     * whole former Status LED page; its headings are one level down under the section's.
     *
     * Task 95: System → Status LED. The RPI-RF-MOD's RGB LED (Charly, CCU3) is occulited's now; this
     * page says what it shows and why, and decides what it shows. The simple view is what most
     * people need - on or off, the look when all is fine, the night, six switches - and everything
     * else (order, colours, patterns, the external control, locate) sits behind "Customise",
     * remembered per browser. On a box without a status LED the page says so, and offers the red
     * power LED as an error light and the board LEDs' switch, which moved here from Network.
     *
     * Task 315: on an RPI-RF-MOD driven over PWM (hardware.brightness) a colour is any #rrggbb -
     * the seven names as presets beside a free picker - the animations gain breathe, and the
     * brightness, a cross-fade between looks and the night's dimming are settings. A row's look is
     * edited in one form that opens from the row's Change… button, for the states, Everything fine
     * and Locate alike (task 56, B-73: inline, under that row); the row itself shows the swatch and
     * says the look.
     */
    import {onMount, tick} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import {auth} from '../lib/auth.svelte';
    import {ask} from '../lib/dialog.svelte';
    import {link} from '../lib/router.svelte';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import Notice from '../lib/Notice.svelte';
    import LEDSwatch from '../lib/LEDSwatch.svelte';
    import {revealDetails} from '../lib/reveal';
    import {moved} from '../lib/sortable';
    import {Sortable} from '../lib/sortable.svelte';
    import SortHandle from '../lib/SortHandle.svelte';
    import {NAMED, RADIO_STATES, SIMPLE_STATES, backgroundOf, changed, dimsAnything, hexOf, normalChoice, normalizeLook, overNormalOption, radioLevels, secondColors, type LEDConfig, type LEDLook, type LEDRadioLevel, type LEDState, type LEDStateConfig, type LEDView} from '../lib/led';

    interface BoardLEDs {
        disabled: boolean;
        leds: {name: string; path: string; trigger: string; normal: string}[];
    }

    let view = $state<LEDView | null>(null);
    let st = $state<LEDState | null>(null);
    let boards = $state<BoardLEDs | null>(null);
    let draft = $state<LEDConfig | null>(null);
    let error = $state('');
    let notice = $state('');
    let busy = $state('');
    let advanced = $state(false);
    const admin = $derived(auth.role === 'admin');
    const dirty = $derived(changed(view?.config ?? null, draft));
    const colors = $derived(view?.hardware.colors?.length ? view.hardware.colors : ['red', 'green', 'blue', 'yellow', 'cyan', 'magenta', 'white', 'off']);
    // task 315: the LED has levels - any colour, breathe, brightness, fades, the night's dimming
    const pwm = $derived(!!view?.hardware.brightness);
    // openccu-lite task 326: the RPI-RF-MOD on a radio adapter, whose LED is on/off
    const adapter = $derived((view?.hardware.kind ?? '').startsWith('hb-rf-'));

    const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

    const life = pageLife();
    onMount(() => {
        try {
            advanced = localStorage.getItem('ol.led.advanced') === '1';
        } catch {
            /* storage may be unavailable */
        }
        void load();
        // task 177: no polls while another page shows, a refresh when it comes back
        const id = setInterval(() => life.active && loadState(), 5000);
        const stopReturn = life.onReturn(() => void loadState());
        return () => {
            stopReturn();
            clearInterval(id);
        };
    });

    async function load() {
        try {
            const [v, s] = await Promise.all([api.get<LEDView>('/api/system/v1/led'), api.get<LEDState>('/api/system/v1/led/state')]);
            const keep = draft !== null && changed(view?.config ?? null, draft);
            view = v;
            st = s;
            if (!keep) draft = clone(v.config);
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
        try {
            boards = await api.get<BoardLEDs>('/api/system/v1/leds');
        } catch {
            boards = null;
        }
    }

    async function loadState() {
        try {
            st = await api.get<LEDState>('/api/system/v1/led/state');
        } catch {
            /* the next poll tries again */
        }
    }

    function onToggleAdvanced(e: Event) {
        advanced = (e.currentTarget as HTMLDetailsElement).open;
        try {
            localStorage.setItem('ol.led.advanced', advanced ? '1' : '0');
        } catch {
            /* ignore */
        }
    }

    async function run(what: string, fn: () => Promise<void>) {
        busy = what;
        notice = '';
        try {
            await fn();
            error = '';
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    const save = () =>
        run('save', async () => {
            if (!draft) return;
            view = await api.put<LEDView>('/api/system/v1/led', draft);
            draft = clone(view.config);
            rebase();
            notice = t('Saved.');
            await loadState();
        });

    function discard() {
        if (view) draft = clone(view.config);
        rebase();
    }

    function reset() {
        if (view && draft) draft = {...clone(view.defaults), pwr_error_light: draft.pwr_error_light};
        rebase();
    }

    /** the page's Save, Discard and Reset replace the draft: an open form starts again from that */
    function rebase() {
        if (editing) editSnapshot = JSON.stringify(subject(editing));
    }

    async function setEnabled(e: Event) {
        const box = e.currentTarget as HTMLInputElement;
        if (!draft) return;
        if (!box.checked && !(await ask({message: t('The LED will stay dark, errors included. Only booting, shutdown and the recovery system still light it.'), confirm: t('Switch off')}))) {
            box.checked = true;
            return;
        }
        draft.enabled = box.checked;
    }

    const locate = () =>
        run('locate', async () => {
            st = st?.locate ? await api.del<LEDState>('/api/system/v1/led/locate') : await api.post<LEDState>('/api/system/v1/led/locate', {});
        });

    const test = (l: LEDLook) =>
        run('test', async () => {
            st = await api.post<LEDState>('/api/system/v1/led/preview', normalizeLook(l, colors));
        });

    const unmute = () =>
        run('unmute', async () => {
            st = await api.del<LEDState>('/api/system/v1/led/mute');
        });

    const clearOverrides = () =>
        run('overrides', async () => {
            st = await api.del<LEDState>('/api/system/v1/led/overrides');
        });

    const toggleBoards = () =>
        run('boards', async () => {
            if (boards) boards = await api.put<BoardLEDs>('/api/system/v1/leds', {disabled: !boards.disabled});
        });

    function stateRow(id: string): LEDStateConfig | undefined {
        return draft?.states.find((s) => s.id === id);
    }

    function lookOf(s: Partial<LEDLook>): LEDLook {
        return {color: s.color ?? 'off', pattern: s.pattern ?? 'solid', ...(s.color2 ? {color2: s.color2} : {}), ...(s.over_normal ? {over_normal: true} : {})};
    }

    /** Applies a change of colour, pattern or blinking over normal to a row (or to normal, or locate) in the API's spelling. */
    function setLook(target: Partial<LEDLook>, patch: Partial<LEDLook>) {
        const n = normalizeLook({...lookOf(target), ...patch}, colors);
        target.color = n.color;
        target.pattern = n.pattern;
        if (n.color2) target.color2 = n.color2;
        else delete target.color2;
        if (n.over_normal) target.over_normal = true;
        else delete target.over_normal;
    }

    /** The colour a look's dark phase shows on the box with the draft's settings (blinking over normal), '' for dark. */
    function bgOf(l: Partial<LEDLook>): string {
        return draft ? backgroundOf(l, draft.normal, draft.enabled) : '';
    }

    function setNormal(choice: 'blue' | 'green' | 'off') {
        if (draft) draft.normal = {color: choice, pattern: 'solid'};
    }

    // task 315: one form for every row's look, opened from the row's Change… button. Task 56 and
    // B-73 (the maintainer, 2026-10-15): it opens inline, directly under the row - or the Settings
    // line - whose button opened it, inside the same panel; one at a time. It edits the draft as
    // before and remembers what the row was, so Cancel and Esc put it back and opening another row
    // with changes asks first; Save saves the page's configuration and closes it. `place` is where
    // it opened: the row's state id, normal-row, locate, or normal (the Settings line).
    type Editing = ({kind: 'state'; id: string} | {kind: 'normal'} | {kind: 'locate'} | {kind: 'radio'; id: string}) & {place: string};
    type EditTarget = {kind: 'state'; id: string} | {kind: 'normal'} | {kind: 'locate'} | {kind: 'radio'; id: string};
    let editing = $state<Editing | null>(null);
    let editSnapshot = '';
    let editButtons = $state<Record<string, HTMLButtonElement>>({});
    let formEl = $state<HTMLElement | null>(null);
    function editKey(e: EditTarget): string {
        return e.kind === 'state' || e.kind === 'radio' ? e.id : e.kind;
    }
    // openccu-lite task 316: the radio rows' levels - a threshold, its switch and its look each
    function levelsOf(id: string): LEDRadioLevel[] {
        return draft ? radioLevels(draft, id) : [];
    }
    function setThreshold(lv: LEDRadioLevel, e: Event) {
        lv.threshold = Math.max(1, Math.min(99, Math.round(Number((e.currentTarget as HTMLInputElement).value) || lv.threshold)));
    }
    /** the row's one-line summary: the enabled levels' thresholds and looks */
    function radioSummary(id: string): string {
        const on = levelsOf(id).filter((l) => l.enabled);
        if (!on.length) return t('no level switched on');
        return on.map((l) => t('> {n} %: {look}', {n: l.threshold, look: lookText(lookOf(l), bgOf(l))})).join(' · ');
    }
    /** what the form changes: the row's look (normal, locate, a state) or a radio row's levels */
    function subject(e: EditTarget): object | null {
        return e.kind === 'radio' ? levelsOf(e.id) : target(e);
    }
    function formDirty(): boolean {
        return !!editing && JSON.stringify(subject(editing)) !== editSnapshot;
    }
    /** puts the form's subject back to what it was when the form opened */
    function restoreEdit() {
        if (!editing || !editSnapshot) return;
        const was = JSON.parse(editSnapshot) as unknown;
        const now = subject(editing);
        if (Array.isArray(now) && Array.isArray(was)) now.splice(0, now.length, ...was);
        else if (now && was && typeof was === 'object') {
            for (const k of Object.keys(now)) delete (now as Record<string, unknown>)[k];
            Object.assign(now, was);
        }
    }
    /** the form may close: true without changes, or when the reader drops them */
    async function mayLeave(): Promise<boolean> {
        if (!editing || !formDirty()) return true;
        if (!(await ask({message: t('Discard the changes to {name}?', {name: editName(editing)}), confirm: t('Discard')}))) return false;
        restoreEdit();
        return true;
    }
    async function edit(e: EditTarget, place = editKey(e)) {
        if (editing?.place === place) {
            // the open form's own button closes it
            if (await mayLeave()) closeEdit();
            return;
        }
        if (!(await mayLeave())) return;
        editing = {...e, place};
        editSnapshot = JSON.stringify(subject(editing));
        await tick();
        formEl?.scrollIntoView({block: 'nearest'});
        (formEl?.querySelector<HTMLElement>('select, input') ?? formEl?.querySelector<HTMLElement>('button'))?.focus();
    }
    function closeEdit() {
        const place = editing?.place;
        editing = null;
        editSnapshot = '';
        if (place) editButtons[place]?.focus();
    }
    function cancelEdit() {
        restoreEdit();
        closeEdit();
    }
    async function saveEdit() {
        await save();
        if (!error) closeEdit();
    }
    function formKey(ev: KeyboardEvent) {
        // the row's own keys (Alt+↑/↓ moves it) are not the form's
        ev.stopPropagation();
        if (ev.key === 'Escape') {
            ev.preventDefault();
            cancelEdit();
        }
    }
    const isEditing = (place: string) => editing?.place === place;
    /** the object the panel edits: a state's row, normal, or locate */
    function target(e: EditTarget): Partial<LEDLook> | null {
        if (!draft) return null;
        if (e.kind === 'normal') return draft.normal;
        if (e.kind === 'locate') return draft.locate;
        if (e.kind === 'radio') return null;
        return stateRow(e.id) ?? null;
    }
    function editName(e: EditTarget): string {
        return stateLabel(e.kind === 'state' || e.kind === 'radio' ? e.id : e.kind);
    }
    /** the colour select's value: a name, or "custom" for a free colour */
    function presetOf(c: string): string {
        return c in NAMED ? c : 'custom';
    }
    function pickColor(tgt: Partial<LEDLook>, value: string) {
        if (value !== 'custom') setLook(tgt, {color: value});
    }
    /** the brightness slider's and the night level's percent, from the inputs (10-100 and 1-100) */
    function percent(e: Event, min: number): number {
        return Math.max(min, Math.min(100, Math.round(Number((e.currentTarget as HTMLInputElement).value) || 100)));
    }

    // task 162: the states are ordered by their handle - drag it, ↑/↓ on it, Alt+↑/↓ in the row; the
    // fixed rows have none, and neither has a user who may only look
    let stateList = $state<HTMLElement | null>(null);
    const sort = new Sortable({
        rows: () => Array.from(stateList?.querySelectorAll<HTMLElement>(':scope > li[data-led-row]') ?? []),
        length: () => draft?.states.length ?? 0,
        name: (i) => stateLabel(draft?.states[i]?.id ?? ''),
        commit: (from, to) => {
            if (draft) draft.states = moved(draft.states, from, to);
        },
        enabled: () => admin,
    });

    function toggleWarning(id: string, shown: boolean) {
        if (!draft) return;
        draft.warnings_off = shown ? draft.warnings_off.filter((w) => w !== id) : [...draft.warnings_off, id];
    }

    function patternsFor(current: string, withAlternate = true): string[] {
        const list = (view?.hardware.patterns?.length ? view.hardware.patterns : ['solid', 'slow', 'fast', 'flash', 'double', 'alternate']).filter((p) => withAlternate || p !== 'alternate');
        return list.includes(current) || !current ? list : [...list, current];
    }

    function colorName(c: string): string {
        if (!(c in NAMED) && hexOf(c)) return hexOf(c);
        switch (c) {
            case 'red':
                return t('Red');
            case 'green':
                return t('Green');
            case 'blue':
                return t('Blue');
            case 'yellow':
                return t('Yellow');
            case 'cyan':
                return t('Cyan');
            case 'magenta':
                return t('Magenta');
            case 'white':
                return t('White');
        }
        return t('Dark');
    }

    function patternName(p: string): string {
        switch (p) {
            case 'slow':
                return t('slow blink');
            case 'fast':
                return t('fast blink');
            case 'flash':
                return t('short flash');
            case 'double':
                return t('double flash');
            case 'breathe':
                return t('breathing');
            case 'alternate':
                return t('alternating');
        }
        return t('steady');
    }

    function lookText(l: LEDLook, background = '', dim = 0): string {
        let text: string;
        if (l.color === 'off') return t('Dark');
        if (l.pattern === 'alternate' && l.color2) text = t('{a} and {b}, alternating', {a: colorName(l.color), b: colorName(l.color2).toLowerCase()});
        else if (background && background !== 'off') text = t('{color}, {pattern} over {background}', {color: colorName(l.color), pattern: patternName(l.pattern), background: colorName(background).toLowerCase()});
        else text = t('{color}, {pattern}', {color: colorName(l.color), pattern: patternName(l.pattern)});
        // task 315: the night's level, as the state view says it
        return dim > 0 && dim < 100 ? t('{look}, dimmed to {n} %', {look: text, n: dim}) : text;
    }

    function stateLabel(id: string): string {
        switch (id) {
            case 'radio-down':
                return t('Radio or interface down');
            case 'no-network':
                return t('No network');
            case 'service-failed':
                return t('A service has failed');
            case 'storage-replace':
                return t('Storage should be replaced');
            case 'interfaces-starting':
                return t('Interfaces starting');
            case 'radio-duty-cycle':
                return t('Radio: duty cycle');
            case 'radio-carrier-sense':
                return t('Radio: carrier sense');
            case 'external':
                return t('External control');
            case 'status-warning':
                return t('Status page warnings');
            case 'firmware-transfer':
                return t('Device firmware being transferred');
            case 'firmware-ready':
                return t('Device firmware waiting for installation');
            case 'system-update':
                return t('System update available');
            case 'addon-update':
                return t('Addon updates available');
            case 'no-internet':
                return t('No internet');
            case 'booting':
                return t('Booting');
            case 'shutdown':
                return t('Shutting down');
            case 'preview':
                return t('Test');
            case 'locate':
                return t('Locate');
        }
        return t('Everything fine');
    }

    function stateMeaning(id: string): string {
        switch (id) {
            case 'radio-down':
                // hs485d counts only where a wired gateway is configured (task 158's follow-up)
                return t('multimacd, rfd or hmipserver is not running, or hs485d where a wired gateway is configured');
            case 'no-network':
                return t('no network link or no address');
            case 'service-failed':
                return t('a system service has failed');
            case 'storage-replace':
                return t('the storage check says the SD card or disk should be replaced');
            case 'interfaces-starting':
                // task 158: at the boot and whenever one of them is restarted
                return t('multimacd, rfd, hmipserver or hs485d is starting');
            case 'radio-duty-cycle':
                // openccu-lite task 316: the Status page's sampling, with hysteresis; the look is the level's
                return t('the duty cycle of a radio interface is above one of the levels below (the Interfaces page\'s values; a level holds until the value is 5 points under its threshold)');
            case 'radio-carrier-sense':
                return t('the carrier sense of a radio interface is above one of the levels below (the Interfaces page\'s values; a level holds until the value is 5 points under its threshold)');
            case 'external':
                return t('a colour set through the API, for example by Home Assistant or Node-RED');
            case 'status-warning':
                return t('a warning on the Status page that nobody silenced');
            case 'firmware-transfer':
                // occulited task 57: the Updates page's device firmware, no radio traffic of the LED's own
                return t('the firmware of a device is being transferred to it over the air');
            case 'firmware-ready':
                return t('a device has received its firmware and waits for the installation on the Updates page');
            case 'system-update':
                return t('a newer openccu-lite release is available');
            case 'addon-update':
                return t('an installed addon has an update');
            case 'no-internet':
                // task 95 (D-67): the check connects to hosts the box talks to anyway, and the page names them
                return view?.internet_hosts?.length
                    ? t('the system has an address but reaches none of the hosts it talks to anyway: {hosts}', {hosts: view.internet_hosts.join(', ')})
                    : t('nothing to check against: the addon catalogue is switched off or local, and ACME is not configured');
            case 'booting':
                return t('the system is starting, until the radio is known');
            case 'shutdown':
                return t('the system is shutting down or restarting');
            case 'preview':
                return t('the Test button, for ten seconds');
            case 'locate':
                return t('the Locate button, to find the system');
        }
        return t('nothing else is active');
    }

    function stateHref(id: string): string {
        switch (id) {
            case 'radio-down':
            case 'interfaces-starting':
            case 'radio-duty-cycle':
            case 'radio-carrier-sense':
                return '/radio';
            case 'no-network':
            case 'no-internet':
                return '/system/network';
            case 'service-failed':
                return '/system/services';
            case 'status-warning':
            case 'storage-replace':
            case 'system-update':
                return '/';
            case 'addon-update':
                return '/addons';
            case 'firmware-transfer':
            case 'firmware-ready':
                return '/system/updates';
        }
        return '';
    }

    function reasonText(s: LEDState): string {
        const sh = s.shown;
        switch (sh.source) {
            case 'state':
                return sh.detail ? `${stateLabel(sh.id)}: ${sh.detail}` : stateLabel(sh.id);
            case 'override':
                return t('Set through the API as {id}', {id: sh.id});
            case 'locate':
                return t('Locating the system');
            case 'preview':
                return t('Testing a look');
            case 'night':
                return t('Night mode');
            case 'feedback':
                // openccu-lite task 164: the button's own short light
                return t('Radio module button');
            case 'off':
                return t('The status LED is switched off');
            case 'fixed':
                return stateLabel(sh.id);
        }
        return t('Everything is fine');
    }

    function noLEDReason(r?: string): string {
        switch (r) {
            case 'no-module':
                return t('The radio module has no status LED.');
            case 'no-leds':
                return t('The LED devices of the radio module are missing.');
            case 'virtual':
                return t('This is a virtual machine.');
            case 'container':
                return t('In a container the LED belongs to the host.');
            case 'hm-lgw':
                return t('In LAN gateway mode the LED shows the blue of the gateway.');
            case 'detecting':
                return t('The radio module is still being detected.');
        }
        return '';
    }

    function timeOf(iso: string): string {
        return new Date(iso).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
    }

    // the two fixed states the scripts share with the controller, in the advanced list's order
    const FIXED_ROWS: {id: string; look: LEDLook}[] = [
        {id: 'shutdown', look: {color: 'yellow', pattern: 'fast'}},
        {id: 'booting', look: {color: 'yellow', pattern: 'solid'}},
    ];
</script>

<!-- task 56, B-73: the one inline form, rendered under the row whose Change… opened it -->
{#snippet editForm()}
    {#if editing && draft}
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div class="ol-led-form" role="group" aria-label={editing.kind === 'radio' ? t('Levels of {name}', {name: editName(editing)}) : t('Look of {name}', {name: editName(editing)})} bind:this={formEl} onkeydown={formKey} data-led-form={editing.place}>
            <h4 class="ol-led-form-title">{editing.kind === 'radio' ? t('Levels of {name}', {name: editName(editing)}) : t('Look of {name}', {name: editName(editing)})}</h4>
                    {#if editing?.kind === 'radio'}
                        {@const rid = editing.id}
                        <!-- openccu-lite task 316: three levels, each with its switch, threshold, colour, animation and overlay/replace -->
                        <div class="ol-led-panel" data-led-panel={rid}>
                            <p class="ol-muted">{t('The highest level the value is above decides; a level holds until the value has fallen 5 points below its threshold. Overlay shows the level over the colour of Everything fine, Replace instead of it.')}</p>
                            <ol class="ol-led-levels">
                                {#each levelsOf(rid) as lv, i (i)}
                                    <li class="ol-led-level" data-led-level={i + 1}>
                                        <label class="ol-led-check">
                                            <input type="checkbox" checked={lv.enabled} onchange={(e) => (lv.enabled = e.currentTarget.checked)} aria-label={t('Level {n} of {name}', {n: i + 1, name: editName(editing)})} />
                                            <LEDSwatch look={lookOf(lv)} background={bgOf(lv)} />
                                            {t('above')}
                                            <input class="hmm-input ol-led-num" type="number" min="1" max="99" value={lv.threshold} onchange={(e) => setThreshold(lv, e)} aria-label={t('Threshold of level {n} of {name}', {n: i + 1, name: editName(editing)})} />
                                            %
                                        </label>
                                        <span class="ol-led-controls">
                                            <select class="hmm-select" aria-label={t('Colour of level {n} of {name}', {n: i + 1, name: editName(editing)})} value={presetOf(lv.color)} onchange={(e) => pickColor(lv, e.currentTarget.value)}>
                                                {#each colors.filter((c) => c !== 'off') as c (c)}<option value={c}>{colorName(c)}</option>{/each}
                                                {#if presetOf(lv.color) === 'custom'}<option value="custom">{t('Custom colour')}</option>{/if}
                                            </select>
                                            {#if pwm}<input class="ol-led-color" type="color" value={hexOf(lv.color) || '#ffff00'} oninput={(e) => setLook(lv, {color: e.currentTarget.value})} aria-label={t('Custom colour of level {n} of {name}', {n: i + 1, name: editName(editing)})} data-led-picker />{/if}
                                            <select class="hmm-select" aria-label={t('Pattern of level {n} of {name}', {n: i + 1, name: editName(editing)})} value={lv.pattern} onchange={(e) => setLook(lv, {pattern: e.currentTarget.value})}>
                                                {#each patternsFor(lv.pattern, false) as p (p)}<option value={p}>{patternName(p)}</option>{/each}
                                            </select>
                                            <div class="ol-seg" role="radiogroup" aria-label={t('Level {n} of {name}: overlay or replace', {n: i + 1, name: editName(editing)})}>
                                                <button type="button" role="radio" aria-checked={!!lv.over_normal} onclick={() => setLook(lv, {over_normal: true})} disabled={overNormalOption(lv, draft.normal) === 'no'} title={overNormalOption(lv, draft.normal) === 'no' ? t('A steady light cannot be an overlay.') : undefined}>{t('Overlay')}</button>
                                                <button type="button" role="radio" aria-checked={!lv.over_normal} onclick={() => setLook(lv, {over_normal: false})}>{t('Replace')}</button>
                                            </div>
                                            {#if admin}<button type="button" class="hmm-button" onclick={() => test(lookOf(lv))} disabled={busy !== ''}>{t('Test')}</button>{/if}
                                        </span>
                                    </li>
                                {/each}
                            </ol>
                        </div>
                    {:else if editing}
                        {@const tgt = target(editing)}
                        {#if tgt}
                            {@const name = editName(editing)}
                            {@const look = lookOf(tgt)}
                            {@const bg = editing.kind === 'state' ? bgOf(tgt) : ''}
                            <div class="ol-led-panel" data-led-panel={editKey(editing)}>
                                <div class="ol-led-panel-preview">
                                    <LEDSwatch look={look} background={bg} size={28} />
                                    <span>{lookText(look, bg)}</span>
                                    {#if admin && look.color !== 'off'}<button type="button" class="hmm-button" onclick={() => test(look)} disabled={busy !== ''}>{t('Test')}</button>{/if}
                                </div>
                                <div class="ol-led-controls">
                                    <label class="ol-led-check">
                                        {t('Colour')}
                                        <select class="hmm-select" aria-label={t('Colour of {name}', {name})} value={presetOf(tgt.color ?? 'off')} onchange={(e) => pickColor(tgt, e.currentTarget.value)}>
                                            {#each colors.filter((c) => editing?.kind !== 'locate' || c !== 'off') as c (c)}<option value={c}>{colorName(c)}</option>{/each}
                                            {#if presetOf(tgt.color ?? 'off') === 'custom'}<option value="custom">{t('Custom colour')}</option>{/if}
                                        </select>
                                        {#if pwm && tgt.color !== 'off'}
                                            <!-- task 315: any colour, mixed on the LED -->
                                            <input class="ol-led-color" type="color" value={hexOf(tgt.color ?? 'off') || '#000000'} oninput={(e) => setLook(tgt, {color: e.currentTarget.value})} aria-label={t('Custom colour of {name}', {name})} data-led-picker />
                                        {/if}
                                    </label>
                                    {#if tgt.color !== 'off'}
                                        <label class="ol-led-check">
                                            {t('Animation')}
                                            <select class="hmm-select" aria-label={t('Pattern of {name}', {name})} value={tgt.pattern ?? 'solid'} onchange={(e) => setLook(tgt, {pattern: e.currentTarget.value})}>
                                                {#each patternsFor(tgt.pattern ?? '', editing.kind !== 'locate') as p (p)}<option value={p}>{patternName(p)}</option>{/each}
                                            </select>
                                        </label>
                                    {/if}
                                    {#if tgt.pattern === 'alternate'}
                                        <label class="ol-led-check">
                                            {t('Second colour')}
                                            <select class="hmm-select" aria-label={t('Second colour of {name}', {name})} value={tgt.color2} onchange={(e) => setLook(tgt, {color2: e.currentTarget.value})}>
                                                {#each secondColors(tgt.color ?? '', colors) as c (c)}<option value={c}>{colorName(c)}</option>{/each}
                                            </select>
                                        </label>
                                    {/if}
                                    {#if editing.kind === 'locate'}
                                        <label class="ol-led-check">
                                            {t('for')}
                                            <input class="hmm-input ol-led-num" type="number" min="1" max="60" value={Math.round(draft.locate.duration_s / 60)} onchange={(e) => draft && (draft.locate.duration_s = Math.max(1, Math.min(60, Number(e.currentTarget.value) || 5)) * 60)} aria-label={t('Minutes')} />
                                            {t('min')}
                                        </label>
                                    {/if}
                                </div>
                                <!-- task 134: a blink over the normal colour instead of over dark, for the patterns with a dark phase -->
                                {#if editing.kind === 'state' && overNormalOption(tgt, draft.normal) === 'yes'}
                                    <div class="ol-led-subs" data-led-over={editing.id}>
                                        <label class="ol-led-check">
                                            <input type="checkbox" checked={!!tgt.over_normal} onchange={(e) => setLook(tgt, {over_normal: e.currentTarget.checked})} />
                                            {t('Blink over the normal colour')}
                                            <Help>{t('The dark phase of the blink shows the colour of Everything fine. While that is off, and at night, the LED blinks over dark.')}</Help>
                                        </label>
                                    </div>
                                {:else if editing.kind === 'state' && overNormalOption(tgt, draft.normal) === 'same'}
                                    <div class="ol-led-subs ol-muted" data-led-over={editing.id}>{t('Blinking over the normal colour is not offered: it is the same colour, the blink would not be seen.')}</div>
                                {/if}
                            </div>
                        {/if}
                    {/if}
            <div class="ol-actions ol-led-form-actions">
                <button type="button" class="hmm-button primary" onclick={saveEdit} disabled={busy !== ''} data-led-form-save>{t('Save')}</button>
                <button type="button" class="hmm-button" onclick={cancelEdit} data-led-form-cancel>{t('Cancel')}</button>
            </div>
        </div>
    {/if}
{/snippet}

<!--
    openccu-lite task 58 (the maintainer, 2026-10-15): the LED settings in panels under a heading
    *RPI-RF-MOD LED* - Settings, Show on the LED, Customise states and order, What the colours mean -
    and the board's LEDs under *Raspberry Pi LEDs*, each section only where the hardware is.
-->
{#if !view || !st || !draft}
    <Loading {error} />
{:else}
    {@const piLEDs = (boards?.leds.length ?? 0) > 0 || (!view.available && !!view.pwr)}
    {@const module = view.available || view.reason === 'detecting' || view.reason === 'hm-lgw'}
    {#if error}<div class="ol-notice error">{error}</div>{/if}
    {#if notice}<div class="ol-notice">{notice}</div>{/if}
    {#if module}
        <h2 id="status-led">{t('RPI-RF-MOD LED')}<Help>{t('The RGB LED on the radio module of a Charly or a CCU3. The system shows its state on it, and this section decides what it shows. Booting (yellow) and the recovery system (magenta) are fixed.')}</Help></h2>
    {/if}
    {#if !view.available}
        {#if module || !piLEDs}<Notice id="no-led"><strong>{t('This system has no status LED.')}</strong> {noLEDReason(view.reason)}</Notice>{/if}
    {:else}
        <div class="ol-card ol-led-now" data-led-now={st.shown.id}>
            <LEDSwatch look={st.shown} background={st.shown.background} dim={st.shown.dim ?? 0} size={44} label={lookText(st.shown, st.shown.background, st.shown.dim)} />
            <div class="ol-led-now-text">
                <div class="ol-led-now-look">{lookText(st.shown, st.shown.background, st.shown.dim)}</div>
                <div class="ol-muted">{reasonText(st)} · {t('since {time}', {time: timeOf(st.shown.since)})}</div>
                {#if st.shown.source === 'state' && stateHref(st.shown.id)}<a href={stateHref(st.shown.id)} use:link>{t('Open page')}</a>{/if}
            </div>
            {#if admin}
                <div class="ol-led-now-actions">
                    <button class="hmm-button" onclick={locate} disabled={busy !== ''}>{st.locate ? t('Stop locating') : t('Locate')}</button>
                    <span class="ol-muted">{t('{look} for {n} minutes', {look: lookText(view.config.locate), n: Math.round(view.config.locate.duration_s / 60)})}</span>
                </div>
            {/if}
        </div>
        {#if st.muted_until}
            <!-- openccu-lite task 164: the button's (or the API's) mute -->
            <div class="ol-notice ol-led-muted" data-led-muted>
                {t('The LED warnings are muted until {time}.', {time: timeOf(st.muted_until)})}
                {#if admin}<button class="hmm-button" onclick={unmute} disabled={busy !== ''} data-led-unmute>{t('Unmute')}</button>{/if}
            </div>
        {/if}
        {#if st.conflict}<Notice kind="warning" id="led-conflict">{t('Another process keeps changing the LED. The system sets it again when its own state changes.')}</Notice>{/if}
        {#if st.write_error}<Notice kind="error" id="led-write">{t('The LED could not be written:')} {st.write_error}</Notice>{/if}
        {#if st.overrides.length}
            <div class="ol-led-overrides" data-led-overrides>
                <span class="ol-muted">{t('Set through the API:')}</span>
                {#each st.overrides as o (o.id)}
                    <span class="ol-led-override"><LEDSwatch look={o} /> <span class="hmm-mono">{o.id}</span> <span class="ol-muted">{o.until ? t('until {time}', {time: timeOf(o.until)}) : t('until cleared')}</span></span>
                {/each}
                {#if admin}<button class="hmm-button" onclick={clearOverrides} disabled={busy !== ''}>{t('Clear')}</button>{/if}
            </div>
        {/if}

        <div class="ol-led-panels">
            <section class="ol-card" data-panel="led-settings">
                <div class="ol-card-head"><h3 class="ol-card-title">{t('Settings')}</h3></div>
                <!-- task 315: what this LED can do -->
                <!-- openccu-lite task 326: the radio adapter's own LED (HB-RF-USB, -USB-2, -ETH) is on/off, whatever image -->
                <!-- task 90 (maintainer): a dimmable LED gets no sentence at all - the controls below say it -->
                {#if !pwm}
                <p class="ol-muted" data-led-capability={adapter ? 'adapter' : 'on-off'}>
                    {#if adapter}{t('This is an on/off LED on the radio adapter: the seven colours and blinking, no dimming, no breathing.')}{#if view?.hardware.kind === 'hb-rf-eth'}{' '}<span data-led-eth-hint>{t("The HB-RF-ETH's own web interface sets the LED's brightness.")}</span>{/if}{:else}{t('This LED switches each of its three colours on or off: the seven colours, blinking, no dimming. A dimmable LED needs the current openccu-lite image on a Raspberry Pi with the radio module on its header.')}{/if}
                </p>
                {/if}
                <fieldset class="ol-led-settings" disabled={!admin}>
                <label class="ol-led-check"><input type="checkbox" checked={draft.enabled} onchange={setEnabled} data-led="enabled" /> {t('Status LED on')}</label>

                <div class="ol-led-line">
                    <span class="ol-led-key">{t('When everything is fine')}</span>
                    <div class="ol-seg" role="radiogroup" aria-label={t('When everything is fine')}>
                        <button type="button" role="radio" aria-checked={normalChoice(draft.normal) === 'blue'} onclick={() => setNormal('blue')}>{t('Blue')}</button>
                        <button type="button" role="radio" aria-checked={normalChoice(draft.normal) === 'green'} onclick={() => setNormal('green')}>{t('Green')}</button>
                        <button type="button" role="radio" aria-checked={normalChoice(draft.normal) === 'off'} onclick={() => setNormal('off')}>{t('Off')}</button>
                    </div>
                    {#if normalChoice(draft.normal) === 'custom'}<LEDSwatch look={draft.normal} /><span class="ol-muted">{lookText(draft.normal)}</span>{/if}
                    {#if admin}<button type="button" class="hmm-button" onclick={() => edit({kind: 'normal'}, 'normal')} bind:this={editButtons.normal} aria-expanded={isEditing('normal')} data-action="edit-normal">{t('Change…')}</button>{/if}
                </div>
                {#if isEditing('normal')}{@render editForm()}{/if}

                {#if pwm}
                    <!-- task 315: the LED's level and the cross-fade, where the hardware has levels -->
                    <div class="ol-led-line">
                        <label class="ol-led-check ol-led-key-label">
                            <span class="ol-led-key">{t('Brightness')}</span>
                            <input class="ol-led-range" type="range" min="10" max="100" step="5" value={draft.brightness} oninput={(e) => draft && (draft.brightness = percent(e, 10))} data-led="brightness" aria-label={t('Brightness')} />
                            <span class="ol-led-pct">{draft.brightness} %</span>
                        </label>
                        <label class="ol-led-check">
                            <input type="checkbox" checked={draft.fade !== false} onchange={(e) => draft && (draft.fade = e.currentTarget.checked)} data-led="fade" />
                            {t('Fade between looks')}
                            <Help>{t('A change of colour or animation blends over 300 ms instead of switching hard.')}</Help>
                        </label>
                    </div>
                {:else if dimsAnything(draft)}
                    <div class="ol-muted" data-led-dim-note>{t('The brightness and the night level in this configuration take effect on a dimmable LED only.')}</div>
                {/if}

                <label class="ol-led-check">
                    <input type="checkbox" bind:checked={draft.night.enabled} data-led="night" />
                    {t('Night mode')}
                    <Help>{t('Between the two times the LED shows less: only errors, or nothing. Locate and Test still light it.')}</Help>
                </label>
                {#if draft.night.enabled}
                    <div class="ol-led-line ol-led-sub">
                        <label class="ol-led-check">{t('from')} <input class="hmm-input" type="time" bind:value={draft.night.from} /></label>
                        <label class="ol-led-check">{t('to')} <input class="hmm-input" type="time" bind:value={draft.night.to} /></label>
                    </div>
                    <div class="ol-led-line ol-led-sub">
                        <span class="ol-led-key">{t('At night show')}</span>
                        <div class="ol-seg" role="radiogroup" aria-label={t('At night show')}>
                            <button type="button" role="radio" aria-checked={draft.night.show === 'errors'} onclick={() => draft && (draft.night.show = 'errors')}>{t('Only errors')}</button>
                            <button type="button" role="radio" aria-checked={draft.night.show === 'off'} onclick={() => draft && (draft.night.show = 'off')}>{t('Nothing')}</button>
                            {#if pwm || draft.night.show === 'dimmed'}
                                <!-- task 315: everything, dimmed to the night level -->
                                <button type="button" role="radio" aria-checked={draft.night.show === 'dimmed'} onclick={() => draft && (draft.night.show = 'dimmed')}>{t('Everything, dimmed')}</button>
                            {/if}
                        </div>
                    </div>
                    {#if pwm && draft.night.show !== 'off'}
                        <div class="ol-led-line ol-led-sub">
                            <label class="ol-led-check ol-led-key-label">
                                <span class="ol-led-key">{t('Night level')}<Help>{t('How bright the LED is at night, in percent of the brightness, for what the night still shows. Locate and Test are never dimmed.')}</Help></span>
                                <input class="ol-led-range" type="range" min="5" max="100" step="5" value={draft.night.dim} oninput={(e) => draft && (draft.night.dim = percent(e, 1))} data-led="night-dim" aria-label={t('Night level')} />
                                <span class="ol-led-pct">{draft.night.dim} %</span>
                            </label>
                        </div>
                    {/if}
                    {#if draft.night.show === 'off'}<div class="ol-warn ol-led-sub">{t('A failed radio will not be visible at night.')}</div>{/if}
                {/if}
                </fieldset>
            </section>

            <section class="ol-card" data-panel="led-show">
                <div class="ol-card-head"><h3 class="ol-card-title">{t('Show on the LED')}</h3></div>
                <fieldset class="ol-led-settings" disabled={!admin}>
                <ul class="ol-led-rows">
                    {#each SIMPLE_STATES as id (id)}
                        {@const s = stateRow(id)}
                        {#if s}
                            <li data-led-simple={id}>
                                <label class="ol-led-row">
                                    <input type="checkbox" checked={s.enabled} onchange={(e) => (s.enabled = e.currentTarget.checked)} />
                                    <LEDSwatch look={lookOf(s)} background={bgOf(s)} />
                                    <span class="ol-led-row-text"><span>{stateLabel(id)}</span> <span class="ol-muted">{lookText(lookOf(s), bgOf(s))} · {stateMeaning(id)}</span></span>
                                </label>
                            </li>
                        {/if}
                    {/each}
                </ul>
                </fieldset>
            </section>

            <section class="ol-card" data-panel="led-states">
                <fieldset class="ol-led-settings" disabled={!admin}>
                <!-- task 98: both disclosures of this page open and close in place (lib/reveal.ts) -->
                <details class="ol-led-advanced" open={advanced} ontoggle={onToggleAdvanced} use:revealDetails data-led-advanced>
                    <summary>{t('Customise states and order')}</summary>
                    <p class="ol-muted">{t('The first active row decides what the LED shows. The fixed rows stay on top, and Everything fine is what is left.')}</p>
                    <!-- task 315: the one panel every row's Change… opens, for that row's look -->
                    <ol class="ol-led-list" bind:this={stateList}>
                        {#each FIXED_ROWS as row (row.id)}
                            <li class="ol-led-item fixed" data-led-fixed={row.id}>
                                <span class="ol-badge">{t('fixed')}</span>
                                <LEDSwatch look={row.look} />
                                <span class="ol-led-name">{stateLabel(row.id)}</span>
                                <span class="ol-muted">{lookText(row.look)} · {stateMeaning(row.id)}</span>
                            </li>
                        {/each}
                        <li class="ol-led-item fixed" data-led-fixed="locate">
                            <span class="ol-badge">{t('fixed')}</span>
                            <LEDSwatch look={lookOf(draft.locate)} />
                            <span class="ol-led-name">{stateLabel('locate')}</span>
                            <span class="ol-muted">{t('{look} for {n} minutes', {look: lookText(lookOf(draft.locate)), n: Math.round(draft.locate.duration_s / 60)})}</span>
                            {#if admin}<span class="ol-led-controls"><button type="button" class="hmm-button" onclick={() => edit({kind: 'locate'})} bind:this={editButtons.locate} aria-expanded={isEditing('locate')} data-action="edit">{t('Change…')}</button></span>{/if}
                            {#if isEditing('locate')}{@render editForm()}{/if}
                        </li>
                        {#each draft.states as s, i (s.id)}
                            <!-- the row takes Alt+↑/↓ from the controls inside it, bubbling up: it is no control itself -->
                            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                            <li class="ol-led-item ol-sortrow" data-led-row={s.id} class:ol-dragging={sort.dragging(s.id)} class:ol-drop-before={sort.before(i)} class:ol-drop-after={sort.after(i)} onkeydown={(ev) => sort.rowKey(ev, i)}>
                                {#if admin}<SortHandle sortable={sort} index={i} key={s.id} name={stateLabel(s.id)} />{/if}
                                <input type="checkbox" checked={s.enabled} onchange={(e) => (s.enabled = e.currentTarget.checked)} aria-label={stateLabel(s.id)} />
                                {#if RADIO_STATES.includes(s.id)}
                                    <!-- openccu-lite task 316: the look is the active level's; the row carries the switch and the place -->
                                    <LEDSwatch look={lookOf(levelsOf(s.id).find((l) => l.enabled) ?? {color: 'off'})} background={bgOf(levelsOf(s.id).find((l) => l.enabled) ?? {})} />
                                    <span class="ol-led-name">{stateLabel(s.id)}<Help>{stateMeaning(s.id)}</Help></span>
                                    <span class="ol-muted ol-led-look" data-led-look={s.id}>{radioSummary(s.id)}</span>
                                    {#if admin}
                                        <span class="ol-led-controls">
                                            <button type="button" class="hmm-button" onclick={() => edit({kind: 'radio', id: s.id})} bind:this={editButtons[s.id]} aria-expanded={isEditing(s.id)} data-action="edit">{t('Levels…')}</button>
                                        </span>
                                    {/if}
                                {:else if s.id === 'external'}
                                    <span class="ol-led-name">{stateLabel(s.id)}<Help>{t('Home Assistant, Node-RED and other programs set a colour with an API token of the role led. Where this row stands decides which states they may cover.')}</Help></span>
                                    <span class="ol-led-controls">
                                        <label class="ol-led-check">
                                            {t('at most')}
                                            <input class="hmm-input ol-led-num" type="number" min="1" max="1440" value={Math.round(draft.external.max_duration_s / 60)} onchange={(e) => draft && (draft.external.max_duration_s = Math.max(1, Math.min(1440, Number(e.currentTarget.value) || 60)) * 60)} aria-label={t('Minutes')} />
                                            {t('min')}
                                        </label>
                                        <label class="ol-led-check"><input type="checkbox" bind:checked={draft.external.allow_until_cleared} /> {t('until cleared allowed')}</label>
                                    </span>
                                {:else}
                                    <LEDSwatch look={lookOf(s)} background={bgOf(s)} />
                                    <span class="ol-led-name">{stateLabel(s.id)}</span>
                                    <span class="ol-muted ol-led-look" data-led-look={s.id}>{lookText(lookOf(s), bgOf(s))}</span>
                                    {#if admin}
                                        <span class="ol-led-controls">
                                            <button type="button" class="hmm-button" onclick={() => edit({kind: 'state', id: s.id})} bind:this={editButtons[s.id]} aria-expanded={isEditing(s.id)} data-action="edit">{t('Change…')}</button>
                                            <button type="button" class="hmm-button" onclick={() => test(lookOf(s))} disabled={busy !== '' || s.color === 'off'}>{t('Test')}</button>
                                        </span>
                                    {/if}
                                {/if}
                                {#if s.id === 'status-warning' && view.warning_ids.length}
                                    <div class="ol-led-subs">
                                        {#each view.warning_ids as w (w)}
                                            <label class="ol-led-check"><input type="checkbox" checked={!draft.warnings_off.includes(w)} onchange={(e) => toggleWarning(w, e.currentTarget.checked)} /> <span class="hmm-mono">{w}</span></label>
                                        {/each}
                                    </div>
                                {/if}
                                {#if s.id === 'service-failed'}
                                    <div class="ol-led-subs"><label class="ol-led-check"><input type="checkbox" bind:checked={draft.addon_units} /> {t('A failed addon counts as well')}</label></div>
                                {/if}
                                {#if s.id === 'no-internet'}
                                    <!-- task 95 (D-67): which host the check connects to -->
                                    <div class="ol-led-subs ol-muted" data-led-internet>{stateMeaning(s.id)}</div>
                                {/if}
                                {#if isEditing(s.id)}{@render editForm()}{/if}
                            </li>
                        {/each}
                        <li class="ol-led-item fixed" data-led-fixed="normal">
                            <span class="ol-badge">{t('last')}</span>
                            <LEDSwatch look={draft.normal} />
                            <span class="ol-led-name">{stateLabel('normal')}</span>
                            <span class="ol-muted ol-led-look" data-led-look="normal">{lookText(draft.normal)}</span>
                            {#if admin}<span class="ol-led-controls"><button type="button" class="hmm-button" onclick={() => edit({kind: 'normal'}, 'normal-row')} bind:this={editButtons['normal-row']} aria-expanded={isEditing('normal-row')} data-action="edit">{t('Change…')}</button></span>{/if}
                            {#if isEditing('normal-row')}{@render editForm()}{/if}
                        </li>
                    </ol>
                    <div class="ol-sronly" role="status" aria-live="polite">{sort.live}</div>
                    {#if admin}<div class="ol-actions"><button type="button" class="hmm-button" onclick={reset}>{t('Reset to defaults')}</button></div>{/if}
                </details>
                </fieldset>
            </section>

            <section class="ol-card" data-panel="led-legend">
            <details class="ol-led-legend" use:revealDetails data-led-legend>
                <summary>{t('What the colours mean')}</summary>
                <ul class="ol-led-rows">
                    {#each [...FIXED_ROWS].reverse() as row (row.id)}
                        <li><LEDSwatch look={row.look} /> <span>{lookText(row.look)}</span> <span class="ol-muted">{stateLabel(row.id)}</span></li>
                    {/each}
                    <li><LEDSwatch look={{color: 'magenta', pattern: 'slow'}} /> <span>{lookText({color: 'magenta', pattern: 'slow'})}</span> <span class="ol-muted">{t('Recovery system starting')}</span></li>
                    <li><LEDSwatch look={{color: 'magenta', pattern: 'solid'}} /> <span>{lookText({color: 'magenta', pattern: 'solid'})}</span> <span class="ol-muted">{t('Recovery system active')}</span></li>
                    <li><LEDSwatch look={{color: 'magenta', pattern: 'fast'}} /> <span>{lookText({color: 'magenta', pattern: 'fast'})}</span> <span class="ol-muted">{t('Update installing')}</span></li>
                    <li><LEDSwatch look={lookOf(draft.locate)} /> <span>{lookText(lookOf(draft.locate))}</span> <span class="ol-muted">{stateLabel('locate')}</span></li>
                    {#if draft.enabled}
                        {#each draft.states.filter((s) => s.enabled && s.id !== 'external') as s (s.id)}
                            {#if RADIO_STATES.includes(s.id)}
                                {#each levelsOf(s.id).filter((l) => l.enabled) as lv (lv.threshold)}
                                    <li><LEDSwatch look={lookOf(lv)} background={bgOf(lv)} /> <span>{lookText(lookOf(lv), bgOf(lv))}</span> <span class="ol-muted">{stateLabel(s.id)} &gt; {lv.threshold} %</span></li>
                                {/each}
                            {:else}
                                <li><LEDSwatch look={lookOf(s)} background={bgOf(s)} /> <span>{lookText(lookOf(s), bgOf(s))}</span> <span class="ol-muted">{stateLabel(s.id)}</span></li>
                            {/if}
                        {/each}
                        <li><LEDSwatch look={draft.normal} /> <span>{lookText(draft.normal)}</span> <span class="ol-muted">{stateLabel('normal')}</span></li>
                    {/if}
                </ul>
            </details>
            </section>
        </div>
        {#if admin}
            <div class="ol-led-save">
                <button class="hmm-button primary" onclick={save} disabled={!dirty || busy !== ''}>{t('Save')}</button>
                <button class="hmm-button" onclick={discard} disabled={!dirty || busy !== ''}>{t('Discard')}</button>
                {#if dirty}<span class="ol-muted">{t('Unsaved changes')}</span>{/if}
            </div>
        {/if}
    {/if}

    {#if piLEDs}
        <h2 id="pi-leds">{t('Raspberry Pi LEDs')}<Help>{t('The green and red LEDs of the Raspberry Pi board itself.')}</Help></h2>
        <section class="ol-card" data-panel="pi-leds">
            {#if !view.available && view.pwr}
                <div class="ol-card-head"><h3 class="ol-card-title">{t('Power LED')}</h3></div>
                <fieldset class="ol-led-settings" disabled={!admin}>
                    <label class="ol-led-check">
                        <input type="checkbox" bind:checked={draft.pwr_error_light} data-led="pwr" />
                        {t('Use the red power LED as an error light')}
                        <Help>{t('It blinks fast while the radio or a service is down, or the storage should be replaced, and returns to its normal light afterwards.')}</Help>
                    </label>
                    {#if st.pwr?.error}<div class="ol-warn">{t('It is blinking now.')}</div>{/if}
                </fieldset>
                {#if admin}
                    <div class="ol-led-save">
                        <button class="hmm-button primary" onclick={save} disabled={!dirty || busy !== ''}>{t('Save')}</button>
                        <button class="hmm-button" onclick={discard} disabled={!dirty || busy !== ''}>{t('Discard')}</button>
                    </div>
                {/if}
            {/if}
            {#if boards && boards.leds.length}
            <div class="ol-led-boards">
                <div>{boards.leds.map((l) => `${l.name}: ${l.trigger || '–'}`).join(' · ')}</div>
                {#if admin}
                    <div class="ol-actions"><button class="hmm-button" onclick={toggleBoards} disabled={busy !== ''}>{boards.disabled ? t('Switch the onboard LEDs on') : t('Switch the onboard LEDs off')}</button></div>
                {/if}
            </div>
            {/if}
        </section>
    {/if}
{/if}

<style>
    .ol-led-now { display: flex; flex-wrap: wrap; align-items: center; gap: 14px; margin-bottom: 12px; }
    .ol-led-now-text { flex: 1 1 220px; min-width: 0; }
    .ol-led-now-look { font-size: 1.15em; font-weight: 600; }
    .ol-led-now-actions { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
    .ol-led-muted { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 12px; }
    .ol-led-overrides { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 12px; margin-bottom: 12px; }
    .ol-led-override { display: inline-flex; align-items: center; gap: 4px; }
    .ol-led-settings { border: 0; padding: 0; margin: 0; min-width: 0; display: flex; flex-direction: column; gap: 10px; }
    .ol-led-check { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 6px; }
    .ol-led-line { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
    .ol-led-key { min-width: 13em; }
    .ol-led-sub { margin-left: 24px; }
    .ol-seg > [aria-checked='true'] { color: var(--hmm-accent); background: var(--hmm-accent-bg); font-weight: 600; }
    .ol-led-rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
    .ol-led-rows > li { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
    .ol-led-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
    .ol-led-row-text { display: flex; flex-wrap: wrap; column-gap: 6px; min-width: 0; }
    /* task 58: one panel per block; the two disclosures' summaries are their panels' titles */
    .ol-led-panels { display: flex; flex-direction: column; gap: 12px; margin-bottom: 4px; }
    .ol-led-panels > .ol-card > .ol-card-head { margin-bottom: 10px; }
    .ol-led-panels > .ol-card > :is(fieldset, p):last-child { margin-bottom: 0; }
    .ol-led-advanced, .ol-led-legend { margin: 0; }
    .ol-led-advanced > summary, .ol-led-legend > summary { cursor: pointer; font-weight: 600; margin: 0; color: var(--hmm-fg); }
    .ol-led-advanced[open] > summary, .ol-led-legend[open] > summary { margin-bottom: 10px; }
    .ol-led-list { list-style: none; margin: 8px 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
    .ol-led-item { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; padding: 6px 8px; border: 1px solid var(--hmm-border-muted); border-radius: var(--hmm-radius); background: var(--hmm-card-bg); min-width: 0; }
    .ol-led-item.fixed { background: var(--hmm-bg-sunken); }
    .ol-led-name { font-weight: 600; min-width: 13em; }
    .ol-led-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
    .ol-led-subs { flex-basis: 100%; display: flex; flex-wrap: wrap; gap: 4px 14px; padding-left: 64px; }
    .ol-led-num { width: 4.5em; }
    .ol-led-look { flex: 1 1 12em; min-width: 0; }
    .ol-led-key-label { gap: 8px; }
    .ol-led-range { width: 160px; max-width: 50vw; accent-color: var(--hmm-accent); }
    .ol-led-pct { min-width: 3.5em; font-variant-numeric: tabular-nums; }
    .ol-led-color { width: 36px; height: 28px; padding: 2px; border: 1px solid var(--hmm-border-muted); border-radius: var(--hmm-radius); background: var(--hmm-card-bg); cursor: pointer; }
    .ol-led-panel { display: flex; flex-direction: column; gap: 10px; }
    /* task 56, B-73: the form under its row, across the row's (or the panel's) full width */
    .ol-led-form { flex-basis: 100%; min-width: 0; margin: 6px 0 2px; padding: 10px 12px; border: 1px solid var(--hmm-border-muted); border-radius: var(--hmm-radius); background: var(--hmm-bg-sunken); display: flex; flex-direction: column; gap: 10px; }
    .ol-led-item.fixed > .ol-led-form { background: var(--hmm-card-bg); }
    .ol-led-form-title { margin: 0; font-size: var(--hmm-font-size); font-weight: 600; }
    .ol-led-form-actions { margin: 0; }
    .ol-led-panel-preview { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
    .ol-led-panel .ol-led-subs { padding-left: 0; }
    .ol-led-levels { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
    .ol-led-level { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
    .ol-led-save { position: sticky; bottom: 0; z-index: 1; display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 8px 0; background: var(--hmm-bg); }
    .ol-led-boards { display: flex; flex-direction: column; gap: 8px; }
    [data-panel='pi-leds'] { display: flex; flex-direction: column; gap: 10px; }
    @media (max-width: 640px) {
        .ol-led-key, .ol-led-name { min-width: 0; }
        .ol-led-name { flex: 1 1 auto; }
        .ol-led-sub { margin-left: 0; }
        .ol-led-subs { padding-left: 0; }
    }
</style>
