import {cubicOut} from 'svelte/easing';
import type {TransitionConfig} from 'svelte/transition';

/**
 * occulited B-109: Svelte's slide without its opacity. An element whose opacity is animated is a stacking context for
 * the whole run, and a device picture inside it (task 52: blended with the card through mix-blend-mode) blends with
 * that transparent group instead - the black square in dark mode, the white one in light, for the 200 ms of the
 * service messages list's opening and closing. Height, padding, margin and border only; overflow hidden.
 */
export function grow(node: Element, {duration = 200, easing = cubicOut}: {duration?: number; easing?: (t: number) => number} = {}): TransitionConfig {
    const s = getComputedStyle(node);
    const n = (k: string) => parseFloat(s.getPropertyValue(k)) || 0;
    const parts = ['height', 'padding-top', 'padding-bottom', 'margin-top', 'margin-bottom', 'border-top-width', 'border-bottom-width'].map((k) => [k, n(k)] as const);
    return {duration, easing, css: (t) => `overflow: hidden; ${parts.map(([k, v]) => `${k}: ${t * v}px`).join('; ')};`};
}
