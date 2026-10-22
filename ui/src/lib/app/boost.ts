// occulited task 103: a thermostat's boost - the device shows its countdown in its display, Control
// shows it on the tile and in the sheet. HmIP reports BOOST_MODE (on/off) and BOOST_TIME (the seconds
// left); a BidCos radiator thermostat CONTROL_MODE 3 and BOOST_STATE (the minutes left). The UI counts
// down locally every second from the last report and re-syncs on every new one; the disc starts a
// boost and cancels a running one.
import type {Description, Values} from './channels';

function num(v: unknown): number | undefined {
    return typeof v === 'number' ? v : typeof v === 'string' && v !== '' && !isNaN(Number(v)) ? Number(v) : undefined;
}
/** OPERATIONS bit 2: the datapoint takes a setValue. */
function writable(desc: Description | undefined, key: string): boolean {
    const op = desc?.[key]?.OPERATIONS;
    return typeof op === 'number' && (op & 2) !== 0;
}

/** Whether the channel reports a running boost: BidCos by its CONTROL_MODE alone (its BOOST_MODE is an action, whose
 *  written true may linger as a value), HmIP by BOOST_MODE. */
export function boostActive(values: Values): boolean {
    const cm = num(values.CONTROL_MODE);
    if (cm !== undefined) return cm === 3;
    return values.BOOST_MODE === true;
}

/** The unit of a datapoint's description in seconds per step: 60 for "min", 1 for "s", undefined without one. */
function unitSeconds(desc: Description | undefined, key: string): number | undefined {
    const u = desc?.[key]?.UNIT;
    if (typeof u !== 'string') return undefined;
    const l = u.trim().toLowerCase();
    return l === 'min' ? 60 : l === 's' ? 1 : undefined;
}

/**
 * The seconds left the device last reported; undefined without one. HmIP: BOOST_TIME in seconds. BidCos (a channel
 * with CONTROL_MODE): BOOST_STATE in minutes, or - the HM-CC-RT-DN-BoM, which has no BOOST_STATE - its BOOST_TIME,
 * also in minutes (0-30, rfd's rf_cc_rt_dn_bom.xml); a BidCos BOOST_TIME is never read as seconds (occulited B-103:
 * production's BoM reports 24 while idle). The description's UNIT wins where it names one.
 */
export function boostReported(values: Values, desc?: Description): number | undefined {
    const bidcos = values.CONTROL_MODE !== undefined;
    const read = (key: string, fallback: number): number | undefined => {
        const v = num(values[key]);
        return v === undefined ? undefined : Math.max(0, Math.round(v * (unitSeconds(desc, key) ?? fallback)));
    };
    if (bidcos) return read('BOOST_STATE', 60) ?? read('BOOST_TIME', 60);
    return read('BOOST_TIME', 1) ?? read('BOOST_STATE', 60);
}

/** The datapoint the remaining time is in, for its change time (task 95's changed_at). */
function reportedKey(values: Values): string | undefined {
    const bidcos = values.CONTROL_MODE !== undefined;
    for (const k of bidcos ? ['BOOST_STATE', 'BOOST_TIME'] : ['BOOST_TIME', 'BOOST_STATE']) if (num(values[k]) !== undefined) return k;
    return undefined;
}

/** Whether the channel takes a boost command (BOOST_MODE is writable). */
export function canBoost(desc: Description | undefined): boolean {
    return writable(desc, 'BOOST_MODE');
}

/** The mode a channel is in besides boost: HmIP SET_POINT_MODE 0/1, BidCos CONTROL_MODE 0/1/2 (party counts as auto). */
export function plainMode(values: Values): 'auto' | 'manual' | undefined {
    const hm = num(values.SET_POINT_MODE);
    if (hm !== undefined) return hm === 1 ? 'manual' : 'auto';
    const cm = num(values.CONTROL_MODE);
    if (cm !== undefined) return cm === 1 ? 'manual' : cm === 3 ? undefined : 'auto';
    return undefined;
}

/**
 * The write that starts a boost, or ends the running one. HmIP: BOOST_MODE true/false. BidCos: BOOST_MODE
 * true starts it; it is ended by going back to the mode before it - AUTO_MODE true, or MANU_MODE with the
 * setpoint (auto when the mode before is not known).
 */
export function boostCommand(desc: Description | undefined, values: Values, start: boolean, before?: 'auto' | 'manual'): Record<string, unknown> {
    if (start) return {BOOST_MODE: true};
    const bidcos = writable(desc, 'AUTO_MODE') || writable(desc, 'MANU_MODE') || values.CONTROL_MODE !== undefined;
    if (!bidcos) return {BOOST_MODE: false};
    if (before === 'manual' && writable(desc, 'MANU_MODE')) return {MANU_MODE: num(values.SET_TEMPERATURE) ?? num(values.SET_POINT_TEMPERATURE) ?? 20};
    return writable(desc, 'AUTO_MODE') ? {AUTO_MODE: true} : {BOOST_MODE: false};
}

