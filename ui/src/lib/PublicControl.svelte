<script lang="ts">
    /*
     * Task 193 (D-118): the Control app without a login - the switch, the account a request without
     * a session becomes, the warning while it is on, and Apply. On the Remote access page until
     * openccu-lite task 223 (the maintainer, 2026-09-24: "move control without a login to settings
     * page in a smaller panel"); the Settings page shows it to an administrator, and the old
     * /system/remote-access#public leads here (lib/systemmenu.ts). It reads and writes the
     * `public` object of GET/PUT /api/system/v1/remote-access, as before.
     */
    import {onMount} from 'svelte';
    import {api} from './api';
    import {t} from './i18n.svelte';
    import Help from './Help.svelte';
    import {scrollToAnchor} from './anchor';

    interface PublicView { enabled: boolean; account: string; available: boolean }

    let view = $state<PublicView | null>(null);
    let on = $state(false);
    let account = $state('guest');
    let busy = $state(false);
    let notice = $state('');
    let error = $state('');
    const dirty = $derived(!!view && (on !== view.enabled || account.trim() !== view.account));

    function take(v: PublicView) {
        view = v;
        on = v.enabled;
        account = v.account || 'guest';
    }
    onMount(async () => {
        try {
            const r = await api.get<{public?: PublicView}>('/api/system/v1/remote-access');
            take(r.public ?? {enabled: false, account: 'guest', available: false});
        } catch {
            view = {enabled: false, account: 'guest', available: false};
        }
        scrollToAnchor('public');
    });

    async function save() {
        busy = true;
        notice = error = '';
        try {
            const r = await api.put<{public: PublicView}>('/api/system/v1/remote-access', {public: {enabled: on, account: account.trim()}});
            take(r.public);
            notice = on ? t('Saved: Control is public now, in force at once.') : t('Saved: Control is behind the login again.');
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }
</script>

<h2 id="public">{t('Control without a login')}<Help>{t('The Control app - rooms, functions, favorites, switching and dimming - reachable without signing in, while the rest of the system stays behind the login or the identity provider. A request without a session becomes the named account, one that operates and nothing more: no pairing, no rooms, no settings, no administration. The switch is in force at once and needs no restart. Use it on a home network only: keep the web port to the local networks on the Firewall page, and never with this system reachable from the internet, through a port forward, a reverse proxy or a tunnel.')}</Help></h2>
{#if view && !view.available}
    <p class="ol-muted" data-switch="public-unavailable">{t('The public mode is not available on this system.')}</p>
{:else if view}
    <div class="ol-cards">
    <div class="ol-card pc-card" data-switch="public">
        <label class="ol-check pc-toggle"><input type="checkbox" bind:checked={on} disabled={busy} /> <strong>{t('Control is public')}</strong></label>
        <p class="ol-muted pc-text">{t('Anyone who reaches the web port operates the house: every device in every room, without a login.')}</p>
        <div class="ol-labelled pc-account">
            <label for="pc-account">{t('As the account')}</label>
            <input id="pc-account" class="hmm-input hmm-mono" bind:value={account} disabled={busy} autocomplete="off" spellcheck="false" />
            <span class="ol-muted pc-hint">{t('An account that reads or operates, with its own favorites; a name without an account is a virtual one that operates. An administrator is refused.')}</span>
        </div>
        {#if on}<div class="ol-notice error pc-notice" data-notice="ra-public">{t('While this is on, the house is operable by anyone on the network: every switch, every dimmer, every lock that Control shows.')}</div>{/if}
        {#if error}<div class="ol-notice error pc-notice">{error}</div>{/if}
        {#if notice}<div class="ol-notice pc-notice" data-notice="ra-public-saved">{notice}</div>{/if}
        <div class="pc-actions"><button type="button" class="hmm-button" class:primary={dirty} onclick={save} disabled={busy || !dirty || !account.trim()} data-public-save>{t('Apply')}</button></div>
    </div>
    </div>
{/if}

<style>
    /* the maintainer: "a smaller panel" - in the settings' card grid, two of its cards wide (one on
       a phone), so it lines up with the Appearance and Control cards above it */
    @media (min-width: 520px) { .pc-card { grid-column: span 2; } }
    .pc-toggle { display: inline-flex; align-items: center; gap: 8px; font-size: 1.05em; }
    .pc-text { margin: 10px 0 0; }
    .pc-account { margin-top: 14px; }
    .pc-account input { max-width: 16em; }
    .pc-hint { font-size: var(--hmm-font-size-small); }
    .pc-notice { margin: 12px 0 0; }
    .pc-actions { margin-top: 14px; }
    .pc-actions > button { align-self: flex-start; }
</style>
