<script lang="ts">
    /*
     * occulited task 102: the icon picker - a search field, the suggestions on top ("Automatisch" and the
     * caller's list), then the whole KNX User Forum set below, grouped by category, fetched as a separate
     * asset when the picker opens (knxall.ts) and never part of the main bundle. A radio grid of fixed
     * 52 px cells with the arrow keys moving the focus; the chosen one in the accent's ring. Reusable:
     * the caller gives the value, the automatic icon, the suggestions and their labels, and gets the
     * choice back (null = automatic). Switch channels use it today; rooms and functions may later.
     */
    import {fit} from './fit';
    import {onMount} from 'svelte';
    import {t} from '../i18n.svelte';
    import {ALL_NAMES, categoryLabel, groupByCategory, humanName, searchIcons} from './knxall';
    import {ensureAllIcons, knxAll} from './knxall.svelte';
    import {knxSVG} from './stateicons';

    let {value, auto, suggested, labelOf = humanName, disabled = false, onchange}: {
        /** the chosen icon's name, null for automatic */
        value: string | null;
        /** the automatic icon's name (what "Automatisch" shows) */
        auto: string;
        /** the suggestions on top, in order */
        suggested: readonly string[];
        /** the (translated) label of a name; a name the caller does not know gets the set's readable name */
        labelOf?: (name: string) => string;
        disabled?: boolean;
        onchange: (name: string | null) => void;
    } = $props();

    let query = $state('');
    let all = $state<Record<string, string> | null>(null);
    onMount(() => {
        ensureAllIcons().then((icons) => (all = icons), () => undefined);
    });
    function retry() {
        ensureAllIcons().then((icons) => (all = icons), () => undefined);
    }

    const suggestedSet = $derived(new Set(suggested));
    const label = (id: string) => (suggestedSet.has(id) ? labelOf(id) : humanName(id));
    const shownSuggested = $derived(searchIcons(query, suggested, label));
    const rest = $derived(ALL_NAMES.filter((n) => !suggestedSet.has(n)));
    const groups = $derived(all ? groupByCategory(searchIcons(query, rest, label)) : []);
    const nothing = $derived(!!all && shownSuggested.length === 0 && groups.length === 0);
    const svg = (id: string) => knxSVG(id) ?? all?.[id] ?? '';

    // the roving tabindex: the chosen cell (or the first) is the one the Tab key reaches
    let root: HTMLElement;
    function cells(): HTMLButtonElement[] {
        return [...root.querySelectorAll<HTMLButtonElement>('button[role="radio"]:not([disabled])')];
    }
    function tabIndex(id: string | null, first: boolean): number {
        if (value === null ? id === null : id === value) return 0;
        return first && value !== null && !document.querySelector(`[data-icon-cell="${CSS.escape(value)}"]`) ? 0 : -1;
    }
    function columnsOf(el: HTMLElement): number {
        const grid = el.closest<HTMLElement>('.ip-grid');
        return grid ? Math.max(1, getComputedStyle(grid).gridTemplateColumns.split(' ').length) : 1;
    }
    function onKey(ev: KeyboardEvent) {
        const el = ev.target as HTMLElement;
        if (el.getAttribute('role') !== 'radio') return;
        const list = cells();
        const i = list.indexOf(el as HTMLButtonElement);
        if (i < 0) return;
        let j = i;
        switch (ev.key) {
            case 'ArrowRight': j = i + 1; break;
            case 'ArrowLeft': j = i - 1; break;
            case 'ArrowDown': j = i + columnsOf(el); break;
            case 'ArrowUp': j = i - columnsOf(el); break;
            case 'Home': j = 0; break;
            case 'End': j = list.length - 1; break;
            default: return;
        }
        ev.preventDefault();
        const target = list[Math.max(0, Math.min(list.length - 1, j))];
        target?.focus();
        target?.scrollIntoView({block: 'nearest'});
    }
</script>

