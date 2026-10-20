import {describe, expect, it} from 'vitest';
import {DEFAULT_SPAN, parseSpan} from './viewspan';

describe('parseSpan (task 83)', () => {
    it.each([
        [null, DEFAULT_SPAN],
        ['', DEFAULT_SPAN],
        ['60', 60],
        ['360', 360],
        ['1440', 1440],
        ['120', DEFAULT_SPAN],
        ['abc', DEFAULT_SPAN],
        ['0', DEFAULT_SPAN],
    ])('%s → %d', (raw, want) => {
        expect(parseSpan(raw)).toBe(want);
    });
});
