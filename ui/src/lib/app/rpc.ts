/*
 * The App's lite-rpc client (task 193, phase 3): JSON-RPC 2.0 batches per interface with the
 * shell's session (api sends the Authorization header the paths need, D-115), the channel
 * descriptions from the system's paramset description cache (task 108: per device model, firmware
 * and channel type), the values, setValue, and the event stream.
 *
 * occulited B-47: the values come from the system's state store (GET /state: the last value of
 * every datapoint a tile draws, kept from the events), not from a getParamset of every channel at
 * every page open - which BidCos answers by asking the device over the radio, three failed
 * attempts for one that is away. Only a channel whose state the store does not have is read from
 * its interface process, and not while its device is known to be unreachable. The stream is read
 * with fetch() (eventstream.ts): an EventSource cannot send the header a page over plain HTTP
 * needs.
 */
import {api, ApiError} from '../api';
import {active} from './badges';
import {classify, splitRef, type Description, type Model, type Values} from './channels';
import {openEventStream, type StreamState} from './eventstream';

type Call = {method: string; params: unknown[]};
type Answer = {result?: unknown; error?: {code: number; message: string}};

/**
 * occulited B-84: ReGa's pseudo-interfaces - the CCU's own objects, no interface process behind
 * them. A metadata object of one is no channel to read or operate (an import before B-84 brought
 * them in; lite-rpc answers "no such interface").
 */
export const PSEUDO_INTERFACES = new Set(['System']);

/**
 * Whether a channel is one of this system's: not of a pseudo-interface, not of one lite-rpc said it
 * does not have. An old ref without an interface stays (a tile with nothing to ask, as before).
 */
export function operable(ref: string, unknown: ReadonlySet<string> | readonly string[] = []): boolean {
    const {iface} = splitRef(ref);
    if (!iface) return true;
    if (PSEUDO_INTERFACES.has(iface)) return false;
    return Array.isArray(unknown) ? !unknown.includes(iface) : !(unknown as ReadonlySet<string>).has(iface);
}

/** One batch to one interface. */
export async function batch(iface: string, calls: Call[]): Promise<Answer[]> {
    if (calls.length === 0) return [];
    const body = calls.map((c, i) => ({jsonrpc: '2.0', method: c.method, params: c.params, id: i + 1}));
    const out = await api.post<Answer[] | Answer>(`/api/rpc/v1/json/${encodeURIComponent(iface)}`, body);
    return Array.isArray(out) ? out : [out];
}

export interface Channel {
    ref: string;
    iface: string;
    address: string;
    type: string;
    /** the device's model (PARENT_TYPE), e.g. HmIP-PSM - task 75: a plug shows a socket */
    parent?: string;
    desc?: Description;
    model: Model;
    values: Values;
    /** occulited task 95: per datapoint, when the system saw its value change (the state store's changed_at); a
     *  datapoint without one has no known time */
    changed?: Record<string, string>;
}

// occulited task 108: the VALUES descriptions come from the system's paramset description cache
// (POST /api/rpc/v1/paramsets/{interface}), keyed per device model, firmware and channel type - two
// models sharing a channel type name (HmIP-WTH and HmIP-eTRV) get their own. descByRef is each
// channel's; descByKey holds one object per key, so equal descriptions are shared.
const descByRef = new Map<string, Description>();
const descByKey = new Map<string, Description>();
const typeByRef = new Map<string, string>();
const parentByRef = new Map<string, string>(); // task 75: the device model of a channel

/** One entry of the state store, as far as the tiles read it. */
interface StateEntry {
    interface: string;
    address: string;
    datapoint: string;
    value: unknown;
    changed_at?: string;
}
interface StatePage {
    entries: StateEntry[];
    next?: string;
    event_id?: string;
}
export interface StoredState {
    /** interface.address (a channel, the :0 too) -> the datapoints the store has */
    values: Map<string, Values>;
    /** interface.address -> datapoint -> changed_at, where the store knows it (task 95) */
    changed: Map<string, Record<string, string>>;
    /** where the event stream resumes so that nothing after this read is lost */
    eventId: string;
}

// the devices of one read: their addresses travel in the query, and a web server caps a request line
const STATE_DEVICES = 40;

function deviceAddress(address: string): string {
    const i = address.lastIndexOf(':');
    return i > 0 ? address.slice(0, i) : address;
}

/**
 * The state store's values for the devices of these channels (a device matches all its channels, so
 * the maintenance channel :0 comes along). undefined when the system has no store to ask (501) or
 * the read failed: the caller then reads the interface processes, as before the store.
 */
