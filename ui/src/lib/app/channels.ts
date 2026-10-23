/*
 * The App's channel model (task 193, phase 3): what a channel is, from the interface process's
 * own description - the CONTROL attribute of its datapoints (getParamsetDescription VALUES),
 * the rule the CCU WebUI dispatches on, so it works on openccu-lite without ReGaHSS. The kind
 * before the dot names the control (DIMMER.LEVEL, SWITCH.STATE, TEMP.SETPOINT); a channel with
 * several kinds takes the highest of the table's order; a channel without any falls back on
 * its datapoint names for a reading. Values come from getParamset VALUES and the event stream.
 */

export type Widget = 'switch' | 'dimmer' | 'blind' | 'thermostat' | 'color' | 'lock' | 'door' | 'button' | 'contact' | 'motion' | 'reading' | 'power' | 'smoke' | 'signal' | 'none';
export type TileKind = 'act-open' | 'act' | 'read';

export interface Datapoint {
    CONTROL?: string;
    OPERATIONS?: number;
    TYPE?: string;
    MIN?: number | string;
    MAX?: number | string;
    UNIT?: string;
    VALUE_LIST?: string[];
}
export type Description = Record<string, Datapoint>;
export type Values = Record<string, unknown>;

export interface Model {
    widget: Widget;
    tile: TileKind;
    /** the datapoint that carries the state (the circle, the state line) */
    stateKey: string;
    /** the datapoint the main action writes; '' = no action */
    commandKey: string;
}

