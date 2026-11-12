<script lang="ts">
    import {dateOnly} from '../lib/datefmt';
    import {api, ApiError, REQUEST_HEADER} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {i18n, t} from '../lib/i18n.svelte';
    import {uploadRefusal, type RestoreSpace} from '../lib/restorespace';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {auth} from '../lib/auth.svelte';
    import {download, ticketFor, withTicket} from '../lib/download';
    import {awaitBackup, prepareBackup, preparedUrl} from '../lib/backupjob';
    import BackupWait from '../lib/BackupWait.svelte';
    import {confirmTicket, returned} from '../lib/confirm';
    import {recoveryKeyError} from '../lib/keyerror';
    import {firmwareRefusalLines, refusalOf, type FirmwareRefusal} from '../lib/hmipfirmware';
    import BackupEncryption, {type EncryptionView} from '../lib/BackupEncryption.svelte';
    import Help from '../lib/Help.svelte';
    import BidCosKeyCheck, {keyConfirmed, keyWarning, type Verdict} from '../lib/BidCosKeyCheck.svelte';
    import BackupFile, {radioEmpty, type BackupAddon, type BackupReport, type Encryption, type RadioBackup, type ThisSystem} from '../lib/BackupFile.svelte';
    import {link} from '../lib/router.svelte';
    import FactoryReset from '../lib/FactoryReset.svelte';
    import BackupTargets from '../lib/BackupTargets.svelte';
    import LastBackups from '../lib/LastBackups.svelte';
    import BackupAddons from '../lib/BackupAddons.svelte';
    import {warnEdge, warnFor} from '../lib/systemmenu.svelte';
    import BootBar from '../lib/BootBar.svelte';
    import {EASE_MS, type BootEntry} from '../lib/bootbar';
    import {beginBoot, removeEntry, watchBoot} from '../lib/bootwatch';
    import {boxUptime} from '../lib/power';
    import {onMount} from 'svelte';
    import {scrollToAnchor} from '../lib/anchor';

    // task 296: backup_key / system_key - which side has a BidCos security key of its own; key_index the backup's
    interface Check { ok: boolean; output: string; backup_version?: string; running_version?: string; needs_key: boolean; has_rega: boolean; backup_key?: boolean; system_key?: boolean; key_index?: number }
    interface RestoreModule { hardware: string; serial: string; sgtin?: string; version?: string }
    interface RestoreTarget { paired: Record<string, {devices: number; known: boolean; error?: string}>; devices: number; unknown?: string[]; user_key: boolean; importable: boolean; hmip_module?: RestoreModule; bidcos_module?: RestoreModule }
    // task 275: module_changed - the backup's HmIP identity belongs to another module than this system's;
    // task 278: non_default_key - the backup's BidCos key store is not the factory key and comes along
    // openccu-lite B-289: hmip_firmware - the move onto this system's module is refused (its firmware cannot take the key)
    // occulited task 142: kind, and what the import of an (Open)CCU backup offers besides the devices -
    // each group with what it is in this backup, this system's value, the default and why it cannot be chosen
    interface ImportGroup { id: string; value?: string; current?: string; default: boolean; unavailable?: string; addons?: BackupAddon[]; sources?: string[] }
    interface DevicesView { backup: RadioBackup; target: RestoreTarget; module_changed?: boolean; non_default_key?: boolean; hmip_firmware?: FirmwareRefusal; kind?: 'openccu-lite' | 'ccu'; groups?: ImportGroup[]; left_behind?: string[] }
    interface GroupResult { ok: boolean; error?: string; value?: string; rules?: number; addons?: {id: string; name: string; imported: boolean; enabled: boolean; disabled?: string; error?: string}[] }
    interface CheckAnswer { file: string; check: Check | null; encryption?: Encryption; backup?: BackupReport | null; system?: ThisSystem }
    let encryption = $state<EncryptionView | null>(null);
    const encrypted = $derived(!!encryption?.enabled);
    let encInfo = $state<Encryption | null>(null);
    let recoveryKey = $state('');
    // the plain download although encryption is on (D-64): the password every time, through a
    // confirmed ticket (task 154) that the navigation carries in ?confirm=
    const BACKUP_PLAIN_PATH = '/api/system/v1/backup/unencrypted';
    // B-95: an account of the identity provider confirms there - the page leaves with the prepared
    // backup's id kept here and resumes with the ticket the provider's return brings
    const PLAIN_PENDING = 'ol.backup.plain-pending';
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
            // B-90: the file first, then the password - the confirmed ticket lives a minute
            const job = await prepare();
            const confirm = await confirmTicket(BACKUP_PLAIN_PATH, {
                title: t('Download unencrypted'),
                message: t('An unencrypted backup holds every key of this system in plain and is confirmed every time.'),
                provider: t('An unencrypted backup asks who you are every time: you sign in at the identity provider once more and come back here.'),
                impossible: t('This account has no password and no identity provider is configured, so it cannot confirm the download.'),
            }, () => {
                try { sessionStorage.setItem(PLAIN_PENDING, job.id); } catch { /* no storage: the return says so */ }
            });
            if (confirm === null) return;
            await followPlain(job.id, confirm);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            downloading = false;
            preparingSince = null;
        }
    }
    async function followPlain(id: string, confirm: string) {
        const ticket = await ticketFor(backupUrl);
        location.assign(withTicket(preparedUrl(id, {encrypted: 'false', confirm}), ticket));
    }
    /** B-95: back from the identity provider - the prepared backup is downloaded with the ticket. */
    async function resumePlain() {
        const back = returned();
        let id = '';
        try {
            id = sessionStorage.getItem(PLAIN_PENDING) ?? '';
            sessionStorage.removeItem(PLAIN_PENDING);
        } catch {
            /* no storage */
        }
        if (!back) return;
        if ('refused' in back) {
            error = t('The confirmation at the identity provider was refused: {reason}', {reason: back.refused});
            return;
        }
        if (!id) {
            error = t('The prepared backup is no longer there. Start the download again.');
            return;
        }
        downloading = true;
        preparingSince = Date.now();
        try {
            const job = await awaitBackup(id);
            preparingSince = null;
            await followPlain(job.id, back.ticket);
        } catch (e) {
            // a 404 (it waits 30 minutes), or downloaded, or failed: nothing to resume
            error = e instanceof ApiError && e.status === 404 ? t('The prepared backup is no longer there. Start the download again.') : (e as Error).message;
        } finally {
            downloading = false;
            preparingSince = null;
        }
    }
    async function decrypt() {
        if (!uploaded || !recoveryKey.trim()) return;
        busy = 'decrypt';
        error = '';
        try {
            const r = await api.post<CheckAnswer>('/api/system/v1/restore/decrypt', {file: uploaded, recovery_key: recoveryKey});
            taken(r);
            recoveryKey = '';
        } catch (e) {
            error = recoveryKeyError(e, t);
            // B-194: a damaged upload is gone with the refusal - back to the file picker
            if (e instanceof ApiError && e.code === 'corrupt') {
                uploaded = '';
                encInfo = null;
                check = null;
                report = null;
            }
        } finally {
            busy = '';
        }
    }
    const when = (iso?: string) => (iso ? dateOnly(iso) : '');

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
    // occulited task 142: one action per backup, so one check and one passphrase - the import's for an
    // (Open)CCU backup, the restore's for an openccu-lite one
    let keyVerdict = $state<Verdict | null>(null);
    let keyPass = $state('');
    // what the checked file is (the check's report) and what this system is
    let report = $state<BackupReport | null>(null);
    let thisSystem = $state<ThisSystem | null>(null);
    // the import's groups: which are ticked
    let chosen = $state<Record<string, boolean>>({});
    /** A check's or a decryption's answer: the file, the verdict, what it is. */
    function taken(r: CheckAnswer) {
        uploaded = r.file;
        check = r.check;
        encInfo = r.encryption ?? null;
        report = r.backup ?? null;
        thisSystem = r.system ?? null;
        void loadDevices();
    }
    let appliedKeyNote = $state('');
    let namesMsg = $state('');
    // task 251: the paired devices of the checked backup, loaded after a check for an administrator
    let devices = $state<DevicesView | null>(null);
    let devicesMsg = $state('');
    const kind = $derived(report?.kind ?? devices?.kind ?? null);
    // task 278: this system has a key store of its own - the backup's replaces it, on the user's word
    let replaceKey = $state(false);
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
        keyVerdict = null;
        keyPass = '';
        key = '';
        chosen = {};
        if (!uploaded || !check || auth.role !== 'admin') return;
        try {
            const v = await api.get<DevicesView>(`/api/system/v1/restore/devices?file=${encodeURIComponent(uploaded)}`);
            // an answer without the two halves (an older daemon, a stub) shows nothing rather than a broken block
            devices = v && v.backup && v.target ? v : null;
            chosen = Object.fromEntries((devices?.groups ?? []).map((g) => [g.id, g.default && !g.unavailable]));
        } catch (e) {
            devicesMsg = (e as Error).message;
        }
    }
    // occulited task 142: one line per group - what it is in this backup, for the checkbox and the question
    const addonNames = (g: ImportGroup, here: boolean) => (g.addons ?? []).filter((a) => !!a.here === here).map((a) => a.name + (a.program ? '' : ' (' + t('settings only') + ')')).join(', ');
    const sourcesText = (src: string[]) => src.map((x) => (x === 'local networks' ? t('the local networks') : x === '0/0' ? t('every address (0/0)') : x)).join(', ');
    function groupLine(g: ImportGroup): string {
        switch (g.id) {
            case 'addons': return t('The addons with their settings: {names}', {names: addonNames(g, false) || addonNames(g, true)});
            case 'hb_rf_eth': return t('The HB-RF-ETH\'s address: {value}', {value: g.value ?? ''});
            case 'hostname': return t('The host name: {value}', {value: g.value ?? ''});
            case 'loghost': return t('The syslog server (LOGHOST): {value}', {value: g.value ?? ''});
            case 'classic_rpc': return t('Classic RPC as on the old system: the XML-RPC ports, plain and TLS, open without a login from {sources}', {sources: sourcesText(g.sources ?? [])});
        }
        return '';
    }
    /** What a group's checkbox says below its line: this system's value, why it cannot be chosen. */
    function groupNote(g: ImportGroup): string {
        if (g.unavailable === 'invalid') return t('This system cannot take this value; set it by hand afterwards.');
        if (g.unavailable === 'auth') return t('The old system asked for a login on these ports (its ReGa accounts), which cannot come along. Switch classic RPC on with a user name and password on Remote access afterwards.');
        if (g.unavailable === 'on') return t('Classic RPC is on here already; its settings stay as they are.');
        if (g.unavailable === 'here') return t('Every addon of the backup is installed here already; the installed ones stay as they are.');
        switch (g.id) {
            case 'addons': {
                const here = addonNames(g, false) ? addonNames(g, true) : '';
                return t('An addon that needs the ReGa, or whose programs are for another processor, comes disabled; it can be uninstalled or installed anew on the Addons page.') + (here ? ' ' + t('Installed here already and left as they are: {names}.', {names: here}) : '');
            }
            case 'hb_rf_eth': return g.current ? t('This system has {current}; the backup\'s replaces it. The board serves one system: the old one must be off.', {current: g.current}) : t('The board serves one system: the old one must be off.');
            case 'hostname': return t('This system is called {current}. Two systems with one name confuse the router: take the name only when the old system goes.', {current: g.current ?? ''});
            case 'loghost': return g.current ? t('This system sends its log to {current}; the backup\'s replaces it.', {current: g.current}) : t('The log is then sent to this server as well.');
            case 'classic_rpc': return t('Programs that talk to the old system on ports 2001, 2010, 9292 or 2000 need this. Nothing on these ports asks who is calling: anyone the firewall lets in controls every device. Left out, classic RPC stays off and can be switched on later on Remote access.');
        }
        return '';
    }
    function groupResultLines(res: Record<string, GroupResult>): string[] {
        const out: string[] = [];
        for (const [id, r] of Object.entries(res)) {
            if (!r.ok) out.push(t('Not imported ({group}): {error}', {group: id, error: r.error ?? ''}));
            if (id === 'addons') {
                for (const a of r.addons ?? []) {
                    if (!a.imported) out.push(t('Addon {name}: not imported ({error}).', {name: a.name, error: a.error ?? ''}));
                    else if (a.enabled) out.push(t('Addon {name}: imported.', {name: a.name}));
                    else out.push(t('Addon {name}: imported, disabled ({why}).', {name: a.name, why: a.disabled === 'rega' ? t('it needs the ReGa') : a.disabled === 'binary' ? t('its programs are for another processor') : a.disabled === 'program' ? t('the backup holds its settings, not its program: install it again') : t('it was switched off on the old system')}));
                }
            } else if (r.ok && id === 'classic_rpc') {
                out.push(t('Classic RPC is switched on: {n} firewall rules.', {n: r.rules ?? 0}));
            } else if (r.ok) {
                out.push(t('Taken from the backup ({group}): {value}', {group: id, value: r.value ?? ''}));
            }
        }
        return out;
    }
    // what an import leaves behind, by the report's ids
    function leftLine(id: string): string {
        switch (id) {
            case 'rega': return t('programs, system variables and scripts of the ReGa (this system has no ReGa)');
            case 'users': return t('the users of the old WebUI and their passwords');
            case 'network': return t('the network settings other than the host name (address, DNS)');
            case 'firewall': return t('the firewall settings other than classic RPC');
            case 'certificate': return t('the web certificate');
            case 'ssh': return t('SSH keys and the root password');
            case 'time': return t('time zone, time servers and location');
            case 'loglevels': return t('the log levels');
            case 'cron': return t('scheduled jobs (crontab) and monit files');
            case 'addon_extras': return t('what an addon keeps outside its own directories, and addon directories without a start script');
            case 'neoserver': return t('mediola\'s NEO Server');
            case 'other': return t('everything else under /usr/local (own scripts, device firmware files)');
        }
        return id;
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
        // occulited task 142: what was ticked comes along, said once more before it happens
        const groups = (devices.groups ?? []).filter((g) => chosen[g.id] && !g.unavailable);
        const along = groups.map((g) => groupLine(g)).filter(Boolean);
        if (along.length) lines.push(t('Also taken from the backup:') + '\n' + along.map((l) => '- ' + l).join('\n'));
        if (groups.some((g) => g.id === 'classic_rpc')) lines.push(t('Classic RPC is switched on without a login, as on the old system: anyone the firewall lets in controls every device.'));
        // task 296: without the confirmed passphrase the warning comes first - the import still runs on the word
        const unconfirmed = !!devices.non_default_key && !keyConfirmed(keyVerdict, true);
        if (unconfirmed) lines.unshift(...keyWarning('import', keyVerdict));
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
            const r = await api.post<{ok: boolean; rebooting: boolean; message?: string; names?: {ok: boolean; objects: number; rooms: number; functions: number; error?: string}; groups?: Record<string, GroupResult>}>('/api/system/v1/restore/import-devices', {file: uploaded, replace_key: replaceKey, key: keyVerdict?.backup === 'skipped' ? '' : keyPass, confirm: true, groups: groups.map((g) => g.id)});
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
                    ...groupResultLines(r.groups ?? {}),
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
        report = null;
        recoveryKey = '';
        applied = '';
        try {
            const data = await api.post<CheckAnswer>('/api/system/v1/restore/check', {target, name});
            taken(data);
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
    // B-90: the backup is prepared on the system first (lib/backupjob.ts); since when, for the line
    let preparingSince = $state<number | null>(null);
    async function prepare() {
        preparingSince = Date.now();
        try {
            return await prepareBackup();
        } finally {
            preparingSince = null;
        }
    }
    async function downloadBackup(e: Event) {
        e.preventDefault();
        if (downloading) return;
        downloading = true;
        error = '';
        try {
            const job = await prepare();
            await download(preparedUrl(job.id));
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
        report = null;
        recoveryKey = '';
        try {
            // occulited B-141: too large or no room is said before the upload runs, not after
            // gigabytes went over the network; when the room cannot be read, the server decides
            const space = await api.get<RestoreSpace>('/api/system/v1/restore/space').catch(() => null);
            const refused = space ? uploadRefusal(file.size, space, i18n.language, t) : '';
            if (refused) throw new Error(refused);
            const fd = new FormData();
            fd.append('file', file, file.name);
            const res = await fetch('/api/system/v1/restore/check', {method: 'POST', headers: REQUEST_HEADER, body: fd});
            // lighttpd's own refusal (a 413 above its ceiling) is a page, not JSON
            const data = await res.json().catch(() => ({message: t('The system refused the upload ({status}).', {status: `${res.status} ${res.statusText}`.trim()})}));
            if (!res.ok) throw new Error(data.message ?? res.statusText);
            taken(data as CheckAnswer);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function apply() {
        if (!check || !uploaded) return;
        if (await firmwareRefused()) return;
        // occulited task 142: the restore replaces the accounts as well - said before it happens
        const question = [
            t('Restore this backup and reboot? /usr/local is replaced: pairings, keys, the HmIP identity, addons and their settings.'),
            report?.accounts
                ? t('This system\'s users and API tokens are replaced by the backup\'s ({u} users, {k} tokens): after the reboot you sign in with an account of the backup.', {u: report.users, k: report.tokens})
                : t('This system\'s users and API tokens are replaced by the backup\'s: after the reboot you sign in with an account of the backup.'),
        ].join('\n\n');
        // task 296: the backup's own BidCos key without a confirmed passphrase - the warning, then the
        // restore on the user's word (force: the script would refuse the key it cannot check)
        const unconfirmed = !!check.backup_key && !keyConfirmed(keyVerdict, true);
        // task 301: the backup's HmIP identity belongs to another module - the restore moves the network
        // onto this one (the adapter exchange), said and confirmed in red before the restore
        const moveLines = devices?.module_changed ? hmipMoveLines(devices.backup, moduleName(devices.target.hmip_module)) : [];
        if (unconfirmed) {
            if (!(await ask({title: t('Restore without the confirmed passphrase?'), message: [...keyWarning('restore', keyVerdict), ...moveLines, question].join('\n\n'), confirm: t('Restore anyway'), danger: true}))) return;
        } else if (moveLines.length) {
            if (!(await ask({title: t('Restore onto another radio module?'), message: [...moveLines, question].join('\n\n'), confirm: t('Restore and reboot'), danger: true}))) return;
        } else if (!(await ask({title: t('Restore this backup?'), message: question, confirm: t('Restore and reboot'), danger: true}))) return;
        // the backup's passphrase confirmed but this system has another key: the restore replaces it (the
        // panel said so), and the script, which wants one key for both sides, needs the force for that
        const forced = force || unconfirmed || (!!check.backup_key && keyVerdict?.system === 'mismatch');
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
            const r = await api.post<{output: string; rebooting: boolean; message?: string}>('/api/system/v1/restore/apply', {file: uploaded, key: check.backup_key ? (keyVerdict?.backup === 'skipped' ? '' : keyPass) : key, force: forced, confirm: true});
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
    onMount(() => {
        void resumePlain();
        scrollToAnchor('restore');
    });
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
    <!-- occulited task 140: the backup-age warning's edge - the panel that makes a backup and shows the last of each way -->
    <div class="ol-panel {warnEdge(['backup-age'])}" data-section="create" {...warnFor(['backup-age'])}>
    {#if encrypted}
        <div class="ol-actions ol-dl">
            <button type="button" class="hmm-button primary" onclick={downloadBackup} disabled={downloading} data-action="download-backup">{t('Download encrypted backup')}</button>
            <button type="button" class="hmm-button" onclick={downloadPlain} disabled={downloading} data-action="download-plain">{t('Download unencrypted (for OpenCCU or a CCU3)…')}</button>
        </div>
        <BackupWait since={preparingSince} />
    {:else}
        <!-- occulited task 88: a button in the panel's flow, like the targets' "Back up now"; that
             backups are not encrypted is the Encryption panel's badge and the Status warning -->
        <div class="ol-actions ol-dl">
            <button type="button" class="hmm-button" onclick={downloadBackup} disabled={downloading} data-action="download-backup">{t('Download backup')}</button>
        </div>
        <BackupWait since={preparingSince} />
    {/if}
    <!-- occulited task 139: the last backup of each way - automatic, Back up now, in the browser, by a program -->
    <LastBackups />
    </div>

    <!-- openccu-lite task 399: the addons that snapshot their data for the backup, each with its switch -->
    <BackupAddons />

    <!-- task 86: the targets - the USB directory, NFS and SMB shares, SSH servers -->
    <!-- occulited task 12: the nightly backup's target and its deliveries -->
    <BackupTargets onrestore={(target, name) => void restoreFromTarget(target, name)} />

    <h2 id="restore">{t('Restore or import a backup')}<Help>{t('Two kinds of backup, two ways. A backup of openccu-lite is restored: everything under /usr/local is replaced and this system becomes the one the backup was made of, with its users.')} {t('A backup of OpenCCU, RaspberryMatic or a CCU3 is imported: its paired devices with their keys, its names, rooms and functions, and what you choose of the rest come onto this system; its programs and system variables cannot be used here. After the check this page says which kind the file is and what it holds.')}</Help></h2>
    <div class="ol-panel" data-section="restore">
    <div class="ol-toolbar">
        <input class="hmm-input" type="file" accept=".sbk,.age" onchange={(e) => { file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null; check = null; encInfo = null; report = null; applied = ''; restoreFrom = ''; }} />
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
        <!-- occulited task 142 (D-125): the file explained before anything is done with it, then the one
             action that fits its kind. data-restore="devices" wraps both: the device lines are in the
             description, the import's notices below it -->
        <div data-restore="devices">
        <BackupFile {report} system={thisSystem} ok={check.ok} output={check.output} encryption={encInfo} />
        {#if kind === 'ccu' && devices}
            {@const b = devices.backup}
            {@const tg = devices.target}
            <div class="ol-subblock ol-devices" data-restore="import">
                <h3>{t('Import into this system')}<Help>{t('The pairings of the CCU or OpenCCU the backup came from: rfd\'s and hs485d\'s device files with the BidCos address and the security key, hmipserver\'s devices with the HmIP identity (and local key mode, if it was on), the LAN gateways. They come across together, or not at all, and only onto a system that has no device paired yet: the import takes over that system\'s radio identity, so devices paired here would be orphaned.')} {t('One action takes all three: the names, rooms and functions of the backup\'s ReGa database are imported first, then the devices with their keys, then the system reboots.')}</Help></h3>
                {#if radioEmpty(b)}
                    <p class="ol-muted" data-import="nothing">{t('This backup holds no paired device and no radio identity: there is nothing to import.')}</p>
                {:else}
                    <p data-import="always">{t('Always taken: the paired devices of all three radios with the radio identity and the keys, and the names, rooms and functions.')}</p>
                    {#if devices.groups?.length}
                        <p class="ol-grouphead">{t('Also take from the backup:')}</p>
                        <div class="ol-groups" data-import="groups">
                            {#each devices.groups as g (g.id)}
                                <label class="ol-group" data-group={g.id} data-unavailable={g.unavailable ?? null}>
                                    <input type="checkbox" checked={!!chosen[g.id]} disabled={!!g.unavailable || busy !== ''} onchange={(e) => (chosen[g.id] = (e.currentTarget as HTMLInputElement).checked)} data-input={`group-${g.id}`} />
                                    <span class="ol-grouptext"><span class="ol-groupline">{groupLine(g)}</span><span class="ol-muted ol-groupnote">{groupNote(g)}</span></span>
                                </label>
                            {/each}
                        </div>
                    {/if}
                    {#if devices.left_behind?.length}
                        <details class="ol-left" data-import="left-behind">
                            <summary>{t('What stays behind on the old system')}</summary>
                            <ul>{#each devices.left_behind as id (id)}<li data-left={id}>{leftLine(id)}</li>{/each}</ul>
                        </details>
                    {/if}
                    {#if tg.unknown?.length}
                        <p class="ol-warn" data-devices-target="unknown">{t('{list} did not answer: whether devices are paired here is not known yet. Try again in a moment.', {list: tg.unknown.join(', ')})}</p>
                    {:else if tg.devices > 0}
                        <!-- D-125: the reason, and the way there - nothing is deleted automatically -->
                        <div class="ol-notice warn" data-devices-target="paired">
                            <p>{t('This system has {n} devices paired ({list}): the import is refused, it would take over another system\'s radio identity and orphan them.', {n: tg.devices, list: Object.entries(tg.paired).filter(([, p]) => p.devices > 0).map(([name, p]) => `${name}: ${p.devices}`).join(', ')})}</p>
                            <p>{t('The way to import this backup is a system without devices: a factory reset empties this one. It deletes everything on it, so make a backup first if anything here is worth keeping.')} <a href="/system/backup#factory-reset" use:link onclick={() => document.getElementById('factory-reset')?.scrollIntoView({block: 'start'})} data-action="to-factory-reset">{t('Factory reset')}</a></p>
                            {#if check.has_rega}
                                <p>{t('The names alone can be taken without it:')} <button class="hmm-button" onclick={importNames} disabled={busy !== ''} data-action="import-names">{t('Import the names from this backup')}</button></p>
                                {#if namesMsg}<p class="ol-muted" data-import="names-result">{namesMsg}</p>{/if}
                            {/if}
                        </div>
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
                        {@const scp = {first: dateOnly(sc.first_connect), offset: String(sc.offset), calc: String(sc.calc), wraps: dateOnly(sc.wraps_at)}}
                        <div class={sc.verdict === 'near' && !sc.behind ? 'ol-notice' : 'ol-notice warn'} data-notice="security-counter" data-verdict={sc.behind ? 'behind' : sc.verdict}>
                            {#if sc.behind}{t('HmIP security counter: the clock of this system is before the access point\'s first connection ({first}). Set the time before the import, or hmipserver computes the counter from the offset alone.', scp)}
                            {:else if sc.verdict === 'near'}{t('HmIP security counter: the access point was first connected on {first}; with its offset of {offset} the counter computed at the first start here is {calc}, past 2^31. It reaches 2^32, where hmipserver\'s protection against a lower value ends, at about {wraps}. Keep the clock of this system synchronised.', scp)}
                            {:else}{t('HmIP security counter: the access point was first connected on {first}; with its offset of {offset} the counter computed at the first start here is {calc}, past 2^32. hmipserver\'s protection against a lower value is gone on this access point: a start without a synchronised clock can lock every HmIP device out, and the counter is set below what the devices have seen whenever it passes 2^32 again. This system holds hmipserver back while the clock is not trusted; if the devices stop answering after a start, power-cycle them.', scp)}{/if}
                        </div>
                    {/if}
                    <!-- openccu-lite task 278 (option B) and 296: the backup's BidCos key store comes along as it is;
                         its passphrase is asked as a check - match, mismatch, skip - and never stops the import.
                         occulited task 142: the one check of this file -->
                    {#if devices.non_default_key}
                        {#if encInfo?.encrypted}
                            <p class="ol-muted">{t('The security key protects the radio link to BidCos devices, and a restore on another system asks for it; the recovery key encrypts the backup itself, so nobody can read it without that key.')}</p>
                        {/if}
                        <div data-notice="bidcos-key">
                            <BidCosKeyCheck file={uploaded} keyIndex={b.key_index} what="import" bind:verdict={keyVerdict} bind:passphrase={keyPass} />
                        </div>
                    {/if}
                    {#if tg.user_key}
                        <label class="ol-replace-key"><input type="checkbox" bind:checked={replaceKey} data-input="replace-key" /> {t('Replace this system\'s own BidCos security key with the backup\'s (no device is paired here that could be orphaned)')}</label>
                    {/if}
                    <div class="ol-actions" style="margin-top:8px">
                        <button class="hmm-button danger" onclick={importDevices} disabled={busy !== '' || !check.ok || !tg.importable || (tg.user_key && !replaceKey) || !!devices.hmip_firmware} data-action="import-devices">{t('Import and reboot')}</button>
                    </div>
                {/if}
                {#if devicesMsg}<div class="ol-notice error" data-notice="devices">{devicesMsg}</div>{/if}
            </div>
        {:else if kind === 'openccu-lite'}
            <div class="ol-subblock" data-restore="restore">
                <h3>{t('Restore onto this system')}</h3>
                <p data-restore="replaces">{t('Everything under /usr/local is replaced by the backup: the paired devices and keys, the names, the addons with their settings, the network and every other setting, and the users and API tokens. What this system has now is gone; the system reboots.')}</p>
                {#if devices?.module_changed && devices.hmip_firmware}
                    <div class="ol-notice error" data-notice="hmip-firmware">
                        <strong>{t('HmIP-RF cannot move to this module')}</strong>
                        {#each firmwareRefusalLines(devices.hmip_firmware, 'backup', t) as line, i (i)}<p>{line}</p>{/each}
                    </div>
                {/if}
                {#if check.needs_key}
                    {#if encInfo?.encrypted}
                        <p class="ol-muted">{t('The security key protects the radio link to BidCos devices, and a restore on another system asks for it; the recovery key encrypts the backup itself, so nobody can read it without that key.')}</p>
                    {/if}
                    {#if check.backup_key}
                        <!-- task 296: the backup's own key - its passphrase as a check, never a gate -->
                        <div data-restore="key-check"><BidCosKeyCheck file={uploaded} keyIndex={check.key_index ?? 0} what="restore" systemKey={!!check.system_key} bind:verdict={keyVerdict} bind:passphrase={keyPass} /></div>
                    {:else}
                        <!-- only this system has a key of its own: the firmware asks for it, as the CCU's restore does -->
                        <label>{t('Security key')} <input class="hmm-input" type="password" bind:value={key} autocomplete="off" data-input="restore-key" /></label>
                        <label><input type="checkbox" bind:checked={force} data-input="restore-force" /> {t('Force (ignore a key mismatch)')}</label>
                    {/if}
                {/if}
                {#if check.ok}
                    <div class="ol-actions" style="margin-top:8px"><button class="hmm-button danger" onclick={apply} disabled={busy !== '' || (check.needs_key && !check.backup_key && !key && !force) || !!devices?.hmip_firmware} data-action="restore-apply">{t('Restore and reboot')}</button></div>
                {/if}
                {#if devicesMsg}<div class="ol-notice error" data-notice="devices">{devicesMsg}</div>{/if}
            </div>
        {:else if devicesMsg}
            <div class="ol-notice error" data-notice="devices">{devicesMsg}</div>
        {/if}
        </div>
    {/if}
    {#if applied || restoring}
        <div class="ol-notice ol-restorenotice" role="status"><strong>{kind === 'ccu' ? t('Importing — the system reboots now.') : t('Restoring — the system reboots now.')}</strong>{#if appliedKeyNote}<p class="ol-warn" data-notice="restore-key-warning">{appliedKeyNote}</p>{/if}{#if restoring}<BootBar entry={restoring} />{/if}{#if applied}<pre class="ol-log">{applied}</pre>{/if}</div>
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
    /* occulited task 142: the import's groups - a checkbox, its line and a note below it */
    .ol-grouphead { margin: 10px 0 4px; font-weight: 600; }
    .ol-groups { display: grid; gap: 8px; max-width: 820px; }
    .ol-group { display: flex; gap: 8px; align-items: flex-start; margin-top: 0; }
    .ol-group input { margin-top: 3px; flex: none; }
    .ol-grouptext { display: block; min-width: 0; overflow-wrap: anywhere; }
    .ol-groupline, .ol-groupnote { display: block; }
    .ol-group[data-unavailable] .ol-groupline { opacity: 0.7; }
    .ol-left { margin-top: 10px; max-width: 820px; }
    .ol-left summary { cursor: pointer; }
    .ol-left ul { margin: 6px 0 0; padding-left: 1.2em; }
</style>
