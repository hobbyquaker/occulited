<script lang="ts">
    import './app.css';
    import {router, link, replace} from './lib/router.svelte';
    import {prefs, loadPreferences, resetPreferences, effectiveStartPage, addonFullscreen} from './lib/prefs.svelte';
    import {onPage, segmentOf, SETTINGS_BASE} from './lib/routes';
    import {opensFilter, shortcutLabel, systemPageAt, worstDot} from './lib/systemmenu';
    import {refreshDots, systemMenu, toggleSystemMenu} from './lib/systemmenu.svelte';
    import SystemMenu from './lib/SystemMenu.svelte';
    import FirewallPage from './pages/FirewallPage.svelte';
    import {t, i18n} from './lib/i18n.svelte';
    import StatusPage from './pages/StatusPage.svelte';
    import AppPage from './pages/AppPage.svelte';
    import RadioPage from './pages/RadioPage.svelte';
    import LANDevicesPage from './pages/LANDevicesPage.svelte';
    import StoragePage from './pages/StoragePage.svelte';
    import KeysPage from './pages/KeysPage.svelte';
    import ServicesPage from './pages/ServicesPage.svelte';
    import AddonsPage from './pages/AddonsPage.svelte';
    import AddonFrame from './pages/AddonFrame.svelte';
    import AddonMenu from './lib/AddonMenu.svelte';
    import LogPage from './pages/LogPage.svelte';
    import AccountPage from './pages/AccountPage.svelte';
    import SettingsPage from './pages/SettingsPage.svelte';
    import Icon from './lib/Icon.svelte';
    import ConfirmDialog from './lib/ConfirmDialog.svelte';
    import PowerMenu from './lib/PowerMenu.svelte';
    import BootReady from './lib/BootReady.svelte';
    import NetworkPage from './pages/NetworkPage.svelte';
    import HardwarePage from './pages/HardwarePage.svelte';
    import CertificatePage from './pages/CertificatePage.svelte';
    import RemoteAccessPage from './pages/RemoteAccessPage.svelte';
    import FirmwarePage from './pages/FirmwarePage.svelte';
    import BackupPage from './pages/BackupPage.svelte';
    import TrustPage from './pages/TrustPage.svelte';
    import WelcomePage from './pages/WelcomePage.svelte';
    import NavFrame from './pages/NavFrame.svelte';
    import FrameHost from './lib/FrameHost.svelte';
    import {dropAll, leaveFrames, noteAddons, recheck} from './lib/frames.svelte';
    import {watchAddons} from './lib/addonsync';
    import {setShellStreamKey} from './lib/shellstream/client';
    import {frontendKey, settingsKey} from './lib/framekeep';
    import {FRAMED_BACK} from './lib/framed';
    import {tick, untrack, type Component} from 'svelte';
    import {fade} from 'svelte/transition';
    import {reducedMotion} from './lib/reveal';
    import KeptPage from './lib/KeptPage.svelte';
    import {PageLife} from './lib/pagelife.svelte';
    import {api, type Addon, type NavEntry} from './lib/api';
    // The header wordmark: OpenCCU's own logo with `lite` beside it. Two files rather than one
    // recoloured by CSS, because a filter that turns the black wordmark white would also turn the
    // cyan glyph orange. Which one shows is decided in app.css, so "system" theme works too.
    import logoLight from './assets/openccu-lite-light.png';
    import logoLight2x from './assets/openccu-lite-light@2x.png';
    import logoLight3x from './assets/openccu-lite-light@3x.png';
    import logoDark from './assets/openccu-lite-dark.png';
    import logoDark2x from './assets/openccu-lite-dark@2x.png';
    import logoDark3x from './assets/openccu-lite-dark@3x.png';
    import LoginPage from './pages/LoginPage.svelte';
    import LicensesPage from './pages/LicensesPage.svelte';
    import UsersPage from './pages/UsersPage.svelte';
    import {auth, refresh} from './lib/auth.svelte';
    import {answerThemeRequests} from './lib/theme.svelte';
    import {onMount} from 'svelte';

    let extra = $state<NavEntry[]>([]);
    // Task 55: the addon dropdown lists every installed addon, not only the ones with a frontend,
    // so it needs the addon list itself - the names, the versions the favicons are cached under,
    // the settings pages, which ones are switched off. (Task 26 fetched the list for the logos in
    // the Info: lines; the dropdown shows the frontends' favicons now, and the logos stay on
    // *Installed addons*.)
    let installed = $state<Addon[]>([]);
    // whether the list has answered, or failed: the favicons wait for the versions
    let installedLoaded = $state(false);
    // 28.5: a framed Node-RED asks for the look; the shell answers
    answerThemeRequests();
    onMount(() => {
        void refresh();
    });
    // ---- the kept pages (task 177) --------------------------------------------------------------
    // The maintainer: pages should not render from new at every switch - keep them, as the addon
    // frames are kept (task 39), and fade in only a page's first load. Every visited page stays
    // mounted, hidden while another shows; its PageLife (lib/pagelife.svelte.ts) tells it whether it
    // is shown, so its polls pause and it refreshes when it comes back.
    type KeptEntry = {key: string; match: (path: string) => boolean; component: Component};
    const KEPT: KeptEntry[] = [
        {key: 'status', match: (p) => p === '/', component: StatusPage},
        {key: 'app', match: (p) => onPage(p, '/app'), component: AppPage},
        {key: 'account', match: (p) => onPage(p, '/account'), component: AccountPage},
        {key: 'settings', match: (p) => onPage(p, '/settings'), component: SettingsPage},
        {key: 'users', match: (p) => onPage(p, '/system/users'), component: UsersPage},
        {key: 'network', match: (p) => onPage(p, '/system/network'), component: NetworkPage},
        {key: 'firewall', match: (p) => onPage(p, '/system/firewall'), component: FirewallPage},
        {key: 'remote-access', match: (p) => onPage(p, '/system/remote-access'), component: RemoteAccessPage},
        {key: 'hardware', match: (p) => onPage(p, '/system/hardware'), component: HardwarePage},
        {key: 'certificates', match: (p) => onPage(p, '/system/certificates'), component: CertificatePage},
        {key: 'trust', match: (p) => onPage(p, '/system/trust'), component: TrustPage},
        {key: 'updates', match: (p) => onPage(p, '/system/updates'), component: FirmwarePage},
        {key: 'backup', match: (p) => onPage(p, '/system/backup'), component: BackupPage},
        {key: 'interfaces', match: (p) => onPage(p, '/system/interfaces'), component: RadioPage},
        {key: 'lan-devices', match: (p) => onPage(p, '/system/lan-devices'), component: LANDevicesPage},
        {key: 'storage', match: (p) => onPage(p, '/system/storage'), component: StoragePage},
        {key: 'keys', match: (p) => onPage(p, '/system/keys'), component: KeysPage},
        {key: 'services', match: (p) => onPage(p, '/system/services'), component: ServicesPage},
        {key: 'addons', match: (p) => onPage(p, '/addons'), component: AddonsPage},
        {key: 'log', match: (p) => onPage(p, '/system/log'), component: LogPage},
    ];
    // the kept pages show when signed in and past the password change (the account page excepted)
    const keepPages = $derived(auth.loaded && auth.authenticated && !(auth.mustChangePassword && !onPage(router.path, '/account')));
    // the page the route shows: '' for the login, the welcome page and the addon frames; any other
    // path is Status, as before
    const activeKey = $derived.by(() => {
        const p = router.path;
        if (!keepPages || onPage(p, '/login') || onPage(p, '/welcome') || onPage(p, '/licenses') || addonId !== '' || navId !== '') return '';
        return KEPT.find((e) => e.match(p))?.key ?? 'status';
    });
    let visited = $state<string[]>([]);
    const lives = new Map<string, PageLife>();
    function lifeOf(key: string): PageLife {
        let l = lives.get(key);
        if (!l) {
            l = new PageLife(key === untrack(() => activeKey));
            lives.set(key, l);
        }
        return l;
    }
    // Each kept page keeps its scroll position: saved when another page shows, restored when it
    // comes back (a page's first visit starts at the top); an anchor in the URL wins on the way back,
    // as it does on a first visit, where the page scrolls to its own section.
    const scrollMemo = new Map<string, {port: number; win: number}>();
    let shownKey = '';
    $effect(() => {
        const key = activeKey;
        const prev = untrack(() => shownKey);
        if (prev === key) return;
        if (prev) scrollMemo.set(prev, {port: portEl?.scrollTop ?? 0, win: window.scrollY});
        shownKey = key;
        let returned = false;
        for (const [k, l] of lives) {
            if (k === key && !l.active) returned = true;
            l.setActive(k === key);
        }
        if (!key) {
            // an addon page, the login, the welcome page: from the top - an addon frame fills the
            // window, and a page area left scrolled put its top under the bar (Homematic Manager's
            // menu, found by the maintainer)
            requestAnimationFrame(() => {
                if (portEl) portEl.scrollTop = 0;
                window.scrollTo({top: 0});
            });
            return;
        }
        const memo = returned ? scrollMemo.get(key) : undefined;
        requestAnimationFrame(() => {
            const id = location.hash.length > 1 ? decodeURIComponent(location.hash.slice(1)) : '';
            const target = id ? document.querySelector(`.ol-keptpage[data-page="${key}"]`)?.querySelector(`[id="${CSS.escape(id)}"]`) : null;
            if (target) {
                target.scrollIntoView({block: 'start'});
                return;
            }
            if (portEl) portEl.scrollTop = memo?.port ?? 0;
            window.scrollTo({top: memo?.win ?? 0});
        });
    });
    // the shown page's query, for the page (a hidden one keeps what it saw)
    $effect(() => {
        const s = router.search;
        const l = activeKey ? lives.get(activeKey) : undefined;
        if (l) l.search = s;
    });
    $effect(() => {
        if (!keepPages) {
            visited = [];
            lives.clear();
        }
    });
    // A page's first visit is covered while it builds up, and fades in once it is ready: nothing in
    // it waits any more ([data-loading]: Loading.svelte, and what a page marks itself - the Status
    // page's warnings), not before COVER_MIN_MS, not after COVER_MAX_MS. A page visited before comes
    // back at once, uncovered. The menu is never covered.
    const COVER_MIN_MS = 120;
    const COVER_MAX_MS = 2000;
    let entering = $state(false);
    $effect.pre(() => {
        const key = activeKey;
        if (!key || untrack(() => visited.includes(key))) {
            entering = false;
            return;
        }
        visited = [...untrack(() => visited), key];
        entering = true;
        const start = performance.now();
        const timer = setInterval(() => {
            const shown = document.querySelector('.ol-keptpage:not([hidden])');
            const waiting = shown?.querySelector('[data-loading]');
            const t = performance.now() - start;
            if ((t >= COVER_MIN_MS && shown && !waiting) || t >= COVER_MAX_MS) {
                entering = false;
                clearInterval(timer);
            }
        }, 40);
        return () => clearInterval(timer);
    });

    // whether the nav list has answered: a deep link to /nav/<id> waits for it instead of showing Status
    let navLoaded = $state(false);
    // task 177: the menu shows the addon entries of the last visit at once, per user, and takes the
    // answers when they come - so the bar does not grow a moment after it appeared. An addon removed
    // in between may show for that moment (the maintainer's choice).
    const MENU_CACHE = 'ol.menuCache';
    function readMenuCache(user: string): {nav: NavEntry[]; addons: Addon[]} | null {
        try {
            const c = JSON.parse(localStorage.getItem(MENU_CACHE) ?? 'null');
            return c && c.user === user && Array.isArray(c.nav) && Array.isArray(c.addons) ? c : null;
        } catch {
            return null;
        }
    }
    function writeMenuCache(user: string) {
        try {
            localStorage.setItem(MENU_CACHE, JSON.stringify({user, nav: $state.snapshot(extra), addons: $state.snapshot(installed)}));
        } catch {
            /* no storage: the next visit loads as before */
        }
    }
    $effect(() => {
        if (auth.authenticated) {
            const user = auth.user ?? '';
            const cached = untrack(() => readMenuCache(user));
            if (cached) {
                extra = cached.nav;
                installed = cached.addons;
                installedLoaded = true;
            }
            api.get<{entries: NavEntry[]}>('/api/system/v1/nav')
                .then((r) => {
                    extra = r.entries;
                    writeMenuCache(user);
                })
                .catch(() => (extra = []))
                .finally(() => (navLoaded = true));
            api.get<{addons: Addon[]}>('/api/system/v1/addons')
                .then((r) => {
                    installed = r.addons;
                    noteAddons(r.addons); // task 39: the kept pages learn their addons' state from it
                    writeMenuCache(user);
                })
                .catch(() => (installed = []))
                .finally(() => (installedLoaded = true));
        } else {
            extra = [];
            installed = [];
            installedLoaded = false;
            navLoaded = false;
            // task 39: after a logout no kept addon page survives
            untrack(dropAll);
        }
    });

    // openccu-lite B-297: the menu follows the addons while the shell is open - an uninstall or an
    // install here (the Addons page says so when its work is done), in another tab, on another
    // device or on the console (the system's addon revision, lib/addonsync.ts). Both lists are
    // read again; a pinned tab of an addon that is gone goes with its row, and its kept page with
    // it (noteAddons). A read already on its way when the next change comes is followed by one more.
    let menuRun: Promise<void> | null = null;
    let menuAgain = false;
    function refreshMenu() {
        if (!auth.authenticated) return;
        if (menuRun) {
            menuAgain = true;
            return;
        }
        const user = auth.user ?? '';
        menuRun = (async () => {
            do {
                menuAgain = false;
                const [n, a] = await Promise.allSettled([
                    api.get<{entries: NavEntry[]}>('/api/system/v1/nav'),
                    api.get<{addons: Addon[]}>('/api/system/v1/addons'),
                ]);
                if (!auth.authenticated || (auth.user ?? '') !== user) break;
                if (n.status === 'fulfilled') extra = n.value.entries;
                if (a.status === 'fulfilled') {
                    installed = a.value.addons;
                    noteAddons(a.value.addons);
                }
                if (n.status === 'fulfilled' || a.status === 'fulfilled') writeMenuCache(user);
            } while (menuAgain);
            menuRun = null;
        })();
    }
    $effect(() => {
        if (!auth.authenticated || auth.public || auth.mustChangePassword) return;
        return untrack(() => watchAddons(refreshMenu));
    });
    // occulited B-53: the shell's stream is shared by the browser's windows; a new sign-in opens it
    // anew under the new session
    $effect(() => setShellStreamKey(auth.authenticated ? auth.sid || auth.user : ''));

    // ---- the menu (task 26) ------------------------------------------------------------------
    // Eleven fixed tabs plus one per addon was too many to read (maintainer, 2026-09-07). Every
    // route is unchanged - deep links and the addon iframe routing depend on the paths - only the
    // presentation is condensed:
    //
    //   Status  <pinned addons>  [Addons]  [System ▾]   …  <user>  ☾  Sprache
    //
    // (task 131, D-86: that and nothing more - Interfaces and Metadata are System pages now; the
    // notes below are how the bar got here.)
    //
    // * the addon dropdown (first, task 26) gained a footer with the two pages *about* addons,
    //   *Installed addons* and *Catalogue*: one subject, one control. That answers the question
    //   task 26 left open, and it makes the dropdown worth opening on a box with no addon at all,
    //   which is exactly the box that needs the catalogue. Task 59 made it a plain *Addons* tab
    //   with the addons the user pinned as tabs after it (lib/AddonMenu.svelte).
    // * *System* collects the four housekeeping pages - Services, Log, Firmware, Backup. Services
    //   and Log are both "what is the box doing"; Firmware and Backup are things one does to a box
    //   a few times a year, not daily.
    // * *Status*, *Radio* and *Network* stay tabs: they are what one comes here for.
    // * *Names* stays a tab of its own. It is the metadata store, not administration of the box,
    //   and it belongs to none of the groups.
    // * *Account* is now the user's own name in the right-hand corner, where a reader looks for it.
    type Tab = {path: string; label: string};
    // 28.1 + 30.4 (maintainer): Addons, Status, Interfaces, Metadata, System. Task 57 (D-68, D-80):
    // *System* is a tab that opens a popover menu (lib/SystemMenu.svelte) over the system pages,
    // which live under /system/<page> (lib/systemmenu.ts lists them, in the menu's order); Firmware
    // moved from the tab bar into that menu, after Backup.
    // Task 131 (D-86): Status, the pinned addons, Addons, System - Interfaces and Metadata moved into
    // the System menu (/system/interfaces; /radio is an alias). Task 193: the Metadata page went, the
    // App is the editor; /metadata, /names and /system/metadata open the App.
    // task 193: the App - the everyday view - is the second tab, beside Status
    // its name is Control (de: Bedienung); the route and the code keep saying app (the maintainer, 2026-09-22)
    const TABS: Tab[] = [{path: '/', label: 'Status'}, {path: '/app', label: 'Control'}];

    // B-81: an addon's settings page is /addon-settings/<id> (lib/routes.ts). /addons/<id>, where it
    // was, is lighttpd's on a box and only an in-app alias now, which the router rewrites.
    const addonId = $derived(segmentOf(router.path, SETTINGS_BASE));
    const navId = $derived(segmentOf(router.path, '/nav'));
    const navEntry = $derived(extra.find((e) => e.id === navId));
    // Task 88 (D-61): an addon's frontend is /nav/<the path segment its drop-in proxies> - /nav/red for
    // RedMatic's Node-RED at /addons/red/. /nav/<addon id>, the route before, from a bookmark or an
    // old link, is replaced by it once the nav list has answered, the query string kept.
    $effect(() => {
        if (!navLoaded || navId === '' || navEntry) return;
        const moved = extra.find((e) => e.addon === navId && e.id !== navId);
        if (moved) untrack(() => replace(`/nav/${encodeURIComponent(moved.id)}${router.search}`));
    });

    // ---- the kept addon pages (task 39) --------------------------------------------------------
    // The route names the page lib/FrameHost.svelte shows; NavFrame and AddonFrame hand it over.
    const activeFrame = $derived(addonId !== '' ? settingsKey(addonId) : navId !== '' ? frontendKey(navId) : '');
    // an addon's settings page is not kept: it goes as soon as another page is shown
    $effect(() => {
        const active = activeFrame;
        untrack(() => leaveFrames(active));
    });
    // A kept page goes when its addon changed (lib/frames.svelte.ts). The addon list is looked at
    // again when the user leaves a page that operates addons - Services, Installed addons, the
    // catalogue - and, at most every 15 s, when a kept page is shown again.
    let lastPath = router.path;
    $effect(() => {
        const path = router.path;
        const from = lastPath;
        lastPath = path;
        if (from === path) return;
        untrack(() => {
            if (['/system/services', '/addons', '/catalog'].some((b) => onPage(from, b))) void recheck(true);
            else if (activeFrame !== '') void recheck(false);
        });
    });

    // Addons used to be appended after every fixed tab, one flat tab each. They are one control
    // now, and it comes first (lib/AddonMenu.svelte, which takes the frontends and the addon list).
    // A nav.d drop-in that is not an addon (the stub's *CCU WebUI ↗*) was a tab after *System* until
    // 2026-09-16; the tab bar is strictly Status, the pins, Addons and System (D-86, the maintainer's
    // follow-up to task 131), so those entries are listed in the Addons dropdown, in a group of their
    // own under the addons.
    const addonNav = $derived(extra.filter((e) => e.source === 'addon'));
    const otherNav = $derived(extra.filter((e) => e.source !== 'addon'));

    function isActive(p: string): boolean {
        return p === '/' ? router.path === '/' : onPage(router.path, p);
    }

    // The System dropdown reads as a breadcrumb once something inside it is open ("System — Log"),
    // and it must not resize as the selection changes: it is sized up front by every label it can
    // produce, all rendered invisibly under the live one in the same grid cell (task 26), so the
    // cell is as wide as the widest of them.
    //
    // Which one *is* the widest is a question only the browser can answer: this picked the longest
    // string by character count until it was looked at, and "System — Sicherung" is exactly as long
    // as "System — Protokoll" and renders 9 px wider, so the bar grew on Backup - in German enough
    // to push the last nav entry onto a second header row - and on Firmware in both languages.
    // Every candidate goes in and the grid measures them.
    //
    // Task 27.7 caps how far that may go: reserving the widest German label outright wanted 323 px
    // of a 1280 px header for one control. app.css puts a max-width on the sizers, so the reserved
    // width stops at a sensible one and the live label is ellipsised inside it. The button is still
    // exactly as wide on every page - the bar cannot hop - and the whole breadcrumb is on the
    // button as a tooltip, in the popup, and on the page itself. (The Addons button dropped its
    // breadcrumb and its sizers in task 59: it is the word alone; the System dropdown went in task 57.)
    // task 57: the System tab is marked on every system route, and it carries a dot while any entry
    // of its menu has one (the worst of them); the menu itself is lib/SystemMenu.svelte, mounted once
    // at the end of the shell, anchored at this tab or at a page's title switcher. It closes itself
    // (Escape, a click outside, a route change), as the Addons dropdown does in lib/AddonMenu.svelte,
    // so the shell keeps no popup state of its own any more.
    const onSystem = $derived(systemPageAt(router.path) !== undefined);
    const systemDot = $derived(worstDot(systemMenu.dots));
    let systemTabEl = $state<HTMLElement | null>(null);
    $effect(() => {
        systemMenu.tab = systemTabEl;
    });
    // the dots: read at sign-in and once a minute; the menu reads them again each time it opens
    $effect(() => {
        if (!auth.authenticated || auth.mustChangePassword) {
            systemMenu.dots = {};
            return;
        }
        void refreshDots();
        const timer = setInterval(() => void refreshDots(), 60_000);
        return () => clearInterval(timer);
    });

    // task 193: the public principal sees the Control tab alone; the login is where the rest begins.
    // openccu-lite task 306: an account may take the Control tab out of the bar (Settings) - the page
    // stays at /app, so a bookmark, the installed app and the start page's fallback still work.
    const tabs = $derived(
        TABS.filter((n) => (auth.public ? n.path === '/app' : n.path !== '/app' || !prefs.appHidden)).map((n) => ({...n, text: t(n.label)})),
    );
    // task 44: the Log page fills the window (the shell bound to the viewport, the page a flex
    // column); the login page and the password notice that can stand in for it on the same route
    // flow as they do everywhere else
    const fill = $derived(auth.authenticated && !auth.mustChangePassword && onPage(router.path, '/system/log'));
    // task 193: the account's preferences (the addon pins, the App's choices) live with the login
    $effect(() => {
        if (!auth.authenticated) return;
        void loadPreferences();
        return resetPreferences;
    });
    // occulited task 24 (openccu-lite #11): an addon's frontend as the whole window too - when its
    // manifest declares ui.fullscreen (GET /addons: the addon's promise of its own way back to /)
    // and the user ticked it for the addon (Settings, kept with the account). The shell adds no way
    // back of its own. Only the frontend at /nav/<id>: the addon's settings page and a new tab are
    // not affected, nor a nav.d page that is no addon.
    const navFull = $derived.by(() => {
        if (navId === '' || !navEntry || navEntry.source !== 'addon') return false;
        const id = navEntry.addon ?? navEntry.id;
        return installed.some((a) => a.id === id && a.fullscreen === true) && addonFullscreen(id);
    });
    // the App as the whole window: the top bar goes while the App is open (Settings)
    const appFull = $derived(auth.authenticated && !auth.mustChangePassword && ((prefs.appFullscreen && onPage(router.path, '/app')) || navFull));
    // the start page: once, when the preferences arrive on the system's own address
    let startDone = false;
    $effect(() => {
        if (!prefs.loaded || startDone) return;
        startDone = true;
        // task 306: with Control's tab hidden the start page falls back to Status
        if (effectiveStartPage(prefs) === 'app' && router.path === '/') untrack(() => replace('/app'));
    });
    // Task 99: the top bar stays at the window's top while the page scrolls (app.css). Its height is
    // not one number - the bar wraps on a narrow window, and the German labels or the pinned tabs change it.
    // B-129 made the bar a row of a fixed-height shell, so anchors and table heads no longer need the
    // offset; the boot timeline's fixed unit panel still hangs below the bar by it, so the height is
    // measured here and named --ol-header-h on the root.
    let headerEl = $state<HTMLElement | null>(null);
    $effect(() => {
        const el = headerEl;
        const root = document.documentElement;
        if (!el) {
            root.style.setProperty('--ol-header-h', '0px'); // the App as the whole window: no bar
            return;
        }
        if (typeof ResizeObserver !== 'function') return;
        const measure = () => root.style.setProperty('--ol-header-h', `${el.getBoundingClientRect().height}px`);
        const observer = new ResizeObserver(measure);
        observer.observe(el);
        measure();
        return () => {
            observer.disconnect();
            root.style.removeProperty('--ol-header-h');
        };
    });

    // Task 191: on a phone the nav is the bar's second row, one line that scrolls sideways when the
    // tabs, the pinned addons and the two menu buttons do not fit (app.css). Two things need script
    // here: the flags that say which edge still has something to show, which the fade reads, and
    // bringing the active tab into view - a tab far to the right is otherwise off screen after a
    // reload or a jump from a menu. Both are no-ops on a wide window, where the row does not scroll.
    let navEl = $state<HTMLElement | null>(null);
    let navRowEl = $state<HTMLElement | null>(null);
    function edgeFlags(el: HTMLElement) {
        const row = navRowEl ?? el;
        const more = el.scrollWidth - el.clientWidth;
        if (more > 1 && el.scrollLeft > 1) row.setAttribute('data-more-left', '');
        else row.removeAttribute('data-more-left');
        if (more > 1 && el.scrollLeft < more - 1) row.setAttribute('data-more-right', '');
        else row.removeAttribute('data-more-right');
    }
    // the active tab into view: `scrollBy` on the row itself, never scrollIntoView - that one walks
    // up the ancestors and would scroll the page under the bar as well.
    function revealActive(el: HTMLElement) {
        if (el.scrollWidth - el.clientWidth <= 1) return;
        const active = el.querySelector<HTMLElement>('a.active, .ol-menubtn.active');
        if (!active) return;
        const a = active.getBoundingClientRect();
        const box = el.getBoundingClientRect();
        const margin = 12;
        const left = a.left - box.left - margin;
        const right = a.right - box.right + margin;
        const by = left < 0 ? left : right > 0 ? right : 0;
        if (by !== 0) el.scrollBy({left: by, behavior: reducedMotion() ? 'auto' : 'smooth'});
        edgeFlags(el);
    }
    $effect(() => {
        const el = navEl;
        if (!el) return;
        const read = () => edgeFlags(el);
        read();
        el.addEventListener('scroll', read, {passive: true});
        const size = typeof ResizeObserver === 'function' ? new ResizeObserver(read) : null;
        size?.observe(el);
        // what is in the row arrives later than the row does - the pinned addons come with the
        // account's preferences, a name changes with the language - and none of that resizes the
        // row itself, only what it can scroll. So the flags are read again whenever its content
        // changes, and a tab appearing or leaving brings the active one back into view; a favicon
        // or a class does not, or a row the user has scrolled by hand would jump back under them.
        const content = typeof MutationObserver === 'function'
            ? new MutationObserver((records) => {
                  read();
                  if (records.some((r) => r.type === 'childList')) revealActive(el);
              })
            : null;
        content?.observe(el, {childList: true, subtree: true});
        return () => {
            el.removeEventListener('scroll', read);
            size?.disconnect();
            content?.disconnect();
        };
    });
    $effect(() => {
        void router.path;
        const el = navEl;
        if (!el) return;
        void tick().then(() => revealActive(el));
    });

    // B-129: the box around `main` is what scrolls, not the document. The browser gives Page Up/Down,
    // Space, Home, End and the arrows to the focused element's scroll container, and where nothing on
    // the page has the focus - a fresh load, or the tab in the top bar that was just clicked - that is
    // the document, which now scrolls nothing. The keys are handed to the port in exactly those two
    // cases: nothing has the focus, or what has it is in the top bar. A control keeps its own keys -
    // a field, a list that scrolls itself, a button that Space presses - and so does anything inside
    // the page, where the keys already reach the port on their own.
    // B-132: an addon whose frame ends up on the box's own page - its session check failed and it sent
    // the frame to `/` - does not draw a second shell inside this one any more: that page shows a
    // notice instead (lib/framed.ts) and says so from the frame. The shell names the addon in the
    // browser console, which is where such a loop is diagnosed; the frame that sent it is the one whose
    // contentWindow the message came from.
    $effect(() => {
        const onMessage = (ev: MessageEvent) => {
            if (ev.origin !== location.origin || (ev.data as {type?: string} | null)?.type !== FRAMED_BACK) return;
            const el = Array.from(document.querySelectorAll('iframe')).find((f) => f.contentWindow === ev.source);
            const id = el?.dataset.addon || el?.title || '?';
            console.warn(`openccu-lite: the addon "${id}" sent its frame back to the shell's own page; the frame shows a notice instead of a second shell`);
        };
        window.addEventListener('message', onMessage);
        return () => window.removeEventListener('message', onMessage);
    });

    let portEl = $state<HTMLElement | null>(null);
    const SCROLL_KEYS = new Set(['PageDown', 'PageUp', 'Home', 'End', 'ArrowDown', 'ArrowUp', ' ']);
    function scrollPort(ev: KeyboardEvent) {
        const el = portEl;
        if (!el || ev.defaultPrevented || ev.ctrlKey || ev.metaKey || ev.altKey || !SCROLL_KEYS.has(ev.key)) return;
        const active = document.activeElement as HTMLElement | null;
        const loose = !active || active === document.body;
        if (!loose && !active.closest?.('.ol-header')) return;
        // Space in the bar belongs to the button or link that has the focus
        if (ev.key === ' ' && !loose) return;
        const step = Math.max(40, el.clientHeight * 0.9);
        if (ev.key === 'Home') el.scrollTo({top: 0});
        else if (ev.key === 'End') el.scrollTo({top: el.scrollHeight});
        else if (ev.key === 'ArrowDown') el.scrollBy({top: 40});
        else if (ev.key === 'ArrowUp') el.scrollBy({top: -40});
        else if (ev.key === 'PageDown') el.scrollBy({top: step});
        else if (ev.key === 'PageUp') el.scrollBy({top: -step});
        else el.scrollBy({top: ev.shiftKey ? -step : step});
        ev.preventDefault();
    }
