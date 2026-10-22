import {describe, expect, it} from 'vitest';
import {classify, formatPower, isMeter, isOn, meterPairs, longAction, mainAction, shown, splitRef, stateText, thermoParts, thermoRow, working, type Description} from './channels';
import prodKinds from '../../../test/fixtures/prod-channel-kinds.json';

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

// occulited B-98: the smoke detectors' VALUES descriptions as rfd and hmipserver hand them out (hm-simulator's
// recorded paramset descriptions): every one names its alarm DANGER.STATE
describe('the smoke detectors (B-98)', () => {
    const bidcosSD: Description = {INSTALL_TEST: {OPERATIONS: 4, TYPE: 'ACTION'}, STATE: {CONTROL: 'DANGER.STATE', OPERATIONS: 5, TYPE: 'BOOL'}};
    const bidcosSD2: Description = {
        ERROR_ALARM_TEST: {CONTROL: 'NONE', OPERATIONS: 5, TYPE: 'ENUM', VALUE_LIST: ['NO_ERROR', 'ALARM_TEST_FAILED']},
        ERROR_SMOKE_CHAMBER: {CONTROL: 'NONE', OPERATIONS: 5, TYPE: 'ENUM', VALUE_LIST: ['NO_ERROR', 'DEGRADED_SMOKE_CHAMBER']},
        INSTALL_TEST: {OPERATIONS: 4, TYPE: 'ACTION'}, LOWBAT: {CONTROL: 'NONE', OPERATIONS: 5, TYPE: 'BOOL'},
        STATE: {CONTROL: 'DANGER.STATE', OPERATIONS: 5, TYPE: 'BOOL'},
    };
    const swsd: Description = {
        SMOKE_DETECTOR_ALARM_STATUS: {CONTROL: 'DANGER.STATE', OPERATIONS: 5, TYPE: 'ENUM', VALUE_LIST: ['IDLE_OFF', 'PRIMARY_ALARM', 'INTRUSION_ALARM', 'SECONDARY_ALARM']},
        SMOKE_DETECTOR_TEST_RESULT: {OPERATIONS: 5, TYPE: 'ENUM', VALUE_LIST: ['NONE', 'SMOKE_TEST_OK', 'SMOKE_TEST_FAILED', 'COMMUNICATION_TEST_SENT', 'COMMUNICATION_TEST_OK']},
        ERROR_CODE: {OPERATIONS: 5, TYPE: 'INTEGER'},
        SMOKE_DETECTOR_COMMAND: {CONTROL: 'SMOKE_DETECTOR.COMMAND', OPERATIONS: 2, TYPE: 'ENUM', VALUE_LIST: ['RESERVED_ALARM_OFF', 'INTRUSION_ALARM_OFF', 'INTRUSION_ALARM', 'SMOKE_TEST', 'COMMUNICATION_TEST', 'COMMUNICATION_TEST_REPEATED']},
    };
    // an HmIP-SWSD described without its command (an older hmipserver): DANGER alone still makes it one
    const swsdNoCommand: Description = {SMOKE_DETECTOR_ALARM_STATUS: swsd.SMOKE_DETECTOR_ALARM_STATUS!};
    const bool = [
        ['HM-Sec-SD (SMOKE_DETECTOR)', bidcosSD],
        ['HM-Sec-SD-2 (SMOKE_DETECTOR)', bidcosSD2],
        ['HM-Sec-SD-Team (SMOKE_DETECTOR_TEAM)', bidcosSD],
        ['HM-Sec-SD-2-Team (SMOKE_DETECTOR_TEAM_V2)', bidcosSD],
    ] as const;
    for (const [name, desc] of bool) {
        it(`${name}: a smoke tile on STATE, quiet or alarming`, () => {
            const m = classify(desc);
            expect(m).toEqual({widget: 'smoke', tile: 'read', stateKey: 'STATE', commandKey: ''});
            expect(stateText(m, {STATE: false}, desc, t)).toBe('Quiet');
            expect(isOn(m, {STATE: false}, desc)).toBe(false);
            expect(stateText(m, {STATE: true}, desc, t)).toBe('Smoke alarm');
            expect(isOn(m, {STATE: true}, desc)).toBe(true);
            expect(mainAction(m, {STATE: false}, desc)).toBeUndefined();
        });
    }
    it('HmIP-SWSD (SMOKE_DETECTOR): the alarm status, and the test and silence in the sheet', () => {
        const m = classify(swsd);
        expect(m).toEqual({widget: 'smoke', tile: 'act-open', stateKey: 'SMOKE_DETECTOR_ALARM_STATUS', commandKey: ''});
        expect(stateText(m, {SMOKE_DETECTOR_ALARM_STATUS: 0}, swsd, t)).toBe('Quiet');
        expect(stateText(m, {SMOKE_DETECTOR_ALARM_STATUS: 1}, swsd, t)).toBe('Smoke alarm');
        expect(stateText(m, {SMOKE_DETECTOR_ALARM_STATUS: 2}, swsd, t)).toBe('Alarm');
        expect(stateText(m, {SMOKE_DETECTOR_ALARM_STATUS: 3}, swsd, t)).toBe('Alarm (secondary)');
        expect(isOn(m, {SMOKE_DETECTOR_ALARM_STATUS: 0}, swsd)).toBe(false);
        expect(isOn(m, {SMOKE_DETECTOR_ALARM_STATUS: 1}, swsd)).toBe(true);
    });
    it('HmIP-SWSD without its command: still a smoke tile, read-only', () => {
        expect(classify(swsdNoCommand)).toEqual({widget: 'smoke', tile: 'read', stateKey: 'SMOKE_DETECTOR_ALARM_STATUS', commandKey: ''});
    });
});

