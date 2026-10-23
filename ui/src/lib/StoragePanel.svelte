<script lang="ts">
    /*
     * occulited task 78 (the maintainer, 2026-10-16): one Status panel per disk - the SD card, the
     * eMMC, the VM's disk, every USB stick - with a ring per partition, concentric, the userfs
     * outermost, then the system and the boot partition, each in its own hue (the --ol-series
     * tokens, validated for both themes). Beside the rings a legend says each one in words and
     * figures, so no reading depends on a colour: the name, "x of y GB" and the percentage. The
     * device's own size is in the head. Decimal units in this panel alone (1 GB = 10^9 bytes).
     *
     * A ring is a meter like the Gauge's - one value against one limit - so each has its track in
     * a light step of its own hue. The panel's edge goes amber or red by the fullest writable
     * partition; a read-only one (the system, the boot partition) is full by design.
     *
     * Task 111 (the maintainer's variant B, 2026-10-23): the disk's health lives here too, not in a
     * panel of its own - a verdict pill in the head, one row of chips (wear, SMART, write load,
     * errors: each only when the disk reports it), its reasons in one line, and the full facts
     * behind "Details", whose open state every disk panel shares (lib/viewstate). Closed, the panel
     * is as high as the Utilisation grid's row (StatusPage's `sp-rows`); the edge is the worse of
     * the verdict and the fullness.
     */
    import Icon from './Icon.svelte';
    import {i18n, t} from './i18n.svelte';
    import type {StorageDevice, StorageReason} from './api';
    import {formatBytes} from './netpanels';
    import {barWidth, detailRows, emmcRange, healthChips, madeMonth, panelEdge, verdictClass, wearLevel, withoutDevice, type HealthChip} from './storage';
    import {MAX_RINGS, deviceLevel, deviceModel, formatDecimal, partitionLevel, ringColours, unallocatedShown, usedOfTotal, usedPct, type StatusDevice, type StatusPartition} from './storagepanels';
    import {view, setStorageOpen} from './viewstate.svelte';
    import {CLOSE_MS, reveal} from './reveal';

    interface Props {
        device: StatusDevice;
        /** the disk's entry of GET /storage, joined by name; absent until it is read */
        health?: StorageDevice;
        /** whether the kernel log could be read (the error count means something) */
        kernelLog?: boolean;
        /** the host watches the disks' health (a VM, a container): one quiet line */
        hostMonitored?: boolean;
        /** the panel the storage warning's `#storage` lands on */
        anchor?: boolean;
        /** the warning's edge on the anchored panel (task 12) */
        warnAttrs?: Record<string, string | undefined>;
        /** a reason's sentence as the warning says it, with the disk's name in front */
        reasonText?: (r: StorageReason) => string;
        /** the disk's name in those sentences ("SD card SN64G") */
        deviceName?: string;
    }
    let {device, health, kernelLog = true, hostMonitored = false, anchor = false, warnAttrs = {}, reasonText, deviceName = ''}: Props = $props();

    const title = $derived.by(() => {
        const d = device;
        const model = deviceModel(d);
        switch (d.kind) {
            case 'sd': return t('SD card');
            case 'emmc': return t('eMMC');
            case 'usb': return model || t('USB drive');
            case 'sata':
            case 'nvme': return model || t('Disk');
        }
        return t('Disk');
    });
    // the kernel's name, and a stick's filesystem label where it has one
    const sub = $derived([device.name, device.kind === 'usb' ? device.partitions.find((p) => p.label)?.label : ''].filter(Boolean).join(' · '));
    const colours = $derived(ringColours(device));
    const level = $derived(deviceLevel(device));
    const lang = $derived(i18n.language);

    // task 111: the verdict, only for a disk that has one of its own - a virtual disk's is the host's
    const verdict = $derived(health && !health.virtual && !hostMonitored ? health.verdict : undefined);
    const edge = $derived(panelEdge(level, verdict, anchor ? warnAttrs['data-warn-edge'] : undefined));
    const chips = $derived(health ? healthChips(health) : []);
    const facts = $derived(health ? detailRows(health) : null);
    const reasons = $derived(health && verdict && verdict !== 'good' ? health.reasons.map((r) => withoutDevice(reasonText ? reasonText(r) : r.code, deviceName)) : []);
    const open = $derived(view.storageOpen);
    const notes = $derived(
        [
            level === 'error' ? t('nearly full') : level === 'warn' ? t('filling up') : '',
            unallocatedShown(device) ? t('not allocated: {size}', {size: formatDecimal(device.unallocated_bytes, lang)}) : '',
            hostMonitored ? t('Disk health is monitored by the host') : '',
        ].filter(Boolean),
    );

    function partName(p: StatusPartition): string {
        switch (p.role) {
            case 'userfs': return t('partition: data');
            case 'root': return t('partition: system');
            case 'boot': return t('partition: boot');
        }
        return p.label || p.mount;
    }
    const figures = (p: StatusPartition) => {
        const [used, total, unit] = usedOfTotal(p.used_bytes, p.total_bytes, lang);
        return t('{used} of {total} {unit}', {used, total, unit});
    };
    const verdictWord = (v: string) => (v === 'replace' ? t('replace') : v === 'watch' ? t('watch') : t('good'));
    function chipText(c: HealthChip): string {
        switch (c.kind) {
            case 'wear': return c.exceeded ? t('past its rated life') : c.upTo ? t('up to {pct} %', {pct: c.percent}) : `${c.percent} %`;
            case 'smart': return c.passed ? t('SMART ok') : t('SMART failed');
            case 'writes': return t('{size}/day', {size: formatBytes(c.bytes)});
            case 'errors': return c.count === 1 ? t('1 error') : t('{n} errors', {n: c.count});
        }
    }
    function chipTitle(c: HealthChip): string {
        switch (c.kind) {
            case 'wear': return t('Wear: the share of the rated life used');
            case 'smart': return t("The drive's own health check (SMART)");
            case 'writes': return t('Write load: written to this disk per day');
            case 'errors': return health?.last_io_error || t('Storage errors in the kernel log since this boot');
        }
    }
    const chipLevel = (c: HealthChip) => (c.kind === 'wear' ? c.level : c.kind === 'smart' ? (c.passed ? 'good' : 'replace') : c.kind === 'errors' ? 'replace' : 'good');

    function emmcText(code: number): string {
        const r = emmcRange(code);
        return r === null ? t('not reported') : r === 'exceeded' ? t('past its rated life') : `${r.from}–${r.to} %`;
    }
    const eolWord = (s?: string) => (s === 'urgent' ? t('almost used up') : s === 'warning' ? t('80 % used') : s === 'normal' ? t('normal') : t('not reported'));
    const ageText = (months: number) => (months >= 12 ? t('{n} years old', {n: Math.floor(months / 12)}) : t('less than a year old'));
    function smartFacts(d: StorageDevice): string[] {
        const s = d.smart;
        if (!s) return [];
        return [
            s.power_on_hours ? t('{n} hours powered on', {n: s.power_on_hours}) : '',
            s.temperature_c ? `${s.temperature_c} °C` : '',
            s.reallocated_sectors != null ? t('{n} reallocated sectors', {n: s.reallocated_sectors}) : '',
            s.pending_sectors != null ? t('{n} pending sectors', {n: s.pending_sectors}) : '',
            s.media_errors != null ? t('{n} media errors', {n: s.media_errors}) : '',
        ].filter(Boolean);
    }

    // the rings on a 100 × 100 grid: 8 wide with a 2 gap, the outermost at r 45
    const STROKE = 8;
    const radius = (i: number) => 45 - i * (STROKE + 2.5);
    const dash = (p: StatusPartition, i: number) => {
        const c = 2 * Math.PI * radius(i);
        const f = Math.max(0, Math.min(1, p.total_bytes > 0 ? p.used_bytes / p.total_bytes : 0));
        return `${(f * c).toFixed(2)} ${c.toFixed(2)}`;
    };
    const uid = $props.id();
