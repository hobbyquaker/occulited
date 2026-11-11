// occulited B-141: a backup is refused before its upload runs when it is larger than the system
// takes or the userfs has no room for it. The server checks the same and answers 413 or 507, but
// an answer given while a browser is still sending gigabytes does not reliably reach the page, so
// the page asks GET /api/system/v1/restore/space first and says why in its own words. Pure (the
// unit tests run without Svelte): the page hands in its t().
import {formatDecimal} from './storagepanels';

type Translate = (key: string, params?: Record<string, string | number>) => string;

export interface RestoreSpace {
    max_bytes: number;
    reserve_bytes: number;
    /** null when the system could not measure its free space */
    free_bytes: number | null;
}

/** why a backup of size bytes cannot be uploaded now; '' when it can (or the room is unknown) */
export function uploadRefusal(size: number, space: RestoreSpace, lang: string, t: Translate): string {
    const f = (n: number) => formatDecimal(n, lang);
    if (space.max_bytes > 0 && size > space.max_bytes) {
        return t('The backup is {size}, larger than the {max} an upload may have.', {size: f(size), max: f(space.max_bytes)});
    }
    // twice the file: the upload, and the copy the system's check unpacks beside it
    const need = 2 * size + space.reserve_bytes;
    if (space.free_bytes !== null && need > space.free_bytes) {
        return t('Not enough free space on the system for this backup: it needs {need} (twice the {size} file - the upload and the copy its check unpacks - and {reserve} that stay free), {free} are free.', {
            need: f(need),
            size: f(size),
            reserve: f(space.reserve_bytes),
            free: f(space.free_bytes),
        });
    }
    return '';
}
