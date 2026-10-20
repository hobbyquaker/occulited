/*
 * The duty cycle history and its meter levels, shared by the Status page and the Interfaces
 * page (tasks 53 and 54). Pure: no DOM, no Svelte - `history.test.ts` covers it.
 *
 * The sampler (internal/health) polls every interface once a minute and keeps 500 samples, across
 * a restart of occulited unless its database is in RAM only (task 214); each sample
 * carries its time `t`, the duty cycle `dc` and whether the interface answered (`up`). What the
 * pages draw is one series per *radio* - the per-sample maximum across the stacks of a module,
 * never their sum (maintainer, 27.2: BidCos-RF and HmIP-RF on a dual-stack module are two reads
 * of one transmitter).
 */

/** one point of a series as the chart draws it */
export interface Sample {
    /** ISO time of the poll */
    t: string;
    /** the value, in the chart's unit (per cent of the budget here) */
    v: number;
    /** false when the interface did not answer at that poll: the line breaks there */
    up?: boolean;
}

/** a sample as /radio/health delivers it */
export interface HealthSample {
    t: string;
    dc: number;
    /** the carrier sense at that poll (task 151); absent when the interface reported none, which is not 0 % */
    cs?: number;
    up: boolean;
}

/**
 * The duty cycle levels, one constant for both pages (task 54): amber from 70 % of the 1 %
 * airtime budget, red from 90 %. The wording next to the ring is the page's, the thresholds
 * are not.
 */
export const DUTY_LEVELS = {warn: 70, err: 90} as const;

/**
 * The memory gauge's levels (occulited task 91): amber from 90 % in use - "raise ram warning
 * threshold to 90%" (maintainer, 2026-10-20; was 85) - red from 95 %. For the value and its peak.
 */
export const MEM_LEVELS = {warn: 90, err: 95} as const;

/**
 * Carrier sense (task 151): above 10 % the channel is busy enough to be bad - interference or a
 * neighbour's traffic - and the panel is red. The maintainer named one threshold, so there is no
 * amber band below it.
 */
export const CARRIER_BAD = 10;

/** red above CARRIER_BAD, ok up to it */
export function carrierLevel(pct: number): 'ok' | 'error' {
    return pct > CARRIER_BAD ? 'error' : 'ok';
}

/** ok until `warn` per cent, amber to `err`, red above it */
export function levelOf(pct: number, warn: number, err: number): 'ok' | 'warn' | 'error' {
    return pct >= err ? 'error' : pct >= warn ? 'warn' : 'ok';
}

/**
 * The per-poll maximum across the stacks of one radio. The sampler writes one row per interface
 * per poll, so the tails of the histories line up sample for sample; the shorter tail decides
 * how many samples there are. A stack that was down at a poll contributes nothing to the value,
 * and the merged sample is up when any stack was.
 */
export function mergeMax(hs: HealthSample[][], field: 'dc' | 'cs' = 'dc'): Sample[] {
    const lists = hs.filter((h) => h.length > 0);
    if (!lists.length) return [];
    const n = Math.min(...lists.map((h) => h.length));
    return Array.from({length: n}, (_, i) => {
        const at = lists.map((h) => h[h.length - n + i]!);
        // task 151: a stack that reported no carrier sense at that poll contributes nothing, and a
        // poll where none did is a gap, not a 0 %
        const vals = at.filter((s) => s.up).map((s) => s[field]).filter((v): v is number => v !== undefined);
        return {
            t: at[0]!.t,
            v: vals.length ? Math.max(...vals.map((v) => Math.max(0, v))) : 0,
            up: vals.length > 0,
        };
    });
}

/** the highest value of the samples that were up in the last `minutes` (task 152), or null when there is none */
export function spanMax(samples: Sample[], minutes: number): number | null {
    const vals = sliceSpan(samples, minutes).filter((s) => s.up !== false).map((s) => s.v);
    return vals.length ? Math.max(...vals) : null;
}

/** the readable steps a graph's top tick can be (per cent of the budget) */
export const STEPS = [1, 2, 5, 10, 20, 50, 100];

