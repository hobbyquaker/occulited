// Passkeys (openccu-lite task 262, occulited task 14): the browser half of the WebAuthn
// ceremonies. A passkey is a login of its own - no name, no password - and never a second factor. The API hands out the options as the library renders them (base64url strings where
// the browser wants ArrayBuffers) and takes the credential back as PublicKeyCredential.toJSON()
// renders it; the two conversions live here, with the three ceremonies the pages run.
import {api} from './api';

export interface KeyView {
    id: string;
    name: string;
    created: string;
    last_used?: string;
    /** signs in (a discoverable credential with user verification); false: a second-factor key from
     *  before occulited task 14, which cannot be used to sign in any more */
    passkey: boolean;
    transports?: string[];
}

/** What the login page asks first: is there a passkey to offer, and the name keys are made on. */
export interface WebAuthnInfo {
    passkeys: boolean;
    registered: boolean;
    name: string;
    /** occulited task 70: how the login page offers a passkey (absent from an older daemon: autofill) */
    offer?: 'autofill' | 'prompt' | 'off';
}

export const b64url = {
    decode(s: string): ArrayBuffer {
        const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4));
        const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/') + pad);
        const out = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
        return out.buffer;
    },
    encode(b: ArrayBuffer): string {
        let s = '';
        for (const c of new Uint8Array(b)) s += String.fromCharCode(c);
        return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    },
};

/** The browser can do WebAuthn at all: a secure context with the API. */
export function supported(): boolean {
    return typeof window !== 'undefined' && window.isSecureContext && typeof PublicKeyCredential !== 'undefined' && !!navigator.credentials?.create;
}

/** Conditional UI: the browser offers passkeys in the name field's autofill. */
export async function conditionalAvailable(): Promise<boolean> {
    try {
        return supported() && typeof PublicKeyCredential.isConditionalMediationAvailable === 'function' && (await PublicKeyCredential.isConditionalMediationAvailable());
    } catch {
        return false;
    }
}

/** Whether the page was opened on an address rather than a name (a key cannot be made there). */
export function onAddress(host = typeof location !== 'undefined' ? location.hostname : ''): boolean {
    const h = host.replace(/^\[|\]$/g, '');
    return /^\d{1,3}(\.\d{1,3}){3}$/.test(h) || h.includes(':');
}

/** Whether the host is a bare label without a domain part (localhost excepted): no RP ID. */
export function bareName(host = typeof location !== 'undefined' ? location.hostname : ''): boolean {
    return !onAddress(host) && !host.includes('.') && host !== 'localhost';
}

// the library's JSON -> the browser's structures
type JSONCreation = Omit<PublicKeyCredentialCreationOptions, 'challenge' | 'user' | 'excludeCredentials'> & {challenge: string; user: {id: string; name: string; displayName: string}; excludeCredentials?: {id: string; type: string; transports?: string[]}[]};
type JSONRequest = Omit<PublicKeyCredentialRequestOptions, 'challenge' | 'allowCredentials'> & {challenge: string; allowCredentials?: {id: string; type: string; transports?: string[]}[]};

export function creationOptions(o: JSONCreation): PublicKeyCredentialCreationOptions {
    return {
        ...o,
        challenge: b64url.decode(o.challenge),
        user: {...o.user, id: b64url.decode(o.user.id)},
        excludeCredentials: (o.excludeCredentials ?? []).map((c) => ({...c, id: b64url.decode(c.id), type: 'public-key' as const, transports: c.transports as AuthenticatorTransport[] | undefined})),
    };
}

export function requestOptions(o: JSONRequest): PublicKeyCredentialRequestOptions {
    return {
        ...o,
        challenge: b64url.decode(o.challenge),
        allowCredentials: (o.allowCredentials ?? []).map((c) => ({...c, id: b64url.decode(c.id), type: 'public-key' as const, transports: c.transports as AuthenticatorTransport[] | undefined})),
    };
}

/** PublicKeyCredential.toJSON(), by hand where the browser lacks it (Safari before 17). */
export function credentialJSON(cred: PublicKeyCredential): unknown {
    const withJSON = cred as PublicKeyCredential & {toJSON?: () => unknown};
    if (typeof withJSON.toJSON === 'function') return withJSON.toJSON();
    const r = cred.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse;
    const response: Record<string, unknown> = {clientDataJSON: b64url.encode(r.clientDataJSON)};
    if ('attestationObject' in r && r.attestationObject) {
        response.attestationObject = b64url.encode(r.attestationObject);
        if (typeof r.getTransports === 'function') response.transports = r.getTransports();
    } else {
        response.authenticatorData = b64url.encode(r.authenticatorData);
        response.signature = b64url.encode(r.signature);
        if (r.userHandle) response.userHandle = b64url.encode(r.userHandle);
    }
    return {id: cred.id, rawId: b64url.encode(cred.rawId), type: cred.type, response, clientExtensionResults: cred.getClientExtensionResults(), authenticatorAttachment: (cred as PublicKeyCredential & {authenticatorAttachment?: string}).authenticatorAttachment};
}

/** The answer of a login: the same shape from the password and the passkey. */
export interface LoginAnswer {
    sid: string;
    user: string;
    role: 'admin' | 'user';
    level?: 'read' | 'operate' | 'configure' | 'administer';
    account_id?: string;
    must_change_password: boolean;
}

/**
 * A passkey signs in, without name and password. mediation 'conditional' is the autofill offer in the name field (it
 * waits until the user picks one, or until signal aborts it); 'required' is the button.
 */
export async function passkeyLogin(mediation: 'conditional' | 'required' = 'required', signal?: AbortSignal): Promise<LoginAnswer> {
    const begun = await api.post<{login: string; options: JSONRequest}>('/api/auth/v1/login/passkey', {});
    const cred = (await navigator.credentials.get({publicKey: requestOptions(begun.options), mediation, signal})) as PublicKeyCredential | null;
    if (!cred) throw new Error('no credential');
    return api.post<LoginAnswer>('/api/auth/v1/login/passkey/finish', {login: begun.login, response: credentialJSON(cred)});
}

/**
 * Registers a key for the caller's account under name, with the confirmed ticket of task 154
 * (the password again, or the provider) in X-Occulite-Confirm.
 */
export async function registerKey(name: string, ticket: string): Promise<KeyView> {
    const begun = await api.postWith<{registration: string; options: JSONCreation}>('/api/auth/v1/me/webauthn/begin', {name}, {'X-Occulite-Confirm': ticket});
    const cred = (await navigator.credentials.create({publicKey: creationOptions(begun.options)})) as PublicKeyCredential | null;
    if (!cred) throw new Error('no credential');
    const r = await api.post<{key: KeyView}>('/api/auth/v1/me/webauthn/finish', {registration: begun.registration, response: credentialJSON(cred)});
    return r.key;
}

/** The browser's own refusal, in the page's words: which one it was decides the sentence. */
export function ceremonyError(e: unknown): 'cancelled' | 'not-allowed' | 'security' | 'unsupported' | 'other' {
    const name = (e as {name?: string})?.name ?? '';
    if (name === 'AbortError') return 'cancelled';
    if (name === 'NotAllowedError') return 'not-allowed';
    if (name === 'SecurityError') return 'security';
    if (name === 'NotSupportedError' || name === 'ConstraintError' || name === 'InvalidStateError') return 'unsupported';
    return 'other';
}
