<script lang="ts">
    /*
     * The Addons page (task 139): one page built on the catalogue, in place of *Installed addons*
     * and *Catalogue*. Every catalogue entry is a card, and so is an installed addon the catalogue
     * does not know (a file install, an adopted CCU addon). Installed first, those with an update
     * on top, then the rest by stars. A card carries what the box knows - the state dot of the
     * addon's unit as the Services page shows it, the version, the badges - and the actions the
     * maintainer named (Q&A 2026-09-16): Install, or Open / Settings / Update to x.y, the Log and
     * Service links always, the rare ones (Start/Stop, Enable/Disable at boot, Reinstall, the
     * legacy session, Uninstall) in one ⋯ menu. Pinning and ordering stay in the Addons popup
     * (the maintainer, 2026-09-22); a card with a frontend says so in one quiet line.
     *
     * Above the list: the filter, *Installed only* (remembered per browser), *Check for updates*
     * (the catalogue refresh) and *Update all* when more than one update waits. Below: the
     * addons' ports (task 157), the legacy session and early start switches, Install from file.
     */
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, REQUEST_HEADER, type Addon, type NavEntry, type Service} from '../lib/api';
    import {ask, askSelect} from '../lib/dialog.svelte';
    import {t, i18n} from '../lib/i18n.svelte';
    import {link} from '../lib/router.svelte';
    import {auth} from '../lib/auth.svelte';
    import Loading from '../lib/Loading.svelte';
    import TokenNotice from '../lib/TokenNotice.svelte';
    import Help from '../lib/Help.svelte';
    import {warnEdge} from '../lib/systemmenu.svelte';
    import {worse, type Edge} from '../lib/warnedge';
    import InstallProgress from '../lib/InstallProgress.svelte';
    import {addonsChanged} from '../lib/addonsync';
    import AddonPorts from '../lib/AddonPorts.svelte';
    import AddonSessionSettings from '../lib/AddonSessionSettings.svelte';
    import AddonStartSettings from '../lib/AddonStartSettings.svelte';
    import {addonIconSrc} from '../lib/addonicon';
    import {imageCandidates, type AddonImages} from '../lib/addonimages';
    import AddonImage from '../lib/AddonImage.svelte';
    import {isDark} from '../lib/theme.svelte';
    import {compareEntries, formatStars, httpURL, releasesProblemKind, repoURL} from '../lib/catalog';
    import {askStartStopped, installFromCatalog, type StoppedAddon} from '../lib/install';
    import ReleasesProblem from '../lib/ReleasesProblem.svelte';
    import {statusDot, statusKind} from '../lib/units';
    import {scrollToAnchor} from '../lib/anchor';
    import CheckDaily from '../lib/CheckDaily.svelte';

    interface InstallResult { exit: number; meaning: string; reboot_required: boolean; output?: string; seconds: number; stopped_addons?: StoppedAddon[] }
    interface UpdateInfo { installed: string; available: string; update_available: boolean; url: string; error?: string }
    // a catalogue item (D-119): the entry - repository, manifest path, untested - and the addon's
    // manifest flattened into it once the user's check has fetched it (id, name, description, ...)
    interface Text { de?: string; en?: string }
    interface Entry {
        git: string; manifest_path: string; untested?: boolean; adapter?: boolean;
        id?: string; name?: Text; description?: Text; homepage?: string; licence?: string;
        release?: {github?: string; prerelease?: boolean}; requires?: {architectures?: string[]; lite?: string};
        ui?: {own_updater?: boolean}; runtime?: {note?: Text};
        /** openccu-lite task 100: the manifest's images as the check fetched them, kind → URL on this origin */
        images?: AddonImages;
        tag?: string; fetched?: string; error?: string; stars?: number;
        latest?: {version: string; asset?: string; notes_url?: string}; update_available?: boolean;
        /** task 26: the release notes of the offered version (the manifest's changelog, else the release's page) */
        release_notes?: string;
    }
    interface FwAddon { id: string; ports: {port: number}[] }

    // releases_error (B-21): a release list the last check could not read - the versions shown are from before it
    interface ReleasesError { code: string; repo: string; message: string; at: string; retry_minutes?: number }
    // occulited B-52: a check that failed - the published catalogue not fetched, no manifest read,
    // nothing loaded - with the host and the reason, and how many in a row since when
    interface CheckError { at: string; message: string; host?: string; failures: number; since: string }
    interface CatalogView {
        addons: Entry[]; checked?: string; releases_error?: ReleasesError;
        /** B-52: `bundled` while the list is the image's own copy alone, `published` once a check reached GitHub */
        source?: 'published' | 'bundled'; bundled_date?: string; check_error?: CheckError;
    }
    let catalogue = $state<CatalogView | null>(null);
    // B-52: what went wrong reading the catalogue - never silent. catalogError: the system did not
    // answer the list (the page shows the installed addons alone); checkError: the check's request
    // itself failed (the system's check_error says why when it got that far)
    let catalogError = $state('');
    let checkError = $state('');
    let installedVersions = $state<Record<string, string>>({});
    let arch = $state('');
    let addons = $state<Addon[] | null>(null);
    let services = $state<Record<string, Service>>({});
    let nav = $state<NavEntry[]>([]);
    let ports = $state<Record<string, number>>({});
    let error = $state('');
    let notice = $state('');
    let busy = $state('');
    let checking = $state('');
    let refreshing = $state(false);
    let updates = $state<Record<string, UpdateInfo>>({});
    let installBusy = $state(false);
    let progressView: InstallProgress | undefined = $state();
    // ?q=<id> deep-links a card (the old Catalogue's way; /catalog?q= is an alias of this page)
    let filter = $state(new URLSearchParams(location.search).get('q') ?? '');
    const INSTALLED_KEY = 'ol.addons.installedOnly';
    let installedOnly = $state(false);
    try {
        installedOnly = localStorage.getItem(INSTALLED_KEY) === '1';
    } catch {
        /* no storage */
    }
    $effect(() => {
        try {
            localStorage.setItem(INSTALLED_KEY, installedOnly ? '1' : '0');
        } catch {
            /* no storage */
        }
    });

    // task 125 (D-77): the legacy session's switches, and task 119 (D-75): the early start's - the
    // global ones in their sections below the list, the per-addon ones in the ⋯ menu
    interface LegacyView { enabled: boolean; off: string[] }
    let legacy = $state<LegacyView | null>(null);
    const legacyOffFor = (a: Addon) => !!legacy && (!legacy.enabled || legacy.off.includes(a.id));
    const legacyCandidate = (a: Addon) => !a.session_header && !!(a.config_url || a.settings?.config_url);
    interface EarlyView { enabled: boolean; off: string[] }
    let early = $state<EarlyView | null>(null);
    const earlyOffFor = (a: Addon) => !!early && (!early.enabled || early.off.includes(a.id));

    async function load(refresh = false) {
        try {
            addons = (await api.get<{addons: Addon[]}>('/api/system/v1/addons')).addons;
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
        await loadCatalogue(refresh);
        try {
            const r = await api.get<{services: Service[]}>('/api/system/v1/services');
            services = Object.fromEntries(r.services.filter((s) => s.kind === 'addon').map((s) => [s.id, s]));
        } catch {
            services = {};
        }
        try {
            nav = (await api.get<{entries: NavEntry[]}>('/api/system/v1/nav')).entries;
        } catch {
            nav = [];
        }
        try {
            ports = Object.fromEntries((await api.get<FwAddon[]>('/api/system/v1/firewall/addons')).map((a) => [a.id, a.ports.length]));
        } catch {
            ports = {};
        }
    }
    async function loadCatalogue(refresh: boolean): Promise<void> {
        try {
            const r = await api.get<{catalog: CatalogView; installed: Record<string, string>; arch: string; daily?: boolean}>(`/api/system/v1/catalog${refresh ? '?refresh=1' : ''}`);
            catalogue = r.catalog;
            daily = r.daily ?? null;
            installedVersions = r.installed;
            arch = r.arch;
            catalogError = '';
            if (refresh) checkError = '';
        } catch (e) {
            if (refresh) {
                // the check failed as a request: say so, and show the list as the system holds it now
                checkError = (e as Error).message;
                await loadCatalogue(false);
                return;
            }
            // 501: this system has no catalogue - the installed addons alone, nothing to report
            catalogError = (e as {status?: number}).status === 501 ? '' : (e as Error).message;
            catalogue = catalogue ?? {addons: []};
        }
    }
    const life = pageLife();
    onMount(() => {
        void load().then(() => scrollToAnchor('addon-ports'));
        if (auth.role === 'admin') void resumeInstall();
        return life.onReturn(() => void load());
    });

    // ---- the cards ------------------------------------------------------------------------------
    interface Card { id: string; name: string; entry: Entry | null; addon: Addon | null; update: string }
    const lang = $derived(i18n.language);
    const admin = $derived(auth.role === 'admin');
    const byId = $derived(new Map((addons ?? []).map((a) => [a.id, a])));
    // a manifest text in the user's language, else English, else German
    const tx = (x?: Text): string => x?.[lang] ?? x?.en ?? x?.de ?? '';
    // an entry whose manifest is not fetched yet is known by its repository alone
    const repoName = (e: Entry): string => e.git.replace(/^https?:\/\/[^/]+\//, '').replace(/\/+$/, '');
    const cardId = (e: Entry): string => e.id ?? `repo-${repoName(e).replace(/[^A-Za-z0-9_.-]+/g, '-')}`;
    const entryOf = (id: string): Entry | null => catalogue?.addons.find((e) => e.id && (e.id === id || e.id === id.replace(/^hm-/, ''))) ?? null;
    const cards = $derived.by((): Card[] => {
        const out: Card[] = [];
        const seen = new Set<string>();
        for (const e of catalogue?.addons ?? []) {
            const a = e.id ? byId.get(e.id) ?? null : null;
            const id = cardId(e);
            // B-52: two entries whose manifests name one id would be one key twice - the list would
            // not render at all; the first entry wins, as the system's merge does for a repository
            if (seen.has(id)) continue;
            seen.add(id);
            if (a) seen.add(a.id);
            out.push({id, name: tx(e.name) || e.id || repoName(e), entry: e, addon: a, update: a && e.update_available && e.latest ? e.latest.version : ''});
        }
        for (const a of addons ?? []) {
            if (seen.has(a.id) || seen.has(a.id.replace(/^hm-/, ''))) continue;
            out.push({id: a.id, name: a.name || a.id, entry: null, addon: a, update: ''});
        }
        // installed first - those with an update at the top, then by name - then the rest by stars
        return out.sort((x, y) => {
            const ix = x.addon ? (x.update ? 0 : 1) : 2;
            const iy = y.addon ? (y.update ? 0 : 1) : 2;
            if (ix !== iy) return ix - iy;
            if (ix < 2) return x.name.localeCompare(y.name, undefined, {sensitivity: 'base'}) || x.id.localeCompare(y.id);
            return compareEntries({id: x.id, name: x.name, stars: x.entry?.stars, git: x.entry?.git}, {id: y.id, name: y.name, stars: y.entry?.stars, git: y.entry?.git});
        });
    });
    const shown = $derived(cards.filter((c) => {
        if (installedOnly && !c.addon) return false;
        if (!filter) return true;
        // the repository's name counts only where it is all the card has (no manifest yet)
        const hay = `${c.id} ${c.name} ${tx(c.entry?.description)} ${c.entry && !c.entry.id ? repoName(c.entry) : ''}`.toLowerCase();
        return hay.includes(filter.toLowerCase());
    }));
    const pending = $derived(cards.filter((c) => c.update));
    // B-52: a first visit - no check has reached GitHub yet - and what failed, in the user's words
    const firstVisit = $derived(!!catalogue && !catalogue.checked && catalogue.source !== 'published');
    // the system's own words for a check cut off by its time limit, translated; other causes are the network's
    const checkMessage = (m: string): string => (m === 'the check did not finish in time' ? t('the check did not finish in time') : m);
    const failure = $derived(checkError || (catalogue?.check_error ? checkMessage(catalogue.check_error.message) : '') || catalogError);
    const when = (iso: string) => new Date(iso).toLocaleString(lang);
    const day = (iso: string) => new Date(iso).toLocaleDateString(lang);
    const archs = (e: Entry) => e.requires?.architectures ?? [];
    const supportsArch = (e: Entry) => archs(e).length === 0 || archs(e).includes(arch) || archs(e).includes('any');
    // the logo (openccu-lite task 100): what the manifest declares, in the theme's variant - the
    // installed addon's images, else the catalogue's copy (lib/addonimages.ts) - then the logo of
    // the addon's own Info: line from its /addons/<id>/ (lib/addonicon.ts); the letter when none loads
    const logo = (c: Card): string[] => {
        const out = imageCandidates(c.addon?.images ?? c.entry?.images, 'logo', isDark());
        const info = c.addon ? addonIconSrc(c.addon.id, c.addon.info) : '';
        if (info) out.push(info);
        return out;
    };
    const letter = (c: Card) => c.name.trim().slice(0, 1).toUpperCase();
    // the addon's frontend, as the shell routes it (/nav/<id>, task 88)
    const frontend = (a: Addon): NavEntry | undefined => nav.find((n) => (n.addon ?? n.id) === a.id && n.source === 'addon');
    // the unit's state as the Services page shows it (lib/units.ts), from the same listing; an
    // addon the listing does not carry (a busybox box) falls back on the addon's own running flag
    const unitOf = (a: Addon): Service | null => services[`addon-${a.id}`] ?? null;
    // occulited task 12: the warnings of the Status page that name an addon lead here, and its card
    // shows their edge; a card in an error state of its own (failed, ended) keeps the red one
    // addon-update is no edge (the maintainer, 2026-10-03): the card's update button says it
    const ADDON_WARNINGS = ['addon-payload', 'addon-ended', 'addon-failed', 'rega', 'arch', 'legacy-session'];
    const cardEdge = (a: Addon | null | undefined): Edge => {
        if (!a) return '';
        const own: Edge = ['failed', 'ended'].includes(statusKind(unitOf(a) ?? {running: a.running, oneshot: a.oneshot, result: a.result, ended: a.ended})) ? 'err' : '';
        return worse(own, warnEdge(ADDON_WARNINGS, a.id));
    };
    const dotOf = (a: Addon) => statusDot(unitOf(a) ?? {running: a.running, oneshot: a.oneshot, result: a.result, ended: a.ended});
    const stateWord = (a: Addon): string => {
        const k = statusKind(unitOf(a) ?? {running: a.running, oneshot: a.oneshot, result: a.result, ended: a.ended});
        if (!a.enabled && k === 'stopped') return t('disabled');
        return k === 'running' ? t('Running') : k === 'starting' ? t('Starting') : k === 'completed' ? t('Completed') : k === 'ended' ? t('Exited') : k === 'failed' ? t('Failed') : k === 'skipped' ? t('skipped') : t('Stopped');
    };
    const catKnows = (a: Addon) => !!entryOf(a.id)?.latest;

    // ---- task 146 (D-106): addons a restore brought back without their program files ---------
    const toReinstall = $derived((addons ?? []).filter((a) => a.payload_missing && !a.reinstall_dismissed));
    // occulited task 23: why the addon's lighttpd fragment is not in use, with the line when the
    // verdict names one
    function rejectedWhy(r: NonNullable<Addon['lighttpd_rejected']>): string {
        return r.line && r.statement
            ? t("The system refused this addon's lighttpd fragment (etc/lighttpd.conf in its directory), so lighttpd serves nothing through it: {reason} — line {line}: {statement}. The output of the install names it too; an install that brings a fragment the check accepts clears this.", {reason: r.reason, line: r.line, statement: r.statement})
            : t("The system refused this addon's lighttpd fragment (etc/lighttpd.conf in its directory), so lighttpd serves nothing through it: {reason}. The output of the install names it too; an install that brings a fragment the check accepts clears this.", {reason: r.reason});
    }
    // the catalogue entry that can reinstall it: known, with a release for this system
    const reinstallEntry = (a: Addon): Entry | null => {
        const e = entryOf(a.id);
        return e?.id && e.latest && supportsArch(e) ? e : null;
    };
    const reinstallable = $derived(toReinstall.filter((a) => reinstallEntry(a)));
    async function reinstall(a: Addon) {
        const e = reinstallEntry(a);
        if (!e?.id) return;
        notice = '';
        if (!(await ask({title: t('Reinstall {name}', {name: a.name || a.id}), message: t('Reinstall {name} {version} from the catalogue? The program files come back; the settings and data the restore brought stay.', {name: a.name || a.id, version: e.latest?.version ?? ''}), confirm: t('Reinstall')}))) return;
        try {
            await api.post(`/api/system/v1/catalog/${encodeURIComponent(e.id)}/install`);
        } catch (err) {
            notice = `${a.name || a.id}: ${(err as Error).message}`;
            return;
        }
        await progressView?.poll();
    }
    // every reinstallable one, one after another, after one question that lists them (as Update all)
    async function reinstallAll() {
        const list = reinstallable.map((a) => `${a.name || a.id}: ${reinstallEntry(a)?.latest?.version ?? ''}`).join('\n');
        if (!(await ask({title: t('Reinstall all'), message: t('Reinstall these addons from the catalogue, one after another? The program files come back; the settings and data the restore brought stay.') + '\n\n' + list, confirm: t('Reinstall all')}))) return;
        notice = '';
        for (const a of reinstallable) {
            const e = reinstallEntry(a);
            if (!e?.id) continue;
            try {
                await api.post(`/api/system/v1/catalog/${encodeURIComponent(e.id)}/install`);
            } catch (err) {
                notice = `${a.name || a.id}: ${(err as Error).message}`;
                break;
            }
            await progressView?.poll();
            while (installBusy) {
                await new Promise((r) => setTimeout(r, 500));
                await progressView?.poll();
            }
        }
        await load();
    }
    async function dismissReinstall(a: Addon) {
        if (!(await ask({message: t('Hide the reinstall hint for {name}? It comes back with a new version of the addon, and the ⋯ menu of its card still offers Reinstall.', {name: a.name || a.id}), confirm: t('Dismiss')}))) return;
        await withBusy(`dismiss-${a.id}`, async () => {
            await api.post(`/api/system/v1/addons/${encodeURIComponent(a.id)}/reinstall-dismiss`);
        });
    }

    // ---- the actions ----------------------------------------------------------------------------
    async function withBusy(id: string, fn: () => Promise<void>) {
        busy = id;
        notice = '';
        try {
            await fn();
        } catch (e) {
            notice = (e as Error).message;
        } finally {
            busy = '';
            await load();
            addonsChanged(); // openccu-lite B-297: the menu follows at once
        }
    }
    async function install(c: Card) {
        if (!c.entry?.id) return;
        notice = '';
        const r = await installFromCatalog({id: c.entry.id, name: c.name, latest: c.entry.latest}, c.addon?.version ?? installedVersions[c.id]);
        if (r === null) return;
        if (r) notice = r;
        else await progressView?.poll();
    }
    // task 139: every update, one after another, after one question that lists them
    async function updateAll() {
        const list = pending.map((c) => `${c.name}: ${c.addon?.version ?? ''} → ${c.update}`).join('\n');
        if (!(await ask({title: t('Update all'), message: t('Update these addons, one after another? Each replaces its own installation; the configuration is kept.') + '\n\n' + list, confirm: t('Update all')}))) return;
        notice = '';
        for (const c of pending) {
            try {
                await api.post(`/api/system/v1/catalog/${encodeURIComponent(c.id)}/install`);
            } catch (e) {
                notice = `${c.name}: ${(e as Error).message}`;
                break;
            }
            await progressView?.poll();
            // the run ends on its own; wait for it before the next one starts
            while (installBusy) {
                await new Promise((r) => setTimeout(r, 500));
                await progressView?.poll();
            }
        }
        await load();
    }
    // task 244: Check daily behind Check for updates - the catalogue's releases and the installed
    // addons' own update checks, once a day; null when this system has no catalogue
    let daily = $state<boolean | null>(null);
    async function setDaily(on: boolean) {
        await api.put('/api/system/v1/catalog/settings', {daily: on});
        daily = on;
    }
    async function checkUpdates() {
        refreshing = true;
        try {
            await load(true);
            // the addons the catalogue does not know: their own check
            for (const a of addons ?? []) {
                if (a.update && !catKnows(a)) await checkUpdate(a);
            }
        } finally {
            refreshing = false;
        }
    }
    async function checkUpdate(a: Addon) {
        checking = a.id;
        try {
            updates = {...updates, [a.id]: await api.get<UpdateInfo>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/update`)};
        } catch (err) {
            updates = {...updates, [a.id]: {installed: a.version, available: '', update_available: false, url: '', error: (err as Error).message}};
        } finally {
            checking = '';
        }
    }
    async function setEnabled(a: Addon, enabled: boolean) {
        if (enabled && a.rega_dependent && !(await ask(t('{name} needs the ReGa, which this system does not have: it will start, and fail or misbehave. Enable it anyway?', {name: a.name || a.id})))) return;
        if (enabled && a.binary_incompatible && !(await ask(t('{name} carries binaries this system cannot execute ({why}). Install a release built for this architecture — from the catalogue, if it has one. Enable it anyway?', {name: a.name || a.id, why: a.binary_reason ?? ''})))) return;
        await withBusy(a.id, async () => {
            await api.post(`/api/system/v1/addons/${encodeURIComponent(a.id)}/${enabled ? 'enable' : 'disable'}`);
        });
    }
    // Start, Stop and Restart are the unit's, as on the Services page; Restart on an addon outside
    // its unit puts it back (task 48)
    async function unitAction(a: Addon, action: 'start' | 'stop' | 'restart') {
        if (action === 'restart' && a.stray && !(await ask(t('Restart {id}? It is stopped where it runs now and started again in its unit.', {id: `addon-${a.id}`})))) return;
        await withBusy(a.id, async () => {
            const r = await api.post<{output?: string}>(`/api/system/v1/services/${encodeURIComponent(`addon-${a.id}`)}/${action}`);
            notice = r.output?.trim() ?? '';
        });
    }
    async function uninstall(a: Addon) {
        if (!(await ask({message: t('Uninstall {name}? Its configuration under /usr/local may be left behind by the addon itself.', {name: a.name || a.id}), confirm: t('Uninstall'), danger: true}))) return;
        await withBusy(a.id, async () => {
            const r = await api.post<{output: string; system_removed?: string[]}>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/uninstall`);
            // B-283: a confined addon's script cannot remove the rc.d entry and its directories itself
            // (root owns the parents) and says "permission denied"; the system did that, and says so
            const removed = r.system_removed?.length ? ` — ${t('The system removed the remaining files: {list}.', {list: r.system_removed.join(', ')})}` : '';
            notice = `${t('Uninstalled')}: ${a.name || a.id}${r.output ? ` — ${r.output}` : ''}${removed}`;
        });
    }
    // occulited task 28: a script that came along from the CCU and fails here (a CCU3 user's own
    // hack, written for the CCU3's kernel) - one click removes its rc.d entry; the file its link led
    // to is named and removed only when ticked
    async function removeRCEntry(a: Addon) {
        const message = t('Remove the rc.d entry of {name}? The script came along from the CCU; it is no longer started, and its unit goes. The CCU backup from before the switch keeps it.', {name: a.name || a.id});
        let target = false;
        if (a.rc_target) {
            const picked = await askSelect({message, confirm: t('Remove'), danger: true, select: {options: [{value: 'entry', label: t('Only the rc.d entry')}, {value: 'target', label: t('The rc.d entry and {path}', {path: a.rc_target})}], initial: 'entry'}});
            if (picked === null) return;
            target = picked === 'target';
        } else if (!(await ask({message, confirm: t('Remove'), danger: true}))) return;
        await withBusy(a.id, async () => {
            const r = await api.post<{removed: string[]}>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/remove-rc-entry`, {target});
            notice = t('Removed: {list}', {list: (r.removed ?? []).join(', ')});
        });
    }
    async function setLegacy(a: Addon, enabled: boolean) {
        await withBusy(a.id, async () => {
            legacy = await api.put<LegacyView>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/legacy-session`, {enabled});
        });
    }
    async function setEarly(a: Addon, enabled: boolean) {
        await withBusy(a.id, async () => {
            early = await api.put<EarlyView>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/early-start`, {enabled});
            notice = enabled
                ? t('{name} starts early from the next boot on.', {name: a.name || a.id})
                : t('{name} waits for the radio interfaces from the next boot on.', {name: a.name || a.id});
        });
    }

    // ---- Install from file ----------------------------------------------------------------------
    let file = $state<File | null>(null);
    let installing = $state(false);
    let result = $state<InstallResult | null>(null);
    let rebootRequired = $state(false);
    // B-4: the upload is stored by the request, the install runs as a job on the system and the page
    // polls it - lighttpd's reload during an install ends requests, not the install
    interface InstallJob { id: string; state: 'running' | 'done' | 'failed'; started: string; bytes: number; result?: InstallResult; error?: string }
    let installSeconds = $state(0);
    async function followJob(id: string): Promise<InstallJob> {
        const started = Date.now();
        for (;;) {
            try {
                const job = await api.get<InstallJob>(`/api/system/v1/addons/install?job=${encodeURIComponent(id)}`);
                if (job.state !== 'running') return job;
            } catch (err) {
                // the web server reloads during an install: a poll it cut (no answer, or a 5xx from the
                // proxy) is asked again; a 4xx - the job gone with a restart, the session ended - is
                // the answer
                const status = (err as {status?: number}).status ?? 0;
                if (status >= 400 && status < 500) throw err;
            }
            installSeconds = Math.round((Date.now() - started) / 1000);
            await new Promise((r) => setTimeout(r, 1000));
        }
    }
    async function finishInstall(job: InstallJob) {
        if (job.error) notice = job.error;
        else if (job.result) {
            result = job.result;
            if (job.result.reboot_required) rebootRequired = true;
        }
    }
    async function installFile(e: Event) {
        e.preventDefault();
        if (!file) return;
        installing = true;
        installSeconds = 0;
        result = null;
        notice = '';
        try {
            const fd = new FormData();
            fd.append('file', file);
            const res = await fetch('/api/system/v1/addons/install', {method: 'POST', headers: REQUEST_HEADER, body: fd});
            const data = (await res.json()) as InstallJob & {error?: string; message?: string};
            if (!res.ok) throw new Error(data.message ?? data.error ?? `HTTP ${res.status}`);
            file = null;
            await finishInstall(await followJob(data.id));
        } catch (err) {
            notice = (err as Error).message;
        } finally {
            installing = false;
            await load();
            addonsChanged(); // openccu-lite B-297
        }
        await askAfterInstall();
    }
    // B-98 (D-67): an addon the failed or reboot-asking install left stopped - ask to start it
    async function askAfterInstall() {
        const done = result as InstallResult | null; // set by finishInstall
        if (done?.stopped_addons?.length && (await askStartStopped(done)).length) await load();
    }
    // an upload install still running when the page opens (a reload during it) is followed again
    async function resumeInstall() {
        let job: InstallJob | undefined;
        try {
            // occulited B-15: 204 (no body) when there has been none since the system service started
            job = await api.get<InstallJob | undefined>('/api/system/v1/addons/install');
        } catch {
            return;
        }
        if (!job || job.state !== 'running' || installing) return;
        installing = true;
        try {
            await finishInstall(await followJob(job.id));
        } catch (err) {
            notice = (err as Error).message;
        } finally {
            installing = false;
            await load();
            addonsChanged(); // openccu-lite B-297
        }
        await askAfterInstall();
    }
    async function reboot() {
        if (!(await ask(t('Reboot now?')))) return;
        await api.post('/api/system/v1/reboot', {confirm: true});
        notice = t('Rebooting…');
    }

    // the Info line is the addon's own HTML: tags dropped, entities decoded, what looks like a link linked (30.3)
    const plain = (html: string) => (new DOMParser().parseFromString(html.replace(/<br\s*\/?>|<\/(p|div|li|h[1-6])>/gi, ' '), 'text/html').body.textContent ?? '').replace(/\s+/g, ' ').trim();
    type Seg = {text: string; href?: string};
    function linkify(text: string): Seg[] {
        const out: Seg[] = [];
        const re = /(https?:\/\/[^\s<>"')]+|\bgithub\.com\/[A-Za-z0-9_.\-\/]+)/g;
        let last = 0;
        for (const m of text.matchAll(re)) {
            const i = m.index ?? 0;
            if (i > last) out.push({text: text.slice(last, i)});
            const raw = m[0].replace(/[.,;:]+$/, '');
            out.push({text: raw, href: raw.startsWith('http') ? raw : `https://${raw}`});
            last = i + raw.length;
        }
        if (last < text.length) out.push({text: text.slice(last)});
        return out;
    }
    // the ⋯ menu, one open at a time, closed by Escape and a click elsewhere
    let openMenu = $state('');
    $effect(() => {
        const onKey = (ev: KeyboardEvent) => { if (ev.key === 'Escape') openMenu = ''; };
        const onDown = (ev: MouseEvent) => {
            const el = (ev.target as HTMLElement | null)?.closest?.('.ol-menu[data-addon]');
            if (!el) openMenu = '';
        };
        document.addEventListener('keydown', onKey);
        document.addEventListener('mousedown', onDown);
        return () => {
            document.removeEventListener('keydown', onKey);
            document.removeEventListener('mousedown', onDown);
        };
    });
    const hasMenu = (a: Addon) => admin;
</script>

<!-- task 51: a badge whose explanation opens as a popup; without one it is the plain badge -->
{#snippet badge(cls: string, text: string, why: string)}
    {#if why}
        <Help class={cls}>{#snippet trigger()}{text}{/snippet}{why}</Help>
    {:else}
        <span class={cls}>{text}</span>
    {/if}
{/snippet}

<h1>{t('Addons')}<Help>{t('Every addon the catalogue knows and every one installed on this system, on one page. Installed with one click from their GitHub releases through the same installer a manual upload uses; the catalogue is fetched on demand only, and this system makes no other outbound call for it.')}</Help></h1>
<TokenNotice />
{#if rebootRequired}
    <div class="ol-notice error">{t('An addon asked for a reboot to finish its installation.')} <button class="hmm-button" onclick={reboot}>{t('Reboot now')}</button></div>
{/if}
{#if notice}<div class="ol-notice">{notice}</div>{/if}
{#if result}
    <div class="ol-notice" class:error={result.exit !== 0 && result.exit !== 10}>
        <strong>{t('Install result')}:</strong> {result.meaning} <span class="ol-muted">(exit {result.exit}, {result.seconds.toFixed(1)} s)</span>
        {#if result.output}<pre class="ol-log" style="margin:6px 0 0">{result.output}</pre>{/if}
    </div>
{/if}
<!-- one bar for a catalogue run, wherever it was started -->
<InstallProgress bind:this={progressView} bind:busy={installBusy} onfinished={() => { void load(); addonsChanged(); }} />
{#if !addons || !catalogue}
    <Loading {error} />
{:else}
    {#if toReinstall.length > 0}
        <!-- task 146 (D-106): the addons a restore brought back without their program files -->
        <!-- occulited task 12: red, as the maintainer asked - these addons do not run -->
        <section class="ol-panel err ad-reinstall" id="reinstall" data-section="reinstall" data-warn-for="addon-payload" data-warn-edge="err" aria-labelledby="reinstall-title">
            <h2 id="reinstall-title">{t('Addons to reinstall after the restore')}<Help>{t("A backup keeps an addon's settings and data but not its program files (the directories marked .nobackup), as on a CCU. After a restore these addons are back without them: the system does not start them, and nothing reinstalls them by itself. Reinstalling from the catalogue puts the program back; the settings stay.")}</Help></h2>
            {#if !catalogue.checked}
                <p class="ol-muted">{t('Check for updates first, so the catalogue knows which of them it can reinstall.')}</p>
            {/if}
            <ul class="ad-reinstall-list">
                {#each toReinstall as a (a.id)}
                    {@const e = reinstallEntry(a)}
                    <li data-reinstall={a.id}>
                        <span class="ad-reinstall-name"><strong>{a.name || a.id}</strong>{#if a.version}<span class="ol-muted"> {a.version}</span>{/if}</span>
                        <!-- B-267: what happened and what to do, then what is missing -->
                        <span class="ad-reinstall-what" data-reinstall-note>{t('Installed before the restore; reinstall it.')}</span>
                        {#if a.payload_missing_dirs?.length}<span class="ol-muted ad-reinstall-dirs">{t('missing: {dirs}', {dirs: a.payload_missing_dirs.join(', ')})}</span>{/if}
                        {#if e}
                            {#if admin}<button class="hmm-button primary" onclick={() => reinstall(a)} disabled={installBusy || busy !== ''} data-action="reinstall">{t('Reinstall {version}', {version: e.latest?.version ?? ''})}</button>{/if}
                        {:else}
                            <span class="ad-reinstall-hand">{catalogue.checked ? t('not in the catalogue — install it by hand') : t('not known yet')}</span>
                        {/if}
                        {#if admin}<button class="hmm-button" onclick={() => dismissReinstall(a)} disabled={busy !== ''} data-action="reinstall-dismiss">{t('Dismiss')}</button>{/if}
                    </li>
                {/each}
            </ul>
            {#if admin && reinstallable.length > 1}
                <div class="ol-actions"><button class="hmm-button primary" onclick={reinstallAll} disabled={installBusy || busy !== ''} data-action="reinstall-all">{t('Reinstall all')} ({reinstallable.length})</button></div>
            {/if}
        </section>
    {/if}
    <div class="ol-toolbar ad-toolbar" data-addons-toolbar>
        <input class="hmm-input ad-filter" placeholder={t('Filter')} bind:value={filter} aria-label={t('Filter')} />
        <label class="ad-switch"><input type="checkbox" bind:checked={installedOnly} /> {t('Installed only')}</label>
        {#if daily !== null}
            <CheckDaily label={t('Check for updates')} busyLabel={t('Checking…')} busy={refreshing} disabled={installBusy} onCheck={checkUpdates} daily={daily} onDaily={setDaily} dailyDisabled={!admin} host={t('GitHub (the catalogue and the addons\' releases) and each addon\'s own update check')} name="catalog" />
        {:else}
            <button class="hmm-button" onclick={checkUpdates} disabled={refreshing || installBusy} data-check-updates>{refreshing ? t('Checking…') : t('Check for updates')}</button>
        {/if}
        {#if admin && pending.length > 1}
            <button class="hmm-button primary" onclick={updateAll} disabled={installBusy} data-update-all>{t('Update all')} ({pending.length})</button>
        {/if}
        <span class="ol-muted ad-meta" data-catalog-meta>
            {arch}
            {#if catalogue.check_error}
                · <span class="ol-warn" data-check-failed title={catalogue.check_error.message}>{t('check failed {when}', {when: when(catalogue.check_error.at)})}</span>{#if catalogue.checked}{' · '}{t('last checked {when}', {when: when(catalogue.checked)})}{/if}
            {:else if catalogue.checked}
                · {t('checked {when}', {when: when(catalogue.checked)})}
            {:else if catalogue.source === 'bundled' && catalogue.bundled_date}
                · {t('built-in list of {date}', {date: day(catalogue.bundled_date)})}
            {:else}
                · {t('not checked yet')}
            {/if}
        </span>
    </div>
    {#if firstVisit || failure}
        <!-- occulited B-52: the catalogue comes from GitHub, and a first visit is told so with a
             button that loads it; a failed load or check says why and offers it again -->
        <div class="ol-notice ad-catalog-load" class:ad-failed={!!failure} data-catalog-load={failure ? 'failed' : 'first'} data-warn-for="catalog-check" role={failure ? 'alert' : undefined}>
            {#if firstVisit}
                <p class="ad-catalog-what">
                    {t('The addon catalogue is loaded from GitHub.')}
                    {#if catalogue.source === 'bundled' && catalogue.bundled_date}
                        {t('Until then this page shows the built-in list of {date}: most addons only by their repository, without version and description, until the catalogue is loaded.', {date: day(catalogue.bundled_date)})}
                    {/if}
                </p>
            {/if}
            {#if failure}
                <p class="ad-catalog-error" data-catalog-error>
                    {catalogError && !checkError && !catalogue.check_error ? t('The catalogue could not be read from the system: {error}', {error: failure}) : t('Loading the catalogue failed: {error}', {error: failure})}
                    {#if catalogue.check_error && catalogue.check_error.failures > 1}
                        <span class="ol-muted">{t('({n} checks in a row since {since})', {n: catalogue.check_error.failures, since: when(catalogue.check_error.since)})}</span>
                    {/if}
                </p>
            {/if}
            <button class="hmm-button primary ad-load-now" onclick={checkUpdates} disabled={refreshing || installBusy} aria-busy={refreshing} data-catalog-load-now>
                {#if refreshing}<span class="ad-spinner" aria-hidden="true"></span>{t('Loading the catalogue…')}{:else if failure}{t('Try again')}{:else}{t('Load the catalogue now')}{/if}
            </button>
        </div>
    {/if}
    {#if catalogue.releases_error}
        {@const re = catalogue.releases_error}
        <div class="ol-notice ol-warn" data-releases-error={re.code}>
            {t('The last check could not read the releases of {repo}: the versions and updates shown are from before it.', {repo: re.repo})}
            {#if releasesProblemKind(re.code, re.retry_minutes)}<ReleasesProblem code={re.code} minutes={re.retry_minutes} />{:else}{re.message}{/if}
        </div>
    {/if}
    <!-- B-52: one card that cannot render must not blank the whole list without a word -->
    <svelte:boundary onerror={(e) => console.error('addons: the list could not be rendered', e)}>
    {#if shown.length === 0}
        <div class="ol-notice">{installedOnly && !filter ? t('No addons installed. Install a frontend such as homematic-manager to pair devices.') : t('Nothing matches the filter.')}</div>
    {/if}
    <div class="ol-cards ad-cards" data-addon-cards={shown.length}>
        {#each shown as c (c.id)}
            {@const a = c.addon}
            {@const e = c.entry}
            {@const repo = e ? repoURL({id: e.id, name: c.name, git: e.git, release: e.release}) : ''}
            {@const fe = a ? frontend(a) : undefined}
            {@const u = a ? updates[a.id] : undefined}
            <!-- the id is what the Addons popup's ⚙ scrolls to (/addons?addon=<id>, task 55) -->
            {@const edge = cardEdge(a)}
            <div class="ol-card ad-card {edge}" class:dim={!!e && !e.id && !a} id={`addon-row-${c.id}`} data-addon-card={c.id} data-installed={a ? '1' : '0'} data-warn-for={a ? ADDON_WARNINGS.join(' ') : undefined} data-warn-edge={edge || undefined}>
                <div class="ad-head">
                    <span class="ad-logo">
                        <AddonImage candidates={logo(c)}>
                            {#snippet fallback()}<span class="ad-letter" aria-hidden="true">{letter(c)}</span>{/snippet}
                        </AddonImage>
                    </span>
                    <div class="ad-titles">
                        <h3 class="ad-name">{#if a}<span class="ol-dot {dotOf(a)}" role="img" aria-label={stateWord(a)} title={stateWord(a)} data-addon-dot={statusKind(unitOf(a) ?? {running: a.running, oneshot: a.oneshot, result: a.result, ended: a.ended})}></span>{/if}{c.name}</h3>
                        <div class="ad-sub ol-muted">
                            {#if a?.payload_missing}
                                <!-- occulited task 12: a restore brought it back without its program files (task 146) -->
                                <span class="ad-needs-reinstall" data-addon-reinstall>{t('Needs reinstalling')}</span>
                                {#if !a.reinstall_dismissed}{' · '}<a href="#reinstall" data-addon-reinstall-link>{t('Reinstall')}</a>{/if}
                            {:else if a}
                                <span class="ad-version">{a.version}</span>
                                {#if c.update}<span class="ol-upd"> → {c.update}</span>{/if}
                                <span class="ad-state" data-addon-state>· {stateWord(a)}</span>
                                {#if a.stray}{' · '}<Help class="ol-warn">{#snippet trigger()}{t('outside its unit')}{/snippet}{t('Runs outside its unit (started by an installer or by hand). Restart puts it back into the unit.')}</Help>{/if}
                            {:else if e?.latest}
                                <span class="ad-version" title={t('Latest release')}>{e.latest.version}</span> <span>· {t('Not installed')}</span>
                            {:else if e && !e.id}
                                <span>{t('Not checked yet')}</span>
                            {:else}
                                <span>{t('Not installed')}</span>
                            {/if}
                        </div>
                    </div>
                    {#if repo}
                        {#if e?.stars}
                            <a class="cat-stars" href={repo} target="_blank" rel="noopener" aria-label={t('{n} GitHub stars — open the repository', {n: e.stars})}><span aria-hidden="true">★</span> {formatStars(e.stars)}</a>
                        {:else}
                            <a class="cat-stars" href={repo} target="_blank" rel="noopener">{t('Repository')} <span aria-hidden="true">↗</span></a>
                        {/if}
                    {/if}
                </div>
                <div class="ad-badges">
                    {#if e?.untested}{@render badge('ol-badge warn ad-untested', t('untested'), t('Not tested on openccu-lite yet: install it at your own risk. Every addon declares what it needs itself; the label neither grants nor refuses anything.'))}{/if}
                    {#if e?.release?.prerelease}<span class="ol-badge">{t('prerelease')}</span>{/if}
                    {#if e?.ui?.own_updater}{@render badge('ol-badge warn', t('own updater'), t('This addon still carries an update mechanism of its own. On openccu-lite the system installs its updates; what the addon\'s own updater does bypasses that.'))}{/if}
                    {#if a}
                        {#if a.rega_dependent}
                            {@render badge('ol-badge bad', a.enabled ? t('incompatible, still enabled') : t('disabled, incompatible'), a.rega_reason ?? '')}
                        {:else if a.binary_incompatible}
                            {@render badge('ol-badge bad', a.enabled ? t('needs an update for this architecture') : t('disabled, needs update'), a.binary_reason ?? '')}
                        {/if}
                        <!-- occulited task 23: the addon's lighttpd fragment failed the check, so its
                             frontend is not served; the verdict stays until an install brings one that passes -->
                        {#if a.lighttpd_rejected}
                            {@render badge('ol-badge bad ad-lighttpd-rejected', t('web configuration refused'), rejectedWhy(a.lighttpd_rejected))}
                        {/if}
                        {#if a.policy_mode === 'root'}
                            {@render badge('ol-badge warn', t('root (unsafe)'), t('This addon runs as root: it can change anything on the system. The Services page switches it to its own user.'))}
                        {:else if a.policy_mode === 'confined'}
                            {@render badge('ol-badge ok ad-confined', t('confined'), t('This addon runs as its own user and cannot write outside its own directories.'))}
                        {/if}
                        {#if a.undeclared}
                            {@render badge('ol-badge', t('undeclared'), t('This addon declared no compatibility: its manifest carries no runtime block, so nobody has said what it needs.'))}
                        {/if}
                        <!-- D-66: no root addon may remount the system partition; the two marks say
                             who kept the right (the entry declares CAP_SYS_ADMIN) and whose remount
                             was refused in this boot (a hint: the addon still works) -->
                        {#if a.may_mount}
                            {@render badge('ol-badge warn', t('may mount file systems'), t('This addon runs as root and its manifest declares CAP_SYS_ADMIN, so it keeps the right to mount file systems and to remount the system partition, which every other root addon has lost.'))}
                        {/if}
                        {#if a.remount_refused}
                            {@render badge('ol-badge', t('remount refused'), t('This addon tried to remount the system partition read-write, which openccu-lite does not allow. It still works: what it writes into the device descriptions lands in the writable extension directory instead.'))}
                        {/if}
                        {#if a.api_scopes?.length}
                            {@render badge('ol-badge ad-scopes', t('API token: {scopes}', {scopes: a.api_scopes.join(', ')}), t('The addon\'s manifest declares these scopes; the system mints a token with exactly them at every start and hands it to the addon alone. Every other addon reads names and rooms with the local token and nothing else.'))}
                        {/if}
                        {#if a.legacy_session}
                            {@render badge('ol-badge warn ad-legacy', t('session in URL'), t('This addon receives your session in its URL (?sid=, the legacy CCU convention) because it does not read the session header. What goes there is an alias that only addon pages accept, never the API, and it ends with your session. The ⋯ menu switches it off for this addon; its pages then refuse to open until the addon reads the header.'))}
                        {:else if legacyCandidate(a) && legacyOffFor(a)}
                            {@render badge('ol-badge ad-legacy-off', t('no session in URL'), t('The legacy session is switched off for this addon: its pages get no session in their URL and refuse to open unless the addon reads the session header. The ⋯ menu switches it on again; the switch for all addons is under Addon sessions, below the list.'))}
                        {/if}
                        {#if a.start_early_declared}
                            {#if a.start_early}
                                {@render badge('ol-badge ok ad-early', t('starts early'), t('This addon copes with radio interfaces that are not ready yet, so the system starts it before them and it connects when they are. The ⋯ menu switches that off for this addon; the switch for all addons is under Addon start, below the list. A change takes effect at the next boot.'))}
                            {:else}
                                {@render badge('ol-badge ad-early-off', t('early start off'), t('This addon could start before the radio interfaces, but the early start is switched off for it, so it waits for them. The ⋯ menu switches it on again; the switch for all addons is under Addon start, below the list. A change takes effect at the next boot.'))}
                            {/if}
                        {/if}
                    {/if}
                </div>
                {#if e?.id}
                    {#if tx(e.description)}<p class="ad-desc">{tx(e.description)}</p>{/if}
                    {#if e.runtime?.note}<p class="ol-muted ad-note">{tx(e.runtime.note)}</p>{/if}
                {:else if e}
                    <p class="ol-muted ad-desc">{t('Its description arrives with the next check for updates, which reads the addon\'s manifest from its repository.')}</p>
                {/if}
                {#if e?.error}<p class="ol-muted ad-check ol-warn">{t('The last check could not read its manifest: {error}', {error: e.error})}</p>{/if}
                {#if a?.from_ccu && (a.failed || !a.enabled)}
                    <p class="ol-notice ad-from-ccu" data-from-ccu>
                        {t('Came from the CCU')}{#if a.failed}{' · '}{t('failed')}{#if a.failed_log}: <span class="hmm-mono">{a.failed_log}</span>{/if}{/if}
                        {#if admin}<button class="hmm-button" disabled={busy !== ''} onclick={() => removeRCEntry(a)} data-remove-rc>{t('Remove the rc.d entry')}</button>{/if}
                    </p>
                {/if}
                {#if a?.info}
                    <p class="ol-muted ad-info">{#each linkify(plain(a.info)) as seg, i (i)}{#if seg.href}<a href={seg.href} target="_blank" rel="noopener">{seg.text}</a>{:else}{seg.text}{/if}{/each}</p>
                {/if}
                {#if u}
                    {#if u.error}<p class="ol-muted ad-check">{u.error}</p>
                    {:else if u.update_available}<p class="ad-check"><span class="ol-upd">→ {u.available}</span>{#if httpURL(u.url)} <a href={u.url} target="_blank" rel="noopener">{t('Download')}</a>{/if}</p>
                    {:else}<p class="ol-muted ad-check">{t('up to date')}</p>{/if}
                {/if}
                <div class="ol-muted ad-meta-line">
                    {#if e}{repoName(e)}{e.licence ? ` · ${e.licence}` : ''}{/if}
                    {#if e && archs(e).length && !supportsArch(e)}{' · '}<span class="ol-warn">{t('Not available for {arch}', {arch})}: {archs(e).join(', ')}</span>{/if}
                    {#if e?.homepage && httpURL(e.homepage)}{' · '}<a href={e.homepage} target="_blank" rel="noopener">{t('Homepage')}</a>{/if}
                    {#if a && fe}{' · '}<span class="ad-pinhint">{t('Pin it to the menu in the Addons menu')}</span>{/if}
                </div>
                <div class="ol-card-foot ad-foot">
                    <div class="ad-actions">
                        {#if a}
                            {#if fe}<a class="hmm-button" href={`/nav/${encodeURIComponent(fe.id)}`} use:link data-addon-open>{t('Open frontend')}</a>{/if}
                            {#if a.config_url || a.settings?.config_url}<a class="hmm-button" href={`/addon-settings/${encodeURIComponent(a.id)}`} use:link>{t('Settings')}</a>{/if}
                            {#if c.update && admin}
                                <button class="hmm-button primary" disabled={installBusy} onclick={() => install(c)} data-addon-update>{t('Update to {version}', {version: c.update})}</button>
                                {#if e?.release_notes && httpURL(e.release_notes)}<a class="hmm-button" href={e.release_notes} target="_blank" rel="noopener" data-addon-notes>{t('Release notes')}</a>{/if}
                            {:else if a.update && !catKnows(a)}
                                <button class="hmm-button" disabled={checking !== ''} onclick={() => checkUpdate(a)}>{t('Check for update')}</button>
                            {/if}
                            {#if a.stray && admin}<button class="hmm-button" disabled={busy !== ''} onclick={() => unitAction(a, 'restart')}>{t('Restart')}</button>{/if}
                        {:else if e && !supportsArch(e)}
                            <span class="ol-muted">{t('Not available for {arch}', {arch})}</span>
                        {:else if e?.id && e.release && admin}
                            <button class="hmm-button primary" disabled={installBusy} onclick={() => install(c)} data-addon-install>{t('Install')}</button>
                        {/if}
                    </div>
                    {#if a}
                        <div class="ad-links">
                            <a href={`/system/services#service-addon-${encodeURIComponent(a.id)}`} use:link data-addon-service>{t('Service')}</a>
                            <a href={`/system/log?unit=${encodeURIComponent(`addon-${a.id}`)}`} use:link data-addon-log>{t('Log')}</a>
                            {#if ports[a.id]}<a href="/addons#addon-ports" use:link data-addon-firewall>{t('Firewall')}</a>{/if}
                        </div>
                        {#if hasMenu(a)}
                            <div class="ol-menu ad-menu" data-addon={a.id}>
                                <button type="button" class="hmm-button ol-morebtn" aria-haspopup="menu" aria-expanded={openMenu === a.id} aria-label={t('More actions')} title={t('More actions')} onclick={() => (openMenu = openMenu === a.id ? '' : a.id)}>⋯</button>
                                {#if openMenu === a.id}
                                    <div class="ol-menupop" role="menu">
                                        {#if a.running && !a.oneshot}
                                            <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => { openMenu = ''; void unitAction(a, 'restart'); }}>{t('Restart')}</button>
                                            <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => { openMenu = ''; void unitAction(a, 'stop'); }}>{t('Stop')}</button>
                                        {:else}
                                            <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => { openMenu = ''; void unitAction(a, 'start'); }}>{t('Start')}</button>
                                        {/if}
                                        {#if a.enabled}
                                            <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => { openMenu = ''; void setEnabled(a, false); }}>{t('Disable at boot')}</button>
                                        {:else}
                                            <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => { openMenu = ''; void setEnabled(a, true); }}>{t('Enable at boot')}</button>
                                        {/if}
                                        {#if e?.id && e.release}
                                            <button type="button" role="menuitem" class="ol-menuitem" disabled={installBusy} onclick={() => { openMenu = ''; void install(c); }}>{t('Reinstall from the catalogue')}</button>
                                        {/if}
                                        {#if legacy && legacyCandidate(a)}
                                            <button type="button" role="menuitemcheckbox" class="ol-menuitem ad-legacy-item" aria-checked={!legacyOffFor(a)} disabled={busy !== '' || !legacy.enabled} onclick={() => { openMenu = ''; void setLegacy(a, legacyOffFor(a)); }}>
                                                <span class="ad-check" aria-hidden="true">{legacyOffFor(a) ? '' : '✓'}</span>{t('Pass the session in the URL')}{#if !legacy.enabled}{' '}<span class="ol-muted">{t('(off for all addons, see Addon sessions below)')}</span>{/if}
                                            </button>
                                        {/if}
                                        {#if early && a.start_early_declared}
                                            <button type="button" role="menuitemcheckbox" class="ol-menuitem ad-early-item" aria-checked={!earlyOffFor(a)} disabled={busy !== '' || !early.enabled} onclick={() => { openMenu = ''; void setEarly(a, earlyOffFor(a)); }}>
                                                <span class="ad-check" aria-hidden="true">{earlyOffFor(a) ? '' : '✓'}</span>{t('Start early')}{' '}<span class="ol-muted">{early.enabled ? t('(from the next boot)') : t('(off for all addons, see Addon start below)')}</span>
                                            </button>
                                        {/if}
                                        {#if a.operations.includes('uninstall')}
                                            <button type="button" role="menuitem" class="ol-menuitem ad-danger" onclick={() => { openMenu = ''; void uninstall(a); }}>{t('Uninstall')}</button>
                                        {/if}
                                    </div>
                                {/if}
                            </div>
                        {/if}
                    {/if}
                </div>
            </div>
        {/each}
    </div>
        {#snippet failed(err, reset)}
            <div class="ol-notice error" data-cards-error>
                {t('The list of addons could not be shown: {error}', {error: (err as Error)?.message ?? String(err)})}
                <button class="hmm-button" onclick={reset}>{t('Try again')}</button>
            </div>
        {/snippet}
    </svelte:boundary>
    <!-- task 157: the addons' declared ports and their switches -->
    <AddonPorts {admin} />
    {#if admin}
        <AddonSessionSettings bind:view={legacy} onsaved={() => void load()} />
        <AddonStartSettings bind:view={early} onsaved={() => void load()} />
        <h2 id="install-file">{t('Install addon')}<Help>{t('A .tar.gz with an executable update_script at its top level — the CCU addon format. It runs as root; install only what you trust.')}</Help></h2>
        <form class="ol-toolbar" onsubmit={installFile}>
            <input type="file" accept=".tar.gz,.tgz,application/gzip" onchange={(e) => (file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
            <button class="hmm-button" type="submit" disabled={!file || installing}>{installing ? t('Installing…') : t('Install')}</button>
            {#if installing && installSeconds > 0}<span class="ol-muted" role="status">{installSeconds} s</span>{/if}
        </form>
    {/if}
{/if}

<style>
    /* the maintainer: bigger panels, good structure. Two columns on a wide screen, one below
       about 1100 px (Q3); the card is task 56's look with the state and the actions added */
    .ad-toolbar { align-items: center; flex-wrap: wrap; gap: 10px; }
    /* task 146: the addons to reinstall after a restore, above the toolbar */
    .ad-reinstall { margin-bottom: 14px; }
    .ad-needs-reinstall { color: var(--hmm-error); font-weight: 600; }
    .ad-reinstall h2 { margin: 0 0 6px; font-size: 1.05em; }
    .ad-reinstall-list { list-style: none; margin: 8px 0 0; padding: 0; display: grid; gap: 8px; }
    .ad-reinstall-list li { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
    .ad-reinstall-name { min-width: 12em; }
    .ad-reinstall-dirs { flex: 1 1 12em; font-size: 0.9em; }
    .ad-reinstall-what { flex: 1 1 14em; }
    .ad-reinstall-hand { font-style: italic; }
    .ad-reinstall .ol-actions { margin-top: 10px; }
    .ad-filter { min-width: 12em; max-width: 100%; }
    .ad-switch { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    .ad-meta { margin-left: auto; font-size: var(--hmm-font-size-small); }
    /* B-52: the first visit's and a failed check's notice, its button with a spinner */
    .ad-catalog-load { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; }
    .ad-catalog-load p { margin: 0; flex: 1 1 22em; min-width: 0; overflow-wrap: anywhere; }
    .ad-catalog-load.ad-failed { border-color: var(--hmm-warn); border-left-width: 4px; }
    .ad-catalog-error { font-weight: 600; }
    .ad-load-now { display: inline-flex; align-items: center; gap: 8px; }
    .ad-spinner { width: 1em; height: 1em; border: 2px solid currentColor; border-right-color: transparent; border-radius: 50%; animation: ad-spin 0.8s linear infinite; }
    @keyframes ad-spin { to { transform: rotate(360deg); } }
    @media (prefers-reduced-motion: reduce) { .ad-spinner { animation-duration: 2.4s; } }
    .ad-cards { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
    @media (max-width: 1100px) { .ad-cards { grid-template-columns: minmax(0, 1fr); } }
    .ad-card { padding: 16px 18px; }
    .dim { opacity: 0.7; }
    .ad-head { display: flex; align-items: center; gap: 14px; min-width: 0; }
    /* the logos are wide wordmarks (width="240", height="48"): a strip, fitted, never stretched */
    .ad-logo { flex: 0 0 auto; display: flex; align-items: center; height: 56px; max-width: 140px; }
    /* occulited B-43: a definite height, the width following by the ratio - an SVG with a viewBox
       and no width/height (OpenCCU-Loom's icon) has no size of its own and came out 0×0 with both
       auto; a wordmark wider than 140 px is contained in the strip */
    .ad-logo :global(img) { display: block; height: 56px; width: auto; max-width: 140px; object-fit: contain; }
    .ad-letter {
        display: flex; align-items: center; justify-content: center; width: 48px; height: 48px;
        border-radius: var(--hmm-radius-card); background: var(--hmm-accent-bg); color: var(--hmm-accent);
        font-size: 22px; font-weight: 600;
    }
    .ad-titles { min-width: 0; flex: 1 1 auto; }
    .ad-name { margin: 0; font-size: 17px; font-weight: 600; line-height: 1.25; color: var(--hmm-fg); overflow-wrap: anywhere; display: flex; align-items: center; gap: 8px; }
    .ad-name .ol-dot { margin: 0; width: 10px; height: 10px; flex: 0 0 auto; }
    .ad-sub { margin-top: 3px; font-size: var(--hmm-font-size-small); display: flex; flex-wrap: wrap; gap: 0 4px; align-items: baseline; }
    .ol-upd { color: var(--hmm-warn); }
    .cat-stars {
        margin-left: auto; align-self: flex-start; white-space: nowrap; font-size: var(--hmm-font-size-small); line-height: 16px;
        padding: 1px 7px; border-radius: 9px; color: var(--hmm-fg-muted); text-decoration: none;
    }
    .cat-stars:hover { color: var(--hmm-link); background: var(--hmm-control-bg-hover); }
    .ad-badges { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 10px; }
    .ad-badges:empty { display: none; }
    .ad-desc { margin: 10px 0 4px; }
    .ad-note { margin: 0 0 4px; }
    .ad-info { margin: 4px 0; font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    .ad-info a { color: var(--hmm-link, var(--hmm-accent)); }
    .ad-check { margin: 4px 0; font-size: var(--hmm-font-size-small); }
    .ad-meta-line { font-size: var(--hmm-font-size-small); margin: 6px 0 12px; overflow-wrap: anywhere; }
    .ad-pinhint { font-style: italic; }
    .ad-foot { border-top: 1px solid var(--hmm-border-muted); display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; }
    .ad-actions { display: flex; flex-wrap: wrap; gap: 8px; }
    .ad-actions .hmm-button { min-height: 30px; text-decoration: none; }
    .ad-links { display: flex; gap: 12px; margin-left: auto; font-size: var(--hmm-font-size-small); }
    .ad-menu { display: block; }
    .ad-menu .ol-menupop { left: auto; right: 0; }
    .ol-morebtn { min-width: 30px; min-height: 30px; padding: 1px 8px; }
    .ad-menu .ol-menuitem:disabled { opacity: 0.45; cursor: default; }
    .ad-legacy-item, .ad-early-item { display: flex; gap: 6px; align-items: baseline; white-space: nowrap; }
    .ad-legacy-item .ad-check, .ad-early-item .ad-check { display: inline-block; width: 1em; margin: 0; }
    .ad-danger { color: var(--hmm-error); }
    @media (max-width: 700px) {
        /* a phone: the actions on their row, the links and the ⋯ on one row below */
        .ad-actions { width: 100%; }
        .ad-links { margin-left: 0; flex: 1 1 auto; }
        .ad-menu { margin-left: auto; }
        .ad-actions .hmm-button { white-space: normal; }
    }
</style>
