import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {classify} from './channels';
import {loadChannels, needsRead, operable, streamValue} from './rpc';

// occulited B-47: the tiles' values come from the state store; an interface process is asked only
// for what the store does not have (BidCos answers a getParamset VALUES by asking the device over
// the radio), and not at all for a device the store knows as unreachable.

type Rpc = {method: string; params: unknown[]; id: number};
const SWITCH = {STATE: {CONTROL: 'SWITCH.STATE', OPERATIONS: 7}};
const KEY = {PRESS_SHORT: {CONTROL: 'BUTTON.SHORT', OPERATIONS: 6}};

/** A system: the state store's entries (undefined: none on this system) and the interface processes. */
function system(o: {entries?: {interface: string; address: string; datapoint: string; value: unknown}[]; pageSize?: number; values?: Record<string, Record<string, unknown>>; types?: Record<string, string>}) {
    const gets: string[] = [];
    const calls: {iface: string; method: string; address: string}[] = [];
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
        const u = new URL(url, 'http://system');
        const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {status});
        if (u.pathname === '/api/rpc/v1/state') {
            gets.push(u.search);
            if (!o.entries) return json({error: 'unsupported', message: 'the state store is not available on this system'}, 501);
            const ifaces = u.searchParams.get('interface')!.split(',');
            const devices = u.searchParams.get('address')!.split(',');
            const all = o.entries.filter((e) => ifaces.includes(e.interface) && devices.some((d) => e.address === d || e.address.startsWith(d + ':')));
            const from = Number(u.searchParams.get('after') ?? 0);
            const size = o.pageSize ?? all.length;
            return json({entries: all.slice(from, from + size), total: all.length, event_id: `boot-${40 + gets.length}`, ...(from + size < all.length ? {next: String(from + size)} : {})});
        }
        const iface = decodeURIComponent(u.pathname.split('/').pop()!);
        return json((JSON.parse(init.body as string) as Rpc[]).map((r) => {
            const address = r.params[0] as string;
            calls.push({iface, method: r.method, address});
            const type = o.types?.[address] ?? 'SWITCH';
            switch (r.method) {
                case 'getDeviceDescription': return {jsonrpc: '2.0', id: r.id, result: {ADDRESS: address, TYPE: type}};
                case 'getParamsetDescription': return {jsonrpc: '2.0', id: r.id, result: type === 'KEY' ? KEY : SWITCH};
                case 'getParamset': return o.values?.[address] ? {jsonrpc: '2.0', id: r.id, result: o.values[address]} : {jsonrpc: '2.0', id: r.id, error: {code: -1, message: 'Failure'}};
            }
            return {jsonrpc: '2.0', id: r.id, error: {code: -32601, message: 'no such method'}};
        }));
    });
    return {gets, calls, read: () => calls.filter((c) => c.method === 'getParamset').map((c) => `${c.iface}.${c.address}`)};
}

