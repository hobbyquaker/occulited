<script lang="ts">
    /*
     * The history graph (task 53): a line over time with the two things the old sparkline
     * lacked - a time axis (the start time at the left, the end time at the right, the day in
     * front of each when the span crosses midnight) and a top tick that says what the top of
     * the band means. The band is scaled to the graph's own peak rounded up to a readable step
     * (history.ts: 1, 2, 5, 10, 20, 50, 100 %), so a quiet house is not a flat line pressed
     * against the floor, and the tick label is what keeps that honest: the reader can never take
     * the height of the curve for a share of the budget. The ring beside it is the figure
     * against the budget.
     *
     * Drawn at its real pixel width (the wrapper's clientWidth), so the stroke and the text keep
     * their size - a `preserveAspectRatio="none"` viewBox stretched both. Hover or tap shows the
     * time and value of the nearest sample, because a line without values invites guessing. A
     * sample at which the interface was down, or a hole in time (a reboot), breaks the line
     * instead of drawing a ramp across it.
     *
     * The span is the last hour by default; the selector offers 6 h and 24 h. The sampler keeps 500
     * samples per interface (task 214, about 8 h), so 24 h shows all that is kept. Used by the Status
     * page and by the Interfaces page (task 54).
     */
    import {i18n, t} from './i18n.svelte';
    import {clock, niceTop, segments, sliceSpan, timeLabels, type Sample} from './history';

    interface Props {
        /** the whole series, oldest first; the chart takes the span it shows out of it */
        samples: Sample[];
        /** what the line is, for the screen reader */
        label: string;
        /** the unit written after a value */
        unit?: string;
        /** the spans offered, in minutes; one entry hides the selector */
        spans?: number[];
        /** the span shown, in minutes */
        span?: number;
        /** the height of the band in pixels */
        height?: number;
        /** a threshold drawn as a dashed line in the error colour (task 151: carrier sense is bad above 10 %); the band always reaches it */
        mark?: number;
        /** the readable steps of the top tick (history.ts STEPS for per cent, RATE_STEPS for a rate) */
        steps?: number[];
        /** how a value is written in the tick and under the pointer; the number as it is by default */
        format?: (v: number) => string;
    }
    let {samples, label, unit = '%', spans = [60, 360, 1440], span = $bindable(60), height = 64, mark, steps, format = (v: number) => String(v)}: Props = $props();

    let width = $state(0);
    // room above the top gridline for the stroke, and a pixel above the baseline for it too
    const PAD_TOP = 4, PAD_BOTTOM = 1;

    const shown = $derived(sliceSpan(samples, span));
    // with a mark, the band reaches it, so a quiet line is read against the threshold
    const top = $derived(niceTop(Math.max(0, ...shown.map((s) => s.v)), steps));
    const topShown = $derived(mark !== undefined && mark > top ? mark : top);
    const t0 = $derived(shown.length ? Date.parse(shown[0]!.t) : 0);
    const t1 = $derived(shown.length ? Date.parse(shown[shown.length - 1]!.t) : 0);
    const x = (s: Sample) => (t1 > t0 ? ((Date.parse(s.t) - t0) / (t1 - t0)) * width : width / 2);
    const y = (v: number) => height - PAD_BOTTOM - (Math.max(0, Math.min(topShown, v)) / topShown) * (height - PAD_TOP - PAD_BOTTOM);
    const runs = $derived(segments(shown).map((run) => {
        const pts = run.map((s) => `${x(s).toFixed(1)},${y(s.v).toFixed(1)}`).join(' ');
        return {
            pts,
            // the wash under the line closes on the baseline below the run's own ends, so a gap
            // stays a gap in the wash as well
            area: `${x(run[0]!).toFixed(1)},${height} ${pts} ${x(run[run.length - 1]!).toFixed(1)},${height}`,
            lone: run.length === 1 ? run[0]! : null,
        };
    }));
    const axis = $derived(shown.length ? timeLabels(new Date(t0), new Date(t1), i18n.language) : null);
    const spanLabel = (m: number) => (m % 1440 === 0 && m >= 2880 ? t('{n} d', {n: m / 1440}) : m % 60 === 0 ? t('{n} h', {n: m / 60}) : t('{n} min', {n: m}));

    // the sample under the pointer: the nearest by x, shown with a marker and its time · value
    let hover = $state<Sample | null>(null);
    function pick(ev: PointerEvent) {
        const box = (ev.currentTarget as SVGSVGElement).getBoundingClientRect();
        const px = ev.clientX - box.left;
        let best: Sample | null = null, bd = Infinity;
        for (const s of shown) {
            if (s.up === false) continue;
            const d = Math.abs(x(s) - px);
            if (d < bd) { bd = d; best = s; }
        }
        hover = best;
    }
    // a mouse leaving clears the marker; a finger lifting keeps it where it tapped
    function leave(ev: PointerEvent) {
        if (ev.pointerType === 'mouse') hover = null;
    }
    function choose(m: number) {
        span = m;
        // the marked sample may not be in the new span
        hover = null;
    }
    const hoverText = $derived(hover ? `${clock(new Date(hover.t), i18n.language)} · ${format(hover.v)} ${unit}` : '');
