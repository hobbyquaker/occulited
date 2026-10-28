import {describe, expect, it} from 'vitest';
import {embedSrc, findView, parseViewRoute, shownGroups, viewHref, words, type AppViewGroup} from './appviews';

const history: AppViewGroup = {addon: 'history', name: {de: 'History', en: 'History'}, views: [{id: 'lab1', title: {de: 'Labor', en: 'Lab'}, path: '/addons/history/embed/dashboard/lab1', icon: 'chart'}]};
const empty: AppViewGroup = {addon: 'zeta', name: {de: 'Z', en: 'Z'}, views: []};

describe('app views (occulited task 125)', () => {
    it('leaves hidden addons and empty groups out', () => {
        expect(shownGroups([history, empty], []).map((g) => g.addon)).toEqual(['history']);
        expect(shownGroups([history], ['history'])).toEqual([]);
    });
    it('picks the language', () => {
        expect(words({de: 'Labor', en: 'Lab'}, 'de')).toBe('Labor');
        expect(words({de: 'Labor', en: 'Lab'}, 'fr')).toBe('Lab');
        expect(words(undefined, 'de', 'x')).toBe('x');
    });
    it('round-trips the deep link', () => {
        const href = viewHref('history', 'a b/c');
        expect(href).toBe('/app/view/history/a%20b%2Fc');
        expect(parseViewRoute(href.slice(5))).toEqual({addon: 'history', id: 'a b/c'});
        expect(parseViewRoute('view/history')).toBeNull();
        expect(parseViewRoute('e/room/x')).toBeNull();
        expect(parseViewRoute('view/%E0%A4%A/x')).toBeNull();
    });
    it('frames only a page under /addons/, with embed, theme and language', () => {
        expect(embedSrc('/addons/history/embed/dashboard/lab1', 'dark', 'de')).toBe('/addons/history/embed/dashboard/lab1?embed=app&theme=dark&lang=de');
        expect(embedSrc('https://evil.example/', 'dark', 'de')).toBe('about:blank');
        expect(embedSrc('javascript:alert(1)', 'dark', 'de')).toBe('about:blank');
    });
    it('finds a view', () => {
        expect(findView([history], 'history', 'lab1')?.view.title.en).toBe('Lab');
        expect(findView([history], 'history', 'nope')).toBeUndefined();
    });
});
