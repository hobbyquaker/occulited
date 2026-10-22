// occulited B-103: a heating group's members, for its boost countdown. A VirtualDevices heating group (HmIP-HEATING,
// BidCos HM-CC-VG-1) reports BOOST_MODE true while its members boost, but its own BOOST_TIME stays 0 (production,
// 2026-10-23: the group INT0000011:1 said 0 while its HmIP-WTH-2 counted 297 s). The time is the members': the
// heating groups API (task 180, GET /groups and /groups/{id}, system:read - every account has it) names them.
import {api} from '../api';
import {splitRef} from './channels';

export interface GroupMember {
    id: string;
    serial?: string;
    type?: string;
}

/** The climate channel of a BidCos thermostat model, where a group member is the device (rfd's channel layout). */
const BIDCOS_CLIMATE: Record<string, number> = {
    'HM-CC-RT-DN': 4,
    'HM-CC-RT-DN-BoM': 4,
    'HM-TC-IT-WM-W-EU': 2,
    'HM-CC-TC': 2,
};

/**
 * The member's channel ref, or undefined when it cannot be named. hmipserver's member ids are addresses on
 * openccu-lite: an HmIP member is its climate channel (`00010000000A10:1`), a BidCos one its device
 * (`KEQ9000003`), whose climate channel follows from its model.
 */
export function memberRef(m: GroupMember): string | undefined {
    const id = (m.id || m.serial || '').trim();
    if (!id) return undefined;
    const [dev, ch] = id.split(':');
    // a BidCos serial is three letters and seven digits (KEQ9000007); HmIP's are 14 hex digits
    const hmip = !/^[A-Z]{3}[0-9]{7}$/.test(dev ?? '');
    if (ch !== undefined && ch !== '') return `${hmip ? 'HmIP-RF' : 'BidCos-RF'}.${id}`;
    if (hmip) return `HmIP-RF.${id}:1`;
    const n = m.type ? BIDCOS_CLIMATE[m.type] : undefined;
    return n === undefined ? undefined : `BidCos-RF.${id}:${n}`;
}

/** Whether a ref is a VirtualDevices group channel (INT000000N:n). */
export function isGroupRef(ref: string): boolean {
    const {iface, address} = splitRef(ref);
    return iface === 'VirtualDevices' && /^INT\d+:\d+$/.test(address);
}

/**
 * The member channels of the groups whose devices are named (`INT0000011`), by device address. A group the API
 * does not list, or an API that refuses (a token without system:read, a system without hmipserver), has none: the
 * tile then shows "Boost" without a time.
 */
export async function loadGroupMembers(devices: string[]): Promise<Map<string, string[]>> {
    const out = new Map<string, string[]>();
    if (devices.length === 0) return out;
    let list: {groups?: {id: number; device: string}[]};
    try {
        list = await api.get<{groups?: {id: number; device: string}[]}>('/api/system/v1/groups');
    } catch {
        return out;
    }
    const want = new Set(devices);
    for (const g of list.groups ?? []) {
        if (!want.has(g.device)) continue;
        try {
            const d = await api.get<{members?: GroupMember[]}>(`/api/system/v1/groups/${encodeURIComponent(String(g.id))}`);
            out.set(g.device, (d.members ?? []).map(memberRef).filter((r): r is string => !!r));
        } catch {
            out.set(g.device, []);
        }
    }
    for (const d of devices) if (!out.has(d)) out.set(d, []);
    return out;
}
