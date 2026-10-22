// occulited task 102: the whole icon set's drawings, loaded once per page and made known to the
// tiles (stateicons.ts), with a reactive flag the tiles re-render on - a switch whose chosen icon
// is outside the bundle shows the automatic one until the set is in.
import {loadAllIcons} from './knxall';
import {registerExtraIcons} from './stateicons';

export const knxAll = $state({loaded: false, error: '', loading: false});

/** Loads the set (once) and registers it; resolves to the drawings, or rejects (knxAll.error says why). */
export async function ensureAllIcons(): Promise<Record<string, string>> {
    knxAll.loading = true;
    knxAll.error = '';
    try {
        const icons = await loadAllIcons();
        registerExtraIcons(icons);
        knxAll.loaded = true;
        return icons;
    } catch (e) {
        knxAll.error = (e as Error).message;
        throw e;
    } finally {
        knxAll.loading = false;
    }
}
