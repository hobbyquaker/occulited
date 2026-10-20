import {describe, expect, it} from 'vitest';
import {cleanNames, realName, shownName, withName} from './displaynames';

describe('display names (task 73)', () => {
    it('keeps only non-blank string names', () => {
        expect(cleanNames(null)).toEqual({});
        expect(cleanNames([])).toEqual({});
        expect(cleanNames({a: ' Lampe ', b: '', c: 3, d: '  '})).toEqual({a: 'Lampe'});
    });
    it('shows the display name, else the metadata name, else the fallback', () => {
        const n = {'HmIP-RF.X:1': 'Leselampe'};
        expect(shownName(n, 'HmIP-RF.X:1', 'Schalter Wohnzimmer', 'X:1')).toBe('Leselampe');
        expect(shownName(n, 'HmIP-RF.Y:1', 'Schalter Flur', 'Y:1')).toBe('Schalter Flur');
        expect(shownName(n, 'HmIP-RF.Y:1', '', 'Y:1')).toBe('Y:1');
    });
    it('names the real name only behind an override that differs', () => {
        const n = {a: 'Leselampe', b: 'Flur'};
        expect(realName(n, 'a', 'Schalter Wohnzimmer', 'a')).toBe('Schalter Wohnzimmer');
        expect(realName(n, 'b', 'Flur', 'b')).toBe('');
        expect(realName(n, 'c', 'Küche', 'c')).toBe('');
        expect(realName(n, 'a', '', 'X:1')).toBe('X:1');
    });
    it('sets and clears one name without touching the others', () => {
        const n = {a: 'A'};
        expect(withName(n, 'b', ' B ')).toEqual({a: 'A', b: 'B'});
        expect(withName(n, 'a', '  ')).toEqual({});
        expect(n).toEqual({a: 'A'});
    });
});
