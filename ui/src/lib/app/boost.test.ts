import {describe, expect, it} from 'vitest';
import {boostActive, boostCommand, boostReported, BoostTracker, canBoost, plainMode, RING_C, ringOffset, splitSeconds} from './boost';

const hmip = {BOOST_MODE: {OPERATIONS: 7}, BOOST_TIME: {OPERATIONS: 5}, SET_POINT_MODE: {OPERATIONS: 7}};
const bidcos = {BOOST_MODE: {OPERATIONS: 2}, BOOST_STATE: {OPERATIONS: 5}, CONTROL_MODE: {OPERATIONS: 5}, AUTO_MODE: {OPERATIONS: 2}, MANU_MODE: {OPERATIONS: 2}, SET_TEMPERATURE: {OPERATIONS: 7}};
const wall = {SET_POINT_TEMPERATURE: {OPERATIONS: 7}, SET_POINT_MODE: {OPERATIONS: 7}};

describe('boost (task 103)', () => {
    it('reads the state and the time left from HmIP and BidCos', () => {
        expect(boostActive({BOOST_MODE: true})).toBe(true);
        expect(boostActive({BOOST_MODE: false, BOOST_TIME: 100})).toBe(false);
        expect(boostActive({CONTROL_MODE: 3})).toBe(true);
        expect(boostActive({CONTROL_MODE: 1})).toBe(false);
        expect(boostActive({CONTROL_MODE: 1, BOOST_MODE: true})).toBe(false); // the BidCos action's value lingering
        expect(boostReported({BOOST_TIME: 168})).toBe(168);
        expect(boostReported({BOOST_TIME: '12'})).toBe(12);
        expect(boostReported({BOOST_STATE: 5})).toBe(300);
        expect(boostReported({BOOST_STATE: -1})).toBe(0);
        expect(boostReported({})).toBeUndefined();
    });
    it('a boost is offered where BOOST_MODE is writable', () => {
        expect(canBoost(hmip)).toBe(true);
        expect(canBoost(bidcos)).toBe(true);
        expect(canBoost(wall)).toBe(false);
        expect(canBoost(undefined)).toBe(false);
    });
    it('the mode besides boost', () => {
        expect(plainMode({SET_POINT_MODE: 0})).toBe('auto');
        expect(plainMode({SET_POINT_MODE: 1})).toBe('manual');
        expect(plainMode({CONTROL_MODE: 0})).toBe('auto');
        expect(plainMode({CONTROL_MODE: 1})).toBe('manual');
        expect(plainMode({CONTROL_MODE: 2})).toBe('auto');
        expect(plainMode({CONTROL_MODE: 3})).toBeUndefined();
        expect(plainMode({})).toBeUndefined();
    });
    it('the commands: HmIP toggles BOOST_MODE; BidCos goes back to the mode before', () => {
        expect(boostCommand(hmip, {}, true)).toEqual({BOOST_MODE: true});
        expect(boostCommand(hmip, {BOOST_MODE: true}, false, 'manual')).toEqual({BOOST_MODE: false});
        expect(boostCommand(bidcos, {CONTROL_MODE: 3}, true)).toEqual({BOOST_MODE: true});
        expect(boostCommand(bidcos, {CONTROL_MODE: 3, SET_TEMPERATURE: 21.5}, false, 'auto')).toEqual({AUTO_MODE: true});
        expect(boostCommand(bidcos, {CONTROL_MODE: 3, SET_TEMPERATURE: 21.5}, false, 'manual')).toEqual({MANU_MODE: 21.5});
        expect(boostCommand(bidcos, {CONTROL_MODE: 3}, false, undefined)).toEqual({AUTO_MODE: true});
    });
    it('the tracker counts down from the last report, re-syncs on a new one, keeps the first length', () => {
        const tr = new BoostTracker();
        const t0 = 1_000_000;
        expect(tr.update('a', {BOOST_MODE: false, SET_POINT_MODE: 1}, t0)).toBeNull();
        expect(tr.before('a')).toBe('manual');
        // boost starts: 300 s reported
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 300}, t0 + 1000)).toEqual({remaining: 300, full: 300});
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 300}, t0 + 2500)).toEqual({remaining: 299, full: 300});
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 300}, t0 + 11_000)).toEqual({remaining: 290, full: 300});
        // the device reports again: the countdown follows it, the length stays
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 280}, t0 + 20_000)).toEqual({remaining: 280, full: 300});
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 280}, t0 + 25_000)).toEqual({remaining: 275, full: 300});
        // never below zero
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 280}, t0 + 400_000)).toEqual({remaining: 0, full: 300});
        // over: nothing; the mode before is still known
        expect(tr.update('a', {BOOST_MODE: false, BOOST_TIME: 0, SET_POINT_MODE: 1}, t0 + 401_000)).toBeNull();
        expect(tr.before('a')).toBe('manual');
        // a new boost is a new length
        expect(tr.update('a', {BOOST_MODE: true, BOOST_TIME: 600}, t0 + 500_000)).toEqual({remaining: 600, full: 600});
        // BidCos: minutes
        expect(tr.update('b', {CONTROL_MODE: 0}, t0)).toBeNull();
        expect(tr.update('b', {CONTROL_MODE: 3, BOOST_STATE: 5}, t0 + 1000)).toEqual({remaining: 300, full: 300});
        expect(tr.update('b', {CONTROL_MODE: 3, BOOST_STATE: 4}, t0 + 61_000)).toEqual({remaining: 240, full: 300});
        expect(tr.before('b')).toBe('auto');
        // boost on without a time: shown, no countdown
        expect(tr.update('c', {BOOST_MODE: true}, t0)).toEqual({remaining: 0, full: 0});
    });
    // occulited B-103: production, 2026-10-23 - the heating group INT0000011:1 said BOOST_MODE 1 with BOOST_TIME 0, its
    // member HmIP-WTH-2 00010000000B42:1 BOOST_MODE 1 with BOOST_TIME 297; the BidCos HM-CC-RT-DN-BoM KEQ9000007:4
    // BOOST_TIME 24 while idle (its BOOST_TIME is minutes, rf_cc_rt_dn_bom.xml)
    it('a BidCos BOOST_TIME is minutes, never seconds; the unit of the description wins (B-103)', () => {
        expect(boostReported({CONTROL_MODE: 0, BOOST_TIME: 24})).toBe(1440);
        expect(boostActive({CONTROL_MODE: 0, BOOST_TIME: 24, BOOST_MODE: true})).toBe(false); // idle: no boost, whatever the time says
        expect(boostReported({CONTROL_MODE: 3, BOOST_STATE: 4, BOOST_TIME: 24})).toBe(240); // BOOST_STATE first
        expect(boostReported({CONTROL_MODE: 3, BOOST_TIME: 3})).toBe(180);
        expect(boostReported({BOOST_TIME: 3}, {BOOST_TIME: {UNIT: 'min', OPERATIONS: 5}})).toBe(180);
        expect(boostReported({CONTROL_MODE: 3, BOOST_TIME: 90}, {BOOST_TIME: {UNIT: 's', OPERATIONS: 5}})).toBe(90);
        expect(boostReported({BOOST_TIME: 297})).toBe(297);
    });
    it('a group with BOOST_TIME 0 takes its members\' highest time left, follows them, and says Boost without one (B-103)', () => {
        const tr = new BoostTracker();
        const t0 = 2_000_000;
        const group = {BOOST_MODE: true, BOOST_TIME: 0, SET_POINT_MODE: 0};
        const wth = (on: boolean, time: number) => ({ref: 'HmIP-RF.00010000000B42:1', values: {BOOST_MODE: on, BOOST_TIME: time, SET_POINT_MODE: 0}});
        const etrv = (on: boolean, time: number) => ({ref: 'HmIP-RF.00010000000B43:1', values: {BOOST_MODE: on, BOOST_TIME: time}});
        // boosting is the group's own BOOST_MODE; with no member time it is "Boost" with no countdown
        expect(tr.update('VirtualDevices.INT0000011:1', group, t0)).toEqual({remaining: 0, full: 0});
        expect(tr.update('VirtualDevices.INT0000011:1', group, t0, {members: [wth(false, 0)]})).toEqual({remaining: 0, full: 0});
        // production's values: the member's 297 s is the group's, counting down
        expect(tr.update('VirtualDevices.INT0000011:1', group, t0 + 1000, {members: [wth(true, 297), etrv(false, 0)]})).toEqual({remaining: 297, full: 297});
        expect(tr.update('VirtualDevices.INT0000011:1', group, t0 + 11_000, {members: [wth(true, 297), etrv(false, 0)]})).toEqual({remaining: 287, full: 297});
        // the highest of those that boost; a member's new report re-syncs it
        expect(tr.update('VirtualDevices.INT0000011:1', group, t0 + 12_000, {members: [wth(true, 297), etrv(true, 300)]})).toEqual({remaining: 300, full: 300});
        expect(tr.update('VirtualDevices.INT0000011:1', group, t0 + 20_000, {members: [wth(true, 270), etrv(true, 300)]})).toEqual({remaining: 292, full: 300});
        // the members' trackers are their own tiles' as well
        expect(tr.update('HmIP-RF.00010000000B42:1', wth(true, 270).values, t0 + 20_000)).toEqual({remaining: 270, full: 297});
        // the group's own time wins when it has one
        expect(tr.update('VirtualDevices.INT0000011:1', {...group, BOOST_TIME: 120}, t0 + 21_000, {members: [wth(true, 270)]})).toEqual({remaining: 120, full: 120});
        // over: the group's BOOST_MODE false ends it, whatever the members still say
        expect(tr.update('VirtualDevices.INT0000011:1', {...group, BOOST_MODE: false}, t0 + 22_000, {members: [wth(true, 260)]})).toBeNull();
        expect(tr.before('VirtualDevices.INT0000011:1')).toBe('auto');
    });
    it('a report is counted from when the system saw it (changed_at), not from the page load (B-103)', () => {
        const tr = new BoostTracker();
        const now = Date.parse('2026-10-23T10:02:00Z');
        // stored 297 at 10:00:00, the page opens two minutes later
        expect(tr.update('w', {BOOST_MODE: true, BOOST_TIME: 297}, now, {changed: {BOOST_TIME: '2026-10-23T10:00:00Z'}})).toEqual({remaining: 177, full: 297});
        // a time in the future (a clock off) is taken as now; one older than the time it reports runs out at 0
        expect(tr.update('x', {BOOST_MODE: true, BOOST_TIME: 60}, now, {changed: {BOOST_TIME: '2026-10-23T10:05:00Z'}})).toEqual({remaining: 60, full: 60});
        expect(tr.update('y', {BOOST_MODE: true, BOOST_TIME: 60}, now, {changed: {BOOST_TIME: '2026-10-23T09:00:00Z'}})).toEqual({remaining: 0, full: 60});
    });
    it('formats minutes and seconds, and the ring drains clockwise from full', () => {
        expect(splitSeconds(168)).toEqual({m: 2, s: '48'});
        expect(splitSeconds(0)).toEqual({m: 0, s: '00'});
        expect(splitSeconds(600)).toEqual({m: 10, s: '00'});
        expect(ringOffset(300, 300)).toBe(0);
        expect(ringOffset(0, 300)).toBeCloseTo(RING_C);
        expect(ringOffset(150, 300)).toBeCloseTo(RING_C / 2);
        expect(ringOffset(10, 0)).toBeCloseTo(RING_C);
    });
});
