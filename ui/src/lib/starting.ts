// Task 94, section 7: the radio stack while the box boots. After the boot-order overhaul the web UI
// answers long before hmipserver's JVM is ready (about 24 s and 65 s on the Charly): an interface
// process whose unit systemd is still starting is "starting", with the time since it began, never
// down or stopped - otherwise "hmipserver does not come up" is everyone's first impression.

/** One unit of the radio stack as GET /api/system/v1/radio/health's `units` carries it. */
export interface InterfaceUnit {
    /** rfd, hmipserver, hs485d, multimacd, hmlangw, or the detection (DETECTION_UNIT) */
    unit: string;
    /** the InterfacesList.xml names the unit's process serves (rfd: BidCos-RF) */
    interfaces: string[];
    active_state: string;
    sub_state: string;
    /** systemd is starting it: activating, or queued behind the units it is ordered after */
    starting?: boolean;
    /** a start job waiting for other units: nothing has begun, so there is no age */
    queued?: boolean;
    /** whole seconds since it began starting, at the moment of the answer (absent when 0 or queued) */
    starting_s?: number;
}

/** The unit that writes /var/hm_mode: until it has run the box knows no radio module. */
export const DETECTION_UNIT = 'occu-init-rf-hardware';

/** The unit behind an interface name, when the box reported the units. */
export function unitOf(units: InterfaceUnit[] | undefined, iface: string): InterfaceUnit | undefined {
    return (units ?? []).find((u) => u.interfaces.includes(iface));
}

/**
 * occulited B-93: the process the system names for an interface before its units are known, by the
 * interface's name as internal/system's UnitOfInterface does for the four the firmware writes - so the
 * Status page's "BidCos-RF · rfd" does not grow when /radio/health's units arrive a poll later.
 */
export function expectedUnit(iface: string): string {
    return iface === 'BidCos-RF' ? 'rfd' : iface === 'BidCos-Wired' ? 'hs485d' : iface === 'HmIP-RF' || iface === 'VirtualDevices' ? 'hmipserver' : '';
}

/** The process behind an interface: its unit from the radio stack's units, the expected one while they are unknown. */
export function processOf(units: InterfaceUnit[] | undefined, iface: string): string {
    return units === undefined ? expectedUnit(iface) : (unitOf(units, iface)?.unit ?? '');
}

/** Whether a unit of the radio stack is starting. */
export function anyStarting(units: InterfaceUnit[] | undefined): boolean {
    return (units ?? []).some((u) => u.starting);
}

/** Whether the radio module detection is still to finish - running, or queued. */
export function detecting(units: InterfaceUnit[] | undefined): boolean {
    return (units ?? []).some((u) => u.unit === DETECTION_UNIT && u.starting);
}

/**
 * How long something has been starting at `now`, in whole seconds: the box's figure at its answer
 * plus the time since that answer arrived (`fetched`). Both ends of the addition are the browser's
 * clock, so a box whose own clock is wrong at boot, or is stepped while the unit starts, still counts
 * right. undefined when it is not starting, or queued (nothing has begun yet).
 */
export function startingSeconds(s: {starting?: boolean; queued?: boolean; starting_s?: number}, fetched: number, now: number): number | undefined {
    if (!s.starting || s.queued) return undefined;
    return Math.floor((s.starting_s ?? 0) + Math.max(0, now - fetched) / 1000);
}
