<script lang="ts">
    /*
     * Task 16: the system update - a release file staged for the recovery system, the daily release
     * check, and the install, whose progress is the recovery system's own page (D-56). The
     * maintainer, 2026-09-19: on the Updates page (it was the Status page's last section), above the
     * device firmware.
     *
     * way_back (task 96): a file that is not an openccu-lite release by its name; staging one
     * switches HSTS off (max-age=0), and the page says what that means for the browser.
     */
    import {onMount} from 'svelte';
    import {api, ApiError, REQUEST_HEADER, type HTTPSView} from './api';
    import {auth} from './auth.svelte';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import {link} from './router.svelte';
    import {pageLife} from './pagelife.svelte';
    import BootBar from './BootBar.svelte';
    import HSTSClearing from './HSTSClearing.svelte';
    import Help from './Help.svelte';
    import {EASE_MS, type BootEntry} from './bootbar';
    import {beginBoot, readEntry, removeEntry, watchBoot} from './bootwatch';
    import {boxUptime} from './power';
    import CheckDaily from './CheckDaily.svelte';
    import UpdateChannel, {channelName} from './UpdateChannel.svelte';
    import {normalChannel, type Channel} from './updatechannel';
    import {lookupBoxAddress, type BoxAddress, type NetworkView} from './recovery';
    import {hstsRemembered} from './hsts';

    /** container: a container has no recovery system to stage a file for; platform: for the boot entry */
    let {container = false, platform}: {container?: boolean; platform?: string} = $props();
    let https = $state<HTTPSView | null>(null);

    // task 16: the system firmware update, staged for the recovery system. It is also how you
    // flash OpenCCU back - but the way back is that flash *plus* the backup taken before the
    // migration (D-35, revised 2026-09-08), not the flash on its own.
    // way_back (task 96): not an openccu-lite release by its name; staging one switches HSTS off (max-age=0)
    interface Staged { file: string; size: number; kind: string; version?: string; board?: string; warning?: string; recovery_armed: boolean; way_back?: boolean }
    let staged = $state<Staged | null>(null);
    interface Feed { enabled: boolean; channel?: string; none_in_channel?: boolean; feed_url?: string; checked?: string; error?: string; error_host?: string; error_timeout?: number; installed_newer?: boolean; downloading?: string; download_error?: string; available: {version: string; name: string; size: number; newer: boolean; notes_url?: string; published?: string} | null }
    let feed = $state<Feed | null>(null);
    // task 256 (the maintainer): the installed version is named here, beside the release check - no
    // longer in the top bar or on Settings. The image's version from this route, occulited's build
    // from the open health route; the API keeps both for clients. On openccu-lite its version alone
    // is shown (occulited task 47: each round tags occulited and the OpenCCU-Base fork with it).
    interface Running { version?: string; lite?: string }
    let running = $state<Running | null>(null);
    let build = $state('');
    const shortBuild = (v: string) => (/^[0-9a-f]{40}$/.test(v) ? v.slice(0, 7) : v);
    async function loadBuild() {
        try {
            const r = await fetch('/api/system/v1/health', {cache: 'no-store'});
            const h = (r.ok ? await r.json() : null) as {version?: string} | null;
            build = h?.version ? shortBuild(h.version) : '';
        } catch {
            build = '';
        }
    }
    // B-247: the recovery unpacks the file on the system's own storage; a file that would not fit is
    // refused before the reboot, with the free and the needed space
    const gbText = (n: unknown) => `${(Number(n) / 1e9).toFixed(1)} GB`;
    function updateError(code: string | undefined, message: string, detail?: Record<string, unknown>): string {
        // B-56: the feed's host accepted and did not answer
        if (code === 'feed-unreachable' && detail?.host) return noAnswer(String(detail.host), Number(detail.timeout));
        if (code === 'no-space' && detail) {
            // B-99: refused before the file was written - the file itself and its unpacking
            if (detail.download) return t('Not enough space for this update: {free} free on the system, {required} needed to store and unpack it. Remove old backups on the Backup page, or keep them on a USB stick or a share.', {free: gbText(detail.free), required: gbText(detail.required)});
            return t('Not enough space for this update: {free} free on the system, {required} needed to unpack it. Remove old backups on the Backup page, or keep them on a USB stick or a share.', {free: gbText(detail.free), required: gbText(detail.required)});
        }
        // B-111: another tab's (or this one's) update is in flight; a download ended by Cancel
        if (code === 'busy' && detail?.phase) return t('Another update is in progress: {what}', {what: busyWhat(String(detail.phase), detail.file ? String(detail.file) : '')});
        if (code === 'download-cancelled') return t('The download was cancelled.');
        return message;
    }
    const busyWhat = (phase: string, file: string) => (phase === 'download' ? (file ? t('a download of {file}', {file}) : t('a download')) : phase === 'upload' ? (file ? t('an upload of {file}', {file}) : t('an upload')) : t('the installation'));
    const noAnswer = (host: string, s: number) => t('{host} does not answer: no answer within {s} seconds. Try again later.', {host, s});
    // B-57: a system ahead of the feed (a round not yet published, a local build) runs a newer one
    // task 118: "newest" is the channel's - on Stable while a newer beta runs, nothing is offered
    const upToDateText = (f: Feed) => (f.installed_newer && f.available ? t('This system runs a newer version than the newest release in its channel ({v}).', {v: f.available.version}) : t('This system runs the newest release.'));
    // task 118: a new channel forgets the last check; the next one is the user's (D-90)
    let channelChanged = $state(false);
    function channelStored(c: Channel) {
        if (feed && normalChannel(feed.channel) !== c) {
            feed = {...feed, channel: c, available: null, checked: undefined, error: undefined, none_in_channel: false, installed_newer: false};
            channelChanged = true;
        }
    }
    const feedErrorText = (f: Feed) => (f.error_host ? noAnswer(f.error_host, f.error_timeout ?? 0) : (f.error ?? ''));
    const errText = (err: unknown) => (err instanceof ApiError ? updateError(err.code, err.message, err.detail) : (err as Error).message);
    let updFile = $state<File | null>(null);
    let updBusy = $state(false);
    let updNotice = $state('');
    async function loadUpdate() {
        try {
            const r = await api.get<{running?: Running; staged: Staged | null; feed: Feed | null; busy?: Busy | null}>('/api/system/v1/system-update');
            running = r.running ?? null;
            staged = r.staged;
            feed = r.feed;
            apiBusy = r.busy ?? null;
            // B-111: the install started from another tab (or before a reload): take its countdown over
            if (apiBusy?.phase === 'install' && !installing) void resumeInstallWatch();
        } catch {
            staged = null;
        }
    }
    // B-111: the one system update in flight as the API reports it - a download from the feed, an
    // upload, or the install once its reboot has started - so a reload or a second tab shows the
    // same state as the tab that clicked. With this tab's own request in flight it is the page's
    // one busy state: every control that would start, restart or change an update is disabled
    // while it is set (same box, no resize), a status line names the phase and the file, and a
    // download has its Cancel (DELETE /system-update). The page polls while it is set.
    interface Busy { phase: 'download' | 'upload' | 'install'; file?: string }
    let apiBusy = $state<Busy | null>(null);
    // what this tab's own request is, shown until the API reports the phase itself
    let localPhase = $state<'' | 'download' | 'upload' | 'install' | 'check' | 'discard'>('');
    let cancelling = $state(false);
    async function cancelDownload() {
        cancelling = true;
        try {
            await api.del('/api/system/v1/system-update');
            updNotice = t('The download was cancelled.');
        } catch (err) {
            updNotice = errText(err);
        } finally {
            cancelling = false;
            await loadUpdate();
        }
    }
    async function uploadUpdate(e: Event) {
        e.preventDefault();
        if (!updFile) return;
        updBusy = true;
        localPhase = 'upload';
        updNotice = '';
        try {
            const fd = new FormData();
            fd.append('file', updFile);
            const res = await fetch('/api/system/v1/system-update/upload', {method: 'POST', headers: REQUEST_HEADER, body: fd});
            const data = (await res.json()) as Staged & {error?: string; message?: string; detail?: Record<string, unknown>; hsts_error?: string};
            if (data.error) throw new Error(updateError(data.error, data.message ?? data.error, data.detail));
            updNotice = [data.warning ?? '', data.hsts_error ? t('HSTS could not be switched off: {e}', {e: data.hsts_error}) : ''].filter(Boolean).join(' ');
            updFile = null;
        } catch (err) {
            updNotice = (err as Error).message;
        } finally {
            updBusy = false;
            localPhase = '';
            await loadUpdate();
            // a way back switched HSTS off (task 96): the staged block says what to do now
            try { https = await api.get<HTTPSView>('/api/system/v1/https'); } catch { /* the next poll */ }
        }
    }
    // The install runs in the recovery system, which has no HTTPS (D-56): the notice links to its
    // progress by the box's address (lib/recovery.ts), resolved as the install starts, says that
    // the page comes back by itself - it reloads once the box answers again - and points to the
    // same address when the box has been away for longer than an install takes.
    const INSTALL_WAIT_MIN = 10;
    // boot: the countdown written before the request (task 94, lib/bootbar.ts)
    let installing = $state<{address: BoxAddress; late: boolean; boot: BootEntry} | null>(null);
    let stopInstallWatch: (() => void) | null = null;
    let lateTimer: ReturnType<typeof setTimeout> | undefined;
    // B-111: the busy state - the API's, this tab's running install, or this tab's own request
    const busy = $derived<Busy | null>(apiBusy ?? (installing ? {phase: 'install', file: staged?.file} : null) ?? (localPhase === 'download' ? {phase: 'download', file: feed?.available?.name} : localPhase === 'upload' ? {phase: 'upload', file: updFile?.name} : null));
    const blocked = $derived(updBusy || !!busy);
    // the install phase shows the reboot notice only: no staged block, no feed line, no upload
    const installPhase = $derived(!!installing || busy?.phase === 'install');
    function busyLine(b: Busy): string {
        if (b.phase === 'upload') return t('Uploading {name}…', {name: b.file ?? ''});
        const av = feed?.available;
        if (av && av.name === b.file) return t('Downloading {name} ({mb} MB)…', {name: av.name, mb: Math.round(av.size / 1048576)});
        return t('Downloading {name}…', {name: b.file ?? ''});
    }
    // B-111: a tab that did not click Reboot and install sees the install from the API; it takes
    // over the countdown the clicking tab wrote (the same browser), or starts one of its own
    let resuming = false;
    async function resumeInstallWatch() {
        if (resuming || installing) return;
        resuming = true;
        try {
            const [address, before] = await Promise.all([lookupBoxAddress(location.hostname, () => api.get<{network?: NetworkView}>('/api/system/v1/network')), boxUptime()]);
            const kept = readEntry();
            const entry = kept && kept.kind === 'update' ? kept : await beginBoot('update', () => api.get('/api/system/v1/boot-expect?kind=update'), platform);
            if (installing) return;
            installing = {address, late: false, boot: entry};
            watchInstall(before, entry);
        } finally {
            resuming = false;
        }
    }
    function watchInstall(before: number, entry: BootEntry) {
        stopInstallWatch?.();
        clearTimeout(lateTimer);
        stopInstallWatch = watchBoot({
            entry,
            before,
            pollMs: 5000,
            onUpdate: (e) => {
                if (installing) installing = {...installing, boot: e};
            },
            onBack: () => setTimeout(() => location.reload(), EASE_MS),
        });
        lateTimer = setTimeout(() => {
            if (installing) installing = {...installing, late: true};
        }, INSTALL_WAIT_MIN * 60_000);
    }
    async function installUpdate() {
        if (!staged) return;
        // task 50 (the maintainer, 2026-10-14): what is kept and what it costs, not how it works
        const question = [t('Reboot now and install {file}? Settings, pairings and addons are kept; the system is unreachable for a few minutes.', {file: staged.file})];
        // task 96: the way back while a browser may still remember HSTS for the box's name
        if (staged.way_back && https?.hsts) question.push(t('HSTS is on: if a browser refuses the system\'s name afterwards, open the system by its IP address.'));
        else if (staged.way_back && https && hstsRemembered(https)) question.push(t('If a browser refuses the system\'s name afterwards, open the system by its IP address.'));
        if (!(await ask(question.join('\n\n')))) return;
        updBusy = true;
        localPhase = 'install';
        // while the box still answers: where the recovery will be, and the uptime to tell the new boot by
        const [address, before] = await Promise.all([lookupBoxAddress(location.hostname, () => api.get<{network?: NetworkView}>('/api/system/v1/network')), boxUptime()]);
        const entry = await beginBoot('update', () => api.get('/api/system/v1/boot-expect?kind=update'), platform);
        try {
            const r = await api.post<{rebooting: boolean; message?: string}>('/api/system/v1/system-update/install');
            if (r.rebooting) {
                updNotice = '';
                installing = {address, late: false, boot: entry};
                watchInstall(before, entry);
            } else {
                removeEntry();
                updNotice = r.message ?? '';
            }
        } catch (err) {
            if (err instanceof ApiError) {
                removeEntry();
                updNotice = errText(err);
            } else {
                // no answer at all: the box went away with the request, into the recovery
                updNotice = '';
                installing = {address, late: false, boot: entry};
                watchInstall(before, entry);
            }
        } finally {
            updBusy = false;
            localPhase = '';
            await loadUpdate();
        }
    }
    async function checkFeed() {
        updBusy = true;
        localPhase = 'check';
        try {
            feed = (await api.post<{feed: Feed}>('/api/system/v1/system-update/check')).feed;
            channelChanged = false;
            updNotice = feed.available ? (feed.available.newer ? t('Release {v} is available.', {v: feed.available.version}) : upToDateText(feed)) : feed.none_in_channel ? t('No release in the {channel} channel yet.', {channel: channelName(normalChannel(feed.channel))}) : feedErrorText(feed);
        } catch (err) {
            updNotice = errText(err);
            await loadUpdate();
        } finally {
            updBusy = false;
            localPhase = '';
        }
    }
    // task 244: Check daily behind Check now - the release feed's daily check
    async function setDaily(on: boolean) {
        try {
            await api.put('/api/system/v1/system-update/settings', {enabled: on});
            if (feed) feed = {...feed, enabled: on};
        } catch (err) {
            updNotice = (err as Error).message;
            throw err;
        }
    }
    const feedHost = (u?: string) => {
        try {
            return u ? new URL(u).host : 'api.github.com';
        } catch {
            return u ?? '';
        }
    };
    async function downloadRelease() {
        if (!feed?.available) return;
        updBusy = true;
        localPhase = 'download';
        updNotice = '';
        try {
            const u = await api.post<Staged>('/api/system/v1/system-update/download');
            updNotice = u.warning ?? '';
        } catch (err) {
            updNotice = errText(err);
        } finally {
            updBusy = false;
            localPhase = '';
            await loadUpdate();
        }
    }
    async function discardUpdate() {
        updBusy = true;
        localPhase = 'discard';
        try {
            await api.del('/api/system/v1/system-update');
            updNotice = '';
        } catch (err) {
            updNotice = errText(err);
        } finally {
            updBusy = false;
            localPhase = '';
            await loadUpdate();
        }
    }

    const life = pageLife();
    // B-111: while an update is in flight - the API's, or this tab's download or upload whose
    // phase the API is about to report - the page follows it every 2 s (only while it shows)
    const polling = $derived(!!apiBusy || localPhase === 'download' || localPhase === 'upload');
    $effect(() => {
        if (!polling) return;
        const id = setInterval(() => {
            if (life.active) void loadUpdate();
        }, 2000);
        return () => clearInterval(id);
    });
    onMount(() => {
        void loadUpdate();
        void loadHTTPS();
        void loadBuild();
        const stopReturn = life.onReturn(() => void loadUpdate());
        return () => {
            stopReturn();
            stopInstallWatch?.();
            clearTimeout(lateTimer);
        };
    });
    async function loadHTTPS() {
        try {
            https = await api.get<HTTPSView>('/api/system/v1/https');
        } catch {
            https = null;
        }
    }
