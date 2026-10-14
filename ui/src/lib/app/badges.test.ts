import {describe, expect, it} from 'vitest';
import {badgesFor, isMaintenanceKey, isProblem} from './badges';
import {messageLook} from '../servicemessages';

// occulited B-46: "not reachable" follows UNREACH alone; STICKY_UNREACH without it is a mild note
// about the past
describe('the reachability badges', () => {
    it('draws UNREACH as the filled red broken link, with or without the sticky flag', () => {
        expect(badgesFor(['UNREACH'])).toEqual([{key: 'UNREACH', icon: 'unlink', tone: 'red', label: 'not reachable', solid: true, mild: undefined}]);
        // both set: one badge, the present one
        expect(badgesFor(['STICKY_UNREACH', 'UNREACH']).map((b) => b.label)).toEqual(['not reachable']);
    });
    it('shows a device that answers again as "was unreachable": the grey clock turned back, mild', () => {
        expect(badgesFor(['STICKY_UNREACH'])).toEqual([{key: 'STICKY_UNREACH', icon: 'history', tone: 'grey', label: 'was unreachable', solid: undefined, mild: true}]);
        // beside another message it keeps its place, the first in the row
        expect(badgesFor(['LOWBAT', 'STICKY_UNREACH']).map((b) => b.label)).toEqual(['was unreachable', 'Low battery']);
    });
    it('counts only what is wrong now as a problem for the rollup', () => {
        expect(isProblem(['STICKY_UNREACH'])).toBe(false);
        expect(isProblem(['STICKY_UNREACH', 'LOWBAT'])).toBe(true);
        expect(isProblem(['UNREACH', 'STICKY_UNREACH'])).toBe(true);
        expect(isProblem([])).toBe(false);
    });
    it('still takes both keys from the :0 channel\'s events', () => {
        expect(isMaintenanceKey('UNREACH')).toBe(true);
        expect(isMaintenanceKey('STICKY_UNREACH')).toBe(true);
        expect(isMaintenanceKey('RSSI_DEVICE')).toBe(false);
    });
});

// occulited task 43: one icon and colour per message, the Status page's and homematic-manager's
describe('the badges look like the Status page', () => {
    it('takes each message\'s own icon and colour, in the row\'s order', () => {
        const keys = ['DUTY_CYCLE', 'ERROR_OVERHEAT', 'FAULT_REPORTING', 'SABOTAGE', 'UPDATE_PENDING', 'CONFIG_PENDING', 'LOWBAT'];
        expect(badgesFor(keys).map((b) => [b.key, b.icon, b.tone])).toEqual([
            ['LOWBAT', 'battery', 'orange'],
            ['CONFIG_PENDING', 'settings', 'blue'],
            ['UPDATE_PENDING', 'arrow-up', 'blue'],
            ['SABOTAGE', 'shield', 'red'],
            ['FAULT_REPORTING', 'x-circle', 'red'],
            ['ERROR_OVERHEAT', 'alert', 'red'],
            ['DUTY_CYCLE', 'activity', 'red'],
        ]);
    });
    it('is the same look the Status page draws for every key', () => {
        for (const k of ['UNREACH', 'STICKY_UNREACH', 'LOW_BAT', 'CONFIG_PENDING', 'DEVICE_IN_BOOTLOADER', 'ERROR_SABOTAGE', 'FAULT_REPORTING', 'ERROR_CODE', 'DUTYCYCLE']) {
            const [b] = badgesFor([k]);
            expect(b && {icon: b.icon, tone: b.tone}).toEqual(messageLook(k));
        }
    });
});
