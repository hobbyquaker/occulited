<script lang="ts" module>
    /*
     * The modal shell of the UI (task 45): a title bar with a × button, a body that scrolls, a
     * footer that stays put. ConfirmDialog.svelte - the shell's one question dialog - is built on
     * it, and so are the editors that need a form of their own (the unit and timer editors of the
     * later tasks): the page keeps `open` in its own state and renders the form as the body.
     *
     * The API:
     *   <ModalDialog open={editing} title={t('Edit timer')} size="large" onclose={() => (editing = false)} dirty={() => form.changed}>
     *       ...the body; it scrolls when the content is taller than the viewport allows...
     *       {#snippet footer()}<button ...>{t('Cancel')}</button><button ...>{t('Save')}</button>{/snippet}
     *   </ModalDialog>
     *
     * - `open`: the dialog is in the DOM only while true; the caller flips it. The dialog never
     *   closes itself - Escape, the × and a click on the backdrop call `onclose`, and the caller
     *   sets `open` to false. So a page can refuse a close, or save first.
     * - `dirty`: asked before every close request; when it answers true the user is asked
     *   "Discard changes?" through the shell dialog (ask()), which opens on top, and `onclose` is
     *   only called when they confirm.
     * - `size`: small (440 px, a question), medium (640 px, a list one has to read), large
     *   (min(1100px, 96vw), an editor). Every size is at most 90dvh tall and 32 px narrower than
     *   the viewport, so a phone shows the whole dialog and the body scrolls.
     * - `layer`: the z-index. Page modals use the default (90); ConfirmDialog uses 100 so the
     *   shell's questions - the dirty check among them - sit on top of a page modal.
     * - `autofocus`: focus moves into the dialog when it opens (the first focusable element of
     *   the body, else the × button); a caller that focuses something of its own passes false.
     *   Either way the focus goes back to the opener when the dialog closes, and Tab and
     *   Shift+Tab cycle inside the dialog while it is the topmost one - a shell question over a
     *   page modal traps for itself.
     * - `subtitle`: one muted line under the title - what the dialog's subject belongs to (the
     *   device type of a firmware bundle whose file is shown).
     * - `class`: an extra class on the box; `footer`: a snippet for the buttons, right-aligned.
     * - `help` (task 51): what the dialog as a whole is about, behind a ? after its title
     *   (lib/Help.svelte) - the explanation of an editor, not the consequence of a question, which
     *   stays the message.
     *
     * While any modal is open the page behind it does not scroll (task 51, moved here from the
     * Services page, which did it for its editors alone): a wheel over the backdrop would otherwise
     * scroll the page away under the dialog, and it would close onto a different place than it
     * opened from. The lock is counted - a shell question over a page modal is a second holder,
     * and only the last one to close gives the page its scrolling back, with the overflow it had.
     *
     * B-93: hiding the overflow took a classic scrollbar away, and the page moved right by its width
     * while a modal was open. The lock gives the scrolling box the width the scrollbar had as
     * padding, measured before the overflow is hidden and taken away with the last lock, so nothing
     * moves. The backdrop is fixed over the whole window and dims the strip the scrollbar leaves.
     *
     * B-129: the box is `.ol-scrollport` (app.css), not the root - the document does not scroll any
     * more. The top bar is outside the port, so it reaches the window edge on its own and needs no
     * reach-through for the padding (B-100's --ol-scrollbar-pad is gone with it). The port's padding
     * is enough: no fixed element that spans the window is shown across a lock (a backdrop comes and
     * goes with its own lock, the power menu's full-page state follows its closed question, a kept
     * addon frame is hidden while it is fixed). Without a port - a test that mounts a dialog on its
     * own - the root is locked as before.
     */
    /** The open modals, innermost last: only the topmost one answers Escape and traps the focus. */
    const stack: HTMLElement[] = [];
    let counter = 0;

    let scrollLocks = 0;
    let overflowBefore = '';
    /** the box's own padding-right while the lock pads it; null while it does not */
    let paddingBefore: string | null = null;
    /** the box the lock holds - kept, so the last unlock gives it back what it took */
    let locked: HTMLElement | null = null;
    /** the shell's scrolling box (B-129); the root where there is none */
    function scroller(): HTMLElement {
        return document.querySelector<HTMLElement>('.ol-scrollport') ?? document.documentElement;
    }
    function lockScroll(): void {
        if (scrollLocks++ !== 0) return;
        const el = scroller();
        locked = el;
        overflowBefore = el.style.overflow;
        // measured before the overflow is hidden; 0 on a page that does not scroll and with an overlay
        // scrollbar, which lose no width
        const scrollbar = el === document.documentElement ? window.innerWidth - el.clientWidth : el.offsetWidth - el.clientWidth;
        if (scrollbar > 0) {
            paddingBefore = el.style.paddingRight;
            el.style.paddingRight = `${scrollbar}px`;
        }
        el.style.overflow = 'hidden';
    }
    function unlockScroll(): void {
        if (scrollLocks === 0 || --scrollLocks !== 0) return;
        const el = locked;
        locked = null;
        if (!el) return;
        el.style.overflow = overflowBefore;
        if (paddingBefore !== null) {
            el.style.paddingRight = paddingBefore;
            paddingBefore = null;
        }
    }

    const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
    function focusables(root: HTMLElement): HTMLElement[] {
        return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => el.offsetParent !== null || el === document.activeElement);
    }