export interface BoostInfo {
    /** the seconds left, counted down locally; 0 when the boost is over or no time is known */
    remaining: number;
    /** the whole boost's seconds (the first report of this boost); the ring's 100 %. 0 when no time is known: the
     *  tile says "Boost" and the ring is indeterminate (occulited B-103) */
    full: number;
}
interface Track {
    /** the last report, and when it was taken */
    sig: string;
    reported: number;
    anchor: number;
    full: number;
    /** the mode before the boost, for the way back */
    before?: 'auto' | 'manual';
}

/** A channel as the tracker reads it: its values, its description (the units), and when the system saw its values
 *  change (the state store's changed_at, task 95). */
export interface BoostSource {
    ref: string;
    values: Values;
    desc?: Description;
    changed?: Record<string, string>;
}

/**
 * The per-channel countdown: `update` on every render takes the channel's values and now (ms), anchors a
 * new report at the moment the system saw it (its changed_at, else now), and answers the seconds left - or
 * null when no boost runs. `before` is the mode the channel had before its boost began.
 *
 * occulited B-103: "boosting" is the channel's own word (BOOST_MODE, BidCos CONTROL_MODE 3), never "time left > 0".
 * A heating group (VirtualDevices) says BOOST_MODE true with BOOST_TIME 0; its time is its members': `members` are
 * the group's member channels, and the highest time left of those that boost is the group's.
 */
export class BoostTracker {
    private tracks = new Map<string, Track>();

    update(ref: string, values: Values, now: number, opt: {desc?: Description; changed?: Record<string, string>; members?: BoostSource[]} = {}): BoostInfo | null {
        const t = this.tracks.get(ref);
        const mode = plainMode(values);
        if (!boostActive(values)) {
            // not boosting: remember the mode for the way back, forget a finished countdown
            if (t) this.tracks.set(ref, {...t, sig: '', reported: 0, anchor: now, full: 0, before: mode ?? t.before});
            else if (mode) this.tracks.set(ref, {sig: '', reported: 0, anchor: now, full: 0, before: mode});
            for (const m of opt.members ?? []) this.update(m.ref, m.values, now, {desc: m.desc, changed: m.changed});
            return null;
        }
        const reported = boostReported(values, opt.desc);
        if (!reported) {
            // no time of its own (a group's BOOST_TIME 0, or none at all): the members' highest, else "Boost" without one
            let best: BoostInfo | null = null;
            for (const m of opt.members ?? []) {
                const b = this.update(m.ref, m.values, now, {desc: m.desc, changed: m.changed});
                if (b && b.full > 0 && (!best || b.remaining > best.remaining)) best = b;
            }
            if (best) return best;
            if (reported === undefined || !t || t.full === 0) {
                if (!t) this.tracks.set(ref, {sig: '', reported: 0, anchor: now, full: 0});
                return {remaining: 0, full: 0};
            }
        }
        const sig = `${reported}`;
        let track = t;
        if (!track || track.sig !== sig) {
            // a new report: the countdown starts from when the system saw it (never later than now, never before the
            // time it reports would have run out), else from now; the first report of this boost is its whole length
            const key = reportedKey(values);
            const at = key ? Date.parse(opt.changed?.[key] ?? '') : NaN;
            const anchor = Number.isFinite(at) && at <= now ? Math.max(at, now - (reported ?? 0) * 1000) : now;
            const full = track && track.full > 0 ? Math.max(track.full, reported ?? 0) : (reported ?? 0);
            track = {sig, reported: reported ?? 0, anchor, full, before: track?.before};
            this.tracks.set(ref, track);
        }
        const remaining = Math.max(0, Math.ceil(track.reported - (now - track.anchor) / 1000));
        return {remaining, full: track.full};
    }

    /** the mode the channel had before its boost, if seen */
    before(ref: string): 'auto' | 'manual' | undefined {
        return this.tracks.get(ref)?.before;
    }

    forget(ref: string): void {
        this.tracks.delete(ref);
    }
}

/** "2 Min 48 s" as minutes and seconds; the caller puts them into its text. */
export function splitSeconds(total: number): {m: number; s: string} {
    const t = Math.max(0, Math.round(total));
    return {m: Math.floor(t / 60), s: String(t % 60).padStart(2, '0')};
}

/** the ring's geometry: the circumference of a radius, and the dash offset for a fraction left */
export const RING_R = 30;
export const RING_C = 2 * Math.PI * RING_R;
export function ringOffset(remaining: number, full: number): number {
    const f = full > 0 ? Math.min(1, Math.max(0, remaining / full)) : 0;
    return RING_C * (1 - f);
}
