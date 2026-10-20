/*
 * occulited task 12 (the maintainer, 2026-10-03): a warning on the Status page is reflected on the
 * page its button leads to - the panel there carries the same 4 px left edge as the Status page's
 * warning, in the warning's severity colour, for as long as the warning is active. The list is the
 * one the System menu's dots read (systemmenu.svelte.ts); a page names the warnings its panel
 * stands for, and optionally the thing the panel is about (an addon id, a backup target, a unit),
 * which a warning names in its params.addons, params.units or its variant.
 *
 * Pure: no Svelte, no DOM, so vitest runs it (warnedge.test.ts).
 */
import type {Warning} from './warnings';

/** the edge a panel shows: the class `.ol-panel`, `.ol-card` and `.ol-warn-edge` paint */
export type Edge = '' | 'warn' | 'err';

/** whether a warning names key: an addon of its list, a unit of its loop list, or a part of its variant */
export function names(w: Warning, key: string): boolean {
    const p = w.params ?? {};
    if ((p.addons ?? []).some((a) => a.id === key)) return true;
    if ((p.units ?? []).some((u) => u.unit === key || u.unit === `${key}.service`)) return true;
    return w.variant.split(/[,:@]/).includes(key);
}

/** the worst edge of the active warnings among ids (that name key, when given) */
export function edgeOf(list: readonly Warning[], ids: readonly string[], key?: string): Edge {
    let edge: Edge = '';
    for (const w of list) {
        if (!ids.includes(w.id) || (key !== undefined && !names(w, key))) continue;
        // a notice (info, task 366) paints no edge
        if (w.severity === 'info') continue;
        if (w.severity === 'error') return 'err';
        edge = 'warn';
    }
    return edge;
}

/** the worse of two edges: a panel that is in an error state of its own keeps it */
export function worse(a: Edge, b: Edge): Edge {
    return a === 'err' || b === 'err' ? 'err' : a || b;
}
