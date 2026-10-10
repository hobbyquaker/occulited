/*
 * The service messages grouped for the Status page (occulited task 27, the maintainer's Q&A of
 * 2026-10-10): one row per device with a chip per kind, kinds coloured by severity (and each with
 * its own symbol, so colour is never the only signal), "communication was disturbed" folded into
 * its "communication disturbed", sorted by severity and then newest first, filtered by kind.
 */
import type {ServiceMessage} from './api';

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

export interface DeviceRow {
    key: string;
    interface: string;
    address: string;
    name?: string;
    type?: string;
    enums?: string[];
    /** the device's messages, sticky folded away where its unreach is there, most severe first */
    messages: ServiceMessage[];
    kinds: Kind[];
    /** the newest message's start */
    since: string;
    severity: number;
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
        out.push({key, interface: first.interface, address: first.address, name: first.name, type: ms.find((m) => m.type)?.type, enums: first.enums, messages: shown, kinds, since, severity: Math.max(...kinds.map((k) => RANK[SEVERITY[k]]))});
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

/** CAP rows are shown before "show all" */
export const CAP = 8;

const FILTER_KEY = 'ol.servicemessages.filter';

/** the filter kept per browser; storage may be missing or throw (a private window) */
export function loadFilter(): Kind | '' {
    try {
        const v = localStorage.getItem(FILTER_KEY) ?? '';
        return (KINDS as string[]).includes(v) ? (v as Kind) : '';
    } catch {
        return '';
    }
}

export function saveFilter(k: Kind | ''): void {
    try {
        if (k) localStorage.setItem(FILTER_KEY, k);
        else localStorage.removeItem(FILTER_KEY);
    } catch {
        /* not kept: the filter still works on this page */
    }
}
