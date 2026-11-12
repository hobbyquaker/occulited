<script lang="ts">
    import {dateTime} from './datefmt';
    import {link} from './router.svelte';
    /*
     * Task 93: the Log page's settings behind the ⚙ at the end of its view switch - the daemons' log
     * levels (27.8, the access log of task 84) and where the journal lives (tasks 23 and 85) - in the
     * shell's modal. They were two disclosures above the viewer, which pushed the lines down each
     * time one opened, and the reader looks at the top of the page.
     *
     * Three tabs, one panel at a time, each with its own Save. The third (task 214) is where
     * occulited's own database file is kept - the radio interfaces' history for now. A change not saved yet survives a switch
     * of tab and is asked about (ModalDialog's `dirty`) before the sheet closes; a closed sheet
     * forgets it, and the next opening reads the box again. The restart buttons a saved level asks
     * for stay until they are used.
     */
    import {untrack} from 'svelte';
    import Tabs from './Tabs.svelte';
    import {api, type DataStoreConfig, type HistoryListConfig, type JournalConfig, type JournalStorage, type LogLevels} from './api';
    import {levelsKey, MULTIMACD_LEVELS, multimacdLevel, OCCULITED_AREAS, OCCULITED_LEVELS, occulitedDebugOn, toggleArea} from './loglevels';
    import {t} from './i18n.svelte';
    import Loading from './Loading.svelte';
    import Help from './Help.svelte';
    import ModalDialog from './ModalDialog.svelte';
    import {formatBytes} from './netpanels';
    import {effectiveStorage, journalBody, STICK_DIR, switchEffect, SYNC_INTERVALS} from './journal';
    import LocationPicker from './LocationPicker.svelte';
    import {folderError, formatLocation, parseLocation} from './locations';
    // the journal's folder on the userfs, which the boot script fixes
    const USERFS_JOURNAL = 'var/log/journal';
    import type {LogSettingsTab} from './logpage';
    import {warnEdge, warnFor} from './systemmenu.svelte';

    let {open = $bindable(false), tab = $bindable<LogSettingsTab>('levels')}: {open?: boolean; tab?: LogSettingsTab} = $props();

    const TABS: {id: LogSettingsTab; label: string}[] = [
        {id: 'levels', label: 'Log levels'},
        {id: 'journal', label: 'Journal'},
        {id: 'history', label: 'History'},
    ];

    // task 23, task 85: where the journal lives and how big it may get. One file, one script at
    // boot, re-run on save. RAM only, RAM copied to the userfs (ram-sync), or persistent on the
    // userfs: a switch onto the userfs or into ram-sync applies at once, one back into RAM at the next
    // reboot - the panel says which, and notes a pending one.
    let journal = $state<JournalConfig | null>(null);
    // the storage as the box has it saved, so the panel can say what saving a new choice does
    let journalSaved = $state<JournalStorage>('');
    // the form as loaded or last saved, for the question before the sheet closes
    let journalSnap = '';
    let journalBusy = $state(false);
    let journalMsg = $state('');
    let journalErr = $state('');
    const journalSwitch = $derived(journal ? switchEffect(journalSaved, journal.storage, journal.default_storage, journal.effective) : 'none');
    // the mode chosen in the form, and the one saved: the copy fields follow the first, Copy now the second
    const journalChosen = $derived(journal ? effectiveStorage(journal.storage, journal.default_storage) : 'ram');
    const journalSavedMode = $derived(journal ? effectiveStorage(journalSaved, journal.default_storage) : 'ram');
    // task 216, task 228: the target - the userfs, a USB stick by its label or a network share by its
    // name, and a folder there (a stick and a share take the copies only: ram-sync). Picked with the
    // location picker; journal.target is put together from the location and the folder.
    let targetLocation = $state('userfs');
    let targetFolder = $state(USERFS_JOURNAL);
    let journalSavedTarget = $state('userfs');
    function formTarget(j: JournalConfig) {
        const tgt = j.target || 'userfs';
        const p = tgt === 'userfs' ? null : parseLocation(tgt);
        targetLocation = p ? p.id : 'userfs';
        targetFolder = p ? p.folder : USERFS_JOURNAL;
        journalSavedTarget = tgt;
    }
    function syncTarget() {
        if (!journal) return;
        journal.target = targetLocation === 'userfs' ? 'userfs' : formatLocation(targetLocation, targetFolder || STICK_DIR);
    }
    // a stick or a share: copies only
    const onStick = $derived(targetLocation !== 'userfs');
    const onShare = $derived(targetLocation.startsWith('share:'));
    function journalBytes(n: number | null | undefined): string {
        return n === null || n === undefined ? '—' : formatBytes(n);
    }
    function journalWhen(iso: string | null | undefined): string {
        if (!iso) return '—';
        return dateTime(iso, {weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'});
    }
    async function loadJournal() {
        if (journal) return;
        try {
            journal = await api.get<JournalConfig>('/api/system/v1/journal');
            journalSaved = journal.storage;
            formTarget(journal);
            journalSnap = JSON.stringify(journalBody(journal));
        } catch (e) {
            journalErr = (e as Error).message;
        }
    }
    async function saveJournal() {
        if (!journal) return;
        journalBusy = true;
        journalErr = '';
        journalMsg = '';
        try {
            journal = await api.put<JournalConfig>('/api/system/v1/journal', journalBody(journal));
            journalSaved = journal.storage;
            formTarget(journal);
            journalSnap = JSON.stringify(journalBody(journal));
            journalMsg = !journal.reboot_pending
                ? t('Saved and applied.')
                : journal.effective === 'ram-sync'
                  ? t('Saved. The copies have stopped; from the next boot the journal is in RAM only.')
                  : t('Saved. The journal stays on the userfs until the next boot; from then on it is in RAM.');
        } catch (e) {
            journalErr = (e as Error).message;
        } finally {
            journalBusy = false;
        }
    }
    // Copy now: occulited restarts the copy unit, whose stop is the copy. The answer brings the new
    // figures; what is typed into the form and not saved yet stays.
    async function copyNow() {
        if (!journal) return;
        journalBusy = true;
        journalErr = '';
        journalMsg = '';
        try {
            const form = journalBody(journal);
            const r = await api.post<JournalConfig>('/api/system/v1/journal/sync', {});
            journal = {...r, ...form};
            if (r.last_sync_result !== 'failed') journalMsg = t('Copied: {n} files.', {n: r.last_sync_copied});
        } catch (e) {
            journalErr = (e as Error).message;
        } finally {
            journalBusy = false;
        }
    }

    // task 214: occulited's database file - the duty cycle and carrier sense history of the
    // Interfaces page - in the journal's three modes. A save applies at once.
    let store = $state<DataStoreConfig | null>(null);
    let storeSnap = '';
    let storeBusy = $state(false);
    let storeMsg = $state('');
    let storeErr = $state('');
    const storeChosen = $derived(store ? (store.mode || store.default_mode) : 'ram');
    const storeBody = (d: DataStoreConfig) => ({mode: d.mode, sync_interval: d.sync_interval, location: d.location});
    // task 228, phase 4: where the database file is, picked with the location picker - the userfs
    // (a folder inside etc/occulite); task 229: a USB stick takes a snapshot of ram-sync's file (the
    // file itself stays on the userfs); a share never
    let storeLoc = $state('userfs');
    let storeFolder = $state('etc/occulite/data');
    function storeLoaded(d: DataStoreConfig) {
        store = d;
        storeSnap = JSON.stringify(storeBody(d));
        const p = parseLocation(d.location || d.default_location);
        storeLoc = p?.id ?? 'userfs';
        storeFolder = p?.folder ?? 'etc/occulite/data';
    }
    const storeOnStick = $derived(storeLoc.startsWith('usb:'));
    function syncStoreLoc() {
        if (!store) return;
        // a stick takes ram-sync's snapshot only
        if (storeLoc.startsWith('usb:') && (store.mode || store.default_mode) !== 'ram-sync') store.mode = 'ram-sync';
        const l = formatLocation(storeLoc, storeFolder);
        // the default is stored as the empty setting, as before
        store.location = l === store.default_location ? '' : l;
    }
    const storeLocProblem = $derived(store ? folderError(storeFolder, false, storeLoc === 'userfs' ? 'etc/occulite' : undefined) : '');
    async function loadStore() {
        if (store) return;
        try {
            storeLoaded(await api.get<DataStoreConfig>('/api/system/v1/datastore'));
        } catch (e) {
            storeErr = (e as Error).message;
        }
    }
    async function saveStore() {
        if (!store) return;
        storeBusy = true;
        storeErr = '';
        storeMsg = '';
        try {
            storeLoaded(await api.put<DataStoreConfig>('/api/system/v1/datastore', storeBody(store)));
            storeMsg = t('Saved and applied.');
        } catch (e) {
            storeErr = (e as Error).message;
        } finally {
            storeBusy = false;
        }
    }
    // task 195: which datapoints the history records - the built-in list (what the App's cards
    // draw), names added and names removed; applied at once, what was recorded stays readable
    let hist = $state<HistoryListConfig | null>(null);
    let histAdd = $state('');
    let histRemove = $state('');
    let histSnap = '';
    let histBusy = $state(false);
    let histMsg = $state('');
    let histErr = $state('');
    const names = (s: string) => s.split(/[\s,]+/).map((x) => x.trim().toUpperCase()).filter(Boolean);
    function histLoaded(h: HistoryListConfig) {
        hist = h;
        histAdd = h.add.join(', ');
        histRemove = h.remove.join(', ');
        histSnap = histAdd + '|' + histRemove;
    }
    async function loadHist() {
        if (hist) return;
        try {
            histLoaded(await api.get<HistoryListConfig>('/api/system/v1/datastore/history'));
        } catch (e) {
            histErr = (e as Error).message;
        }
    }
    async function saveHist() {
        histBusy = true;
        histErr = '';
        histMsg = '';
        try {
            histLoaded(await api.put<HistoryListConfig>('/api/system/v1/datastore/history', {add: names(histAdd), remove: names(histRemove)}));
            histMsg = t('Saved and applied.');
        } catch (e) {
            histErr = (e as Error).message;
        } finally {
            histBusy = false;
        }
    }
    const modeLabel = (m: string) => (m === 'persistent' ? t('Persistent') : m === 'ram-sync' ? t('RAM, written to the userfs') : t('RAM'));

    // task 27.8: the levels live with the log because that is where their effect is read. Five
    // daemons, three mechanisms - and occulited's own level (task 101), which applies at once.
    // B-162: the numbers are eQ-3's own and 1 is the most verbose - measured level by level on
    // 2026-09-22 (20 lines at 1, 4 at 7, errors at every level). What looked like silence was
    // B-161 (the running daemon kept the boot's level) and an idle rfd, which writes nothing at
    // any level.
    const RFD_LEVELS = [
        {v: 1, k: 'Debug'},
        {v: 2, k: 'Info'},
        {v: 4, k: 'Warning'},
        {v: 5, k: 'Error'},
    ];
    // What debug costs, where it is chosen.
    const RFD_DEBUG_NOTE = () => t('Debug writes every step the daemon takes; on a busy system that fills the journal. A daemon with nothing to do writes nothing, whatever the level.');
    // A file may carry a number the page does not offer - eQ-3's scale runs from 1 to 7 and
    // OpenCCU's WebUI offers four of them. It stands in the list so the reader sees what is
    // stored, it cannot be chosen again, and the row asks for one of the four.
    const STALE_NOTE = () => t('This system carries a number the page does not offer. The daemon takes 1 to 7, 1 being the most and 7 the least; choose one of the levels and save.');
    const offScale = (levels: {v: number; k: string}[], cur: number | null) => cur !== null && !levels.some((l) => l.v === cur);
    const withStored = (list: {v: number; k: string}[], cur: number | null) => (offScale(list, cur) ? [...list, {v: cur as number, k: ''}] : list);
    const HMIP_LEVELS = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR'];
    const OCC_LEVEL_LABELS: Record<string, string> = {debug: 'Debug', info: 'Info', warn: 'Warning', error: 'Error'};
    // the units a restart of the radio stack takes along (POST /radio/restart)
    const RADIO_STACK = ['multimacd', 'rfd', 'hmipserver'];
    let levels = $state<LogLevels | null>(null);
    let levelsSnap = '';
    // task 101: multimacd's level as saved - a form that changes it restarts the radio stack, and
    // the row says so before saving. Task 297: its own level only, rfd's never touches it.
    let savedMultimacd = $state(2);
    const radioRestartAhead = $derived(!!levels && levels.multimacd !== savedMultimacd);
    let levelsBusy = $state(false);
    let levelsMsg = $state('');
    let levelsErr = $state('');
    let restartUnits = $state<string[]>([]);
    function loadedLevels(l: LogLevels) {
        l.multimacd = multimacdLevel(l.multimacd);
        l.occulited = l.occulited ?? {level: 'info', debug_areas: []};
        levels = l;
        levelsSnap = levelsKey(l);
        savedMultimacd = l.multimacd;
    }
    function setArea(id: string, on: boolean) {
        if (levels) levels.occulited.debug_areas = toggleArea(levels.occulited.debug_areas, id, on);
    }
    async function loadLevels() {
        if (levels) return;
        try {
            loadedLevels(await api.get<LogLevels>('/api/system/v1/loglevels'));
        } catch (e) {
            levelsErr = (e as Error).message;
        }
    }
    async function saveLevels() {
        if (!levels) return;
        levelsBusy = true;
        levelsErr = '';
        levelsMsg = '';
        try {
            const r = await api.put<LogLevels>('/api/system/v1/loglevels', levels);
            loadedLevels(r);
            restartUnits = r.restart;
            const parts: string[] = [];
            if (r.applied.length) parts.push(t('{units} took the level live.', {units: r.applied.join(', ')}));
            if (r.restart.length) parts.push(t('{units}: the change shows after a restart.', {units: r.restart.join(', ')}));
            if (r.errors) for (const [u, m] of Object.entries(r.errors)) parts.push(`${u}: ${m}`);
            levelsMsg = parts.join(' ') || t('Saved.');
        } catch (e) {
            levelsErr = (e as Error).message;
        } finally {
            levelsBusy = false;
        }
    }
    async function restartUnit(id: string) {
        levelsBusy = true;
        try {
            if (id === 'multimacd') {
                // multimacd cannot start again under rfd and hmipserver, which hold its nodes: the
                // stack stops and starts in order, and the other two are restarted with it
                await api.post('/api/system/v1/radio/restart', {});
                restartUnits = restartUnits.filter((u) => !RADIO_STACK.includes(u));
                levelsMsg = t('The radio stack restarted: hmipserver, rfd and multimacd stopped and started again.');
                return;
            }
            await api.post(`/api/system/v1/services/${encodeURIComponent(id)}/restart`, {});
            restartUnits = restartUnits.filter((u) => u !== id);
            levelsMsg = t('{id} restarted.', {id});
        } catch (e) {
            levelsErr = (e as Error).message;
        } finally {
            levelsBusy = false;
        }
    }

    // the open tab's panel is read from the box when it is first shown
    $effect(() => {
        if (!open) return;
        const which = tab;
        untrack(() => void (which === 'levels' ? loadLevels() : which === 'journal' ? loadJournal() : (loadStore(), loadHist())));
    });

    function dirty(): boolean {
        return (!!levels && levelsKey(levels) !== levelsSnap) || (!!journal && JSON.stringify(journalBody(journal)) !== journalSnap) || (!!store && JSON.stringify(storeBody(store)) !== storeSnap) || (!!hist && histAdd + '|' + histRemove !== histSnap);
    }
    function close() {
        open = false;
        levels = null;
        levelsSnap = '';
        levelsMsg = '';
        levelsErr = '';
        journal = null;
        journalSnap = '';
        journalMsg = '';
        journalErr = '';
        store = null;
        storeSnap = '';
        storeMsg = '';
        storeErr = '';
        hist = null;
        histSnap = '';
        histMsg = '';
        histErr = '';
    }
</script>

<ModalDialog {open} title={t('Log settings')} size="large" onclose={close} {dirty}>
    <Tabs tabs={TABS.map((tb) => ({id: tb.id, label: t(tb.label)}))} bind:value={tab} label={t('Log settings')} idPrefix="ls" class="ls-tabs" />
    {#if tab === 'levels'}
        <div class="ls-panel" role="tabpanel" id="ls-panel-levels" aria-labelledby="ls-tab-levels">
            {#if levelsErr}<div class="ol-warn">{levelsErr}</div>{/if}
            {#if levels}
                <!-- task 51: what each daemon does with its level is behind the ? after its name. Each
                     label names its control by `for`: without it the ? would be the label's first
                     labelable child, and the label would name that. -->
                <div class="lv-grid">
                    <!-- task 101: occulited's own level first, across the grid; it applies at once -->
                    <div class="lv-field lv-wide" data-level="occulited">
                        <label class="lv-name" for="ol-lv-occulited">occulited <span class="ol-muted">· {t('this service')}</span><Help>{t("occulited's own lines. Applied at once, without a restart; stored in occulited.json, so it stays after a restart and is part of the backup.")}</Help></label>
                        <select id="ol-lv-occulited" class="hmm-select lv-narrow" bind:value={levels.occulited.level} disabled={levelsBusy}>
                            {#each OCCULITED_LEVELS as l (l)}<option value={l}>{t(OCC_LEVEL_LABELS[l] ?? l)}</option>{/each}
                        </select>
                        {#if levels.occulited.level !== 'debug'}
                            <fieldset class="lv-areas">
                                <legend>{t('Debug for these areas only')}</legend>
                                <div class="lv-area-list">
                                    {#each OCCULITED_AREAS as a (a.id)}
                                        <label class="lv-check"><input type="checkbox" checked={levels.occulited.debug_areas.includes(a.id)} onchange={(ev) => setArea(a.id, ev.currentTarget.checked)} disabled={levelsBusy} /> <span>{t(a.label)}</span></label>
                                    {/each}
                                </div>
                            </fieldset>
                        {/if}
                        {#if occulitedDebugOn(levels)}
                            <p class="ol-muted lv-note" data-note="debug">{t('Debug writes many lines. In a RAM journal they push older lines out sooner; with copies to the userfs or a persistent journal they are written to the SD card. Switch it back when you are done.')}</p>
                        {/if}
                    </div>
                    <div class="lv-field" data-level="rfd">
                        <label class="lv-name" for="ol-lv-rfd">rfd <span class="ol-muted">· BidCos-RF</span><Help>{t('Applied live over the interface.')}</Help></label>
                        <select id="ol-lv-rfd" class="hmm-select" bind:value={levels.rfd} disabled={levelsBusy}>
                            {#each withStored(RFD_LEVELS, levels.rfd) as l (l.v)}<option value={l.v} disabled={!l.k}>{l.k ? `${t(l.k)} (${l.v})` : t('{n} · stored, not one of the levels', {n: l.v})}</option>{/each}
                        </select>
                        {#if levels.rfd === 1}<p class="ol-muted lv-note" data-note="rfd-debug">{RFD_DEBUG_NOTE()}</p>{/if}
                        {#if offScale(RFD_LEVELS, levels.rfd)}<p class="ol-warn lv-note" data-note="rfd-stale">{STALE_NOTE()}</p>{/if}
                    </div>
                    <!-- task 101: multimacd's own level; task 297: Debug or Info only, never rfd's. A change restarts the radio stack -->
                    <div class="lv-field" data-level="multimacd">
                        <label class="lv-name" for="ol-lv-multimacd">multimacd <span class="ol-muted">· {t('radio module')}</span><Help>{t('Between the radio module and rfd and hmipserver. Info or Debug only: at a quieter level it logs nothing at its start, and the system reads that start to tell whether the radio module answered. It takes a new level only when it starts, and it cannot start again under rfd and hmipserver: the radio stack stops and starts in order.')}</Help></label>
                        <select id="ol-lv-multimacd" class="hmm-select" bind:value={levels.multimacd} disabled={levelsBusy}>
                            {#each MULTIMACD_LEVELS as l (l.v)}<option value={l.v}>{`${t(l.k)} (${l.v})`}</option>{/each}
                        </select>
                        {#if radioRestartAhead}
                            <p class="ol-warn lv-note" data-note="radio-restart">{t('Saving asks for a restart of the radio stack: hmipserver, rfd and multimacd stop and start again, and no device can be reached until they are back, a minute or more.')}</p>
                        {/if}
                    </div>
                    <div class="lv-field" data-level="hs485d">
                        <label class="lv-name" for="ol-lv-hs485d">hs485d <span class="ol-muted">· BidCos-Wired</span><Help>{t('Applied live over the interface, like rfd.')}</Help></label>
                        <select id="ol-lv-hs485d" class="hmm-select" bind:value={levels.hs485d} disabled={levelsBusy}>
                            {#each withStored(RFD_LEVELS, levels.hs485d) as l (l.v)}<option value={l.v} disabled={!l.k}>{l.k ? `${t(l.k)} (${l.v})` : t('{n} · stored, not one of the levels', {n: l.v})}</option>{/each}
                        </select>
                        {#if levels.hs485d === 1}<p class="ol-muted lv-note" data-note="hs485d-debug">{RFD_DEBUG_NOTE()}</p>{/if}
                        {#if offScale(RFD_LEVELS, levels.hs485d)}<p class="ol-warn lv-note" data-note="hs485d-stale">{STALE_NOTE()}</p>{/if}
                    </div>
                    <label class="lv-field" for="ol-lv-hmip">
                        <span>hmipserver <span class="ol-muted">· log4j2</span><Help>{t('Written into log4j2.xml when hmipserver starts: restart to apply.')}</Help></span>
                        <select id="ol-lv-hmip" class="hmm-select" bind:value={levels.hmip} disabled={levelsBusy}>
                            {#each HMIP_LEVELS as l (l)}<option value={l}>{l}</option>{/each}
                        </select>
                    </label>
                    <div class="lv-field">
                        <span>lighttpd <span class="ol-muted">· debug.*</span><Help>{t('lighttpd has no level, only switches; they go into a drop-in on the userfs. Restart to apply — this page reloads with it.')}</Help></span>
                        <label class="lv-check"><input type="checkbox" bind:checked={levels.lighttpd.request_handling} disabled={levelsBusy} /> <span class="hmm-mono">debug.log-request-handling</span></label>
                        <label class="lv-check"><input type="checkbox" bind:checked={levels.lighttpd.condition_handling} disabled={levelsBusy} /> <span class="hmm-mono">debug.log-condition-handling</span></label>
                        <label class="lv-check"><input type="checkbox" bind:checked={levels.lighttpd.file_not_found} disabled={levelsBusy} /> <span class="hmm-mono">debug.log-file-not-found</span></label>
                        <!-- the access log is a drop-in of its own, off by default: every request an entry -->
                        <label class="lv-check" for="ol-lv-access"><input id="ol-lv-access" type="checkbox" bind:checked={levels.lighttpd.access_log} disabled={levelsBusy} /> <span>{t('Access log')}</span><Help>{t('Every request becomes a journal entry, listed as lighttpd access log. Meant for debugging: switch it off again afterwards. It applies at the lighttpd restart this page does.')}</Help></label>
                    </div>
                    <label class="lv-field" for="ol-lv-loghost">
                        <span>{t('Syslog server')} <span class="ol-muted">· LOGHOST</span><Help>{t('Every journal entry is sent there as RFC 5424 over UDP, port 514 unless given as host:port (occu-syslog-forward). The forwarder takes a change at its next start.')}</Help></span>
                        <input id="ol-lv-loghost" class="hmm-input" bind:value={levels.loghost} placeholder={t('host or host:port, empty = none')} disabled={levelsBusy} />
                    </label>
                </div>
                <div class="ol-actions">
                    <button class="hmm-button primary" disabled={levelsBusy} onclick={saveLevels}>{t('Save')}</button>
                    {#each restartUnits as u (u)}
                        <button class="hmm-button" disabled={levelsBusy} onclick={() => restartUnit(u)}>{u === 'multimacd' ? t('Restart the radio stack') : t('Restart {id}', {id: u})}</button>
                    {/each}
                </div>
                {#if levelsMsg}<p class="ol-muted">{levelsMsg}</p>{/if}
            {:else if !levelsErr}
                <Loading />
            {/if}
        </div>
    {:else if tab === 'history'}
        <!-- occulited task 12: the Status page's warnings about the database's and the journal's copies -->
        <div class="ls-panel ol-warn-edge {warnEdge(['store-target'])}" role="tabpanel" id="ls-panel-history" aria-labelledby="ls-tab-history" {...warnFor(['store-target'])}>
            {#if storeErr}<div class="ol-warn">{storeErr}</div>{/if}
            {#if store}
                <div class="ds-panel">
                    <p class="ol-muted ds-now">
                        {!store.open && store.effective !== 'ram'
                            ? t('The history is in RAM now')
                            : store.effective === 'persistent'
                              ? t('The history is written to the userfs as it is taken')
                              : store.effective === 'ram-sync'
                                ? t('The history is in RAM now and written to the userfs at the interval')
                                : t('The history is in RAM now')}.
                    </p>
                    <dl class="jr-figures">
                        <div><dt>{t('Kept per interface')}</dt><dd class="hmm-mono" data-figure="rows">{t('{n} samples', {n: store.rows_per_series})}</dd></div>
                        <!-- the location as task 228's picker will store it: a location id and a folder -->
                        <div><dt>{t('Location')}</dt><dd class="hmm-mono" data-figure="location">{(store.location || store.default_location).replace(/^userfs:/, 'userfs · ')}</dd></div>
                        <div><dt>{t('On the userfs')}</dt><dd class="hmm-mono" data-figure="size">{store.open ? journalBytes(store.size) : '—'}</dd></div>
                        {#if store.open}
                            <div><dt>{t('Last write')}</dt><dd data-figure="last-sync">{store.last_sync ? journalWhen(store.last_sync) : t('none yet')}</dd></div>
                        {/if}
                        {#if store.kept?.state !== undefined}
                            <div><dt>{t('Device values')}</dt><dd class="hmm-mono" data-figure="state">{t('{n} datapoints', {n: String(store.kept.state)})}</dd></div>
                        {/if}
                        {#if store.open && store.effective !== 'ram' && store.next_sync}
                            <div><dt>{t('Next write')}</dt><dd data-figure="next-sync">{journalWhen(store.next_sync)}</dd></div>
                        {/if}
                    </dl>
                    {#if store.error}
                        <p class="ol-warn ds-error">{t('The database could not be opened, the history is in RAM: {reason}', {reason: store.error})}</p>
                    {/if}
                    {#if store.copy}
                        <!-- task 229: the snapshot on the USB stick -->
                        <dl class="jr-figures" data-store-copy>
                            <div><dt>{t('Copy on the stick {label}', {label: store.copy.label})}</dt><dd data-figure="last-copy">{store.copy.last_copy ? journalWhen(store.copy.last_copy) : t('none yet')}</dd></div>
                        </dl>
                        {#if !store.copy.present}
                            <p class="ol-warn ds-error" data-copy-missing>{t('The USB stick {label} is not plugged in: the history stays on the userfs, and the copy goes to the stick once it is back.', {label: store.copy.label})}</p>
                        {/if}
                        {#if store.copy.last_error}
                            <p class="ol-warn ds-error" data-copy-error>{t('The last copy to the USB stick failed: {reason}', {reason: store.copy.last_error})}</p>
                        {/if}
                        {#if store.copy.loaded}
                            <p class="ol-muted" data-copy-loaded>{t('At the start the copy on the stick was newer than the file on the userfs and was loaded ({when}).', {when: journalWhen(store.copy.loaded)})}</p>
                        {/if}
                    {/if}
                    {#if store.last_sync_error}
                        <p class="ol-warn ds-error">{t('The last write failed: {reason}', {reason: store.last_sync_error})}</p>
                    {/if}
                    <div class="lv-grid">
                        <label class="lv-field" for="ol-ds-mode">
                            <span>{t('Storage mode')}<Help>{t('The duty cycle and carrier sense of the radio interfaces, as the Interfaces page draws them, and the last value of the datapoints the cards of the App show, with when each was reported and when it last changed. On the SD card products it is kept in RAM and written at the interval, which spares the card; the VM and the container products write it as it is taken.')}</Help></span>
                            <select id="ol-ds-mode" class="hmm-select" bind:value={store.mode} disabled={storeBusy}>
                                <option value="" disabled={storeOnStick && store.default_mode !== 'ram-sync'}>{t('Product default ({mode})', {mode: modeLabel(store.default_mode)})}</option>
                                <option value="ram" disabled={storeOnStick}>{t('RAM only')}</option>
                                <option value="ram-sync">{t('RAM, written to the userfs')}</option>
                                <option value="persistent" disabled={storeOnStick}>{t('Persistent on the userfs')}</option>
                            </select>
                        </label>
                        {#if storeChosen === 'ram-sync' || storeChosen === 'persistent'}
                            <label class="lv-field" for="ol-ds-interval">
                                <span>{t('Write interval')}<Help>{storeChosen === 'persistent'
                                    ? t('How often the times of device values reported again unchanged are written: 15min to 1d; empty = {default}. Changed values and the samples are written at once, and everything when the system service stops.', {default: store.default_sync_interval})
                                    : t('How often the history is written from RAM to the userfs: 15min to 1d; empty = {default}. It is also written when the system service stops, at every shutdown and reboot.', {default: store.default_sync_interval})}</Help></span>
                                <input id="ol-ds-interval" class="hmm-input hmm-mono" list="ol-ds-intervals" bind:value={store.sync_interval} placeholder={store.default_sync_interval} disabled={storeBusy} />
                                <datalist id="ol-ds-intervals">
                                    {#each store.sync_intervals as iv (iv)}<option value={iv}></option>{/each}
                                </datalist>
                            </label>
                        {/if}
                    </div>
                    {#if storeChosen !== 'ram'}
                        <div class="lv-field ds-location" data-store-location>
                            <span>{t('Where the file is')}<Help>{t('The database file stays on local storage: the system\'s own storage, in a folder inside etc/occulite (the part the system service may write). Never on a network share - the file is mapped into memory and needs locking and fsync that NFS and SMB do not guarantee. A USB stick takes a copy: the file stays on the system storage in RAM, written to the userfs, and at every write and when the system service stops a copy goes to the stick; when the system starts with a newer copy on the stick - a new card, say - that copy is loaded.')}</Help></span>
                            <LocationPicker use="store" bind:location={storeLoc} bind:folder={storeFolder} disabled={storeBusy} onchange={syncStoreLoc} />
                        </div>
                    {/if}
                    <p class="ol-muted ds-tradeoff">{t('RAM only is empty after every restart of the system service. RAM, written to the userfs, writes the card once per interval and when the service stops: a restart loses nothing, a power loss the time since the last write. Persistent on the userfs writes every sample round, once a minute, and every device value that changes at once; the times of values reported again unchanged are written at the interval. The history is not part of a backup.')}</p>
                    <div class="ol-actions">
                        <button class="hmm-button primary" disabled={storeBusy || !!storeLocProblem} onclick={saveStore}>{t('Save')}</button>
                    </div>
                    {#if storeMsg}<p class="ol-muted">{storeMsg}</p>{/if}
                </div>
                {#if hist}
                    <div class="ds-history">
                        <h3 class="ds-h">{t('Recorded datapoints')}</h3>
                        <p class="ol-muted ds-tradeoff">{t('The history keeps the last {n} rows of each datapoint the cards of the App draw: a value at most once a minute and only when it moved, a contact, a motion or a key at every change. A device that reports rarely keeps a longer history than one that reports often.', {n: String(hist.rows_per_series)})}</p>
                        <dl class="jr-figures">
                            <div><dt>{t('Series')}</dt><dd class="hmm-mono" data-figure="series">{hist.series}</dd></div>
                            <div><dt>{t('Recorded')}</dt><dd class="hmm-mono ds-list" data-figure="datapoints">{Object.keys(hist.datapoints).sort().join(', ')}</dd></div>
                        </dl>
                        <div class="lv-grid">
                            <label class="lv-field" for="ol-hist-add">
                                <span>{t('Also record')}<Help>{t('Datapoint names, separated by commas, recorded in addition to the list, such as CARBON_DIOXIDE_CONCENTRATION.')}</Help></span>
                                <input id="ol-hist-add" class="hmm-input hmm-mono" bind:value={histAdd} disabled={histBusy} />
                            </label>
                            <label class="lv-field" for="ol-hist-remove">
                                <span>{t('Do not record')}<Help>{t('Datapoint names of the list that are not recorded. What was recorded stays readable until the ring overwrites it.')}</Help></span>
                                <input id="ol-hist-remove" class="hmm-input hmm-mono" bind:value={histRemove} disabled={histBusy} />
                            </label>
                        </div>
                        {#if histErr}<p class="ol-warn ds-error">{histErr}</p>{/if}
                        <div class="ol-actions">
                            <button class="hmm-button primary" data-action="save-history" disabled={histBusy} onclick={saveHist}>{t('Save')}</button>
                        </div>
                        {#if histMsg}<p class="ol-muted">{histMsg}</p>{/if}
                    </div>
                {/if}
            {:else if !storeErr}
                <Loading />
            {/if}
        </div>
    {:else}
        <div class="ls-panel ol-warn-edge {warnEdge(['journal-target', 'journal-sync'])}" role="tabpanel" id="ls-panel-journal" aria-labelledby="ls-tab-journal" {...warnFor(['journal-target', 'journal-sync'])}>
            {#if journalErr}<div class="ol-warn">{journalErr}</div>{/if}
            {#if journal}
                <div class="jr-panel">
                    <!-- the state stays on the page; why RAM is the default on a card is the Storage field's ? -->
                    <p class="ol-muted jr-now">
                        {journal.effective === 'persistent'
                            ? t('The journal is on the userfs now')
                            : journal.effective === 'ram-sync' && journal.target_label
                              ? t('The journal is in RAM now and copied to the USB stick {label}', {label: journal.target_label})
                              : journal.effective === 'ram-sync' && journal.target_share
                                ? t('The journal is in RAM now and copied to the network share {name}', {name: journal.target_share})
                              : journal.effective === 'ram-sync'
                                ? t('The journal is in RAM now and copied to the userfs')
                                : t('The journal is in RAM now')}{#if journal.usage}{' · '}{journal.usage}{/if}.
                    </p>
                    <dl class="jr-figures">
                        <div><dt>{t('In RAM')}</dt><dd class="hmm-mono" data-figure="ram">{journalBytes(journal.ram_usage)}</dd></div>
                        <div><dt>{journal.target_label ? t('On the USB stick') : journal.target_share ? t('On the network share') : t('On the userfs')}</dt><dd class="hmm-mono" data-figure="target">{journalBytes(journal.target_usage)}</dd></div>
                        <div><dt>{journal.target_label ? t('Free on the USB stick') : journal.target_share ? t('Free on the network share') : t('Free on the userfs')}</dt><dd class="hmm-mono" data-figure="free">{journalBytes(journal.target_free)}</dd></div>
                    </dl>
                    {#if journal.reboot_pending}
                        <p class="ol-warn jr-pending">
                            {journal.effective === 'ram-sync'
                                ? t('A reboot is pending: the copies have stopped and stay readable until the next boot; from then on the journal is in RAM only.')
                                : t('A reboot is pending: the journal stays on the userfs until the next boot; from then on it is in RAM.')}
                        </p>
                    {/if}
                    {#if journal.fallback && journal.target_label && effectiveStorage(journalSaved, journal.default_storage) === 'ram-sync'}
                        <p class="ol-warn jr-fallback" data-fallback="usb">{t('The USB stick {label} is not plugged in: the journal is in RAM, and the copies go to the stick as soon as it is back.', {label: journal.target_label})}</p>
                    {:else if journal.fallback && journal.target_share && effectiveStorage(journalSaved, journal.default_storage) === 'ram-sync'}
                        <p class="ol-warn jr-fallback" data-fallback="share">{t('The network share {name} could not be reached: the journal is in RAM, and the copies go to it as soon as a copy reaches it.', {name: journal.target_share})}</p>
                    {:else if journal.fallback}
                        <p class="ol-warn jr-fallback">{t('The journal could not be set up on the userfs and is in RAM: {reason}', {reason: journal.fallback})}</p>
                    {/if}
                    {#if journal.target_label}
                        {#if journal.target_path && !journal.target_ok}
                            <p class="ol-warn jr-target-warn">{t('The USB stick {label} is read-only: the copies cannot go to it.', {label: journal.target_label})}</p>
                        {/if}
                    {:else if journal.target_share}
                        {#if !journal.target_ok}
                            <p class="ol-warn jr-target-warn">{t('The network share {name} is mounted read-only: the copies cannot go to it.', {name: journal.target_share})}</p>
                        {:else if journal.target_unreadable}
                            <!-- B-223: written as root, and not readable by the system's own user -->
                            <p class="ol-warn jr-target-warn" data-journal-unreadable>{t('The copies are written to the network share {name}, but the system cannot read them back, so the Log page cannot show them.', {name: journal.target_share})} {t('On an NFS export that maps root to nobody (root_squash), map root to root (TrueNAS: Maproot User root) or map all users to one account (TrueNAS: Mapall User).')}</p>
                        {/if}
                    {:else if !journal.target_ok}
                        <p class="ol-warn jr-target-warn">{t('The userfs cannot take the journal right now: /usr/local is not mounted or not writable.')}</p>
                    {/if}
                    <div class="lv-grid">
                        <label class="lv-field" for="ol-jr-storage">
                            <span>{t('Storage mode')} <span class="ol-muted">· STORAGE</span><Help>{t("On the SD card products the journal is in RAM by default, because journald is the card's biggest writer; the VM and the container products keep it on their disk. Whatever is chosen here survives a firmware update.")}</Help></span>
                            <select id="ol-jr-storage" class="hmm-select" bind:value={journal.storage} disabled={journalBusy}>
                                <option value="">{t('Product default ({mode})', {mode: journal.default_storage === 'persistent' ? t('Persistent') : t('RAM')})}</option>
                                <option value="ram">{t('RAM only')}</option>
                                <option value="ram-sync">{onShare ? t('RAM, copied to the network share') : onStick ? t('RAM, copied to the USB stick') : t('RAM, copied to the userfs')}</option>
                                <option value="persistent" disabled={onStick}>{t('Persistent on the userfs')}</option>
                            </select>
                        </label>
                    </div>
                    <!-- task 228: the one location picker - the userfs, a USB stick by its label, a network share by its name -->
                    <div class="lv-field jr-target" data-journal-target>
                        <span>{t('Target')} <span class="ol-muted">· TARGET</span><Help>{t("Where RAM with copies and persistent keep the journal. A USB stick or a network share takes only the copies: the journal stays in RAM and is copied there at the interval and at shutdown (a stick also when it is plugged in and before it is unmounted), so pulling a stick or losing a share never leaves the journal with open files. A stick is named by its label, whichever USB port it is in, and a stick with another label is never written; a share by its name on System → Storage.")}</Help></span>
                        <LocationPicker use="journal" bind:location={targetLocation} bind:folder={targetFolder} disabled={journalBusy} onchange={syncTarget} />
                        {#if onStick && journal.target && journal.target !== 'userfs'}
                            {@const p = parseLocation(journal.target)}
                            {#if p}
                                <p class="ol-muted" data-stick-chosen>{p.kind === 'usb'
                                    ? t('The copies go to {dir} on the USB stick labelled {label}; a stick with another label is never written.', {dir: p.folder || STICK_DIR, label: p.name})
                                    : t('The copies go to {dir} on the network share {name}; when it cannot be reached, a copy is skipped and the journal stays in RAM.', {dir: p.folder || STICK_DIR, name: p.name})}</p>
                            {/if}
                        {/if}
                        {#if onStick && journalChosen !== 'ram-sync'}
                            <p class="ol-warn" data-stick-mode>{onShare ? t('A network share takes only the copies: choose RAM, copied to the network share, as the storage.') : t('A USB stick takes only the copies: choose RAM, copied to the USB stick, as the storage.')}</p>
                        {/if}
                    </div>
                    <p class="ol-muted jr-tradeoff">{t('RAM only is lost at every reboot and costs memory, up to the RAM limit. RAM, copied to the userfs, writes the card once per interval and at every shutdown: a reboot loses nothing, a power loss the time since the last copy. Persistent on the userfs survives a reboot and a power loss and costs almost no RAM, but writes the SD card continuously.')}</p>
                    <p class="ol-muted jr-when">{t('A switch to persistent or to RAM with copies applies at once. A switch to RAM only applies at the next reboot, because the journal on the userfs is not unmounted while in use. The size limits and the copy interval apply at once.')}</p>
                    {#if journalSwitch === 'persistent'}
                        <p class="jr-switch">{t('Saving moves the journal onto the userfs at once.')}</p>
                    {:else if journalSwitch === 'ram-sync' && onShare}
                        <p class="jr-switch">{t('Saving keeps the journal in RAM and starts the copies to the network share; the first one comes at the interval, or now with Copy now.')}</p>
                    {:else if journalSwitch === 'ram-sync' && onStick}
                        <p class="jr-switch">{t('Saving keeps the journal in RAM and starts the copies to the USB stick at once, while it is plugged in.')}</p>
                    {:else if journalSwitch === 'ram-sync'}
                        <p class="jr-switch">
                            {journal.effective === 'persistent'
                                ? t('Saving moves the journal into RAM at once and starts the copies; what is on the userfs stays readable.')
                                : t('Saving starts the copies to the userfs at once; the journal stays in RAM.')}
                        </p>
                    {:else if journalSwitch === 'reboot'}
                        <p class="jr-switch">{t('Saving keeps the journal on the userfs until the next reboot; from then on it is in RAM.')}</p>
                    {:else if journalSwitch === 'stop-copies'}
                        <p class="jr-switch">{t('Saving stops the copies at once, after a last one; they stay readable until the next reboot, and from then on the journal is in RAM only.')}</p>
                    {:else if journalChosen === 'ram-sync' && journal.target !== journalSavedTarget && journal.target !== 'usb:'}
                        <p class="jr-switch" data-switch="target">{onShare ? t('Saving sends the next copies to the network share; the copies made so far stay where they are.') : onStick ? t('Saving sends the next copies to the USB stick; the copies made so far stay where they are.') : t('Saving sends the next copies to the userfs; the copies made so far stay where they are.')}</p>
                    {/if}
                    {#if journalChosen === 'ram-sync' || journal.effective === 'ram-sync'}
                        <!-- task 85: the copies - when the last and the next one are, and their three settings -->
                        <div class="jr-sync">
                            <dl class="jr-figures jr-copies">
                                <div>
                                    <dt>{t('Last copy')}</dt>
                                    <dd data-figure="last-sync">{journal.last_sync ? t('{when}, {n} files', {when: journalWhen(journal.last_sync), n: journal.last_sync_copied}) : t('none yet')}{#if journal.last_sync && journal.last_sync_reason}{@const why = {interval: t('at the interval'), early: t('early: the RAM journal was nearly full'), shutdown: t('at shutdown'), manual: t('by hand'), plug: t('when the USB stick was plugged in'), unplug: t('before the USB stick was unmounted')}[journal.last_sync_reason]}<span class="ol-muted" data-sync-reason={journal.last_sync_reason}> · {why}</span>{/if}</dd>
                                </div>
                                {#if journal.effective === 'ram-sync'}
                                    <div><dt>{t('Next copy')}</dt><dd data-figure="next-sync">{journalWhen(journal.next_sync)}</dd></div>
                                {/if}
                            </dl>
                            {#if journal.last_sync_result === 'failed'}
                                <p class="ol-warn jr-sync-failed">{t('The last copy failed: {reason}', {reason: journal.last_sync_error ?? ''})}</p>
                            {/if}
                            {#if journalChosen === 'ram-sync'}
                                <div class="lv-grid">
                                    <label class="lv-field" for="ol-jr-interval">
                                        <span>{t('Copy interval')} <span class="ol-muted">· SYNC_INTERVAL</span><Help>{t('How often the journal is copied from RAM to the userfs: 15min to 7d, e.g. 1h, 6h, 12h or 24h; empty = 6h. It is also copied at every shutdown and reboot, and early when the journal takes 80 % of the RAM limit (at most every 15 minutes). Between two copies the RAM limit applies: what does not fit is dropped before it is copied.')}</Help></span>
                                        <input id="ol-jr-interval" class="hmm-input hmm-mono" list="ol-jr-intervals" bind:value={journal.sync_interval} placeholder="6h" disabled={journalBusy} />
                                        <datalist id="ol-jr-intervals">
                                            {#each SYNC_INTERVALS as iv (iv)}<option value={iv}></option>{/each}
                                        </datalist>
                                    </label>
                                    <label class="lv-field" for="ol-jr-targetuse">
                                        <span>{t('Copies: size limit')} <span class="ol-muted">· TARGET_MAX_USE</span><Help>{t('The most the copies on the userfs may take; the oldest go first. Empty = 64M.')}</Help></span>
                                        <input id="ol-jr-targetuse" class="hmm-input hmm-mono" bind:value={journal.target_max_use} placeholder="64M" disabled={journalBusy} />
                                    </label>
                                    <label class="lv-field" for="ol-jr-targetage">
                                        <span>{t('Copies: age limit')} <span class="ol-muted">· TARGET_MAX_AGE</span><Help>{t('Copies older than this are removed, e.g. 30d or 2w; empty = no age limit.')}</Help></span>
                                        <input id="ol-jr-targetage" class="hmm-input hmm-mono" bind:value={journal.target_max_age} placeholder="30d" disabled={journalBusy} />
                                    </label>
                                </div>
                            {/if}
                        </div>
                    {/if}
                    <div class="lv-grid">
                        <label class="lv-field" for="ol-jr-runtime">
                            <span>{t('RAM limit')} <span class="ol-muted">· RUNTIME_MAX_USE</span><Help>{t("journald's RuntimeMaxUse: the most the journal may take in RAM (/run/log/journal); empty = the image's default, 16M.")}</Help></span>
                            <input id="ol-jr-runtime" class="hmm-input hmm-mono" bind:value={journal.runtime_max_use} placeholder="16M" disabled={journalBusy} />
                        </label>
                        <label class="lv-field" for="ol-jr-maxuse">
                            <span>SystemMaxUse <span class="ol-muted">· SYSTEM_MAX_USE</span><Help>{t('The most the journal may take; empty = journald\'s own default (10% of the filesystem, at most 4G).')}</Help></span>
                            <input id="ol-jr-maxuse" class="hmm-input hmm-mono" bind:value={journal.system_max_use} placeholder="32M" disabled={journalBusy} />
                        </label>
                        <label class="lv-field" for="ol-jr-maxfile">
                            <span>SystemMaxFileSize <span class="ol-muted">· SYSTEM_MAX_FILE</span><Help>{t('Size of one journal file before it is rotated; smaller files mean finer-grained vacuuming.')}</Help></span>
                            <input id="ol-jr-maxfile" class="hmm-input hmm-mono" bind:value={journal.system_max_file} placeholder="8M" disabled={journalBusy} />
                        </label>
                        <label class="lv-field" for="ol-jr-burst">
                            <span>RateLimitBurst <span class="ol-muted">· RATE_LIMIT_BURST</span><Help>{t('Messages one service may log per 30 s before journald drops the rest; empty = journald\'s default.')}</Help></span>
                            <input id="ol-jr-burst" class="hmm-input hmm-mono" bind:value={journal.rate_limit_burst} placeholder="10000" disabled={journalBusy} />
                        </label>
                    </div>
                    <div class="ol-actions">
                        <button class="hmm-button primary" disabled={journalBusy || (onStick && !!folderError(targetFolder, false))} onclick={saveJournal}>{t('Save')}</button>
                        {#if journal.effective === 'ram-sync'}
                            <!-- a copy now is what the saved mode does, not the form's: after a switch away it has stopped -->
                            <button class="hmm-button jr-copy-now" disabled={journalBusy || journalSavedMode !== 'ram-sync'} onclick={copyNow}>{t('Copy now')}</button>
                        {/if}
                    </div>
                    {#if journalMsg}<p class="ol-muted">{journalMsg}</p>{/if}
                </div>
            {:else if !journalErr}
                <Loading />
            {/if}
        </div>
    {/if}
</ModalDialog>

<style>
    :global(.ls-tabs) { align-self: flex-start; margin: 2px 0 14px; flex-shrink: 0; /* a tall panel must not squeeze the tabs away (task 228) */ }
    .ls-panel { padding-bottom: 16px; }
    /* 27.8: the level grid, one card per daemon */
    .lv-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(260px, 100%), 1fr)); gap: 12px 20px; margin-bottom: 10px; }
    .lv-field { display: flex; flex-direction: column; gap: var(--ol-label-gap); }
    .lv-field > span:first-child { font-weight: 600; }
    .lv-check { display: flex; align-items: center; gap: 6px; }
    /* task 101: occulited's row across the grid, its areas wrapping on a phone; the notes under a select */
    .lv-wide { grid-column: 1 / -1; }
    .lv-name { font-weight: 600; }
    .lv-narrow { max-width: min(260px, 100%); }
    .lv-areas { border: 0; padding: 0; margin: 4px 0 0; min-width: 0; }
    .lv-areas legend { padding: 0; margin-bottom: 4px; font-size: var(--hmm-font-size-small); color: var(--hmm-fg-muted); }
    .lv-area-list { display: flex; flex-wrap: wrap; gap: 4px 18px; }
    .lv-note { margin: 2px 0 0; overflow-wrap: anywhere; }
    /* task 85: the journal's figures, a row of label over value that wraps on a phone */
    .jr-figures { display: flex; flex-wrap: wrap; gap: 6px 28px; margin: 0 0 10px; }
    .jr-figures > div { display: flex; flex-direction: column; gap: 2px; }
    .jr-figures dt { font-size: var(--hmm-font-size-small); color: var(--hmm-fg-muted); }
    .jr-figures dd { margin: 0; font-weight: 600; }
    .jr-tradeoff, .jr-when { margin: 0 0 6px; }
    /* task 214: the history's panel, the journal's shapes */
    .ds-now, .ds-tradeoff { margin: 0 0 10px; }
    .ds-error { overflow-wrap: anywhere; }
    /* task 195: the history's list below the storage */
    .ds-history { margin-top: 18px; padding-top: 12px; border-top: 1px solid var(--hmm-border-muted); }
    .ds-h { margin: 0 0 8px; font-size: 1em; }
    .ds-list { font-weight: 400; overflow-wrap: anywhere; }
    .jr-switch { margin: 0 0 10px; font-weight: 600; }
    /* the copies of ram-sync, set apart from the journald limits below them */
    .jr-sync { border-left: 3px solid var(--hmm-border-muted); padding-left: 12px; margin: 4px 0 12px; }
    .jr-sync-failed, .jr-fallback { overflow-wrap: anywhere; }
    /* task 228: the target's location picker and what it says below it */
    .ds-location { margin: 0 0 12px; display: flex; flex-direction: column; gap: 8px; }
    .jr-target { margin: 0 0 12px; display: flex; flex-direction: column; gap: 8px; }
    .jr-target p { margin: 0; overflow-wrap: anywhere; }
</style>
