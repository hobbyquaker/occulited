// Task 40 (the maintainer, 2026-10-13: "we currently have 3 panels for that. that is verwirrend ...
// filter out devices that cant be updated ... make it clearer which updates are downloaded/available.
// add a search filter"): the Firmware page's one device-firmware view. GET /firmware's three lists -
// the paired devices, the last run per type, the bundles deployed in /etc/config/firmware - become
// one row per paired device type with one status, and the bundles no paired type takes stay
// visible beside it.
import {newerFirmware, shownVersion} from './fwversion';

export interface FwDevice {
    interface: string;
    address: string;
    type: string;
    firmware: string;
    available_firmware?: string;
    latest?: string;
    not_listed?: boolean;
    update_available: boolean;
    updatable?: boolean;
    /** the device's name in the metadata store (task 40) */
    name?: string;
    /** the interface's FIRMWARE_UPDATE_STATE as last read (task 44) */
    update_state?: string;
    /** the background transfer as hmipserver's journal told it (task 44) */
    transfer?: FwTransfer;
}
export interface FwTransfer {
    state: 'running' | 'interrupted' | 'done';
    /** bytes of size, whole numbers; -1 until a progress line was seen */
    percent: number;
    bytes?: number;
    size?: number;
    since: string;
    updated: string;
    reason?: string;
}
export interface FwBundle {
    type_code: string;
    dir?: string;
    name: string;
    version: string;
    version_from_name?: string;
    date_from_name?: string;
    files: string[];
    /** the bundle lies in the held directory, not offered to the interfaces (task 45) */
    held?: boolean;
}
export interface FwResult {
    type: string;
    version: string;
    version_from_name?: string;
    date_from_name?: string;
    action: string;
    detail?: string;
}

/**
 * One status per type, in the order the view lists them. The first five are the live ones of a
 * deployed firmware on its way to HmIP devices (task 44: the interface's states, the percent from
 * hmipserver's journal), a row taking the most active state among its devices:
 * - `installing`: a device flashes the firmware (DO_UPDATE_PENDING, PERFORMING_UPDATE);
 * - `transferring`: the background transfer to a device runs (DELIVER_FIRMWARE_IMAGE), with the percent when known;
 * - `interrupted`: hmipserver gave the device up for now and continues later;
 * - `queued`: the deployed firmware waits for its turn (NEW_FIRMWARE_AVAILABLE);
 * - `ready`: transferred, waiting for the install in the frontend (READY_FOR_UPDATE);
 * then the ones the check knows:
 * - `downloaded`: a newer firmware is deployed in /etc/config/firmware, ready to install in the frontend (BidCos, or no live state);
 * - `held`: a newer firmware is on the system but held back - downloaded, not delivered: not offered to the interfaces, not transferred (tasks 45, 48);
 * - `available`: eQ-3 has a newer one that is not downloaded yet;
 * - `unchecked`: nothing is known yet (no check has read eQ-3's list, no bundle);
 * - `current`: every device runs the newest known firmware;
 * the three that cannot be updated here, hidden unless all devices are shown:
 * - `not-listed`: eQ-3's list has no firmware for the type;
 * - `image-only`: BidCos-Wired, whose firmware comes with the system image;
 * - `not-updatable`: the interface says the devices cannot be updated over the air (UPDATABLE 0).
 */
export type FwStatus = 'installing' | 'transferring' | 'interrupted' | 'queued' | 'ready' | 'downloaded' | 'held' | 'available' | 'unchecked' | 'current' | 'not-listed' | 'image-only' | 'not-updatable';

const ORDER: Record<FwStatus, number> = {installing: 0, transferring: 1, interrupted: 2, queued: 3, ready: 4, downloaded: 5, held: 6, available: 7, unchecked: 8, current: 9, 'not-listed': 10, 'image-only': 11, 'not-updatable': 12};

/** The statuses whose row has a bundle the per-type switch applies to (task 45). */
export function switchable(s: FwStatus): boolean {
    return isLive(s) || s === 'downloaded' || s === 'held';
}