export async function loadState(byIface: Map<string, string[]>): Promise<StoredState | undefined> {
    const devices = [...new Set([...byIface.values()].flat().map(deviceAddress))];
    const ifaces = [...byIface.keys()].map(encodeURIComponent).join(',');
    const out: StoredState = {values: new Map(), changed: new Map(), eventId: ''};
    try {
        for (let i = 0; i < devices.length; i += STATE_DEVICES) {
            const q = `interface=${ifaces}&address=${devices.slice(i, i + STATE_DEVICES).map(encodeURIComponent).join(',')}&limit=5000`;
            let after = '';
            do {
                const page = await api.get<StatePage>(`/api/rpc/v1/state?${q}${after ? `&after=${encodeURIComponent(after)}` : ''}`);
                // the first read's position: an event after it that a later read has too is harmless
                if (!out.eventId) out.eventId = page.event_id ?? '';
                for (const e of page.entries ?? []) {
                    const ref = `${e.interface}.${e.address}`;
                    out.values.set(ref, {...out.values.get(ref), [e.datapoint]: e.value});
                    if (e.changed_at) out.changed.set(ref, {...out.changed.get(ref), [e.datapoint]: e.changed_at});
                }
                after = page.next ?? '';
            } while (after);
        }
    } catch {
        return undefined;
    }
    return out;
}

/** Whether a tile's state is missing from the store's values: such a channel is read from its interface. */
export function needsRead(model: Model, stored: Values | undefined): boolean {
    // a key shows its presses as they happen and nothing else; a channel without a widget has no tile
    if (model.widget === 'none' || model.widget === 'button' || !model.stateKey) return false;
    return stored?.[model.stateKey] === undefined;
}

/**
 * Loads what the tiles need: the channel type and description (cached), and the values - from the
 * state store, and from the interface process for the channels the store cannot answer. eventId is
 * the stream's resume point of the store's read ('' without one).
 */
export async function loadChannels(refs: string[]): Promise<{channels: Map<string, Channel>; errors: string[]; eventId: string; unknown: string[]}> {
    const channels = new Map<string, Channel>();
    const errors: string[] = [];
    // B-84: the interfaces lite-rpc does not have - left out, never an error on the page
    const unknown: string[] = [];
    const byIface = new Map<string, string[]>();
    for (const ref of refs) {
        const {iface, address} = splitRef(ref);
        if (!iface) continue; // an old ref without the interface: nothing to ask
        if (!operable(ref)) continue; // B-84: a pseudo-interface (System): nothing to ask
        byIface.set(iface, [...(byIface.get(iface) ?? []), address]);
    }
    const state = byIface.size ? await loadState(byIface) : undefined;
    for (const [iface, addrs] of byIface) {
        try {
            // 1. the types of the channels not seen yet
            const unknown = addrs.filter((a) => !typeByRef.has(`${iface}.${a}`));
            if (unknown.length) {
                const ans = await batch(iface, unknown.map((a) => ({method: 'getDeviceDescription', params: [a]})));
                unknown.forEach((a, i) => {
                    const r = ans[i]?.result as {TYPE?: string; PARENT_TYPE?: string} | undefined;
                    if (r?.TYPE) typeByRef.set(`${iface}.${a}`, r.TYPE);
                    if (r?.PARENT_TYPE) parentByRef.set(`${iface}.${a}`, r.PARENT_TYPE);
                });
            }
            // 2. the descriptions of the channels not seen yet, from the system's cache (task 108)
            const need = addrs.filter((a) => typeByRef.has(`${iface}.${a}`) && !descByRef.has(`${iface}.${a}`));
            if (need.length) {
                const got = await api.post<{keys: Record<string, string>; paramsets: Record<string, Description>}>(`/api/rpc/v1/paramsets/${encodeURIComponent(iface)}`, {addresses: need, paramset: 'VALUES'});
                for (const a of need) {
                    const key = got.keys[a];
                    if (!key) continue;
                    let desc = descByKey.get(key);
                    if (!desc) {
                        desc = got.paramsets[key];
                        if (desc) descByKey.set(key, desc);
                    }
                    if (desc) descByRef.set(`${iface}.${a}`, desc);
                }
            }
            // 3. the values: the state store's
            for (const a of addrs) {
                const ref = `${iface}.${a}`;
                const type = typeByRef.get(ref) ?? '';
                const desc = descByRef.get(ref);
                channels.set(ref, {ref, iface, address: a, type, parent: parentByRef.get(ref), desc, model: classify(desc, type || undefined), values: {...state?.values.get(ref)}, changed: {...state?.changed.get(ref)}});
            }
            // 4. the interface process for what the store does not have - every channel when there
            // is no store. A device the store knows as unreachable is not asked: BidCos would try
            // the radio, and the tile says "not reachable" either way.
            const ask = addrs.filter((a) => {
                if (!state) return true;
                if (active(state.values.get(`${iface}.${deviceAddress(a)}:0`)?.UNREACH)) return false;
                const c = channels.get(`${iface}.${a}`)!;
                return needsRead(c.model, c.values);
            });
            if (ask.length) {
                const ans = await batch(iface, ask.map((a) => ({method: 'getParamset', params: [a, 'VALUES']})));
                ask.forEach((a, i) => {
                    const c = channels.get(`${iface}.${a}`)!;
                    const got = (ans[i]?.result as Values | undefined) ?? {};
                    // a value read from the interface that is not the store's: when it changed is not known
                    const changed = {...c.changed};
                    for (const k of Object.keys(got)) if (c.values[k] !== got[k]) delete changed[k];
                    c.values = {...c.values, ...got};
                    c.changed = changed;
                });
            }
        } catch (e) {
            if (e instanceof ApiError && e.code === 'unknown-interface') unknown.push(iface);
            else errors.push(`${iface}: ${(e as Error).message}`);
        }
    }
    return {channels, errors, eventId: state?.eventId ?? '', unknown};
}

