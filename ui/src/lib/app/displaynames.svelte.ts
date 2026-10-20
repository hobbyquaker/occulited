// occulited task 73: the account's display names in Control, kept with the account on the system
// (GET/PUT/DELETE /api/auth/v1/me/display-names) so a phone and a desktop show the same names.
// With the login switched off there is no account - the system answers 409 no-account - and the
// browser keeps them then, as it does the shell's preferences.
import {api, ApiError} from '../api';
import {auth} from '../auth.svelte';
import {cleanNames, withName, type DisplayNames} from './displaynames';

export const displayNames = $state({names: {} as DisplayNames, loaded: false});

const KEY = 'ol.displayNames';
const PATH = '/api/auth/v1/me/display-names';

function fromBrowser(): DisplayNames {
    try {
        return cleanNames(JSON.parse(localStorage.getItem(KEY) ?? 'null'));
    } catch {
        return {};
    }
}

function toBrowser(names: DisplayNames): void {
    try {
        localStorage.setItem(KEY, JSON.stringify(names));
    } catch {
        /* ignore */
    }
}

/** Reads the caller's names: from the system, or from the browser when the login is off. */
export async function loadDisplayNames(): Promise<void> {
    if (auth.authOff) {
        displayNames.names = fromBrowser();
        displayNames.loaded = true;
        return;
    }
    try {
        const r = await api.get<{display_names?: unknown}>(PATH);
        displayNames.names = cleanNames(r?.display_names);
    } catch {
        /* keep what is shown: the next load repairs it */
    } finally {
        displayNames.loaded = true;
    }
}

/** Sets (or, for a blank name, removes) one channel's display name; throws when the system refuses. */
export async function setDisplayName(ref: string, name: string): Promise<void> {
    const next = withName(displayNames.names, ref, name);
    if (auth.authOff) {
        displayNames.names = next;
        toBrowser(next);
        return;
    }
    const url = `${PATH}/${encodeURIComponent(ref)}`;
    try {
        const r = name.trim() ? await api.put<{display_names?: unknown}>(url, {name: name.trim()}) : await api.del<{display_names?: unknown}>(url);
        displayNames.names = cleanNames(r?.display_names);
    } catch (e) {
        if (e instanceof ApiError && e.code === 'no-account') {
            displayNames.names = next;
            toBrowser(next);
            return;
        }
        throw e;
    }
}
