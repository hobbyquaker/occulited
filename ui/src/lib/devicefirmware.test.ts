import {describe, expect, it} from 'vitest';
import {archivable, agoParts, bidcosBundle, cannotUpdate, deviceActivity, etaParts, etaRange, filterOthers, filterRows, firmwareRows, hmipFirmwareRows, isLive, shownDevices, rowActivity, summary, switchable, typeKey, type FwBundle, type FwDevice, type FwTransfer} from './devicefirmware';

const dev = (type: string, address: string, firmware: string, more: Partial<FwDevice> = {}): FwDevice => ({interface: 'HmIP-RF', address, type, firmware, update_available: false, ...more});
const bundle = (name: string, version: string, more: Partial<FwBundle> = {}): FwBundle => ({type_code: '1', name, version, files: ['x.efw', 'info'], ...more});

describe('typeKey', () => {
    it('matches as the daemon does', () => {
        expect(typeKey('HMIP-WRC2')).toBe(typeKey('HmIP-WRC2'));
        expect(typeKey('HmIP-HAP_JS1')).toBe('hmip-hap-js1');
        expect(typeKey('HmIP-HAP JS1')).toBe('hmip-hap-js1');
        expect(typeKey(' HM-CC-RT-DN ')).toBe('hm-cc-rt-dn');
    });
});

