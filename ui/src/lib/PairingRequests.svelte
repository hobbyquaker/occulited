<script lang="ts">
    // openccu-lite task 219: programs asking for access, on the Status page for an administrator.
    // The maintainer's decisions (2026-09-24): the card shows the code the program also shows, the
    // access it asks for per area (the users' ladder: read, operate, configure, administer) with its
    // own words for why, and one Approve beside Reject - all or nothing. Devices at administer (may
    // pair and delete devices) is shown in red. Live through the pairing stream; nothing to silence.
    import {onMount} from 'svelte';
    import {api} from './api';
    import {confirmTicket, returned} from './confirm';
    import {pageLife} from './pagelife.svelte';
    import {subscribeShell} from './shellstream/client';
    import {t} from './i18n.svelte';
    import {link} from './router.svelte';
    import Notice from './Notice.svelte';

    interface Request {
        id: string; app: string; app_version?: string; instance?: string; name: string; address: string;
        // B-296: null from a system before the fix when the request asks for addons alone
        access: Record<string, string> | null; purpose?: Record<string, string>; scopes: string[]; code: string;
        fingerprint?: string; created: string; expires: string; look_alike?: boolean;
        // openccu-lite task 307: the addons whose pages the program asks to reach through the system
        addons?: {id: string; name: string}[];
        // occulited task 135: a request for backups - approving asks who the administrator is, and
        // the token comes limited to these ranges
        needs_confirmation?: boolean; token_ips?: string[];
    }
    let requests = $state<Request[]>([]);
    let busy = $state('');
    let err = $state('');
    const life = pageLife();

    async function load() {
        try {
            const r = await api.get<{enabled: boolean; requests: Request[]}>('/api/auth/v1/pairing');
            requests = r.requests ?? [];
        } catch {
            /* 501 on a system without pairing, 403 for a non-administrator: no card */
        }
    }
    // the topic pairing of the shell's stream (occulited B-53: one stream for every window of the
    // browser), while the page is shown
    $effect(() => {
        if (!life.active) return;
        return subscribeShell('pairing', (data) => {
            try {
                requests = JSON.parse(data).requests ?? [];
            } catch {
                /* the poll repairs it */
            }
        });
    });
    // back from the identity provider with the confirmation of a request for backups: the fragment
    // says only that, the request is the one noted before the page left
    const RESUME = 'ol.pairing.confirming';
    function remember(id: string) {
        try {
            sessionStorage.setItem(RESUME, id);
        } catch {
            /* then the approval is asked for again */
        }
    }
    function remembered(): string {
        try {
            const id = sessionStorage.getItem(RESUME) ?? '';
            sessionStorage.removeItem(RESUME);
            return id;
        } catch {
            return '';
        }
    }
    async function resume() {
        const id = remembered();
        if (!id) return;
        const back = returned();
        if (back && 'refused' in back) err = t('The confirmation at the identity provider was refused: {reason}', {reason: back.refused});
        await load();
        const r = requests.find((x) => x.id === id);
        if (back && 'ticket' in back) {
            if (r) await approve(r, back.ticket);
            else err = t('The request is gone; the program has to ask again.');
        }
    }

    onMount(() => {
        void resume().then(() => load());
        const poll = setInterval(() => life.active && load(), 15000);
        const stopReturn = life.onReturn(() => void load());
        return () => {
            clearInterval(poll);
            stopReturn();
        };
    });

    async function approve(r: Request, ticket?: string) {
        const path = `/api/auth/v1/pairing/${encodeURIComponent(r.id)}/approve`;
        busy = r.id;
        err = '';
        try {
            await api.postWith(path, {code: r.code}, ticket ? {'X-Occulite-Confirm': ticket} : {});
            requests = requests.filter((x) => x.id !== r.id);
        } catch (e) {
            err = (e as Error).message;
            void load();
        } finally {
            busy = '';
        }
    }

    // occulited task 135: a request for backups asks who the administrator is first; a cancelled
    // question leaves the request as it is
    async function approveAsked(r: Request) {
        if (!r.needs_confirmation) return approve(r);
        busy = r.id;
        err = '';
        let ticket: string | null = null;
        try {
            ticket = await confirmTicket(`/api/auth/v1/pairing/${encodeURIComponent(r.id)}/approve`, {
                title: t('Give {name} the backups?', {name: r.name}),
                message: t('Backups contain the radio keys.'),
                provider: t('Giving a program the backups asks who you are: the identity provider asks for your login again, then you come back here.'),
                impossible: t('This account can confirm neither with a password nor at an identity provider.'),
            }, () => remember(r.id));
        } catch (e) {
            err = (e as Error).message;
        } finally {
            busy = '';
        }
        if (ticket) await approve(r, ticket);
    }

    async function decide(r: Request, yes: boolean) {
        if (yes) return approveAsked(r);
        busy = r.id;
        err = '';
        try {
            await api.post(`/api/auth/v1/pairing/${encodeURIComponent(r.id)}/reject`, {});
            requests = requests.filter((x) => x.id !== r.id);
        } catch (e) {
            err = (e as Error).message;
            void load();
        } finally {
            busy = '';
        }
    }

    const AREAS: {key: string; label: string}[] = [
        {key: 'devices', label: 'Devices'},
        {key: 'names', label: 'Names and rooms'},
        {key: 'system', label: 'System'},
    ];
    const LEVELS: Record<string, string> = {read: 'read', operate: 'operate', configure: 'configure', administer: 'administer'};
    const levelText = (l: string | undefined) => (l && LEVELS[l] ? t(LEVELS[l]) : t('none'));
    const codeText = (c: string) => `${c.slice(0, 3)} ${c.slice(3)}`;
    // openccu-lite B-296: a request of addons alone shows its addons as the only grant, not three
    // areas at "none"; its access may come as null or as an empty object
    // occulited task 135: so does a request for backups alone
    const asked = (r: Request) => Object.keys(r.access ?? {}).filter((a) => a !== 'backup');
    const areasOf = (r: Request) => (asked(r).length > 0 || (!r.addons?.length && !r.access?.backup) ? AREAS : []);
