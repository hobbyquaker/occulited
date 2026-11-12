<script lang="ts">
    import {dateTime} from '../lib/datefmt';
    /*
     * Task 157 (D-105): the firewall as one ordered list of INPUT rules - "clear iptables rules". Each
     * rule is one port and protocol, from a source (an address, a network, 0/0 or the local
     * networks), for IPv4, IPv6 or both, with a target and an optional log. Rules an enabled feature
     * needs (the web server, SSH, the HmIP access points, an addon's opened port) are added by the box
     * and carry their owner; they can be edited, duplicated or deleted, and go when the feature is
     * switched off. What INPUT always allows comes first and is shown read-only; the policy per family
     * comes last.
     *
     * Edits collect as a draft. Apply loads it with a confirm window: the page confirms over the new
     * rules, and a box that does not hear from it within the window puts the old ones back - a rule
     * that locks the page out undoes itself. The listeners below say, per socket, which rule decides
     * its packets, or that none does and the policy applies.
     */
    import {onDestroy, onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {t} from '../lib/i18n.svelte';
    import {auth} from '../lib/auth.svelte';
    import {link} from '../lib/router.svelte';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import Disclosure from '../lib/Disclosure.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {holdColumns} from '../lib/holdcolumns';
    import {moved} from '../lib/sortable';
    import {Sortable} from '../lib/sortable.svelte';
    import SortHandle from '../lib/SortHandle.svelte';
    import WarnEdge from '../lib/WarnEdge.svelte';

    type Family = 'ipv4' | 'ipv6' | 'both';
    type Target = 'ACCEPT' | 'DROP' | 'REJECT';
    type Proto = 'tcp' | 'udp' | 'igmp';
    // port 0 is every port; sport and dest narrow a rule (the network discovery's rules)
    interface Rule { id: string; port: number; port_to?: number; disabled?: boolean; proto: Proto; sport?: number; source: string; dest?: string; family: Family; target: Target; log?: boolean; comment?: string; owner?: string }
    interface Policy { ipv4: 'ACCEPT' | 'DROP'; ipv6: 'ACCEPT' | 'DROP'; log?: boolean }
    interface Note { text: string; args?: Record<string, string> }
    // local_ipv6 (openccu-lite B-339): the user's IPv6 networks "local networks" includes besides the automatic ones
    interface Config { policy: Policy; rules: Rule[]; local_ipv6?: string[]; migration?: {at: string; notes: Note[]; dismissed?: boolean} }
    interface View { config: Config; draft: Config | null; pending: {started: string; deadline: string} | null; frame: Record<string, string[]>; local_networks: {ipv4: string[]; ipv6: string[]}; owners: Record<string, string>; error?: string }
    interface Coverage { rule?: string; target: string; source?: string; owner?: string }
    interface Listener { proto: string; port: number; address: string; loopback: boolean; pid?: number; process?: string; unit?: string; cover: Record<string, Coverage> }

    // iptables' own words (source, destination, target, policy, ...) stay as iptables has them, in
    // every language (maintainer): they are written here without t()
    const LOCAL = 'local networks';
    const ANY = '0/0';
    // 0/0 is shown as iptables -L shows it: anywhere (maintainer, 2026-09-18); the file keeps 0/0,
    // and typing 0/0 still works
    const ANYWHERE = 'anywhere';
    const FAMS = ['ipv4', 'ipv6'] as const;

    const life = pageLife();
    let view = $state<View | null>(null);
    let listeners = $state<Listener[]>([]);
    let error = $state('');
    let notice = $state('');
    let busy = $state('');
    // the rules as edited on the page, and the policy - the draft's when there is one, else the file's
    let rules = $state<Rule[]>([]);
    let policy = $state<Policy>({ipv4: 'DROP', ipv6: 'DROP'});
    // B-339: the IPv6 local networks as typed - networks separated by spaces or commas
    let localV6 = $state('');
    let now = $state(Date.now());
    let tick: ReturnType<typeof setInterval> | undefined;
    const admin = $derived(auth.role === 'admin');

    function take(v: View) {
        view = v;
        const base = v.draft ?? v.config;
        rules = structuredClone(base.rules);
        policy = structuredClone(base.policy);
        localV6 = (base.local_ipv6 ?? []).join(' ');
    }
    const splitNets = (s: string) => s.split(/[\s,;]+/).filter(Boolean);
    const localV6List = $derived(splitNets(localV6));
    // an IPv6 address or network: hex groups with colons, an optional prefix length
    const isV6Net = (s: string) => /^[0-9a-f:]*:[0-9a-f:]*(\/(\d|[1-9]\d|1[01]\d|12[0-8]))?$/i.test(s);
    const badLocalV6 = $derived(localV6List.some((n) => !isV6Net(n)));
    // the automatic list and the confirmed user's networks: what "local networks" stands for now
    const localV6Now = $derived(view ? [...view.local_networks.ipv6, ...(view.config.local_ipv6 ?? []).filter((n) => !view!.local_networks.ipv6.includes(n))] : []);

    // task 177: a placeholder until the first answer, not "nothing is listening"
    let listenersLoaded = $state(false);
    async function loadListeners() {
        try {
            listeners = (await api.get<{listeners: Listener[]}>('/api/system/v1/firewall/listeners')).listeners;
        } catch {
            listeners = [];
        }
        listenersLoaded = true;
    }
    async function load() {
        try {
            take(await api.get<View>('/api/system/v1/firewall'));
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
        await loadListeners();
    }

    // task 167: packets and bytes per rule since the last load, polled while the page is shown
    interface Count { packets: number; bytes: number }
    interface Counters { at: string; since?: string; known: boolean; rules: Record<string, Count>; policy: Record<string, Count> }
    let counters = $state<Counters | null>(null);
    let countTick: ReturnType<typeof setInterval> | undefined;
    async function loadCounters() {
        if ((typeof document !== 'undefined' && document.visibilityState === 'hidden') || !life.active) return;
        try {
            counters = await api.get<Counters>('/api/system/v1/firewall/counters');
        } catch {
            counters = null;
        }
    }
    async function resetCounters() {
        busy = 'counters';
        try {
            counters = await api.post<Counters>('/api/system/v1/firewall/counters/reset');
            error = '';
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    const pkts = (id: string): Count | undefined => (counters?.known ? counters.rules[id] : undefined);
    function short(n: number): string {
        return n >= 1e9 ? `${(n / 1e9).toFixed(1)}G` : n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e4 ? `${Math.round(n / 1e3)}k` : String(n);
    }

    onMount(() => {
        void load();
        void loadCounters();
        tick = setInterval(() => (now = Date.now()), 1000);
        countTick = setInterval(() => void loadCounters(), 5000);
        // task 177: back from another page - the listeners and counters now, the rules too unless an
        // edit waits to be applied
        stopReturn = life.onReturn(() => {
            if (!dirty && !view?.pending) void load();
            else void loadListeners();
            void loadCounters();
        });
    });
    let stopReturn: (() => void) | undefined;
    onDestroy(() => {
        stopReturn?.();
        clearInterval(tick);
        clearInterval(countTick);
    });

    function famName(f: string): string {
        return f === 'ipv4' ? 'IPv4' : 'IPv6';
    }
    function familyLabel(f: Family): string {
        return f === 'both' ? t('IPv4 and IPv6') : famName(f);
    }
    function sourceLabel(s: string): string {
        return s === ANY ? ANYWHERE : s;
    }
    function ruleText(r: Rule): string {
        const what = Number(r.port) ? `${portText(r)}/${r.proto}` : r.proto;
        let from = sourceLabel(r.source);
        if (Number(r.sport)) from += `, source port ${Number(r.sport)}`;
        if (r.dest) from += `, destination ${r.dest}`;
        return t('{what} from {source} ({family}) → {target}', {what, source: from, family: familyLabel(r.family), target: r.target});
    }
    function ownerLabel(o?: string): string {
        if (!o) return '';
        const known: Record<string, string> = {web: t('Web server'), ssh: 'SSH', rpc: t('Classic RPC'), 'hmip-ap': t('HmIP access points'), discovery: t('Network discovery'), acme: 'ACME HTTP-01'};
        return known[o] ?? (o.startsWith('addon:') ? t('addon: {id}', {id: o.slice(6)}) : o);
    }
    // a line of the conversion's notice: the box sends the English template and its values, the
    // owners as their ids
    function noteText(n: Note): string {
        const args: Record<string, string> = {...n.args};
        if (args.owners !== undefined) args.owners = args.owners.split(' ').filter(Boolean).map(ownerLabel).join(', ');
        return t(n.text, args);
    }

    // the page's edits against what is confirmed: what Apply would change, in words
    const changes = $derived.by(() => {
        if (!view) return [] as string[];
        const was = view.config;
        const out: string[] = [];
        for (const f of FAMS) {
            if (was.policy[f] !== policy[f]) out.push(t('Policy {family}: {from} → {to}', {family: famName(f), from: was.policy[f], to: policy[f]}));
        }
        if (!!was.policy.log !== !!policy.log) out.push(policy.log ? t('The policy is logged') : t('The policy is no longer logged'));
        const wasV6 = (was.local_ipv6 ?? []).join(', ');
        if (wasV6 !== localV6List.join(', ')) out.push(t('local networks (IPv6): {from} → {to}', {from: wasV6 || '–', to: localV6List.join(', ') || '–'}));
        const byId = new Map(was.rules.map((r) => [r.id, r]));
        const ids = new Set(rules.map((r) => r.id));
        const norm = (r: Rule) => JSON.stringify([!!r.disabled, Number(r.port) || 0, Number(r.port_to) || 0, r.proto, Number(r.sport) || 0, r.source, r.dest ?? '', r.family, r.target, !!r.log, r.comment ?? '', r.owner ?? '']);
        for (const r of rules) {
            const o = byId.get(r.id);
            if (!o) out.push(t('New: {rule}', {rule: ruleText(r)}));
            else if (norm(o) !== norm(r)) {
                // task 170: only switched on or off says so; with other edits it is a change
                const flipped = !!o.disabled !== !!r.disabled && norm({...o, disabled: r.disabled}) === norm(r);
                if (flipped) out.push(r.disabled ? t('Switched off: {rule}', {rule: ruleText(r)}) : t('Switched on: {rule}', {rule: ruleText(r)}));
                else out.push(t('Changed: {rule}', {rule: ruleText(r)}));
            }
        }
        for (const o of was.rules) if (!ids.has(o.id)) out.push(t('Removed: {rule}', {rule: ruleText(o)}));
        const order = (xs: Rule[]) => xs.filter((r) => byId.has(r.id) && ids.has(r.id)).map((r) => r.id).join(',');
        if (order(rules) !== order(was.rules)) out.push(t('The order of the rules changed'));
        return out;
    });
    const dirty = $derived(changes.length > 0);
    const remaining = $derived(view?.pending ? Math.max(0, Math.ceil((Date.parse(view.pending.deadline) - now) / 1000)) : 0);
    const locked = $derived(!admin || !!view?.pending);

    // an IPv4 source limits the rule to IPv4, an IPv6 one to IPv6
    function sourceFamily(s: string): Family | '' {
        const v = s.trim().split('/')[0] ?? '';
        if (s === ANY || s === LOCAL || v === '') return '';
        if (/^\d{1,3}(\.\d{1,3}){3}$/.test(v)) return 'ipv4';
        if (v.includes(':')) return 'ipv6';
        return '';
    }
    function setSource(r: Rule, s: string) {
        r.source = s;
        const f = sourceFamily(s);
        if (f) r.family = f;
    }
    // a destination limits the family as a source does
    function setDest(r: Rule, s: string) {
        r.dest = s.trim();
        const f = sourceFamily(r.dest);
        if (f) r.family = f;
    }
    // an empty port field is 0: every port
    // task 171: the port field takes one port or a range, "1880-1890" (or 1880:1890); what it holds
    // while typed and not parseable yet stays as typed, marked, and Apply waits
    let portRaw = $state<Record<string, string>>({});
    const portText = (r: Rule) => (Number(r.port_to) ? `${r.port}-${r.port_to}` : Number(r.port) > 0 ? String(r.port) : '');
    function parsePort(s: string): {port: number; port_to: number} | null {
        const v = s.trim();
        const m = /^(\d{1,5})\s*[-:]\s*(\d{1,5})$/.exec(v);
        if (m) {
            const a = Number(m[1]), b = Number(m[2]);
            return a >= 1 && b > a && b <= 65535 ? {port: a, port_to: b} : null;
        }
        if (/^\d{0,5}$/.test(v)) {
            const n = Number(v) || 0;
            return n <= 65535 ? {port: n, port_to: 0} : null;
        }
        return null;
    }
    function setPort(r: Rule, s: string) {
        const p = parsePort(s);
        if (p) {
            r.port = p.port;
            r.port_to = p.port_to || undefined;
            const {[r.id]: _, ...rest} = portRaw;
            portRaw = rest;
        } else {
            portRaw = {...portRaw, [r.id]: s};
        }
    }
    const badPorts = $derived(Object.keys(portRaw).filter((id) => rules.some((r) => r.id === id)).length > 0);
    function portValue(e: Event): number {
        return Number((e.currentTarget as HTMLInputElement).value) || 0;
    }
    function setProto(r: Rule, p: Proto) {
        r.proto = p;
        if (p === 'igmp') r.port = r.sport = 0;
    }
    // the list sorted and filtered for reading; the rules keep their order (#), which is the order
    // they are checked in - so moving is only offered in that order, unfiltered
    type SortKey = 'n' | 'port' | 'proto' | 'source' | 'sport' | 'dest' | 'family' | 'target' | 'owner' | 'comment' | 'pkts';
    let sortKey = $state<SortKey>('n');
    let sortAsc = $state(true);
    let filter = $state('');
    function sortBy(k: SortKey) {
        if (sortKey === k) sortAsc = !sortAsc;
        else {
            sortKey = k;
            sortAsc = true;
        }
    }
    const arrow = (k: SortKey) => (sortKey === k ? (sortAsc ? ' ▲' : ' ▼') : '');
    function sortValue(r: Rule, k: SortKey): string | number {
        switch (k) {
            case 'port': return Number(r.port) || 0;
            case 'sport': return Number(r.sport) || 0;
            case 'proto': return r.proto;
            case 'source': return r.source;
            case 'dest': return r.dest ?? '';
            case 'family': return r.family;
            case 'target': return r.target;
            case 'owner': return ownerLabel(r.owner);
            case 'comment': return r.comment ?? '';
            case 'pkts': return pkts(r.id)?.packets ?? -1;
        }
        return 0;
    }
    const shown = $derived.by(() => {
        const q = filter.trim().toLowerCase();
        const list = rules.map((r, i) => ({r, i})).filter(({r}) => {
            if (!q) return true;
            const text = [Number(r.port) || '', r.proto, Number(r.sport) || '', r.source, r.dest ?? '', familyLabel(r.family), r.target, ownerLabel(r.owner), r.comment ?? ''].join(' ');
            return text.toLowerCase().includes(q);
        });
        if (sortKey === 'n') return sortAsc ? list : list.reverse();
        return list.sort((a, b) => {
            const x = sortValue(a.r, sortKey), y = sortValue(b.r, sortKey);
            const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y));
            return (sortAsc ? c : -c) || a.i - b.i;
        });
    });
    let frameOpen = $state(false);
    // task 168: a logged rule leads to its lines - only once it is loaded (the confirmed list), a
    // draft's rule has written none
    const loggedLive = (id: string) => !!view?.config.rules.some((x) => x.id === id && x.log && !x.disabled);
    const logLink = (prefix: string) => `/system/log?source=kernel&q=${encodeURIComponent(prefix)}`;
    const inOrder = $derived(sortKey === 'n' && sortAsc && filter.trim() === '');
    // task 162: the rules are ordered by the handle in their # cell - drag it, ↑/↓ on it, Alt+↑/↓ in
    // the row - and only in the order they are checked in, unfiltered; otherwise the handle is gone
    let rulesBody = $state<HTMLElement | null>(null);
    const canOrder = $derived(inOrder && !locked);
    const sort = new Sortable({
        rows: () => Array.from(rulesBody?.querySelectorAll<HTMLElement>(':scope > tr') ?? []),
        length: () => rules.length,
        name: (i) => t('Rule {n}', {n: i + 1}),
        commit: (from, to) => (rules = moved(rules, from, to)),
        enabled: () => canOrder,
    });

    // the source menu: local networks, 0/0, and every source another rule uses
    let srcMenu = $state<{id: string; top: number; left: number} | null>(null);
    const sourceChoices = $derived([LOCAL, ANY, ...[...new Set(rules.map((r) => r.source.trim()))].filter((s) => s && s !== LOCAL && s !== ANY).sort()]);
    let srcAnchor: HTMLElement | null = null;
    function openSources(r: Rule, ev: MouseEvent) {
        srcAnchor = ev.currentTarget as HTMLElement;
        const b = srcAnchor.getBoundingClientRect();
        srcMenu = srcMenu?.id === r.id ? null : {id: r.id, top: b.bottom + 2, left: b.left};
    }
    function pickSource(r: Rule, s: string) {
        setSource(r, s);
        srcMenu = null;
    }
    $effect(() => {
        if (!srcMenu) return;
        const close = () => (srcMenu = null);
        // the menu is fixed to the window: it follows its button when the page or the table scrolls
        const follow = () => {
            if (!srcMenu || !srcAnchor?.isConnected) return close();
            const b = srcAnchor.getBoundingClientRect();
            srcMenu = {...srcMenu, top: b.bottom + 2, left: b.left};
        };
        const onKey = (ev: KeyboardEvent) => {
            if (ev.key === 'Escape') close();
        };
        const onDown = (ev: MouseEvent) => {
            if (!(ev.target as HTMLElement | null)?.closest?.('.fw-srcmenu, .fw-srcbtn')) close();
        };
        document.addEventListener('keydown', onKey);
        document.addEventListener('mousedown', onDown);
        window.addEventListener('scroll', follow, true);
        window.addEventListener('resize', follow);
        return () => {
            document.removeEventListener('keydown', onKey);
            document.removeEventListener('mousedown', onDown);
            window.removeEventListener('scroll', follow, true);
            window.removeEventListener('resize', follow);
        };
    });

    function newRule(port = 0, proto: Proto = 'tcp'): Rule {
        return {id: `new-${Math.random().toString(16).slice(2, 10)}`, port, proto, source: ANY, family: 'both', target: 'ACCEPT'};
    }
    function add() {
        rules = [...rules, newRule()];
    }
    function duplicate(i: number) {
        // decision 4: the copy directly below the original, for editing; it keeps the owner
        const copy = {...structuredClone($state.snapshot(rules[i]!)), id: `new-${Math.random().toString(16).slice(2, 10)}`};
        rules = [...rules.slice(0, i + 1), copy, ...rules.slice(i + 1)];
    }
    function remove(i: number) {
        rules = rules.filter((_, j) => j !== i);
    }
    async function setPolicy(f: 'ipv4' | 'ipv6', el: HTMLSelectElement) {
        const v = el.value as 'ACCEPT' | 'DROP';
        if (v === 'ACCEPT' && policy[f] !== 'ACCEPT') {
            const ok = await ask({
                title: t('Accept everything not dropped?'),
                message: t('With the policy ACCEPT, {family} lets through every connection no rule drops - every service on the system that listens is reachable. Rules then close what should be closed.', {family: famName(f)}),
                confirm: t('Set ACCEPT'),
                danger: true,
                focusCancel: true,
            });
            if (!ok) {
                // the value did not change, so nothing re-renders the select: put it back by hand
                el.value = policy[f];
                return;
            }
        }
        policy = {...policy, [f]: v};
    }

    function payload() {
        return {policy: $state.snapshot(policy), local_ipv6: localV6List, rules: $state.snapshot(rules).map((r) => ({...r, id: r.id.startsWith('new-') ? '' : r.id, port: Number(r.port) || 0, port_to: Number(r.port_to) || undefined, sport: Number(r.sport) || 0, dest: r.dest?.trim() || undefined}))};
    }
    async function run(what: string, fn: () => Promise<View>): Promise<boolean> {
        busy = what;
        try {
            take(await fn());
            error = '';
            return true;
        } catch (e) {
            error = (e as Error).message;
            return false;
        } finally {
            busy = '';
        }
    }
    async function apply() {
        const list = changes;
        const ok = await ask({
            title: t('Apply the firewall rules?'),
            message: [t('The changes:'), list.join('\n'), t('The new rules are loaded at once. Confirm them within {n} seconds from this page; if the page cannot reach the system any more, the system puts the old rules back by itself.', {n: 60})].join('\n\n'),
            confirm: t('Apply'),
        });
        if (!ok) return;
        notice = '';
        if (!(await run('draft', () => api.put<View>('/api/system/v1/firewall', payload())))) return;
        await run('apply', () => api.post<View>('/api/system/v1/firewall/apply'));
    }
    async function confirmApply() {
        if (await run('confirm', () => api.post<View>('/api/system/v1/firewall/confirm'))) {
            notice = t('The firewall rules are confirmed.');
            await loadListeners();
        }
    }
    async function revert() {
        if (await run('revert', () => api.post<View>('/api/system/v1/firewall/revert'))) notice = t('The rules before the change are back; the change stays as a draft.');
    }
    async function discard() {
        if (view?.draft) await run('discard', () => api.del<View>('/api/system/v1/firewall/draft'));
        else if (view) take(view);
    }
    async function dismiss() {
        await run('dismiss', () => api.post<View>('/api/system/v1/firewall/migration/dismiss'));
    }
    // a listener with no rule gets one: its port and protocol, 0/0, both families, ACCEPT
    function acceptFor(l: Listener) {
        const r = newRule(l.port, l.proto.startsWith('udp') ? 'udp' : 'tcp');
        rules = [...rules, r];
        notice = t('A rule for {port}/{proto} is added at the end of the list; Apply loads it.', {port: l.port, proto: r.proto});
    }
    function coverText(c: Coverage): string {
        if (!c.rule) return t('(no rule, default: {policy})', {policy: c.target});
        const i = (view?.config.rules ?? []).findIndex((r) => r.id === c.rule);
        const who = c.owner ? ` · ${ownerLabel(c.owner)}` : '';
        return t('#{n} {target} from {source}', {n: i + 1, target: c.target, source: sourceLabel(c.source ?? ANY)}) + who;
    }
    function coverLines(l: Listener): string[] {
        if (l.loopback) return [t('loopback only')];
        const fams = Object.keys(l.cover);
        if (fams.length === 2 && JSON.stringify(l.cover.ipv4) === JSON.stringify(l.cover.ipv6)) return [coverText(l.cover.ipv4!)];
        return fams.map((f) => `${famName(f)}: ${coverText(l.cover[f]!)}`);
    }
    // task 169: the listeners sorted and filtered like the rules
    type LSortKey = 'proto' | 'address' | 'port' | 'process' | 'rule';
    let lSortKey = $state<LSortKey>('port');
    let lSortAsc = $state(true);
    let lFilter = $state('');
    function lSortBy(k: LSortKey) {
        if (lSortKey === k) lSortAsc = !lSortAsc;
        else {
            lSortKey = k;
            lSortAsc = true;
        }
    }
    const lArrow = (k: LSortKey) => (lSortKey === k ? (lSortAsc ? ' ▲' : ' ▼') : '');
    // the rule column sorts by the first covering rule's position, the policy's cases after every rule
    function ruleRank(l: Listener): number {
        if (l.loopback) return 1e6;
        const ids = Object.values(l.cover).map((c) => (c.rule ? (view?.config.rules ?? []).findIndex((r) => r.id === c.rule) : -1));
        const hit = ids.filter((i) => i >= 0);
        return hit.length ? Math.min(...hit) : 1e5;
    }
    const shownListeners = $derived.by(() => {
        const q = lFilter.trim().toLowerCase();
        const list = listeners.filter((l) => !q || [l.proto, l.address, l.port, l.process ?? '', l.pid ?? '', l.unit ?? '', ...coverLines(l)].join(' ').toLowerCase().includes(q));
        const val = (l: Listener): string | number => {
            switch (lSortKey) {
                case 'proto': return l.proto;
                case 'address': return l.address;
                case 'port': return l.port;
                case 'process': return l.process ?? '';
                case 'rule': return ruleRank(l);
            }
        };
        // ties keep the API's order (by address within a port, B-80)
        const pos = new Map(listeners.map((l, i) => [l, i]));
        return [...list].sort((a, b) => {
            const x = val(a), y = val(b);
            const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y));
            return (lSortAsc ? c : -c) || pos.get(a)! - pos.get(b)!;
        });
    });
    const uncovered = (l: Listener) => !l.loopback && Object.values(l.cover).some((c) => !c.rule);
    const acceptFams = $derived(FAMS.filter((f) => policy[f] === 'ACCEPT').map(famName));