describe('firmwareRows', () => {
    it('groups the devices by type, case-insensitively, and lists their versions oldest first', () => {
        const {rows} = firmwareRows([dev('HMIP-WRC2', 'A', '1.0.3', {latest: '1.18.2'}), dev('HmIP-WRC2', 'B', '1.18.2', {latest: '1.18.2'}), dev('HmIP-WRC2', 'C', '1.10.0', {latest: '1.18.2'})], []);
        expect(rows).toHaveLength(1);
        expect(rows[0]!.devices.map((d) => d.address)).toEqual(['A', 'B', 'C']);
        expect(rows[0]!.installed).toEqual(['1.0.3', '1.10.0', '1.18.2']);
        expect(rows[0]!.status).toBe('available');
        expect(rows[0]!.version).toBe('1.18.2');
        expect(rows[0]!.behind).toBe(2);
    });

    it('a newer deployed bundle is downloaded, ready to install', () => {
        const {rows, others} = firmwareRows([dev('HmIP-PDT', 'A', '2.2.2', {latest: '2.2.4'})], [bundle('HmIP-PDT', '2.2.4')]);
        expect(rows[0]!.status).toBe('downloaded');
        expect(rows[0]!.version).toBe('2.2.4');
        expect(rows[0]!.bundle?.name).toBe('HmIP-PDT');
        expect(others).toEqual([]);
    });

    it("the interface's available firmware counts as downloaded; its 0.0.0 does not", () => {
        expect(firmwareRows([dev('HmIP-PDT', 'A', '2.2.2', {available_firmware: '2.2.4'})], []).rows[0]!.status).toBe('downloaded');
        expect(firmwareRows([dev('HmIP-PDT', 'A', '2.2.2', {available_firmware: '0.0.0'})], []).rows[0]!.status).toBe('unchecked');
    });

    it("eQ-3's version beyond the downloaded one is still available at eQ-3, naming the downloaded", () => {
        const r = firmwareRows([dev('HmIP-PDT', 'A', '2.2.0', {latest: '2.2.6'})], [bundle('HmIP-PDT', '2.2.4')]).rows[0]!;
        expect(r.status).toBe('available');
        expect(r.version).toBe('2.2.6');
        expect(r.downloaded).toBe('2.2.4');
    });

    it('up to date: the newest known version; a downloaded bundle the devices already run is not an update', () => {
        const r = firmwareRows([dev('HmIP-PDT', 'A', '2.2.4', {latest: '2.2.4'})], [bundle('HmIP-PDT', '2.2.4')]).rows[0]!;
        expect(r.status).toBe('current');
        expect(r.version).toBe('2.2.4');
        expect(r.behind).toBe(0);
    });

    it('a BidCos device reporting major.minor is compared on those two', () => {
        expect(firmwareRows([dev('HM-CC-RT-DN', 'A', '1.5', {interface: 'BidCos-RF', latest: '1.5.3'})], []).rows[0]!.status).toBe('current');
        expect(firmwareRows([dev('HM-CC-RT-DN', 'A', '1.4', {interface: 'BidCos-RF', latest: '1.5.3'})], []).rows[0]!.status).toBe('available');
    });

    it('the three that cannot be updated: not listed, BidCos-Wired, UPDATABLE 0', () => {
        const {rows} = firmwareRows([
            dev('HM-CC-TC', 'A', '2.1', {interface: 'BidCos-RF', not_listed: true}),
            dev('HMW-IO-12-Sw7-DR', 'B', '1.0', {interface: 'BidCos-Wired', not_listed: true}),
            dev('HM-Sec-SC', 'C', '1.0', {interface: 'BidCos-RF', updatable: false}),
        ], []);
        expect(Object.fromEntries(rows.map((r) => [r.type, r.status]))).toEqual({'HM-CC-TC': 'not-listed', 'HMW-IO-12-Sw7-DR': 'image-only', 'HM-Sec-SC': 'not-updatable'});
        expect(rows.every((r) => cannotUpdate(r.status))).toBe(true);
    });

    it('a type eQ-3 does not list with a bundle uploaded by hand is updatable', () => {
        expect(firmwareRows([dev('HM-CC-TC', 'A', '2.1', {interface: 'BidCos-RF', not_listed: true})], [bundle('HM-CC-TC', '2.2')]).rows[0]!.status).toBe('downloaded');
    });

    it('the HmIP-HAP-B1 takes the HmIP-HAP bundle; a bundle no type takes is one of the others', () => {
        const {rows, others} = firmwareRows([dev('HmIP-HAP-B1', 'A', '2.2.18')], [bundle('HmIP-HAP', '2.2.20'), bundle('HmIP-XYZ', '')]);
        expect(rows[0]!.status).toBe('downloaded');
        expect(others.map((b) => b.name)).toEqual(['HmIP-XYZ']);
    });

    it("a version from the update file's name is marked", () => {
        const r = firmwareRows([dev('HmIPW-DRS8', 'A', '1.2.0')], [bundle('HmIPW-DRS8', '', {version_from_name: '1.2.6', date_from_name: '2022-09-28'})]).rows[0]!;
        expect(r.status).toBe('downloaded');
        expect(r.fromName).toEqual({version: '1.2.6', date: '2022-09-28'});
    });

    it("a failed download is the row's", () => {
        const r = firmwareRows([dev('HmIP-PDT', 'A', '2.2.0', {latest: '2.2.4'})], [], [{type: 'HMIP-PDT', version: '2.2.4', action: 'failed', detail: 'HTTP 404'}]).rows[0]!;
        expect(r.failed).toBe('HTTP 404');
    });

    it('lists the types with something to do first, then by name', () => {
        const {rows} = firmwareRows([
            dev('HmIP-B', 'A', '1.0', {latest: '1.0'}),
            dev('HM-CC-TC', 'B', '2.1', {interface: 'BidCos-RF', not_listed: true}),
            dev('HmIP-A', 'C', '1.0', {latest: '1.0'}),
            dev('HmIP-Z', 'D', '1.0', {latest: '1.1'}),
            dev('HmIP-Y', 'E', '1.0'),
        ], [bundle('HmIP-Y', '1.1')]);
        expect(rows.map((r) => r.type)).toEqual(['HmIP-Y', 'HmIP-Z', 'HmIP-A', 'HmIP-B', 'HM-CC-TC']);
    });

    it('sorts the devices by name, unnamed ones last', () => {
        const r = firmwareRows([dev('HmIP-PDT', 'B', '1'), dev('HmIP-PDT', 'A', '1'), dev('HmIP-PDT', 'C', '1', {name: 'Kitchen'})], []).rows[0]!;
        expect(r.devices.map((d) => d.address)).toEqual(['C', 'A', 'B']);
    });
});

