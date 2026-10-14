<script lang="ts">
    /*
     * The factory reset (task 109, D-106): a section of the Backup page, administrators only. It
     * says what is erased, offers the backup first, and warns about what a wiped userfs cannot
     * undo - BidCos-RF devices paired under an individual security key keep that key, HmIP devices
     * have to be reset on the device before they can be paired again, and local key mode's network
     * key goes with the reset. Then the host name typed into the shell's dialog, the marker set
     * through the API, and the reboot counted down with the restore's figures (task 94). No key
     * is ever shown here.
     */
    import {onMount} from 'svelte';
    import {api, ApiError} from './api';
    import {askText} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import Notice from './Notice.svelte';
    import BootBar from './BootBar.svelte';
    import {EASE_MS, type BootEntry} from './bootbar';
    import {beginBoot, removeEntry, watchBoot} from './bootwatch';
    import {boxUptime} from './power';
    import {download} from './download';
    import {sameHostname} from './factoryreset';

    interface Paired { devices: number; known: boolean; error?: string }
    interface View {
        hostname: string;
        armed: boolean;
        container: string;
        update_staged: boolean;
        interfaces: Record<string, Paired>;
        security_key_set: boolean;
        security_key_known: boolean;
        security_key_error?: string;
        hmip_local_key: boolean;
    }

    let view = $state<View | null>(null);
    let loadError = $state('');
    let busy = $state('');
    let error = $state('');
    let resetting = $state<BootEntry | null>(null);

    async function load() {
        try {
            view = await api.get<View>('/api/system/v1/factory-reset');
            loadError = '';
        } catch (e) {
            loadError = (e as Error).message;
        }
    }
    onMount(() => void load());

    const bidcos = $derived(view?.interfaces['BidCos-RF']);
    const hmip = $derived(view?.interfaces['HmIP-RF']);
    /** the individual-key warning: devices paired and the key not the default - or not checkable */
    const keyWarning = $derived(!!bidcos && bidcos.known && bidcos.devices > 0 && (view!.security_key_set || !view!.security_key_known));

    async function backup() {
        busy = 'backup';
        error = '';
        try {
            await download('/api/system/v1/backup');
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function reset() {
        if (!view) return;
        error = '';
        const typed = await askText({
            title: t('Factory reset'),
            message: [
                t('Erase everything on this system and reboot? Users, names and rooms, addons and their settings, certificates, the pairings of every radio device and the radio keys are gone; the system starts as new, unpaired, with its first-start page.'),
                t('Type the host name {host} to confirm.', {host: view.hostname}),
            ].join('\n\n'),
            input: {label: t('Host name'), placeholder: view.hostname},
            confirm: t('Erase and reboot'),
            danger: true,
            focusCancel: true,
        });
        if (typed === null) return;
        if (!sameHostname(typed, view.hostname)) {
            error = t('The name typed is not this system\'s host name; nothing happened.');
            return;
        }
        busy = 'reset';
        // the countdown before the request (task 94): a reset's boot makes the userfs anew and then
        // runs a first boot, which is the restore's shape - there is no table of its own
        const before = await boxUptime();
        const entry = await beginBoot('restore', () => api.get('/api/system/v1/boot-expect?kind=restore'));
        const wait = () => {
            resetting = entry;
            // back = a system with its first-start page, most likely a new certificate and maybe
            // another name: the shell starts from the top
            watchBoot({entry, before, onUpdate: (e) => (resetting = e), onBack: () => setTimeout(() => location.assign('/'), EASE_MS)});
        };
        try {
            await api.post('/api/system/v1/factory-reset', {confirm: true, hostname: typed});
            wait();
        } catch (e) {
            if (e instanceof ApiError) {
                removeEntry();
                error = e.message;
                await load();
            } else {
                // no answer at all: the reboot took the system away with the request
                wait();
            }
        } finally {
            busy = '';
        }
    }

    async function cancel() {
        busy = 'cancel';
        error = '';
        try {
            await api.del('/api/system/v1/factory-reset');
            await load();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
</script>

<h2 id="factory-reset">{t('Factory reset')}<Help>{t('Sets the firmware\'s reset marker and reboots: at that boot the user file system is made anew, as a reset from the recovery system does it. Nothing is kept - not the network settings, not the administrator account. What the radio devices remember cannot be undone from here; that is what the notes above the button are about.')}</Help></h2>
<div class="ol-fr" data-section="factory-reset">
    <p>{t('Everything this system holds is erased: users and tokens, names, rooms and functions, addons with their settings, certificates, the interface configuration, the pairings kept by rfd and hmipserver, the BidCos security key and the HmIP network key. Afterwards it starts as new with its first-start page. Download a backup first; a restore brings all of it back.')}</p>
    {#if loadError}
        <Notice kind="warning" id="factory-reset-unknown">{t('What is still paired could not be checked:')} {loadError}</Notice>
    {:else if view}
        {#if view.armed}
            <Notice kind="warning" id="factory-reset-armed">
                {t('A factory reset is set for the next boot; the system is erased when it starts the next time.')}
                {#snippet actions()}<button type="button" class="hmm-button" data-action="factory-reset-cancel" disabled={busy !== ''} onclick={cancel}>{t('Cancel the reset')}</button>{/snippet}
            </Notice>
        {/if}
        {#if view.update_staged}
            <Notice kind="warning" id="factory-reset-update-staged" href="/system/updates" label={t('Updates')}>{t('A system update is set to install at the next boot; install or discard it first, the reset is refused meanwhile.')}</Notice>
        {/if}
        {#if bidcos && !bidcos.known}
            <Notice kind="warning" id="factory-reset-bidcos-unknown">{t('rfd did not answer, so whether BidCos-RF devices are still paired is unknown.')} {#if bidcos.error}<span class="hmm-muted">({bidcos.error})</span>{/if}</Notice>
        {:else if keyWarning}
            <Notice kind="error" id="factory-reset-bidcos-key">
                <strong>{t('{n} BidCos-RF devices are still paired', {n: bidcos!.devices})}</strong>{view!.security_key_known ? t(', and this system uses an individual BidCos security key.') : t(', and whether this system uses an individual BidCos security key could not be checked.')}
                {t('After the reset the devices still use that key: without it they cannot be paired again. They should be unpaired from this system first!')}
            </Notice>
        {:else if bidcos && bidcos.devices > 0}
            <Notice kind="warning" id="factory-reset-bidcos">{t('{n} BidCos-RF devices are still paired. To pair them again after the reset, unpair them from this system first or reset each on the device itself.', {n: bidcos.devices})}</Notice>
        {/if}
        {#if hmip && !hmip.known}
            <Notice kind="warning" id="factory-reset-hmip-unknown">{t('hmipserver did not answer, so whether HmIP devices are still paired is unknown.')} {#if hmip.error}<span class="hmm-muted">({hmip.error})</span>{/if}</Notice>
        {:else if hmip && hmip.devices > 0}
            <Notice kind="warning" id="factory-reset-hmip">{t('{n} HmIP devices are still paired. After the reset each has to be reset on the device itself before it can be paired again.', {n: hmip.devices})}</Notice>
        {/if}
        {#if view.hmip_local_key}
            <Notice kind="warning" id="factory-reset-local-key">{t('This system holds the HmIP network key of local key mode; the reset erases it. The backup downloaded here keeps it (in hmip_user.conf): only with that key does a system reach the HmIP devices paired under it.')}</Notice>
        {/if}
    {/if}
    {#if error}<div class="ol-notice err" role="alert">{error}</div>{/if}
    {#if resetting}
        <div class="ol-notice ol-restorenotice" role="status"><strong>{t('Factory reset - the system reboots now.')}</strong><BootBar entry={resetting} /></div>
    {:else}
        <div class="ol-actions">
            <button type="button" class="hmm-button" data-action="factory-reset-backup" disabled={busy !== ''} onclick={backup}>{t('Download a backup first')}</button>
            <button type="button" class="hmm-button danger" data-action="factory-reset" disabled={busy !== '' || !view || view.update_staged || view.armed} onclick={reset}>{t('Reset to factory settings…')}</button>
        </div>
    {/if}
</div>

<style>
    .ol-fr p { max-width: 720px; }
    .ol-fr .ol-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
    .ol-notice.err { border-left: 3px solid var(--hmm-error); }
</style>
