<script lang="ts">
    /*
     * openccu-lite task 228, phase 2 (the maintainer, 2026-09-24: "on the new storage page i'd like
     * to also have possibility to mount smb/nfs shares"): System → Storage's network shares. Each
     * share has a name - its mount point /media/net/<name> and how the journal and the backups
     * name it - and is mounted by the system on first use and unmounted when idle, so a share that
     * is down never blocks the boot. Test mounts it, writes, reads back and deletes a probe file (a
     * read-only share is listed instead) and unmounts it again. The password is write-only.
     */
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {auth} from './auth.svelte';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import SectionHead from './SectionHead.svelte';
    import Disclosure from './Disclosure.svelte';
    import Icon from './Icon.svelte';
    import {link} from './router.svelte';
    import {formatBytes} from './netpanels';
    import {nameError, normalisePath, source, suggestName, type Share, type SharesView, type ShareKind, type ShareTest} from './shares';

    let {active = () => true}: {active?: () => boolean} = $props();

    const admin = $derived(auth.role === 'admin');
    let view = $state<SharesView | null>(null);
    let error = $state('');
    let busy = $state('');
    let msg = $state<Record<string, string>>({});
    let tests = $state<Record<string, ShareTest>>({});

    export async function load() {
        try {
            view = await api.get<SharesView>('/api/system/v1/storage/shares');
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(() => {
        void load();
        // a share mounts and unmounts on its own (first use, idle): look again now and then
        const poll = setInterval(() => active() && busy === '' && !draft && void load(), 15000);
        return () => clearInterval(poll);
    });

    const KIND: Record<ShareKind, () => string> = {nfs: () => t('NFS share'), cifs: () => t('SMB share')};
    const STATE: Record<string, () => string> = {
        mounted: () => t('mounted'),
        idle: () => t('mounted when used'),
        writable: () => t('writable'),
        readable: () => t('readable'),
        unreadable: () => t('not readable by the system'),
        unsupported: () => t('not available here'),
        unreachable: () => t('unreachable'),
        'auth-failed': () => t('login refused'),
        'read-only': () => t('not writable'),
        full: () => t('full'),
        stale: () => t('not answering'),
        error: () => t('error'),
    };
    const stateText = (s: string) => (STATE[s] ?? (() => s))();
    const dot = (s: string) => (s === 'mounted' || s === 'writable' || s === 'readable' ? 'good' : s === 'idle' || s === 'unsupported' ? 'idle' : 'bad');
    const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : '');
    const unsupportedText = (why?: string) => (why === 'container' ? t('This system is a container: mount the share on the host and pass it into the container (Docker: a volume; Proxmox: a mount point).') : t('The program for this kind of share is not part of this system image yet.'));
    const say = (id: string, text: string) => (msg = {...msg, [id]: text});
    const useWord = (u: {kind: string; name?: string; id?: string}) => (u.kind === 'journal' ? t('the journal\'s copies') : t('the backup target {name}', {name: u.name ?? u.id ?? ''}));

    async function act(s: Share, what: 'test' | 'mount' | 'unmount') {
        busy = s.id + what;
        say(s.id, '');
        try {
            if (what === 'test') {
                const r = await api.post<ShareTest>(`/api/system/v1/storage/shares/${s.id}/test`, {});
                tests = {...tests, [s.id]: r};
            } else {
                await api.post(`/api/system/v1/storage/shares/${s.id}/${what}`, {});
            }
            await load();
        } catch (e) {
            say(s.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }

    async function remove(s: Share) {
        const ok = await ask({title: t('Remove share'), message: t('Remove the share "{name}"? It is unmounted and forgotten; the files on the server stay where they are.', {name: s.id}), confirm: t('Remove'), danger: true});
        if (!ok) return;
        busy = s.id + 'remove';
        try {
            await api.del(`/api/system/v1/storage/shares/${s.id}`);
            await load();
        } catch (e) {
            say(s.id, (e as Error).message);
        } finally {
            busy = '';
        }
    }

    // the form, adding and editing: an in-page panel under the heading that grows out of the button
    // that opened it and shrinks back into it (lib/Disclosure.svelte, as Add gateway on LAN devices;
    // task 241, the maintainer: no popup window for a form)
    interface Draft { editing: boolean; id: string; kind: ShareKind; server: string; path: string; version: string; seal: boolean; read_only: boolean; user: string; domain: string; password: string; has_password: boolean; named: boolean }
    let draft = $state<Draft | null>(null);
    let formOpen = $state(false);
    let formErr = $state('');
    const taken = $derived((view?.shares ?? []).map((s) => s.id));
    const draftNameErr = $derived(draft && !draft.editing ? nameError(draft.id) : '');
    // task 268: the button that opens the panel is hidden while it is open (the Disclosure hides
    // the one it is given); the panel's Cancel closes it
    let addButton = $state<HTMLButtonElement | null>(null);
    let editButtons = $state<Record<string, HTMLButtonElement>>({});
    let formTrigger = $state<HTMLElement | null>(null);
    // the button under the heading opens the panel for a new share
    function add() {
        formTrigger = addButton;
        formErr = '';
        const kind: ShareKind = view?.kinds.cifs ? 'nfs' : 'cifs';
        draft = {editing: false, id: suggestName('', taken), kind, server: '', path: '', version: '', seal: false, read_only: false, user: '', domain: '', password: '', has_password: false, named: false};
        formOpen = true;
    }
    // a card's Edit opens the same panel for that share
    function edit(s: Share) {
        formTrigger = editButtons[s.id] ?? null;
        formErr = '';
        draft = {editing: true, id: s.id, kind: s.kind, server: s.server, path: s.path, version: s.version, seal: !!s.seal, read_only: s.read_only, user: s.user ?? '', domain: s.domain ?? '', password: '', has_password: s.has_password, named: true};
        formOpen = true;
    }
    function serverInput() {
        // the name follows the server until the user types one of their own
        if (draft && !draft.editing && !draft.named) draft.id = suggestName(draft.server.trim(), taken);
    }
    function kindChange() {
        if (draft) draft.version = '';
    }
    function body(d: Draft): Record<string, unknown> {
        const b: Record<string, unknown> = {id: d.id, kind: d.kind, server: d.server.trim(), path: normalisePath(d.kind, d.path), version: d.version, read_only: d.read_only};
        if (d.kind === 'cifs') {
            b.seal = d.seal;
            b.user = d.user.trim();
            if (d.domain.trim()) b.domain = d.domain.trim();
            if (d.password || !d.editing) b.password = d.password;
        }
        return b;
    }
    async function save() {
        if (!draft || draftNameErr) return;
        busy = 'save';
        formErr = '';
        try {
            const r = draft.editing
                ? await api.put<{share: Share; apply_error?: string}>(`/api/system/v1/storage/shares/${draft.id}`, body(draft))
                : await api.post<{share: Share; apply_error?: string}>('/api/system/v1/storage/shares', body(draft));
            if (r.apply_error) say(r.share.id, t('Saved, but the mount could not be prepared: {error}', {error: r.apply_error}));
            formOpen = false;
            await load();
        } catch (e) {
            formErr = e instanceof ApiError ? e.message : (e as Error).message;
        } finally {
            busy = '';
        }
    }
</script>

{#snippet addAction()}
    <button type="button" class="hmm-button" aria-expanded={formOpen && !draft?.editing} onclick={add} bind:this={addButton} disabled={busy !== '' || !view || (!!view.kinds.nfs && !!view.kinds.cifs)} data-action="add-share">{t('Add share')}</button>
{/snippet}
<SectionHead id="shares" title={t('Network shares')} help={t('SMB and NFS shares on a NAS or a server. The system mounts a share when something uses it - a backup, the journal\'s copies - and unmounts it when it has been idle for five minutes, so a server that is down never holds up the boot. The journal and the backups name a share by its name.')} actions={admin && view && !view.container ? addAction : undefined} />
<Disclosure title={draft?.editing ? t('Edit share {name}', {name: draft.id}) : t('Add a network share')} bind:open={formOpen} trigger={formTrigger}>
    {#if draft}
        <form class="sh-form" onsubmit={(e) => { e.preventDefault(); void save(); }} data-share-form={draft.kind}>
            <fieldset class="sh-kind" disabled={draft.editing}>
                <legend>{t('Kind')}</legend>
                {#each ['cifs', 'nfs'] as const as k (k)}
                    <label class:ol-muted={!!view?.kinds[k]}>
                        <input type="radio" name="sh-kind" value={k} bind:group={draft.kind} onchange={kindChange} disabled={!!view?.kinds[k]} data-field="kind-{k}" />
                        <strong>{k === 'cifs' ? 'SMB' : 'NFS'}</strong>
                        <span class="ol-muted">{k === 'cifs' ? t('Windows, Synology, QNAP, TrueNAS: a user and a password') : t('Linux and NAS exports: the server checks the address')}{view?.kinds[k] ? ' · ' + unsupportedText(view.kinds[k]) : ''}</span>
                    </label>
                {/each}
            </fieldset>
            <label>{t('Server')} <input class="hmm-input hmm-mono" bind:value={draft.server} oninput={serverInput} placeholder="nas.local" required autocomplete="off" data-field="server" /></label>
            {#if draft.kind === 'cifs'}
                <label>{t('Share')} <input class="hmm-input hmm-mono" bind:value={draft.path} placeholder="backup" required data-field="path" /> <span class="ol-muted">{t('its name on the server, optionally with a folder in it (backup/ccu)')}</span></label>
                <label>{t('User')} <input class="hmm-input" bind:value={draft.user} autocomplete="off" required data-field="user" /></label>
                <label>{t('Password')} <input class="hmm-input" type="password" bind:value={draft.password} autocomplete="new-password" placeholder={draft.has_password ? t('(unchanged)') : ''} data-field="password" /></label>
                <label>{t('Domain')} <input class="hmm-input" bind:value={draft.domain} placeholder={t('(optional)')} data-field="domain" /></label>
                <label>{t('SMB version')} <select class="hmm-select" bind:value={draft.version} data-field="version"><option value="">{t('3.0 or newer')}</option><option value="3.1.1">3.1.1</option><option value="3.0">3.0</option></select></label>
                <label class="sh-check"><input type="checkbox" bind:checked={draft.seal} data-field="seal" /> {t('Encrypt the transfer (SMB3)')}</label>
            {:else}
                <label>{t('Export')} <input class="hmm-input hmm-mono" bind:value={draft.path} placeholder="/volume1/data" required data-field="path" /></label>
                <label>{t('NFS version')} <select class="hmm-select" bind:value={draft.version} data-field="version"><option value="">{t('automatic (4.2, 4.1, 4, then 3)')}</option><option value="4.2">4.2</option><option value="4.1">4.1</option><option value="4">4.0</option><option value="3">3</option></select></label>
                <p class="ol-muted">{t('NFS checks no password: limit the export to this system\'s address. The system writes as root; an export that maps root to nobody needs "map all users" to one account that owns the directory.')}</p>
            {/if}
            <label class="sh-check"><input type="checkbox" bind:checked={draft.read_only} data-field="read-only" /> {t('Read-only')} <span class="ol-muted">{t('- nothing on the share is changed; the journal and the backups cannot use it')}</span></label>
            <label>{t('Name')}
                <input class="hmm-input hmm-mono" bind:value={draft.id} oninput={() => draft && (draft.named = true)} maxlength="16" required disabled={draft.editing} data-field="name" />
                <span class="ol-muted">{t('mounted at /media/net/{name}; the journal and the backups name the share by it', {name: draft.id || '…'})}</span>
            </label>
            {#if draftNameErr}<div class="ol-warn" data-name-problem>{t(draftNameErr)}</div>{/if}
            {#if draft.kind === 'cifs'}<p class="ol-muted">{t('Use an account of its own on the NAS for this: its password travels in every backup of this system, so a restored system mounts the share again without it.')}</p>{/if}
            <p class="ol-muted">{t('Programs, device files and set-user-ID files on a share are never run: it is mounted noexec, nodev and nosuid.')}</p>
            {#if formErr}<div class="ol-notice error" data-form-error>{formErr}</div>{/if}
            <div class="ol-form-buttons"><button type="submit" class="hmm-button primary" disabled={busy !== '' || !!draftNameErr} data-action="save">{busy === 'save' ? t('Saving…') : t('Save')}</button></div>
        </form>
    {/if}
</Disclosure>
{#if error}<div class="ol-notice error" data-notice="shares-error">{error}</div>{/if}
{#if view}
    {#if view.container}<div class="ol-notice" data-notice="shares-container">{unsupportedText('container')}</div>{/if}
    {#if view.shares.length === 0}
        <p class="ol-muted" data-shares-empty>{t('No network share yet.')}</p>
    {:else}
        <div class="sh-list">
            {#each view.shares as s (s.id)}
                {@const st = s.state}
                <section class="ol-card sh-card" data-share={s.id} data-kind={s.kind}>
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="disk" size={14} /></span>
                        <div class="ol-card-titles">
                            <h3 class="ol-card-title">{s.id} <span class="ol-muted">· {KIND[s.kind]()}</span>{#if s.read_only}{' '}<span class="ol-badge">{t('read-only')}</span>{/if}</h3>
                            <div class="ol-card-sub hmm-mono">{source(s)}</div>
                        </div>
                        <span class="sh-state" data-state={st.state}><span class="sh-dot {dot(st.state)}" aria-hidden="true"></span>{stateText(st.state)}</span>
                    </div>
                    <div class="sh-detail">
                        {#if st.state === 'unsupported'}
                            <div data-notice="unsupported">{unsupportedText(st.unsupported)}</div>
                        {:else}
                            <div class="ol-muted">{t('Mount point {path}', {path: s.where})}</div>
                            {#if st.mounted && st.total_bytes}<div data-space>{t('{free} free of {total}', {free: formatBytes(st.free_bytes ?? 0), total: formatBytes(st.total_bytes)})}</div>{/if}
                            {#if st.detail && dot(st.state) === 'bad'}<div class="ol-warn" data-detail>{st.detail}</div>{/if}
                            {#if st.next_retry_at && dot(st.state) === 'bad'}<div class="ol-muted">{t('Next check {when}', {when: when(st.next_retry_at)})}</div>{/if}
                            {#if s.uses?.length}
                                <div data-uses>{t('In use by')}: {#each s.uses as u, i (i)}{#if i > 0}{', '}{/if}<a href={u.kind === 'journal' ? '/system/log?settings=journal' : '/system/backup#targets'} use:link>{useWord(u)}</a>{/each}</div>
                            {/if}
                        {/if}
                    </div>
                    {#if tests[s.id]}
                        {@const r = tests[s.id]!}
                        <div class="ol-notice" class:error={!r.ok} data-test-result={r.state}>
                            {#if r.ok && r.state === 'readable'}
                                {t('Mounted and read; it is mounted read-only, so nothing was written.')}
                            {:else if r.ok && r.state === 'unreadable'}
                                <!-- B-223: root wrote, the system's own user cannot read it back -->
                                <span class="ol-warn">{t('Writable, but the system cannot read back what it wrote: the Log page cannot show the journal\'s copies there.')}{#if s.kind === 'nfs'}{' ' + t('On an NFS export that maps root to nobody (root_squash), map root to root (TrueNAS: Maproot User root) or map all users to one account (TrueNAS: Mapall User).')}{/if}</span>
                            {:else if r.ok}
                                {t('Writable: 1 MB written, read back and deleted.')}{#if r.write_mbps}{' ' + t('About {mbps} MB/s.', {mbps: r.write_mbps.toFixed(1)})}{/if}
                            {:else}
                                {stateText(r.state)} ({r.step}){#if r.error}: {r.error}{/if}
                                {#if r.state === 'read-only' && s.kind === 'nfs'}{' '}{t('An export that maps root to nobody (root_squash) needs a directory that user may write, or "map all users" to one account.')}{/if}
                            {/if}
                        </div>
                    {/if}
                    {#if msg[s.id]}<div class="ol-notice" data-msg>{msg[s.id]}</div>{/if}
                    {#if admin}
                        <div class="ol-form-buttons sh-actions">
                            {#if st.state !== 'unsupported'}
                                <button type="button" class="hmm-button" onclick={() => void act(s, 'test')} disabled={busy !== ''} data-action="test">{busy === s.id + 'test' ? t('Testing…') : t('Test')}</button>
                                {#if st.mounted}
                                    <button type="button" class="hmm-button" onclick={() => void act(s, 'unmount')} disabled={busy !== ''} data-action="unmount">{t('Unmount')}</button>
                                {:else}
                                    <button type="button" class="hmm-button" onclick={() => void act(s, 'mount')} disabled={busy !== ''} data-action="mount">{busy === s.id + 'mount' ? t('Mounting…') : t('Mount')}</button>
                                {/if}
                            {/if}
                            <button type="button" class="hmm-button" aria-expanded={formOpen && !!draft?.editing && draft.id === s.id} onclick={() => edit(s)} disabled={busy !== ''} bind:this={editButtons[s.id]} data-action="edit">{t('Edit')}</button>
                            <button type="button" class="hmm-button" onclick={() => void remove(s)} disabled={busy !== '' || !!s.uses?.length} title={s.uses?.length ? t('In use: pick another location there first.') : undefined} data-action="remove">{t('Remove')}</button>
                        </div>
                    {/if}
                </section>
            {/each}
        </div>
    {/if}
{/if}


<style>
    .sh-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(360px, 100%), 1fr)); gap: 12px; margin: 8px 0 10px; }
    .sh-card { display: flex; flex-direction: column; min-width: 0; }
    .sh-card .ol-card-sub { overflow-wrap: anywhere; }
    .sh-state { display: inline-flex; align-items: center; gap: 6px; flex: 0 0 auto; font-size: var(--hmm-font-size-small); white-space: nowrap; }
    .sh-dot { width: 9px; height: 9px; border-radius: 50%; background: var(--hmm-fg-faint); }
    .sh-dot.good { background: var(--hmm-ok); }
    .sh-dot.bad { background: var(--hmm-error); }
    .sh-detail { display: flex; flex-direction: column; gap: 2px; overflow-wrap: anywhere; min-width: 0; }
    .sh-actions { margin-top: auto; padding-top: 10px; display: flex; gap: 8px; flex-wrap: wrap; }
    .sh-form { display: flex; flex-direction: column; gap: 10px; }
    .sh-form label { display: flex; flex-direction: column; gap: var(--ol-label-gap, 4px); }
    .sh-form label.sh-check { flex-direction: row; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .sh-form input.hmm-input, .sh-form select { max-width: 24em; }
    .sh-kind { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 6px; }
    .sh-kind legend { font-weight: 600; margin-bottom: 4px; }
    .sh-kind label { flex-direction: row; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    [data-test-result] { overflow-wrap: anywhere; }
</style>