describe('filterRows', () => {
    const {rows} = firmwareRows([
        dev('HmIP-PDT', '0001D0000000B1', '2.2.4', {latest: '2.2.4', name: 'Plug kitchen'}),
        dev('HMIP-WRC2', '00010000000A10', '1.0.3', {latest: '1.18.2'}),
        dev('HM-CC-TC', 'JEQ9000001', '2.1', {interface: 'BidCos-RF', not_listed: true, name: 'Kitchen thermostat'}),
    ], []);

    it('hides what cannot be updated unless all are shown', () => {
        expect(filterRows(rows, '', false).shown.map((r) => r.type)).toEqual(['HMIP-WRC2', 'HmIP-PDT']);
        expect(filterRows(rows, '', false).hidden).toBe(1);
        expect(filterRows(rows, '', true).shown).toHaveLength(3);
    });

    it('searches the type, the device names and the addresses', () => {
        expect(filterRows(rows, 'wrc', false).shown.map((r) => r.type)).toEqual(['HMIP-WRC2']);
        const kitchen = filterRows(rows, 'KITCHEN', false);
        expect(kitchen.shown.map((r) => r.type)).toEqual(['HmIP-PDT']);
        expect(kitchen.byDevice.has(rows.find((r) => r.type === 'HmIP-PDT')!.key)).toBe(true);
        expect(kitchen.hidden).toBe(1);
        expect(filterRows(rows, 'jeq9', true).shown.map((r) => r.type)).toEqual(['HM-CC-TC']);
        expect(filterRows(rows, 'nothing', true).shown).toEqual([]);
    });

    it('a match by type does not open the row', () => {
        expect(filterRows(rows, 'pdt', false).byDevice.size).toBe(0);
    });
});

describe('summary', () => {
    it('counts the statuses', () => {
        const {rows} = firmwareRows([dev('A', '1', '1.0', {latest: '1.1'}), dev('B', '2', '1.0', {latest: '1.0'}), dev('C', '3', '1.0'), dev('D', '4', '1.0', {not_listed: true})], [bundle('C', '1.2')]);
        expect(summary(rows)).toEqual({downloaded: 1, transferring: 0, installing: 0, held: 0, available: 1, current: 1, hidden: 1});
    });
});

