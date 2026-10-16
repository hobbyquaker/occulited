// Task 95: the status LED page's model - the API's shapes and the pure steps the page takes on a
// configuration (moving a row, the look choices a row may have). No texts here: the page owns them,
// so the i18n check sees every key.

export interface LEDLook {
    color: string;
    pattern: string;
    color2?: string;
    /** task 134: the blink's off phase shows the normal colour instead of dark */
    over_normal?: boolean;
}

export interface LEDStateConfig extends Partial<LEDLook> {
    id: string;
    enabled: boolean;
}

/** openccu-lite task 316: one threshold of the radio load with its look; over_normal is the page's "overlay" */
export interface LEDRadioLevel extends LEDLook {
    enabled: boolean;
    threshold: number;
}

export interface LEDRadioConfig {
    duty_cycle: LEDRadioLevel[];
    carrier_sense: LEDRadioLevel[];
}

/** the rows whose look is the active level's (task 316) */
export const RADIO_STATES = ['radio-duty-cycle', 'radio-carrier-sense'];

export interface LEDConfig {
    enabled: boolean;
    /** task 315: the LED's level in percent (10-100; PWM only) */
    brightness: number;
    /** task 315: a 300 ms cross-fade between looks (PWM only); absent = on */
    fade?: boolean;
    normal: LEDLook;
    /** task 315: dim is the night's level in percent of brightness; dimmed shows everything at it */
    night: {enabled: boolean; from: string; to: string; show: 'errors' | 'off' | 'dimmed'; dim: number};
    states: LEDStateConfig[];
    warnings_off: string[];
    addon_units: boolean;
    locate: {color: string; pattern: string; duration_s: number};
    external: {max_duration_s: number; allow_until_cleared: boolean};
    pwr_error_light: boolean;
    /** task 316: the radio load's levels behind the radio-duty-cycle and radio-carrier-sense rows */
    radio: LEDRadioConfig;
}

/** The radio levels of a row, by its id (task 316). */
export function radioLevels(c: LEDConfig, id: string): LEDRadioLevel[] {
    return id === 'radio-carrier-sense' ? c.radio.carrier_sense : c.radio.duty_cycle;
}

export interface LEDView {
    available: boolean;
    reason?: string;
    /** brightness (task 315): the LED has levels - colours are mixed from any #rrggbb, breathe, dimming */
    hardware: {kind?: string; leds: string[]; colors: string[]; patterns: string[]; brightness: boolean; max_brightness?: number};
    pwr: boolean;
    config: LEDConfig;
    defaults: LEDConfig;
    fixed: string[];
    error_states: string[];
    warning_ids: string[];
    /** task 95 (D-67): the hosts the no-internet check connects to, [] for none */
    internet_hosts?: string[];
}

export interface LEDOverride extends LEDLook {
    id: string;
    priority: 'low' | 'normal' | 'high';
    night: 'respect' | 'ignore';
    until?: string;
    by: string;
    since: string;
}

export interface LEDState {
    available: boolean;
    reason?: string;
    /** background: the colour the blink's off phase shows (task 134), absent for dark */
    shown: LEDLook & {source: string; id: string; detail?: string; since: string; background?: string; dim?: number; level?: number};
    active: {id: string; since: string; detail?: string; level?: number}[];
    overrides: LEDOverride[];
    locate?: LEDLook & {until: string};
    preview?: LEDLook & {until: string};
    night: boolean;
    booting: boolean;
    conflict: boolean;
    write_error?: string;
    pwr?: {enabled: boolean; error: boolean};
    capabilities: {colors: string[]; patterns: string[]; brightness: boolean; max_brightness?: number};
    /** openccu-lite task 164: the warnings are muted until then */
    muted_until?: string;
}

/** The seven names, each as #rrggbb (task 315: the presets of the colour picker). */
export const NAMED: Record<string, string> = {
    red: '#ff0000',
    green: '#00ff00',
    blue: '#0000ff',
    yellow: '#ffff00',
    cyan: '#00ffff',
    magenta: '#ff00ff',
    white: '#ffffff',
    off: '#000000',
};

const HEX = /^#[0-9a-fA-F]{6}$/;

/** Whether a colour is one occulited takes: a name or #rrggbb. */
export function isColor(c: string): boolean {
    return c in NAMED || HEX.test(c);
}

/** A colour as #rrggbb: a name's value, a hex value lower-cased; '' for anything else. */
export function hexOf(c: string): string {
    const named = NAMED[c];
    if (named) return named;
    return HEX.test(c) ? c.toLowerCase() : '';
}

/** A colour in occulited's one spelling: a hex value that is a name's becomes the name, black off. */
export function normalizeColor(c: string): string {
    const h = hexOf(c);
    if (!h) return c;
    for (const [name, v] of Object.entries(NAMED)) if (v === h) return name;
    return h;
}

/** The red, green and blue of a colour, 0-255 each; [0,0,0] for an unknown one. */
export function rgbOf(c: string): [number, number, number] {
    const h = hexOf(c);
    if (!h) return [0, 0, 0];
    return [parseInt(h.slice(1, 3), 16), parseInt(h.slice(3, 5), 16), parseInt(h.slice(5, 7), 16)];
}

