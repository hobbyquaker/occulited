/*
 * The service-message corner's symbols (occulited task 92): a pinned subset of Lucide
 * (lucide-static 1.61.0, https://lucide.dev, ISC, (c) Lucide Icons and Contributors; the licence
 * text is scripts/vendored/lucide-LICENSE and on the Licences page). Only the seven symbols the
 * corner needs, their shapes copied unchanged from the package's icons/<name>.svg; the wrapper is
 * ours, so the corner can raise the stroke (2.5 at 12 px, where Lucide's 2 is too thin). The
 * tiles' own icons stay the knx-uf set (task 75).
 */

export type CornerKind = 'unreach' | 'sabotage' | 'error' | 'duty' | 'lowbat' | 'config' | 'update';

/** The Lucide icon each kind takes (its name in the package). */
export const LUCIDE_NAMES: Record<CornerKind, string> = {
    unreach: 'wifi-off',
    sabotage: 'shield-alert',
    error: 'circle-alert',
    duty: 'gauge',
    lowbat: 'battery-low',
    config: 'refresh-cw',
    update: 'download',
};

const SHAPES: Record<CornerKind, string> = {
    // wifi-off
    unreach: '<path d="M12 20h.01"/><path d="M8.5 16.429a5 5 0 0 1 7 0"/><path d="M5 12.859a10 10 0 0 1 5.17-2.69"/><path d="M19 12.859a10 10 0 0 0-2.007-1.523"/><path d="M2 8.82a15 15 0 0 1 4.177-2.643"/><path d="M22 8.82a15 15 0 0 0-11.288-3.764"/><path d="m2 2 20 20"/>',
    // shield-alert
    sabotage: '<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="M12 8v4"/><path d="M12 16h.01"/>',
    // circle-alert
    error: '<circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/>',
    // gauge
    duty: '<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
    // battery-low
    lowbat: '<path d="M22 14v-4"/><path d="M6 14v-4"/><rect x="2" y="6" width="16" height="12" rx="2"/>',
    // refresh-cw
    config: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
    // download
    update: '<path d="M12 15V3"/><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m7 10 5 5 5-5"/>',
};

/** The kind's symbol as an inline SVG in currentColor, with the given stroke width. */
export function lucideSVG(kind: CornerKind, stroke = 2): string {
    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${SHAPES[kind]}</svg>`;
}
