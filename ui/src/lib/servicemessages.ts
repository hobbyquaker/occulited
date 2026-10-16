/*
 * The service messages grouped for the Status page (occulited task 27, the maintainer's Q&A of
 * 2026-10-10): one row per device with a chip per kind, kinds coloured by severity (and each with
 * its own symbol, so colour is never the only signal), "communication was disturbed" folded into
 * its "communication disturbed", sorted by severity and then newest first, filtered by kind.
 */
import type {ServiceMessage} from './api';
import type {IconName} from './Icon.svelte';

export type Kind = 'unreach' | 'fault' | 'lowbat' | 'other' | 'config' | 'update' | 'sticky';
export type Severity = 'red' | 'orange' | 'blue' | 'grey';

/** the kinds in the order of the filter chips: most severe first */
export const KINDS: Kind[] = ['unreach', 'fault', 'lowbat', 'other', 'config', 'update', 'sticky'];

export const SEVERITY: Record<Kind, Severity> = {
    unreach: 'red',
    fault: 'red',
    lowbat: 'orange',
    other: 'orange',
    config: 'blue',
    update: 'blue',
    sticky: 'grey',
};

const RANK: Record<Severity, number> = {red: 4, orange: 3, blue: 2, grey: 1};

/** kindOf maps a maintenance datapoint to its kind; what is not known is "other" */
export function kindOf(key: string): Kind {
    switch (key) {
        case 'UNREACH':
            return 'unreach';
        case 'STICKY_UNREACH':
            return 'sticky';
        case 'LOWBAT':
        case 'LOW_BAT':
            return 'lowbat';
        case 'CONFIG_PENDING':
            return 'config';
        case 'UPDATE_PENDING':
            return 'update';
    }
    if (key === 'SABOTAGE' || key === 'FAULT_REPORTING' || key.startsWith('ERROR') || key.startsWith('DUTY_CYCLE') || key.startsWith('DUTYCYCLE')) return 'fault';
    return 'other';
}

/**
 * The look of one message (occulited task 43): its icon and colour, the same on the Status page's
 * chips and the cards' badges, and the same as homematic-manager's device table (its task 92) -
 * a broken link for "communication disturbed", a clock turned back for "was disturbed", and the
 * faults told apart: sabotage a shield, the fault report a crossed circle, the duty cycle its
 * activity line, any other error the alert. The colour is the kind's severity.
 */
export interface MessageLook {
    icon: IconName;
    tone: Severity;
}

const LOOK: {keys: RegExp; icon: IconName}[] = [
    {keys: /^UNREACH$/, icon: 'unlink'},
    {keys: /^STICKY_UNREACH$/, icon: 'history'},
    {keys: /^(LOWBAT|LOW_BAT)$/, icon: 'battery'},
    {keys: /^CONFIG_PENDING$/, icon: 'settings'},
    {keys: /^(UPDATE_PENDING|DEVICE_IN_BOOTLOADER)$/, icon: 'arrow-up'},
    {keys: /^(SABOTAGE|ERROR_SABOTAGE|STICKY_SABOTAGE)$/, icon: 'shield'},
    {keys: /^FAULT_REPORTING$/, icon: 'x-circle'},
    {keys: /^(DUTY_CYCLE|DUTYCYCLE)/, icon: 'activity'},
];

/** messageLook is the icon and colour of a maintenance datapoint; an unknown one is the alert */
export function messageLook(key: string): MessageLook {
    const row = LOOK.find((r) => r.keys.test(key));
    return {icon: row?.icon ?? 'alert', tone: SEVERITY[kindOf(key)]};
}

export interface DeviceRow {
    key: string;
    interface: string;
    address: string;
    name?: string;
    type?: string;
    enums?: string[];
    /** the device type's small picture at the CCU's URL (occulited task 39), when the system has one */
    image?: string;
    /** the rooms and functions by name (occulited B-66), the last id of a path where the system sends no name */
    places: string[];
    /** the device's messages, sticky folded away where its unreach is there, most severe first */
    messages: ServiceMessage[];
    kinds: Kind[];
    /** the newest message's start */
    since: string;
    severity: number;
}

/** confirmable says whether a message can be acknowledged (openccu-lite task 82): a sticky one
 *  (STICKY_UNREACH, STICKY_SABOTAGE) whose cause is gone - the same datapoint without STICKY_ is not
 *  active on the device (`device` is the device's messages) */
export function confirmable(m: ServiceMessage, device: ServiceMessage[]): boolean {
    if (m.channel !== '0' || !m.key.startsWith('STICKY_') || m.value !== true) return false;
    const cause = m.key.slice('STICKY_'.length);
    return !device.some((o) => o.channel === m.channel && o.key === cause);
}

