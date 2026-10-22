/*
 * occulited B-100: no ellipsis on Control's tiles. A line that does not fit the tile's width
 * shrinks its font in 0.5 px steps until it does, down to a minimum; only below that is it cut,
 * with the whole text in its tooltip. The line keeps its height (CSS), so the tile's box never
 * changes. It is fitted again when its width changes (one ResizeObserver for all lines) and when
 * its text changes (the action's update).
 */

export interface FitOptions {
    /** the smallest font size in px */
    min: number;
    /** the line's text: a change fits it again (and is the tooltip when it is cut) */
    text?: string;
    /** false: the element's title is someone else's (the name's), leave it alone */
    tip?: boolean;
}

/** The largest size from base down to min, in 0.5 px steps, for which fits(size) holds; min when none does. */
export function fitSize(base: number, min: number, fits: (size: number) => boolean, guess?: number): number {
    if (fits(base)) return base;
    const step = (s: number) => Math.round(s * 2) / 2;
    // a first guess from the overflow (text scales with its font), then the exact step around it
    let size = Math.min(base - 0.5, Math.max(min, step(Math.floor((guess ?? base) * 2) / 2)));
    while (size < base - 0.5 && fits(size + 0.5)) size += 0.5;
    while (size > min && !fits(size)) size = Math.max(min, size - 0.5);
    return size;
}

const opts = new WeakMap<HTMLElement, FitOptions>();
const widths = new WeakMap<HTMLElement, number>();
const observer =
    typeof ResizeObserver === 'undefined'
        ? undefined
        : new ResizeObserver((entries) => {
              for (const e of entries) {
                  const el = e.target as HTMLElement;
                  const w = Math.round(e.contentRect.width);
                  if (widths.get(el) === w) continue; // a height change, or the first report of a width fitted already
                  refit(el);
              }
          });

const overflows = (el: HTMLElement) => el.scrollWidth > el.clientWidth + 0.5;

/** Fits one element now. */
export function refit(el: HTMLElement): void {
    const o = opts.get(el);
    if (!o) return;
    el.style.fontSize = '';
    widths.set(el, Math.round(el.clientWidth));
    let size = parseFloat(getComputedStyle(el).fontSize);
    if (el.clientWidth > 0 && overflows(el)) {
        const base = size;
        const guess = (base * el.clientWidth) / el.scrollWidth;
        size = fitSize(base, Math.min(o.min, base), (s) => {
            el.style.fontSize = `${s}px`;
            return !overflows(el);
        }, guess);
        el.style.fontSize = `${size}px`;
    }
    const cut = el.clientWidth > 0 && overflows(el);
    el.dataset.fit = String(size);
    if (cut) el.dataset.fitCut = '1';
    else delete el.dataset.fitCut;
    if (o.tip !== false) {
        if (cut) el.title = o.text ?? el.textContent ?? '';
        else el.removeAttribute('title');
    }
}

// the action's update can run before the element's own text is updated in the same flush: the
// fit waits for the end of it (one microtask, once per element however often it is asked)
const queued = new Set<HTMLElement>();
function soon(el: HTMLElement) {
    if (queued.has(el)) return;
    queued.add(el);
    queueMicrotask(() => {
        queued.delete(el);
        if (el.isConnected) refit(el);
    });
}

/** The Svelte action: use:fit={{min: 13, text}}. */
export function fit(node: HTMLElement, o: FitOptions) {
    opts.set(node, o);
    observer?.observe(node);
    soon(node);
    return {
        update(n: FitOptions) {
            opts.set(node, n);
            soon(node);
        },
        destroy() {
            observer?.unobserve(node);
            opts.delete(node);
        },
    };
}
