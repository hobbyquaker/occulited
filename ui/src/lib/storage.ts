// Task 69: the Status page's storage health panel - what a device's facts mean for the rows it
// gets. The page translates; the decisions live here so they are tested.
import type {StorageDevice, StorageReport, StorageVerdict} from './api';

/**
 * Task 111: the host watches the disks' health - a VM, a container, a box without smartctl. The panel
 * says so in one quiet line; no SMART was read, so no row says it is missing.
 */
export function hostMonitored(r: Pick<StorageReport, 'health_source'>): boolean {
    return r.health_source === 'host';
}

/** The badge colour of a verdict: the kit's good, warn and bad. */
export function verdictClass(v: StorageVerdict): 'good' | 'warn' | 'bad' {
    return v === 'replace' ? 'bad' : v === 'watch' ? 'warn' : 'good';
}

/**
 * An eMMC life_time value as the range of rated life it stands for: 0x01 is 0-10 %, 0x0A is
 * 90-100 %, 0x0B is past it; 0 means the device does not say.
 */
export function emmcRange(code: number): {from: number; to: number} | 'exceeded' | null {
    if (!code || code < 0) return null;
    if (code >= 0x0b) return 'exceeded';
    return {from: (code - 1) * 10, to: code * 10};
}

/** The width of a wear bar, 0 to 100: an eMMC value's upper bound, or an SSD's percentage. */
export function barWidth(percent: number): number {
    return Math.max(0, Math.min(100, Math.round(percent)));
}

/** The level a wear figure is drawn in: from 80 % watch, from 100 % replace (the Go thresholds). */
export function wearLevel(percent: number): StorageVerdict {
    return percent >= 100 ? 'replace' : percent >= 80 ? 'watch' : 'good';
}

/**
 * What the wear and SMART rows show for a device. A virtual disk gets one note instead of both; an
 * SD card says in both rows that it has no such counter, so an empty field never reads as a
 * healthy one; an eMMC has its own estimate and no SMART; a drive shows what SMART gave, or says
 * that it gave nothing.
 */
export type WearRow = 'virtual' | 'sd' | 'emmc' | 'emmc-silent' | 'smart' | 'unreported' | 'none';
export type SmartRow = 'virtual' | 'sd' | 'value' | 'unavailable' | 'none';

export function rowsFor(d: StorageDevice): {wear: WearRow; smart: SmartRow} {
    if (d.virtual) return {wear: 'virtual', smart: 'virtual'};
    switch (d.kind) {
        case 'sd':
            return {wear: 'sd', smart: 'sd'};
        case 'emmc':
            return {wear: d.emmc ? 'emmc' : 'emmc-silent', smart: 'none'};
        case 'usb':
        case 'sata':
        case 'nvme':
        case 'other':
            if (!d.smart) return {wear: 'none', smart: 'none'};
            return {wear: d.smart.wear_percent != null ? 'smart' : 'unreported', smart: d.smart.available ? 'value' : 'unavailable'};
    }
    return {wear: 'none', smart: 'none'};
}

/** A device's identity after its kind: vendor (unless the model names it), model, capacity. */
export function identity(d: StorageDevice, formatBytes: (n: number) => string): string[] {
    const model = d.model ?? '';
    const vendor = d.vendor && !model.toLowerCase().startsWith(d.vendor.toLowerCase()) ? d.vendor : '';
    return [vendor, model, d.capacity_bytes > 0 ? formatBytes(d.capacity_bytes) : ''].filter(Boolean);
}

/** YYYY-MM as the month the card was made, MM/YYYY. */
export function madeMonth(manufactured: string): string {
    const m = /^(\d{4})-(\d{2})$/.exec(manufactured);
    return m ? `${m[2]}/${m[1]}` : manufactured;
}

