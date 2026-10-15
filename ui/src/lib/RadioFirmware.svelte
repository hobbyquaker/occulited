<script lang="ts">
    /*
     * Task 41: the coprocessor firmware — what the image ships under /firmware and what was
     * uploaded, per radio module, with the flash and the run's log. The maintainer, 2026-09-20: on
     * the Updates page (it was the Interfaces page's last section), one panel, and each module
     * carries its own upload control.
     *
     * The list is read with the page; while a flash runs the status is polled every 2 s and the
     * phase log is rendered, as the Certificate page does with its attempts.
     */
    import {onMount} from 'svelte';
    import {api, type RadioFirmware, type FirmwareModule, type FirmwareFile} from './api';
    import {auth} from './auth.svelte';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import {pageLife} from './pagelife.svelte';
    import Help from './Help.svelte';
    import Loading from './Loading.svelte';
    import RunLog from './RunLog.svelte';

    /** onflashed: a finished flash changed what the modules run (the Interfaces page's cards) */
    let {onflashed}: {onflashed?: () => void} = $props();
    const admin = $derived(auth.role === 'admin');
    const life = pageLife();

    // task 41: the coprocessor firmware. The list is read with the page; while a flash runs the
    // status is polled every 2 s and the phase log rendered, as the Certificate page does with
    // its attempts. The flash itself is one POST; everything else is the box's.
    let fw = $state<RadioFirmware | null>(null);
    let fwErr = $state('');
    // task 177: an administrator sees a placeholder where the firmware section comes
    let fwTried = $state(false);
    let fwBusy = $state(false);
    let fwNotice = $state('');
    // the maintainer, 2026-09-20: one upload control per module panel, so a file is never
    // uploaded for the wrong module; the key is the module's device node, which is unique
    let fwFiles = $state<Record<string, File | null>>({});
    let fwPoll: ReturnType<typeof setInterval> | null = null;
    async function loadFirmware() {
        try {
            fw = await api.get<RadioFirmware>('/api/system/v1/radio/firmware');
            fwErr = '';
            if (fw.running && !fwPoll) fwPoll = setInterval(() => life.active && loadFirmware(), 2000);
            if (!fw.running && fwPoll) {
                clearInterval(fwPoll);
                fwPoll = null;
                // the flash is over: the module card above shows what /var/hm_mode says now
                onflashed?.();
            }
        } catch (e) {
            // a user role gets 403 here and no section; anything else is shown
            fwErr = (e as {status?: number}).status === 403 ? '' : (e as Error).message;
        }
        fwTried = true;
    }
    const verdictText = (m: FirmwareModule) =>
        m.verdict === 'up-to-date' ? t('up to date')
        : m.verdict === 'newer-available' ? t('a newer firmware is on the system')
        : m.verdict === 'older-only' ? t('every file on the system is older than what runs')
        : m.verdict === 'no-file' ? t('no firmware file for this module')
        : m.verdict === 'skipped' ? t('not flashable from here')
        : m.verdict === 'unusable' ? t('no usable firmware: the system has no radio on it until it is flashed')
        : t('the running version is unknown');
    const directionText = (f: FirmwareFile) => (f.direction === 'upgrade' ? t('upgrade') : f.direction === 'downgrade' ? t('downgrade') : f.direction === 'same' ? t('same version') : '–');
    const kb = (n: number) => `${Math.round(n / 1024)} KB`;
    // task 50: a note the daemon names by its code is worded here, in the user's language
    const noteText = (m: FirmwareModule) => (m.note_code === 'no-eq3-firmware' ? t('No firmware from eQ-3 for this stick') : (m.note ?? ''));
    const flashBlocked = $derived(!fw || fw.force_no_update || fw.firmware_staged || fw.radio_busy || !!fw.running || fwBusy);
    async function flash(m: FirmwareModule, f: FirmwareFile) {
        const p = {device: m.device, from: m.running_version || '?', to: f.version ?? f.name};
        // the direction first, and never "update" for a downgrade (41.0.6)
        const head = f.direction === 'downgrade' ? t('Downgrade the coprocessor of {device} from {from} to {to}?', p)
            : f.direction === 'same' ? t('Flash {to} onto the coprocessor of {device} again? It already runs this version.', p)
            : t('Update the coprocessor of {device} from {from} to {to}?', p);
        const ok = await ask({
            title: t('Flash coprocessor firmware'),
            message: `${head} ${t('Devices are unreachable while it runs. Do not cut the power: a failed flash can leave the module unusable.')}`,
            confirm: f.direction === 'downgrade' ? t('Downgrade') : t('Flash'),
            danger: true,
        });
        if (!ok) return;
        fwBusy = true;
        fwNotice = '';
        try {
            fw = await api.post<RadioFirmware>('/api/system/v1/radio/firmware/flash', {module: m.device, file: f.name});
            if (fw.running && !fwPoll) fwPoll = setInterval(() => life.active && loadFirmware(), 2000);
        } catch (e) {
            fwNotice = (e as Error).message;
        } finally {
            fwBusy = false;
        }
    }
    async function deleteFile(m: FirmwareModule, f: FirmwareFile) {
        if (!(await ask({message: t('Delete {name}?', {name: f.name}), confirm: t('Delete'), danger: true}))) return;
        fwBusy = true;
        try {
            await api.del(`/api/system/v1/radio/firmware/${encodeURIComponent(m.device)}/${encodeURIComponent(f.name)}`);
            await loadFirmware();
        } catch (e) {
            fwNotice = (e as Error).message;
        } finally {
            fwBusy = false;
        }
    }
    async function uploadFirmware(e: Event, m: FirmwareModule) {
        e.preventDefault();
        const file = fwFiles[m.device_node];
        if (!file) return;
        fwBusy = true;
        fwNotice = '';
        try {
            const fd = new FormData();
            fd.append('file', file);
            const r = await api.postForm<FirmwareFile>(`/api/system/v1/radio/firmware/upload?module=${encodeURIComponent(m.device)}`, fd);
            fwNotice = t('Uploaded: {name} ({sha}).', {name: r.name, sha: r.sha256.slice(0, 12) + '…'});
            fwFiles = {...fwFiles, [m.device_node]: null};
            await loadFirmware();
        } catch (err) {
            fwNotice = (err as Error).message;
        } finally {
            fwBusy = false;
        }
    }

    onMount(() => {
        void loadFirmware();
        const stop = life.onReturn(() => void loadFirmware());
        return () => {
            stop();
            if (fwPoll) clearInterval(fwPoll);
        };
    });
