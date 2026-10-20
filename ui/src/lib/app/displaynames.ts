// occulited task 73: Control does not rename channels. A user gives a channel a display name of
// their own instead - shown only in Control and only to them; the metadata store keeps the name
// every other view and app shows (GET/PUT/DELETE /api/auth/v1/me/display-names).

export type DisplayNames = Record<string, string>;

/** the map as the API or the browser handed it: only string keys with non-blank string names */
export function cleanNames(raw: unknown): DisplayNames {
    const out: DisplayNames = {};
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return out;
    for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
        if (typeof v === 'string' && v.trim()) out[k] = v.trim();
    }
    return out;
}

/** what Control shows for a channel: the user's display name, else the metadata name, else the fallback */
export function shownName(names: DisplayNames, ref: string, metaName: string | undefined, fallback: string): string {
    return names[ref] || metaName || fallback;
}

/** the metadata name behind an overridden one, for the "actually: …" tooltip; '' when nothing is overridden */
export function realName(names: DisplayNames, ref: string, metaName: string | undefined, fallback: string): string {
    const d = names[ref];
    if (!d) return '';
    const real = metaName || fallback;
    return d === real ? '' : real;
}

/** the map with one name set (trimmed) or, for a blank one, removed */
export function withName(names: DisplayNames, ref: string, name: string): DisplayNames {
    const out = {...names};
    const n = name.trim();
    if (n) out[ref] = n;
    else delete out[ref];
    return out;
}