// the order decides when a channel carries several kinds: the richer control first
const KINDS: {kind: RegExp; widget: Widget; state: string; command: string; tile: TileKind}[] = [
    {kind: /^(TEMP|HEATING_CONTROL(_HMIP)?|CLIMATE_TRANSCEIVER|CLIMATECONTROL_FLOOR(_PUMP)?_TRANSCEIVER)$/, widget: 'thermostat', state: 'SET_POINT_TEMPERATURE|SET_TEMPERATURE|SETPOINT', command: 'SET_POINT_TEMPERATURE|SET_TEMPERATURE|SETPOINT', tile: 'act-open'},
    {kind: /^(RGBW_COLOR|RGB_COLOR|RGBW_AUTOMATIC|COLORTEMP|DUAL_WHITE_BRIGHTNESS|DUAL_WHITE_COLOR)$/, widget: 'color', state: 'LEVEL', command: 'LEVEL', tile: 'act-open'},
    // an analog output and a servo are a level 0..1 too: the dimmer's controls fit them
    {kind: /^(DIMMER|DIMMER_REAL|BACKLIGHTING_RECEIVER|DIGITAL_ANALOG_OUTPUT|ANALOG_OUTPUT_TRANSMITTER|SERVO_TRANSMITTER|SERVO_VIRTUAL_RECEIVER)$/, widget: 'dimmer', state: 'LEVEL', command: 'LEVEL', tile: 'act-open'},
    {kind: /^(BLIND|BLIND_TRANSMITTER|BLIND_VIRTUAL_RECEIVER|SHUTTER_TRANSMITTER|SHUTTER_VIRTUAL_RECEIVER|JALOUSIE|WINDOW_DRIVE_RECEIVER)$/, widget: 'blind', state: 'LEVEL', command: 'STOP', tile: 'act-open'},
    {kind: /^(LOCK|DOOR_LOCK_STATE_TRANSCEIVER|DOOR_LOCK_STATE_TRANSMITTER|DOOR_LOCK_TRANSCEIVER|AUTO_RELOCK_TRANSCEIVER)$/, widget: 'lock', state: 'LOCK_STATE|STATE', command: 'LOCK_TARGET_LEVEL|OPEN|STATE', tile: 'act-open'},
    {kind: /^(DOOROPENER|DOOR_RECEIVER|DOOR_STATE_TRANSCEIVER|SIMPLE_SWITCH_RECEIVER|ACCESS_RECEIVER|ACCESS_TRANSCEIVER)$/, widget: 'door', state: 'STATE|DOOR_STATE', command: 'OPEN|STATE', tile: 'act'},
    // the sirens' and the MP3 player's sound and light: a selection, a duration and a trigger, in the sheet
    {kind: /^(ALARM_SWITCH_VIRTUAL_RECEIVER|ACOUSTIC_SIGNAL_TRANSMITTER|ACOUSTIC_SIGNAL_VIRTUAL_RECEIVER|ACOUSTIC_DISPLAY_RECEIVER|OPTICAL_SIGNAL_RECEIVER|_DANGER)$/, widget: 'signal', state: 'ACOUSTIC_ALARM_ACTIVE|OPTICAL_ALARM_ACTIVE|STATE|LEVEL', command: '', tile: 'act-open'},
    // occulited task 100: a switch has a sheet too (its big on/off buttons); the circle still switches at once
    {kind: /^(SWITCH|SWITCH_TRANSMITTER|WATER_SWITCH|UNIVERSAL_LIGHT_RECEIVER|DIGITAL_STATE|ARMING)$/, widget: 'switch', state: 'STATE', command: 'STATE', tile: 'act-open'},
    // a smoke detector: the alarm state on the tile; its test and silence in the sheet when it takes a command.
    // Every smoke detector names its alarm DANGER.STATE (B-98): the BidCos HM-Sec-SD/-SD-2 and their team
    // channels (SMOKE_DETECTOR, SMOKE_DETECTOR_TEAM, SMOKE_DETECTOR_TEAM_V2) carry no other kind; the
    // HmIP-SWSD's SMOKE_DETECTOR_ALARM_STATUS is DANGER.STATE too, beside its SMOKE_DETECTOR.COMMAND.
    {kind: /^(SMOKE_DETECTOR|DANGER)$/, widget: 'smoke', state: 'SMOKE_DETECTOR_ALARM_STATUS|STATE', command: '', tile: 'read'},
    {kind: /^(MOTIONDETECTOR(_TRANSCEIVER)?|PASSAGE_DETECTOR_DIRECTION_TRANSMITTER|ACCELERATION_TRANSCEIVER)$/, widget: 'motion', state: 'MOTION|PRESENCE_DETECTION_STATE|PASSAGE_COUNTER_VALUE|MOTION_DETECTION_ACTIVE', command: '', tile: 'read'},
    {kind: /^(DOOR_SENSOR|WIN_SC|WIN_SC_SECURE|WIN_SC_SENSOR|RHS|WINDOW)$/, widget: 'contact', state: 'STATE', command: '', tile: 'read'},
    {kind: /^POWERMETER/, widget: 'power', state: 'POWER', command: '', tile: 'read'},
    {kind: /^(WEATHER_TRANSMIT|TEMP_HUM_PARTICLE_MATTER_TRANSMITTER|CARBON_DIOXIDE_RECEIVER|BRIGHTNESS_TRANSMITTER|SOIL_MOISTURE_TRANSMITTER|RAIN_DETECTION_TRANSMITTER|WATER_DETECTION_TRANSMITTER|DISTANCE_TRANSMITTER|GENERIC_INPUT_TRANSMITTER|GENERIC_MEASURING_TRANSMITTER|ANALOG_INPUT|FLOW_METER_TRANSMITTER|CAPACITIVE_FILLING_LEVEL_SENSOR|COND_SWITCH_TRANSMITTER_TEMPERATURE)$/, widget: 'reading', state: 'ACTUAL_TEMPERATURE|TEMPERATURE|HUMIDITY|ILLUMINATION|STATE', command: '', tile: 'read'},
    {kind: /^(BUTTON|BUTTON_NO_FUNCTION|BTN_SHORT_ONLY)$/, widget: 'button', state: 'PRESS_SHORT', command: 'PRESS_SHORT', tile: 'read'},
];

/** OPERATIONS bit 2: the datapoint takes a setValue. */
function writable(desc: Description | undefined, key: string): boolean {
    const op = desc?.[key]?.OPERATIONS;
    return typeof op === 'number' && (op & 2) !== 0;
}

function first(alts: string, desc: Description): string {
    for (const k of alts.split('|')) if (k && k in desc) return k;
    return '';
}

/** The channel kinds its datapoints name in CONTROL (`DIMMER.LEVEL` -> DIMMER). */
export function kindsOf(desc: Description | undefined): Set<string> {
    const kinds = new Set<string>();
    for (const dp of Object.values(desc ?? {})) {
        const c = dp?.CONTROL;
        if (c && c !== 'NONE') kinds.add(c.split('.')[0] ?? '');
    }
    return kinds;
}

