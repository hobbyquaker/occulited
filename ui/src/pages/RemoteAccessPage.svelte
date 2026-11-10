<script lang="ts">
    /*
     * Task 143 (D-95): System → Remote access. Classic CCU RPC - the ports a client configured for a
     * CCU3 or OpenCCU talks to (homematic-manager, node-red-contrib-ccu, ioBroker, Home Assistant,
     * FHEM, openHAB), plain and TLS as two switches, with no authentication or with a user name and
     * password of their own, checked by lighttpd as on a CCU (realm theRealm). The firewall is not
     * set here: every open port has classic RPC's rule on the Firewall page (task 157); the page
     * says what the firewall does with each open port.
     * lite-rpc (task 77, D-115; always on since D-117): the web port's /api/rpc/v1 - requests and
     * the event stream with API tokens - with its limits as read-only values, the open streams and
     * the API tokens below it, the RPC trace last (task 224).
     *
     * openccu-lite task 223 (the maintainer, 2026-09-24) regrouped the page under three headings:
     * Classic RPC (the switches, the authentication, the firewall panel of the access points'
     * style instead of the rule table, the registered clients), lite-rpc (its panel over the full
     * width, the open streams, the API tokens - occulited B-9) and SSH in three panels. Control
     * without a login went to the Settings page (lib/PublicControl.svelte).
     */
    import {onMount, tick} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type LiteRPCView} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import {auth} from '../lib/auth.svelte';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import CopyButton from '../lib/CopyButton.svelte';
    import {bringIntoView} from '../lib/intoview';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import Subscribers from '../lib/Subscribers.svelte';
    import SSHAccess from '../lib/SSHAccess.svelte';
    import APITokens from '../lib/APITokens.svelte';
    import StreamClients from '../lib/StreamClients.svelte';
    import RPCTrace from '../lib/RPCTrace.svelte';
    import SectionHead from '../lib/SectionHead.svelte';
    import FirewallVerdicts, {type PortVerdict} from '../lib/FirewallVerdicts.svelte';
    import WarnEdge from '../lib/WarnEdge.svelte';

    interface Classic { plain: boolean; tls: boolean; auth: 'none' | 'password'; user?: string; password_set: boolean }
    interface Port { port: number; tls: boolean; interface: string; process: string; backend: number; open: boolean; running: boolean }
    interface Rule { id: string; port: number; proto: string; source: string; family: string; target: string; comment?: string }
    interface Firewall { ports: PortVerdict[]; hint: 'closed' | 'open' | 'blocked' }
    interface View { classic: Classic; ports: Port[]; hs485d: boolean; rules: Rule[]; firewall?: Firewall | null; lite: LiteRPCView; password?: string }

    const MIN = 12;
    let view = $state<View | null>(null);
    let error = $state('');
    let notice = $state('');
    let busy = $state('');
    // the switches as edited, applied with Save
    let plain = $state(false);
    let tls = $state(false);
    let authMode = $state<'none' | 'password'>('none');
    // the pair's form: shown while no pair is set, or after Change
    let editPair = $state(false);
    let user = $state('ccu');
    let pw = $state('');
    let pw2 = $state('');
    let generated = $state('');
    let generatedEl = $state<HTMLElement | null>(null);
    let generatedNotice = $state<HTMLElement | null>(null);
    const admin = $derived(auth.role === 'admin');
    function take(v: View) {
        view = v;
        plain = v.classic.plain;
        tls = v.classic.tls;
        authMode = v.classic.auth;
        if (v.classic.user) user = v.classic.user;
    }
    async function load() {
        try {
            take(await api.get<View>('/api/system/v1/remote-access'));
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    const life = pageLife();
    onMount(() => {
        void load();
        // task 177: back from another page - the settings as they are now, unless a change waits
        return life.onReturn(() => {
            if (!dirty) void load();
        });
    });

    const dirty = $derived(!!view && (plain !== view.classic.plain || tls !== view.classic.tls || authMode !== view.classic.auth));
    // password auth needs the pair before it can be saved
    const needsPair = $derived(authMode === 'password' && !view?.classic.password_set);
    const showPairForm = $derived(authMode === 'password' && (!view?.classic.password_set || editPair));
    const pwOK = $derived(pw.length >= MIN && pw === pw2);
    const ports = (tlsPorts: boolean) => (view?.ports ?? []).filter((p) => p.tls === tlsPorts);
    // the firewall panel's hint (task 223), the verdicts the API's
    const FW_HINT: Record<Firewall['hint'], string> = {
        closed: 'No classic port is switched on, so there is nothing for the firewall to let in.',
        open: 'Every open port is accepted from the source its rule names. Narrow or widen a source on the Firewall page; switching a port off removes its rule.',
        blocked: 'A port that is switched on is not accepted: clients cannot reach it. Set its rule to ACCEPT on the Firewall page.',
    };

    async function save() {
        busy = 'save';
        notice = '';
        // ports that come or go restart lighttpd a moment after the answer; the login alone reloads it
        const restart = !!view && (plain !== view.classic.plain || tls !== view.classic.tls);
        try {
            take(await api.put<View>('/api/system/v1/remote-access', {classic: {plain, tls, auth: authMode}}));
            notice = restart ? t('Saved; lighttpd restarts in a moment to open or close the ports - the page may not answer for a second.') : t('Saved; lighttpd was reloaded.');
            error = '';
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    async function setPair(generate: boolean) {
        busy = 'pair';
        notice = '';
        generated = '';
        try {
            const v = await api.put<View>('/api/system/v1/remote-access/classic-password', generate ? {user, generate: true} : {user, password: pw});
            take(v);
            generated = v.password ?? '';
            pw = pw2 = '';
            editPair = false;
            notice = t('The user name and password are set; lighttpd was reloaded, the old password no longer works.');
            // B-137: the password is shown once; the form with the Generate button is gone by now and
            // took the focus with it, so it goes to the notice's Copy button, and the notice into view
            // where a small window has it below the edge
            if (generated) {
                await tick();
                bringIntoView(generatedNotice, generatedNotice?.querySelector<HTMLElement>('[data-action="copy-generated"]'));
            }
            error = '';
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
</script>

<SystemTitle />

{#if !view}
    <Loading {error} />
{:else}
    <!-- occulited task 12: classic RPC open to the network is a warning of the Status page -->
    <WarnEdge ids={['classic-rpc-open']}>
    <SectionHead id="classic" title={t('Classic RPC (as on a CCU)')} help={t('The XML-RPC ports a client configured for a CCU3 or OpenCCU talks to - Homematic Manager, node-red-contrib-ccu, ioBroker, Home Assistant, FHEM, openHAB - on the same ports and with the same login. lighttpd serves them and forwards to the interface processes, which stay on the loopback. The interface processes call a client back at the address it registers, so the client must be reachable from this system. No per-method rights, no trace, no lockout - as on a CCU.')} />
    {#if error}<div class="ol-notice error" data-notice="ra-error">{error}</div>{/if}
    {#if notice}<div class="ol-notice" data-notice="ra-notice">{notice}</div>{/if}

    <div class="ra-switches">
        {#each [{tlsPorts: false, label: t('Plain ports'), id: 'plain'}, {tlsPorts: true, label: t('TLS ports'), id: 'tls'}] as s (s.id)}
            <div class="ol-card ra-switch" data-switch={s.id}>
                <label class="ra-toggle">
                    {#if s.tlsPorts}
                        <input type="checkbox" bind:checked={tls} disabled={!admin || busy !== ''} />
                    {:else}
                        <input type="checkbox" bind:checked={plain} disabled={!admin || busy !== ''} />
                    {/if}
                    <strong>{s.label}</strong>
                </label>
                {#if s.tlsPorts}<div class="ol-muted">{t('With the system\'s certificate (Certificate page).')}</div>{/if}
                <table class="ol-table ra-ports">
                    <tbody>
                        {#each ports(s.tlsPorts) as p (p.port)}
                            <tr data-port={p.port}>
                                <td class="hmm-mono">{p.port}</td>
                                <td>{p.interface}</td>
                                <td class="ol-muted">{p.process}</td>
                                <td><span class="ol-dot" class:ok={p.running}></span>{p.running ? t('running') : t('not running')}</td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
                {#if !view.hs485d}<div class="ol-muted">{s.tlsPorts ? t('42000 (BidCos-Wired) only where hs485d runs.') : t('2000 (BidCos-Wired) only where hs485d runs.')}</div>{/if}
            </div>
        {/each}
    </div>

    <section class="ol-panel" data-panel="classic-auth">
        <h3>{t('Authentication')}<Help>{t('Applies to the plain and the TLS ports, as on a CCU. The user name and password exist only for classic RPC - not the system\'s accounts, not API tokens. Clients on the system itself (the loopback) are not asked.')}</Help></h3>
        <div class="ra-auth" role="radiogroup" aria-label={t('Authentication')}>
            <label><input type="radio" name="ra-auth" value="none" bind:group={authMode} disabled={!admin || busy !== ''} /> {t('None')}</label>
            <label><input type="radio" name="ra-auth" value="password" bind:group={authMode} disabled={!admin || busy !== ''} /> {t('User name and password')}</label>
        </div>
        {#if authMode === 'password' && view.classic.password_set && !editPair}
            <div class="ra-pair" data-pair>
                {t('User')}: <strong class="hmm-mono">{view.classic.user}</strong> · {t('password set')}
                {#if admin}<button type="button" class="hmm-button" onclick={() => (editPair = true)} disabled={busy !== ''}>{t('Change')}</button>{/if}
            </div>
        {/if}
        {#if showPairForm && admin}
            <div class="ol-form ra-pairform" data-pair-form>
                <label>{t('User name')} <input class="hmm-input hmm-mono" bind:value={user} autocomplete="off" /></label>
                <label>{t('Password')} <input class="hmm-input" type="password" bind:value={pw} autocomplete="new-password" placeholder={t('min. {n} characters', {n: MIN})} /></label>
                <label>{t('Repeat')} <input class="hmm-input" type="password" bind:value={pw2} autocomplete="new-password" /></label>
                {#if pw2 && pw !== pw2}<div class="ol-warn ol-form-buttons">{t('The passwords differ.')}</div>{/if}
                <div class="ol-form-buttons">
                    <button type="button" class="hmm-button primary" onclick={() => setPair(false)} disabled={busy !== '' || !pwOK || !user}>{t('Set password')}</button>
                    <button type="button" class="hmm-button" onclick={() => setPair(true)} disabled={busy !== '' || !user}>{t('Generate')}</button>
                    {#if editPair}<button type="button" class="hmm-button" onclick={() => (editPair = false)} disabled={busy !== ''}>{t('Cancel')}</button>{/if}
                </div>
                <div class="ol-muted ol-form-buttons">{t('Generate makes a password of 32 characters and shows it once.')}</div>
            </div>
        {/if}
        {#if generated}
            <div class="ol-notice ol-secret" data-notice="ra-generated" bind:this={generatedNotice}>
                <strong>{t('The generated password (shown only now):')}</strong> <code class="hmm-mono" bind:this={generatedEl}>{generated}</code>
                <span class="ol-actions"><CopyButton text={() => generated} shown={() => generatedEl} place="row-end" action="copy-generated" /><button type="button" class="hmm-button" onclick={() => (generated = '')}>{t('Hide')}</button></span>
            </div>
        {/if}

        <!-- what the settings on the page (not yet saved ones) mean -->
        {#if (plain || tls) && authMode === 'none'}
            <div class="ol-notice error" data-notice="ra-noauth">{t('Classic RPC without authentication: anyone the firewall lets in controls every device.')}</div>
        {/if}
        {#if plain && authMode === 'password'}
            <div class="ol-notice warn" data-notice="ra-plainpw">{t('On the plain ports the password travels in clear text: use it nowhere else, prefer the TLS ports, a narrow source in the firewall and a generated password.')}</div>
        {/if}

        {#if admin}
            <div class="ol-actions ra-save">
                <button type="button" class="hmm-button primary" onclick={save} disabled={busy !== '' || !dirty || needsPair}>{t('Save')}</button>
                {#if needsPair}<span class="ol-muted">{t('Set the user name and password first.')}</span>{/if}
            </div>
        {/if}
    </section>

    <!-- task 223, the maintainer: "remove the firewall table. i want the same panel as we have for
         the firewall rules for hmip-access-points instead, same style" - what the firewall does with
         each open port (the API's verdict), a hint, the way to the Firewall page -->
    {#if view.firewall}
        <SectionHead level={3} id="classic-firewall" title={t('Firewall')} help={t('Switching a port on makes a rule for it, owned by Classic RPC, from the local networks. A firewall converted from OpenCCU keeps what it allowed there: the local networks, or one rule for each allowed address. Every source a port is accepted from is named here. Narrow or widen them on the Firewall page; switching the port off removes its rules.')} />
        <FirewallVerdicts ports={view.firewall.ports} text={t(FW_HINT[view.firewall.hint] ?? FW_HINT.closed)} error={view.firewall.hint === 'blocked'} data-ra-hint={view.firewall.hint} />
    {/if}

    <!-- the maintainer, 2026-09-19: the clients on the network, only while classic RPC is on (as
         saved); task 223: directly below the firewall. The clients on this system are the
         Interfaces page's. -->
    {#if view.classic.plain || view.classic.tls}
        <Subscribers {admin} scope="external" level={3} />
    {/if}
    </WarnEdge>

    <!-- the maintainer, 2026-09-22: lite-rpc after classic RPC; task 223: its panel over the page's
         width, then its streams and the API tokens a program uses with it (occulited B-9) -->
    <SectionHead id="lite-rpc" title="lite-rpc" help={t('Requests and events over the web port with API tokens: XML-RPC and JSON-RPC 2.0 under /api/rpc/v1, and the interface processes\' events as a stream (SSE or WebSocket) instead of callbacks from this system to the client. No new port, no callback address to configure, the firewall as for the web port. Every call needs the token\'s tier (read, operate, configure, administer); init is refused. The shell\'s own session works on the same paths.')} />
    {#if !view.lite.available}
        <p class="ol-muted" data-lite="unavailable">{t('lite-rpc is not available on this system.')}</p>
    {:else}
        <section class="ol-panel ra-lite" data-switch="lite">
            <h3>lite-rpc <span class="ol-badge good">{t('always on')}</span></h3>
            <p class="ol-muted ra-lite-text">{t('XML-RPC and JSON-RPC 2.0 requests, events over SSE or WebSocket - with API tokens or the shell\'s session, per-method rights by tier, init refused. Control reads and switches through it.')}</p>
            <dl class="ol-kv ra-lite-kv">
                <dt>{t('Streams')}</dt><dd data-lite="streams">{t('{open} open · at most {per} per token or session, {total} in total', {open: String(view.lite.streams.open), per: String(view.lite.streams.per_token), total: String(view.lite.streams.total)})}</dd>
                <dt>{t('Ring buffer')}</dt><dd data-lite="buffer">{t('{seconds} s or {events} events: what a client that reconnects can catch up on', {seconds: String(view.lite.buffer.seconds), events: String(view.lite.buffer.events)})}</dd>
                <dt>{t('Heartbeat')}</dt><dd data-lite="heartbeat">{t('every 15 s; a client treats 45 s of silence as a dead connection')}</dd>
            </dl>
        </section>
        <StreamClients {admin} />
    {/if}
    {#if admin}
        <section class="ol-panel" data-panel="api-tokens"><APITokens /></section>
    {/if}
    <!-- openccu-lite task 224: the RPC trace, the last panel of the lite-rpc section (it was the
         Interfaces page's) -->
    <RPCTrace {admin} />

    <!-- task 185: SSH moved here from the Network page; task 223: its heading, then three panels -->
    <SSHAccess {admin} />
{/if}

<style>
    .ra-switches { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(300px, 100%), 1fr)); gap: 12px; margin: 8px 0 14px; }
    .ra-toggle { display: inline-flex; align-items: center; gap: 8px; font-size: 1.1em; }
    .ra-ports { margin: 8px 0 4px; }
    .ra-ports td:first-child { white-space: nowrap; }
    .ra-auth { display: flex; gap: 22px; flex-wrap: wrap; margin: 4px 0 10px; }
    .ra-pair { margin: 0 0 10px; }
    .ra-pair button { margin-left: 10px; }
    .ra-pairform { max-width: 520px; }
    .ra-save { margin: 14px 0 0; display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
    .ra-lite h3 .ol-badge { text-transform: none; }
    .ra-lite-text { margin: 0 0 10px; }
    .ra-lite-kv { gap: 6px 18px; margin: 0; }
    .ol-notice.warn { border-left: 3px solid var(--hmm-warn); }
</style>
