import {describe, expect, it} from 'vitest';
import {fitSize} from './fit';

// occulited B-100: the font steps of a line that does not fit
describe('fitSize', () => {
    // a text that is 10 px wide per px of font in a 150 px line: fits up to 15 px
    const fits = (s: number) => s * 10 <= 150;
    it('keeps the size when the text fits', () => {
        expect(fitSize(12, 10, () => true)).toBe(12);
    });
    it('finds the largest 0.5 px step that fits, from any guess', () => {
        expect(fitSize(19, 13, fits, 15.3)).toBe(15);
        expect(fitSize(19, 13, fits, 13)).toBe(15);
        expect(fitSize(19, 13, fits, 18)).toBe(15);
        expect(fitSize(19, 13, fits)).toBe(15);
    });
    it('stops at the minimum when nothing fits', () => {
        expect(fitSize(19, 13, () => false, 5)).toBe(13);
    });
    it('half-pixel steps', () => {
        expect(fitSize(19, 10, (s) => s <= 16.5, 16.9)).toBe(16.5);
    });
});