// occulited task 104: a smoke detector's channel types - the BidCos HM-Sec-SD/-SD-2 and their teams, the HmIP-SWSD
const SMOKE_TYPES = /^SMOKE_DETECTOR(_TEAM(_V2)?)?$/;

/** Classifies a channel by its VALUES description; `type` (the channel's TYPE from getDeviceDescription), when given,
 *  keeps the DANGER kind to smoke detectors' channel types (task 104). */
export function classify(desc: Description | undefined, type?: string): Model {
    if (!desc) return {widget: 'none', tile: 'read', stateKey: '', commandKey: ''};
    const kinds = kindsOf(desc);
    for (const k of KINDS) {
        if (k.widget === 'smoke' && type !== undefined && !SMOKE_TYPES.test(type)) continue;
        for (const kind of kinds) {
            if (k.kind.test(kind)) {
                const m: Model = {widget: k.widget, tile: k.tile, stateKey: first(k.state, desc), commandKey: first(k.command, desc)};
                // a key: a virtual key (the central's, the HmIP-RCV-1's) takes a press over setValue
                // and acts; a remote's own key only reports presses. Every channel with a writable
                // PRESS_SHORT is pressable the same way.
                if (m.widget === 'button') {
                    if (writable(desc, 'PRESS_SHORT')) m.tile = 'act';
                    else m.commandKey = '';
                }
                if (m.widget === 'smoke' && writable(desc, 'SMOKE_DETECTOR_COMMAND')) m.tile = 'act-open';
                return m;
            }
        }
    }
    if (writable(desc, 'PRESS_SHORT')) return {widget: 'button', tile: 'act', stateKey: 'PRESS_SHORT', commandKey: 'PRESS_SHORT'};
    // no CONTROL (the HM-CC-TC's WEATHER channel): a reading, if there is one
    const reading = first('ACTUAL_TEMPERATURE|TEMPERATURE|HUMIDITY|ILLUMINATION|POWER', desc);
    if (reading) return {widget: 'reading', tile: 'read', stateKey: reading, commandKey: ''};
    return {widget: 'none', tile: 'read', stateKey: '', commandKey: ''};
}

/** Whether the channel is worth a tile at all (task 104: a channel the interface describes, whose description gives
 *  it no widget - a week profile, a heating group's receiver, an empty paramset - gets no tile anywhere; one whose
 *  description could not be read keeps its tile, so an outage does not blank the page). */
export function shown(c: {desc?: Description; model: Model} | undefined): boolean {
    return !c || c.desc === undefined || c.model.widget !== 'none';
}

function num(v: unknown): number | undefined {
    return typeof v === 'number' ? v : typeof v === 'string' && v !== '' && !isNaN(Number(v)) ? Number(v) : undefined;
}
function pct(v: unknown): string {
    const n = num(v);
    return n === undefined ? '' : `${Math.round(n * 100)} %`;
}

/** A lock's state from LOCK_STATE's value list (HmIP: UNKNOWN, LOCKED, UNLOCKED) or a KeyMatic's STATE (true = unlocked). */
export function lockState(values: Values, desc: Description | undefined, key: string): 'locked' | 'unlocked' | 'open' | undefined {
    const v = values[key];
    const list = desc?.[key]?.VALUE_LIST;
    if (list && typeof v === 'number') {
        const n = list[v] ?? '';
        if (/UNLOCK/.test(n)) return 'unlocked';
        if (/OPEN/.test(n)) return 'open';
        if (/LOCK/.test(n)) return 'locked';
        return undefined;
    }
    if (v === true || v === 1) return 'unlocked';
    if (v === false || v === 0) return 'locked';
    return undefined;
}

/** The valve position in % (0-100) a thermostat channel reports, the highest of its valves; undefined without one.
 *  HmIP: LEVEL (0..1); BidCos: VALVE_STATE (0..99/100 %). */
export function valvePercent(values: Values, desc: Description | undefined): number | undefined {
    let best: number | undefined;
    for (const k of ['LEVEL', 'VALVE_STATE', 'VALVE_LEVEL']) {
        if (desc && !(k in desc) && values[k] === undefined) continue;
        const v = num(values[k]);
        if (v === undefined) continue;
        const max = num(desc?.[k]?.MAX);
        const p = (max !== undefined ? max > 1.5 : v > 1) ? v : v * 100;
        best = best === undefined ? p : Math.max(best, p);
    }
    return best === undefined ? undefined : Math.min(100, Math.max(0, Math.round(best)));
}

