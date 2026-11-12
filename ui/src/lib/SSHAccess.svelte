<script lang="ts">
    import {dateTime} from './datefmt';
    // Task 185: SSH on the Remote access page (it was on the Network page) - the switch, root's
    // password, root's keys and the sessions open now. A pasted key goes into occulited's section
    // of root's authorized_keys; the keys outside it are listed and left alone. Adding a key and
    // setting root's password ask for the user's own password every time (lib/confirm.ts).
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import SectionHead from './SectionHead.svelte';
    import {confirmTicket, returned, type ConfirmTexts} from './confirm';

    interface Props {
        admin: boolean;
    }
    let {admin}: Props = $props();

    interface SSH { enabled: boolean; running: boolean; key_only?: boolean }
    interface Key { type: string; bits?: number; comment?: string; fingerprint: string; options?: string }
    interface Session { id: number; user: string; tty?: string; from?: string; port?: number; since?: string; method?: string; key_type?: string; key_fingerprint?: string; own?: boolean }

    const KEYS = '/api/system/v1/ssh/keys';
    const PASSWORD = '/api/system/v1/ssh/password';
    // a pasted key waits here while the user confirms at the identity provider; a password never does
    const PENDING = 'ol.ssh.pending';

    let ssh = $state<SSH | null>(null);
    let keys = $state<{managed: Key[]; other: Key[]}>({managed: [], other: []});
    let sessions = $state<Session[]>([]);
    let error = $state('');
    let notice = $state('');
    // task 223: the panel whose action the error or the notice answers
    type Where = 'access' | 'keys' | 'password';
    let at = $state<Where>('access');
    let busy = $state('');
    let newKey = $state('');
    let rootPw = $state('');
    let rootPw2 = $state('');
    // back from the identity provider for the password: the ticket waits for the password typed again
    let pwTicket = $state('');

    async function load() {
        try {
            ssh = await api.get<SSH>('/api/system/v1/ssh');
        } catch {
            ssh = null;
        }
        try {
            keys = await api.get<{managed: Key[]; other: Key[]}>(KEYS);
        } catch (e) {
            at = 'keys';
            error = (e as Error).message;
        }
        await loadSessions();
    }
    async function loadSessions() {
        try {
            sessions = (await api.get<{sessions: Session[]}>('/api/system/v1/ssh/sessions')).sessions;
        } catch {
            /* the next round tries again */
        }
    }

    function texts(title: string, what: string): ConfirmTexts {
        return {
            title,
            message: what,
            provider: t('{what} It asks who you are every time: you sign in at the identity provider once more and come back here.', {what}),
            impossible: t('This account has no password and no identity provider is configured, so it cannot confirm this.'),
        };
    }
    const keyTexts = () => texts(t('Add an SSH key'), t('A key in root\'s authorized_keys logs in as root without a password.'));
    const pwTexts = () => texts(t('Set root\'s password'), t('The new password logs in as root over SSH.'));

    async function toggle() {
        if (!ssh) return;
        if (ssh.enabled && !(await ask(t('Disable SSH? A running session stays open; new logins are refused and port 22 is closed.')))) return;
        busy = 'ssh';
        at = 'access';
        error = notice = '';
        try {
            ssh = await api.put<SSH>('/api/system/v1/ssh', {enabled: !ssh.enabled});
            notice = ssh.enabled ? t('SSH enabled.') : t('SSH disabled.');
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function addKey(ticket?: string, key = newKey) {
        at = 'keys';
        error = notice = '';
        busy = 'key';
        try {
            // without a ticket first: a key that is not taken, or is there already, is said before
            // the password is asked for
            let k: Key | null = null;
            if (!ticket) {
                try {
                    k = await api.post<Key>(KEYS, {key});
                } catch (e) {
                    if (!(e instanceof ApiError && e.status === 403)) throw e;
                    const tk = await confirmTicket(KEYS, keyTexts(), () => keep({key}));
                    if (!tk) return;
                    ticket = tk;
                }
            }
            k ??= await api.postWith<Key>(KEYS, {key}, {'X-Occulite-Confirm': ticket!});
            notice = t('Key added: {name}', {name: k.comment || k.fingerprint});
            newKey = '';
            await load();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    function keep(v: {key?: string; password?: boolean}) {
        try {
            sessionStorage.setItem(PENDING, JSON.stringify(v));
        } catch {
            /* no storage: back from the provider, the page only shows that it confirmed */
        }
    }

    async function removeKey(k: Key) {
        if (!(await ask({title: t('Remove the key'), message: t('Remove the key {name}? It no longer logs in as root.', {name: k.comment || k.fingerprint}), confirm: t('Remove')}))) return;
        at = 'keys';
        error = notice = '';
        busy = 'key';
        try {
            await api.del(`${KEYS}?fingerprint=${encodeURIComponent(k.fingerprint)}`);
            notice = t('Key removed: {name}', {name: k.comment || k.fingerprint});
            await load();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    // task 245: root's password refused, a key the only login - refused by the system while root
    // has no key, and the last key cannot be removed while it is on
    const keyCount = $derived(keys.managed.length + keys.other.length);
    async function setKeyOnly(on: boolean, box: HTMLInputElement) {
        at = 'password';
        error = notice = '';
        busy = 'keyonly';
        try {
            ssh = await api.put<SSH>('/api/system/v1/ssh/key-only', {on});
            notice = on ? t('Root logs in over SSH with a key only now; the password is refused.') : t('Root may log in over SSH with the password again.');
        } catch (e) {
            error = (e as Error).message;
            // the box goes back: the view's value did not change, so nothing redraws it
            box.checked = !on;
            await load();
        } finally {
            busy = '';
        }
    }

    async function setPassword() {
        at = 'password';
        if (rootPw !== rootPw2) {
            error = t('The two passwords differ.');
            return;
        }
        error = notice = '';
        busy = 'pw';
        try {
            let ticket = pwTicket;
            if (!ticket) {
                const tk = await confirmTicket(PASSWORD, pwTexts(), () => keep({password: true}));
                if (!tk) return;
                ticket = tk;
            }
            await api.postWith(PASSWORD, {password: rootPw}, {'X-Occulite-Confirm': ticket});
            rootPw = rootPw2 = pwTicket = '';
            notice = t('Root password set.');
        } catch (e) {
            error = (e as Error).message;
            pwTicket = '';
        } finally {
            busy = '';
        }
    }

    async function end(s: Session) {
        const message = s.own
            ? t('End the session of {user} from {from}? It comes from this browser\'s address - it may be your own.', {user: s.user, from: s.from ?? '?'})
            : t('End the session of {user} from {from}? Whatever runs in it stops.', {user: s.user, from: s.from ?? '?'});
        if (!(await ask({title: t('End the SSH session'), message, confirm: t('End')}))) return;
        at = 'access';
        error = notice = '';
        busy = 'session';
        try {
            await api.del(`/api/system/v1/ssh/sessions/${s.id}`);
            notice = t('Session ended.');
            await loadSessions();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    const since = (s?: string) => (s ? dateTime(s) : '');
    const keyLabel = (k: Key) => `${k.type}${k.bits ? ` · ${k.bits}` : ''}`;

    onMount(() => {
        // back from the identity provider: the pasted key goes in, or the password form waits
        const back = returned();
        let pending: {key?: string; password?: boolean} = {};
        try {
            pending = JSON.parse(sessionStorage.getItem(PENDING) ?? '{}') as typeof pending;
            sessionStorage.removeItem(PENDING);
        } catch {
            /* nothing kept */
        }
        if (back) at = pending.password ? 'password' : 'keys';
        if (back && 'refused' in back) error = t('The confirmation at the identity provider was refused: {reason}', {reason: back.refused});
        if (back && 'ticket' in back) {
            if (pending.key) void addKey(back.ticket, pending.key);
            else if (pending.password) {
                pwTicket = back.ticket;
                notice = t('Confirmed. Enter root\'s new password now.');
            }
        }
        void load();
        const timer = setInterval(() => {
            if (!document.hidden) void loadSessions();
        }, 15000);
        return () => clearInterval(timer);
    });
</script>

<!-- openccu-lite task 223 (the maintainer, 2026-09-24): "split ssh panel. ssh as heading, then 3
     panels below: 1) enable/disable and session display 2) Keys, Add a key 3) Root Password". What an
     action answers is said in the panel it was done in. -->
<SectionHead id="ssh" title="SSH" help={t('The shell is root. The firewall opens port 22 for the local networks only while SSH is enabled.')} />

{#snippet said(where: Where)}
    {#if error && at === where}<div class="ol-notice error" data-notice="ssh-error">{error}</div>{/if}
    {#if notice && at === where}<div class="ol-notice" data-notice="ssh-notice">{notice}</div>{/if}
{/snippet}

<section class="ol-panel" data-panel="ssh-access">
    <h3>{t('Access')}</h3>
    {@render said('access')}
    {#if ssh}
        <div class="ssh-state">
            <span data-ssh="state"><span class="ol-dot" class:ok={ssh.running}></span>{ssh.enabled ? (ssh.running ? t('enabled and running') : t('enabled, not running')) : t('disabled')}</span>
            {#if admin}<button class="hmm-button" onclick={toggle} disabled={busy !== ''} data-ssh="toggle">{ssh.enabled ? t('Disable SSH') : t('Enable SSH')}</button>{/if}
        </div>
    {/if}
    <h4 class="ssh-sub">{t('Sessions')}</h4>
    {#if sessions.length === 0}
        <p class="ol-muted ssh-none" data-ssh="no-sessions">{t('No SSH session is open.')}</p>
    {:else}
        <table class="ol-table ol-stack ssh-table" data-ssh="sessions">
            <thead><tr><th>{t('User')}</th><th>{t('From')}</th><th>{t('Since')}</th><th>{t('Kind')}</th><th>{t('Login')}</th>{#if admin}<th></th>{/if}</tr></thead>
            <tbody>
                {#each sessions as s (s.id)}
                    <tr data-session={s.id}>
                        <td data-label={t('User')}>{s.user}</td>
                        <td data-label={t('From')} class="hmm-mono">{s.from ?? '?'}{#if s.port}<span class="ol-muted">:{s.port}</span>{/if}{#if s.own}{' '}<span class="ol-badge" title={t('The address this browser reaches the system from')}>{t('this browser\'s address')}</span>{/if}</td>
                        <td data-label={t('Since')}>{since(s.since)}</td>
                        <td data-label={t('Kind')}>{s.tty ? t('terminal ({tty})', {tty: s.tty}) : t('command or file copy')}</td>
                        <td data-label={t('Login')}>{s.method === 'publickey' ? t('key') : s.method === 'password' ? t('password') : (s.method ?? '')}{#if s.key_fingerprint}{' '}<span class="hmm-mono ol-muted ssh-fp" title={s.key_fingerprint}>{s.key_type} {s.key_fingerprint}</span>{/if}</td>
                        {#if admin}<td class="ssh-end"><button class="hmm-button" onclick={() => end(s)} disabled={busy !== ''} data-ssh="end">{t('End')}</button></td>{/if}
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
</section>

<section class="ol-panel" data-panel="ssh-keys">
    <h3 id="ssh-keys">{t('Keys')}<Help>{t("The public keys that log in as root. The ones added here are kept in a section of root's authorized_keys that comment lines mark as this page's; the lines outside it are shown and left as they are.")}</Help></h3>
    {@render said('keys')}
    {#if keys.managed.length === 0 && keys.other.length === 0}
        <p class="ol-muted ssh-none" data-ssh="no-keys">{t('No key logs in as root.')}</p>
    {:else}
        <table class="ol-table ol-stack ssh-table" data-ssh="keys">
            <thead><tr><th>{t('Name')}</th><th>{t('Type')}</th><th>{t('Fingerprint')}</th><th></th></tr></thead>
            <tbody>
                <!-- by position: a file edited by hand can hold one key on two lines -->
                {#each keys.managed as k, i (i)}
                    <tr data-key="managed">
                        <td data-label={t('Name')}>{k.comment || '–'}</td>
                        <td data-label={t('Type')} class="hmm-mono">{keyLabel(k)}</td>
                        <td data-label={t('Fingerprint')} class="hmm-mono ssh-fp">{k.fingerprint}</td>
                        <td class="ssh-end">{#if admin}<button class="hmm-button" onclick={() => removeKey(k)} disabled={busy !== ''} data-ssh="remove">{t('Remove')}</button>{/if}</td>
                    </tr>
                {/each}
                {#each keys.other as k, i (i)}
                    <tr data-key="other">
                        <td data-label={t('Name')}>{k.comment || '–'}{#if k.options}{' '}<span class="hmm-mono ol-muted">{k.options}</span>{/if}</td>
                        <td data-label={t('Type')} class="hmm-mono">{keyLabel(k)}</td>
                        <td data-label={t('Fingerprint')} class="hmm-mono ssh-fp">{k.fingerprint}</td>
                        <td class="ol-muted">{t('added outside this page')}</td>
                    </tr>
                {/each}
            </tbody>
        </table>
    {/if}
    {#if admin}
        <div class="ssh-add">
            <label for="ssh-new-key">{t('Add a key')}<Help>{t('One public key, as the .pub file holds it: ssh-ed25519, ecdsa-sha2-nistp256/384/521, a security key (sk-…) or ssh-rsa of 3072 bits and more. Its comment becomes its name. Options such as from= or command= are not taken here.')}</Help></label>
            <textarea id="ssh-new-key" class="hmm-input hmm-mono" rows="3" bind:value={newKey} placeholder="ssh-ed25519 AAAA… name@laptop" spellcheck="false" autocomplete="off"></textarea>
            <div class="ol-actions"><button class="hmm-button primary" onclick={() => addKey()} disabled={busy !== '' || !newKey.trim()} data-ssh="add">{t('Add key')}</button></div>
        </div>
    {/if}
</section>

{#if admin}
    <section class="ol-panel" data-panel="ssh-password">
        <h3 id="ssh-password">{t('Root password')}</h3>
        {@render said('password')}
        {#if ssh?.key_only !== undefined}
            <label class="ssh-keyonly" data-ssh="key-only">
                <input type="checkbox" checked={ssh.key_only} disabled={busy !== '' || (!ssh.key_only && keyCount === 0)} onchange={(e) => void setKeyOnly((e.currentTarget as HTMLInputElement).checked, e.currentTarget as HTMLInputElement)} />
                {t('Only key login (no password)')}<Help>{t('SSH then refuses the password for root and takes a key of the Keys panel only. It can be switched on only while root has a key, and the last key cannot be removed while it is on. The web interface and the console stay a way in.')}</Help>
            </label>
            {#if !ssh.key_only && keyCount === 0}<p class="ol-muted ssh-keyonly-hint" data-ssh="key-only-needs-key">{t('Add a key first: only key login needs one.')}</p>{/if}
        {/if}
        <div class="ol-form ssh-pw">
            <label>{t('New password')} <input class="hmm-input" type="password" bind:value={rootPw} autocomplete="new-password" placeholder={t('min. 8 characters')} data-ssh="pw" /></label>
            <label>{t('Repeat')} <input class="hmm-input" type="password" bind:value={rootPw2} autocomplete="new-password" data-ssh="pw2" /></label>
            <div class="ol-form-buttons"><button class="hmm-button" onclick={setPassword} disabled={busy !== '' || rootPw.length < 8 || !rootPw2} data-ssh="set-pw">{t('Set')}</button></div>
        </div>
    </section>
{/if}

<style>
    .ssh-keyonly { display: inline-flex; align-items: center; gap: 8px; margin: 0 0 10px; }
    .ssh-keyonly-hint { margin: -6px 0 10px; font-size: var(--hmm-font-size-small); }
    .ssh-state { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin: 0 0 4px; }
    .ssh-sub { font-size: var(--hmm-font-size-small); font-weight: 600; color: var(--hmm-fg-muted); text-transform: uppercase; letter-spacing: 0.04em; margin: 14px 0 6px; }
    .ssh-none { margin: 0; }
    .ssh-table { margin: 0; }
    .ssh-end { text-align: right; white-space: nowrap; }
    .ssh-fp { overflow-wrap: anywhere; font-size: var(--hmm-font-size-small); }
    .ssh-add { display: flex; flex-direction: column; gap: var(--ol-label-gap); max-width: 720px; margin: 14px 0 0; }
    .ssh-add textarea { width: 100%; resize: vertical; }
    .ssh-add .ol-actions { margin-top: 2px; }
    .ssh-pw { max-width: 520px; margin-bottom: 0; }
</style>