// task 44: the deployed firmware on its way to HmIP devices - the interface's live state and the
// journal's transfer refine "downloaded" into installing, transferring (with the percent),
// interrupted, queued and ready
describe('live statuses', () => {
    const tr = (state: 'running' | 'interrupted' | 'done', percent: number, more: Partial<FwTransfer> = {}): FwTransfer => ({state, percent, since: '2026-10-14T13:15:35Z', updated: '2026-10-14T13:19:05Z', ...more});
    const deployed = [bundle('HmIP-BBL', '1.10.16')];
    it('a running transfer with its percent', () => {
        const {rows} = firmwareRows([dev('HmIP-BBL', 'A', '1.0.2', {available_firmware: '1.10.16', update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: tr('running', 42)})], deployed);
        expect(rows[0]!.status).toBe('transferring');
        expect(rows[0]!.activity).toEqual({kind: 'transferring', percent: 42, updated: '2026-10-14T13:19:05Z', reason: undefined});
        expect(rows[0]!.version).toBe('1.10.16');
    });
    it('the interface state alone, without the journal, is transferring without a percent', () => {
        const {rows} = firmwareRows([dev('HmIP-BBL', 'A', '1.0.2', {available_firmware: '1.10.16', update_state: 'DELIVER_FIRMWARE_IMAGE'})], deployed);
        expect(rows[0]!.status).toBe('transferring');
        expect(rows[0]!.activity?.percent).toBeUndefined();
    });
    it('a percent of -1 (no progress line yet) is none', () => {
        expect(deviceActivity(dev('HmIP-BBL', 'A', '1.0.2', {update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: tr('running', -1)}))?.percent).toBeUndefined();
    });
    it('the row takes the most active device, the percent the furthest running one', () => {
        const devs = [
            dev('HmIP-BBL', 'A', '1.0.2', {available_firmware: '1.10.16', update_state: 'READY_FOR_UPDATE'}),
            dev('HmIP-BBL', 'B', '1.0.2', {available_firmware: '1.10.16', update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: tr('running', 10)}),
            dev('HmIP-BBL', 'C', '1.0.2', {available_firmware: '1.10.16', update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: tr('running', 60)}),
            dev('HmIP-BBL', 'D', '1.0.2', {available_firmware: '1.10.16', update_state: 'NEW_FIRMWARE_AVAILABLE'}),
        ];
        expect(firmwareRows(devs, deployed).rows[0]!.activity).toMatchObject({kind: 'transferring', percent: 60});
        devs.push(dev('HmIP-BBL', 'E', '1.0.2', {available_firmware: '1.10.16', update_state: 'PERFORMING_UPDATE'}));
        expect(firmwareRows(devs, deployed).rows[0]!.status).toBe('installing');
        expect(rowActivity([devs[0]!, devs[3]!])).toMatchObject({kind: 'queued'});
        expect(rowActivity([devs[0]!])).toMatchObject({kind: 'ready'});
    });
    it('interrupted keeps the percent and the reason; done counts as transferring until the interface says ready', () => {
        expect(deviceActivity(dev('HmIP-BBL', 'A', '1.0.2', {update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: tr('interrupted', 50, {reason: 'unreachable'})}))).toEqual({kind: 'interrupted', percent: 50, updated: '2026-10-14T13:19:05Z', reason: 'unreachable'});
        expect(deviceActivity(dev('HmIP-BBL', 'A', '1.0.2', {update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: tr('done', 100)}))).toMatchObject({kind: 'transferring', percent: 100});
        expect(deviceActivity(dev('HmIP-BBL', 'A', '1.0.2', {update_state: 'UP_TO_DATE'}))).toBeUndefined();
        expect(deviceActivity(dev('HmIP-BBL', 'A', '1.0.2'))).toBeUndefined();
    });
    it('a device up to date, or without a state, keeps the row at downloaded', () => {
        const {rows} = firmwareRows([dev('HmIP-BBL', 'A', '1.0.2', {available_firmware: '1.10.16'}), dev('HmIP-BBL', 'B', '1.10.16', {available_firmware: '1.10.16', update_state: 'UP_TO_DATE'})], deployed);
        expect(rows[0]!.status).toBe('downloaded');
        expect(rows[0]!.activity).toBeUndefined();
    });
    it('the live rows come first, in their order', () => {
        const {rows} = firmwareRows(
            [
                dev('HmIP-BBL', 'A', '1.0.2', {available_firmware: '1.10.16', update_state: 'READY_FOR_UPDATE'}),
                dev('HmIP-PDT', 'B', '2.2.4', {available_firmware: '2.2.6', update_state: 'NEW_FIRMWARE_AVAILABLE'}),
                dev('HmIP-WRC2', 'C', '1.10.0', {available_firmware: '1.18.2', update_state: 'DELIVER_FIRMWARE_IMAGE'}),
                dev('HM-LC-Sw1', 'D', '1.0', {available_firmware: '1.4'}),
                dev('HmIP-SWDO', 'E', '1.4.0', {available_firmware: '1.4.0'}),
            ],
            [bundle('HmIP-BBL', '1.10.16'), bundle('HmIP-PDT', '2.2.6'), bundle('HmIP-WRC2', '1.18.2'), bundle('HM-LC-Sw1', '1.4')],
        );
        expect(rows.map((r) => [r.type, r.status])).toEqual([
            ['HmIP-WRC2', 'transferring'],
            ['HmIP-PDT', 'queued'],
            ['HmIP-BBL', 'ready'],
            ['HM-LC-Sw1', 'downloaded'],
            ['HmIP-SWDO', 'current'],
        ]);
        expect(summary(rows)).toEqual({downloaded: 2, transferring: 2, installing: 0, held: 0, available: 0, current: 1, hidden: 0});
        expect(isLive('ready')).toBe(true);
        expect(isLive('downloaded')).toBe(false);
    });
});