/** The thermostat tile's T2E parts (occulited task 92): the room temperature, the setpoint, the valve in % if
 *  the channel reports one, and whether the window is open (HmIP reports it on the thermostat itself).
 *  occulited task 103 (V0): the humidity, when the channel has one - HmIP's wall thermostats report HUMIDITY, the
 *  BidCos HM-TC-IT's thermostat channel ACTUAL_HUMIDITY. */
export function thermoParts(m: Model, values: Values, desc: Description | undefined): {actual?: number; set?: number; valve?: number; window: boolean; humidity?: number} {
    const h = num(values.HUMIDITY ?? values.ACTUAL_HUMIDITY);
    return {
        actual: num(values.ACTUAL_TEMPERATURE),
        set: num(values[m.stateKey]),
        valve: valvePercent(values, desc),
        window: values.WINDOW_STATE === 1 || values.WINDOW_STATE === '1' || values.WINDOW_OPEN === true,
        ...(h === undefined ? {} : {humidity: Math.round(h)}),
    };
}

/** Whether the thermostat tile has its valve/window row (occulited B-100): only a channel that reports a valve
 *  or a window has one - a wall thermostat has neither, and its Ist/Soll pair is centred without it. Decided
 *  by the description, and by a value the channel reports. */
export function thermoRow(values: Values, desc: Description | undefined): boolean {
    const keys = ['LEVEL', 'VALVE_STATE', 'VALVE_LEVEL', 'WINDOW_STATE', 'WINDOW_OPEN'];
    return keys.some((k) => (!!desc && k in desc) || values[k] !== undefined);
}

// ---- occulited task 113: what a blind or a lock is doing right now. HmIP says ACTIVITY_STATE (UNKNOWN, UP, DOWN,
// STABLE) and PROCESS (STABLE, NOT_STABLE), BidCos DIRECTION (NONE, UP, DOWN, UNDEFINED) and WORKING; a lock's UP is
// unlocking or opening (the KeyMatic's DIRECTION, the HmIP-DLD's ACTIVITY_STATE), DOWN locking.

export type Motion = {dir?: 'up' | 'down'};

/** An enum datapoint's value name: the value list's entry for an index, or the string itself. */
function enumName(values: Values, desc: Description | undefined, key: string): string | undefined {
    const v = values[key];
    if (typeof v === 'string' && isNaN(Number(v))) return v;
    const n = num(v);
    if (n === undefined) return undefined;
    // without a description both lists have UP at 1 and DOWN at 2
    return desc?.[key]?.VALUE_LIST?.[n] ?? (['', 'UP', 'DOWN'][n] || undefined);
}

/** The action a blind or a lock is running, or undefined when it stands still. A channel that reports PROCESS or
 *  WORKING moves exactly while that says so (the direction only gives the arrow); one without either moves while
 *  its direction is UP or DOWN. */
export function motion(m: Model, values: Values, desc?: Description): Motion | undefined {
    if (m.widget !== 'blind' && m.widget !== 'lock') return undefined;
    const name = enumName(values, desc, 'ACTIVITY_STATE') ?? enumName(values, desc, 'DIRECTION');
    const dir = name === 'UP' ? 'up' : name === 'DOWN' ? 'down' : undefined;
    const has = (k: string) => (!!desc && k in desc) || values[k] !== undefined;
    const moving = has('PROCESS') || has('WORKING') ? working(values) : dir !== undefined;
    return moving ? (dir ? {dir} : {}) : undefined;
}

// ---- occulited B-108: a click that starts an action shows the action at once - never the target state before the
// device reports it reached. The written value is not applied; the action is pending from the click until the device
// reports its own (motion), an end (PROCESS/WORKING done), STOP, a write error or the timeout.

/** How long a pending action waits for the device's report, per widget (ms). */
export const PENDING_MS: Record<string, number> = {blind: 30_000, lock: 15_000};

/** The action a write starts on a blind or a lock, with its direction and timeout; undefined for a write that starts
 *  none (STOP, a slat level, a level or lock state the channel is already at). */
