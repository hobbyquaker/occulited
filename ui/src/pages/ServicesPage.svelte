<script lang="ts">
    import {timeOnly} from '../lib/datefmt';
    import {probeHealth} from '../lib/bootwatch';
    import {onMount, untrack} from 'svelte';
    import {scrollToAnchor} from '../lib/anchor';
    /** task 139: the unit's row an addon card's link named (#service-<id>) */
    let anchored = $state('');
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type LocalTimer, type UnitOverride, type Service, type Timer} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {i18n, t} from '../lib/i18n.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import {link, replace, router} from '../lib/router.svelte';
    import Loading from '../lib/Loading.svelte';
    import TimerEditor from '../lib/TimerEditor.svelte';
    import ModalDialog from '../lib/ModalDialog.svelte';
    import Help from '../lib/Help.svelte';
    import Icon, {type IconName} from '../lib/Icon.svelte';
    import {ENABLED_RANK, STATUS_RANK, enabledKind, span, statusDot, statusKind, switchable, unknownState, type EnabledKind} from '../lib/units';
    import {runTime, runTimeFull, timeLeft} from '../lib/timers';
    import {startingSeconds} from '../lib/starting';
    import BootTimeline from '../lib/BootTimeline.svelte';
    import {holdColumns} from '../lib/holdcolumns';
    import {imageCandidates} from '../lib/addonimages';
    import AddonImage from '../lib/AddonImage.svelte';
    import {isDark} from '../lib/theme.svelte';
    import {warnEdge} from '../lib/systemmenu.svelte';
    import {worse, type Edge} from '../lib/warnedge';
    // occulited task 12: a unit the Status page's crash-loop warning names, and an addon its
    // ownership warning names, carry the warning's edge on their row
    const rowEdge = (id: string): Edge => worse(warnEdge(['crash-loop'], id), id.startsWith('addon-') ? warnEdge(['addon-ownership'], id.slice('addon-'.length)) : '');

    type Action = 'start' | 'stop' | 'restart' | 'enable' | 'disable';
    let services = $state<Service[] | null>(null);
    let timers = $state<Timer[]>([]);
    let systemd = $state(false);
    let error = $state('');
    let busy = $state('');
    let output = $state('');

    // task 94: when the last listing arrived (the browser's clock), and a clock that ticks every second
    // while a unit is starting, so its seconds count on between two polls
    let servicesAt = $state(0);
    let now = $state(Date.now());
    $effect(() => {
        if (!(services ?? []).some((s) => s.starting)) return;
        now = Date.now();
        const id = setInterval(() => (now = Date.now()), 1000);
        return () => clearInterval(id);
    });

    // B-83: the box says how long to wait between two polls - eight times what its listing costs,
    // 5 s at the least - so a slow box is not kept busy listing units for a page
    let pollSeconds = 5;
    async function load() {
        try {
            const r = await api.get<{services: Service[]; systemd?: boolean; poll_seconds?: number}>('/api/system/v1/services');
            services = r.services;
            servicesAt = Date.now();
            systemd = r.systemd ?? false;
            pollSeconds = Math.max(5, r.poll_seconds ?? 5);
            error = '';
            if (systemd) timers = (await api.get<{timers: Timer[]}>('/api/system/v1/timers')).timers ?? [];
            // task 139: a link from an addon's card names the unit's row, which is marked and scrolled to
            if (location.hash.startsWith('#service-')) {
                anchored = location.hash.slice(1);
                scrollToAnchor(anchored);
            }
        } catch (e) {
            error = (e as Error).message;
        }
    }
    // B-60: the poll costs the box systemctl runs; a tab nobody looks at does not poll. B-83: the next
    // poll is timed from the end of the last one, at the interval the box asked for, so a slow
    // answer never overlaps the next request.
    const life = pageLife();
    onMount(() => {
        let id: ReturnType<typeof setTimeout> | undefined;
        let mounted = true;
        // one chain of polls per generation: a stop starts a new generation, so a load that was in
        // flight when the tab was hidden (and shown again) ends its chain instead of starting a second
        let generation = 0;
        const schedule = (g: number) => {
            // task 177: a kept page polls only while it shows
            if (!mounted || document.hidden || !life.active || g !== generation) return;
            id = setTimeout(async () => {
                id = undefined;
                await load();
                schedule(g);
            }, pollSeconds * 1000);
        };
        const stop = () => {
            generation++;
            if (id !== undefined) clearTimeout(id);
            id = undefined;
        };
        const begin = () => {
            const g = generation;
            void load().then(() => schedule(g));
        };
        const visibility = () => {
            stop();
            if (!document.hidden) begin();
        };
        begin();
        // task 57: arrived with ?edit=<unit>: that unit's editor, as it was left
        const linkedEdit = new URLSearchParams(router.search).get('edit');
        if (linkedEdit) void openEditor(linkedEdit);
        document.addEventListener('visibilitychange', visibility);
        // task 177: back from another page - a fresh load and the chain again
        const stopReturn = life.onReturn(visibility);
        return () => {
            stopReturn();
            mounted = false;
            stop();
            document.removeEventListener('visibilitychange', visibility);
        };
    });

    // D-36: confined is the default, root is the opt-out. The switch rewrites the addon's policy
    // and restarts it; root needs the explicit `unsafe` flag the API asks for, and the question
    // says what it means before it is sent.
    async function setMode(s: Service, mode: 'root' | 'confined') {
        const id = s.id.replace(/^addon-/, '');
        const question = mode === 'confined'
            ? t('Run {id} as its own user? It then cannot write outside its directories; an addon that needs more shows errors in the log and can be switched back.', {id})
            : t('Run {id} as root? It can then change anything on this system: the firmware, openccu-lite itself and every other addon. Only choose this when the addon does not work under its own user.', {id});
        if (!(await ask(question))) return;
        busy = s.id;
        try {
            const r = await api.put<{restarted: boolean; restart_error?: string}>(`/api/system/v1/addons/${encodeURIComponent(id)}/policy`, mode === 'root' ? {mode, unsafe: true} : {mode});
            output = r.restart_error ?? '';
        } catch (e) {
            output = (e as Error).message;
        } finally {
            busy = '';
            await load();
        }
    }
    async function control(s: Service, action: Action) {
        const question =
            action === 'stop'
                ? t('Stop {id}? Devices on this interface become unreachable until it runs again.', {id: s.id})
                : action === 'restart'
                  ? // task 48: on an addon outside its unit Restart is more than a restart - the
                    // box stops the unit, the addon's processes outside it by pid, and starts the
                    // unit - so the question says where it will run afterwards
                    s.stray
                      ? t('Restart {id}? It is stopped where it runs now and started again in its unit.', {id: s.id})
                      : t('Restart {id}?', {id: s.id})
                  : action === 'disable'
                    ? t('Disable {id}? It stops now and no longer starts at boot.', {id: s.id})
                    : '';
        // task 243: the web interface runs through it - the page goes away for a moment and comes back
        const ui = action === 'restart' && s.ui;
        const q = ui ? t('Restart {id}? The web interface runs through it: this page is gone for a few seconds and reconnects by itself.', {id: s.id}) : question;
        if (q && !(await ask(q))) return;
        busy = s.id;
        try {
            const r = await api.post<{output: string}>(`/api/system/v1/services/${encodeURIComponent(s.id)}/${action}`);
            output = r.output?.trim() ?? '';
        } catch (e) {
            // a restart of the unit that answered may end the answer with it
            output = ui ? '' : (e as Error).message;
        } finally {
            if (ui) await reconnect();
            busy = '';
            await load();
        }
    }
    // task 243: after a restart of lighttpd or occulited, wait until the system answers again (90 s at most)
    let reconnecting = $state(false);
    async function reconnect() {
        reconnecting = true;
        try {
            await new Promise((r) => setTimeout(r, 1000));
            const until = Date.now() + 90_000;
            while (Date.now() < until) {
                const h = await probeHealth(3000);
                if (h.status === 200) return;
                await new Promise((r) => setTimeout(r, 1000));
            }
        } finally {
            reconnecting = false;
        }
    }
    // CPU utilisation from the change in cpu_seconds between two polls
    let cpuPct = $state<Record<string, number>>({});
    let last: Record<string, {cpu: number; at: number}> = {};
    $effect(() => {
        if (!services) return;
        const now = Date.now();
        const next: Record<string, number> = {};
        for (const s of services) {
            if (s.cpu_seconds === undefined) continue;
            const prev = last[s.id];
            if (prev && now > prev.at && s.cpu_seconds >= prev.cpu) next[s.id] = ((s.cpu_seconds - prev.cpu) / ((now - prev.at) / 1000)) * 100;
            last[s.id] = {cpu: s.cpu_seconds, at: now};
        }
        cpuPct = next;
    });
    // why this addon is root: the user asked for it, it was already installed when the default
    // flipped (D-36), or confining it failed (B-28: /etc must be writable to create its user)
    function rootWhy(s: Service) {
        if (s.policy_source === 'migrated') return t('This addon was already installed when confinement became the default, so it was left as root. It can change anything on the system; the button next to it gives it its own user.');
        if (s.policy_source === 'fallback') return t('This addon could not be confined — its user could not be created — so it runs as root and can change anything on the system.');
        return t('This addon runs as root: it can change anything on the system.');
    }
    function mem(b?: number) {
        if (!b) return '';
        return b >= 1048576 ? `${(b / 1048576).toFixed(1)} MB` : `${Math.round(b / 1024)} kB`;
    }
    function uptime(since?: string) {
        return since ? span((Date.now() - new Date(since).getTime()) / 1000) : '';
    }
    // B-65: the Status cell's word; lib/units.ts decides the kind
    function statusText(s: Service): string {
        switch (statusKind(s)) {
            case 'running':
                return t('Running');
            case 'starting': {
                // task 94: with its seconds, counted on between two polls
                const secs = startingSeconds(s, servicesAt, now);
                return secs === undefined ? t('Starting') : t('Starting · {span}', {span: span(secs)});
            }
            case 'completed':
                return t('Completed');
            case 'ended':
                // B-158: the addon's daemon ended; when, from its last journal line
                return s.ended_at ? t('Exited · {time}', {time: timeOnly(s.ended_at)}) : t('Exited');
            case 'failed':
                return t('Failed');
            case 'skipped':
                return t('Skipped');
            default:
                return t('Stopped');
        }
    }
    // B-75: what the Kind and Description columns say, as one line under the id where the window has
    // no room for those two columns. Task 80: the protocol and port are not shown on this page any
    // more - the Interfaces page's process cards name them - so neither is here.
    function subline(s: Service): string {
        return [t(s.category ?? (s.kind === 'addon' ? 'addon' : 'system')), s.description ?? ''].filter(Boolean).join(' · ');
    }
    // B-68: how long until a timer's next run, in the page's own span format
    function whenLeft(tm: Timer): string {
        const s = timeLeft(tm.left, tm.next);
        return s === undefined ? '' : t('in {span}', {span: span(s)});
    }
    function cpu(s: Service) {
        const p = cpuPct[s.id];
        if (p === undefined) return s.cpu_seconds !== undefined && s.running ? '…' : '';
        return `${p < 10 ? p.toFixed(1) : Math.round(p)} %`;
    }
    // task 49: the pid cell. A oneshot unit - every generated addon unit - has no MainPID, so the
    // API names the leader of its cgroup and counts the rest (`2210 +5`). The popup lists every
    // process with its command line, the leader first. A stray addon's pids are outside its unit
    // (task 48) and the popup says so before the list, so a pid never points into the wrong cgroup
    // unannounced. Task 51: the list is the popup of the `+N` (of the pid itself when it is the only
    // process) - lib/Help.svelte in its badge form; a `title` needed a mouse and was out of reach on
    // a phone. It is data, not help, but it is the same kind of thing: more about what is shown.
    function pidLines(s: Service): string[] {
        const lines = (s.procs ?? []).map((p) => `${p.pid}  ${p.cmd}`);
        // the API lists at most 32 processes: more than that is said, not silently cut off
        if (s.pids_more && lines.length > 0 && lines.length < s.pids_more + 1) lines.push('…');
        return lines;
    }
    // task 49: the Enabled cell - the verdict, the kind of yes or no after it, and a popup where
    // occulited B-132: a switched-off addon's unit is a link to /dev/null: no user to name, nothing to start
    const addonOff = (s: Service) => s.kind === 'addon' && s.unit_file_state === 'masked-runtime';
    // the kind changes what the switch in the ⋯ menu does (lib/units.ts maps the raw state)
    function enabledText(k: EnabledKind): string {
        return k === 'static' ? '—' : k === 'yes' || k === 'addon' ? t('Yes') : t('No');
    }
    function enabledNote(k: EnabledKind, state?: string): string {
        if (k === 'addon') return t('addon');
        if (k === 'static') return t('static');
        if (k === 'off') return t('switched off');
        return unknownState(state);
    }
    function enabledHelp(k: EnabledKind): string | undefined {
        if (k === 'addon') return t("Comes from the addon's rc.d script; Disable switches it off at boot.");
        if (k === 'static') return t('No install section: it starts when another unit needs it and cannot be switched.');
        if (k === 'off') return t('Masked: systemd does not start it, not even when another unit needs it.');
        return undefined;
    }
    // one table: sortable, filterable, with the system and occu categories hidden by default
    type SortKey = 'id' | 'category' | 'description' | 'running' | 'enabled' | 'memory_bytes' | 'cpu_seconds' | 'since';
    let sortKey = $state<SortKey>('id');
    let sortAsc = $state(true);
    let filter = $state('');
    let hideSystem = $state(true);
    let hideOccu = $state(true);
    try {
        hideSystem = localStorage.getItem('ol.services.hideSystem') !== '0';
        hideOccu = localStorage.getItem('ol.services.hideOccu') !== '0';
    } catch { /* no storage */ }
    $effect(() => {
        try {
            localStorage.setItem('ol.services.hideSystem', hideSystem ? '1' : '0');
            localStorage.setItem('ol.services.hideOccu', hideOccu ? '1' : '0');
        } catch { /* no storage */ }
    });
    function sortBy(k: SortKey) {
        if (sortKey === k) sortAsc = !sortAsc;
        else { sortKey = k; sortAsc = true; }
    }
    const catRank: Record<string, number> = {core: 0, addon: 1, occu: 2, system: 3};
    const shown = $derived.by(() => {
        const q = filter.trim().toLowerCase();
        // 28.3: a typed filter searches every service, hidden categories included - the two
        // checkboxes are what to show when nothing is typed, not a second filter on top. Task 80: the
        // protocol is not searched - no row is found by a text the page does not show.
        const list = (services ?? []).filter((s) => {
            if (q) return `${s.id} ${s.description ?? ''} ${s.user ?? ''} ${s.category ?? ''}`.toLowerCase().includes(q);
            const c = s.category ?? (s.kind === 'addon' ? 'addon' : 'system');
            if (hideSystem && c === 'system') return false;
            if (hideOccu && c === 'occu') return false;
            return true;
        });
        const v = (s: Service): string | number => {
            switch (sortKey) {
                case 'category': return catRank[s.category ?? 'system'] ?? 9;
                case 'running': return STATUS_RANK[statusKind(s)];
                case 'enabled': return ENABLED_RANK[enabledKind(s.unit_file_state, s.enabled)];
                case 'memory_bytes': return s.memory_bytes ?? -1;
                case 'cpu_seconds': return cpuPct[s.id] ?? -1;
                case 'since': return s.since ? new Date(s.since).getTime() : 0;
                case 'description': return (s.description ?? '').toLowerCase();
                default: return s.id.toLowerCase();
            }
        };
        return list.sort((a, b) => {
            const x = v(a), y = v(b);
            const r = x < y ? -1 : x > y ? 1 : a.id.localeCompare(b.id);
            return sortAsc ? r : -r;
        });
    });
    const hiddenCount = $derived((services ?? []).length - shown.length);

    // 27.3: six full-size buttons per row wrapped onto a second line and each one sized itself to
    // its own label. Three stay in the row - the state toggle, Restart and the new log link - and
    // the two rare ones move behind the row's overflow menu, the same ol-menu the header uses.
    // Every action is still one click plus, for the rare ones, opening the menu.
    let openMenu = $state('');
    // task 27.4: editing a unit. What is edited is the OVERRIDE - a drop-in in /run, kept on the
    // userfs and replayed at boot - never the shipped file, which is on the read-only rootfs and
    // which a drop-in cannot delete lines from. The effective unit is shown read-only beside it.
    // Task 50: `id` may name a timer (`occu-fstrim.timer`) - a shipped timer gets an override too.
    // Every unit editor is a modal (lib/ModalDialog.svelte, rendered at the end of the page): the
    // page behind it cannot be reached, so one editor is open at most and nothing has to be
    // reconciled between two of them.
    let editor = $state<{id: string; data: UnitOverride; text: string; saving: boolean; error: string; notice: string} | null>(null);
    // task 57: the unit whose editor is open is in the query string (`?edit=rfd`), so it can be linked
    // to and survives a reload; written with replaceState, so Back does not reopen it, and read once
    // on arrival
    $effect(() => {
        const id = editor?.id ?? '';
        untrack(() => {
            if (!life.active) return; // task 177: never another page's query
            const p = new URLSearchParams(router.search);
            if ((p.get('edit') ?? '') === id) return;
            if (id) p.set('edit', id);
            else p.delete('edit');
            const q = p.toString();
            replace(router.path + (q ? `?${q}` : ''));
        });
    });
    async function openEditor(id: string) {
        openMenu = '';
        try {
            const data = await api.get<UnitOverride>(`/api/system/v1/services/${encodeURIComponent(id)}/unit`);
            editor = {id, data, text: data.override, saving: false, error: '', notice: ''};
        } catch (e) {
            output = (e as Error).message;
        }
    }
    const overrideDirty = () => editor !== null && editor.text !== editor.data.override;
    // task 50: an own timer's editor (lib/TimerEditor.svelte): a new timer, or one opened again
    // with its two files. `key` mounts a fresh editor for another timer, so its form starts afresh;
    // after a creation the same editor goes on with the stored timer.
    let timerEdit = $state<{key: number; timer: LocalTimer | null; analyze: boolean} | null>(null);
    let timerEditor: TimerEditor | undefined = $state();
    let timerDirty = $state(false);
    let timerSaving = $state(false);
    let editKey = 0;
    // The footer's Close asks what ModalDialog asks on Escape, × and the backdrop - the same
    // question in the same words, through the shell's dialog - and closes only when nothing is lost
    async function discardOk(unsaved: boolean): Promise<boolean> {
        return !unsaved || (await ask({title: t('Discard changes?'), message: t('What you changed here is not saved.'), confirm: t('Discard'), danger: true}));
    }
    async function closeOverride() {
        if (await discardOk(overrideDirty())) editor = null;
    }
    function timerClosed() {
        timerEdit = null;
        timerDirty = false;
    }
    async function closeTimer() {
        if (await discardOk(timerDirty)) timerClosed();
    }
    // (the page behind an open editor stays put: lib/ModalDialog.svelte locks the page's scrolling
    // while any modal is open - task 51 moved the lock there from this page)
    async function newTimer() {
        try {
            // `analyze`: whether systemd-analyze verifies the files on this system; the editor says so
            const own = await api.get<{analyze: boolean}>('/api/system/v1/timers/own');
            timerEdit = {key: ++editKey, timer: null, analyze: own.analyze};
        } catch (e) {
            output = (e as Error).message;
        }
    }
    // Edit… on an own timer opens its two files; on a shipped one the override, as for a service
    async function editTimer(tm: Timer) {
        openMenu = '';
        if (!tm.own) return openEditor(tm.unit);
        try {
            const [own, stored] = await Promise.all([
                api.get<{analyze: boolean}>('/api/system/v1/timers/own'),
                api.get<LocalTimer>(`/api/system/v1/timers/own/${encodeURIComponent(tm.unit)}`),
            ]);
            timerEdit = {key: ++editKey, timer: stored, analyze: own.analyze};
        } catch (e) {
            output = (e as Error).message;
        }
    }
    function timerSaved(saved: LocalTimer, created: boolean) {
        if (timerEdit) timerEdit.timer = saved;
        output = created ? t('{unit} created: it is enabled and waits for its first run.', {unit: saved.unit}) : '';
        void load();
    }
    // Run now: an own timer's service is started without waiting (`start --no-block`, the outcome is
    // in the log). A shipped timer's service goes through the services control, which waits for a
    // oneshot to end - so it is asked first: a backup or an fstrim is the firmware's job and takes a
    // while.
    async function runTimer(tm: Timer) {
        if (!tm.own && !(await ask(t('Run {unit} now? The page waits until it has finished.', {unit: tm.activates})))) return;
        busy = tm.unit;
        try {
            const r = tm.own
                ? await api.post<{output?: string}>(`/api/system/v1/timers/own/${encodeURIComponent(tm.unit)}/run`)
                : await api.post<{output?: string}>(`/api/system/v1/services/${encodeURIComponent(tm.activates)}/start`);
            output = r.output?.trim() || t('{unit} started; its output is in the log.', {unit: tm.activates});
        } catch (e) {
            output = (e as Error).message;
        } finally {
            busy = '';
            await load();
        }
    }
    // Enable/Disable on a timer is the services switch: a runtime mask for a shipped timer, a
    // runtime disable plus a marker for an own one; either way it holds across a reboot
    async function switchTimer(tm: Timer, on: boolean) {
        openMenu = '';
        if (!on && !(await ask(t('Disable {id}? It no longer runs until it is enabled again, also after a reboot.', {id: tm.unit})))) return;
        busy = tm.unit;
        try {
            const r = await api.post<{output?: string}>(`/api/system/v1/services/${encodeURIComponent(tm.unit)}/${on ? 'enable' : 'disable'}`);
            output = r.output?.trim() ?? '';
        } catch (e) {
            output = (e as Error).message;
        } finally {
            busy = '';
            await load();
        }
    }
    async function deleteTimer(tm: Timer) {
        openMenu = '';
        if (!(await ask({message: t('Delete {unit}? The timer and its service are removed from the system.', {unit: tm.unit}), confirm: t('Delete'), danger: true}))) return;
        busy = tm.unit;
        try {
            await api.del(`/api/system/v1/timers/own/${encodeURIComponent(tm.unit)}`);
            // an open editor of the deleted timer has nothing left to save into
            if (timerEdit?.timer?.unit === tm.unit) {
                timerEdit = null;
                timerDirty = false;
            }
            output = t('{unit} deleted.', {unit: tm.unit});
        } catch (e) {
            output = (e as Error).message;
        } finally {
            busy = '';
            await load();
        }
    }
    // 27.5: a timer's log is its service's; the API appends `.service` to a bare name itself
    function timerLogHref(tm: Timer) {
        return `/system/log?unit=${encodeURIComponent(tm.activates.replace(/\.service$/, ''))}`;
    }
    // task 50: Save applies and keeps the dialog open - the effective text reloaded in place, a short
    // notice under the override; an error from systemd stays in the dialog next to the text
    async function saveEditor(clear = false) {
        if (!editor) return;
        const id = editor.id;
        editor.saving = true;
        editor.error = '';
        editor.notice = '';
        try {
            const data = await api.put<UnitOverride>(`/api/system/v1/services/${encodeURIComponent(id)}/unit`, {override: clear ? '' : editor.text});
            if (!editor) return; // closed meanwhile
            const notice = clear ? t('Override removed and systemd reloaded. Restart {id} for it to take effect.', {id}) : t('Override saved and systemd reloaded. Restart {id} for it to take effect.', {id});
            editor = {...editor, data, text: data.override, saving: false, notice};
        } catch (e) {
            if (editor) editor = {...editor, saving: false, error: (e as Error).message};
        }
    }
    function hasMore(s: Service) {
        return s.managed === true && systemd;
    }
    // an action chosen from the menu closes it first; the confirm dialog must not appear under a
    // popup that then has nothing to close it
    function run(s: Service, action: () => Promise<void>) {
        openMenu = '';
        void action();
    }
    // 27.5: the log with this unit already selected. A route, not shared state, so the link can be
    // copied, opened in a new tab and walked back to. The API appends `.service` itself.
    function logHref(s: Service) {
        return `/system/log?unit=${encodeURIComponent(s.id)}`;
    }
    // close on Escape and on a click outside the open row's menu; listeners exist only while open
    $effect(() => {
        if (openMenu === '') return;
        const onKey = (ev: KeyboardEvent) => {
            if (ev.key === 'Escape') openMenu = '';
        };
        const onDown = (ev: MouseEvent) => {
            const el = (ev.target as HTMLElement | null)?.closest?.('.ol-menu') as HTMLElement | null;
            if (!el || el.dataset.svc !== openMenu) openMenu = '';
        };
        document.addEventListener('keydown', onKey);
        document.addEventListener('mousedown', onDown);
        return () => {
            document.removeEventListener('keydown', onKey);
            document.removeEventListener('mousedown', onDown);
        };
    });
