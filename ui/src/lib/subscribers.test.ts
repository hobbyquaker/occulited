import {describe, expect, it} from 'vitest';
import {clientKind, clientLabel, processAddress, processOrder, streamDetail, streamName, streamsOf} from './subscribers';

describe('the subscriber rows', () => {
    it.each([
        ['nr_Ab1Cd2_HmIP-RF', 'node-red'],
        ['hmm_VirtualDevices', 'hmm'],
        ['BidCos-RF_java', 'java'],
        ['1007', 'rega'],
        ['mb_BidCos_RF', ''],
        ['olt_test', ''],
    ])('names the client of %s', (id, kind) => {
        expect(clientKind(id)).toBe(kind);
    });

    it.each([
        ['xmlrpc_bin://127.0.0.1:32001', '127.0.0.1:32001'],
        ['xmlrpc://127.0.0.1:39292/groups', '127.0.0.1:39292/groups'],
        ['xmlrpc://127.0.0.1:2010/', '127.0.0.1:2010'],
        ['127.0.0.1:1', '127.0.0.1:1'],
    ])('shows %s as %s', (url, shown) => {
        expect(processAddress(url)).toBe(shown);
    });

    it('orders the processes under their module cards, the rest after them as listed', () => {
        const list = [{name: 'VirtualDevices'}, {name: 'HmIP-RF'}, {name: 'BidCos-Wired'}, {name: 'BidCos-RF'}];
        expect(processOrder(list, ['BidCos-RF', 'HmIP-RF']).map((i) => i.name)).toEqual(['BidCos-RF', 'HmIP-RF', 'VirtualDevices', 'BidCos-Wired']);
        expect(processOrder(list, []).map((i) => i.name)).toEqual(['VirtualDevices', 'HmIP-RF', 'BidCos-Wired', 'BidCos-RF']);
        expect(processOrder(list, ['HmIP-RF', 'Missing']).map((i) => i.name)).toEqual(['HmIP-RF', 'VirtualDevices', 'BidCos-Wired', 'BidCos-RF']);
    });
});

describe('clientLabel (task 84)', () => {
    it.each([
        ['nr_Ab1Cd2_BidCos-RF', false, 'node-red-contrib-ccu'],
        ['hmm_VirtualDevices', false, 'Homematic Manager'],
        ['HmIP-RF_java', false, 'hmipserver'],
        ['1007', false, 'ReGa'],
        ['occulited_HmIP-RF', true, 'occulited'],
        ['mb_HmIP_RF', false, ''],
    ])('%s (own %s) → %s', (id, own, want) => {
        expect(clientLabel(id, own)).toBe(want);
    });
});

describe('the lite-rpc stream rows (occulited task 87)', () => {
    const t = (key: string, p?: Record<string, string | number>) => Object.entries(p ?? {}).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), key);
    const subs = {
        'HmIP-RF': {total: 2, internal: 2, external: 0, clients: [
            {id: 'occulited_HmIP-RF', url: 'http://127.0.0.1:8184/cb/HmIP-RF', internal: true, own: true},
            {id: 'addon:x', url: '', internal: true, stream: {id: '3', kind: 'token', transport: 'sse', remote: '127.0.0.1'}},
        ]},
    };
    it('names a stream by its holder', () => {
        expect(streamName({id: 'addon:x', url: '', internal: true, stream: {id: '3', kind: 'token', transport: 'sse'}}, t)).toBe('addon:x (stream via occulited)');
        expect(streamName({id: 'admin', url: '', internal: false, stream: {id: '4', kind: 'session', transport: 'websocket'}}, t)).toBe('session of admin (stream via occulited)');
        expect(streamName({id: 'b1', url: '', internal: false, stream: {id: '5', kind: 'public', transport: 'websocket'}}, t)).toBe('public browser b1 (stream via occulited)');
    });
    it('says the transport and the remote', () => {
        expect(streamDetail(subs['HmIP-RF'].clients[1]!)).toBe('SSE · 127.0.0.1');
        expect(streamDetail({id: 'a', url: '', internal: false, stream: {id: '4', kind: 'session', transport: 'websocket'}})).toBe('WebSocket');
        expect(streamDetail({id: 'a', url: '', internal: false})).toBe('');
    });
    it('picks the streams of one interface', () => {
        expect(streamsOf(subs, 'HmIP-RF').map((c) => c.id)).toEqual(['addon:x']);
        expect(streamsOf(subs, 'BidCos-RF')).toEqual([]);
        expect(streamsOf(undefined, 'HmIP-RF')).toEqual([]);
    });
});
