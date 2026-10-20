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
     */
    import Icon from './Icon.svelte';
    import {i18n, t} from './i18n.svelte';
    import {MAX_RINGS, deviceLevel, deviceModel, formatDecimal, partitionLevel, ringColours, unallocatedShown, usedOfTotal, usedPct, type StatusDevice, type StatusPartition} from './storagepanels';

    let {device}: {device: StatusDevice} = $props();

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

    // the rings on a 100 × 100 grid: 8 wide with a 2 gap, the outermost at r 45
    const STROKE = 8;
    const radius = (i: number) => 45 - i * (STROKE + 2.5);
    const dash = (p: StatusPartition, i: number) => {
        const c = 2 * Math.PI * radius(i);
        const f = Math.max(0, Math.min(1, p.total_bytes > 0 ? p.used_bytes / p.total_bytes : 0));
        return `${(f * c).toFixed(2)} ${c.toFixed(2)}`;
    };
</script>

<div class="ol-card ol-storage-panel" class:warn={level === 'warn'} class:err={level === 'error'} data-storage-device={device.name}>
    <div class="ol-card-head">
        <span class="ol-card-icon"><Icon name="disk" size={14} /></span>
        <div class="ol-card-titles">
            <div class="ol-card-title sp-title" title={title}>{title}</div>
            <div class="ol-card-sub">{sub}</div>
        </div>
        <span class="sp-size" data-device-size title={t('the size of the whole device')}>{formatDecimal(device.size_bytes, lang)}</span>
    </div>
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
    {#if level !== 'ok'}
        <div class="sp-note"><Icon name="alert" size={13} />{level === 'error' ? t('nearly full') : t('filling up')}</div>
    {/if}
    {#if unallocatedShown(device)}
        <div class="ol-muted sp-unalloc" data-unallocated>{t('not allocated: {size}', {size: formatDecimal(device.unallocated_bytes, lang)})}</div>
    {/if}
</div>

<style>
    .ol-storage-panel { display: flex; flex-direction: column; gap: 10px; }
    .sp-title { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .sp-size { flex: 0 0 auto; margin-left: auto; font-size: 15px; font-weight: 600; color: var(--hmm-fg); white-space: nowrap; }
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
    .sp-note { display: flex; align-items: center; gap: 4px; font-size: var(--hmm-font-size-small); color: var(--hmm-warn); }
    .ol-storage-panel.err .sp-note { color: var(--hmm-error); }
    .sp-unalloc { font-size: var(--hmm-font-size-small); }
</style>