export function actionOf(m: Model, key: string, value: unknown, values: Values, desc?: Description): {dir?: 'up' | 'down'; ms: number} | undefined {
    if (m.widget === 'blind') {
        if (key !== 'LEVEL') return undefined;
        const to = num(value);
        const from = num(values.LEVEL);
        if (to === undefined || (from !== undefined && Math.abs(to - from) < 0.005)) return undefined;
        return {dir: from === undefined ? undefined : to > from ? 'up' : 'down', ms: PENDING_MS.blind!};
    }
    if (m.widget === 'lock') {
        const now = lockState(values, desc, m.stateKey);
        if (key === 'OPEN') return value === true || value === 1 ? {dir: 'up', ms: PENDING_MS.lock!} : undefined;
        let lock: boolean | undefined;
        if (key === 'LOCK_TARGET_LEVEL') {
            const n = num(value);
            if (n === undefined) return undefined;
            lock = n === 0;
            // OPEN (2) opens an unlocked lock too
            if (n === 2) return {dir: 'up', ms: PENDING_MS.lock!};
        } else if (key === 'STATE') lock = value === false || value === 0;
        else return undefined;
        if ((lock && now === 'locked') || (!lock && (now === 'unlocked' || now === 'open'))) return undefined;
        return {dir: lock ? 'down' : 'up', ms: PENDING_MS.lock!};
    }
    return undefined;
}

/** The values a channel shows while its action is pending: moving (PROCESS or WORKING) in the direction. */
export function withPending(values: Values, desc: Description | undefined, dir?: 'up' | 'down'): Values {
    const v = {...values};
    const has = (k: string) => (!!desc && k in desc) || values[k] !== undefined;
    if (has('PROCESS') || !has('WORKING')) v.PROCESS = 1;
    if (has('WORKING')) v.WORKING = true;
    const dk = has('ACTIVITY_STATE') || !has('DIRECTION') ? 'ACTIVITY_STATE' : 'DIRECTION';
    const list = desc?.[dk]?.VALUE_LIST;
    const at = (n: string, fallback: number) => (list && list.indexOf(n) >= 0 ? list.indexOf(n) : fallback);
    v[dk] = dir === 'up' ? at('UP', 1) : dir === 'down' ? at('DOWN', 2) : 0;
    return v;
}

/** The datapoints a running action reports on the way (held until it stands): the levels, and a lock's state. */
export function heldKeys(m: Model): Set<string> {
    return m.widget === 'lock' && m.stateKey ? new Set([...LEVEL_KEYS, m.stateKey]) : LEVEL_KEYS;
}

/** occulited task 114: what a click on a blind's disc does - stop it while it moves, otherwise open a mostly closed
 *  one (LEVEL below 50 %; Homematic's LEVEL 1 is open) and close a mostly open one (50 % and more). A channel whose
 *  LEVEL takes no write only stops. */
export function blindAction(values: Values, desc?: Description): 'stop' | 'open' | 'close' {
    if (motion({widget: 'blind', tile: 'act-open', stateKey: 'LEVEL', commandKey: 'STOP'}, values, desc)) return 'stop';
    if (desc && !writable(desc, 'LEVEL')) return 'stop';
    return (num(values.LEVEL) ?? 0) < 0.5 ? 'open' : 'close';
}

/** Whether the circle is "on" (lit). */
export function isOn(m: Model, values: Values, desc?: Description): boolean {
    const v = values[m.stateKey];
    switch (m.widget) {
        case 'switch': case 'door': return v === true || v === 1;
        case 'dimmer': case 'color': case 'blind': return (num(v) ?? 0) > 0;
        case 'lock': return ['unlocked', 'open'].includes(lockState(values, desc, m.stateKey) ?? ''); // unlocked or open
        case 'motion': return v === true;
        case 'contact': return v !== false && v !== 0 && v !== undefined; // open or tilted
        case 'smoke': return v === true || (num(v) ?? 0) > 0; // an alarm
        case 'signal': return values.ACOUSTIC_ALARM_ACTIVE === true || values.OPTICAL_ALARM_ACTIVE === true || v === true || (num(v) ?? 0) > 0;
        default: return false;
    }
}