/*
 * Task 111 (the maintainer's variant B): each disk's usage panel carries its health - a verdict
 * pill, one row of chips, the reasons in one line and the full facts behind "Details". What a disk
 * does not report is not shown: an SD card gets the write-load chip alone (and errors, if any).
 */

/** one chip of a disk's row, in the row's order: wear, SMART, write load, errors */
export type HealthChip =
    | {kind: 'wear'; percent: number; upTo: boolean; exceeded: boolean; level: StorageVerdict}
    | {kind: 'smart'; passed: boolean}
    | {kind: 'writes'; bytes: number}
    | {kind: 'errors'; count: number};

export function healthChips(d: StorageDevice): HealthChip[] {
    const out: HealthChip[] = [];
    if (!d.virtual) {
        const rows = rowsFor(d);
        if (rows.wear === 'emmc' && d.emmc) {
            // the higher of the two estimates' upper bounds: "up to 20 %"
            const code = Math.max(d.emmc.life_time_a || 0, d.emmc.life_time_b || 0);
            const r = emmcRange(code);
            if (r === 'exceeded') out.push({kind: 'wear', percent: 100, upTo: false, exceeded: true, level: 'replace'});
            else if (r) out.push({kind: 'wear', percent: r.to, upTo: true, exceeded: false, level: code >= 0x09 ? 'watch' : 'good'});
        } else if (rows.wear === 'smart' && d.smart?.wear_percent != null) {
            const p = Math.round(d.smart.wear_percent);
            out.push({kind: 'wear', percent: p, upTo: false, exceeded: false, level: wearLevel(p)});
        }
        if (rows.smart === 'value' && d.smart && d.smart.passed != null) out.push({kind: 'smart', passed: d.smart.passed});
    }
    if (d.writes.per_day_bytes && d.writes.per_day_basis) out.push({kind: 'writes', bytes: d.writes.per_day_bytes});
    if (d.io_errors > 0) out.push({kind: 'errors', count: d.io_errors});
    return out;
}

/** the facts behind "Details" that exist for a disk - none of an SD card's or a silent drive's missing counters */
export function detailRows(d: StorageDevice): {wear: 'emmc' | 'smart' | null; smart: boolean} {
    if (d.virtual) return {wear: null, smart: false};
    const rows = rowsFor(d);
    return {wear: rows.wear === 'emmc' && d.emmc ? 'emmc' : rows.wear === 'smart' ? 'smart' : null, smart: rows.smart === 'value' && !!d.smart};
}

export type Edge = 'ok' | 'warn' | 'error';
const EDGE_RANK: Record<Edge, number> = {ok: 0, warn: 1, error: 2};

/** the panel's edge: the worse of its fullness and its disk's verdict (and a warning that leads to it) */
export function panelEdge(...levels: (string | undefined)[]): Edge {
    let out: Edge = 'ok';
    for (const l of levels) {
        const e: Edge = l === 'replace' || l === 'error' || l === 'err' ? 'error' : l === 'watch' || l === 'warn' ? 'warn' : 'ok';
        if (EDGE_RANK[e] > EDGE_RANK[out]) out = e;
    }
    return out;
}

/**
 * The disk the storage warning's `#storage` lands on: the first panel whose disk has a verdict
 * other than good, else the first panel. Empty when there is no panel.
 */
export function anchorDisk(panels: string[], health: Pick<StorageDevice, 'name' | 'verdict'>[]): string {
    const bad = panels.find((n) => health.some((d) => d.name === n && d.verdict !== 'good'));
    return bad ?? panels[0] ?? '';
}

/**
 * A reason as the disk's own panel says it: the sentence without the leading "<device>: " the
 * warning needs, its first letter upper case. A sentence of another shape is kept as it is.
 */
export function withoutDevice(sentence: string, device: string): string {
    const prefix = `${device}: `;
    if (!sentence.startsWith(prefix)) return sentence;
    const rest = sentence.slice(prefix.length);
    return rest.charAt(0).toUpperCase() + rest.slice(1);
}