</script>

{#if auth.role === 'admin'}
    <!-- task 51: what an uploaded release file is, behind the section's ?; a container has no
         upload, so nothing to explain there -->
    <h2>{t('System update')}{#if !container}<Help>{t('A release file for this platform: a newer openccu-lite, or OpenCCU\'s own release zip to go back. The file is checked and staged exactly as the CCU WebUI did it; the recovery system installs it at the next boot and keeps /usr/local (pairings, keys, addons, the names).')}</Help>{/if}</h2>
    {#if updNotice}<div class="ol-notice">{updNotice}</div>{/if}
    <!-- B-111: the last download's failure, for a tab that did not start it; the controls are back -->
    {#if !updNotice && !busy && feed?.download_error}<div class="ol-notice error" data-download-error>{t('The last download failed: {e}', {e: feed.download_error})}</div>{/if}
    <!-- B-111: the running download or upload - in every tab - with the download's Cancel; the
         button is there from the first moment (same box) and usable once the API reports the download -->
    {#if busy && busy.phase !== 'install'}
        <div class="ol-notice ol-updbusy" role="status" data-update-busy={busy.phase}>
            <span class="ol-updbusy-text">{busyLine(busy)}</span>
            {#if busy.phase === 'download'}<button type="button" class="hmm-button" disabled={cancelling || apiBusy?.phase !== 'download'} onclick={cancelDownload} data-update-cancel>{t('Cancel')}</button>{/if}
        </div>
    {/if}
    {#if installing}
        <!-- the install's progress is the recovery system's own page, reached by the box's address -->
        <div class="ol-notice ol-installnotice" class:error={installing.late} role="status">
            <strong>{t('Rebooting into the recovery system…')}</strong>
            <BootBar entry={installing.boot} url={installing.address.url} hint={false} />
            <div>{t('The installation runs in the recovery system; its progress is shown at')} <a class="hmm-mono" href={installing.address.url}>{installing.address.url}</a></div>
            {#if installing.address.kind === 'name'}<div class="ol-muted" data-install-by-name>{t('If the browser refuses the name, open the system by its IP address.')}<Help>{t('No address of the system could be read, so the link uses its name. The recovery system has no HTTPS, and a browser that remembers HSTS for this name refuses it there.')}</Help></div>{/if}
            <div class="ol-muted">{t('This page comes back by itself when the installation is done.')}</div>
            {#if installing.late}<div class="ol-warn ol-installlate">{t('The system has not come back after {min} minutes. If the installation failed, it stays in the recovery system, which shows what happened at', {min: INSTALL_WAIT_MIN})} <a class="hmm-mono" href={installing.address.url}>{installing.address.url}</a></div>{/if}
        </div>
    {:else if busy?.phase === 'install'}
        <!-- B-111: the install another tab started, before this one has taken its countdown over -->
        <div class="ol-notice ol-installnotice" role="status" data-update-install-notice>
            <strong>{t('Rebooting into the recovery system…')}</strong>
            <div>{busy.file ? t('The installation of {file} runs in the recovery system.', {file: busy.file}) : t('The installation runs in the recovery system.')} {t('This page comes back by itself when the installation is done.')}</div>
        </div>
    {/if}
    <!-- task 34: a container has no recovery system to stage a file for; the rootfs is the
         template, swapped on the host, and /usr/local is the mount point that survives it.
         The release check stays: it names the newer template. -->
    {#if container}
        <div class="ol-notice" data-container-update>{t('This system is a container: update it by replacing the container on the host.')}<Help>{t('A new release is a new template. On Proxmox: create a new container from the template and move the /usr/local volume over; /usr/local keeps the pairings, keys, addons and names. Nothing is downloaded or installed from in here.')}</Help></div>
    {/if}
    {#if staged && !installPhase}
        <div class="ol-notice" data-staged>
            <strong>{staged.file}</strong> ({Math.round(staged.size / 1048576)} MB, {staged.kind}{staged.version ? `, ${staged.version} ${staged.board ?? ''}` : ''})
            {#if staged.recovery_armed}{' — '}{t('installs at the next boot')}{:else}{' — '}{t('staged, not yet scheduled')}{/if}
            {#if staged.warning}<div class="ol-muted">{staged.warning}</div>{/if}
            <!-- task 96 (D-64): the way back while a browser may remember HSTS for the box's name -->
            {#if staged.way_back && https?.hsts}
                <div class="ol-warn ol-wayback">{t('Not an openccu-lite release, and HSTS is on: a browser may refuse the system\'s name after the install. Switch HSTS off first, or open the system by its IP address afterwards.')} <a href="/system/certificates#https" use:link>{t('Certificate')}</a><Help>{t('The file may be the way back to OpenCCU, which serves a self-signed certificate: a browser that remembers HSTS for the system\'s name refuses it. After switching HSTS off, open the system once by its name in every browser you use, so each forgets it.')}</Help></div>
            {:else if staged.way_back && https?.hsts_clearing}
                <div class="ol-warn ol-wayback"><HSTSClearing view={https} lead={t('Not an openccu-lite release: HSTS is switched off for it.')} /><Help>{t('The file may be the way back to OpenCCU, which sends no HSTS and serves a self-signed certificate: a browser that still remembers HSTS for the system\'s name would refuse it.')}</Help></div>
            {/if}
            <div class="ol-actions" style="margin-top:6px">
                <button class="hmm-button" disabled={blocked} onclick={installUpdate} data-update-install>{t('Reboot and install')}</button>
                <button class="hmm-button" disabled={blocked} onclick={discardUpdate} data-update-discard>{t('Discard')}</button>
            </div>
        </div>
    {/if}
    {#if running && (running.lite || running.version)}
        <!-- task 256: what runs now, right above the release check that names what is available -->
        <p class="ol-installed" data-installed>{t('Installed')}: <span class="hmm-mono" data-installed-version>{running.lite ? `openccu-lite ${running.lite}` : `OpenCCU ${running.version}`}</span>{#if !running.lite && build}<span class="ol-muted" data-installed-build>{` · occulited ${build}`}</span>{/if}</p>
    {/if}
    {#if feed && !installPhase}
        <div class="ol-toolbar">
            {#if feed.available}
                <span data-release-available>{feed.available.newer ? t('Release {v} is available.', {v: feed.available.version}) : upToDateText(feed)}{#if feed.available.notes_url}{' '}<a href={feed.available.notes_url} target="_blank" rel="noopener">{t('Release notes')}</a>{/if}</span>
                {#if feed.available.newer && !container}<button class="hmm-button" disabled={blocked || !!feed.downloading} onclick={downloadRelease} data-update-download>{t('Download and stage')}</button>{/if}
            {:else}
                <span class="ol-muted" data-release-none>{feed.error ? t('Release check failed: {e}', {e: feedErrorText(feed)}) : feed.none_in_channel ? t('No release in the {channel} channel yet.', {channel: channelName(normalChannel(feed.channel))}) : channelChanged ? t('Channel changed: check now for its newest release.') : feed.enabled ? t('No release check yet.') : t('The daily release check is off.')}</span>
            {/if}
            <CheckDaily label={t('Check now')} busy={localPhase === 'check'} disabled={blocked} onCheck={checkFeed} daily={feed.enabled} onDaily={setDaily} host={feedHost(feed.feed_url)} name="system-update" />
            <UpdateChannel channel={normalChannel(feed.channel)} disabled={blocked} onchanged={channelStored} />
            {#if feed.checked}<span class="ol-muted">{t('checked')} {new Date(feed.checked).toLocaleString()}</span>{/if}
        </div>
    {/if}
    {#if !container && !installPhase}
        <form class="ol-toolbar" onsubmit={uploadUpdate}>
            <input type="file" accept=".zip,.tgz,.tar.gz,.img,application/zip,application/gzip" disabled={blocked} onchange={(e) => (updFile = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} data-update-file />
            <button class="hmm-button" type="submit" disabled={!updFile || blocked} data-update-upload>{t('Upload')}</button>
        </form>
    {/if}
{/if}

<style>
    /* B-111: the running download or upload and its Cancel on one line */
    .ol-updbusy { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 12px; }
    .ol-updbusy-text { flex: 1 1 auto; min-width: 0; overflow-wrap: anywhere; }
    .ol-installed { margin: 0 0 8px; overflow-wrap: anywhere; }
    .ol-installed .ol-muted { font-size: var(--hmm-font-size-small); }
</style>