describe('filterOthers', () => {
    const others = [bundle('HmIP-FROLL', '1.0.2', {type_code: '0044', files: ['HmIP-FROLL_update_V1_0_2.efw', 'info']}), bundle('HM-ES-PMSw1-Pl', '2.6', {type_code: '0083', files: ['x.eq3']})];
    it('matches by name, type code, version or file, case-insensitively', () => {
        expect(filterOthers(others, '').length).toBe(2);
        expect(filterOthers(others, 'froll').map((b) => b.name)).toEqual(['HmIP-FROLL']);
        expect(filterOthers(others, '0083').map((b) => b.name)).toEqual(['HM-ES-PMSw1-Pl']);
        expect(filterOthers(others, '2.6').map((b) => b.name)).toEqual(['HM-ES-PMSw1-Pl']);
        expect(filterOthers(others, '.eq3').map((b) => b.name)).toEqual(['HM-ES-PMSw1-Pl']);
        expect(filterOthers(others, 'nothing')).toEqual([]);
    });
});

describe('agoParts', () => {
    const now = Date.parse('2026-10-14T16:00:00Z');
    it('words the age in the unit that fits', () => {
        expect(agoParts('2026-10-14T15:59:40Z', now)).toEqual({unit: 'just', n: 0});
        expect(agoParts('2026-10-14T15:41:00Z', now)).toEqual({unit: 'minutes', n: 19});
        expect(agoParts('2026-10-14T13:19:05Z', now)).toEqual({unit: 'hours', n: 3});
        expect(agoParts('2026-10-11T16:00:00Z', now)).toEqual({unit: 'days', n: 3});
        expect(agoParts(undefined, now)).toBeUndefined();
        expect(agoParts('nope', now)).toBeUndefined();
    });
});

// task 45: a bundle in the held directory makes its type "held back" - unless a device is already
// on its way (a live state wins), and the switch applies to every row with a bundle on the system
describe('held bundles', () => {
    it('a held bundle is the row\'s status, after downloaded and before available', () => {
        const {rows} = firmwareRows(
            [dev('HmIP-BBL', 'A', '1.0.2', {latest: '1.10.16'}), dev('HmIP-PDT', 'B', '2.2.4', {available_firmware: '2.2.6'}), dev('HmIP-WRC2', 'C', '1.10.0', {latest: '1.18.2'})],
            [bundle('HmIP-BBL', '1.10.16', {held: true}), bundle('HmIP-PDT', '2.2.6')],
        );
        expect(rows.map((r) => [r.type, r.status])).toEqual([
            ['HmIP-PDT', 'downloaded'],
            ['HmIP-BBL', 'held'],
            ['HmIP-WRC2', 'available'],
        ]);
        expect(rows[1]!.version).toBe('1.10.16');
        expect(rows[1]!.bundle?.held).toBe(true);
        expect(summary(rows)).toEqual({downloaded: 1, transferring: 0, installing: 0, held: 1, available: 1, current: 0, hidden: 0});
    });
    it('a live state wins over the held mark (the interface still works on it)', () => {
        const {rows} = firmwareRows([dev('HmIP-BBL', 'A', '1.0.2', {available_firmware: '1.10.16', update_state: 'DELIVER_FIRMWARE_IMAGE'})], [bundle('HmIP-BBL', '1.10.16', {held: true})]);
        expect(rows[0]!.status).toBe('transferring');
    });
    it('the switch applies to rows with a bundle on the system', () => {
        expect(['installing', 'transferring', 'interrupted', 'queued', 'ready', 'downloaded', 'held'].every((s) => switchable(s as never))).toBe(true);
        expect(['available', 'unchecked', 'current', 'not-listed', 'image-only', 'not-updatable'].some((s) => switchable(s as never))).toBe(false);
    });
});

