import {describe, expect, it} from 'vitest';
import {foreignSummary} from './foreign';

const t = (key: string, p?: Record<string, string | number>) => Object.entries(p ?? {}).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), key);

describe('foreignSummary (B-84)', () => {
    it('counts names, devices and channels, singular where one', () => {
        expect(foreignSummary({interface: 'CUxD', objects: 12, devices: 2, channels: 10}, t)).toBe('12 names (2 devices, 10 channels)');
        expect(foreignSummary({interface: 'CUxD', objects: 2, devices: 1, channels: 1}, t)).toBe('2 names (1 device, 1 channel)');
    });
    it('leaves out a part that is 0', () => {
        expect(foreignSummary({interface: 'HmIP-RF', objects: 1, devices: 0, channels: 1}, t)).toBe('1 name (1 channel)');
        expect(foreignSummary({interface: 'X', objects: 0, devices: 0, channels: 0}, t)).toBe('0 names');
    });
});
