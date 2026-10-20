// occulited B-84: GET /api/meta/v1/foreign - the names of interfaces this system does not list
// right now, per interface; ForeignNames.svelte offers each for removal.
export interface ForeignInterface {
    interface: string;
    objects: number;
    devices: number;
    channels: number;
}

export interface ForeignAnswer {
    revision: number;
    list_read: boolean;
    listed: string[];
    interfaces: ForeignInterface[];
}

type T = (key: string, params?: Record<string, string | number>) => string;

/** "12 names (2 devices, 10 channels)", singular where one; a part that is 0 is left out. */
export function foreignSummary(f: ForeignInterface, t: T): string {
    const names = f.objects === 1 ? t('1 name') : t('{n} names', {n: f.objects});
    const parts: string[] = [];
    if (f.devices) parts.push(f.devices === 1 ? t('1 device') : t('{n} devices', {n: f.devices}));
    if (f.channels) parts.push(f.channels === 1 ? t('1 channel') : t('{n} channels', {n: f.channels}));
    return parts.length ? `${names} (${parts.join(', ')})` : names;
}