// task 48: downloaded, not delivered - the row knows its offered bundle (what Hold back moves) and a
// newer held one (what Deliver to devices moves); every bundle of the type belongs to its row
describe('offered and deliverable bundles', () => {
    it('a newer held download beside an older offered one is deliverable; the older stays the offered one', () => {
        const offered = bundle('HmIP-BBL', '1.10.10', {dir: '332'});
        const held = bundle('HmIP-BBL', '1.10.16', {dir: '332-HmIP-BBL-1.10.16', held: true});
        const {rows, others} = firmwareRows([dev('HmIP-BBL', 'A', '1.0.2', {latest: '1.10.16'})], [offered, held]);
        expect(rows[0]!.status).toBe('held');
        expect(rows[0]!.version).toBe('1.10.16');
        expect(rows[0]!.offered).toBe(offered);
        expect(rows[0]!.deliverable).toBe(held);
        expect(others).toEqual([]);
    });
    it('after the delivery the older one lies held: nothing to deliver, not among the others', () => {
        const offered = bundle('HmIP-BBL', '1.10.16', {dir: '332'});
        const older = bundle('HmIP-BBL', '1.10.10', {dir: '332-HmIP-BBL-1.10.10', held: true});
        const {rows, others} = firmwareRows([dev('HmIP-BBL', 'A', '1.0.2', {latest: '1.10.16'})], [older, offered]);
        expect(rows[0]!.status).toBe('downloaded');
        expect(rows[0]!.offered).toBe(offered);
        expect(rows[0]!.deliverable).toBeUndefined();
        expect(others).toEqual([]);
    });
    it('only held: deliverable, nothing offered; the HAP-B1 takes the HAP\'s bundles', () => {
        const held = bundle('HmIP-HAP', '3.0.18', {held: true});
        const {rows} = firmwareRows([dev('HmIP-HAP-B1', 'A', '2.2.0', {latest: '3.0.18'})], [held]);
        expect(rows[0]!.status).toBe('held');
        expect(rows[0]!.deliverable).toBe(held);
        expect(rows[0]!.offered).toBeUndefined();
    });
});

// task 49: several versions per type, the user's choice of the delivered one
describe('versions', () => {
    it('older firmware is offered for HmIP device types only', () => {
        expect(['HmIP-PDT', 'HMIP-WRC2', 'HmIPW-DRS8', 'ELV-SH-CTH'].every(archivable)).toBe(true);
        expect(['HM-LC-Sw1-Pl-2', 'HMW-IO-12-Sw7-DR', 'HmIP-HAP', 'HmIP-HAP-B1', 'HMIP-HAP JS1', 'HmIPW-DRAP', ''].some(archivable)).toBe(false);
    });
    it('a row lists every bundle of its type, newest first, and is archivable for HmIP-RF devices', () => {
        const {rows} = firmwareRows([dev('HmIP-PDT', 'A', '2.2.4'), dev('HM-LC-Sw1-Pl-2', 'B', '1.9', {interface: 'BidCos-RF'})], [bundle('HmIP-PDT', '1.4.8', {held: true}), bundle('HmIP-PDT', '2.2.4')]);
        const pdt = rows.find((r) => r.type === 'HmIP-PDT')!;
        expect(pdt.bundles.map((b) => b.version)).toEqual(['2.2.4', '1.4.8']);
        expect(pdt.archivable).toBe(true);
        expect(rows.find((r) => r.type === 'HM-LC-Sw1-Pl-2')!.archivable).toBe(false);
    });
    it('an older version delivered on the way to devices that run a newer one shows its live state', () => {
        const {rows} = firmwareRows([dev('HmIP-PDT', 'A', '2.2.4', {available_firmware: '1.4.8', update_state: 'DELIVER_FIRMWARE_IMAGE'})], [bundle('HmIP-PDT', '1.4.8'), bundle('HmIP-PDT', '2.2.4', {held: true})]);
        expect(rows[0]!.status).toBe('transferring');
        expect(rows[0]!.version).toBe('1.4.8');
        expect(rows[0]!.offered?.version).toBe('1.4.8');
        expect(rows[0]!.deliverable?.version).toBe('2.2.4');
    });
});