</script>

{#each requests as r (r.id)}
    {#snippet actions()}
        <button type="button" class="hmm-button primary" disabled={busy !== ''} onclick={() => decide(r, true)} data-pair-approve>{t('Approve')}</button>
        <button type="button" class="hmm-button" disabled={busy !== ''} onclick={() => decide(r, false)} data-pair-reject>{t('Reject')}</button>
    {/snippet}
    <Notice kind={r.access?.backup ? 'error' : 'warning'} id="pairing" {actions}>
        <div class="pr-card" data-pair={r.id}>
            <p class="pr-head">
                {t('{name} asks for access', {name: r.name})}
                <span class="ol-muted">({r.address}{#if r.app_version}{' · '}{r.app} {r.app_version}{/if})</span>
            </p>
            <p class="pr-code-line">{t('The program shows this code. Approve only when it is the same:')} <strong class="pr-code hmm-mono" data-pair-code>{codeText(r.code)}</strong></p>
            {#if r.access?.backup}
                <p class="pr-backup" data-pair-backup>{t('{name} ({app}) on {address} asks for the backups of this system. Backups contain the radio keys: whoever holds one can read the keys of every paired device.', {name: r.name, app: r.app, address: r.address})}</p>
                <p class="pr-backup-note" data-pair-backup-note>{t('Approving asks for your password or passkey. The token works only from {range}.', {range: (r.token_ips ?? []).join(', ')})}</p>
            {/if}
            {#if r.look_alike}<p class="ol-warn pr-look" data-pair-lookalike>{t('Another program asks under this name or from this address: compare the code carefully.')}</p>{/if}
            <dl class="pr-access">
                {#each areasOf(r) as a (a.key)}
                    <div data-pair-area={a.key}>
                        <dt>{t(a.label)}</dt>
                        <dd class:pr-red={a.key === 'devices' && r.access?.[a.key] === 'administer'}>
                            {levelText(r.access?.[a.key])}{#if a.key === 'devices' && r.access?.[a.key] === 'administer'}{' · '}{t('may pair and delete devices')}{/if}
                            {#if r.purpose?.[a.key]}<span class="pr-purpose">“{r.purpose[a.key]}”</span>{/if}
                        </dd>
                    </div>
                {/each}
                {#if r.access?.backup}
                    <div data-pair-area="backup">
                        <dt>{t('Backups')}</dt>
                        <dd class="pr-red">
                            {t('download')}{' · '}{t('contains the radio keys')}
                            {#if r.purpose?.backup}<span class="pr-purpose">“{r.purpose.backup}”</span>{/if}
                        </dd>
                    </div>
                {/if}
                {#each r.addons ?? [] as ad (ad.id)}
                    <div data-pair-addon={ad.id}>
                        <dt>{t('Addon')}</dt>
                        <dd>{t('{name}: its pages through the system', {name: ad.name})} <code class="ol-muted">addon:{ad.id}</code></dd>
                    </div>
                {/each}
            </dl>
            <p class="ol-muted pr-cert" data-pair-cert>
                {#if r.fingerprint}{t('The code is bound to the certificate the program sees ({fp}).', {fp: r.fingerprint.slice(0, 23) + '…'})}{:else}{t('Plain HTTP: the code proves the request, not the connection.')}{/if}
                <a href="/system/users" use:link>{t('What the levels mean')}</a>
            </p>
        </div>
    </Notice>
{/each}
{#if err}<p class="ol-warn" data-pair-error>{err}</p>{/if}

<style>
    .pr-card { display: flex; flex-direction: column; gap: 6px; }
    .pr-card p { margin: 0; overflow-wrap: anywhere; }
    .pr-head { font-weight: 600; }
    .pr-code { font-size: 1.35em; letter-spacing: 0.08em; margin-left: 4px; }
    .pr-access { display: grid; grid-template-columns: max-content 1fr; gap: 2px 14px; margin: 2px 0; }
    .pr-access > div { display: contents; }
    .pr-access dt { color: var(--hmm-fg-muted); }
    .pr-access dd { margin: 0; overflow-wrap: anywhere; }
    .pr-purpose { display: block; color: var(--hmm-fg-muted); font-style: italic; }
    .pr-red { color: var(--hmm-error); font-weight: 600; }
    .pr-backup { color: var(--hmm-error); font-weight: 600; }
    .pr-backup-note { font-weight: 600; }
</style>
