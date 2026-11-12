// The locale for a page language (occulited B-133): German de-DE, English en-GB (day before month,
// 24 hours). Kept apart from datefmt.ts so the pure formatting modules need no i18n state.
export function dateLocale(lang: string): string {
    return lang === 'de' ? 'de-DE' : 'en-GB';
}