describe('loadChannels', () => {
    beforeEach(() => {
        vi.stubGlobal('sessionStorage', {getItem: () => 'S1'});
        vi.stubGlobal('window', {dispatchEvent: () => true});
    });
    afterEach(() => vi.unstubAllGlobals());

    it('takes the values from the state store and asks no interface process for them', async () => {
        const s = system({entries: [
            {interface: 'BidCos-RF', address: 'AAA0000001:1', datapoint: 'STATE', value: true},
            {interface: 'BidCos-RF', address: 'AAA0000001:0', datapoint: 'UNREACH', value: false},
            {interface: 'HmIP-RF', address: 'AAA0000002:3', datapoint: 'STATE', value: false},
            {interface: 'HmIP-RF', address: 'AAA0000002:4', datapoint: 'STATE', value: true}, // another channel of the device: not this page's
        ]});
        const r = await loadChannels(['BidCos-RF.AAA0000001:1', 'HmIP-RF.AAA0000002:3']);
        expect(r.errors).toEqual([]);
        expect(r.channels.get('BidCos-RF.AAA0000001:1')).toMatchObject({iface: 'BidCos-RF', address: 'AAA0000001:1', type: 'SWITCH', values: {STATE: true}, model: {widget: 'switch'}});
        expect(r.channels.get('HmIP-RF.AAA0000002:3')!.values).toEqual({STATE: false});
        expect([...r.channels.keys()]).toEqual(['BidCos-RF.AAA0000001:1', 'HmIP-RF.AAA0000002:3']);
        // one read of the store for the devices of both interfaces; the stream resumes from it
        expect(s.gets).toEqual(['?interface=BidCos-RF,HmIP-RF&address=AAA0000001,AAA0000002&limit=5000']);
        expect(r.eventId).toBe('boot-41');
        expect(s.read()).toEqual([]);
    });

    it('reads from the interface only the channel whose state the store does not have', async () => {
        const s = system({
            entries: [
                {interface: 'BidCos-RF', address: 'BBB0000001:1', datapoint: 'STATE', value: true},
                {interface: 'BidCos-RF', address: 'BBB0000002:1', datapoint: 'LEVEL', value: 0.5}, // an entry, but not the tile's state
            ],
            values: {'BBB0000002:1': {STATE: false, WORKING: false}, 'BBB0000003:1': {STATE: true}},
        });
        const r = await loadChannels(['BidCos-RF.BBB0000001:1', 'BidCos-RF.BBB0000002:1', 'BidCos-RF.BBB0000003:1']);
        expect(s.read()).toEqual(['BidCos-RF.BBB0000002:1', 'BidCos-RF.BBB0000003:1']);
        expect(r.channels.get('BidCos-RF.BBB0000001:1')!.values).toEqual({STATE: true});
        expect(r.channels.get('BidCos-RF.BBB0000002:1')!.values).toEqual({LEVEL: 0.5, STATE: false, WORKING: false});
        expect(r.channels.get('BidCos-RF.BBB0000003:1')!.values).toEqual({STATE: true});
    });

    it('does not ask for a device the store knows as unreachable, nor for a key', async () => {
        const s = system({
            entries: [
                {interface: 'BidCos-RF', address: 'CCC0000001:0', datapoint: 'UNREACH', value: true},
                {interface: 'BidCos-RF', address: 'CCC0000001:0', datapoint: 'STICKY_UNREACH', value: true},
                {interface: 'BidCos-RF', address: 'CCC0000002:0', datapoint: 'UNREACH', value: false},
            ],
            types: {'CCC0000003:2': 'KEY'},
        });
        const r = await loadChannels(['BidCos-RF.CCC0000001:1', 'BidCos-RF.CCC0000002:1', 'BidCos-RF.CCC0000003:2']);
        // the reachable one without a value is asked (and fails here: its tile stays without a state)
        expect(s.read()).toEqual(['BidCos-RF.CCC0000002:1']);
        expect(r.errors).toEqual([]);
        expect(r.channels.get('BidCos-RF.CCC0000001:1')!.values).toEqual({});
        expect(r.channels.get('BidCos-RF.CCC0000002:1')!.values).toEqual({});
        expect(r.channels.get('BidCos-RF.CCC0000003:2')!.model.widget).toBe('button');
    });

    it('follows the store\'s pages', async () => {
        const s = system({pageSize: 1, entries: [
            {interface: 'HmIP-RF', address: 'DDD0000001:1', datapoint: 'STATE', value: true},
            {interface: 'HmIP-RF', address: 'DDD0000001:2', datapoint: 'STATE', value: false},
            {interface: 'HmIP-RF', address: 'DDD0000001:3', datapoint: 'STATE', value: true},
        ]});
        const r = await loadChannels(['HmIP-RF.DDD0000001:1', 'HmIP-RF.DDD0000001:3']);
        expect(s.gets).toEqual(['?interface=HmIP-RF&address=DDD0000001&limit=5000', '?interface=HmIP-RF&address=DDD0000001&limit=5000&after=1', '?interface=HmIP-RF&address=DDD0000001&limit=5000&after=2']);
        expect(r.eventId).toBe('boot-41'); // the first read's: nothing after it is lost
        expect(r.channels.get('HmIP-RF.DDD0000001:3')!.values).toEqual({STATE: true});
        expect(s.read()).toEqual([]);
    });

    it('asks for many devices in several reads: their addresses travel in the query', async () => {
        const refs = Array.from({length: 85}, (_, i) => `HmIP-RF.EEE${String(i).padStart(7, '0')}:1`);
        const s = system({entries: refs.map((ref) => ({interface: 'HmIP-RF', address: ref.slice('HmIP-RF.'.length), datapoint: 'STATE', value: true}))});
        const r = await loadChannels(refs);
        expect(s.gets).toHaveLength(3);
        expect(Math.max(...s.gets.map((g) => g.length))).toBeLessThan(600);
        expect([...r.channels.values()].every((c) => c.values.STATE === true)).toBe(true);
        expect(s.read()).toEqual([]);
    });

    it('on a system without the state store every channel is read from its interface, as before', async () => {
        const s = system({values: {'FFF0000001:1': {STATE: true}, 'FFF0000002:1': {STATE: false}}});
        const r = await loadChannels(['BidCos-RF.FFF0000001:1', 'BidCos-RF.FFF0000002:1']);
        expect(s.read()).toEqual(['BidCos-RF.FFF0000001:1', 'BidCos-RF.FFF0000002:1']);
        expect(r.channels.get('BidCos-RF.FFF0000001:1')!.values).toEqual({STATE: true});
        expect(r.eventId).toBe('');
        expect(r.errors).toEqual([]);
    });
});

describe('needsRead', () => {
    it('is about the tile\'s state datapoint only', () => {
        const sw = classify(SWITCH);
        expect(needsRead(sw, undefined)).toBe(true);
        expect(needsRead(sw, {LEVEL: 1})).toBe(true);
        expect(needsRead(sw, {STATE: false})).toBe(false);
        // a key shows presses as they come; a channel without a widget has no tile
        expect(needsRead(classify(KEY), undefined)).toBe(false);
        expect(needsRead(classify(undefined), undefined)).toBe(false);
    });
});

