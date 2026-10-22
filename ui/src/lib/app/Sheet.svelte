<script lang="ts">
    /*
     * The channel's detail sheet (task 193, phase 3): the complex controls a tile keeps behind
     * itself - a dimmer's presets and slider, a blind's up/stop/down and levels, a thermostat's
     * setpoint and boost, a lock's states - and for a channel without controls what there is to
     * show, its values. The shell's modal; the page behind it goes frosted (app.css).
     */
    import {onMount} from 'svelte';
    import ModalDialog from '../ModalDialog.svelte';
    import Icon from '../Icon.svelte';
    import {i18n, t} from '../i18n.svelte';
    import {formatPower, isOn, mainAction, stateText, type Model} from './channels';
    import type {Channel} from './rpc';
    import {stateIcon, type ContactIcons} from './stateicons';
    import type {CornerMessage} from './badges';
    import {lucideSVG} from './lucide';
    import {durationLine, nextDurationChange, sinceLine} from './duration';
    import {canBoost, splitSeconds, type BoostInfo} from './boost';

    // onassign (occulited task 96): the gear left of close opens the assignment sheet; only given to an account that may use it
    // boost/onboost (occulited task 103): a thermostat's running boost countdown, and the toggle that starts or ends it
    let {channel, meter = null, name, messages = [], from = null, onclose, onset, onput, onassign, icon: chosen, icons, boost = null, onboost}: {channel: Channel; name: string; messages?: CornerMessage[]; from?: DOMRect | null; onclose: () => void; onset: (key: string, value: unknown) => Promise<void>; onput: (values: Record<string, unknown>) => Promise<void>; onassign?: () => void; meter?: Channel | null; icon?: string; icons?: ContactIcons; boost?: BoostInfo | null; onboost?: () => Promise<void>} = $props();
    const boostText = $derived(boost ? (boost.full > 0 || boost.remaining > 0 ? t('Boost: {m} min {s} s', {m: String(splitSeconds(boost.remaining).m), s: splitSeconds(boost.remaining).s}) : t('Boost')) : '');

    // the sheet grows out of the card it came from (a FLIP from the card's rectangle to the
    // sheet's own) and shrinks back into it when it closes, the backdrop fading with it; reduced
    // motion skips both
    function frames(el: HTMLElement): [Keyframe, Keyframe] | null {
        if (!from || typeof el.animate !== 'function' || window.matchMedia('(prefers-reduced-motion: reduce)').matches) return null;
        const to = el.getBoundingClientRect();
        if (to.width === 0 || to.height === 0) return null;
        const dx = from.left - to.left;
        const dy = from.top - to.top;
        el.style.transformOrigin = 'top left';
        return [{transform: `translate(${dx}px, ${dy}px) scale(${from.width / to.width}, ${from.height / to.height})`, opacity: 0.4}, {transform: 'none', opacity: 1}];
    }
    const sheetEl = () => document.querySelector<HTMLElement>('.ol-modal.app-sheet');
    onMount(() => {
        const el = sheetEl();
        const f = el && frames(el);
        if (!el || !f) return;
        el.animate(f, {duration: 240, easing: 'cubic-bezier(0.2, 0.8, 0.2, 1)'});
        el.parentElement?.animate([{backgroundColor: 'transparent', backdropFilter: 'blur(0px)'}, {}], {duration: 240, easing: 'ease-out'});
    });
    let closing = false;
    function close() {
        if (closing) return;
        closing = true;
        const el = sheetEl();
        const f = el && frames(el);
        if (!el || !f) {
            onclose();
            return;
        }
        el.style.pointerEvents = 'none';
        const a = el.animate([f[1], f[0]], {duration: 200, easing: 'cubic-bezier(0.4, 0, 1, 1)', fill: 'forwards'});
        el.parentElement?.animate([{}, {backgroundColor: 'transparent', backdropFilter: 'blur(0px)'}], {duration: 200, easing: 'ease-in', fill: 'forwards'});
        a.finished.then(onclose, onclose);
    }
    const m = $derived<Model>(channel.model);
    const v = $derived(channel.values);
    const changedAt = $derived(channel.changed?.[channel.model.stateKey]);
    let minuteNow = $state(Date.now());
    $effect(() => {
        void minuteNow;
        const wait = changedAt ? nextDurationChange([Date.parse(changedAt)], Date.now()) : undefined;
        if (wait === undefined) return;
        const timer = setTimeout(() => (minuteNow = Date.now()), wait);
        return () => clearTimeout(timer);
    });
    const since = $derived(sinceLine(m, v, changedAt, i18n.language, new Date(minuteNow)));
    let busy = $state(false);
    let error = $state('');
    // the slider's live position while it is dragged; sent on release
    let dragging = $state<number | null>(null);

    async function set(key: string, value: unknown) {
        busy = true;
        error = '';
        try {
            await onset(key, value);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }
    async function put(values: Record<string, unknown>) {
        busy = true;
        error = '';
        try {
            await onput(values);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }
    function num(x: unknown): number {
        return typeof x === 'number' ? x : typeof x === 'string' && x !== '' ? Number(x) : 0;
    }
    const writable = (key: string) => typeof channel.desc?.[key]?.OPERATIONS === 'number' && ((channel.desc![key].OPERATIONS as number) & 2) !== 0;
    const list = (key: string): string[] => channel.desc?.[key]?.VALUE_LIST ?? [];
    const bound = (key: string, which: 'MIN' | 'MAX', fallback: number) => {
        const b = channel.desc?.[key]?.[which];
        return b === undefined || b === '' ? fallback : num(b);
    };
    // ---- colour (the RGBW and dual-white actuators, HmIP-BSL's colour ring, the classic HM-LC-RGBW-WM):
    // what the description offers - hue and saturation, the colour temperature, or one COLOR value
    const hasHue = $derived(writable('HUE'));
    const hasSat = $derived(writable('SATURATION'));
    const hasCT = $derived(writable('COLOR_TEMPERATURE'));
    const colorList = $derived(list('COLOR'));
    const hasColorEnum = $derived(writable('COLOR') && colorList.length > 0);
    const hasColorNum = $derived(writable('COLOR') && colorList.length === 0);
    // the BSL's colours by their names (the CSS's own colour names; the ring has these eight)
    const SWATCH: Record<string, string> = {BLACK: 'black', BLUE: 'blue', GREEN: 'green', TURQUOISE: 'turquoise', RED: 'red', PURPLE: 'purple', YELLOW: 'yellow', WHITE: 'white'};
    const COLOR_WORDS: Record<string, string> = {BLACK: 'Off', BLUE: 'Blue', GREEN: 'Green', TURQUOISE: 'Turquoise', RED: 'Red', PURPLE: 'Purple', YELLOW: 'Yellow', WHITE: 'White'};
    // ---- readings: what a meter or a sensor measures, in the WebUI's order, each with its unit
    const READINGS: {key: string; label: string; unit: string; digits?: number; kilo?: boolean}[] = [
        {key: 'POWER', label: 'Power', unit: 'W', digits: 1},
        {key: 'ENERGY_COUNTER', label: 'Energy', unit: 'Wh', kilo: true},
        {key: 'VOLTAGE', label: 'Voltage', unit: 'V', digits: 1},
        {key: 'CURRENT', label: 'Current', unit: 'mA', digits: 0},
        {key: 'FREQUENCY', label: 'Frequency', unit: 'Hz', digits: 2},
        {key: 'ACTUAL_TEMPERATURE', label: 'Temperature', unit: '°C', digits: 1},
        {key: 'TEMPERATURE', label: 'Temperature', unit: '°C', digits: 1},
        {key: 'HUMIDITY', label: 'Humidity', unit: '%', digits: 0},
        {key: 'ILLUMINATION', label: 'Illumination', unit: 'lx', digits: 0},
        {key: 'CURRENT_ILLUMINATION', label: 'Illumination', unit: 'lx', digits: 0},
        {key: 'WIND_SPEED', label: 'Wind speed', unit: 'km/h', digits: 1},
        {key: 'WIND_DIR', label: 'Wind direction', unit: '°', digits: 0},
        {key: 'WIND_DIRECTION', label: 'Wind direction', unit: '°', digits: 0},
        {key: 'RAIN_COUNTER', label: 'Rain', unit: 'mm', digits: 1},
        {key: 'SUNSHINEDURATION', label: 'Sunshine', unit: 'min', digits: 0},
        {key: 'CARBON_DIOXIDE_CONCENTRATION', label: 'CO₂', unit: 'ppm', digits: 0},
        {key: 'MASS_CONCENTRATION_PM_2_5_24H_AVERAGE', label: 'Particulate matter 2.5', unit: 'µg/m³', digits: 0},
        {key: 'MASS_CONCENTRATION_PM_10_24H_AVERAGE', label: 'Particulate matter 10', unit: 'µg/m³', digits: 0},
        {key: 'SOIL_MOISTURE', label: 'Soil moisture', unit: '%', digits: 0},
        {key: 'DISTANCE', label: 'Distance', unit: 'mm', digits: 0},
    ];
    // occulited task 109: a switch with its device's power meter shows the meter's values below its buttons
    const rv = $derived(meter ? meter.values : v);
    const rdesc = $derived(meter ? meter.desc : channel.desc);
    const readings = $derived(READINGS.filter((r) => r.key in rv && typeof rv[r.key] !== 'boolean').map((r) => {
        const n = num(rv[r.key]);
        const unit = rdesc?.[r.key]?.UNIT || r.unit;
        if (r.key === 'POWER' && unit === 'W') return {...r, text: formatPower(n, i18n.language)};
        if (r.kilo && unit === 'Wh' && Math.abs(n) >= 1000) return {...r, text: `${(n / 1000).toFixed(2)} kWh`};
        return {...r, text: `${r.digits === undefined ? n : n.toFixed(r.digits)} ${unit}`.trim()};
    }));
    // ---- the smoke detector's commands (HmIP-SWSD): the test, the alarm off, the alarm
    const smokeCommands = $derived(list('SMOKE_DETECTOR_COMMAND'));
    const smokeIndex = (name: string) => smokeCommands.indexOf(name);
    // ---- the signal: a sound and a light out of the lists, for a duration, or off
    const SIGNAL_WORDS: Record<string, string> = {
        DISABLE_ACOUSTIC_SIGNAL: 'Off', DISABLE_OPTICAL_SIGNAL: 'Off', FREQUENCY_RISING: 'Rising', FREQUENCY_FALLING: 'Falling', FREQUENCY_RISING_AND_FALLING: 'Rising and falling',
        FREQUENCY_ALTERNATING_LOW_HIGH: 'Alternating low, high', FREQUENCY_ALTERNATING_LOW_MID_HIGH: 'Alternating low, mid, high', FREQUENCY_HIGHON_OFF: 'High on, off', FREQUENCY_HIGHON_LONGOFF: 'High on, long off',
        FREQUENCY_LOWON_OFF_HIGHON_OFF: 'Low on, off, high on, off', FREQUENCY_LOWON_LONGOFF_HIGHON_LONGOFF: 'Low on, long off, high on, long off', LOW_BATTERY: 'Low battery', DISARMED: 'Disarmed',
        INTERNALLY_ARMED: 'Armed internally', EXTERNALLY_ARMED: 'Armed externally', DELAYED_INTERNALLY_ARMED: 'Armed internally, delayed', DELAYED_EXTERNALLY_ARMED: 'Armed externally, delayed', EVENT: 'Event', ERROR: 'Error',
        BLINKING_ALTERNATELY_REPEATING: 'Blinking alternately', BLINKING_BOTH_REPEATING: 'Blinking both', DOUBLE_FLASHING_REPEATING: 'Double flashing', FLASHING_BOTH_REPEATING: 'Flashing both',
        CONFIRMATION_SIGNAL_0: 'Confirmation 0', CONFIRMATION_SIGNAL_1: 'Confirmation 1', CONFIRMATION_SIGNAL_2: 'Confirmation 2',
    };
    const word = (x: string) => (SIGNAL_WORDS[x] ? t(SIGNAL_WORDS[x]) : x.toLowerCase().replace(/_/g, ' '));
    const soundKey = $derived(writable('ACOUSTIC_ALARM_SELECTION') ? 'ACOUSTIC_ALARM_SELECTION' : writable('SOUNDFILE') ? 'SOUNDFILE' : '');
    const lightKey = $derived(writable('OPTICAL_ALARM_SELECTION') ? 'OPTICAL_ALARM_SELECTION' : '');
    const hasDuration = $derived(writable('DURATION_VALUE') && writable('DURATION_UNIT'));
    let sound = $state(1);
    let light = $state(1);
    let duration = $state('30');
    // seconds, minutes or hours as DURATION_UNIT counts them: S 0, M 1, H 2
    const DURATIONS: {label: string; value: number; unit: number}[] = [
        {label: '5 s', value: 5, unit: 0}, {label: '30 s', value: 30, unit: 0}, {label: '2 min', value: 2, unit: 1}, {label: '10 min', value: 10, unit: 1}, {label: '1 h', value: 1, unit: 2},
    ];
    function signalValues(on: boolean): Record<string, unknown> {
        const out: Record<string, unknown> = {};
        if (soundKey) out[soundKey] = on ? sound : 0;
        if (lightKey) out[lightKey] = on ? light : 0;
        if (hasDuration) {
            const d = DURATIONS.find((x) => String(x.value) + ':' + x.unit === duration) ?? {value: 30, unit: 0};
            out.DURATION_VALUE = on ? d.value : 0;
            out.DURATION_UNIT = on ? d.unit : 0;
        }
        if (!soundKey && !lightKey && writable('LEVEL')) out.LEVEL = on ? 1 : 0;
        return out;
    }
    onMount(() => {
        duration = '30:0';
        if (soundKey && list(soundKey).length > 1) sound = 1;
        if (lightKey && list(lightKey).length > 1) light = 1;
    });
    const level = $derived(dragging ?? Math.round(num(v[m.stateKey]) * 100));
    const setpointKey = $derived(m.widget === 'thermostat' ? m.commandKey : '');
    const setMin = $derived(num(channel.desc?.[setpointKey]?.MIN ?? 5));
    const setMax = $derived(num(channel.desc?.[setpointKey]?.MAX ?? 30));
    const PRESETS = [0, 25, 50, 75, 100];
    // the head is the tile's: the circle (the main action, lit as on the card), the name, the state
    const on = $derived(isOn(m, v, channel.desc));
    // task 75: the tile's state icon, the same in the sheet's circle
    const icon = $derived(stateIcon({model: m, values: v, desc: channel.desc, parent: channel.parent, icon: chosen, icons, name}));
    const action = $derived(mainAction(m, v, channel.desc));
    async function act() {
        const a = mainAction(m, v, channel.desc);
        if (a) await set(a.key, a.value);
    }
</script>

<ModalDialog open={true} title={name} size="medium" onclose={close} class="app-sheet">
    {#snippet actions()}
        {#if onassign}
            <button type="button" class="ol-modal-action" title={t('Name, rooms and functions')} aria-label={t('Name, rooms and functions')} onclick={onassign} data-sheet-assign><Icon name="settings" size={16} /></button>
        {/if}
    {/snippet}
    <div class="sh-head" class:sh-gear={!!onassign}>
        {#if m.widget === 'thermostat' && onboost && canBoost(channel.desc)}
            <!-- task 103: the disc is the boost button here too -->
            <button type="button" class="sh-circle sh-circle-act" class:boost={!!boost} class:on={!boost && (icon.tone === 'on' || on)} title={boost ? t('End boost') : t('Start boost')} aria-label={`${name}: ${boost ? t('End boost') : t('Start boost')}`} aria-pressed={!!boost} disabled={busy} onclick={() => void onboost()} data-icon={icon.name} data-sheet-boost-btn={boost ? 'on' : 'off'}><span class="sh-ico">{@html icon.svg}</span></button>
        {:else if action && m.commandKey}
            <button type="button" class="sh-circle sh-circle-act" class:on={icon.tone === 'on' || on} class:alarm={icon.tone === 'alarm'} aria-label={t('Switch {name}', {name})} aria-pressed={on} disabled={busy} onclick={() => void act()} data-icon={icon.name}><span class="sh-ico">{@html icon.svg}</span></button>
        {:else}
            <span class="sh-circle" class:on={icon.tone === 'on'} class:alarm={icon.tone === 'alarm'} aria-hidden="true" data-icon={icon.name}><span class="sh-ico">{@html icon.svg}</span></span>
        {/if}
        <div class="sh-titles">
            <div class="sh-name">{name}</div>
            <div class="sh-state ol-muted" data-sheet-state>{stateText(m, v, channel.desc, t)}</div>
            {#if boost}<div class="sh-boost" data-sheet-boost={boost.remaining}>{boostText}</div>{/if}
            <!-- task 95: how long, and since when exactly; nothing while the time is not known -->
            {#if since}<div class="sh-since ol-muted" data-sheet-since title={since}>{durationLine(m, v, changedAt, minuteNow, i18n.language)} · {since}</div>{/if}
        </div>
    </div>
    {#if messages.length}
        <!-- task 92: every service message of the device, most severe first (the tile's corner shows the first) -->
        <ul class="sh-messages" data-sheet="messages" aria-label={t('Service messages')}>
            {#each messages as msg (msg.key)}
                <li data-sheet-message={msg.key}><span class="sh-msg-ico" aria-hidden="true">{@html lucideSVG(msg.kind, 2.25)}</span>{t(msg.label)}</li>
            {/each}
        </ul>
    {/if}
    {#if error}<div class="ol-notice error">{error}</div>{/if}
    {#if m.widget === 'switch' && m.commandKey}
        <!-- task 100: on and off as two large buttons of one size; the one that is the state is lit -->
        <div class="sh-presets sh-switch" data-sheet="switch" role="group" aria-label={t('Switch {name}', {name})}>
            <!-- task 109 (the maintainer): Off left, On right; the keyboard order follows -->
            <button type="button" class="hmm-button sh-btn sh-big" class:sh-lit-off={!on} aria-pressed={!on} disabled={busy} onclick={() => void set(m.commandKey, false)} data-sheet-off>{t('Off')}</button>
            <button type="button" class="hmm-button sh-btn sh-big" class:sh-lit={on} aria-pressed={on} disabled={busy} onclick={() => void set(m.commandKey, true)} data-sheet-on>{t('On')}</button>
        </div>
    {:else if m.widget === 'dimmer' || m.widget === 'color'}
        <div class="sh-presets" data-sheet="presets">
            {#each PRESETS as p (p)}
                <button type="button" class="hmm-button sh-btn" class:primary={level === p} disabled={busy} onclick={() => void set(m.commandKey, p / 100)}>{p} %</button>
            {/each}
        </div>
        <label class="sh-slider"><span>{t('Brightness')}</span>
            <input type="range" min="0" max="100" step="1" value={level} aria-label={t('Brightness')} disabled={busy} oninput={(e) => (dragging = Number(e.currentTarget.value))} onchange={(e) => { const n = Number(e.currentTarget.value); dragging = n; void set(m.commandKey, n / 100).finally(() => (dragging = null)); }} />
            <output>{level} %</output>
        </label>
        {#if hasColorEnum}
            <div class="sh-presets sh-swatches" data-sheet="colors" role="group" aria-label={t('Colour')}>
                {#each colorList as c, i (c)}
                    <button type="button" class="hmm-button sh-btn sh-swatch" class:primary={num(v.COLOR) === i} aria-pressed={num(v.COLOR) === i} disabled={busy} onclick={() => void set('COLOR', i)}><span class="sh-dot" style:background={SWATCH[c] ?? 'transparent'}></span>{COLOR_WORDS[c] ? t(COLOR_WORDS[c]) : word(c)}</button>
                {/each}
            </div>
        {/if}
        {#if hasHue}
            <label class="sh-slider"><span>{t('Hue')}</span>
                <input class="sh-hue" type="range" min={bound('HUE', 'MIN', 0)} max={bound('HUE', 'MAX', 360)} step="1" value={Math.round(num(v.HUE))} aria-label={t('Hue')} disabled={busy} onchange={(e) => void set('HUE', Number(e.currentTarget.value))} />
                <output>{Math.round(num(v.HUE))}°</output>
            </label>
        {/if}
        {#if hasSat}
            <label class="sh-slider"><span>{t('Saturation')}</span>
                <input type="range" min="0" max="100" step="1" value={Math.round(num(v.SATURATION) * 100)} aria-label={t('Saturation')} disabled={busy} onchange={(e) => void set('SATURATION', Number(e.currentTarget.value) / 100)} />
                <output>{Math.round(num(v.SATURATION) * 100)} %</output>
            </label>
        {/if}
        {#if hasColorNum}
            <label class="sh-slider"><span>{t('Colour')}</span>
                <input class="sh-hue" type="range" min={bound('COLOR', 'MIN', 0)} max={bound('COLOR', 'MAX', 200)} step="1" value={Math.round(num(v.COLOR))} aria-label={t('Colour')} disabled={busy} onchange={(e) => void set('COLOR', Number(e.currentTarget.value))} />
                <output>{num(v.COLOR) >= bound('COLOR', 'MAX', 200) ? t('White') : Math.round(num(v.COLOR))}</output>
            </label>
        {/if}
        {#if hasCT}
            <label class="sh-slider"><span>{t('Colour temperature')}</span>
                <input class="sh-ct" type="range" min={bound('COLOR_TEMPERATURE', 'MIN', 2000)} max={bound('COLOR_TEMPERATURE', 'MAX', 6500)} step="50" value={Math.round(num(v.COLOR_TEMPERATURE))} aria-label={t('Colour temperature')} disabled={busy} onchange={(e) => void set('COLOR_TEMPERATURE', Number(e.currentTarget.value))} />
                <output>{Math.round(num(v.COLOR_TEMPERATURE))} K</output>
            </label>
        {/if}
    {:else if m.widget === 'blind'}
        <div class="sh-presets" data-sheet="blind">
            <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('LEVEL', 1)}>{t('Up')}</button>
            <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('STOP', true)}>{t('Stop')}</button>
            <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('LEVEL', 0)}>{t('Down')}</button>
        </div>
        <label class="sh-slider"><span>{t('Position')}</span>
            <input type="range" min="0" max="100" step="1" value={level} aria-label={t('Position')} disabled={busy} oninput={(e) => (dragging = Number(e.currentTarget.value))} onchange={(e) => { const n = Number(e.currentTarget.value); dragging = n; void set('LEVEL', n / 100).finally(() => (dragging = null)); }} />
            <output>{level} %</output>
        </label>
        {#if writable('LEVEL_2')}
            <label class="sh-slider"><span>{t('Slats')}</span>
                <input type="range" min="0" max="100" step="1" value={Math.round(num(v.LEVEL_2) * 100)} aria-label={t('Slats')} disabled={busy} onchange={(e) => void set('LEVEL_2', Number(e.currentTarget.value) / 100)} />
                <output>{Math.round(num(v.LEVEL_2) * 100)} %</output>
            </label>
        {/if}
    {:else if m.widget === 'thermostat' && setpointKey}
        {@const sp = num(v[setpointKey])}
        <div class="sh-setpoint" data-sheet="setpoint">
            <button type="button" class="hmm-button sh-btn sh-round" aria-label={t('Lower')} disabled={busy || sp <= setMin} onclick={() => void set(setpointKey, Math.max(setMin, Math.round((sp - 0.5) * 2) / 2))}>−</button>
            <span class="sh-setpoint-value">{sp.toFixed(1)} °C</span>
            <button type="button" class="hmm-button sh-btn sh-round" aria-label={t('Raise')} disabled={busy || sp >= setMax} onclick={() => void set(setpointKey, Math.min(setMax, Math.round((sp + 0.5) * 2) / 2))}>+</button>
        </div>
        <label class="sh-slider"><span>{t('Setpoint')}</span>
            <input type="range" min={setMin} max={setMax} step="0.5" value={sp} aria-label={t('Setpoint')} disabled={busy} onchange={(e) => void set(setpointKey, Number(e.currentTarget.value))} />
            <output>{sp.toFixed(1)} °C</output>
        </label>
        <!-- the mode: HmIP says SET_POINT_MODE 0 auto / 1 manual and BOOST_MODE; a BidCos radiator
             thermostat reads CONTROL_MODE (0 auto, 1 manual, 2 party, 3 boost) and is written through
             AUTO_MODE, MANU_MODE (with the setpoint) and BOOST_MODE -->
        {#if writable('SET_POINT_MODE') || writable('AUTO_MODE') || writable('BOOST_MODE')}
            {@const mode = writable('SET_POINT_MODE') ? (v.BOOST_MODE === true ? 'boost' : num(v.SET_POINT_MODE) === 1 ? 'manual' : 'auto') : (num(v.CONTROL_MODE) === 3 ? 'boost' : num(v.CONTROL_MODE) === 1 ? 'manual' : num(v.CONTROL_MODE) === 2 ? 'party' : 'auto')}
            <div class="sh-presets sh-modes" data-sheet="mode" role="group" aria-label={t('Mode')}>
                {#if writable('SET_POINT_MODE') || writable('AUTO_MODE')}
                    <button type="button" class="hmm-button sh-btn" class:primary={mode === 'auto'} aria-pressed={mode === 'auto'} disabled={busy} onclick={() => void (writable('SET_POINT_MODE') ? set('SET_POINT_MODE', 0) : set('AUTO_MODE', true))}>{t('Auto')}</button>
                    <button type="button" class="hmm-button sh-btn" class:primary={mode === 'manual'} aria-pressed={mode === 'manual'} disabled={busy} onclick={() => void (writable('SET_POINT_MODE') ? set('SET_POINT_MODE', 1) : set('MANU_MODE', sp))}>{t('Manual')}</button>
                {/if}
                {#if writable('BOOST_MODE')}
                    <button type="button" class="hmm-button sh-btn" class:primary={mode === 'boost'} aria-pressed={mode === 'boost'} disabled={busy} onclick={() => void (onboost ? onboost() : set('BOOST_MODE', mode !== 'boost'))}>{t('Boost')}</button>
                {/if}
            </div>
        {/if}
    {:else if m.widget === 'lock'}
        <div class="sh-presets" data-sheet="lock">
            {#if writable('LOCK_TARGET_LEVEL')}
                <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('LOCK_TARGET_LEVEL', 0)}>{t('Lock')}</button>
                <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('LOCK_TARGET_LEVEL', 1)}>{t('Unlock')}</button>
                <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('LOCK_TARGET_LEVEL', 2)}>{t('Open')}</button>
            {:else}
                <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('STATE', false)}>{t('Lock')}</button>
                <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('STATE', true)}>{t('Unlock')}</button>
                {#if writable('OPEN')}<button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('OPEN', true)}>{t('Open')}</button>{/if}
            {/if}
        </div>
    {:else if m.widget === 'smoke' && smokeCommands.length}
        <div class="sh-presets" data-sheet="smoke">
            {#if smokeIndex('SMOKE_TEST') >= 0}<button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('SMOKE_DETECTOR_COMMAND', smokeIndex('SMOKE_TEST'))}>{t('Alarm test')}</button>{/if}
            {#if smokeIndex('INTRUSION_ALARM_OFF') >= 0}<button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void set('SMOKE_DETECTOR_COMMAND', smokeIndex('INTRUSION_ALARM_OFF'))}>{t('Silence')}</button>{/if}
            {#if smokeIndex('INTRUSION_ALARM') >= 0}<button type="button" class="hmm-button sh-btn danger" disabled={busy} onclick={() => void set('SMOKE_DETECTOR_COMMAND', smokeIndex('INTRUSION_ALARM'))}>{t('Alarm')}</button>{/if}
        </div>
    {:else if m.widget === 'signal'}
        <div class="sh-fields" data-sheet="signal">
            {#if soundKey}
                <label class="sh-field"><span>{t('Sound')}</span>
                    <select class="hmm-select sh-select" bind:value={sound} disabled={busy} aria-label={t('Sound')}>
                        {#each list(soundKey) as x, i (x)}{#if i > 0}<option value={i}>{word(x)}</option>{/if}{/each}
                    </select>
                </label>
            {/if}
            {#if lightKey}
                <label class="sh-field"><span>{t('Light')}</span>
                    <select class="hmm-select sh-select" bind:value={light} disabled={busy} aria-label={t('Light')}>
                        {#each list(lightKey) as x, i (x)}{#if i > 0}<option value={i}>{word(x)}</option>{/if}{/each}
                    </select>
                </label>
            {/if}
            {#if hasDuration}
                <label class="sh-field"><span>{t('Duration')}</span>
                    <select class="hmm-select sh-select" bind:value={duration} disabled={busy} aria-label={t('Duration')}>
                        {#each DURATIONS as d (d.label)}<option value={`${d.value}:${d.unit}`}>{d.label}</option>{/each}
                    </select>
                </label>
            {/if}
            {#if writable('LEVEL') && (soundKey || lightKey)}
                <label class="sh-slider"><span>{t('Volume')}</span>
                    <input type="range" min="0" max="100" step="1" value={Math.round(num(v.LEVEL) * 100)} aria-label={t('Volume')} disabled={busy} onchange={(e) => void set('LEVEL', Number(e.currentTarget.value) / 100)} />
                    <output>{Math.round(num(v.LEVEL) * 100)} %</output>
                </label>
            {/if}
        </div>
        <div class="sh-presets">
            <button type="button" class="hmm-button sh-btn primary" disabled={busy} onclick={() => void put(signalValues(true))}>{t('Trigger')}</button>
            <button type="button" class="hmm-button sh-btn" disabled={busy} onclick={() => void put(signalValues(false))}>{t('Stop')}</button>
        </div>
    {/if}
    {#if readings.length && (!!meter || m.widget === 'power' || m.widget === 'reading' || m.widget === 'thermostat')}
        <dl class="sh-readings" data-sheet="readings">
            {#each readings as r (r.key)}
                <dt>{t(r.label)}</dt><dd data-reading={r.key}>{r.text}</dd>
            {/each}
        </dl>
    {/if}
</ModalDialog>

<style>
    /* the maintainer, 2026-09-22: bigger - larger click areas, a larger slider knob; the head is
       the tile's circle with the name and the state beside it (the modal's own titles are hidden,
       app.css) */
    /* the close (and task 96's gear) sit over the head's right end: the name stays clear of them */
    .sh-head { display: flex; align-items: center; gap: 16px; margin: 0 0 18px; padding-right: 40px; }
    .sh-head.sh-gear { padding-right: 78px; }
    .sh-circle { width: 56px; height: 56px; border-radius: 50%; border: 2px solid var(--hmm-border-muted); display: inline-flex; align-items: center; justify-content: center; color: var(--hmm-fg-muted); background: none; padding: 0; flex: 0 0 auto; }
    .sh-circle.on { color: var(--hmm-warn); border-color: var(--hmm-warn); }
    .sh-circle.alarm { color: var(--hmm-error); border-color: var(--hmm-error); }
    .sh-circle.boost { color: var(--ol-boost); border-color: var(--ol-boost); }
    .sh-boost { font-size: 1.05em; font-weight: 600; color: var(--ol-boost); font-variant-numeric: tabular-nums; }
    .sh-ico { width: 90%; height: 90%; display: block; pointer-events: none; }
    .sh-ico :global(svg) { width: 100%; height: 100%; display: block; }
    .sh-circle-act { cursor: pointer; font: inherit; }
    .sh-circle-act:hover { border-color: var(--hmm-fg); }
    .sh-circle-act:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .sh-titles { min-width: 0; }
    .sh-name { font-size: 1.25em; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .sh-state { font-size: 1.05em; }
    .sh-since { font-size: 0.9em; margin-top: 2px; font-variant-numeric: tabular-nums; }
    .sh-messages { list-style: none; margin: -6px 0 16px; padding: 0; display: flex; flex-wrap: wrap; gap: 6px 14px; color: color-mix(in srgb, var(--hmm-warn) 50%, var(--hmm-error)); font-weight: 600; }
    .sh-messages li { display: inline-flex; align-items: center; gap: 6px; }
    .sh-msg-ico { width: 16px; height: 16px; display: block; flex: 0 0 auto; }
    .sh-msg-ico :global(svg) { width: 100%; height: 100%; display: block; }
    .sh-presets { display: flex; gap: 10px; flex-wrap: wrap; margin: 4px 0 16px; }
    .sh-btn { min-height: 48px; min-width: 64px; padding: 0 18px; font-size: 1.1em; flex: 1 1 auto; }
    /* task 100: the switch's two buttons, one size whatever the state; on lit in the "on" colour */
    .sh-big { height: 72px; flex: 1 1 0; font-size: 1.3em; font-weight: 600; }
    .sh-big.sh-lit { background: color-mix(in srgb, var(--hmm-warn) 22%, var(--hmm-card-bg)); border-color: var(--hmm-warn); color: var(--hmm-warn); }
    .sh-big.sh-lit-off { background: color-mix(in srgb, var(--hmm-fg-muted) 18%, var(--hmm-card-bg)); border-color: var(--hmm-fg-muted); }
    .sh-round { flex: 0 0 auto; width: 56px; min-width: 56px; height: 56px; border-radius: 50%; font-size: 1.6em; padding: 0; }
    .sh-slider { display: grid; grid-template-columns: 6em 1fr 5em; align-items: center; gap: 12px; margin: 10px 0 16px; font-size: 1.05em; }
    .sh-slider input { width: 100%; min-width: 0; height: 44px; margin: 0; -webkit-appearance: none; appearance: none; background: transparent; cursor: pointer; }
    .sh-slider input::-webkit-slider-runnable-track { height: 10px; border-radius: 5px; background: var(--hmm-border-muted); }
    .sh-slider input::-moz-range-track { height: 10px; border-radius: 5px; background: var(--hmm-border-muted); }
    .sh-slider input::-webkit-slider-thumb { -webkit-appearance: none; appearance: none; width: 32px; height: 32px; border-radius: 50%; background: var(--hmm-card-bg); border: 2px solid var(--hmm-fg); margin-top: -11px; box-shadow: 0 2px 6px rgba(0, 0, 0, 0.3); }
    .sh-slider input::-moz-range-thumb { width: 32px; height: 32px; border-radius: 50%; background: var(--hmm-card-bg); border: 2px solid var(--hmm-fg); box-shadow: 0 2px 6px rgba(0, 0, 0, 0.3); }
    .sh-slider input:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .sh-slider output { text-align: right; font-variant-numeric: tabular-nums; font-size: 1.15em; }
    .sh-setpoint { display: flex; align-items: center; justify-content: center; gap: 18px; margin: 6px 0 14px; }
    .sh-setpoint-value { font-size: 2em; font-weight: 600; min-width: 5em; text-align: center; font-variant-numeric: tabular-nums; }
    /* the hue and colour sliders' tracks show what they set; the temperature's runs warm to cold */
    .sh-slider input.sh-hue::-webkit-slider-runnable-track { background: linear-gradient(to right, hsl(0 90% 55%), hsl(60 90% 55%), hsl(120 90% 45%), hsl(180 90% 45%), hsl(240 90% 55%), hsl(300 90% 55%), hsl(360 90% 55%)); }
    .sh-slider input.sh-hue::-moz-range-track { background: linear-gradient(to right, hsl(0 90% 55%), hsl(60 90% 55%), hsl(120 90% 45%), hsl(180 90% 45%), hsl(240 90% 55%), hsl(300 90% 55%), hsl(360 90% 55%)); }
    .sh-slider input.sh-ct::-webkit-slider-runnable-track { background: linear-gradient(to right, hsl(30 90% 60%), hsl(40 60% 85%), hsl(210 70% 80%)); }
    .sh-slider input.sh-ct::-moz-range-track { background: linear-gradient(to right, hsl(30 90% 60%), hsl(40 60% 85%), hsl(210 70% 80%)); }
    .sh-swatch { display: inline-flex; align-items: center; gap: 10px; }
    .sh-dot { width: 18px; height: 18px; border-radius: 50%; border: 1px solid var(--hmm-border-muted); flex: 0 0 auto; }
    .sh-fields { display: grid; gap: 12px; margin: 4px 0 16px; }
    .sh-field { display: grid; grid-template-columns: 6em 1fr; align-items: center; gap: 12px; font-size: 1.05em; }
    .sh-select { min-height: 48px; font-size: 1.05em; width: 100%; max-width: 100%; }
    .sh-readings { display: grid; grid-template-columns: auto 1fr; gap: 8px 18px; margin: 6px 0 4px; font-size: 1.1em; }
    .sh-readings dt { color: var(--hmm-fg-muted); }
    .sh-readings dd { margin: 0; font-variant-numeric: tabular-nums; font-weight: 600; }
</style>