// occulited task 92: the thermostat tile's T2E parts
describe('the thermostat tile (task 92)', () => {
    const hmip: Description = {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7}, ACTUAL_TEMPERATURE: {CONTROL: 'NONE'}, LEVEL: {CONTROL: 'NONE', MIN: 0, MAX: 1}, WINDOW_STATE: {CONTROL: 'NONE'}};
    const m = classify(hmip);
    it('reads Ist, Soll and the valve', () => {
        expect(thermoParts(m, {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 20.3, LEVEL: 0.62}, hmip)).toEqual({actual: 20.3, set: 21, valve: 62, window: false});
        expect(thermoParts(m, {SET_POINT_TEMPERATURE: 21}, hmip)).toEqual({actual: undefined, set: 21, valve: undefined, window: false});
        // task 103: the humidity row's value, from HmIP's HUMIDITY or the HM-TC-IT's ACTUAL_HUMIDITY, rounded
        expect(thermoParts(m, {SET_POINT_TEMPERATURE: 21, HUMIDITY: 54}, hmip).humidity).toBe(54);
        expect(thermoParts(m, {SET_POINT_TEMPERATURE: 21, ACTUAL_HUMIDITY: 47.6}, hmip).humidity).toBe(48);
    });
    it('knows an open window', () => {
        expect(thermoParts(m, {SET_POINT_TEMPERATURE: 21, WINDOW_STATE: 1}, hmip).window).toBe(true);
    });
    // occulited B-100: a wall thermostat (no valve, no window) has no row; the description decides, not a value
    it('has the valve row only with a valve or a window', () => {
        expect(thermoRow({}, hmip)).toBe(true);
        const wall: Description = {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7}, ACTUAL_TEMPERATURE: {CONTROL: 'NONE'}};
        expect(thermoRow({SET_POINT_TEMPERATURE: 21}, wall)).toBe(false);
        expect(thermoRow({VALVE_STATE: 40}, undefined)).toBe(true);
        expect(thermoRow({SET_POINT_TEMPERATURE: 21}, undefined)).toBe(false);
    });
});