/** The live statuses: a deployed firmware on its way to the devices, or installing. */
export type FwLive = 'installing' | 'transferring' | 'interrupted' | 'queued' | 'ready';
const LIVE_ORDER: Record<FwLive, number> = {installing: 0, transferring: 1, interrupted: 2, queued: 3, ready: 4};

/** What is going on with one device, from its live state and transfer (task 44); undefined for nothing. */
export interface FwActivity {
    kind: FwLive;
    /** the transfer's percent, when a progress line was seen */
    percent?: number;
    /** the last line of the transfer */
    updated?: string;
    /** why it waits: the device did not answer */
    reason?: string;
}

/** A device's activity from the interface's state and the journal's transfer. */
export function deviceActivity(d: FwDevice): FwActivity | undefined {
    const st = d.update_state ?? '';
    const tr = d.transfer;
    const pct = tr && tr.percent >= 0 ? tr.percent : undefined;
    if (st === 'DO_UPDATE_PENDING' || st === 'PERFORMING_UPDATE' || st === 'LIVE_DELIVER_FIRMWARE_IMAGE') return {kind: 'installing'};
    if (tr?.state === 'interrupted') return {kind: 'interrupted', percent: pct, updated: tr.updated, reason: tr.reason};
    if (tr?.state === 'running' || st === 'DELIVER_FIRMWARE_IMAGE') return {kind: 'transferring', percent: pct, updated: tr?.updated, reason: tr?.reason};
    if (st === 'NEW_FIRMWARE_AVAILABLE') return {kind: 'queued'};
    if (st === 'READY_FOR_UPDATE') return {kind: 'ready'};
    return undefined;
}

/**
 * A type's activity: the most active of its devices' (installing before transferring before
 * interrupted, queued, ready); the percent is the furthest running transfer's.
 */
export function rowActivity(devices: FwDevice[]): FwActivity | undefined {
    let best: FwActivity | undefined;
    for (const d of devices) {
        const a = deviceActivity(d);
        if (!a) continue;
        if (!best || LIVE_ORDER[a.kind] < LIVE_ORDER[best.kind]) best = {...a};
        else if (a.kind === best.kind) {
            if ((a.percent ?? -1) > (best.percent ?? -1)) best.percent = a.percent;
            if ((a.updated ?? '') > (best.updated ?? '')) best.updated = a.updated;
        }
    }
    return best;
}

/** Whether a status is one of the live ones. */
export function isLive(s: FwStatus): s is FwLive {
    return s in LIVE_ORDER;
}

/** The statuses whose types the view hides by default. */
export function cannotUpdate(s: FwStatus): boolean {
    return s === 'not-listed' || s === 'image-only' || s === 'not-updatable';
}

export interface FwRow {
    /** the type's key (typeKey), unique per row */
    key: string;
    /** the type as the devices report it */
    type: string;
    /** the interfaces of the type's devices, sorted */
    interfaces: string[];
    devices: FwDevice[];
    /** the firmware versions the devices run, distinct, oldest first */
    installed: string[];
    status: FwStatus;
    /** the version the status is about: the downloaded one, eQ-3's newer one, or the current one */
    version: string;
    /** the version is read from the update file's name (the bundle's info states none) */
    fromName?: {version: string; date?: string};
    /** eQ-3's version for the type, when the check read it */
    latest: string;
    /** the version deployed in /etc/config/firmware or offered by the interface, when there is one */
    downloaded: string;
    /** the deployed bundle of the type: the newest, offered or held */
    bundle?: FwBundle;
    /** the type's newest offered bundle (in /etc/config/firmware): what Hold back moves (task 48) */
    offered?: FwBundle;
    /** the type's newest held bundle when it is newer than the offered one: what Deliver to devices moves (task 48) */
    deliverable?: FwBundle;
    /** every bundle of the type on the system, offered and held, newest first (task 49) */
    bundles: FwBundle[];
    /** older versions can be chosen for the type: HmIP devices, not an access point (task 49) */
    archivable: boolean;
    /** devices that run an older firmware than `version` */
    behind: number;
    /** the last check's outcome for the type when it failed */
    failed?: string;
    /** what is going on with the type's devices (task 44), for the live statuses */
    activity?: FwActivity;
}