/** The tile's one state line. Words the caller translates (t) are given as keys. */
export function stateText(m: Model, values: Values, desc: Description | undefined, t: (k: string, p?: Record<string, string>) => string): string {
    const v = values[m.stateKey];
    if (m.widget === 'button') return ''; // a press is a moment, shown by the tile as it happens, not a state
    if (v === undefined) return '';
    switch (m.widget) {
        case 'switch': return v === true ? t('On') : t('Off');
        case 'dimmer': case 'color': {
            const n = num(v) ?? 0;
            return n > 0 ? `${t('On')} · ${pct(n)}` : t('Off');
        }
        case 'blind': {
            const mv = motion(m, values, desc);
            if (mv) return t(mv.dir === 'up' ? 'Moving up · {p}' : mv.dir === 'down' ? 'Moving down · {p}' : 'Moving · {p}', {p: pct(v)});
            return t('{p} open', {p: pct(v)});
        }
        case 'thermostat': {
            // task 75: with a valve the room temperature and the valve; without one the setpoint first
            const set = num(v);
            const actual = num(values.ACTUAL_TEMPERATURE);
            const valve = valvePercent(values, desc);
            const parts: string[] = [];
            if (valve !== undefined) {
                if (actual !== undefined) parts.push(`${actual.toFixed(1)} °C`);
                parts.push(t('valve {v}', {v: `${valve} %`}));
            } else {
                if (set !== undefined) parts.push(t('set {v}', {v: `${set.toFixed(1)} °C`}));
                if (actual !== undefined) parts.push(`${actual.toFixed(1)} °C`);
            }
            // HmIP reports an open window on the thermostat itself (its own mode drop)
            if (values.WINDOW_STATE === 1 || values.WINDOW_STATE === '1' || values.WINDOW_OPEN === true) parts.push(t('Window open'));
            return parts.join(' · ');
        }
        case 'lock': {
            const mv = motion(m, values, desc);
            if (mv) return t(mv.dir === 'up' ? 'Opening …' : mv.dir === 'down' ? 'Locking …' : 'In progress …');
            const st = lockState(values, desc, m.stateKey);
            return st === 'open' ? t('Open') : st === 'unlocked' ? t('Unlocked') : st === 'locked' ? t('Locked') : '';
        }
        case 'door': return v === true ? t('Open') : t('Closed');
        case 'contact': {
            const list = desc?.[m.stateKey]?.VALUE_LIST;
            if (list && typeof v === 'number' && list[v]) return t(list[v] === 'OPEN' ? 'Open' : list[v] === 'TILTED' ? 'Tilted' : list[v] === 'CLOSED' ? 'Closed' : list[v]);
            return v === true || v === 1 ? t('Open') : t('Closed');
        }
        case 'motion': return v === true ? t('Motion') : t('No motion');
        case 'smoke': {
            const list = desc?.[m.stateKey]?.VALUE_LIST;
            if (list && typeof v === 'number') return t(list[v] === 'IDLE_OFF' ? 'Quiet' : list[v] === 'PRIMARY_ALARM' ? 'Smoke alarm' : list[v] === 'INTRUSION_ALARM' ? 'Alarm' : list[v] === 'SECONDARY_ALARM' ? 'Alarm (secondary)' : 'Alarm');
            return v === true ? t('Smoke alarm') : t('Quiet');
        }
        case 'signal': return isOn(m, values, desc) ? t('Alarm') : t('Quiet');
        case 'power': return `${num(v)?.toFixed(1) ?? v} W`;
        case 'reading': {
            const parts: string[] = [];
            const tmp = num(values.ACTUAL_TEMPERATURE ?? values.TEMPERATURE);
            if (tmp !== undefined) parts.push(`${tmp.toFixed(1)} °C`);
            const hum = num(values.HUMIDITY);
            if (hum !== undefined) parts.push(`${Math.round(hum)} %`);
            if (parts.length === 0) parts.push(String(v));
            return parts.join(' · ');
        }
    }
    return '';
}