</script>

<!-- 27.3: every action button carries a hidden copy of every label the column can show, stacked
     in the live label's own grid cell, so each button's minimum width is the widest of them. Task 87:
     one sizer for both tables - the services' Start, Stop, Restart and Log and the timers' Run now -
     so every text button on the page has the same width, in each language, and a timer's Log stands
     under a service's. The icon comes first; below 1100 px the label is hidden and the button is an
     icon square, named by its aria-label and its title. -->
{#snippet act(icon: IconName, label: string)}
    <Icon name={icon} size={14} />
    <span class="ol-actlabel">
        <span class="ol-actsizer" aria-hidden="true">{t('Start')}</span>
        <span class="ol-actsizer" aria-hidden="true">{t('Stop')}</span>
        <span class="ol-actsizer" aria-hidden="true">{t('Restart')}</span>
        <span class="ol-actsizer" aria-hidden="true">{t('Run now')}</span>
        <span class="ol-actsizer" aria-hidden="true">{t('Log')}</span>
        <span class="ol-acttext">{label}</span>
    </span>
{/snippet}

<!-- task 87: an item of a row's ⋯ menu, its icon before the words -->
{#snippet item(icon: IconName, label: string, warn = false)}
    <span class="sv-menuicon" class:warn><Icon name={icon} size={14} /></span>{label}
{/snippet}

<!-- task 49: the Enabled cell, shared by the services and the timers table; data-state carries the
     raw UnitFileState for whoever needs it exact -->
{#snippet enabledCell(state: string | undefined, enabled: boolean | undefined)}
    {@const k = enabledKind(state, enabled)}
    {@const note = enabledNote(k, state)}
    {@const help = enabledHelp(k)}
    <!-- task 51: the kind after the verdict (addon, static, switched off) opens what it means for
         the switch, where its `title` used to say it -->
    <span class="ol-enabled" data-state={state ?? ''}>{enabledText(k)}{#if note}<span class="ol-muted">{' · '}</span>{#if help}<Help class="ol-muted">{#snippet trigger()}{note}{/snippet}{help}</Help>{:else}<span class="ol-muted">{note}</span>{/if}{/if}</span>
{/snippet}

<!-- task 49: the processes of a unit, the popup of its pid cell (task 51) -->
{#snippet procList(s: Service)}
    {#if s.stray}<p>{t('outside its unit')}:</p>{/if}
    <ul class="ol-procs hmm-mono">{#each pidLines(s) as line, i (i)}<li>{line}</li>{/each}</ul>
{/snippet}

<!-- `2210 +5`: the +5 opens the list; a single process, the pid itself; no list, a plain pid -->
{#snippet pidCell(s: Service)}
    {#if pidLines(s).length === 0}
        <span class:ol-warn={s.stray}>{s.pid}</span>
    {:else if s.pids_more}
        <span class:ol-warn={s.stray}>{s.pid}</span>{' '}<Help class="ol-muted">{#snippet trigger()}+{s.pids_more}{/snippet}{@render procList(s)}</Help>
    {:else}
        <Help class={s.stray ? 'ol-warn' : ''}>{#snippet trigger()}{s.pid}{/snippet}{@render procList(s)}</Help>
    {/if}
{/snippet}

{#snippet table(list: Service[])}
    <table class="ol-table ol-stack sv-table" use:holdColumns={filter.trim() !== ''}>
        <thead><tr>
            <th class="ol-sort" onclick={() => sortBy('id')}>{t('Service')}{sortKey === 'id' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th>
            <th class="ol-sort sv-kind" onclick={() => sortBy('category')}>{t('Kind')}{sortKey === 'category' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th>
            <th class="ol-sort sv-desc" onclick={() => sortBy('description')}>{t('Description')}{sortKey === 'description' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th>
            <th>PID</th>
            <th class="ol-sort" onclick={() => sortBy('running')}>{t('Status')}{sortKey === 'running' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th>
            <th class="ol-sort" onclick={() => sortBy('enabled')}>{t('Enabled')}{sortKey === 'enabled' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th>
            <th>{t('User')}</th>
            {#if systemd}<th class="ol-sort" onclick={() => sortBy('memory_bytes')}>{t('Memory')}{sortKey === 'memory_bytes' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th><th class="ol-sort sv-cpu" onclick={() => sortBy('cpu_seconds')}>CPU{sortKey === 'cpu_seconds' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th><th class="ol-sort sv-up" onclick={() => sortBy('since')}>{t('Uptime')}{sortKey === 'since' ? (sortAsc ? ' ▲' : ' ▼') : ''}</th>{/if}
            <th></th></tr></thead>
        <tbody>
            {#each list as s (s.id)}
                <!-- task 139: the Addons page's Service link lands on the unit's row (#service-<id>) -->
                {@const edge = rowEdge(s.id)}
                <tr data-service={s.id} id={`service-${s.id}`} class="ol-warn-edge {edge}" class:sv-anchored={anchored === `service-${s.id}`} data-warn-for="crash-loop addon-ownership" data-warn-edge={edge || undefined}>
                    <!-- B-65: the dot is red for a failure only (lib/units.ts). B-75: kind and description
                         stand in a line under the id where their columns are hidden -->
                    <!-- openccu-lite task 100: an addon unit's row carries the addon's icon, where its manifest declares one -->
                    <td class="sv-name"><span class="sv-id"><span class={`ol-dot ${statusDot(s)}`}></span>{#if s.images}<AddonImage candidates={imageCandidates(s.images, 'icon', isDark())} size={16} class="sv-icon" />{/if}{s.id}</span><span class="sv-sub ol-muted">{subline(s)}</span></td>
                    <td class="ol-muted sv-kind">{t(s.category ?? (s.kind === 'addon' ? 'addon' : 'system'))}</td>
                    <td class="ol-muted sv-desc">{s.description ?? ''}</td>
                    <td class="hmm-mono ol-pid" data-label="PID">{#if s.pid}{@render pidCell(s)}{/if}</td>
                    <!-- task 49: whether it is enabled has a column of its own now; task 48: Restart
                         puts a stray addon back into its unit, and the popup says so (task 51: the
                         state itself opens it). Task 94: a unit systemd is starting says so, with its seconds -->
                    <td data-cell="status">{statusText(s)}{#if statusKind(s) === 'skipped'}{' · '}<Help class="ol-muted">{#snippet trigger()}{t('condition not met')}{/snippet}{t('systemd did not start it: a condition in its unit (ConditionPathExists=, ExecCondition= and the like) is not met on this system. That is how a unit stays off where it does not apply, such as the wired interface without its hardware; it is not a failure.')}{#if s.note}{'\n\n'}{t('The radio plan says: {reason}', {reason: s.note})}{/if}</Help>{/if}{#if s.ended}{' · '}<Help class="ol-warn">{#snippet trigger()}{t('why')}{/snippet}{t('The addon keeps a program running, and nothing of it runs any more. Its last lines in the log:')}{'\n\n'}{(s.ended_log ?? []).join('\n') || t('(none)')}</Help>{/if}{#if s.stray}{' · '}<Help class="ol-warn">{#snippet trigger()}{t('outside its unit')}{/snippet}{t('Runs outside its unit (started by an installer or by hand). Restart puts it back into the unit.')}</Help>{/if}</td>
                    <td class="ol-enabledcell" data-label={t('Enabled')}>{@render enabledCell(s.unit_file_state, s.enabled)}</td>
                    <!-- the user column (maintainer, 2026-09-09): root is root, and why it matters is
                         what `root` and `undeclared` open (task 51) -->
                    <td data-label={t('User')}>{#if addonOff(s)}—{:else if s.user}<span class="hmm-mono">{s.user}</span>{:else if systemd && s.kind === 'addon'}<Help class="hmm-mono ol-warn">{#snippet trigger()}root{/snippet}{rootWhy(s)}</Help>{:else}<span class="hmm-mono ol-muted">root</span>{/if}{#if s.undeclared}{' '}<Help class="ol-muted">{#snippet trigger()}{t('undeclared')}{/snippet}{t('This addon declared no compatibility: its manifest carries no runtime block, so nobody has said what it needs. It runs on the system default and may want more than that — the log says what it could not do.')}</Help>{/if}{#if s.may_mount || s.remount_refused}<span class="sv-marks">{#if s.may_mount}<Help class="ol-warn">{#snippet trigger()}{t('may mount file systems')}{/snippet}{t('This addon runs as root and its manifest declares CAP_SYS_ADMIN, so it keeps the right to mount file systems and to remount the system partition, which every other root addon has lost.')}</Help>{/if}{#if s.remount_refused}{#if s.may_mount}{' '}{/if}<Help class="ol-muted">{#snippet trigger()}{t('remount refused')}{/snippet}{t('This addon tried to remount the system partition read-write, which openccu-lite does not allow. It still works: what it writes into the device descriptions lands in the writable extension directory instead.')}</Help>{/if}</span>{/if}</td>
                    {#if systemd}
                        <!-- B-75: where CPU and Uptime have no column of their own they stand under the memory -->
                        <td class="hmm-mono">{mem(s.memory_bytes)}<span class="sv-stack">{cpu(s)}</span><span class="sv-stack">{uptime(s.since)}</span></td>
                        <td class="hmm-mono sv-cpu">{cpu(s)}</td>
                        <td class="sv-up">{uptime(s.since)}</td>
                    {/if}
                    <td class="ol-actions">
                        <div class="ol-rowactions">
                            {#if s.managed}
                                {#if s.running && s.ui}
                                    <!-- task 243: the web interface runs through it - no Stop -->
                                    <span></span>
                                {:else if s.running}
                                    <button class="hmm-button ol-act" disabled={busy !== ''} onclick={() => control(s, 'stop')} aria-label={t('Stop')} title={t('Stop')}>{@render act('stop', t('Stop'))}</button>
                                {:else}
                                    <button class="hmm-button ol-act" disabled={busy !== '' || addonOff(s)} onclick={() => control(s, 'start')} aria-label={t('Start')} title={addonOff(s) ? t('Switched off: enable it in the menu first') : t('Start')}>{@render act('play', t('Start'))}</button>
                                {/if}
                                <button class="hmm-button ol-act" disabled={busy !== '' || addonOff(s)} onclick={() => control(s, 'restart')} aria-label={t('Restart')} title={addonOff(s) ? t('Switched off: enable it in the menu first') : t('Restart')}>{@render act('restart', t('Restart'))}</button>
                            {:else}
                                <span></span><span></span>
                            {/if}
                            <a class="hmm-button ol-act ol-logbtn" href={logHref(s)} use:link aria-label={t('Log')} title={t('Show the log for {id}', {id: s.id})}>{@render act('log', t('Log'))}</a>
                            {#if !hasMore(s)}
                                <span class="ol-morebtn-gap"></span>
                            {:else}
                                <div class="ol-menu" data-svc={s.id}>
                                    <button
                                        type="button"
                                        class="hmm-button ol-act ol-morebtn"
                                        aria-haspopup="menu"
                                        aria-expanded={openMenu === s.id}
                                        aria-label={t('More actions')}
                                        title={t('More actions')}
                                        onclick={() => (openMenu = openMenu === s.id ? '' : s.id)}
                                    ><Icon name="more" size={14} /></button>
                                    {#if openMenu === s.id}
                                        <div class="ol-menupop" role="menu">
                                            {#if systemd && s.kind === 'addon' && s.id.startsWith('addon-') && !addonOff(s)}
                                                {#if s.user}
                                                    <!-- task 51: no `title` - the question this opens says what root means -->
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => run(s, () => setMode(s, 'root'))}>{@render item('alert', t('Run as root (unsafe)'), true)}</button>
                                                {:else}
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => run(s, () => setMode(s, 'confined'))}>{@render item('user', t('Own user'))}</button>
                                                {/if}
                                            {/if}
                                            {#if systemd}
                                                <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => openEditor(s.id)}>{@render item('edit', t('Edit unit…'))}</button>
                                                <!-- task 49: systemctl refuses both on a static unit -->
                                                {#if !switchable(s.unit_file_state) || s.ui}
                                                    <!-- nothing to switch -->
                                                {:else if s.enabled}
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => run(s, () => control(s, 'disable'))}>{@render item('toggle-off', t('Disable'))}</button>
                                                {:else}
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => run(s, () => control(s, 'enable'))}>{@render item('toggle-on', t('Enable'))}</button>
                                                {/if}
                                            {/if}
                                        </div>
                                    {/if}
                                </div>
                            {/if}
                        </div>
                    </td>
                </tr>
            {/each}
        </tbody>
    </table>
{/snippet}

<!-- task 51: what an addon unit is and how an addon runs, behind the page heading's ? while the box
     has addon units (it was two paragraphs under the table) -->
<SystemTitle>{#if systemd && (services ?? []).some((s) => s.category === 'addon')}<Help><p>{t('Each addon runs in its own unit: stop ends every process it started, and its output is in the log under the unit name.')}</p><p>{t('A newly installed addon runs as its own user with what its manifest declares. An addon marked undeclared has declared nothing; one marked root can change anything on the system.')}</p></Help>{/if}</SystemTitle>
{#if !services}
    <Loading {error} />
{:else}
    {#if reconnecting}<div class="ol-notice" data-reconnecting><span class="ol-dot starting"></span>{t('Reconnecting…')}</div>{/if}
    {#if output}<pre class="ol-notice ol-log">{output}</pre>{/if}
    <div class="ol-toolbar">
        <input class="hmm-input" placeholder={t('Filter')} bind:value={filter} />
        <label><input type="checkbox" bind:checked={hideSystem} /> {t('Hide system services')}</label>
        <label><input type="checkbox" bind:checked={hideOccu} /> {t('Hide occu services')}</label>
        {#if filter.trim()}<span class="ol-muted">{t('{n} of {total} match; a typed filter searches every service', {n: shown.length, total: (services ?? []).length})}</span>{:else if hiddenCount}<span class="ol-muted">{t('{n} hidden', {n: hiddenCount})}</span>{/if}
    </div>
    {#if shown.length === 0}
        <div class="ol-muted">{t('Nothing matches.')}</div>
    {:else}
        {@render table(shown)}
    {/if}
    {#if systemd && (services ?? []).length > 0 && !(services ?? []).some((s) => s.id === 'occu-etc-writable' && s.running)}
        <!-- 28.7: on an image without B-28's unit (beta.2) every switch to confined is refused with
             addgroup's read-only error; say so before the reader tries -->
        <p class="ol-warn">{t('This image cannot confine addons: occu-etc-writable.service is not active, so no addon user can be created and every addon runs as root. An image from 2026-09-08 or later has it.')}</p>
    {/if}
    {#if systemd}
        <h2>{t('Timers')}</h2>
        <!-- task 50: own timers - local-<name>.timer with its service - are made here -->
        <div class="ol-timerbar">
            <button class="hmm-button" disabled={busy !== ''} onclick={newTimer}>{t('New timer…')}</button>
        </div>
        {#if timers.length === 0}
            <div class="ol-muted">{t('No timers.')}</div>
        {:else}
            <table class="ol-table ol-stack ol-timers sv-table">
                <thead><tr><th>{t('Timer')}</th><th class="sv-runs">{t('Runs')}</th><th>{t('Next')}</th><th>{t('Last')}</th><th>{t('Status')}</th><th>{t('Enabled')}</th><th></th></tr></thead>
                <tbody>
                    {#each timers as tm (tm.unit)}
                        {@const left = whenLeft(tm)}
                        <tr>
                            <td>{tm.unit}{#if tm.own}{' '}<span class="ol-badge">{t('own')}</span>{/if}<span class="sv-runs-sub ol-muted">{tm.activates}</span></td>
                            <td class="sv-runs">{tm.activates}</td>
                            <!-- B-68: the runs in the reader's language and time zone, short, the full stamp as
                                 the title; the time and the countdown each stay whole, so a narrow cell
                                 is two lines at most -->
                            <td data-label={t('Next')} title={runTimeFull(tm.next, i18n.language) || undefined}>{#if tm.next}<span class="sv-nowrap">{runTime(tm.next, i18n.language)}</span>{#if left}{' '}<span class="sv-nowrap ol-muted">({left})</span>{/if}{:else}—{/if}</td>
                            <td class="sv-nowrap" data-label={t('Last')} title={runTimeFull(tm.last, i18n.language) || undefined}>{tm.last ? runTime(tm.last, i18n.language) : '—'}</td>
                            <td>{tm.active ? t('Active') : t('Inactive')}</td>
                            <td class="ol-enabledcell" data-label={t('Enabled')}>{@render enabledCell(tm.unit_file_state, tm.enabled)}</td>
                            <td class="ol-actions">
                                <!-- task 87: the services' grid - Run now where Start is, the place of
                                     Restart left empty - so Log and the menu stand under a service's -->
                                <div class="ol-rowactions ol-timeractions">
                                    <button class="hmm-button ol-act" disabled={busy !== ''} onclick={() => runTimer(tm)} aria-label={t('Run now')} title={t('Run now')}>{@render act('zap', t('Run now'))}</button>
                                    <span></span>
                                    <a class="hmm-button ol-act ol-logbtn" href={timerLogHref(tm)} use:link aria-label={t('Log')} title={t('Show the log for {id}', {id: tm.activates})}>{@render act('log', t('Log'))}</a>
                                    <div class="ol-menu" data-svc={tm.unit}>
                                        <button
                                            type="button"
                                            class="hmm-button ol-act ol-morebtn"
                                            aria-haspopup="menu"
                                            aria-expanded={openMenu === tm.unit}
                                            aria-label={t('More actions')}
                                            title={t('More actions')}
                                            onclick={() => (openMenu = openMenu === tm.unit ? '' : tm.unit)}
                                        ><Icon name="more" size={14} /></button>
                                        {#if openMenu === tm.unit}
                                            <div class="ol-menupop" role="menu">
                                                <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => editTimer(tm)}>{@render item('edit', t('Edit…'))}</button>
                                                {#if !switchable(tm.unit_file_state)}
                                                    <!-- a static timer cannot be switched (task 49) -->
                                                {:else if tm.enabled !== false}
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => switchTimer(tm, false)}>{@render item('toggle-off', t('Disable'))}</button>
                                                {:else}
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => switchTimer(tm, true)}>{@render item('toggle-on', t('Enable'))}</button>
                                                {/if}
                                                {#if tm.own}
                                                    <button type="button" role="menuitem" class="ol-menuitem" disabled={busy !== ''} onclick={() => deleteTimer(tm)}>{@render item('trash', t('Delete'))}</button>
                                                {/if}
                                            </div>
                                        {/if}
                                    </div>
                                </div>
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        {/if}
        <!-- task 93: the boot order graph, below the timers (lib/BootTimeline.svelte) -->
        <BootTimeline />
    {/if}
{/if}

<!-- task 50: every unit editor is a modal (lib/ModalDialog.svelte) - large, two columns while there
     is room and one above the other on a phone, the focus kept inside, Escape, × and the backdrop
     close it, and unsaved changes ask first. It left the addon block of the page, where a filter
     that hid every addon hid an open editor too. Save applies and keeps it open with a notice. -->
<ModalDialog open={editor !== null} title={editor ? `${t('Edit unit')} ${editor.data.unit}` : t('Edit unit')} size="large" onclose={() => (editor = null)} dirty={overrideDirty}>
    <!-- task 51: what an override is, behind the ? of the dialog's title -->
    {#snippet help()}{t('The rootfs is read-only, so what you edit is an override: a drop-in under /run/systemd/system that systemd applies on top of the shipped unit. It is kept on the userfs and re-applied at every boot. A drop-in adds or overrides directives and cannot delete them; to replace a list directive such as ExecStart= write an empty ExecStart= first, then the new one.')}{/snippet}
    {#if editor}
        <div class="editor-grid">
            <div class="editor-col">
                <div class="lbl">{t('Effective unit')} <span class="ol-muted">· systemctl cat</span></div>
                <pre class="hmm-mono effective">{editor.data.effective}</pre>
            </div>
            <div class="editor-col">
                <div class="lbl">{t('Override')} <span class="ol-muted hmm-mono">{editor.data.path}</span></div>
                <textarea class="hmm-input hmm-mono override" bind:value={editor.text} aria-label={t('Override')} spellcheck="false" placeholder={editor.id.endsWith('.timer') ? '[Timer]\nRandomizedDelaySec=10min' : '[Service]\nEnvironment=EXAMPLE=1'} disabled={editor.saving}></textarea>
                {#if editor.error}<div class="ol-warn editor-msg" role="alert">{editor.error}</div>{/if}
                {#if editor.notice}<div class="ol-muted editor-msg" role="status">{editor.notice}</div>{/if}
            </div>
        </div>
    {/if}
    {#snippet footer()}
        <button class="hmm-button primary" disabled={!editor || editor.saving || editor.text.trim() === ''} onclick={() => saveEditor(false)}>{t('Save override')}</button>
        <button class="hmm-button" disabled={!editor || editor.saving || editor.data.override === ''} onclick={() => saveEditor(true)}>{t('Remove override')}</button>
        <button class="hmm-button" disabled={editor?.saving} onclick={closeOverride}>{t('Close')}</button>
    {/snippet}
</ModalDialog>

<ModalDialog open={timerEdit !== null} title={timerEdit?.timer ? `${t('Edit timer')} ${timerEdit.timer.unit}` : t('New timer')} size="large" onclose={timerClosed} dirty={() => timerDirty}>
    <!-- task 51: that the two files are what is saved was a note above them in TimerEditor; it is
         the editor's help as a whole, behind the ? of the dialog's title -->
    {#snippet help()}{t('What is saved are these two files; any directive the form does not have can be written into them.')}{/snippet}
    {#if timerEdit}
        {#key timerEdit.key}
            <TimerEditor bind:this={timerEditor} timer={timerEdit.timer} analyze={timerEdit.analyze} bind:dirty={timerDirty} bind:saving={timerSaving} onsaved={timerSaved} />
        {/key}
    {/if}
    {#snippet footer()}
        <button class="hmm-button primary" disabled={timerSaving || !timerDirty} onclick={() => timerEditor?.save()}>{t('Save')}</button>
        <button class="hmm-button" disabled={timerSaving} onclick={closeTimer}>{t('Close')}</button>
    {/snippet}
</ModalDialog>

<style>
    /* 27.3: the actions column. One grid per row - the state toggle, Restart, Log, and the
       overflow button - so the three text buttons are the same width in a row and, because every
       row's grid is as wide as the shared table column, the same width down the column too. That
       is what "not aligned, different widths" was: an inline-flex row of buttons each as wide as
       its label, which in German also wrapped onto a second line. The nowrap and the fixed columns
       keep it to one line; the smaller type and padding suit a 12 px grid. */
    /* task 87: the column is as narrow as its grid and the grid stands at its right edge. Both tables
       are as wide as the page, their last cells have the same padding, and every row's grid is the
       same width (one sizer, one track list), so Log and the menu of a timer stand exactly under a
       service's, and the first buttons line up too. */
    td.ol-actions {
        white-space: nowrap;
        width: 1%;
    }
    /* task 49: `2210 +5` and `Yes · addon` stay on one line; the table scrolls sideways on a phone */
    td.ol-pid,
    td.ol-enabledcell {
        white-space: nowrap;
    }
    /* the mount marks of a root addon stand under its user on a line of their own, so the User
       column stays as narrow as its widest word (German needs every pixel at 1120 px) */
    .sv-marks { display: block; font-size: var(--hmm-font-size-small); }
    .ol-rowactions {
        display: grid;
        grid-template-columns: 1fr 1fr 1fr 28px;
        gap: 4px;
        align-items: center;
        width: max-content;
        margin-left: auto;
    }
    .ol-rowactions .ol-act {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 4px;
        box-sizing: border-box;
        min-height: 20px;
        /* 5 px, not 6: with the icon German needs every pixel of B-75's 1101 px (measured, task 87) */
        padding: 1px 5px;
        font-size: var(--hmm-font-size-small);
        line-height: 16px;
        /* `white-space: nowrap` with the default `overflow: visible` is what makes a 1fr track's
           automatic minimum the label's own width: the three columns then come out equal to the
           widest label rather than collapsing to nothing. An `overflow: hidden` here would zero
           that minimum and squash the buttons to 14 px, which is exactly what it did. */
        white-space: nowrap;
    }
    .ol-actlabel {
        display: inline-grid;
    }
    .ol-actsizer,
    .ol-acttext {
        grid-area: 1 / 1;
        text-align: center;
    }
    .ol-actsizer {
        visibility: hidden;
        pointer-events: none;
    }
    /* the log action is a link so it can be middle-clicked and copied; it still looks like a button */
    a.ol-logbtn {
        color: inherit;
        text-decoration: none;
    }
    .ol-morebtn-gap { display: block; width: 28px; }
    /* the button above the timers; not an .ol-toolbar - the page's one toolbar is the filter's */
    .ol-timerbar { display: flex; gap: 8px; margin-bottom: 8px; }
    .ol-rowactions .ol-morebtn {
        width: 100%;
        padding: 1px 0;
    }
    /* task 87: a menu item's icon; Run as root's in the warning colour its words imply */
    .sv-menuicon { display: inline-flex; color: var(--hmm-fg-muted); }
    .sv-menuicon.warn { color: var(--hmm-warn); }
    /* the column is the last one in the table, so the popup hangs from the right edge or it would
       leave the window */
    .ol-rowactions .ol-menu {
        display: block;
    }
    .ol-rowactions .ol-menupop {
        left: auto;
        right: 0;
    }
    .ol-rowactions .ol-menuitem:disabled {
        opacity: 0.45;
        cursor: default;
    }
    /* 27.4, task 50: the unit editor in its modal. Two columns while the dialog has room - the
       effective unit to read, the override to write - and one on a phone. The effective text is a
       <pre> that scrolls by itself; the dialog's body scrolls when the two do not fit. */
    /* task 51: the process list in the pid cell's popup - one line per process, the command kept whole */
    :global(.ol-help-body) ul.ol-procs { list-style: none; margin: 0; padding: 0; white-space: pre-wrap; font-size: var(--hmm-font-size-grid); }
    .editor-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(420px, 100%), 1fr)); gap: 12px; padding-bottom: 4px; }
    .editor-col { min-width: 0; }
    .editor-col .lbl { color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); margin-bottom: 4px; overflow-wrap: anywhere; }
    .effective { margin: 0; max-height: 50dvh; overflow: auto; padding: 8px; font-size: 11px; line-height: 1.4;
        border: 1px solid var(--hmm-border-muted); border-radius: var(--hmm-radius); background: var(--hmm-bg); white-space: pre; }
    .override { width: 100%; box-sizing: border-box; min-height: 240px; resize: vertical; font-size: 12px; line-height: 1.4; }
    .editor-msg { margin-top: 6px; white-space: pre-wrap; overflow-wrap: anywhere; }
    /* B-65: skipped - a condition keeps the unit off this box - is neither a failure nor a plain
       stop: a hollow ring in the neutral grey */
    /* B-68: a timer's run and its countdown each stay whole, so a narrow cell is two lines at most */
    .sv-nowrap { white-space: nowrap; }
    /* B-75: the table was as wide as its columns' minimum widths - 1489 px on a 768 px tablet, and
       on a 1280 px desktop in German the action column ran past the right edge. Two steps fold it
       instead of letting it overflow. Up to 1350 px Kind and Description become one line under the
       id. German needs about 1270 px for every column (task 80 measured 1266 px with the layout
       test's fixture, 1344 px while the Protocol column was there, which is why this step was at
       1400 px); the rest is room for a classic scrollbar's gutter and for longer figures on a real
       box, and a 1366 px laptop shows every column. Up to 1100 px CPU and Uptime stand
       under the memory, Runs under the timer's name, the Enabled cell may wrap, and the action
       buttons take two rows of two; the buttons stay equal down the column (27.3), the PID may wrap
       before its +N, and the cells' side padding narrows. A description and a timer's unit names
       wrap anywhere, so no word in them can widen a table: an own timer's name is up to 32
       characters without a hyphen. */
    .sv-id :global(img.sv-icon) { width: 16px; height: 16px; object-fit: contain; vertical-align: -3px; margin-right: 6px; }
    .sv-sub, .sv-runs-sub, .sv-stack { display: none; }
    td.sv-desc, table.ol-timers td:first-child, td.sv-runs { overflow-wrap: anywhere; }
    @media (max-width: 1350px) {
        th.sv-kind, td.sv-kind, th.sv-desc, td.sv-desc { display: none; }
        .sv-sub { display: block; padding-left: 14px; font-size: var(--hmm-font-size-small); overflow-wrap: anywhere; }
    }
    @media (max-width: 1100px) {
        th.sv-cpu, td.sv-cpu, th.sv-up, td.sv-up, th.sv-runs, td.sv-runs { display: none; }
        .sv-stack { display: block; }
        .sv-runs-sub { display: block; font-size: var(--hmm-font-size-small); }
        td.ol-enabledcell, td.ol-pid { white-space: normal; }
        .sv-table th, .sv-table td { padding-left: 5px; padding-right: 5px; }
        /* task 87: the actions become icon squares in one row, where 27.3 had two rows of two text
           buttons: the rows stay short on a tablet and a phone. The label leaves the layout; the
           button keeps its aria-label and its title, a 28 px hit area and the focus ring. */
        .ol-rowactions { grid-template-columns: repeat(4, 28px); }
        .ol-rowactions .ol-act { width: 28px; min-height: 28px; padding: 0; }
        .ol-actlabel { display: none; }
    }
    /* B-105: at 412 px the services table was 517 px and the timers 479 px (German 606 and 504), so
       the page scrolled sideways and a timer's name wrapped letter by letter in what was left. Below
       700 px both stack (app.css `.ol-stack`): the name and the four action squares (4 x 28 px and
       three gaps, 124 px) on the first line, the other cells after them as a wrapping line that names
       its values. The services' head stays as a row of its sort buttons, so a phone still sorts. */
    @media (max-width: 700px) {
        .sv-table > thead { display: block; }
        .sv-table > thead > tr { display: flex; flex-wrap: wrap; background: var(--hmm-header-bg); border-bottom: 1px solid var(--hmm-border); }
        .sv-table > thead > tr > th { position: static; border-bottom: 0; background: none; }
        .sv-table > thead > tr > th:not(.ol-sort) { display: none; }
        .ol-timers > thead { display: none; }
        .sv-table > tbody > tr > td { order: 2; }
        .sv-table > tbody > tr > td:first-child { order: 0; flex: 1 1 calc(100% - 136px); }
        .sv-table > tbody > tr > td.ol-actions { order: 1; flex: 0 0 auto; width: auto; align-self: center; }
        .sv-stack:not(:empty) { display: inline; }
        .sv-stack:not(:empty)::before { content: ' · '; }
        .sv-stack:empty { display: none; }
        /* the boot order graph's highlight (BootTimeline.svelte) on the row, not on each cell's box */
        .sv-table > tbody > tr:global(.ol-bt-highlight) { background: var(--hmm-accent-bg); box-shadow: inset 0 1px 0 var(--hmm-accent), inset 0 -1px 0 var(--hmm-accent); }
        .sv-table > tbody > tr:global(.ol-bt-highlight) > td { box-shadow: none; }
    }
    /* task 139: the row a link from the Addons page named */
    tr.sv-anchored > td { background: var(--hmm-accent-bg); }
</style>
