<script lang="ts">
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, REQUEST_HEADER} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {auth} from '../lib/auth.svelte';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import ModalDialog from '../lib/ModalDialog.svelte';
    import {bundleFilePath, viewableBundleFile} from '../lib/bundlefiles';
    import SystemUpdate from '../lib/SystemUpdate.svelte';
    import RadioFirmware from '../lib/RadioFirmware.svelte';
    import CheckDaily from '../lib/CheckDaily.svelte';
    import SearchInput from '../lib/SearchInput.svelte';
    import Icon from '../lib/Icon.svelte';
    import type {Status} from '../lib/api';
    import {newerFirmware} from '../lib/fwversion';
    import {agoParts, cannotUpdate, deviceActivity, filterOthers, filterRows, firmwareRows, isLive, summary, switchable, type FwActivity, type FwBundle, type FwDevice, type FwResult, type FwRow, type FwStatus} from '../lib/devicefirmware';
    import WarnEdge from '../lib/WarnEdge.svelte';
    import {ask} from '../lib/dialog.svelte';

    // version_from_name and date_from_name: what the update file's name carries, sent only when the
    // bundle's info states no version
    type Bundle = FwBundle;
    interface Transfers { running: number; queued: number; interrupted: number; ready: number; installing: number }
    interface State { enabled: boolean; running: boolean; last_run?: string; next_run?: string; last_error?: string; last_result: FwResult[]; devices: FwDevice[]; deployed: Bundle[]; index_size: number; interface_errors?: Record<string, string>; live_at?: string; transfers?: Transfers; auto_deploy?: boolean; held_types?: string[] }

    let st = $state<State | null>(null);
    // the system update needs to know whether this is a container (no recovery system to stage for)
    // and the platform (the boot entry the install writes)
    let status = $state<Status | null>(null);
    let error = $state('');
    let notice = $state('');
    let file = $state<File | null>(null);
    let busy = $state(false);

    async function load() {
        try {
            const r = await api.get<State>('/api/system/v1/firmware');
            // an empty or partial answer (a stub, an older daemon) must not throw in the page
            st = {...r, devices: r.devices ?? [], last_result: r.last_result ?? [], deployed: (r.deployed ?? []).map((b) => ({...b, files: b.files ?? []}))};
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    const life = pageLife();
    onMount(() => {
        void load();
        void api.get<Status>('/api/system/v1/status').then((s) => (status = s), () => undefined);
        // task 177: no polls while another page shows, a refresh when it comes back
        const id = setInterval(() => {
            now = Date.now();
            if (life.active) void load();
        }, 5000);
        const stopReturn = life.onReturn(() => void load());
        return () => {
            stopReturn();
            clearInterval(id);
        };
    });
    async function check() {
        notice = '';
        try {
            await api.post('/api/system/v1/firmware/check');
            notice = t('Check started');
        } catch (e) {
            notice = (e as Error).message;
        }
        await load();
    }
    async function toggle(on: boolean) {
        try {
            await api.put('/api/system/v1/firmware/settings', {enabled: on});
        } catch (e) {
            notice = (e as Error).message;
            throw e;
        } finally {
            await load();
        }
    }
    // task 45: the global switch - a downloaded bundle offered to the interfaces at once, or held
    // until the type's switch - and the per-type one, which moves the bundle; both ask first
    let autoSaving = $state(false);
    async function setAutoDeploy(on: boolean) {
        autoSaving = true;
        notice = '';
        try {
            await api.put('/api/system/v1/firmware/settings', {auto_deploy: on});
        } catch (e) {
            notice = (e as Error).message;
        } finally {
            autoSaving = false;
            await load();
        }
    }
    let switching = $state<Record<string, boolean>>({});
    async function setTransfer(r: FwRow, on: boolean) {
        if (!r.bundle) return;
        const dir = r.bundle.dir || r.bundle.type_code;
        if (!on && isLive(r.status)) {
            const ok = await ask({title: t('Hold this firmware back?'), message: t('Holding it back stops the transfer to the devices of this type. Switching it back on later starts the transfer over from the beginning, which costs time and airtime.'), confirm: t('Hold back'), danger: true});
            if (!ok) return;
        }
        switching = {...switching, [r.key]: true};
        notice = '';
        try {
            await api.put(`/api/system/v1/firmware/bundles/${encodeURIComponent(dir)}/transfer`, {transfer: on});
        } catch (e) {
            notice = (e as Error).message;
        } finally {
            switching = {...switching, [r.key]: false};
            await load();
        }
    }
    // task 45: the radio's duty cycle beside running transfers - the cost of a transfer is airtime;
    // read once a minute while one runs (the sampler reads it once a minute itself)
    interface RadioHealth { interfaces?: {interface: string; duty_cycle?: number}[] }
    let dutyCycle = $state<Record<string, number>>({});
    let dutyAt = 0;
    async function loadDuty(force = false) {
        if (!force && Date.now() - dutyAt < 60_000) return;
        dutyAt = Date.now();
        try {
            const h = await api.get<RadioHealth>('/api/system/v1/radio/health');
            const dc: Record<string, number> = {};
            for (const i of h.interfaces ?? []) if (typeof i.duty_cycle === 'number') dc[i.interface] = i.duty_cycle;
            dutyCycle = dc;
        } catch {
            dutyCycle = {};
        }
    }
    $effect(() => {
        if (counts.transferring || counts.installing) void loadDuty();
    });
    const dutyText = $derived(Object.entries(dutyCycle).filter(([i]) => i === 'HmIP-RF' || i === 'BidCos-RF').map(([i, v]) => `${i}: ${v} %`).join(' · '));
    // task 40: one row per paired device type with one status; what cannot be updated is hidden
    // until Show all devices; the search runs over type, device name and address
    const view = $derived(st ? firmwareRows(st.devices, st.deployed, st.last_result) : {rows: [], others: []});
    const counts = $derived(summary(view.rows));
    let query = $state('');
    let showAll = $state(false);
    const filtered = $derived(filterRows(view.rows, query, showAll));
    // task 44: the search covers the bundles no paired type takes as well
    const others = $derived(filterOthers(view.others, query));
    // the clock for "last progress … ago", moved along with the poll so the text ages
    let now = $state(Date.now());
    // the rows opened by hand; a row the search found by one of its devices opens by itself
    let opened = $state<Record<string, boolean>>({});
    // a new search opens the rows it finds by a device again, whatever was closed by hand before
    $effect(() => {
        void query;
        opened = {};
    });
    const isOpen = (r: FwRow) => opened[r.key] ?? filtered.byDevice.has(r.key);
    const toggleRow = (r: FwRow) => (opened = {...opened, [r.key]: !isOpen(r)});
    function statusLabel(s: FwStatus): string {
        switch (s) {
            // task 44: the deployed firmware on its way to HmIP devices, live from the interface and hmipserver's journal
            case 'installing': return t('Installing');
            case 'transferring': return t('Transferring to the device');
            case 'interrupted': return t('Transfer interrupted');
            case 'queued': return t('Queued for transfer');
            case 'ready': return t('Transferred, ready to install');
            case 'downloaded': return t('Downloaded, ready to install');
            case 'held': return t('Held back');
            case 'available': return t('Available at eQ-3');
            case 'unchecked': return t('Not checked yet');
            case 'current': return t('Up to date');
            case 'not-listed': return t('No firmware at eQ-3');
            case 'image-only': return t('Only with the system image');
            case 'not-updatable': return t('Not updatable over the air');
        }
    }
    function statusHint(s: FwStatus): string {
        switch (s) {
            case 'installing': return t('The device installs the firmware it received. A battery device may need its system button pressed.');
            case 'transferring': return t('The firmware is being transferred to the device in the background. A battery device receives it only while awake, so this can take hours.');
            case 'interrupted': return t('The device did not answer; the transfer continues by itself from where it stopped once the device is reachable again.');
            case 'queued': return t('The firmware waits for its turn: the interface transfers to one device at a time.');
            case 'ready': return t('The device has received the firmware. Start the update in the frontend.');
            case 'downloaded': return t('The firmware is on the system and offered to the device. Start the update in the frontend.');
            case 'held': return t('The firmware is on the system but not offered to the interfaces: nothing is transferred until Transfer this firmware is switched on.');
            case 'available': return t('eQ-3 has a newer firmware; Download now fetches it onto the system.');
            case 'unchecked': return t("No check has read eQ-3's list yet.");
            case 'current': return t('The devices run the newest firmware known.');
            case 'not-listed': return t("eQ-3's list has no firmware for this type. A bundle can still be uploaded by hand.");
            case 'image-only': return t('BidCos-Wired devices get firmware only with the system image.');
            case 'not-updatable': return t('The interface says these devices cannot be updated over the air.');
        }
    }
    let uploadOpen = $state(false);
    // the live label with its percent, and the "last progress" line under it
    function activityLabel(s: FwStatus, a?: FwActivity): string {
        const label = statusLabel(s);
        return a && a.kind === s && a.percent !== undefined && (s === 'transferring' || s === 'interrupted') ? `${label} · ${a.percent} %` : label;
    }
    function agoText(iso?: string): string {
        const p = agoParts(iso, now);
        if (!p) return '';
        switch (p.unit) {
            case 'just': return t('just now');
            case 'minutes': return t('{n} min ago', {n: String(p.n)});
            case 'hours': return t('{n} h ago', {n: String(p.n)});
            case 'days': return t('{n} days ago', {n: String(p.n)});
        }
    }
    function progressText(a?: FwActivity): string {
        if (!a || !a.updated || (a.kind !== 'transferring' && a.kind !== 'interrupted')) return '';
        const when = agoText(a.updated);
        const waiting = a.reason === 'unreachable' ? ` · ${t('waiting for the device')}` : '';
        return `${t('Last progress')}: ${when}${waiting}`;
    }
    // BidCos types are named HM-…; everything else in eQ-3's list is HmIP (HmIP-, HmIPW-, ELV-SH-, the access points)
    const interfaceOf = (name: string) => (/^hm-/i.test(name) ? 'BidCos-RF' : 'HmIP-RF');
    // the files that are not the two texts: the firmware (.efw/.eq3/.zip), and anything else a bundle carries
    const firmwareFiles = (b: Bundle) => b.files.filter((f) => f !== 'changelog.txt' && f !== 'info');
    async function upload(e: Event) {
        e.preventDefault();
        if (!file) return;
        busy = true;
        notice = '';
        try {
            const fd = new FormData();
            fd.append('file', file);
            const res = await fetch('/api/system/v1/firmware/upload', {method: 'POST', headers: REQUEST_HEADER, body: fd});
            const data = (await res.json()) as Bundle & {error?: string; message?: string};
            if (data.error) throw new Error(data.message ?? data.error);
            notice = `${t('Deployed')}: ${data.name} ${data.version}`;
            file = null;
            uploadOpen = false;
        } catch (err) {
            notice = (err as Error).message;
        } finally {
            busy = false;
            await load();
        }
    }
    const admin = $derived(auth.role === 'admin');
    const fmt = (s?: string) => (s ? new Date(s).toLocaleString() : '–');

    // A bundle's text files - its changelog, its info - open in a dialog: the file name as the
    // title, the device type under it, the text as it is in a monospaced block. The answer of a
    // file opened before is dropped when another one was opened meanwhile.
    interface Viewer { name: string; type: string; text: string; error: string; loading: boolean }
    let viewer = $state<Viewer | null>(null);
    let viewerSeq = 0;
    async function openFile(b: Bundle, name: string) {
        const seq = ++viewerSeq;
        viewer = {name, type: b.name || b.type_code, text: '', error: '', loading: true};
        try {
            const text = await api.getText(bundleFilePath(b.dir || b.type_code, name));
            if (seq === viewerSeq && viewer) viewer = {...viewer, text, loading: false};
        } catch (e) {
            if (seq === viewerSeq && viewer) viewer = {...viewer, error: (e as Error).message, loading: false};
        }
    }
    function closeFile() {
        viewerSeq++;
        viewer = null;
    }
</script>

<!-- a version a bundle does not state (eQ-3's 0.0.0 arrives as an empty one) is the one its update
     file's name carries, muted and marked as such; a quiet dash when the name gives none either -->
{#snippet version(v: {version: string; version_from_name?: string; date_from_name?: string})}{#if v.version}{v.version}{:else if v.version_from_name}<span class="ol-muted ol-fromname" title={t('The bundle states no version; this one is read from the name of its update file.')}>{v.date_from_name ? `${v.version_from_name} (${v.date_from_name})` : v.version_from_name}{' '}<em class="ol-fromname-mark">{t('from the file name')}</em></span>{:else}<span class="ol-muted">–</span>{/if}{/snippet}

<SystemTitle />

<!-- the maintainer, 2026-09-19/20: the system update (from the Status page), the radio firmware
     (from the Interfaces page) and the paired devices' firmware, each in a panel of its own -->
<section class="ol-panel" data-panel="system-update">
    <SystemUpdate container={!!status?.container} platform={status?.version?.platform} />
</section>

<WarnEdge ids={['radio-firmware', 'radio-module-unusable']}><RadioFirmware /></WarnEdge>

<!-- task 40 (the maintainer, 2026-10-13): one device-firmware view instead of the paired devices, the
     last run and the deployed bundles - one row per paired device type with one status, what cannot
     be updated hidden until Show all devices, a search, the upload behind a button -->
<section class="ol-panel" data-panel="device-firmware">
<!-- task 51: how the page works is help, not the system's state - behind the heading's ? -->
<h2 id="device-firmware">{t('Device firmware')}<Help>{t('Firmware for the paired device types is fetched from eQ-3 - on Check now, and once a day with Check daily - and made available to the interfaces. Nothing is installed onto a device by itself: that stays your decision in the frontend.')} {t('hmipserver (HmIP-RF, access points) and rfd (BidCos-RF) read it from /etc/config/firmware; BidCos-Wired devices get firmware only with the system image.')}</Help></h2>
{#if notice}<div class="ol-notice">{notice}</div>{/if}
{#if !st}
    <Loading {error} />
{:else}
    <div class="ol-toolbar fw-head">
        {#if admin}
            <!-- task 244: the check button with Check daily behind it (lib/CheckDaily.svelte) -->
            <CheckDaily label={t('Check now')} busyLabel={t('Running…')} busy={st.running} onCheck={check} daily={st.enabled} onDaily={toggle} host="ccu3-update.homematic.com" name="firmware" />
        {/if}
        <span class="ol-muted" data-last-check>{t('Last check')}: {fmt(st.last_run)}{#if st.next_run} · {t('Next run')}: {fmt(st.next_run)}{/if}</span>
        {#if admin}
            <!-- task 45: the global switch; off, every new download waits in the held directory for the type's switch -->
            <label class="fw-auto" title={t('On: a downloaded firmware is offered to the interfaces at once, which transfer it to the devices in the background. Off: it waits until you switch Transfer this firmware on for the type.')}><input type="checkbox" checked={st.auto_deploy !== false} disabled={autoSaving} onchange={(e) => setAutoDeploy((e.currentTarget as HTMLInputElement).checked)} data-auto-deploy /> {t('Deploy automatically')}</label>
        {/if}
        {#if admin}<button type="button" class="hmm-button fw-upload-open" onclick={() => (uploadOpen = true)} data-upload-open><Icon name="arrow-up" size={14} /> {t('Upload a bundle')}</button>{/if}
    </div>
    {#if st.last_error}<div class="ol-notice error">{st.last_error}</div>{/if}
    {#if st.interface_errors}
        {#each Object.entries(st.interface_errors) as [name, err] (name)}<div class="ol-notice error">{name}: {err}</div>{/each}
    {/if}

    {#if view.rows.length === 0}
        <p class="ol-muted" data-fw-none>{t('No paired devices known yet: the list is read from the interfaces at each check.')}</p>
    {:else}
        <!-- what there is to do at a glance: the counts of the statuses, each in its row's colours -->
        <p class="fw-summary" data-fw-summary>
            {#if counts.installing}<span class="ol-badge good fw-sum" data-sum="installing">{t('{n} installing', {n: String(counts.installing)})}</span>{/if}
            {#if counts.transferring}<span class="ol-badge good fw-sum" data-sum="transferring">{t('{n} transferring', {n: String(counts.transferring)})}</span>{/if}
            {#if counts.downloaded}<span class="ol-badge good fw-sum" data-sum="downloaded">{t('{n} ready to install', {n: String(counts.downloaded)})}</span>{/if}
            {#if counts.held}<span class="ol-badge fw-sum" data-sum="held">{t('{n} held back', {n: String(counts.held)})}</span>{/if}
            {#if counts.available}<span class="ol-badge warn fw-sum" data-sum="available">{t('{n} available at eQ-3', {n: String(counts.available)})}</span>{/if}
            {#if counts.current}<span class="ol-badge fw-sum" data-sum="current">{t('{n} up to date', {n: String(counts.current)})}</span>{/if}
            {#if !counts.downloaded && !counts.available && !counts.transferring && !counts.installing && !counts.held}<span class="ol-muted fw-sum-none">{t('No update waiting.')}</span>{/if}
        </p>
        <!-- task 44: what the background transfer means, while one is on its way -->
        {#if counts.transferring}<p class="ol-muted fw-transfer-help" data-fw-transfer-help>{t('Firmware is transferred to the devices in the background, one device after the other; battery devices receive it only while awake, which can take 8 to 42 hours.')}{#if dutyText}{' '}<span data-duty-cycle title={t('The radio\'s duty cycle: the transfer uses the same airtime as your devices.')}>{t('Duty cycle')} {dutyText}</span>{/if}</p>{/if}
        <div class="ol-toolbar fw-filter">
            <SearchInput bind:value={query} placeholder={t('Search type, name or address')} label={t('Search the device firmware')} delay={0} />
            <label class="fw-showall"><input type="checkbox" bind:checked={showAll} data-show-all /> {t('Show all devices')}</label>
        </div>
        <table class="ol-table ol-stack fw-types">
            <colgroup><col /><col class="fw-c-if" /><col class="fw-c-inst" /><col class="fw-c-status" /></colgroup>
            <thead><tr><th>{t('Device type')}</th><th>{t('Interface')}</th><th>{t('Installed')}</th><th>{t('Status')}</th></tr></thead>
            <tbody>
                {#each filtered.shown as r (r.key)}
                    {@const open = isOpen(r)}
                    {@const texts = r.bundle ? r.bundle.files.filter((f) => f === 'changelog.txt' || f === 'info') : []}
                    <tr class="fw-row" class:fw-muted={cannotUpdate(r.status)} data-type={r.type} data-status={r.status}>
                        <td class="ol-stack-line fw-type">
                            <button type="button" class="fw-expand" aria-expanded={open} onclick={() => toggleRow(r)} data-expand>
                                <span class="fw-chev" aria-hidden="true"></span>
                                <span class="fw-typename">{r.type}</span>
                                <span class="ol-muted fw-count">{r.devices.length === 1 ? t('1 device') : t('{n} devices', {n: String(r.devices.length)})}</span>
                            </button>
                        </td>
                        <td data-label={t('Interface')}>{r.interfaces.join(', ')}</td>
                        <td data-label={t('Installed')} class="fw-num" data-fw-installed>{r.installed.join(', ') || '–'}</td>
                        <td data-label={t('Status')} class="fw-status">
                            <!-- the version in a slot of its own, so the label starts at the same x in every row (homematic-manager task 93) -->
                            <span class="fw-status-line">
                                <span class="fw-ver fw-num" class:ol-fromname={!!r.fromName} data-version>{r.version}</span>
                                <span class="ol-badge fw-badge" class:good={r.status === 'downloaded' || isLive(r.status)} class:warn={r.status === 'available'} title={statusHint(r.status)} data-badge>{activityLabel(r.status, r.activity)}</span>
                                <span class="fw-texts">{#each texts as f (f)}<button type="button" class="ol-iconlink fw-icon" onclick={() => openFile(r.bundle!, f)} aria-label={f} title={f === 'info' ? t('The bundle\'s info') : t('The changelog')} data-text={f}><Icon name={f === 'info' ? 'info' : 'log'} size={16} /></button>{/each}</span>
                                {#if r.status === 'available' && admin}<button type="button" class="hmm-button fw-download" disabled={st.running} onclick={check} data-download>{st.running ? t('Running…') : t('Download now')}</button>{/if}
                            </span>
                            <!-- task 44: the transfer's bar and its last progress -->
                            {#if r.activity && (r.status === 'transferring' || r.status === 'interrupted') && r.activity.percent !== undefined}
                                <div class="ol-progress fw-progress" class:fw-paused={r.status === 'interrupted'} role="progressbar" aria-valuenow={r.activity.percent} aria-valuemin={0} aria-valuemax={100} aria-label={statusLabel(r.status)} data-progress={r.activity.percent}><div style={`width: ${r.activity.percent}%`}></div></div>
                            {/if}
                            {#if progressText(r.activity)}<span class="ol-muted fw-sub" data-last-progress>{progressText(r.activity)}</span>{/if}
                            <!-- a version the bundle does not state is the one its update file's name carries, marked as such -->
                            {#if r.fromName}<span class="ol-muted fw-sub" title={t('The bundle states no version; this one is read from the name of its update file.')} data-from-name><em>{t('from the file name')}</em>{r.fromName.date ? ` · ${r.fromName.date}` : ''}</span>{/if}
                            {#if r.behind && r.behind < r.devices.length}<span class="ol-muted fw-sub" data-behind>{t('for {n} of {total} devices', {n: String(r.behind), total: String(r.devices.length)})}</span>{/if}
                            {#if r.status === 'available' && r.downloaded}<span class="ol-muted fw-sub" data-older-download>{t('{version} is downloaded', {version: r.downloaded})}</span>{/if}
                            {#if r.failed !== undefined}<span class="fw-sub fw-failed" data-failed>{t('Last download failed')}{r.failed ? `: ${r.failed}` : ''}</span>{/if}
                            <!-- task 45: the per-type switch on a row whose bundle is on the system -->
                            {#if admin && r.bundle && switchable(r.status)}
                                <label class="fw-sub fw-switch" title={r.status === 'held' ? t('Switching it on offers the firmware to the interfaces, which transfer it to the devices in the background.') : t('Switching it off holds the firmware back: the transfer stops. Switching it back on later starts the transfer over from the beginning.')}><input type="checkbox" checked={r.status !== 'held'} disabled={switching[r.key]} onchange={(e) => { const el = e.currentTarget as HTMLInputElement; const on = el.checked; el.checked = !on; void setTransfer(r, on); }} data-transfer-switch /> {t('Transfer this firmware')}</label>
                            {/if}
                        </td>
                    </tr>
                    {#if open}
                        <tr class="fw-devices-row"><td colspan="4" class="ol-stack-line">
                            <ul class="fw-devices" data-devices>
                                {#each r.devices as d (d.interface + d.address)}
                                    {@const act = deviceActivity(d)}
                                    <li data-device={d.address}>
                                        <span class="fw-dname">{#if d.name}{d.name}{:else}<span class="ol-muted">{t('unnamed')}</span>{/if}</span>
                                        <span class="hmm-mono fw-daddr">{d.address}</span>
                                        <span class="fw-num fw-dfw">{d.firmware || '–'}{#if r.version && newerFirmware(r.version, d.firmware) && !cannotUpdate(r.status)}{' '}<span class="fw-arrow" title={statusLabel(r.status)}>→ {r.version}</span>{/if}{#if act}{' '}<span class="ol-badge fw-dact" class:good={act.kind === 'ready' || act.kind === 'installing'} title={progressText(act) || statusHint(act.kind)} data-device-state={act.kind}>{activityLabel(act.kind, act)}</span>{/if}</span>
                                    </li>
                                {/each}
                            </ul>
                        </td></tr>
                    {/if}
                {/each}
            </tbody>
        </table>
        {#if filtered.shown.length === 0}<p class="ol-muted" data-fw-nomatch>{query.trim() ? t('Nothing matches the search.') : t('No device here can be updated over the air.')}</p>{/if}
        {#if filtered.hidden > 0 && !showAll}
            <p class="ol-muted fw-hidden" data-fw-hidden>{filtered.hidden === 1 ? t('1 device type that cannot be updated is hidden.') : t('{n} device types that cannot be updated are hidden.', {n: String(filtered.hidden)})} <button type="button" class="ol-filelink" onclick={() => (showAll = true)}>{t('Show all devices')}</button></p>
        {/if}
    {/if}

    <!-- the bundles in /etc/config/firmware no paired type takes: collapsed, so nothing deployed is invisible -->
    {#if view.others.length > 0}
        <details class="fw-others" data-fw-others>
            <summary>{t('Other downloaded firmware')} <span class="ol-muted">· {query.trim() ? `${others.length}/${view.others.length}` : view.others.length}</span></summary>
            <p class="ol-muted fw-others-help">{t('Bundles in /etc/config/firmware for types no paired device has.')}</p>
            {#if others.length === 0}<p class="ol-muted" data-fw-others-nomatch>{t('Nothing matches the search.')}</p>{:else}
            <table class="ol-table ol-stack ol-bundles">
                <thead><tr><th>{t('Device type')}</th><th>{t('Version')}</th><th>{t('Interface')}</th><th>{t('Firmware file')}</th></tr></thead>
                <tbody>
                    {#each others as b (b.dir || b.type_code)}
                        {@const texts = b.files.filter((f) => f === 'changelog.txt' || f === 'info')}
                        <tr data-bundle={b.name || b.type_code}>
                            <td class="ol-stack-line">{b.name || b.type_code} <span class="ol-muted hmm-mono">{b.type_code}</span></td>
                            <td data-label={t('Version')}>{@render version(b)}</td>
                            <td data-label={t('Interface')}>{interfaceOf(b.name)}</td>
                            <td class="ol-files hmm-mono" data-label={t('Firmware file')}>{#each texts as f (f)}<button type="button" class="ol-iconlink fw-icon" onclick={() => openFile(b, f)} aria-label={f} title={f === 'info' ? t('The bundle\'s info') : t('The changelog')} data-text={f}><Icon name={f === 'info' ? 'info' : 'log'} size={16} /></button>{/each}{#each firmwareFiles(b) as f, i (f)}{#if i > 0}<span class="ol-muted">{', '}</span>{/if}{#if viewableBundleFile(f)}<button type="button" class="ol-filelink" onclick={() => openFile(b, f)}>{f}</button>{:else}<span>{f}</span>{/if}{/each}</td>
                        </tr>
                    {/each}
                </tbody>
            </table>
            {/if}
        </details>
    {/if}
{/if}
</section>

<ModalDialog open={uploadOpen} title={t('Upload a bundle')} size="small" onclose={() => (uploadOpen = false)}>
    <p class="ol-muted fw-upload-help">{t('An eQ-3 device firmware .tgz — for a system without internet access or firmware the vendor no longer serves.')}</p>
    <form class="ol-toolbar fw-upload" onsubmit={upload}>
        <input type="file" accept=".tgz,.tar.gz,application/gzip" aria-label={t('Firmware bundle')} onchange={(e) => (file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
        <button class="hmm-button primary" type="submit" disabled={!file || busy}>{t('Deploy')}</button>
    </form>
</ModalDialog>

<ModalDialog open={viewer !== null} title={viewer?.name ?? ''} subtitle={viewer?.type ?? ''} size="large" class="ol-fileviewer" onclose={closeFile}>
    {#if viewer}
        {#if viewer.loading}
            <div class="ol-muted ol-fileviewer-state">{t('Loading…')}</div>
        {:else if viewer.error}
            <div class="ol-notice error ol-fileviewer-state">{viewer.error}</div>
        {:else if viewer.text === ''}
            <div class="ol-muted ol-fileviewer-state">{t('empty file')}</div>
        {:else}
            <pre class="hmm-mono ol-filetext">{viewer.text}</pre>
        {/if}
    {/if}
</ModalDialog>

<style>
    /* task 40: the device-firmware view */
    .fw-head { justify-content: flex-start; }
    .fw-upload-open { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; }
    .fw-summary { display: flex; flex-wrap: wrap; gap: 6px; margin: 4px 0 10px; }
    .fw-summary .ol-badge { margin-left: 0; font-size: var(--hmm-font-size-small); }
    .fw-filter { margin: 0 0 8px; }
    .fw-showall { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    .fw-c-if { width: 9em; }
    .fw-c-inst { width: 10em; }
    .fw-c-status { width: 46%; }
    .fw-num { font-variant-numeric: tabular-nums; }
    .fw-expand { display: inline-flex; align-items: baseline; gap: 8px; padding: 0; border: 0; background: none; color: var(--hmm-fg); font: inherit; text-align: left; cursor: pointer; }
    .fw-expand:focus-visible { outline: 1px solid var(--hmm-focus); outline-offset: 2px; }
    .fw-typename { font-weight: 600; overflow-wrap: anywhere; }
    .fw-count { font-size: var(--hmm-font-size-small); white-space: nowrap; }
    /* the disclosure triangle: points right while closed, down when open */
    .fw-chev { display: inline-block; width: 0; height: 0; border-style: solid; border-width: 4px 0 4px 6px; border-color: transparent transparent transparent var(--hmm-fg-muted); transition: transform 0.15s; align-self: center; }
    .fw-expand[aria-expanded='true'] .fw-chev { transform: rotate(90deg); }
    @media (prefers-reduced-motion: reduce) { .fw-chev { transition: none; } }
    .fw-status-line { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; }
    /* the version's own slot: the status label starts at the same x in every row */
    .fw-ver { display: inline-block; min-width: 4.5em; }
    .fw-badge { margin-left: 0; }
    .fw-texts { display: inline-flex; white-space: nowrap; }
    .fw-sub { display: block; font-size: var(--hmm-font-size-small); margin-top: 2px; }
    /* task 44: the transfer's thin bar; paused (interrupted) in the warn colour */
    .fw-progress { max-width: 16em; margin: 4px 0 2px; }
    .fw-progress.fw-paused > div { background: var(--hmm-warn); }
    .fw-transfer-help { margin: -4px 0 10px; font-size: var(--hmm-font-size-small); }
    .fw-dact { margin-left: 0; font-size: var(--hmm-font-size-small); }
    /* task 45: the two switches */
    .fw-auto { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    .fw-switch { display: inline-flex; align-items: center; gap: 6px; margin-top: 4px; cursor: pointer; }
    .fw-failed { color: var(--hmm-error); }
    .fw-download { padding: 1px 10px; font-size: var(--hmm-font-size-small); }
    .fw-row.fw-muted .fw-typename { font-weight: normal; color: var(--hmm-fg-muted); }
    .fw-devices-row > td { background: var(--hmm-bg-sunken); }
    .fw-devices { list-style: none; margin: 0; padding: 2px 0 2px 14px; display: grid; grid-template-columns: minmax(0, max-content) max-content max-content; gap: 3px 18px; }
    .fw-devices li { display: contents; }
    .fw-dname { overflow-wrap: anywhere; min-width: 0; }
    .fw-arrow { color: var(--hmm-warn); white-space: nowrap; }
    .fw-hidden { margin-top: 8px; }
    .fw-others { margin-top: 14px; }
    .fw-others > summary { cursor: pointer; color: var(--hmm-fg-muted); font-weight: 600; padding: 4px 0; }
    .fw-others-help { margin: 4px 0 6px; font-size: var(--hmm-font-size-small); }
    .fw-upload-help { margin: 0 0 12px; }
    .fw-upload { margin-bottom: 16px; }
    .fw-upload input[type='file'] { max-width: 100%; }
    @media (max-width: 700px) {
        .fw-devices { grid-template-columns: minmax(0, 1fr) max-content; padding-left: 4px; }
        .fw-daddr { grid-column: 1; font-size: var(--hmm-font-size-small); color: var(--hmm-fg-muted); }
        .fw-dfw { grid-column: 2; grid-row: span 1; }
        .fw-upload-open { margin-left: 0; }
        .fw-ver:empty { display: none; }
    }
    .fw-icon { display: inline-flex; padding: 2px; margin-right: 2px; border: 0; background: none; color: var(--hmm-accent); cursor: pointer; border-radius: var(--hmm-radius); vertical-align: middle; }
    .fw-icon:hover { background: var(--hmm-hover, rgba(127, 127, 127, 0.15)); }
    /* the file list wraps on a phone instead of widening the table */
    .ol-bundles .ol-files { white-space: normal; overflow-wrap: anywhere; }
    .ol-filelink { display: inline; padding: 0; border: 0; background: none; color: var(--hmm-link); font: inherit; text-align: left; cursor: pointer; overflow-wrap: anywhere; }
    .ol-filelink:hover { text-decoration: underline; }
    /* the version taken from the update file's name: muted, with a small marker that stays on its line */
    .ol-fromname { color: var(--hmm-fg-muted); }
    .ol-fromname-mark { font-size: var(--hmm-font-size-small); white-space: nowrap; }
    .ol-filelink:focus-visible { outline: 1px solid var(--hmm-focus); outline-offset: 1px; }
    .ol-filetext { margin: 0 0 16px; padding: 10px 12px; white-space: pre-wrap; overflow-wrap: anywhere; font-size: var(--hmm-font-size-grid); line-height: 1.45; color: var(--hmm-fg); background: var(--hmm-bg-sunken); border: 1px solid var(--hmm-border-muted); border-radius: var(--hmm-radius); }
    .ol-fileviewer-state { margin-bottom: 16px; }
</style>
