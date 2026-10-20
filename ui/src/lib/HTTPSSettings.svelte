<script lang="ts">
    /*
     * Task 132 (D-87): the HTTP → HTTPS redirect, HSTS (task 36, D-51, task 96) and the redirect of
     * the bare host name (task 74), a section of System → Certificate - on the Security page before.
     * The guard stays where D-51 put it, in the API (HSTS is refused while the certificate is
     * self-signed); the section only does not offer it. `refresh` changes when the page above changed
     * the certificate or switched HSTS off itself, and the section reads the box again.
     */
    import {onMount, untrack} from 'svelte';
    import {api, type HTTPSView} from './api';
    import {ask} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Loading from './Loading.svelte';
    import Help from './Help.svelte';
    import {lookupBoxAddress, type NetworkView} from './recovery';
    import HSTSClearing from './HSTSClearing.svelte';
    import {scrollToAnchor} from './anchor';

    let {refresh = ''}: {refresh?: string} = $props();

    // ---- the redirect and HSTS (task 36) -----------------------------------------------------
    let https = $state<HTTPSView | null>(null);
    let httpsError = $state('');
    let httpsNotice = $state('');
    let httpsNoticeError = $state(false);
    let httpsBusy = $state(false);
    let httpsUnavailable = $state(false);
    let redirect = $state(false);
    let hsts = $state(false);
    // task 96 (D-64): a week unless the box has a value stored
    let days = $state(7);
    // the tick for a certificate occulited did not install (restored from a backup, put there by
    // hand): the page cannot tell whether the browsers trust it, so the user says so
    let trustOwnCA = $state(false);

    function fillHTTPS(v: HTTPSView) {
        https = v;
        redirect = v.redirect_https;
        hsts = v.hsts;
        days = v.hsts_max_age_days;
        fqdn = !!v.redirect_fqdn;
    }
    async function loadHTTPS() {
        try {
            fillHTTPS(await api.get<HTTPSView>('/api/system/v1/https'));
        } catch (e) {
            const msg = (e as Error).message;
            if (msg.includes('501') || msg.includes('not available')) httpsUnavailable = true;
            else httpsError = msg;
        }
    }
    // the certificate rules: HSTS only with a certificate the browser trusts - one the box
    // obtained or installed (managed), or one the user vouches for; never a self-signed one
    const certSelfSigned = $derived(!https?.certificate || https.certificate.self_signed);
    const hstsOffered = $derived(!!https?.certificate && !https.certificate.self_signed && (https.certificate.managed || trustOwnCA));

    async function saveHTTPS() {
        if (!https) return;
        const turningOn = hsts && !https.hsts;
        if (turningOn && !(await confirmHSTS())) return;
        httpsBusy = true;
        httpsNotice = '';
        try {
            fillHTTPS(await api.put<HTTPSView>('/api/system/v1/https', {redirect_https: redirect, hsts, hsts_max_age_days: days, ...fqdnChange()}));
            httpsNotice = t('Saved. lighttpd was reloaded.');
            httpsNoticeError = false;
        } catch (e) {
            httpsNotice = (e as Error).message;
            httpsNoticeError = true;
        } finally {
            httpsBusy = false;
        }
    }

    // ---- the HSTS question: the recovery system by address ----------------------------------
    // Before HSTS goes on, the question says what a browser that remembers it no longer reaches by
    // name (D-56): the recovery system and a system update's install phase are HTTP only, a way
    // back to OpenCCU or a reset comes up self-signed, switching HSTS off later undoes nothing in
    // such a browser, and the IP address always works. The address is resolved as the question
    // opens (lib/recovery.ts); the paragraphs are separated by a blank line.
    async function confirmHSTS(): Promise<boolean> {
        const a = await lookupBoxAddress(location.hostname, () => api.get<{network?: NetworkView}>('/api/system/v1/network'));
        const message = [
            t('Every browser that sees the header will insist on https:// for {days} days and refuse any certificate it does not trust.', {days}),
            a.kind === 'name'
                ? t("The recovery system and the installation of a system update have no HTTPS: while a browser remembers HSTS, it reaches them only by the system's IP address, which could not be read just now; the Network page shows it.")
                : t("The recovery system and the installation of a system update have no HTTPS: while a browser remembers HSTS, it reaches them only by the system's IP address, {url}.", {url: a.url}),
            t('The way back to OpenCCU, a reset and a switch back to a self-signed certificate serve a certificate such a browser does not trust: that locks that browser out of the name for up to {days} days, with no way past.', {days}),
            t('Switching HSTS off later sends max-age=0 for a while, which undoes that only in a browser that opens the system by its name in that time - before a switch back to self-signed or to OpenCCU. The IP address always works. Continue?'),
        ].join('\n\n');
        return ask({title: t('Switch HSTS on'), message, confirm: t('Switch HSTS on'), danger: true});
    }

    // ---- the bare host name redirected to <host>.<domain> (task 74) --------------------------
    // One name for the browser: one session cookie, one HSTS entry, one saved password. The API
    // decides what is possible - a domain, a certificate that covers the name - and says why not;
    // the switch is greyed out with that reason while it is off. On, it can always be switched
    // off. It is saved with the two switches above, and sent only when it changed.
    let fqdn = $state(false);
    const fqdnName = $derived(https?.redirect_fqdn_target ?? `${https?.redirect_fqdn_host ?? ''}.${t('<domain>')}`);
    const fqdnBlocked = $derived(!!https && !https.redirect_fqdn && https.redirect_fqdn_state === 'unavailable');
    const fqdnSuspended = $derived(!!https && https.redirect_fqdn && https.redirect_fqdn_state === 'suspended');
    const fqdnReason = $derived.by(() => {
        switch (https?.redirect_fqdn_reason) {
            case 'no-domain':
                return t('The system knows no DNS domain: /etc/resolv.conf names none (a DHCP server hands it out with the lease).');
            case 'invalid-name':
                return t('{name} is not a DNS name the redirect can use.', {name: fqdnName});
            case 'no-certificate':
                return t('No certificate could be read.');
            case 'not-covered':
                return t('The certificate does not cover {name}, so the redirect would end in a certificate warning.', {name: fqdnName});
            default:
                return '';
        }
    });
    function fqdnChange(): {redirect_fqdn?: boolean} {
        // a daemon without the field counts as off, and nothing is sent unless the switch moved
        return https && fqdn !== !!https.redirect_fqdn ? {redirect_fqdn: fqdn} : {};
    }

    onMount(async () => {
        await loadHTTPS();
        scrollToAnchor('https');
    });
    // the page above changed the certificate: what is offered follows it
    let seen = untrack(() => refresh);
    $effect(() => {
        const r = refresh;
        if (r === seen) return;
        seen = r;
        void loadHTTPS();
    });
