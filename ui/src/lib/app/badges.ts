/*
 * The badges on a card (task 193): the device's maintenance channel made visible - unreachable,
 * low battery, configuration or update pending, sabotage, a fault, the duty cycle - in a fixed
 * order, at most three on the tile and the rest behind a +n. The keys are the CCU's service
 * messages; occulited's store lists the active ones for the whole house (the rollup on the
 * rooms, functions and drawer entries), the :0 channel's events keep an open page live.
 *
 * Reachability follows UNREACH alone (occulited B-46). STICKY_UNREACH without it means the device
 * answers again and nobody has acknowledged the outage yet: a mild "was unreachable" and no problem
 * for the rollup - the tile is about now.
 *
 * Icon and colour are the message's own (occulited task 43, messageLook in servicemessages.ts): the
 * same as on the Status page and in homematic-manager's device table. UNREACH is the one filled
 * badge - the device is away now.
 */
import type {IconName} from '../Icon.svelte';
import {messageLook, type Severity} from '../servicemessages';

export interface Badge {
    key: string;
    icon: IconName;
    /** the label, a t() key */
    label: string;
    /** the colour: the message kind's severity */
    tone: Severity;
    /** drawn filled: the device is unreachable now */
    solid?: boolean;
    /** a note about the past, drawn muted: nothing is wrong now */
    mild?: boolean;
}

// the order is the row's order: the same problem always sits in the same place
const TABLE: {keys: RegExp; label: string; solid?: boolean; mild?: boolean; unless?: RegExp}[] = [
    {keys: /^UNREACH$/, label: 'not reachable', solid: true},
    // only while the device is reachable: with UNREACH set, "not reachable" says it all
    {keys: /^STICKY_UNREACH$/, label: 'was unreachable', mild: true, unless: /^UNREACH$/},
    {keys: /^(LOWBAT|LOW_BAT)$/, label: 'Low battery'},
    {keys: /^CONFIG_PENDING$/, label: 'Configuration pending'},
    {keys: /^(UPDATE_PENDING|DEVICE_IN_BOOTLOADER)$/, label: 'Update pending'},
    {keys: /^(SABOTAGE|ERROR_SABOTAGE)$/, label: 'Sabotage'},
    {keys: /^FAULT_REPORTING$/, label: 'Fault'},
    {keys: /^(ERROR|ERROR_CODE|ERROR_[A-Z_]+)$/, label: 'Fault'},
    {keys: /^(DUTY_CYCLE|DUTYCYCLE)$/, label: 'Duty cycle exceeded'},
];

/** The badges for a set of active maintenance keys, in the row's order, one per kind. */
export function badgesFor(keys: Iterable<string>): Badge[] {
    const out: Badge[] = [];
    const set = new Set(keys);
    for (const row of TABLE) {
        const hit = [...set].find((k) => row.keys.test(k));
        if (!hit || (row.unless && [...set].some((k) => row.unless!.test(k)))) continue;
        const look = messageLook(hit);
        out.push({key: hit, icon: look.icon, tone: look.tone, label: row.label, solid: row.solid, mild: row.mild});
    }
    return out;
}

/** Whether a set of active maintenance keys holds a problem of now: anything but a mild badge. */
export function isProblem(keys: Iterable<string>): boolean {
    return badgesFor(keys).some((b) => !b.mild);
}

/** Whether a maintenance datapoint's value means an active message (as the store decides it). */
export function active(value: unknown): boolean {
    if (typeof value === 'boolean') return value;
    if (typeof value === 'number') return value !== 0;
    if (typeof value === 'string') return value !== '' && value !== '0' && value.toLowerCase() !== 'false';
    return false;
}

/** Whether a key of the :0 channel is a message at all (the well-known ones; RSSI and the like are not). */
export function isMaintenanceKey(key: string): boolean {
    return TABLE.some((row) => row.keys.test(key));
}

/** The device of a ref: <interface>.<address> without the channel. */
export function deviceOf(ref: string): string {
    const i = ref.lastIndexOf(':');
    return i > 0 ? ref.slice(0, i) : ref;
}
