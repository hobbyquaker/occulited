import {describe, expect, it} from 'vitest';
import {classify, isOn, kindsOf, lockState, mainAction, stateText, valvePercent, type Description} from './channels';
import {gaugeSVG, KNX, stateIcon, thermoFill, thermometerSVG} from './stateicons';
import {NAMES} from '../../../scripts/knx-names.mjs';

const t = (k: string, p?: Record<string, string>) => k.replace(/\{(\w+)\}/g, (_, n) => p?.[n] ?? '');
const icon = (desc: Description, values: Record<string, unknown>, parent = '', pressed = false) => stateIcon({model: classify(desc), values, desc, parent, pressed});

describe('the state icons (task 75)', () => {
    it('ships exactly the mapped icons, prepared: currentColor, no white, no ids', () => {
        expect(Object.keys(KNX).sort()).toEqual([...NAMES].sort());
        for (const [name, svg] of Object.entries(KNX)) {
            expect(svg, name).toMatch(/^<svg[^>]*viewBox="0 0 361 361"/);
            expect(svg, name).toContain('currentColor');
            expect(svg, name).not.toMatch(/#fff|\sid=|<\?xml|DOCTYPE/i);
        }
    });
    it('switches: a lamp, a plug a socket', () => {
        const sw: Description = {STATE: {CONTROL: 'SWITCH.STATE', OPERATIONS: 7}};
        expect(icon(sw, {STATE: false})).toMatchObject({name: 'light_light_dim_00', tone: ''});
        expect(icon(sw, {STATE: true})).toMatchObject({name: 'light_light_dim_100', tone: 'on'});
        expect(icon(sw, {STATE: true}, 'HmIP-PSM')).toMatchObject({name: 'message_socket_on_off', tone: 'on'});
        expect(icon(sw, {STATE: false}, 'HM-LC-Sw1-Pl-DN-R1')).toMatchObject({name: 'message_socket', tone: ''});
    });
    it('dimmers in 10 % steps, blinds by the closed part from 10 to 100', () => {
        const dim: Description = {LEVEL: {CONTROL: 'DIMMER.LEVEL', OPERATIONS: 7}};
        expect(icon(dim, {LEVEL: 0}).name).toBe('light_light_dim_00');
        expect(icon(dim, {LEVEL: 0.34})).toMatchObject({name: 'light_light_dim_30', tone: 'on'});
        expect(icon(dim, {LEVEL: 1}).name).toBe('light_light_dim_100');
        const blind: Description = {LEVEL: {CONTROL: 'BLIND.LEVEL', OPERATIONS: 7}, STOP: {CONTROL: 'BLIND.STOP', OPERATIONS: 2}};
        expect(icon(blind, {LEVEL: 0})).toMatchObject({name: 'fts_shutter_100', tone: ''});
        expect(icon(blind, {LEVEL: 0.3})).toMatchObject({name: 'fts_shutter_70', tone: 'on'});
        expect(icon(blind, {LEVEL: 1}).name).toBe('fts_shutter_10');
        expect(icon({LEVEL: {CONTROL: 'RGBW_COLOR.LEVEL', OPERATIONS: 7}}, {LEVEL: 0.7})).toMatchObject({name: 'light_led_stripe_rgb', tone: 'on'});
    });
    it('locks by the value list (HmIP) or STATE (KeyMatic: true = unlocked); the circle never opens the door', () => {
        const dld: Description = {LOCK_STATE: {CONTROL: 'DOOR_LOCK_STATE_TRANSMITTER.LOCK_STATE', VALUE_LIST: ['UNKNOWN', 'LOCKED', 'UNLOCKED']}, LOCK_TARGET_LEVEL: {CONTROL: 'NONE', OPERATIONS: 2, VALUE_LIST: ['LOCKED', 'UNLOCKED', 'OPEN']}};
        expect(icon(dld, {LOCK_STATE: 1})).toMatchObject({name: 'fts_door_locked', tone: ''});
        expect(icon(dld, {LOCK_STATE: 2})).toMatchObject({name: 'fts_door_unlocked', tone: 'on'});
        expect(stateText(classify(dld), {LOCK_STATE: 1}, dld, t)).toBe('Locked');
        expect(isOn(classify(dld), {LOCK_STATE: 1}, dld)).toBe(false);
        expect(mainAction(classify(dld), {LOCK_STATE: 1}, dld)).toEqual({key: 'LOCK_TARGET_LEVEL', value: 1});
        expect(mainAction(classify(dld), {LOCK_STATE: 2}, dld)).toEqual({key: 'LOCK_TARGET_LEVEL', value: 0});
        const key: Description = {STATE: {CONTROL: 'LOCK.STATE', OPERATIONS: 7}, OPEN: {CONTROL: 'LOCK.OPEN', OPERATIONS: 2}};
        expect(icon(key, {STATE: false}).name).toBe('fts_door_locked');
        expect(icon(key, {STATE: true}).name).toBe('fts_door_unlocked');
        expect(stateText(classify(key), {STATE: true}, key, t)).toBe('Unlocked');
        expect(mainAction(classify(key), {STATE: false}, key)).toEqual({key: 'STATE', value: true});
        expect(mainAction(classify(key), {STATE: true}, key)).toEqual({key: 'STATE', value: false});
        expect(lockState({X: 3}, {X: {VALUE_LIST: ['A', 'B', 'C', 'OPEN']}}, 'X')).toBe('open');
    });
    it('a thermostat: the valve gauge (the highest valve) or the setpoint as a filled thermometer', () => {
        const etrv: Description = {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7}, ACTUAL_TEMPERATURE: {}, LEVEL: {MAX: 1}};
        expect(icon(etrv, {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 20.5, LEVEL: 0.34})).toMatchObject({name: 'gauge:34', tone: 'on'});
        expect(icon(etrv, {LEVEL: 0})).toMatchObject({name: 'gauge:0', tone: ''});
        expect(stateText(classify(etrv), {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 20.5, LEVEL: 0.34}, etrv, t)).toBe('20.5 °C · valve 34 %');
        const rt: Description = {SET_TEMPERATURE: {CONTROL: 'HEATING_CONTROL.SETPOINT', OPERATIONS: 7}, VALVE_STATE: {MAX: 99, UNIT: '%'}};
        expect(valvePercent({VALVE_STATE: 65}, rt)).toBe(65);
        expect(valvePercent({LEVEL: 0.2, VALVE_STATE: 80}, {LEVEL: {MAX: 1}, VALVE_STATE: {MAX: 100}})).toBe(80); // the highest
        const wth: Description = {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7}, ACTUAL_TEMPERATURE: {}};
        expect(icon(wth, {SET_POINT_TEMPERATURE: 25}).name).toBe('thermometer:100');
        expect(icon(wth, {SET_POINT_TEMPERATURE: 12}).name).toBe('thermometer:0');
        expect(icon(wth, {SET_POINT_TEMPERATURE: 18.5}).name).toBe('thermometer:50');
        expect(stateText(classify(wth), {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 19.8}, wth, t)).toBe('set 21.0 °C · 19.8 °C');
    });
    it('draws the gauge and the thermometer as the spec says', () => {
        expect(thermoFill(5)).toBe(0);
        expect(thermoFill(30)).toBe(1);
        expect(thermometerSVG(0.5)).toContain('<rect x="160" y="160.0" width="19" height="80.0" rx="3" fill="currentColor"/></svg>');
        const g = gaugeSVG(0);
        expect(g.match(/<line /g)).toHaveLength(7); // six ticks and the needle
        expect(g).toContain('stroke-width="10"');
        // 0 % points at 210°, 100 % at -30°
        expect(gaugeSVG(0)).toContain('x2="128.5" y2="235.0"');
        expect(gaugeSVG(100)).toContain('x2="232.5" y2="235.0"');
    });
    it('contacts: a window tilts, a door opens', () => {
        const rhs: Description = {STATE: {CONTROL: 'RHS.STATE', VALUE_LIST: ['CLOSED', 'TILTED', 'OPEN']}};
        expect(icon(rhs, {STATE: 1})).toMatchObject({name: 'fts_window_1w_tilt', tone: 'on'});
        expect(icon(rhs, {STATE: 2}).name).toBe('fts_window_1w_open');
        expect(icon(rhs, {STATE: 0})).toMatchObject({name: 'fts_window_1w', tone: ''});
        const door: Description = {STATE: {CONTROL: 'DOOR_SENSOR.STATE', VALUE_LIST: ['CLOSED', 'OPEN']}};
        expect(icon(door, {STATE: 0}).name).toBe('fts_door');
        expect(icon(door, {STATE: 1})).toMatchObject({name: 'fts_door_open', tone: 'on'});
        expect(icon({STATE: {CONTROL: 'WIN_SC.STATE'}}, {STATE: true}).name).toBe('fts_window_1w_open');
    });
    it('detectors: motion on, smoke, water and siren in the alarm colour', () => {
        expect(icon({MOTION: {CONTROL: 'MOTIONDETECTOR_TRANSCEIVER.MOTION'}}, {MOTION: true})).toMatchObject({name: 'message_presence_active', tone: 'on'});
        expect(icon({MOTION: {CONTROL: 'MOTIONDETECTOR_TRANSCEIVER.MOTION'}}, {MOTION: false}).name).toBe('message_presence');
        const smoke: Description = {SMOKE_DETECTOR_ALARM_STATUS: {CONTROL: 'SMOKE_DETECTOR.SMOKE_DETECTOR_ALARM_STATUS', VALUE_LIST: ['IDLE_OFF', 'PRIMARY_ALARM']}};
        expect(icon(smoke, {SMOKE_DETECTOR_ALARM_STATUS: 1})).toMatchObject({name: 'secur_smoke_detector', tone: 'alarm'});
        expect(icon(smoke, {SMOKE_DETECTOR_ALARM_STATUS: 0}).tone).toBe('');
        const water: Description = {MOISTURE_DETECTED: {CONTROL: 'WATER_DETECTION_TRANSMITTER.MOISTURE_DETECTED'}, WATERLEVEL_DETECTED: {}};
        expect(icon(water, {MOISTURE_DETECTED: true})).toMatchObject({name: 'sani_leakage', tone: 'alarm'});
        expect(icon(water, {MOISTURE_DETECTED: false})).toMatchObject({name: 'sani_leakage', tone: ''});
        const siren: Description = {ACOUSTIC_ALARM_SELECTION: {CONTROL: 'ALARM_SWITCH_VIRTUAL_RECEIVER.ACOUSTIC_ALARM_SELECTION'}, ACOUSTIC_ALARM_ACTIVE: {}};
        expect(icon(siren, {ACOUSTIC_ALARM_ACTIVE: true})).toMatchObject({name: 'secur_alarm_alarm', tone: 'alarm'});
        expect(icon(siren, {ACOUSTIC_ALARM_ACTIVE: false})).toMatchObject({name: 'secur_alarm', tone: ''});
    });
    it('readings, power, keys and door openers', () => {
        expect(icon({TEMPERATURE: {}, HUMIDITY: {}}, {TEMPERATURE: 18.5}).name).toBe('thermometer:50');
        expect(icon({HUMIDITY: {CONTROL: 'WEATHER_TRANSMIT.HUMIDITY'}}, {HUMIDITY: 54}).name).toBe('weather_humidity_rel');
        expect(icon({ILLUMINATION: {CONTROL: 'BRIGHTNESS_TRANSMITTER.ILLUMINATION'}}, {}).name).toBe('weather_light_meter');
        expect(icon({RAINING: {CONTROL: 'RAIN_DETECTION_TRANSMITTER.RAINING'}}, {RAINING: true})).toMatchObject({name: 'weather_rain', tone: 'on'});
        expect(icon({HUMIDITY: {CONTROL: 'SOIL_MOISTURE_TRANSMITTER.HUMIDITY'}}, {}).name).toBe('weather_humidity_soil');
        expect(icon({ACTUAL_TEMPERATURE: {CONTROL: 'WEATHER_TRANSMIT.TEMPERATURE'}, WIND_SPEED: {}}, {}).name).toBe('weather_wind_speed');
        expect(icon({CONCENTRATION: {CONTROL: 'CARBON_DIOXIDE_RECEIVER.CONCENTRATION'}}, {}).name).toBe('weather_station_generic');
        expect(icon({POWER: {CONTROL: 'POWERMETER_IGL.POWER'}}, {POWER: 12})).toMatchObject({name: 'measure_power_meter', tone: 'on'});
        expect(icon({POWER: {CONTROL: 'POWERMETER_IGL.POWER'}}, {POWER: 0}).tone).toBe('');
        const vkey: Description = {PRESS_SHORT: {CONTROL: 'BUTTON.SHORT', OPERATIONS: 6}};
        expect(icon(vkey, {})).toMatchObject({name: 'control_switch_1', tone: ''});
        expect(icon(vkey, {}, '', true).tone).toBe('on');
        const opener: Description = {STATE: {CONTROL: 'DOOROPENER.STATE', OPERATIONS: 7}};
        expect(icon(opener, {STATE: false}).name).toBe('fts_door');
        expect(icon(opener, {STATE: true})).toMatchObject({name: 'fts_door_opener_key', tone: 'on'});
    });
    it('every widget kind of the model has an icon', () => {
        expect(kindsOf({A: {CONTROL: 'X.A'}, B: {CONTROL: 'NONE'}})).toEqual(new Set(['X']));
        for (const w of ['switch', 'dimmer', 'blind', 'thermostat', 'color', 'lock', 'door', 'button', 'contact', 'motion', 'reading', 'power', 'smoke', 'signal'] as const) {
            const s = stateIcon({model: {widget: w, tile: 'read', stateKey: 'STATE', commandKey: ''}, values: {}});
            expect(s.svg, w).toMatch(/^<svg/);
        }
    });
});
