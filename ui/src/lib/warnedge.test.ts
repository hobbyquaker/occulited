import {describe, expect, it} from 'vitest';
import {ADDON_CARD_WARNINGS, edgeOf, names, worse} from './warnedge';
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

describe('the available system update (occulited task 123)', () => {
    it('paints the Updates page panel while active, and nothing once gone', () => {
        const list: Warning[] = [{id: 'system-update-available', variant: '1.0.1', severity: 'warning'}];
        expect(edgeOf(list, ['system-update-available'])).toBe('warn');
        expect(edgeOf([], ['system-update-available'])).toBe('');
    });
});

describe('an addon card with an available update (occulited task 129)', () => {
    const update: Warning = {id: 'addon-update', variant: 'mosquitto@2.1.2,redmatic@9.7.4', severity: 'warning',
        params: {count: 2, addons: [{id: 'mosquitto', name: 'Mosquitto', installed: '2.1.0', available: '2.1.2'}, {id: 'redmatic', name: 'RedMatic', installed: '9.4.0', available: '9.7.4'}]}};
    it('turns yellow for the addons the warning names, and only for them', () => {
        expect(edgeOf([update], ADDON_CARD_WARNINGS, 'mosquitto')).toBe('warn');
        expect(edgeOf([update], ADDON_CARD_WARNINGS, 'redmatic')).toBe('warn');
        expect(edgeOf([update], ADDON_CARD_WARNINGS, 'hm2mqtt')).toBe('');
        // a version in the variant is no addon
        expect(edgeOf([update], ADDON_CARD_WARNINGS, '2.1.2')).toBe('');
    });
    it('is red when the same addon also failed', () => {
        const failed: Warning = {id: 'addon-failed', variant: 'mosquitto', severity: 'error', params: {addons: [{id: 'mosquitto'}]}};
        expect(edgeOf([update, failed], ADDON_CARD_WARNINGS, 'mosquitto')).toBe('err');
        expect(edgeOf([update, failed], ADDON_CARD_WARNINGS, 'redmatic')).toBe('warn');
    });
    it('is gone once the update is installed (the warning clears)', () => {
        expect(edgeOf([], ADDON_CARD_WARNINGS, 'mosquitto')).toBe('');
    });
});
