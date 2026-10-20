<script lang="ts">
    /*
     * openccu-lite task 366 (the maintainer, 2026-10-16): the Hardware page's section for the
     * Raspberry Pi 4/5 bootloader EEPROM. The system never flashes it by itself - the image keeps
     * the update outside the live /boot - so this section shows the running version and the one
     * the image carries, and offers the update while the image's is newer: asked first (a reboot,
     * do not cut the power), staged by the privileged helper, then the reboot counted down as the
     * power menu's (GET /api/system/v1/hardware/bootloader, POST …/update). Hidden where the system
     * has no EEPROM (Pi 3, CCU3, VM, container).
     */
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, ApiError} from '../lib/api';
    import {t} from '../lib/i18n.svelte';
    import {auth} from '../lib/auth.svelte';
    import {ask} from '../lib/dialog.svelte';
    import Help from '../lib/Help.svelte';
    import BootBar from '../lib/BootBar.svelte';
    import {EASE_MS, type BootEntry} from '../lib/bootbar';
    import {beginBoot, removeEntry, watchBoot} from '../lib/bootwatch';
    import {boxUptime} from '../lib/power';

    interface BootloaderView {
        available: boolean;
        reason?: string;
        chip?: string;
        current?: number;
        current_date?: string;
        latest?: number;
        latest_date?: string;
        update: boolean;
        newer?: boolean;
        staged?: boolean;
        vl805?: boolean;
        image_error?: string;
        error?: string;
    }

    let view = $state<BootloaderView | null>(null);
    let error = $state('');
    let busy = $state(false);
    let rebooting = $state<BootEntry | null>(null);
    const admin = $derived(auth.role === 'admin');

    const life = pageLife();
    onMount(() => {
        void load();
        return life.onReturn(() => void load());
    });

    async function load() {
        try {
            view = await api.get<BootloaderView>('/api/system/v1/hardware/bootloader');
        } catch {
            view = null;
        }
    }

    /** a build time as its day and time, UTC, as rpi-eeprom-info prints it */
    function stamp(ts?: number): string {
        return ts ? new Date(ts * 1000).toISOString().replace('T', ' ').replace(/:\d\d\.\d+Z$/, ' UTC') : '';
    }

    async function update() {
        if (!view) return;
        error = '';
        const yes = await ask({
            title: t('Update bootloader'),
            message: [
                t('Install the bootloader of {date} and reboot? The system copies it to the boot partition and reboots; the Raspberry Pi then writes it into its EEPROM, which takes a few seconds longer than a normal start.', {date: view.latest_date ?? ''}),
                t('Do not cut the power until the system is back: an interrupted write can leave the Raspberry Pi unable to start until its EEPROM is recovered from an SD card.'),
            ].join('\n\n'),
            confirm: t('Update and reboot'),
            focusCancel: true,
        });
        if (!yes) return;
        busy = true;
        const before = await boxUptime();
        const entry = await beginBoot('reboot', () => api.get('/api/system/v1/boot-expect?kind=reboot'));
        const wait = () => {
            rebooting = entry;
            watchBoot({entry, before, onUpdate: (e) => (rebooting = e), onBack: () => setTimeout(() => location.reload(), EASE_MS)});
        };
        try {
            await api.post('/api/system/v1/hardware/bootloader/update', {confirm: true});
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
            busy = false;
        }
    }
</script>

{#if view?.available}
    <!-- task 58: a heading and a panel, as the other sections -->
    <h2 id="bootloader">{t('Bootloader')}<Help>{t('The bootloader in the EEPROM of the Raspberry Pi 4 and 5 starts the system before anything on the card. The system never updates it by itself: a newer version that comes with a system update is offered here.')}</Help></h2>
    <section class="ol-card ol-hw-panel" data-panel="bootloader">
        {#if error}<div class="ol-notice error" role="alert">{error}</div>{/if}
        {#if view.error}<div class="ol-notice error" data-bootloader-error>{t('The bootloader update could not be prepared:')} {view.error}</div>{/if}
        <dl class="ol-hw-versions">
            <dt>{t('Installed')}</dt>
            <dd data-bootloader="current" title={stamp(view.current)}>{view.current_date}</dd>
            <dt>{t('In the system image')}</dt>
            <dd data-bootloader="latest" title={stamp(view.latest)}>{view.latest_date || '–'}</dd>
        </dl>
        <p class="ol-muted" data-bootloader-state>
            {#if view.staged}{t('The update is prepared; the Raspberry Pi writes it at the next start.')}
            {:else if view.update}{t('A newer bootloader is available.')}{#if view.vl805}{' '}{t("The USB controller's own EEPROM is updated along with it.")}{/if}
            {:else if view.newer}{t('The installed bootloader is newer than the one in the system image. Nothing to do.')}
            {:else if !view.latest}{t('The system image carries no bootloader update.')}{#if view.image_error}{' '}<span class="hmm-mono">{view.image_error}</span>{/if}
            {:else}{t('The bootloader is up to date.')}{/if}
        </p>
        {#if rebooting}
            <div class="ol-notice" role="status"><strong>{t('The bootloader is updated - the system reboots now.')}</strong><BootBar entry={rebooting} /></div>
        {:else if view.update && !view.staged && admin}
            <button type="button" class="hmm-button" data-action="bootloader-update" disabled={busy} onclick={update}>{t('Update bootloader…')}</button>
        {/if}
    </section>
{/if}

<style>
    .ol-hw-panel { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; }
    .ol-hw-panel > p { margin: 0; }
    .ol-hw-versions { display: grid; grid-template-columns: max-content auto; gap: 4px 16px; margin: 0; }
    .ol-hw-versions dt { color: var(--hmm-fg-muted); }
    .ol-hw-versions dd { margin: 0; font-variant-numeric: tabular-nums; }
</style>
