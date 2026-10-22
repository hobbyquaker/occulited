import {describe, expect, it} from 'vitest';
import {durationLine, durationText, nextDurationChange, sinceLine} from './duration';
import type {Model} from './channels';

const MIN = 60_000;
const H = 60 * MIN;
const D = 24 * H;
const contact: Model = {widget: 'contact', tile: 'read', stateKey: 'STATE', commandKey: ''};
const motion: Model = {widget: 'motion', tile: 'read', stateKey: 'MOTION', commandKey: ''};
const sw: Model = {widget: 'switch', tile: 'act-open', stateKey: 'STATE', commandKey: 'STATE'};

// occulited task 95: at most two units, minutes rounded down, whole days from 7 days
describe('durationText', () => {
    it('German', () => {
        expect(durationText(30_000, 'de')).toBe('');
        expect(durationText(4 * MIN + 59_000, 'de')).toBe('4 Min');
        expect(durationText(59 * MIN, 'de')).toBe('59 Min');
        expect(durationText(H, 'de')).toBe('1 Std');
        expect(durationText(3 * H + 12 * MIN, 'de')).toBe('3 Std 12 Min');
        expect(durationText(23 * H + 59 * MIN, 'de')).toBe('23 Std 59 Min');
        expect(durationText(D, 'de')).toBe('1 Tg');
        expect(durationText(2 * D + 5 * H + 7 * MIN, 'de')).toBe('2 Tg 5 Std');
        expect(durationText(6 * D + 23 * H + 59 * MIN, 'de')).toBe('6 Tg 23 Std');
        expect(durationText(7 * D, 'de')).toBe('7 Tagen');
        expect(durationText(11 * D + 2 * H, 'de')).toBe('11 Tagen');
    });
    it('English', () => {
        expect(durationText(4 * MIN, 'en')).toBe('4 min');
        expect(durationText(3 * H + 12 * MIN, 'en')).toBe('3 h 12 min');
        expect(durationText(2 * D + 5 * H, 'en')).toBe('2 d 5 h');
        expect(durationText(11 * D, 'en')).toBe('11 days');
    });
    it('a time in the future (a clock ahead) is under a minute', () => {
        expect(durationText(-5 * MIN, 'de')).toBe('');
    });
});

describe('durationLine', () => {
    const now = Date.parse('2026-10-22T12:00:00Z');
    const ago = (ms: number) => new Date(now - ms).toISOString();
    it('a contact: "seit …", "seit unter 1 Min"', () => {
        expect(durationLine(contact, {STATE: 1}, ago(4 * MIN), now, 'de')).toBe('seit 4 Min');
        expect(durationLine(contact, {STATE: 0}, ago(10_000), now, 'de')).toBe('seit unter 1 Min');
        expect(durationLine(contact, {STATE: 0}, ago(3 * H + 12 * MIN), now, 'en')).toBe('for 3 h 12 min');
        expect(durationLine(contact, {STATE: 0}, ago(10_000), now, 'en')).toBe('for under 1 min');
    });
    it('motion: "seit" while detected, "zuletzt vor" after', () => {
        expect(durationLine(motion, {MOTION: true}, ago(2 * MIN), now, 'de')).toBe('seit 2 Min');
        expect(durationLine(motion, {MOTION: false}, ago(14 * MIN), now, 'de')).toBe('zuletzt vor 14 Min');
        expect(durationLine(motion, {MOTION: false}, ago(14 * MIN), now, 'en')).toBe('last 14 min ago');
        expect(durationLine(motion, {MOTION: false}, ago(5_000), now, 'de')).toBe('zuletzt gerade eben');
    });
    it('nothing when the time is unknown, or for a tile without a duration', () => {
        expect(durationLine(contact, {STATE: 1}, undefined, now, 'de')).toBe('');
        expect(durationLine(contact, {STATE: 1}, 'garbage', now, 'de')).toBe('');
        expect(durationLine(sw, {STATE: true}, ago(4 * MIN), now, 'de')).toBe('');
        expect(durationLine(undefined, {}, ago(4 * MIN), now, 'de')).toBe('');
    });
});

describe('sinceLine', () => {
    it('the exact time for the sheet', () => {
        const at = new Date(2026, 9, 21, 14, 3).toISOString();
        const now = new Date(2026, 9, 22, 12, 0);
        expect(sinceLine(contact, {STATE: 1}, at, 'de', now)).toBe('seit 21.10., 14:03');
        expect(sinceLine(motion, {MOTION: false}, at, 'de', now)).toBe('zuletzt am 21.10., 14:03');
        expect(sinceLine(contact, {STATE: 1}, at, 'en', now)).toBe('since 21/10, 14:03');
        expect(sinceLine(contact, {STATE: 1}, undefined, 'de', now)).toBe('');
    });
});

describe('nextDurationChange', () => {
    const now = Date.parse('2026-10-22T12:00:30Z');
    it('the earliest full minute of the shown durations', () => {
        // one changed 10 s ago: its minute is full in 50 s; one changed 45 s ago: in 15 s
        expect(nextDurationChange([now - 10_000], now)).toBe(50_020);
        expect(nextDurationChange([now - 10_000, now - 45_000], now)).toBe(15_020);
        expect(nextDurationChange([now - 3 * H - 12 * MIN - 59_000], now)).toBe(1_020);
    });
    it('exactly on a minute: the next one', () => {
        expect(nextDurationChange([now - 2 * MIN], now)).toBe(60_020);
    });
    it('a time in the future, none, or a broken one', () => {
        expect(nextDurationChange([now + 5_000], now)).toBe(5_020);
        expect(nextDurationChange([], now)).toBeUndefined();
        expect(nextDurationChange([NaN], now)).toBeUndefined();
    });
});
