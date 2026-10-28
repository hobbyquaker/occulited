// occulited task 125: the addons' views in Control's drawer (GET /api/system/v1/app-views). Pure
// helpers here, so vitest needs no Svelte runtime.

export type AppView = {id: string; title: Record<string, string>; path: string; icon?: string};
export type AppViewGroup = {addon: string; name: Record<string, string>; views: AppView[]};

/** The groups the drawer shows: the account's hidden addons left out, and a group without views. */
export function shownGroups(groups: readonly AppViewGroup[], hidden: readonly string[]): AppViewGroup[] {
    return groups.filter((g) => g.views.length > 0 && !hidden.includes(g.addon));
}

/** A de/en text in the language, English as the fallback. */
export function words(m: Record<string, string> | undefined, lang: string, fallback = ''): string {
    if (!m) return fallback;
    return (lang === 'de' ? m.de : m.en) || m.en || m.de || fallback;
}

/** The deep link of a view: /app/view/<addon>/<view id>. */
export function viewHref(addon: string, id: string): string {
    return `/app/view/${encodeURIComponent(addon)}/${encodeURIComponent(id)}`;
}

/** Parses the route's rest after /app/ - view/<addon>/<id> - or null. */
export function parseViewRoute(rest: string): {addon: string; id: string} | null {
    const m = /^view\/([^/]+)\/([^/]+)$/.exec(rest);
    if (!m) return null;
    try {
        return {addon: decodeURIComponent(m[1]!), id: decodeURIComponent(m[2]!)};
    } catch {
        return null;
    }
}

/** The view's frame: its path with ?embed=app&theme=…&lang=… (a path the system accepted has no query). */
export function embedSrc(path: string, theme: string, lang: string): string {
    // only a page under /addons/ is ever framed - the system refuses anything else
    if (!path.startsWith('/addons/')) return 'about:blank';
    return `${path}${path.includes('?') ? '&' : '?'}embed=app&theme=${encodeURIComponent(theme)}&lang=${encodeURIComponent(lang)}`;
}

/** The registered view, or undefined. */
export function findView(groups: readonly AppViewGroup[], addon: string, id: string): {group: AppViewGroup; view: AppView} | undefined {
    const group = groups.find((g) => g.addon === addon);
    const view = group?.views.find((v) => v.id === id);
    return group && view ? {group, view} : undefined;
}
