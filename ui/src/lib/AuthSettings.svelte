<script lang="ts">
    /*
     * Task 132 (D-87): how the box authenticates, a section of System → Users - on the Security page
     * before (D-51), on Settings before that (task 29). The page shows it to an administrator only.
     */
    import {onMount} from 'svelte';
    import {api} from './api';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Loading from './Loading.svelte';
    import Help from './Help.svelte';
    import {scrollToAnchor} from './anchor';
    import {authConfigBody, durationMinutes, sessionLengths} from './authconfig';
    import OIDCTrust from './OIDCTrust.svelte';

    // ---- authentication ---------------------------------------------------------------------
    // task 19 (D-53, D-54): a provider login is the account of the same name here, with the
    // account's role - no group mapping any more. Password login beside the provider is a
    // switch; it can only be turned off from a session that came through the provider (so the
    // one who turns it off has just proven the provider signs them in), and the page says what
    // the way back is before it lets that happen: the console.
    interface AuthConfig {
        mode: 'local' | 'oidc' | 'off';
        modes: string[];
        name: string;
        issuer: string;
        client_id: string;
        client_secret_set: boolean;
        username_claim: string;
        scopes: string;
        password_login: boolean;
        running: string;
        restart_required: boolean;
        /** task 262: the sessions' idle timeout and lifetime, Go durations ("30m", "12h") */
        session_idle?: string;
        session_max?: string;
        /** occulited task 70: the login page's options (absent from an older daemon) */
        hide_password_form?: boolean;
        provider_login_seen?: boolean;
        passkey_offer?: 'autofill' | 'prompt' | 'off';
    }
    let cfg = $state<AuthConfig | null>(null);
    let authError = $state('');
    let authNotice = $state('');
    let authBusy = $state(false);
    let secret = $state('');
    let authUnavailable = $state(false);
    // the switch as stored, and how this session was opened (the state route's `method`)
    let storedPasswordLogin = $state(true);
    let sessionMethod = $state('');
    const viaProvider = $derived(sessionMethod === 'oidc');
    const turningOff = $derived(!!cfg && cfg.mode === 'oidc' && storedPasswordLogin && !cfg.password_login);
    const providerName = $derived(cfg?.name || 'SSO');
    // task 262: the session lengths as the page edits them - minutes idle, hours lifetime - from
    // and to the API's durations (lib/authconfig)
    let idleMinutes = $state(30);
    let maxHours = $state(12);

    async function loadAuth() {
        try {
            cfg = await api.get<AuthConfig>('/api/auth/v1/config');
            storedPasswordLogin = cfg.password_login !== false;
            idleMinutes = durationMinutes(cfg.session_idle, 30);
            maxHours = durationMinutes(cfg.session_max, 12 * 60) / 60;
            try { sessionMethod = (await api.get<{method?: string}>('/api/auth/v1/state')).method ?? ''; } catch { /* an older daemon */ }
        } catch (e) {
            const msg = (e as Error).message;
            if (msg.includes('501') || msg.includes('no-config') || msg.includes('configuration file')) authUnavailable = true;
            else authError = msg;
        }
    }
    // the break-glass, said before the switch goes off (the console is the only way back in
    // while the provider is down); the paragraphs are separated by a blank line
    async function confirmPasswordLoginOff(): Promise<boolean> {
        const message = [
            t('With password login off, {name} is the only way into the web interface. If the provider is down, misconfigured or mid-upgrade, nobody gets in through the browser.', {name: providerName}),
            t('The way back is SSH or the console: `occulited auth password-login on` turns this switch back on, `occulited passwd <user>` sets a password. Keep that access.'),
            t('Sessions that are open stay open. Switch password login off?'),
        ].join('\n\n');
        return ask({title: t('Switch password login off'), message, confirm: t('Switch off'), danger: true});
    }
    async function saveAuth() {
        if (!cfg) return;
        if (cfg.mode === 'off' && !(await ask(t('Switch the login off? Everyone who can reach this system is then an administrator - on the web UI, the API and every addon page. Only for a system alone on a trusted network.')))) return;
        if (turningOff && !(await confirmPasswordLoginOff())) return;
        authBusy = true;
        authNotice = '';
        try {
            // B-206: the writable fields only - the API refuses GET's read-only ones
            cfg = await api.put<AuthConfig>('/api/auth/v1/config', authConfigBody(cfg, secret, sessionLengths(idleMinutes, maxHours)));
            storedPasswordLogin = cfg.password_login !== false;
            idleMinutes = durationMinutes(cfg.session_idle, 30);
            maxHours = durationMinutes(cfg.session_max, 12 * 60) / 60;
            secret = '';
            authNotice = cfg.restart_required ? t('Saved. The change takes effect when occulited is restarted.') : t('Saved.');
        } catch (e) {
            authNotice = (e as Error).message;
        } finally {
            authBusy = false;
        }
    }
    async function restart() {
        authBusy = true;
        try {
            await api.post('/api/system/v1/services/occulited/restart', {});
        } catch {
            /* the connection drops with the restart; the reload below finds the new daemon */
        }
        setTimeout(() => location.reload(), 4000);
    }
    const origin = $derived(typeof location !== 'undefined' ? location.origin : 'http://<box>');

    onMount(async () => {
        await loadAuth();
        scrollToAnchor('authentication');
    });