</script>

{#if admin && !fwTried}
    <Loading />
{:else if fw && admin}
    <section class="ol-panel" data-panel="radio-firmware">
        <!-- task 51: what the section lists and what a flash does, behind the heading's ? -->
        <!-- task 50 (the maintainer, 2026-10-14): one visible line; how it works stays in the Help -->
        <h2>{t('Radio firmware')}<Help>{t('What the image ships under /firmware and what was uploaded. Flashing stops hmipserver, rfd and multimacd, writes the coprocessor over the raw UART, re-runs the detection and starts them again.')}</Help></h2>
        <p class="ol-muted fw-intro" data-radio-fw-intro>{t('Firmware for the radio module and the USB stick.')}</p>
        {#if fwErr}<div class="ol-warn">{fwErr}</div>{/if}
        {#if fw.force_no_update}<div class="ol-notice">{t('/etc/config/force-no-coprocessor-update exists: coprocessor updates are switched off on this system. Remove the file to flash from here.')}</div>{/if}
        {#if fw.forced_version}<div class="ol-notice">{t('The boot-time flasher is pinned to version {v} (/etc/config/forced_coprocessor_version).', {v: fw.forced_version})}</div>{/if}
        {#if fw.firmware_staged}<div class="ol-notice">{t('A system update is staged: a flash waits until it is installed or discarded.')}</div>{/if}
        {#if fw.radio_busy}<div class="ol-notice">{t('The radio is busy ({i}): a flash waits until the duty cycle drops.', {i: fw.radio_busy_interface ?? ''})}</div>{/if}
        {#if fwNotice}<div class="ol-notice">{fwNotice}</div>{/if}
        {#if fw.modules.length === 0}
            <div class="ol-muted">{t('No radio module detected.')}</div>
        {/if}
        {#each fw.modules as m (m.device + m.device_node)}
            <div class="ol-card fw-module">
                <div class="k">{m.device} <span class="hmm-mono">{m.device_node}</span> · {m.protocols.join(', ')}</div>
                <dl class="ol-kv" style="margin-top:8px">
                    <dt>{t('Running version')}</dt><dd class="fw-running">{m.running_version || '–'}</dd>
                    <dt>{t('Newest file')}</dt><dd class="hmm-mono">{m.newest ?? '–'}</dd>
                    <dt>{t('Status')}</dt><dd class="fw-verdict" data-verdict={m.verdict}>{verdictText(m)}</dd>
                </dl>
                {#if m.note}<p class="ol-muted meta" data-note>{noteText(m)}</p>{/if}
                {#if m.family === 'hmcfgusb'}
                    <!-- task 147 (D-100): the image does not ship eQ-3's file for the adapter -->
                    <p class="ol-muted meta fw-hmcfgusb-source">{t('The image does not ship this firmware. Upload hmusbif.03c7.enc (0.967) from hmcfgusb\'s firmware directory:')} <a href="https://git.zerfleddert.de/hmcfgusb/firmware/" target="_blank" rel="noopener noreferrer">git.zerfleddert.de/hmcfgusb/firmware</a></p>
                {/if}
                {#if m.flashable}
                    <!-- the maintainer, 2026-09-20: this module's own upload -->
                    <form class="ol-toolbar fw-upload" onsubmit={(e) => uploadFirmware(e, m)}>
                        <input type="file" accept=".eq3,.enc" aria-label={t('Upload firmware for {device}', {device: m.device})} data-upload={m.device_node} onchange={(e) => (fwFiles = {...fwFiles, [m.device_node]: (e.currentTarget as HTMLInputElement).files?.[0] ?? null})} />
                        <button class="hmm-button" type="submit" disabled={!fwFiles[m.device_node] || fwBusy}>{t('Upload')}</button>
                        <Help>{t('Keep the name eQ-3 gives the file: the version is read from it. The file lands in {dir} on the userfs and survives an update.', {dir: fw.upload_dir})}</Help>
                    </form>
                    <p class="ol-muted meta fw-upload-hint" data-upload-hint>{t('An .eq3 or .enc file from eQ-3, at most 4 MiB.')}</p>
                {/if}
                {#if m.files.length}
                    <div class="ol-scroll">
                        <table class="ol-table fw-files">
                            <thead><tr><th>{t('Name')}</th><th>{t('Source')}</th><th>{t('Version')}</th><th>{t('Size')}</th><th>SHA-256</th><th>{t('Direction')}</th><th></th></tr></thead>
                            <tbody>
                                {#each m.files as f (f.path)}
                                    <tr>
                                        <td class="hmm-mono">{f.name}</td>
                                        <td>{f.source === 'shipped' ? t('shipped') : t('uploaded')}</td>
                                        <td>{f.version ?? '–'}</td>
                                        <td>{kb(f.size)}</td>
                                        <td class="hmm-mono" title={f.sha256}>{f.sha256.slice(0, 12)}…</td>
                                        <td>{directionText(f)}</td>
                                        <td class="ol-actions">
                                            <button class="hmm-button" class:primary={f.direction === 'upgrade'} disabled={flashBlocked || !m.flashable || !f.version} onclick={() => flash(m, f)}>{f.direction === 'downgrade' ? t('Downgrade') : t('Flash')}</button>
                                            {#if f.source === 'uploaded'}
                                                <button class="hmm-button" disabled={fwBusy || !!fw.running} onclick={() => deleteFile(m, f)}>{t('Delete')}</button>
                                            {/if}
                                        </td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {/if}
            </div>
        {/each}
        {#if fw.running}
            <h3>{t('Flash running')} <span class="ol-muted">· {fw.running.module} · {fw.running.file.split('/').pop()}</span></h3>
            <!-- task 102: the run's lines are in the journal -->
            <RunLog runId={fw.running.run_id} running cls="fw-log" />
        {:else if fw.last}
            <h3>{t('Last flash')} <span class="ol-muted">· {fw.last.module} · {fw.last.file.split('/').pop()} · {fw.last.ok ? t('succeeded') : t('failed')}</span></h3>
            {#if !fw.last.ok}<div class="ol-notice error">{fw.last.error} {fw.last.after && fw.last.after === fw.last.before ? t('The version was not changed.') : ''}</div>{/if}
            <RunLog runId={fw.last.run_id} cls="fw-log" />
        {/if}
    </section>
{/if}

<style>
    /* the file table is wider than a phone: it scrolls inside its own box, the page never scrolls
       sideways (it was the Interfaces page's rule and came along with the section) */
    .ol-scroll { overflow-x: auto; max-width: 100%; }
    .fw-verdict[data-verdict="newer-available"] { color: var(--hmm-accent); }
    .fw-verdict[data-verdict="older-only"], .fw-verdict[data-verdict="no-file"], .fw-verdict[data-verdict="unusable"] { color: var(--hmm-warn); }
    .fw-module { margin-bottom: 10px; }
    .fw-module .ol-table { margin-top: 8px; }
    .fw-upload { margin: 8px 0 4px; }
    .fw-files td { white-space: nowrap; }
    .meta { font-size: var(--hmm-font-size-small); margin: 6px 0 0; }
    .fw-intro { margin: -4px 0 10px; }
    .fw-upload-hint { margin-top: 0; }
</style>
