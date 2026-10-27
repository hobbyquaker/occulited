<script lang="ts" module>
    import {t} from './i18n.svelte';
    import type {Channel} from './updatechannel';

    /** The select's label: the channel's name and what it offers. */
    export function channelLabel(c: Channel): string {
        return c === 'beta' ? t('Beta: betas and release candidates too') : c === 'dev' ? t('Dev: every build') : t('Stable: releases only');
    }

    /** The channel's short name, for a sentence. */
    export function channelName(c: Channel): string {
        return c === 'beta' ? t('Beta') : c === 'dev' ? t('Dev') : t('Stable');
    }
</script>

<script lang="ts">
    /*
     * occulited task 118 (D-122): the update channel - stable (releases only), beta (betas and
     * release candidates too) or dev (every build). Set on the Updates page beside the release
     * check and in the welcome page's update step; stored in occulited.json. Only a newer version
     * than the installed one is ever offered, so switching back to stable while a newer beta runs
     * offers nothing until a newer release is out. Changing the channel asks no server: the page
     * says to check, the check is the user's (D-90).
     */
    import {api} from './api';
    import Help from './Help.svelte';
    import {CHANNELS} from './updatechannel';

    interface Props {
        /** the channel as the page read it */
        channel: Channel;
        disabled?: boolean;
        /** after the channel was stored, with the stored one */
        onchanged?: (channel: Channel) => void;
    }
    let {channel, disabled = false, onchanged}: Props = $props();
    let saving = $state(false);
    let error = $state('');
    // the select's own value while a save runs; the page's value otherwise
    let shown = $state<Channel | null>(null);

    async function change(next: Channel) {
        shown = next;
        saving = true;
        error = '';
        try {
            const r = await api.put<{channel: Channel}>('/api/system/v1/system-update/settings', {channel: next});
            onchanged?.(r.channel);
        } catch (err) {
            error = (err as Error).message;
        } finally {
            saving = false;
            shown = null;
        }
    }
</script>

<span class="ol-channel" data-update-channel-box>
    <label>{t('Update channel')}
        <select value={shown ?? channel} disabled={disabled || saving} onchange={(e) => change((e.currentTarget as HTMLSelectElement).value as Channel)} data-update-channel>
            {#each CHANNELS as c (c)}<option value={c}>{channelLabel(c)}</option>{/each}
        </select></label><Help>{t('Which system updates this system is offered. Stable: releases only (1.0.0, 1.1.0, …). Beta: betas and release candidates too. Dev: the development builds too. Only a version newer than the installed one is offered: back on Stable while a newer beta runs, nothing is offered until a newer release is out.')}</Help>
    {#if error}<span class="ol-notice error" data-notice="update-channel">{error}</span>{/if}
</span>

<style>
    .ol-channel { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; }
    .ol-channel label { display: inline-flex; align-items: center; gap: 6px; }
</style>