// occulited task 104: which channels get a tile - a channel the interface describes without a widget gets none;
// the DANGER kind makes a smoke detector only of a smoke detector's channel type
describe('channels with a UI (task 104)', () => {
    const smokeDesc: Description = {STATE: {CONTROL: 'DANGER.STATE', OPERATIONS: 5, TYPE: 'BOOL'}};
    const none = {widget: 'none', tile: 'read', stateKey: '', commandKey: ''} as const;
    it('DANGER.STATE is a smoke detector on a smoke detector channel type only', () => {
        expect(classify(smokeDesc).widget).toBe('smoke'); // no type known: as B-98 had it
        for (const ty of ['SMOKE_DETECTOR', 'SMOKE_DETECTOR_TEAM', 'SMOKE_DETECTOR_TEAM_V2']) expect(classify(smokeDesc, ty).widget, ty).toBe('smoke');
        expect(classify(smokeDesc, 'ALARM_COND_SWITCH_TRANSMITTER')).toEqual(none);
        expect(classify(smokeDesc, 'WATER_DETECTION_TRANSMITTER')).toEqual(none);
        // the other kinds are untouched by the type
        expect(classify({STATE: {CONTROL: 'SWITCH.STATE', OPERATIONS: 7}}, 'SWITCH_VIRTUAL_RECEIVER').widget).toBe('switch');
    });
    it('a tile only for a channel with a widget, or one whose description could not be read', () => {
        const m = (desc: Description | undefined) => ({desc, model: classify(desc)});
        expect(shown(undefined)).toBe(true); // not loaded yet
        expect(shown(m(undefined))).toBe(true); // the interface did not describe it: kept, an outage blanks nothing
        expect(shown(m({}))).toBe(false); // an empty VALUES paramset (the HmIP-SRH's ALARM_COND_SWITCH_TRANSMITTER)
        expect(shown(m({WEEK_PROGRAM_CHANNEL_LOCKS: {CONTROL: 'WEEK_PROFILE.CHANNEL_LOCKS'}}))).toBe(false);
        expect(shown(m({STATE: {CONTROL: 'SWITCH.STATE', OPERATIONS: 7}}))).toBe(true);
        expect(shown(m({TEMPERATURE: {CONTROL: 'NONE'}}))).toBe(true); // a reading without a CONTROL
    });
    // a real CCU3 installation's channel types (BidCos-RF, HmIP-RF, VirtualDevices; 244 device/channel combinations)
    // with the CONTROL kinds of their VALUES paramsets as the interface processes describe them: nothing but the
    // smoke detectors comes out as smoke, the widget-less ones are the heating groups' receivers, the week profiles,
    // the maintenance and condition channels and the HmIP ALARM_COND_SWITCH_TRANSMITTER (an empty paramset)
    it('maps a real installation: smoke detectors alone are smoke; the rest has a UI or is known to have none', () => {
        const table = prodKinds as Record<string, string[]>;
        expect(Object.keys(table).length).toBeGreaterThan(200);
        const smoke: string[] = [];
        const without: string[] = [];
        for (const [key, kinds] of Object.entries(table)) {
            const [, type] = key.split('/');
            const desc: Description = Object.fromEntries(kinds.map((k, i) => [`DP${i}`, {CONTROL: `${k}.X`, OPERATIONS: 5}]));
            const m = classify(desc, type);
            if (m.widget === 'smoke') smoke.push(key);
            if (m.widget === 'none') without.push(type!);
        }
        expect(smoke.sort()).toEqual(['HM-Sec-SD-2-Team/SMOKE_DETECTOR_TEAM_V2', 'HM-Sec-SD-2/SMOKE_DETECTOR', 'HM-Sec-SD-Team/SMOKE_DETECTOR_TEAM', 'HM-Sec-SD/SMOKE_DETECTOR']);
        const types = [...new Set(without)].sort();
        for (const t of ['ALARM_COND_SWITCH_TRANSMITTER', 'HEATING_CLIMATECONTROL_RECEIVER', 'HEATING_KEY_RECEIVER', 'HEATING_SHUTTER_CONTACT_RECEIVER', 'SWITCH_WEEK_PROFILE', 'CONDITION_POWER', 'MAINTENANCE']) expect(types, t).toContain(t);
        // and none of the operable kinds is among them
        for (const t of ['SWITCH_VIRTUAL_RECEIVER', 'DIMMER_VIRTUAL_RECEIVER', 'HEATING_CLIMATECONTROL_TRANSCEIVER', 'KEY_TRANSCEIVER', 'SHUTTER_CONTACT', 'SMOKE_DETECTOR']) expect(types, t).not.toContain(t);
    });

    // occulited task 109: a switching actuator with a power meter is one tile
    it('pairs a switch with its device\'s meter, per production\'s device types (task 109)', () => {
        const table = prodKinds as Record<string, string[]>;
        const model = (key: string) => classify(Object.fromEntries((table[key] ?? []).map((k, i) => [i === 0 && k.startsWith('POWERMETER') ? 'POWER' : `DP${i}`, {CONTROL: `${k}.X`, OPERATIONS: 5}])), key.split('/')[1]);
        for (const dev of ['HMIP-PSM', 'HmIP-BSM']) {
            expect(isMeter(model(`${dev}/ENERGIE_METER_TRANSMITTER`)), dev).toBe(true);
            expect(model(`${dev}/SWITCH_VIRTUAL_RECEIVER`).widget, dev).toBe('switch');
        }
        expect(isMeter(model('HM-ES-PMSw1-Pl/POWERMETER'))).toBe(true);
        expect(model('HM-ES-PMSw1-Pl/SWITCH').widget).toBe('switch');
        // the PSM: the switch's virtual channel 4 and the meter 7 on one device; a dimmer elsewhere stays
        const psm = meterPairs([
            {ref: 'HmIP-RF.0001D3C99C0001:4', model: model('HMIP-PSM/SWITCH_VIRTUAL_RECEIVER')},
            {ref: 'HmIP-RF.0001D3C99C0001:7', model: model('HMIP-PSM/ENERGIE_METER_TRANSMITTER')},
            {ref: 'HmIP-RF.0001D3C99C0002:7', model: model('HmIP-BSM/ENERGIE_METER_TRANSMITTER')},
        ]);
        expect([...psm.meter]).toEqual([['HmIP-RF.0001D3C99C0001:4', 'HmIP-RF.0001D3C99C0001:7']]);
        // the meter alone (its switch not in the view) keeps its tile
        expect([...psm.hidden]).toEqual(['HmIP-RF.0001D3C99C0001:7']);
        // BidCos, and the same address on another interface is another device
        const pmsw = meterPairs([
            {ref: 'BidCos-RF.MEQ0000001:1', model: model('HM-ES-PMSw1-Pl/SWITCH')},
            {ref: 'BidCos-RF.MEQ0000001:2', model: model('HM-ES-PMSw1-Pl/POWERMETER')},
            {ref: 'HmIP-RF.MEQ0000001:2', model: model('HM-ES-PMSw1-Pl/POWERMETER')},
        ]);
        expect([...pmsw.hidden]).toEqual(['BidCos-RF.MEQ0000001:2']);
        expect(meterPairs([{ref: 'HmIP-RF.0001D3C99C0001:7', model: model('HMIP-PSM/ENERGIE_METER_TRANSMITTER')}]).hidden.size).toBe(0);
    });
    it('formats the power: one decimal below 100 W, none above, kW from 1000 W (task 109)', () => {
        expect(formatPower(12.54)).toBe('12.5 W');
        expect(formatPower(12.54, 'de')).toBe('12,5 W');
        expect(formatPower(0.3, 'de')).toBe('0,3 W');
        expect(formatPower(99.94)).toBe('99.9 W');
        expect(formatPower(100)).toBe('100 W');
        expect(formatPower(999.4)).toBe('999 W');
        expect(formatPower(1234, 'de')).toBe('1,23 kW');
        expect(formatPower(12345)).toBe('12.3 kW');
        expect(formatPower(undefined)).toBe('');
        expect(formatPower('7.5')).toBe('7.5 W');
    });
});
