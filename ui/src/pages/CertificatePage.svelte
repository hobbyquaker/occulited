<script lang="ts">
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type CertStatus, type CertSettings, type CertProvider, type CertInspect, type CertManualResult, type CertPending, type CertInfo, type HTTPSView} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {clearingUntil} from '../lib/hsts';
    import HSTSClearing from '../lib/HSTSClearing.svelte';
    import {t} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {auth} from '../lib/auth.svelte';
    import {link, replace, router} from '../lib/router.svelte';
    import {untrack} from 'svelte';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import ACMETrustList from '../lib/ACMETrustList.svelte';
    import RunLog from '../lib/RunLog.svelte';
    import Disclosure from '../lib/Disclosure.svelte';
    import HTTPSSettings from '../lib/HTTPSSettings.svelte';
    import {warnEdge, warnFor} from '../lib/systemmenu.svelte';

    /*
     * Task 35 (D-48): the box's TLS certificate - S50lighttpd's self-signed one, or one from an
     * ACME CA (Let's Encrypt, ZeroSSL, a LAN CA such as step-ca) that occulited issues and renews.
     * What the box serves now, the mode switch, the ACME settings, three buttons and the last
     * attempt's log lines. The secrets (the EAB HMAC, a DNS provider's token) are write-only: the
     * answer says whether one is set, the field stays empty, and an empty field on save keeps the
     * stored value.
     *
     * Task 38: the third mode, manual - the user's own certificate, uploaded as files (PEM or DER)
     * or pasted into tall PEM fields with a parsed preview under each, and a key + CSR made on the
     * box whose certificate then comes back the same way. The key never leaves the box.
     *
     * Task 132 (D-87): the HTTP → HTTPS redirect, HSTS and the bare-host redirect are this page's
     * last section (lib/HTTPSSettings.svelte, `#https`), no longer on a Security page of their own.
     * What it offers depends on the certificate, so it reads the box again whenever the certificate
     * above changed or this page switched HSTS off before a switch to self-signed.
     */
    let status = $state<CertStatus | null>(null);
    let error = $state('');
    let notice = $state('');
    let noticeError = $state(false);
    let busy = $state('');
    // task 96: HSTS switched off here before a switch to self-signed - what the box sends and the names to visit
    let hstsOff = $state<HTTPSView | null>(null);
    let hstsOffTick = $state(0);
    // the form: the settings as the API answered them, plus the secrets typed here
    let mode = $state<'self-signed' | 'acme' | 'manual'>('self-signed');
    let form = $state<CertSettings | null>(null);
    // task 231: bumps after a save, so the ACME trust list below the CA root field re-reads
    let savedTick = $state(0);
    let namesText = $state('');
    let eabHmac = $state('');
    let creds = $state<Record<string, string>>({});
    let poll: ReturnType<typeof setInterval> | null = null;

    // task 57: the mode the reader is looking at is in the query string (`?mode=acme`), so the ACME
    // form can be linked to; read on arrival, written with replaceState while the radios change, so
    // Back does not walk through them. The box's own mode is the page's plain path.
    type Mode = 'self-signed' | 'acme' | 'manual';
    const isMode = (v: string | null): v is Mode => v === 'self-signed' || v === 'acme' || v === 'manual';
    const life = pageLife();
    let linkedMode: Mode | null = isMode(new URLSearchParams(router.search).get('mode')) ? (new URLSearchParams(router.search).get('mode') as Mode) : null;
    $effect(() => {
        const m = mode;
        const s = status;
        if (!s) return;
        untrack(() => {
            if (!life.active) return; // task 177: never another page's query
            const want = m === s.settings.mode ? '' : `?mode=${m}`;
            if (want !== router.search) replace(router.path + want + location.hash);
        });
    });

    function fill(s: CertStatus) {
        form = {...s.settings, names: [...s.settings.names], dns_credentials: {...s.settings.dns_credentials}, dns_secrets_set: {...s.settings.dns_secrets_set}};
        mode = linkedMode ?? s.settings.mode;
        linkedMode = null;
        namesText = s.settings.names.join(', ');
        creds = {...s.settings.dns_credentials};
        eabHmac = '';
    }

    async function load(refill = false) {
        try {
            const s = await api.get<CertStatus>('/api/system/v1/certificate');
            status = s;
            if (refill || !form) fill(s);
            error = '';
            // while an attempt runs, the page follows it
            if (s.running && !poll) poll = setInterval(() => life.active && void load(), 2000);
            if (!s.running && poll) {
                clearInterval(poll);
                poll = null;
                if (s.last) {
                    notice = s.last.ok ? doneText(s.last.kind) : t('{kind} failed: {error}', {kind: kindText(s.last.kind), error: s.last.error ?? ''});
                    noticeError = !s.last.ok;
                }
            }
        } catch (e) {
            error = (e as Error).message;
        }
    }
    onMount(() => {
        void load(true);
        return () => {
            if (poll) clearInterval(poll);
        };
    });

    const provider = $derived<CertProvider | undefined>(status?.providers.find((p) => p.id === form?.dns_provider));
    // while no names are stored, the box proposes its FQDN and its host name; an empty field on the
    // ACME form takes them when saved
    const suggested = $derived(status?.suggested_names ?? []);
    const typedNames = () => namesText.split(/[\s,]+/).filter(Boolean);
    const kindText = (k: string) => (k === 'test' ? t('Test') : k === 'issue' ? t('Issue') : t('Renewal'));
    const doneText = (k: string) => (k === 'test' ? t('The test passed: the CA issued a certificate for these names; nothing was installed.') : k === 'issue' ? t('Issued and installed.') : t('Renewed and installed.'));
    const days = (n: number) => (n < 0 ? t('expired') : n === 1 ? t('1 day left') : t('{n} days left', {n}));
    // the issuer as a person reads it: the common name and the organisation. The whole DN - the
    // e-mail attribute a step-ca adds is hex in it - is the tooltip, and what shows when the answer
    // names neither.
    const issuerName = (c: CertInfo) => [c.issuer_cn, c.issuer_org !== c.issuer_cn ? c.issuer_org : ''].filter(Boolean).join(' · ') || c.issuer;
    const when = (s?: string) => (s ? new Date(s).toLocaleString() : '');
    const date = (s?: string) => (s ? new Date(s).toLocaleDateString() : '');

    // a field with options (OVH's endpoint) starts on its first one when the provider is chosen
    function choiceDefaults(id?: string): Record<string, string> {
        const out: Record<string, string> = {};
        for (const f of status?.providers.find((p) => p.id === id)?.fields ?? []) {
            const first = f.options?.[0];
            if (first) out[f.key] = first;
        }
        return out;
    }

    function body() {
        if (!form) return null;
        const dns_credentials: Record<string, string> = {};
        for (const f of provider?.fields ?? []) dns_credentials[f.key] = creds[f.key] || f.options?.[0] || '';
        return {
            mode,
            directory: form.directory,
            directory_url: form.directory_url,
            ca_root: form.ca_root,
            email: form.email,
            eab_kid: form.eab_kid,
            eab_hmac: eabHmac,
            names: typedNames().length || mode !== 'acme' ? typedNames() : suggested,
            challenge: form.challenge,
            dns_provider: form.challenge === 'dns-01' ? form.dns_provider : '',
            dns_credentials,
        };
    }

    // forTest: a Test must not switch the box to ACME - the settings are saved with the stored
    // mode (the service validates them as if ACME for the run) and the form stays open
    async function save(forTest = false): Promise<boolean> {
        const b = body();
        if (!b) return false;
        if (forTest && b.mode === 'acme' && status?.settings.mode !== 'acme') b.mode = 'self-signed';
        busy = 'save';
        notice = '';
        try {
            // switching back to self-signed is its own route: it removes the live file
            if (b.mode === 'self-signed' && status?.settings.mode !== 'self-signed') {
                // task 96 (D-64): max-age=0 reaches a browser only over the certificate it trusts, so
                // HSTS goes off in a step of its own before the certificate changes, with a visit by
                // name in every browser in between. The API still takes HSTS along for a script.
                const hv = await api.get<HTTPSView>('/api/system/v1/https').catch(() => null);
                if (hv?.hsts) {
                    const first = [
                        t('HSTS is on: a browser that has seen the header refuses the self-signed certificate under this name for up to {days} days, with no way past.', {days: hv.hsts_max_age_days}),
                        t('Switch HSTS off first: the system then sends max-age=0 while it still serves this certificate, and a browser that opens it once by its name forgets HSTS. Do that in every browser you use, then switch to self-signed.'),
                    ].join('\n\n');
                    if (await ask({title: t('Switch HSTS off first'), message: first, confirm: t('Switch HSTS off now')})) {
                        // the redirect switches stay as they are; redirect_fqdn is left out, so it is kept
                        hstsOff = await api.put<HTTPSView>('/api/system/v1/https', {redirect_https: hv.redirect_https, hsts: false, hsts_max_age_days: hv.hsts_max_age_days});
                        hstsOffTick++;
                    }
                    return false;
                }
                const question = [t('Remove the CA certificate? /etc/config/server.pem is deleted and lighttpd reloaded; the system then generates a new self-signed certificate (ten years, host name and address), and the confined addons of the certs group are restarted. The ACME settings and the account are kept; HSTS, if on, is switched off.')];
                if (hv?.hsts_clearing) {
                    const until = clearingUntil(hv);
                    question.push((until ? t('HSTS is off and the system sends max-age=0 until {date}.', {date: until.toLocaleDateString()}) : t('HSTS is off and the system sends max-age=0.')) + ' ' + t('A browser that has not opened the system by its name since HSTS was switched off refuses the self-signed certificate under that name: open it once in every browser you use before you continue. The IP address always works.'));
                }
                if (!(await ask({title: t('Back to self-signed'), message: question.join('\n\n'), confirm: t('Back to self-signed'), danger: true}))) return false;
                hstsOff = null;
                // the other fields are saved first, so nothing typed is lost
                await api.put<CertStatus>('/api/system/v1/certificate/settings', {...b, mode: status?.settings.mode}).catch(() => undefined);
                const r = await api.post<{status: CertStatus; restarted_addons: string[]; hsts_disabled?: boolean}>('/api/system/v1/certificate/self-signed');
                status = r.status;
                fill(r.status);
                notice = (r.restarted_addons.length ? t('Self-signed again. Restarted: {list}.', {list: r.restarted_addons.join(', ')}) : t('Self-signed again.')) + (r.hsts_disabled ? ' ' + t('HSTS was switched off.') : '');
                noticeError = false;
                return true;
            }
            // mode manual: Save installs what the fields hold (or only records the mode when
            // they are empty and a certificate is already installed by hand)
            if (b.mode === 'manual' && !forTest) return await saveManual(b);
            status = await api.put<CertStatus>('/api/system/v1/certificate/settings', b);
            savedTick++; // task 231: a pasted CA root is in the ACME trust store now
            fill(status);
            if (forTest) mode = 'acme';
            notice = t('Saved.');
            noticeError = false;
            return true;
        } catch (e) {
            notice = (e as Error).message;
            noticeError = true;
            return false;
        } finally {
            busy = '';
        }
    }

    async function start(kind: 'test' | 'issue' | 'renew') {
        if (!(await save(kind === 'test'))) return;
        if (kind === 'issue' && !(await ask({title: t('Issue now'), message: t('Order a certificate from {dir} for {names} and install it? lighttpd is reloaded and the confined addons of the certs group are restarted.', {dir: dirName(form?.directory ?? ''), names: namesText.trim() || suggested.join(', ')}), confirm: t('Issue now')}))) return;
        busy = kind;
        notice = '';
        try {
            status = await api.post<CertStatus>(`/api/system/v1/certificate/${kind}`);
            if (!poll) poll = setInterval(() => life.active && void load(), 2000);
        } catch (e) {
            notice = (e as Error).message;
            noticeError = true;
        } finally {
            busy = '';
        }
    }
    const dirName = (d: string) => (d === 'letsencrypt' ? "Let's Encrypt" : d === 'letsencrypt-staging' ? "Let's Encrypt (staging)" : d === 'zerossl' ? 'ZeroSSL' : form?.directory_url || t('custom CA'));
    const running = $derived(status?.running ?? null);
    const shown = $derived(status?.running ?? status?.last ?? null);
    // what the HTTPS section's offer depends on: the certificate served and who installed it
    const httpsKey = $derived(`${status?.current?.fingerprint ?? ''}|${status?.managed ?? ''}|${status?.managed_mode ?? ''}|${hstsOffTick}`);

    // ---- task 38: the parsed preview under a PEM field -----------------------------------------
    // one request for the whole manual section (the key's match needs the certificate), one for
    // the ACME CA root; debounced, the newest answer wins
    let manCert = $state('');
    let manChain = $state('');
    let manKey = $state('');
    let manPreview = $state<CertInspect | null>(null);
    let rootPreview = $state<CertInspect | null>(null);
    let inspectTimer: ReturnType<typeof setTimeout> | null = null;
    let inspectSeq = 0;
    const lines = (s: string) => (s.trim() ? s.trim().split('\n').length : 0);
    function inspectSoon() {
        if (inspectTimer) clearTimeout(inspectTimer);
        inspectTimer = setTimeout(() => void inspectNow(), 350);
    }
    async function inspectNow() {
        // the preview is a POST, an administrator's: a user's session sees neither the fields nor
        // their previews, and was refused with a 403 in the console for the CA root's on every visit
        if (auth.role !== 'admin') return;
        const seq = ++inspectSeq;
        const man = manCert.trim() || manChain.trim() || manKey.trim();
        const root = form?.directory === 'custom' && form.ca_root.trim();
        const [m, r] = await Promise.all([
            man ? api.post<CertInspect>('/api/system/v1/certificate/inspect', {certificate: manCert, chain: manChain, key: manKey}).catch(() => null) : Promise.resolve(null),
            root ? api.post<CertInspect>('/api/system/v1/certificate/inspect', {certificate: form?.ca_root ?? '', chain: '', key: ''}).catch(() => null) : Promise.resolve(null),
        ]);
        if (seq !== inspectSeq) return;
        manPreview = m;
        rootPreview = r;
    }
    $effect(() => {
        // the fields are the dependencies; the request follows after the pause
        void manCert;
        void manChain;
        void manKey;
        void form?.ca_root;
        void form?.directory;
        inspectSoon();
    });

    // ---- task 38: the files ---------------------------------------------------------------------
    let fileCert = $state<FileList | null>(null);
    let fileChain = $state<FileList | null>(null);
    let fileKey = $state<FileList | null>(null);
    const anyFile = $derived(!!(fileCert?.length || fileChain?.length || fileKey?.length));
    const anyPaste = $derived(!!(manCert.trim() || manChain.trim() || manKey.trim()));

    async function saveManual(b: NonNullable<ReturnType<typeof body>>): Promise<boolean> {
        if (!anyFile && !anyPaste) {
            if (status?.settings.mode === 'manual') {
                status = await api.put<CertStatus>('/api/system/v1/certificate/settings', b);
            savedTick++; // task 231: a pasted CA root is in the ACME trust store now
                fill(status);
                notice = t('Saved.');
                noticeError = false;
                return true;
            }
            notice = t('Nothing to install: upload or paste a certificate (and its key, unless the system generated one), or generate a key and request first.');
            noticeError = true;
            return false;
        }
        const c = status?.current;
        if (c && !c.self_signed && !(await ask({title: t('Replace the installed certificate'), message: t('Replace the certificate the system serves ({subject}, until {date}) with the one given here? lighttpd is reloaded and the confined addons of the certs group are restarted.', {subject: c.subject, date: date(c.not_after)}), confirm: t('Replace')}))) return false;
        let r: CertManualResult;
        if (anyFile) {
            const fd = new FormData();
            if (fileCert?.[0]) fd.append('certificate', fileCert[0]);
            else fd.append('certificate', manCert);
            if (fileChain?.[0]) fd.append('chain', fileChain[0]);
            else fd.append('chain', manChain);
            if (fileKey?.[0]) fd.append('key', fileKey[0]);
            else fd.append('key', manKey);
            r = await api.putForm<CertManualResult>('/api/system/v1/certificate/manual', fd);
        } else {
            r = await api.put<CertManualResult>('/api/system/v1/certificate/manual', {certificate: manCert, chain: manChain, key: manKey});
        }
        status = r.status;
        fill(r.status);
        manCert = manChain = manKey = '';
        fileCert = fileChain = fileKey = null;
        manPreview = null;
        const parts = [t('Installed: {subject}, until {date}.', {subject: r.result.certificate.subject, date: date(r.result.certificate.not_after)})];
        if (r.restarted_addons.length) parts.push(t('Restarted: {list}.', {list: r.restarted_addons.join(', ')}));
        if (r.warning) parts.push(t('Warning') + ': ' + r.warning);
        notice = parts.join(' ');
        noticeError = false;
        return true;
    }

    // ---- task 38: the key and the request ------------------------------------------------------
    let keyAlg = $state<'p256' | 'p384' | 'rsa2048'>('p256');
    let keyCN = $state('');
    let keySANs = $state('');
    let keyOrg = $state('');
    let csrOpen = $state(false);
    // task 268: Show text becomes the panel with the request's text; the panel's Close brings it back
    let csrButton = $state<HTMLButtonElement | null>(null);
    let copied = $state(false);
    const pending = $derived<CertPending | null>(status?.pending ?? null);
    $effect(() => {
        // the common name defaults to what the box is reached by: the pending request's, the
        // ACME names' first, or the address bar
        if (keyCN) return;
        keyCN = pending?.cn ?? status?.settings.names[0] ?? status?.suggested_names?.[0] ?? (typeof location !== 'undefined' && !/^[\d.:]+$/.test(location.hostname) ? location.hostname : '');
    });
    async function generateKey() {
        if (pending && !(await ask({title: t('Replace the pending key'), message: t('A key and request for {cn} ({alg}, made {when}) are waiting for their certificate. Generate a new pair? The old key is discarded and a certificate signed for the old request can no longer be installed.', {cn: pending.cn, alg: pending.algorithm, when: when(pending.created)}), confirm: t('Generate anyway'), danger: true}))) return;
        busy = 'key';
        notice = '';
        try {
            const r = await api.post<{pending: CertPending; csr: string; status: CertStatus}>('/api/system/v1/certificate/key', {algorithm: keyAlg, cn: keyCN, sans: keySANs.split(/[\s,]+/).filter(Boolean), org: keyOrg});
            status = r.status;
            csrOpen = true;
            notice = t('Key and request generated ({alg}, {fp}). Download the request, have your CA sign it, and install the certificate above; the key stays on the system.', {alg: r.pending.algorithm, fp: short(r.pending.fingerprint)});
            noticeError = false;
        } catch (e) {
            notice = (e as Error).message;
            noticeError = true;
        } finally {
            busy = '';
        }
    }
    const short = (fp: string) => fp.split(':').slice(0, 6).join(':') + '…';
    async function copyCSR() {
        if (!pending?.csr) return;
        try {
            await navigator.clipboard.writeText(pending.csr);
            copied = true;
            setTimeout(() => (copied = false), 2000);
        } catch {
            /* no clipboard: the text is selectable */
        }
    }
    const modeText = (s: CertStatus) => {
        const c = s.current;
        if (!c) return '';
        if (s.managed) return s.managed_mode === 'manual' ? t('manual, installed by hand') : t('ACME, issued by this system');
        return c.self_signed ? t('self-signed') : t('from a CA');
    };