/** the Control app's favourites taxonomy: a per-user list, not a place (occulited task 39) */
export const FAVORITE_ENUM = 'favorite';

/** places names a device's rooms and functions: the names the system sends, the path's last id
 *  otherwise; the favourites are left out (occulited task 39) */
export function places(m: ServiceMessage): string[] {
    const out: string[] = [];
    (m.enums ?? []).forEach((e, i) => {
        if (e === FAVORITE_ENUM || e.startsWith(FAVORITE_ENUM + '/')) return;
        out.push(m.enum_names?.[i] || e.split('/').slice(-1)[0]!);
    });
    return out;
}

/**
 * The type pills (occulited task 39, the maintainer 2026-10-13): the list is collapsed until a pill
 * is on, several may be on at once, and "Show all" turns every present kind on - or, when all of
 * them are on, every one off again. A pure set operation, so the page and the tests share it.
 */
export function toggleKind(on: Kind[], k: Kind): Kind[] {
    return on.includes(k) ? on.filter((x) => x !== k) : KINDS.filter((x) => x === k || on.includes(x));
}

/** whether every kind present is on */
export function allOn(on: Kind[], present: Kind[]): boolean {
    return present.length > 0 && present.every((k) => on.includes(k));
}

/** "Show all": every present kind on, or all off when they are on already */
export function toggleAll(on: Kind[], present: Kind[]): Kind[] {
    return allOn(on, present) ? [] : KINDS.filter((k) => present.includes(k));
}

/** the rows of the kinds that are on (none on: none) */
export function visibleRows(list: DeviceRow[], on: Kind[]): DeviceRow[] {
    return list.filter((r) => r.kinds.some((k) => on.includes(k)));
}

/** rows groups the messages by device, folds sticky into unreach, sorts by severity then newest first */
export function rows(list: ServiceMessage[]): DeviceRow[] {
    const by = new Map<string, ServiceMessage[]>();
    for (const m of list) {
        const k = `${m.interface}|${m.address}`;
        const l = by.get(k);
        if (l) l.push(m);
        else by.set(k, [m]);
    }
    const out: DeviceRow[] = [];
    for (const [key, ms] of by) {
        const hasUnreach = ms.some((m) => kindOf(m.key) === 'unreach');
        const shown = ms.filter((m) => !(hasUnreach && kindOf(m.key) === 'sticky'));
        shown.sort((a, b) => RANK[SEVERITY[kindOf(b.key)]] - RANK[SEVERITY[kindOf(a.key)]] || KINDS.indexOf(kindOf(a.key)) - KINDS.indexOf(kindOf(b.key)));
        const kinds = [...new Set(shown.map((m) => kindOf(m.key)))];
        const first = ms.find((m) => m.name) ?? ms[0]!;
        const since = ms.reduce((acc, m) => (m.since > acc ? m.since : acc), ms[0]!.since);
        out.push({key, interface: first.interface, address: first.address, name: first.name, type: ms.find((m) => m.type)?.type, enums: first.enums, image: ms.find((m) => m.image)?.image, places: places(first), messages: shown, kinds, since, severity: Math.max(...kinds.map((k) => RANK[SEVERITY[k]]))});
    }
    out.sort((a, b) => b.severity - a.severity || (b.since > a.since ? 1 : b.since < a.since ? -1 : 0) || a.key.localeCompare(b.key));
    return out;
}

/** counts is the number of devices per kind, as the rows show them */
export function counts(list: DeviceRow[]): Map<Kind, number> {
    const out = new Map<Kind, number>();
    for (const r of list) for (const k of r.kinds) out.set(k, (out.get(k) ?? 0) + 1);
    return out;
}

/**
 * The panel's left edge (occulited task 53, the maintainer 2026-10-15: "the thick left border in
 * the color of the most severe service message like the other warning panels on the page"): the
 * most severe kind among the messages decides - red `err`, orange `warn`, blue `info` - with the
 * classes the warning panels use (app.css). A grey one alone (communication was disturbed) and no
 * message at all give no edge. The rows are every message on the system, whatever pill is on: the
 * panel stands for all of them, collapsed or not.
 */
export type PanelEdge = 'err' | 'warn' | 'info' | '';

const EDGE: Record<Severity, PanelEdge> = {red: 'err', orange: 'warn', blue: 'info', grey: ''};

export function panelEdge(list: DeviceRow[]): PanelEdge {
    let top: Severity | undefined;
    for (const r of list) for (const k of r.kinds) if (!top || RANK[SEVERITY[k]] > RANK[top]) top = SEVERITY[k];
    return top ? EDGE[top] : '';
}

/** CAP rows are shown before "show all" */
export const CAP = 8;

