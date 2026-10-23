<script lang="ts">
    /*
     * The App (task 193): the everyday view of the system for the person who lives in the house
     * - favorites, rooms and functions, each a page of the assigned channels' tiles - beside
     * Status, which is for the person who runs it. Phase 2: the shell - the drawer generated from
     * the metadata tree (folded sections remembered per browser, the whole tree indented), the
     * favorites leaf of this account, Servicemeldungen with its count, Einstellungen for the
     * accounts that may configure; a floating hamburger on a phone that hides while the page
     * scrolls down; the leaf pages as tiles without values yet (phase 3 brings the channel model,
     * the values over lite-rpc and the controls).
     */
    import {onMount, untrack} from 'svelte';
    import {link, navigate, replace, router} from '../lib/router.svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type MetaSnapshot, type MetaNode, type MetaObject} from '../lib/api';
    import {auth} from '../lib/auth.svelte';
    import {prefs} from '../lib/prefs.svelte';
    import {i18n, t} from '../lib/i18n.svelte';
    import Icon from '../lib/Icon.svelte';
    import Loading from '../lib/Loading.svelte';
    import {actionOf, blindAction, formatPower, heldKeys, isMeter, isOn, motion, withPending, LEVEL_KEYS, longAction, mainAction, meterPairs, shown as hasUI, stateText, thermoParts, thermoRow, working, type Model, type Values} from '../lib/app/channels';
    import {fit} from '../lib/app/fit';
    import {durationLine, hasDuration, nextDurationChange} from '../lib/app/duration';
    import {loadChannels, openEvents, operable, setValue, type Channel, putValues} from '../lib/app/rpc';
    import type {StreamState} from '../lib/app/eventstream';
    import {subscribeShell} from '../lib/shellstream/client';
    import Sheet from '../lib/app/Sheet.svelte';
    import AssignSheet from '../lib/app/AssignSheet.svelte';
    import TaxonomySheet from '../lib/app/TaxonomySheet.svelte';
    import ForeignNames from '../lib/app/ForeignNames.svelte';
    import {active, cornerMessages, deviceOf, isMaintenanceKey, isProblem, type CornerMessage} from '../lib/app/badges';
    import {lucideSVG} from '../lib/app/lucide';
    import {ask} from '../lib/dialog.svelte';
    import type {ServiceMessage} from '../lib/api';
    import {askText} from '../lib/dialog.svelte';
    import {displayNames, loadDisplayNames} from '../lib/app/displaynames.svelte';
    import {realName, shownName} from '../lib/app/displaynames';
    import {shortenName} from '../lib/app/shorten';
    import {isKnxName} from '../lib/app/knxall';
    import {ensureAllIcons, knxAll} from '../lib/app/knxall.svelte';
    import {boostActive, boostCommand, BoostTracker, canBoost, plainMode, RING_C, RING_R, ringOffset, splitSeconds, tileMode, type BoostInfo, type BoostSource, type TileMode} from '../lib/app/boost';
    import {isGroupRef, loadGroupMembers} from '../lib/app/groups';
    import {chosenIcon, chosenIcons, CONTACT_STATES, hasTilted, isWindow, KNX, stateIcon, type StateIcon} from '../lib/app/stateicons';

    const FAVORITE_ENUM = 'favorite';
    // task 75: a tile whose channel is not loaded yet shows the neutral icon in its circle
    const NO_ICON: StateIcon = {name: 'weather_station_generic', svg: KNX.weather_station_generic ?? '', tone: ''};
    let snap = $state<MetaSnapshot | null>(null);
    let error = $state('');
    let drawerOpen = $state(false);
    let fabHidden = $state(false);
    const life = pageLife();

    async function load() {
        try {
            snap = await api.get<MetaSnapshot>('/api/meta/v1/snapshot');
            error = '';
        } catch (e) {
            // a fetch that never reached the system (offline, the standalone window without a network)
            error = e instanceof TypeError ? t('No connection to the system.') : (e as Error).message;
        }
        try {
            const m = await api.get<{count: number; messages: ServiceMessage[]}>('/api/system/v1/service-messages');
            takeMessages(m);
        } catch {
            /* the corners stay as they were */
        }
    }
    // ---- the badges: the active maintenance messages per device, from occulited's store (the whole
    // house, pushed as they change) and the :0 channel's events of an open page
    let maint = $state<Map<string, Set<string>>>(new Map());
    function takeMessages(m: {count: number; messages: ServiceMessage[]}) {
        const next = new Map<string, Set<string>>();
        for (const x of m.messages ?? []) {
            const dev = `${x.interface}.${x.address}`;
            if (!next.has(dev)) next.set(dev, new Set());
            next.get(dev)!.add(x.key);
        }
        maint = next;
    }
    function maintEvent(ref: string, key: string, value: unknown) {
        if (!isMaintenanceKey(key)) return;
        const dev = deviceOf(ref);
        const next = new Map(maint);
        const set = new Set(next.get(dev) ?? []);
        if (active(value)) set.add(key);
        else set.delete(key);
        if (set.size) next.set(dev, set);
        else next.delete(dev);
        maint = next;
    }
    // task 92: the device's messages for the tile's corner, the tooltip and the sheet (STICKY_UNREACH left out)
    function corner(ref: string): CornerMessage[] {
        return cornerMessages(maint.get(deviceOf(ref)) ?? []);
    }
    // the rollup: every node with a problem somewhere below it
    const problemNodes = $derived.by(() => {
        const out = new Set<string>();
        if (!snap) return out;
        for (const [ref, o] of Object.entries(snap.objects)) {
            // a device that only "was unreachable" is no problem of now (B-46)
            if (!isProblem(maint.get(deviceOf(ref)) ?? [])) continue;
            for (const p of o.enums) {
                const parts = p.split('/');
                for (let i = 2; i <= parts.length; i++) out.add(parts.slice(0, i).join('/'));
            }
        }
        return out;
    });
    function showMessages(ref: string, name: string) {
        const list = corner(ref).map((b) => `• ${t(b.label)}`).join('\n');
        void ask({title: name, message: list, confirm: t('OK'), cancel: ''});
    }
    // the corner opens the sheet, which lists the messages; a tile without a sheet names them in a dialog
    function cornerClick(ref: string) {
        if (opens(ref)) openSheet(ref);
        else showMessages(ref, nameOf(ref));
    }
    // a temperature on the thermostat tile: one decimal in the reader's notation, '–' without a value
    function deg(v: number | undefined): string {
        if (v === undefined) return '–';
        const s = v.toFixed(1);
        return `${i18n.language === 'de' ? s.replace('.', ',') : s}°`;
    }
    // the service messages: the topic of the shell's stream (occulited B-53: one stream for every
    // window of the browser), while the page is shown
    $effect(() => {
        if (!life.active) return;
        return subscribeShell('service-messages', (data) => {
            try {
                takeMessages(JSON.parse(data));
            } catch {
                /* a torn line: the poll repairs it */
            }
        });
    });
    onMount(() => {
        void load();
        void loadDisplayNames();
        const poll = setInterval(() => life.active && load(), 30000);
        // back on the page: the tree and the messages, and the values of its tiles again
        const stopReturn = life.onReturn(() => {
            void load();
            void loadDisplayNames();
            // a parked stream: the return below reads them, with the stream (occulited B-53)
            if (!parked) reloadValues();
        });
        // the floating button hides while the page scrolls down, comes back on scroll up
        const port = document.querySelector('.ol-scrollport');
        let last = port?.scrollTop ?? 0;
        const onScroll = () => {
            const y = port?.scrollTop ?? 0;
            fabHidden = y > last + 4 && y > 40;
            if (y < last - 4 || y <= 40) fabHidden = false;
            last = y;
        };
        port?.addEventListener('scroll', onScroll, {passive: true});
        const onKey = (ev: KeyboardEvent) => {
            if (ev.key === 'Escape' && drawerOpen) drawerOpen = false;
        };
        window.addEventListener('keydown', onKey);
        return () => {
            clearInterval(poll);
            stopReturn();
            port?.removeEventListener('scroll', onScroll);
            window.removeEventListener('keydown', onKey);
        };
    });

    // the last page is remembered per browser (the maintainer, 2026-09-22): the Control tab - the bare
    // /app - opens where the App was left, so a trip to a System page and back changes nothing
    const LAST = 'ol.app.last';
    $effect(() => {
        const p = router.path;
        if (p === '/app') {
            let last = '';
            try {
                last = localStorage.getItem(LAST) ?? '';
            } catch {
                /* not remembered */
            }
            if (last.startsWith('/app/') && last !== p) untrack(() => replace(last));
            return;
        }
        if (p.startsWith('/app/')) {
            try {
                localStorage.setItem(LAST, p);
            } catch {
                /* a private window */
            }
        }
    });

    // ---- the route: /app (= favorites), /app/favorites, /app/e/<enum>/<node path>, /app/messages, /app/settings
    const rest = $derived(router.path === '/app' ? '' : router.path.startsWith('/app/') ? router.path.slice(5) : '');
    type View = {kind: 'favorites'} | {kind: 'node'; enumId: string; path: string} | {kind: 'messages'} | {kind: 'unassigned'};
    const view = $derived.by<View>(() => {
        if (rest === '' || rest === 'favorites') return {kind: 'favorites'};
        if (rest === 'messages') return {kind: 'messages'}; // task 98: left for the Status page's service messages
        if (rest === 'unassigned') return {kind: 'unassigned'};
        if (rest.startsWith('e/')) {
            const [enumId, ...node] = rest.slice(2).split('/');
            return {kind: 'node', enumId: decodeURIComponent(enumId ?? ''), path: node.map(decodeURIComponent).join('/')};
        }
        return {kind: 'favorites'};
    });

    // task 98: the drawer has no Servicemeldungen entry any more; an old link to it goes to the Status page's list
    $effect(() => {
        if (view.kind === 'messages') replace('/#service-messages');
    });

    // ---- the drawer: every enum but the favorites, as a foldable tree; Favoriten is this account's node
    // every enum but the favorites, and only those with nodes (the maintainer, 2026-09-22: empty ones are hidden)
    // occulited task 18 (the maintainer, 2026-10-04): rooms always first, functions second, whatever the
    // language and the order the snapshot's object brings them in (Go writes a map's keys sorted, so
    // "function" came before "room"); any other enum after them, in the snapshot's order
    const sectionRank = (id: string) => (id === 'room' || id === 'rooms' ? 0 : id === 'function' || id === 'functions' ? 1 : 2);
    const sections = $derived(
        Object.entries(snap?.enums ?? {})
            .filter(([id, e]) => id !== FAVORITE_ENUM && e.tree.length > 0)
            .map((x, i) => ({x, i}))
            .sort((a, b) => sectionRank(a.x[0]) - sectionRank(b.x[0]) || a.i - b.i)
            .map(({x}) => x),
    );
    const favoritesPath = $derived(auth.accountId ? `${FAVORITE_ENUM}/${auth.accountId}` : '');
    const canConfigure = $derived(auth.level === 'configure' || auth.level === 'administer');
    // which sections and nodes are open: remembered per browser
    let open = $state<Record<string, boolean>>({});
    onMount(() => {
        try {
            open = JSON.parse(localStorage.getItem('ol.app.open') ?? '{}');
        } catch {
            open = {};
        }
    });
    function toggle(key: string) {
        open = {...open, [key]: !(open[key] ?? true)};
        try {
            localStorage.setItem('ol.app.open', JSON.stringify(open));
        } catch {
            /* a private window: not remembered */
        }
    }
    const isOpen = (key: string) => open[key] ?? true;
    function nodeHref(enumId: string, path: string): string {
        return `/app/e/${encodeURIComponent(enumId)}/${path.split('/').map(encodeURIComponent).join('/')}`;
    }
    const activePath = $derived(view.kind === 'node' ? `${view.enumId}/${view.path}` : view.kind === 'favorites' ? favoritesPath : '');
    function pick() {
        drawerOpen = false;
    }
    // a node's badge: something inside has a problem (phase 3 fills it from the maintenance channels)
    function nodeLabel(n: MetaNode): string {
        return n.name || n.id;
    }
    function enumLabel(id: string, e: {name: Record<string, string>}): string {
        const lang = document.documentElement.lang === 'de' ? 'de' : 'en';
        return e.name[lang] ?? e.name.en ?? id;
    }

    // ---- the leaf: the assigned channels (the subtree), sorted by rank (favorites) or by name
    // occulited B-84: the interfaces lite-rpc said it does not have; their channels, and those of
    // ReGa's System pseudo-interface, are no tiles - they cannot be operated
    let unknownIfaces = $state<string[]>([]);
    function isChannel(ref: string): boolean {
        const i = ref.lastIndexOf(':');
        return i > 0 && ref.slice(i + 1) !== '0' && operable(ref, unknownIfaces);
    }
    function rankOf(o: MetaObject, path: string): number | undefined {
        const ns = o.meta?.occulite as {order?: Record<string, number>} | undefined;
        const r = ns?.order?.[path];
        return typeof r === 'number' ? r : undefined;
    }
    function members(path: string, subtree: boolean): {ref: string; o: MetaObject}[] {
        if (!snap) return [];
        const out: {ref: string; o: MetaObject}[] = [];
        for (const [ref, o] of Object.entries(snap.objects)) {
            if (!isChannel(ref)) continue;
            if (o.enums.some((p) => p === path || (subtree && p.startsWith(path + '/')))) out.push({ref, o});
        }
        return out;
    }
    const tiles = $derived.by(() => {
        if (!snap) return [];
        if (view.kind === 'favorites') {
            if (!favoritesPath) return [];
            const m = members(favoritesPath, false);
            return m.sort((a, b) => {
                const ra = rankOf(a.o, favoritesPath), rb = rankOf(b.o, favoritesPath);
                if (ra !== undefined && rb !== undefined && ra !== rb) return ra - rb;
                if (ra !== undefined && rb === undefined) return -1;
                if (ra === undefined && rb !== undefined) return 1;
                return nameOf(a.ref).localeCompare(nameOf(b.ref));
            });
        }
        if (view.kind === 'node') return members(`${view.enumId}/${view.path}`, true).sort((a, b) => tileName(a.ref).localeCompare(tileName(b.ref)));
        if (view.kind === 'unassigned') {
            return Object.entries(snap.objects).filter(([ref, o]) => isChannel(ref) && !o.enums.some((p) => !p.startsWith(FAVORITE_ENUM + '/'))).map(([ref, o]) => ({ref, o})).sort((a, b) => nameOf(a.ref).localeCompare(nameOf(b.ref)));
        }
        return [];
    });
    function nodeByPath(enumId: string, path: string): MetaNode | undefined {
        let list = snap?.enums[enumId]?.tree ?? [];
        let node: MetaNode | undefined;
        for (const id of path.split('/')) {
            node = list.find((n) => n.id === id);
            if (!node) return undefined;
            list = node.children ?? [];
        }
        return node;
    }
    const title = $derived.by(() => {
        switch (view.kind) {
            case 'favorites': return t('Favorites');
            case 'messages': return t('Service messages');
            case 'unassigned': return t('Unassigned channels');
            case 'node': return nodeByPath(view.enumId, view.path)?.name ?? view.path;
        }
    });
    // ---- phase 3: the channels' models and values over lite-rpc, live from the stream
    let channels = $state<Map<string, Channel>>(new Map());
    let rpcError = $state('');
    let acting = $state<Record<string, string>>({}); // ref -> a failed command's message
    let closeEvents: () => void = () => undefined;
    // occulited B-47: the snapshot is read again every 30 s, and each time made new tiles of the same
    // channels - which read every value again from the interface processes (BidCos asks the device
    // over the radio for that) and opened the stream anew. The values are loaded when the page's
    // channels are others than the ones loaded, when the page is shown again and when the stream
    // says it lost events; in between the stream keeps them, and it stays open across the pages.
    let loadedRefs = ''; // the channels the values were last loaded for
    let streamFor = ''; // the interfaces the open stream is for
    // occulited task 19: where the stream stands, and the quiet "not live" mark in the title - shown
    // when the stream has not been live for NOT_LIVE_AFTER (a reconnect after a restart of the
    // stream is quicker, and says nothing), with the reason: another window holds the account's
    // streams (429), the connection is being made again, or the system refused it
    const NOT_LIVE_AFTER = 2000;
    let streamState = $state<StreamState | ''>('');
    let notLive = $state<'' | 'busy' | 'reconnecting' | 'refused'>('');
    $effect(() => {
        const s = streamState;
        if (s === '' || s === 'live') {
            notLive = '';
            return;
        }
        if (notLive) {
            notLive = s; // shown already: the new reason at once
            return;
        }
        const timer = setTimeout(() => (notLive = s), NOT_LIVE_AFTER);
        return () => clearTimeout(timer);
    });
    const notLiveReason = $derived(notLive === 'busy' ? t('too many windows') : notLive === 'refused' ? t('connection refused') : t('reconnecting'));
    const notLiveWhy = $derived(
        notLive === 'busy'
            ? t('This account has as many live connections open as the system allows (other windows or devices). The values here are not updated until one of them closes; this window keeps trying.')
            : notLive === 'refused'
              ? t('The system refused the live connection. Reload the page to try again.')
              : t('The live connection to the system is interrupted (a restart, the network). The values shown may be out of date; it is made again by itself.'),
    );
    // occulited B-103: a heating group's members (by the group device's address), read once per page for the groups
    // among the tiles; their channels are loaded and followed with the tiles' (not shown), so a group that says
    // BOOST_MODE true with BOOST_TIME 0 counts down from its members' highest time left
    let groupMembers = $state<Record<string, string[]>>({});
    const groupDevices = $derived([...new Set(tiles.map((x) => x.ref).filter(isGroupRef).map((r) => r.slice(r.indexOf('.') + 1).split(':')[0]!))].sort());
    let groupsAsked = '';
    $effect(() => {
        const want = groupDevices.filter((d) => !(d in untrack(() => groupMembers)));
        const key = want.join(' ');
        if (!key || key === groupsAsked) return;
        groupsAsked = key;
        void loadGroupMembers(want).then((m) => (groupMembers = {...groupMembers, ...Object.fromEntries(m)}));
    });
    function membersOf(ref: string): string[] {
        if (!isGroupRef(ref)) return [];
        return groupMembers[ref.slice(ref.indexOf('.') + 1).split(':')[0]!] ?? [];
    }
    const refs = $derived([...new Set([...tiles.map((x) => x.ref), ...tiles.flatMap((x) => membersOf(x.ref))])]);
    // occulited task 104: only channels with a UI are tiles - a channel the interface describes without a widget
    // (a week profile, a heating group's receiver, an empty paramset) is left out everywhere, in the unassigned view,
    // rooms, functions and favorites; the values are still read for every member, so the model is known
    // occulited task 109: a switch and its device's power meter in one view are one tile - the switch's, with the power
    // in its state line; the meter's tile is left out (a meter alone keeps it)
    const pairs = $derived(meterPairs(tiles.map((x) => ({ref: x.ref, model: channels.get(x.ref)?.model}))));
    const uiTiles = $derived(tiles.filter((x) => hasUI(channels.get(x.ref)) && !pairs.hidden.has(x.ref)));
    /** the power meter channel merged into a switch tile, if any */
    function meterOf(ref: string): Channel | undefined {
        const r = pairs.meter.get(ref);
        return r ? channels.get(r) : undefined;
    }
    /** the tile's state line: a switch with a meter says the power after its state ("On · 12.5 W") */
    function tileState(ref: string, c: Channel, m: Model): string {
        // a meter's own tile: the same units and decimals as the switch's line
        const s = isMeter(m) && m.stateKey === 'POWER' ? formatPower(c.values.POWER, i18n.language) || stateText(m, c.values, c.desc, t) : stateText(m, c.values, c.desc, t);
        const mc = meterOf(ref);
        const p = mc ? formatPower(mc.values[mc.model.stateKey || 'POWER'], i18n.language) : '';
        return p ? (s ? `${s} · ${p}` : p) : s;
    }
    $effect(() => {
        const list = refs;
        untrack(() => {
            const key = list.join(' ');
            if (key === loadedRefs) return;
            loadedRefs = key;
            void loadValues(list);
        });
    });
    function reloadValues() {
        const list = untrack(() => refs);
        loadedRefs = list.join(' ');
        void loadValues(list);
    }
    function onEvent(ref: string, key: string, value: unknown, changedAt?: string) {
        if (ref.endsWith(':0')) {
            maintEvent(ref, key, value);
            return;
        }
        const c = channels.get(ref);
        if (!c) return;
        // a key's press is a moment: the tile shows it for a second
        if ((key === 'PRESS_SHORT' || key === 'PRESS_LONG') && value) {
            flash(ref, key === 'PRESS_LONG' ? t('long press') : t('short press'));
            return;
        }
        const m = new Map(channels);
        const values = {...c.values, [key]: value};
        // a level reported while the channel ramps is intermediate (the maintainer,
        // 2026-09-22: a slider set to 100 % jumped back to 0.5 % and crept up): keep the
        // shown level - the one sent, or the last settled one - until the ramp is over,
        // then take what the channel reports
        // B-108: the same while a click's action is pending, and for a lock its state too - a device that echoes the
        // target, or reports it before its activity, shows it only once it stands
        const held = heldKeys(c.model);
        let ended = false;
        if (held.has(key) && (working(c.values) || !!pendingAct[ref])) {
            pendingLevel.set(`${ref}|${key}`, value);
            values[key] = c.values[key];
        } else if ((key === 'WORKING' || key === 'PROCESS') && !working(values)) {
            takeHeld(ref, held, values);
            ended = true;
        }
        // task 95: when the system saw the value change; an event without it leaves the time unknown
        const changed = {...c.changed};
        if (changedAt) {
            changed[key] = changedAt;
            minuteNow = Date.now(); // the new time is counted from now, not from the last full minute
        } else delete changed[key];
        m.set(ref, {...c, values, changed});
        channels = m;
        // B-108: the device's own report ends the pending action - its activity (which then shows itself) or its end
        if (pendingAct[ref] && (ended || motion(c.model, values, c.desc))) endPending(ref, false);
    }
    // ---- occulited B-108: the action a click started, shown at once until the device reports its own
    let pendingAct = $state<Record<string, {dir?: 'up' | 'down'}>>({});
    const pendingTimers = new Map<string, ReturnType<typeof setTimeout>>();
    /** the held reports of a channel's datapoints into values (the action is over) */
    function takeHeld(ref: string, keys: Set<string>, values: Values) {
        for (const k of keys) {
            const p = pendingLevel.get(`${ref}|${k}`);
            if (p !== undefined) {
                values[k] = p;
                pendingLevel.delete(`${ref}|${k}`);
            }
        }
    }
    /** starts the pending action a write of key=value begins; false when it begins none */
    function beginPending(ref: string, key: string, value: unknown): boolean {
        const c = channels.get(ref);
        const a = c ? actionOf(c.model, key, value, c.values, c.desc) : undefined;
        if (!c || !a) return false;
        clearTimeout(pendingTimers.get(ref));
        pendingTimers.set(ref, setTimeout(() => endPending(ref, true), a.ms));
        pendingAct = {...pendingAct, [ref]: a.dir ? {dir: a.dir} : {}};
        return true;
    }
    /** ends it; release: the device has not taken over, so what it reported meanwhile shows now (timeout, STOP, an error) */
    function endPending(ref: string, release: boolean) {
        clearTimeout(pendingTimers.get(ref));
        pendingTimers.delete(ref);
        if (!pendingAct[ref]) return;
        const p = {...pendingAct};
        delete p[ref];
        pendingAct = p;
        const c = channels.get(ref);
        if (release && c && !working(c.values)) {
            const values = {...c.values};
            takeHeld(ref, heldKeys(c.model), values);
            const m = new Map(channels);
            m.set(ref, {...c, values});
            channels = m;
        }
    }
    /** the channel as the tile and the sheet show it: moving while its click's action is pending */
    function viewOf(ref: string): Channel | undefined {
        const c = channels.get(ref);
        const p = pendingAct[ref];
        return c && p ? {...c, values: withPending(c.values, c.desc, p.dir)} : c;
    }
    // task 95: the durations are recomputed on the full minute of each, without fetching anything
    let minuteNow = $state(Date.now());
    const durationTimes = $derived(tiles.flatMap((x) => {
        const c = channels.get(x.ref);
        const at = c && hasDuration(c.model) ? c.changed?.[c.model.stateKey] : undefined;
        return at ? [Date.parse(at)] : [];
    }));
    $effect(() => {
        void minuteNow;
        const wait = nextDurationChange(durationTimes, Date.now());
        if (wait === undefined) return;
        const timer = setTimeout(() => (minuteNow = Date.now()), wait);
        return () => clearTimeout(timer);
    });
    // occulited task 112 (the maintainer's M3): the thermostat's mode as a badge on its disc. A BidCos boost hides the
    // mode (CONTROL_MODE 3), so the badge keeps the tile's last one - the mode that applies again after the boost
    const lastMode = new Map<string, TileMode>();
    function modeOf(ref: string, values: Values): TileMode | undefined {
        const md = tileMode(values, lastMode.get(ref));
        if (md && !boostActive(values)) lastMode.set(ref, md);
        return md;
    }
    const modeWord = (md: TileMode) => (md === 'manual' ? t('Manual') : md === 'holiday' ? t('Holiday/Party') : t('Auto'));
    // occulited task 103: a thermostat's boost - the countdown ticks every second while any tile boosts,
    // counted down locally from the device's last report (BoostTracker re-syncs on each new one)
    const boosts = new BoostTracker();
    let secondNow = $state(Date.now());
    const anyBoost = $derived(uiTiles.some((x) => { const c = channels.get(x.ref); return c?.model.widget === 'thermostat' && boostActive(c.values); }));
    $effect(() => {
        if (!anyBoost) return;
        const id = setInterval(() => (secondNow = Date.now()), 1000);
        return () => clearInterval(id);
    });
    /** the tile's boost countdown, or null; it reads secondNow so the tile re-renders every second */
    function boostOf(ref: string, c: Channel): BoostInfo | null {
        void secondNow;
        // B-103: a group's time is its members'
        const members: BoostSource[] = membersOf(ref).flatMap((r) => {
            const mc = channels.get(r);
            return mc ? [{ref: r, values: mc.values, desc: mc.desc, changed: mc.changed}] : [];
        });
        return boosts.update(ref, c.values, Date.now(), {desc: c.desc, changed: c.changed, members});
    }
    function boostText(b: BoostInfo): string {
        return b.full > 0 || b.remaining > 0 ? t('Boost: {m} min {s} s', {m: String(splitSeconds(b.remaining).m), s: splitSeconds(b.remaining).s}) : t('Boost');
    }
    // the disc starts a boost, and ends a running one (BidCos: back to the mode before it); the tile shows the
    // new mode at once and takes it back when the write is refused
    let boostBusy = $state<Record<string, boolean>>({});
    async function toggleBoost(ref: string) {
        const c = channels.get(ref);
        if (!c || boostBusy[ref]) return;
        const start = !boostActive(c.values);
        const before = boosts.before(ref) ?? plainMode(c.values);
        const cmd = boostCommand(c.desc, c.values, start, before);
        const hmip = c.values.BOOST_MODE !== undefined && c.values.CONTROL_MODE === undefined;
        const optimistic = hmip ? {BOOST_MODE: start} : {CONTROL_MODE: start ? 3 : before === 'manual' ? 1 : 0};
        boostBusy = {...boostBusy, [ref]: true};
        acting = {...acting, [ref]: ''};
        const undo = apply(ref, optimistic);
        try {
            const keys = Object.keys(cmd);
            if (keys.length === 1) await setValue(ref, keys[0]!, cmd[keys[0]!]);
            else await putValues(ref, cmd);
        } catch (e) {
            undo();
            acting = {...acting, [ref]: (e as Error).message};
        } finally {
            const b = {...boostBusy};
            delete b[ref];
            boostBusy = b;
        }
    }
    async function loadValues(list: string[]) {
        if (list.length === 0) return;
        try {
            const r = await loadChannels(list);
            const next = new Map(channels);
            for (const [ref, c] of r.channels) next.set(ref, c);
            channels = next;
            minuteNow = Date.now(); // task 95: the durations as of this read
            rpcError = r.errors.join('; ');
            if (r.unknown.some((i) => !unknownIfaces.includes(i))) unknownIfaces = [...new Set([...unknownIfaces, ...r.unknown])];
            // a read that failed is tried again with the next snapshot
            if (rpcError) loadedRefs = '';
            // one stream for the interfaces of every channel loaded so far, from where the values
            // were read; a page with the same interfaces keeps it
            const ifaces = [...new Set([...next.values()].map((c) => c.iface))].sort();
            if (ifaces.join(' ') !== streamFor && untrack(() => shown)) {
                closeEvents();
                streamFor = ifaces.join(' ');
                streamState = '';
                closeEvents = openEvents(ifaces, onEvent, {
                    lastEventId: r.eventId,
                    onResync: reloadValues,
                    onState: (s) => (streamState = s),
                    // refused: nothing is tried again by the stream; the next load of values opens one
                    onRefused: () => (streamFor = ''),
                });
            }
        } catch (e) {
            rpcError = (e as Error).message;
            loadedRefs = '';
        }
    }
    onMount(() => () => closeEvents());
    // occulited B-53: lite-rpc's stream only while the app is shown - not while another page of the
    // shell is (the pages stay mounted, task 177) or the window is hidden, so the account's streams
    // (rpc.streams_per_session) and the browser's connections are the shown windows'. After
    // HIDE_GRACE, so a quick look at another window keeps it; shown again, the values are read again
    // and the stream opened from where they were read.
    const HIDE_GRACE = 10_000;
    let docVisible = $state(typeof document === 'undefined' || document.visibilityState !== 'hidden');
    const shown = $derived(life.active && docVisible);
    let parked = false;
    onMount(() => {
        const f = () => (docVisible = document.visibilityState !== 'hidden');
        document.addEventListener('visibilitychange', f);
        return () => document.removeEventListener('visibilitychange', f);
    });
    $effect(() => {
        if (shown) {
            if (parked) {
                parked = false;
                untrack(reloadValues);
            }
            return;
        }
        const timer = setTimeout(() => {
            parked = true;
            closeEvents();
            closeEvents = () => undefined;
            streamFor = '';
            streamState = '';
        }, HIDE_GRACE);
        return () => clearTimeout(timer);
    });
    function modelOf(ref: string): Model | undefined {
        return channels.get(ref)?.model;
    }
    function tileKind(ref: string): string {
        const m = modelOf(ref);
        return m ? m.tile : 'read';
    }
    // the levels reported during a ramp, applied when it ends
    const pendingLevel = new Map<string, unknown>();
    // pressed keys flash their circle and say the press for a second (a press is an event, not a state)
    let flashed = $state<Record<string, string>>({});
    function flash(ref: string, text: string) {
        flashed = {...flashed, [ref]: text};
        setTimeout(() => {
            const f = {...flashed};
            delete f[ref];
            flashed = f;
        }, 1200);
    }
    async function act(ref: string, long = false) {
        const c = viewOf(ref);
        if (!c) return;
        const a = long ? longAction(c.model, c.desc) : mainAction(c.model, c.values, c.desc);
        if (!a) return;
        const blind = c.model.widget === 'blind';
        if (blind && discBusy[ref]) return;
        acting = {...acting, [ref]: ''};
        // the tile shows what was sent at once (the stream confirms it); a refused write is taken back. Task 114: a
        // blind shows where it is, not where it was sent - its movement comes back as ACTIVITY_STATE/PROCESS or
        // DIRECTION/WORKING, and the level it reports on the way is held until it stands (onEvent)
        // B-108: a lock does not show its target either - the click's action is pending until the device reports
        const lock = c.model.widget === 'lock';
        const undo = c.model.widget === 'button' || blind || lock ? () => undefined : apply(ref, {[a.key]: a.value});
        if (blind) discBusy = {...discBusy, [ref]: true};
        if (a.key === 'STOP') endPending(ref, true);
        else beginPending(ref, a.key, a.value);
        try {
            await setValue(ref, a.key, a.value);
            if (c.model.widget === 'button') flash(ref, long ? t('long press') : t('short press'));
        } catch (e) {
            endPending(ref, true);
            undo();
            acting = {...acting, [ref]: (e as Error).message};
        } finally {
            if (blind) {
                const b = {...discBusy};
                delete b[ref];
                discBusy = b;
            }
        }
    }
    // occulited task 114: a blind's disc write in flight (its disc is busy until the write is answered)
    let discBusy = $state<Record<string, boolean>>({});
    /** what a click on a blind's disc does, in words (its aria-label and tooltip) */
    function blindVerb(c: Channel): string {
        const b = blindAction(c.values, c.desc);
        return b === 'stop' ? t('blind stop action') : b === 'open' ? t('blind open action') : t('blind close action');
    }
    // a key's circle: tap = short press, hold (500 ms) = long press; a keyboard press is short
    const HOLD = 500;
    let hold: {ref: string; timer: ReturnType<typeof setTimeout>; fired: boolean} | null = null;
    function pressStart(ref: string) {
        const c = channels.get(ref);
        if (!c || !longAction(c.model, c.desc)) return;
        if (hold) clearTimeout(hold.timer);
        const h = {ref, fired: false, timer: setTimeout(() => {
            h.fired = true;
            void act(ref, true);
        }, HOLD)};
        hold = h;
    }
    // the click that follows a fired hold is not a short press
    const swallow = new Set<string>();
    function pressEnd(ref: string) {
        if (!hold || hold.ref !== ref) return;
        clearTimeout(hold.timer);
        if (hold.fired) {
            swallow.add(ref);
            setTimeout(() => swallow.delete(ref), 300);
        }
        hold = null;
    }
    function pressClick(ref: string) {
        if (swallow.has(ref)) {
            swallow.delete(ref);
            return;
        }
        void act(ref);
    }
    // ---- the very-long press (1.2 s; the maintainer, 2026-09-22): on a card the assignment sheet, on a
    // drawer entry drag-and-drop sorting while the finger stays down, or its menu on a release without a move
    const VERY_LONG = 1200;
    let assignRef = $state<string | null>(null);
    let assignFrom = $state.raw<DOMRect | null>(null);
    // a very-long press holds still: a finger that travels more than SLOP px before the timer fires
    // is a scroll or a swipe, and the press is off (the swipe that closes the drawer, a scrolled grid)
    const SLOP = 12;
    let cardHold: {ref: string; timer: ReturnType<typeof setTimeout>; x: number; y: number} | null = null;
    const swallowCard = new Set<string>();
    // on the favorites page the very-long press arms sorting, as in the drawer: the tile lifts and
    // follows the finger over the grid; a release without a move opens the assignment sheet
    let tileDrag = $state<{ref: string; refs: string[]; moved: boolean; from: DOMRect} | null>(null);
    function cardDown(ref: string, ev: PointerEvent) {
        if (!canConfigure || ev.button !== 0) return;
        if (cardHold) clearTimeout(cardHold.timer);
        const el = ev.currentTarget as HTMLElement;
        const pointerId = ev.pointerId;
        cardHold = {ref, x: ev.clientX, y: ev.clientY, timer: setTimeout(() => {
            cardHold = null;
            swallowCard.add(ref);
            setTimeout(() => swallowCard.delete(ref), 400);
            if (view.kind === 'favorites') {
                tileDrag = {ref, refs: uiTiles.map((x) => x.ref), moved: false, from: el.getBoundingClientRect()};
                try {
                    el.setPointerCapture(pointerId);
                } catch {
                    /* a pointer that is gone */
                }
                if (navigator.vibrate) navigator.vibrate(20);
                return;
            }
            assignFrom = el.getBoundingClientRect();
            assignRef = ref;
        }, VERY_LONG)};
    }
    function tileMove(ev: PointerEvent) {
        if (cardHold && Math.hypot(ev.clientX - cardHold.x, ev.clientY - cardHold.y) > SLOP) {
            clearTimeout(cardHold.timer);
            cardHold = null;
        }
        if (!tileDrag) return;
        const under = document.elementFromPoint(ev.clientX, ev.clientY)?.closest<HTMLElement>('[data-app-tile]');
        const target = under?.dataset.appTile;
        if (!target || target === tileDrag.ref) return;
        const refs = tileDrag.refs.filter((r) => r !== tileDrag!.ref);
        const i = refs.indexOf(target);
        if (i < 0) return;
        // before the target when coming from after it, after it otherwise
        const was = tileDrag.refs.indexOf(tileDrag.ref);
        const at = tileDrag.refs.indexOf(target);
        refs.splice(was < at ? i + 1 : i, 0, tileDrag.ref);
        if (refs.join() !== tileDrag.refs.join()) tileDrag = {...tileDrag, refs, moved: true};
    }
    async function cardUp(ref: string) {
        if (cardHold?.ref === ref) {
            clearTimeout(cardHold.timer);
            cardHold = null;
            return;
        }
        if (!tileDrag || tileDrag.ref !== ref) return;
        const d = tileDrag;
        tileDrag = null;
        if (!d.moved) {
            assignFrom = d.from;
            assignRef = ref;
            return;
        }
        await writeRank(d.refs, ref);
    }
    // the rank: sparse (1000, 2000, ...), the moved card takes the midpoint of its neighbours; when
    // there is no room, or a neighbour has no rank yet, every card is renumbered
    async function writeRank(order: string[], moved: string) {
        if (!snap || !favoritesPath) return;
        const rank = (r: string) => rankOf(snap!.objects[r]!, favoritesPath);
        const i = order.indexOf(moved);
        const prev = i > 0 ? rank(order[i - 1]!) : 0;
        const next = i < order.length - 1 ? rank(order[i + 1]!) : undefined;
        let writes: [string, number][];
        if ((i === 0 || prev !== undefined) && (next === undefined || (prev !== undefined && next - prev >= 2)) && (i === 0 || (prev as number) > 0 || next !== undefined)) {
            const value = next === undefined ? (prev ?? 0) + 1000 : Math.floor(((prev ?? 0) + next) / 2);
            writes = [[moved, value]];
        } else {
            writes = order.map((r, n) => [r, (n + 1) * 1000]);
        }
        try {
            for (const [r, value] of writes) {
                const ns = (snap.objects[r]?.meta?.occulite as Record<string, unknown> | undefined) ?? {};
                const orderMap = (ns.order as Record<string, number> | undefined) ?? {};
                await api.patch(`/api/meta/v1/objects/${encodeURIComponent(r)}`, {meta: {occulite: {...ns, order: {...orderMap, [favoritesPath]: value}}}});
            }
            await load();
        } catch (e) {
            error = (e as Error).message;
        }
    }
    const shownTiles = $derived.by(() => {
        if (!tileDrag) return uiTiles;
        const by = new Map(uiTiles.map((x) => [x.ref, x]));
        return tileDrag.refs.map((r) => by.get(r)).filter((x): x is {ref: string; o: MetaObject} => !!x);
    });
    function cardClick(ref: string, ev: Event) {
        if (swallowCard.has(ref)) {
            swallowCard.delete(ref);
            return;
        }
        openSheet(ref, ev);
    }

    // the drawer: a node's siblings in the tree's order, for the sorting
    function siblings(enumId: string, parentPath: string): MetaNode[] {
        let list = snap?.enums[enumId]?.tree ?? [];
        for (const id of parentPath ? parentPath.split('/') : []) {
            const n = list.find((x) => x.id === id);
            if (!n) return [];
            list = n.children ?? [];
        }
        return list;
    }
    function nodeURL(enumId: string, path: string): string {
        return `/api/meta/v1/enums/${encodeURIComponent(enumId)}/nodes/${path.split('/').map(encodeURIComponent).join('/')}`;
    }
    // the drag: the list being sorted (its parent key) and the ids in their current, live order
    let drag = $state<{enumId: string; parent: string; ids: string[]; id: string; from: number; moved: boolean} | null>(null);
    let nodeHold: {key: string; timer: ReturnType<typeof setTimeout>; x: number; y: number} | null = null;
    const swallowNode = new Set<string>();
    function nodeDown(enumId: string, parent: string, id: string, ev: PointerEvent) {
        if (!canConfigure || ev.button !== 0) return;
        const key = `${enumId}/${parent ? parent + '/' : ''}${id}`;
        if (nodeHold) clearTimeout(nodeHold.timer);
        const el = ev.currentTarget as HTMLElement;
        const pointerId = ev.pointerId;
        nodeHold = {key, x: ev.clientX, y: ev.clientY, timer: setTimeout(() => {
            nodeHold = null;
            swallowNode.add(key);
            const ids = siblings(enumId, parent).map((n) => n.id);
            drag = {enumId, parent, ids, id, from: ids.indexOf(id), moved: false};
            try {
                el.setPointerCapture(pointerId);
            } catch {
                /* a pointer that is gone */
            }
            if (navigator.vibrate) navigator.vibrate(20);
        }, VERY_LONG)};
    }
    function nodeMove(ev: PointerEvent) {
        if (nodeHold && Math.hypot(ev.clientX - nodeHold.x, ev.clientY - nodeHold.y) > SLOP) {
            clearTimeout(nodeHold.timer);
            nodeHold = null;
        }
        if (!drag) return;
        const rows = [...document.querySelectorAll<HTMLElement>(`[data-app-sib="${CSS.escape(drag.enumId + '/' + drag.parent)}"]`)];
        let target = drag.ids.indexOf(drag.id);
        for (let i = 0; i < rows.length; i++) {
            const r = rows[i]?.getBoundingClientRect();
            if (!r) continue;
            if (ev.clientY > r.top + r.height / 2) target = i;
            else break;
        }
        if (ev.clientY < (rows[0]?.getBoundingClientRect().top ?? 0)) target = 0;
        const cur = drag.ids.indexOf(drag.id);
        if (target !== cur && target >= 0 && target < drag.ids.length) {
            const ids = [...drag.ids];
            ids.splice(cur, 1);
            ids.splice(target, 0, drag.id);
            drag = {...drag, ids, moved: true};
        }
    }
    async function nodeUp(enumId: string, parent: string, id: string) {
        const key = `${enumId}/${parent ? parent + '/' : ''}${id}`;
        if (nodeHold?.key === key) {
            clearTimeout(nodeHold.timer);
            nodeHold = null;
            return;
        }
        if (!drag || drag.id !== id) return;
        const d = drag;
        drag = null;
        setTimeout(() => swallowNode.delete(key), 400);
        const path = parent ? `${parent}/${id}` : id;
        if (d.moved) {
            const to = d.ids.indexOf(id);
            try {
                await api.patch(nodeURL(enumId, path), {position: to});
                await load();
            } catch (e) {
                error = (e as Error).message;
            }
            return;
        }
        // a very-long press without a move: the entry's menu - its name
        const node = siblings(enumId, parent).find((n) => n.id === id);
        const name = await askText({title: t('Rename {name}', {name: node?.name ?? id}), message: t('The name of this room or function, as the drawer and the tiles show it.'), input: {label: t('Name'), initial: node?.name ?? '', minLength: 1}, confirm: t('Rename')});
        if (name === null || !name.trim() || name.trim() === node?.name) return;
        try {
            await api.patch(nodeURL(enumId, path), {name: name.trim()});
            await load();
        } catch (e) {
            error = (e as Error).message;
        }
    }
    function nodeClick(enumId: string, parent: string, id: string, ev: MouseEvent) {
        const key = `${enumId}/${parent ? parent + '/' : ''}${id}`;
        if (swallowNode.has(key) || drag) {
            ev.preventDefault();
            swallowNode.delete(key);
            return;
        }
        pick();
    }
    // the siblings as the drawer shows them: the live order while one of them is dragged
    function ordered(enumId: string, parent: string, nodes: MetaNode[]): MetaNode[] {
        if (!drag || drag.enumId !== enumId || drag.parent !== parent) return nodes;
        const by = new Map(nodes.map((n) => [n.id, n]));
        return drag.ids.map((id) => by.get(id)).filter((n): n is MetaNode => !!n);
    }

    // a swipe to the left on the open drawer (a phone) closes it: 60 px of leftward travel, mostly horizontal
    let swipe: {x: number; y: number} | null = null;
    function swipeStart(ev: PointerEvent) {
        swipe = drawerOpen && !drag ? {x: ev.clientX, y: ev.clientY} : null;
    }
    function swipeMove(ev: PointerEvent) {
        if (!swipe || drag) return;
        const dx = ev.clientX - swipe.x;
        const dy = Math.abs(ev.clientY - swipe.y);
        if (dx < -60 && dy < Math.abs(dx)) {
            swipe = null;
            drawerOpen = false;
        }
    }
    function swipeEnd() {
        swipe = null;
    }

    // rooms and functions: the editor sheet from the drawer's foot (an account that may configure)
    let taxonomyOpen = $state(false);

    // the sheet: the card (outside the circle) opens it for a tile that acts and opens, or reads only
    let sheetRef = $state<string | null>(null);
    let sheetFrom = $state.raw<DOMRect | null>(null); // the card's rectangle, for the sheet to grow out of
    const sheetChannel = $derived(sheetRef ? viewOf(sheetRef) : undefined);
    const sheetName = $derived(sheetRef ? nameOf(sheetRef) : '');
    function opens(ref: string): boolean {
        const m = modelOf(ref);
        return !!m && m.widget !== 'none' && m.tile !== 'act';
    }
    function openSheet(ref: string, ev?: Event) {
        if (!opens(ref)) return;
        const card = (ev?.currentTarget as HTMLElement | null) ?? document.querySelector<HTMLElement>(`[data-app-tile="${CSS.escape(ref)}"]`);
        sheetFrom = card?.getBoundingClientRect() ?? null;
        sheetRef = ref;
    }
    // a write shows at once and is taken back if it fails (the maintainer, 2026-09-22: the slider's knob
    // snapped back to the old level for the round trip and then jumped to the new one)
    function apply(ref: string, values: Record<string, unknown>): () => void {
        const c = channels.get(ref);
        if (!c) return () => undefined;
        const m = new Map(channels);
        m.set(ref, {...c, values: {...c.values, ...values}});
        channels = m;
        return () => {
            const now = channels.get(ref);
            if (!now) return;
            const back = new Map(channels);
            const restored = {...now.values};
            for (const k of Object.keys(values)) {
                if (k in c.values) restored[k] = c.values[k];
                else delete restored[k];
            }
            back.set(ref, {...now, values: restored});
            channels = back;
        };
    }
    async function sheetSet(ref: string, key: string, value: unknown) {
        // B-108: an action the write starts (a lock's, a blind's level) shows as such, not as its target; STOP ends it
        const w = channels.get(ref)?.model.widget;
        if (key === 'STOP') endPending(ref, true);
        const action = beginPending(ref, key, value);
        const undo = action || ((w === 'blind' || w === 'lock') && key === 'STOP') ? () => undefined : apply(ref, {[key]: value});
        try {
            await setValue(ref, key, value);
        } catch (e) {
            endPending(ref, true);
            undo();
            throw e;
        }
    }
    async function sheetPut(ref: string, values: Record<string, unknown>) {
        const undo = apply(ref, values);
        try {
            await putValues(ref, values);
        } catch (e) {
            undo();
            throw e;
        }
    }
    // occulited task 73: the account's own display name in Control, else the metadata name
    function nameOf(ref: string): string {
        return shownName(displayNames.names, ref, snap?.objects[ref]?.name, channelSub(ref));
    }
    /** the metadata name behind an overridden one ('' when none) - the "actually: …" tooltip */
    function realOf(ref: string): string {
        return realName(displayNames.names, ref, snap?.objects[ref]?.name, channelSub(ref));
    }
    function channelSub(ref: string): string {
        const i = ref.lastIndexOf(':');
        return i > 0 ? ref.slice(0, i).replace(/^[^.]*\./, '') + ':' + ref.slice(i + 1) : ref;
    }
    // occulited task 102: a switch whose chosen icon is outside the bundle needs the whole set, loaded once;
    // the tiles read knxAll.loaded so they re-render when it is in
    $effect(() => {
        if (knxAll.loaded || knxAll.loading || knxAll.error || !snap) return; // after a failure only the picker's "Noch einmal" tries again
        const outside = (c: string | undefined) => c !== undefined && !KNX[c] && isKnxName(c);
        if (uiTiles.some((x) => { const meta = snap!.objects[x.ref]?.meta; return outside(chosenIcon(meta)) || Object.values(chosenIcons(meta)).some(outside); })) ensureAllIcons().catch(() => undefined);
    });
    /** the tile's icon; it reads knxAll.loaded so a chosen icon outside the bundle appears once the set is in */
    function tileIcon(ref: string, c: Channel | undefined): StateIcon {
        void knxAll.loaded;
        return c ? stateIcon({model: c.model, values: c.values, desc: c.desc, parent: c.parent, pressed: !!flashed[ref], icon: chosenIcon(snap?.objects[ref]?.meta), icons: chosenIcons(snap?.objects[ref]?.meta), name: nameOf(ref)}) : NO_ICON;
    }
    // occulited task 101: on a room's or function's page its own name is left out of the tiles'
    // names (the heading has it), unless the account chose the full names; favorites, the
    // tooltip, the aria labels and the sheet keep the full name
    const pageName = $derived(view.kind === 'node' ? (nodeByPath(view.enumId, view.path)?.name ?? '') : '');
    function tileName(ref: string): string {
        const full = nameOf(ref);
        return pageName && !prefs.appFullNames ? shortenName(full, pageName) : full;
    }
