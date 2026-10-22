/*
 * occulited task 95: how long a contact or a motion detector has been in its state - "seit 3 Std 12 Min" - from the
 * state store's changed_at (the moment the system saw the change happen; absent when it does not know it, and then
 * nothing is shown). At most two units, rounded down to the minute, so the text changes at most once a minute: the
 * page recomputes it on the full minute without fetching anything (nextDurationChange).
 */
import {runTime} from '../timers';
import type {Model, Values} from './channels';

const MIN = 60_000;

/** The duration in the reader's language: "4 Min", "3 Std 12 Min", "2 Tg 5 Std", "11 Tagen" from 7 days; "" under a minute. */
export function durationText(ms: number, lang: string): string {
    const m = Math.floor(Math.max(0, ms) / MIN);
    if (m < 1) return '';
    const d = Math.floor(m / 1440);
    const h = Math.floor((m % 1440) / 60);
    const mm = m % 60;
    const de = lang === 'de';
    const U = de ? {m: 'Min', h: 'Std', d: 'Tg', days: 'Tagen'} : {m: 'min', h: 'h', d: 'd', days: 'days'};
    if (d >= 7) return `${d} ${U.days}`;
    if (d >= 1) return `${d} ${U.d}` + (h ? ` ${h} ${U.h}` : '');
    if (h >= 1) return `${h} ${U.h}` + (mm ? ` ${mm} ${U.m}` : '');
    return `${mm} ${U.m}`;
}

/** Whether a tile of this model shows how long it is in its state. */
export function hasDuration(m: Model | undefined): boolean {
    return !!m && (m.widget === 'contact' || m.widget === 'motion');
}

/** A motion detector that reports no motion says when it last did ("zuletzt vor 14 Min"). */
function lastSeen(m: Model, values: Values): boolean {
    return m.widget === 'motion' && values[m.stateKey] !== true;
}

/** The tile's duration line, or "" when the model has none or the time is not known. */
export function durationLine(m: Model | undefined, values: Values, changedAt: string | undefined, now: number, lang: string): string {
    if (!hasDuration(m) || !changedAt) return '';
    const at = Date.parse(changedAt);
    if (Number.isNaN(at)) return '';
    const d = durationText(now - at, lang);
    const de = lang === 'de';
    if (lastSeen(m!, values)) return d ? (de ? `zuletzt vor ${d}` : `last ${d} ago`) : de ? 'zuletzt gerade eben' : 'last just now';
    return de ? `seit ${d || 'unter 1 Min'}` : `for ${d || 'under 1 min'}`;
}

/** The sheet's exact time: "seit 21.10., 14:03", "zuletzt am 21.10., 14:03"; "" when not known. */
export function sinceLine(m: Model | undefined, values: Values, changedAt: string | undefined, lang: string, now: Date = new Date()): string {
    if (!hasDuration(m) || !changedAt || Number.isNaN(Date.parse(changedAt))) return '';
    const when = runTime(changedAt, lang, now);
    const de = lang === 'de';
    if (lastSeen(m!, values)) return de ? `zuletzt am ${when}` : `last on ${when}`;
    return de ? `seit ${when}` : `since ${when}`;
}

/**
 * When the next shown duration changes: the earliest full minute of any of them (at + k minutes), a little after it
 * so the clock has passed it; undefined without a time. Ticking on the durations' own minutes, not the clock's, keeps
 * every line exact to the minute; nothing is fetched for it.
 */
export function nextDurationChange(times: readonly number[], now: number): number | undefined {
    let best: number | undefined;
    for (const at of times) {
        if (Number.isNaN(at)) continue;
        const into = (((now - at) % MIN) + MIN) % MIN;
        const wait = MIN - into;
        if (best === undefined || wait < best) best = wait;
    }
    return best === undefined ? undefined : best + 20;
}