</script>

<div class="chart" bind:clientWidth={width}>
    {#if width > 0 && shown.length}
        <svg {width} {height} viewBox="0 0 {width} {height}" role="img" aria-label={label}
             onpointermove={pick} onpointerdown={pick} onpointerleave={leave}>
            <!-- the top tick and its gridline, the 0 baseline -->
            <line class="grid" x1="0" x2={width} y1={y(topShown) + 0.5} y2={y(topShown) + 0.5} />
            {#if mark !== undefined}<line class="mark" x1="0" x2={width} y1={y(mark) + 0.5} y2={y(mark) + 0.5} />{/if}
            <line class="base" x1="0" x2={width} y1={height - 0.5} y2={height - 0.5} />
            {#each runs as run, i (i)}
                {#if run.lone}
                    <circle class="dot" cx={x(run.lone)} cy={y(run.lone.v)} r="2" />
                {:else}
                    <polygon class="wash" points={run.area} />
                    <polyline class="line" points={run.pts} />
                {/if}
            {/each}
            <!-- after the line, with a halo in the card's colour, so a curve at its peak does
                 not run through the label -->
            <text class="tick" x="2" y={y(topShown) + 12}>{format(topShown)} {unit}</text>
            {#if mark !== undefined && mark < topShown}<text class="tick marktick" x={width - 2} y={y(mark) - 3} text-anchor="end">{format(mark)} {unit}</text>{/if}
            {#if hover}
                {@const hx = x(hover)}
                {@const left = hx < width / 2}
                <line class="cursor" x1={hx} x2={hx} y1={PAD_TOP} y2={height} />
                <circle class="marker" cx={hx} cy={y(hover.v)} r="3.5" />
                <text class="value" x={left ? hx + 6 : hx - 6} y={Math.max(PAD_TOP + 12, Math.min(height - 4, y(hover.v) - 6))} text-anchor={left ? 'start' : 'end'}>{hoverText}</text>
            {/if}
        </svg>
    {:else}
        <div class="empty" style={`height:${height}px`}>{t('no history yet')}</div>
    {/if}
    <div class="axis">
        <span class="time start">{axis?.start ?? ''}</span>
        {#if spans.length > 1}
            <span class="spans" role="group" aria-label={t('Span')}>
                {#each spans as m (m)}
                    <button type="button" class:on={m === span} onclick={() => choose(m)} aria-pressed={m === span}>{spanLabel(m)}</button>
                {/each}
            </span>
        {/if}
        <span class="time end">{axis?.end ?? ''}</span>
    </div>
</div>

<style>
    .chart { width: 100%; min-width: 0; }
    /* occulited B-74: clipped to its own box - whatever the series holds, nothing is drawn over the
       next panel (the ticks and the hover label are placed inside) */
    svg { display: block; overflow: hidden; touch-action: pan-y; cursor: crosshair; }
    .grid { stroke: var(--hmm-border); stroke-dasharray: 2 3; }
    .base { stroke: var(--hmm-border); }
    .mark { stroke: var(--hmm-error); stroke-dasharray: 4 3; opacity: 0.8; }
    .marktick { fill: var(--hmm-error); }
    .tick, .value {
        font-size: var(--hmm-font-size-small); fill: var(--hmm-fg-muted); pointer-events: none;
        paint-order: stroke; stroke: var(--hmm-card-bg); stroke-width: 3px; stroke-linejoin: round;
    }
    .value { fill: var(--hmm-fg); font-weight: 600; }
    .wash { fill: var(--hmm-accent); opacity: 0.12; stroke: none; }
    .line { fill: none; stroke: var(--hmm-accent); stroke-width: 1.5; stroke-linejoin: round; stroke-linecap: round; }
    .dot { fill: var(--hmm-accent); }
    .cursor { stroke: var(--hmm-border-strong); }
    .marker { fill: var(--hmm-card-bg); stroke: var(--hmm-accent); stroke-width: 2; }
    .empty { display: flex; align-items: center; justify-content: center; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); }
    .axis { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-top: 3px; font-size: var(--hmm-font-size-small); color: var(--hmm-fg-muted); }
    .time { font-variant-numeric: tabular-nums; white-space: nowrap; }
    .spans { display: inline-flex; gap: 2px; }
    .spans button {
        border: 0; background: none; color: var(--hmm-fg-muted); font: inherit; cursor: pointer;
        padding: 0 5px; border-radius: var(--hmm-radius); line-height: 16px;
    }
    .spans button:hover { color: var(--hmm-fg); background: var(--hmm-control-bg-hover); }
    .spans button.on { color: var(--hmm-accent); background: var(--hmm-accent-bg); }
</style>