</script>

<div class="app" class:app-drawer-open={drawerOpen}>
    <!-- the drawer: a column on the desktop, off-canvas behind the hamburger on a phone -->
    {#if drawerOpen}<button type="button" class="app-scrim" aria-label={t('Close menu')} onclick={pick}></button>{/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <!-- task 98: the expand triangle, drawn: a filled triangle with rounded corners, pointing right when
         folded and down when open (turned, animated without reduced motion) -->
    {#snippet caret(open: boolean)}
        <svg class="app-caret" class:open viewBox="0 0 12 12" aria-hidden="true" data-app-caret={open ? 'open' : 'closed'}><path d="M4.2 2.6 L9 6 L4.2 9.4 Z" /></svg>
    {/snippet}
    <nav class="app-drawer" class:app-sorting={!!drag} aria-label={t('App menu')} data-app-drawer={drawerOpen ? 'open' : 'closed'} data-app-sorting={drag ? '1' : undefined} onpointerdown={swipeStart} onpointermove={(ev) => { nodeMove(ev); swipeMove(ev); }} onpointerup={swipeEnd} onpointercancel={swipeEnd}>
        <ul class="app-tree">
            <li><a class="app-leaf" class:active={view.kind === 'favorites'} href="/app/favorites" use:link onclick={pick} data-app-entry="favorites">{t('Favorites')}</a></li>
            {#each sections as [id, e] (id)}
                <li>
                    <button type="button" class="app-fold" aria-expanded={isOpen(id)} onclick={() => toggle(id)} data-app-section={id}><span class="app-fold-label">{enumLabel(id, e)}</span>{@render caret(isOpen(id))}</button>
                    {#if isOpen(id)}
                        {#snippet subtree(nodes: MetaNode[], parent: string, depth: number)}
                            <ul class="app-sub" style={`--depth:${depth}`}>
                                {#each ordered(id, parent, nodes) as n (n.id)}
                                    {@const path = parent ? `${parent}/${n.id}` : n.id}
                                    {@const key = `${id}/${path}`}
                                    <li>
                                        <div class="app-row" class:app-lifted={drag?.enumId === id && drag?.parent === parent && drag?.id === n.id} data-app-sib={`${id}/${parent}`}>
                                            <a class="app-leaf" class:active={activePath === key} href={nodeHref(id, path)} use:link onclick={(ev) => nodeClick(id, parent, n.id, ev)} draggable="false" ondragstart={(ev) => ev.preventDefault()} onpointerdown={(ev) => nodeDown(id, parent, n.id, ev)} onpointerup={() => void nodeUp(id, parent, n.id)} onpointercancel={() => void nodeUp(id, parent, n.id)} oncontextmenu={(ev) => { if (canConfigure) ev.preventDefault(); }} data-app-node={key}>{nodeLabel(n)}{#if problemNodes.has(key)}<span class="app-dot" role="img" aria-label={t('Something in here reports a problem')} title={t('Something in here reports a problem')} data-app-problem></span>{/if}</a>
                                            <!-- task 98: the triangle at the right end, after the dot; a fixed width so nothing moves -->
                                            {#if n.children?.length}<button type="button" class="app-fold app-fold-node" aria-expanded={isOpen(key)} aria-label={isOpen(key) ? t('Collapse') : t('Expand')} onclick={() => toggle(key)}>{@render caret(isOpen(key))}</button>{/if}
                                        </div>
                                        {#if n.children?.length && isOpen(key)}{@render subtree(n.children, path, depth + 1)}{/if}
                                    </li>
                                {/each}
                            </ul>
                        {/snippet}
                        {@render subtree(e.tree, '', 1)}
                    {/if}
                </li>
            {/each}
        </ul>
        <!-- the drawer's foot (the maintainer, 2026-09-22): icon buttons like the top bar's - the
             unassigned channels for an account that may configure, and, while the top bar is hidden,
             the way back to Status with the account and the settings; no Settings section in the tree.
             (The icons are placeholders until the icon concept.) -->
        {#if canConfigure || prefs.appFullscreen}
            <div class="app-drawer-foot" data-app-foot>
                {#if prefs.appFullscreen}<a class="ol-iconlink app-foot-icon" href="/" use:link onclick={pick} title={t('Leave to Status')} aria-label={t('Leave to Status')} data-app-leave><Icon name="leave" size={20} /></a>{/if}
                <span class="app-foot-space"></span>
                {#if canConfigure}
                    <a class="ol-iconlink app-foot-icon" class:active={view.kind === 'unassigned'} href="/app/unassigned" use:link onclick={pick} title={t('Unassigned channels')} aria-label={t('Unassigned channels')} data-app-entry="unassigned"><Icon name="tag" size={20} /></a>
                    <button type="button" class="ol-iconlink app-foot-icon" title={t('Rooms and functions')} aria-label={t('Rooms and functions')} onclick={() => { pick(); taxonomyOpen = true; }} data-app-entry="taxonomy"><Icon name="edit" size={20} /></button>
                {/if}
                {#if prefs.appFullscreen && auth.public}
                    <a class="ol-iconlink app-foot-icon" href="/login" use:link onclick={pick} title={t('Sign in')} aria-label={t('Sign in')}><Icon name="user" size={20} /></a>
                {:else if prefs.appFullscreen}
                    <a class="ol-iconlink app-foot-icon" href="/account" use:link onclick={pick} title={`${t('Account')}: ${auth.user}`} aria-label={t('Account')}><Icon name="user" size={20} /></a>
                    <a class="ol-iconlink app-foot-icon" href="/settings" use:link onclick={pick} title={t('Settings')} aria-label={t('Settings')}><Icon name="settings" size={20} /></a>
                {/if}
            </div>
        {/if}
    </nav>

    <section class="app-content">
        {#if error}<div class="ol-notice error">{error}</div>{/if}
        {#if !snap && !error}
            <Loading />
        {:else if snap}
            <h1 class="app-title" data-app-title>{title}{#if view.kind === 'node' && problemNodes.has(`${view.enumId}/${view.path}`)}<span class="app-dot" role="img" aria-label={t('Something in here reports a problem')} title={t('Something in here reports a problem')}></span>{/if}{#if notLive}<span class="app-notlive" data-app-notlive={notLive} title={notLiveWhy}>{t('not live')}: {notLiveReason}</span>{/if}</h1>
            {#if view.kind === 'messages'}
                <p class="ol-muted">{t('The service messages are listed on the Status page, where a message whose cause is gone is confirmed.')} <a href="/#service-messages" use:link>{t('Status')}</a></p>
            {:else if uiTiles.length === 0}
                <p class="ol-muted" data-app-empty>{view.kind === 'favorites' ? t('No favorites yet. A channel is added to your favorites from its card.') : t('No channels assigned here.')}</p>
            {:else}
                {#if rpcError}<div class="ol-notice error">{rpcError}</div>{/if}
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div class="app-grid" data-app-tiles={uiTiles.length} data-app-sorting={tileDrag ? '1' : undefined} onpointermove={tileMove}>
                    {#each shownTiles as x (x.ref)}
                        {@const c = viewOf(x.ref)}
                        {@const m = c?.model}
                        {@const on = c && m ? isOn(m, c.values, c.desc) : false}
                        {@const bst = c && m && m.widget === 'thermostat' ? boostOf(x.ref, c) : null}
                        {@const si = tileIcon(x.ref, c)}
                        {@const state = c && m ? tileState(x.ref, c, m) : ''}
                        {@const kind = tileKind(x.ref)}
                        {@const cm = corner(x.ref)}
                        {@const tone = si.tone === 'alarm' ? 'alarm' : bst ? 'boost' : si.tone === 'on' || on ? 'on' : ''}
                        {@const th = c && m && m.widget === 'thermostat' && !acting[x.ref] ? thermoParts(m, c.values, c.desc) : null}
                        {@const md = c && m && m.widget === 'thermostat' ? modeOf(x.ref, c.values) : undefined}
                        <!-- task 113: a blind's or a lock's running action - an arrow (a lock: its open or closed shackle) and a spinner ring -->
                        {@const mv = c && m ? motion(m, c.values, c.desc) : undefined}
                        <!-- B-100: a tile with no state line (a key) has its name centred; a press or an error shows below it -->
                        {@const noState = !!c && (!m || m.widget === 'button' || m.widget === 'none')}
                        <!-- task 95 (V1): how long a contact or a motion detector is in its state; the line is reserved on every tile -->
                        {@const dur = c && m ? durationLine(m, c.values, c.changed?.[m.stateKey], minuteNow, i18n.language) : ''}
                        <!-- svelte-ignore a11y_no_static_element_interactions -->
                        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
                        <div class="ol-card app-tile" class:app-tile-read={kind === 'read'} class:app-tile-act={kind !== 'read'} class:app-tile-open={opens(x.ref)} class:app-tile-on={tone === 'on'} class:app-tile-alarm={tone === 'alarm'} class:app-tile-boost={tone === 'boost'} class:app-tile-svc={cm.length > 0} data-app-tile={x.ref} data-app-widget={m?.widget ?? ''} data-app-on={on ? '1' : '0'} data-app-tone={tone} role={opens(x.ref) ? 'button' : undefined} tabindex={opens(x.ref) ? 0 : undefined} aria-label={opens(x.ref) ? t('Open {name}', {name: nameOf(x.ref)}) : undefined} class:app-lifted={tileDrag?.ref === x.ref} onclick={(ev) => cardClick(x.ref, ev)} onpointerdown={(ev) => cardDown(x.ref, ev)} onpointerup={() => void cardUp(x.ref)} onpointercancel={() => void cardUp(x.ref)} oncontextmenu={(ev) => { if (canConfigure) ev.preventDefault(); }} onkeydown={(ev) => { if (opens(x.ref) && (ev.key === 'Enter' || ev.key === ' ') && ev.target === ev.currentTarget) { ev.preventDefault(); openSheet(x.ref, ev); } }}>
                            <!-- task 112: the disc and, on a thermostat, its mode badge (md) at the bottom right (display only) -->
                            <span class="app-disc">
                                {#if m && m.widget === 'thermostat' && c && canBoost(c.desc)}
                                    <!-- task 103: the thermostat's disc is its boost button; while boost runs, V4's ring drains around it -->
                                    <button type="button" class="app-circle app-circle-act" class:boost={!!bst} class:on={!bst && (si.tone === 'on' || on)} data-icon={si.name} title={bst ? t('End boost') : t('Start boost')} aria-label={`${nameOf(x.ref)}: ${bst ? t('End boost') : t('Start boost')}`} aria-pressed={!!bst} disabled={!!boostBusy[x.ref]} onpointerdown={(ev) => ev.stopPropagation()} onclick={(ev) => { ev.stopPropagation(); void toggleBoost(x.ref); }} data-app-act data-app-boost-btn={bst ? 'on' : 'off'}>
                                        <span class="app-ico">{@html si.svg}</span>
                                        <!-- B-103: no time known (a group whose members say none): the ring is indeterminate, a quarter arc going round -->
                                        {#if bst}<svg class="app-ring" class:indet={bst.full === 0} viewBox="0 0 64 64" aria-hidden="true" data-app-ring={bst.full > 0 ? Math.round((100 * bst.remaining) / bst.full) : 'indeterminate'}><circle class="bg" cx="32" cy="32" r={RING_R} />{#if bst.full > 0}<circle class="fg" cx="32" cy="32" r={RING_R} stroke-dasharray={RING_C.toFixed(1)} stroke-dashoffset={ringOffset(bst.remaining, bst.full).toFixed(1)} />{:else}<circle class="fg" cx="32" cy="32" r={RING_R} stroke-dasharray="{(RING_C / 4).toFixed(1)} {((3 * RING_C) / 4).toFixed(1)}" />{/if}</svg>{/if}
                                    </button>
                                {:else if m && m.commandKey && kind !== 'read' && m.widget !== 'thermostat'}
                                    <button type="button" class="app-circle app-circle-act" class:on={si.tone === 'on' || on || !!flashed[x.ref]} class:alarm={si.tone === 'alarm'} data-icon={si.name} title={m.widget === 'blind' && c ? blindVerb(c) : undefined} aria-label={m.widget === 'button' ? t('Press {name}', {name: nameOf(x.ref)}) : m.widget === 'blind' && c ? `${nameOf(x.ref)}: ${blindVerb(c)}` : t('Switch {name}', {name: nameOf(x.ref)})} aria-pressed={m.widget === 'button' || m.widget === 'blind' ? undefined : on} aria-busy={discBusy[x.ref] ? 'true' : undefined} data-app-disc-action={m.widget === 'blind' && c ? blindAction(c.values, c.desc) : undefined} onpointerdown={(ev) => { ev.stopPropagation(); pressStart(x.ref); }} onpointerup={() => pressEnd(x.ref)} onpointercancel={() => pressEnd(x.ref)} onpointerleave={() => pressEnd(x.ref)} onclick={(ev) => { ev.stopPropagation(); pressClick(x.ref); }} oncontextmenu={(ev) => { if (m.widget === 'button') ev.preventDefault(); }} data-app-act data-app-pressed={flashed[x.ref] ? '1' : undefined}>
                                        <span class="app-ico">{@html si.svg}</span>
                                    </button>
                                {:else}
                                    <span class="app-circle" class:on={si.tone === 'on'} class:alarm={si.tone === 'alarm'} aria-hidden="true" data-icon={si.name}><span class="app-ico">{@html si.svg}</span></span>
                                {/if}
                                {#if mv}
                                    <svg class="app-spin" viewBox="0 0 64 64" aria-hidden="true" data-app-spin><circle cx="32" cy="32" r={RING_R} stroke-dasharray="{(RING_C / 4).toFixed(1)} {((3 * RING_C) / 4).toFixed(1)}" /></svg>
                                    <span class="app-mode app-motion" role="img" aria-label={state} title={state} data-app-motion={mv.dir ?? 'moving'}>
                                        {#if m?.widget === 'lock' && mv.dir === 'up'}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round"><rect x="2.5" y="5.5" width="7" height="5" rx="1" /><path d="M4.2 5.5V3.8a1.8 1.8 0 0 1 3.5-.6" /></svg>
                                        {:else if m?.widget === 'lock' && mv.dir === 'down'}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"><rect x="2.5" y="5.5" width="7" height="5" rx="1" /><path d="M4.2 5.5V3.8a1.8 1.8 0 0 1 3.6 0v1.7" /></svg>
                                        {:else if mv.dir === 'up'}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 10V2.5M2.8 5.6 6 2.4l3.2 3.2" /></svg>
                                        {:else if mv.dir === 'down'}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 2v7.5M2.8 6.4 6 9.6l3.2-3.2" /></svg>
                                        {:else}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 1.8v3.4M6 6.8v3.4M3.6 3.6 6 1.4l2.4 2.2M3.6 8.4 6 10.6l2.4-2.2" /></svg>{/if}
                                    </span>
                                {/if}
                                {#if md}
                                    <span class="app-mode app-mode-{md}" role="img" title={modeWord(md)} aria-label={t('Mode: {mode}', {mode: modeWord(md)})} data-app-mode={md}>
                                        {#if md === 'auto'}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><circle cx="6" cy="6" r="4.6" /><path d="M6 3.4V6l1.8 1.2" /></svg>
                                        {:else if md === 'manual'}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6.5V3.2a.8.8 0 0 1 1.6 0V6M5.6 5.6V2.4a.8.8 0 0 1 1.6 0v3.2M7.2 5.6V3.2a.8.8 0 0 1 1.6 0v3.6c0 2-1.2 3.5-3 3.5-1.4 0-2.2-.7-2.8-1.8L2 6.8a.8.8 0 0 1 1.4-.8L4 7" /></svg>
                                        {:else}<svg viewBox="0 0 12 12" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"><rect x="2" y="4" width="8" height="6" rx="1" /><path d="M4.5 4V2.8h3V4" /></svg>{/if}
                                    </span>
                                {/if}
                            </span>
                            <div class="app-tile-text" class:app-tile-nostate={noState} class:app-tile-th={!!th && !flashed[x.ref]}>
                                <div class="app-tile-name" title={realOf(x.ref) ? t('actually: {name}', {name: realOf(x.ref)}) : nameOf(x.ref)} data-app-real={realOf(x.ref) || undefined} data-app-short={tileName(x.ref) !== nameOf(x.ref) ? '1' : undefined} use:fit={{min: 10, text: tileName(x.ref), tip: false}}>{tileName(x.ref)}</div>
                                {#if th && !flashed[x.ref]}
                                    <!-- task 92 (T2E): Ist and Soll side by side, the valve a thin bar with its percent below -->
                                    <div class="app-thermo" data-app-thermo title={state} aria-label={state} use:fit={{min: 13, text: `${deg(th.actual)} ${deg(th.set)}`, tip: false}}>
                                        <span class="app-th-lab">{t('thermostat actual')}</span><span class="app-th-lab">{t('thermostat set')}</span>
                                        <span class="app-th-val" data-app-actual>{deg(th.actual)}</span><span class="app-th-val app-th-set" data-app-set>{deg(th.set)}</span>
                                        <!-- task 103 (V0): the humidity in a small row of its own, when the channel has one -->
                                        {#if th.humidity !== undefined}<div class="app-th-hum" data-app-hum={th.humidity} use:fit={{min: 9, text: t('{h} % humidity', {h: String(th.humidity)}), tip: false}}><svg viewBox="0 0 10 12" aria-hidden="true"><path fill="currentColor" d="M5 .5C3.4 3 1 5.3 1 7.6A4 4 0 0 0 9 7.6C9 5.3 6.6 3 5 .5Z" /></svg><span>{t('{h} % humidity', {h: String(th.humidity)})}</span></div>{/if}
                                        <!-- B-100: a thermostat that reports neither a valve nor a window (a wall thermostat) has no row, so the pair is centred;
                                             task 103: while boost runs the countdown takes the row's place (the valve is open anyway), and a thermostat that
                                             can boost keeps the row - empty - so nothing moves when a boost starts (the maintainer: "reserve the row") -->
                                        {#if bst}
                                            <div class="app-th-row app-th-boost" data-app-th-row data-app-boost={bst.remaining} use:fit={{min: 9, text: boostText(bst), tip: false}}>{boostText(bst)}</div>
                                        {:else if thermoRow(c!.values, c!.desc)}
                                            <div class="app-th-row" data-app-th-row>
                                                {#if th.window}<span class="app-th-note" data-app-window>{t('Window open')}</span>
                                                {:else if th.valve !== undefined}<span class="app-vbar" title={t('valve {v}', {v: `${th.valve} %`})}><i style:width="{th.valve}%"></i></span><span class="app-vpct" data-app-valve>{th.valve} %</span>{/if}
                                            </div>
                                        {:else if canBoost(c!.desc)}
                                            <div class="app-th-row" data-app-th-row data-app-th-reserved aria-hidden="true"></div>
                                        {/if}
                                    </div>
                                {:else}
                                    {@const shownState = acting[x.ref] ? acting[x.ref] : flashed[x.ref] ? flashed[x.ref] : state}
                                    <div class="app-tile-state" class:on={tone === 'on'} class:alarm={tone === 'alarm'} data-app-state use:fit={{min: 13, text: shownState}}>{shownState}</div>
                                    {#if !noState}<div class="app-tile-dur" data-app-dur={dur ? '1' : undefined} use:fit={{min: 10, text: dur}}>{dur}</div>{/if}
                                {/if}
                            </div>
                            {#if cm[0]}
                                <!-- task 92 (S6): the corner, always the same size, with the most severe message's symbol only -->
                                <button type="button" class="app-svc" title={cm.map((b) => t(b.label)).join(', ')} aria-label={t('{n} service messages', {n: String(cm.length)}) + ': ' + cm.map((b) => t(b.label)).join(', ')} onclick={(ev) => { ev.stopPropagation(); cornerClick(x.ref); }} onpointerdown={(ev) => ev.stopPropagation()} data-app-svc={cm[0].key} data-app-svc-kind={cm[0].kind} data-app-svc-count={cm.length}>
                                    <span class="app-svc-ico">{@html lucideSVG(cm[0].kind, 2.5)}</span>
                                </button>
                            {/if}
                        </div>
                    {/each}
                </div>
            {/if}
            <!-- occulited B-84: the names of interfaces this system does not list, removed only here -->
            {#if view.kind === 'unassigned' && canConfigure}<ForeignNames onchanged={() => void load()} />{/if}
        {/if}
    </section>

    {#if taxonomyOpen && snap}
        <TaxonomySheet {snap} onclose={() => (taxonomyOpen = false)} onchanged={load} />
    {/if}
    {#if assignRef && snap}
        {@const assignCh = channels.get(assignRef)}
        {@const assignWidget = assignCh?.model.widget}
        <!-- task 105: a contact (or a door opener) gets one picker per state: closed, open, and tilted where the sensor has it -->
        {@const contactOf = assignCh && (assignWidget === 'contact' || assignWidget === 'door') ? {kind: 'contact' as const, states: hasTilted(assignCh.desc, assignCh.model.stateKey) ? CONTACT_STATES : CONTACT_STATES.filter((st) => st !== 'tilted'), window: assignWidget === 'contact' && isWindow(assignCh.desc, assignCh.model.stateKey), opener: assignWidget === 'door'} : undefined}
        <AssignSheet ref={assignRef} {snap} {favoritesPath} from={assignFrom} iconOf={assignWidget === 'switch' || assignWidget === 'button' ? {kind: assignWidget === 'switch' ? 'switch' : 'key', name: nameOf(assignRef), parent: assignCh?.parent ?? ''} : contactOf} layer={sheetRef ? 95 : 90} onclose={() => (assignRef = null)} onchanged={() => void load()} />
    {/if}
    {#if sheetRef && sheetChannel}
        <Sheet channel={sheetChannel} meter={meterOf(sheetRef) ?? null} name={sheetName} icon={chosenIcon(snap?.objects[sheetRef]?.meta)} icons={chosenIcons(snap?.objects[sheetRef]?.meta)} messages={corner(sheetRef)} from={sheetFrom} boost={sheetChannel.model.widget === 'thermostat' ? boostOf(sheetRef, sheetChannel) : null} onboost={sheetChannel.model.widget === 'thermostat' && canBoost(sheetChannel.desc) ? () => toggleBoost(sheetRef!) : undefined} onclose={() => (sheetRef = null)} onset={(k, val) => sheetSet(sheetRef!, k, val)} onput={(vals) => sheetPut(sheetRef!, vals)} onassign={canConfigure ? () => { assignFrom = null; assignRef = sheetRef; } : undefined} />
    {/if}

    <!-- the floating hamburger, on a phone only (CSS): bottom left, the drawer's side; gone while the
         drawer is open (the maintainer, 2026-09-22) - the scrim or a swipe to the left closes it -->
    {#if !drawerOpen}
        <button type="button" class="app-fab" class:app-fab-hidden={fabHidden} aria-label={t('Open menu')} aria-expanded={false} onclick={() => (drawerOpen = true)} data-app-fab>
            <Icon name="more" size={20} />
        </button>
    {/if}
</div>

<style>
    /* the maintainer, 2026-09-22: on the desktop the drawer looks as on a phone - a card-coloured
       column fixed to the window's left edge, full height below the top bar (to the very top when
       the App is the whole window) - and the content moves right only as far as the column reaches
       into the centred page column */
    .app { display: block; }
    .app-drawer { position: fixed; left: 0; top: var(--ol-header-h, 0px); bottom: 0; width: 260px; overflow: auto; background: var(--hmm-card-bg, var(--hmm-bg)); border-right: 1px solid var(--hmm-border-muted); padding: 14px 10px; z-index: 5; display: flex; flex-direction: column; }
    .app-tree { flex: 0 0 auto; }
    .app-content { margin-left: max(0px, calc(256px - max(0px, (100vw - 1200px) / 2))); }
    .app-tree, .app-sub { list-style: none; margin: 0; padding: 0; }
    .app-sub { padding-left: calc(var(--depth, 1) * 12px); }
    .app-row { display: flex; align-items: center; }
    /* the maintainer, 2026-09-22: larger type and click areas - the column's width has the room */
    .app-tree { font-size: 1.12em; }
    /* task 98: a section's label starts where Favoriten's does (the leaf's 10 px); the triangle is at the row's right end */
    .app-fold { background: none; border: 0; color: var(--hmm-fg); font: inherit; cursor: pointer; padding: 0 10px; min-height: 44px; text-align: left; width: 100%; display: flex; align-items: center; gap: 6px; border-radius: 8px; }
    .app-fold-label { flex: 1 1 auto; min-width: 0; }
    .app-fold:hover { background: var(--hmm-bg-hover, rgba(127, 127, 127, 0.12)); }
    .app-fold-node { width: 44px; min-width: 44px; justify-content: flex-end; padding: 0 10px 0 0; } /* its triangle under the sections' */
    .app-caret { display: block; width: 14px; height: 14px; flex: 0 0 14px; fill: currentColor; stroke: currentColor; stroke-width: 1.6; stroke-linejoin: round; color: var(--hmm-fg-muted); transition: transform 150ms ease-out; }
    .app-caret.open { transform: rotate(90deg); }
    .app-leaf { display: flex; align-items: center; gap: 8px; padding: 0 10px; min-height: 44px; border-radius: 8px; color: var(--hmm-fg); text-decoration: none; flex: 1 1 auto; min-width: 0; }
    .app-leaf:hover { background: var(--hmm-bg-hover, rgba(127, 127, 127, 0.12)); }
    .app-leaf.active { background: var(--hmm-accent-bg, rgba(127, 127, 127, 0.18)); font-weight: 600; }
    .app-sorting { touch-action: none; }
    .app-sorting .app-leaf { cursor: grabbing; }
    .app-lifted { background: var(--hmm-card-bg); box-shadow: 0 4px 14px rgba(0, 0, 0, 0.25); border-radius: 8px; position: relative; z-index: 1; }
    .app-lifted .app-leaf { font-weight: 600; }
    .app-drawer-foot { margin-top: auto; padding: 10px 4px 0; border-top: 1px solid var(--hmm-border-muted); display: flex; align-items: center; gap: 6px; }
    .app-foot-space { flex: 1 1 auto; }
    .app-foot-icon { display: inline-flex; align-items: center; justify-content: center; width: 44px; height: 44px; border-radius: 8px; color: var(--hmm-fg); background: none; border: 0; cursor: pointer; padding: 0; }
    .app-foot-icon:hover, .app-foot-icon.active { background: var(--hmm-bg-hover, rgba(127, 127, 127, 0.12)); }
    .app-title { margin: 0 0 12px; font-size: 1.3em; }
    .app-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
    /* task 92: one fixed height for every tile (today's), the icon left in a soft disc, the name small
       on top and the state large below; a tile that is on or alarming is tinted (D), and a service
       message colours the top right corner (S6). Nothing in a tile changes its box. */
    /* task 103 (the maintainer: "118 / 4"): 4 px top and bottom inside, so three rows under the name fit; left and right as before */
    .app-tile { --app-tile-bg: var(--hmm-card-bg); --app-svc: color-mix(in srgb, var(--hmm-warn) 50%, var(--hmm-error)); position: relative; overflow: hidden; height: 118px; box-sizing: border-box; padding: 4px 14px; display: flex; flex-direction: row; align-items: center; gap: 14px; }
    .app-tile-read { --app-tile-bg: var(--hmm-bg, transparent); background: var(--app-tile-bg); box-shadow: none; }
    .app-tile-on { background: color-mix(in srgb, var(--hmm-warn) 7%, var(--app-tile-bg)); border-color: color-mix(in srgb, var(--hmm-warn) 35%, var(--hmm-border-muted)); }
    .app-tile-alarm { background: color-mix(in srgb, var(--hmm-error) 8%, var(--app-tile-bg)); border-color: color-mix(in srgb, var(--hmm-error) 45%, var(--hmm-border-muted)); }
    /* task 103: boost in its own orange-red, not the "on" colour */
    .app-tile-boost { background: color-mix(in srgb, var(--ol-boost) 8%, var(--app-tile-bg)); border-color: color-mix(in srgb, var(--ol-boost) 45%, var(--hmm-border-muted)); }
    .app-tile-text { min-width: 0; flex: 1 1 auto; display: grid; align-content: center; }
    .app-tile-svc .app-tile-text { padding-right: 16px; }
    /* B-100: no ellipsis - a line too long for the tile shrinks its font (use:fit, lib/app/fit.ts) and
       keeps its height, so nothing moves; only below the minimum is it cut, the text in its tooltip */
    .app-tile-name { font-size: 12.5px; font-weight: 500; color: var(--hmm-fg-muted); white-space: nowrap; overflow: hidden; height: 17px; line-height: 17px; }
    .app-tile-state { font-size: 19px; font-weight: 600; height: 25px; line-height: 25px; color: var(--hmm-fg); white-space: nowrap; overflow: hidden; font-variant-numeric: tabular-nums; }
    /* B-100: a key has no state line: its name is centred, a press or an error shows below it without moving it */
    .app-tile-nostate { align-self: stretch; grid-template-rows: 1fr auto 1fr; align-content: stretch; }
    .app-tile-nostate .app-tile-name { grid-row: 2; }
    .app-tile-nostate .app-tile-state { grid-row: 3; align-self: start; }
    .app-tile-state.on { color: var(--hmm-warn); }
    /* task 95: the duration, small and muted under the state; reserved (empty) on every tile with a state line */
    .app-tile-dur { font-size: 12px; color: var(--hmm-fg-muted); white-space: nowrap; overflow: hidden; height: 17px; line-height: 17px; font-variant-numeric: tabular-nums; }
    .app-tile-state.alarm { color: var(--hmm-error); }
    /* T2E: Ist and Soll labelled side by side, the Soll in the "on" colour; the valve a thin bar
       with a small fixed-width percent, so the bar does not move between 0 and 100 % */
    .app-thermo { display: grid; grid-template-columns: auto auto; column-gap: 14px; align-items: end; justify-content: start; font-variant-numeric: tabular-nums; min-width: 0; overflow: hidden; font-size: 21px; }
    .app-th-lab { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--hmm-fg-muted); }
    .app-th-val { font-size: 1em; font-weight: 600; height: 24px; line-height: 24px; white-space: nowrap; }
    .app-th-set { color: var(--hmm-warn); }
    .app-th-row { grid-column: 1 / -1; display: flex; align-items: center; gap: 6px; margin-top: 5px; max-width: 150px; height: 14px; min-width: 0; }
    .app-vbar { height: 4px; border-radius: 2px; background: var(--hmm-border-muted); overflow: hidden; flex: 1 1 auto; }
    .app-vbar i { display: block; height: 100%; background: var(--hmm-accent); }
    .app-vpct { font-size: 11px; color: var(--hmm-fg-muted); width: 5.2ch; white-space: nowrap; text-align: right; flex: 0 0 auto; }
    .app-th-note { font-size: 11px; color: var(--hmm-fg-muted); white-space: nowrap; overflow: hidden; }
    /* task 103 (V0): the humidity row, small and blue; the boost countdown in the valve row's place */
    .app-th-hum { grid-column: 1 / -1; display: flex; align-items: center; gap: 3px; height: 16px; margin-top: 2px; font-size: 11.5px; color: var(--ol-hum); white-space: nowrap; overflow: hidden; font-variant-numeric: tabular-nums; min-width: 0; }
    .app-th-hum svg { width: 9px; height: 11px; flex: 0 0 auto; display: block; }
    .app-th-boost { color: var(--ol-boost); font-weight: 600; font-size: 11.5px; white-space: nowrap; overflow: hidden; font-variant-numeric: tabular-nums; display: block; line-height: 14px; }
    /* S6: a fixed 34 px triangle along the tile's rounded corner, the most severe message's symbol in it */
    .app-svc { position: absolute; top: 0; right: 0; width: 34px; height: 34px; padding: 0; border: 0; cursor: pointer; background: linear-gradient(225deg, var(--app-svc) 50%, transparent 50%); color: var(--hmm-bg); }
    .app-svc:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: -2px; }
    .app-svc-ico { position: absolute; top: 4px; right: 4px; width: 12px; height: 12px; display: block; pointer-events: none; }
    .app-svc-ico :global(svg) { width: 100%; height: 100%; display: block; }
    /* task 19: quiet - the title's size is not its own, nor a colour that alarms */
    .app-notlive { margin-left: 12px; font-size: 0.6em; font-weight: normal; color: var(--hmm-fg-muted); white-space: nowrap; vertical-align: middle; cursor: help; }
    .app-notlive::before { content: ''; display: inline-block; width: 7px; height: 7px; border-radius: 50%; border: 1.5px solid currentColor; margin-right: 5px; vertical-align: 1px; }
    .app-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--hmm-warn); margin-left: 8px; flex: 0 0 auto; }
    /* task 92: the state icon (task 75) in a soft filled disc, about 84 % of it; 46 px on phones, 56 px
       from 900 px; a state change swaps the icon and the tint only, the disc keeps its box */
    .app-circle { width: 46px; height: 46px; box-sizing: border-box; flex: 0 0 auto; border-radius: 50%; border: 0; display: inline-flex; align-items: center; justify-content: center; color: var(--hmm-fg-muted); background: color-mix(in srgb, var(--hmm-border-muted) 70%, transparent); padding: 0; overflow: hidden; }
    .app-circle.on { color: var(--hmm-warn); background: color-mix(in srgb, var(--hmm-warn) 16%, transparent); }
    .app-circle.alarm { color: var(--hmm-error); background: color-mix(in srgb, var(--hmm-error) 18%, transparent); }
    /* task 103: the boosting disc, with V4's ring drained around it - absolutely placed, so the layout does not move */
    .app-circle { position: relative; }
    /* task 112 (the maintainer's M3): the disc's box, which the mode badge hangs on at the bottom right, overlapping the
       disc's edge; it takes the disc's own size, so a tile with or without a badge lays out the same */
    .app-disc { position: relative; flex: 0 0 auto; display: inline-flex; }
    .app-mode { position: absolute; right: -3px; bottom: -3px; width: 18px; height: 18px; box-sizing: border-box; border-radius: 50%; background: var(--hmm-card-bg); border: 1.5px solid var(--hmm-border-muted); display: inline-flex; align-items: center; justify-content: center; color: var(--hmm-fg-muted); pointer-events: none; }
    .app-mode svg { width: 12px; height: 12px; display: block; }
    .app-mode-manual { color: var(--ol-manual); border-color: color-mix(in srgb, var(--ol-manual) 60%, var(--hmm-border-muted)); }
    .app-mode-holiday { color: var(--hmm-accent); border-color: color-mix(in srgb, var(--hmm-accent) 60%, var(--hmm-border-muted)); }
    /* task 113: a blind's or a lock's running action - an indeterminate quarter arc around the disc in the "on" colour
       (absolutely placed: nothing moves), and the arrow in the badge's place; with reduced motion a still, full ring */
    .app-spin { position: absolute; inset: -3px; width: calc(100% + 6px); height: calc(100% + 6px); pointer-events: none; animation: app-spin 1.2s linear infinite; }
    .app-spin circle { fill: none; stroke: var(--hmm-warn); stroke-width: 3; stroke-linecap: round; vector-effect: non-scaling-stroke; }
    @keyframes app-spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
    @media (prefers-reduced-motion: reduce) { .app-spin { animation: none; } .app-spin circle { stroke-dasharray: none; opacity: 0.7; } }
    .app-motion { color: var(--hmm-warn); border-color: color-mix(in srgb, var(--hmm-warn) 60%, var(--hmm-border-muted)); }
    .app-circle.boost { color: var(--ol-boost); background: color-mix(in srgb, var(--ol-boost) 18%, transparent); overflow: visible; }
    .app-ring { position: absolute; inset: -3px; width: calc(100% + 6px); height: calc(100% + 6px); transform: rotate(-90deg); pointer-events: none; }
    .app-ring circle { fill: none; stroke-width: 3; vector-effect: non-scaling-stroke; }
    .app-ring .bg { stroke: color-mix(in srgb, var(--ol-boost) 22%, transparent); }
    .app-ring .fg { stroke: var(--ol-boost); stroke-linecap: round; transition: stroke-dashoffset 1s linear; }
    .app-ring.indet { animation: app-ring-spin 1.6s linear infinite; }
    @keyframes app-ring-spin { from { transform: rotate(-90deg); } to { transform: rotate(270deg); } }
    @media (prefers-reduced-motion: reduce) { .app-ring .fg { transition: none; } .app-ring.indet { animation: none; } }
    .app-ico { width: 84%; height: 84%; display: block; pointer-events: none; }
    .app-ico :global(svg) { width: 100%; height: 100%; display: block; }
    .app-circle-act { cursor: pointer; font: inherit; touch-action: manipulation; -webkit-user-select: none; user-select: none; }
    .app-circle-act:hover { box-shadow: inset 0 0 0 2px color-mix(in srgb, currentColor 45%, transparent); }
    .app-circle-act:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .app-tile-act { cursor: default; }
    .app-tile-open { cursor: pointer; }
    .app-tile { -webkit-user-select: none; user-select: none; -webkit-touch-callout: none; }
    .app-grid[data-app-sorting] { touch-action: none; }
    .app-tile.app-lifted { box-shadow: 0 6px 18px rgba(0, 0, 0, 0.3); transform: scale(1.03); z-index: 1; }
    .app-tile-open:hover { box-shadow: 0 2px 10px rgba(0, 0, 0, 0.18); }
    .app-tile-open:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .app-fab { display: none; }
    .app-scrim { display: none; }
    @media (min-width: 900px) {
        .app-grid { grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); }
        .app-circle { width: 56px; height: 56px; }
        .app-mode { width: 20px; height: 20px; }
    }
    @media (max-width: 899px) {
        .app-tile { gap: 10px; padding: 4px 12px; }
        .app-thermo { font-size: 18px; }
        .app-thermo { column-gap: 10px; }
        .app { padding-bottom: calc(72px + env(safe-area-inset-bottom)); }
        .app-content { margin-left: 0; }
        .app-drawer { top: 0; width: min(300px, 85vw); transform: translateX(-105%); transition: transform 200ms ease-out; z-index: 60; }
        .app-drawer-open .app-drawer { transform: none; }
        .app-scrim { display: block; position: fixed; inset: 0; background: rgba(0, 0, 0, 0.35); border: 0; z-index: 55; }
        .app-fab { display: inline-flex; position: fixed; left: calc(16px + env(safe-area-inset-left)); bottom: calc(16px + env(safe-area-inset-bottom)); width: 52px; height: 52px; border-radius: 50%; align-items: center; justify-content: center; background: var(--hmm-card-bg); color: var(--hmm-fg); border: 1px solid var(--hmm-border-muted); box-shadow: 0 4px 14px rgba(0, 0, 0, 0.3); cursor: pointer; z-index: 65; transition: transform 200ms ease-out, opacity 200ms; }
        .app-fab-hidden { transform: translateY(90px); opacity: 0; }
        .app-fab:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    }
    @media (prefers-reduced-motion: reduce) {
        .app-drawer, .app-fab, .app-caret { transition: none; }
    }
</style>