</script>

<script lang="ts">
    import type {Snippet} from 'svelte';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';

    interface Props {
        open: boolean;
        title: string;
        /** one muted line under the title */
        subtitle?: string;
        size?: 'small' | 'medium' | 'large';
        /** the user wants the dialog closed: Escape, the ×, the backdrop, or a confirmed discard */
        onclose: () => void;
        /** answers whether closing would lose something; true asks "Discard changes?" first */
        dirty?: () => boolean;
        layer?: number;
        autofocus?: boolean;
        /** an extra class on the dialog box, for a caller's own styling */
        class?: string;
        children: Snippet;
        footer?: Snippet;
        /** what the dialog is about, behind a ? after the title (task 51) */
        help?: Snippet;
        /** buttons left of the close button, styled like it (class ol-modal-action; occulited task 96) */
        actions?: Snippet;
    }
    let {open, title, subtitle = '', size = 'small', onclose, dirty, layer = 90, autofocus = true, class: extra = '', children, footer, help, actions}: Props = $props();

    counter += 1;
    const titleId = `ol-modal-title-${counter}`;
    let root: HTMLElement | undefined = $state();
    let body: HTMLElement | undefined = $state();
    let closeButton: HTMLButtonElement | undefined = $state();
    // the dirty question is open: a second Escape or click must not ask twice
    let asking = false;

    const isTop = () => root !== undefined && stack[stack.length - 1] === root;

    $effect(() => {
        if (!open || !root) return;
        const el = root;
        const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        stack.push(el);
        lockScroll();
        if (autofocus) {
            // after the body rendered, so a form's first field gets it, not the × button
            queueMicrotask(() => ((body && focusables(body)[0]) ?? closeButton)?.focus());
        }
        return () => {
            const at = stack.indexOf(el);
            if (at >= 0) stack.splice(at, 1);
            // on a close and on an unmount alike: this cleanup runs for both
            unlockScroll();
            // back to where the user was - the button or the row that opened the dialog, usually;
            // a keyboard user would otherwise start over at the top of the page
            if (opener && document.contains(opener)) opener.focus();
        };
    });

    async function requestClose() {
        if (asking) return;
        if (dirty?.()) {
            asking = true;
            const discard = await ask({title: t('Discard changes?'), message: t('What you changed here is not saved.'), confirm: t('Discard'), danger: true});
            asking = false;
            if (!discard) return;
        }
        onclose();
    }

    function onKey(ev: KeyboardEvent) {
        if (!open || !root || !isTop()) return;
        if (ev.key === 'Escape') {
            ev.preventDefault();
            ev.stopPropagation();
            void requestClose();
        } else if (ev.key === 'Tab') {
            // the focus cycles inside the dialog: the page behind it is inert for the keyboard
            const list = focusables(root);
            if (list.length === 0) {
                ev.preventDefault();
                return;
            }
            const first = list[0];
            const last = list[list.length - 1];
            const active = document.activeElement;
            if (ev.shiftKey && (active === first || !root.contains(active))) {
                ev.preventDefault();
                last?.focus();
            } else if (!ev.shiftKey && (active === last || !root.contains(active))) {
                ev.preventDefault();
                first?.focus();
            }
        }
    }
    function onFocusIn(ev: FocusEvent) {
        // a click into the page behind (or a script) moved the focus out: pull it back
        if (!open || !root || !isTop() || root.contains(ev.target as Node)) return;
        (focusables(root)[0] ?? closeButton)?.focus();
    }
