import {describe, expect, it} from 'vitest';
import {classify, isOn, longAction, mainAction, splitRef, stateText, working, type Description} from './channels';

const t = (k: string, p?: Record<string, string>) => k.replace(/\{(\w+)\}/g, (_, n) => p?.[n] ?? '');

describe('the channel model', () => {
    const dimmer: Description = {LEVEL: {CONTROL: 'DIMMER.LEVEL', OPERATIONS: 7}, ON_TIME: {CONTROL: 'NONE'}};
    const thermo: Description = {SETPOINT: {CONTROL: 'TEMP.SETPOINT'}, STATE: {CONTROL: 'SWITCH.STATE'}};
    const contact: Description = {STATE: {CONTROL: 'DOOR_SENSOR.STATE', VALUE_LIST: ['CLOSED', 'OPEN']}, LOWBAT: {}};
    const weather: Description = {TEMPERATURE: {}, HUMIDITY: {}};
    const button: Description = {PRESS_SHORT: {CONTROL: 'BUTTON.SHORT', OPERATIONS: 4}, PRESS_LONG: {CONTROL: 'BUTTON.LONG', OPERATIONS: 4}};
    const vkey: Description = {PRESS_SHORT: {CONTROL: 'BUTTON.SHORT', OPERATIONS: 6}, PRESS_LONG: {CONTROL: 'BUTTON.LONG', OPERATIONS: 6}, LEVEL: {CONTROL: 'NONE', OPERATIONS: 7}};
    it('classifies by the CONTROL kind, the richer one first', () => {
        expect(classify(dimmer)).toEqual({widget: 'dimmer', tile: 'act-open', stateKey: 'LEVEL', commandKey: 'LEVEL'});
        expect(classify(thermo).widget).toBe('thermostat');
        expect(classify(contact)).toEqual({widget: 'contact', tile: 'read', stateKey: 'STATE', commandKey: ''});
        expect(classify(weather)).toEqual({widget: 'reading', tile: 'read', stateKey: 'TEMPERATURE', commandKey: ''});
        // a remote's key only reports; a virtual key is pressed over setValue, short and long
        expect(classify(button)).toEqual({widget: 'button', tile: 'read', stateKey: 'PRESS_SHORT', commandKey: ''});
        expect(classify(vkey)).toEqual({widget: 'button', tile: 'act', stateKey: 'PRESS_SHORT', commandKey: 'PRESS_SHORT'});
        expect(mainAction(classify(vkey), {})).toEqual({key: 'PRESS_SHORT', value: true});
        expect(longAction(classify(vkey), vkey)).toEqual({key: 'PRESS_LONG', value: true});
        expect(longAction(classify(button), button)).toBeUndefined();
        expect(stateText(classify(vkey), {}, vkey, t)).toBe('');
        expect(classify({WEEK_PROGRAM_CHANNEL_LOCKS: {CONTROL: 'WEEK_PROFILE.CHANNEL_LOCKS'}}).widget).toBe('none');
        expect(classify({MOTION: {CONTROL: 'MOTIONDETECTOR_TRANSCEIVER.MOTION'}}).widget).toBe('motion');
        expect(classify({PASSAGE_COUNTER_VALUE: {CONTROL: 'PASSAGE_DETECTOR_DIRECTION_TRANSMITTER.PASSAGE_COUNTER_VALUE'}}).widget).toBe('motion');
        expect(classify(undefined).widget).toBe('none');
    });
    it('reads the state and the main action', () => {
        const m = classify(dimmer);
        expect(isOn(m, {LEVEL: 0.6})).toBe(true);
        expect(stateText(m, {LEVEL: 0.6}, dimmer, t)).toBe('On · 60 %');
        expect(stateText(m, {LEVEL: 0}, dimmer, t)).toBe('Off');
        expect(mainAction(m, {LEVEL: 0.6})).toEqual({key: 'LEVEL', value: 0});
        expect(mainAction(m, {LEVEL: 0})).toEqual({key: 'LEVEL', value: 1});
        const c = classify(contact);
        expect(stateText(c, {STATE: 1}, contact, t)).toBe('Open');
        expect(mainAction(c, {STATE: 1})).toBeUndefined();
        const w = classify(weather);
        expect(stateText(w, {TEMPERATURE: 22.14, HUMIDITY: 45}, weather, t)).toBe('22.1 °C · 45 %');
        const th = classify(thermo);
        expect(stateText(th, {SETPOINT: 21.5, ACTUAL_TEMPERATURE: 20.2}, thermo, t)).toBe('set 21.5 °C · 20.2 °C') // task 75: without a valve the setpoint first;
    });
    it('knows a ramping channel', () => {
        expect(working({WORKING: true})).toBe(true);
        expect(working({PROCESS: 1})).toBe(true);
        expect(working({PROCESS: 0, LEVEL: 0.5})).toBe(false);
        expect(working({})).toBe(false);
    });
    it('splits a ref', () => {
        expect(splitRef('HmIP-RF.000ABC:1')).toEqual({iface: 'HmIP-RF', address: '000ABC:1'});
        expect(splitRef('NEQ0123456:1')).toEqual({iface: '', address: 'NEQ0123456:1'});
    });
});
