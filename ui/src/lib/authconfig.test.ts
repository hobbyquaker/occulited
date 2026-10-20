import {describe, expect, it} from 'vitest';
import {authConfigBody, durationMinutes, sessionLengths} from './authconfig';

// openccu-lite B-206: the PUT body has exactly the writable keys, whatever GET answered
describe('authConfigBody', () => {
    it('keeps the writable fields and drops the read-only ones', () => {
        const view = {mode: 'oidc' as const, modes: ['local', 'oidc', 'off'], name: 'authentik', issuer: 'https://auth.example.org/application/o/lite/', client_id: 'abc', client_secret_set: true, username_claim: 'preferred_username', scopes: 'openid profile', password_login: true, running: 'local', restart_required: true, something_new: 1};
        const body = authConfigBody(view, '');
        expect(Object.keys(body).sort()).toEqual(['client_id', 'client_secret', 'issuer', 'mode', 'name', 'password_login', 'scopes', 'username_claim']);
        expect(body).toEqual({mode: 'oidc', name: 'authentik', issuer: 'https://auth.example.org/application/o/lite/', client_id: 'abc', client_secret: '', username_claim: 'preferred_username', scopes: 'openid profile', password_login: true});
        expect(authConfigBody(view, 's3cret').client_secret).toBe('s3cret');
    });
});

// openccu-lite task 262: the session lengths between the page's numbers and the API's durations
describe('session lengths', () => {
    it('reads the durations the API renders, whole minutes', () => {
        expect(durationMinutes('30m0s', 1)).toBe(30);
        expect(durationMinutes('12h0m0s', 1)).toBe(720);
        expect(durationMinutes('24h', 1)).toBe(1440);
        expect(durationMinutes('1h30m', 1)).toBe(90);
        expect(durationMinutes(undefined, 30)).toBe(30);
        expect(durationMinutes('', 30)).toBe(30);
        expect(durationMinutes('soon', 30)).toBe(30);
    });
    it('writes minutes and hours as durations', () => {
        expect(sessionLengths(45, 24)).toEqual({session_idle: '45m', session_max: '24h'});
        expect(sessionLengths(0.4, 0)).toEqual({session_idle: '1m', session_max: '1h'});
    });
    it('puts the lengths beside the writable fields only when given', () => {
        const v = {mode: 'local' as const, name: '', issuer: '', client_id: '', username_claim: '', scopes: '', password_login: true};
        expect(authConfigBody(v, '')).not.toHaveProperty('session_idle');
        expect(authConfigBody(v, '', sessionLengths(30, 12)).session_max).toBe('12h');
    });
});

// occulited task 70: the login page's options go out when the daemon has them, not otherwise
describe('authConfigBody: the login page options', () => {
    const v = {mode: 'oidc' as const, name: 'authentik', issuer: 'https://a/', client_id: 'x', username_claim: '', scopes: '', password_login: true};
    it('sends them when the view has them', () => {
        expect(authConfigBody({...v, hide_password_form: true, passkey_offer: 'prompt'}, '')).toMatchObject({hide_password_form: true, passkey_offer: 'prompt'});
        expect(authConfigBody({...v, hide_password_form: false, passkey_offer: 'off'}, '')).toMatchObject({hide_password_form: false, passkey_offer: 'off'});
    });
    it('leaves them out for an older daemon', () => {
        const b = authConfigBody(v, '');
        expect(b).not.toHaveProperty('hide_password_form');
        expect(b).not.toHaveProperty('passkey_offer');
    });
});
