// occulited task 101: on a room or function page, Control leaves that page's own name out of each
// tile's name - it is the page's heading and the active entry of the drawer already. On Räume ›
// Küche "Fenster Küche links" reads "Fenster links"; on Gewerke › Fenster it reads "Küche links".
// An option of the account, on by default (prefs.appFullNames switches it off). Favorites, the
// search, the tooltip and the sheet keep the full name.

// a word's edge: anything but a letter or a digit (JavaScript's \b knows ASCII letters only, so
// "Küche" in "Küchenlampe" or "Bad" in "Bäder" would need it); a hyphen is an edge, so "Küche-Fenster"
// loses its "Küche" while "Küchen-Fenster" keeps its word
const EDGE = '[^\\p{L}\\p{N}]';
const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** the pattern that finds `context` as whole words, case-insensitively; its blanks match any run of blanks */
function pattern(context: string): RegExp | null {
    const words = context.trim().split(/\s+/).filter(Boolean);
    if (words.length === 0) return null;
    const body = words.map(escape).join('\\s+');
    return new RegExp(`(^|${EDGE})${body}(?=$|${EDGE})`, 'giu');
}

// the separators a removed word may leave behind: " - ", " · ", ": ", ", ", " / ", " | "
const SEP = '[-\\u2010-\\u2015\\u00b7:,/|]';
const DANGLING = new RegExp(`^(?:\\s*${SEP})+\\s*|(?:\\s*${SEP})+\\s*$`, 'gu');
const DOUBLED = new RegExp(`(\\s*${SEP})(?:\\s*${SEP})+`, 'gu');
// a channel's suffix alone (":1", the fallback name's tail) is no name
const SUFFIX_ONLY = /^(?:[^\p{L}\p{N}]*\d+)?$/u;

/**
 * `name` without `context` (a room's or function's name) as a whole word, the leftover separators
 * tidied. The full name when nothing matches or when nothing readable would remain.
 */
export function shortenName(name: string, context: string): string {
    const re = pattern(context);
    if (!re) return name;
    const removed = name.replace(re, '$1');
    if (removed === name) return name;
    const tidy = removed.replace(DOUBLED, '$1').replace(DANGLING, '').replace(/\s+/g, ' ').trim();
    if (!tidy || SUFFIX_ONLY.test(tidy)) return name;
    return tidy;
}
