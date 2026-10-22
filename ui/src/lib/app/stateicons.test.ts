import {describe, expect, it} from 'vitest';
import {classify, isOn, kindsOf, lockState, mainAction, stateText, valvePercent, type Description} from './channels';
import {chosenIcon, chosenIcons, switchStateDefault, CONTACT_ICONS, contactDefault, hasTilted, isWindow, KEY_ICONS, KNX, stateIcon, SWITCH_ICONS, switchIconId, thermoFill, thermometerSVG} from './stateicons';
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
    // occulited task 97: every thermostat shows the thermometer, filled from the room temperature; an open valve tints it
    it('a thermostat: the thermometer from the room temperature, with a valve too', () => {
        const etrv: Description = {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7}, ACTUAL_TEMPERATURE: {}, LEVEL: {MAX: 1}};
        expect(icon(etrv, {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 18.5, LEVEL: 0.34})).toMatchObject({name: 'thermometer:50', tone: 'on'});
        expect(icon(etrv, {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 25, LEVEL: 0})).toMatchObject({name: 'thermometer:100', tone: ''});
        expect(icon(etrv, {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 20, LEVEL: 0.5}).svg).toContain('<rect x="160"');
        expect(stateText(classify(etrv), {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 20.5, LEVEL: 0.34}, etrv, t)).toBe('20.5 °C · valve 34 %');
        const rt: Description = {SET_TEMPERATURE: {CONTROL: 'HEATING_CONTROL.SETPOINT', OPERATIONS: 7}, VALVE_STATE: {MAX: 99, UNIT: '%'}};
        expect(valvePercent({VALVE_STATE: 65}, rt)).toBe(65);
        expect(valvePercent({LEVEL: 0.2, VALVE_STATE: 80}, {LEVEL: {MAX: 1}, VALVE_STATE: {MAX: 100}})).toBe(80); // the highest
        const wth: Description = {SET_POINT_TEMPERATURE: {CONTROL: 'HEATING_CONTROL_HMIP.SET_POINT_TEMPERATURE', OPERATIONS: 7}, ACTUAL_TEMPERATURE: {}};
        expect(icon(wth, {SET_POINT_TEMPERATURE: 25}).name).toBe('thermometer:100');
        expect(icon(wth, {SET_POINT_TEMPERATURE: 12}).name).toBe('thermometer:0');
        expect(icon(wth, {SET_POINT_TEMPERATURE: 18.5}).name).toBe('thermometer:50');
        expect(icon(wth, {SET_POINT_TEMPERATURE: 25, ACTUAL_TEMPERATURE: 12}).name).toBe('thermometer:0'); // the room, not the setpoint
        expect(stateText(classify(wth), {SET_POINT_TEMPERATURE: 21, ACTUAL_TEMPERATURE: 19.8}, wth, t)).toBe('set 21.0 °C · 19.8 °C');
    });
    it('draws the thermometer as the spec says', () => {
        expect(thermoFill(5)).toBe(0);
        expect(thermoFill(30)).toBe(1);
        expect(thermometerSVG(0.5)).toContain('<rect x="160" y="160.0" width="19" height="80.0" rx="3" fill="currentColor"/></svg>');
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
        // occulited B-98: the BidCos HM-Sec-SD/-SD-2 (and their team channels) name only DANGER.STATE
        const bidcos: Description = {STATE: {CONTROL: 'DANGER.STATE', OPERATIONS: 5, TYPE: 'BOOL'}, INSTALL_TEST: {OPERATIONS: 4, TYPE: 'ACTION'}};
        expect(icon(bidcos, {STATE: false})).toMatchObject({name: 'secur_smoke_detector', tone: ''});
        expect(icon(bidcos, {STATE: true})).toMatchObject({name: 'secur_smoke_detector', tone: 'alarm'});
        const swsd: Description = {SMOKE_DETECTOR_ALARM_STATUS: {CONTROL: 'DANGER.STATE', VALUE_LIST: ['IDLE_OFF', 'PRIMARY_ALARM']}, SMOKE_DETECTOR_COMMAND: {CONTROL: 'SMOKE_DETECTOR.COMMAND', OPERATIONS: 2}};
        expect(icon(swsd, {SMOKE_DETECTOR_ALARM_STATUS: 1})).toMatchObject({name: 'secur_smoke_detector', tone: 'alarm'});
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

// occulited task 99: a switch's icon - chosen, else a keyword of its name, else a plug's socket, else the light
describe('the switch icon (task 99)', () => {
    const sw: Description = {STATE: {CONTROL: 'SWITCH.STATE', OPERATIONS: 7, TYPE: 'BOOL'}};
    const m = classify(sw);
    it('a switch opens a sheet (task 100)', () => {
        expect(m).toEqual({widget: 'switch', tile: 'act-open', stateKey: 'STATE', commandKey: 'STATE'});
    });
    it('keywords, case-insensitive, also inside a compound; English as whole words', () => {
        expect(switchIconId(undefined, 'Steckdosen Werkstatt')).toBe('message_socket');
        expect(switchIconId(undefined, 'Standby TV')).toBe('message_socket');
        expect(switchIconId(undefined, 'Desk plug')).toBe('message_socket');
        expect(switchIconId(undefined, 'Ventilator Bad')).toBe('vent_ventilation');
        expect(switchIconId(undefined, 'Badlüfter')).toBe('vent_ventilation');
        expect(switchIconId(undefined, 'Ceiling fan')).toBe('vent_ventilation');
        expect(switchIconId(undefined, 'Gartenpumpe')).toBe('sani_pump');
        expect(switchIconId(undefined, 'Pool pump')).toBe('sani_pump');
        expect(switchIconId(undefined, 'Licht Werkstatt')).toBe('light_light_dim_00');
        expect(switchIconId(undefined, 'Licht Stefan')).toBe('light_light_dim_00'); // no fan in Stefan
        // the first match wins: a socket for the fan
        expect(switchIconId(undefined, 'Steckdose Ventilator')).toBe('message_socket');
    });
    it('a chosen icon wins over a keyword; an unknown one is ignored; a plug is a socket without a keyword', () => {
        expect(switchIconId('light_floor_lamp', 'Steckdose Stehlampe')).toBe('light_floor_lamp');
        expect(switchIconId('not_an_icon', 'Steckdose')).toBe('message_socket');
        expect(switchIconId(undefined, 'Werkstatt', 'HmIP-PSM')).toBe('message_socket');
        expect(switchIconId(undefined, 'Pumpe', 'HmIP-PSM')).toBe('sani_pump');
    });
    it('on and off: the light and the socket have their own on picture, the others are tinted', () => {
        expect(stateIcon({model: m, values: {STATE: true}, desc: sw, name: 'Licht'})).toMatchObject({name: 'light_light_dim_100', tone: 'on'});
        expect(stateIcon({model: m, values: {STATE: true}, desc: sw, name: 'Steckdose'})).toMatchObject({name: 'message_socket_on_off', tone: 'on'});
        expect(stateIcon({model: m, values: {STATE: false}, desc: sw, name: 'Steckdose'})).toMatchObject({name: 'message_socket', tone: ''});
        expect(stateIcon({model: m, values: {STATE: true}, desc: sw, name: 'Licht', icon: 'vent_ventilation'})).toMatchObject({name: 'vent_ventilation', tone: 'on'});
        expect(stateIcon({model: m, values: {STATE: false}, desc: sw, icon: 'vent_ventilation'})).toMatchObject({name: 'vent_ventilation', tone: ''});
    });
    it('a switch: a chosen picture per state (task 110), the single icon for both where none, the defaults else', () => {
        const on = {model: m, values: {STATE: true}, desc: sw, name: 'Licht'};
        const off = {model: m, values: {STATE: false}, desc: sw, name: 'Licht'};
        expect(stateIcon({...on, icons: {on: 'light_floor_lamp', off: 'vent_ventilation'}})).toMatchObject({name: 'light_floor_lamp', tone: 'on'});
        expect(stateIcon({...off, icons: {on: 'light_floor_lamp', off: 'vent_ventilation'}})).toMatchObject({name: 'vent_ventilation', tone: ''});
        // one state chosen: the other is the single icon's, or the automatic pair
        expect(stateIcon({...off, icons: {on: 'light_floor_lamp'}})).toMatchObject({name: 'light_light_dim_00'});
        expect(stateIcon({...off, icon: 'sani_pump', icons: {on: 'light_floor_lamp'}})).toMatchObject({name: 'sani_pump'});
        expect(stateIcon({...on, icon: 'sani_pump', icons: {off: 'light_floor_lamp'}})).toMatchObject({name: 'sani_pump', tone: 'on'});
        // an unknown name is ignored; a contact's keys mean nothing to a switch
        expect(stateIcon({...on, icons: {on: 'not_an_icon'}})).toMatchObject({name: 'light_light_dim_100'});
        expect(stateIcon({...on, icons: {open: 'fts_window_2w_open'}})).toMatchObject({name: 'light_light_dim_100'});
        expect(switchStateDefault('on', undefined, 'Steckdose')).toBe('message_socket_on_off');
        expect(switchStateDefault('off', undefined, 'Steckdose')).toBe('message_socket');
        expect(switchStateDefault('on', 'sani_pump', 'Licht')).toBe('sani_pump');
        expect(chosenIcons({control: {icon: 'sani_pump', icons: {on: 'light_floor_lamp', off: 3, open: 'fts_door'}}})).toEqual({on: 'light_floor_lamp', open: 'fts_door'});
    });
    it('a key: the switch symbol, lit while pressed; a chosen icon instead (task 106), an unknown one ignored', () => {
        const vkey: Description = {PRESS_SHORT: {CONTROL: 'BUTTON.SHORT', OPERATIONS: 6}};
        const key = classify(vkey);
        expect(stateIcon({model: key, values: {}, desc: vkey})).toMatchObject({name: 'control_switch_1', tone: ''});
        expect(stateIcon({model: key, values: {}, desc: vkey, pressed: true})).toMatchObject({name: 'control_switch_1', tone: 'on'});
        expect(stateIcon({model: key, values: {}, desc: vkey, icon: 'message_bell_door'})).toMatchObject({name: 'message_bell_door', tone: ''});
        expect(stateIcon({model: key, values: {}, desc: vkey, icon: 'message_bell_door', pressed: true})).toMatchObject({name: 'message_bell_door', tone: 'on'});
        expect(stateIcon({model: key, values: {}, desc: vkey, icon: 'not_an_icon'})).toMatchObject({name: 'control_switch_1'});
        for (const id of KEY_ICONS) expect(KNX[id], id).toContain('currentColor');
    });
    it('a contact: a chosen picture per state (task 105), the defaults where none, the tint by the state', () => {
        const handle: Description = {STATE: {CONTROL: 'RHS.STATE', OPERATIONS: 5, VALUE_LIST: ['CLOSED', 'TILTED', 'OPEN']}};
        const door: Description = {STATE: {CONTROL: 'DOOR_SENSOR.STATE', OPERATIONS: 5, VALUE_LIST: ['CLOSED', 'OPEN']}};
        const h = classify(handle), d = classify(door);
        expect(hasTilted(handle, 'STATE')).toBe(true);
        expect(hasTilted(door, 'STATE')).toBe(false);
        expect(isWindow(handle, 'STATE')).toBe(true);
        expect(isWindow(door, 'STATE')).toBe(false);
        // the defaults as before
        expect(stateIcon({model: h, values: {STATE: 0}, desc: handle})).toMatchObject({name: 'fts_window_1w', tone: ''});
        expect(stateIcon({model: h, values: {STATE: 1}, desc: handle})).toMatchObject({name: 'fts_window_1w_tilt', tone: 'on'});
        expect(stateIcon({model: h, values: {STATE: 2}, desc: handle})).toMatchObject({name: 'fts_window_1w_open', tone: 'on'});
        expect(stateIcon({model: d, values: {STATE: 1}, desc: door})).toMatchObject({name: 'fts_door_open', tone: 'on'});
        // the chosen ones per state; a state without a choice keeps its default; an unknown name is ignored
        const icons = {open: 'fts_window_2w_open', tilted: 'fts_window_2w_tilt'};
        expect(stateIcon({model: h, values: {STATE: 2}, desc: handle, icons})).toMatchObject({name: 'fts_window_2w_open', tone: 'on'});
        expect(stateIcon({model: h, values: {STATE: 1}, desc: handle, icons})).toMatchObject({name: 'fts_window_2w_tilt', tone: 'on'});
        expect(stateIcon({model: h, values: {STATE: 0}, desc: handle, icons})).toMatchObject({name: 'fts_window_1w', tone: ''});
        expect(stateIcon({model: d, values: {STATE: 0}, desc: door, icons: {closed: 'fts_frontdoor'}})).toMatchObject({name: 'fts_frontdoor', tone: ''});
        expect(stateIcon({model: d, values: {STATE: 1}, desc: door, icons: {open: 'not_an_icon'}})).toMatchObject({name: 'fts_door_open'});
        // a door opener (the door widget): the key when open, a chosen picture instead
        const opener: Description = {STATE: {CONTROL: 'DOOROPENER.STATE', OPERATIONS: 7}};
        const o = classify(opener);
        expect(stateIcon({model: o, values: {STATE: true}, desc: opener})).toMatchObject({name: 'fts_door_opener_key', tone: 'on'});
        expect(stateIcon({model: o, values: {STATE: true}, desc: opener, icons: {open: 'fts_yard_gate_2w_open'}})).toMatchObject({name: 'fts_yard_gate_2w_open', tone: 'on'});
        expect(contactDefault('open', true)).toBe('fts_window_1w_open');
        expect(contactDefault('tilted', false)).toBe('fts_door_open');
        expect(contactDefault('open', false, true)).toBe('fts_door_opener_key');
        // the metadata: the icons key, only the three states, only strings
        expect(chosenIcons({control: {icons: {open: 'fts_window_2w_open', tilted: 3, other: 'x'}}})).toEqual({open: 'fts_window_2w_open'});
        expect(chosenIcons({control: {icon: 'sani_pump'}})).toEqual({});
        expect(chosenIcons(undefined)).toEqual({});
        for (const id of CONTACT_ICONS) expect(KNX[id], id).toContain('currentColor');
    });
    it('every picker icon is shipped, and the metadata is read from the control namespace', () => {
        for (const id of SWITCH_ICONS) expect(KNX[id], id).toContain('currentColor');
        expect(chosenIcon({control: {icon: 'sani_pump'}})).toBe('sani_pump');
        expect(chosenIcon({control: {icon: 3}})).toBeUndefined();
        expect(chosenIcon({'ccu-jack': {}})).toBeUndefined();
        expect(chosenIcon(undefined)).toBeUndefined();
    });
});