<div class="ip" bind:this={root} data-icon-picker onkeydown={onKey} role="presentation">
    <input class="hmm-input ip-search" type="search" bind:value={query} placeholder={t('Search icons')} aria-label={t('Search icons')} {disabled} data-icon-search />
    <div class="ip-section">{t('Suggestions')}</div>
    <div class="ip-grid" role="radiogroup" aria-label={t('Suggestions')} data-icon-group="suggested">
        <button type="button" class="ip-cell ip-auto" role="radio" aria-checked={value === null} tabindex={tabIndex(null, true)} title={t('Automatic')} {disabled} onclick={() => onchange(null)} data-icon-cell="">
            <span class="ip-svg" aria-hidden="true">{@html svg(auto)}</span><span class="ip-label" use:fit={{min: 9, text: t('Automatic'), tip: false}}>{t('Automatic')}</span>
        </button>
        {#each shownSuggested as id (id)}
            <button type="button" class="ip-cell" role="radio" aria-checked={value === id} tabindex={tabIndex(id, false)} title={label(id)} aria-label={label(id)} {disabled} onclick={() => onchange(id)} data-icon-cell={id}>
                <span class="ip-svg" aria-hidden="true">{@html svg(id)}</span>
            </button>
        {/each}
    </div>
    <div class="ip-section">{t('All icons')}</div>
    <!-- the box has one height from the start - while the set loads, with every icon, with a search's few - so the
         sheet around it does not move when the set comes in -->
    {#if !all}
        <div class="ip-all ol-muted ip-note" data-icon-loading={knxAll.error ? undefined : '1'}>
            {#if knxAll.error}
                {t('The icon set could not be loaded.')} <button type="button" class="hmm-button ip-retry" onclick={retry} data-icon-retry>{t('Try again')}</button>
            {:else}
                {t('Loading all icons…')}
            {/if}
        </div>
    {:else}
        <div class="ip-all" data-icon-group="all">
            {#if nothing}<div class="ol-muted ip-note" data-icon-nothing>{t('No icon found.')}</div>{/if}
            {#each groups as g (g.id)}
                <div class="ip-cat" data-icon-category={g.id}>
                    <div class="ip-cat-label">{t(categoryLabel(g.id))}</div>
                    <div class="ip-grid" role="radiogroup" aria-label={t(categoryLabel(g.id))}>
                        {#each g.names as id (id)}
                            <button type="button" class="ip-cell" role="radio" aria-checked={value === id} tabindex={tabIndex(id, false)} title={label(id)} aria-label={label(id)} {disabled} onclick={() => onchange(id)} data-icon-cell={id}>
                                <span class="ip-svg" aria-hidden="true">{@html svg(id)}</span>
                            </button>
                        {/each}
                    </div>
                </div>
            {/each}
        </div>
    {/if}
</div>

<style>
    .ip { display: flex; flex-direction: column; gap: 6px; }
    .ip-search { min-height: 44px; font-size: 1.05em; }
    .ip-section { margin: 8px 0 0; font-size: 0.95em; color: var(--hmm-fg-muted); font-weight: 600; }
    /* a grid of equal squares; the chosen one in the accent's ring; no cell changes its box with its state */
    .ip-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(52px, 1fr)); grid-auto-rows: 52px; gap: 6px; }
    .ip-cell { width: 100%; height: 100%; padding: 4px; border: 2px solid transparent; border-radius: 10px; background: color-mix(in srgb, var(--hmm-border-muted) 45%, transparent); color: var(--hmm-fg-muted); cursor: pointer; display: flex; flex-direction: column; align-items: center; justify-content: center; }
    .ip-cell:hover { color: var(--hmm-fg); }
    .ip-cell[aria-checked='true'] { border-color: var(--hmm-accent); color: var(--hmm-warn); }
    .ip-cell:focus-visible { outline: 2px solid var(--hmm-accent); outline-offset: 2px; }
    .ip-cell:disabled { cursor: default; opacity: 0.7; }
    .ip-svg { display: block; width: 36px; height: 36px; }
    .ip-svg :global(svg) { width: 100%; height: 100%; display: block; }
    /* B-104: "Automatisch" fits the two cells - the B-100 way: the label shrinks to 9 px before it would be cut
       (the cell's title says it whole); its line keeps its height */
    .ip-auto { grid-column: span 2; flex-direction: row; gap: 4px; padding: 4px 6px; }
    .ip-auto .ip-svg { width: 28px; height: 28px; flex: 0 0 auto; }
    .ip-label { font-size: 12px; line-height: 16px; white-space: nowrap; overflow: hidden; min-width: 0; }
    .ip-auto .ip-label { flex: 0 1 auto; }
    /* the whole set scrolls in its own box, so the caller's rows below stay within reach */
    .ip-all { height: 42vh; box-sizing: border-box; overflow-y: auto; overscroll-behavior: contain; padding: 2px; border: 1px solid var(--hmm-border-muted); border-radius: 10px; }
    .ip-all.ip-note { align-items: flex-start; padding: 10px; }
    .ip-cat { padding: 4px 4px 8px; content-visibility: auto; contain-intrinsic-size: auto 200px; }
    .ip-cat-label { font-size: 12px; color: var(--hmm-fg-muted); margin: 6px 2px 4px; }
    .ip-note { min-height: 44px; display: flex; align-items: center; gap: 10px; padding: 0 4px; }
    .ip-retry { min-height: 36px; padding: 0 12px; }
</style>