</script>

{#snippet certPreview(p: CertInspect | null, text: string, what: 'certificate' | 'chain' | 'key')}
    <div class="pv" data-preview={what}>
        <span class="ol-muted">{lines(text) ? t('{n} lines', {n: lines(text)}) : t('empty')}</span>
        {#if p && text.trim()}
            {#if what === 'certificate'}
                {#if p.certificate}
                    <span class="hmm-mono pv-sub" title={p.certificate.subject}>{p.certificate.subject}</span>
                    <span>{t('issued by')} <span class="pv-sub" title={p.certificate.issuer}>{issuerName(p.certificate)}</span></span>
                    <span>{t('valid {from} – {until}', {from: date(p.certificate.not_before), until: date(p.certificate.not_after)})} · {days(p.certificate.days_left)}</span>
                    {#if p.certificate.names.length}<span class="ol-muted hmm-mono">{p.certificate.names.join(', ')}</span>{/if}
                    {#if p.validity}<span class="ol-warn">{p.validity}</span>{/if}
                    {#if p.pending_matches === true}<span class="pv-good">{t("the system's pending key belongs to this certificate")}</span>{/if}
                    {#if p.pending_matches === false && !p.key}<span class="ol-warn">{t("the system's pending key does not belong to this certificate")}</span>{/if}
                {:else if p.certificate_error}
                    <span class="ol-warn">{p.certificate_error}</span>
                {/if}
            {:else if what === 'chain'}
                {#if p.certificate && (p.chain ?? 0) > 1}
                    <span>{t('{n} certificates', {n: p.chain ?? 0})}</span>
                    {#if p.chain_warning}<span class="ol-warn">{p.chain_warning}</span>{/if}
                {:else if p.certificate_error && p.certificate_error.includes('chain')}
                    <span class="ol-warn">{p.certificate_error}</span>
                {/if}
            {:else if p.key}
                <span>{p.key.algorithm} · <span class="hmm-mono pv-sub" title={p.key.fingerprint}>{p.key.fingerprint}</span></span>
                {#if p.matches === true}<span class="pv-good">{t('belongs to the certificate')}</span>{/if}
                {#if p.matches === false}<span class="ol-warn">{t('does not belong to the certificate')}</span>{/if}
            {:else if p.key_error}
                <span class="ol-warn">{p.key_error}</span>
            {/if}
        {/if}
    </div>
{/snippet}

<!-- task 51: every explanation of this page is behind a ? - after a heading, after the label of
     the field it is about, after the name of a mode. Task 132: the HTTPS switches are this page's
     own last section, so the heading points nowhere else any more. -->
<SystemTitle />
{#if !status || !form}
    <Loading {error} />
{:else}
    {#if status.warning === 'expiring' && status.settings.mode === 'manual'}
        <div class="ol-notice error">{t('The certificate expires soon ({days}). Nobody renews a manually installed certificate: have your CA issue a new one and install it below.', {days: days(status.current?.days_left ?? 0)})}</div>
    {:else if status.warning === 'expiring'}
        <div class="ol-notice error">{t('The certificate expires soon ({days}). The renewal runs twice a day below {n} days; if it keeps failing, the lines below say why.', {days: days(status.current?.days_left ?? 0), n: status.renew_below_days})}</div>
    {:else if status.warning === 'last-attempt-failed'}
        <div class="ol-notice error">{t('The last renewal failed: {error}. The current certificate stays in service; the next attempt is in at most twelve hours, or press Renew now.', {error: status.last?.error ?? ''})}</div>
    {:else if status.warning === 'self-signed' && status.settings.mode === 'manual'}
        <div class="ol-notice error">{t('The mode is manual but the system serves a self-signed certificate: nothing has been installed yet, or the system regenerated one. Install a certificate below.')}</div>
    {:else if status.warning === 'self-signed'}
        <div class="ol-notice error">{t('The mode is ACME but the system serves a self-signed certificate: nothing has been issued yet, or the system regenerated one. Press Issue now.')}</div>
    {/if}
    {#if notice}<div class="ol-notice" class:error={noticeError}>{notice}</div>{/if}
    {#if hstsOff?.hsts_clearing}<div class="ol-notice ol-hsts-off"><HSTSClearing view={hstsOff} lead={t('HSTS is off.')} /> {t('Then switch to self-signed.')}</div>{/if}

    <!-- occulited task 12: the Status page's certificate warning (expiring, expired, renewal failed) -
         on the section's panel. Task 71 (the maintainer: "page should be grouped into panels"): each
         section one panel under its heading, as the Backup page (task 59) -->
    <h2 id="current">{t('Current certificate')}</h2>
    <div class="ol-panel {warnEdge(['certificate'])}" data-section="current" {...warnFor(['certificate'])}>
    {#if status.current}
        {@const c = status.current}
        {@const names = (c.names.length ? c.names.join(', ') : c.subject) + (c.ips?.length ? ' · ' + c.ips.join(', ') : '')}
        <div class="ol-cards">
            <div class="ol-card"><div class="k">{t('Mode')}</div><div class="v">{modeText(status)}{#if status.managed}<Help>{status.managed_mode === 'manual' ? t('managed: installed by hand, the init script leaves it alone') : t('managed: the system renews it, the init script leaves it alone')}</Help>{/if}</div></div>
            <div class="ol-card"><div class="k">{t('Names')}</div><div class="v hmm-mono ol-clamp" title={names}>{names}</div></div>
            <div class="ol-card"><div class="k">{t('Issuer')}</div><div class="v ol-clamp" title={c.issuer}>{issuerName(c)}</div></div>
            <div class="ol-card"><div class="k">{t('Valid until')}</div><div class="v">{new Date(c.not_after).toLocaleDateString()} <span class="ol-badge" class:bad={c.days_left < 14} class:good={c.days_left >= 14}>{days(c.days_left)}</span></div></div>
            <div class="ol-card"><div class="k">{t('Fingerprint (SHA-256)')}</div><div class="v hmm-mono ol-clamp" title={c.fingerprint}>{c.fingerprint}</div></div>
        </div>
    {:else}
        <p class="ol-muted">{t('No certificate could be read')}{status.current_error ? `: ${status.current_error}` : ''}.</p>
    {/if}
    {#if status.next_check && status.settings.mode === 'acme'}
        <p class="ol-muted">{t('Next renewal check: {when}. Renewal below {n} days.', {when: when(status.next_check), n: status.renew_below_days})}</p>
    {/if}
    </div>
    {#if auth.role === 'admin'}
        <!-- the section's ? explains what Save and the buttons do in the mode that is chosen -->
        <h2>{t('Settings')}{#if mode === 'acme'}<Help><p>{t('The whole flow against the staging directory (or the custom CA); nothing is installed.')}</p><p>{t('Test runs against the staging directory without switching the system; Save with mode ACME, then Issue now, installs for real.')}</p></Help>{:else if mode === 'manual'}<Help><p>{t('Certificate, chain and key as files (PEM or DER) or pasted as PEM; the chain may be appended to the certificate instead. The key can be left out when the system generated it below and the certificate was signed for that request. Install writes it to the system.')}</p><p>{t('The chosen files are sent as they are; a pasted text beside a file is sent for the fields without a file.')}</p></Help>{:else if status.settings.mode !== 'self-signed'}<Help>{t('Saving removes the CA certificate and lets the system generate a self-signed one; the ACME settings are kept for the next switch.')}</Help>{/if}</h2>
        <div class="ol-panel" data-section="settings">
        <div class="ol-radios">
            <label><input type="radio" bind:group={mode} value="self-signed" /> <strong>{t('Self-signed')}</strong><Help>{t('generated by the system, ten years, host name and address; browsers warn')}</Help></label>
            <label><input type="radio" bind:group={mode} value="acme" /> <strong>ACME</strong><Help>{t("Let's Encrypt, ZeroSSL, or a LAN CA such as step-ca; renewed by the system below {n} days", {n: status.renew_below_days})}</Help></label>
            <label><input type="radio" bind:group={mode} value="manual" /> <strong>{t('Manual')}</strong><Help>{t('your own certificate - from a company CA, bought, or made elsewhere - uploaded or pasted; or a key and request made here for your CA to sign')}</Help></label>
        </div>
        {#if mode === 'acme'}
            <div class="ol-form ol-certform">
                <label><span>{t('Directory')}</span>
                    <select class="hmm-select" bind:value={form.directory}>
                        <option value="letsencrypt">Let's Encrypt</option>
                        <option value="letsencrypt-staging">Let's Encrypt (staging)</option>
                        <option value="zerossl">ZeroSSL</option>
                        <option value="custom">{t('custom (step-ca, a company CA)')}</option>
                    </select>
                </label>
                {#if form.directory === 'custom'}
                    <label><span>{t('Directory URL')}</span><input class="hmm-input hmm-mono" bind:value={form.directory_url} placeholder="https://ca.lan:9000/acme/acme/directory" /></label>
                    <!-- a label with a ? in it names the field by `for`: without it the ? would be
                         the first labelable child, and the label would name that -->
                    <label class="tall" for="ol-cert-ca-root"><span>{t('CA root (PEM)')}<Help>{t('The CA\'s root certificate: saved into the ACME trust store, which the directory connection trusts besides occulited\'s own store; the system-wide store is not changed. Empty when the CA\'s certificate is one occulited trusts already.')}</Help></span><textarea id="ol-cert-ca-root" class="hmm-input hmm-mono pem ol-pem" rows="30" bind:value={form.ca_root} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false"></textarea></label>
                    <div class="hint">{@render certPreview(rootPreview, form.ca_root, 'certificate')}</div>
                    <!-- openccu-lite task 231: what the ACME store holds, the same list as System → Trust stores -->
                    {#if auth.role === 'admin'}<div class="hint"><ACMETrustList refresh={savedTick} /></div>{/if}
                {/if}
                <label><span>{t('E-mail')}</span><input class="hmm-input" bind:value={form.email} placeholder="admin@example.org" /></label>
                <label for="ol-cert-names"><span>{t('Names')}<Help>{t('The DNS names the system is reached by, the first one the subject. An ACME certificate cannot carry an address.')}</Help></span><input id="ol-cert-names" class="hmm-input hmm-mono" bind:value={namesText} placeholder={suggested.length ? suggested.join(', ') : 'ccu.example.org, ccu.lan'} /></label>
                {#if !namesText.trim() && suggested.length}
                    <div class="hint ol-muted" data-suggested>{suggested.length > 1 ? t('suggested from the host name and the domain: {names}', {names: suggested.join(', ')}) : t('suggested from the host name: {names}', {names: suggested[0] ?? ''})}</div>
                {/if}
                <label><span>{t('EAB key id')}</span><input class="hmm-input hmm-mono" bind:value={form.eab_kid} autocomplete="off" placeholder={t('(optional: ZeroSSL, company CAs)')} /></label>
                <label><span>{t('EAB HMAC')}</span><input class="hmm-input hmm-mono" type="password" bind:value={eabHmac} autocomplete="new-password" placeholder={form.eab_hmac_set ? t('(set — leave empty to keep)') : ''} /></label>
                <!-- not a label around the two labels: a nested label would name the first radio after the whole row -->
                <!-- the ? of Challenge explains the one that is chosen -->
                <div class="frow"><span>{t('Challenge')}<Help>{#if form.challenge === 'http-01'}{t('The CA fetches http://<name>/.well-known/acme-challenge/ on port 80. For Let\'s Encrypt that port must reach the system from the internet: a port forward to it. With HTTP-01 saved, the firewall has a rule \'ACME HTTP-01\' that lets port 80 in from anywhere; lighttpd answers there only the challenge path and the redirect to HTTPS. For a LAN CA the rule can be switched off on the Firewall page')} (<a href="/system/firewall" use:link>{t('Firewall')}</a>). {t('For a LAN CA the firewall allows it as it stands. A system behind NAT is better served by DNS-01.')}{:else}{t('The system puts a TXT record into your zone through the provider\'s API; nothing has to reach it. The choice for a system behind NAT, and the only one for a wildcard.')}{/if}</Help></span>
                    <span class="ol-radios inline">
                        <label><input type="radio" bind:group={form.challenge} value="http-01" /> HTTP-01</label>
                        <label><input type="radio" bind:group={form.challenge} value="dns-01" /> DNS-01</label>
                    </span>
                </div>
                {#if form.challenge === 'dns-01'}
                    <!-- the provider's note (what its token needs, how slow it is) is the provider
                         field's help once one is chosen -->
                    <label for="ol-cert-provider"><span>{t('DNS provider')}{#if provider?.note}<Help>{t(provider.note)}</Help>{/if}</span>
                        <select id="ol-cert-provider" class="hmm-select" bind:value={form.dns_provider} onchange={() => (creds = choiceDefaults(form?.dns_provider))}>
                            <option value="">{t('(choose)')}</option>
                            {#each status.providers as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
                        </select>
                    </label>
                    {#if provider}
                        {#each provider.fields as f (f.key)}
                            {#if f.options?.length}
                            <label><span>{t(f.label)}</span><select class="hmm-select hmm-mono" bind:value={creds[f.key]}>{#each f.options as o (o)}<option value={o}>{o}</option>{/each}</select></label>
                            {:else}
                            <label><span>{t(f.label)}</span><input class="hmm-input hmm-mono" type={f.secret ? 'password' : 'text'} bind:value={creds[f.key]} autocomplete={f.secret ? 'new-password' : 'off'} placeholder={f.secret && status.settings.dns_provider === provider.id && status.settings.dns_secrets_set[f.key] ? t('(set — leave empty to keep)') : (f.placeholder ?? (f.optional ? t('(optional)') : ''))} /></label>
                            {/if}
                        {/each}
                    {/if}
                {/if}
            </div>
        {:else if mode === 'manual'}
            <!-- task 38: the certificate, the chain and the key - each as a file (PEM or DER) or pasted -->
            <div class="man-grid">
                <div class="man-field">
                    <label class="man-head" for="man-cert-file"><strong>{t('Certificate')}</strong> <span class="ol-muted">{t('(the leaf the CA issued)')}</span></label>
                    <input id="man-cert-file" class="hmm-input" type="file" accept=".pem,.crt,.cer,.der,.txt,application/x-x509-ca-cert,application/pkix-cert,application/x-pem-file" bind:files={fileCert} />
                    <textarea class="hmm-input hmm-mono pem ol-pem" rows="30" bind:value={manCert} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false" aria-label={t('Certificate (PEM)')}></textarea>
                    {@render certPreview(manPreview, manCert, 'certificate')}
                </div>
                <div class="man-field">
                    <label class="man-head" for="man-chain-file"><strong>{t('Chain')}</strong> <span class="ol-muted">{t('(the intermediate certificates; optional)')}</span></label>
                    <input id="man-chain-file" class="hmm-input" type="file" accept=".pem,.crt,.cer,.der,.txt,application/x-x509-ca-cert,application/pkix-cert,application/x-pem-file" bind:files={fileChain} />
                    <textarea class="hmm-input hmm-mono pem ol-pem" rows="30" bind:value={manChain} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false" aria-label={t('Chain (PEM)')}></textarea>
                    {@render certPreview(manPreview, manChain, 'chain')}
                </div>
                <div class="man-field">
                    <label class="man-head" for="man-key-file"><strong>{t('Private key')}</strong> <span class="ol-muted">{pending ? t('(optional: the system holds the key of the pending request)') : t('(PKCS#8, PKCS#1 or SEC 1; not encrypted)')}</span></label>
                    <input id="man-key-file" class="hmm-input" type="file" accept=".pem,.key,.der,.txt,application/x-pem-file,application/pkcs8" bind:files={fileKey} />
                    <textarea class="hmm-input hmm-mono pem ol-pem" rows="30" bind:value={manKey} placeholder="-----BEGIN PRIVATE KEY-----" spellcheck="false" aria-label={t('Private key (PEM)')}></textarea>
                    {@render certPreview(manPreview, manKey, 'key')}
                </div>
            </div>
        {/if}
        <div class="ol-actions cert-actions">
            <button class="hmm-button primary" onclick={() => save()} disabled={busy !== '' || !!running}>{mode === 'manual' && (anyFile || anyPaste) ? t('Install') : t('Save')}</button>
            {#if mode === 'acme'}
                <button class="hmm-button" onclick={() => start('test')} disabled={busy !== '' || !!running}>{t('Test')}</button>
                <button class="hmm-button" onclick={() => start('issue')} disabled={busy !== '' || !!running || status.settings.mode !== 'acme'}>{t('Issue now')}</button>
                {#if status.issued}<button class="hmm-button" onclick={() => start('renew')} disabled={busy !== '' || !!running || status.settings.mode !== 'acme'}>{t('Renew now')}</button>{/if}
            {/if}
        </div>

        </div>
        {#if mode === 'manual'}
            <h2>{t('Key and certificate request')}<Help>{t('The system makes a private key and a certificate signing request (CSR) for it; you hand the request to your CA and install the certificate it returns above. The key stays on the system and is never downloadable.')}</Help></h2>
            <div class="ol-panel" data-section="csr">
            {#if pending}
                <div class="ol-notice pend">
                    <div><strong>{t('Pending request')}</strong>: <span class="hmm-mono">{pending.cn}</span>{#if pending.sans.length}{' · '}{pending.sans.join(', ')}{/if}{#if pending.org}{' · '}{pending.org}{/if} · {pending.algorithm} · {t('made {when}', {when: when(pending.created)})}</div>
                    <div class="ol-muted small">{t('Key fingerprint (SHA-256)')}: <span class="hmm-mono pv-sub" title={pending.fingerprint}>{pending.fingerprint}</span></div>
                    <div class="ol-actions pend-actions">
                        <a class="hmm-button primary" href="/api/system/v1/certificate/csr" download={`${pending.cn}.csr`}>{t('Download CSR')}</a>
                        <button class="hmm-button" onclick={copyCSR}>{copied ? t('Copied') : t('Copy CSR')}</button>
                        {#if pending.csr}<button class="hmm-button" aria-expanded={csrOpen} onclick={() => (csrOpen = true)} bind:this={csrButton} data-action="csr-text">{t('Show text')}</button>{/if}
                    </div>
                    <!-- task 98: the request's text opens and closes in place; task 268: as the button's panel -->
                    {#if pending.csr}
                        <Disclosure title={t('The request as text')} bind:open={csrOpen} readOnly trigger={csrButton}>
                            <textarea class="hmm-input hmm-mono pem ol-pem csr" rows="16" readonly value={pending.csr} aria-label={t('Certificate request (PEM)')}></textarea>
                        </Disclosure>
                    {/if}
                </div>
            {/if}
            <div class="ol-form ol-certform">
                <label><span>{t('Key algorithm')}</span>
                    <select class="hmm-select" bind:value={keyAlg}>
                        <option value="p256">ECDSA P-256 ({t('default')})</option>
                        <option value="p384">ECDSA P-384</option>
                        <option value="rsa2048">RSA 2048</option>
                    </select>
                </label>
                <label><span>{t('Common name')}</span><input class="hmm-input hmm-mono" bind:value={keyCN} placeholder="ccu.example.org" /></label>
                <label for="ol-cert-sans"><span>{t('Other names')}<Help>{t('Subject alternative names beside the common name, comma-separated; an address becomes an IP entry. Whether the CA keeps them is the CA\'s business.')}</Help></span><input id="ol-cert-sans" class="hmm-input hmm-mono" bind:value={keySANs} placeholder="ccu.lan, 192.0.2.119" /></label>
                <label><span>{t('Organisation')}</span><input class="hmm-input" bind:value={keyOrg} placeholder={t('(optional)')} /></label>
            </div>
            <div class="ol-actions cert-actions">
                <button class="hmm-button" onclick={generateKey} disabled={busy !== '' || !!running}>{pending ? t('Generate a new key and request') : t('Generate key and request')}</button>
            </div>
            </div>
        {/if}
    {/if}

    {#if shown}
        <h2>{running ? t('Running: {kind}', {kind: kindText(running.kind)}) : t('Last attempt')}</h2>
        <div class="ol-panel" data-section="attempt">
        <div class="ol-toolbar">
            {#if !running}<span class="ol-dot once" class:failed={!shown.ok}></span>{/if}
            <span>{kindText(shown.kind)} · {shown.names.join(', ')} · <span class="hmm-mono">{shown.directory}</span></span>
            <span class="ol-muted">{when(shown.started)}{shown.finished ? ` → ${when(shown.finished)}` : ''}</span>
            {#if !running && shown.installed}<span class="ol-badge good">{t('installed')}</span>{/if}
            {#if shown.restarted_addons?.length}<span class="ol-muted">{t('restarted: {list}', {list: shown.restarted_addons.join(', ')})}</span>{/if}
        </div>
        {#if shown.error}<div class="ol-notice error">{shown.error}</div>{/if}
        {#if shown.warning}<div class="ol-notice"><span class="ol-warn">{t('Warning')}:</span> {shown.warning}</div>{/if}
        <!-- task 102: the attempt's lines are in the journal -->
        <RunLog runId={shown.run_id} running={!!running} cls="cert-log" />
        </div>
    {/if}

    <!-- task 132 (D-87): the redirect, HSTS and the bare-host redirect, an administrator's (#https) -->
    {#if auth.role === 'admin'}
        <HTTPSSettings refresh={httpsKey} />
    {/if}
{/if}

<style>
    .ol-radios { display: flex; flex-direction: column; gap: 8px; margin: 8px 0 12px; }
    .ol-radios label { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .ol-radios.inline { flex-direction: row; gap: 16px; margin: 0; }
    .ol-certform { display: grid; grid-template-columns: max-content minmax(240px, 560px); gap: 8px 12px; align-items: center; margin-bottom: 12px; }
    .ol-certform label, .ol-certform .frow { display: contents; }
    .ol-certform label.tall span { align-self: start; padding-top: 6px; }
    /* the text fields take the column; a radio or a checkbox keeps its own size - with a bare
       `input` here the challenge's HTTP-01/DNS-01 radios were stretched into big circles and their
       names broke onto two lines */
    .ol-certform input:not([type='radio']):not([type='checkbox']), .ol-certform select, .ol-certform textarea { width: 100%; }
    /* the challenge's options: a radio and its name are one unit, not two items of the row
       (`.ol-certform label` is display: contents for the field labels of the grid) */
    .ol-certform .ol-radios label { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    .ol-certform textarea { resize: vertical; font-size: var(--hmm-font-size-small); }
    .ol-certform .hint { grid-column: 2; font-size: var(--hmm-font-size-small); }
    /* the maintainer, 2026-09-19: on a phone the label goes above its field, and the fields start
       at the page's edge instead of behind a label column */
    @media (max-width: 700px) {
        .ol-certform { grid-template-columns: 1fr; gap: 2px 0; }
        .ol-certform label.tall span { padding-top: 0; }
        .ol-certform .hint { grid-column: 1; }
        .ol-certform input:not([type='radio']):not([type='checkbox']), .ol-certform select, .ol-certform textarea, .ol-certform .ol-radios { margin-bottom: 10px; }
    }
    .cert-actions { margin: 4px 0 8px; }
    .small { font-size: var(--hmm-font-size-small); }
    /* task 38: every PEM field tall enough for a whole certificate - the look is app.css's
       textarea.ol-pem, shared with the OIDC trust field (task 235); the preview under it */
    textarea.csr { min-height: 16lh; width: 100%; margin-top: 8px; }
    .man-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(360px, 100%), 1fr)); gap: 12px 16px; margin-bottom: 8px; }
    .man-field { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
    .man-field textarea { width: 100%; box-sizing: border-box; }
    .man-head { display: flex; gap: 6px; flex-wrap: wrap; align-items: baseline; }
    .pv { display: flex; flex-direction: column; gap: 2px; font-size: var(--hmm-font-size-small); min-height: 1.4em; overflow-wrap: anywhere; }
    .pv-sub { overflow-wrap: anywhere; }
    .pv-good { color: var(--hmm-ok, var(--hmm-accent)); font-weight: 600; }
    .pend { display: flex; flex-direction: column; gap: 6px; overflow-wrap: anywhere; }
    .pend-actions { margin-top: 4px; }
</style>
