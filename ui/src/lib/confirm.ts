// A confirmed ticket (task 154, D-104; task 185): an action that asks who the user is every time,
// even in a valid session. The account's password when it has one; a fresh login at the identity
// provider when it signs in there - the page leaves then, and comes back with the ticket in the
// fragment (#confirm=… or #confirm-error=…), which returned() reads.
//
// The password is the signed-in account's own login password, never the one an act sets
// (occulited task 20, openccu-lite#5): every caller asks through confirmTicket, whose dialog says
// so and names the account in the field's label, and hands the name to password managers as a
// hidden username beside a current-password field.
import {api, ApiError} from './api';
import {auth} from './auth.svelte';
import {ask, askText, type AskOptions} from './dialog.svelte';
import {t} from './i18n.svelte';
import {passkeyConfirm} from './webauthn';

export interface ConfirmTexts {
    title: string;
    /** what the act does, one sentence; the dialog adds whose password confirms it */
    message: string;
    /** why the page goes to the identity provider, before it goes */
    provider: string;
    /** the account can confirm neither way */
    impossible: string;
}

/**
 * The password question of a confirmation (task 20, variant C): the act's sentence, then "Confirm
 * with your login password.", the field labelled with the account's name. wrong puts the refusal
 * of the last try above it.
 */
export function passwordQuestion(texts: Pick<ConfirmTexts, 'title' | 'message'>, user: string, wrong = false): AskOptions {
    return {
        title: texts.title,
        message: (wrong ? t('The password was wrong.') + '\n\n' : '') + `${texts.message} ${t('Confirm with your login password.')}`,
        input: {type: 'password', label: user ? t('Login password ({user})', {user}) : t('Login password'), username: user || undefined},
        confirm: t('Confirm'),
    };
}

/**
 * A ticket for path, or null when the user cancelled or the page is on its way to the identity
 * provider - beforeLeave runs just before it goes, to keep what the page resumes with. An error is
 * thrown with the reason the page shows.
 */
export async function confirmTicket(path: string, texts: ConfirmTexts, beforeLeave?: () => void): Promise<string | null> {
    const how = await api.get<{method: string; url?: string; passkey?: boolean}>(`/api/auth/v1/confirm?path=${encodeURIComponent(path)}&return=${encodeURIComponent(location.pathname)}`);
    // occulited task 135: where a passkey can confirm this act, it is offered first; the other way
    // stays open behind the second button, and a dismissed browser question falls through to it
    if (how.passkey && typeof navigator.credentials?.get === 'function') {
        const other = how.method === 'password' ? t('Use the password') : how.method === 'oidc' ? t('Use the identity provider') : '';
        if (await ask({title: texts.title, message: `${texts.message} ${t('Confirm with your passkey.')}`, confirm: t('Use a passkey'), ...(other ? {cancel: other} : {})})) {
            const ticket = await passkeyConfirm(path);
            if (ticket) return ticket;
        }
        if (!other) return null;
    }
    if (how.method === 'oidc' && how.url) {
        if (await ask({title: texts.title, message: texts.provider, confirm: t('Continue')})) {
            beforeLeave?.();
            location.href = how.url;
        }
        return null;
    }
    if (how.method !== 'password' && how.method !== 'none') throw new Error(texts.impossible);
    let wrong = false;
    for (;;) {
        let password = '';
        if (how.method === 'password') {
            const pw = await askText(passwordQuestion(texts, auth.user, wrong));
            if (pw === null) return null;
            password = pw;
        }
        try {
            return (await api.post<{ticket: string}>('/api/auth/v1/ticket', {path, confirm: true, password})).ticket;
        } catch (e) {
            if (e instanceof ApiError && e.status === 401 && how.method === 'password') {
                wrong = true;
                continue;
            }
            throw e;
        }
    }
}

/**
 * Back from the identity provider: the ticket, or the reason there is none, from the fragment -
 * which it removes. undefined when the page was not coming back from a confirmation.
 */
export function returned(): {ticket: string} | {refused: string} | undefined {
    const h = location.hash;
    if (!h.startsWith('#confirm=') && !h.startsWith('#confirm-error=')) return undefined;
    history.replaceState(history.state, '', location.pathname + location.search);
    if (h.startsWith('#confirm=')) return {ticket: decodeURIComponent(h.slice('#confirm='.length))};
    return {refused: decodeURIComponent(h.slice('#confirm-error='.length))};
}
