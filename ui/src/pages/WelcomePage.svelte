<script lang="ts">
    import {onMount} from 'svelte';
    import {api} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {t} from '../lib/i18n.svelte';
    import {link, navigate} from '../lib/router.svelte';
    import {openedAs, validHostname} from '../lib/hostname';
    import {certificateText, renameText, type AcmeNames, type CertNames, type Rename} from '../lib/rename';
    import UpdateChannel from '../lib/UpdateChannel.svelte';
    import {normalChannel, type Channel} from '../lib/updatechannel';

    interface FwState { enabled: boolean }
    interface FeedState { feed?: {enabled: boolean; channel?: string} | null }
    interface CatalogState { daily?: boolean }
    interface FirstBoot { objects: number; devices: number; channels: number; rooms: number; functions: number; unnamed: number; error?: string }
    let firstBoot = $state<FirstBoot | null>(null);

    // B-241 (D-90): the daily checks are off on a fresh system; the page asks once, one checkbox
    // per destination. GitHub is two settings behind one box: the release check and the
    // catalogue's daily check (which carries the installed addons' update checks).
    let fwEnabled = $state(false);
    let relEnabled = $state(false);
    // occulited task 118: the update channel, beside the release check; null = no feed
    let relChannel = $state<Channel | null>(null);
    let catDaily = $state(false);
    const ghOn = $derived(relEnabled && catDaily);
    // occulited task 4: the devices of an old CCU come across from its backup (openccu-lite task
    // 251, System → Backup), and only onto a system with no device paired yet - the import takes
    // over the old system's radio identity. The factory reset's view counts the paired devices per
    // interface; when it cannot be read the offer stays, and the Backup page checks again.
    interface PairedView { interfaces?: Record<string, {devices: number; known: boolean}> }
    let paired = $state(0);
    let msg = $state('');
    let busy = $state('');
    // task 149 (D-103): the local key step, only while HmIP-RF runs with no paired device - there
    // is no re-pairing cost then. An explicit choice, nothing pre-selected; Done waits for it.
    interface LocalKeyView { available: boolean; enabled: boolean; devices?: number }
    let lk = $state<LocalKeyView | null>(null);
    let lkChoice = $state<'' | 'local' | 'eq3'>('');
    const lkStep = $derived(!!lk && lk.available && !lk.enabled && lk.devices === 0);
    const lkBlocked = $derived(lkStep && lkChoice === '');
    // openccu-lite task 322: HmIP only - BidCos-RF's connection "none" (rfd and, but on the PCB,
    // multimacd off; hmipserver directly on the module). Asked only where rfd runs on a local module
    // or USB adapter with no LAN gateway and the choice is still automatic; never preselected, Done
    // waits for it. With BidCos devices paired there is nothing to choose: one line says BidCos-RF
    // stays on. An HmIP-only module (TK stick) runs HmIP only already: nothing is said.
    interface ConnPlanView { rfd: {run: boolean}; rfd_local: boolean; rfd_usb_adapter: boolean; rfd_lan_gateway: boolean; multimacd: {run: boolean}; hmipserver: {node?: string} }
    interface ConnView { available: boolean; choices: {hmip: string; bidcos: string}; plan?: ConnPlanView; running?: unknown; last?: {ok: boolean; error?: string} | null }
    let conn = $state<ConnView | null>(null);
    let bidcosDevices = $state<{devices: number; known: boolean} | null>(null);
    let hoChoice = $state<'' | 'hmip-only' | 'keep'>('');
    const hoOffer = $derived(!!conn?.available && !!conn.plan && conn.plan.rfd.run && (conn.plan.rfd_local || conn.plan.rfd_usb_adapter)
        && !conn.plan.rfd_lan_gateway && conn.choices.bidcos === '' && !conn.running);
    const hoPaired = $derived(hoOffer && !!bidcosDevices?.known && bidcosDevices.devices > 0);
    const hoStep = $derived(hoOffer && !hoPaired);
    const hoBlocked = $derived(hoStep && hoChoice === '');
    // the HM-MOD-RPI-PCB keeps multimacd for HmIP (D-101): the text promises no multiplexer-free path there
    let isPCB = $state(false);
    const hoMultimacdStays = $derived(isPCB);
    // openccu-lite task 327 (#4): the system's name, offered first - a fresh install is called
    // openccu-lite-<the end of its MAC>, and a rename now costs nothing (no passkey, no certificate
    // anyone trusts yet). The Network page's path: a hostname-only POST, which renews the DHCP lease
    // under the new name. Not in a container (the host names it) and not while a network change
    // waits for its confirmation.
    interface NetSettings { hostname: string; mode: 'dhcp' | 'static'; address?: string; netmask?: string; gateway?: string; dns: string[] }
    interface NetView { settings: NetSettings; writable: boolean; host_managed: boolean; pending: unknown }
    let netv = $state<NetView | null>(null);
    let hostName = $state('');
    let hnNotice = $state('');
    let hnCert = $state('');
    let hnError = $state('');
    let hnOldPage = $state('');
    const hnStep = $derived(!!netv && netv.writable && !netv.host_managed && !netv.pending);
    const hnValid = $derived(validHostname(hostName.trim()));
    const hnChanged = $derived(!!netv && hostName.trim() !== netv.settings.hostname);
    const first = $derived(hnStep ? 1 : 0);
    const steps = $derived(first + 3 + (hoOffer ? 1 : 0) + (lkStep ? 1 : 0));
    const blocked = $derived(lkBlocked || hoBlocked);

    onMount(async () => {
        try {
            netv = await api.get<NetView>('/api/system/v1/network');
            hostName = netv.settings.hostname;
        } catch { /* not an administrator, or an older daemon: no step */ }
        try { lk = await api.get<LocalKeyView>('/api/system/v1/radio/hmip/local-key?devices=1'); } catch { /* no HmIP, or not an administrator */ }
        try { fwEnabled = (await api.get<FwState>('/api/system/v1/firmware')).enabled; } catch { /* no fetcher */ }
        try {
            const f = (await api.get<FeedState>('/api/system/v1/system-update')).feed;
            relEnabled = f?.enabled ?? false;
            relChannel = f ? normalChannel(f.channel) : null;
        } catch { /* no feed */ }
        try { catDaily = (await api.get<CatalogState>('/api/system/v1/catalog')).daily ?? false; } catch { /* no catalogue */ }
        try { firstBoot = (await api.get<{first_boot_import?: FirstBoot}>('/api/system/v1/status')).first_boot_import ?? null; } catch { /* none */ }
        try {
            const ifs = (await api.get<PairedView>('/api/system/v1/factory-reset')).interfaces ?? {};
            paired = Object.values(ifs).reduce((n, i) => n + (i.known ? i.devices : 0), 0);
            bidcosDevices = ifs['BidCos-RF'] ?? null;
        } catch { /* not known: the offer stays */ }
        try {
            conn = await api.get<ConnView & {modules?: {hardware: string; roles?: string[]}[]}>('/api/system/v1/radio/connections');
            isPCB = ((conn as {modules?: {hardware: string; roles?: string[]}[]}).modules ?? []).some((m) => m.hardware === 'HM-MOD-RPI-PCB' && (m.roles ?? []).length > 0);
        } catch { /* not an administrator, or no occulited radio stack: no step */ }
    });

    async function renameSystem() {
        const name = hostName.trim();
        if (!netv || !validHostname(name) || name === netv.settings.hostname) return;
        // task 262: passkeys are bound to the full name - none on a fresh system, but the warning stays where one exists
        let registered = false;
        try { registered = (await api.get<{registered: boolean}>('/api/auth/v1/webauthn')).registered; } catch { /* an older daemon */ }
        if (registered && !(await ask({title: t('Rename the system?'), message: t('Passkeys are bound to the system\'s name. After the rename no registered passkey works any more: accounts sign in with their password until new passkeys are added. Rename anyway?'), confirm: t('Rename'), danger: true}))) return;
        busy = 'hn';
        hnNotice = hnCert = hnError = hnOldPage = '';
        const s: NetSettings = {...netv.settings, hostname: name};
        if (s.mode === 'dhcp') { delete s.address; delete s.netmask; delete s.gateway; }
        try {
            const r = await api.post<{applied: boolean; pending: unknown; rename?: Rename; certificate?: CertNames; acme_names?: AcmeNames}>('/api/system/v1/network', s);
            if (r.rename) {
                hnNotice = renameText(r.rename);
                hnCert = certificateText(r.rename, r.certificate, r.acme_names);
                if (openedAs(location.hostname, r.rename.previous)) hnOldPage = t('This page was opened under the old name. Open it as {name} once the router knows the new one.', {name: r.rename.hostname});
                netv = {...netv, settings: {...netv.settings, hostname: r.rename.hostname}};
            } else if (r.pending) {
                // not a rename alone: the Network page holds the confirmation
                hnError = t('The change waits for its confirmation on the Network page.');
            }
        } catch (e) {
            hnError = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function setFw(on: boolean) {
        busy = 'fw';
        try { fwEnabled = (await api.put<FwState>('/api/system/v1/firmware/settings', {enabled: on})).enabled; } catch (e) { msg = (e as Error).message; } finally { busy = ''; }
    }

    async function setGitHub(on: boolean) {
        busy = 'gh';
        try {
            relEnabled = (await api.put<{enabled: boolean}>('/api/system/v1/system-update/settings', {enabled: on})).enabled;
            catDaily = (await api.put<{daily: boolean}>('/api/system/v1/catalog/settings', {daily: on})).daily;
        } catch (e) {
            msg = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

    /** task 322: BidCos-RF off, and wait until the connection change has finished - before the local key */
    async function applyHmIPOnly(): Promise<boolean> {
        busy = 'ho';
        try {
            await api.put('/api/system/v1/radio/connections', {hmip: conn?.choices.hmip ?? '', bidcos: 'none'});
            for (let i = 0; i < 180; i++) {
                await sleep(i === 0 ? 500 : 1000);
                const st = await api.get<ConnView>('/api/system/v1/radio/connections');
                if (!st.running) {
                    conn = st;
                    if (st.last && !st.last.ok) { msg = t('BidCos-RF could not be switched off: {e}', {e: st.last.error ?? ''}); return false; }
                    return true;
                }
            }
            msg = t('The radio connection change did not finish in time; the Interfaces page shows how it stands.');
            return false;
        } catch (e) {
            msg = (e as Error).message;
            return false;
        } finally {
            busy = '';
        }
    }

    /** after a connection change HmIP-RF starts again: the local key waits until it lists its (no) devices */
    async function hmipReady(): Promise<boolean> {
        for (let i = 0; i < 180; i++) {
            try {
                const v = await api.get<LocalKeyView>('/api/system/v1/radio/hmip/local-key?devices=1');
                if (v.available && v.devices === 0) return true;
            } catch { /* starting */ }
            await sleep(1000);
        }
        return false;
    }

    async function done(to = '/') {
        // the connection change first, the local key after it - never both at once (task 322)
        let changed = false;
        if (hoStep && hoChoice === 'hmip-only') {
            if (!(await applyHmIPOnly())) return;
            changed = true;
        }
        if (lkStep && lkChoice === 'local') {
            // openccu-lite task 317 (D-120): the module's identity files are written under the new
            // key - said and confirmed; no device is paired, so nothing has to be taught in again
            const ok = await ask({
                title: t('Generate a local key now?'),
                message: [
                    t('A new random key is written into the radio module and kept on this system; HmIP-RF writes its identity files under it and restarts at once.'),
                    t('The key then lies in a file on this system and in every backup - whoever has it can join the network, so protecting the backups becomes crucial.'),
                ].join('\n\n'),
                confirm: t('Generate'),
                danger: true,
            });
            if (!ok) return;
            busy = 'lk';
            if (changed && !(await hmipReady())) {
                msg = t('HmIP-RF did not come back after the connection change; generate the key later on the Keys page.');
                busy = '';
                return;
            }
            try {
                await api.put('/api/system/v1/radio/hmip/local-key', {mode: 'generate', confirm: true});
            } catch (e) {
                msg = (e as Error).message;
                busy = '';
                return;
            }
            busy = '';
        }
        try { localStorage.setItem('ol.welcomed', '1'); } catch { /* ignore */ }
        navigate(to);
    }
</script>

<h1>{t('Welcome')}</h1>
<p>{steps === 6 ? t('The administrator exists. Six things worth deciding now; each can be changed later on its page.') : steps === 5 ? t('The administrator exists. Five things worth deciding now; each can be changed later on its page.') : steps === 4 ? t('The administrator exists. Four things worth deciding now; each can be changed later on its page.') : t('The administrator exists. Three things worth deciding now; each can be changed later on its page.')}</p>

<!-- openccu-lite task 319: the three statements of the update packages' EULA preamble, without the licence text -->
<section class="about" data-welcome-about aria-labelledby="welcome-about">
    <h2 id="welcome-about">{t('About openccu-lite')}</h2>
    <p>{t('openccu-lite is a separate project based on OpenCCU, but it is not OpenCCU: no ReGaHSS, no WebUI programs or Homematic scripts, its own web interface and the system service occulited.')}</p>
    <p>{t('Support: please report questions and problems with openccu-lite only in its issue tracker - not in the OpenCCU forum and not in OpenCCU\'s issue tracker. The OpenCCU team cannot help with openccu-lite.')} <a href="https://github.com/hobbyquaker/openccu-lite/issues" target="_blank" rel="noopener noreferrer" data-about="issues">{t('openccu-lite\'s issue tracker')}</a></p>
    <p>{t('Donations: openccu-lite\'s maintainer does not accept donations. If you want to support the project, give it a star on GitHub.')} <a href="https://github.com/hobbyquaker/openccu-lite" target="_blank" rel="noopener noreferrer" data-about="star">{t('openccu-lite on GitHub')}</a></p>
    <p class="ol-muted">{t('The system is licensed under the Apache License 2.0; its parts carry their own licences.')} <a href="https://github.com/hobbyquaker/openccu-lite/blob/main/LICENSE" target="_blank" rel="noopener noreferrer" data-about="license">{t('The licence')}</a> · <a href="/licenses" use:link data-about="licenses">{t('Licenses')}</a></p>
</section>

{#if hnStep && netv}
    <section data-welcome-hostname>
    <h2>1 · {t('The system\'s name')}</h2>
    <p>{t('The system tells the router this name, and the router lists it so; where the router resolves names, the system can be reached by it. A fresh system is called openccu-lite and the end of its network address (MAC). A rename is easiest now: later it also orphans passkeys and certificates made for the old name.')}</p>
    <div class="ol-actions hn-row">
        <label class="hn-field"><span>{t('Hostname')}</span>
            <input class="hmm-input" bind:value={hostName} disabled={busy !== ''} maxlength="63" autocomplete="off" spellcheck="false" aria-invalid={hnChanged && !hnValid} data-welcome-hostname-input onkeydown={(e) => { if (e.key === 'Enter' && hnChanged && hnValid) void renameSystem(); }} /></label>
        <button class="hmm-button" disabled={busy !== '' || !hnChanged || !hnValid} onclick={renameSystem} data-action="welcome-rename">{t('Rename')}</button>
    </div>
    {#if hnChanged && !hnValid}<p class="ol-muted hn-rule" data-welcome-hostname-invalid>{t('Letters, digits and hyphens, at most 63 characters; no hyphen at the start or the end.')}</p>{/if}
    {#if netv.settings.mode !== 'dhcp' && !hnNotice}<p class="ol-muted" data-welcome-hostname-static>{t('A static address: the name is set on the system, there is no DHCP server to tell. Enter it in the router or the DNS server by hand.')}</p>{/if}
    {#if hnNotice}<div class="ol-notice" data-notice="welcome-rename">{hnNotice}{#if hnOldPage} {hnOldPage}{/if}</div>{/if}
    {#if hnCert}<div class="ol-notice" data-notice="welcome-rename-certificate">{hnCert} <a href="/system/certificates" use:link>{t('Open certificate settings')}</a></div>{/if}
    {#if hnError}<div class="ol-notice error" data-notice="welcome-rename-error">{hnError} <a href="/system/network" use:link>{t('Network')}</a></div>{/if}
    </section>
{/if}

<h2>{first + 1} · {t('Automatic checks')}</h2>
<p>{t('This system connects to the internet only when you ask it to. Two checks can run daily instead; each is named here with where it connects, each is off, and each can be changed later on its page.')}</p>
<div class="ol-actions outbound" data-welcome-outbound>
    <label><input type="checkbox" checked={ghOn} onchange={(e) => setGitHub((e.currentTarget as HTMLInputElement).checked)} disabled={busy !== ''} data-outbound="github" /> {t('GitHub (api.github.com, raw.githubusercontent.com): check daily for a new system release and refresh the addon catalogue; the installed addons\' own update checks run with it.')}</label>
    {#if relChannel}<UpdateChannel channel={relChannel} disabled={busy !== ''} onchanged={(c) => (relChannel = c)} />{/if}
    <label><input type="checkbox" checked={fwEnabled} onchange={(e) => setFw((e.currentTarget as HTMLInputElement).checked)} disabled={busy !== ''} data-outbound="eq3" /> {t('eQ-3 (ccu3-update.homematic.com): check daily for new firmware of the paired device types and download it — the same server a CCU asks. Installing stays your decision.')}</label>
</div>

<h2>{first + 2} · {t('Devices from a CCU or OpenCCU')}</h2>
{#if firstBoot && !firstBoot.error}
    <div class="ol-notice">{t('This system was updated from a CCU: its names, rooms and functions were read from the ReGa database on this first boot — {o} named devices and channels, {r} rooms, {f} functions ({u} still carried the default name and were left out). Programs and system variables did not come across; nothing here could run them.', {o: firstBoot.objects, r: firstBoot.rooms, f: firstBoot.functions, u: firstBoot.unnamed})}</div>
{:else if firstBoot?.error}
    <div class="ol-notice">{t('A ReGa database was found but could not be read: {e}', {e: firstBoot.error})}</div>
{/if}
<p>{t('This does not restore a backup: it takes over only the paired devices, with their keys and names, from the backup of a CCU or OpenCCU into this system. The system reboots afterwards.')}</p>
<div class="ol-actions" data-welcome-devices>
    {#if paired > 0}
        <span class="ol-muted" data-welcome-devices-paired>{t('This system has {n} devices paired already: the import only works on a system without paired devices.', {n: paired})}</span>
    {:else}
        <a class="hmm-button" href="/system/backup#restore" use:link data-action="welcome-import-devices">{t('Import paired devices, keys and names from a backup')}</a>
    {/if}
</div>
{#if msg}<div class="ol-notice">{msg}</div>{/if}

{#if hoOffer}
    <section data-welcome-hmip-only>
    <h2>{first + 3} · {t('HmIP only?')}</h2>
    {#if hoPaired}
        <p data-welcome-ho-paired>{t('BidCos-RF has {n} paired devices and stays on. It can be turned off later on the Interfaces page.', {n: bidcosDevices?.devices ?? 0})} <a href="/system/interfaces#connections" use:link>{t('Interfaces')}</a></p>
    {:else}
        <p>{hoMultimacdStays
            ? t('BidCos-RF is the older Homematic radio standard; new devices are HmIP. This system has no BidCos device paired. With BidCos-RF off, the radio module serves HmIP only.')
            : t('BidCos-RF is the older Homematic radio standard; new devices are HmIP. This system has no BidCos device paired. With BidCos-RF off, HmIP has the radio module to itself, without the multiplexer in between.')}</p>
        <div class="ol-actions ho-choice" role="radiogroup" aria-label={t('HmIP only?')}>
            <label><input type="radio" name="ho" value="hmip-only" bind:group={hoChoice} disabled={busy !== ''} /> {t('HmIP only')}</label>
            <label><input type="radio" name="ho" value="keep" bind:group={hoChoice} disabled={busy !== ''} /> {t('Keep BidCos-RF')}</label>
        </div>
        <p class="ol-muted" data-welcome-ho-later>{t('BidCos-RF can be switched on again at any time on the Interfaces page (BidCos-RF, Automatic); nothing is lost. The change restarts the radio for about a minute.')} <a href="/system/interfaces#connections" use:link>{t('Interfaces')}</a></p>
    {/if}
    </section>
{/if}

{#if lkStep}
    <h2>{first + (hoOffer ? 4 : 3)} · {t('The HmIP network key')}</h2>
    <p>{t("HmIP devices share one network key. Normally it is locked in the radio module, and eQ-3's key server is needed to move it to another radio module and to pair a device without its key. It can be kept on this system instead: it is then offline-capable, and a later radio swap never needs the internet. No HmIP device is paired yet, so nothing has to be taught in again for this.")}</p>
    <div class="ol-actions lk-choice" role="radiogroup" aria-label={t('The HmIP network key')}>
        <label><input type="radio" name="lk" value="local" bind:group={lkChoice} disabled={busy !== ''} /> {t('Generate a local key now')}</label>
        <label><input type="radio" name="lk" value="eq3" bind:group={lkChoice} disabled={busy !== ''} /> {t("Keep eQ-3's key server")}</label>
    </div>
    {#if lkChoice === 'local'}
        <div class="ol-notice" data-notice="lk-backup">{t('The key then lies in a file on this system and in every backup - whoever has it can join the network, so protecting the backups becomes crucial.')} <a href="/system/backup">{t('Backup')}</a></div>
    {/if}
    <!-- B-290: the section is the Keys page's since task 183, no longer the Interfaces page's -->
    <p class="ol-muted" data-welcome-lk-later>{t('Either way this can be changed later on the Keys page, under Local key mode. Generating the key restarts HmIP-RF.')} <a href="/system/keys#local-key" use:link>{t('Keys')}</a></p>
{/if}

<h2>{steps} · {t('A frontend')}</h2>
<p>{t('This system ships no device management of its own. Homematic Manager (pairing, parameters, direct links, names) and the other addons are one click away in the catalogue.')}</p>

<div class="ol-actions" style="margin-top:16px">
    <button class="hmm-button" disabled={blocked || busy !== ''} onclick={() => done('/catalog')}>{t('Open the catalogue')}</button>
    <button class="hmm-button" disabled={blocked || busy !== ''} onclick={() => done()}>{t('Done')}</button>
    {#if hoBlocked}<span class="ol-muted" data-welcome-ho-blocked>{t('Choose whether BidCos-RF stays on first.')}</span>
    {:else if lkBlocked}<span class="ol-muted">{t('Choose where the HmIP network key is kept first.')}</span>{/if}
    {#if busy === 'ho'}<span class="ol-muted" data-welcome-ho-busy>{t('Switching BidCos-RF off - the radio restarts, about a minute …')}</span>{/if}
</div>

<style>
    label { margin-right: 16px; }
    .about { border: 1px solid var(--hmm-border); border-radius: 6px; padding: 4px 16px; margin: 12px 0 16px; }
    .outbound, .ho-choice { flex-direction: column; align-items: flex-start; gap: 8px; }
    .outbound label { margin-right: 0; display: flex; gap: 8px; align-items: baseline; }
    .outbound input { flex: none; }
    /* the name and its button on one line, the field taking what the phone leaves */
    .hn-row { align-items: flex-end; flex-wrap: wrap; gap: 8px; }
    .hn-field { display: flex; flex-direction: column; gap: var(--ol-label-gap); margin-right: 0; flex: 1 1 16em; max-width: 24em; }
    .hn-field span { color: var(--hmm-fg-muted); }
    .hn-field input { width: 100%; }
    .hn-rule { margin-top: 4px; }
</style>
