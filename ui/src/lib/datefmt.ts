// Dates and times in the page's language (occulited B-133). A bare toLocaleString() follows the
// browser's locale, so a German page in an English browser said "11/1/2026, 12:18:32 AM" inside a
// German sentence. Every date the UI shows goes through these three: German as de-DE, English as
// en-GB (day before month, 24 hours), the same pair the timers, the log and the boot timeline use.
// The network page's clock comparison keeps its own clockLocale (liveclock.ts): it takes the
// browser's regional variant of the page language on purpose.
import {i18n} from './i18n.svelte';
import {dateLocale as localeOf} from './datelocale';

/** The locale for the page language (or for lang). */
export function dateLocale(lang: string = i18n.language): string {
    return localeOf(lang);
}

export type Stamp = string | number | Date | null | undefined;

function toDate(v: Stamp): Date | null {
    if (v === null || v === undefined || v === '') return null;
    const d = v instanceof Date ? v : new Date(v);
    return Number.isNaN(d.getTime()) ? null : d;
}

// what an unreadable stamp shows: the text as it came, nothing for anything else
const raw = (v: Stamp): string => (typeof v === 'string' ? v : '');

/** Date and time: 11.11.2026, 19:16:34 / 11/11/2026, 19:16:34. */
export function dateTime(v: Stamp, opts?: Intl.DateTimeFormatOptions): string {
    const d = toDate(v);
    return d ? d.toLocaleString(dateLocale(), opts) : raw(v);
}

/** The date alone: 11.11.2026 / 11/11/2026. */
export function dateOnly(v: Stamp, opts?: Intl.DateTimeFormatOptions): string {
    const d = toDate(v);
    return d ? d.toLocaleDateString(dateLocale(), opts) : raw(v);
}

/** The time alone: 19:16:34 (both languages, 24 hours). */
export function timeOnly(v: Stamp, opts?: Intl.DateTimeFormatOptions): string {
    const d = toDate(v);
    return d ? d.toLocaleTimeString(dateLocale(), opts) : raw(v);
}