describe('streamValue', () => {
    it('takes an interface\'s event and the state store\'s message alike', () => {
        expect(streamValue('event', '{"interface":"HmIP-RF","address":"AAA0000001:1","key":"STATE","value":true,"ts":"x"}')).toEqual({ref: 'HmIP-RF.AAA0000001:1', key: 'STATE', value: true});
        expect(streamValue('state', '{"interface":"BidCos-RF","address":"AAA0000001:0","datapoint":"STICKY_UNREACH","value":false,"source":"sweep"}')).toEqual({ref: 'BidCos-RF.AAA0000001:0', key: 'STICKY_UNREACH', value: false});
    });
    it('leaves out the other messages, a torn line, and a press that is only the store\'s entry', () => {
        expect(streamValue('hello', '{"boot_id":"b"}')).toBeUndefined();
        expect(streamValue('interface', '{"interface":"HmIP-RF","state":"up"}')).toBeUndefined();
        expect(streamValue('event', '{"interface":"HmIP-RF","addr')).toBeUndefined();
        expect(streamValue('event', '{"interface":"HmIP-RF","address":"A:1"}')).toBeUndefined();
        expect(streamValue('state', '{"interface":"HmIP-RF","address":"A:1","datapoint":"PRESS_SHORT","value":true}')).toBeUndefined();
        expect(streamValue('event', '{"interface":"HmIP-RF","address":"A:1","key":"PRESS_SHORT","value":true}')).toEqual({ref: 'HmIP-RF.A:1', key: 'PRESS_SHORT', value: true});
    });
});

// occulited B-84: "System: no such interface: System" - ReGa's pseudo-interface and an interface
// lite-rpc does not have are left out, never an error on the page
describe('foreign interfaces (B-84)', () => {
    beforeEach(() => {
        vi.stubGlobal('sessionStorage', {getItem: () => 'S1'});
        vi.stubGlobal('window', {dispatchEvent: () => true});
    });
    afterEach(() => vi.unstubAllGlobals());

    it('a System object is no channel, and nothing asks for it', () => {
        expect(operable('System.BidCoS-RF:1')).toBe(false);
        expect(operable('HmIP-RF.00010000000A10:1')).toBe(true);
        expect(operable('CUxD.CUX2801001:1', ['CUxD'])).toBe(false);
        expect(operable('CUxD.CUX2801001:1', new Set(['CUxD']))).toBe(false);
        expect(operable('CUxD.CUX2801001:1')).toBe(true);
        // an old ref without an interface stays a tile, with nothing to ask
        expect(operable('BidCoS-RF:1')).toBe(true);
    });
    it('reads the others; an interface lite-rpc does not have is named, not an error', async () => {
        const asked: string[] = [];
        vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
            const u = new URL(url, 'http://system');
            const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {status});
            if (u.pathname === '/api/rpc/v1/state') {
                asked.push(`state ${u.searchParams.get('interface')}`);
                return json({entries: [{interface: 'HmIP-RF', address: '00010000000A10:1', datapoint: 'STATE', value: true}], event_id: 'boot-1'});
            }
            const iface = decodeURIComponent(u.pathname.split('/').pop()!);
            asked.push(iface);
            if (iface === 'CUxD') return json({error: 'unknown-interface', message: 'no such interface: CUxD'}, 404);
            return json((JSON.parse(init.body as string) as Rpc[]).map((r) => (r.method === 'getDeviceDescription' ? {jsonrpc: '2.0', id: r.id, result: {TYPE: 'SWITCH'}} : r.method === 'getParamsetDescription' ? {jsonrpc: '2.0', id: r.id, result: SWITCH} : {jsonrpc: '2.0', id: r.id, result: {}})));
        });
        const r = await loadChannels(['HmIP-RF.00010000000A10:1', 'System.BidCoS-RF:1', 'CUxD.CUX2801001:1']);
        expect(r.errors).toEqual([]);
        expect(r.unknown).toEqual(['CUxD']);
        expect([...r.channels.keys()]).toEqual(['HmIP-RF.00010000000A10:1']);
        expect(r.channels.get('HmIP-RF.00010000000A10:1')!.values.STATE).toBe(true);
        expect(asked.some((a) => a.includes('System'))).toBe(false);
        // any other failure is still an error
        vi.stubGlobal('fetch', async (url: string) => {
            const u = new URL(url, 'http://system');
            if (u.pathname === '/api/rpc/v1/state') return new Response(JSON.stringify({entries: []}), {status: 200});
            return new Response(JSON.stringify({error: 'unavailable', message: 'HmIP-RF does not answer'}), {status: 503});
        });
        const r2 = await loadChannels(['HmIP-RF.00010000000A11:1']);
        expect(r2.errors).toEqual(['HmIP-RF: HmIP-RF does not answer']);
        expect(r2.unknown).toEqual([]);
    });
});
