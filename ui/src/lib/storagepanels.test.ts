import {describe, expect, it} from 'vitest';
import {deviceLevel, deviceModel, formatDecimal, ringColours, unallocatedShown, usedOfTotal, type StatusDevice} from './storagepanels';

describe('decimal units (task 78)', () => {
    it.each([
        [16e9, 'en', '16 GB'],
        [15931539456, 'en', '16 GB'],
        [268435456, 'en', '268 MB'],
        [2147483648, 'en', '2.1 GB'],
        [2147483648, 'de', '2,1 GB'],
        [64e9, 'de', '64 GB'],
        [2e12, 'en', '2.0 TB'],
        [0, 'en', '0 MB'],
    ])('%d (%s) → %s', (n, lang, want) => {
        expect(formatDecimal(n, lang)).toBe(want);
    });
    it('used in the total\'s unit', () => {
        expect(usedOfTotal(68e6, 267e6, 'en')).toEqual(['68', '267', 'MB']);
        expect(usedOfTotal(500e6, 13e9, 'de')).toEqual(['0,5', '13', 'GB']);
        expect(usedOfTotal(1.61e9, 2.04e9, 'de')).toEqual(['1,6', '2,0', 'GB']);
    });
});

const dev = (parts: Partial<StatusDevice['partitions'][number]>[], extra: Partial<StatusDevice> = {}): StatusDevice => ({
    name: 'x', kind: 'usb', size_bytes: 16e9, unallocated_bytes: 0,
    partitions: parts.map((p, i) => ({name: `p${i}`, mount: `/m${i}`, role: 'other', size_bytes: 1e9, total_bytes: 100, used_bytes: 10, ...p})),
    ...extra,
});

describe('the panel', () => {
    it('names a stick by vendor and model, once', () => {
        expect(deviceModel({vendor: 'SanDisk', model: 'Ultra Fit'})).toBe('SanDisk Ultra Fit');
        expect(deviceModel({vendor: 'Samsung', model: 'Samsung PSSD T7'})).toBe('Samsung PSSD T7');
        expect(deviceModel({model: 'Rainbow Line'})).toBe('Rainbow Line');
        expect(deviceModel({})).toBe('');
    });
    it('keeps a hue per role and gives a stick the free ones', () => {
        expect(ringColours(dev([{role: 'userfs'}, {role: 'root'}, {role: 'boot'}]))).toEqual(['var(--ol-series-1)', 'var(--ol-series-2)', 'var(--ol-series-3)']);
        expect(ringColours(dev([{role: 'root'}]))).toEqual(['var(--ol-series-2)']);
        expect(ringColours(dev([{}, {}]))).toEqual(['var(--ol-series-1)', 'var(--ol-series-2)']);
        expect(ringColours(dev([{role: 'userfs'}, {}]))).toEqual(['var(--ol-series-1)', 'var(--ol-series-2)']);
    });
    it('warns for a writable partition only', () => {
        expect(deviceLevel(dev([{used_bytes: 95, read_only: true}]))).toBe('ok');
        expect(deviceLevel(dev([{used_bytes: 85}]))).toBe('warn');
        expect(deviceLevel(dev([{used_bytes: 85}, {used_bytes: 93}]))).toBe('error');
    });
    it('says unallocated space when it is worth it', () => {
        expect(unallocatedShown(dev([], {unallocated_bytes: 32e6}))).toBe(false);
        expect(unallocatedShown(dev([], {unallocated_bytes: 4e9}))).toBe(true);
        expect(unallocatedShown(dev([], {size_bytes: 1e12, unallocated_bytes: 2e9}))).toBe(false);
    });
});