/** The rows of the simple view, in the order the page lists them. */
export const SIMPLE_STATES = ['radio-down', 'no-network', 'service-failed', 'status-warning', 'system-update'];

/** Which of red, green, blue a colour lights at all. */
function channels(c: string): [boolean, boolean, boolean] | null {
    if (!isColor(c)) return null;
    const [r, g, b] = rgbOf(c);
    return [r > 0, g > 0, b > 0];
}

/** Whether two colours share no channel - the pairs that can alternate. */
export function disjoint(a: string, b: string): boolean {
    const ca = channels(a);
    const cb = channels(b);
    if (!ca || !cb) return false;
    return !ca.some((on, i) => on && cb[i]);
}

/** The second colours a colour may alternate with: they light, and on other channels. */
export function secondColors(color: string, colors: string[]): string[] {
    return colors.filter((c) => c !== 'off' && c !== color && disjoint(color, c));
}

/** The patterns that can blink over the normal colour (task 134; breathe pulses between the two, task 315). */
export const OVER_NORMAL_PATTERNS = ['slow', 'fast', 'flash', 'double', 'breathe'];

/**
 * Whether a look can blink over the normal colour: 'yes', 'same' (the normal colour is the blink's
 * own, so the blink would not be seen and the page says so) or 'no' (the pattern has no off phase,
 * or the LED is dark).
 */
export function overNormalOption(l: Partial<LEDLook>, normal: LEDLook): 'yes' | 'same' | 'no' {
    if (!l.color || l.color === 'off' || !OVER_NORMAL_PATTERNS.includes(l.pattern ?? '')) return 'no';
    return normal.color === l.color ? 'same' : 'yes';
}

/** The colour a look's off phase shows on the box: the normal colour when it blinks over it visibly, '' for dark. */
export function backgroundOf(l: Partial<LEDLook>, normal: LEDLook, enabled = true): string {
    return enabled && l.over_normal && normal.color !== 'off' && overNormalOption(l, normal) === 'yes' ? normal.color : '';
}

/** A look in the API's one spelling: the colour's name where it has one, off is solid, a second colour only for alternate (and then a valid one), over_normal only set and only for a blinking pattern. */
export function normalizeLook<T extends Partial<LEDLook>>(l: T, colors: string[]): T {
    const out = {...l};
    if (out.color) out.color = normalizeColor(out.color);
    if (out.color2) out.color2 = normalizeColor(out.color2);
    if (!out.over_normal || out.color === 'off' || !OVER_NORMAL_PATTERNS.includes(out.pattern ?? '')) delete out.over_normal;
    if (out.color === 'off') {
        out.pattern = 'solid';
        delete out.color2;
        return out;
    }
    if (out.pattern !== 'alternate') {
        delete out.color2;
        return out;
    }
    const options = secondColors(out.color ?? '', colors);
    if (!out.color2 || !options.includes(out.color2)) out.color2 = options[0];
    if (!out.color2) out.pattern = 'solid';
    return out;
}

/** What the segmented control "when everything is fine" shows: blue, green, off, or custom. */
export function normalChoice(l: LEDLook): 'blue' | 'green' | 'off' | 'custom' {
    if (l.color === 'off') return 'off';
    if (l.pattern === 'solid' && (l.color === 'blue' || l.color === 'green')) return l.color;
    return 'custom';
}

/** Whether a stored configuration and the page's draft differ. */
export function changed(a: LEDConfig | null, b: LEDConfig | null): boolean {
    return JSON.stringify(a) !== JSON.stringify(b);
}

/** The CSS colour of the seven names for the swatch: a little softer than the pure values. */
export const SWATCH: Record<string, string> = {
    red: '#e5252c',
    green: '#22b24a',
    blue: '#2474e8',
    yellow: '#f1c21b',
    cyan: '#1cc6d3',
    magenta: '#cf3ad6',
    white: '#f5f5f5',
    off: 'transparent',
};

/** The CSS colour of any LED colour for the swatch: a name's swatch, a hex value itself. */
export function swatchOf(c: string): string {
    return SWATCH[c] ?? (HEX.test(c) ? c.toLowerCase() : 'transparent');
}

/** Whether the configuration shows a brightness below full, or a dimmed night, that an on/off LED cannot show. */
export function dimsAnything(c: LEDConfig): boolean {
    return c.brightness < 100 || (c.night.enabled && c.night.dim < 100);
}

/**
 * occulited task 62: when a mute ends, for the Status page's line - the time alone on the same day,
 * day and month before it otherwise (a mute lasts up to 24 hours); '' for a stamp that is not one.
 */
export function mutedUntilText(iso: string, lang: string, now: Date = new Date()): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    const loc = lang === 'de' ? 'de-DE' : 'en-GB';
    const opts: Intl.DateTimeFormatOptions = {hour: '2-digit', minute: '2-digit', hourCycle: 'h23'};
    if (d.toDateString() !== now.toDateString()) {
        opts.day = '2-digit';
        opts.month = '2-digit';
    }
    return d.toLocaleString(loc, opts);
}
