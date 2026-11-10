<script lang="ts">
    /*
     * Task 179: the licences of everything in the image, from its SBOM (CycloneDX 1.6, GET
     * /api/system/v1/sbom, no login needed). One flat list with a search (the maintainer,
     * 2026-09-19: no tree) in the order lib/sbom.ts groupOf gives, and a page per component at
     * /licenses/c/<ref> with its facts and its licence text.
     */
    import {onMount} from 'svelte';
    import {navigate, router} from '../lib/router.svelte';
    import {segmentOf} from '../lib/routes';
    import {t} from '../lib/i18n.svelte';
    import {licences, loadLicences} from '../lib/licences.svelte';
    import {partsOf, searchRows, type Row} from '../lib/sbom';
    import Loading from '../lib/Loading.svelte';
    import CopyButton from '../lib/CopyButton.svelte';

    onMount(() => {
        void loadLicences();
    });

    const ref = $derived(segmentOf(router.path, '/licenses/c'));
    const rows = $derived(licences.data ? searchRows(licences.data.rows, licences.query) : []);
    const row = $derived(ref && licences.data ? (licences.data.rows.find((r) => r.refs.includes(ref)) ?? null) : null);
    const parts = $derived(row && licences.data ? partsOf(licences.data.rows, row) : []);

    const href = (r: Row) => `/licenses/c/${encodeURIComponent(r.ref)}`;
    const linkLabel = (type: string) => (type === 'website' ? t('Homepage') : type === 'vcs' ? t('Source code') : type === 'distribution' ? t('Download') : type);

    // Back to the list the way the visitor came. Each component page's history entry keeps how many
    // component pages lie between it and the list (licDepth): 1 when a row of the list opened it, one
    // more for each step to a part or a parent. The back link steps back that many entries, so the
    // list comes back with its search and scroll position; the browser's Back keeps the count right,
    // since it lives in the entries. A page opened from elsewhere (a bookmark) links to the list.
    // One handler does it all: with the router's `use:link` beside an onclick, both would run.
    const depthHere = () => (history.state as {licDepth?: number} | null)?.licDepth ?? 0;

    function follow(ev: MouseEvent, to: string, fromList: boolean) {
        if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
        ev.preventDefault();
        const depth = fromList ? 1 : depthHere() > 0 ? depthHere() + 1 : 0;
        const before = location.pathname;
        navigate(to);
        if (depth && location.pathname !== before) history.replaceState({...((history.state as object | null) ?? {}), licDepth: depth}, '');
    }

    function back(ev: MouseEvent) {
        if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
        ev.preventDefault();
        const d = depthHere();
        if (d > 0) history.go(-d);
        else navigate('/licenses');
    }

    // B-135: the texts' elements, selected where the browser cannot copy
    let texts = $state<(HTMLElement | undefined)[]>([]);
    // task 236 (the maintainer): openccu-lite's own licence and author under the product line, and
    // the licence's disclaimer of warranty (Apache 2.0 section 7) in a panel at the head's right.
    // The licence text is the fork's LICENSE, served with the UI.
    const OWN_LICENCE = 'Apache License 2.0';
    const OWN_LICENCE_URL = '/openccu-lite-LICENSE.txt';
    const OWN_AUTHOR = 'Sebastian Raff (hobbyquaker)';
    // task 266: the privacy statement - every call the system makes to an outside source, field by field
    const PRIVACY_URL = 'https://github.com/hobbyquaker/openccu-lite/blob/main/docs/privacy.md';
    // an unknown ref (a stale link) goes to the list
    $effect(() => {
        if (ref && licences.data && !row) navigate('/licenses');
    });
</script>

