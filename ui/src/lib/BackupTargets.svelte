<script lang="ts" module>
    // openccu-lite task 86: the Backup page's targets - the USB directory, NFS and SMB shares
    // (mounted on demand by the system) and SSH servers (an SFTP upload). One card per target
    // with its state, the last backup, the test, Back up now, the mount, the backups there and
    // Restore this; the SSH key and the server's key; the nightly switch on top.
    export interface TargetState { state: string; detail?: string; checked_at?: string; failures: number; next_retry_at?: string; mounted: boolean; source?: string; options?: string; free_bytes?: number; total_bytes?: number; unsupported?: string }
    export interface LastBackup { at: string; ok: boolean; state: string; step?: string; error?: string; name?: string; size?: number; duration_ms?: number; encrypted: boolean; last_ok?: string; removed?: string[] }
    export interface Target {
        id: string; name: string; kind: 'directory' | 'share' | 'nfs' | 'cifs' | 'sftp'; enabled: boolean; max_backups: number; subdir: string; encrypt: boolean; append_only: boolean; created?: string;
        directory?: {path: string; location?: string};
        share?: {id: string; folder: string};
        nfs?: {server: string; export: string; version: string};
        cifs?: {server: string; share: string; version: string; seal: boolean; user: string; domain?: string; has_password: boolean};
        sftp?: {host: string; port: number; user: string; path: string; public_key?: string; host_key?: {type: string; fingerprint: string}};
        state: TargetState; last_backup?: LastBackup;
    }
    export interface TargetsView { nightly: {enabled: boolean; time: string}; container: string; kinds: Record<string, string>; encryption: boolean; hostname: string; needed_bytes: number; targets: Target[] }
    export interface TargetBackup { name: string; size: number; time: string; encrypted: boolean; recovery_fingerprint?: string; known?: string }
</script>

