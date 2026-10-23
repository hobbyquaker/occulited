import {afterEach, describe, expect, it, vi} from 'vitest';
import {grow} from './grow';

describe('the grow transition (B-109)', () => {
    afterEach(() => vi.unstubAllGlobals());
    it('animates the box only, never the opacity', () => {
        const style: Record<string, string> = {height: '100px', 'padding-top': '10px', 'margin-top': '12px'};
        vi.stubGlobal('getComputedStyle', () => ({getPropertyValue: (k: string) => style[k] ?? '0px'}));
        const c = grow({} as Element, {duration: 200});
        expect(c.duration).toBe(200);
        const mid = c.css!(0.5, 0.5);
        expect(mid).toContain('overflow: hidden');
        expect(mid).toContain('height: 50px');
        expect(mid).toContain('padding-top: 5px');
        expect(mid).toContain('margin-top: 6px');
        for (const t of [0, 0.05, 0.5, 1]) expect(c.css!(t, 1 - t)).not.toMatch(/opacity|transform|filter/);
    });
});
