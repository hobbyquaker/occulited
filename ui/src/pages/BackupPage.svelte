<script lang="ts">
    import {api, ApiError, REQUEST_HEADER} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {t} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {auth} from '../lib/auth.svelte';
    import {download, ticketFor, withTicket} from '../lib/download';
    import {confirmTicket} from '../lib/confirm';
    import {recoveryKeyError} from '../lib/keyerror';
    import {firmwareRefusalLines, refusalOf, type FirmwareRefusal} from '../lib/hmipfirmware';
    import BackupEncryption, {type EncryptionView} from '../lib/BackupEncryption.svelte';
    import Help from '../lib/Help.svelte';
    import BidCosKeyCheck, {keyConfirmed, keyWarning, type Verdict} from '../lib/BidCosKeyCheck.svelte';
    import FactoryReset from '../lib/FactoryReset.svelte';
    import BackupTargets from '../lib/BackupTargets.svelte';
    import BootBar from '../lib/BootBar.svelte';
    import {EASE_MS, type BootEntry} from '../lib/bootbar';
    import {beginBoot, removeEntry, watchBoot} from '../lib/bootwatch';
    import {boxUptime} from '../lib/power';
    import {onMount} from 'svelte';
    import {scrollToAnchor} from '../lib/anchor';

    // task 296: backup_key / system_key - which side has a BidCos security key of its own; key_index the backup's
    interface Check { ok: boolean; output: string; backup_version?: string; running_version?: string; needs_key: boolean; has_rega: boolean; backup_key?: boolean; system_key?: boolean; key_index?: number }
    // openccu-lite task 251: the paired devices a checked backup holds, and this system's side
    interface RadioBackup {
        bidcos_rf: {devices: number; address?: string; serial?: string; has_key: boolean; gateways: number};
        // openccu-lite task 299: what the access point file says about the HmIP security counter
        hmip: {devices: number; identity_sgtin?: string; local_key: boolean; device_key_map: boolean; security_counter?: {first_connect: string; offset: number; calc: number; behind?: boolean; verdict: string; wraps_at: string}};
        bidcos_wired: {devices: number; gateways: number};
        key_index: number;
        files: string[];
        version?: string;
    }
    interface RestoreModule { hardware: string; serial: string; sgtin?: string; version?: string }
    interface RestoreTarget { paired: Record<string, {devices: number; known: boolean; error?: string}>; devices: number; unknown?: string[]; user_key: boolean; importable: boolean; hmip_module?: RestoreModule; bidcos_module?: RestoreModule }
    // task 275: module_changed - the backup's HmIP identity belongs to another module than this system's;
    // task 278: non_default_key - the backup's BidCos key store is not the factory key and comes along
    // openccu-lite B-289: hmip_firmware - the move onto this system's module is refused (its firmware cannot take the key)
    interface DevicesView { backup: RadioBackup; target: RestoreTarget; module_changed?: boolean; non_default_key?: boolean; hmip_firmware?: FirmwareRefusal }
    // task 91: what the upload's age header said, and whether the recovery key is still needed
    interface Encryption { encrypted: boolean; format?: string; armored?: boolean; box_fingerprint?: string; recovery_fingerprint?: string; known?: 'current' | 'previous' | 'unknown'; key_created?: string; opened_with?: 'box' | 'recovery'; needs_recovery_key: boolean; passphrase?: boolean; created_here: boolean }
    let encryption = $state<EncryptionView | null>(null);
    const encrypted = $derived(!!encryption?.enabled);
    let encInfo = $state<Encryption | null>(null);
    let recoveryKey = $state('');
    // the plain download although encryption is on (D-64): the password every time, through a
    // confirmed ticket (task 154) that the navigation carries in ?confirm=
    const BACKUP_PLAIN_PATH = '/api/system/v1/backup/unencrypted';
    async function downloadPlain() {
        if (downloading) return;
        error = '';
        const go = await ask({
            title: t('Download unencrypted'),
            message: t('This file contains every key of this system in plain: the radio keys, password hashes, tokens and certificates. Keep it only as long as you need it - for a restore on OpenCCU or a CCU3, which cannot read encrypted backups. The download is noted in the journal.'),
            confirm: t('Continue'),
            danger: true,
        });
        if (!go) return;
        downloading = true;
        try {
            const confirm = await confirmTicket(BACKUP_PLAIN_PATH, {
                title: t('Download unencrypted'),
                message: t('An unencrypted backup holds every key of this system in plain and is confirmed every time.'),
                provider: t('An unencrypted backup asks who you are every time: you sign in at the identity provider once more and come back here.'),
                impossible: t('This account has no password and no identity provider is configured, so it cannot confirm the download.'),
            });
            if (confirm === null) return;
            const ticket = await ticketFor(backupUrl);
            location.assign(withTicket(`${backupUrl}?encrypted=false&confirm=${encodeURIComponent(confirm)}`, ticket));
        } catch (e) {
            error = (e as Error).message;
        } finally {
            downloading = false;
        }
    }
    async function decrypt() {
        if (!uploaded || !recoveryKey.trim()) return;
        busy = 'decrypt';
        error = '';
        try {
            const r = await api.post<{file: string; check: Check; encryption: Encryption}>('/api/system/v1/restore/decrypt', {file: uploaded, recovery_key: recoveryKey});
            uploaded = r.file;
            check = r.check;
            encInfo = r.encryption;
            recoveryKey = '';
            restoreVerdict = null;
            key = '';
        } catch (e) {
            error = recoveryKeyError(e, t);
            // B-194: a damaged upload is gone with the refusal - back to the file picker
            if (e instanceof ApiError && e.code === 'corrupt') {
                uploaded = '';
                encInfo = null;
                check = null;
            }
        } finally {
            busy = '';
        }
    }
    const when = (iso?: string) => (iso ? new Date(iso).toLocaleDateString() : '');

    let busy = $state('');
    let error = $state('');
    let file = $state<File | null>(null);
    let uploaded = $state('');
    let check = $state<Check | null>(null);
    let key = $state('');
    let force = $state(false);
    let applied = $state('');
    // task 296: the passphrase check of the backup's BidCos key - the restore's and the import's -
    // and the warning the result repeats when it was not confirmed
    let restoreVerdict = $state<Verdict | null>(null);
    let importVerdict = $state<Verdict | null>(null);
    let importPass = $state('');
    let appliedKeyNote = $state('');
    let namesMsg = $state('');
    // task 251: the paired devices of the checked backup, loaded after a check for an administrator
    let devices = $state<DevicesView | null>(null);
    let devicesMsg = $state('');
    // task 278: this system has a key store of its own - the backup's replaces it, on the user's word
    let replaceKey = $state(false);
    const backupEmpty = (b: RadioBackup) => b.bidcos_rf.devices === 0 && b.hmip.devices === 0 && b.bidcos_wired.devices === 0 && !b.hmip.identity_sgtin && !b.bidcos_rf.address;
    const moduleName = (m?: RestoreModule) => m ? `${m.hardware} ${m.serial}`.trim() + (m.sgtin ? ` (${m.sgtin})` : '') : '';
    // openccu-lite task 301 (maintainer, 2026-09-30): the backup's HmIP identity belongs to another module
    // than this system's - the import or the restore moves the network onto this module through eQ-3's key
    // server, which may refuse it; said in the danger question of both, and that the source system must stop
    function hmipMoveLines(b: RadioBackup, to: string): string[] {
        return [
            t('The HmIP identity belongs to module {from}; hmipserver moves the HmIP network onto {to} at the start (the adapter exchange).', {from: b.hmip.identity_sgtin ?? '', to}),
            b.hmip.local_key
                ? t('The backup is in local key mode: no key server is involved in the move.')
                : t('The move goes through eQ-3\'s key server, which needs an internet connection and may refuse it; after a refusal HmIP-RF stays stopped, and the Interfaces page shows the ways out.'),
            t('The system this backup comes from must not keep running this HmIP network: one network, one running system.'),
        ];
    }
    // openccu-lite B-289: the import and the restore are refused onto a module whose firmware cannot take
    // the HmIP network's key - said in a notice with nothing to confirm, and in the daemon's 422 alike
    async function firmwareRefused(): Promise<boolean> {
        if (!devices?.hmip_firmware) return false;
        await ask({title: t('HmIP-RF cannot move to this module'), message: firmwareRefusalLines(devices.hmip_firmware, 'backup', t).join('\n\n'), confirm: t('Close'), cancel: ''});
        return true;
    }
    function apiMessage(e: unknown): string {
        const r = e instanceof ApiError && e.code === 'hmip-firmware' ? refusalOf(e.detail) : null;
        return r ? firmwareRefusalLines(r, 'backup', t).join(' ') : (e as Error).message;
    }
    async function loadDevices() {
        devices = null;
        devicesMsg = '';
        replaceKey = false;
        importVerdict = null;
        importPass = '';
        restoreVerdict = null;
        key = '';
        if (!uploaded || auth.role !== 'admin') return;
        try {
            const v = await api.get<DevicesView>(`/api/system/v1/restore/devices?file=${encodeURIComponent(uploaded)}`);
            // an answer without the two halves (an older daemon, a stub) shows nothing rather than a broken block
            devices = v && v.backup && v.target ? v : null;
        } catch (e) {
            devicesMsg = (e as Error).message;
        }
    }
    // the import takes over the backup's radio identity and reboots, like the restore
    async function importDevices() {
        if (!devices || !uploaded) return;
        if (await firmwareRefused()) return;
        const b = devices.backup;
        const parts = [
            t('{n} BidCos-RF devices', {n: b.bidcos_rf.devices}),
            t('{n} HmIP devices', {n: b.hmip.devices}),
            t('{n} BidCos-Wired devices', {n: b.bidcos_wired.devices}),
        ].join(', ');
        const lines = [
            t('The paired devices of this backup - {parts} - come onto this system with the radio identity they are bound to: the BidCos address and security key, the HmIP identity, the LAN gateways.', {parts}),
            t('This system\'s own radio identity is set aside and it reboots.'),
        ];
        if (devices.module_changed) lines.push(...hmipMoveLines(b, moduleName(devices.target.hmip_module)));
        if (devices.non_default_key) lines.push(t('The backup\'s BidCos security key is not the default key: it comes along as it is.'));
        if (devices.target.user_key) lines.push(t('This system\'s own security key is replaced by the backup\'s.'));
        if (check?.has_rega) lines.push(t('The names, rooms and functions of the backup\'s ReGa database are imported first (merged with what this system has), then the system reboots.'));
        // task 296: without the confirmed passphrase the warning comes first - the import still runs on the word
        const unconfirmed = !!devices.non_default_key && !keyConfirmed(importVerdict, true);
        if (unconfirmed) lines.unshift(...keyWarning('import', importVerdict));
        if (!(await ask({title: unconfirmed ? t('Import without the confirmed passphrase?') : t('Import the paired devices'), message: lines.join('\n\n'), confirm: unconfirmed ? t('Import anyway and reboot') : t('Import and reboot'), danger: true}))) return;
        busy = 'devices';
        error = '';
        devicesMsg = '';
        const before = await boxUptime();
        const entry = await beginBoot('restore', () => api.get('/api/system/v1/boot-expect?kind=restore'));
        const wait = () => {
            restoring = entry;
            watchBoot({entry, before, onUpdate: (e) => (restoring = e), onBack: () => setTimeout(() => location.reload(), EASE_MS)});
        };
        try {
            const r = await api.post<{ok: boolean; rebooting: boolean; message?: string; names?: {ok: boolean; objects: number; rooms: number; functions: number; error?: string}}>('/api/system/v1/restore/import-devices', {file: uploaded, replace_key: replaceKey, key: importVerdict?.backup === 'skipped' ? '' : importPass, confirm: true});
            // task 281: the names of the same file came first; a failure is said, and the names
            // import above still works while the file is there (until the reboot)
            const namesLine = r.names ? (r.names.ok ? t('Names: {o} named objects, {r} rooms, {f} functions imported from the backup.', {o: r.names.objects, r: r.names.rooms, f: r.names.functions}) : t('The names could not be imported from the backup: {error}', {error: r.names.error ?? ''})) : '';
            if (r.names && !r.names.ok) namesMsg = namesLine;
            if (r.rebooting) {
                applied = [
                    t('The paired devices are imported.'),
                    namesLine,
                    devices.module_changed ? t('HmIP: the identity of module {from} is moved onto {to} when hmipserver starts. The outcome is shown on the Interfaces page; battery devices are re-keyed when they wake up.', {from: b.hmip.identity_sgtin ?? '', to: moduleName(devices.target.hmip_module)}) : '',
                    devices.non_default_key && !unconfirmed ? t('The backup\'s non-default BidCos security key came along. Keep the other system\'s passphrase safe: it is needed to change the key later, or to pair a device that still holds it.') : '',
                ].filter(Boolean).join('\n');
                appliedKeyNote = unconfirmed ? t('The backup\'s non-default BidCos security key came along without a confirmed passphrase. Find it before you change the key, re-key or re-pair these devices, or restore onto a system with another key: without it, only a factory reset of every such device and pairing it again helps.') : '';
                wait();
            } else {
                removeEntry();
                devicesMsg = t('The devices are imported but the reboot did not start: {message} Reboot the system to apply it.', {message: r.message ?? ''});
            }
        } catch (e) {
            if (e instanceof ApiError) {
                removeEntry();
                devicesMsg = apiMessage(e);
            } else {
                wait();
            }
        } finally {
            busy = '';
        }
    }
    async function importNames() {
        if (!uploaded) return;
        busy = 'names';
        namesMsg = '';
        try {
            const r = await api.post<{objects: number; result: {rooms: number; functions: number; unnamed: number}; changed?: boolean}>('/api/meta/v1/import/regadom', {source: 'restore:' + uploaded, mode: 'merge'});
            namesMsg = t('Imported: {o} named objects, {r} rooms, {f} functions.', {o: r.objects, r: r.result.rooms, f: r.result.functions});
        } catch (e) {
            namesMsg = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    // task 86: Restore this on a target's backup - the same check as an upload, the file fetched
    // by the system from the target
    let restoreFrom = $state('');
    async function restoreFromTarget(target: string, name: string) {
        busy = 'check';
        error = '';
        check = null;
        encInfo = null;
        recoveryKey = '';
        applied = '';
        try {
            const data = await api.post<{file: string; check: Check | null; encryption?: Encryption}>('/api/system/v1/restore/check', {target, name});
            uploaded = data.file;
            check = data.check;
            void loadDevices();
            encInfo = data.encryption ?? null;
            restoreFrom = name;
            document.getElementById('restore')?.scrollIntoView({block: 'start'});
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    // task 125: the download link carries no session; the click fetches a one-time ticket and
    // follows the link with it (lib/download.ts)
    const backupUrl = '/api/system/v1/backup';
    let downloading = $state(false);
    async function downloadBackup(e: Event) {
        e.preventDefault();
        if (downloading) return;
        downloading = true;
        error = '';
        try {
            await download(backupUrl);
        } catch (err) {
            error = (err as Error).message;
        } finally {
            downloading = false;
        }
    }

    async function runCheck() {
        if (!file) return;
        busy = 'check';
        error = '';
        check = null;
        encInfo = null;
        recoveryKey = '';
        try {
            const fd = new FormData();
            fd.append('file', file, file.name);
            const res = await fetch('/api/system/v1/restore/check', {method: 'POST', headers: REQUEST_HEADER, body: fd});
            const data = await res.json();
            if (!res.ok) throw new Error(data.message ?? res.statusText);
            uploaded = data.file;
            check = data.check;
            void loadDevices();
            encInfo = data.encryption ?? null;
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function apply() {
        if (!check || !uploaded) return;
        if (await firmwareRefused()) return;
        const question = t('Restore this backup and reboot? /usr/local is replaced: pairings, keys, the HmIP identity, addons and their settings.');
        // task 296: the backup's own BidCos key without a confirmed passphrase - the warning, then the
        // restore on the user's word (force: the script would refuse the key it cannot check)
        const unconfirmed = !!check.backup_key && !keyConfirmed(restoreVerdict, true);
        // task 301: the backup's HmIP identity belongs to another module - the restore moves the network
        // onto this one (the adapter exchange), said and confirmed in red before the restore
        const moveLines = devices?.module_changed ? hmipMoveLines(devices.backup, moduleName(devices.target.hmip_module)) : [];
        if (unconfirmed) {
            if (!(await ask({title: t('Restore without the confirmed passphrase?'), message: [...keyWarning('restore', restoreVerdict), ...moveLines, question].join('\n\n'), confirm: t('Restore anyway'), danger: true}))) return;
        } else if (moveLines.length) {
            if (!(await ask({title: t('Restore onto another radio module?'), message: [...moveLines, question].join('\n\n'), confirm: t('Restore and reboot'), danger: true}))) return;
        } else if (!(await ask({title: t('Restore this backup?'), message: question, confirm: t('Restore and reboot'), danger: true}))) return;
        // the backup's passphrase confirmed but this system has another key: the restore replaces it (the
        // panel said so), and the script, which wants one key for both sides, needs the force for that
        const forced = force || unconfirmed || (!!check.backup_key && restoreVerdict?.system === 'mismatch');
        busy = 'apply';
        error = '';
        // the countdown before the request (task 94): the restore reboots the box when it is done
        const before = await boxUptime();
        const entry = await beginBoot('restore', () => api.get('/api/system/v1/boot-expect?kind=restore'));
        const wait = () => {
            restoring = entry;
            watchBoot({entry, before, onUpdate: (e) => (restoring = e), onBack: () => setTimeout(() => location.reload(), EASE_MS)});
        };
        try {
            // B-193: the answer comes before the reboot; rebooting false = staged for the next boot
            const r = await api.post<{output: string; rebooting: boolean; message?: string}>('/api/system/v1/restore/apply', {file: uploaded, key: restoreVerdict?.backup === 'skipped' ? '' : key, force: forced, confirm: true});
            if (r.rebooting) {
                applied = r.output;
                appliedKeyNote = unconfirmed ? t('Restored without a confirmed passphrase for the backup\'s BidCos security key. Find it before you change the key, re-key or re-pair these devices, or restore onto a system with another key: without it, only a factory reset of every such device and pairing it again helps.') : '';
                wait();
            } else {
                removeEntry();
                error = t('The restore is prepared but the reboot did not start: {message} Reboot the system to apply it.', {message: r.message ?? ''});
            }
        } catch (e) {
            if (e instanceof ApiError) {
                removeEntry();
                error = apiMessage(e);
            } else {
                // no answer at all: the restore's reboot took the box away with the request
                wait();
            }
        } finally {
            busy = '';
        }
    }
    let restoring = $state<BootEntry | null>(null);
    // the welcome page's device import (occulited task 4) lands on #restore: the encryption and the
    // targets above it load after the page and push it down, so the jump is made again
    onMount(() => scrollToAnchor('restore'));
</script>

<SystemTitle />
{#if auth.role !== 'admin'}
    <div class="ol-notice">{t('Backups contain the radio keys; an administrator session is required.')}</div>
{:else}
    {#if error}<div class="ol-notice err">{error}</div>{/if}
    <!-- task 91: the encryption first - what every other section hands out depends on it. occulited
         task 59: every section is a panel under its heading; a warning's edge is on the panel -->
    <BackupEncryption onchange={(v) => (encryption = v)} />

    <!-- task 51: what each section does is behind its heading's ?; the warnings stay on the page -->
    <h2>{t('Create a backup')}<Help>{t('A CCU-compatible .sbk: /usr/local — pairings, keys, interface configuration, addons, names and rooms, occulited\'s own state. It restores on openccu-lite, OpenCCU and a CCU3 alike.')} {t("An encrypted backup (.sbk.age) restores on openccu-lite with this system's key or your recovery key; for OpenCCU or a CCU3, decrypt it first or download it unencrypted.")}</Help></h2>
    <div class="ol-panel" data-section="create">
    {#if encrypted}
        <div class="ol-actions ol-dl">
            <a class="hmm-button primary" href={backupUrl} download onclick={downloadBackup} aria-disabled={downloading} data-action="download-backup">{t('Download encrypted backup')}</a>
            <button type="button" class="hmm-button" onclick={downloadPlain} disabled={downloading} data-action="download-plain">{t('Download unencrypted (for OpenCCU or a CCU3)…')}</button>
        </div>
    {:else}
        <a class="hmm-button" href={backupUrl} download onclick={downloadBackup} aria-disabled={downloading} data-action="download-backup">{t('Download backup')}</a>
        {#if encryption}
            <div class="ol-notice" data-notice="backup-plain">{t('This backup is not encrypted: whoever can read the file has every key of this system.')} <a href="#encryption" onclick={(e) => { e.preventDefault(); document.getElementById('encryption')?.scrollIntoView({block: 'start'}); }}>{t('Set up encryption')}</a></div>
        {/if}
    {/if}
    </div>

    <!-- task 86: the targets - the USB directory, NFS and SMB shares, SSH servers -->
    <!-- occulited task 12: the nightly backup's target and its deliveries -->
    <BackupTargets onrestore={(target, name) => void restoreFromTarget(target, name)} />

    <h2 id="restore">{t('Restore a backup')}<Help>{t('A backup from OpenCCU or a CCU3 is accepted: the pairings, keys, interface configuration and addons in it are exactly what this system wants. Its ReGa database — names, rooms, functions, programs, system variables — cannot be used here; names, rooms and functions come across with the paired devices, or by themselves with the button the check offers. An update from OpenCCU or a CCU keeps them anyway.')}</Help></h2>
    <div class="ol-panel" data-section="restore">
    <div class="ol-toolbar">
        <input class="hmm-input" type="file" accept=".sbk,.age" onchange={(e) => { file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null; check = null; encInfo = null; applied = ''; restoreFrom = ''; }} />
        <button class="hmm-button" onclick={runCheck} disabled={!file || busy !== ''}>{t('Check')}</button>
    </div>
    {#if restoreFrom && uploaded}<p class="ol-muted" data-restore="from-target">{t('From a backup target: {name}', {name: restoreFrom})}</p>{/if}
    {#if encInfo?.encrypted && encInfo.needs_recovery_key}
        <div class="ol-subblock ol-decrypt" data-restore="needs-recovery-key">
            {#if encInfo.passphrase}
                <div class="ol-notice err" data-notice="restore-passphrase">{t('This is an age file protected by a passphrase, which this system cannot open. Decrypt it on a PC with age and upload the .sbk.')}</div>
            {:else}
                <p data-notice="restore-recovery-key">
                    {#if encInfo.known === 'current'}
                        {t('This backup is encrypted to your current recovery key ({fp}). Enter it to decrypt the backup on this system.', {fp: encInfo.recovery_fingerprint ?? ''})}
                    {:else if encInfo.known === 'previous'}
                        {t('This backup needs the earlier recovery key {fp} from {date}.', {fp: encInfo.recovery_fingerprint ?? '', date: when(encInfo.key_created)})}
                    {:else}
                        {t('This backup is encrypted, and not to a key this system knows{fp}. Enter the recovery key from the emergency kit of the system that made it.', {fp: encInfo.recovery_fingerprint ? ` (${encInfo.recovery_fingerprint})` : ''})}
                    {/if}
                </p>
                <label>{t('Recovery key')} <input class="hmm-input hmm-mono ol-keyinput" bind:value={recoveryKey} autocomplete="off" spellcheck="false" placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX" data-input="restore-recovery-key" /></label>
                <div class="ol-actions" style="margin-top:8px"><button class="hmm-button primary" onclick={decrypt} disabled={busy !== '' || !recoveryKey.trim()} data-action="restore-decrypt">{t('Decrypt')}</button></div>
            {/if}
        </div>
    {/if}
    {#if check}
        <pre class="ol-log">{check.output}</pre>
        <dl class="ol-kv">
            <dt>{t('Result')}</dt><dd>{check.ok ? t('valid') : t('rejected')}</dd>
            {#if encInfo?.encrypted}
                <dt>{t('Encryption')}</dt><dd data-restore="opened-with">{encInfo.opened_with === 'box' ? t("encrypted · opened with this system's key") : t('encrypted · decrypted with the recovery key')}{#if encInfo.created_here} · {t('made by this system')}{:else} · <span class="ol-warn">{t('this system did not create this file')}</span>{/if}</dd>
            {/if}
            {#if check.backup_version}<dt>{t('Versions')}</dt><dd>{t('backup {b}, running {r}', {b: check.backup_version, r: check.running_version ?? ''})}</dd>{/if}
            <dt>ReGa</dt><dd>{check.has_rega ? t('contains a ReGa database — the restore ignores it; its names, rooms and functions can be imported here:') : t('none')} {#if check.has_rega}<button class="hmm-button" onclick={importNames} disabled={busy !== ''}>{t('Import the names from this backup')}</button>{/if}{#if namesMsg}<div class="ol-muted">{namesMsg}</div>{/if}</dd>
        </dl>
        <!-- openccu-lite task 251: the paired devices of this backup, and the import onto a system
             without any (all three radios, with the identity and the keys, then a reboot) -->
        {#if devices}
            {@const b = devices.backup}
            {@const tg = devices.target}
            <div class="ol-subblock ol-devices" data-restore="devices">
                <h3>{t('Paired devices in this backup')}<Help>{t('The pairings of the CCU or OpenCCU the backup came from: rfd\'s and hs485d\'s device files with the BidCos address and the security key, hmipserver\'s devices with the HmIP identity (and local key mode, if it was on), the LAN gateways. They come across together, or not at all, and only onto a system that has no device paired yet: the import takes over that system\'s radio identity, so devices paired here would be orphaned.')} {t('One action takes all three: the names, rooms and functions of the backup\'s ReGa database are imported first, then the devices with their keys, then the system reboots.')}</Help></h3>
                {#if backupEmpty(b)}
                    <p class="ol-muted" data-devices="none">{t('None: the backup holds no paired device and no radio identity.')}</p>
                {:else}
                    <dl class="ol-kv">
                        <dt>BidCos-RF</dt><dd data-devices="bidcos-rf">{t('{n} devices', {n: b.bidcos_rf.devices})}{#if b.bidcos_rf.address}{' · '}{t('address {a}', {a: b.bidcos_rf.address})}{/if}{#if b.bidcos_rf.has_key}{' · '}{t('an individual security key')}{/if}{#if b.bidcos_rf.gateways}{' · '}{t('{n} LAN gateways', {n: b.bidcos_rf.gateways})}{/if}</dd>
                        <dt>HmIP</dt><dd data-devices="hmip">{t('{n} devices', {n: b.hmip.devices})}{#if b.hmip.identity_sgtin}{' · '}{t('identity of module {sgtin}', {sgtin: b.hmip.identity_sgtin})}{/if}{#if b.hmip.local_key}{' · '}{t('local key mode')}{/if}{#if b.hmip.device_key_map}{' · '}{t('device key map')}{/if}</dd>
                        <dt>BidCos-Wired</dt><dd data-devices="wired">{t('{n} devices', {n: b.bidcos_wired.devices})}{#if b.bidcos_wired.gateways}{' · '}{t('{n} LAN gateways', {n: b.bidcos_wired.gateways})}{/if}</dd>
                    </dl>
                    {#if tg.unknown?.length}
                        <p class="ol-warn" data-devices-target="unknown">{t('{list} did not answer: whether devices are paired here is not known yet. Try again in a moment.', {list: tg.unknown.join(', ')})}</p>
                    {:else if tg.devices > 0}
                        <p class="ol-warn" data-devices-target="paired">{t('This system has {n} devices paired ({list}): the import is refused, it would take over another system\'s radio identity and orphan them.', {n: tg.devices, list: Object.entries(tg.paired).filter(([, p]) => p.devices > 0).map(([name, p]) => `${name}: ${p.devices}`).join(', ')})}</p>
                    {:else}
                        <p class="ol-muted" data-devices-target="free">{t('This system has no paired devices: the import can take over the backup\'s.')}</p>
                    {/if}
                    <!-- openccu-lite task 275: the backup's HmIP identity belongs to another module - hmipserver
                         moves it onto this one at the start after the import (the adapter exchange); the user is
                         told before what happens, how long, what can fail -->
                    {#if devices.module_changed && devices.hmip_firmware}
                        <!-- openccu-lite B-289: refused - the module's firmware cannot take the network key -->
                        <div class="ol-notice error" data-notice="hmip-firmware">
                            <strong>{t('HmIP-RF cannot move to this module')}</strong>
                            {#each firmwareRefusalLines(devices.hmip_firmware, 'backup', t) as line, i (i)}<p>{line}</p>{/each}
                        </div>
                    {:else if devices.module_changed}
                        <div class="ol-notice" data-notice="module-change">
                            <strong>{t('Another radio module: HmIP re-keys.')}</strong>
                            {t('The HmIP identity in this backup belongs to module {from}; this system runs HmIP-RF on {to}. When hmipserver starts after the import, it takes the identity over onto this module - the adapter exchange: offline in local key mode, otherwise through eQ-3\'s key server, which needs an internet connection and may refuse the move.', {from: b.hmip.identity_sgtin ?? '', to: moduleName(tg.hmip_module)})}
                            {t('Every HmIP device is then re-keyed for the new module; a battery device only when it wakes up, so press a button on it if it stays silent. The Interfaces page shows how the move went and offers a retry. A refusal keeps HmIP-RF stopped; the Interfaces page then names the ways out: the module the network is on now, the saved files of an earlier module, or a fresh start with this module. The system this backup comes from must not keep running this HmIP network.')}
                        </div>
                    {:else if b.hmip.identity_sgtin && !tg.hmip_module}
                        <div class="ol-notice" data-notice="module-change">{t('This system has no HmIP module: the HmIP identity of module {from} is imported and waits for one.', {from: b.hmip.identity_sgtin})}</div>
                    {/if}
                    <!-- openccu-lite task 299 (eq-3/occu#134): the access point's security counter as the file
                         foretells it - a migrating user learns the state of the system he brings -->
                    {#if b.hmip.security_counter && (b.hmip.security_counter.verdict !== 'fine' || b.hmip.security_counter.behind)}
                        {@const sc = b.hmip.security_counter}
                        {@const scp = {first: new Date(sc.first_connect).toLocaleDateString(), offset: String(sc.offset), calc: String(sc.calc), wraps: new Date(sc.wraps_at).toLocaleDateString()}}
                        <div class={sc.verdict === 'near' && !sc.behind ? 'ol-notice' : 'ol-notice warn'} data-notice="security-counter" data-verdict={sc.behind ? 'behind' : sc.verdict}>
                            {#if sc.behind}{t('HmIP security counter: the clock of this system is before the access point\'s first connection ({first}). Set the time before the import, or hmipserver computes the counter from the offset alone.', scp)}
                            {:else if sc.verdict === 'near'}{t('HmIP security counter: the access point was first connected on {first}; with its offset of {offset} the counter computed at the first start here is {calc}, past 2^31. It reaches 2^32, where hmipserver\'s protection against a lower value ends, at about {wraps}. Keep the clock of this system synchronised.', scp)}
                            {:else}{t('HmIP security counter: the access point was first connected on {first}; with its offset of {offset} the counter computed at the first start here is {calc}, past 2^32. hmipserver\'s protection against a lower value is gone on this access point: a start without a synchronised clock can lock every HmIP device out, and the counter is set below what the devices have seen whenever it passes 2^32 again. This system holds hmipserver back while the clock is not trusted; if the devices stop answering after a start, power-cycle them.', scp)}{/if}
                        </div>
                    {/if}
                    <!-- openccu-lite task 278 (option B) and 296: the backup's BidCos key store comes along as it is;
                         its passphrase is asked as a check - match, mismatch, skip - and never stops the import -->
                    {#if devices.non_default_key}
                        <div data-notice="bidcos-key">
                            <BidCosKeyCheck file={uploaded} keyIndex={b.key_index} what="import" bind:verdict={importVerdict} bind:passphrase={importPass} />
                        </div>
                    {/if}
                    {#if tg.user_key}
                        <label class="ol-replace-key"><input type="checkbox" bind:checked={replaceKey} data-input="replace-key" /> {t('Replace this system\'s own BidCos security key with the backup\'s (no device is paired here that could be orphaned)')}</label>
                    {/if}
                    <div class="ol-actions" style="margin-top:8px">
                        <button class="hmm-button danger" onclick={importDevices} disabled={busy !== '' || !tg.importable || (tg.user_key && !replaceKey) || !!devices.hmip_firmware} data-action="import-devices">{t('Import the paired devices and reboot')}</button>
                    </div>
                {/if}
                {#if devicesMsg}<div class="ol-notice error" data-notice="devices">{devicesMsg}</div>{/if}
            </div>
        {:else if devicesMsg}
            <div class="ol-notice error" data-notice="devices">{devicesMsg}</div>
        {/if}
        {#if check.needs_key}
            {#if encInfo?.encrypted}
                <p class="ol-muted">{t('The security key protects the radio link to BidCos devices, and a restore on another system asks for it; the recovery key encrypts the backup itself, so nobody can read it without that key.')}</p>
            {/if}
            {#if check.backup_key}
                <!-- task 296: the backup's own key - its passphrase as a check, never a gate -->
                <div data-restore="key-check"><BidCosKeyCheck file={uploaded} keyIndex={check.key_index ?? 0} what="restore" systemKey={!!check.system_key} bind:verdict={restoreVerdict} bind:passphrase={key} /></div>
            {:else}
                <!-- only this system has a key of its own: the firmware asks for it, as the CCU's restore does -->
                <label>{t('Security key')} <input class="hmm-input" type="password" bind:value={key} autocomplete="off" data-input="restore-key" /></label>
                <label><input type="checkbox" bind:checked={force} data-input="restore-force" /> {t('Force (ignore a key mismatch)')}</label>
            {/if}
        {/if}
        {#if check.ok}
            <div class="ol-actions" style="margin-top:8px"><button class="hmm-button" onclick={apply} disabled={busy !== '' || (check.needs_key && !check.backup_key && !key && !force) || !!devices?.hmip_firmware} data-action="restore-apply">{t('Restore and reboot')}</button></div>
        {/if}
    {/if}
    {#if applied || restoring}
        <div class="ol-notice ol-restorenotice" role="status"><strong>{t('Restoring — the system reboots now.')}</strong>{#if appliedKeyNote}<p class="ol-warn" data-notice="restore-key-warning">{appliedKeyNote}</p>{/if}{#if restoring}<BootBar entry={restoring} />{/if}{#if applied}<pre class="ol-log">{applied}</pre>{/if}</div>
    {/if}
    </div>
{/if}
{#if auth.role === 'admin'}
    <FactoryReset />
{/if}

<style>
    .ol-notice.err { border-left: 3px solid #d33; }
    label { display: block; margin-top: 6px; }
    /* B-115: a file name is one run without a break where the engine does not break after a hyphen
       (Firefox: the backups table 462 px on a 412 px phone); it breaks anywhere, so the column can shrink */
    .ol-dl { display: flex; flex-wrap: wrap; gap: 8px; }
    .ol-decrypt p { max-width: 720px; }
    .ol-decrypt label { display: block; }
    .ol-keyinput { width: min(100%, 26em); }
</style>
