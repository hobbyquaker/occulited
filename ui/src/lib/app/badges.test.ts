import {describe, expect, it} from 'vitest';
import {badgesFor, cornerMessages, isMaintenanceKey, isProblem} from './badges';
import {LUCIDE_NAMES, lucideSVG} from './lucide';
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

// occulited task 92: the corner shows the most severe message only, in the maintainer's order
describe('the service-message corner (task 92)', () => {
    const kinds = (keys: string[]) => cornerMessages(keys).map((m) => m.kind);
    it('orders UNREACH > SABOTAGE > ERROR_* > DUTY_CYCLE > LOWBAT > CONFIG_PENDING > UPDATE_PENDING', () => {
        const all = ['UPDATE_PENDING', 'CONFIG_PENDING', 'LOW_BAT', 'DUTY_CYCLE', 'ERROR_CODE', 'SABOTAGE', 'UNREACH'];
        expect(kinds(all)).toEqual(['unreach', 'sabotage', 'error', 'duty', 'lowbat', 'config', 'update']);
        for (let i = 0; i < all.length; i++) expect(cornerMessages(all.slice(0, i + 1))[0]!.key, all.slice(0, i + 1).join()).toBe(all[i]);
    });
    it('knows the BidCos and the HmIP spellings and the error codes', () => {
        expect(kinds(['LOWBAT'])).toEqual(['lowbat']);
        expect(kinds(['DUTYCYCLE'])).toEqual(['duty']);
        expect(kinds(['DEVICE_IN_BOOTLOADER'])).toEqual(['update']);
        expect(kinds(['FAULT_REPORTING'])).toEqual(['error']);
        expect(kinds(['ERROR_ALARM_TEST', 'ERROR_SMOKE_CHAMBER'])).toEqual(['error']);
        // a sabotage reported as an error code is a sabotage
        expect(kinds(['ERROR_SABOTAGE'])).toEqual(['sabotage']);
        expect(kinds(['ERROR_SABOTAGE', 'ERROR_OVERHEAT'])).toEqual(['sabotage', 'error']);
    });
    it('never shows STICKY_UNREACH, nor what is no message', () => {
        expect(cornerMessages(['STICKY_UNREACH'])).toEqual([]);
        expect(kinds(['STICKY_UNREACH', 'LOW_BAT'])).toEqual(['lowbat']);
        expect(cornerMessages(['RSSI_DEVICE'])).toEqual([]);
        expect(cornerMessages([])).toEqual([]);
    });
    it('labels each kind', () => {
        expect(cornerMessages(['UNREACH', 'LOW_BAT']).map((m) => m.label)).toEqual(['not reachable', 'Low battery']);
    });
    it('draws every kind with a Lucide symbol, the stroke as asked', () => {
        for (const k of Object.keys(LUCIDE_NAMES) as (keyof typeof LUCIDE_NAMES)[]) {
            const svg = lucideSVG(k, 2.5);
            expect(svg, k).toMatch(/^<svg [^>]*viewBox="0 0 24 24"/);
            expect(svg, k).toContain('stroke-width="2.5"');
            expect(svg, k).toContain('stroke="currentColor"');
        }
    });
});