</script>

<svelte:window onkeydown={onKey} onfocusin={onFocusIn} />

{#if open}
    <div class="ol-backdrop" style="z-index:{layer}" role="presentation" onmousedown={(ev) => { if (ev.target === ev.currentTarget) void requestClose(); }}>
        <div class="ol-modal ol-card {size} {extra}" role="dialog" aria-modal="true" aria-labelledby={titleId} bind:this={root}>
            <div class="ol-modal-head">
                <div class="ol-modal-titles">
                    <h2 id={titleId}>{title}{#if help}<Help>{@render help()}</Help>{/if}</h2>
                    {#if subtitle}<div class="ol-modal-sub">{subtitle}</div>{/if}
                </div>
                {#if actions}{@render actions()}{/if}
                <button class="ol-modal-close" type="button" aria-label={t('Close')} title={t('Close')} bind:this={closeButton} onclick={() => void requestClose()}>
                    <!-- drawn, not a × character: the dialog's text stays the title and the message -->
                    <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" /></svg>
                </button>
            </div>
            <div class="ol-modal-body" bind:this={body}>
                {@render children()}
            </div>
            {#if footer}
                <div class="ol-modal-foot">
                    {@render footer()}
                </div>
            {/if}
        </div>
    </div>
{/if}

<style>
    .ol-backdrop { position: fixed; inset: 0; background: var(--hmm-backdrop); display: flex; align-items: center; justify-content: center; padding: 16px; }    /* a column: the head and the foot keep their height, the body takes the rest and scrolls */
    .ol-modal { display: flex; flex-direction: column; width: 100%; max-height: 90dvh; padding: 0; box-shadow: 0 12px 40px rgba(0, 0, 0, 0.35); }
    .ol-modal.small { max-width: 440px; }
    .ol-modal.medium { max-width: 640px; }
    .ol-modal.large { max-width: min(1100px, 96vw); }
    .ol-modal-head { display: flex; align-items: center; gap: 8px; padding: 12px 12px 8px 18px; flex: 0 0 auto; }
    .ol-modal-titles { flex: 1 1 auto; min-width: 0; }
    .ol-modal-sub { margin-top: 2px; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    .ol-modal-head h2 { margin: 0; font-size: 1.05em; text-transform: none; letter-spacing: 0; color: var(--hmm-fg); overflow-wrap: anywhere; }
    .ol-modal-close { flex: 0 0 auto; width: 26px; height: 26px; padding: 0; border: 1px solid transparent; border-radius: var(--hmm-radius); background: none; color: var(--hmm-fg-muted); font: inherit; font-size: 18px; line-height: 1; cursor: pointer; }
    .ol-modal-close svg { display: block; width: 14px; height: 14px; margin: auto; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; }
    .ol-modal-close:hover { background: var(--hmm-control-bg-hover); color: var(--hmm-fg); }
    .ol-modal-head :global(.ol-modal-action) { flex: 0 0 auto; width: 26px; height: 26px; padding: 0; border: 1px solid transparent; border-radius: var(--hmm-radius); background: none; color: var(--hmm-fg-muted); font: inherit; line-height: 1; cursor: pointer; display: inline-flex; align-items: center; justify-content: center; }
    .ol-modal-head :global(.ol-modal-action:hover) { background: var(--hmm-control-bg-hover); color: var(--hmm-fg); }
    .ol-modal-head :global(.ol-modal-action:focus-visible) { border-color: var(--hmm-accent); outline: none; }
    .ol-modal-close:focus-visible { border-color: var(--hmm-accent); outline: none; }
    /* a flex column too, so a list inside can take the free height and scroll by itself */
    .ol-modal-body { flex: 1 1 auto; min-height: 0; overflow: auto; display: flex; flex-direction: column; padding: 0 18px; }
    .ol-modal-foot { flex: 0 0 auto; display: flex; justify-content: flex-end; align-items: center; gap: 8px; padding: 12px 18px 16px; }
</style>