</script>

<section class="ol-section" data-section="https">
    <h2 id="https">HTTPS</h2>
    <!-- occulited task 71: the section is one panel under its heading -->
    <div class="ol-panel">
    {#if httpsUnavailable}
        <p class="ol-muted">{t('The HTTPS settings are not available on this system.')}</p>
    {:else if !https}
        <Loading error={httpsError} />
    {:else}
        {#if https.hsts && certSelfSigned}
            <div class="ol-notice error">{t('HSTS is on, but the system serves a self-signed certificate - a browser that has seen the header refuses it. Switch HSTS off below, or install a certificate from a CA above.')}</div>
        {/if}
        {#if httpsNotice}<div class="ol-notice" class:error={httpsNoticeError}>{httpsNotice}</div>{/if}
        <div class="ol-checks">
            <label><input type="checkbox" bind:checked={redirect} /> <strong>{t('Redirect HTTP to HTTPS')}</strong><Help>{t('every request on port 80 from off the system is answered with a redirect to https://; the loopback and the ACME challenge path are left alone')}</Help></label>
            {#if redirect && certSelfSigned}
                <div class="ol-warn sub">{t('The certificate is self-signed: every first visit then shows the browser\'s warning. A certificate from a CA avoids it.')}</div>
            {/if}
            <label><input type="checkbox" bind:checked={hsts} disabled={!hstsOffered} /> <strong>HSTS</strong><Help>{t('Strict-Transport-Security: a browser that has seen the header uses https:// on its own and refuses a certificate it does not trust, for the whole period')}</Help></label>
            {#if certSelfSigned}
                <div class="ol-muted sub">{t('Not offered while the certificate is self-signed: a browser that has seen the header would refuse it, and you would be locked out for the whole period.')}</div>
            {:else if !https.certificate?.managed}
                <label class="sub"><input type="checkbox" bind:checked={trustOwnCA} /> {t('This certificate is from a CA my browsers trust')} <span class="ol-muted">{t('(it was not installed by this system, so it cannot tell)')}</span></label>
            {/if}
            {#if hsts}
                <!-- the unit stays beside the input; the advice on the period is behind the ? -->
                <label class="sub days"><span>{t('Period')}</span> <input class="hmm-input" type="number" min="1" max="730" bind:value={days} /> <span class="ol-muted">{t('days')}</span><Help>{t('days; max-age. A week is the default: every visit renews it, and a browser locked out after a way back or a reset gets in again a week after its last visit. Two years at most.')}</Help></label>
            {:else if https.hsts}
                <!-- task 96: switching off clears instead of sending nothing -->
                <div class="ol-muted sub">{t('Saving switches HSTS off: the system then sends max-age=0 for as long as the period was, at most 30 days, so that browsers forget it.')}</div>
            {:else if https.hsts_clearing}
                <div class="ol-notice sub"><HSTSClearing view={https} lead={t('HSTS is off.')} /></div>
            {/if}
            <!-- the bare host name redirected to <host>.<domain>, with the real names -->
            <label><input type="checkbox" bind:checked={fqdn} disabled={fqdnBlocked} /> <strong>{t('Redirect https://{host} to https://{fqdn}', {host: https.redirect_fqdn_host, fqdn: fqdnName})}</strong><Help>{t('A browser that comes by the short name moves on to the full one, so the login, saved passwords and HSTS belong to one name. Only page visits are redirected (GET and HEAD, not /api/): scripts, Node-RED and addons that talk to the API keep working. The loopback, IP addresses and the ACME challenge path are left alone.')}</Help></label>
            {#if fqdnBlocked}
                <div class="ol-muted sub">{t('Not available:')}{' '}{fqdnReason}</div>
            {:else if fqdnSuspended}
                <div class="ol-warn sub">{t('Suspended:')}{' '}{fqdnReason}{' '}{t('It resumes by itself once a certificate for {name} is installed - with ACME at the next renewal, or order it now above.', {name: fqdnName})}</div>
            {:else if fqdn && certSelfSigned}
                <div class="ol-warn sub">{t('The certificate is self-signed: every first visit then shows the browser\'s warning. A certificate from a CA avoids it.')}</div>
            {/if}
        </div>
        <div class="ol-actions" style="margin-top:10px">
            <button class="hmm-button primary" onclick={saveHTTPS} disabled={httpsBusy || (hsts && !hstsOffered)}>{t('Save')}</button>
        </div>
    {/if}
    </div>
</section>

<style>
    .ol-checks { display: flex; flex-direction: column; gap: 8px; margin: 8px 0 4px; max-width: 760px; }
    .ol-checks label { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .ol-checks .sub { margin-left: 26px; font-size: var(--hmm-font-size-small); }
    .ol-checks .days input { width: 6em; }
</style>
