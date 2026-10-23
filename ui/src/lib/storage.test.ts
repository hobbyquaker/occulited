import {describe, expect, it} from 'vitest';
import type {StorageDevice} from './api';
import {anchorDisk, barWidth, detailRows, emmcRange, healthChips, hostMonitored, identity, madeMonth, panelEdge, rowsFor, verdictClass, wearLevel, withoutDevice} from './storage';
import {formatBytes} from './netpanels';

const base: StorageDevice = {name: 'mmcblk0', kind: 'sd', capacity_bytes: 63864569856, mounts: ['/'], filesystems: [], io_errors: 0, writes: {since_boot_bytes: 0}, verdict: 'good', reasons: []};

describe('the storage panel', () => {
    it('colours the verdicts', () => {
        expect(verdictClass('good')).toBe('good');
        expect(verdictClass('watch')).toBe('warn');
        expect(verdictClass('replace')).toBe('bad');
    });

    it('reads an eMMC life_time value as a range', () => {
        expect(emmcRange(0)).toBeNull();
        expect(emmcRange(0x01)).toEqual({from: 0, to: 10});
        expect(emmcRange(0x02)).toEqual({from: 10, to: 20});
        expect(emmcRange(0x09)).toEqual({from: 80, to: 90});
        expect(emmcRange(0x0a)).toEqual({from: 90, to: 100});
        expect(emmcRange(0x0b)).toBe('exceeded');
    });

    it('draws wear in the thresholds of the verdict', () => {
        expect(barWidth(-3)).toBe(0);
        expect(barWidth(83.4)).toBe(83);
        expect(barWidth(140)).toBe(100);
        expect(wearLevel(79)).toBe('good');
        expect(wearLevel(80)).toBe('watch');
        expect(wearLevel(100)).toBe('replace');
    });

    it('gives an SD card its two notes, a virtual disk one, an eMMC its own estimate', () => {
        expect(rowsFor(base)).toEqual({wear: 'sd', smart: 'sd'});
        expect(rowsFor({...base, kind: 'virtio', virtual: true})).toEqual({wear: 'virtual', smart: 'virtual'});
        // a QEMU disk on the SATA bus is virtual too
        expect(rowsFor({...base, kind: 'sata', virtual: true, smart: {available: true, passed: true, read: ''}})).toEqual({wear: 'virtual', smart: 'virtual'});
        expect(rowsFor({...base, kind: 'emmc', emmc: {life_time_a: 2, life_time_b: 1, pre_eol: 'warning'}})).toEqual({wear: 'emmc', smart: 'none'});
        expect(rowsFor({...base, kind: 'emmc'})).toEqual({wear: 'emmc-silent', smart: 'none'});
    });

    it('shows what SMART gave a drive, and says when it gave nothing', () => {
        expect(rowsFor({...base, kind: 'nvme', smart: {available: true, passed: true, wear_percent: 83, read: ''}})).toEqual({wear: 'smart', smart: 'value'});
        expect(rowsFor({...base, kind: 'sata', smart: {available: true, passed: true, read: ''}})).toEqual({wear: 'unreported', smart: 'value'});
        expect(rowsFor({...base, kind: 'usb', smart: {available: false, message: 'Unknown USB bridge', read: ''}})).toEqual({wear: 'unreported', smart: 'unavailable'});
        // not read at all (a development root)
        expect(rowsFor({...base, kind: 'usb'})).toEqual({wear: 'none', smart: 'none'});
    });

    it('says the host monitors the disks only when the report says so (task 111)', () => {
        expect(hostMonitored({health_source: 'host'})).toBe(true);
        expect(hostMonitored({health_source: 'device'})).toBe(false);
        // a daemon from before the field reads its own disks
        expect(hostMonitored({})).toBe(false);
        // the host's disk a container lists had no SMART read: no row says one is missing
        expect(rowsFor({...base, kind: 'sata'})).toEqual({wear: 'none', smart: 'none'});
    });

    it('names a device without saying the vendor twice', () => {
        expect(identity({...base, vendor: 'SanDisk', model: 'SN64G'}, formatBytes)).toEqual(['SanDisk', 'SN64G', '63.9 GB']);
        expect(identity({...base, kind: 'virtio', vendor: 'QEMU', model: 'QEMU HARDDISK', capacity_bytes: 34359738368}, formatBytes)).toEqual(['QEMU HARDDISK', '34.4 GB']);
        expect(identity({...base, model: 'WD Blue SN570 500GB', capacity_bytes: 0}, formatBytes)).toEqual(['WD Blue SN570 500GB']);
        expect(madeMonth('2024-06')).toBe('06/2024');
        expect(madeMonth('06/2024')).toBe('06/2024');
    });
});

