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
    import {t} from '../lib/i18n.svelte';
    import Icon from '../lib/Icon.svelte';
    import Loading from '../lib/Loading.svelte';
    import {isOn, LEVEL_KEYS, longAction, mainAction, stateText, working, type Model} from '../lib/app/channels';
    import {loadChannels, openEvents, setValue, type Channel, putValues} from '../lib/app/rpc';
    import type {StreamState} from '../lib/app/eventstream';
    import {subscribeShell} from '../lib/shellstream/client';
    import Sheet from '../lib/app/Sheet.svelte';
    import AssignSheet from '../lib/app/AssignSheet.svelte';
    import TaxonomySheet from '../lib/app/TaxonomySheet.svelte';
    import {active, badgesFor, deviceOf, isMaintenanceKey, isProblem, type Badge} from '../lib/app/badges';
    import {ask} from '../lib/dialog.svelte';
    import type {ServiceMessage} from '../lib/api';
    import {askText} from '../lib/dialog.svelte';

    const FAVORITE_ENUM = 'favorite';
    let snap = $state<MetaSnapshot | null>(null);
    let error = $state('');
    let messages = $state(0);
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
            messages = 0;
        }
    }
    // ---- the badges: the active maintenance messages per device, from occulited's store (the whole
    // house, pushed as they change) and the :0 channel's events of an open page
    let maint = $state<Map<string, Set<string>>>(new Map());
    function takeMessages(m: {count: number; messages: ServiceMessage[]}) {
        messages = m.count;
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
    function badges(ref: string): Badge[] {
        return badgesFor(maint.get(deviceOf(ref)) ?? []);
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
    function showBadges(ref: string, name: string) {
        const list = badges(ref).map((b) => `• ${t(b.label)}`).join('\n');
        void ask({title: name, message: list, confirm: t('OK'), cancel: ''});
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
        const poll = setInterval(() => life.active && load(), 30000);
        // back on the page: the tree and the messages, and the values of its tiles again
        const stopReturn = life.onReturn(() => {
            void load();
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
        if (rest === 'messages') return {kind: 'messages'};
        if (rest === 'unassigned') return {kind: 'unassigned'};
        if (rest.startsWith('e/')) {
            const [enumId, ...node] = rest.slice(2).split('/');
            return {kind: 'node', enumId: decodeURIComponent(enumId ?? ''), path: node.map(decodeURIComponent).join('/')};
        }
        return {kind: 'favorites'};
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
    function isChannel(ref: string): boolean {
        const i = ref.lastIndexOf(':');
        return i > 0 && ref.slice(i + 1) !== '0';
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
                return a.o.name.localeCompare(b.o.name);
            });
        }
        if (view.kind === 'node') return members(`${view.enumId}/${view.path}`, true).sort((a, b) => a.o.name.localeCompare(b.o.name));
        if (view.kind === 'unassigned') {
            return Object.entries(snap.objects).filter(([ref, o]) => isChannel(ref) && !o.enums.some((p) => !p.startsWith(FAVORITE_ENUM + '/'))).map(([ref, o]) => ({ref, o})).sort((a, b) => a.o.name.localeCompare(b.o.name));
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
    const refs = $derived(tiles.map((x) => x.ref));
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
    function onEvent(ref: string, key: string, value: unknown) {
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
        if (LEVEL_KEYS.has(key) && working(c.values)) {
            pendingLevel.set(`${ref}|${key}`, value);
            values[key] = c.values[key];
        } else if ((key === 'WORKING' || key === 'PROCESS') && !working(values)) {
            for (const k of LEVEL_KEYS) {
                const p = pendingLevel.get(`${ref}|${k}`);
                if (p !== undefined) {
                    values[k] = p;
                    pendingLevel.delete(`${ref}|${k}`);
                }
            }
        }
        m.set(ref, {...c, values});
        channels = m;
    }
    async function loadValues(list: string[]) {
        if (list.length === 0) return;
        try {
            const r = await loadChannels(list);
            const next = new Map(channels);
            for (const [ref, c] of r.channels) next.set(ref, c);
            channels = next;
            rpcError = r.errors.join('; ');
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
        const c = channels.get(ref);
        if (!c) return;
        const a = long ? longAction(c.model, c.desc) : mainAction(c.model, c.values);
        if (!a) return;
        acting = {...acting, [ref]: ''};
        // the tile shows what was sent at once (the stream confirms it); a refused write is taken back
        const undo = c.model.widget === 'button' ? () => undefined : apply(ref, {[a.key]: a.value});
        try {
            await setValue(ref, a.key, a.value);
            if (c.model.widget === 'button') flash(ref, long ? t('long press') : t('short press'));
        } catch (e) {
            undo();
            acting = {...acting, [ref]: (e as Error).message};
        }
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
                tileDrag = {ref, refs: tiles.map((x) => x.ref), moved: false, from: el.getBoundingClientRect()};
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
        if (!tileDrag) return tiles;
        const by = new Map(tiles.map((x) => [x.ref, x]));
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
    const sheetChannel = $derived(sheetRef ? channels.get(sheetRef) : undefined);
    const sheetName = $derived(sheetRef ? (snap?.objects[sheetRef]?.name || channelSub(sheetRef)) : '');
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
        const undo = apply(ref, {[key]: value});
        try {
            await setValue(ref, key, value);
        } catch (e) {
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
    function channelSub(ref: string): string {
        const i = ref.lastIndexOf(':');
        return i > 0 ? ref.slice(0, i).replace(/^[^.]*\./, '') + ':' + ref.slice(i + 1) : ref;
    }
</script>

<div class="app" class:app-drawer-open={drawerOpen}>
    <!-- the drawer: a column on the desktop, off-canvas behind the hamburger on a phone -->
    {#if drawerOpen}<button type="button" class="app-scrim" aria-label={t('Close menu')} onclick={pick}></button>{/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <nav class="app-drawer" class:app-sorting={!!drag} aria-label={t('App menu')} data-app-drawer={drawerOpen ? 'open' : 'closed'} data-app-sorting={drag ? '1' : undefined} onpointerdown={swipeStart} onpointermove={(ev) => { nodeMove(ev); swipeMove(ev); }} onpointerup={swipeEnd} onpointercancel={swipeEnd}>
        <ul class="app-tree">
            <li><a class="app-leaf" class:active={view.kind === 'favorites'} href="/app/favorites" use:link onclick={pick} data-app-entry="favorites">{t('Favorites')}</a></li>
            {#each sections as [id, e] (id)}
                <li>
                    <button type="button" class="app-fold" aria-expanded={isOpen(id)} onclick={() => toggle(id)} data-app-section={id}><span class="app-caret" aria-hidden="true">{isOpen(id) ? '▾' : '▸'}</span> {enumLabel(id, e)}</button>
                    {#if isOpen(id)}
                        {#snippet subtree(nodes: MetaNode[], parent: string, depth: number)}
                            <ul class="app-sub" style={`--depth:${depth}`}>
                                {#each ordered(id, parent, nodes) as n (n.id)}
                                    {@const path = parent ? `${parent}/${n.id}` : n.id}
                                    {@const key = `${id}/${path}`}
                                    <li>
                                        <div class="app-row" class:app-lifted={drag?.enumId === id && drag?.parent === parent && drag?.id === n.id} data-app-sib={`${id}/${parent}`}>
                                            {#if n.children?.length}<button type="button" class="app-fold app-fold-node" aria-expanded={isOpen(key)} aria-label={isOpen(key) ? t('Collapse') : t('Expand')} onclick={() => toggle(key)}><span class="app-caret" aria-hidden="true">{isOpen(key) ? '▾' : '▸'}</span></button>{:else}<span class="app-caret app-caret-none" aria-hidden="true"></span>{/if}
                                            <a class="app-leaf" class:active={activePath === key} href={nodeHref(id, path)} use:link onclick={(ev) => nodeClick(id, parent, n.id, ev)} draggable="false" ondragstart={(ev) => ev.preventDefault()} onpointerdown={(ev) => nodeDown(id, parent, n.id, ev)} onpointerup={() => void nodeUp(id, parent, n.id)} onpointercancel={() => void nodeUp(id, parent, n.id)} oncontextmenu={(ev) => { if (canConfigure) ev.preventDefault(); }} data-app-node={key}>{nodeLabel(n)}{#if problemNodes.has(key)}<span class="app-dot" role="img" aria-label={t('Something in here reports a problem')} title={t('Something in here reports a problem')} data-app-problem></span>{/if}</a>
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
            {#if messages > 0 || view.kind === 'messages'}<li><a class="app-leaf" class:active={view.kind === 'messages'} href="/app/messages" use:link onclick={pick} data-app-entry="messages">{t('Service messages')}<span class="ol-badge warn app-count" data-app-messages={messages}>{messages}</span></a></li>{/if}
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
            {:else if tiles.length === 0}
                <p class="ol-muted" data-app-empty>{view.kind === 'favorites' ? t('No favorites yet. A channel is added to your favorites from its card.') : t('No channels assigned here.')}</p>
            {:else}
                {#if rpcError}<div class="ol-notice error">{rpcError}</div>{/if}
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div class="app-grid" data-app-tiles={tiles.length} data-app-sorting={tileDrag ? '1' : undefined} onpointermove={tileMove}>
                    {#each shownTiles as x (x.ref)}
                        {@const c = channels.get(x.ref)}
                        {@const m = c?.model}
                        {@const on = c && m ? isOn(m, c.values) : false}
                        {@const state = c && m ? stateText(m, c.values, c.desc, t) : ''}
                        {@const kind = tileKind(x.ref)}
                        {@const bs = badges(x.ref)}
                        <!-- svelte-ignore a11y_no_static_element_interactions -->
                        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
                        <div class="ol-card app-tile" class:app-tile-read={kind === 'read'} class:app-tile-act={kind !== 'read'} class:app-tile-open={opens(x.ref)} data-app-tile={x.ref} data-app-widget={m?.widget ?? ''} data-app-on={on ? '1' : '0'} role={opens(x.ref) ? 'button' : undefined} tabindex={opens(x.ref) ? 0 : undefined} aria-label={opens(x.ref) ? t('Open {name}', {name: x.o.name || channelSub(x.ref)}) : undefined} class:app-lifted={tileDrag?.ref === x.ref} onclick={(ev) => cardClick(x.ref, ev)} onpointerdown={(ev) => cardDown(x.ref, ev)} onpointerup={() => void cardUp(x.ref)} onpointercancel={() => void cardUp(x.ref)} oncontextmenu={(ev) => { if (canConfigure) ev.preventDefault(); }} onkeydown={(ev) => { if (opens(x.ref) && (ev.key === 'Enter' || ev.key === ' ') && ev.target === ev.currentTarget) { ev.preventDefault(); openSheet(x.ref, ev); } }}>
                            <div class="app-tile-head">
                                {#if bs.length}
                                    <button type="button" class="app-badges" aria-label={t('{n} messages', {n: String(bs.length)})} title={bs.map((b) => t(b.label)).join(', ')} onclick={(ev) => { ev.stopPropagation(); showBadges(x.ref, x.o.name || channelSub(x.ref)); }} onpointerdown={(ev) => ev.stopPropagation()} data-app-badges={bs.length}>
                                        {#each bs.slice(0, 3) as b (b.key)}<span class="app-badge app-badge-{b.tone}" class:app-badge-solid={b.solid} class:app-badge-mild={b.mild} data-app-badge={b.key} data-icon={b.icon}><Icon name={b.icon} size={14} /></span>{/each}
                                        {#if bs.length > 3}<span class="app-badge app-badge-more">+{bs.length - 3}</span>{/if}
                                    </button>
                                {/if}
                                {#if m && m.commandKey && (kind !== 'read')}
                                    <button type="button" class="app-circle app-circle-act" class:on={on || !!flashed[x.ref]} aria-label={m.widget === 'button' ? t('Press {name}', {name: x.o.name || channelSub(x.ref)}) : t('Switch {name}', {name: x.o.name || channelSub(x.ref)})} aria-pressed={m.widget === 'button' ? undefined : on} onpointerdown={(ev) => { ev.stopPropagation(); pressStart(x.ref); }} onpointerup={() => pressEnd(x.ref)} onpointercancel={() => pressEnd(x.ref)} onpointerleave={() => pressEnd(x.ref)} onclick={(ev) => { ev.stopPropagation(); pressClick(x.ref); }} oncontextmenu={(ev) => { if (m.widget === 'button') ev.preventDefault(); }} data-app-act data-app-pressed={flashed[x.ref] ? '1' : undefined}>
                                        <Icon name={m.widget === 'dimmer' || m.widget === 'color' ? 'bulb' : m.widget === 'lock' || m.widget === 'door' ? 'lock' : m.widget === 'thermostat' ? 'gauge' : 'power'} size={18} />
                                    </button>
                                {:else}
                                    <span class="app-circle" class:on={on} aria-hidden="true"><Icon name={m?.widget === 'reading' || m?.widget === 'thermostat' ? 'gauge' : m?.widget === 'contact' ? 'shield' : m?.widget === 'motion' || m?.widget === 'signal' ? 'activity' : m?.widget === 'smoke' ? 'alert' : m?.widget === 'power' ? 'power' : m?.widget === 'button' ? 'zap' : 'tag'} size={18} /></span>
                                {/if}
                            </div>
                            <div class="app-tile-name" title={x.o.name}>{x.o.name || channelSub(x.ref)}</div>
                            <div class="ol-muted app-tile-state" data-app-state>{acting[x.ref] ? acting[x.ref] : flashed[x.ref] ? flashed[x.ref] : state}</div>
                        </div>
                    {/each}
                </div>
            {/if}
        {/if}
    </section>

    {#if taxonomyOpen && snap}
        <TaxonomySheet {snap} onclose={() => (taxonomyOpen = false)} onchanged={load} />
    {/if}
    {#if assignRef && snap}
        <AssignSheet ref={assignRef} {snap} {favoritesPath} from={assignFrom} onclose={() => (assignRef = null)} onchanged={() => void load()} />
    {/if}
    {#if sheetRef && sheetChannel}
        <Sheet channel={sheetChannel} name={sheetName} from={sheetFrom} onclose={() => (sheetRef = null)} onset={(k, val) => sheetSet(sheetRef!, k, val)} onput={(vals) => sheetPut(sheetRef!, vals)} />
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
    .app-fold { background: none; border: 0; color: var(--hmm-fg); font: inherit; cursor: pointer; padding: 0 6px; min-height: 44px; text-align: left; width: 100%; display: flex; align-items: center; gap: 6px; border-radius: 8px; }
    .app-fold:hover { background: var(--hmm-bg-hover, rgba(127, 127, 127, 0.12)); }
    .app-fold-node { width: 44px; min-width: 44px; justify-content: center; padding: 0; }
    .app-caret { display: inline-block; width: 18px; color: var(--hmm-fg-muted); font-size: 0.9em; }
    .app-leaf { display: flex; align-items: center; gap: 8px; padding: 0 10px; min-height: 44px; border-radius: 8px; color: var(--hmm-fg); text-decoration: none; flex: 1 1 auto; min-width: 0; }
    .app-leaf:hover { background: var(--hmm-bg-hover, rgba(127, 127, 127, 0.12)); }
    .app-leaf.active { background: var(--hmm-accent-bg, rgba(127, 127, 127, 0.18)); font-weight: 600; }
    .app-count { margin-left: auto; }
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
    .app-tile { padding: 12px; min-height: 110px; display: flex; flex-direction: column; }
    .app-tile-read { background: var(--hmm-bg, transparent); box-shadow: none; }
    .app-tile-head { display: flex; justify-content: space-between; flex-direction: row-reverse; margin-bottom: auto; }
    .app-badges { display: inline-flex; align-items: center; gap: 4px; background: none; border: 0; padding: 2px; cursor: pointer; color: var(--hmm-warn); align-self: flex-start; }
    /* occulited task 43: the message's icon and colour, as on the Status page and in homematic-manager's
       device table - a circle in the colour, UNREACH filled (away now), the sticky one only outlined */
    .app-badge { position: relative; display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box; width: 22px; height: 22px; border-radius: 50%; border: 1px solid currentColor; background: color-mix(in srgb, currentColor 14%, transparent); }
    .app-badge-red { color: var(--hmm-error); }
    .app-badge-orange { color: var(--hmm-warn); }
    .app-badge-blue { color: var(--hmm-accent); }
    .app-badge-grey, .app-badge-mild { color: var(--hmm-fg-muted); background: transparent; }
    .app-badge-solid { background: var(--hmm-error); border-color: var(--hmm-error); }
    .app-badge-solid :global(svg) { color: var(--hmm-bg); }
    .app-badge-more { font-size: 0.75em; font-weight: 600; width: auto; padding: 0 5px; }
    /* task 19: quiet - the title's size is not its own, nor a colour that alarms */
    .app-notlive { margin-left: 12px; font-size: 0.6em; font-weight: normal; color: var(--hmm-fg-muted); white-space: nowrap; vertical-align: middle; cursor: help; }
    .app-notlive::before { content: ''; display: inline-block; width: 7px; height: 7px; border-radius: 50%; border: 1.5px solid currentColor; margin-right: 5px; vertical-align: 1px; }
    .app-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--hmm-warn); margin-left: 8px; flex: 0 0 auto; }
    .app-circle { width: 40px; height: 40px; border-radius: 50%; border: 2px solid var(--hmm-border-muted); display: inline-flex; align-items: center; justify-content: center; color: var(--hmm-fg-muted); background: none; padding: 0; }
    .app-circle.on { color: var(--hmm-warn); border-color: var(--hmm-warn); }
    .app-circle-act { cursor: pointer; font: inherit; touch-action: manipulation; -webkit-user-select: none; user-select: none; }
    .app-circle-act:hover { border-color: var(--hmm-fg); }
    .app-circle-act:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .app-tile-act { cursor: default; }
    .app-tile-open { cursor: pointer; }
    .app-tile { -webkit-user-select: none; user-select: none; -webkit-touch-callout: none; }
    .app-grid[data-app-sorting] { touch-action: none; }
    .app-tile.app-lifted { box-shadow: 0 6px 18px rgba(0, 0, 0, 0.3); transform: scale(1.03); z-index: 1; }
    .app-tile-open:hover { box-shadow: 0 2px 10px rgba(0, 0, 0, 0.18); }
    .app-tile-open:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .app-tile-name { font-weight: 600; margin-top: 10px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .app-tile-state { font-size: var(--hmm-font-size-small); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .app-fab { display: none; }
    .app-scrim { display: none; }
    @media (min-width: 900px) {
        .app-grid { grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); }
    }
    @media (max-width: 899px) {
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
        .app-drawer, .app-fab { transition: none; }
    }
</style>
