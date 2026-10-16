import {describe, expect, it} from 'vitest';
import {allOn, confirmable, counts, kindOf, messageLook, panelEdge, places, rows, toggleAll, toggleKind, visibleRows} from './servicemessages';
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
    it('names the rooms by the names the system sends, by the last id without one (occulited B-66)', () => {
        expect(places(m('A', 'LOW_BAT', '2026-10-01T00:00:00Z', {enums: ['room/roomgarage', 'function/gone'], enum_names: ['Garage', '']}))).toEqual(['Garage', 'gone']);
        expect(places(m('A', 'LOW_BAT', '2026-10-01T00:00:00Z', {enums: ['room/eg/flur']}))).toEqual(['flur']);
        expect(places(m('A', 'LOW_BAT', '2026-10-01T00:00:00Z'))).toEqual([]);
        expect(rows([m('A', 'LOW_BAT', '2026-10-01T00:00:00Z', {enums: ['room/roomgarage'], enum_names: ['Garage']})])[0]!.places).toEqual(['Garage']);
    });
    it('offers to confirm a sticky message only when its cause is gone (openccu-lite task 82)', () => {
        const t0 = '2026-10-01T00:00:00Z';
        const sticky = m('A', 'STICKY_UNREACH', t0);
        expect(confirmable(sticky, [sticky])).toBe(true);
        expect(confirmable(sticky, [sticky, m('A', 'UNREACH', t0)])).toBe(false);
        const sab = m('A', 'STICKY_SABOTAGE', t0);
        expect(confirmable(sab, [sab, m('A', 'SABOTAGE', t0)])).toBe(false);
        expect(confirmable(sab, [sab, m('A', 'LOW_BAT', t0)])).toBe(true);
        for (const k of ['LOW_BAT', 'UNREACH', 'CONFIG_PENDING']) expect(confirmable(m('A', k, t0), [])).toBe(false);
        expect(confirmable({...sticky, value: false}, [])).toBe(false);
    });
    it('turns pills on and off, several at once, and all of them (occulited task 39)', () => {
        expect(toggleKind([], 'lowbat')).toEqual(['lowbat']);
        expect(toggleKind(['lowbat'], 'unreach')).toEqual(['unreach', 'lowbat']); // in the pills' order
        expect(toggleKind(['unreach', 'lowbat'], 'lowbat')).toEqual(['unreach']);
        expect(toggleKind(['unreach'], 'unreach')).toEqual([]); // the last one off: collapsed
        const present = ['unreach', 'config'] as const;
        expect(allOn([], [...present])).toBe(false);
        expect(allOn(['config', 'unreach'], [...present])).toBe(true);
        expect(allOn([], [])).toBe(false);
        expect(toggleAll(['config'], [...present])).toEqual(['unreach', 'config']);
        expect(toggleAll(['unreach', 'config'], [...present])).toEqual([]);
    });
    it('shows the devices of the kinds that are on, none while none is', () => {
        const t0 = '2026-10-01T00:00:00Z';
        const list = rows([m('A', 'LOW_BAT', t0), m('B', 'UNREACH', t0), m('B', 'CONFIG_PENDING', t0), m('C', 'CONFIG_PENDING', t0, {image: '/config/img/devices/50/c.png'})]);
        expect(visibleRows(list, []).map((r) => r.address)).toEqual([]);
        expect(visibleRows(list, ['config']).map((r) => r.address).sort()).toEqual(['B', 'C']);
        expect(visibleRows(list, ['lowbat', 'unreach']).map((r) => r.address).sort()).toEqual(['A', 'B']);
        expect(list.find((r) => r.address === 'C')!.image).toBe('/config/img/devices/50/c.png');
        expect(list.find((r) => r.address === 'A')!.image).toBeUndefined();
    });
    it('leaves the favourites out of the places', () => {
        expect(places(m('A', 'LOW_BAT', '2026-10-01T00:00:00Z', {enums: ['favorite/0badf00d', 'room/garage', 'function/light'], enum_names: ['Me', 'Garage', 'Licht']}))).toEqual(['Garage', 'Licht']);
    });
});

// occulited task 43: one icon per message, as homematic-manager's device table (its task 92)
describe('the look of a message', () => {
    it('names an icon and the kind\'s colour per datapoint', () => {
        const look = (k: string) => Object.values(messageLook(k)).join(' ');
        expect(look('UNREACH')).toBe('unlink red');
        expect(look('STICKY_UNREACH')).toBe('history grey');
        expect(look('LOWBAT')).toBe('battery orange');
        expect(look('LOW_BAT')).toBe('battery orange');
        expect(look('CONFIG_PENDING')).toBe('settings blue');
        expect(look('UPDATE_PENDING')).toBe('arrow-up blue');
        expect(look('ERROR')).toBe('alert red');
        expect(look('ERROR_CODE')).toBe('alert red');
        expect(look('ERROR_OVERHEAT')).toBe('alert red');
        expect(look('SABOTAGE')).toBe('shield red');
        expect(look('FAULT_REPORTING')).toBe('x-circle red');
        expect(look('DUTY_CYCLE')).toBe('activity red');
        expect(look('SOMETHING_ELSE')).toBe('alert orange');
    });
});

describe('the panel edge (occulited task 53)', () => {
    const at = '2026-10-15T10:00:00Z';
    const edge = (...keys: string[]) => panelEdge(rows(keys.map((k, i) => m(`A${i}`, k, at))));
    it('takes the most severe kind present', () => {
        expect(edge('CONFIG_PENDING', 'LOW_BAT', 'UNREACH')).toBe('err');
        expect(edge('UPDATE_PENDING', 'SABOTAGE')).toBe('err');
        expect(edge('CONFIG_PENDING', 'LOW_BAT')).toBe('warn');
        expect(edge('SOMETHING_ELSE')).toBe('warn');
        expect(edge('UPDATE_PENDING', 'CONFIG_PENDING', 'STICKY_UNREACH')).toBe('info');
    });
    it('gives no edge to a grey message alone or to none', () => {
        expect(edge('STICKY_UNREACH')).toBe('');
        expect(edge()).toBe('');
    });
    it('counts every device, whatever pill is on', () => {
        expect(panelEdge(rows([m('A', 'LOW_BAT', at), m('B', 'CONFIG_PENDING', at), m('A', 'UNREACH', at)]))).toBe('err');
    });
});
