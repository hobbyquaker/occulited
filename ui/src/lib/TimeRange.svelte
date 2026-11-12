<script lang="ts">
    import {dateTime} from './datefmt';
    // task 31: the Log page's time range. One button that names the range, and a popover with the
    // quick presets (the last 15 minutes … 7 days) and a from/to pair of native date-time pickers
    // - the browser's own control has a calendar and a clock everywhere the shell runs, which is
    // more than a hand-rolled one would offer. What leaves here is what journalctl takes: a
    // relative "-1h" for a preset, "@<epoch seconds>" for a picked instant, so the browser's and
    // the box's time zones never argue about a wall-clock string.
    import {t} from './i18n.svelte';
    import {onscreen} from './popover';

    let {
        since = $bindable(''),
        until = $bindable(''),
        onchange,
    }: {since?: string; until?: string; onchange?: () => void} = $props();

    const PRESETS: [string, string][] = [
        ['-15min', '15 min'],
        ['-1h', '1 h'],
        ['-6h', '6 h'],
        ['-24h', '24 h'],
        ['-7d', '7 d'],
    ];
    let open = $state(false);
    let from = $state('');
    let to = $state('');
    let root: HTMLDivElement | undefined = $state();

    // a datetime-local value is a wall-clock string in the browser's zone; Date() reads it as such
    const epoch = (v: string) => (v ? `@${Math.floor(new Date(v).getTime() / 1000)}` : '');
    const short = (v: string) => dateTime(v, {dateStyle: 'short', timeStyle: 'short'});
    function fromEpoch(v: string): string {
        // back from "@<seconds>" to a datetime-local value, for the inputs after a reload
        const d = new Date(Number(v.slice(1)) * 1000);
        const pad = (n: number) => String(n).padStart(2, '0');
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    }
    const label = $derived.by(() => {
        if (!since && !until) return `${t('Time')}: ${t('all')}`;
        const preset = PRESETS.find(([v]) => v === since);
        if (preset && !until) return t('last {d}', {d: t(preset[1])});
        const a = since.startsWith('@') ? short(fromEpoch(since)) : since || '…';
        const b = until.startsWith('@') ? short(fromEpoch(until)) : t('now');
        return `${a} – ${b}`;
    });

    function pick(v: string) {
        since = v;
        until = '';
        from = '';
        to = '';
        open = false;
        onchange?.();
    }
    function apply() {
        since = epoch(from);
        until = epoch(to);
        open = false;
        onchange?.();
    }
    function toggle() {
        open = !open;
        if (open) {
            from = since.startsWith('@') ? fromEpoch(since) : '';
            to = until.startsWith('@') ? fromEpoch(until) : '';
        }
    }
    function onWindowClick(e: MouseEvent) {
        if (open && root && !root.contains(e.target as Node)) open = false;
    }
    function onKey(e: KeyboardEvent) {
        if (e.key === 'Escape' && open) open = false;
    }
</script>

<svelte:window onclick={onWindowClick} onkeydown={onKey} />

<div class="ol-range" bind:this={root}>
    <button type="button" class="hmm-button ol-range-btn" onclick={toggle} aria-expanded={open} aria-haspopup="dialog" aria-label={t('Time range')}>
        <span>{label}</span><span class="ol-caret" aria-hidden="true">▾</span>
    </button>
    {#if open}
        <div class="ol-menupop ol-range-pop" role="dialog" aria-label={t('Time range')} use:onscreen>
            <div class="ol-range-presets">
                <button type="button" class="hmm-button" class:primary={!since && !until} onclick={() => pick('')}>{t('all')}</button>
                {#each PRESETS as [v, name] (v)}
                    <button type="button" class="hmm-button" class:primary={since === v && !until} onclick={() => pick(v)}>{t(name)}</button>
                {/each}
            </div>
            <label class="ol-range-field"><span>{t('From')}</span><input class="hmm-input" type="datetime-local" bind:value={from} max={to || undefined} /></label>
            <label class="ol-range-field"><span>{t('To')}</span><input class="hmm-input" type="datetime-local" bind:value={to} min={from || undefined} /></label>
            <div class="ol-range-actions">
                <button type="button" class="hmm-button primary" onclick={apply} disabled={!from && !to}>{t('Apply')}</button>
                <button type="button" class="hmm-button" onclick={() => pick('')}>{t('Clear')}</button>
            </div>
        </div>
    {/if}
</div>