/**
 * Whether older firmware is offered for a type (internal/firmware Archivable, task 49): HmIP device
 * types, not BidCos (HM-…, HMW-…) and not the access points (HmIP-HAP…, HmIPW-DRAP…).
 */
export function archivable(type: string): boolean {
    const k = typeKey(type);
    return !!k && !/^(hm|hmw)-/.test(k) && !/^(hmip-hap|hmipw-drap|hmip-drap)/.test(k);
}

/** A device type as eQ-3's WebUI and the daemon match it (internal/firmware TypeKey). */
export function typeKey(t: string): string {
    let k = t.trim().toLowerCase();
    const i = k.lastIndexOf('_');
    if (i >= 0) k = k.slice(0, i) + ' ' + k.slice(i + 1);
    if (k === 'hmip-hap js1') k = 'hmip-hap-js1';
    return k;
}

// a type that uses another type's firmware (internal/firmware typeFallback): the HmIP-HAP-B1 the HmIP-HAP's
const FALLBACK: Record<string, string> = {'hmip-hap-b1': 'hmip-hap'};

function max(vs: string[]): string {
    let best = '';
    for (const v of vs) if (v && (!best || newerFirmware(v, best))) best = v;
    return best;
}

/** A bundle's version: the one its info states, or the one its update file's name carries. */
export function bundleVersion(b: FwBundle): string {
    return b.version || b.version_from_name || '';
}

/**
 * The view's rows - one per paired device type, the types with something to do first - and the
 * deployed bundles no paired type takes.
 */
/**
 * occulited task 60 (the maintainer, 2026-10-15: "system - updates firmwares should only show HmIP
 * devices, hide Bidcos-RF, Wired and VirtualDevices"): the interfaces the device firmware list leaves
 * out. HmIP wired devices (HmIPW-…) are hmipserver's, on HmIP-RF, and stay. The download and hold
 * logic of the daemon does not change: only what the pages show.
 */
export const HIDDEN_INTERFACES = new Set(['BidCos-RF', 'BidCos-Wired', 'VirtualDevices']);

/** the devices the list shows: those of hmipserver */
export function shownDevices<T extends {interface: string}>(devices: T[]): T[] {
    return devices.filter((d) => !HIDDEN_INTERFACES.has(d.interface));
}

/** a BidCos bundle (its type HM-…, as eQ-3 names them): not shown, not among "other downloaded firmware" either */
export function bidcosBundle(b: {name?: string; type_code?: string}): boolean {
    return /^hm-/i.test(b.name || b.type_code || '');
}

/** firmwareRows for the HmIP devices and bundles only (task 60) */
export function hmipFirmwareRows(devices: FwDevice[], deployed: FwBundle[], results: FwResult[] = []): {rows: FwRow[]; others: FwBundle[]} {
    return firmwareRows(shownDevices(devices), deployed.filter((b) => !bidcosBundle(b)), results);
}

