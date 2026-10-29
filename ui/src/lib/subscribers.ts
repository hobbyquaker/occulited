// The Interfaces page's subscriber rows (task 76): which client a registration belongs to, where
// its id tells it, and the process address without the scheme.

/** The kinds of client an id names; '' when it names none the page knows. */
export type ClientKind = 'node-red' | 'hmm' | 'java' | 'rega' | '';

/**
 * The client behind a subscriber id, by the conventions described in internal/system/handlers.go:
 * `nr_…` node-red-contrib-ccu, `hmm_…` Homematic Manager, `…_java` the box's own Java server, a
 * bare number ReGa (its process id).
 */
export function clientKind(id: string): ClientKind {
    if (id.startsWith('nr_')) return 'node-red';
    if (id.startsWith('hmm_')) return 'hmm';
    if (id.endsWith('_java')) return 'java';
    if (/^\d+$/.test(id)) return 'rega';
    return '';
}

/**
 * occulited task 84: the client's name the Status and Interfaces pages show beside the id - the
 * system's own registration is occulited's; '' when the id names no client the page knows.
 */
export function clientLabel(id: string, own = false): string {
    if (own) return 'occulited';
    const k = clientKind(id);
    return k === 'node-red' ? 'node-red-contrib-ccu' : k === 'hmm' ? 'Homematic Manager' : k === 'java' ? 'hmipserver' : k === 'rega' ? 'ReGa' : '';
}

/** `xmlrpc_bin://127.0.0.1:32001` → `127.0.0.1:32001`; a path stays, a trailing slash goes. */
export function processAddress(url: string): string {
    const i = url.indexOf('://');
    return (i >= 0 ? url.slice(i + 3) : url).replace(/\/$/, '');
}

/**
 * Task 76's follow-up (D-64): whether a subscriber's callback accepted a TCP connection when the page
 * opened (GET /radio/subscribers/reachability). `reachable` is absent for a URL with no address to
 * connect to; `reason` says why not.
 */
export interface SubscriberReach {
    interface: string;
    id: string;
    url: string;
    reachable?: boolean;
    reason?: 'refused' | 'timeout' | 'unreachable';
}

/** The key a verdict is kept under: an id and its callback are unique per interface. */
export function reachKey(iface: string, id: string, url: string): string {
    return `${iface}\n${id}\n${url}`;
}

/** The process cards' order: the interfaces with a module card first, in the modules' order, then the rest as listed. */
export function processOrder<T extends {name: string}>(interfaces: T[], protocols: string[]): T[] {
    const first = protocols.map((p) => interfaces.find((i) => i.name === p)).filter((i): i is T => i !== undefined);
    return [...first, ...interfaces.filter((i) => !first.includes(i))];
}

/**
 * occulited B-45: /radio/health's subscribers per interface - the callbacks and lite-rpc's event
 * streams (`stream`: a token's, an account's session, a public browser's) - on the Status page and,
 * since task 87, on the Interfaces page.
 */
export interface SubClient {
    id: string;
    url: string;
    internal: boolean;
    own?: boolean;
    stream?: {id: string; kind: string; transport: string; remote?: string};
}
export interface SubSummary {
    total: number;
    internal: number;
    external: number;
    clients: SubClient[];
}

type T = (key: string, params?: Record<string, string | number>) => string;

/**
 * A lite-rpc stream's row: named by its holder alone; the row carries a "lite-rpc" pill (task 121,
 * the maintainer: "instead of \"(Stream über occulited)\" i'd like to have a pill showing
 * \"literpc\""), whose tooltip is streamPillTitle.
 */
export function streamName(c: SubClient, t: T): string {
    const k = c.stream?.kind;
    return k === 'token' ? c.id : k === 'session' ? t('session of {name}', {name: c.id}) : t('public browser {name}', {name: c.id});
}

/** The lite-rpc pill's tooltip: the stream's kind and transport, and that it comes through occulited. */
export function streamPillTitle(c: SubClient, t: T): string {
    if (!c.stream) return '';
    const k = c.stream.kind;
    const kind = k === 'token' ? t('API token') : k === 'session' ? t('session') : t('public browser');
    return t('Reads the events through occulited with lite-rpc: {kind}, {transport}', {kind, transport: c.stream.transport === 'websocket' ? 'WebSocket' : 'SSE'});
}

/** The stream's transport and where it comes from: "WebSocket · 198.51.100.20". */
export function streamDetail(c: SubClient): string {
    if (!c.stream) return '';
    return [c.stream.transport === 'websocket' ? 'WebSocket' : 'SSE', c.stream.remote].filter(Boolean).join(' · ');
}

/** The lite-rpc streams of one interface, from /radio/health's subscribers. */
export function streamsOf(subs: Record<string, SubSummary> | undefined, iface: string): SubClient[] {
    return (subs?.[iface]?.clients ?? []).filter((c) => c.stream);
}
