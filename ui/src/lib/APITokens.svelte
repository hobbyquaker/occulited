<script lang="ts">
    /*
     * Task 132 (D-87): the API tokens (task 66's scopes, expiry and ranges). On System → Remote
     * access under lite-rpc - the maintainer, 2026-09-24 (openccu-lite task 223, occulited B-9):
     * the tokens are what a program uses with lite-rpc; on System → Users before (2026-09-19),
     * the Security page and the Metadata page before that. The page shows it to an administrator
     * only: only administrators create tokens (D-85).
     */
    import {onMount, tick} from 'svelte';
    import {api, REQUEST_HEADER} from './api';
    import {ask, askText} from './dialog.svelte';
    import {t} from './i18n.svelte';
    import Disclosure from './Disclosure.svelte';
    import Help from './Help.svelte';
    import CopyButton from './CopyButton.svelte';
    import {scrollToAnchor} from './anchor';
    import {bringIntoView} from './intoview';

    // ---- the API tokens (from the Metadata page, task 29; scopes since task 66) ------------
    // A token carries scopes, an optional expiry and optional allowed address ranges; the box
    // answers the scopes it offers, in its order, and the page keeps their labels.
    // task 219: a paired program's record
    interface TokenClient { app: string; app_version?: string; instance?: string; label: string; paired_at: string; paired_by: string; address: string; last_address?: string; addons?: string[] }
    interface Token { name: string; scopes: string[]; prefix: string; created: string; last_used?: string; expires?: string; ips?: string[]; client?: TokenClient }
    // openccu-lite task 307: the installed addons' ingress scopes (addon:<id>) - a program with one
    // reaches that addon's pages through the system as Authorization: Bearer, and nothing else
    interface AddonScope { id: string; name: string; scope: string }
    let tokens = $state<Token[]>([]);
    let scopeList = $state<string[]>([]);
    let addonList = $state<AddonScope[]>([]);
    let tokName = $state('');
    let tokFull = $state(false);
    let tokScopes = $state<Record<string, boolean>>({});
    let tokExpires = $state('');
    let tokIPs = $state('');
    let newSecret = $state('');
    let secretEl = $state<HTMLElement | null>(null);
    let secretNotice = $state<HTMLElement | null>(null);
    let saidEl = $state<HTMLElement | null>(null);
    let tokNotice = $state('');
    let addTokenOpen = $state(false);
    const chosenScopes = $derived(tokFull ? ['*'] : [...scopeList, ...addonList.map((a) => a.scope)].filter((s) => tokScopes[s]));
    const SCOPE_LABEL: Record<string, string> = {
        '*': 'Full access (includes future permissions)',
        'meta:read': 'Names and rooms: read',
        'meta:write': 'Names and rooms: write',
        'system:read': 'System: read',
        'logs:read': 'Journal: read',
        'system:write': 'System: configure',
        'addons:write': 'Addons: manage',
        power: 'Reboot, halt, update, restore',
        backup: 'Backups',
        led: 'Status LED',
        'power:halt': 'Power off, nothing else',
        'radio:keys': 'HmIP device keys (reads every key in clear)',
        'auth:admin': 'Users, tokens, login settings',
        'rpc:read': 'RPC: read',
        'rpc:operate': 'RPC: operate',
        'rpc:configure': 'RPC: configure',
        'rpc:admin': 'RPC: administer',
    };
    const scopeLabel = (s: string) => {
        if (SCOPE_LABEL[s]) return t(SCOPE_LABEL[s]);
        const addon = addonList.find((a) => a.scope === s);
        if (addon) return t('Addon {name}: its pages through the system', {name: addon.name});
        if (s.startsWith('addon:')) return t('Addon {name}: its pages through the system', {name: s.slice(6)});
        return s;
    };
    async function loadTokens() {
        try {
            const r = await api.get<{tokens: Token[]; scopes?: string[]; addons?: AddonScope[]}>('/api/auth/v1/tokens');
            tokens = r.tokens;
            scopeList = r.scopes ?? [];
            addonList = r.addons ?? [];
        } catch (e) {
            tokNotice = (e as Error).message;
        }
    }
    // the ranges as typed: spaces or commas between them
    const rangesOf = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter((x) => x);
    async function createToken() {
        tokNotice = '';
        const body: Record<string, unknown> = {name: tokName.trim(), scopes: chosenScopes};
        if (tokExpires) body.expires = new Date(tokExpires + 'T23:59:59').toISOString();
        if (rangesOf(tokIPs).length) body.ips = rangesOf(tokIPs);
        try {
            const r = await api.post<{token: string}>('/api/auth/v1/tokens', body);
            newSecret = r.token;
            tokNotice = t('Token created — copy it now, it is not shown again');
            // B-137: the notice stands above the token table and the Create panel below it, so on a
            // page of some length the token - shown once - appeared out of view. It is brought into
            // view and the focus goes to its Copy button, before the panel closes: a closing panel
            // hands the focus back to its own button unless the focus is already elsewhere.
            // The line above it, which says to copy it now, comes along where the window has room.
            await tick();
            bringIntoView(saidEl);
            bringIntoView(secretNotice, secretNotice?.querySelector<HTMLElement>('[data-action="copy-token"]'));
            tokName = '';
            tokFull = false;
            tokScopes = {};
            tokExpires = '';
            tokIPs = '';
            addTokenOpen = false;
            await loadTokens();
        } catch (e) {
            tokNotice = (e as Error).message;
        }
    }
    async function deleteToken(tk: Token) {
        if (!(await ask({message: t('Revoke token {name}?', {name: tk.name}), confirm: t('Revoke'), danger: true}))) return;
        try {
            const r = await fetch(`/api/auth/v1/tokens/${encodeURIComponent(tk.name)}`, {method: 'DELETE', headers: REQUEST_HEADER});
            if (!r.ok) throw new Error(`${r.status}`);
            tokNotice = t('Token revoked');
            await loadTokens();
        } catch (e) {
            tokNotice = (e as Error).message;
        }
    }

    // task 219: whether programs may ask for access (on by default); a paired program's label
    let pairingOn = $state<boolean | null>(null);
    async function loadPairing() {
        try {
            pairingOn = (await api.get<{enabled: boolean}>('/api/auth/v1/pairing')).enabled;
        } catch {
            pairingOn = null; // a system without pairing
        }
    }
    async function setPairing(on: boolean) {
        tokNotice = '';
        try {
            pairingOn = (await api.put<{enabled: boolean}>('/api/auth/v1/pairing/settings', {enabled: on})).enabled;
        } catch (e) {
            tokNotice = (e as Error).message;
            await loadPairing();
        }
    }
    async function renameToken(tk: Token) {
        const label = await askText({title: t('Rename'), message: t('The name the list and the Status page show for {name}. The token keeps its name.', {name: tk.name}), input: {label: t('Label'), initial: tk.client?.label ?? ''}, confirm: t('Save')});
        if (label === null) return;
        try {
            await api.patch(`/api/auth/v1/tokens/${encodeURIComponent(tk.name)}`, {label});
            await loadTokens();
        } catch (e) {
            tokNotice = (e as Error).message;
        }
    }

    onMount(async () => {
        void loadPairing();
        await loadTokens();
        scrollToAnchor('api-tokens');
    });