export function firmwareRows(devices: FwDevice[], deployed: FwBundle[], results: FwResult[] = []): {rows: FwRow[]; others: FwBundle[]} {
    const bundles = new Map<string, FwBundle>();
    // every bundle of a type, offered and held (task 48: a delivery moves the older offered one
    // into the held directory) - all of them belong to the type's row, none to the others
    const allOf = new Map<string, FwBundle[]>();
    for (const b of deployed) {
        const k = typeKey(b.name || b.type_code);
        const cur = bundles.get(k);
        if (!cur || newerFirmware(bundleVersion(b), bundleVersion(cur))) bundles.set(k, b);
        allOf.set(k, [...(allOf.get(k) ?? []), b]);
    }
    const failed = new Map<string, string>();
    for (const r of results) if (r.action === 'failed') failed.set(typeKey(r.type), r.detail ?? '');

    const groups = new Map<string, FwDevice[]>();
    for (const d of devices) {
        const k = typeKey(d.type);
        const g = groups.get(k);
        if (g) g.push(d);
        else groups.set(k, [d]);
    }
    const used = new Set<FwBundle>();
    const rows: FwRow[] = [];
    for (const [key, devs] of groups) {
        const bkey = bundles.has(key) ? key : FALLBACK[key] && bundles.has(FALLBACK[key]!) ? FALLBACK[key]! : key;
        const bundle = bundles.get(bkey);
        const all = allOf.get(bkey) ?? [];
        for (const b of all) used.add(b);
        const newest = (list: FwBundle[]) => list.reduce<FwBundle | undefined>((best, b) => (!best || newerFirmware(bundleVersion(b), bundleVersion(best)) ? b : best), undefined);
        const offered = newest(all.filter((b) => !b.held));
        const heldBest = newest(all.filter((b) => b.held));
        const deliverable = heldBest && (!offered || newerFirmware(bundleVersion(heldBest), bundleVersion(offered))) ? heldBest : undefined;
        const fws = [...new Set(devs.map((d) => d.firmware).filter(Boolean))].sort((a, b) => (newerFirmware(a, b) ? 1 : newerFirmware(b, a) ? -1 : a.localeCompare(b)));
        const latest = max(devs.map((d) => d.latest ?? ''));
        const bv = bundle ? bundleVersion(bundle) : '';
        const downloaded = max([bv, ...devs.map((d) => shownVersion(d.available_firmware))]);
        const behindOf = (v: string) => (v ? devs.filter((d) => newerFirmware(v, d.firmware)).length : 0);
        const row: FwRow = {key, type: devs[0]!.type, interfaces: [...new Set(devs.map((d) => d.interface))].sort(), devices: [...devs].sort((a, b) => (a.name || '￿').localeCompare(b.name || '￿') || a.address.localeCompare(b.address)), installed: fws, status: 'current', version: '', latest, downloaded, bundle, offered, deliverable, bundles: [...all].sort((a, b) => (newerFirmware(bundleVersion(a), bundleVersion(b)) ? -1 : newerFirmware(bundleVersion(b), bundleVersion(a)) ? 1 : 0)), archivable: archivable(devs[0]!.type) && devs.every((d) => d.interface === 'HmIP-RF'), behind: 0};
        if (failed.has(key)) row.failed = failed.get(key);
        if (devs.every((d) => d.interface === 'BidCos-Wired')) row.status = 'image-only';
        else if (devs.every((d) => d.updatable === false)) row.status = 'not-updatable';
        else if (latest && behindOf(latest) > 0 && (!downloaded || newerFirmware(latest, downloaded))) {
            row.status = 'available';
            row.version = latest;
            row.behind = behindOf(latest);
        } else if (downloaded && behindOf(downloaded) > 0) {
            row.status = 'downloaded';
            row.version = downloaded;
            row.behind = behindOf(downloaded);
            // task 44: the deployed firmware on its way - the interface's live state, the journal's percent
            const act = rowActivity(devs.filter((d) => newerFirmware(downloaded, d.firmware)));
            if (act) {
                row.status = act.kind;
                row.activity = act;
            } else if (bundle?.held) {
                // task 45: held back - the interfaces do not see it, so no device is on its way
                row.status = 'held';
            }
        } else if (!downloaded && !latest && devs.every((d) => d.not_listed)) row.status = 'not-listed';
        else if (!downloaded && !latest) row.status = 'unchecked';
        else row.version = max([latest, downloaded, fws[fws.length - 1] ?? '']);
        // task 49: an older version delivered on the user's choice is on its way to devices that run a
        // newer one - the live state of those devices, about the delivered version
        if (offered && !isLive(row.status) && !cannotUpdate(row.status)) {
            const ov = bundleVersion(offered);
            const act = ov ? rowActivity(devs.filter((d) => newerFirmware(d.firmware, ov))) : undefined;
            if (act) {
                row.status = act.kind;
                row.activity = act;
                row.version = ov;
            }
        }
        // the downloaded version read from the update file's name, marked as such
        if (bundle && !bundle.version && bundle.version_from_name && row.version === bundle.version_from_name) {
            row.fromName = {version: bundle.version_from_name, date: bundle.date_from_name};
        }
        rows.push(row);
    }
    rows.sort((a, b) => ORDER[a.status] - ORDER[b.status] || a.type.localeCompare(b.type, undefined, {sensitivity: 'base'}));
    const others = deployed.filter((b) => !used.has(b)).sort((a, b) => (a.name || a.type_code).localeCompare(b.name || b.type_code, undefined, {sensitivity: 'base'}));
    return {rows, others};
}