</script>

<svelte:window onkeydown={scrollPort} />

<!-- B-129: the shell is exactly one viewport tall - the top bar a row of it that does not scroll,
     `.ol-scrollport` around `main` the box that does, so the page's scroll bar begins under the bar
     and the bar spans the whole window width. On /log (task 44) and in an addon's frame the port does
     not scroll: the page's own journal box, or the frame, takes the height that is left. -->
<div class="ol-shell" class:ol-fill={fill}>
    {#if !appFull}
    <header class="ol-header" bind:this={headerEl}>
        <!-- task 177: while a page's first visit is covered, a soft light runs once along the bar's
             bottom edge in COVER_MAX_MS, and goes as soon as the page is ready (placed first: the power
             control stays the bar's last) -->
        {#if entering}<div class="ol-loadbar" style={`--ol-loadbar-ms: ${COVER_MAX_MS}ms`} out:fade={{duration: reducedMotion() ? 0 : 150}} aria-hidden="true"></div>{/if}
        <a class="ol-brand" href="/" use:link aria-label="openccu-lite">
            <img class="ol-logo ol-logo-light" src={logoLight} srcset={`${logoLight} 1x, ${logoLight2x} 2x, ${logoLight3x} 3x`} alt="openccu-lite" />
            <img class="ol-logo ol-logo-dark" src={logoDark} srcset={`${logoDark} 1x, ${logoDark2x} 2x, ${logoDark3x} 3x`} alt="" aria-hidden="true" />
        </a>
        <!-- task 191: the wrapper is `display: contents` above 700 px and the bar's second row below
             it; it carries the fade at the edge that still has something to show -->
        <div class="ol-navrow" bind:this={navRowEl}>
            <nav class="ol-nav" aria-label="Navigation" bind:this={navEl}>
                {#each (auth.authenticated ? tabs : []) as n (n.path)}
                    <a href={n.path} use:link class:active={isActive(n.path)}>{n.text}</a>
                {/each}
                {#if auth.authenticated && !auth.public}
                    <!-- Task 26: the addons are one dropdown. Its footer holds the two pages about
                         addons - installed and catalogue - so the subject is one control; that is why
                         the dropdown renders with no addon installed too. Task 59: a plain *Addons* tab
                         with the addons the user pinned as tabs of their own; task 131 (D-86): those
                         stand after Status and before *Addons* (lib/AddonMenu.svelte renders both). -->
                    <AddonMenu {installed} {installedLoaded} nav={addonNav} links={otherNav} {navId} {addonId} />
                {/if}
                {#if auth.authenticated && !auth.public}
                    <!-- task 57 (D-68): the box's own housekeeping behind one tab, which always opens the
                         menu - a popover under it, a bottom sheet on a phone (lib/SystemMenu.svelte).
                         Task 188: the filter takes the focus whenever the open cannot bring up an
                         on-screen keyboard - the keyboard's own click and a mouse or pen - and never on a
                         tap, which would cover the sheet it just opened (lib/systemmenu.svelte.ts's
                         opensFilter). The tab says the shortcut in its tooltip and its aria-keyshortcuts,
                         and the menu shows it beside the filter. -->
                    <button
                        type="button"
                        class="ol-menubtn ol-systab"
                        class:active={onSystem}
                        aria-haspopup="menu"
                        aria-expanded={systemMenu.open && systemMenu.anchor === systemTabEl}
                        bind:this={systemTabEl}
                        onclick={(ev) => toggleSystemMenu(systemTabEl, opensFilter(ev))}
                        title={t('System menu ({keys})', {keys: shortcutLabel()})}
                        aria-keyshortcuts="Control+K Meta+K"
                    >
                        <span class="ol-menubtn-live"
                            ><span class="ol-menubtn-text">{t('System')}</span
            ><!-- the dot's place is reserved while there is none: it arrives with the warnings after the
                                 load, and a tab that widened then would re-wrap the bar on a phone under a page
                                 that has already scrolled to its anchor (task 26: the bar cannot hop) --><span
                                class="ol-sysdot"
                                class:error={systemDot === 'error'}
                                class:warning={systemDot === 'warning'}
                                class:ol-sysdot-none={!systemDot}
                                role={systemDot ? 'img' : undefined}
                                aria-label={systemDot === 'error' ? t('Error') : systemDot === 'warning' ? t('Warning') : undefined}
                                aria-hidden={systemDot ? undefined : 'true'}
                            ></span><span class="ol-caret" aria-hidden="true">▾</span
                        ></span>
                    </button>
                {/if}
            </nav>
        </div>
        <div class="ol-spacer"></div>
        <!-- task 29: the right-hand icons - the source, the account, the settings. The theme and
             language switches moved to Settings. -->
        <!-- task 179: the licences of everything in the image, from its SBOM - also before the login -->
        <a class="ol-iconlink ol-licences" class:active={isActive('/licenses')} href="/licenses" use:link title={t('Licenses')} aria-label={t('Licenses')}><Icon name="licences" size={18} /></a>
        <a class="ol-iconlink" href="https://github.com/hobbyquaker/openccu-lite" target="_blank" rel="noopener" title="GitHub" aria-label="openccu-lite on GitHub"><Icon name="github" size={18} /></a>
        {#if auth.authenticated && auth.public}
            <!-- task 193: the public principal: the way to sign in, nothing else of the bar -->
            <a class="ol-iconlink" class:active={isActive('/login')} href="/login" use:link title={t('Sign in')} aria-label={t('Sign in')} data-public-login><Icon name="user" size={18} /></a>
        {:else if auth.authenticated}
            <a class="ol-iconlink" class:active={isActive('/account')} class:ol-authoff={auth.authOff} href="/account" use:link title={auth.authOff ? t('Login is off — anonymous administrator') : `${t('Account')}: ${auth.user}`} aria-label={t('Account')}><Icon name="user" size={18} /></a>
            <a class="ol-iconlink" class:active={isActive('/settings')} href="/settings" use:link title={t('Settings')} aria-label={t('Settings')}><Icon name="settings" size={18} /></a>
            <!-- task 60: reboot, halt, recovery - at the far right, for an administrator only -->
            {#if auth.role === 'admin'}<PowerMenu />{/if}
        {/if}
    </header>
    {/if}
    <!-- B-129: the scrolling box. `main` keeps its centred column and its page classes; the port is
         what carries the scroll bar, so the bar belongs to the window's right edge and starts at the
         top bar's lower edge. -->
    <div class="ol-scrollport" class:ol-wide={addonId !== '' || navId !== ''} class:ol-fill={fill} bind:this={portEl}>
    <!-- every page test is onPage (lib/routes.ts): the path itself or one below it, so /login is
         not /log (B-63) -->
    <main class="ol-main" class:ol-entering={entering} class:ol-wide={addonId !== '' || navId !== ''} class:ol-full={onPage(router.path, '/system/log') || onPage(router.path, '/system/services') || onPage(router.path, '/system/firewall') || router.path === '/addons'} class:ol-fill={fill}>
        <!-- task 94: after a reboot from here, the interfaces' bar and the timing report -->
        {#if auth.authenticated}<BootReady />{/if}
        {#if !auth.loaded}
            <div class="ol-muted">…</div>
        {:else if !auth.authenticated}
            {#if onPage(router.path, '/licenses')}<LicensesPage />{:else}<LoginPage />{/if}
        {:else if auth.public && !onPage(router.path, '/app') && !onPage(router.path, '/licenses')}
            <!-- task 193: the public principal has the Control app; every other page is the login's -->
            <LoginPage />
        {:else if auth.mustChangePassword && !onPage(router.path, '/account')}
            <div class="ol-notice">{t('Please change your password now.')} <a href="/account" use:link>{t('Account')}</a></div>
        {:else if onPage(router.path, '/login')}
            <!-- signed in: sends the browser on to ?return= or to Status -->
            <LoginPage />
        {:else if onPage(router.path, '/licenses')}
            <LicensesPage />
        {:else if addonId !== ''}
            <AddonFrame id={addonId} />
        {:else if navId !== '' && navEntry}
            <NavFrame entry={navEntry} />
        {:else if navId !== '' && !navLoaded}
            <div class="ol-muted" style="padding:14px">…</div>
        {:else if navId !== ''}
            <!-- openccu-lite B-297: the addon this page belonged to was uninstalled (here or elsewhere) -->
            <div class="ol-notice" style="margin:14px" data-nav-gone>{t('This addon is no longer installed.')} <a href="/addons" use:link>{t('Addons')}</a></div>
        {:else if onPage(router.path, '/welcome')}
            <WelcomePage />
        {/if}
        <!-- task 177: the pages stay mounted once visited, hidden while another shows (as the addon
             frames do); a logout unmounts them all -->
        {#if keepPages}
            {#each KEPT as p (p.key)}
                {#if visited.includes(p.key)}
                    <div class="ol-keptpage" data-page={p.key} hidden={p.key !== activeKey}>
                        <KeptPage life={lifeOf(p.key)} page={p.component} />
                    </div>
                {/if}
            {/each}
        {/if}
        <!-- task 39: the kept addon pages, beside whatever page shows - outside the branches above, so
             switching pages never moves or unmounts them; a logout unmounts them all -->
        {#if auth.authenticated && !auth.mustChangePassword}
            <FrameHost active={activeFrame} />
        {/if}
    </main>
    </div>
    <ConfirmDialog />
    <!-- task 57: the System menu, one panel for the tab, the page titles and Ctrl/⌘+K -->
    {#if auth.authenticated && !auth.mustChangePassword && !auth.public}<SystemMenu />{/if}
</div>