</script>

<section class="ol-section" data-section="api-tokens">
    <h3 id="api-tokens">{t('API tokens')}<Help>{t('For programs, not people: a daemon addon, a script, a CI job. A token carries scopes - what it may read and change - and is shown once. Addons on the system read the local token from the state directory (local-token) without any setup; it reads names and rooms and nothing else.')}</Help></h3>
    {#if pairingOn !== null}
        <label class="ol-pairing-switch" data-pairing-switch><input type="checkbox" checked={pairingOn} onchange={(e) => setPairing((e.currentTarget as HTMLInputElement).checked)} /> {t('Allow programs to ask for access')}<Help>{t('A program on the local network (hm2mqtt.js, Node-RED, Homematic Manager, ...) asks for the access it needs and shows a code; the request appears on the Status page with the same code, and an administrator approves or rejects it. Nothing is granted without that click. Off, a program gets a token only from this list.')}</Help></label>
    {/if}
    {#if tokNotice}<div class="ol-notice" data-notice="token-said" bind:this={saidEl}>{tokNotice}</div>{/if}
    {#if newSecret}
        <!-- B-135: the copy says what became of it; the words have their place at the row's end from the start -->
        <div class="ol-notice ol-secret" data-notice="new-token" bind:this={secretNotice}><strong>{t('New token')}:</strong> <code class="hmm-mono" bind:this={secretEl}>{newSecret}</code> <span class="ol-actions"><CopyButton text={() => newSecret} shown={() => secretEl} place="row-end" action="copy-token" /><button class="hmm-button" onclick={() => (newSecret = '')}>{t('Hide')}</button></span></div>
    {/if}
    <!-- eight columns: on a phone the table scrolls in its own box, never the page sideways -->
    <div class="ol-scroll">
    <table class="ol-table ol-tokens">
        <thead><tr><th>{t('Name')}</th><th>{t('Permissions (scopes)')}</th><th>{t('Prefix')}</th><th>{t('Created')}</th><th>{t('Last used')}</th><th>{t('Expires')}</th><th>{t('Allowed from')}</th><th></th></tr></thead>
        <tbody>
            {#each tokens as tk (tk.name)}
                <tr>
                    <td>
                        {tk.name}
                        {#if tk.client}
                            <div class="ol-muted ol-paired" data-paired={tk.name}>{tk.client.label} · {t('paired by {user}', {user: tk.client.paired_by})}{#if tk.client.last_address}{' · '}{t('last from {address}', {address: tk.client.last_address})}{/if}</div>
                        {/if}
                    </td>
                    <td class="ol-scopes">
                        {#if (tk.scopes ?? []).includes('*')}
                            <span class="ol-badge good" title={scopeLabel('*')}>{t('Full access')}</span>
                        {:else}
                            {#each tk.scopes ?? [] as s (s)}<span class="ol-badge hmm-mono" title={scopeLabel(s)}>{s}</span>{/each}
                        {/if}
                    </td>
                    <td class="hmm-mono">olt_{tk.prefix}…</td>
                    <td>{new Date(tk.created).toLocaleString()}</td>
                    <td>{tk.last_used ? new Date(tk.last_used).toLocaleString() : '–'}</td>
                    <td>{tk.expires ? new Date(tk.expires).toLocaleDateString() : '–'}</td>
                    <td class="hmm-mono">{tk.ips?.length ? tk.ips.join(' ') : '–'}</td>
                    <td class="ol-tok-actions">{#if tk.client}<button class="hmm-button" onclick={() => renameToken(tk)} data-rename={tk.name}>{t('Rename')}</button>{/if}<button class="hmm-button" onclick={() => deleteToken(tk)}>{t('Revoke')}</button></td>
                </tr>
            {/each}
        </tbody>
    </table>
    </div>
    <div style="margin-top:8px">
        <Disclosure label={t('Create token')} bind:open={addTokenOpen}>
            <div class="ol-tokform">
                <label for="ol-tok-name">{t('Name')}</label>
                <input id="ol-tok-name" class="hmm-input" placeholder={t('Token name (e.g. hm2mqtt)')} bind:value={tokName} autocomplete="off" />
                <span>{t('Scopes')}</span>
                <fieldset class="ol-scopepick">
                    <label class="ol-scope-full"><input type="checkbox" bind:checked={tokFull} /> <strong>{scopeLabel('*')}</strong></label>
                    <p class="ol-muted ol-scope-hint">{t('Explicit scopes are safer for programs: a token then reaches only what it needs, and nothing that is added later.')}</p>
                    <div class="ol-scope-grid">
                        {#each scopeList as s (s)}
                            <label><input type="checkbox" checked={tokFull || !!tokScopes[s]} disabled={tokFull} onchange={(e) => (tokScopes = {...tokScopes, [s]: (e.currentTarget as HTMLInputElement).checked})} /> {scopeLabel(s)} <code class="ol-muted">{s}</code></label>
                        {/each}
                    </div>
                    {#if addonList.length > 0}
                        <p class="ol-muted ol-scope-hint ol-scope-addons-hint" data-addon-scopes-hint>{t("An addon scope lets a program reach that addon's pages through the system (Authorization: Bearer), and nothing else.")}</p>
                        <div class="ol-scope-grid" data-addon-scopes>
                            {#each addonList as a (a.scope)}
                                <label><input type="checkbox" checked={tokFull || !!tokScopes[a.scope]} disabled={tokFull} onchange={(e) => (tokScopes = {...tokScopes, [a.scope]: (e.currentTarget as HTMLInputElement).checked})} /> {scopeLabel(a.scope)} <code class="ol-muted">{a.scope}</code></label>
                            {/each}
                        </div>
                    {/if}
                </fieldset>
                <label for="ol-tok-expires">{t('Expires')}</label>
                <div><input id="ol-tok-expires" class="hmm-input" type="date" bind:value={tokExpires} /> <span class="ol-muted">{t('optional; empty means never')}</span></div>
                <label for="ol-tok-ips">{t('Allowed from')}</label>
                <div><input id="ol-tok-ips" class="hmm-input hmm-mono" bind:value={tokIPs} placeholder="192.168.1.0/24 10.0.0.5" autocomplete="off" /> <span class="ol-muted">{t('optional; addresses or ranges the token is accepted from, as lighttpd sees them')}</span></div>
                <span></span>
                <div><button class="hmm-button primary" onclick={createToken} disabled={tokName.trim().length < 2 || chosenScopes.length === 0}>{t('Create token')}</button></div>
            </div>
        </Disclosure>
    </div>
</section>

<style>
    .ol-tokform { display: grid; grid-template-columns: max-content minmax(240px, 640px); gap: 8px 12px; align-items: start; }
    .ol-tokform > label, .ol-tokform > span { padding-top: 4px; }
    .ol-scopepick { border: 1px solid var(--hmm-border-muted); border-radius: 4px; padding: 8px 10px; margin: 0; }
    .ol-scope-hint { margin: 4px 0 8px; font-size: 0.9em; }
    .ol-scope-addons-hint { margin-top: 10px; }
    .ol-scope-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 4px 12px; }
    .ol-scope-grid label { display: flex; gap: 6px; align-items: baseline; flex-wrap: wrap; }
    .ol-scope-grid code { font-size: 0.85em; }
    .ol-scopes .ol-badge { margin: 1px 4px 1px 0; }
    @media (max-width: 600px) { .ol-tokform { grid-template-columns: 1fr; } }
    .ol-scroll { overflow-x: auto; max-width: 100%; }
    .ol-pairing-switch { display: flex; align-items: center; gap: 6px; margin: 0 0 8px; }
    .ol-paired { font-size: 0.9em; }
    .ol-tok-actions { display: flex; gap: 6px; flex-wrap: wrap; }
</style>
