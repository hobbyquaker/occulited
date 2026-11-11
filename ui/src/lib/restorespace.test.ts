import {describe, expect, it} from 'vitest';
import {uploadRefusal as refusal, type RestoreSpace} from './restorespace';

// the English text with its parameters filled in, as the page's t() does for English
const t = (key: string, params?: Record<string, string | number>) => key.replace(/\{(\w+)\}/g, (_, k) => String(params?.[k] ?? ''));
const uploadRefusal = (size: number, space: RestoreSpace, lang: string) => refusal(size, space, lang, t);

// occulited B-141: the page refuses a backup that is too large or has no room before it uploads it
describe('uploadRefusal', () => {
    const space = {max_bytes: 16 * 2 ** 30, reserve_bytes: 128 * 2 ** 20, free_bytes: 6e9};
    it('lets a backup through that fits twice with the reserve left', () => {
        expect(uploadRefusal(2.9e9, space, 'en')).toBe('');
    });
    it('names the sizes when the room is missing: the file twice, for the upload and the check', () => {
        const m = uploadRefusal(3e9, space, 'en');
        expect(m).toContain('Not enough free space');
        expect(m).toContain('it needs 6.1 GB (twice the 3.0 GB file');
        expect(m).toContain('6.0 GB are free');
        expect(m).toContain('134 MB that stay free');
    });
    it('refuses one over the cap', () => {
        expect(uploadRefusal(20 * 2 ** 30, {...space, free_bytes: 1e12}, 'en')).toContain('larger than the 17 GB an upload may have');
    });
    it('does not guess when the free space is unknown', () => {
        expect(uploadRefusal(6e9, {...space, free_bytes: null}, 'en')).toBe('');
    });
});