</script>

<SystemTitle />

{#if !view}
    <Loading {error} />
{:else}
    {#if error}<div class="ol-notice error" data-notice="fw-error">{error}</div>{/if}
    {#if view.error}<div class="ol-notice error" data-notice="fw-box-error">{view.error}</div>{/if}
    {#if notice}<div class="ol-notice" data-notice="fw-notice">{notice}</div>{/if}

    {#if view.config.migration && !view.config.migration.dismissed}
        <!-- decision 11: what the conversion from firewall.conf did, until dismissed -->
        <div class="ol-notice" data-notice="fw-migrated">
            <strong>{t('The firewall was converted from the old configuration (firewall.conf):')}</strong>
            <ul>{#each view.config.migration.notes as n, i (i)}<li>{noteText(n)}</li>{/each}</ul>
            {#if admin}<button type="button" class="hmm-button" onclick={dismiss} disabled={busy !== ''}>{t('Dismiss')}</button>{/if}
        </div>
    {/if}

    {#if view.pending}
        <div class="ol-notice fw-pending" data-notice="fw-pending">
            <strong>{t('The new rules are loaded.')}</strong>
            {t('Confirm them within {n} s, or the system puts the old ones back.', {n: remaining})}
            {#if admin}
                <button type="button" class="hmm-button primary" onclick={confirmApply} disabled={busy !== ''}>{t('Confirm')}</button>
                <button type="button" class="hmm-button" onclick={revert} disabled={busy !== ''}>{t('Revert now')}</button>
            {/if}
        </div>
    {/if}

    <!-- occulited task 12: the rules not loaded (the Status page's firewall warning) -->
    <WarnEdge ids={['firewall']}>
    <h2>{t('Rules')}<Help>{t('The rules of the INPUT chain, in order: the first rule whose port, protocol, family and source match decides. What no rule matches gets the policy at the end. Rules an enabled feature needs are added by the system and show their owner; switching the feature off removes them.')}</Help></h2>
    <!-- the policy first and prominent (maintainer): what every packet no rule matches gets -->
    <div class="ol-card fw-policybox" class:fw-policybox-open={acceptFams.length > 0} data-policy>
        <div class="fw-policyhead"><strong>Policy</strong> <span class="ol-muted hmm-mono">iptables -P INPUT</span><Help>{t('What reaches the end of the chain: no rule matched. DROP closes everything no rule accepts; ACCEPT lets everything through that no rule drops.')}</Help></div>
        <div class="fw-policy-sel">
            {#each FAMS as f (f)}
                <label>{famName(f)}
                    <select class="hmm-select" class:fw-policy-drop={policy[f] === 'DROP'} class:fw-policy-accept={policy[f] === 'ACCEPT'} value={policy[f]} onchange={(e) => setPolicy(f, e.currentTarget as HTMLSelectElement)} disabled={locked} aria-label={t('Policy {family}', {family: famName(f)})}>
                        <option value="DROP">DROP</option><option value="ACCEPT">ACCEPT</option>
                    </select>
                </label>
            {/each}
            <label><input type="checkbox" bind:checked={policy.log} disabled={locked} aria-label={t('Log the policy')} /> Log</label>
            {#if view.config.policy.log}<a class="fw-loglink" href={logLink('fw policy')} use:link title={t('Its lines in the kernel log')}>{t('lines')}</a>{/if}
            {#if counters?.known}
                <span class="ol-muted fw-policy-pkts" data-policy-pkts>{FAMS.map((f) => `${famName(f)} ${short(counters?.policy[f]?.packets ?? 0)}`).join(' · ')} pkts</span>
            {/if}
        </div>
        {#if acceptFams.length}
            <div class="ol-notice error" data-notice="fw-accept">{t('The policy is ACCEPT for {families}: everything no rule drops is reachable.', {families: acceptFams.join(', ')})}</div>
        {/if}
    </div>

    <!-- openccu-lite B-339: what "local networks" stands for, and the user's IPv6 networks - IPv6 has no
         private range that covers every VLAN, and the interfaces give one /64 each -->
    <div class="ol-card fw-localbox" data-localv6>
        <div class="fw-localhead"><strong>local networks</strong><Help>{t('What a rule with the Source "local networks" accepts from. IPv4: the private, link-local and CGNAT ranges - every VLAN of a home network. IPv6: the loopback, the unique local and the link-local ranges and the prefixes on the system\'s interfaces, a /64 each - one VLAN; clients in another VLAN of the same delegated prefix are not local by themselves. Add their networks here, the delegated prefix, say (2001:db8:1::/56), separated by spaces. An IPv6 connection attempt (TCP SYN) that no "local networks" rule accepts is answered with a TCP reset instead of being dropped, so a dual-stack client falls back to IPv4 at once instead of hanging.')}</Help></div>
        <p class="ol-muted fw-local">{t('"local networks" stands for: {v4}; {v6}', {v4: view.local_networks.ipv4.join(', '), v6: localV6Now.join(', ')})}</p>
        <div class="fw-localrow">
            <label class="fw-locallabel">{t('IPv6, also')}
                <input class="hmm-input hmm-mono fw-localv6" class:fw-invalid={badLocalV6} bind:value={localV6} placeholder="2001:db8:1::/56" disabled={locked} aria-label="local networks (IPv6)" />
            </label>
            {#if badLocalV6}<span class="ol-warn" data-notice="fw-badlocal">{t('An entry of the IPv6 local networks is not an IPv6 address or network.')}</span>{/if}
        </div>
    </div>

    <div class="fw-filter" class:fw-frame-open={frameOpen}>
        <input class="hmm-input" placeholder={t('Filter the rules')} aria-label={t('Filter the rules')} bind:value={filter} />
        {#if filter.trim()}<span class="ol-muted">{t('{n} of {total} rules shown', {n: shown.length, total: rules.length})}</span>{/if}
        {#if counters?.known && counters.since}<span class="ol-muted" data-counters-since>{t('pkts since {time}', {time: dateTime(counters.since)})}</span>{/if}
        {#if admin && !view.pending}<button type="button" class="hmm-button" onclick={resetCounters} disabled={busy !== ''}>{t('Reset counters')}</button>{/if}
        {#if !inOrder && !locked}<span class="ol-muted" data-notice="fw-order">{t('Sorted or filtered: the rules are still checked in the order of #. Sort by # without a filter to move them.')}</span>{/if}
        <div class="fw-framebox">
            <Disclosure bind:open={frameOpen} label={t('Always allowed, before the rules')} title={t('Always allowed')} readOnly>
                <p class="ol-muted">{t("Loopback, replies to connections the system made, the ICMP a network needs and the DHCP client's replies. Not editable.")}</p>
                {#each FAMS as f (f)}
                    <div class="fw-frame"><div class="ol-muted">{famName(f)}</div><pre class="hmm-mono">{(view.frame[f] ?? []).join('\n')}</pre></div>
                {/each}
            </Disclosure>
        </div>
    </div>
    <div class="ol-scroll-x">
        <table class="ol-table fw-rules" use:holdColumns={filter.trim() !== ''}>
            <thead><tr>
                <th class="ol-sort" onclick={() => sortBy('n')}>#{arrow('n')}</th>
                <th class="ol-sort" onclick={() => sortBy('port')}>Port{arrow('port')}</th>
                <th class="ol-sort" onclick={() => sortBy('proto')}>Protocol{arrow('proto')}</th>
                <th class="ol-sort" onclick={() => sortBy('source')}>Source{arrow('source')}</th>
                <th class="ol-sort" onclick={() => sortBy('sport')}>Source port{arrow('sport')}</th>
                <th class="ol-sort" onclick={() => sortBy('dest')}>Destination{arrow('dest')}</th>
                <th class="ol-sort" onclick={() => sortBy('family')}>{t('Family')}{arrow('family')}</th>
                <th class="ol-sort" onclick={() => sortBy('target')}>Target{arrow('target')}</th>
                <th class="ol-sort" onclick={() => sortBy('pkts')} title={t('Packets since the rules were loaded')}>pkts{arrow('pkts')}</th>
                <th>{t('On')}</th>
                <th>Log</th>
                <th class="ol-sort" onclick={() => sortBy('owner')}>{t('Owner')}{arrow('owner')}</th>
                <th class="ol-sort" onclick={() => sortBy('comment')}>{t('Comment')}{arrow('comment')}</th>
                <th></th>
            </tr></thead>
            <tbody bind:this={rulesBody}>
                {#each shown as {r, i} (r.id)}
                    {@const owned = !!r.owner}
                    {@const sf = sourceFamily(r.source) || sourceFamily(r.dest ?? '')}
                    {@const portless = r.proto === 'igmp'}
                    <tr data-rule={r.id} data-port={r.port} class="ol-sortrow" class:fw-off={r.disabled} class:ol-dragging={sort.dragging(r.id)} class:ol-drop-before={sort.before(i)} class:ol-drop-after={sort.after(i)} onkeydown={(ev) => sort.rowKey(ev, i)}>
                        <td class="ol-muted fw-n">{#if canOrder}<SortHandle sortable={sort} index={i} key={r.id} name={t('Rule {n}', {n: i + 1})} />{:else if !locked}<span class="ol-grip ol-grip-none" aria-hidden="true"></span>{/if}{i + 1}</td>
                        <td><input class="hmm-input hmm-mono fw-port fw-dport" class:fw-invalid={portRaw[r.id] !== undefined} type="text" inputmode="numeric" value={portRaw[r.id] ?? portText(r)} placeholder={portless ? '' : 'any'} oninput={(e) => setPort(r, (e.currentTarget as HTMLInputElement).value)} disabled={locked || owned || portless} aria-label="Port" title={t('One port, or a range such as 1880-1890')} /></td>
                        <td>
                            <select class="hmm-select" value={r.proto} onchange={(e) => setProto(r, (e.currentTarget as HTMLSelectElement).value as Proto)} disabled={locked || owned} aria-label="Protocol">
                                <option value="tcp">tcp</option><option value="udp">udp</option><option value="igmp">igmp</option>
                            </select>
                        </td>
                        <td class="fw-srccell">
                            <input class="hmm-input hmm-mono fw-source" value={sourceLabel(r.source)}
                                   oninput={(e) => { const v = (e.currentTarget as HTMLInputElement).value; setSource(r, v.trim() === ANYWHERE ? ANY : v); }}
                                   disabled={locked} aria-label="Source" />
                            {#if !locked}<button type="button" class="hmm-button fw-srcbtn" aria-haspopup="menu" aria-expanded={srcMenu?.id === r.id} aria-label={t('Choose a source')} title={t('Choose a source')} onclick={(e) => openSources(r, e)}>▾</button>{/if}
                            {#if srcMenu?.id === r.id}
                                <div class="ol-menupop fw-srcmenu" role="menu" style={`position: fixed; top: ${srcMenu.top}px; left: ${srcMenu.left}px`}>
                                    {#each sourceChoices as s (s)}
                                        <button type="button" role="menuitem" class="ol-menuitem hmm-mono" onclick={() => pickSource(r, s)}>{sourceLabel(s)}</button>
                                    {/each}
                                </div>
                            {/if}
                        </td>
                        <td><input class="hmm-input hmm-mono fw-port" type="number" min="1" max="65535" value={r.sport || ''} placeholder={portless ? '' : 'any'} oninput={(e) => (r.sport = portValue(e))} disabled={locked || owned || portless} aria-label="Source port" /></td>
                        <td><input class="hmm-input hmm-mono fw-dest" value={r.dest ?? ''} placeholder="any" oninput={(e) => setDest(r, (e.currentTarget as HTMLInputElement).value)} disabled={locked || owned} aria-label="Destination" /></td>
                        <td>
                            <!-- a select, not three radios: the table fits a desktop window with its actions -->
                            <select class="hmm-select" bind:value={r.family} disabled={locked} aria-label={t('Family')}>
                                {#each ['both', 'ipv4', 'ipv6'] as f (f)}
                                    <option value={f} disabled={sf !== '' && sf !== f}>{familyLabel(f as Family)}</option>
                                {/each}
                            </select>
                        </td>
                        <td>
                            <select class="hmm-select" bind:value={r.target} disabled={locked} aria-label="Target">
                                <option value="ACCEPT">ACCEPT</option><option value="DROP">DROP</option><option value="REJECT">REJECT</option>
                            </select>
                        </td>
                        <td class="hmm-mono fw-pkts" title={pkts(r.id) ? t('{bytes} bytes', {bytes: pkts(r.id)!.bytes}) : ''}>{pkts(r.id) ? short(pkts(r.id)!.packets) : '–'}</td>
                        <td><input type="checkbox" checked={!r.disabled} onchange={(e) => (r.disabled = !(e.currentTarget as HTMLInputElement).checked || undefined)} disabled={locked} aria-label={t('Rule on')} title={t('A rule switched off stays in the list and is not loaded')} /></td>
                        <td class="fw-logcell"><input type="checkbox" bind:checked={r.log} disabled={locked} aria-label="Log" />{#if loggedLive(r.id)}<a class="fw-loglink" href={logLink(`fw ${r.id}`)} use:link title={t('Its lines in the kernel log')}>{t('lines')}</a>{/if}</td>
                        <td>{#if owned}<span class="ol-badge fw-owner" data-owner={r.owner}>{ownerLabel(r.owner)}</span>{/if}</td>
                        <td><input class="hmm-input fw-comment" bind:value={r.comment} disabled={locked} aria-label={t('Comment')} /></td>
                        <td class="fw-actions">
                            {#if !locked}
                                <button type="button" class="hmm-button" onclick={() => duplicate(i)} aria-label={t('Duplicate')} title={t('Duplicate')}>⧉</button>
                                <button type="button" class="hmm-button" onclick={() => remove(i)} aria-label={t('Delete')} title={t('Delete')}>✕</button>
                            {/if}
                        </td>
                    </tr>
                {/each}
            </tbody>
        </table>
        <div class="ol-sronly" role="status" aria-live="polite">{sort.live}</div>
    </div>

    {#if !locked}
        <div class="ol-toolbar fw-toolbar">
            <button type="button" class="hmm-button" onclick={add} disabled={busy !== ''}>{t('Add rule')}</button>
            <button type="button" class="hmm-button primary" onclick={apply} disabled={!dirty || busy !== '' || badPorts || badLocalV6}>{t('Apply')}</button>
            {#if badPorts}<span class="ol-warn" data-notice="fw-badport">{t('A port field holds neither a port nor a range.')}</span>{/if}
            {#if dirty || view.draft}<button type="button" class="hmm-button" onclick={discard} disabled={busy !== ''}>{t('Discard changes')}</button>{/if}
        </div>
        {#if dirty}
            <div class="fw-diff" data-diff>
                <div class="ol-muted">{t('Not applied yet:')}</div>
                <ul>{#each changes as c, i (i)}<li>{c}</li>{/each}</ul>
            </div>
        {/if}
    {/if}

    </WarnEdge>

    <!-- occulited task 12: hmipserver's port listening beyond the loopback -->
    <WarnEdge ids={['hmip-port-open']}>
    <h2>{t('Listening')}<Help>{t('Every socket on the system that waits for connections, the process holding it, and per family the rule that decides its packets - or the policy, when no rule names its port. A socket bound to loopback is not reachable from the network.')}</Help></h2>
    {#if !listenersLoaded}
        <Loading />
    {:else}
    <div class="fw-filter">
        <input class="hmm-input" placeholder={t('Filter the listeners')} aria-label={t('Filter the listeners')} bind:value={lFilter} />
        {#if lFilter.trim()}<span class="ol-muted">{t('{n} of {total} listeners shown', {n: shownListeners.length, total: listeners.length})}</span>{/if}
    </div>
    <div class="ol-scroll-x">
        <table class="ol-table fw-listeners" use:holdColumns={lFilter.trim() !== ''}>
            <thead><tr>
                <th class="ol-sort" onclick={() => lSortBy('proto')}>Protocol{lArrow('proto')}</th>
                <th class="ol-sort" onclick={() => lSortBy('address')}>{t('Address')}{lArrow('address')}</th>
                <th class="ol-sort" onclick={() => lSortBy('port')}>Port{lArrow('port')}</th>
                <th class="ol-sort" onclick={() => lSortBy('process')}>{t('Process')}{lArrow('process')}</th>
                <th class="ol-sort" onclick={() => lSortBy('rule')}>{t('Rule')}{lArrow('rule')}</th>
                <th></th>
            </tr></thead>
            <tbody>
                <!-- one port is bound on several addresses (B-80): protocol, address and port are unique -->
                {#each shownListeners as l (`${l.proto} ${l.address} ${l.port}`)}
                    <tr data-listener={`${l.proto}/${l.address}/${l.port}`} class:fw-uncovered={uncovered(l)}>
                        <td>{l.proto}</td>
                        <td class="hmm-mono">{l.address}</td>
                        <td class="hmm-mono">{l.port}</td>
                        <td>{#if l.process}{l.process} <span class="ol-muted">({l.pid}{l.unit ? `, ${l.unit}` : ''})</span>{:else}<span class="ol-muted">—</span>{/if}</td>
                        <td class="fw-cover">{#each coverLines(l) as c, i (i)}<div>{c}</div>{/each}</td>
                        <td>{#if !locked && uncovered(l)}<button type="button" class="hmm-button" onclick={() => acceptFor(l)} disabled={busy !== ''}>{t('Add ACCEPT rule')}</button>{/if}</td>
                    </tr>
                {/each}
                {#if listeners.length === 0}
                    <tr><td colspan="6" class="ol-muted">{t('Nothing is listening, or /proc is not readable.')}</td></tr>
                {/if}
            </tbody>
        </table>
    </div>
    {/if}
    </WarnEdge>
{/if}

<style>
    .fw-port { width: 5.5em; }
    .fw-source { width: 9.5em; }
    .fw-srccell { white-space: nowrap; }
    .fw-logcell { white-space: nowrap; }
    .fw-dport { width: 7em; }
    .fw-pkts { text-align: right; }
    .fw-policy-pkts { font-weight: normal; font-size: 0.85em; }
    .fw-off td:not(:nth-last-child(-n + 1)) { opacity: 0.45; }
    .fw-invalid { border-color: var(--hmm-error); outline: 1px solid var(--hmm-error); }
    .fw-loglink { margin-left: 6px; font-size: 0.9em; }
    .fw-srcbtn { padding: 0 6px; margin-left: 2px; }
    /* fixed to the window, so .ol-menupop's min-width: 100% would be the window's width: as wide
       as its entries instead */
    .fw-srcmenu { z-index: 50; max-height: 50vh; overflow-y: auto; min-width: 0; width: max-content; max-width: 22em; }
    .fw-dest { width: 9em; }
    .fw-comment { width: 100%; min-width: 18em; }
    .fw-policybox { margin: 8px 0 12px; border-left: 4px solid var(--hmm-ok); }
    .fw-policybox.fw-policybox-open { border-left-color: var(--hmm-error); }
    .fw-policyhead { font-size: 1.15em; margin-bottom: 8px; }
    .fw-policy-sel { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 22px; font-size: 1.1em; }
    .fw-policy-sel label { display: inline-flex; align-items: center; gap: 8px; white-space: nowrap; font-weight: 600; }
    .fw-policy-sel select { font-weight: 700; min-width: 7em; }
    .fw-policy-drop { color: var(--hmm-ok); }
    .fw-policy-accept { color: var(--hmm-error); }
    .fw-policybox .ol-notice { margin: 10px 0 0; }
    .fw-localbox { margin: 0 0 12px; }
    .fw-localhead { margin-bottom: 4px; }
    .fw-localbox .fw-local { margin: 0 0 8px; }
    .fw-localrow { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; }
    .fw-locallabel { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; flex: 1 1 20em; }
    .fw-localv6 { flex: 1 1 16em; max-width: 44em; }
    .fw-filter { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; margin: 0 0 10px; }
    .fw-framebox { margin-left: auto; }
    .fw-frame-open .fw-framebox { flex-basis: 100%; margin-left: 0; }
    .fw-actions { white-space: nowrap; }
    .fw-n .ol-grip { margin-right: 4px; }
    .fw-actions button { padding: 0 6px; }
    .fw-frame pre { margin: 2px 0 8px; font-size: 0.85em; white-space: pre-wrap; }
    .fw-diff ul { margin: 4px 0; padding-left: 18px; }
    .fw-toolbar { margin-top: 10px; }
    .fw-pending button { margin-left: 8px; }
    .ol-scroll-x { max-width: 100%; overflow-x: auto; }
    .fw-rules td, .fw-listeners td { white-space: nowrap; }
</style>