describe('HmIP devices only (occulited task 60)', () => {
    const devices = [
        dev('HmIP-WRC2', 'H1', '1.0.0', {latest: '1.18.2'}),
        dev('HmIPW-DRS8', 'W1', '1.2.0'),
        dev('HM-CC-RT-DN', 'B1', '1.4', {interface: 'BidCos-RF', latest: '1.5'}),
        dev('HMW-IO-12-Sw7-DR', 'X1', '1.0', {interface: 'BidCos-Wired'}),
        dev('HM-CC-VG-1', 'V1', '', {interface: 'VirtualDevices'}),
    ];
    it('leaves out BidCos-RF, BidCos-Wired and VirtualDevices, keeps HmIP wired', () => {
        expect(shownDevices(devices).map((d) => d.address)).toEqual(['H1', 'W1']);
        expect(hmipFirmwareRows(devices, []).rows.map((r) => r.type).sort()).toEqual(['HmIP-WRC2', 'HmIPW-DRS8']);
    });
    it('a BidCos bundle is not among the other downloaded firmware either', () => {
        expect(bidcosBundle(bundle('HM-CC-RT-DN', '1.5'))).toBe(true);
        expect(bidcosBundle(bundle('HmIP-WRC2', '1.18.2'))).toBe(false);
        const {others} = hmipFirmwareRows(devices, [bundle('HM-CC-RT-DN', '1.5'), bundle('HmIP-PSM', '2.0.0')]);
        expect(others.map((b) => b.name)).toEqual(['HmIP-PSM']);
    });
});

