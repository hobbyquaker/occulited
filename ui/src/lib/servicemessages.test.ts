import {describe, expect, it} from 'vitest';
import {counts, kindOf, rows} from './servicemessages';
import type {ServiceMessage} from './api';

const m = (address: string, key: string, since: string, extra: Partial<ServiceMessage> = {}): ServiceMessage => ({interface: 'HmIP-RF', address, channel: '0', key, value: true, since, seen: 'event', ...extra});

describe('service messages (task 27)', () => {
    it('maps the datapoints to kinds', () => {
        expect(kindOf('UNREACH')).toBe('unreach');
        expect(kindOf('STICKY_UNREACH')).toBe('sticky');
        expect(kindOf('LOW_BAT')).toBe('lowbat');
        expect(kindOf('LOWBAT')).toBe('lowbat');
        expect(kindOf('CONFIG_PENDING')).toBe('config');
        expect(kindOf('UPDATE_PENDING')).toBe('update');
        expect(kindOf('SABOTAGE')).toBe('fault');
        expect(kindOf('ERROR_OVERHEAT')).toBe('fault');
        expect(kindOf('DUTY_CYCLE')).toBe('fault');
        expect(kindOf('SOMETHING_ELSE')).toBe('other');
    });

    it('one row per device, sticky folded into unreach, severity then newest first', () => {
        const list = [
            m('A', 'LOW_BAT', '2026-10-10T10:00:00Z', {name: 'Alpha'}),
            m('B', 'STICKY_UNREACH', '2026-10-10T09:00:00Z'),
            m('B', 'UNREACH', '2026-10-10T09:00:00Z'),
            m('C', 'CONFIG_PENDING', '2026-10-10T12:00:00Z'),
            m('D', 'STICKY_UNREACH', '2026-10-10T13:00:00Z'),
            m('E', 'UNREACH', '2026-10-10T11:00:00Z'),
            m('E', 'LOW_BAT', '2026-10-10T08:00:00Z'),
        ];
        const r = rows(list);
        expect(r.map((x) => x.address)).toEqual(['E', 'B', 'A', 'C', 'D']);
        expect(r.find((x) => x.address === 'B')!.kinds).toEqual(['unreach']);
        expect(r.find((x) => x.address === 'E')!.kinds).toEqual(['unreach', 'lowbat']);
        expect(r.find((x) => x.address === 'D')!.kinds).toEqual(['sticky']);
        expect(r.find((x) => x.address === 'A')!.name).toBe('Alpha');
        const c = counts(r);
        expect(c.get('unreach')).toBe(2);
        expect(c.get('lowbat')).toBe(2);
        expect(c.get('sticky')).toBe(1);
        expect(c.get('fault')).toBeUndefined();
    });
});