/**
 * The steps of an event rate's top tick (occulited task 13; per minute since task 17). A quiet house
 * delivers a few events a minute, and a poll a minute apart sees no less than one, so the steps
 * start at half an event per minute.
 */
export const RATE_STEPS = [0.5, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000];

/**
 * occulited B-79: below this many events a minute nothing is an outlier the rate graph's scale
 * leaves out (scalePeak) - a quiet hour's single burst of a dozen is drawn as it is.
 */
export const RATE_OUTLIER_FLOOR = 5;

/** the most of the band the peak may take: a fifth of it stays above the line */
const HEADROOM = 0.8;

/**
 * The value of the top tick: the graph's own peak rounded up to a readable step that leaves room
 * above it, so a quiet house is not a flat line pressed against the floor of the band, and the
 * tick label says what the top means.
 *
 * With room, not merely `>=` the peak (B-67): a steady 1 % got a 1 % top, the line ran along the
 * top edge and the wash under it filled the band - it read as a budget used up. The peak now takes
 * at most four fifths of the band (1 % tops out at 2 %, 4 % at 5 %, 4.1 % at 10 %).
 *
 * From 80 % of the budget to the budget itself the top is 100: a line near the top edge is the
 * truth there, and a higher tick would claim a share beyond the legal limit. A peak beyond 100
 * (which the budget does not allow, but a chart owes its data the truth) goes to the next hundred
 * with the same room.
 */
export function niceTop(peak: number, steps: number[] = STEPS): number {
    if (!Number.isFinite(peak) || peak <= 0) return steps[0]!;
    const step = steps.find((s) => peak <= s * HEADROOM);
    if (step !== undefined) return step;
    if (peak <= 100) return 100;
    return Math.ceil(peak / HEADROOM / 100) * 100;
}

/** occulited B-79: how far above the rest a value must be to count as an outlier the scale leaves out */
export const OUTLIER_FACTOR = 10;

/**
 * The peak a graph is scaled to when a lone outlier must not flatten it (occulited B-79: an event
 * rate of 500 a minute among values of 2 pressed the real line onto the baseline). Up to one value
 * in fifty - at least one - may lie above OUTLIER_FACTOR times the next value below them (or times
 * `floor`, whichever is higher); those are left out, and the peak is the highest of the rest. The
 * chart draws them clipped at its top. Below `floor` nothing is an outlier: a single event in a
 * quiet hour is drawn as it is.
 */
export function scalePeak(values: number[], floor: number): number {
    const vals = values.filter((v) => Number.isFinite(v)).sort((a, b) => b - a);
    if (vals.length < 2) return Math.max(0, vals[0] ?? 0);
    const k = Math.max(1, Math.floor(vals.length / 50));
    const base = Math.max(vals[Math.min(k, vals.length - 1)]!, floor);
    const limit = OUTLIER_FACTOR * base;
    return Math.max(0, vals.find((v) => v <= limit) ?? base);
}

/**
 * The samples of the last `minutes` before the newest sample, oldest first. The newest is the one
 * written last, whatever its time says: a system without a clock of its own can boot with a wrong
 * one and keep a few samples stamped years ahead or behind (occulited B-74: three from 2067 on a
 * Pi 4, kept across the reboot). Those fall out - a time after the last sample's or before the
 * span - and what is left is put in time order, so the chart's x stays between its two ends.
 */
export function sliceSpan(samples: Sample[], minutes: number): Sample[] {
    const last = samples[samples.length - 1];
    if (!last) return [];
    const to = Date.parse(last.t);
    const from = to - minutes * 60_000;
    return samples
        .map((s) => ({s, t: Date.parse(s.t)}))
        .filter(({t}) => t >= from && t <= to)
        .sort((a, b) => a.t - b.t)
        .map(({s}) => s);
}

/**
 * The runs the line is drawn in. A sample that was not `up` is left out and breaks the line,
 * and so does a hole in time - the box rebooted, the sampler was not running - which shows as
 * an interval much longer than the poll interval (2.5 × the median spacing). Drawing a ramp
 * across either would invent a duty cycle nobody measured.
 */
