import {describe, expect, it} from 'vitest';
import {edgeOf, names, worse} from './warnedge';
import type {Warning} from './warnings';

const w = (id: string, severity: 'error' | 'warning', variant = '', params: Warning['params'] = {}): Warning => ({id, variant, severity, params});

describe('the edge of a warning panel (occulited task 12)', () => {
    it('is the worst active warning among the panel ids', () => {
        const list = [w('backup-unencrypted', 'warning'), w('backup-target', 'error', '/media/usb1')];
        expect(edgeOf(list, ['backup-unencrypted'])).toBe('warn');
        expect(edgeOf(list, ['backup-unencrypted', 'backup-target'])).toBe('err');
        expect(edgeOf(list, ['certificate'])).toBe('');
        expect(edgeOf([], ['certificate'])).toBe('');
    });
    it('matches the thing a warning names: an addon, a unit, a part of the variant', () => {
        const list = [w('addon-failed', 'error', 'hmm', {addons: [{id: 'hmm'}]}), w('addon-update', 'warning', 'redmatic,mosquitto'),
            w('crash-loop', 'error', 'rfd', {units: [{unit: 'rfd.service', restarts: 5, total: 5, since: ''}]}), w('backup-delivery', 'warning', 'nas:too-old')];
        expect(edgeOf(list, ['addon-failed', 'addon-update'], 'hmm')).toBe('err');
        expect(edgeOf(list, ['addon-failed', 'addon-update'], 'mosquitto')).toBe('warn');
        expect(edgeOf(list, ['addon-failed', 'addon-update'], 'hm2mqtt')).toBe('');
        expect(edgeOf(list, ['crash-loop'], 'rfd')).toBe('err');
        expect(edgeOf(list, ['backup-delivery'], 'nas')).toBe('warn');
        expect(names(list[3]!, 'too')).toBe(false);
    });
    it('keeps the worse of two edges', () => {
        expect(worse('', 'warn')).toBe('warn');
        expect(worse('err', 'warn')).toBe('err');
        expect(worse('warn', '')).toBe('warn');
        expect(worse('', '')).toBe('');
    });
});

describe('a notice (openccu-lite task 366)', () => {
    it('paints no edge', () => {
        const list: Warning[] = [{id: 'bootloader-update', variant: '2026-11-20', severity: 'info'}];
        expect(edgeOf(list, ['bootloader-update'])).toBe('');
    });
});
