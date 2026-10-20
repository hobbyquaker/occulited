// occulited task 78: the Status page's storage panels - one per disk, a ring per partition. The
// maintainer, 2026-10-16: this panel alone counts 1 GB = 10^9 bytes, so a 16 GB card says 16 GB;
// every other view keeps its binary units.

/** a disk of GET /status's `storage` */
export interface StatusPartition {
    name: string;
    mount: string;
    role: 'root' | 'boot' | 'userfs' | 'other';
    label?: string;
    read_only?: boolean;
    size_bytes: number;
    total_bytes: number;
    used_bytes: number;
}
export interface StatusDevice {
    name: string;
    kind: string;
    vendor?: string;
    model?: string;
    virtual?: boolean;
    size_bytes: number;
    unallocated_bytes: number;
    partitions: StatusPartition[];
}

const UNITS = ['MB', 'GB', 'TB'] as const;
type Unit = (typeof UNITS)[number];
const FACTOR: Record<Unit, number> = {MB: 1e6, GB: 1e9, TB: 1e12};

/** the unit a byte count reads in: MB below 1 GB, GB below 1 TB, then TB (decimal) */
export function decimalUnit(bytes: number): Unit {
    return bytes >= 1e12 ? 'TB' : bytes >= 1e9 ? 'GB' : 'MB';
}

/** the figure in a unit: one decimal below 10, whole from 10 up ("2,1", "16", "268") */
export function decimalFigure(bytes: number, unit: Unit, lang: string): string {
    const v = bytes / FACTOR[unit];
    const digits = v < 10 && unit !== 'MB' ? 1 : 0;
    return new Intl.NumberFormat(lang === 'de' ? 'de-DE' : 'en-US', {minimumFractionDigits: digits, maximumFractionDigits: digits}).format(v);
}

/** a byte count in decimal units: 16 000 000 000 → "16 GB", 268 435 456 → "268 MB" */
export function formatDecimal(bytes: number, lang: string): string {
    const u = decimalUnit(bytes);
    return `${decimalFigure(bytes, u, lang)} ${u}`;
}

/** used and total in the total's unit: ["1,5", "2,0", "GB"] */
export function usedOfTotal(used: number, total: number, lang: string): [string, string, Unit] {
    const u = decimalUnit(total);
    return [decimalFigure(used, u, lang), decimalFigure(total, u, lang), u];
}

/** the share in use, whole per cent */
export function usedPct(p: {total_bytes: number; used_bytes: number}): number {
    return p.total_bytes > 0 ? Math.round((p.used_bytes / p.total_bytes) * 100) : 0;
}

/** a USB disk's name as it reports it, vendor and model, without the vendor twice */
export function deviceModel(d: {vendor?: string; model?: string}): string {
    const v = (d.vendor ?? '').trim();
    const m = (d.model ?? '').trim();
    if (!v) return m;
    if (!m) return v;
    return m.toLowerCase().startsWith(v.toLowerCase()) ? m : `${v} ${m}`;
}

/** the rings' order, outermost first (the daemon sends them so) */
export const ROLE_ORDER: StatusPartition['role'][] = ['userfs', 'root', 'boot', 'other'];
const ROLE_SLOT: Record<string, number> = {userfs: 1, root: 2, boot: 3};

/**
 * Each partition's colour token: the userfs, the system and the boot partition always the same hue
 * (slots 1-3), any other partition (a stick's) the free slots in order - so a stick's only
 * partition is slot 1, as the userfs is on the card. Four slots; a fifth ring repeats none of them
 * but is not drawn (MAX_RINGS).
 */
export function ringColours(d: StatusDevice): string[] {
    const taken = new Set(d.partitions.map((p) => ROLE_SLOT[p.role]).filter(Boolean));
    const free = [1, 2, 3, 4].filter((n) => !taken.has(n));
    let k = 0;
    return d.partitions.map((p) => `var(--ol-series-${ROLE_SLOT[p.role] ?? free[k++] ?? 4})`);
}

/** the rings drawn: at most four, the outermost first */
export const MAX_RINGS = 4;

/** the unallocated space worth a line: at least 1 GB and 5 % of the disk */
export function unallocatedShown(d: StatusDevice): boolean {
    return d.unallocated_bytes >= 1e9 && d.unallocated_bytes >= d.size_bytes * 0.05;
}

/** the warning level of a writable partition - a read-only one fills only with an update */
export function partitionLevel(p: StatusPartition): 'ok' | 'warn' | 'error' {
    if (p.read_only) return 'ok';
    const pct = usedPct(p);
    return pct >= 92 ? 'error' : pct >= 80 ? 'warn' : 'ok';
}

export function deviceLevel(d: StatusDevice): 'ok' | 'warn' | 'error' {
    const ls = d.partitions.map(partitionLevel);
    return ls.includes('error') ? 'error' : ls.includes('warn') ? 'warn' : 'ok';
}
