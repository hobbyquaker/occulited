<script lang="ts">
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, REQUEST_HEADER} from '../lib/api';
    import {i18n, t} from '../lib/i18n.svelte';
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
    import {agoParts, bundleVersion, cannotUpdate, deviceActivity, etaParts, filterOthers, filterRows, hmipFirmwareRows, isLive, summary, switchable, type FwActivity, type FwBundle, type FwDevice, type FwResult, type FwRow, type FwStatus} from '../lib/devicefirmware';
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
    // task 48: a held firmware is delivered only on the user's word - the question says what that
    // starts (HmIP: a background transfer of hours; BidCos: nothing until the install)
    // occulited task 68: Install on a device that is READY_FOR_UPDATE - hmipserver's installFirmware;
    // the badge then follows the device's states (Install requested, Installing, Up to date). A
    // request is remembered for the device so the badge says so at once, until the interface's state
    // moves or the WebUI's 10.5 minutes have passed without it
    const INSTALL_WAIT = 630_000;
    let installs = $state<Record<string, {at: number; pending: boolean; error?: string}>>({});
    let installBusy = $state<Record<string, boolean>>({});
    const installReq = (d: FwDevice) => installs[d.address];
    const canInstall = (d: FwDevice) => admin && d.interface === 'HmIP-RF' && d.update_state === 'READY_FOR_UPDATE';
    function shownActivity(d: FwDevice): FwActivity | undefined {
        const a = deviceActivity(d);
        const req = installReq(d);
        if (req && !req.error && d.update_state === 'READY_FOR_UPDATE' && now - req.at < INSTALL_WAIT) return {kind: 'installing', requested: true};
        return a;
    }
    function installNote(d: FwDevice): string {
        const req = installReq(d);
        if (!req) return '';
        if (req.error) return `${t('The install could not be started')}: ${req.error}`;
        if (d.update_state === 'READY_FOR_UPDATE' && now - req.at >= INSTALL_WAIT) return t('The device has not started the install. A battery device starts it when it wakes up or its system button is pressed.');
        if (req.pending && (d.update_state === 'READY_FOR_UPDATE' || d.update_state === 'DO_UPDATE_PENDING')) return t('The device is asleep: it installs when it wakes up or its system button is pressed.');
        return '';
    }
    function targetOf(r: FwRow, d: FwDevice): string {
        return d.available_firmware && d.available_firmware !== '0.0.0' ? d.available_firmware : r.version;
    }
    // one install request; the state is kept per device, the page read again by the caller
    async function requestInstall(d: FwDevice) {
        installBusy = {...installBusy, [d.address]: true};
        try {
            const res = await api.post<{install: {pending: boolean}}>(`/api/system/v1/firmware/devices/${encodeURIComponent(d.address)}/install`);
            installs = {...installs, [d.address]: {at: Date.now(), pending: !!res.install?.pending}};
        } catch (e) {
            installs = {...installs, [d.address]: {at: Date.now(), pending: false, error: (e as Error).message}};
        } finally {
            installBusy = {...installBusy, [d.address]: false};
            now = Date.now();
        }
    }
    const offersInstall = (d: FwDevice) => canInstall(d) && !(installReq(d) && !installReq(d)?.error && now - installReq(d)!.at < INSTALL_WAIT);
    async function install(r: FwRow, d: FwDevice) {
        const target = targetOf(r, d);
        const older = !!target && newerFirmware(d.firmware, target);
        const name = d.name || d.address;
        const ok = await ask({
            title: t('Install {version} on {name}?', {version: target, name}),
            message: `${older ? t('This is an older version than the one installed ({current}): a downgrade. eQ-3 does not support going back; settings and functions can be lost.', {current: d.firmware}) + ' ' : ''}${t('The device restarts with the new firmware and is not available for a moment. A battery device installs when it wakes up or its system button is pressed. Do not cut its power during the install.')}`,
            confirm: t('Install'),
            danger: older,
        });
        if (!ok) return;
        await requestInstall(d);
        await load();
    }
    // occulited task 68 (the maintainer, 2026-10-20): every device that is ready, one request after
    // the other - never in parallel; each device's badge follows its own install as before
    let installingAll = $state(false);
    async function installAll() {
        const list = readyAll;
        if (!list.length || installingAll) return;
        const older = list.filter(({r, d}) => {
            const target = targetOf(r, d);
            return !!target && newerFirmware(d.firmware, target);
        });
        const ok = await ask({
            title: list.length === 1 ? t('Install the firmware on 1 device?') : t('Install the firmware on {n} devices?', {n: String(list.length)}),
            message: `${older.length ? t('For {names} this is an older version than the one installed: a downgrade. eQ-3 does not support going back; settings and functions can be lost.', {names: older.map(({d}) => d.name || d.address).join(', ')}) + ' ' : ''}${t('The devices are asked one after the other. Each restarts with the new firmware and is not available for a moment; a battery device installs when it wakes up or its system button is pressed. Do not cut their power during the install.')}`,
            confirm: t('Install all'),
            danger: older.length > 0,
        });
        if (!ok) return;
        installingAll = true;
        try {
            for (const {d} of list) await requestInstall(d);
        } finally {
            installingAll = false;
            await load();
        }
    }
    async function setTransfer(r: FwRow, on: boolean, chosen?: Bundle) {
        // on delivers the newer held bundle (or the version chosen in the Versions dialog), off holds the offered one back
        const b = on ? (chosen ?? r.deliverable) : r.offered;
        if (!b) return;
        const dir = b.dir || b.type_code;
        // task 49: a version older than what a device runs is a downgrade - its own warning
        const older = on && r.devices.some((d) => newerFirmware(d.firmware, bundleVersion(b)));
        if (older) {
            const ok = await ask({title: t('Deliver the older version {version}?', {version: bundleVersion(b)}), message: t('The devices run a newer firmware. eQ-3 does not support going back: settings and functions can be lost, and an older firmware may not suit a newer device. The transfer takes hours and uses airtime; the frontend then offers the install as an update. Do this only if you know why.'), confirm: t('Deliver older version'), danger: true});
            if (!ok) return;
        } else if (on) {
            const hmip = r.interfaces.some((i) => i !== 'BidCos-RF');
            const ok = await ask({title: t('Deliver {version} to the devices?', {version: bundleVersion(b)}), message: hmip ? t('The devices of this type receive it in the background. This can take hours and uses the radio\'s airtime. You start the install afterwards in the frontend.') : t('The frontend then offers the update. Nothing is transferred until you start it there.'), confirm: t('Deliver')});
            if (!ok) return;
        } else if (isLive(r.status)) {
            const ok = await ask({title: t('Hold this firmware back?'), message: t('The transfer to the devices of this type stops. Delivered again later, it starts over from the beginning, which costs time and airtime.'), confirm: t('Hold back'), danger: true});
            if (!ok) return;
        }
        switching = {...switching, [r.key]: true};
        notice = '';
        try {
            await api.put(`/api/system/v1/firmware/bundles/${encodeURIComponent(dir)}/transfer`, {transfer: on});
        } catch (e) {
            notice = (e as Error).message;
            if (versionsKey) archiveNotice = notice;
        } finally {
            switching = {...switching, [r.key]: false};
            await load();
        }
    }
    // task 49: the versions of a type - on the system (offered and held) and older ones from the
    // OpenCCU firmware archive, fetched on the user's word; the row is looked up again after
    // every load, so the dialog follows the bundles as they move
    interface ArchiveVersion { type: string; version: string; date?: string; path: string; size: number; on_system: boolean }
    let versionsKey = $state<string | null>(null);
    let archive = $state<ArchiveVersion[] | null>(null);
    let archiveError = $state('');
    let archiveNotice = $state('');
    let fetching = $state<Record<string, boolean>>({});
    async function loadArchive(type: string) {
        archiveError = '';
        try {
            const r = await api.get<{versions: ArchiveVersion[]}>(`/api/system/v1/firmware/archive?type=${encodeURIComponent(type)}`);
            archive = r.versions ?? [];
        } catch (e) {
            archive = [];
            archiveError = (e as Error).message;
        }
    }
    function openVersions(r: FwRow) {
        versionsKey = r.key;
        archive = null;
        archiveNotice = '';
        void loadArchive(r.type);
    }
    function closeVersions() {
        versionsKey = null;
        archive = null;
    }
    async function fetchArchived(r: FwRow, v: ArchiveVersion) {
        fetching = {...fetching, [v.path]: true};
        archiveNotice = '';
        try {
            await api.post('/api/system/v1/firmware/archive/fetch', {path: v.path});
        } catch (e) {
            archiveNotice = (e as Error).message;
        } finally {
            fetching = {...fetching, [v.path]: false};
            await load();
            await loadArchive(r.type);
        }
    }
    // task 40: one row per paired device type with one status; what cannot be updated is hidden
    // until Show all devices; the search runs over type, device name and address
    // task 60: HmIP devices only - BidCos-RF, BidCos-Wired and VirtualDevices are not listed
    const view = $derived(st ? hmipFirmwareRows(st.devices, st.deployed, st.last_result) : {rows: [], others: []});
    // task 68: the devices Install all asks for
    const readyAll = $derived(view.rows.flatMap((r) => r.devices.filter(offersInstall).map((d) => ({r, d}))));
    const counts = $derived(summary(view.rows));
    // task 49: the Versions dialog's row, looked up again after every load
    const versionsRow = $derived(versionsKey ? view.rows.find((r) => r.key === versionsKey) : undefined);
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
            case 'held': return t('Downloaded, not delivered');
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
            case 'ready': return t('The device has received the firmware. Install starts the update, here or in the frontend.');
            case 'downloaded': return t('The firmware is on the system and offered to the device. Start the update in the frontend.');
            case 'held': return t('The firmware is on the system. Nothing reaches the devices until you deliver it.');
            case 'available': return t('eQ-3 has a newer firmware; Download now fetches it onto the system.');
            case 'unchecked': return t("No check has read eQ-3's list yet.");
            case 'current': return t('The devices run the newest firmware known.');
            case 'not-listed': return t("eQ-3's list has no firmware for this type. A bundle can still be uploaded by hand.");
            case 'image-only': return t('BidCos-Wired devices get firmware only with the system image.');
            case 'not-updatable': return t('The interface says these devices cannot be updated over the air.');
        }
    }
    // task 51: the row's further lines - in its expanded part and in the badge's tooltip, so a
    // collapsed row stays one line
    const behindText = (r: FwRow) => (r.behind && r.behind < r.devices.length ? t('for {n} of {total} devices', {n: String(r.behind), total: String(r.devices.length)}) : '');
    const olderText = (r: FwRow) => (r.status === 'available' && r.downloaded ? t('{version} is downloaded', {version: r.downloaded}) : '');
    // task 49: an older version the user delivered, while the newest waits
    const deliveredText = (r: FwRow) => (r.offered && r.version && bundleVersion(r.offered) !== r.version ? t('{version} delivered', {version: bundleVersion(r.offered)}) : '');
    const failedText = (r: FwRow) => (r.failed !== undefined ? `${t('Last download failed')}${r.failed ? `: ${r.failed}` : ''}` : '');
    const details = (r: FwRow) => [pausedText(r.activity), progressText(r.activity), etaText(r.activity), behindText(r), olderText(r), deliveredText(r), failedText(r)].filter(Boolean).concat(r.fromName ? [t('from the file name')] : []);
    const hasActions = (r: FwRow) => admin && ((r.status === 'available') || (switchable(r.status) && !!(r.deliverable || r.offered)) || (r.archivable && !cannotUpdate(r.status)));
    const badgeTitle = (r: FwRow) => [statusHint(r.status), ...details(r).filter((d) => d !== t('from the file name'))].join('\n');
    let uploadOpen = $state(false);
    // the live label with its percent, and the "last progress" line under it; with a percent the
    // label is the short one, so the pill holds "Übertragung · 100 %" and the pause mark (B-80)
    const pctOf = (s: FwStatus, a?: FwActivity) => (a && a.kind === s && a.percent !== undefined && (s === 'transferring' || s === 'interrupted') ? a.percent : undefined);
    function activityLabel(s: FwStatus, a?: FwActivity): string {
        // task 68: an install asked for and waiting for the device
        if (s === 'installing' && a?.requested) return t('Install requested');
        const pct = pctOf(s, a);
        if (pct === undefined) return statusLabel(s);
        return `${s === 'interrupted' ? t('Interrupted') : t('Transferring')} · ${pct} %`;
    }
    // B-80: the numbers in the page's language - "45.120 / 101.588 Bytes", "45,120 / 101,588 bytes"
    const num = (n: number) => n.toLocaleString(i18n.language === 'de' ? 'de-DE' : 'en-US');
    function sizeText(a?: FwActivity): string {
        if (!a || a.bytes === undefined || a.size === undefined || a.percent === undefined) return '';
        const params = {n: num(a.bytes), total: num(a.size), percent: String(a.percent)};
        return a.blocks ? t('{n} / {total} blocks ({percent} %)', params) : t('{n} / {total} bytes ({percent} %)', params);
    }
    // B-80: the time left, "about 5 h 20 min left"; unknown while paused or with too few progress lines
    function etaText(a?: FwActivity): string {
        if (!a || a.kind !== 'transferring' || a.percent === undefined) return '';
        const p = a.paused ? undefined : etaParts(a.eta);
        if (!p) return t('time left unknown');
        const time = p.h && p.m ? t('{h} h {m} min', {h: String(p.h), m: String(p.m)}) : p.h ? t('{h} h', {h: String(p.h)}) : t('{m} min', {m: String(p.m)});
        return t('about {time} left', {time});
    }
    // task 72: hmipserver suspended the transfer at the access point's duty-cycle threshold
    function pausedText(a?: FwActivity): string {
        if (!a?.paused) return '';
        const at = a.pausedSince ? new Date(a.pausedSince) : undefined;
        if (!at || Number.isNaN(at.getTime())) return t('Paused (duty cycle); hmipserver continues by itself.');
        return t('Paused (duty cycle) since {time}; hmipserver continues by itself.', {time: at.toLocaleTimeString(i18n.language === 'de' ? 'de-DE' : 'en-GB', {hour: '2-digit', minute: '2-digit'})});
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
<!-- B-80 (the maintainer, 2026-10-16): the live pill - the transfer's progress as its fill from the
     left plus the number in its text, a small pause mark at its end while hmipserver has suspended the
     transfer (task 72). A live pill takes its whole track, which reserves room for "· 100 %" and the
     mark, so it keeps its box in every state: running, paused, 1 to 3 digits, queued, ready. -->
{#snippet pill(s: FwStatus, a: FwActivity | undefined, title: string, cls: string, deviceState?: string)}
    {@const pct = pctOf(s, a)}
    {@const paused = s === 'transferring' && !!a?.paused}
    <span class={`ol-badge ${cls}`} class:fw-live={isLive(s)} class:fw-filled={pct !== undefined} class:good={s === 'downloaded' || isLive(s)} class:warn={s === 'available'} {title} data-badge={deviceState ? undefined : ''} data-device-state={deviceState} data-paused={paused ? '' : undefined}>
        {#if pct !== undefined}<span class="fw-fill" class:fw-fill-stopped={s === 'interrupted' || paused} role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={statusLabel(s)} data-progress={pct} style={`width: ${pct}%`}></span>{/if}
        <span class="fw-badge-text">{activityLabel(s, a)}</span>
        {#if isLive(s)}<span class="fw-pause" aria-hidden={paused ? undefined : 'true'} role={paused ? 'img' : undefined} aria-label={paused ? t('paused (duty cycle)') : undefined} data-pause-mark={paused ? '' : undefined}>{#if paused}<Icon name="pause" size={10} />{/if}</span>{/if}
    </span>
{/snippet}

{#snippet version(v: {version: string; version_from_name?: string; date_from_name?: string})}{#if v.version}{v.version}{:else if v.version_from_name}<span class="ol-muted ol-fromname" title={t('The bundle states no version; this one is read from the name of its update file.')}>{v.date_from_name ? `${v.version_from_name} (${v.date_from_name})` : v.version_from_name}{' '}<em class="ol-fromname-mark">{t('from the file name')}</em></span>{:else}<span class="ol-muted">–</span>{/if}{/snippet}

<SystemTitle />

<!-- the maintainer, 2026-09-19/20: the system update (from the Status page), the radio firmware
     (from the Interfaces page) and the paired devices' firmware, each in a panel of its own -->
<!-- occulited B-68: the system update is an administrator's alone; for anyone else no empty panel -->
{#if admin}
    <section class="ol-panel" data-panel="system-update">
        <SystemUpdate container={!!status?.container} platform={status?.version?.platform} />
    </section>
{/if}

<WarnEdge ids={['radio-firmware', 'radio-module-unusable']}><RadioFirmware /></WarnEdge>

<!-- task 40 (the maintainer, 2026-10-13): one device-firmware view instead of the paired devices, the
     last run and the deployed bundles - one row per paired device type with one status, what cannot
     be updated hidden until Show all devices, a search, the upload behind a button -->
<section class="ol-panel" data-panel="device-firmware">
<!-- task 51: how the page works is help, not the system's state - behind the heading's ? -->
<h2 id="device-firmware">{t('Device firmware')}<Help>{t('Firmware for the paired device types is fetched from eQ-3 - on Check now, and once a day with Check daily - and made available to the interfaces. Nothing is installed onto a device by itself: that stays your decision in the frontend.')} {t('hmipserver (HmIP-RF, access points) and rfd (BidCos-RF) read it from /etc/config/firmware; BidCos-Wired devices get firmware only with the system image.')}<br /><br />{t('Firmware is transferred to the devices in the background, one device after the other; battery devices receive it only while awake, which can take 8 to 42 hours.')}<br /><br />{t('With Deploy automatically off (the default), a download for HmIP devices waits in /etc/config/firmware-held until you deliver it: hmipserver does not see it there and transfers nothing. Deploy automatically on delivers every download at once. BidCos firmware is offered to the frontends straight after the download, whatever the switch says: rfd transfers nothing by itself, the update stays a click in the frontend.')}</Help></h2>
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
            <label class="fw-auto"><input type="checkbox" checked={st.auto_deploy === true} disabled={autoSaving} onchange={(e) => setAutoDeploy((e.currentTarget as HTMLInputElement).checked)} data-auto-deploy /> {t('Deploy automatically')}</label>
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
            {#if counts.held}<span class="ol-badge fw-sum" data-sum="held">{t('{n} not delivered', {n: String(counts.held)})}</span>{/if}
            {#if counts.available}<span class="ol-badge warn fw-sum" data-sum="available">{t('{n} available at eQ-3', {n: String(counts.available)})}</span>{/if}
            {#if counts.current}<span class="ol-badge fw-sum" data-sum="current">{t('{n} up to date', {n: String(counts.current)})}</span>{/if}
            {#if !counts.downloaded && !counts.available && !counts.transferring && !counts.installing && !counts.held}<span class="ol-muted fw-sum-none">{t('No update waiting.')}</span>{/if}
            <!-- task 68: every device that is ready, one after the other; the label stays while it runs (no resize) -->
            {#if admin && (readyAll.length || installingAll)}<button type="button" class="hmm-button fw-install-all" disabled={installingAll} aria-busy={installingAll} onclick={() => void installAll()} data-install-all>{t('Install all')}</button>{/if}
        </p>
        <!-- task 48 (the maintainer, 2026-10-14): held firmware is invisible to clients - say so where it is -->
        {#if counts.held}<p class="ol-muted fw-held-note" data-fw-held-note>{t('Other apps show firmware that is not delivered as up to date.')}</p>{/if}
        <!-- task 44: what the background transfer means, while one is on its way -->
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
                            <!-- task 51 (the maintainer, 2026-10-15): one line per row and fixed tracks - the
                                 version, the badge (the transfer's bar inside it), the text icons and the actions
                                 at the same x in every row; the row's other lines are in its expanded part and the
                                 badge's tooltip. The version has its own track (homematic-manager task 93). -->
                            <div class="fw-status-line">
                                <span class="fw-ver fw-num" class:ol-fromname={!!r.fromName} title={r.fromName ? t('The bundle states no version; this one is read from the name of its update file.') : undefined} data-version>{r.version}</span>
                                {@render pill(r.status, r.activity, badgeTitle(r), 'fw-badge')}
                                <span class="fw-texts">{#each texts as f (f)}<button type="button" class="ol-iconlink fw-icon" onclick={() => openFile(r.bundle!, f)} aria-label={f} title={f === 'info' ? t('The bundle\'s info') : t('The changelog')} data-text={f}><Icon name={f === 'info' ? 'info' : 'log'} size={16} /></button>{/each}{#if r.failed !== undefined}<span class="fw-icon fw-failed-icon" role="img" aria-label={t('Last download failed')} title={`${t('Last download failed')}${r.failed ? `: ${r.failed}` : ''}`} data-failed-icon><Icon name="alert" size={16} /></span>{/if}</span>
                                {#if hasActions(r)}<span class="fw-actions">
                                    {#if r.status === 'available' && admin}<button type="button" class="hmm-button fw-download" disabled={st.running} onclick={check} data-download>{st.running ? t('Running…') : t('Download now')}</button>{/if}
                                    <!-- task 48: a held firmware is delivered by the user's action; a delivered one can be held back again (task 45) -->
                                    {#if admin && switchable(r.status)}
                                        {#if r.deliverable}
                                            <button type="button" class="hmm-button fw-deliver" disabled={switching[r.key]} onclick={() => void setTransfer(r, true)} data-deliver>{t('Deliver to devices')}</button>
                                        {:else if r.offered}
                                            <button type="button" class="ol-filelink fw-holdback" disabled={switching[r.key]} onclick={() => void setTransfer(r, false)} data-hold-back>{t('Hold back')}</button>
                                        {/if}
                                    {/if}
                                    <!-- task 49: which version the devices receive, older ones from the archive -->
                                    <span class="fw-vslot">{#if admin && r.archivable && !cannotUpdate(r.status)}<button type="button" class="ol-filelink fw-versions" onclick={() => openVersions(r)} data-versions>{t('Versions')}</button>{/if}</span>
                                </span>{/if}
                            </div>
                        </td>
                    </tr>
                    {#if open}
                        <!-- task 51: what the collapsed row leaves out - the transfer's last progress, how many devices are
                             behind, where the version comes from, a delivered older version, a failed download -->
                        {#if details(r).length}
                            <tr class="fw-devices-row"><td colspan="4" class="ol-stack-line">
                                <div class="fw-details" data-details>
                                    {#if pausedText(r.activity)}<span class="fw-paused-text" data-paused-text>{pausedText(r.activity)}</span>{/if}
                                    {#if progressText(r.activity)}<span class="ol-muted" data-last-progress>{progressText(r.activity)}</span>{/if}
                                    {#if etaText(r.activity)}<span class="ol-muted" data-eta>{etaText(r.activity)}</span>{/if}
                                    {#if r.fromName}<span class="ol-muted" title={t('The bundle states no version; this one is read from the name of its update file.')} data-from-name><em>{t('from the file name')}</em>{r.fromName.date ? ` · ${r.fromName.date}` : ''}</span>{/if}
                                    {#if behindText(r)}<span class="ol-muted" data-behind>{behindText(r)}</span>{/if}
                                    {#if olderText(r)}<span class="ol-muted" data-older-download>{olderText(r)}</span>{/if}
                                    {#if deliveredText(r)}<span class="ol-muted" data-delivered>{deliveredText(r)}</span>{/if}
                                    {#if r.failed !== undefined}<span class="fw-failed" data-failed>{t('Last download failed')}{r.failed ? `: ${r.failed}` : ''}</span>{/if}
                                </div>
                            </td></tr>
                        {/if}
                        <!-- B-81 (the maintainer, 2026-10-16: "keep versions and status badge aligned with the outer
                             tables columns"): a device is a row of the table - its name and address under the type,
                             its version under Installed, the target version and its badge in the status cell's
                             tracks, at the x of the type row's -->
                        {#each r.devices as d (d.interface + d.address)}
                            {@const act = shownActivity(d)}
                            {@const behind = !!r.version && newerFirmware(r.version, d.firmware) && !cannotUpdate(r.status)}
                            <tr class="fw-device" data-device-row data-type-of={r.type} data-device={d.address}>
                                <td colspan="2" class="ol-stack-line fw-dwho">
                                    <span class="fw-dname">{#if d.name}{d.name}{:else}<span class="ol-muted">{t('unnamed')}</span>{/if}</span>
                                    <span class="hmm-mono fw-daddr">{d.address}</span>
                                </td>
                                <td data-label={t('Installed')} class="fw-num fw-dfw"><span data-device-installed>{d.firmware || '–'}</span>{#if behind}{' '}<span class="fw-arrow" aria-hidden="true">→</span>{/if}</td>
                                <td data-label={t('Status')} class="fw-status" class:fw-dnone={!act && !behind}>
                                    <div class="fw-status-line">
                                        <span class="fw-ver fw-num fw-target" title={behind ? statusLabel(r.status) : undefined} data-device-version>{behind ? r.version : ''}</span>
                                        {#if act}{@render pill(act.kind, act, [pausedText(act) || statusHint(act.kind), progressText(act), etaText(act)].filter(Boolean).join('\n'), 'fw-badge fw-dact', act.kind)}{/if}
                                        <!-- task 68: the install of a transferred firmware, in the actions track -->
                                        {#if offersInstall(d)}<span class="fw-actions"><button type="button" class="hmm-button fw-install" disabled={installBusy[d.address] || installingAll} onclick={() => void install(r, d)} data-install>{t('Install')}</button></span>{/if}
                                        {#if installNote(d)}<span class="fw-dprog" class:fw-failed={!!installReq(d)?.error} data-install-note>{installNote(d)}</span>{/if}
                                        <!-- B-80: the device's own numbers - transferred of total, the time left, the last progress -
                                             in fixed widths, so the line does not jump while they grow -->
                                        {#if act && (act.kind === 'transferring' || act.kind === 'interrupted') && (sizeText(act) || progressText(act))}
                                            <span class="fw-dprog" data-device-progress>
                                                <span class="fw-num fw-dbytes" data-bytes>{sizeText(act)}</span>
                                                <span class="fw-deta" data-eta>{etaText(act)}</span>
                                                <span class="ol-muted" data-last-progress>{progressText(act)}</span>
                                            </span>
                                        {/if}
                                    </div>
                                </td>
                            </tr>
                        {/each}
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

<!-- task 49: the versions of a type; older ones from the OpenCCU firmware archive -->
<ModalDialog open={versionsKey !== null} title={t('Firmware versions')} subtitle={versionsRow?.type ?? ''} size="medium" onclose={closeVersions}>
    {#if versionsRow}
        {@const r = versionsRow}
        <p class="ol-muted fw-versions-help">{t('Which version the devices of this type receive. The newest is the default.')}</p>
        {#if archiveNotice}<div class="ol-notice error" data-archive-notice>{archiveNotice}</div>{/if}
        <h3 class="fw-versions-h">{t('On the system')}</h3>
        <table class="ol-table fw-versions-table" data-versions-system>
            <colgroup><col class="fw-v-ver" /><col /><col class="fw-v-act" /></colgroup>
            <tbody>
                {#each r.bundles as b (b.dir || b.type_code)}
                    <tr data-version={bundleVersion(b)}>
                        <td class="fw-num">{bundleVersion(b) || '–'}</td>
                        <td>{#if !b.held}<span class="ol-badge good">{t('Delivered')}</span>{:else}<span class="ol-muted">{t('Not delivered')}</span>{/if}</td>
                        <td class="ol-actions">{#if b.held}<button type="button" class="hmm-button" disabled={switching[r.key]} onclick={() => void setTransfer(r, true, b)} data-deliver-version>{t('Deliver')}</button>{/if}</td>
                    </tr>
                {:else}
                    <tr><td class="ol-muted" colspan="3">{t('No firmware of this type on the system.')}</td></tr>
                {/each}
            </tbody>
        </table>
        <h3 class="fw-versions-h">{t('Older versions')}<Help>{t('From the OpenCCU firmware archive on GitHub (github.com/OpenCCU/HMDeviceFirmware), which keeps eQ-3\'s past update files; eQ-3 serves only the newest. A download is checked like one from eQ-3 and is not delivered by itself. The system keeps the newest and the previously delivered version of a type.')}</Help></h3>
        {#if archive === null}
            <div class="ol-muted">{t('Loading…')}</div>
        {:else if archiveError}
            <div class="ol-notice error" data-archive-error>{archiveError}</div>
        {:else if archive.length === 0}
            <div class="ol-muted" data-archive-none>{t('The archive has no firmware for this type.')}</div>
        {:else}
            <table class="ol-table fw-versions-table" data-versions-archive>
            <colgroup><col class="fw-v-ver" /><col /><col class="fw-v-act" /></colgroup>
                <tbody>
                    {#each archive as v (v.path)}
                        <tr data-archive-version={v.version}>
                            <td class="fw-num">{v.version}</td>
                            <td class="ol-muted">{v.date ?? ''}</td>
                            <td class="ol-actions">{#if v.on_system}<span class="ol-muted">{t('On the system')}</span>{:else}<button type="button" class="hmm-button" disabled={fetching[v.path]} onclick={() => void fetchArchived(r, v)} data-fetch>{fetching[v.path] ? t('Running…') : t('Download')}</button>{/if}</td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        {/if}
    {/if}
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
    .fw-c-if { width: 7.5em; }
    .fw-c-inst { width: 8.5em; }
    .fw-c-status { width: 58%; }
    .fw-num { font-variant-numeric: tabular-nums; }
    .fw-expand { display: inline-flex; align-items: baseline; gap: 8px; padding: 0; border: 0; background: none; color: var(--hmm-fg); font: inherit; text-align: left; cursor: pointer; }
    .fw-expand:focus-visible { outline: 1px solid var(--hmm-focus); outline-offset: 2px; }
    .fw-typename { font-weight: 600; overflow-wrap: anywhere; }
    /* task 51: a collapsed row is one line - the type's name and count do not break on a desktop */
    @media (min-width: 701px) { .fw-expand, .fw-typename, .fw-row > td[data-fw-installed] { white-space: nowrap; overflow-wrap: normal; } }
    .fw-row > td { vertical-align: middle; }
    .fw-count { font-size: var(--hmm-font-size-small); white-space: nowrap; }
    /* the disclosure triangle: points right while closed, down when open */
    .fw-chev { display: inline-block; width: 0; height: 0; border-style: solid; border-width: 4px 0 4px 6px; border-color: transparent transparent transparent var(--hmm-fg-muted); transition: transform 0.15s; align-self: center; }
    .fw-expand[aria-expanded='true'] .fw-chev { transform: rotate(90deg); }
    @media (prefers-reduced-motion: reduce) { .fw-chev { transition: none; } }
    /* task 51: fixed tracks - version, badge, text icons, actions - so each starts at the same x in
       every row; the badge's text is cut with an ellipsis (the whole text in its tooltip) rather than
       pushing the rest along, and the actions keep to the right edge */
    .fw-status-line { display: grid; grid-template-columns: 4.5em 13.5rem 46px minmax(max-content, 1fr); grid-template-areas: 'ver badge texts actions'; align-items: center; column-gap: 8px; row-gap: 4px; min-height: 24px; }
    .fw-ver { grid-area: ver; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .fw-badge { grid-area: badge; position: relative; margin-left: 0; max-width: 100%; justify-self: start; overflow: hidden; text-overflow: ellipsis; box-sizing: border-box; }
    .fw-badge-text { display: block; overflow: hidden; text-overflow: ellipsis; }
    .fw-texts { grid-area: texts; display: inline-flex; white-space: nowrap; }
    .fw-actions { grid-area: actions; justify-self: end; display: inline-flex; align-items: center; gap: 12px; white-space: nowrap; }
    /* Versions has a slot of its own at the right end, kept where a row has none, so the action
       before it lines up as well */
    .fw-vslot { margin-left: auto; min-width: 5.5em; text-align: right; }
    /* B-80: a live pill fills its track (the badge track of task 51), so its box is the same in
       every live state; the text is cut with an ellipsis, the pause mark has its slot at the end
       whether shown or not */
    .ol-badge.fw-live { display: flex; align-items: center; width: 100%; justify-self: stretch; position: relative; overflow: hidden; box-sizing: border-box; }
    .fw-live > .fw-badge-text { position: relative; flex: 1 1 auto; min-width: 0; }
    .fw-pause { position: relative; flex: 0 0 10px; width: 10px; height: 10px; margin-left: 4px; display: inline-flex; }
    /* the progress as the pill's fill from the left, a muted shade of its colour; a stopped
       transfer (interrupted, paused) in the warn colour */
    .fw-fill { position: absolute; left: 0; top: 0; bottom: 0; background: color-mix(in srgb, var(--hmm-accent) 30%, var(--hmm-accent-bg)); transition: width 0.4s linear; }
    /* the text over a fill in the page's own colour: readable over both halves in both themes */
    .ol-badge.fw-filled { color: var(--hmm-fg); }
    .fw-fill.fw-fill-stopped { background: color-mix(in srgb, var(--hmm-warn) 34%, var(--hmm-accent-bg)); }
    @media (prefers-reduced-motion: reduce) { .fw-fill { transition: none; } }
    /* task 51: the expanded row's lines above its devices */
    .fw-details { display: flex; flex-wrap: wrap; gap: 2px 14px; padding: 2px 0 6px 14px; font-size: var(--hmm-font-size-small); }
    .fw-failed-icon { color: var(--hmm-error); cursor: default; }
    .fw-failed-icon:hover { background: none; }
    .fw-paused-text { color: var(--hmm-warn); }
    /* B-81: a type's devices, rows of the table on the sunken ground of the open type */
    /* top-aligned: a device's transfer line under its badge leaves the other cells on the badge's line */
    .fw-device > td { background: var(--hmm-bg-sunken); vertical-align: top; }
    .fw-device > td.fw-dwho, .fw-device > td.fw-dfw { line-height: 24px; }
    @media (max-width: 700px) { .fw-device > td.fw-dnone { display: none !important; } .fw-device { background: var(--hmm-bg-sunken); } .fw-device > td { background: transparent; } }
    .fw-dwho { padding-left: 26px !important; }
    .fw-dwho .fw-daddr { margin-left: 10px; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); }
    /* B-80: a device's transfer line under its badge - fixed widths, the numbers in tabular figures */
    .fw-dprog { grid-column: 2 / -1; display: flex; flex-wrap: wrap; gap: 0 14px; font-size: var(--hmm-font-size-small); }
    .fw-dbytes { min-width: 27ch; white-space: nowrap; }
    .fw-deta { min-width: 20ch; white-space: nowrap; }
    /* task 45: the two switches */
    .fw-auto { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    /* task 48: Deliver to devices, and Hold back as a quiet link */
    .fw-deliver { padding: 1px 10px; font-size: var(--hmm-font-size-small); }
    .fw-holdback { width: auto; }
    .fw-held-note { margin: -4px 0 10px; font-size: var(--hmm-font-size-small); }
    /* task 49: the versions dialog */
    .fw-versions { width: auto; }
    .fw-versions-help { margin: 0 0 10px; }
    .fw-versions-h { margin: 14px 0 6px; font-size: var(--hmm-font-size); }
    .fw-versions-table { margin-bottom: 8px; }
    .fw-versions-table td { white-space: nowrap; }
    .fw-versions-table { table-layout: fixed; width: 100%; }
    .fw-v-ver { width: 6em; }
    .fw-v-act { width: 9em; }
    .fw-versions-table .ol-actions { text-align: right; }
    .fw-failed { color: var(--hmm-error); }
    .fw-download, .fw-install, .fw-install-all { padding: 1px 10px; font-size: var(--hmm-font-size-small); }
    .fw-install-all { margin-left: auto; }
    .fw-row.fw-muted .fw-typename { font-weight: normal; color: var(--hmm-fg-muted); }
    .fw-devices-row > td { background: var(--hmm-bg-sunken); }
    .fw-dname { overflow-wrap: anywhere; min-width: 0; }
    .fw-arrow, .fw-target { color: var(--hmm-warn); white-space: nowrap; }
    .fw-hidden { margin-top: 8px; }
    .fw-others { margin-top: 14px; }
    .fw-others > summary { cursor: pointer; color: var(--hmm-fg-muted); font-weight: 600; padding: 4px 0; }
    .fw-others-help { margin: 4px 0 6px; font-size: var(--hmm-font-size-small); }
    .fw-upload-help { margin: 0 0 12px; }
    .fw-upload { margin-bottom: 16px; }
    .fw-upload input[type='file'] { max-width: 100%; }
    /* below a desktop's width the actions take a second line, starting under the badge */
    @media (max-width: 1100px) {
        .fw-status-line { grid-template-columns: 4.5em 13.5rem 46px; grid-template-areas: 'ver badge texts' '. actions actions'; }
        .fw-actions { justify-self: stretch; }
    }
    @media (max-width: 700px) {
        /* a phone: the stacked cell wraps by itself, the label first */
        .ol-table.fw-types > tbody > tr > td.fw-status { flex: 1 1 100%; display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
        .fw-status-line { display: contents; }
        .fw-vslot { min-width: 0; margin-left: 0; text-align: left; }
        .fw-vslot:empty { display: none; }
        .fw-badge { max-width: 100%; }
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
