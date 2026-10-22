<script lang="ts">
    import {t, i18n, setLanguage} from '../lib/i18n.svelte';
    import {theme, setTheme, type Theme} from '../lib/theme.svelte';
    import Help from '../lib/Help.svelte';
    import {prefs, setAppPreferences, setAddonFullscreen, addonFullscreen, type StartPage} from '../lib/prefs.svelte';
    import {auth} from '../lib/auth.svelte';
    import {api, type Addon} from '../lib/api';
    import {pageLife} from '../lib/pagelife.svelte';
    import PublicControl from '../lib/PublicControl.svelte';
    import WarnEdge from '../lib/WarnEdge.svelte';

    // task 29: the Settings page behind the gear - the two switches that used to sit in the top
    // bar. How the box authenticates was here too until 2026-09-10; it is on System → Users now,
    // with the API tokens (task 132; on a Security page in between).

    // occulited task 24 (openccu-lite #11): the installed, enabled addons whose manifest declares
    // ui.fullscreen - each gets a card below, as Control has one. Read when the page is shown (a
    // kept page: again on every return, so an addon installed meanwhile gets its card); the public
    // principal has no addons.
    const life = pageLife();
    let fullAddons = $state<Addon[]>([]);
    async function loadAddons(): Promise<void> {
        if (!auth.authenticated || auth.public) return;
        try {
            const all = (await api.get<{addons: Addon[]}>('/api/system/v1/addons')).addons;
            fullAddons = all.filter((a) => a.fullscreen === true && a.enabled !== false).sort((x, y) => (x.name || x.id).localeCompare(y.name || y.id, i18n.language, {sensitivity: 'base'}) || x.id.localeCompare(y.id));
        } catch {
            /* the section stays as it was */
        }
    }
    $effect(() => {
        void loadAddons();
        return life.onReturn(() => void loadAddons());
    });
</script>

<h1>{t('Settings')}</h1>

<!-- task 51: where the two choices are kept, behind the section's ? -->
<h2>{t('Appearance')}<Help>{t('Both are kept in this browser and handed to embedded addons.')}</Help></h2>
<div class="ol-cards">
    <div class="ol-card">
        <div class="k">{t('Language')}</div>
        <select class="hmm-select" value={i18n.language} onchange={(e) => setLanguage((e.currentTarget as HTMLSelectElement).value as 'de' | 'en')} style="margin-top:6px">
            <option value="de">Deutsch</option>
            <option value="en">English</option>
        </select>
    </div>
    <div class="ol-card">
        <div class="k">{t('Theme')}</div>
        <select class="hmm-select" value={theme.value} onchange={(e) => setTheme((e.currentTarget as HTMLSelectElement).value as Theme)} style="margin-top:6px">
            <option value="system">{t('Theme: system')}</option>
            <option value="light">{t('Theme: light')}</option>
            <option value="dark">{t('Theme: dark')}</option>
        </select>
    </div>
</div>