// occulited B-80 and task 72: the multicast transfer's numbers, the time left, the duty-cycle pause
describe('transfer figures, time left and pause (B-80, task 72)', () => {
    const tr = (state: FwTransfer['state'], percent: number, more: Partial<FwTransfer> = {}): FwTransfer => ({state, percent, since: '2026-10-15T18:00:00Z', updated: '2026-10-15T20:00:00Z', ...more});
    const deployed = [bundle('HmIP-BSM', '1.18.24')];
    const bsm = (address: string, t: FwTransfer) => dev('HmIP-BSM', address, '1.18.14', {available_firmware: '1.18.24', update_state: 'DELIVER_FIRMWARE_IMAGE', transfer: t});
    it('a device carries its bytes, its size, the time left and the last progress line', () => {
        const a = deviceActivity(bsm('A', tr('running', 44, {bytes: 45120, size: 101588, progress_at: '2026-10-15T19:58:00Z', eta_seconds: 19200})));
        expect(a).toEqual({kind: 'transferring', percent: 44, updated: '2026-10-15T19:58:00Z', reason: undefined, bytes: 45120, size: 101588, eta: 19200});
        // the multicast handler's blocks
        expect(deviceActivity(bsm('A', tr('running', 24, {bytes: 12, size: 50, unit: 'blocks'})))).toMatchObject({bytes: 12, size: 50, blocks: true});
        // unknown time left, no figures before a progress line
        expect(deviceActivity(bsm('A', tr('running', 44, {bytes: 45120, size: 101588, eta_seconds: null})))?.eta).toBeUndefined();
        expect(deviceActivity(bsm('A', tr('running', -1)))?.bytes).toBeUndefined();
    });
    it('a paused transfer is transferring, paused, since when', () => {
        expect(deviceActivity(bsm('A', tr('paused', 44, {reason: 'duty-cycle', paused_since: '2026-10-15T20:30:38Z'})))).toMatchObject({kind: 'transferring', percent: 44, paused: true, pausedSince: '2026-10-15T20:30:38Z', reason: 'duty-cycle'});
        const {rows} = firmwareRows([bsm('A', tr('paused', 44, {paused_since: '2026-10-15T20:30:38Z'})), bsm('B', tr('paused', 40, {paused_since: '2026-10-15T20:30:39Z'}))], deployed);
        expect(rows[0]!.status).toBe('transferring');
        expect(rows[0]!.activity).toMatchObject({paused: true, percent: 44, pausedSince: '2026-10-15T20:30:38Z', reason: 'duty-cycle'});
    });
    it('a row is paused only when every transferring device is; its figures are the furthest device\'s', () => {
        const a = bsm('A', tr('running', 44, {bytes: 45120, size: 101588, eta_seconds: 19200, progress_at: '2026-10-15T19:00:00Z'}));
        const b = bsm('B', tr('paused', 30, {bytes: 30000, size: 101588, progress_at: '2026-10-15T19:30:00Z'}));
        const act = rowActivity([b, a]);
        expect(act).toMatchObject({kind: 'transferring', percent: 44, bytes: 45120, eta: 19200, updated: '2026-10-15T19:30:00Z'});
        expect(act?.paused).toBeUndefined();
    });
    it('the time left: 10 minutes from an hour on, 5 below, at least 5; unknown otherwise', () => {
        expect(etaParts(5 * 3600 + 20 * 60)).toEqual({h: 5, m: 20});
        expect(etaParts(5 * 3600 + 24 * 60)).toEqual({h: 5, m: 20});
        expect(etaParts(5 * 3600 + 26 * 60)).toEqual({h: 5, m: 30});
        expect(etaParts(59 * 60 + 50)).toEqual({h: 1, m: 0});
        expect(etaParts(57 * 60)).toEqual({h: 0, m: 55});
        expect(etaParts(42 * 60 + 40)).toEqual({h: 0, m: 45});
        expect(etaParts(30)).toEqual({h: 0, m: 5});
        expect(etaParts(0)).toEqual({h: 0, m: 5});
        expect(etaParts(undefined)).toBeUndefined();
        expect(etaParts(null)).toBeUndefined();
        expect(etaParts(-1)).toBeUndefined();
        expect(etaParts(Number.NaN)).toBeUndefined();
    });

    // occulited B-112: before an estimate, the hours at the measured 110-140 bytes/min, as a range
    it('the range a transfer of what is left takes at the measured pace (B-112)', () => {
        // the report: 30 of 87,352 bytes - 87,322 left: 10.4 h at 140 B/min, 13.2 h at 110
        expect(etaRange(87352 - 30)).toEqual({lo: 10, hi: 14});
        // the lab's image from the start: 99,540 bytes - 11.8 to 15.1 h
        expect(etaRange(99540)).toEqual({lo: 11, hi: 16});
        // never a single figure: the floor at the fast rate is always below the ceiling at the slow one
        for (const left of [8400, 16800, 50000, 101588]) {
            const r = etaRange(left)!;
            expect(r.lo, String(left)).toBeLessThan(r.hi);
        }
        // under an hour at the slow rate: hi is 1
        expect(etaRange(6000)).toEqual({lo: 1, hi: 1});
        expect(etaRange(0)).toEqual({lo: 1, hi: 1});
        expect(etaRange(undefined)).toBeUndefined();
        expect(etaRange(null)).toBeUndefined();
        expect(etaRange(-1)).toBeUndefined();
        expect(etaRange(Number.NaN)).toBeUndefined();
    });
});

describe('radio modules (occulited task 65)', () => {
    it('are not listed, nor counted', () => {
        const devices = [
            dev('RPI-RF-MOD', 'M1', '4.4.18', {latest: '4.4.22', update_available: true, radio_module: true}),
            dev('HmIP-RFUSB', 'M2', '4.4.18', {radio_module: true}),
            dev('HmIP-HAP', 'A1', '2.4.6', {latest: '2.4.6'}),
            dev('HmIP-PDT', 'P1', '2.2.4', {latest: '2.2.6', update_available: true}),
        ];
        expect(shownDevices(devices).map((d) => d.address)).toEqual(['A1', 'P1']);
        const {rows} = hmipFirmwareRows(devices, []);
        expect(rows.map((r) => r.type).sort()).toEqual(['HmIP-HAP', 'HmIP-PDT']);
        expect(summary(rows)).toMatchObject({available: 1, current: 1});
    });
});