<!-- the maintainer, 2026-09-19: an origin leads to its repository, or to the parent component's page -->
{#snippet originLinks(r: Row, fromList: boolean)}
    {#each r.origins as o, i (o.name)}{#if i}{', '}{/if}{#if o.url}<a href={o.url} target="_blank" rel="noopener">{o.name}</a>{:else if o.ref}{@const to = `/licenses/c/${encodeURIComponent(o.ref)}`}<a href={to} onclick={(ev) => follow(ev, to, fromList)}>{o.name}</a>{:else}{o.name}{/if}{/each}
{/snippet}

<div class="lic-page" data-loading={licences.data || licences.missing || licences.error ? undefined : ''}>
    {#if licences.error}
        <div class="ol-notice error">{licences.error}</div>
    {:else if licences.missing}
        <h2>{t('Licenses')}</h2>
        <div class="ol-notice">{t('This image carries no SBOM yet.')}</div>
    {:else if !licences.data}
        <Loading />
    {:else if ref && row}
        <p class="lic-back"><a href="/licenses" onclick={back}>← {t('Licenses')}</a></p>
        <h2 class="lic-title">{row.name} <span class="hmm-mono ol-muted">{row.version}</span></h2>
        {#if row.description}<p class="ol-muted">{row.description}</p>{/if}
        <dl class="lic-facts">
            <dt>{t('License')}</dt>
            <dd class="hmm-mono">{row.licence || '—'}</dd>
            {#if row.origins.length}<dt>{t('Origin')}</dt><dd>{@render originLinks(row, false)}</dd>{/if}
            {#if row.author}<dt>{t('Author')}</dt><dd>{row.author}</dd>{/if}
            {#if row.copyright}<dt>Copyright</dt><dd>{row.copyright}</dd>{/if}
            {#if row.purl}<dt>purl</dt><dd class="hmm-mono lic-wrap">{row.purl}</dd>{/if}
            {#each row.links as l (l.type + l.url)}
                <dt>{linkLabel(l.type)}</dt>
                <dd class="lic-wrap"><a href={l.url} target="_blank" rel="noopener">{l.url}</a></dd>
            {/each}
        </dl>
        {#if row.texts.length === 0}
            <p class="ol-muted">{parts.length ? t('The SBOM carries no license text for this component as a whole; its parts below carry theirs.') : t('The SBOM carries no license text for this component.')}</p>
        {/if}
        {#each row.texts as lt, i (i)}
            <div class="lic-texthead">
                <h3>{lt.title || t('License text')}</h3>
                <CopyButton text={lt.text} shown={() => texts[i]} place="before" />
            </div>
            <pre class="lic-text" bind:this={texts[i]}>{lt.text}</pre>
        {/each}
        <!-- what it holds (a JAR's libraries, occulited's modules, multilib32's packages), each to its page -->
        {#if parts.length}
            <h3>{t('Parts')} <span class="ol-muted">· {parts.length}</span></h3>
            <table class="ol-table ol-stack lic-table lic-parts" data-parts>
                <colgroup><col class="lic-p-name" /><col class="lic-p-ver" /><col /></colgroup>
                <thead><tr><th>{t('Name')}</th><th>{t('Version')}</th><th>{t('License')}</th></tr></thead>
                <tbody>
                    {#each parts as p (p.ref)}
                        <tr><td class="lic-name"><a href={href(p)} onclick={(ev) => follow(ev, href(p), false)}>{p.name}</a></td><td class="hmm-mono lic-ver">{p.version}</td><td class="hmm-mono lic-lic">{p.licence || '—'}</td></tr>
                    {/each}
                </tbody>
            </table>
        {/if}
    {:else}
        <!-- task 236: the head is two columns - left the heading, the product with its licence and
             author, the search and the download; right the licence's disclaimer of warranty in a
             panel, as high as the left and ending at the table's right edge (the maintainer's
             correction: the disclaimer moved into the panel, its bold title the panel's title). The
             whole text always shows, never a scrollbar: where it needs more than the left area's
             height, the panel sets the row's height and the left area stays at the top. In German OpenCCU's own
             wording, not a translation of the English. On a phone the panel comes first, above the heading. -->
        <div class="lic-top">
            <div class="lic-left" data-lic-left>
                <h2>{t('Licenses')}</h2>
                <p class="lic-head">
                    <span>{licences.data.product} <span class="hmm-mono">{licences.data.version}</span></span>
                    <a class="hmm-button" href="/api/system/v1/sbom?download=1" download>{t('Download SBOM')}</a>
                </p>
                <dl class="lic-own" data-own-lines>
                    <dt>{t('License')}</dt><dd><a href={OWN_LICENCE_URL} target="_blank" rel="noopener" data-own-license>{OWN_LICENCE}</a></dd>
                    <dt>{t('Author')}</dt><dd data-own-author>{OWN_AUTHOR}</dd>
                    <dt>{t('Privacy')}</dt><dd><a href={PRIVACY_URL} target="_blank" rel="noopener" data-own-privacy>{t('What the system sends to outside sources')}</a></dd>
                </dl>
                <div class="lic-search">
                    <input class="hmm-input" type="search" placeholder={t('Search name, version, license, author, origin')} aria-label={t('Search the licenses')} bind:value={licences.query} />
                    <span class="ol-muted">{rows.length === 1 ? t('1 component') : t('{n} components', {n: String(rows.length)})}</span>
                </div>
            </div>
            <aside class="ol-panel lic-panel" data-own-panel aria-labelledby="lic-disclaimer-title">
                <h3 id="lic-disclaimer-title">{t('Disclaimer of Warranty')}</h3>
                <p class="ol-muted lic-disclaimer" data-disclaimer>{t('Unless required by applicable law or agreed to in writing, openccu-lite is provided by the Contributors (and each Contributor provides its Contributions) on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied, including, without limitation, any warranties or conditions of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A PARTICULAR PURPOSE. You are solely responsible for determining the appropriateness of using or redistributing openccu-lite and assume any risks associated with Your exercise of permissions under this License.')}</p>
            </aside>
        </div>
        <table class="ol-table ol-stack lic-table">
            <colgroup><col class="lic-c-name" /><col class="lic-c-ver" /><col class="lic-c-lic" /><col class="lic-c-author" /><col class="lic-c-origin" /></colgroup>
            <thead><tr><th>{t('Name')}</th><th>{t('Version')}</th><th>{t('License')}</th><th>{t('Author')}</th><th>{t('Origin')}</th></tr></thead>
            <tbody>
                {#each rows as r (r.ref)}
                    <tr data-group={r.group}>
                        <td class="lic-name"><a href={href(r)} onclick={(ev) => follow(ev, href(r), true)}>{r.name}</a></td>
                        <td class="hmm-mono lic-ver">{r.version}</td>
                        <!-- the maintainer, 2026-09-19: the licence leads to the component's page too, where its text is -->
                        <td class="hmm-mono lic-lic">{#if r.licence}<a href={href(r)} onclick={(ev) => follow(ev, href(r), true)} title={r.texts.length ? t('The license text') : ''}>{r.licence}</a>{:else}—{/if}</td>
                        <td class="lic-author" title={r.author}>{r.author}</td>
                        <td class="ol-muted lic-origin" title={r.origin}>{@render originLinks(r, true)}</td>
                    </tr>
                {/each}
            </tbody>
        </table>
        {#if rows.length === 0}<p class="ol-muted">{t('Nothing found')}</p>{/if}
    {/if}
</div>

<style>
    .lic-page h2 { margin-top: 0; }
    /* task 236: the head's two columns, stretched to one height; the panel ends at the table's right
       edge (the page's content edge, which the table fills) */
    .lic-top { display: flex; align-items: stretch; gap: 12px 20px; margin-bottom: 10px; }
    .lic-left { flex: 1 1 auto; min-width: 0; }
    .lic-left .lic-search { margin-bottom: 0; }
    /* wide (the maintainer: never a scrollbar, the whole text shows), the small muted text of long
       explanations; a taller text makes the row taller, the left area stays at its top */
    .lic-panel { flex: 0 1 62%; max-width: 820px; min-width: 0; margin: 0 0 0 auto; padding-bottom: 10px; }
    .lic-panel > h3 { margin: 10px 0 4px; }
    .lic-panel > .lic-disclaimer { margin: 0; line-height: 1.35; }
    .lic-own { display: grid; grid-template-columns: max-content 1fr; gap: 2px 12px; margin: 0 0 6px; }
    .lic-own dt { color: var(--hmm-fg-muted); }
    .lic-own dd { margin: 0; min-width: 0; }
    .lic-disclaimer { font-size: var(--hmm-font-size-small); }
    .lic-head { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; margin: 0 0 6px; }
    .lic-search { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; margin-bottom: 10px; }
    .lic-search input { flex: 1 1 260px; max-width: 420px; }
    /* B-156: the columns keep their widths while the search narrows the list */
    .lic-table { table-layout: fixed; }
    .lic-c-name { width: 28%; }
    .lic-c-ver { width: 13%; }
    .lic-c-lic { width: 21%; }
    .lic-c-author { width: 20%; }
    .lic-parts { max-width: 88ch; }
    .lic-p-name { width: 50%; }
    .lic-p-ver { width: 20%; }
    .lic-table td { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .lic-back { margin: 0 0 6px; }
    .lic-title { overflow-wrap: anywhere; }
    .lic-facts { display: grid; grid-template-columns: max-content 1fr; gap: 4px 16px; margin: 0 0 16px; }
    .lic-facts dt { color: var(--hmm-fg-muted); }
    .lic-facts dd { margin: 0; min-width: 0; }
    .lic-wrap { overflow-wrap: anywhere; }
    .lic-texthead { display: flex; align-items: center; justify-content: space-between; gap: 12px; max-width: 88ch; }
    .lic-texthead h3 { margin: 12px 0 6px; }
    .lic-text { max-width: 88ch; white-space: pre-wrap; overflow-wrap: anywhere; font-family: var(--hmm-font-mono); font-size: var(--hmm-font-size-small); line-height: 1.5; padding: 12px 14px; border: 1px solid var(--hmm-border); border-radius: 6px; background: var(--hmm-header-bg); margin: 0 0 12px; }
    @media (max-width: 700px) {
        /* the panel first on the page, above the heading, full width (the maintainer) */
        .lic-top { flex-direction: column; }
        .lic-panel { max-width: none; margin: 0; order: -1; }
        .lic-table td { white-space: normal; overflow: visible; }
        .lic-table td.lic-name { flex: 1 1 100%; font-weight: 600; }
    }
</style>