<!-- task 193: the App's choices, kept with the account (with the login off: in this browser) -->
<h2>{t('Control')}<Help>{t('Control is the everyday view - favorites, rooms, functions with their controls. These choices are kept with your account, so a phone and a desktop agree.')}</Help></h2>
<div class="ol-cards">
    <div class="ol-card" data-setting="start-page">
        <div class="k">{t('Start page')}</div>
        <select class="hmm-select" value={prefs.startPage || 'status'} onchange={(e) => setAppPreferences({startPage: (e.currentTarget as HTMLSelectElement).value as StartPage})} style="margin-top:6px" aria-label={t('Start page')} disabled={!prefs.loaded}>
            <option value="status">{t('Status')}</option>
            <option value="app">{t('Control')}</option>
        </select>
        <div class="ol-card-detail">{t('Where the UI opens after a login and when the address is the system alone.')}</div>
        {#if prefs.startPage === 'app' && prefs.appHidden}<div class="ol-card-detail" data-start-fallback>{t('While Control is not in the menu, the UI opens on Status.')}</div>{/if}
    </div>
    <div class="ol-card" data-setting="app-fullscreen">
        <div class="k">{t('Control without the top bar')}</div>
        <label class="ol-check" style="margin-top:6px"><input type="checkbox" checked={prefs.appFullscreen} onchange={(e) => setAppPreferences({appFullscreen: e.currentTarget.checked})} disabled={!prefs.loaded} /> {t('Show Control as the whole window')}</label>
        <div class="ol-card-detail">{t('The tab bar and the icons stay away while Control is open; its menu keeps the way back to Status and to these settings.')}</div>
    </div>
    <!-- openccu-lite task 306: Control's tab out of the top bar - a preference, not a permission;
         last, so the public-mode panel below still lines up with the first two -->
    <div class="ol-card" data-setting="app-in-menu">
        <div class="k">{t('Control in the menu')}</div>
        <label class="ol-check" style="margin-top:6px"><input type="checkbox" checked={!prefs.appHidden} onchange={(e) => setAppPreferences({appHidden: !e.currentTarget.checked})} disabled={!prefs.loaded} /> {t('Show "Control" in the menu')}</label>
        <div class="ol-card-detail">{t('Off: the tab goes from the top bar. Control itself stays and opens at its address, /app.')}</div>
    </div>
    <!-- occulited task 101: a room or function page leaves its own name out of the tiles' names; on by default -->
    <div class="ol-card" data-setting="app-short-names">
        <div class="k">{t('Shorten names')}<Help>{t("On a room or function page, the page's own name is left out of each tile's name: on Küche, \"Fenster Küche links\" reads \"Fenster links\"; on Fenster, \"Küche links\". Whole words only; a name that would end up empty stays as it is. Favorites, the tooltip and the sheet show the full name.")}</Help></div>
        <label class="ol-check" style="margin-top:6px"><input type="checkbox" checked={!prefs.appFullNames} onchange={(e) => setAppPreferences({appFullNames: !e.currentTarget.checked})} disabled={!prefs.loaded} /> {t('Leave the room or function out of the names')}</label>
        <div class="ol-card-detail">{t('On a room or function page the tiles show the rest of the name; the page heading names the room or function.')}</div>
    </div>
</div>

<!-- occulited task 24 (openccu-lite #11): an addon whose manifest declares ui.fullscreen may be
     shown as the whole window, as Control may. Off until ticked; kept with the account like the
     pins (a flag on the addon's entry in the preferences). The shell adds no way back: the
     declaration is the addon's promise of its own. -->
{#if fullAddons.length > 0}
    <h2>{t('Addons')}<Help>{t('An addon that brings a header and a menu of its own can be shown as the whole window, without the top bar. The choice is offered only for addons that declare it; the addon itself offers the way back to the system. Kept with your account.')}</Help></h2>
    <div class="ol-cards">
        {#each fullAddons as a (a.id)}
            <div class="ol-card" data-setting="addon-fullscreen" data-addon={a.id}>
                <div class="k">{t('{name} without the top bar', {name: a.name || a.id})}</div>
                <label class="ol-check" style="margin-top:6px"><input type="checkbox" checked={addonFullscreen(a.id)} onchange={(e) => setAddonFullscreen(a.id, e.currentTarget.checked)} disabled={!prefs.loaded} /> {t('Show {name} as the whole window', {name: a.name || a.id})}</label>
                <div class="ol-card-detail">{t('The tab bar and the icons stay away while the addon is open; the addon itself offers the way back to the system.')}</div>
            </div>
        {/each}
    </div>
{/if}

<!-- openccu-lite task 223 (the maintainer, 2026-09-24): Control without a login, from the Remote
     access page - beside the Control choices, for an administrator, who alone may switch it -->
{#if auth.role === 'admin'}
    <WarnEdge ids={['app-public']}><PublicControl /></WarnEdge>
{/if}
