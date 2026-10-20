<script lang="ts">
    import {onMount} from 'svelte';
    import {api, REQUEST_HEADER} from '../lib/api';
    import {ask, askText} from '../lib/dialog.svelte';
    import {t} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {auth} from '../lib/auth.svelte';
    import Loading from '../lib/Loading.svelte';
    import Disclosure from '../lib/Disclosure.svelte';
    import Help from '../lib/Help.svelte';
    import AuthSettings from '../lib/AuthSettings.svelte';
    import {link} from '../lib/router.svelte';

    // task 29: the Users page under System - who has an account, and who is logged in. The
    // account itself (own password, logout) is behind the user icon.
    // task 132 (D-87): below the accounts and the sessions, an administrator finds what the Security
    // page held besides HTTPS - how the box authenticates and the API tokens (lib/AuthSettings,
    // lib/APITokens; #authentication, #api-tokens). The addon sessions were here too until the
    // maintainer's follow-up (2026-09-16) moved them to the Addons page (/addons#addon-sessions).
    // A user's page stays what it was: the own sessions, nothing of auth:admin.
    // task 19 (D-53): a provider login is an account of the same name here, with or without a
    // password. The list says how each account signs in and when the provider last did; while a
    // provider is configured the password of a new account is optional, and while password login
    // is switched off none is offered at all.
    // task 78 (D-116): an account's place on the ladder - read, operate, configure, administer;
    // role is the derived field older clients read (administer = admin, else user)
    type Level = 'read' | 'operate' | 'configure' | 'administer';
    interface User { name: string; role: 'admin' | 'user'; level: Level; created: string; must_change_password?: boolean; password_set: boolean; last_provider_login?: string; webauthn_keys?: number; webauthn_unusable?: number }
    interface Session { id: string; user: string; role: string; created: string; last_seen: string; remote?: string; agent?: string }
    interface Provider { enabled: boolean; name?: string; password_login?: boolean }

    let users = $state<User[] | null>(null);
    let sessions = $state<Session[]>([]);
    let provider = $state<Provider>({enabled: false, password_login: true});
    const passwordLogin = $derived(provider.password_login !== false);
    // the sign-in column only where it says something: a provider, or an account without a password
    const signInColumn = $derived(provider.enabled || (users ?? []).some((u) => !u.password_set));
    let current = $state('');
    let error = $state('');
    let notice = $state('');
    let newName = $state('');
    let newPw = $state('');
    let newLevel = $state<Level>('operate');
    const LEVELS: {id: Level; label: string; hint: string}[] = [
        {id: 'read', label: 'read', hint: 'sees everything, changes nothing'},
        {id: 'operate', label: 'operate', hint: 'switches and sets values; no names, rooms or favorites'},
        {id: 'configure', label: 'configure', hint: 'also names, rooms, functions and favorites'},
        {id: 'administer', label: 'administer', hint: 'everything: the System pages, accounts, tokens, pairing, deleting'},
    ];
    let addUserOpen = $state(false);
    // a password is needed without a provider; with one it may be left empty (provider only), but
    // once typed it has to be a password
    const canCreate = $derived(!!newName && (newPw.length >= 8 || (provider.enabled && newPw.length === 0)));

    async function load() {
        try {
            users = auth.role === 'admin' ? (await api.get<{users: User[]}>('/api/auth/v1/users')).users : [];
            const s = await api.get<{sessions: Session[]; current: string}>('/api/auth/v1/sessions' + (auth.role === 'admin' ? '?all=true' : ''));
            sessions = s.sessions;
            current = s.current;
            try { provider = await api.get<Provider>('/api/auth/v1/oidc'); } catch { /* an older daemon: local only */ }
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(load);

    /** true when it worked - a form only closes and clears itself on that. */
    async function run(fn: () => Promise<unknown>, ok: string): Promise<boolean> {
        notice = '';
        try {
            await fn();
            notice = ok;
            await load();
            return true;
        } catch (e) {
            notice = (e as Error).message;
            return false;
        }
    }
    async function createUser() {
        if (!(await run(() => api.post('/api/auth/v1/users', {username: newName.trim(), password: passwordLogin ? newPw : '', level: newLevel}), t('User created')))) return;
        newName = '';
        newPw = '';
        addUserOpen = false;
    }
    const deleteUser = async (u: User) => { if (!(await ask({message: t('Delete user {name}?', {name: u.name}), confirm: t('Delete'), danger: true}))) return; await run(() => fetch(`/api/auth/v1/users/${encodeURIComponent(u.name)}`, {method: 'DELETE', headers: REQUEST_HEADER}).then((r) => { if (!r.ok) throw new Error(`${r.status}`); }), t('User deleted')); };
    const resetPw = async (u: User) => { const label = u.password_set ? t('Reset password') : t('Set password'); const pw = await askText({title: label, message: t('New password for {name}', {name: u.name}), input: {type: 'password', minLength: 8, placeholder: t('New password (min. 8)')}, confirm: label}); if (pw) void run(() => api.post('/api/auth/v1/password', {user: u.name, password: pw}), t('Password changed')); };
    const setLevel = (u: User, level: Level) => run(() => api.patch(`/api/auth/v1/users/${encodeURIComponent(u.name)}`, {level}), t('Level changed'));
    // task 262: an administrator removes another account's passkeys - the recovery when one is
    // lost; it ends that account's sessions like a password reset
    const removeKeys = async (u: User) => {
        if (!(await ask({title: t('Remove passkeys'), message: t('Remove all {n} passkeys of {name}? The account signs in with its password then, and its sessions end.', {n: String(u.webauthn_keys ?? 0), name: u.name}), confirm: t('Remove'), danger: true}))) return;
        await run(async () => {
            const r = await api.get<{keys: {id: string}[]}>(`/api/auth/v1/users/${encodeURIComponent(u.name)}/webauthn`);
            for (const k of r.keys) await api.del(`/api/auth/v1/users/${encodeURIComponent(u.name)}/webauthn/${encodeURIComponent(k.id)}`);
        }, t('Passkeys removed'));
    };
    const endSession = (s: Session) => run(() => fetch(`/api/auth/v1/sessions/${encodeURIComponent(s.id)}`, {method: 'DELETE', headers: REQUEST_HEADER}).then((r) => { if (!r.ok) throw new Error(`${r.status}`); }), t('Session ended'));
</script>

<SystemTitle />
{#if !users}
    <Loading {error} />
{:else}
    {#if auth.authOff}
        <div class="ol-warn">{t('Login is switched off (Authentication, below): everyone who reaches this system is an administrator. The accounts apply again once a login mode is chosen and occulited restarted.')}</div>
    {/if}
    {#if notice}<div class="ol-notice">{notice}</div>{/if}
    {#if auth.role === 'admin'}
        <!-- the accounts; the sections of task 132 follow the sessions. occulited task 69 (the maintainer:
             "page should be grouped into panels"): each section one panel under its heading, as the
             Backup page (task 59) -->
        <section data-section="accounts">
            <h2 id="accounts">{t('Accounts')}</h2>
            <div class="ol-panel">
            {#if !passwordLogin}
                <p class="ol-muted">{t('Password login is switched off (Authentication, below): every account signs in through {name}. Passwords can be set again once it is on.', {name: provider.name ?? 'SSO'})}</p>
            {/if}
            <!-- task 69: on a phone a row is its name, then level, sign-in and date, then its buttons
                 (inside the panel the table had too little room for its columns, B-105's way) -->
            <table class="ol-table ol-stack">
                <thead><tr><th>{t('Username')}</th><th>{t('Level')}<Help>{t('What an account may do, one of four: read sees everything and changes nothing; operate also switches and sets values; configure also edits names, rooms, functions and favorites; administer does everything - the System pages, accounts, tokens, pairing and deleting. The same ladder as an API token\'s RPC tier.')}</Help></th>{#if signInColumn}<th>{t('Sign-in')}</th>{/if}<th>{t('Created')}</th><th></th></tr></thead>
                <tbody>
                    {#each users as u (u.name)}
                        <tr>
                            <td class="ol-stack-line">{u.name}{u.must_change_password && u.password_set ? ` · ${t('must change password')}` : ''}{#if (u.webauthn_keys ?? 0) > 0}<div class="ol-muted us-keys" data-keys={u.webauthn_keys}>{t('{n} passkey(s)', {n: String(u.webauthn_keys)})}{#if u.webauthn_unusable}{' · '}{t('{n} cannot sign in', {n: String(u.webauthn_unusable)})}{/if}</div>{/if}</td>
                            <td data-label={t('Level')}>
                                <select class="hmm-select us-level" value={u.level ?? (u.role === 'admin' ? 'administer' : 'operate')} onchange={(e) => setLevel(u, (e.currentTarget as HTMLSelectElement).value as Level)} disabled={u.name === auth.user} aria-label={t('Level')} title={LEVELS.find((l) => l.id === (u.level ?? (u.role === 'admin' ? 'administer' : 'operate')))?.hint ? t(LEVELS.find((l) => l.id === (u.level ?? (u.role === 'admin' ? 'administer' : 'operate')))!.hint) : ''}>
                                    {#each LEVELS as l (l.id)}<option value={l.id}>{t(l.label)}</option>{/each}
                                </select>
                            </td>
                            {#if signInColumn}
                                <td class="us-signin" data-label={t('Sign-in')}>
                                    {u.password_set ? t('password') : t('provider only')}
                                    {#if u.last_provider_login}<div class="ol-muted">{t('last through the provider: {when}', {when: new Date(u.last_provider_login).toLocaleString()})}</div>{/if}
                                </td>
                            {/if}
                            <td data-label={t('Created')}>{new Date(u.created).toLocaleDateString()}</td>
                            <td class="ol-actions us-acts ol-stack-line">
                                {#if passwordLogin}<button class="hmm-button" onclick={() => resetPw(u)}>{u.password_set ? t('Reset password') : t('Set password')}</button>{/if}
                                {#if (u.webauthn_keys ?? 0) > 0}<button class="hmm-button" onclick={() => removeKeys(u)} data-remove-keys>{t('Remove keys')}</button>{/if}
                                <button class="hmm-button" onclick={() => deleteUser(u)} disabled={u.name === auth.user}>{t('Delete')}</button>
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
            <div style="margin-top:10px">
                <Disclosure label={t('Add user')} title={t('Create user')} bind:open={addUserOpen}>
                    <div class="ol-toolbar">
                        <input class="hmm-input" placeholder={t('Username')} bind:value={newName} autocomplete="off" />
                        {#if passwordLogin}
                            <input class="hmm-input" type="password" placeholder={provider.enabled ? t('Password (optional: empty = provider only)') : t('Password (min. 8)')} bind:value={newPw} autocomplete="new-password" />
                        {/if}
                        <select class="hmm-select us-level" bind:value={newLevel} aria-label={t('Level')}>{#each LEVELS as l (l.id)}<option value={l.id}>{t(l.label)}</option>{/each}</select>
                        <button class="hmm-button primary" onclick={createUser} disabled={!canCreate}>{t('Create user')}</button>
                    </div>
                    {#if provider.enabled}
                        <p class="ol-muted us-hint">{t('The name must be exactly what {name} sends as the user name claim: that is how a provider login finds its account here.', {name: provider.name ?? 'SSO'})}</p>
                    {/if}
                </Disclosure>
            </div>
            </div>
        </section>
    {/if}

    <section data-section="sessions">
        <h2 id="sessions">{t('Sessions')}</h2>
        <!-- B-105: on a phone a session is its id and user, then when and from where, then End session -->
        <div class="ol-panel">
        <table class="ol-table ol-stack">
            <thead><tr><th>{t('Session')}</th><th>{t('User')}</th><th>{t('Last seen')}</th><th>{t('From')}</th><th></th></tr></thead>
            <tbody>
                {#each sessions as s (s.id)}
                    <tr>
                        <td class="hmm-mono">{s.id}{s.id === current ? ` (${t('this one')})` : ''}</td>
                        <td>{s.user}</td>
                        <td class="ol-stack-line" data-label={t('Last seen')}>{new Date(s.last_seen).toLocaleString()}</td>
                        <td class="ol-muted ol-stack-line us-from" data-label={t('From')}>{s.remote ?? ''} {s.agent ? `· ${s.agent.slice(0, 40)}` : ''}</td>
                        <td class="ol-actions ol-stack-line">{#if s.id !== current}<button class="hmm-button" onclick={() => endSession(s)}>{t('End session')}</button>{/if}</td>
                    </tr>
                {/each}
            </tbody>
        </table>
        </div>
    </section>

    {#if auth.role === 'admin'}
        <AuthSettings />
        <!-- the maintainer, 2026-09-19: the API tokens are the Remote access page's now -->
        <p class="ol-muted" data-tokens-link><a href="/system/remote-access#api-tokens" use:link>{t('API tokens: Remote access')}</a></p>
    {/if}
{/if}

<style>
    /* task 78: the level select sizes to its widest option (konfigurieren); on a phone that made
       the table wider than the window, so it is capped and the text may be cut */
    .us-level { max-width: 5.5em; min-width: 0; }

    td.us-signin .ol-muted, .us-keys { font-size: var(--hmm-font-size-small); }
    .us-hint { margin: 6px 0 0; font-size: var(--hmm-font-size-small); }
    /* an IPv6 address and a user agent have no break of their own */
    @media (max-width: 700px) {
        td.us-from { overflow-wrap: anywhere; }
        td.us-acts { white-space: normal; }
    }
</style>