/** setValue on one channel; the daemon's fault is the error's message. */
export async function setValue(ref: string, key: string, value: unknown): Promise<void> {
    const {iface, address} = splitRef(ref);
    const [ans] = await batch(iface, [{method: 'setValue', params: [address, key, value]}]);
    if (ans?.error) throw new Error(ans.error.message);
}

/** putParamset VALUES on one channel: several datapoints in one write (a siren's selection, duration and
 *  trigger belong together; rpc:operate, as setValue). */
export async function putValues(ref: string, values: Values): Promise<void> {
    const {iface, address} = splitRef(ref);
    const [ans] = await batch(iface, [{method: 'putParamset', params: [address, 'VALUES', values]}]);
    if (ans?.error) throw new Error(ans.error.message);
}

export interface EventsOptions {
    /** where the stream starts: loadChannels' eventId, so nothing between the read and the stream is lost */
    lastEventId?: string;
    /** the system could not replay what was missed (a restart, a gap longer than its ring, a queue
     *  that overflowed): the values are to be read again */
    onResync?: (reason: string) => void;
    /** the stream was refused and is not tried again */
    onRefused?: (status: number) => void;
    /** where the stream stands: live, or why not (occulited task 19) */
    onState?: (s: StreamState) => void;
}

/** One message of the stream as the tiles take it, or undefined for the others (hello, interface, …). */
export function streamValue(event: string, data: string): {ref: string; key: string; value: unknown; changedAt?: string} | undefined {
    // an event is the interface's own; a state message is the state store's sweep creating, changing
    // or confirming an entry - the same value by another road, named datapoint there
    if (event !== 'event' && event !== 'state') return undefined;
    try {
        const d = JSON.parse(data) as {interface: string; address: string; key?: string; datapoint?: string; value: unknown; changed_at?: string};
        const key = d.key ?? d.datapoint;
        if (!key) return undefined;
        // a press is a moment, and only the interface's own event is one: the store's entry of it is "last pressed"
        if (event === 'state' && (key === 'PRESS_SHORT' || key === 'PRESS_LONG')) return undefined;
        // task 95: when the system saw the value change; absent when it does not know (the tile shows nothing then)
        return {ref: `${d.interface}.${d.address}`, key, value: d.value, ...(d.changed_at ? {changedAt: d.changed_at} : {})};
    } catch {
        return undefined; // a torn line
    }
}

/** The event stream for the interfaces in use; onEvent gets (ref, key, value, changedAt). It reconnects by itself
 *  and resumes where it was. */
export function openEvents(ifaces: string[], onEvent: (ref: string, key: string, value: unknown, changedAt?: string) => void, opt: EventsOptions = {}): () => void {
    if (typeof fetch === 'undefined' || ifaces.length === 0) return () => undefined;
    const q = ifaces.map((i) => `interface=${encodeURIComponent(i)}`).join('&');
    return openEventStream({
        url: `/api/rpc/v1/events?${q}&type=event,state`,
        lastEventId: opt.lastEventId,
        onRefused: opt.onRefused,
        onState: opt.onState,
        onMessage: (m) => {
            if (m.event === 'resync') {
                let reason = '';
                try {
                    reason = (JSON.parse(m.data) as {reason?: string}).reason ?? '';
                } catch {
                    /* the reason is for the log only */
                }
                opt.onResync?.(reason);
                return;
            }
            const v = streamValue(m.event, m.data);
            if (v) onEvent(v.ref, v.key, v.value, v.changedAt);
        },
    });
}
