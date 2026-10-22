import {describe, expect, it} from 'vitest';
import {isGroupRef, memberRef} from './groups';

describe('heating group members (B-103)', () => {
    it('names a member\'s climate channel', () => {
        expect(memberRef({id: '00010000000B42:1', type: 'HmIP-WTH-2'})).toBe('HmIP-RF.00010000000B42:1');
        expect(memberRef({id: '00010000000A10'})).toBe('HmIP-RF.00010000000A10:1');
        expect(memberRef({id: 'KEQ9000007', type: 'HM-CC-RT-DN-BoM'})).toBe('BidCos-RF.KEQ9000007:4');
        expect(memberRef({id: 'KEQ9000003', type: 'HM-TC-IT-WM-W-EU'})).toBe('BidCos-RF.KEQ9000003:2');
        expect(memberRef({id: 'KEQ9000004', type: 'HM-CC-VD'})).toBeUndefined();
        expect(memberRef({id: ''})).toBeUndefined();
    });
    it('knows a group channel', () => {
        expect(isGroupRef('VirtualDevices.INT0000011:1')).toBe(true);
        expect(isGroupRef('HmIP-RF.00010000000B42:1')).toBe(false);
        expect(isGroupRef('VirtualDevices.INT0000011')).toBe(false);
    });
});