/** The main action's next value, or undefined when the widget has none. */
export function mainAction(m: Model, values: Values, desc?: Description): {key: string; value: unknown} | undefined {
    if (!m.commandKey) return undefined;
    switch (m.widget) {
        case 'switch': return {key: m.commandKey, value: !(values[m.stateKey] === true)};
        case 'dimmer': case 'color': return {key: m.commandKey, value: (num(values[m.stateKey]) ?? 0) > 0 ? 0 : 1};
        case 'blind': {
            const b = blindAction(values, desc);
            return b === 'stop' ? {key: 'STOP', value: true} : {key: 'LEVEL', value: b === 'open' ? 1 : 0};
        }
        case 'door': return {key: m.commandKey, value: true};
        case 'lock': {
            // task 75: a locked lock is unlocked, an unlocked or open one locked - never the door
            // opened from the circle (a KeyMatic's OPEN): LOCK_TARGET_LEVEL (HmIP: 0 locked,
            // 1 unlocked) or STATE (BidCos: true unlocked)
            const locked = lockState(values, desc, m.stateKey) === 'locked';
            if (m.commandKey === 'LOCK_TARGET_LEVEL') return {key: m.commandKey, value: locked ? 1 : 0};
            if (desc && !('STATE' in desc)) return undefined;
            return {key: 'STATE', value: locked};
        }
        case 'button': return {key: 'PRESS_SHORT', value: true};
    }
    return undefined;
}

/** The long action - a key's long press - or undefined. */
export function longAction(m: Model, desc: Description | undefined): {key: string; value: unknown} | undefined {
    if (m.widget === 'button' && m.commandKey && writable(desc, 'PRESS_LONG')) return {key: 'PRESS_LONG', value: true};
    return undefined;
}

/** Whether the channel is still ramping: BidCos says WORKING, HmIP PROCESS 1 (the level reported meanwhile is
 *  intermediate; the tile and the slider keep the settled or the sent value until it is over). */
export function working(values: Values): boolean {
    return values.WORKING === true || values.PROCESS === 1 || values.PROCESS === '1';
}

/** The level keys a ramp reports intermediate values for. */
export const LEVEL_KEYS = new Set(['LEVEL', 'LEVEL_2']);

/** interface and address of a metadata ref (<interface>.<address>); the interface may be missing on an old ref. */
export function splitRef(ref: string): {iface: string; address: string} {
    const i = ref.indexOf('.');
    if (i < 0) return {iface: '', address: ref};
    return {iface: ref.slice(0, i), address: ref.slice(i + 1)};
}

// ---- occulited task 109: a switching actuator with a power meter (HmIP-PSM, -BSM, -FSM; BidCos HM-ES-PMSw1-Pl) is
// one tile: the switch's, its state line with the current power ("On · 12.5 W"); the meter's own tile is left out
// where the switch's is shown. A meter alone in a view keeps its tile.

/** Whether a channel is a power meter: its tile's state is POWER (HmIP ENERGIE_METER_TRANSMITTER, BidCos POWERMETER). */
export function isMeter(m: Model | undefined): boolean {
    return !!m && (m.widget === 'power' || (m.widget === 'reading' && m.stateKey === 'POWER'));
}

/**
 * The switch-and-meter pairs among a view's channels: `meter` maps each switch channel to its device's meter channel,
 * `hidden` holds the meters whose device has a switch in the view. Same interface, same device (the address before
 * the colon); a device with several switch channels gives the meter to each.
 */
export function meterPairs(list: {ref: string; model?: Model}[]): {meter: Map<string, string>; hidden: Set<string>} {
    const dev = (ref: string) => {
        const {iface, address} = splitRef(ref);
        const i = address.lastIndexOf(':');
        return `${iface}.${i > 0 ? address.slice(0, i) : address}`;
    };
    const meters = new Map<string, string>();
    for (const x of list) if (isMeter(x.model) && !meters.has(dev(x.ref))) meters.set(dev(x.ref), x.ref);
    const meter = new Map<string, string>();
    const hidden = new Set<string>();
    for (const x of list) {
        if (x.model?.widget !== 'switch') continue;
        const mref = meters.get(dev(x.ref));
        if (!mref) continue;
        meter.set(x.ref, mref);
        hidden.add(mref);
    }
    return {meter, hidden};
}

/** A power in W as the tile and the sheet show it: one decimal below 100 W, none up to 1000 W, kW from there (two
 *  decimals below 10 kW, one above); the language's decimal mark. */
export function formatPower(w: unknown, language = 'en'): string {
    const n = num(w);
    if (n === undefined) return '';
    const fmt = (x: number, d: number) => new Intl.NumberFormat(language === 'de' ? 'de-DE' : 'en-US', {minimumFractionDigits: d, maximumFractionDigits: d, useGrouping: false}).format(x);
    const a = Math.abs(n);
    if (a >= 1000) return `${fmt(n / 1000, a >= 10_000 ? 1 : 2)} kW`;
    return `${fmt(n, a >= 100 ? 0 : 1)} W`;
}
