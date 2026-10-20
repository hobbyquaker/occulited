// occulited task 83: the spans the history charts offer, and the remembered one read back.

/** the spans a history chart offers, in minutes (HistoryChart's default `spans`) */
export const SPANS: readonly number[] = [60, 360, 1440];

/** the default span: 24 h (occulited task 81) */
export const DEFAULT_SPAN = 1440;

/** a stored span back as minutes; anything not offered is the default */
export function parseSpan(raw: string | null | undefined): number {
    const m = Number(raw);
    return raw && SPANS.includes(m) ? m : DEFAULT_SPAN;
}