/** Whether a device matches the search: its type, name or address. */
export function deviceMatches(d: FwDevice, q: string): boolean {
    return [d.type, d.name ?? '', d.address].some((v) => v.toLowerCase().includes(q));
}

/**
 * The rows the view shows for a search and the Show-all switch. A row matches by its type or by one
 * of its devices; `byDevice` names the rows that matched only through their devices (the view opens
 * them, so the device is seen), `hidden` counts the matching rows the switch keeps back.
 */
export function filterRows(rows: FwRow[], query: string, showAll: boolean): {shown: FwRow[]; byDevice: Set<string>; hidden: number} {
    const q = query.trim().toLowerCase();
    const shown: FwRow[] = [];
    const byDevice = new Set<string>();
    let hidden = 0;
    for (const r of rows) {
        let match = true;
        if (q) {
            const byType = r.type.toLowerCase().includes(q);
            const byDev = r.devices.some((d) => deviceMatches(d, q));
            match = byType || byDev;
            if (!byType && byDev) byDevice.add(r.key);
        }
        if (!match) continue;
        if (!showAll && cannotUpdate(r.status)) hidden++;
        else shown.push(r);
    }
    return {shown, byDevice, hidden};
}

/**
 * The counts of the summary line: types with a downloaded update (ready to install, in the
 * frontend's hands: `downloaded` and `ready`), on their way to the devices (`transferring`,
 * `interrupted`, `queued`), installing, held back (task 45), with one at eQ-3, up to date.
 */
export function summary(rows: FwRow[]): {downloaded: number; transferring: number; installing: number; held: number; available: number; current: number; hidden: number} {
    const s = {downloaded: 0, transferring: 0, installing: 0, held: 0, available: 0, current: 0, hidden: 0};
    for (const r of rows) {
        if (r.status === 'downloaded' || r.status === 'ready') s.downloaded++;
        else if (r.status === 'held') s.held++;
        else if (r.status === 'transferring' || r.status === 'interrupted' || r.status === 'queued') s.transferring++;
        else if (r.status === 'installing') s.installing++;
        else if (r.status === 'available') s.available++;
        else if (r.status === 'current') s.current++;
        else if (cannotUpdate(r.status)) s.hidden++;
    }
    return s;
}

/** The bundles under Other downloaded firmware that match the search: by type, name, version or file. */
export function filterOthers(others: FwBundle[], query: string): FwBundle[] {
    const q = query.trim().toLowerCase();
    if (!q) return others;
    return others.filter((b) => [b.name, b.type_code, bundleVersion(b), ...b.files].some((v) => (v ?? '').toLowerCase().includes(q)));
}

/**
 * How long ago a time was, in whole minutes or hours, for "last progress …": the unit and the
 * number, so the page can word it; `just` for under a minute, `days` from two days on.
 */
export function agoParts(iso: string | undefined, now: number = Date.now()): {unit: 'just' | 'minutes' | 'hours' | 'days'; n: number} | undefined {
    if (!iso) return undefined;
    const t = Date.parse(iso);
    if (Number.isNaN(t)) return undefined;
    const s = Math.max(0, Math.round((now - t) / 1000));
    if (s < 60) return {unit: 'just', n: 0};
    if (s < 3600) return {unit: 'minutes', n: Math.round(s / 60)};
    if (s < 48 * 3600) return {unit: 'hours', n: Math.round(s / 3600)};
    return {unit: 'days', n: Math.round(s / 86400)};
}
