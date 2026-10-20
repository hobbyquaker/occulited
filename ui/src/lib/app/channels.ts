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
    {kind: /^(SWITCH|SWITCH_TRANSMITTER|WATER_SWITCH|UNIVERSAL_LIGHT_RECEIVER|DIGITAL_STATE|ARMING)$/, widget: 'switch', state: 'STATE', command: 'STATE', tile: 'act'},
    // a smoke detector: the alarm state on the tile; its test and silence in the sheet when it takes a command
    {kind: /^SMOKE_DETECTOR$/, widget: 'smoke', state: 'SMOKE_DETECTOR_ALARM_STATUS|STATE', command: '', tile: 'read'},
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

/** Classifies a channel by its VALUES description. */
export function classify(desc: Description | undefined): Model {
    if (!desc) return {widget: 'none', tile: 'read', stateKey: '', commandKey: ''};
    const kinds = kindsOf(desc);
    for (const k of KINDS) {
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

/** Whether the channel is worth a tile at all. */
export function shown(m: Model): boolean {
    return m.widget !== 'none';
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
        case 'blind': return t('{p} open', {p: pct(v)});
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
        case 'blind': return {key: 'STOP', value: true};
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