</script>

<!-- task 111: one of an eMMC's two wear estimates, a bar in the verdict's colours and its range -->
{#snippet emmcBar(label: string, code: number)}
    <div class="sp-emmc" data-cells={label}>
        <span class="ol-muted">{label}</span>
        <div class="ol-progress sp-wear" class:warn={code >= 0x09 && code < 0x0b} class:failed={code >= 0x0b}><div style:width="{barWidth(code * 10)}%"></div></div>
        <span class="sp-emmc-range">{emmcText(code)}</span>
    </div>
{/snippet}

<div
    class="ol-card ol-storage-panel"
    class:warn={edge === 'warn'}
    class:err={edge === 'error'}
    class:sp-closed={!open}
    id={anchor ? 'storage' : undefined}
    data-storage-device={device.name}
    data-verdict={verdict}
    {...anchor ? warnAttrs : {}}
>
    <div class="ol-card-head">
        <span class="ol-card-icon"><Icon name="disk" size={14} /></span>
        <div class="ol-card-titles">
            <div class="ol-card-title sp-title" title={title}>{title}</div>
            <div class="ol-card-sub sp-sub">{sub}</div>
        </div>
        {#if verdict}
            <!-- the box fits the longer words (in its pseudo-elements, unseen), so a change of the verdict moves nothing -->
            <span class="ol-badge sp-pill {verdictClass(verdict)}" data-pill={verdict} data-w1={verdictWord('watch')} data-w2={verdictWord('replace')}><span class="sp-pill-word">{verdictWord(verdict)}</span></span>
        {/if}
        <span class="sp-size" data-device-size title={t('the size of the whole device')}>{formatDecimal(device.size_bytes, lang)}</span>
    </div>
    {#if device.partitions.length}
        <div class="sp-row">
            <svg class="sp-rings" viewBox="0 0 100 100" aria-hidden="true">
                {#each device.partitions.slice(0, MAX_RINGS) as p, i (p.name)}
                    <circle class="track" cx="50" cy="50" r={radius(i)} style:stroke={colours[i]} />
                    <circle class="arc" data-ring={p.mount} cx="50" cy="50" r={radius(i)} style:stroke={colours[i]} stroke-dasharray={dash(p, i)} stroke-linecap={p.used_bytes > 0 ? 'round' : 'butt'} transform="rotate(-90 50 50)" />
                {/each}
            </svg>
            <ul class="sp-legend">
                {#each device.partitions as p, i (p.name)}
                    {@const pl = partitionLevel(p)}
                    <li data-partition={p.mount} data-role={p.role} title={t('{pct} % of {total} in use', {pct: usedPct(p), total: formatDecimal(p.total_bytes, lang)})}>
                        <span class="sw" style:background={i < MAX_RINGS ? colours[i] : 'transparent'} aria-hidden="true"></span>
                        <span class="sp-name"><span class="sp-role">{partName(p)}</span>{#if p.role !== 'other' || p.label}<span class="ol-muted hmm-mono sp-mount">{p.mount}</span>{/if}</span>
                        <span class="sp-fig" data-figures>{figures(p)}</span>
                        <span class="sp-pct" class:warn={pl === 'warn'} class:err={pl === 'error'} data-pct>{usedPct(p)} %</span>
                    </li>
                {/each}
            </ul>
        </div>
    {:else}
        <!-- a disk with no mounted file system, shown for its health alone -->
        <div class="ol-muted sp-unmounted" data-unmounted>{t('not mounted')}</div>
    {/if}
    {#if reasons.length}
        <!-- one line: the first reason, and how many more stand behind Details -->
        <div class="sp-reasons" class:err={verdict === 'replace'} data-reasons={reasons.length} title={reasons.join('\n')}>
            <Icon name="alert" size={13} /><span class="sp-reason">{reasons[0]}</span>{#if reasons.length > 1}<span class="sp-more">{t('+{n} more', {n: reasons.length - 1})}</span>{/if}
        </div>
    {/if}
    {#if chips.length}
        <div class="sp-chips" data-chips>
            {#each chips as c (c.kind)}
                {@const l = chipLevel(c)}
                <span class="sp-chip" class:warn={l === 'watch'} class:err={l === 'replace'} data-chip={c.kind} title={chipTitle(c)}>
                    {#if c.kind === 'wear'}<span class="sp-chip-k">{t('Wear')}</span>{' '}{:else if c.kind === 'writes'}<span class="sp-chip-k">{t('Writes')}</span>{' '}{/if}<span class="sp-chip-v">{chipText(c)}</span>
                </span>
            {/each}
        </div>
    {/if}
    <div class="sp-foot">
        {#if health && facts}
            <button type="button" class="sp-toggle" aria-expanded={open} aria-controls="{uid}-details" data-details-toggle onclick={() => setStorageOpen(!open)}>
                <span class="sp-caret" aria-hidden="true">▸</span>{t('Details')}
            </button>
        {/if}
        {#if notes.length}
            <span class="sp-notes" class:warn={level === 'warn'} class:err={level === 'error'} data-notes title={notes.join(' · ')}>{notes.join(' · ')}</span>
        {/if}
    </div>
    {#if open && health && facts}
        <div id="{uid}-details" class="sp-details" data-details in:reveal out:reveal={{duration: CLOSE_MS}}>
            {#if reasons.length > 1}
                <ul class="sp-reasons-all" data-reasons-all>
                    {#each reasons as r, i (i)}<li>{r}</li>{/each}
                </ul>
            {/if}
            <dl class="sp-facts">
                {#if health.manufactured}
                    <dt>{t('Made')}</dt>
                    <dd data-fact="made">{madeMonth(health.manufactured)}{#if health.age_months != null}{' '}<span class="ol-muted">({ageText(health.age_months)})</span>{/if}</dd>
                {/if}
                {#if facts.wear === 'emmc' && health.emmc}
                    <dt>{t('Wear')}</dt>
                    <dd data-fact="wear">
                        {@render emmcBar(t('cell type A'), health.emmc.life_time_a)}
                        {@render emmcBar(t('cell type B'), health.emmc.life_time_b)}
                    </dd>
                    <dt>{t('Reserve blocks')}</dt>
                    <dd data-fact="eol" class:sp-warn-text={health.emmc.pre_eol === 'warning'} class:sp-bad={health.emmc.pre_eol === 'urgent'}>{eolWord(health.emmc.pre_eol)}</dd>
                {:else if facts.wear === 'smart' && health.smart?.wear_percent != null}
                    {@const pct = health.smart.wear_percent}
                    <dt>{t('Wear')}</dt>
                    <dd data-fact="wear">
                        <div class="ol-progress sp-wear" class:warn={wearLevel(pct) === 'watch'} class:failed={wearLevel(pct) === 'replace'}><div style:width="{barWidth(pct)}%"></div></div>
                        {t('{pct} % of its rated life used', {pct})}
                    </dd>
                {/if}
                {#if facts.smart && health.smart}
                    <dt>SMART</dt>
                    <dd data-fact="smart"><span class:sp-bad={health.smart.passed === false} class:sp-good={health.smart.passed === true}>{health.smart.passed === false ? t('failed') : health.smart.passed ? t('passed') : t('no verdict')}</span>{#each smartFacts(health) as f, i (i)}{' · '}{f}{/each}</dd>
                {/if}
                <dt>{t('Errors')}</dt>
                <dd data-fact="errors">
                    {#if !kernelLog}
                        <span class="ol-muted">{t('not counted: the kernel log could not be read')}</span>
                    {:else if health.io_errors > 0}
                        <span class="sp-bad" title={health.last_io_error || undefined}>{t('{n} storage errors in the kernel log since this boot', {n: health.io_errors})}</span>
                    {:else}
                        {t('none in the kernel log since this boot')}
                    {/if}
                    {#each health.filesystems as fs (fs.name)}
                        {#if fs.errors > 0}
                            <div class="sp-warn-text" data-fs={fs.name}>{t('{fs}: {n} file system errors recorded, the last on {date}', {fs: fs.mounts[0] ?? fs.name, n: fs.errors, date: fs.last_error ? new Date(fs.last_error).toLocaleDateString() : '–'})}</div>
                        {/if}
                    {/each}
                    {#if health.filesystems.length && health.filesystems.every((fs) => fs.errors === 0)}
                        <div class="ol-muted">{t('no file system errors recorded')}</div>
                    {/if}
                </dd>
                <dt>{t('Writes')}</dt>
                <dd data-fact="writes">
                    {#if health.writes.per_day_bytes && health.writes.per_day_basis}
                        {t('{size} a day', {size: formatBytes(health.writes.per_day_bytes)})}{' '}<span class="ol-muted">({health.writes.per_day_basis === 'samples' ? t('over the last {n} days', {n: Math.max(1, Math.round(health.writes.per_day_days ?? 0))}) : t('estimated since this boot')})</span>
                    {/if}
                    <div class="ol-muted">{t('{size} since this boot', {size: formatBytes(health.writes.since_boot_bytes)})}</div>
                    {#each health.filesystems as fs (fs.name)}
                        {#if fs.lifetime_write_bytes > 0}
                            <div class="ol-muted">{t('{fs}: {size} written since the file system was made', {fs: fs.mounts[0] ?? fs.name, size: formatBytes(fs.lifetime_write_bytes)})}</div>
                        {/if}
                    {/each}
                </dd>
            </dl>
        </div>
    {/if}
</div>

<style>
    .ol-storage-panel { display: flex; flex-direction: column; gap: 10px; scroll-margin-top: 72px; }
    /* task 111: closed, the panel takes the Utilisation row's height and adds nothing to it (the
       grid's rows are as high as its tallest gauge, StatusPage's sp-rows); what does not fit is
       behind Details */
    .ol-storage-panel.sp-closed { contain: size; overflow: clip; }
    .sp-title { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .sp-sub { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; overflow-wrap: normal; }
    .sp-size { flex: 0 0 auto; margin-left: auto; font-size: 15px; font-weight: 600; color: var(--hmm-fg); white-space: nowrap; }
    /* the pill: its word and, unseen in the same cell, the two longer ones - the widest sets the box
       ("good" is never the widest) */
    .sp-pill { flex: 0 0 auto; display: inline-grid; padding: 1px 9px; border-radius: 999px; font-size: var(--hmm-font-size-small); font-weight: 600; text-align: center; }
    .sp-pill::before { content: attr(data-w1); }
    .sp-pill::after { content: attr(data-w2); }
    .sp-pill::before, .sp-pill::after { grid-area: 1 / 1; visibility: hidden; white-space: nowrap; }
    .sp-pill-word { grid-area: 1 / 1; white-space: nowrap; }
    /* the rings left, the legend right; on a narrow card the legend goes under the rings */
    .sp-row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; }
    .sp-rings { flex: 0 0 auto; width: 88px; height: 88px; display: block; }
    .track { fill: none; stroke-width: 8; opacity: 0.18; }
    .arc { fill: none; stroke-width: 8; }
    .sp-legend { flex: 1 1 150px; min-width: 0; list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: auto minmax(0, 1fr) auto auto; gap: 4px 8px; align-items: baseline; font-size: var(--hmm-font-size-small); }
    .sp-legend li { display: contents; }
    .sw { width: 10px; height: 10px; border-radius: 50%; align-self: center; }
    .sp-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--hmm-fg); }
    .sp-mount { margin-left: 5px; }
    .sp-fig { white-space: nowrap; color: var(--hmm-fg); text-align: right; font-variant-numeric: tabular-nums; }
    /* room for "100 %" whatever the figure, so a change of the value never moves the row */
    .sp-pct { white-space: nowrap; text-align: right; min-width: 4.2ch; color: var(--hmm-fg-muted); font-variant-numeric: tabular-nums; }
    .sp-pct.warn { color: var(--hmm-warn); }
    .sp-pct.err { color: var(--hmm-error); }
    .sp-unmounted { font-size: var(--hmm-font-size-small); }
    /* the reasons in one line; the full list is in Details and the tooltip */
    .sp-reasons { display: flex; align-items: center; gap: 5px; min-width: 0; font-size: var(--hmm-font-size-small); color: var(--hmm-warn); }
    .sp-reasons.err { color: var(--hmm-error); }
    .sp-reasons :global(svg) { flex: 0 0 auto; }
    .sp-reason { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .sp-more { flex: 0 0 auto; white-space: nowrap; color: var(--hmm-fg-muted); }
    /* the chips: whole, never cut - they wrap to a second line at most, inside the panel's fixed
       height (a phone, a narrow column); the foot stays at the bottom whatever they take */
    .sp-chips { display: flex; flex-wrap: wrap; gap: 4px 6px; min-width: 0; max-height: calc(2 * 1.6em + 4px); overflow: hidden; font-size: var(--hmm-font-size-small); }
    .sp-chip { flex: 0 0 auto; display: inline-flex; gap: 4px; padding: 0 8px; line-height: 1; box-sizing: border-box; height: 1.6em; align-items: center; border-radius: 999px; border: 1px solid var(--hmm-border-muted); color: var(--hmm-fg); white-space: nowrap; font-variant-numeric: tabular-nums; }
    .sp-chip-k { color: var(--hmm-fg-muted); }
    .sp-chip.warn { border-color: var(--hmm-warn); }
    .sp-chip.warn .sp-chip-v { color: var(--hmm-warn); }
    .sp-chip.err { border-color: var(--hmm-error); }
    .sp-chip.err .sp-chip-v { color: var(--hmm-error); font-weight: 600; }
    /* the foot: Details and the quiet notes (full, unallocated, the host) in one line */
    .sp-foot { margin-top: auto; display: flex; align-items: center; gap: 4px 12px; min-width: 0; font-size: var(--hmm-font-size-small); }
    .sp-toggle { flex: 0 0 auto; display: inline-flex; align-items: center; gap: 4px; border: 0; padding: 1px 4px 1px 0; background: none; color: var(--hmm-link); font: inherit; cursor: pointer; }
    .sp-toggle:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; border-radius: var(--hmm-radius); }
    .sp-caret { display: inline-block; width: 1em; text-align: center; transition: transform 0.15s ease; }
    .sp-toggle[aria-expanded='true'] .sp-caret { transform: rotate(90deg); }
    @media (prefers-reduced-motion: reduce) { .sp-caret { transition: none; } }
    .sp-notes { flex: 1 1 auto; min-width: 0; margin-left: auto; text-align: right; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--hmm-fg-muted); }
    .sp-notes.warn { color: var(--hmm-warn); }
    .sp-notes.err { color: var(--hmm-error); }
    .sp-details { font-size: var(--hmm-font-size-small); min-width: 0; }
    .sp-reasons-all { margin: 0 0 8px; padding-left: 18px; overflow-wrap: anywhere; }
    .sp-facts { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 5px 14px; margin: 0; }
    .sp-facts dt { color: var(--hmm-fg-muted); }
    .sp-facts dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
    .sp-good { color: var(--hmm-ok); font-weight: 600; }
    .sp-bad { color: var(--hmm-error); font-weight: 600; }
    .sp-warn-text { color: var(--hmm-warn); }
    .sp-wear { max-width: 200px; margin: 3px 0 2px; }
    .sp-wear.warn > div { background: var(--hmm-warn); }
    .sp-emmc { display: grid; grid-template-columns: max-content minmax(40px, 160px) max-content; align-items: center; gap: 0 8px; }
    .sp-emmc .sp-wear { margin: 0; }
    @media (max-width: 480px) {
        .sp-facts { grid-template-columns: minmax(0, 1fr); gap: 2px 0; }
        .sp-facts dt { padding-top: 6px; }
    }
</style>
