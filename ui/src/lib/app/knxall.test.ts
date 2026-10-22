import {afterEach, describe, expect, it, vi} from 'vitest';
import {gunzipSync, gzipSync} from 'node:zlib';
import {readFileSync} from 'node:fs';
import {ALL_ICONS_URL, ALL_NAMES, CATEGORIES, categoryOf, fold, groupByCategory, humanName, isKnxName, loadAllIcons, searchIcons} from './knxall';
import {SWITCH_ICONS} from './stateicons';

describe('the whole knx-uf set for the icon picker (task 102)', () => {
    it('ships every name of the set, sorted and unique, every suggested icon among them', () => {
        expect(ALL_NAMES.length).toBeGreaterThan(900);
        expect([...ALL_NAMES]).toEqual([...new Set(ALL_NAMES)].sort());
        for (const id of SWITCH_ICONS) expect(isKnxName(id), id).toBe(true);
        expect(isKnxName('scene_aquarium')).toBe(true);
        expect(isKnxName('not_an_icon')).toBe(false);
        expect(isKnxName(undefined)).toBe(false);
    });
    it('knows every category of the set by name', () => {
        const cats = new Set(ALL_NAMES.map(categoryOf));
        for (const c of cats) expect(CATEGORIES.some((x) => x.id === c), c).toBe(true);
        expect(categoryOf('light_ceiling_light')).toBe('light');
        expect(humanName('light_ceiling_light')).toBe('ceiling light');
        expect(humanName('fts_shutter_50')).toBe('shutter 50');
    });
    it('groups by category in the picker order, lights first', () => {
        const g = groupByCategory(['weather_rain', 'light_wall_1', 'light_bedside', 'sani_pump']);
        expect(g.map((x) => x.id)).toEqual(['light', 'sani', 'weather']);
        expect(g[0]!.names).toEqual(['light_wall_1', 'light_bedside']);
    });
    it('folds case, umlauts and their transliterations', () => {
        expect(fold('Lüfter')).toBe('lufter');
        expect(fold('Luefter')).toBe('lufter');
        expect(fold('STRAẞE')).toBe('strasse');
        expect(fold('Küche')).toBe(fold('Kueche'));
    });
    it('searches the names, the labels and the German and English category words', () => {
        const names = ['light_ceiling_light', 'message_socket', 'vent_ventilation', 'sani_pump', 'scene_aquarium', 'weather_rain'];
        const labels: Record<string, string> = {message_socket: 'Steckdose', vent_ventilation: 'Ventilator'};
        const by = (q: string) => searchIcons(q, names, (n) => labels[n] ?? '');
        expect(by('')).toEqual(names);
        expect(by('Lampe')).toEqual(['light_ceiling_light']); // a German category word
        expect(by('socket')).toEqual(['message_socket']); // the name
        expect(by('steckdose')).toEqual(['message_socket']); // the label
        expect(by('Lüfter')).toEqual(['vent_ventilation']); // a German category word, the umlaut folded
        expect(by('luefter')).toEqual(['vent_ventilation']);
        expect(by('fan')).toEqual(['vent_ventilation']);
        expect(by('aquarium')).toEqual(['scene_aquarium']);
        expect(by('ceiling light')).toEqual(['light_ceiling_light']); // every word must match
        expect(by('ceiling rain')).toEqual([]);
        expect(by('wasser')).toEqual(['sani_pump']);
        expect(by('xyzzy')).toEqual([]);
    });
});

describe('the lazily loaded drawings (task 102)', () => {
    afterEach(() => vi.unstubAllGlobals());
    it('is a separate gzipped asset holding every name, inflated in the browser', async () => {
        // the asset itself, as the build ships it
        const gz = readFileSync(new URL('./knx-all/icons.json.gz', import.meta.url));
        const all = JSON.parse(gunzipSync(gz).toString('utf8')) as Record<string, string>;
        expect(Object.keys(all).sort()).toEqual([...ALL_NAMES]);
        for (const [n, svg] of Object.entries(all)) {
            expect(svg.startsWith('<svg'), n).toBe(true);
            expect(svg, n).toContain('currentColor');
            expect(svg, n).not.toMatch(/\sid=|#fff/i);
        }
        expect(ALL_ICONS_URL).toMatch(/icons\.json\.gz/);
        // one fetch for every caller, inflated with DecompressionStream
        const small = gzipSync(JSON.stringify({a: '<svg/>'}));
        const fetchMock = vi.fn(async (_url: string) => new Response(small, {status: 200}));
        vi.stubGlobal('fetch', fetchMock);
        const [x, y] = await Promise.all([loadAllIcons(), loadAllIcons()]);
        expect(x).toEqual({a: '<svg/>'});
        expect(y).toBe(x);
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(String(fetchMock.mock.calls[0]![0])).toBe(ALL_ICONS_URL);
    });
});
