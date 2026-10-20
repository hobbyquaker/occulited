<script lang="ts">
    import {flushSync, onMount, untrack} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type LogLine} from '../lib/api';
    import {t, i18n} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {router, navigate} from '../lib/router.svelte';
    import Loading from '../lib/Loading.svelte';
    import TimeRange from '../lib/TimeRange.svelte';
    import Select from '../lib/Select.svelte';
    import Icon from '../lib/Icon.svelte';
    import LogSettings from '../lib/LogSettings.svelte';
    import SearchInput from '../lib/SearchInput.svelte';
    import {CLOSE_MS, reveal} from '../lib/reveal';
    import {onscreen} from '../lib/popover';
    import {download} from '../lib/download';
    import {OCCULITED_AREAS} from '../lib/loglevels';
    import {LASTING_MAX, LASTING_MIN} from '../lib/lasting';
    import {bootParam, bootSpanMs, durationLabel, findBoot, isEarlierBoot, kernelParam, lineStamp, logPath, runParam, settingsParam, sourceParam, type BootInfo, type BootList, type LogSettingsTab, type LogSource} from '../lib/logpage';
    import {countLabel, joinPages, LOG_KEEP, LOG_LOAD_AHEAD, LOG_PAGE, rowAt, rowOffsets, rowWindow} from '../lib/logvirtual';

    // Task 93: the viewer shows every message, the system's without the kernel's, or the kernel's
    // alone (a Source among the filters); above it the boot the lines are of, the download, and the
    // settings behind the gear (lib/LogSettings.svelte), which stood above the viewer as two
    // disclosures. The source, the unit and the boot are the page's URL (lib/logpage.ts).
    // task 177: the query this page last saw while it showed - hidden, it must not follow another page's
    // task 79: the trace lines' fold (longer than TRACE_FOLD characters) and copy
    const TRACE_FOLD = 160;
    // by the line itself (task 178): its index moves as pages are added at either end
    let traceOpen = $state.raw(new Set<LogLine>());
    function toggleTrace(l: LogLine) {
        const n = new Set(traceOpen);
        if (n.has(l)) n.delete(l);
        else n.add(l);
        traceOpen = n;
    }
    function copyTrace(text: string) {
        try {
            void navigator.clipboard?.writeText(text);
        } catch {
            /* no clipboard (an insecure context): nothing to do */
        }
    }
    const life = pageLife();
    const query = $derived(new URLSearchParams(life.search));
    const routeSource = $derived(sourceParam(query.get('source')));
    // 27.5: the unit filter is a route parameter (`/log?unit=rfd`), not component state the
    // Services page reaches into. So the link from a service's Log button is shareable, opens in a
    // new tab, and the back button walks the filter back off again. The kernel's lines have no unit.
    const routeUnit = $derived(routeSource === 'kernel' ? '' : (query.get('unit') ?? ''));
    // the boot is a route parameter as well, and it stays when the source changes
    const routeBoot = $derived(bootParam(query.get('boot')));
    // task 102: one run's lines (a firmware flash, an ACME attempt), from its Show log link
    const routeRun = $derived(runParam(query.get('run')));

    let settingsOpen = $state(false);
    let settingsTab = $state<LogSettingsTab>('levels');
    function openSettings(tb: LogSettingsTab) {
        settingsTab = tb;
        settingsOpen = true;
    }

    // ---- the boot: GET /boots, a menu above the viewer ----
    let bootList = $state<BootList | null>(null);
    const currentBoot = $derived(bootList?.current ?? '');
    // the boots the journal holds; a boot only kept for the Services page's timeline has no lines
    const journalBoots = $derived((bootList?.boots ?? []).filter((b) => b.journal !== false));
    // in RAM the journal holds this boot only: the menu says why there is nothing more in it
    const ramOnly = $derived(!!bootList && !bootList.persistent && journalBoots.length <= 1);
    const locale = $derived(i18n.language === 'de' ? 'de-DE' : 'en-GB');
    function when(iso: string | undefined): string {
        if (!iso) return '…';
        return new Date(iso).toLocaleString(locale, {weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'});
    }
    function bootTitle(b: BootInfo): string {
        return b.current ? t('This boot') : when(b.first);
    }
    function bootDetail(b: BootInfo): string {
        if (b.current) return b.first ? t('since {time}', {time: when(b.first)}) : '';
        const ms = bootSpanMs(b);
        return ms === null ? '' : `${t('until {time}', {time: when(b.last)})} · ${durationLabel(ms)}`;
    }
    // what the menu marks: '' every boot (the default), '0' this boot (the kernel's default: its
    // stamps count from one boot's start), or an earlier boot's id
    const chosenBoot = $derived.by(() => {
        if (routeBoot === '') return routeSource === 'kernel' ? '0' : '';
        if (!isEarlierBoot(routeBoot, currentBoot)) return '0';
        return findBoot(journalBoots, routeBoot)?.boot_id ?? routeBoot;
    });
    const bootLabel = $derived.by(() => {
        if (chosenBoot === '') return t('all boots');
        if (chosenBoot === '0') return t('this boot');
        const b = findBoot(journalBoots, chosenBoot);
        return b ? when(b.first) : chosenBoot.startsWith('-') ? chosenBoot : chosenBoot.slice(0, 8);
    });
    let bootOpen = $state(false);
    let bootRoot: HTMLDivElement | undefined = $state();
    let bootButton: HTMLButtonElement | undefined = $state();
    // a choice only moves the route, as the unit's does; the route effect below loads
    function chooseBoot(b: string) {
        bootOpen = false;
        navigate(logPath({source: routeSource, unit: routeUnit, boot: b, run: routeRun}));
    }
    function chooseSource(v: string) {
        navigate(logPath({source: sourceParam(v), unit: routeUnit, boot: routeBoot, run: routeRun}));
    }
    function clearRun() {
        navigate(logPath({source: routeSource, unit: routeUnit, boot: routeBoot}));
    }

    // task 178: the lines are one contiguous run of the log, loaded in pages of LOG_PAGE while
    // scrolling and kept up to LOG_KEEP; past that the far end is unloaded and loads again on
    // scroll back. `older` and `newer` say whether the log goes on beyond the run's two ends -
    // the API's flags, and, when an end was unloaded, that. While `newer` is set the run is
    // detached from the live end: the stream stays closed until the pages have reached it again.
    // Raw state: twenty thousand lines are not proxied one by one; the run is replaced whole.
    let lines = $state.raw<LogLine[] | null>(null);
    let older = $state(false);
    let newer = $state(false);
    let olderUnloaded = $state(false);
    let newerUnloaded = $state(false);
    let loadingOlder = $state(false);
    let loadingNewer = $state(false);
    // the head is asked for on the next load (the jump to the start of a boot the route moves to)
    let pendingHead = false;
    // B-59: on a Pi's journal a filtered query takes seconds, and the answer to an earlier,
    // wider filter can land after the newer one and overwrite its lines - the tag showed as
    // chosen while the lines were the unit's alone. Only the newest request's answer counts.
    let loadSeq = 0;
    let error = $state('');
    // B-223: the journal's copies on a share the system cannot read - the lines are RAM's alone
    let copiesUnreadable = $state<{path: string; error: string} | null>(null);
    let source = $state<'syslog' | 'journald' | 'dmesg'>('syslog');
    // task 79: the tag and the severity may come with the link (the RPC trace's link, Remote access since task 224,
    // opens /system/log?tag=rpc-trace&severity=debug)
    let tag = $state(new URLSearchParams(router.search).get('tag') ?? '');
    // task 186: one of occulited's areas (OCCULITED_AREA in the journal) - its lines alone
    let area = $state('');
    // what the lines on screen were loaded for; the route effect sets them before each load
    let shown_ = $state<LogSource>('all');
    const kernel = $derived(shown_ === 'kernel');
    let unit = $state('');
    let boot = $state('');
    let run = $state('');
    // an earlier boot's log does not grow: nothing to follow
    const earlier = $derived(isEarlierBoot(boot, currentBoot));
    let severity = $state(new URLSearchParams(router.search).get('severity') ?? '');
    // The start of the time range can come with the URL too: the Status page's unclean-shutdown
    // warning links to `/log?since=@<epoch seconds>`, an hour before that boot. `@<seconds>` is
    // already what TimeRange sends for a picked instant, so the value is taken as it is and the
    // range button names it. Read on arrival only - the range is the page's own state afterwards,
    // and a unit chosen later moves the route without it (27.5).
    let since = $state(new URLSearchParams(router.search).get('since') ?? '');
    // arrived with a start: in RAM that is the warning's link for a journal without the boot before
    // this one, and the page says so above the viewer
    const sinceLinked = new URLSearchParams(router.search).has('since');
    let until = $state('');
    // task 168: the text filter can come with the URL too (`?q=fw%20<id>`, the Firewall page's
    // link to a logged rule's lines); read on arrival only, like the range's start
    let q = $state(new URLSearchParams(router.search).get('q') ?? '');
    // task 40: oldest first is the default - a terminal, the newest line at the bottom and the
    // view following it (task 31 had chosen newest first). The choice sticks per browser, and a
    // browser that has already chosen keeps its choice: no migration, no reset.
    let newestFirst = $state(false);
    try {
        newestFirst = localStorage.getItem('ol.logOrder') === 'newest';
    } catch {
        /* no storage: the default */
    }
    function setOrder(v: string) {
        newestFirst = v === 'newest';
        try {
            localStorage.setItem('ol.logOrder', v);
        } catch {
            /* fine */
        }
        scrollHome();
    }
    // task 93: a kernel line's time since the boot, as dmesg prints it, or the wall clock; per browser
    let kernelClock = $state<'boot' | 'wall'>('boot');
    try {
        kernelClock = localStorage.getItem('ol.kernelClock') === 'wall' ? 'wall' : 'boot';
    } catch {
        /* no storage: since the boot */
    }
    function setKernelClock(v: string) {
        kernelClock = v === 'wall' ? 'wall' : 'boot';
        try {
            localStorage.setItem('ol.kernelClock', kernelClock);
        } catch {
            /* fine */
        }
    }
    // most kernel lines are noise: one press for the warnings and errors, again for all
    function toggleWarnings() {
        severity = severity === 'warning' ? '' : 'warning';
        reload();
    }
    const shown = $derived(newestFirst ? [...(lines ?? [])].reverse() : (lines ?? []));
    const count = $derived(countLabel(lines?.length ?? 0, locale));
    // task 104: below 700 px the panel's second row folds behind a Filters toggle that counts the filters
    // set, so the log keeps most of the height; wider, the row is always there
    let wide = $state(typeof matchMedia !== 'function' || matchMedia('(min-width: 701px)').matches);
    onMount(() => {
        if (typeof matchMedia !== 'function') return;
        const mq = matchMedia('(min-width: 701px)');
        const change = () => (wide = mq.matches);
        mq.addEventListener('change', change);
        return () => mq.removeEventListener('change', change);
    });
    // folded by default; a reader who unfolds the filters finds them unfolded next time (per browser, as
    // the boot timeline and the LED page's details are remembered)
    let filtersOpen = $state(false);
    try {
        filtersOpen = localStorage.getItem('ol.log.filters') === 'open';
    } catch {
        /* no storage: folded */
    }
    function toggleFilters() {
        filtersOpen = !filtersOpen;
        try {
            localStorage.setItem('ol.log.filters', filtersOpen ? 'open' : 'closed');
        } catch {
            /* fine */
        }
    }
    // task 102: a run is a filter too, and a folded card says so
    const filterCount = $derived([routeSource !== 'all', !kernel && !!unit, !kernel && !!tag, !kernel && !!area, !!severity, !!since || !!until, !!run].filter(Boolean).length);
    let auto = $state(true);
    let follow = $state(true);
    let box: HTMLDivElement | undefined = $state();
    let stream: EventSource | null = null;

    function params() {
        const p = new URLSearchParams();
        const k = kernelParam(shown_);
        if (k) p.set('kernel', k);
        if (!kernel && tag) p.set('tag', tag);
        if (!kernel && area) p.set('area', area);
        if (!kernel && unit) p.set('unit', unit);
        if (boot) p.set('boot', boot);
        if (run) p.set('run', run);
        if (severity) p.set('severity', severity);
        if (since) p.set('since', since);
        if (until) p.set('until', until);
        if (q) p.set('q', q);
        p.set('limit', String(LOG_PAGE));
        return p;
    }
    type LogAnswer = {lines: LogLine[]; source: 'syslog' | 'journald' | 'dmesg'; error?: string; copies_unreadable?: {path: string; error: string}; older?: boolean; newer?: boolean};

    // task 64: the download, a menu of the two formats above the viewer. It is what the filters
    // select, not the lines on screen: the same filters without the page's limit, and oldest first
    // whatever the page's order (the box sends it so). A plain link, so a long journal streams to
    // the disk instead of into a JavaScript blob. The link carries no session (task 125): the
    // click fetches a one-time ticket for it and follows the link with that (lib/download.ts), so a
    // session without a cookie - the shell's Bearer on a host without one - works too and the
    // session never stands in a URL. The link does not depend on the page's path.
    let downloadOpen = $state(false);
    let downloadRoot: HTMLDivElement | undefined = $state();
    let downloadButton: HTMLButtonElement | undefined = $state();
    function downloadHref(format: 'text' | 'json'): string {
        const p = params();
        p.delete('limit');
        p.set('format', format);
        return `/api/system/v1/log/download?${p}`;
    }
    function downloadWith(format: 'text' | 'json') {
        return (e: Event) => {
            e.preventDefault();
            downloadChosen();
            void download(downloadHref(format)).catch((err: Error) => (error = err.message));
        };
    }
    // a menu above the viewer closes on Escape (the focus back on its button) and on a click outside
    // it; the listeners exist only while it is open
    function dismissable(isOpen: boolean, root: HTMLElement | undefined, button: HTMLElement | undefined, close: () => void) {
        if (!isOpen) return;
        const onKey = (ev: KeyboardEvent) => {
            if (ev.key !== 'Escape') return;
            close();
            button?.focus();
        };
        const onDown = (ev: MouseEvent) => {
            if (root && !root.contains(ev.target as Node)) close();
        };
        document.addEventListener('keydown', onKey);
        document.addEventListener('mousedown', onDown);
        return () => {
            document.removeEventListener('keydown', onKey);
            document.removeEventListener('mousedown', onDown);
        };
    }
    $effect(() => dismissable(downloadOpen, downloadRoot, downloadButton, () => (downloadOpen = false)));
    $effect(() => dismissable(bootOpen, bootRoot, bootButton, () => (bootOpen = false)));
    // after the click has done its work: the link must still be in the page when the browser
    // follows it
    function downloadChosen() {
        setTimeout(() => (downloadOpen = false));
    }
    // ---- task 178: the windowed list ----
    // Only the rows in view and a margin around it are in the DOM; a spacer above and below
    // stands for the rest. A row's height is measured by a ResizeObserver once it is rendered
    // (lines wrap, so no fixed height holds) and remembered by the line; a row not yet measured
    // counts as the mean of the measured ones. The offsets of every row follow from that, and
    // they are recomputed whole when a height changes - twenty thousand additions, nothing.
    let heights = new WeakMap<LogLine, number>();
    let measuredSum = 0;
    let measuredCount = 0;
    let estimate = $state(20);
    let hv = $state(0); // bumped when a measured height changes: the offsets follow
    let scrollTop = $state(0);
    let viewport = $state(600);
    let edgeTop: HTMLDivElement | undefined = $state();
    let edgeTopH = $state(0);
    const offsets = $derived.by(() => {
        void hv;
        const est = estimate;
        return rowOffsets(shown.length, (i) => heights.get(shown[i]!) ?? est);
    });
    const win = $derived(rowWindow(offsets, scrollTop - edgeTopH, viewport));
    const visible = $derived(shown.slice(win.start, win.end));
    const padTop = $derived(offsets[win.start] ?? 0);
    const padBottom = $derived((offsets[shown.length] ?? 0) - (offsets[win.end] ?? 0));
    // the rows' sizes, in batches: the observer's, one callback per frame, and the rows recycled
    // for other lines as the window moves, read once the DOM has settled (one reflow for all).
    // A batch takes the view's anchor first, records the heights, and keeps the view in place.
    const rowOf = new Map<Element, LogLine>();
    // B-292: a height as the layout has it, fractions included. offsetHeight rounds to whole pixels,
    // while the observer's borderBoxSize does not: a row of 37.5 px recycled for another line was
    // recorded as 38, and the offsets drifted from the DOM by half a pixel per such row - the
    // view's anchor moved by 2-3 px when a page was added above it.
    const heightOf = (n: Element) => n.getBoundingClientRect().height;
    const ro = typeof ResizeObserver === 'function'
        ? new ResizeObserver((entries) => {
              applyMeasured(entries.map((e) => [e.target, e.borderBoxSize?.[0]?.blockSize ?? heightOf(e.target)]));
          })
        : null;
    const recycled = new Set<Element>();
    let recycledDue = false;
    function measureRecycled() {
        recycledDue = false;
        const nodes = [...recycled];
        recycled.clear();
        applyMeasured(nodes.map((n) => [n, heightOf(n)]));
    }
    function applyMeasured(sizes: [Element, number][]) {
        const a = anchor();
        let changed = false;
        for (const [node, h] of sizes) {
            const l = rowOf.get(node);
            if (l && record(l, h)) changed = true;
        }
        if (!changed) return;
        flushSync(() => (hv += 1));
        restore(a);
        if (home) scrollHome();
    }
    function record(l: LogLine, h: number): boolean {
        if (!(h > 0)) return false;
        const was = heights.get(l);
        if (was === h) return false;
        if (was === undefined) {
            measuredSum += h;
            measuredCount += 1;
        } else {
            measuredSum += h - was;
        }
        heights.set(l, h);
        estimate = measuredSum / measuredCount;
        return true;
    }
    // each rendered row: measured while it is in the DOM
    function measure(node: HTMLElement, l: LogLine) {
        rowOf.set(node, l);
        ro?.observe(node);
        return {
            update(next: LogLine) {
                rowOf.set(node, next);
                recycled.add(node);
                if (!recycledDue) {
                    recycledDue = true;
                    queueMicrotask(measureRecycled);
                }
            },
            destroy() {
                rowOf.delete(node);
                recycled.delete(node);
                ro?.unobserve(node);
            },
        };
    }
    // the row at the top of the view and where its top sits: what a change must keep in place.
    // The line is what is remembered, since its index moves with the pages.
    function anchor(): {line: LogLine; top: number} | null {
        if (!box || shown.length === 0) return null;
        const y = box.scrollTop - edgeTopH;
        const i = Math.min(rowAt(offsets, y), shown.length - 1);
        return {line: shown[i]!, top: (offsets[i] ?? 0) - y};
    }
    function restore(a: {line: LogLine; top: number} | null) {
        if (!a || !box) return;
        const i = shown.indexOf(a.line);
        if (i < 0) return;
        const want = (offsets[i] ?? 0) - a.top + edgeTopH;
        if (Math.abs(want - box.scrollTop) > 0.5) {
            box.scrollTop = want;
            scrollTop = box.scrollTop;
        }
    }
    // a change of the rows or their heights with the view kept where it was - or at its home
    // end, when it stood there. The DOM is brought up to date first: a spacer must have its new
    // height before the scroll position can be set past it.
    function keep(change: () => void) {
        const a = anchor();
        flushSync(change);
        restore(a);
        if (home) scrollHome();
    }
    // the end of the log the reader watches: the bottom when oldest first, the top otherwise
    function scrollHome() {
        queueMicrotask(() => {
            if (!box) return;
            box.scrollTop = newestFirst ? 0 : box.scrollHeight;
            scrollTop = box.scrollTop;
        });
    }
    // task 178: the other end - the start of the boot after the jump there
    function scrollStart() {
        queueMicrotask(() => {
            if (!box) return;
            box.scrollTop = newestFirst ? box.scrollHeight : 0;
            scrollTop = box.scrollTop;
        });
    }
    // task 40: whether the view is at that end (within 40 px). A reader who has scrolled up into
    // the history must not be yanked back by every arriving line; the view follows again as
    // soon as he scrolls back to the edge.
    function atHome(): boolean {
        if (!box) return true;
        const away = newestFirst ? box.scrollTop : box.scrollHeight - box.scrollTop - box.clientHeight;
        return away <= 40;
    }
    // task 104: the log's height changes with the panel above it - a row that wraps once the boot list has
    // answered, the phone's fold, a resized window. A view that stood at its end stays at its end; one the
    // reader has scrolled into the history is left where it is. Its own scrolling says which it is, since a
    // change of height scrolls nothing.
    let home = true;
    $effect(() => {
        const el = box;
        if (!el || typeof ResizeObserver !== 'function') return;
        let width = el.clientWidth;
        const observer = new ResizeObserver(() => {
            viewport = el.clientHeight;
            if (el.clientWidth !== width) {
                // the lines wrap differently now: every remembered height is stale, and the
                // rows in the DOM are measured again
                width = el.clientWidth;
                heights = new WeakMap();
                measuredSum = 0;
                measuredCount = 0;
                keep(() => (hv += 1));
                applyMeasured([...rowOf.keys()].map((n) => [n, heightOf(n)]));
            }
            if (home) scrollHome();
        });
        observer.observe(el);
        viewport = el.clientHeight;
        return () => observer.disconnect();
    });
    $effect(() => {
        const el = edgeTop;
        if (!el || typeof ResizeObserver !== 'function') {
            edgeTopH = 0;
            return;
        }
        const observer = new ResizeObserver(() => (edgeTopH = heightOf(el)));
        observer.observe(el);
        edgeTopH = heightOf(el);
        return () => observer.disconnect();
    });
    // occulited B-92: only a scroll away from the home end leaves it. The scroll event of the page's
    // own scrollHome can arrive after the box has shrunk again (the phone's fold, in steps of 90 px
    // on a loaded machine), and a judgement from the distance alone then took the shrink for the
    // reader's scroll and stopped following; a box that changes height scrolls nothing itself.
    let seenTop = 0;
    function onScroll() {
        if (!box) return;
        scrollTop = box.scrollTop;
        const moved = box.scrollTop - seenTop;
        seenTop = box.scrollTop;
        if (newestFirst ? moved > 0.5 : moved < -0.5) home = atHome();
        else if (atHome()) home = true;
        nearEnds();
    }
    // the next page loads when the view comes within LOG_LOAD_AHEAD px of an end that has more
    function nearEnds() {
        if (!box || !lines || lines.length === 0) return;
        const top = box.scrollTop;
        const bottom = box.scrollHeight - box.scrollTop - box.clientHeight;
        const nearOld = (newestFirst ? bottom : top) < LOG_LOAD_AHEAD;
        const nearNew = (newestFirst ? top : bottom) < LOG_LOAD_AHEAD;
        if (nearOld && older && !loadingOlder) void loadOlder();
        if (nearNew && newer && !loadingNewer) void loadNewer();
    }
    // after every change of the run: the view may still be within reach of an end. A microtask
    // later, since a run replaced whole is scrolled to its home end by one queued before this.
    $effect(() => {
        void shown;
        void older;
        void newer;
        queueMicrotask(nearEnds);
    });
    // the run replaced whole: the tail (a load), or the head (the jump to the start)
    function replaceRun(r: LogAnswer, head: boolean) {
        lines = r.lines;
        reconnects = new Map();
        source = r.source ?? 'syslog';
        takeIn(r.lines);
        // a log that could not be read (dmesg refused, journalctl failed) says so
        error = r.error ?? '';
        copiesUnreadable = r.copies_unreadable ?? null;
        older = !!r.older;
        newer = !!r.newer;
        olderUnloaded = false;
        newerUnloaded = false;
        loadingOlder = false;
        loadingNewer = false;
        if (head) {
            home = false;
            scrollStart();
        } else {
            home = true;
            scrollHome();
        }
    }
    async function load() {
        const seq = ++loadSeq;
        const head = pendingHead;
        pendingHead = false;
        const p = params();
        if (head) p.set('head', '1');
        try {
            const r = await api.get<LogAnswer>(`/api/system/v1/log?${p}`);
            if (seq !== loadSeq) return; // a newer filter's answer is in, or on its way
            replaceRun(r, head);
            if (source === 'journald' && follow && !earlier && !newer) openStream();
        } catch (e) {
            if (seq === loadSeq) error = (e as Error).message;
        }
    }
    // task 178: the page before the run's first line, added at the old end
    async function loadOlder() {
        const first = lines?.[0];
        if (!first?.cursor || loadingOlder) return;
        const seq = loadSeq;
        loadingOlder = true;
        const p = params();
        p.set('before', first.cursor);
        try {
            const r = await api.get<LogAnswer>(`/api/system/v1/log?${p}`);
            if (seq !== loadSeq || !lines || lines[0] !== first) return; // reloaded meanwhile
            if (r.error) {
                error = r.error;
                return;
            }
            takeIn(r.lines);
            const joined = joinPages(lines, r.lines, 'old');
            keep(() => {
                lines = joined.lines;
                older = !!r.older;
                olderUnloaded = false;
                if (joined.dropped === 'new') {
                    newer = true;
                    newerUnloaded = true;
                    closeStream();
                }
            });
        } catch (e) {
            if (seq === loadSeq) error = (e as Error).message;
        } finally {
            if (seq === loadSeq) loadingOlder = false;
        }
    }
    // the page after the run's last line, added at the new end; the last one reattaches the run
    // to the live end, and the stream takes over from its last line
    async function loadNewer() {
        const last = lines?.at(-1);
        if (!last?.cursor || loadingNewer) return;
        const seq = loadSeq;
        loadingNewer = true;
        const p = params();
        p.set('after', last.cursor);
        try {
            const r = await api.get<LogAnswer>(`/api/system/v1/log?${p}`);
            if (seq !== loadSeq || !lines || lines.at(-1) !== last) return;
            if (r.error) {
                error = r.error;
                return;
            }
            takeIn(r.lines);
            const joined = joinPages(lines, r.lines, 'new');
            keep(() => {
                lines = joined.lines;
                newer = !!r.newer;
                newerUnloaded = false;
                if (joined.dropped === 'old') {
                    older = true;
                    olderUnloaded = true;
                }
            });
        } catch (e) {
            if (seq === loadSeq) error = (e as Error).message;
        } finally {
            if (seq === loadSeq) loadingNewer = false;
        }
    }
    // the jump to the start: the oldest page of the boot (or of the range), paging towards the
    // new end from there. With every boot on screen it is this boot's start, so the route moves
    // to this boot first and the load that follows asks for the head.
    function jumpToStart() {
        closeStream();
        pendingHead = true;
        if (chosenBoot === '' && !since) {
            chooseBoot('0');
            return;
        }
        reload();
    }
    // back to the newest: the tail, as the page opened
    function jumpToNewest() {
        reload();
    }
    // journald: after the initial page the server streams new entries (SSE); dmesg: poll. The
    // stream starts after the run's last line (task 178), so nothing between the page and the
    // stream is lost; it is batched into the run a few times a second, not line by line.
    let inbox: LogLine[] = [];
    let inboxTimer: ReturnType<typeof setTimeout> | null = null;
    // occulited task 19 (maintainer, 2026-10-06): Follow stays on when the stream breaks - a restart
    // of occulited, lighttpd's 502 meanwhile, the network. The stream is opened again after 1 s, then
    // twice as long each time up to 30 s (lasting.ts' steps), from the last line on screen, so no
    // line comes twice; where it was broken the log says "reconnected" with the time.
    let followRetry: ReturnType<typeof setTimeout> | undefined;
    let followWait = LASTING_MIN;
    let broken = false; // the stream broke, and the next one that opens says so
    let reconnects = $state.raw(new Map<LogLine, string>()); // the line after which a stream opened again
    function openStream() {
        closeStream();
        const p = params();
        p.set('limit', '0');
        const last = lines?.at(-1)?.cursor;
        if (last) p.set('after', last);
        const s = new EventSource(`/api/system/v1/log/stream?${p}`);
        stream = s;
        s.onopen = () => {
            followWait = LASTING_MIN;
            if (!broken) return;
            broken = false;
            const at = lines?.at(-1);
            if (at) reconnects = new Map(reconnects).set(at, new Date().toLocaleTimeString(locale));
        };
        s.onmessage = (ev) => {
            const l = JSON.parse(ev.data) as LogLine;
            note(l);
            inbox.push(l);
            if (!inboxTimer) inboxTimer = setTimeout(flushInbox, 40);
        };
        s.onerror = () => {
            if (stream !== s) return;
            // the lines that arrived stay, and the next stream starts after them; the browser's own
            // reconnect would ask from the first stream's start again
            flushInbox();
            closeStream();
            broken = true;
            followRetry = setTimeout(() => {
                followRetry = undefined;
                if (follow && !stream && life.active && source === 'journald' && !earlier && !newer) openStream();
            }, followWait);
            followWait = Math.min(followWait * 2, LASTING_MAX);
        };
    }
    function flushInbox() {
        inboxTimer = null;
        if (inbox.length === 0) return;
        const page = inbox;
        inbox = [];
        home = atHome(); // measured before the lines are in the DOM
        const joined = joinPages(lines ?? [], page, 'new');
        keep(() => {
            lines = joined.lines;
            if (joined.dropped === 'old') {
                older = true;
                olderUnloaded = true;
            }
        });
    }
    function closeStream() {
        clearTimeout(followRetry);
        followRetry = undefined;
        stream?.close();
        stream = null;
        if (inboxTimer) clearTimeout(inboxTimer);
        inboxTimer = null;
        inbox = [];
    }
    function reload() {
        closeStream();
        void load();
    }
    onMount(() => {
        // task 85: a link with settings=journal (the journal's Status warnings) opens the sheet on that tab
        const linked = settingsParam(new URLSearchParams(router.search).get('settings'));
        if (linked) openSettings(linked);
        api.get<{services: {id: string}[]}>('/api/system/v1/services').then((r) => (known = r.services.map((s) => s.id))).catch(() => {});
        api.get<BootList>('/api/system/v1/boots').then((r) => (bootList = r)).catch(() => {});
        // the poll replaces the run with the tail: not while the reader is in the history (task 178)
        const id = setInterval(() => life.active && auto && lines !== null && source !== 'journald' && home && !newer && load(), 5000);
        // task 177: hidden, the live stream closes; back, the lines load again (and the stream with them)
        const stopHide = $effect.root(() => {
            $effect(() => {
                if (!life.active) closeStream();
            });
        });
        const stopReturn = life.onReturn(() => void load());
        return () => {
            stopReturn();
            stopHide();
            clearInterval(id);
            closeStream();
        };
    });
    // the route is the source of truth: arriving, walking back, a source, a unit or a boot chosen -
    // each loads once. The kernel's lines are another log than the system's: its lines, tags and
    // units are not the ones on screen, which go.
    let loadedKey = '';
    $effect(() => {
        const key = `${routeSource}|${routeUnit}|${routeBoot}|${routeRun}`;
        if (key === loadedKey) return;
        loadedKey = key;
        const [toSource, toUnit, toBoot, toRun] = [routeSource, routeUnit, routeBoot, routeRun];
        untrack(() => {
            if ((toSource === 'kernel') !== (shown_ === 'kernel')) {
                lines = null;
                error = '';
                tag = '';
                area = '';
                tagList = [];
                unitList = [];
            }
            shown_ = toSource;
            unit = toUnit;
            boot = toBoot;
            run = toRun;
            reload();
        });
    });
    // the select only moves the route; the effect above takes the unit from there and reloads,
    // so there is one path into `unit` whatever fired first (B-59)
    function chooseUnit(v: string) {
        if (v === routeUnit) {
            unit = v;
            reload();
            return;
        }
        navigate(logPath({source: routeSource, unit: v, boot: routeBoot, run: routeRun}));
    }
    function chooseTag(v: string) {
        tag = v;
        reload();
    }
    $effect(() => {
        if (source !== 'journald' || earlier || newer) {
            closeStream();
            return;
        }
        if (follow && !stream) openStream();
        if (!follow) closeStream();
    });
    // task 31: the tag and unit lists grow with every load and never shrink - a filtered page
    // has one tag, and a list of that one tag plus "all" made the dropdown look broken.
    // B-59: they are snapshots, changed only by a load - the stream's tags and units are noted
    // silently and taken in on the next reload, so the two selects' option lists never move
    // under an open dropdown while lines arrive.
    let tagList = $state<string[]>([]);
    let unitList = $state<string[]>([]);
    const pendingTags = new Set<string>();
    const pendingUnits = new Set<string>();
    function note(l: LogLine) {
        if (l.tag) pendingTags.add(l.tag);
        if (l.unit) pendingUnits.add(l.unit);
    }
    function takeIn(ls: LogLine[]) {
        for (const l of ls) note(l);
        const tg = new Set([...tagList, ...pendingTags]);
        const un = new Set([...unitList, ...pendingUnits]);
        pendingTags.clear();
        pendingUnits.clear();
        if (tg.size !== tagList.length) tagList = [...tg].sort();
        if (un.size !== unitList.length) unitList = [...un].sort();
    }
    // the chosen value is always an option, so the select can show it whatever the list holds
    const tags = $derived(tag && !tagList.includes(tag) ? [tag, ...tagList].sort() : tagList);
    // the unit filter offers the services the box knows plus whatever appeared in the lines
    let known = $state<string[]>([]);
    // the unit named by the route belongs in the list even before /services has answered
    const units = $derived(Array.from(new Set([unit, ...known, ...unitList].filter(Boolean))).sort());
    // task 40: the two lists as the Select's options; the value is the label
    const unitOptions = $derived(units.map((u) => ({value: u, label: u})));
    // the box's own identifiers by name; the value stays the identifier, which the filter finds
    // too. lighttpd (its error log) is its own name, and so is every other tag.
    function tagLabel(tg: string): string {
        switch (tg) {
            case 'hmipserver':
                return t('HmIP server');
            case 'lighttpd-access':
                return t('lighttpd access log');
            case 'addon-install':
                return t('Addon installs');
            // task 145: the recovery system's install log, carried in at the boot after an update
            case 'recovery':
                return t('Recovery system');
            // task 102: the runs' lines
            case 'radio-firmware':
                return t('Radio firmware');
            case 'acme':
                return t('Certificate (ACME)');
            default:
                return tg;
        }
    }
    const tagOptions = $derived(tags.map((tg) => ({value: tg, label: tagLabel(tg)})));
    function level(l: LogLine) {
        return (l.severity ?? '').replace('warning', 'warn').replace('emerg', 'emerg').toUpperCase().slice(0, 6);
    }
    function stamp(l: LogLine): string {
        return lineStamp(l, kernel, kernelClock);
    }
</script>

<!-- task 104: the heading and the filter panel keep the page's side margin; the log below them runs from
     the window's left edge to its right edge and down to its bottom, the one thing on the page that scrolls -->
<div class="lg-top">
<SystemTitle />
<div class="ol-card lg-panel">
<div class="lg-head">
    <!-- task 104: the text filter takes the rest of the first row and filters while typing -->
    <SearchInput bind:value={q} placeholder={t('Text filter')} label={t('Text filter')} onsearch={reload} />
    {#if bootList && bootList.source === 'journald'}
        <div class="ol-menu lg-boot" bind:this={bootRoot}>
            <button type="button" class="hmm-button lg-menu-btn" bind:this={bootButton} aria-haspopup="menu" aria-expanded={bootOpen} onclick={() => (bootOpen = !bootOpen)}>
                <Icon name="restart" size={14} /><span>{t('Boot')}: {bootLabel}</span><span class="ol-caret" aria-hidden="true">▾</span>
            </button>
            {#if bootOpen}
                <div class="ol-menupop lg-menu-pop" role="menu" aria-label={t('Boot')} use:onscreen>
                    {#if routeSource !== 'kernel'}
                        <button type="button" role="menuitemradio" aria-checked={chosenBoot === ''} class="ol-menuitem lg-menu-item" onclick={() => chooseBoot('')}>
                            <span class="lg-menu-label">{t('All boots')}</span>
                        </button>
                    {/if}
                    {#each journalBoots as b (b.boot_id)}
                        {@const value = b.current ? '0' : b.boot_id}
                        <button type="button" role="menuitemradio" aria-checked={chosenBoot === value} class="ol-menuitem lg-menu-item" data-boot={b.boot_id} onclick={() => chooseBoot(value)}>
                            <span class="lg-menu-label">{bootTitle(b)}</span>
                            {#if bootDetail(b)}<span class="lg-menu-hint">{bootDetail(b)}</span>{/if}
                        </button>
                    {/each}
                    {#if ramOnly}
                        <div class="ol-menusep"></div>
                        <p class="lg-menu-note">{t('Earlier boots need a persistent journal.')}</p>
                        <button
                            type="button"
                            role="menuitem"
                            class="ol-menuitem lg-menu-item"
                            onclick={() => {
                                bootOpen = false;
                                openSettings('journal');
                            }}><span class="lg-menu-label">{t('Storage settings')}</span></button
                        >
                    {/if}
                </div>
            {/if}
        </div>
    {/if}
    <div class="ol-menu lg-download" bind:this={downloadRoot}>
        <button type="button" class="hmm-button lg-menu-btn" bind:this={downloadButton} aria-haspopup="menu" aria-expanded={downloadOpen} onclick={() => (downloadOpen = !downloadOpen)}>
            <Icon name="download" size={14} /><span>{t('Download')}</span><span class="ol-caret" aria-hidden="true">▾</span>
        </button>
        {#if downloadOpen}
            <div class="ol-menupop lg-menu-pop lg-download-pop" role="menu" aria-label={t('Download')} use:onscreen>
                <p class="lg-menu-note">{t('Everything the filters select, oldest first — not only the lines shown.')}</p>
                <a role="menuitem" class="ol-menuitem lg-menu-item" data-format="text" href={downloadHref('text')} download onclick={downloadWith('text')}>
                    <span class="lg-menu-label">{t('Text')}{' '}<span class="hmm-mono ol-muted">.txt</span></span>
                    <span class="lg-menu-hint">{t('One line per entry, as journalctl prints it')}</span>
                </a>
                <a role="menuitem" class="ol-menuitem lg-menu-item" data-format="json" href={downloadHref('json')} download onclick={downloadWith('json')}>
                    <span class="lg-menu-label">JSON{' '}<span class="hmm-mono ol-muted">.jsonl</span></span>
                    <span class="lg-menu-hint">{t('One JSON object per entry, one per line')}</span>
                </a>
            </div>
        {/if}
    </div>
    <button type="button" class="hmm-button lg-menu-btn lg-settings" aria-label={t('Log settings')} title={t('Log settings')} onclick={() => openSettings('levels')}>
        <Icon name="settings" size={16} />
    </button>
</div>
{#if !wide}
    <!-- task 104: on a phone the second row folds away, so the log keeps most of the height -->
    <button type="button" class="hmm-button lg-filters-toggle" aria-expanded={filtersOpen} aria-controls="lg-filters" onclick={toggleFilters}>
        <span class="ol-caret" aria-hidden="true">{filtersOpen ? '▾' : '▸'}</span>{t('Filters')}{#if filterCount > 0}<span class="ol-badge good" data-filter-count>{filterCount}</span>{/if}
    </button>
{/if}
{#if wide || filtersOpen}
<div class="ol-toolbar lg-filters" id="lg-filters" in:reveal out:reveal={{duration: CLOSE_MS}}>
    <!-- task 93: what the lines are - every message, the system's, or the kernel's -->
    <select class="hmm-select" value={routeSource} onchange={(e) => chooseSource(e.currentTarget.value)} aria-label={t('Source')}>
        <option value="all">{t('Source')}: {t('all')}</option>
        <option value="system">{t('Source')}: {t('system')}</option>
        <option value="kernel">{t('Source')}: {t('kernel')}</option>
    </select>
    <!-- task 40: the shell's own filterable select; the option lists stay the snapshots B-59
         made them, and the choice still only moves the route (unit) or reloads (tag). The
         kernel's lines have neither. -->
    {#if !kernel && source === 'journald'}
        <Select value={unit} options={unitOptions} label={t('Unit')} onchange={chooseUnit} />
    {/if}
    {#if !kernel}
        <Select value={tag} options={tagOptions} label={t('Tag')} onchange={chooseTag} />
    {/if}
    <!-- task 186: occulited's lines of one area (the Log settings' debug areas) -->
    {#if !kernel && source === 'journald'}
        <select class="hmm-select" bind:value={area} onchange={reload} aria-label={t('Area')} data-filter="area">
            <option value="">{t('Area')}: {t('all')}</option>
            {#each OCCULITED_AREAS as a (a.id)}<option value={a.id}>{t(a.label)}</option>{/each}
        </select>
    {/if}
    {#if run}
        <!-- task 102: the lines of one run; the ✕ shows the whole log again -->
        <span class="lg-run" data-run={run}>{t('Run')}: <span class="hmm-mono">{run}</span><button type="button" class="hmm-button lg-run-clear" aria-label={t('Show the whole log')} title={t('Show the whole log')} onclick={clearRun}>✕</button></span>
    {/if}
    <select class="hmm-select" bind:value={severity} onchange={reload} aria-label={t('Severity')}>
        <option value="">{t('Severity')}: {t('all')}</option>
        {#each ['debug', 'info', 'notice', 'warning', 'err', 'crit'] as s (s)}<option value={s}>≥ {s}</option>{/each}
    </select>
    {#if kernel}
        <button type="button" class="hmm-button lg-chip" aria-pressed={severity === 'warning'} onclick={toggleWarnings}>{t('Warnings and errors only')}</button>
    {/if}
    {#if source === 'journald'}
        <TimeRange bind:since bind:until onchange={reload} />
    {/if}
    <select class="hmm-select" value={newestFirst ? 'newest' : 'oldest'} onchange={(e) => setOrder(e.currentTarget.value)} aria-label={t('Order')}>
        <option value="newest">{t('Order')}: {t('newest first')}</option>
        <option value="oldest">{t('Order')}: {t('oldest first')}</option>
    </select>
    {#if kernel}
        <select class="hmm-select" value={kernelClock} onchange={(e) => setKernelClock(e.currentTarget.value)} aria-label={t('Timestamps')}>
            <option value="boot">{t('Time')}: {t('since boot')}</option>
            <option value="wall">{t('Time')}: {t('wall clock')}</option>
        </select>
    {/if}
    {#if source === 'journald'}
        <label title={earlier ? t('An earlier boot gets no new lines.') : undefined}><input type="checkbox" bind:checked={follow} disabled={earlier} /> {t('Follow')}</label>
    {:else}
        <label><input type="checkbox" bind:checked={auto} /> {t('Auto refresh')}</label>
    {/if}
    <button class="hmm-button" onclick={reload}>{t('Refresh')}</button>
    <!-- task 178: the jump to the oldest page - of the boot, or of the range when one is set - and,
         while the run is detached from the live end, back to the newest -->
    <button type="button" class="hmm-button" data-jump="start" onclick={jumpToStart} disabled={!lines || !!error}>{since ? t('Start of the range') : t('Start of the boot')}</button>
    {#if newer}
        <button type="button" class="hmm-button" data-jump="newest" onclick={jumpToNewest}>{t('Newest')}</button>
    {/if}
    <span class="ol-muted">{source === 'journald' ? 'journald' : source === 'dmesg' ? 'dmesg' : t('syslog (busybox)')}</span>
    {#if lines && lines.length > 0}
        <!-- task 178: how many are in the page, and that the rest loads while scrolling - the page never
             cuts the log silently -->
        <span class="ol-muted" data-count={lines.length} data-first={lines[0]?.cursor ?? ''}>{t('{n} entries', {n: count})}{#if older || newer}{' · '}{t('more load while scrolling')}{/if}</span>
    {/if}
    <!-- a route can name a unit on a box whose log is busybox syslog, which has no unit to filter
         on. Say so rather than showing an unfiltered log under a filtered link. -->
    {#if source === 'syslog' && unit}
        <span class="ol-muted">{t('Unit')}: <span class="hmm-mono">{unit}</span> — {t('the busybox log cannot filter by unit')}</span>
        <button class="hmm-button" onclick={() => chooseUnit('')}>{t('Clear filter')}</button>
    {/if}
</div>
{/if}
{#if sinceLinked && ramOnly}
    <!-- the unclean-shutdown warning opens the boot before this one (?boot=-1) where the journal
         still holds it; in RAM it holds this boot alone, so the link opened this boot's log from an
         hour before the marker, and the page says why there is no more (task 104: in the panel) -->
    <div class="ol-notice ol-notice-link" data-notice="earlier-boots">
        <span class="ol-notice-text">{t('Earlier boots need a persistent journal.')}</span>
        <button type="button" class="hmm-button" onclick={() => openSettings('journal')}>{t('Storage settings')}</button>
    </div>
{/if}
{#if copiesUnreadable}
    <div class="ol-notice ol-notice-link" data-notice="copies-unreadable" title={copiesUnreadable.error}>
        <span class="ol-notice-text ol-warn">{t('The journal\'s copies in {path} cannot be read by the system: the lines shown are from RAM only.', {path: copiesUnreadable.path})} {t('On an NFS export that maps root to nobody (root_squash), map root to root (TrueNAS: Maproot User root) or map all users to one account (TrueNAS: Mapall User).')}</span>
        <button type="button" class="hmm-button" onclick={() => openSettings('journal')}>{t('Storage settings')}</button>
    </div>
{/if}
</div>
</div>
<LogSettings bind:open={settingsOpen} bind:tab={settingsTab} />
{#if !lines}
    <div class="lg-state"><Loading {error} /></div>
{:else if error}
    <!-- a filter whose query failed must not pass off the previous lines as its result -->
    <div class="lg-state"><div class="ol-notice error">{error}</div></div>
{:else if lines.length === 0}
    <div class="lg-state ol-muted">{t('No lines.')}</div>
{:else}
    <!-- task 178: the windowed list. The rows in view and a margin are in the DOM between two spacers
         that stand for the rest; at each end a row says what is there - the start of the boot, a page
         loading, or entries unloaded to keep the page small. The browser's own scroll anchoring is off:
         the page keeps the view in place itself when rows are added above it. -->
    {#snippet edge(end: 'old' | 'new')}
        {@const more = end === 'old' ? older : newer}
        {@const loading = end === 'old' ? loadingOlder : loadingNewer}
        {@const unloaded = end === 'old' ? olderUnloaded : newerUnloaded}
        {#if end === 'old' && !more}
            <div class="lg-edge ol-muted" data-edge="start">{since ? t('Start of the time range') : chosenBoot === '' && !kernel ? t('Start of the log') : t('Start of the boot')}</div>
        {:else if more && loading}
            <div class="lg-edge ol-muted" data-edge="loading" aria-busy="true">{end === 'old' ? t('Loading older entries…') : t('Loading newer entries…')}</div>
        {:else if more && unloaded}
            <div class="lg-edge ol-muted" data-edge="unloaded">{end === 'old' ? t('Older entries were unloaded; they load again while scrolling.') : t('Newer entries were unloaded; they load again while scrolling.')}</div>
        {:else if more}
            <div class="lg-edge ol-muted" data-edge="more">{end === 'old' ? t('Older entries load while scrolling.') : t('Newer entries load while scrolling.')}</div>
        {/if}
    {/snippet}
    <div class="ol-log ol-journal lg-box" class:lg-kernel={kernel} bind:this={box} onscroll={onScroll}>
        <div class="lg-edges" bind:this={edgeTop}>{@render edge(newestFirst ? 'new' : 'old')}</div>
        <div class="lg-pad" style:height="{padTop}px" aria-hidden="true"></div>
        <!-- unkeyed on purpose: identical lines are legal in a log, and a row is recycled for the next line -->
        {#each visible as l}
            <div class={`ol-line sev-${l.severity ?? ''}`} class:lg-trace={l.tag === 'rpc-trace'} use:measure={l}>
                <span class="ol-muted ol-ts">{stamp(l)}</span>
                <span class="ol-lvl">{level(l)}</span>
                {#if !kernel}
                    <span class="ol-src" title={l.unit ?? ''}>{l.tag ? `${l.tag}${l.pid ? `[${l.pid}]` : ''}` : (l.unit ?? '')}</span>
                {/if}
                {#if l.tag === 'rpc-trace'}
                    <!-- task 79: a long trace line (a parameter list, a device list) is one line with a
                         triangle that expands it; every trace line has a copy button -->
                    {@const long = l.message.length > TRACE_FOLD}
                    {@const open = traceOpen.has(l)}
                    {#if long}<button type="button" class="lg-trace-toggle" aria-label={open ? t('Collapse') : t('Expand')} aria-expanded={open} onclick={() => toggleTrace(l)}>{open ? '▾' : '▸'}</button>{/if}
                    <span class="ol-msg lg-trace-msg" class:lg-trace-folded={long && !open}>{long && !open ? l.message.slice(0, TRACE_FOLD) + '…' : l.message}</span>
                    <button type="button" class="lg-trace-copy" aria-label={t('Copy line')} title={t('Copy line')} onclick={() => copyTrace(l.message)}>⧉</button>
                {:else}
                    <span class="ol-msg">{l.message}</span>
                {/if}
                {#if reconnects.has(l)}<span class="lg-reconnected ol-muted" class:lg-reconnected-up={newestFirst} data-log-reconnected>{t('reconnected')} · {reconnects.get(l)}</span>{/if}
            </div>
        {/each}
        <div class="lg-pad" style:height="{padBottom}px" aria-hidden="true"></div>
        <div class="lg-edges">{@render edge(newestFirst ? 'old' : 'new')}</div>
    </div>
{/if}

<style>
    /* task 79: the trace lines - the triangle before, the copy button after the message */
    /* task 19: where Follow's stream broke and was opened again - its own line inside the row it follows */
    .ol-journal :global(.ol-line:has(.lg-reconnected)) { flex-wrap: wrap; }
    .lg-reconnected { flex: 0 0 100%; order: 99; text-align: center; font-size: var(--hmm-font-size-small); padding-top: 3px; border-top: 1px dashed var(--hmm-border-muted); }
    .lg-reconnected.lg-reconnected-up { order: -1; padding: 0 0 3px; border-top: 0; border-bottom: 1px dashed var(--hmm-border-muted); }
    .lg-trace-toggle, .lg-trace-copy { background: none; border: 0; color: var(--hmm-fg-muted); cursor: pointer; padding: 0 4px; font: inherit; line-height: inherit; }
    .lg-trace-toggle:hover, .lg-trace-copy:hover { color: var(--hmm-fg); }
    .lg-trace-folded { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

    /* task 93: the boot, the download and the gear in one row above the filters */
    /* task 104: the heading and the panel with the page's side margin; the log below goes edge to edge */
    .lg-top { padding: 14px 16px 10px; }
    /* the heading is the title switcher's (lib/SystemTitle.svelte, task 57), so the rule reaches into it */
    .lg-top :global(h1) { margin-bottom: 10px; }
    /* the filters in a card (task 98's surface): the first row the text filter, growing, with the boot,
       the download and the gear at its right end; the second row the other filters, wrapping */
    .lg-panel { padding: 10px 12px; }
    .lg-head { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 8px; }
    .lg-head > :global(.ol-search) { flex: 1 1 240px; }
    .lg-panel .ol-toolbar { margin: 0; }
    .lg-panel > .ol-notice { margin: 8px 0 0; }
    .lg-filters-toggle { display: inline-flex; align-items: center; gap: 6px; }
    .lg-state { padding: 0 16px 14px; }
    /* task 64: the row's menus - the same hmm-button, an icon before its word and the caret of the
       shell's other menus after it; the menu hangs under it like the power menu's */
    .lg-menu-btn { display: inline-flex; align-items: center; gap: 6px; }
    .lg-settings { padding: 3px 7px; }
    .lg-menu-pop { min-width: 260px; max-width: min(92vw, 320px); }
    .lg-menu-note { margin: 4px 8px 6px; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); white-space: normal; }
    .lg-menu-item { flex-direction: column; align-items: flex-start; gap: 2px; white-space: normal; text-decoration: none; }
    .lg-menu-item[aria-checked='true'] { color: var(--hmm-accent); background: var(--hmm-accent-bg); }
    .lg-menu-label { font-weight: 600; }
    .lg-menu-hint { color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); }
    .lg-menu-item[aria-checked='true'] .lg-menu-hint { color: inherit; }
    /* task 102: the run filter, its id and the button that takes it away */
    .lg-run { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; overflow-wrap: anywhere; }
    .lg-run-clear { padding: 1px 7px; }
    /* task 93: the kernel's chip, pressed in the accent colour */
    .lg-chip[aria-pressed='true'] { color: var(--hmm-accent); background: var(--hmm-accent-bg); border-color: var(--hmm-accent); }
    /* task 44: the journal box takes whatever height the heading, the row above the filters and the
       filter bar leave. The page is a flex column exactly as tall as the viewport on /log (app.css,
       .ol-fill), so `flex: 1` is the rest of the window - the box is the one thing on the page that
       scrolls, and the filter bar stays in view while reading far down.
       `contain: size` is what makes that hold with two thousand lines in the box: the box is sized
       as if it were empty, so its lines are not part of the page's natural height. Without it the
       page (a flex item of the shell whose automatic minimum height is its content) grew to the
       height of every line and scrolled instead of the box; with `min-height: 0` on the page
       instead, the page could shrink below the rows above the box too, and a tall filter bar on a
       phone spilled out of it past its bottom padding. Now the page grows beyond the window only
       when the rows above the box and the box's floor need more room than there is - the floor
       keeps a few lines readable, and only then does the page itself scroll, which beats a box of
       zero height.
       Before this the size was an inline `max-height: 70vh`: a cap, not a height, so the box was as
       tall as its lines up to 70 % of the window whatever sat above it, with an empty band
       underneath on a tall screen and a page that scrolled as well as the box on a short one. */
    /* task 104: edge to edge - no panel around the log, no radius, no side margin: its scroll bar is the
       rightmost thing in the window and its bottom the window's bottom (app.css, .ol-main.ol-fill has no
       padding). A hairline above it where the page meets the log, the log's own surface, and a small
       inner padding so the text does not touch the window's edges. */
    .lg-box {
        flex: 1 1 auto; min-height: 80px; contain: size; overflow: auto; overflow-anchor: none;
        margin: 0; border: 0; border-top: 1px solid var(--hmm-border-muted); border-radius: 0; padding: 6px 16px;
        background: var(--hmm-bg);
    }
    /* task 178: the rows at the two ends of the run, and the spacers for the rows not in the DOM */
    .lg-edge { padding: 6px 0; font-size: var(--hmm-font-size-small); font-style: italic; }
    .lg-pad { flex: none; }
    /* task 93: dmesg's stamp keeps its padding, and err and above stand out of the kernel's noise */
    .lg-kernel .ol-ts { white-space: pre; }
    .lg-kernel :global(.ol-line.sev-err), .lg-kernel :global(.ol-line.sev-crit), .lg-kernel :global(.ol-line.sev-alert), .lg-kernel :global(.ol-line.sev-emerg) {
        background: color-mix(in srgb, var(--hmm-error) 12%, transparent); font-weight: 600;
    }
    /* B-72: on a phone time, level and source kept their widths as columns (app.css .ol-journal)
       and the message got the rest of the line - about ten characters, an sshd line thirty rows
       tall. Below the shell's phone breakpoint the three are a compact head line in the small
       type, the source cut with an ellipsis where it is long, and the message takes the whole
       width under it; a hairline between the entries says where one ends. */
    @media (max-width: 700px) {
        .ol-journal .ol-line { flex-wrap: wrap; gap: 0 8px; padding: 3px 0; border-bottom: 1px solid var(--hmm-border-muted); }
        .ol-journal .ol-line:last-child { border-bottom: 0; }
        .ol-journal .ol-ts, .ol-journal .ol-lvl, .ol-journal .ol-src { font-size: var(--hmm-font-size-small); line-height: 1.5; }
        .ol-journal .ol-lvl { width: auto; }
        .ol-journal .ol-src { flex: 0 1 auto; min-width: 0; max-width: none; }
        .ol-journal .ol-msg { flex: 1 0 100%; }
        /* task 104: the phone's side margin */
        .lg-top { padding: 10px 10px 8px; }
        .lg-box { padding: 4px 10px; }
        .lg-state { padding: 0 10px 10px; }
    }
</style>