<script lang="ts">
    import {link} from './router.svelte';
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import {warnEdge, warnFor} from './systemmenu.svelte';
    // the Status page's warnings that lead to this section (occulited task 12)
    const TARGET_WARNINGS = ['backup-delivery', 'backup-target', 'backup-userfs'];
    import Disclosure from './Disclosure.svelte';
    import LocationPicker from './LocationPicker.svelte';
    import {folderError, formatLocation, parseLocation} from './locations';

    let {onrestore}: {onrestore?: (target: string, name: string) => void} = $props();

    let view = $state<TargetsView | null>(null);
    let busy = $state('');
    let msg = $state<Record<string, string>>({});
    let err = $state('');
    let open = $state<Record<string, TargetBackup[] | null>>({});
    // task 268: a target's Backups here becomes the panel of its backups, and its Edit is gone
    // while its form is open (the panel's Close / the form's Cancel bring them back)
    let backupsButtons = $state<Record<string, HTMLButtonElement>>({});
    let hostkeys = $state<Record<string, {type: string; fingerprint: string; trusted: boolean; changed: boolean; trusted_fingerprint?: string}>>({});
    let tests = $state<Record<string, {ok: boolean; state: string; step: string; error?: string; free_bytes: number; needed_bytes: number; write_mbps?: number}>>({});

    // the directory target's own checks (upstream's markers): a path that is not there, or one on
    // the system itself
    interface Schedule { path: string; path_exists: boolean; on_userfs?: boolean; real_path?: string; max_backups: number }
    let sched = $state<Schedule | null>(null);
    async function load() {
        try {
            view = await api.get<TargetsView>('/api/system/v1/backup/targets');
            err = '';
        } catch (e) {
            err = (e as Error).message;
            return;
        }
        if (view.targets.some((x) => x.kind === 'directory')) {
            try { sched = await api.get<Schedule>('/api/system/v1/backup/schedule'); } catch { sched = null; }
        } else {
            sched = null;
        }
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    // a run in progress: look again until it is done
    $effect(() => {
        const running = view?.targets.some((x) => x.state.state === 'running');
        clearTimeout(timer);
        if (running) timer = setTimeout(() => void load(), 3000);
        return () => clearTimeout(timer);
    });
    onMount(() => {
        void load();
    });

    const KIND: Record<Target['kind'], () => string> = {directory: () => t('USB / directory'), share: () => t('Network share'), nfs: () => t('NFS share'), cifs: () => t('SMB share'), sftp: () => t('SSH server (SFTP)')};
    const STATE: Record<string, () => string> = {
        'not-configured': () => t('not set up'),
        unsupported: () => t('not available here'),
        idle: () => t('ready'),
        connecting: () => t('connecting'),
        writable: () => t('writable'),
        'read-only': () => t('not writable'),
        full: () => t('full'),
        unreachable: () => t('unreachable'),
        'auth-failed': () => t('login refused'),
        'host-key-unknown': () => t("server's key not confirmed"),
        'host-key-changed': () => t("server's key changed"),
        'no-sftp': () => t('no SFTP on the server'),
        stale: () => t('not answering'),
        'no-medium': () => t('no USB stick'),
        'update-room': () => t('skipped: room for an update'),
        running: () => t('backing up…'),
        error: () => t('error'),
    };
    const stateText = (s: string) => (STATE[s] ?? (() => s))();
    const stateClass = (s: string) => (s === 'writable' || s === 'idle' ? 'good' : s === 'running' || s === 'connecting' || s === 'unsupported' || s === 'host-key-unknown' ? '' : 'bad');
    const failed = (s: string) => stateClass(s) === 'bad';
    const size = (n?: number) => (n === undefined ? '' : n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : `${Math.max(1, Math.round(n / 1e6))} MB`);
    const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : '');
    const where = (x: Target) =>
        x.kind === 'directory' ? (x.directory?.location ? locationText(x.directory.location, x.directory.path) : x.directory?.path ?? '') :
        x.kind === 'share' ? `${t('share {name}', {name: x.share?.id ?? ''})}${x.share?.folder ? ' · ' + x.share.folder : ''}` :
        x.kind === 'nfs' ? `${x.nfs?.server}:${x.nfs?.export}${x.subdir ? ' · ' + x.subdir : ''}` :
        x.kind === 'cifs' ? `//${x.cifs?.server}/${x.cifs?.share}${x.subdir ? ' · ' + x.subdir : ''}` :
        `${x.sftp?.user}@${x.sftp?.host}${x.sftp?.port && x.sftp.port !== 22 ? ':' + x.sftp.port : ''}:${x.sftp?.path || '~'}${x.subdir ? '/' + x.subdir : ''}`;
    const unsupportedText = (why?: string) => (why === 'container' ? t('This system is a container. Mount the share on the host and pass it into the container (Docker: a volume; Proxmox: a mount point), then choose it here as a directory target.') : t('The program for this kind of share is not part of this system image yet.'));

    // a directory chosen as a location: the stick by its label or the system storage, and where it is now
    function locationText(loc: string, path: string): string {
        const p = parseLocation(loc);
        if (!p) return path;
        if (p.kind === 'userfs') return `${t('System storage')} · ${p.folder}`;
        const now = path.startsWith('/media/.usb-not-plugged-in') ? t('not plugged in') : path;
        return `${t('USB stick {name}', {name: p.name})} · ${p.folder} · ${now}`;
    }

    function say(id: string, text: string) {
        msg = {...msg, [id]: text};
    }

    async function nightly(on: boolean) {
        busy = 'nightly';
        try {
            await api.put('/api/system/v1/backup/nightly', {enabled: on});
            await load();
        } catch (e) {
            err = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function runAll() {
        busy = 'run';
        err = '';
        try {
            await api.post('/api/system/v1/backup/run', {});
            await load();
        } catch (e) {
            err = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function act(x: Target, what: 'test' | 'run' | 'mount' | 'unmount') {
        busy = x.id + what;
        say(x.id, '');
        try {
            if (what === 'test') {
                const r = await api.post<(typeof tests)[string]>(`/api/system/v1/backup/targets/${x.id}/test`, {});
                tests = {...tests, [x.id]: r};
            } else {
                await api.post(`/api/system/v1/backup/targets/${x.id}/${what}`, {});
                if (what === 'run') say(x.id, t('The backup is running; this card shows when it is done.'));
            }
            await load();
        } catch (e) {
            say(x.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }

    async function remove(x: Target) {
        const ok = await ask({title: t('Remove target'), message: t('Remove the target "{name}"? The backups already there stay where they are; the system only stops writing to it.', {name: x.name}), confirm: t('Remove'), danger: true});
        if (!ok) return;
        busy = x.id + 'remove';
        try {
            await api.del(`/api/system/v1/backup/targets/${x.id}`);
            await load();
        } catch (e) {
            say(x.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }

    async function toggleBackups(x: Target) {
        if (open[x.id] !== undefined) {
            const {[x.id]: _, ...rest} = open;
            open = rest;
            return;
        }
        open = {...open, [x.id]: null};
        try {
            const r = await api.get<{backups: TargetBackup[]}>(`/api/system/v1/backup/targets/${x.id}/backups`);
            open = {...open, [x.id]: r.backups};
        } catch (e) {
            const {[x.id]: _, ...rest} = open;
            open = rest;
            say(x.id, (e as Error).message);
        }
    }

    async function restore(x: Target, b: TargetBackup) {
        if (!(await ask({title: t('Restore this'), message: t('Check {name} from "{target}" for a restore? It is copied onto this system first; nothing is applied before you confirm below.', {name: b.name, target: x.name}), confirm: t('Check')}))) return;
        onrestore?.(x.id, b.name);
    }

    // the SSH key and the server's key
    async function newKey(x: Target) {
        if (x.sftp?.public_key && !(await ask({title: t('New key'), message: t('Make a new key for this target? The server then has to get the new line in authorized_keys; the old key stops working here.'), confirm: t('New key'), danger: true}))) return;
        busy = x.id + 'key';
        try {
            await api.post(`/api/system/v1/backup/targets/${x.id}/keypair`, {});
            await load();
        } catch (e) {
            say(x.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }
    const keyLine = (x: Target) => (x.sftp?.public_key ? `restrict ${x.sftp.public_key}` : '');
    async function copyKey(x: Target) {
        try {
            await navigator.clipboard.writeText(keyLine(x));
            say(x.id, t('Copied.'));
        } catch {
            say(x.id, t('Select the line and copy it by hand.'));
        }
    }
    async function scan(x: Target) {
        busy = x.id + 'scan';
        say(x.id, '');
        try {
            const r = await api.get<(typeof hostkeys)[string]>(`/api/system/v1/backup/targets/${x.id}/hostkey`);
            hostkeys = {...hostkeys, [x.id]: r};
        } catch (e) {
            say(x.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }
    async function trust(x: Target) {
        const hk = hostkeys[x.id];
        if (!hk) return;
        if (hk.changed && !(await ask({title: t("The server's key changed"), message: t('The server shows a different key than the one confirmed before ({old}). Trust the new one only when you know why it changed - a reinstalled server, say. Otherwise someone may be in between.', {old: hk.trusted_fingerprint ?? ''}), confirm: t('Trust the new key'), danger: true}))) return;
        busy = x.id + 'trust';
        try {
            await api.put(`/api/system/v1/backup/targets/${x.id}/hostkey`, {fingerprint: hk.fingerprint});
            const {[x.id]: _, ...rest} = hostkeys;
            hostkeys = rest;
            await load();
        } catch (e) {
            say(x.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }

    // the form: adding and editing
    interface Draft { id: string; kind: Target['kind']; name: string; enabled: boolean; max_backups: number; subdir: string; encrypt: boolean; append_only: boolean; path: string; server: string; exportPath: string; share: string; version: string; seal: boolean; user: string; domain: string; password: string; host: string; port: number; sshpath: string; has_password: boolean; loc: string; folder: string }
    let draft = $state<Draft | null>(null);
    let formErr = $state('');
    function blank(kind: Target['kind']): Draft {
        const name = {directory: 'USB', share: 'NAS', nfs: 'NAS', cifs: 'NAS', sftp: 'SSH'}[kind];
        return {id: '', kind, name, enabled: true, max_backups: 30, subdir: kind === 'directory' || kind === 'share' ? '' : view?.hostname ?? '', encrypt: true, append_only: false, path: '', server: '', exportPath: '', share: '', version: '', seal: false, user: '', domain: '', password: '', host: '', port: 22, sshpath: '', has_password: false,
            loc: '', folder: kind === 'share' ? view?.hostname ?? '' : 'backup'};
    }
    function edit(x: Target) {
        formErr = '';
        const d: Draft = {...blank(x.kind), id: x.id, name: x.name, enabled: x.enabled, max_backups: x.max_backups, subdir: x.subdir, encrypt: x.encrypt, append_only: x.append_only,
            path: x.directory?.path ?? '', server: x.nfs?.server ?? x.cifs?.server ?? '', exportPath: x.nfs?.export ?? '', share: x.cifs?.share ?? '', version: x.nfs?.version ?? x.cifs?.version ?? '',
            seal: x.cifs?.seal ?? false, user: x.cifs?.user ?? x.sftp?.user ?? '', domain: x.cifs?.domain ?? '', host: x.sftp?.host ?? '', port: x.sftp?.port ?? 22, sshpath: x.sftp?.path ?? '', has_password: x.cifs?.has_password ?? false,
            loc: '', folder: 'backup'};
        const p = x.directory?.location ? parseLocation(x.directory.location) : null;
        if (p) {
            d.loc = p.id;
            d.folder = p.folder;
        }
        if (x.kind === 'share' && x.share) {
            d.loc = `share:${x.share.id}`;
            d.folder = x.share.folder;
        }
        draft = d;
    }
    function add(kind: Target['kind']) {
        formErr = '';
        draft = blank(kind);
    }
    // a location picked with a usable folder (the directory target may keep an older path)
    const draftReady = $derived(
        !draft ? false :
        draft.kind === 'share' ? !!draft.loc && !folderError(draft.folder, true) :
        draft.kind === 'directory' ? (draft.loc ? !folderError(draft.folder, false, draft.loc === 'userfs' ? 'backup' : undefined) : !!draft.path) :
        true,
    );
    const hasDirectory = $derived(!!view?.targets.some((x) => x.kind === 'directory'));
    async function encryptChange(e: Event) {
        const box = e.currentTarget as HTMLInputElement;
        if (!draft || box.checked) {
            if (draft) draft.encrypt = true;
            return;
        }
        const nfs = draft.kind === 'nfs' ? ' ' + t('An NFS share checks no password at all: any computer with an allowed address reads every file on it.') : '';
        const ok = await ask({title: t('Back up without encryption?'), message: t('Unencrypted backups carry every key of this system in plain - radio keys, password hashes, certificates.') + nfs, confirm: t('Without encryption'), danger: true});
        draft.encrypt = !ok;
        box.checked = !ok;
    }
    function body(d: Draft): Record<string, unknown> {
        const b: Record<string, unknown> = {kind: d.kind, name: d.name.trim(), enabled: d.enabled, max_backups: Number(d.max_backups), subdir: d.subdir.trim(), encrypt: d.encrypt, append_only: d.append_only};
        // task 228: chosen as a location (the stick by its label, the system storage); an older
        // setting's path is kept only while no location is picked
        if (d.kind === 'directory') b.directory = d.loc ? {path: '', location: formatLocation(d.loc, d.folder)} : {path: d.path.trim()};
        if (d.kind === 'share') b.share = {id: d.loc.replace(/^share:/, ''), folder: d.folder.replace(/^\/+|\/+$/g, '')};
        if (d.kind === 'nfs') b.nfs = {server: d.server.trim(), export: d.exportPath.trim(), version: d.version};
        if (d.kind === 'cifs') b.cifs = {server: d.server.trim(), share: d.share.trim().replace(/^\/+|\/+$/g, ''), version: d.version, seal: d.seal, user: d.user.trim(), domain: d.domain.trim(), ...(d.password ? {password: d.password} : {})};
        if (d.kind === 'sftp') b.sftp = {host: d.host.trim(), port: Number(d.port) || 22, user: d.user.trim(), path: d.sshpath.trim()};
        return b;
    }
    async function save() {
        if (!draft) return;
        busy = 'save';
        formErr = '';
        try {
            const r = draft.id
                ? await api.put<{target: Target; apply_error?: string}>(`/api/system/v1/backup/targets/${draft.id}`, body(draft))
                : await api.post<{target: Target; apply_error?: string}>('/api/system/v1/backup/targets', body(draft));
            if (r.apply_error) say(r.target.id, t('Saved, but the mount could not be prepared: {error}', {error: r.apply_error}));
            draft = null;
            await load();
        } catch (e) {
            formErr = e instanceof ApiError ? e.message : (e as Error).message;
        } finally {
            busy = '';
        }
    }
</script>

<h2 id="targets">{t('Backup targets')}<Help>{t('Where the nightly backup goes: a USB stick, an NFS or SMB share on a NAS, an SSH server. The backup is made once and copied to every enabled target, checked after writing, and only this system\'s older files are removed beyond the number to keep. A share is mounted only while it is used.')}</Help></h2>
<!-- occulited task 59: the section's panel, with the edge of the warnings that lead here -->
<div class="ol-panel ol-targets-panel {warnEdge(TARGET_WARNINGS)}" {...warnFor(TARGET_WARNINGS)}>
{#if err}<div class="ol-notice err">{err}</div>{/if}
{#if view}
    <div class="ol-toolbar ol-targets-bar" data-section="targets">
        <label><input type="checkbox" checked={view.nightly.enabled} disabled={busy !== ''} onchange={(e) => void nightly((e.currentTarget as HTMLInputElement).checked)} data-action="nightly" /> {t('Every night at {time} to every enabled target', {time: view.nightly.time})}</label>
        <button class="hmm-button" onclick={runAll} disabled={busy !== '' || !view.targets.some((x) => x.enabled)} data-action="run-all">{t('Back up now')}</button>
    </div>
    {#if !view.targets.length}
        <p class="ol-muted" data-notice="no-targets">{t('No target yet: the backups exist only when you download one.')}</p>
    {/if}
    <div class="ol-targets">
        {#each view.targets as x (x.id)}
            <div class="ol-card" class:err={x.enabled && failed(x.state.state)} data-target={x.id} data-kind={x.kind}>
                <div class="ol-card-head">
                    <div class="ol-card-titles">
                        <div class="ol-card-title">{x.name} <span class="ol-muted">· {KIND[x.kind]()}</span>{#if !x.enabled} <span class="ol-badge">{t('off')}</span>{/if}</div>
                        <div class="ol-card-sub">{where(x)}</div>
                    </div>
                    <span class="ol-badge {stateClass(x.state.state)}" data-state={x.state.state}>{stateText(x.state.state)}</span>
                </div>
                <div class="ol-card-detail">
                    {#if x.kind === 'directory' && sched && !sched.path_exists}
                        <div class="ol-warn" data-notice="directory-missing">{t('{path} cannot be read: plug in the USB stick, or change the directory.', {path: sched.path})}</div>
                    {:else if x.kind === 'directory' && sched?.on_userfs}
                        <div class="ol-warn" data-notice="directory-userfs">{t('{path} is on the system itself ({real}), so a backup there is lost with the system - and {n} of them fill the storage the system runs on. Use a USB stick or a share.', {path: sched.path, real: sched.real_path ?? sched.path, n: sched.max_backups || 30})}</div>
                    {/if}
                    {#if x.state.state === 'unsupported'}
                        <div data-notice="unsupported">{unsupportedText(x.state.unsupported)}</div>
                    {:else}
                        {#if x.state.detail && failed(x.state.state)}<div class="ol-warn" data-detail>{x.state.detail}</div>{/if}
                        {#if x.last_backup}
                            <div data-last>
                                {#if x.last_backup.ok}
                                    {t('Last backup {when} · {size} · {secs} s', {when: when(x.last_backup.at), size: size(x.last_backup.size), secs: Math.round((x.last_backup.duration_ms ?? 0) / 1000)})}{#if x.last_backup.encrypted} · {t('encrypted')}{/if}
                                {:else}
                                    {t('Last attempt {when} failed', {when: when(x.last_backup.at)})}{#if x.last_backup.last_ok} · {t('last success {when}', {when: when(x.last_backup.last_ok)})}{/if}
                                {/if}
                            </div>
                        {:else}
                            <div>{t('No backup here yet.')}</div>
                        {/if}
                        {#if x.state.free_bytes}<div>{t('{free} free of {total}', {free: size(x.state.free_bytes), total: size(x.state.total_bytes)})}</div>{/if}
                        {#if x.state.mounted}<div data-mounted>{t('Mounted: {source}', {source: x.state.source ?? ''})}</div>{/if}
                        {#if x.state.next_retry_at && failed(x.state.state)}<div>{t('Next check {when}', {when: when(x.state.next_retry_at)})}</div>{/if}
                        {#if x.append_only}<div>{t('Append-only: this system never deletes here; prune old backups on the server.')}</div>{/if}
                        {#if !x.encrypt}<div class="ol-warn" data-notice="target-plain">{t('Backups to this target are not encrypted.')}</div>{/if}
                    {/if}
                </div>
                {#if tests[x.id]}
                    {@const r = tests[x.id]!}
                    <div class="ol-notice" class:err={!r.ok || r.state === 'full'} data-test-result={r.state}>
                        {#if r.ok && r.state !== 'full'}
                            {t('Writable: 1 MB written, read back and deleted.')}{#if r.write_mbps}{' ' + t('About {mbps} MB/s.', {mbps: r.write_mbps.toFixed(1)})}{/if}
                        {:else if r.state === 'full'}
                            {t('Writable, but {free} free is less than the next backup needs ({needed}).', {free: size(r.free_bytes), needed: size(r.needed_bytes)})}
                        {:else}
                            {stateText(r.state)} ({r.step}){#if r.error}: {r.error}{/if}
                            {#if r.state === 'read-only' && (x.kind === 'nfs' || x.kind === 'share')} {t('An export that maps root to nobody (root_squash) needs a directory that user may write, or "map all users" to one account.')}{/if}
                        {/if}
                    </div>
                {/if}
                {#if x.kind === 'sftp' && x.state.state !== 'unsupported'}
                    <div class="ol-sshkey" data-ssh>
                        {#if x.sftp?.public_key}
                            <div class="ol-muted">{t("This system's key, for the server's authorized_keys (restrict allows file transfer only):")}</div>
                            <code class="hmm-mono ol-keyline" data-public-key>{keyLine(x)}</code>
                            <div class="ol-actions"><button class="hmm-button" onclick={() => void copyKey(x)} data-action="copy-key">{t('Copy')}</button><button class="hmm-button" onclick={() => void newKey(x)} disabled={busy !== ''} data-action="new-key">{t('New key')}</button></div>
                        {:else}
                            <button class="hmm-button" onclick={() => void newKey(x)} disabled={busy !== ''} data-action="new-key">{t('Make a key')}</button>
                        {/if}
                        <div class="ol-muted" data-host-key>{#if x.sftp?.host_key}{t("Server's key: {type} {fp}", {type: x.sftp.host_key.type, fp: x.sftp.host_key.fingerprint})}{:else}{t("The server's key is not confirmed yet.")}{/if}</div>
                        {#if hostkeys[x.id]}
                            {@const hk = hostkeys[x.id]!}
                            <div class="ol-notice" class:err={hk.changed} data-scan={hk.trusted ? 'trusted' : hk.changed ? 'changed' : 'new'}>
                                {#if hk.trusted}{t('The server shows the confirmed key.')}{:else}
                                    {t('The server shows {type} {fp}. Compare it with the server (ssh-keygen -lf on its host key) and trust it only if it matches.', {type: hk.type, fp: hk.fingerprint})}
                                    <button class="hmm-button primary" onclick={() => void trust(x)} disabled={busy !== ''} data-action="trust-key">{t('Trust this key')}</button>
                                {/if}
                            </div>
                        {:else}
                            <button class="hmm-button" onclick={() => void scan(x)} disabled={busy !== ''} data-action="scan-key">{x.sftp?.host_key ? t("Check the server's key") : t("Get the server's key")}</button>
                        {/if}
                    </div>
                {/if}
                {#if msg[x.id]}<div class="ol-notice" data-msg>{msg[x.id]}</div>{/if}
                <div class="ol-actions ol-card-foot">
                    {#if x.state.state !== 'unsupported'}
                        <button class="hmm-button" onclick={() => void act(x, 'test')} disabled={busy !== '' || x.state.state === 'running'} data-action="test">{busy === x.id + 'test' ? t('Testing…') : t('Test')}</button>
                        <button class="hmm-button" onclick={() => void act(x, 'run')} disabled={busy !== '' || x.state.state === 'running'} data-action="run">{t('Back up now')}</button>
                        {#if x.kind === 'nfs' || x.kind === 'cifs' || x.kind === 'share'}
                            {#if x.state.mounted}<button class="hmm-button" onclick={() => void act(x, 'unmount')} disabled={busy !== ''} data-action="unmount">{t('Unmount')}</button>
                            {:else}<button class="hmm-button" onclick={() => void act(x, 'mount')} disabled={busy !== ''} data-action="mount">{t('Mount')}</button>{/if}
                        {/if}
                        <button class="hmm-button" aria-expanded={open[x.id] !== undefined} onclick={() => void toggleBackups(x)} bind:this={backupsButtons[x.id]} data-action="backups">{t('Backups here')}</button>
                    {/if}
                    {#if !(draft && draft.id === x.id)}<button class="hmm-button" onclick={() => edit(x)} disabled={busy !== ''} data-action="edit">{t('Edit')}</button>{/if}
                    <button class="hmm-button" onclick={() => void remove(x)} disabled={busy !== ''} data-action="remove">{t('Remove')}</button>
                </div>
                <Disclosure title={t('Backups here')} readOnly trigger={backupsButtons[x.id]} bind:open={() => open[x.id] !== undefined, (v) => { if (!v && open[x.id] !== undefined) void toggleBackups(x); }}>
                {#if open[x.id] === null}
                    <p class="ol-muted">{t('Loading…')}</p>
                {:else if open[x.id]}
                    {@const list = open[x.id] ?? []}
                    {#if !list.length}<p class="ol-muted" data-backups-empty>{t('No backups there.')}</p>{:else}
                        <table class="ol-table" data-backups>
                            <thead><tr><th>{t('File')}</th><th>{t('Size')}</th><th>{t('Time')}</th><th></th></tr></thead>
                            <tbody>
                                {#each list as b (b.name)}
                                    <tr>
                                        <td class="hmm-mono">{b.name}{#if b.encrypted && b.known === 'previous'} <span class="ol-badge warn">{t('earlier key')}</span>{:else if b.encrypted && b.known === 'unknown'} <span class="ol-badge bad">{t('unknown key')}</span>{/if}</td>
                                        <td>{size(b.size)}</td>
                                        <td>{when(b.time)}</td>
                                        <td><button class="hmm-button" onclick={() => void restore(x, b)} data-action="restore-this">{t('Restore this')}</button></td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    {/if}
                {/if}
                </Disclosure>
                {#if draft && draft.id === x.id}{@render form()}{/if}
            </div>
        {/each}
    </div>
    {#if draft && !draft.id}
        <div class="ol-card ol-newtarget" data-new-target={draft.kind}>
            <div class="ol-card-title">{t('New target')}: {KIND[draft.kind]()}</div>
            {@render form()}
        </div>
    {:else if !draft}
        <div class="ol-actions ol-addtarget" data-section="add-target">
            <span class="ol-muted">{t('Add target')}:</span>
            {#if !hasDirectory}<button class="hmm-button" onclick={() => add('directory')} data-add="directory">{KIND.directory()}</button>{/if}
            <!-- task 228: NFS and SMB are shares of System → Storage; a target picks one and a folder -->
            <button class="hmm-button" onclick={() => add('share')} disabled={!!view.kinds.share} title={view.kinds.share ? unsupportedText(view.kinds.share) : ''} data-add="share">{KIND.share()}</button>
            <button class="hmm-button" onclick={() => add('sftp')} data-add="sftp">{KIND.sftp()}</button>
        </div>
        {#if view.container}<div class="ol-notice" data-notice="container">{unsupportedText('container')}</div>{/if}
    {/if}
{/if}
</div>

{#snippet form()}
    {#if draft}
        <form class="ol-form ol-targetform" onsubmit={(e) => { e.preventDefault(); void save(); }} data-form={draft.kind}>
            <label>{t('Name')} <input class="hmm-input" bind:value={draft.name} maxlength="64" required data-field="name" /></label>
            {#if draft.kind === 'directory'}
                <!-- task 228: the location picker - a USB stick by its label (in any port), or the system's own storage -->
                <LocationPicker use="backup" kinds={['usb', 'userfs']} bind:location={draft.loc} bind:folder={draft.folder} label={t('Where')} />
                {#if !draft.loc && draft.path}
                    <p class="ol-muted" data-legacy-path>{t('Chosen before as the path {path}. Pick the stick above, so the backups find it by its label in any USB port.', {path: draft.path})}</p>
                {/if}
            {:else if draft.kind === 'share'}
                <LocationPicker use="backup" kinds={['share']} bind:location={draft.loc} bind:folder={draft.folder} label={t('Share')} />
                <p class="ol-muted">{t('The folder is this system\'s own on the share (the host name by default), so several systems can share it.')}</p>
            {:else if draft.kind === 'nfs'}
                <label>{t('Server')} <input class="hmm-input hmm-mono" bind:value={draft.server} placeholder="nas.local" required data-field="server" /></label>
                <label>{t('Export')} <input class="hmm-input hmm-mono" bind:value={draft.exportPath} placeholder="/volume1/backup" required data-field="export" /></label>
                <label>{t('NFS version')} <select class="hmm-select" bind:value={draft.version} data-field="version"><option value="">{t('automatic (4.2, 4.1, 4, then 3)')}</option><option value="4.2">4.2</option><option value="4.1">4.1</option><option value="4">4.0</option><option value="3">3</option></select></label>
                <p class="ol-muted">{t('NFS checks no password: limit the export to this system\'s address. The system writes as root; an export that maps root to nobody needs "map all users" to one account that owns the directory.')}</p>
            {:else if draft.kind === 'cifs'}
                <label>{t('Server')} <input class="hmm-input hmm-mono" bind:value={draft.server} placeholder="nas.local" required data-field="server" /></label>
                <label>{t('Share')} <input class="hmm-input hmm-mono" bind:value={draft.share} placeholder="backup" required data-field="share" /></label>
                <label>{t('User')} <input class="hmm-input" bind:value={draft.user} autocomplete="off" required data-field="user" /></label>
                <label>{t('Password')} <input class="hmm-input" type="password" bind:value={draft.password} autocomplete="new-password" placeholder={draft.has_password ? t('(unchanged)') : ''} data-field="password" /></label>
                <label>{t('Domain')} <input class="hmm-input" bind:value={draft.domain} placeholder={t('(optional)')} data-field="domain" /></label>
                <label>{t('SMB version')} <select class="hmm-select" bind:value={draft.version} data-field="version"><option value="">{t('3.0 or newer')}</option><option value="3.1.1">3.1.1</option><option value="3.0">3.0</option></select></label>
                <label><input type="checkbox" bind:checked={draft.seal} data-field="seal" /> {t('Encrypt the transfer (SMB3)')}</label>
                <p class="ol-muted">{t('Use an account of its own on the NAS for this: its password travels in every backup of this system, so a restored system reconnects without it.')}</p>
            {:else}
                <label>{t('Server')} <input class="hmm-input hmm-mono" bind:value={draft.host} placeholder="nas.local" required data-field="host" /></label>
                <label>{t('Port')} <input class="hmm-input" type="number" min="1" max="65535" bind:value={draft.port} style="width:7em" data-field="port" /></label>
                <label>{t('User')} <input class="hmm-input" bind:value={draft.user} autocomplete="off" required data-field="user" /></label>
                <label>{t('Directory on the server')} <input class="hmm-input hmm-mono" bind:value={draft.sshpath} placeholder={t('(home directory)')} data-field="sshpath" /></label>
                <p class="ol-muted">{t('The system makes a key of its own when the target is saved; you then add its line to the server\'s authorized_keys and confirm the server\'s key. Passwords are not used.')}</p>
            {/if}
            {#if draft.kind !== 'directory' && draft.kind !== 'share'}
                <label>{t('Subdirectory')} <input class="hmm-input hmm-mono" bind:value={draft.subdir} placeholder={view?.hostname ?? ''} data-field="subdir" /> <span class="ol-muted">{t('one per system, so several can share it')}</span></label>
            {/if}
            <label>{t('Keep')} <input class="hmm-input" type="number" min="0" max="1000" bind:value={draft.max_backups} style="width:6em" data-field="max" /> <span class="ol-muted">{t('backups of this system (0 = all)')}</span></label>
            <label><input type="checkbox" checked={draft.encrypt} onchange={encryptChange} data-field="encrypt" /> {t('Encrypt the backups (with the recovery key)')}</label>
            <label><input type="checkbox" bind:checked={draft.append_only} data-field="append-only" /> {t('Append-only: never delete anything here')}</label>
            <label><input type="checkbox" bind:checked={draft.enabled} data-field="enabled" /> {t('Include in the nightly backup')}</label>
            {#if formErr}<div class="ol-notice err" data-form-error>{formErr}</div>{/if}
            <div class="ol-actions">
                <button class="hmm-button primary" type="submit" disabled={busy !== '' || !draftReady} data-action="save">{t('Save')}</button>
                <button class="hmm-button" type="button" onclick={() => (draft = null)} data-action="cancel">{t('Cancel')}</button>
            </div>
        </form>
    {/if}
{/snippet}

<style>
    /* task 161: the mounted sticks, one button each; the text wraps on a phone */
    .ol-sticks { display: flex; flex-wrap: wrap; gap: 6px; }
    .ol-stick { white-space: normal; text-align: left; overflow-wrap: anywhere; max-width: 100%; }
    .ol-targets { display: flex; flex-direction: column; gap: 12px; margin: 10px 0; }
    /* occulited task 59: inside the section's panel a target is a bordered box, not a second raised card */
    .ol-targets .ol-card, .ol-newtarget { box-shadow: none; }
    .ol-targets-bar { flex-wrap: wrap; gap: 12px; align-items: center; }
    .ol-targets-bar label { display: inline-flex; gap: 6px; align-items: center; }
    .ol-card-head .ol-badge { flex: 0 0 auto; }
    .ol-card-detail > div { margin-top: 2px; }
    .ol-sshkey { margin-top: 10px; display: flex; flex-direction: column; gap: 6px; min-width: 0; }
    /* a fingerprint is one run without a break: it breaks anywhere, so a phone keeps its width */
    .ol-sshkey > div, .ol-card-detail, [data-test-result] { overflow-wrap: anywhere; min-width: 0; }
    .ol-keyline { display: block; overflow-wrap: anywhere; font-size: var(--hmm-font-size-small); padding: 6px 8px; border: 1px solid var(--hmm-border-muted); border-radius: 4px; }
    .ol-card-foot { flex-wrap: wrap; gap: 6px; }
    .ol-addtarget { flex-wrap: wrap; gap: 6px; align-items: center; margin: 8px 0; }
    .ol-targetform { margin-top: 10px; max-width: 640px; display: flex; flex-direction: column; gap: 8px; }
    .ol-targetform label { display: block; }
    .ol-table td.hmm-mono { overflow-wrap: anywhere; }
    .ol-notice.err { border-left: 3px solid var(--hmm-error); }
</style>
