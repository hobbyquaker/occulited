import {describe, expect, it} from 'vitest';
import {dateLocale} from './datelocale';
import {runTimeFull} from './timers';

// occulited B-133: a date follows the page's language, never the browser's locale.
describe('dateLocale', () => {
    const at = new Date(2026, 10, 1, 0, 18, 32);
    const cases: [string, string, RegExp][] = [
        ['de', 'de-DE', /^1\.11\.2026, 00:18:32$/],
        ['en', 'en-GB', /^01\/11\/2026, 00:18:32$/],
        ['fr', 'en-GB', /^01\/11\/2026, 00:18:32$/], // anything else is the English page
    ];
    for (const [lang, locale, shape] of cases) {
        it(`${lang} is ${locale}`, () => {
            expect(dateLocale(lang)).toBe(locale);
            expect(at.toLocaleString(dateLocale(lang))).toMatch(shape);
        });
    }
    it('the pure formatters use it', () => {
        expect(runTimeFull('2026-11-01T00:18:32', 'de')).toMatch(/^01\.11\.2026, 00:18:32$/);
        expect(runTimeFull('2026-11-01T00:18:32', 'en')).toMatch(/^1 Nov 2026, 00:18:32$/);
    });
});