export function segments(samples: Sample[]): Sample[][] {
    const times = samples.map((s) => Date.parse(s.t));
    const gaps = times.slice(1).map((t, i) => t - times[i]!).filter((d) => d > 0).sort((a, b) => a - b);
    const median = gaps.length ? gaps[Math.floor(gaps.length / 2)]! : Infinity;
    const out: Sample[][] = [];
    let run: Sample[] = [];
    samples.forEach((s, i) => {
        const hole = i > 0 && times[i]! - times[i - 1]! > 2.5 * median;
        if (s.up === false || hole) {
            if (run.length) out.push(run);
            run = [];
        }
        if (s.up !== false) run.push(s);
    });
    if (run.length) out.push(run);
    return out;
}

/** `HH:MM` in the UI language, always on a 24-hour clock */
export function clock(d: Date, lang?: string): string {
    return d.toLocaleTimeString(lang, {hour: '2-digit', minute: '2-digit', hourCycle: 'h23'});
}

/**
 * The labels of the time axis: `HH:MM` at both ends, and the day in front of each when the span
 * crosses midnight, so "23:40 … 00:20" cannot read as twenty minutes the wrong way round.
 * `lang` is the UI language; the tests pass one so they do not depend on the machine's locale.
 * The clock is 24-hour in either language (`hourCycle: 'h23'`): "09:05 PM" does not fit under
 * a graph, and the box's own log writes 21:05.
 */
export function timeLabels(start: Date, end: Date, lang?: string): {start: string; end: string} {
    const time = (d: Date) => clock(d, lang);
    const sameDay = start.getFullYear() === end.getFullYear() && start.getMonth() === end.getMonth() && start.getDate() === end.getDate();
    if (sameDay) return {start: time(start), end: time(end)};
    const day = (d: Date) => d.toLocaleDateString(lang, {day: '2-digit', month: '2-digit'});
    return {start: `${day(start)} ${time(start)}`, end: `${day(end)} ${time(end)}`};
}

/** a rate sample as /radio/health delivers it (occulited task 13) */
export interface RateSample {
    t: string;
    /** the events per minute the interface process delivered (occulited task 17) */
    in: number;
    up: boolean;
}

/** a rate series as the chart draws it */
export function rateSeries(rs: RateSample[] | undefined): Sample[] {
    return (rs ?? []).map((r) => ({t: r.t, v: r.up ? r.in : 0, up: r.up}));
}

/**
 * A rate as the panel writes it: whole numbers as they are and from 10 on, one decimal from 1, two below - in the
 * UI language's decimal mark. 0 is "0".
 */
export function formatRate(v: number, lang?: string): string {
    const d = Number.isInteger(v) || v >= 10 ? 0 : v >= 1 ? 1 : 2;
    return v.toLocaleString(lang, {minimumFractionDigits: d, maximumFractionDigits: d});
}

/**
 * occulited task 37: the steps of the load graph's top tick. The load average has no budget; the
 * graph is read against the CPU count, which the Status page draws as the mark (the band always
 * reaches it), so a Pi 3 at 3.5 of 4 reads as busy and an x86 box at 3.5 of 8 does not.
 */
export const LOAD_STEPS = [0.5, 1, 2, 4, 8, 16, 32, 64];

/** a sample of GET /status/history (occulited task 37) */
export interface SystemSample {
    t: string;
    /** the 1-minute load average */
    load: number;
    /** the memory in use in per cent, -1 unknown */
    mem: number;
}

/** the load series as the chart draws it */
export function loadSeries(h: SystemSample[] | undefined): Sample[] {
    return (h ?? []).map((s) => ({t: s.t, v: s.load}));
}

/** the memory series as the chart draws it; an unknown reading breaks the line */
export function memSeries(h: SystemSample[] | undefined): Sample[] {
    return (h ?? []).map((s) => ({t: s.t, v: Math.max(0, s.mem), up: s.mem >= 0}));
}

/** a load as the page writes it: two places, in the UI language's decimal mark */
export function formatLoad(v: number, lang?: string): string {
    return v.toLocaleString(lang, {minimumFractionDigits: v < 10 ? 2 : 1, maximumFractionDigits: v < 10 ? 2 : 1});
}
