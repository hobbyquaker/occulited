<script lang="ts">
    import {dateTime} from './datefmt';
    // Task 154's key sheet (D-103, D-104): every stored HmIP device key as the QR code its sticker
    // had, so a lost sticker keeps a scannable paper copy. The browser prints it (to paper or a
    // PDF); no PDF library, no server-side drawing. The keys live only in this component's props:
    // closing drops them, and nothing goes into the browser's storage.
    import {t} from './i18n.svelte';
    import {formatSGTIN, printedKey, type ExportedKey} from './devicekeys';
    import {qrSvg} from './qrencode';

    interface Props {
        keys: ExportedKey[];
        onclose: () => void;
    }
    let {keys, onclose}: Props = $props();

    const made = dateTime(new Date());
    const host = location.host;
    const codes = $derived(keys.map((k) => ({k, qr: qrSvg(k.payload)})));

    function onkey(e: KeyboardEvent) {
        if (e.key === 'Escape') onclose();
    }
</script>

<svelte:window onkeydown={onkey} />

<div class="ks-backdrop" role="dialog" aria-modal="true" aria-label={t('HmIP device key sheet')} data-sheet="keys">
    <div class="ks-toolbar">
        <button type="button" class="hmm-button primary" onclick={() => window.print()} data-sheet="print">{t('Print')}</button>
        <button type="button" class="hmm-button" onclick={onclose} data-sheet="close">{t('Close')}</button>
        <span class="ks-toolbar-note">{t('Printing to a PDF file works from the same dialog. Closing this view drops the keys from the page.')}</span>
    </div>
    <article class="ks-sheet">
        <header>
            <h1>{t('HmIP device keys')}</h1>
            <p class="ks-meta">{host} · {made} · {keys.length === 1 ? t('1 device') : t('{n} devices', {n: keys.length})}</p>
            <p class="ks-warning">{t('This sheet holds every HmIP device key of this system in clear. Whoever has it can pair the devices elsewhere: keep it like a key, and destroy old copies.')}</p>
        </header>
        <div class="ks-grid">
            {#each codes as {k, qr} (k.sgtin)}
                <section class="ks-card" data-sgtin={k.sgtin}>
                    <svg viewBox="0 0 {qr.size} {qr.size}" shape-rendering="crispEdges" role="img" aria-label={k.sgtin}>
                        <rect width={qr.size} height={qr.size} fill="#fff" />
                        <path d={qr.path} fill="#000" />
                    </svg>
                    <div class="ks-text">
                        <strong>{k.name || k.type || t('Not paired')}</strong>
                        {#if k.name && k.type}<span>{k.type}</span>{/if}
                        <span class="ks-label">SGTIN</span>
                        <span class="ks-mono">{formatSGTIN(k.sgtin)}</span>
                        <span class="ks-label">KEY</span>
                        <span class="ks-mono">{printedKey(k.key)}</span>
                    </div>
                </section>
            {/each}
        </div>
    </article>
</div>

<style>
    .ks-backdrop { position: fixed; inset: 0; z-index: 1000; overflow: auto; background: var(--ol-paper-backdrop); padding: 16px; }
    .ks-toolbar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; max-width: 210mm; margin: 0 auto 12px; }
    .ks-toolbar-note { color: var(--ol-paper-backdrop-ink); font-size: 0.9em; }
    .ks-sheet { max-width: 210mm; margin: 0 auto; background: var(--ol-paper); color: var(--ol-paper-ink); padding: 12mm; box-sizing: border-box; font-family: system-ui, sans-serif; }
    .ks-sheet h1 { font-size: 18pt; margin: 0 0 2mm; }
    .ks-meta { margin: 0 0 3mm; font-size: 10pt; color: var(--ol-paper-meta); }
    .ks-warning { margin: 0 0 6mm; padding: 2mm 3mm; border: 1.5pt solid var(--ol-paper-ink); font-size: 10pt; font-weight: 600; }
    .ks-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(58mm, 1fr)); gap: 5mm; }
    .ks-card { display: flex; flex-direction: column; align-items: center; gap: 2mm; padding: 3mm; border: 0.5pt solid var(--ol-paper-rule); break-inside: avoid; page-break-inside: avoid; }
    .ks-card svg { width: 32mm; height: 32mm; }
    .ks-text { display: flex; flex-direction: column; align-items: center; text-align: center; font-size: 8.5pt; gap: 0.5mm; }
    .ks-label { margin-top: 1mm; font-size: 6.5pt; letter-spacing: 0.05em; color: var(--ol-paper-label); }
    .ks-mono { font-family: ui-monospace, monospace; font-size: 7.5pt; white-space: nowrap; }
    @media (max-width: 600px) {
        .ks-backdrop { padding: 8px; }
        .ks-sheet { padding: 6mm; }
    }
    /* only the sheet goes to paper */
    @media print {
        :global(html), :global(body) { background: var(--ol-paper) !important; }
        :global(body *) { visibility: hidden; }
        .ks-backdrop, .ks-backdrop :global(*) { visibility: visible; }
        .ks-backdrop { position: absolute; inset: 0; background: none; padding: 0; overflow: visible; }
        .ks-toolbar { display: none; }
        .ks-sheet { max-width: none; padding: 0; }
    }
</style>