</script>

<section class="ol-section" data-section="authentication">
    <h2 id="authentication">{t('Authentication')}</h2>
    <!-- occulited task 69: the section is one panel; the session lengths and the provider's trusted
         certificates are sub-blocks in it, set off by a hairline (task 59) -->
    <div class="ol-panel">
    {#if authUnavailable}
        <p class="ol-muted">{t('This daemon runs without a configuration file; the mode cannot be changed here.')}</p>
    {:else if !cfg}
        <Loading error={authError} />
    {:else}
        {#if cfg.restart_required}
            <div class="ol-warn">{t('The stored mode is {mode}, the running daemon is on {running}: restart occulited to apply it.', {mode: cfg.mode, running: cfg.running})} <button class="hmm-button" onclick={restart} disabled={authBusy}>{t('Restart occulited')}</button></div>
        {/if}
        {#if authNotice}<div class="ol-notice">{authNotice}</div>{/if}
        <div class="ol-radios">
            <!-- task 51: what an option means is behind its ?; the warning of *Off* stays on the page -->
            <label><input type="radio" bind:group={cfg.mode} value="local" /> <strong>{t('Local users')}</strong><Help>{t('name and password, managed on System → Users')}</Help></label>
            <label><input type="radio" bind:group={cfg.mode} value="oidc" /> <strong>{t('OpenID Connect')}</strong><Help>{t('an external login (authentik, Keycloak, Authelia, Zitadel) next to the local users')}</Help></label>
            <label><input type="radio" bind:group={cfg.mode} value="off" /> <strong>{t('Off')}</strong> <span class="ol-warn">{t('no login at all: everyone who reaches the system is an administrator')}</span></label>
        </div>
        {#if cfg.mode === 'oidc'}
            <div class="ol-form ol-secform">
                <label><span>{t('Button label')}</span><input class="hmm-input" bind:value={cfg.name} placeholder="authentik" /></label>
                <label><span>{t('Issuer')}</span><input class="hmm-input" bind:value={cfg.issuer} placeholder="https://auth.example.org/application/o/openccu-lite/" /></label>
                <label><span>{t('Client ID')}</span><input class="hmm-input hmm-mono" bind:value={cfg.client_id} autocomplete="off" /></label>
                <label><span>{t('Client secret')}</span><input class="hmm-input hmm-mono" type="password" bind:value={secret} placeholder={cfg.client_secret_set ? t('(set — leave empty to keep)') : t('(empty for a public client, PKCE only)')} autocomplete="new-password" /></label>
                <label><span>{t('Username claim')}</span><input class="hmm-input hmm-mono" bind:value={cfg.username_claim} placeholder="preferred_username" /></label>
                <label><span>{t('Permissions (scopes)')}</span><input class="hmm-input hmm-mono" bind:value={cfg.scopes} placeholder="openid profile email" /></label>
            </div>
            <p class="ol-muted ol-oidc-note">{t('A login through {name} is the account of exactly that user name here (System → Users), with the role it has here; nothing is created from a provider login.', {name: providerName})}</p>
            <!-- the switch: off only from a session that came through the provider, and the page
                 says the way back before it lets that happen (saveAuth asks) -->
            <div class="ol-checks">
                <label><input type="checkbox" bind:checked={cfg.password_login} disabled={storedPasswordLogin && !viaProvider} /> <strong>{t('Password login')}</strong><Help>{t('the name-and-password form on the login page beside the provider button; off, the provider is the only way into the web interface and the console is the way back')}</Help></label>
                <!-- occulited task 70: the form behind a link on the login page, once the provider has
                     signed someone in; the password login itself stays -->
                {#if cfg.hide_password_form !== undefined}
                    <label class="ol-hidepw"><input type="checkbox" bind:checked={cfg.hide_password_form} disabled={!cfg.password_login || (!cfg.provider_login_seen && !cfg.hide_password_form)} data-hide-password-form /> <strong>{t('Hide the name-and-password login')}</strong><Help>{t('The login page shows the button of {name} (and the passkey\'s) and puts the name-and-password form behind a small link "Sign in with a password", which is always there - so a provider that is down never locks anyone out. Scripts and the API keep signing in with a password.', {name: providerName})}</Help></label>
                    {#if !cfg.provider_login_seen && !cfg.hide_password_form}<div class="ol-muted sub" data-hide-password-wait>{t('Possible once an account has signed in through {name}.', {name: providerName})}</div>{/if}
                {/if}
                {#if storedPasswordLogin && !viaProvider}
                    <div class="ol-muted sub">{t('Can only be switched off from a session that came through {name}: sign in that way first, so you have proven the provider signs you in.', {name: providerName})}</div>
                {:else if !cfg.password_login}
                    <div class="ol-warn sub">{t('With password login off, {name} is the only way into the web interface. If the provider is down, the way back is SSH or the console: `occulited auth password-login on` turns this switch back on, `occulited passwd <user>` sets a password.', {name: providerName})}</div>
                {/if}
            </div>
            <!-- task 51: the four steps were a bordered box under the fields. Its title stays where the
                 box was, as the trigger of a popup with the steps (the badge form of Help): a
                 reader setting up OIDC still sees that there is a guide, and nobody else reads it. -->
            <div class="ol-oidc-guide">
                <Help>
                    {#snippet trigger()}{t('Setting it up in authentik')}{/snippet}
                    <ol>
                        <li>{t('Applications → Providers → Create: OAuth2/OpenID Provider, client type Confidential. Redirect URI:')} <code class="hmm-mono">{origin}/api/auth/v1/oidc/callback</code> {t('(register http and https if you use both; the scheme follows how the browser reached the system). Signing key: any.')}</li>
                        <li>{t('Applications → Applications → Create, bound to that provider. Copy the Client ID and the Client Secret of the provider into the fields above.')}</li>
                        <li>{t('The Issuer is the "OpenID Configuration Issuer" shown on the provider, of the form')} <code class="hmm-mono">https://auth.example.org/application/o/&lt;slug&gt;/</code>.</li>
                        <li>{t('Create the account here (System → Users) under exactly the name authentik sends as the username claim — the login name, with preferred_username. Its role is the account\'s; leave the password empty if it should sign in through authentik only.')}</li>
                    </ol>
                </Help>
            </div>
        {/if}
        {#if cfg.mode !== 'off'}
            <!-- task 262: the session lengths; the defaults are ASVS Level 2's (30 min idle, 12 h) -->
            <div class="ol-sessions ol-subblock" data-section="session-lengths">
                <h3>{t('Sessions')}<Help>{t('A signed-in browser stays signed in until it has been idle for the first time, or in any case after the second - then it signs in again (a phone on the wall or a household tablet more often with short values). The defaults, 30 minutes and 12 hours, are the values ASVS Level 2 asks of a system that controls a home; the values before, 24 hours and 30 days, can be set back here. A change applies to running sessions at their next request.')}</Help></h3>
                <div class="ol-form ol-secform">
                    <label><span>{t('Sign out after idle (minutes)')}</span><input class="hmm-input" type="number" min="5" max="43200" step="1" bind:value={idleMinutes} data-session-idle /></label>
                    <label><span>{t('Sign out at the latest after (hours)')}</span><input class="hmm-input" type="number" min="1" max="2160" step="1" bind:value={maxHours} data-session-max /></label>
                    <!-- occulited task 70: how the login page offers a passkey -->
                    {#if cfg.passkey_offer !== undefined}
                        <label><span>{t('Offer a passkey on the login page')}<Help>{t('In the name field: the browser offers the passkey in the user name\'s autofill, without a dialog. Ask at once: the passkey dialog opens by itself when the login page opens (once per browser tab; cancelled, the page stays as it is). Off: only the button "Sign in with a passkey".')}</Help></span>
                            <select class="hmm-select" bind:value={cfg.passkey_offer} data-passkey-offer>
                                <option value="autofill">{t('in the name field (autofill)')}</option>
                                <option value="prompt">{t('ask at once')}</option>
                                <option value="off">{t('Off')}</option>
                            </select>
                        </label>
                    {/if}
                </div>
            </div>
        {/if}
        <div class="ol-actions" style="margin-top:10px">
            <button class="hmm-button primary" onclick={saveAuth} disabled={authBusy}>{t('Save')}</button>
        </div>
        <!-- task 230: the provider's certificate, trusted by upload or paste; applies at once -->
        {#if cfg.mode === 'oidc'}<div class="ol-subblock"><OIDCTrust issuer={cfg.issuer.trim()} /></div>{/if}
    {/if}
    </div>
</section>

<style>
    .ol-radios { display: flex; flex-direction: column; gap: 8px; margin: 8px 0 12px; }
    .ol-radios label { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .ol-secform { display: grid; grid-template-columns: max-content minmax(240px, 520px); gap: 8px 12px; align-items: center; margin-bottom: 12px; }
    .ol-secform label { display: contents; }
    .ol-secform input { width: 100%; }
    /* B-105: the German labels' column and the 240 px field were 458 px on a 412 px phone; there the
       label stands above its field - B-112: with the shared label gap (app.css), not 3 px */
    @media (max-width: 700px) {
        .ol-secform { grid-template-columns: minmax(0, 1fr); gap: var(--ol-label-gap) 0; }
        .ol-secform input { margin-bottom: 7px; }
    }
    .ol-oidc-guide { margin: 0 0 4px; }
    .ol-sessions { margin-bottom: 4px; }
    .ol-hidepw { display: flex; gap: 8px; align-items: baseline; margin-top: 6px; }
    /* task 69: the trusted certificates as a sub-block: the hairline is its top edge */
    .ol-subblock > :global(.ot) { margin-top: 0; }
    .ol-sessions h3 { margin: 0 0 8px; font-size: 1em; }
    .ol-oidc-note { margin: 0 0 8px; max-width: 760px; }
    .ol-checks { display: flex; flex-direction: column; gap: 8px; margin: 8px 0 4px; max-width: 760px; }
    .ol-checks label { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .ol-checks .sub { margin-left: 26px; font-size: var(--hmm-font-size-small); }
</style>
