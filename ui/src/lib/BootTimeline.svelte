<script lang="ts">
    import {dateTime, timeOnly} from './datefmt';
    /*
     * Task 93, in the maintainer's layout of 2026-09-12 evening: the boot order graph on the Services
     * page, below the timers - when each unit of this boot began to start and when it was up, the chain
     * multi-user.target waited for, as a chart (plain SVG, D-65) or as a list. GET /boot reads every
     * unit, which takes 3.5 s on a Pi 3, so the section stays collapsed until it is opened (the choice
     * sticks per browser) and `/services#boot` opens it.
     *
     * A click on a unit selects it: its row in the Services tables above is highlighted and scrolled
     * into view, and a panel at the side of the window names its times, what it waited for and what
     * started after it, and links to its log of this boot. The panel stays while the page scrolls.
     *
     * An earlier boot's timeline is there when the box kept it (the last ten, GET /boots marks them),
     * and a boot is compared with the one kept before it: a delta beside each unit in the list, the
     * earlier bars as ghosts in the chart, and the change of the figures.
     */
    import {onMount, tick, untrack} from 'svelte';
    import {api} from './api';
    import {t, i18n} from './i18n.svelte';
    import Loading from './Loading.svelte';
    import Help from './Help.svelte';
    import Icon from './Icon.svelte';
    import {onscreen} from './popover';
    import {CLOSE_MS, reveal} from './reveal';
    import {link} from './router.svelte';
    import {logPath} from './logpage';
    import {chartSVG, compareUnits, deltaKind, deltaLabel, duration, exportName, filterUnits, followers, laterNames, logUnit, restartedAt, seconds, sortUnits, tableRowSelector, tickStep, ticks, timelineEnd, unitEnd, waitedFor, type BootTimeline, type BootUnit, type ListKey, type UnitDelta} from './boottimeline';

    const OPEN_KEY = 'ol.bootTimeline';
    let open = $state(false);
    try {
        open = localStorage.getItem(OPEN_KEY) === 'open';
    } catch {
        /* no storage: closed */
    }
    if (location.hash === '#boot') open = true;
    function toggle() {
        open = !open;
        try {
            localStorage.setItem(OPEN_KEY, open ? 'open' : 'closed');
        } catch {
            /* fine */
        }
    }

    let data = $state<BootTimeline | null>(null);
    let error = $state('');
    let loading = $state(false);
    // which boot: this one (''), or one the box kept when it had finished
    let chosen = $state('');
    let kept = $state<{boot_id: string; first?: string; current: boolean; snapshot?: boolean; total_ms?: number}[]>([]);
    const bootPath = (id: string) => (id ? `/api/system/v1/boot?id=${encodeURIComponent(id)}` : '/api/system/v1/boot');
    async function load() {
        loading = true;
        error = '';
        try {
            data = await api.get<BootTimeline>(bootPath(chosen));
        } catch (e) {
            error = (e as Error).message;
        } finally {
            loading = false;
        }
    }
    async function loadKept() {
        try {
            const r = await api.get<{boots: typeof kept}>('/api/system/v1/boots');
            kept = r.boots.filter((b) => b.snapshot && !b.current);
        } catch {
            kept = [];
        }
    }
    $effect(() => {
        if (open && !data)
            untrack(() => {
                if (loading) return;
                void load();
                void loadKept();
            });
    });
    function chooseBoot(id: string) {
        chosen = id;
        compare = false;
        prev = null;
        selected = null;
        unhighlight();
        void load();
    }

    // ---- the comparison with the boot kept before the one shown ----
    let compare = $state(false);
    let prev = $state<BootTimeline | null>(null);
    let prevError = $state('');
    async function toggleCompare() {
        if (compare) {
            compare = false;
            return;
        }
        const want = data?.previous?.boot_id;
        if (!want) return;
        prevError = '';
        if (prev?.boot_id !== want) {
            try {
                prev = await api.get<BootTimeline>(bootPath(want));
            } catch (e) {
                prevError = (e as Error).message;
                return;
            }
        }
        compare = true;
    }
    const deltas = $derived(compare && prev && data ? compareUnits(data.units, prev.units) : new Map<string, UnitDelta>());

    let section: HTMLElement | undefined = $state();
    // `/services#boot`: the section opens and is scrolled to - and kept in view while the page above
    // it still fills in (the timers arrive after the services, the figures after the section opened),
    // for three seconds or until the reader scrolls or types himself
    let followUntil = 0;
    function stopFollowing() {
        followUntil = 0;
    }
    function followAnchor() {
        if (location.hash !== '#boot') return;
        open = true;
        followUntil = Date.now() + 3000;
        const step = () => {
            if (Date.now() > followUntil) return;
            section?.scrollIntoView({block: 'start'});
            setTimeout(step, 120);
        };
        void tick().then(step);
    }
    onMount(() => {
        followAnchor();
        window.addEventListener('hashchange', followAnchor);
        window.addEventListener('wheel', stopFollowing, {passive: true});
        window.addEventListener('touchstart', stopFollowing, {passive: true});
        window.addEventListener('keydown', stopFollowing);
        return () => {
            stopFollowing();
            window.removeEventListener('hashchange', followAnchor);
            window.removeEventListener('wheel', stopFollowing);
            window.removeEventListener('touchstart', stopFollowing);
            window.removeEventListener('keydown', stopFollowing);
            unhighlight();
        };
    });

    const lang = $derived(i18n.language);
    const phone = typeof matchMedia === 'function' && matchMedia('(max-width: 700px)').matches;
    // the list is the chart's accessible form and a phone's default
    let view = $state<'chart' | 'list'>(phone ? 'list' : 'chart');
    let q = $state('');
    let hideShort = $state(true);
    let onlyServices = $state(false);
    let chain = $state(false);
    const shown = $derived(data ? filterUnits(data, {q, hideShort, onlyServices, chain}) : []);
    const chainSet = $derived(new Set(chain && data ? data.critical_chain : []));
    const byId = $derived(new Map((data?.units ?? []).map((u) => [u.id, u])));
    const inChain = $derived(new Set(data?.critical_chain ?? []));

    // ---- the chart: the names in a column of their own, the axis on top, the bars in one SVG ----
    //
    // Task 113: the chart has no scroll area of its own any anymore ("startup graph not in a panel
    // with own scrollers. instead it should just grow the page and the user scrolls the whole page",
    // maintainer, 2026-09-13). It is two grid rows: the axis row, which is `position: sticky` in the
    // *page's* scroll container - so the scale stays under the top bar while the reader scrolls the
    // whole page - and the body row under it, which is as tall as its units. Only the bars scroll
    // sideways, in `.bt-plot`, and the axis strip is scrolled with it from here: `.bt-axisrow` cannot
    // be inside `.bt-plot`, because a sticky element sticks to its nearest scrolling ancestor, which
    // would then be the strip and not the page. The names keep their column in both rows.
    const ROW = 20;
    const AXIS = 26;
    const NAME_W = phone ? 150 : 240;
    /** the width the bars have: the plot strip's, which the names column and the border leave */
    let boxWidth = $state(800);
    let zoom = $state(1);
    const MAX_ZOOM = 64;
    // the earlier boot's bars count when they reach further
    const end = $derived.by(() => {
        if (!data) return 1;
        let e = timelineEnd(data, shown);
        for (const u of shown) {
            const d = deltas.get(u.id);
            if (d) e = Math.max(e, unitEnd(d.other));
        }
        return e;
    });
    const fitPx = $derived(Math.max(0.2, (boxWidth - 14) / end));
    const pxPerS = $derived(fitPx * zoom);
    const plotW = $derived(Math.ceil(end * pxPerS) + 12);
    const tickList = $derived(ticks(end, tickStep(pxPerS)));
    const milestones = $derived(
        data
            ? [
                  {s: data.timestamps.userspace, label: t('Userspace')},
                  {s: data.timestamps.multi_user, label: 'multi-user.target'},
                  {s: data.timestamps.finish, label: t('Startup finished')},
              ].filter((m): m is {s: number; label: string} => !!m.s)
            : [],
    );
    /** the strip the bars scroll in, and the axis strip that follows it */
    let plotBox: HTMLDivElement | undefined = $state();
    let axisBox: HTMLDivElement | undefined = $state();
    function syncAxis() {
        if (axisBox && plotBox) axisBox.scrollLeft = plotBox.scrollLeft;
    }
    function setZoom(z: number, anchorX?: number) {
        const box = plotBox;
        const before = zoom;
        zoom = Math.min(MAX_ZOOM, Math.max(1, z));
        if (!box || anchorX === undefined || zoom === before) return;
        // the time under the pointer stays under it (anchorX is measured in the strip)
        const target = (anchorX + box.scrollLeft) * (zoom / before) - anchorX;
        void tick().then(() => {
            box.scrollTo({left: Math.max(0, target)});
            syncAxis();
        });
    }
    // Ctrl or ⌘ with the wheel - a trackpad's pinch sends that - and two fingers zoom; the plain wheel
    // scrolls as everywhere. A listener of its own: the wheel has to be cancelable.
    function zoomable(node: HTMLElement) {
        const onWheel = (ev: WheelEvent) => {
            if (!ev.ctrlKey && !ev.metaKey) return;
            ev.preventDefault();
            // the anchor is measured in the plot strip, which is where the bars are
            setZoom(zoom * (ev.deltaY < 0 ? 1.25 : 0.8), ev.clientX - node.getBoundingClientRect().left);
        };
        const touches = new Map<number, number>();
        let startDist = 0;
        let startZoom = 1;
        const spread = () => {
            const [a, b] = [...touches.values()];
            return Math.abs((a ?? 0) - (b ?? 0));
        };
        const down = (ev: PointerEvent) => {
            if (ev.pointerType !== 'touch') return;
            touches.set(ev.pointerId, ev.clientX);
            if (touches.size === 2) {
                startDist = spread();
                startZoom = zoom;
            }
        };
        const move = (ev: PointerEvent) => {
            if (!touches.has(ev.pointerId)) return;
            touches.set(ev.pointerId, ev.clientX);
            if (touches.size === 2 && startDist > 20) setZoom((startZoom * spread()) / startDist);
        };
        const up = (ev: PointerEvent) => {
            touches.delete(ev.pointerId);
            if (touches.size < 2) startDist = 0;
        };
        node.addEventListener('wheel', onWheel, {passive: false});
        node.addEventListener('pointerdown', down);
        node.addEventListener('pointermove', move);
        node.addEventListener('pointerup', up);
        node.addEventListener('pointercancel', up);
        return {
            destroy() {
                node.removeEventListener('wheel', onWheel);
                node.removeEventListener('pointerdown', down);
                node.removeEventListener('pointermove', move);
                node.removeEventListener('pointerup', up);
                node.removeEventListener('pointercancel', up);
            },
        };
    }

    // ---- hover: the exact times at the pointer ----
    let tip = $state<{u: BootUnit; x: number; y: number} | null>(null);
    function hover(ev: MouseEvent, u: BootUnit) {
        tip = {u, x: Math.min(ev.clientX + 14, window.innerWidth - 300), y: ev.clientY + 14};
    }

    // ---- the list ----
    let listKey = $state<ListKey>('start');
    let listAsc = $state(true);
    const listed = $derived(sortUnits(shown, listKey, listAsc));
    function sortBy(k: ListKey) {
        if (listKey === k) listAsc = !listAsc;
        else {
            listKey = k;
            listAsc = k !== 'duration';
        }
    }
    function sortState(k: ListKey): 'ascending' | 'descending' | undefined {
        return listKey === k ? (listAsc ? 'ascending' : 'descending') : undefined;
    }

    // ---- a unit chosen: its row above, and its panel ----
    let selected = $state<BootUnit | null>(null);
    let rowHidden = $state(false);
    let opener: HTMLElement | null = null;
    let highlighted: Element | null = null;
    let panelClose: HTMLButtonElement | undefined = $state();
    function unhighlight() {
        highlighted?.classList.remove('ol-bt-highlight');
        highlighted = null;
    }
    function choose(u: BootUnit, from: HTMLElement | null) {
        selected = u;
        if (from) opener = from;
        unhighlight();
        rowHidden = false;
        const selector = tableRowSelector(u.id);
        if (selector) {
            const row = document.querySelector(selector);
            if (row) {
                row.classList.add('ol-bt-highlight');
                highlighted = row;
                const smooth = !matchMedia('(prefers-reduced-motion: reduce)').matches;
                row.scrollIntoView({block: 'center', behavior: smooth ? 'smooth' : 'auto'});
            } else {
                // a service or a timer the tables above do not show: a filter hides it
                rowHidden = true;
            }
        }
        void tick().then(() => panelClose?.focus({preventScroll: true}));
    }
    function closePanel() {
        selected = null;
        unhighlight();
        opener?.focus({preventScroll: true});
    }
    function onKey(ev: KeyboardEvent) {
        // a dialog above the page answers Escape first
        if (ev.key === 'Escape' && selected && !document.querySelector('.ol-backdrop')) closePanel();
    }

    // ---- export ----
    let exportOpen = $state(false);
    let exportRoot: HTMLDivElement | undefined = $state();
    let exportButton: HTMLButtonElement | undefined = $state();
    $effect(() => {
        if (!exportOpen) return;
        const onKeyDown = (ev: KeyboardEvent) => {
            if (ev.key !== 'Escape') return;
            exportOpen = false;
            exportButton?.focus();
        };
        const onDown = (ev: MouseEvent) => {
            if (exportRoot && !exportRoot.contains(ev.target as Node)) exportOpen = false;
        };
        document.addEventListener('keydown', onKeyDown);
        document.addEventListener('mousedown', onDown);
        return () => {
            document.removeEventListener('keydown', onKeyDown);
            document.removeEventListener('mousedown', onDown);
        };
    });
    // a file made in the page: a blob behind a link that is clicked once and removed
    function save(name: string, type: string, body: string) {
        const url = URL.createObjectURL(new Blob([body], {type}));
        const a = document.createElement('a');
        a.href = url;
        a.download = name;
        document.body.append(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 5000);
    }
    function token(name: string, fallback: string): string {
        return (section && getComputedStyle(section).getPropertyValue(name).trim()) || fallback;
    }
    function exportSVG() {
        exportOpen = false;
        if (!data) return;
        const colors = {
            bg: token('--hmm-bg', '#ffffff'),
            fg: token('--hmm-fg', '#3b3b3b'),
            muted: token('--hmm-fg-muted', '#6d6d6d'),
            grid: token('--hmm-border-muted', '#e6e6e6'),
            activating: token('--hmm-accent', '#0070c1'),
            active: token('--hmm-accent', '#0070c1'),
            failed: token('--hmm-error', '#cd3131'),
            chain: token('--hmm-warn', '#a06800'),
        };
        const svg = chartSVG(data, shown, {pxPerSecond: pxPerS, nameWidth: NAME_W, rowHeight: ROW, chain, colors, title: `${t('Boot timeline')} ${data.boot_id}`});
        save(exportName(data, 'svg'), 'image/svg+xml', svg);
    }
    function exportJSON() {
        exportOpen = false;
        if (data) save(exportName(data, 'json'), 'application/json', `${JSON.stringify(data, null, 2)}\n`);
    }

    function when(iso: string | undefined): string {
        return dateTime(iso, {weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'});
    }
    const upAt = (u: BootUnit) => (u.active !== undefined && u.active >= u.activating ? u.active : undefined);
</script>

<svelte:window onkeydown={onKey} />

<!-- B-153: a unit of the running boot restarted since - its bar is the boot's start, its state of now -->
{#snippet restartMark(u: BootUnit)}
    {#if u.restarted !== undefined && data}{@const at = restartedAt(data, u)}{' '}<span class="ol-badge bt-restarted" data-restarted title={t('Its bar is its start during the boot; it was started again since, and its state is of now.')}>{at ? t('restarted {time}', {time: timeOnly(at, {hour: '2-digit', minute: '2-digit'})}) : t('restarted')}</span>{/if}
{/snippet}

<!-- a figure's change against the boot compared with -->
{#snippet delta(now: number | undefined, before: number | undefined)}
    {#if now !== undefined && before !== undefined}<span class={`bt-deltachip bt-delta ${deltaKind(now - before, before)}`}>{deltaLabel(now - before, lang)}</span>{/if}
{/snippet}

<section class="bt" id="boot" bind:this={section}>
    <h2 class="bt-head">
        <button type="button" class="bt-toggle" aria-expanded={open} aria-controls="bt-body" onclick={toggle}>
            <span class="bt-caret" aria-hidden="true">{open ? '▾' : '▸'}</span>{t('Boot timeline')}
        </button>
        <Help>{t('When each unit of this boot began to start and when it was up, as systemd recorded it. Reading every unit takes a few seconds on a Pi 3, so it is read when this section is opened.')}</Help>
    </h2>
    {#if open}
        <!-- task 98: the section opens and closes in place, like every panel that opens in the page -->
        <div id="bt-body" in:reveal out:reveal={{duration: CLOSE_MS}}>
            {#if !data}
                <Loading {error} />
            {:else}
                {@const s = data.summary}
                {@const p = compare && prev ? prev.summary : null}
                <p class="ol-muted bt-when">
                    {#if data.snapshot}<span class="ol-badge">{t('kept')}</span>{' '}{/if}{data.started ? t('since {time}', {time: when(data.started)}) : ''}{#if data.later > 0}{@const later = laterNames(data)}{data.started ? ' · ' : ''}<span data-later>{#if later.names.length}{later.more ? t('Started after the boot, not shown: {units} and {n} more.', {units: later.names.join(', '), n: later.more}) : t('Started after the boot, not shown: {units}.', {units: later.names.join(', ')})}{:else}{t('{n} units started after the boot are not shown.', {n: data.later})}{/if}</span>{/if}
                </p>
                <div class="bt-cards">
                    <div class="ol-card bt-fig" data-figure="kernel"><div class="k">{t('Kernel')}</div><div class="v">{duration(s.kernel_ms, lang)}{@render delta(s.kernel_ms, p?.kernel_ms)}</div></div>
                    {#if s.initrd_ms !== undefined}<div class="ol-card bt-fig" data-figure="initrd"><div class="k">initrd</div><div class="v">{duration(s.initrd_ms, lang)}</div></div>{/if}
                    <div class="ol-card bt-fig" data-figure="userspace"><div class="k">{t('Userspace')}</div><div class="v">{s.userspace_ms !== undefined ? duration(s.userspace_ms, lang) : t('still starting')}{@render delta(s.userspace_ms, p?.userspace_ms)}</div></div>
                    <div class="ol-card bt-fig" data-figure="total"><div class="k">{t('Startup finished')}</div><div class="v">{s.total_ms !== undefined ? duration(s.total_ms, lang) : '—'}{@render delta(s.total_ms, p?.total_ms)}</div></div>
                    <div class="ol-card bt-fig" data-figure="multi-user"><div class="k">{t('multi-user.target reached')}</div><div class="v">{s.multi_user_ms !== undefined ? duration(s.multi_user_ms, lang) : '—'}{@render delta(s.multi_user_ms, p?.multi_user_ms)}</div></div>
                    <div class="ol-card bt-fig" data-figure="slowest">
                        <div class="k">{t('Slowest unit')}</div>
                        {#if s.slowest}
                            <div class="v"><button type="button" class="bt-link hmm-mono" onclick={(ev) => byId.get(s.slowest!.id) && choose(byId.get(s.slowest!.id)!, ev.currentTarget)}>{s.slowest.id}</button></div>
                            <div class="ol-card-detail">{duration(s.slowest.duration_ms, lang)}</div>
                        {:else}<div class="v">—</div>{/if}
                    </div>
                </div>

                <div class="ol-toolbar bt-tools">
                    {#if kept.length > 0}
                        <select class="hmm-select" aria-label={t('Boot')} value={chosen} onchange={(e) => chooseBoot(e.currentTarget.value)}>
                            <option value="">{t('Boot')}: {t('this boot')}</option>
                            {#each kept as k (k.boot_id)}
                                <option value={k.boot_id}>{t('Boot')}: {when(k.first) || k.boot_id.slice(0, 8)}{k.total_ms !== undefined ? ` · ${duration(k.total_ms, lang)}` : ''}</option>
                            {/each}
                        </select>
                    {/if}
                    <button type="button" class="hmm-button bt-chip" aria-pressed={compare} disabled={!data.previous} title={data.previous ? undefined : t('No earlier boot is kept yet.')} onclick={toggleCompare}>{t('Compare with the previous boot')}</button>
                    <input class="hmm-input" type="search" placeholder={t('Search units')} aria-label={t('Search units')} bind:value={q} />
                    <label><input type="checkbox" bind:checked={hideShort} /> {t('Hide units under 10 ms')}</label>
                    <label><input type="checkbox" bind:checked={onlyServices} /> {t('Only services')}</label>
                    <button type="button" class="hmm-button bt-chip" aria-pressed={chain} disabled={data.critical_chain.length === 0} onclick={() => (chain = !chain)}>{t('Critical chain')}</button>
                    <select class="hmm-select" bind:value={view} aria-label={t('View')}>
                        <option value="chart">{t('View')}: {t('chart')}</option>
                        <option value="list">{t('View')}: {t('list')}</option>
                    </select>
                    {#if view === 'chart'}
                        <span class="bt-zoom">
                            <button type="button" class="hmm-button" aria-label={t('Zoom out')} title={t('Zoom out')} disabled={zoom <= 1} onclick={() => setZoom(zoom / 1.5)}>−</button>
                            <button type="button" class="hmm-button" aria-label={t('Fit to width')} title={t('Fit to width')} disabled={zoom === 1} onclick={() => setZoom(1)}>{t('Fit')}</button>
                            <button type="button" class="hmm-button" aria-label={t('Zoom in')} title={t('Zoom in')} disabled={zoom >= MAX_ZOOM} onclick={() => setZoom(zoom * 1.5)}>+</button>
                        </span>
                    {/if}
                    <div class="ol-menu" bind:this={exportRoot}>
                        <button type="button" class="hmm-button bt-menubtn" bind:this={exportButton} aria-haspopup="menu" aria-expanded={exportOpen} onclick={() => (exportOpen = !exportOpen)}>
                            <Icon name="download" size={14} /><span>{t('Export')}</span><span class="ol-caret" aria-hidden="true">▾</span>
                        </button>
                        {#if exportOpen}
                            <div class="ol-menupop" role="menu" aria-label={t('Export')} use:onscreen>
                                <button type="button" role="menuitem" class="ol-menuitem" data-format="svg" onclick={exportSVG}>{t('Chart as SVG')}</button>
                                <button type="button" role="menuitem" class="ol-menuitem" data-format="json" onclick={exportJSON}>{t('Data as JSON')}</button>
                            </div>
                        {/if}
                    </div>
                    <button type="button" class="hmm-button" onclick={load} disabled={loading}>{t('Refresh')}</button>
                    <span class="ol-muted bt-count">{t('{n} of {total} units', {n: shown.length, total: data.units.length})}</span>
                </div>

                {#if error}<div class="ol-notice error">{error}</div>{/if}
                {#if prevError}<div class="ol-notice error">{t('The previous boot could not be read: {error}', {error: prevError})}</div>{/if}
                {#if shown.length === 0}
                    <div class="ol-muted">{t('Nothing matches.')}</div>
                {:else if view === 'chart'}
                    <!-- task 113: no scroll area of its own - the section is as tall as its units and
                         the page scrolls. The axis row sticks to the top of the page's scrolling box
                         (under the top bar), the body row grows, and only the bars scroll sideways. -->
                    <div class="bt-chart" style:--bt-name-w={`${NAME_W}px`}>
                        <div class="bt-axisrow">
                            <div class="bt-corner" style:height={`${AXIS}px`}>{t('Unit')}</div>
                            <div class="bt-axisstrip" bind:this={axisBox}>
                                <svg class="bt-axis" width={plotW} height={AXIS} aria-hidden="true">
                                    {#each tickList as s (s)}
                                        <line class="bt-tick" x1={s * pxPerS} y1={AXIS - 6} x2={s * pxPerS} y2={AXIS} />
                                        <text class="bt-ticklabel" x={s * pxPerS + 3} y={AXIS - 10}>{s} s</text>
                                    {/each}
                                </svg>
                            </div>
                        </div>
                        <div class="bt-body">
                            <div class="bt-names">
                                {#each shown as u (u.id)}
                                    <button type="button" class="bt-name hmm-mono" class:dim={chain && !chainSet.has(u.id)} class:sel={selected?.id === u.id} title={u.description ?? u.id} onclick={(ev) => choose(u, ev.currentTarget)}>{u.id}</button>
                                {/each}
                            </div>
                            <!-- svelte-ignore a11y_no_static_element_interactions -->
                            <div class="bt-plot" bind:this={plotBox} bind:clientWidth={boxWidth} use:zoomable onscroll={syncAxis}>
                            <svg class="bt-bars" width={plotW} height={shown.length * ROW} role="img" aria-label={t('Boot timeline')}>
                                <defs>
                                    <pattern id="bt-stripes" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
                                        <rect class="bt-stripe-a" width="6" height="6" />
                                        <rect class="bt-stripe-b" width="3" height="6" />
                                    </pattern>
                                </defs>
                                {#each tickList as s (s)}<line class="bt-gridline" x1={s * pxPerS} y1="0" x2={s * pxPerS} y2={shown.length * ROW} />{/each}
                                {#each milestones as m (m.label)}<line class="bt-milestone" x1={m.s * pxPerS} y1="0" x2={m.s * pxPerS} y2={shown.length * ROW}><title>{m.label}: {seconds(m.s, lang)}</title></line>{/each}
                                {#each shown as u, i (u.id)}
                                    {@const up = upAt(u)}
                                    {@const ghost = deltas.get(u.id)}
                                    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
                                    <g
                                        class="bt-row"
                                        class:dim={chain && !chainSet.has(u.id)}
                                        class:chain={chainSet.has(u.id)}
                                        class:failed={u.state === 'failed'}
                                        class:starting={u.state === 'activating'}
                                        class:sel={selected?.id === u.id}
                                        data-unit={u.id}
                                        onclick={() => choose(u, null)}
                                        onmousemove={(ev) => hover(ev, u)}
                                        onmouseleave={() => (tip = null)}
                                    >
                                        <rect class="bt-hit" x="0" y={i * ROW} width={plotW} height={ROW} />
                                        {#if ghost}<rect class="bt-ghost" x={ghost.other.activating * pxPerS} y={i * ROW + 2} width={Math.max(1, (unitEnd(ghost.other) - ghost.other.activating) * pxPerS)} height={ROW - 4} rx="2" />{/if}
                                        <rect class="bt-span" x={u.activating * pxPerS} y={i * ROW + 5} width={Math.max(1, (unitEnd(u) - u.activating) * pxPerS)} height={ROW - 10} rx="2" />
                                        {#if up !== undefined}<rect class="bt-up" x={up * pxPerS - 1} y={i * ROW + 3} width="2" height={ROW - 6} />{/if}
                                    </g>
                                {/each}
                            </svg>
                            </div>
                        </div>
                    </div>
                {:else}
                    <div class="bt-listbox">
                        <table class="ol-table bt-list">
                            <thead>
                                <tr>
                                    <th aria-sort={sortState('start')}><button type="button" class="bt-sort" onclick={() => sortBy('start')}>{t('Began')}{listKey === 'start' ? (listAsc ? ' ▲' : ' ▼') : ''}</button></th>
                                    <th aria-sort={sortState('duration')}><button type="button" class="bt-sort" onclick={() => sortBy('duration')}>{t('Took')}{listKey === 'duration' ? (listAsc ? ' ▲' : ' ▼') : ''}</button></th>
                                    <th aria-sort={sortState('unit')}><button type="button" class="bt-sort" onclick={() => sortBy('unit')}>{t('Unit')}{listKey === 'unit' ? (listAsc ? ' ▲' : ' ▼') : ''}</button></th>
                                    <th aria-sort={sortState('state')}><button type="button" class="bt-sort" onclick={() => sortBy('state')}>{t('State')}{listKey === 'state' ? (listAsc ? ' ▲' : ' ▼') : ''}</button></th>
                                    {#if compare && prev}<th class="bt-num">{t('Δ began')}</th><th class="bt-num">{t('Δ took')}</th>{/if}
                                </tr>
                            </thead>
                            <tbody>
                                {#each listed as u (u.id)}
                                    {@const d = deltas.get(u.id)}
                                    <tr class:dim={chain && !chainSet.has(u.id)} class:chain={chainSet.has(u.id)} class:sel={selected?.id === u.id} data-unit={u.id}>
                                        <td class="hmm-mono bt-num">{seconds(u.activating, lang)}</td>
                                        <td class="hmm-mono bt-num">{duration(u.duration_ms, lang)}</td>
                                        <td class="bt-unit"><button type="button" class="bt-link hmm-mono" onclick={(ev) => choose(u, ev.currentTarget)}>{u.id}</button>{#if u.description}<span class="bt-sub ol-muted">{u.description}</span>{/if}</td>
                                        <td class:ol-warn={u.state === 'failed'}>{u.state}{@render restartMark(u)}</td>
                                        {#if compare && prev}
                                            <td class={`hmm-mono bt-num bt-delta ${d ? deltaKind(d.startMs, 0) : ''}`}>{d ? deltaLabel(d.startMs, lang) : '—'}</td>
                                            <td class={`hmm-mono bt-num bt-delta ${d ? deltaKind(d.tookMs, d.other.duration_ms) : ''}`}>{d ? deltaLabel(d.tookMs, lang) : '—'}</td>
                                        {/if}
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {/if}
            {/if}
        </div>
    {/if}
</section>

{#if tip}
    {@const up = upAt(tip.u)}
    <div class="ol-card bt-tip" role="tooltip" style:left={`${tip.x}px`} style:top={`${tip.y}px`}>
        <div class="hmm-mono bt-tip-id">{tip.u.id}</div>
        <div>{t('Began to start')}: {seconds(tip.u.activating, lang)}</div>
        {#if up !== undefined}<div>{t('Up')}: {seconds(up, lang)}</div>{/if}
        <div>{t('Took')}: {duration(tip.u.duration_ms, lang)}</div>
        {#if deltas.get(tip.u.id)}<div>{t('Against the previous boot')}: {deltaLabel(deltas.get(tip.u.id)!.tookMs, lang)}</div>{/if}
        {#if waitedFor(tip.u, byId).length}<div class="ol-muted">{t('Waited for')}: {waitedFor(tip.u, byId).slice(0, 3).map((w) => w.id).join(', ')}</div>{/if}
    </div>
{/if}

{#if selected && data}
    {@const up = upAt(selected)}
    {@const waited = waitedFor(selected, byId)}
    {@const after = followers(selected.id, data.units)}
    <aside class="ol-card bt-panel" aria-labelledby="bt-panel-title">
        <div class="bt-panel-head">
            <h3 id="bt-panel-title" class="hmm-mono">{selected.id}</h3>
            <button type="button" class="ol-modal-close" aria-label={t('Close')} title={t('Close')} bind:this={panelClose} onclick={closePanel}><Icon name="x" size={16} /></button>
        </div>
        {#if selected.description}<p class="ol-muted bt-panel-desc">{selected.description}</p>{/if}
        {#if inChain.has(selected.id)}<p><span class="ol-badge">{t('In the critical chain')}</span></p>{/if}
        <dl class="bt-facts">
            <div><dt>{t('Began to start')}</dt><dd class="hmm-mono">{seconds(selected.activating, lang)}</dd></div>
            {#if up !== undefined}<div><dt>{t('Up')}</dt><dd class="hmm-mono">{seconds(up, lang)}</dd></div>{/if}
            {#if selected.inactive !== undefined && selected.inactive >= selected.activating}<div><dt>{t('Ended')}</dt><dd class="hmm-mono">{seconds(selected.inactive, lang)}</dd></div>{/if}
            <div><dt>{t('Took')}</dt><dd class="hmm-mono">{duration(selected.duration_ms, lang)}</dd></div>
            {#if deltas.get(selected.id)}{@const d = deltas.get(selected.id)!}<div><dt>{t('Previous boot')}</dt><dd class="hmm-mono">{duration(d.other.duration_ms, lang)} ({deltaLabel(d.tookMs, lang)})</dd></div>{/if}
            <div><dt>{t('State')}</dt><dd class:ol-warn={selected.state === 'failed'}>{selected.state}{selected.sub ? ` (${selected.sub})` : ''}{@render restartMark(selected)}</dd></div>
        </dl>
        {#if rowHidden}<p class="ol-muted">{t('Not in the tables above: a filter hides it.')}</p>{/if}
        {#if waited.length}
            <h4>{t('Waited for')}</h4>
            <ul class="bt-rel">
                {#each waited.slice(0, 8) as w (w.id)}<li><button type="button" class="bt-link hmm-mono" onclick={() => choose(w, null)}>{w.id}</button> <span class="ol-muted">{seconds(unitEnd(w), lang)}</span></li>{/each}
            </ul>
        {/if}
        {#if after.length}
            <h4>{t('Started after it')}</h4>
            <ul class="bt-rel">
                {#each after.slice(0, 8) as w (w.id)}<li><button type="button" class="bt-link hmm-mono" onclick={() => choose(w, null)}>{w.id}</button> <span class="ol-muted">{seconds(w.activating, lang)}</span></li>{/each}
            </ul>
        {/if}
        <div class="bt-panel-actions">
            <a class="hmm-button" href={logPath({unit: logUnit(selected.id), boot: data.boot_id})} use:link>{t('Log of this unit in this boot')}</a>
        </div>
    </aside>
{/if}

<style>
    .bt { margin-top: 28px; }
    .bt-head { display: flex; align-items: center; gap: 4px; }
    .bt-toggle { display: inline-flex; align-items: center; gap: 6px; padding: 0; border: 0; background: none; color: inherit; font: inherit; cursor: pointer; }
    .bt-caret { display: inline-block; width: 1em; color: var(--hmm-fg-muted); }
    .bt-when { margin: 0 0 10px; }
    /* the figures of systemd-analyze time, one card each, wrapping on a phone */
    .bt-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(170px, 100%), 1fr)); gap: 10px; margin-bottom: 12px; }
    .bt-fig .v { font-size: 15px; font-weight: 600; }
    .bt-tools { margin-top: 4px; }
    .bt-zoom { display: inline-flex; gap: 2px; }
    .bt-menubtn { display: inline-flex; align-items: center; gap: 6px; }
    .bt-chip[aria-pressed='true'] { color: var(--hmm-accent); background: var(--hmm-accent-bg); border-color: var(--hmm-accent); }
    .bt-link { padding: 0; border: 0; background: none; color: var(--hmm-link); font: inherit; text-align: left; cursor: pointer; overflow-wrap: anywhere; }
    .bt-link:hover { text-decoration: underline; }
    /*
     * Task 113: the chart grows to its units and the *page* scrolls - it had a box of its own with a
     * vertical scroll bar inside the page ("startup graph not in a panel with own scrollers. instead
     * it should just grow the page and the user scrolls the whole page"). Two grid rows over the same
     * two columns, the names' and the plot's:
     *
     *   .bt-axisrow   .bt-corner | .bt-axisstrip > svg.bt-axis     sticky at the top of the page
     *   .bt-body      .bt-names  | .bt-plot      > svg.bt-bars     as tall as the units
     *
     * `.bt-axisrow` is sticky in the page's scroll container (B-129: `.ol-scrollport`, which begins at
     * the top bar's lower edge, so `top: 0` is directly under the bar). That is why the axis is not
     * inside `.bt-plot`: a sticky element sticks to its nearest *scrolling* ancestor, and inside the
     * strip it would stick to the strip and scroll off the screen with the page. The strip is scrolled
     * from the script instead, with the bars (`syncAxis`).
     *
     * `overflow-y: clip` rather than the default `visible` on the two strips: a used `overflow-x: auto`
     * turns a `visible` overflow-y into `auto`, and the section would have a vertical scroll bar again
     * the moment anything inside it were taller than the strip. `clip` keeps the pair honest; neither
     * strip is ever taller than its content, so it clips nothing.
     */
    .bt-chart {
        position: relative;
        border: 1px solid var(--hmm-border-muted); border-radius: var(--hmm-radius); background: var(--hmm-bg);
    }
    .bt-axisrow {
        position: sticky; top: 0; z-index: 3; display: grid; grid-template-columns: var(--bt-name-w) minmax(0, 1fr);
        background: var(--hmm-bg); border-bottom: 1px solid var(--hmm-border-muted);
    }
    .bt-body { display: grid; grid-template-columns: var(--bt-name-w) minmax(0, 1fr); }
    .bt-corner {
        display: flex; align-items: center; padding: 0 8px;
        border-right: 1px solid var(--hmm-border-muted);
        font-size: var(--hmm-font-size-small); color: var(--hmm-fg-muted);
    }
    .bt-axisstrip { min-width: 0; overflow-x: hidden; overflow-y: clip; }
    .bt-axis { display: block; }
    .bt-plot { min-width: 0; overflow-x: auto; overflow-y: clip; touch-action: pan-x pan-y; }
    .bt-names { background: var(--hmm-bg); border-right: 1px solid var(--hmm-border-muted); }
    .bt-name {
        display: block; width: 100%; height: 20px; padding: 0 8px; border: 0; background: none; color: var(--hmm-fg);
        font-size: var(--hmm-font-size-grid); line-height: 20px; text-align: left; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: pointer;
    }
    .bt-name:hover { background: var(--hmm-row-hover); }
    .bt-name.sel { background: var(--hmm-accent-bg); color: var(--hmm-accent); }
    .bt-name.dim { opacity: 0.35; }
    .bt-bars { display: block; }
    .bt-tick, .bt-gridline { stroke: var(--hmm-border-muted); stroke-width: 1; }
    .bt-ticklabel { fill: var(--hmm-fg-muted); font-size: 11px; }
    .bt-milestone { stroke: var(--hmm-fg-muted); stroke-width: 1; stroke-dasharray: 4 3; }
    .bt-hit { fill: transparent; }
    .bt-row { cursor: pointer; }
    .bt-row:hover .bt-hit { fill: var(--hmm-row-hover); }
    .bt-row.sel .bt-hit { fill: var(--hmm-accent-bg); }
    .bt-span { fill: var(--hmm-accent); fill-opacity: 0.4; }
    .bt-up { fill: var(--hmm-accent); }
    .bt-row.chain .bt-span { fill: var(--hmm-warn); fill-opacity: 1; }
    .bt-row.failed .bt-span { fill: var(--hmm-error); fill-opacity: 1; }
    .bt-row.starting .bt-span { fill: url(#bt-stripes); fill-opacity: 1; }
    .bt-stripe-a { fill: var(--hmm-accent-bg); }
    .bt-stripe-b { fill: var(--hmm-accent); }
    .bt-row.dim { opacity: 0.28; }
    /* the boot compared with: its bar as an outline behind this boot's, the deltas coloured */
    .bt-ghost { fill: none; stroke: var(--hmm-fg-muted); stroke-width: 1; stroke-dasharray: 3 2; }
    .bt-deltachip { display: inline-block; margin-left: 8px; font-size: var(--hmm-font-size-small); font-weight: 600; }
    .bt-delta.worse { color: var(--hmm-error); }
    .bt-delta.better { color: var(--hmm-ok); }
    /* task 113: the list grows with the page too - it scrolls sideways at most, never down */
    .bt-listbox { overflow-x: auto; overflow-y: clip; }
    .bt-list { width: 100%; }
    .bt-sort { padding: 0; border: 0; background: none; color: inherit; font: inherit; font-weight: 600; cursor: pointer; white-space: nowrap; }
    .bt-num { white-space: nowrap; text-align: right; }
    .bt-unit { min-width: 0; }
    .bt-sub { display: block; font-size: var(--hmm-font-size-small); }
    .bt-list tr.chain td { background: color-mix(in srgb, var(--hmm-warn) 14%, transparent); }
    .bt-list tr.dim td { opacity: 0.45; }
    .bt-list tr.sel td { background: var(--hmm-accent-bg); }
    .bt-tip { position: fixed; z-index: 60; pointer-events: none; max-width: 300px; padding: 8px 10px; font-size: var(--hmm-font-size-small); line-height: 1.5; }
    .bt-tip-id { font-weight: 600; margin-bottom: 2px; overflow-wrap: anywhere; }
    /* the unit's panel stays at the side of the window while the page scrolls to its row - below the
       sticky top bar whatever its height (task 99; 72 px on a desktop, as before) */
    .bt-panel {
        position: fixed; z-index: 50; right: 16px; top: calc(var(--ol-header-h, 36px) + 36px); width: min(360px, calc(100vw - 32px));
        max-height: calc(100dvh - var(--ol-header-h, 36px) - 60px); overflow: auto;
        box-shadow: 0 12px 40px rgba(0, 0, 0, 0.3);
    }
    .bt-panel-head { display: flex; align-items: flex-start; gap: 8px; }
    .bt-panel-head h3 { flex: 1 1 auto; margin: 0; overflow-wrap: anywhere; }
    .bt-panel-desc { margin: 4px 0 8px; }
    .bt-panel h4 { margin: 12px 0 4px; font-size: var(--hmm-font-size); }
    .bt-facts { display: grid; grid-template-columns: auto 1fr; gap: 2px 12px; margin: 8px 0; }
    .bt-facts > div { display: contents; }
    .bt-facts dt { color: var(--hmm-fg-muted); }
    .bt-facts dd { margin: 0; }
    .bt-rel { margin: 0; padding-left: 18px; }
    .bt-panel-actions { margin-top: 12px; }
    .bt-panel-actions a { text-decoration: none; color: var(--hmm-fg); display: inline-block; }
    @media (max-width: 700px) {
        .bt-panel { top: auto; bottom: 10px; right: 16px; max-height: 55dvh; }
    }
    /* the row of the Services tables a chosen unit stands in */
    :global(tr.ol-bt-highlight > td) { background: var(--hmm-accent-bg); box-shadow: inset 0 1px 0 var(--hmm-accent), inset 0 -1px 0 var(--hmm-accent); }
</style>
