// The session as the shell sees it. The cookie is HttpOnly; the sid is kept here (and in
// sessionStorage for the Bearer on a host without the cookie, api.ts) and never put into a URL
// (task 125). What an addon that lives by the CCU convention gets in its URL is the session's
// legacy alias (?sid=@..@, ten characters), which the box makes on request (POST /legacy-sid) and
// which only lighttpd's gate and the tclrega shim accept, for /addons/ alone.
import type {LoginAnswer} from './webauthn';
import {api, forgetSid} from './api';
import {addonUrl} from './addonurl';

export interface AuthState {
    loaded: boolean;
    setupRequired: boolean;
    authenticated: boolean;
    user: string;
    role: 'admin' | 'user' | '';
    /** task 78: the account's place on the ladder; '' for a token or before the state is loaded */
    level: 'read' | 'operate' | 'configure' | 'administer' | '';
    /** task 193: the account's stable id, the key of its favorites node */
    accountId: string;
    sid: string;
    /** the session's legacy alias, once asked for (ensureLegacySid) */
    legacySid: string;
    mustChangePassword: boolean;
    /** task 29: auth.mode off — everyone is the anonymous administrator */
    authOff: boolean;
    /** task 193: the Control app's public mode — this browser is the public principal, not signed in */
    public: boolean;
}

export const auth = $state<AuthState>({loaded: false, setupRequired: false, authenticated: false, user: '', role: '', level: '', accountId: '', sid: '', legacySid: '', mustChangePassword: false, authOff: false, public: false});

function remember(sid: string) {
    auth.sid = sid;
    try {
        sessionStorage.setItem('ol.sid', sid);
    } catch {
        /* ignore */
    }
}

/**
 * B-174: a browser session opened with an API token (a token sent as the Bearer): signed in, but
 * with no account behind it - no role, not the public principal, not the anonymous administrator
 * of a system without login. Tokens do not administer in the browser (maintainer, 2026-09-23): the
 * admin pages stay read-only, and TokenNotice says why.
 */
export function isTokenSession(): boolean {
    return auth.loaded && auth.authenticated && !auth.role && !auth.public && !auth.authOff;
}

export async function refresh(): Promise<void> {
    try {
        const s = await api.get<{setup_required: boolean; authenticated: boolean; user?: string; role?: 'admin' | 'user'; level?: AuthState['level']; account_id?: string; must_change_password?: boolean; sid?: string; legacy_sid?: string; auth_off?: boolean; public?: boolean}>('/api/auth/v1/state');
        auth.setupRequired = s.setup_required;
        auth.authenticated = s.authenticated;
        auth.user = s.user ?? '';
        auth.role = s.role ?? '';
        // an older daemon without levels: the role says it (admin = administer, user = operate)
        auth.level = s.level ?? (s.role === 'admin' ? 'administer' : s.role === 'user' ? 'operate' : '');
        auth.accountId = s.account_id ?? '';
        auth.mustChangePassword = s.must_change_password ?? false;
        auth.authOff = s.auth_off ?? false;
        auth.public = s.public ?? false;
        if (s.sid) remember(s.sid);
        else if (!auth.sid) {
            try {
                auth.sid = sessionStorage.getItem('ol.sid') ?? '';
            } catch {
                /* ignore */
            }
        }
        // the box says which alias it knows; one it has forgotten (a restart) keeps working at
        // the gate until the next ask, so the shell keeps what it has
        if (s.legacy_sid) auth.legacySid = s.legacy_sid;
        if (!s.authenticated) auth.legacySid = '';
    } finally {
        auth.loaded = true;
    }
}

/** The password login (an account's passkeys do not touch it: occulited task 14). */
export async function login(username: string, password: string, setup = false): Promise<void> {
    finishLogin(await api.post<LoginAnswer>(setup ? '/api/auth/v1/setup' : '/api/auth/v1/login', {username, password}), setup);
}

/** A login's answer - from the password or a passkey - becomes the shell's session. */
export function finishLogin(r: LoginAnswer, setup = false): void {
    remember(r.sid);
    auth.legacySid = '';
    auth.authenticated = true;
    auth.setupRequired = false;
    auth.user = r.user;
    auth.role = r.role;
    auth.level = r.level ?? (r.role === 'admin' ? 'administer' : 'operate');
    auth.accountId = r.account_id ?? '';
    auth.mustChangePassword = r.must_change_password;
    if (setup) {
        // first boot: the welcome flow (firmware download switch, names from an old CCU, the catalogue)
        try {
            if (!localStorage.getItem('ol.welcomed')) {
                history.replaceState(null, '', '/welcome');
                window.dispatchEvent(new PopStateEvent('popstate'));
            }
        } catch {
            /* ignore */
        }
    }
}

export async function logout(): Promise<void> {
    try {
        await api.post('/api/auth/v1/logout');
    } finally {
        auth.authenticated = false;
        auth.user = '';
        auth.role = '';
        auth.legacySid = '';
        auth.public = false;
        // occulited task 127: the id goes, not an empty one in its place
        auth.sid = '';
        forgetSid();
    }
    // B-48: with the login switched off (task 29) the box answers the next /state as the
    // anonymous administrator again - ask it, so the shell lands on Status and not on a login
    // page nobody can pass
    await refresh();
}

let asking: Promise<string> | null = null;

/** The session's legacy alias, asked for once (task 125): the box makes it the first time an addon
 *  that lives by the CCU convention is opened. '' without a session, or when the box refuses. */
export async function ensureLegacySid(): Promise<string> {
    if (auth.legacySid) return auth.legacySid;
    if (!auth.authenticated) return '';
    if (!asking) {
        asking = (async () => {
            try {
                const r = await api.post<{legacy_sid: string}>('/api/auth/v1/legacy-sid');
                auth.legacySid = r.legacy_sid ?? '';
            } catch {
                auth.legacySid = '';
            } finally {
                asking = null;
            }
            return auth.legacySid;
        })();
    }
    return asking;
}

/** An addon's URL as the shell opens it: with ?sid=@<alias>@ when the box says the addon gets the legacy session (task 125), plain otherwise. */
export function addonHref(url: string, legacySession?: boolean): string {
    return addonUrl(url, auth.legacySid, legacySession);
}

window.addEventListener('ol:unauthenticated', (e) => {
    auth.authenticated = false;
    // occulited task 127: the id that failed is gone from the storage (api.ts); the shell's copy
    // too, unless a newer one took its place meanwhile
    const sent = (e as CustomEvent<{sid?: string}>).detail?.sid;
    if (sent && auth.sid === sent) {
        auth.sid = '';
        auth.legacySid = '';
    }
});