// task 111: the disk's health in its usage panel - chips, edge, anchor, the reasons' words
describe('the health in the usage panel', () => {
    it('gives an SD card the write load alone, and errors when the kernel counted any', () => {
        const sd = {...base, writes: {since_boot_bytes: 1, per_day_bytes: 8200000, per_day_days: 14, per_day_basis: 'samples' as const}};
        expect(healthChips(sd)).toEqual([{kind: 'writes', bytes: 8200000}]);
        expect(healthChips({...sd, io_errors: 3}).map((c) => c.kind)).toEqual(['writes', 'errors']);
        // no per-day figure yet: no chip
        expect(healthChips(base)).toEqual([]);
    });

    it('reads an eMMC as "up to" the higher of its two ranges', () => {
        const emmc = {...base, kind: 'emmc' as const, emmc: {life_time_a: 2, life_time_b: 1}};
        expect(healthChips(emmc)[0]).toEqual({kind: 'wear', percent: 20, upTo: true, exceeded: false, level: 'good'});
        expect(healthChips({...emmc, emmc: {life_time_a: 9, life_time_b: 1}})[0]).toMatchObject({percent: 90, level: 'watch'});
        expect(healthChips({...emmc, emmc: {life_time_a: 0x0b, life_time_b: 1}})[0]).toMatchObject({exceeded: true, level: 'replace'});
        expect(healthChips({...base, kind: 'emmc'})).toEqual([]);
    });

    it('gives a drive its wear and SMART chips only when it reports them', () => {
        const ssd = {...base, kind: 'usb' as const, smart: {available: true, passed: false, wear_percent: 88, read: ''}};
        expect(healthChips(ssd)).toEqual([{kind: 'wear', percent: 88, upTo: false, exceeded: false, level: 'watch'}, {kind: 'smart', passed: false}]);
        expect(healthChips({...ssd, smart: {available: false, read: ''}})).toEqual([]);
        expect(healthChips({...ssd, virtual: true})).toEqual([]);
        expect(detailRows(ssd)).toEqual({wear: 'smart', smart: true});
        expect(detailRows(base)).toEqual({wear: null, smart: false});
    });

    it('takes the worse of fullness, verdict and a warning for the edge', () => {
        expect(panelEdge('ok', 'good')).toBe('ok');
        expect(panelEdge('ok', 'watch')).toBe('warn');
        expect(panelEdge('warn', 'replace')).toBe('error');
        expect(panelEdge('error', 'watch')).toBe('error');
        expect(panelEdge('ok', undefined, 'err')).toBe('error');
    });

    it('anchors the warning on the first disk that is not good', () => {
        const h = [{name: 'sda', verdict: 'good' as const}, {name: 'nvme0n1', verdict: 'watch' as const}];
        expect(anchorDisk(['sda', 'nvme0n1'], h)).toBe('nvme0n1');
        expect(anchorDisk(['sda'], h)).toBe('sda');
        expect(anchorDisk([], h)).toBe('');
    });

    it("drops the disk's name from a reason and starts it upper case", () => {
        expect(withoutDevice("SD card SN64G: the drive's own health check failed.", 'SD card SN64G')).toBe("The drive's own health check failed.");
        expect(withoutDevice('something else', 'SD card SN64G')).toBe('something else');
    });
});
