import {describe, expect, it} from 'vitest';
import {DETECTION_UNIT, anyStarting, detecting, processOf, startingSeconds, unitOf, type InterfaceUnit} from './starting';

// task 94: the radio stack's units as /radio/health reports them during a boot
const units: InterfaceUnit[] = [
    {unit: DETECTION_UNIT, interfaces: [], active_state: 'active', sub_state: 'exited'},
    {unit: 'multimacd', interfaces: [], active_state: 'active', sub_state: 'running'},
    {unit: 'rfd', interfaces: ['BidCos-RF'], active_state: 'inactive', sub_state: 'dead', starting: true, queued: true},
    {unit: 'hmipserver', interfaces: ['HmIP-RF', 'VirtualDevices'], active_state: 'activating', sub_state: 'start', starting: true, starting_s: 23},
];

describe('the radio stack while it starts', () => {
    it('finds the unit behind an interface name', () => {
        expect(unitOf(units, 'VirtualDevices')?.unit).toBe('hmipserver');
        expect(unitOf(units, 'BidCos-RF')?.unit).toBe('rfd');
        expect(unitOf(units, 'BidCos-Wired')).toBeUndefined();
        expect(unitOf(undefined, 'HmIP-RF')).toBeUndefined();
    });
    it('says whether anything starts, and whether the detection still runs', () => {
        expect(anyStarting(units)).toBe(true);
        expect(anyStarting(units.slice(0, 2))).toBe(false);
        expect(anyStarting(undefined)).toBe(false);
        expect(detecting(units)).toBe(false);
        expect(detecting([{unit: DETECTION_UNIT, interfaces: [], active_state: 'activating', sub_state: 'start', starting: true}])).toBe(true);
        expect(detecting([{unit: DETECTION_UNIT, interfaces: [], active_state: 'inactive', sub_state: 'dead', starting: true, queued: true}])).toBe(true);
    });
    it.each<[string, {starting?: boolean; queued?: boolean; starting_s?: number}, number, number | undefined]>([
        ['the figure at the answer', {starting: true, starting_s: 23}, 0, 23],
        ['plus the time since the answer', {starting: true, starting_s: 23}, 4_900, 27],
        ['a start that has just begun (starting_s omitted at 0)', {starting: true}, 2_000, 2],
        ['a queued unit has not begun', {starting: true, queued: true}, 5_000, undefined],
        ['not starting', {starting: false, starting_s: 9}, 0, undefined],
        // the browser's clock going backwards between the answer and now does not count backwards
        ['a clock that went back', {starting: true, starting_s: 23}, -3_000, 23],
    ])('%s', (_, s, sinceFetch, want) => {
        const fetched = 1_789_000_000_000;
        expect(startingSeconds(s, fetched, fetched + sinceFetch)).toBe(want);
    });
});

describe('processOf (B-93)', () => {
    const known: InterfaceUnit[] = [{unit: 'rfd', interfaces: ['BidCos-RF'], active_state: 'active', sub_state: 'running'}];
    it('names the expected process while the units are unknown', () => {
        expect(processOf(undefined, 'BidCos-RF')).toBe('rfd');
        expect(processOf(undefined, 'HmIP-RF')).toBe('hmipserver');
        expect(processOf(undefined, 'VirtualDevices')).toBe('hmipserver');
        expect(processOf(undefined, 'BidCos-Wired')).toBe('hs485d');
        expect(processOf(undefined, 'CUxD')).toBe('');
    });
    it('takes the units once they are known, none where they name none', () => {
        expect(processOf(known, 'BidCos-RF')).toBe('rfd');
        expect(processOf(known, 'HmIP-RF')).toBe('');
        expect(processOf([], 'BidCos-RF')).toBe('');
    });
});
