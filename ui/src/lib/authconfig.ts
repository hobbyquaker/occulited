/*
 * openccu-lite B-206 (the maintainer: "when trying to save authentik config i get json unknown
 * field modes"): PUT /api/auth/v1/config takes the writable fields only - GET's answer carries
 * read-only ones too (modes, client_secret_set, restart_required, running), and the API refuses
 * any field it does not know. The body is built here, key by key, never from the view.
 */

export interface AuthConfigPut {
    mode: 'local' | 'oidc' | 'off';
    name: string;
    issuer: string;
    client_id: string;
    /** write-only: empty keeps the stored secret */
    client_secret: string;
    username_claim: string;
    scopes: string;
    password_login: boolean;
    /** task 262: the session lengths as Go durations; absent keeps what is stored */
    session_idle?: string;
    session_max?: string;
    /** occulited task 70: the login page's options; absent keeps what is stored (an older daemon has none) */
    hide_password_form?: boolean;
    passkey_offer?: 'autofill' | 'prompt' | 'off';
}

export function authConfigBody(v: Omit<AuthConfigPut, 'client_secret' | 'session_idle' | 'session_max'>, secret: string, sessions?: {session_idle: string; session_max: string}): AuthConfigPut {
    return {
        mode: v.mode,
        name: v.name,
        issuer: v.issuer,
        client_id: v.client_id,
        client_secret: secret,
        username_claim: v.username_claim,
        scopes: v.scopes,
        password_login: v.password_login,
        ...(v.hide_password_form !== undefined ? {hide_password_form: v.hide_password_form} : {}),
        ...(v.passkey_offer !== undefined ? {passkey_offer: v.passkey_offer} : {}),
        ...(sessions ?? {}),
    };
}

/**
 * Task 262: a Go duration as the API renders it ("30m0s", "12h0m0s", "24h0m0s") in whole minutes;
 * fallback for an absent or unreadable value (an older daemon).
 */
export function durationMinutes(d: string | undefined, fallback: number): number {
    if (!d) return fallback;
    const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(d.trim());
    if (!m || (!m[1] && !m[2] && !m[3])) return fallback;
    return Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0) + Math.round(Number(m[3] ?? 0) / 60);
}

/** The page's minutes and hours as the durations PUT /config takes. */
export function sessionLengths(idleMinutes: number, maxHours: number): {session_idle: string; session_max: string} {
    return {session_idle: `${Math.max(1, Math.round(idleMinutes))}m`, session_max: `${Math.max(1, Math.round(maxHours))}h`};
}
