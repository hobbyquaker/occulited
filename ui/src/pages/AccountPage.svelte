<script lang="ts">
    import {dateTime, dateOnly} from '../lib/datefmt';
    import {onMount} from 'svelte';
    import {api, REQUEST_HEADER} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import {auth, logout} from '../lib/auth.svelte';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import {link} from '../lib/router.svelte';
    import {ask, askText} from '../lib/dialog.svelte';
    import {confirmTicket} from '../lib/confirm';
    import {bareName, ceremonyError, onAddress, registerKey, supported as webauthnSupported, type KeyView} from '../lib/webauthn';

    // task 29: behind the user icon - the account itself: who, logout, own password, own sessions.
    interface Session { id: string; user: string; role: string; created: string; last_seen: string; remote?: string; agent?: string }
    let sessions = $state<Session[] | null>(null);
    let current = $state('');
    let error = $state('');
    let notice = $state('');
    let curPw = $state('');
    let chgPw = $state('');
    // task 19: with password login switched off the box takes no password change either
    let provider = $state<{enabled: boolean; name?: string; password_login?: boolean}>({enabled: false, password_login: true});
    const passwordLogin = $derived(provider.password_login !== false);

    async function load() {
        try {
            const s = await api.get<{sessions: Session[]; current: string}>('/api/auth/v1/sessions');
            sessions = s.sessions;
            current = s.current;
            try { provider = await api.get<typeof provider>('/api/auth/v1/oidc'); } catch { /* an older daemon */ }
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(load);
    async function run(fn: () => Promise<unknown>, ok: string): Promise<void> {
        notice = '';
        try {
            await fn();
            notice = ok;
            await load();
        } catch (e) {
            notice = (e as Error).message;
        }
    }
    const changePw = () => run(async () => { await api.post('/api/auth/v1/password', {current: curPw, password: chgPw}); curPw = ''; chgPw = ''; }, t('Password changed'));

    // task 262, occulited task 14: the account's passkeys - each a login of its own, without name
    // and password; never a second factor. A key registered as a second factor before task 14 is
    // listed as one that cannot sign in, with the offer to remove it and add a passkey instead.
    // Adding and removing ask for the password again (the confirmed ticket of task 154, or the
    // provider). A passkey is bound to the system's name: the card says so when the page is on an
    // address or a bare host name.
    let keys = $state<KeyView[] | null>(null);
    let keysName = $state('');
    let keysError = $state('');
    let keyBusy = $state(false);
    const canWebAuthn = webauthnSupported();
    const unusable = $derived((keys ?? []).filter((k) => !k.passkey).length);
    const passkeys = $derived((keys ?? []).length - unusable);
    const wrongPlace = $derived(onAddress() || bareName() || (keysName !== '' && location.hostname.toLowerCase() !== keysName && location.hostname !== 'localhost'));
    async function loadKeys() {
        try {
            const r = await api.get<{keys: KeyView[]; name: string}>('/api/auth/v1/me/webauthn');
            keys = r.keys;
            keysName = (r.name ?? '').toLowerCase();
            keysError = '';
        } catch (e) {
            keysError = (e as Error).message;
            keys = keys ?? [];
        }
    }
    onMount(loadKeys);
    const confirmTexts = {
        title: t('Add or remove a passkey'),
        message: t('Adding or removing a passkey is confirmed every time.'),
        provider: t('Adding or removing a passkey asks for a fresh login at the identity provider.'),
        impossible: t('This account has no password and no identity provider is configured, so it cannot confirm.'),
    };
    async function addKey() {
        const name = await askText({title: t('Add passkey'), message: t('A name for the passkey, so you can tell them apart later (for example the device it lives on, or the make of the security key).'), input: {placeholder: t('Name of the passkey')}, confirm: t('Continue')});
        if (!name || !name.trim()) return;
        keyBusy = true;
        keysError = '';
        try {
            const ticket = await confirmTicket('/api/auth/v1/me/webauthn', confirmTexts);
            if (!ticket) return;
            await registerKey(name.trim(), ticket);
            notice = t('Passkey added');
            await loadKeys();
        } catch (e) {
            const status = (e as {status?: number}).status;
            if (status) keysError = (e as Error).message;
            else switch (ceremonyError(e)) {
                case 'cancelled': break;
                case 'not-allowed': keysError = t('No passkey was added: the browser cancelled or timed out, the key is registered already, or it cannot make a passkey (a security key needs a PIN for one).'); break;
                case 'security': keysError = t('The browser refused: a passkey can only be added on the system\'s name over a trusted certificate (System → Certificate).'); break;
                case 'unsupported': keysError = t('This browser or device cannot make a passkey here.'); break;
                default: keysError = (e as Error).message;
            }
        } finally {
            keyBusy = false;
        }
    }
    async function removeKey(k: KeyView) {
        if (!(await ask({title: t('Remove passkey'), message: k.passkey ? t('Remove the passkey {name}? It cannot sign in any more.', {name: k.name}) : t('Remove the key {name}? It cannot be used to sign in anyway.', {name: k.name}), confirm: t('Remove'), danger: true}))) return;
        keyBusy = true;
        keysError = '';
        try {
            const ticket = await confirmTicket('/api/auth/v1/me/webauthn', confirmTexts);
            if (!ticket) return;
            await api.delWith(`/api/auth/v1/me/webauthn/${encodeURIComponent(k.id)}`, {'X-Occulite-Confirm': ticket});
            notice = t('Passkey removed');
            await loadKeys();
        } catch (e) {
            keysError = (e as Error).message;
        } finally {
            keyBusy = false;
        }
    }
    const signOutEverywhere = () => run(() => fetch('/api/auth/v1/sessions', {method: 'DELETE', headers: REQUEST_HEADER}), t('Other sessions ended'));
</script>

<h1>{t('Account')}</h1>
{#if !sessions}
    <Loading {error} />
{:else}
    {#if auth.authOff}
        <div class="ol-warn">{t('Login is switched off (System → Users → Authentication): this is the anonymous administrator, not a user account.')}</div>
    {/if}
    {#if notice}<div class="ol-notice">{notice}</div>{/if}
    <div class="ol-cards">
        <div class="ol-card">
            <div class="k">{t('Logged in as')}</div>
            <div class="v">{auth.user} <span class="ol-muted">({t(auth.role)})</span></div>
            {#if !auth.authOff}<div style="margin-top:8px"><button class="hmm-button" onclick={() => logout()}>{t('Logout')}</button></div>{/if}
        </div>
        {#if !auth.authOff && passwordLogin}
            <div class="ol-card">
                <div class="k">{t('Change password')}</div>
                <input class="hmm-input" type="password" placeholder={t('Current password')} bind:value={curPw} autocomplete="current-password" style="width:100%;margin-top:6px" />
                <input class="hmm-input" type="password" placeholder={t('New password (min. 8)')} bind:value={chgPw} autocomplete="new-password" style="width:100%;margin-top:6px" />
                <div style="margin-top:6px"><button class="hmm-button" onclick={changePw} disabled={chgPw.length < 8 || !curPw}>{t('Change password')}</button></div>
            </div>
        {:else if !auth.authOff}
            <div class="ol-card">
                <div class="k">{t('Change password')}</div>
                <div class="ol-muted" style="margin-top:6px">{t('Password login is switched off (System → Users → Authentication): accounts sign in through {name}.', {name: provider.name ?? 'SSO'})}</div>
            </div>
        {/if}
    </div>

    {#if !auth.authOff && passwordLogin}
        <!-- task 262, occulited task 14: the account's passkeys; each user manages their own here,
             where the password is changed -->
        <section class="ol-card ol-keys" data-section="security-keys">
            <div class="k">{t('Passkeys')}<Help>{t('A passkey signs you in without name and password: the one on your phone or computer, or a security key with a PIN (FIDO2, for example a YubiKey), unlocked with PIN or biometrics. The password keeps working beside it. Register a second passkey and keep it somewhere safe.')}</Help></div>
            {#if keysError}<div class="ol-notice error">{keysError}</div>{/if}
            {#if !keys}
                <Loading />
            {:else}
                {#if keys.length}
                    <!-- B-105's phone shape (ol-stack): the name and kind on the row, the rest as labelled lines -->
                    <table class="ol-table ol-stack ol-keys-table">
                        <thead><tr><th>{t('Name')}</th><th>{t('Kind')}</th><th>{t('Added')}</th><th>{t('Last used')}</th><th></th></tr></thead>
                        <tbody>
                            {#each keys as k (k.id)}
                                <tr data-key={k.id} class:ol-key-unusable={!k.passkey}>
                                    <td>{k.name}</td>
                                    <td data-key-kind>{k.passkey ? t('passkey') : t('cannot be used to sign in')}</td>
                                    <td class="ol-stack-line" data-label={t('Added')}>{dateOnly(k.created)}</td>
                                    <td class="ol-muted ol-stack-line" data-label={t('Last used')}>{k.last_used ? dateTime(k.last_used) : t('never')}</td>
                                    <td class="ol-actions ol-stack-line"><button class="hmm-button" onclick={() => removeKey(k)} disabled={keyBusy}>{t('Remove')}</button></td>
                                </tr>
                            {/each}
                        </tbody>
                    </table>
                    {#if unusable}
                        <!-- occulited task 14: keys made as a second factor before; the offer to replace them -->
                        <div class="ol-notice ol-keys-unusable" data-keys-unusable>{unusable === 1 ? t('One key was registered as a second factor, which this system no longer has: it cannot be used to sign in. Remove it and add a passkey instead.') : t('{n} keys were registered as a second factor, which this system no longer has: they cannot be used to sign in. Remove them and add a passkey instead.', {n: unusable})}</div>
                    {/if}
                    {#if passkeys === 1}<p class="ol-muted ol-keys-hint">{t('One passkey only: add a second one as the way in should this one be lost.')}</p>{/if}
                {:else}
                    <p class="ol-muted ol-keys-hint">{t('No passkey yet: sign in with the password.')}</p>
                {/if}
                {#if !canWebAuthn}
                    <p class="ol-muted ol-keys-hint" data-keys-unsupported>{t('This browser cannot register a passkey here: it needs a secure context (HTTPS) and WebAuthn support.')}</p>
                {:else if wrongPlace}
                    <p class="ol-muted ol-keys-hint" data-keys-wrong-place>{t('A passkey is bound to the system\'s name: to add one, open the system as {name} over a trusted certificate (System → Certificate: ACME or an own certificate, and the redirect from the bare name).', {name: keysName ? `https://${keysName}/` : t('its full name (host and domain)')})} <a href="/system/certificates" use:link>{t('Certificate')}</a></p>
                {:else}
                    <div style="margin-top:8px"><button class="hmm-button" onclick={addKey} disabled={keyBusy} data-add-key>{t('Add passkey')}</button></div>
                {/if}
                <!-- occulited task 14: the way back in -->
                <p class="ol-muted ol-keys-hint" data-keys-recovery>{t('Passkey lost? The password still signs in, and an administrator can remove the passkey on the Users page. Locked out entirely: as root on the system (ssh, or keyboard and display), {command} removes the account\'s passkeys and sets a one-time password.', {command: 'occulited admin reset-auth <user>'})}</p>
            {/if}
        </section>
    {/if}

    <h2>{t('My sessions')}</h2>
    <table class="ol-table">
        <thead><tr><th>{t('Session')}</th><th>{t('Last seen')}</th><th>{t('From')}</th></tr></thead>
        <tbody>
            {#each sessions as s (s.id)}
                <tr>
                    <td class="hmm-mono">{s.id}{s.id === current ? ` (${t('this one')})` : ''}</td>
                    <td>{dateTime(s.last_seen)}</td>
                    <td class="ol-muted">{s.remote ?? ''} {s.agent ? `· ${s.agent.slice(0, 40)}` : ''}</td>
                </tr>
            {/each}
        </tbody>
    </table>
    {#if !auth.authOff}<div style="margin-top:8px"><button class="hmm-button" onclick={signOutEverywhere}>{t('Sign out everywhere else')}</button></div>{/if}
{/if}

<style>
    .ol-keys { margin-top: 12px; }
    .ol-keys-table { margin-top: 8px; }
    .ol-keys-hint { margin: 8px 0 0; font-size: var(--hmm-font-size-small); }
    .ol-keys-unusable { margin: 8px 0 0; }
    .ol-key-unusable td[data-key-kind] { color: var(--hmm-warn); }
</style>
