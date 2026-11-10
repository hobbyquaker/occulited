<script lang="ts">
    // B-135: the UI's Copy button. Over plain HTTP the page has no `navigator.clipboard`, and the seven
    // buttons that called it by hand did nothing and said nothing - with an API token or a generated
    // password that is shown once. Every copy goes through lib/clipboard.ts now, and the button says
    // what became of it: "Copied.", or, where the browser copies neither way, that the text is to be
    // copied by hand - it is selected then, so Ctrl+C is all that is left to do.
    //
    // Nothing moves when the words come: the button keeps its label and its box, and the words have
    // their place from the start - both sentences lie unseen in the same grid cell, so the cell is as
    // wide (and, where it wraps on a phone, as high) as the longer one.
    import {onDestroy} from 'svelte';
    import {t} from './i18n.svelte';
    import {copyText, type CopyOutcome} from './clipboard';

    type Shown = Element | null | undefined;
    interface Props {
        /** the text, or how to get it at the moment of the click */
        text: string | (() => string);
        /** the button's label; "Copy" when absent */
        label?: string;
        /** the element that shows the text on the page: selected when the copy failed */
        shown?: () => Shown | Promise<Shown>;
        /**
         * Where the words go: `after` the button (the default), `before` it (a button at a row's right
         * end), or `row-end` - the button and the words become items of the flex row around them and
         * the words its last one, so the buttons that follow stay beside this one.
         */
        place?: 'after' | 'before' | 'row-end';
        /** `data-action` of the button */
        action?: string;
        disabled?: boolean;
    }
    let {text, label, shown, place = 'after', action, disabled = false}: Props = $props();

    let outcome = $state<CopyOutcome | ''>('');
    let timer: ReturnType<typeof setTimeout> | undefined;
    onDestroy(() => clearTimeout(timer));

    function select(el: Element) {
        if (el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement) {
            el.focus({preventScroll: true});
            el.select();
            return;
        }
        const sel = el.ownerDocument.getSelection();
        if (!sel) return;
        const range = el.ownerDocument.createRange();
        range.selectNodeContents(el);
        sel.removeAllRanges();
        sel.addRange(range);
    }

    async function copy() {
        clearTimeout(timer);
        const o = await copyText(typeof text === 'function' ? text() : text);
        outcome = o;
        if (o === 'failed') {
            // stays until the next click: the user still has something to do
            const el = await shown?.();
            if (el) select(el);
            return;
        }
        timer = setTimeout(() => (outcome = ''), 4000);
    }

    const done = $derived(t('Copied.'));
    const byHand = $derived(t('Select the text and copy it by hand.'));
</script>

<span class="ol-copy" class:before={place === 'before'} class:row-end={place === 'row-end'}>
    <button type="button" class="hmm-button" data-action={action} {disabled} onclick={copy}>{label ?? t('Copy')}</button>
    <span class="ol-copy-said" class:failed={outcome === 'failed'} data-copy-said={outcome} data-w1={done} data-w2={byHand}><span role="status">{outcome === '' ? '' : outcome === 'failed' ? byHand : done}</span></span>
</span>

<style>
    .ol-copy { display: inline-flex; align-items: center; gap: 4px 10px; min-width: 0; max-width: 100%; vertical-align: middle; }
    .ol-copy > button { flex: 0 0 auto; }
    .ol-copy.before > .ol-copy-said { order: -1; text-align: right; }
    /* the row's own items: the words last, whatever follows the button in the markup */
    .ol-copy.row-end { display: contents; }
    /* …and the first to give way where the row is narrow, so no button beside it has to wrap its label */
    .ol-copy.row-end > .ol-copy-said { order: 1; align-self: center; margin-left: 6px; flex: 0 9999 auto; min-width: min(100%, 11em); }
    /* the words and, unseen in the same cell, both sentences: the longer one sets the box */
    .ol-copy-said { display: inline-grid; flex: 0 1 auto; min-width: 0; font-size: var(--hmm-font-size-small); font-weight: normal; color: var(--hmm-ok); }
    .ol-copy-said.failed { color: var(--hmm-error); }
    .ol-copy-said::before { content: attr(data-w1); }
    .ol-copy-said::after { content: attr(data-w2); }
    .ol-copy-said::before, .ol-copy-said::after { grid-area: 1 / 1; visibility: hidden; }
    .ol-copy-said > span { grid-area: 1 / 1; }
</style>
