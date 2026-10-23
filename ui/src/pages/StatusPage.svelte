<script lang="ts">
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type Status, type CertStatus, type CertInfo, type HTTPSView, type StorageReason, type StorageReport} from '../lib/api';
    import {shortVersion} from '../lib/version';
    import {formatBytes} from '../lib/netpanels';
    import {anchorDisk, hostMonitored} from '../lib/storage';
    import type {StatusDevice} from '../lib/storagepanels';
    import {ask} from '../lib/dialog.svelte';
    import {i18n, t} from '../lib/i18n.svelte';
    import Loading from '../lib/Loading.svelte';
    import Gauge from '../lib/Gauge.svelte';
    import HistoryChart from '../lib/HistoryChart.svelte';
    import {view, setChartSpan, setSubsOpen} from '../lib/viewstate.svelte';
    import SubscriberLabel from '../lib/SubscriberLabel.svelte';
    import StoragePanel from '../lib/StoragePanel.svelte';
    import {clientLabel, streamDetail, streamName as streamLabel, type SubClient, type SubSummary} from '../lib/subscribers';
    import Icon from '../lib/Icon.svelte';
    import ServiceMessages from '../lib/ServiceMessages.svelte';
    import Notice from '../lib/Notice.svelte';
    import PairingRequests from '../lib/PairingRequests.svelte';
    import {PERIODS, periodLabel, silencedLine, silencePath, splitSilenced, warningLink, warningText, type WarnAddon, type WarnDevice, type Warning, type WarningsView} from '../lib/warnings';
    import Help from '../lib/Help.svelte';
    import {CLOSE_MS, reveal} from '../lib/reveal';
    import {shownDevices} from '../lib/devicefirmware';
    import {mutedUntilText, type LEDState} from '../lib/led';
    import {DUTY_LEVELS, MEM_LEVELS, levelOf, mergeMax, type HealthSample, CARRIER_BAD, carrierLevel, spanMax, RATE_STEPS, RATE_OUTLIER_FLOOR, rateSeries, formatRate, type RateSample, LOAD_STEPS, loadSeries, memSeries, formatLoad, type SystemSample} from '../lib/history';
    import {pendingUpdates} from '../lib/catalog';
    import {span} from '../lib/units';
    import {anyStarting, startingSeconds, unitOf, type InterfaceUnit} from '../lib/starting';

    import {link} from '../lib/router.svelte';
    import {auth} from '../lib/auth.svelte';
    import {warnEdge, warnFor} from '../lib/systemmenu.svelte';

    // occulited task 13: `module`, `adapter`, `path` - the radio behind the entry, from the radio plan
    interface RadioIf { interface: string; address: string; connected: boolean; duty_cycle: number; dc_threshold?: number; transfer?: boolean; carrier_sense?: number; carrier_sense_source?: 'interface' | 'device'; radio?: string; radio_name?: string; module?: string; adapter?: string; path?: string }
    // occulited task 13: who is subscribed to an interface process - internal is a callback on this system (loopback or its own address)
    // occulited B-45: `stream` - a client subscribed through lite-rpc's event stream (a token's, an account's)
    // task 94: `units` are the radio stack's units, so a starting interface says so instead of "not answering";
    // occulited task 13: `rates` the events per minute of each interface process (task 17), `subscribers` its callbacks and lite-rpc streams (B-45)
    interface Health { interfaces: RadioIf[]; answering?: string[]; busy: boolean; errors: Record<string, string>; history?: Record<string, HealthSample[]>; units?: InterfaceUnit[]; rates?: Record<string, RateSample[]>; subscribers?: Record<string, SubSummary> }
    interface FwState { enabled: boolean; devices: {interface: string; update_available: boolean}[]; last_error?: string }
    let status = $state<Status | null>(null);
    let health = $state<Health | null>(null);
    // occulited task 37: the system's load and memory history; null on a system without the sampler
    let sysHist = $state<SystemSample[] | null>(null);
    // task 129 phase 3: an interface process off by choice, or a chosen module that is missing - the
    // interface list leaves such an interface out, so the health cards alone would not show it
    interface ConnView { available: boolean; choices: {hmip: string; bidcos: string}; plan?: {rfd: {run: boolean}; missing_hmip?: string; missing_bidcos?: string; hmip_pin?: string} }
    let conn = $state<ConnView | null>(null);
    // task 94: when the last /radio/health arrived (the browser's clock), and a clock that ticks every
    // second while an interface process is starting, so its seconds count on between two polls
    let healthAt = $state(0);
    let now = $state(Date.now());
    $effect(() => {
        if (!anyStarting(health?.units)) return;
        now = Date.now();
        const id = setInterval(() => (now = Date.now()), 1000);
        return () => clearInterval(id);
    });
    // the interfaces of a starting unit the sampler has not seen at all - neither answered nor failed
    // (a unit queued behind the detection, a poll that has not happened): a starting card each.
    // VirtualDevices is never sampled, so it has no card of its own here.
    const unseenStarting = $derived.by(() => {
        if (!health) return [];
        const seen = new Set([...health.interfaces.map((i) => i.interface), ...(health.answering ?? []), ...Object.keys(health.errors)]);
        const out: {name: string; unit: InterfaceUnit}[] = [];
        for (const u of health.units ?? []) {
            if (!u.starting) continue;
            for (const name of u.interfaces) {
                if (name !== 'VirtualDevices' && !seen.has(name)) out.push({name, unit: u});
            }
        }
        return out;
    });
    const startingTitle = (u: InterfaceUnit) =>
        u.queued ? t('systemd has queued {unit}: it starts once the units it waits for are up.', {unit: u.unit}) : t('systemd is starting {unit}. After a boot the HmIP server takes about a minute.', {unit: u.unit});
    let fw = $state<FwState | null>(null);
    let addonCount = $state<number | null>(null);
    // task 56: the addons with an update waiting - the catalogue's and their own checks', each once
    let addonUpdates = $state<string[]>([]);
    // task 81: the warnings as occulited evaluates them (lib/warnings.ts), read with the page and
    // every half minute; an administrator's own silences come along on them (D-64). The names the
    // addon warnings say: the addon's own, else the catalogue's, else its id (B-69).
    const life = pageLife();
    let warnings = $state<Warning[]>([]);
    let periods = $state<number[]>(PERIODS);
    let addonNames = $state<Record<string, string>>({});
    let warnBusy = $state(false);
    let warnNotice = $state('');
    let showSilenced = $state(false);
    // the warning whose Silence menu is open, by warnKey
    let silenceOpen = $state('');
    const admin = $derived(auth.role === 'admin');
    const split = $derived(splitSilenced(warnings));
    const warnKey = (w: {id: string; variant: string}) => `${w.id}\n${w.variant}`;
    function takeWarnings(r: WarningsView) {
        warnings = r.warnings ?? [];
        if (r.periods?.length) periods = r.periods;
    }
    // task 177: the page's first fade-in waits for the warnings (the maintainer: they appeared later)
    let warningsLoaded = $state(false);
    async function loadWarnings() {
        try {
            takeWarnings(await api.get<WarningsView>('/api/system/v1/warnings'));
        } catch {
            warnings = [];
        }
        warningsLoaded = true;
    }
    // occulited task 62: the LED's mute (the radio module's button or System -> Hardware), read with
    // the warnings; an administrator can end it here. Nothing shows without a mute, or when the
    // state cannot be read (no LED, a role without the scope).
    let mutedUntil = $state('');
    let muteBusy = $state(false);
    async function loadMute() {
        try {
            const st = await api.get<LEDState>('/api/system/v1/led/state');
            mutedUntil = st.muted_until ?? '';
        } catch {
            mutedUntil = '';
        }
    }
    async function unmute() {
        muteBusy = true;
        try {
            const st = await api.del<LEDState>('/api/system/v1/led/mute');
            mutedUntil = st.muted_until ?? '';
            warnNotice = '';
        } catch (e) {
            warnNotice = (e as Error).message;
            await loadMute();
        } finally {
            muteBusy = false;
        }
    }
    // D-65: the administrator picks the period; the silence still ends when the warning clears
    async function silence(w: Warning, days: number) {
        silenceOpen = '';
        warnBusy = true;
        try {
            takeWarnings(await api.post<WarningsView>('/api/system/v1/warnings/silence', {id: w.id, variant: w.variant, days}));
            warnNotice = '';
        } catch (e) {
            warnNotice = (e as Error).message;
        } finally {
            warnBusy = false;
        }
    }
    async function unsilence(w: Warning) {
        warnBusy = true;
        try {
            takeWarnings(await api.del<WarningsView>(silencePath(w)));
            warnNotice = '';
        } catch (e) {
            warnNotice = (e as Error).message;
            await loadWarnings();
        } finally {
            warnBusy = false;
        }
    }
    const nameOf = (a: WarnAddon) => a.name || addonNames[a.id] || a.id;
    // B-92: the files an update wrote as root go back to the addon's user, addon by addon
    async function fixOwnership(w: Warning) {
        warnBusy = true;
        const fixed: string[] = [];
        const left: string[] = [];
        // task 81 (D-67): an addon whose unit had failed is started once after the fix
        const started: string[] = [];
        try {
            for (const a of w.params?.addons ?? []) {
                const r = await api.post<{id: string; root_owned: string; started?: boolean; start_error?: string}>(`/api/system/v1/addons/${encodeURIComponent(a.id)}/ownership`);
                if (r.root_owned) left.push(`${nameOf(a)}: ${r.root_owned}`);
                else if (!r.started && !r.start_error) fixed.push(nameOf(a));
                if (r.started) started.push(t('{name} had failed: its files belong to the addon again, and it was started.', {name: nameOf(a)}));
                else if (r.start_error) started.push(t('{name} had failed: its files belong to the addon again, but starting it failed: {error}', {name: nameOf(a), error: r.start_error}));
            }
            warnNotice = [
                fixed.length ? t('The files of {list} belong to the addon again. An addon that did not start can be started on the Services page.', {list: fixed.join(', ')}) : '',
                ...started,
                left.length ? t('Still owned by root: {list}', {list: left.join(', ')}) : '',
            ].filter(Boolean).join(' ');
        } catch (e) {
            warnNotice = (e as Error).message;
        } finally {
            warnBusy = false;
            await loadWarnings();
        }
    }
    // the Silence menu hangs from the button's right edge, and from its left edge where that
    // would run off the left of a phone
    function keepOnScreen(el: HTMLElement) {
        if (el.getBoundingClientRect().left < 4) {
            el.style.right = 'auto';
            el.style.left = '0';
        }
    }
    function onWindowClick(e: MouseEvent) {
        if (silenceOpen && !(e.target as Element | null)?.closest?.('.ol-silence')) silenceOpen = '';
    }
    function onWindowKey(e: KeyboardEvent) {
        if (e.key === 'Escape') silenceOpen = '';
    }
    // task 35: the certificate card - mode, expiry, a warning below 14 days or after a failed renewal
    let cert = $state<CertStatus | null>(null);
    // task 36: the redirect and HSTS words on that card
    let https = $state<HTTPSView | null>(null);
    let error = $state('');
    // task 69: the disks' health (task 111: in their usage panels), read once a minute - the box caches SMART for an hour and
    // the kernel log for five minutes, so the ten-second poll of the rest has nothing to add
    let storage = $state<StorageReport | null>(null);
    // task 111: the panels are there before it answers; its pill and chips take no height of their own
    async function loadStorage() {
        try { storage = await api.get<StorageReport>('/api/system/v1/storage'); } catch { storage = null; }
    }
    // task 177: the page shows once its local answers are in - the status and the component cards -
    // instead of the cards arriving one by one; the catalogue's badge (which may go to the internet)
    // and the certificate come after
    let firstDone = $state(false);
    async function load() {
        type Installed = {id: string; name: string; rega_dependent?: boolean; binary_incompatible?: boolean; enabled: boolean};
        const [, , , , , installed] = await Promise.all([
            (async () => {
                try {
                    status = await api.get<Status>('/api/system/v1/status');
                    error = '';
                } catch (e) {
                    error = (e as Error).message;
                }
            })(),
            // the secondary cards degrade quietly: a box without the sampler or the fetcher still has a status page
            (async () => { try { health = await api.get<Health>('/api/system/v1/radio/health'); healthAt = Date.now(); } catch { health = null; } })(),
            (async () => { try { conn = await api.get<ConnView>('/api/system/v1/radio/connections'); } catch { conn = null; } })(),
            (async () => { try { sysHist = (await api.get<{samples: SystemSample[]}>('/api/system/v1/status/history')).samples; } catch { sysHist = null; } })(),
            (async () => { try { fw = await api.get<FwState>('/api/system/v1/firmware'); } catch { fw = null; } })(),
            (async (): Promise<Installed[] | null> => {
                try {
                    const list = (await api.get<{addons: Installed[]}>('/api/system/v1/addons')).addons;
                    addonCount = list.length;
                    return list;
                } catch {
                    addonCount = null;
                    return null;
                }
            })(),
        ]);
        firstDone = true;
        // task 56: the badge on the Addons card counts what the Installed addons page offers to
        // update - the catalogue's `update_available` and the addons' own daily `Update:` checks -
        // and an addon that is in both is one update. Either source may be missing on a box.
        let checks: {id: string; info?: {update_available?: boolean}}[] = [];
        let catalogue: {id: string; name?: {de?: string; en?: string}; update_available?: boolean}[] = [];
        try { checks = (await api.get<{results?: typeof checks}>('/api/system/v1/addons/updates')).results ?? []; } catch { /* no checker */ }
        // D-119: an entry whose manifest is not fetched yet has no id and names nothing
        try { catalogue = ((await api.get<{catalog?: {addons?: typeof catalogue}}>('/api/system/v1/catalog')).catalog?.addons ?? []).filter((e) => !!e.id); } catch { /* no catalogue */ }
        addonUpdates = pendingUpdates(catalogue, checks);
        if (installed) {
            // B-69: a warning names an addon as a person knows it - the name it declares (the API
            // already falls back to its hm_addons.cfg entry and a known name), else the catalogue's,
            // the rc.d id only when there is nothing else. Which addons a warning names is
            // occulited's (task 81, GET /warnings); the names are the page's.
            const catalogName = new Map(catalogue.map((e) => [e.id, e.name?.en ?? e.name?.de ?? '']));
            addonNames = Object.fromEntries(installed.map((a) => [a.id, a.name || catalogName.get(a.id) || a.id]));
        }
        try { cert = await api.get<CertStatus>('/api/system/v1/certificate'); } catch { cert = null; }
        try { https = await api.get<HTTPSView>('/api/system/v1/https'); } catch { https = null; }
    }
    // occulited task 60: the HmIP devices, as the device firmware list shows them
    const fwUpdates = $derived(fw ? shownDevices(fw.devices).filter((d) => d.update_available).length : 0);
    onMount(() => {
        void load();
        void loadStorage();
        void loadWarnings();
        void loadMute();
        // task 177: kept while another page shows - no polls then, a refresh when it comes back
        const id = setInterval(() => life.active && load(), 10000);
        const storageId = setInterval(() => life.active && loadStorage(), 60000);
        const warnId = setInterval(() => {
            if (!life.active) return;
            void loadWarnings();
            void loadMute();
        }, 30000);
        const stopReturn = life.onReturn(() => {
            void load();
            void loadWarnings();
            void loadMute();
        });
        return () => {
            stopReturn();
            clearInterval(id);
            clearInterval(storageId);
            clearInterval(warnId);
        };
    });

    const uptime = $derived.by(() => {
        if (!status) return '';
        const s = status.uptime_s;
        const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
        return (d > 0 ? `${d} ${t('days')} ${h}h ${m}m` : `${h}h ${m}m`);
    });

    /*
     * The meters (D-21, and the `dataviz` form heuristic).
     *
     * Three of the page's numbers are genuinely proportional - a value against a capacity that is
     * fixed and known - and those three get a ring: the memory against the RAM, each filesystem
     * against its size, and the duty cycle against the 1 % of airtime the radio is allowed. The
     * others are not shares of anything: the load average has no ceiling to be a share of, the
     * uptime counts upwards forever, the addon and firmware counts are counts. Drawing a ring
     * around any of those would invent a denominator, so they stay as figures and text.
     */
    const mb = (kb: number) => Math.round(kb / 1024);
    const memUsedKb = $derived(status ? status.mem_total_kb - status.mem_available_kb : 0);
    const memPct = $derived(status && status.mem_total_kb > 0 ? Math.round((memUsedKb / status.mem_total_kb) * 100) : 0);
    const diskPct = (d: {total_kb: number; used_kb: number}) => (d.total_kb > 0 ? Math.round((d.used_kb / d.total_kb) * 100) : 0);

    // task 156: one duty cycle per physical radio. The box names the transmitter of every entry
    // (`radio`, from the radio plan): both stacks of a module shared through multimacd are one radio
    // and one panel, BidCos-RF and HmIP-RF on two modules are two, and a LAN gateway is its own.
    // Without the key (an older box) every entry is its own radio.
    //
    // A panel of two stacks shows the *highest* of them, never their sum (task 27.2): BidCos-RF and
    // HmIP-RF on one module are two reads of one transmitter, so adding them would report twice the
    // airtime in use; the larger is an understatement at worst. The per-stack figures stay as text.
    // The sampler keeps one row per interface per poll, so the tails line up sample for sample and
    // the per-sample maximum is the same figure over time that the ring shows now (history.ts).
    interface RadioGroup { key: string; name: string; entries: RadioIf[] }
    const radios = $derived.by<RadioGroup[]>(() => {
        const out: RadioGroup[] = [];
        for (const e of health?.interfaces ?? []) {
            const key = e.radio || `if:${e.interface}/${e.address}`;
            const g = out.find((x) => x.key === key);
            if (g) g.entries.push(e);
            else out.push({key, name: e.radio_name || `${e.interface} ${e.address}`, entries: [e]});
        }
        return out;
    });
    const historyOf = (g: RadioGroup, field: 'dc' | 'cs') => mergeMax(g.entries.map((e) => health?.history?.[`${e.interface}/${e.address}`] ?? []), field);
    // occulited task 80 (the maintainer): the radio by its module and serial only - no stack names
    const radioSub = (g: RadioGroup) => g.name;
    // the span the graphs show (task 152; its maximum follows it): occulited task 83 (the maintainer),
    // one span for every chart of the page, remembered in this browser - 24 h by default (task 81)
    const spanName = (m: number) => t('{n} h', {n: m / 60});
    const dutyPanels = $derived(radios.flatMap((g) => {
        const on = g.entries.filter((e) => e.duty_cycle >= 0);
        if (!on.length) return [];
        const value = Math.max(...on.map((e) => e.duty_cycle));
        const history = historyOf(g, 'dc');
        // occulited task 79 (the maintainer, corrected 2026-10-16): the warning's threshold as a line;
        // the band reaches 25 % always, during a transfer the current value + 25 points - the 75 %
        // line is drawn when it lies inside, said at the top edge when not
        const transfer = on.some((e) => e.transfer);
        const thr = Math.max(...on.map((e) => e.dc_threshold ?? 25));
        const floor = transfer ? value + 25 : 25;
        return [{g, value, history, floor, threshold: {value: thr, label: transfer ? t('{n} % · transfer', {n: thr}) : `${thr} %`, above: t('Threshold {n} %', {n: thr})}}];
    }));

    // task 150: carrier sense in a panel of its own, and only for a radio that reports it - how busy
    // the channel was heard, not a share of the duty cycle budget, so it never shares the DC ring.
    // Task 151: red above 10 % (history.ts CARRIER_BAD), and its history in the foot. Task 156: one
    // per radio, like the duty cycle.
    const carrierPanels = $derived(radios.flatMap((g) => {
        // occulited B-76 (the maintainer: "there is no carrier sense for bidcos"): an HmIP value - the
        // access point's CARRIER_SENSE_LEVEL - so the panel and its subtitle name HmIP-RF, not the
        // BidCos-RF stack that shares the module
        const cs = g.entries.filter((e) => e.carrier_sense !== undefined && !e.interface.startsWith('BidCos'));
        if (!cs.length) return [];
        const top = cs.reduce((a, b) => ((b.carrier_sense ?? 0) > (a.carrier_sense ?? 0) ? b : a));
        return [{
            g,
            value: top.carrier_sense ?? 0,
            fromDevice: top.carrier_sense_source === 'device',
            history: historyOf(g, 'cs'),
        }];
    }));

    /*
     * occulited task 13: the interface process cards under Components carry, beside up or down, the
     * radio as their subtitle (module, HB-RF board, the way to it - a link to the Interfaces page's
     * connections), the events per minute (task 17) with their history as the duty cycle's is
     * drawn, and the number of subscribers, split and named on hover or tap. One card per interface:
     * rfd's LAN gateways beside its module are lines of the same card, since the rates and the
     * subscribers are the process's.
     */
    interface IfCard { name: string; entries: RadioIf[] }
    const ifCards = $derived.by<IfCard[]>(() => {
        const out: IfCard[] = [];
        for (const e of health?.interfaces ?? []) {
            const c = out.find((x) => x.name === e.interface);
            if (c) c.entries.push(e);
            else out.push({name: e.interface, entries: [e]});
        }
        return out;
    });
    const pathText = (p?: string) => (p === 'multimacd' ? t('via multimacd') : p === 'direct' ? t('direct') : p === 'usb' ? 'USB' : p === 'lan' ? 'LAN' : '');
    const linkText = (e: RadioIf) => [e.module, e.adapter, pathText(e.path)].filter(Boolean).join(' · ');
    const upText = (c: IfCard) => {
        const up = c.entries.filter((e) => e.connected).length;
        return up === c.entries.length ? t('up') : up === 0 ? t('down') : t('{n} of {m} up', {n: up, m: c.entries.length});
    };
    const fmtRate = (v: number) => formatRate(v, i18n.language);
    // occulited task 37: the load and the memory over time; the load is read against the CPU count
    const loadHist = $derived(loadSeries(sysHist ?? undefined));
    const memHist = $derived(memSeries(sysHist ?? undefined));
    const memPeak = $derived(spanMax(memHist, view.span));
    const fmtLoad = (v: number) => formatLoad(v, i18n.language);
    // the graph's ticks: a whole number as it is (the CPU count, a top of 8), anything else as the figure
    const fmtLoadTick = (v: number) => (Number.isInteger(v) ? String(v) : fmtLoad(v));
    const rateNow = (name: string) => {
        const rs = health?.rates?.[name];
        return rs?.length ? rs[rs.length - 1]! : null;
    };
    const subsCount = (s: SubSummary) => (s.total === 1 ? t('1 subscriber') : t('{n} subscribers', {n: s.total}));
    const subsSplit = (s: SubSummary) => t('{x} external, {y} internal', {x: s.external, y: s.internal});
    // occulited B-45: a lite-rpc stream is named by its holder and marked "via occulited"; the streams
    // follow occulited's own registration in the list, as they come through it
    const streamName = (c: SubClient) => streamLabel(c, t);
    const clientName = (c: SubClient) => (c.stream ? streamName(c) : c.id);
    // occulited task 84: the client's name beside the id, as the Interfaces page shows it
    const clientText = (c: SubClient) => {
        const l = c.stream ? '' : clientLabel(c.id, c.own);
        return l ? `${c.id} · ${l}` : clientName(c);
    };
    // occulited task 84 (the maintainer): the process serving the interface after its name - rfd,
    // hmipserver, hs485d - from the radio stack's units (the interface list), none for another
    const procOf = (name: string) => unitOf(health?.units, name)?.unit ?? '';
    const clientKey = (c: SubClient) => (c.stream ? `stream:${c.stream.id}` : `${c.id} ${c.url}`);
    const clientList = (s: SubSummary): {c: SubClient; via: boolean}[] => {
        const streams = s.clients.filter((c) => c.stream);
        const out: {c: SubClient; via: boolean}[] = [];
        let placed = false;
        for (const c of s.clients) {
            if (c.stream) continue;
            out.push({c, via: false});
            if (c.own && !placed) {
                out.push(...streams.map((x) => ({c: x, via: true})));
                placed = true;
            }
        }
        if (!placed) out.push(...streams.map((x) => ({c: x, via: false})));
        return out;
    };
    const subsTitle = (s: SubSummary) => [subsSplit(s), ...clientList(s).map(({c, via}) => `${via ? '  ↳ ' : ''}${clientText(c)} - ${c.internal ? t('internal') : t('external')}`)].join('\n');
    // the subscriber lists (a tap on the phone, where there is no hover): occulited task 83 (the
    // maintainer), opened or closed in every component panel at once - view.subsOpen

    // task 53: the certificate's warning as a sentence, for the notice above the page; the card
    // below carries the same state as its coloured edge
    // the issuer as the Certificate page names it: common name and organisation, the DN the tooltip
    const issuerName = (c: CertInfo) => [c.issuer_cn, c.issuer_org !== c.issuer_cn ? c.issuer_org : ''].filter(Boolean).join(' · ') || c.issuer;
    const certWarn = $derived.by(() => {
        const c = cert?.current;
        if (!cert || !c) return '';
        if (cert.warning === 'last-attempt-failed') return t('The last certificate renewal failed; the current certificate stays in service.');
        if (c.days_left < 0) return t('The certificate has expired.');
        if (cert.warning === 'expiring' || c.days_left < 14) return t('The certificate expires in {n} days.', {n: c.days_left});
        return '';
    });

    // task 69: the storage panel's words. A device is named by its kind and model ("SD card SN64G"):
    // the kernel's mmcblk0 means nothing to most readers.
    function kindWord(d: {kind: string}): string {
        switch (d.kind) {
            case 'sd': return t('SD card');
            case 'emmc': return t('eMMC');
            case 'usb': return t('USB drive');
            case 'sata': return t('SATA drive');
            case 'nvme': return t('NVMe SSD');
            case 'virtio': return t('Virtual disk');
        }
        return t('Disk');
    }
    // a device by its kind and model; a warning carries the devices its reasons name (task 81)
    function deviceName(name: string, devices: WarnDevice[]): string {
        const d = devices.find((x) => x.name === name);
        return d ? `${kindWord(d)} ${d.model || d.name}` : name;
    }
    function reasonText(r: StorageReason, devices: WarnDevice[] = storage?.devices ?? []): string {
        const device = deviceName(r.device, devices);
        const n = r.count ?? 0;
        switch (r.code) {
            case 'smart-failed': return t("{device}: the drive's own health check (SMART) failed.", {device});
            case 'emmc-eol-urgent': return t('{device}: the eMMC has almost no reserve blocks left.', {device});
            case 'emmc-eol-warning': return t('{device}: the eMMC has used 80 % of its reserve blocks.', {device});
            case 'emmc-life': return t("{device}: {from}–{to} % of the eMMC's rated life is used.", {device, from: r.percent ?? 0, to: (r.percent ?? 0) + 10});
            case 'emmc-life-exceeded': return t('{device}: the eMMC is past its rated life.', {device});
            case 'wear': return t('{device}: {pct} % of the rated life is used.', {device, pct: r.percent ?? 0});
            case 'wear-exceeded': return t('{device}: the rated life is used up ({pct} %).', {device, pct: r.percent ?? 0});
            case 'io-errors': return t('{device}: {n} storage errors in the kernel log since this boot.', {device, n});
            case 'reallocated': return t('{device}: {n} sectors have been replaced by spares.', {device, n});
            case 'pending': return t('{device}: {n} sectors could not be read and wait to be replaced.', {device, n});
            case 'media-errors': return t('{device}: {n} media errors.', {device, n});
            case 'ext4-errors': return t('{device}: the file system on {fs} has recorded {n} errors.', {device, fs: r.filesystem ?? '', n});
            case 'sd-age-writes': return t('{device}: the card is {years} years old and receives {size} a day.', {device, years: r.years ?? 0, size: formatBytes(r.bytes_per_day ?? 0)});
        }
        return `${device}: ${r.code}`;
    }
    const summaryOf = (verdict: string) =>
        verdict === 'replace' ? t('A storage device should be replaced soon.') : verdict === 'watch' ? t('A storage device should be watched.') : t('The storage devices look healthy.');
    // task 111: the disk panels - every disk with a mounted file system, then a disk without one
    // whose health is not good (it would have nowhere else to say so); the storage warning's
    // #storage lands on the first panel whose disk is not good
    const diskPanels = $derived.by((): StatusDevice[] => {
        const mounted = status?.storage ?? [];
        if (!mounted.length) return [];
        const extra = (storage?.devices ?? [])
            .filter((h) => h.verdict !== 'good' && !mounted.some((d) => d.name === h.name))
            .map((h) => ({name: h.name, kind: h.kind, vendor: h.vendor, model: h.model, virtual: h.virtual, size_bytes: h.capacity_bytes, unallocated_bytes: 0, partitions: []}));
        return [...mounted, ...extra];
    });
    const storageAnchor = $derived(anchorDisk(diskPanels.map((d) => d.name), storage?.devices ?? []));
    // task 81: a warning's sentence (lib/warnings.ts). The storage warning says the verdict and every
    // reason behind it, naming the devices the warning carries; its button goes to the panel.
    function textOf(w: Warning): string {
        return warningText(w, {
            t,
            addonName: nameOf,
            storage: (x) => [summaryOf(x.params?.verdict ?? x.variant), ...(x.params?.reasons ?? []).map((r) => reasonText(r, x.params?.devices ?? []))].join(' '),
            when: (iso) => new Date(iso).toLocaleString(),
        });
    }
</script>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKey} />

<h1>{t('Status')}</h1>
<!-- task 177: holds the first fade-in until the warnings are in -->
{#if !warningsLoaded}<span data-loading hidden></span>{/if}
{#if !status || !firstDone}
    <Loading {error} />
{:else}
    <!-- Who this box is, and the four figures that are not shares of anything. -->
    <div class="ol-card ol-hero">
        <div class="who">
            <span class="ol-card-icon hero-icon"><Icon name="server" size={22} width={1.6} /></span>
            <div>
                <div class="host">{status.hostname}</div>
                <!-- occulited task 47: every round tags occulited and the OpenCCU-Base fork with the
                     openccu-lite version (D-121), so on openccu-lite that version alone names both -->
                <div class="ol-muted ver">
                    {#if status.version.lite}openccu-lite {status.version.lite}{:else}{#if status.occulited_version}<span title={status.occulited_version}>occulited {shortVersion(status.occulited_version)}</span>{' · '}{/if}{status.version.version || '–'}{/if} · {status.version.product}
                </div>
            </div>
        </div>
        <div class="figs">
            <div class="fig">
                <div class="fk"><Icon name="clock" size={13} />{t('Uptime')}</div>
                <div class="fv">{uptime}</div>
            </div>
            <div class="fig">
                <div class="fk"><Icon name="activity" size={13} />{t('Load average (1 / 5 / 15 min)')}</div>
                <div class="fv">{status.load.map((l) => l.toFixed(2)).join(' · ')}</div>
            </div>
            <div class="fig">
                <div class="fk"><Icon name="radio" size={13} />{t('Radio mode')}</div>
                <div class="fv">{status.hm_mode || '–'}</div>
            </div>
            <div class="fig">
                <div class="fk"><Icon name="globe" size={13} />{t('Timezone')}</div>
                <!-- the zone name, as the Network page shows it; the POSIX string the C library
                     reads is for whoever wants it, behind the pointer -->
                <div class="fv" title={status.tz || undefined}>{status.timezone || '–'}</div>
            </div>
        </div>
    </div>

    <!-- task 81: every warning directly under the box's identity, in the order occulited answers
         (errors first). Each says what is wrong and its button goes where it is handled (task 53);
         an administrator silences one for a period of their choice (D-64, D-65), and it shows again
         when that ends or when the warning clears and comes back. -->
    <div class="ol-warnings" data-warnings>
        <!-- openccu-lite task 219: programs asking for access, above the warnings, for an administrator -->
        {#if admin}<PairingRequests />{/if}
        {#each split.shown as w (warnKey(w))}
            {@const ln = warningLink(w, t)}
            <Notice kind={w.severity === 'info' ? 'plain' : w.severity === 'warning' ? 'warning' : 'error'} id={w.id} href={ln?.href ?? ''} label={ln?.label ?? ''}>
                {#snippet actions()}
                    {#if admin}
                        {#if w.id === 'addon-ownership'}
                            <button class="hmm-button" type="button" data-action="fix-ownership" disabled={warnBusy} onclick={() => fixOwnership(w)}>{t('Fix ownership')}</button>
                        {/if}
                        <span class="ol-silence">
                            <button class="hmm-button ol-silence-btn" type="button" data-action="silence" aria-haspopup="menu" aria-expanded={silenceOpen === warnKey(w)} aria-label={t('Silence…')} title={t('Silence…')} disabled={warnBusy} onclick={() => (silenceOpen = silenceOpen === warnKey(w) ? '' : warnKey(w))}><Icon name="bell-off" size={14} /></button>
                            {#if silenceOpen === warnKey(w)}
                                <div class="ol-menupop ol-silence-pop" role="menu" use:keepOnScreen>
                                    {#each periods as d (d)}
                                        <button type="button" role="menuitem" class="ol-menuitem" data-days={d} onclick={() => silence(w, d)}>{periodLabel(d, t)}</button>
                                    {/each}
                                    <div class="ol-silence-hint">{t('It shows again earlier if the warning clears and comes back.')}</div>
                                </div>
                            {/if}
                        </span>
                    {/if}
                {/snippet}
                {textOf(w)}
            </Notice>
        {/each}
        {#if mutedUntil}
            <!-- occulited task 62: the LED's warnings are muted (openccu-lite task 164) -->
            <Notice id="led-muted" href="/system/hardware" label={t('Hardware')}>
                {#snippet actions()}
                    {#if admin}<button class="hmm-button" type="button" data-action="unmute" disabled={muteBusy} onclick={unmute}>{t('Unmute')}</button>{/if}
                {/snippet}
                {t('The LED warnings are muted until {time}.', {time: mutedUntilText(mutedUntil, i18n.language)})}
                <Help>{t('Muted by the radio module\'s button or through the API, the LED shows the normal state; radio and network down still show. The mute ends by itself at that time.')}</Help>
            </Notice>
        {/if}
        {#if warnNotice}<div class="ol-notice" data-notice="warnings-result" role="status">{warnNotice}</div>{/if}
        {#if admin && split.silenced.length}
            <div class="ol-muted ol-silenced" data-silenced>{silencedLine(split.silenced.length, t)} · <button type="button" class="ol-textbutton" aria-expanded={showSilenced} onclick={() => (showSilenced = !showSilenced)}>{showSilenced ? t('Hide') : t('Show')}</button></div>
            {#if showSilenced}
                <!-- task 98: the list opens and closes in place -->
                <ul class="ol-silenced-list" in:reveal out:reveal={{duration: CLOSE_MS}}>
                    {#each split.silenced as w (warnKey(w))}
                        {@const s = w.silenced!}
                        <li data-silenced-warning={w.id}>
                            <div>{textOf(w)}</div>
                            <div class="ol-muted ol-small">{t('silenced on {date} by {user}, until {until}', {date: new Date(s.at).toLocaleDateString(), user: s.by, until: new Date(s.until).toLocaleDateString()})}</div>
                            <button class="hmm-button" type="button" data-action="unsilence" disabled={warnBusy} onclick={() => unsilence(w)}>{t('Unsilence')}</button>
                        </li>
                    {/each}
                </ul>
            {/if}
        {/if}
        {#if status.clock && !status.clock.synchronised}
            <!-- task 94: the boot's clock gate ran out - no RTC and no time server at boot. A quiet notice,
                 not a warning: the box works and keeps asking; it goes once chrony has synchronised.
                 openccu-lite task 299: a time server whose time lies outside the image's window is not
                 taken either, and a refused real-time clock is named -->
            <div class="ol-notice" data-notice="clock" data-clock-state={status.clock.state}>{t('The clock is not synchronised.')}
                {#if status.clock.state === 'ntp-implausible'}{' '}{t("The time server's time is outside what this image trusts (from its build to 15 years after) and was not taken. Check the time servers on the Network page.")}{/if}
                {#if status.clock.rtc_implausible}{' '}{t('The real-time clock said {time}, outside what this image trusts (from its build to 15 years after), and was not used.', {time: new Date(status.clock.rtc_implausible).toLocaleString()})}{/if}
                <Help>{t('The system started without a time from a real-time clock or a time server, and the radio stack did not wait any longer. It keeps asking its time servers; until one answers, times in the log and on devices may be wrong.')}</Help></div>
        {/if}
    </div>

    <!-- task 75: the service messages, the box's own without ReGa; a stream keeps them current -->
    <ServiceMessages />

    <h2>{t('Utilisation')}</h2>
    <div class="ol-cards ol-gauges" class:sp-rows={!!status.storage?.length && !view.storageOpen}>
        <Gauge
            label={t('Memory')}
            icon="memory"
            value={memPct / 100}
            display={`${memPct} %`}
            detail={`${mb(memUsedKb)} / ${mb(status.mem_total_kb)} MB`}
            level={levelOf(memPct, MEM_LEVELS.warn, MEM_LEVELS.err)}
            note={t('little memory left')}
            title={t('{pct} % of {total} MB in use', {pct: memPct, total: mb(status.mem_total_kb)})}
            peak={memPeak === null ? null : memPeak / 100}
            peakLevel={memPeak === null ? 'ok' : levelOf(memPeak, MEM_LEVELS.warn, MEM_LEVELS.err)}
            peakText={memPeak === null ? '' : t('max {pct} % ({span})', {pct: memPeak, span: spanName(view.span)})}>
            {#if memHist.length}<HistoryChart samples={memHist} label={t('memory history')} bind:span={() => view.span, setChartSpan} />{/if}
        </Gauge>
        {#if loadHist.length}
            {@const lp = spanMax(loadHist, view.span)}
            <!-- occulited task 37: the load has no ceiling, so no ring (see the meters above): the
                 figure, the CPU count it is read against, and the graph with that count as its mark -->
            <div class="ol-card ol-loadcard" data-load-card>
                <div class="ol-card-head">
                    <span class="ol-card-icon"><Icon name="activity" size={14} /></span>
                    <div class="ol-card-titles"><div class="ol-card-title">{t('Load')}</div></div>
                </div>
                <div class="load-row">
                    <span class="load-num" data-load-now>{fmtLoad(status.load[0] ?? 0)}</span>
                    <div class="load-body">
                        {#if status.cpus}<div class="load-det" data-load-cpus>{t('1-minute average, {n} CPUs', {n: status.cpus})}</div>{/if}
                        {#if lp !== null}<div class="ol-muted load-peak">{t('max {load} ({span})', {load: fmtLoad(lp), span: spanName(view.span)})}</div>{/if}
                    </div>
                </div>
                <div class="ol-card-foot"><HistoryChart samples={loadHist} label={t('load history')} unit="" steps={LOAD_STEPS} format={fmtLoadTick} mark={status.cpus || undefined} bind:span={() => view.span, setChartSpan} /></div>
            </div>
        {/if}
        <!-- occulited task 78: one panel per disk with a ring per partition; a daemon without
             `storage` (an older one, a container whose disks are the host's) keeps the mount gauges -->
        <!-- task 111 (the maintainer's variant B): each disk's health in its own panel - joined by
             the kernel's name; a disk with no mounted file system gets a panel only when its
             health says something -->
        {#each diskPanels as d (d.name)}
            {@const h = storage?.devices.find((x) => x.name === d.name)}
            <StoragePanel
                device={d}
                health={h}
                kernelLog={storage?.kernel_log ?? true}
                hostMonitored={!!storage && hostMonitored(storage)}
                anchor={d.name === storageAnchor}
                warnAttrs={warnFor(['storage'])}
                reasonText={(r) => reasonText(r)}
                deviceName={deviceName(d.name, storage?.devices ?? [])} />
        {/each}
        {#each status.storage?.length ? [] : status.disks as d (d.mount)}
            <Gauge
                label={t('Disk')}
                icon="disk"
                sub={d.mount}
                value={diskPct(d) / 100}
                display={`${diskPct(d)} %`}
                detail={`${mb(d.used_kb)} / ${mb(d.total_kb)} MB`}
                level={levelOf(diskPct(d), 80, 92)}
                note={diskPct(d) >= 92 ? t('nearly full') : t('filling up')}
                title={t('{pct} % of {total} MB in use', {pct: diskPct(d), total: mb(d.total_kb)})} />
        {/each}
        {#each dutyPanels as d (d.g.key)}
            {@const k = `dc:${d.g.key}`}
            {@const peak = spanMax(d.history, view.span)}
            <Gauge
                label={t('Duty cycle')}
                icon="radio"
                sub={radioSub(d.g)}
                value={d.value / 100}
                display={`${d.value} %`}
                detail={t('of the 1 % budget')}
                level={levelOf(d.value, DUTY_LEVELS.warn, DUTY_LEVELS.err)}
                note={t('close to the legal limit')}
                title={t('{pct} % of the permitted transmit time is used', {pct: d.value})}
                peak={peak === null ? null : peak / 100}
                peakLevel={peak === null ? 'ok' : levelOf(peak, DUTY_LEVELS.warn, DUTY_LEVELS.err)}
                peakText={peak === null ? '' : t('max {pct} % ({span})', {pct: peak, span: spanName(view.span)})}>
                <HistoryChart samples={d.history} label={t('duty cycle history')} floor={d.floor} threshold={d.threshold} bind:span={() => view.span, setChartSpan} />
            </Gauge>
        {/each}
        {#each carrierPanels as c (c.g.key)}
            {@const k = `cs:${c.g.key}`}
            {@const peak = spanMax(c.history, view.span)}
            <Gauge
                label={t('Carrier sense')}
                icon="radio"
                sub={radioSub(c.g)}
                value={c.value / 100}
                display={`${c.value} %`}
                detail={t('of the time the channel was heard busy')}
                detailTitle={c.fromDevice ? t("Read from channel 0 of the radio module's own device.") : t('Reported by the interface process with its radio interfaces.')}
                level={carrierLevel(c.value)}
                note={t('the channel is busy: interference or a neighbour\'s traffic')}
                title={t('The radio heard the channel busy {pct} % of the time', {pct: c.value})}
                peak={peak === null ? null : peak / 100}
                peakLevel={peak === null ? 'ok' : carrierLevel(peak)}
                peakText={peak === null ? '' : t('max {pct} % ({span})', {pct: peak, span: spanName(view.span)})}>
                <HistoryChart samples={c.history} label={t('carrier sense history')} bind:span={() => view.span, setChartSpan} mark={CARRIER_BAD} />
            </Gauge>
        {/each}
    </div>

    <!-- task 53: every card is title, then value, then detail - the serial or SGTIN under the
         interface's name, never beside it, so a 24-character code cannot wrap through the name.
         The cards that lead somewhere are links as a whole. -->
    <!-- occulited task 84: "BidCos-RF · rfd" - the process in muted text on the title's line, cut, never wrapped -->
    {#snippet ifTitle(name: string, proc = procOf(name))}
        <div class="ol-card-title ol-if-title" title={proc ? `${name} · ${proc}` : undefined}>{name}{#if proc}<span class="ol-if-proc" data-process-name>{' · '}{proc}</span>{/if}</div>
    {/snippet}
    <h2>{t('Components')}</h2>
    <div class="ol-cards ol-components ol-if-cards">
        {#if health}
            <!-- task 94: an interface process systemd is still starting - hmipserver's JVM for about
                 40 s after the web UI is up at boot - is starting, with its seconds, never "not answering" -->
            {#snippet startingCard(name: string, u: InterfaceUnit)}
                {@const secs = startingSeconds(u, healthAt, now)}
                <div class="ol-card" data-component={name} data-state="starting">
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles">{@render ifTitle(name, u.unit)}</div>
                    </div>
                    <div class="ol-card-body" title={startingTitle(u)}><span class="ol-dot starting"></span>{secs === undefined ? t('starting') : t('starting · {span}', {span: span(secs)})}</div>
                </div>
            {/snippet}
            <!-- occulited task 13: the process's events per minute (task 17) with their history, and its
                 subscribers - the same for a card with a radio list and one without -->
            {#snippet traffic(name: string)}
                {@const cur = rateNow(name)}
                {@const subs = health?.subscribers?.[name]}
                {#if cur || subs}
                    <div class="ol-if-figs">
                        {#if cur}
                            <span class="ol-if-rate" data-rate="in" title={t('Events per minute the interface process delivered: a device sends the datapoints of a channel in one telegram, which arrives as one event however many values it carries.')}><span class="ol-if-k">{t('Events')}</span> {cur.up ? fmtRate(cur.in) : '–'}/min</span>
                        {/if}
                        {#if subs}
                            <button type="button" class="ol-if-subs" data-subscribers={subs.total} title={subsTitle(subs)} aria-expanded={view.subsOpen} onclick={() => setSubsOpen(!view.subsOpen)}>{subsCount(subs)}</button>
                        {/if}
                    </div>
                    {#if subs && view.subsOpen}
                        <div class="ol-if-clients">
                            <div class="ol-muted">{subsSplit(subs)}</div>
                            {#if subs.clients.length}
                                <ul>
                                    {#each clientList(subs) as {c, via} (clientKey(c))}
                                        <li class:via data-stream={c.stream ? c.stream.id : undefined}><SubscriberLabel id={clientName(c)} label={c.stream ? '' : clientLabel(c.id, c.own)} internal={c.internal} own={c.own} title={c.stream ? [clientName(c), streamDetail(c)].filter(Boolean).join(' · ') : [clientText(c), c.url].filter(Boolean).join(' · ')} /></li>
                                    {/each}
                                </ul>
                            {/if}
                        </div>
                    {/if}
                    {#if health?.rates?.[name]?.length}
                        {@const k = `ev:${name}`}
                        <div class="ol-card-foot">
                            <HistoryChart samples={rateSeries(health.rates[name])} label={t('event rate history')} unit="/min" steps={RATE_STEPS} format={fmtRate} outlierFloor={RATE_OUTLIER_FLOOR} bind:span={() => view.span, setChartSpan} />
                        </div>
                    {/if}
                {/if}
            {/snippet}
            {#each ifCards as c (c.name)}
                {@const links = c.entries.map(linkText).filter(Boolean)}
                <div class="ol-card" data-component={c.name}>
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles">
                            {@render ifTitle(c.name)}
                            <!-- occulited task 13: the radio as the subtitle, the address without a plan (an older system) -->
                            {#if links.length}
                                <div class="ol-card-sub ol-if-module">{#each links as l, i (i)}<a href="/system/interfaces#connections" use:link>{l}</a>{/each}</div>
                            {:else}
                                <div class="ol-card-sub">{c.entries.map((e) => e.address).join(' · ')}</div>
                            {/if}
                        </div>
                    </div>
                    <div class="ol-card-body"><span class="ol-dot" class:ok={c.entries.some((e) => e.connected)} class:warn={c.entries.some((e) => !e.connected) && c.entries.some((e) => e.connected)}></span>{upText(c)}</div>
                    {#if links.length}<div class="ol-card-detail hmm-mono">{c.entries.map((e) => e.address).join(' · ')}</div>{/if}
                    {@render traffic(c.name)}
                </div>
            {/each}
            <!-- an interface process without a radio list (hs485d, CUxD): it answered, so it is up -->
            {#each health.answering ?? [] as name (name)}
                <div class="ol-card" data-component={name}>
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles">{@render ifTitle(name)}</div>
                    </div>
                    <div class="ol-card-body"><span class="ol-dot ok"></span>{t('up')}</div>
                    {@render traffic(name)}
                </div>
            {/each}
            {#each Object.entries(health.errors) as [name, err] (name)}
                {@const u = unitOf(health.units, name)}
                {#if u?.starting}
                    {@render startingCard(name, u)}
                {:else}
                    <div class="ol-card err" data-component={name}>
                        <div class="ol-card-head">
                            <span class="ol-card-icon"><Icon name="alert" size={14} /></span>
                            <div class="ol-card-titles">{@render ifTitle(name)}</div>
                        </div>
                        <div class="ol-card-body"><span class="ol-dot err"></span>{t('not answering')}</div>
                        <div class="ol-card-detail">{err}</div>
                    </div>
                {/if}
            {/each}
            {#each unseenStarting as c (c.name)}
                {@render startingCard(c.name, c.unit)}
            {/each}
        {/if}
        {#if conn?.available && conn.plan}
            {@const cp = conn.plan}
            {#if conn.choices.bidcos === 'none' && !cp.rfd.run}
                <a class="ol-card" href="/system/interfaces#connections" use:link data-component="BidCos-RF" data-state="off">
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles">{@render ifTitle('BidCos-RF', 'rfd')}</div>
                    </div>
                    <!-- openccu-lite task 322: information, not a warning - HmIP only is a mode, not a fault -->
                    <div class="ol-card-body"><span class="ol-dot"></span>{t('off (HmIP only)')}</div>
                </a>
            {/if}
            {#each [['HmIP-RF', cp.missing_hmip], ['BidCos-RF', cp.missing_bidcos]] as [name, id] (name)}
                {#if id}
                    <a class="ol-card err" href="/system/interfaces#connections" use:link data-component={`${name}-module`}>
                        <div class="ol-card-head">
                            <span class="ol-card-icon"><Icon name="alert" size={14} /></span>
                            <div class="ol-card-titles">{@render ifTitle(name ?? '')}</div>
                        </div>
                        <!-- openccu-lite task 318: on Automatic, the module holding the HmIP network -->
                        <div class="ol-card-body"><span class="ol-dot err"></span>{name === 'HmIP-RF' && cp.hmip_pin && cp.hmip_pin === id ? t('module missing') : t('chosen module missing')}</div>
                        <div class="ol-card-detail hmm-mono">{id}</div>
                    </a>
                {/if}
            {/each}
        {/if}
    </div>
    <!-- occulited task 13: the interface process cards carry a history and are taller than the
         rest, so they have a grid of their own and the short cards keep their height -->
    <div class="ol-cards ol-components">
        {#if fw}
            <a class="ol-card" href="/system/updates" use:link data-component="firmware">
                <div class="ol-card-head">
                    <span class="ol-card-icon"><Icon name="firmware" size={14} /></span>
                    <div class="ol-card-titles"><div class="ol-card-title">{t('Device firmware')}</div></div>
                </div>
                <div class="ol-card-body">{fwUpdates === 1 ? t('1 update available') : fwUpdates ? t('{n} updates available', {n: fwUpdates}) : t('all current')}</div>
                {#if !fw.enabled}<div class="ol-card-detail">{t('(automatic download off)')}</div>{/if}
            </a>
        {/if}
        {#if cert?.current}
            {@const c = cert.current}
            <a class="ol-card ol-cert" class:warn={!!certWarn} href="/system/certificates" use:link data-component="certificate">
                <div class="ol-card-head">
                    <span class="ol-card-icon"><Icon name="lock" size={14} /></span>
                    <div class="ol-card-titles"><div class="ol-card-title">{t('Certificate')}</div></div>
                </div>
                <div class="ol-card-body">{c.self_signed ? t('self-signed') : cert.settings.mode === 'acme' ? 'ACME' : t('from a CA')}{#if c.days_left < 14} <span class="ol-badge bad">{c.days_left < 0 ? t('expired') : (c.days_left === 1 ? t('1 day left') : t('{n} days left', {n: c.days_left}))}</span>{/if}</div>
                <div class="ol-card-detail">{t('until {date}', {date: new Date(c.not_after).toLocaleDateString()})}{#if https?.redirect_https} · {t('redirect on')}{/if}{#if https?.hsts} · {t('HSTS on')}{:else if https?.hsts_clearing} · {t('HSTS off (max-age=0)')}{/if}</div>
                {#if !c.self_signed}<div class="ol-muted ol-clamp" title={c.issuer}>{issuerName(c)}</div>{/if}
                {#if cert.warning === 'last-attempt-failed'}<div class="ol-warn small">{t('The last renewal failed.')}</div>{/if}
            </a>
        {/if}
        {#if addonCount !== null}
            <a class="ol-card" href="/addons" use:link data-component="addons">
                <div class="ol-card-head">
                    <span class="ol-card-icon"><Icon name="package" size={14} /></span>
                    <div class="ol-card-titles"><div class="ol-card-title">{t('Addons')}</div></div>
                    {#if addonUpdates.length}<span class="ol-badge good head-badge" title={addonUpdates.join(', ')}>{(addonUpdates.length === 1 ? t('1 update') : t('{n} updates', {n: addonUpdates.length}))}</span>{/if}
                </div>
                <div class="ol-card-body">{addonCount ? t('{n} installed', {n: addonCount}) : t('none — install one from the catalogue')}</div>
            </a>
        {/if}
    </div>

    <!-- task 111: the old storage health panel is gone - its disks are the usage panels above. What
         it held beyond them stays here: the files beside the journal (task 84), and for a daemon
         without disk panels (a container) the host line and the reasons. -->
    {#if storage && (storage.log_files?.length || !diskPanels.length)}
        {@const bare = !diskPanels.length}
        <div class="ol-card ol-storage-extra" id={bare ? 'storage' : undefined} data-storage-extra {...bare ? warnFor(['storage']) : {}}>
            {#if bare && storage.reasons.length}
                <ul class="ol-storage-reasons">
                    {#each storage.reasons as r, i (i)}<li>{reasonText(r)}</li>{/each}
                </ul>
            {/if}
            {#if bare && hostMonitored(storage)}
                <div class="ol-muted ol-small" data-storage-host>{t('Disk health is monitored by the host')}</div>
            {/if}
            {#if storage.log_files?.length}
                <!-- task 84: the files written beside the journal (D-59) - a hint, no part of the verdict -->
                <div class="ol-storage-logs" data-storage-logs>
                    <div class="ol-storage-line">
                        <strong>{t('Log files beside the journal')}</strong><Help>{t("The journal is the one log on this system. These files are written next to it: on the userfs they wear the card, under /var and /run they take RAM. An addon's files are its own project's to change; the list is a hint and takes no part in the verdict.")}</Help>
                    </div>
                    <ul class="ol-storage-logfiles">
                        {#each storage.log_files as f (f.path)}
                            <li data-logfile={f.path}>
                                <span class="hmm-mono">{f.path}</span>{' · '}{formatBytes(f.size)}{#if f.addon}{' · '}{t('addon {id}', {id: f.addon})}{/if}{' · '}<span class="ol-muted">{f.userfs ? t('on the userfs') : t('in RAM')}</span>
                                {#if f.growing}<span class="ol-badge warn" data-growing>{t('growing, +{size}', {size: formatBytes(f.grown_bytes ?? 0)})}</span>{/if}
                            </li>
                        {/each}
                    </ul>
                    {#if storage.log_files_more}<div class="ol-muted ol-small">{t('and {n} more log files', {n: storage.log_files_more})}</div>{/if}
                </div>
            {/if}
        </div>
    {/if}
{/if}

<style>
    /* The identity strip. One card, the hostname reading as the heading it is, and beside it the
       figures that have no ceiling and therefore no ring: uptime, load, radio mode, timezone. */
    .ol-hero {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 12px 28px;
        margin-bottom: 14px;
    }
    .ol-hero .who { display: flex; align-items: center; gap: 12px; min-width: 0; }
    .ol-hero .hero-icon { width: 40px; height: 40px; }
    .ol-hero .host { font-size: 20px; font-weight: 600; color: var(--hmm-fg); line-height: 1.15; overflow-wrap: anywhere; }
    .ol-hero .ver { font-size: var(--hmm-font-size-small); }
    .ol-hero .figs { display: flex; flex-wrap: wrap; gap: 10px 28px; margin-left: auto; }
    .ol-hero .fk { display: flex; align-items: center; gap: 5px; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); }
    .ol-hero .fv { font-size: 15px; margin-top: 1px; }
    /* occulited task 37: the load's tile among the gauges - a figure where they have a ring */
    .ol-loadcard { display: flex; flex-direction: column; gap: 10px; }
    .ol-loadcard .load-row { display: flex; align-items: center; gap: 14px; min-height: 88px; }
    .ol-loadcard .load-num { font-size: 26px; font-weight: 600; color: var(--hmm-fg); min-width: 88px; text-align: center; letter-spacing: -0.01em; }
    .ol-loadcard .load-body { min-width: 0; }
    .ol-loadcard .load-det { font-size: 15px; overflow-wrap: anywhere; }
    .ol-loadcard .load-peak { font-size: var(--hmm-font-size-small); margin-top: 2px; }

    /* task 81: the warnings under the identity card. The Silence button is a quiet square beside the
       notice's link with its menu under it; the silenced ones wait behind a muted line. */
    .ol-silence { position: relative; display: inline-flex; }
    .ol-silence-btn { display: inline-flex; align-items: center; justify-content: center; padding: 2px 7px; color: var(--hmm-fg-muted); }
    .ol-silence-btn:hover { color: var(--hmm-fg); }
    .ol-silence-pop { left: auto; right: 0; min-width: 210px; max-width: min(92vw, 300px); }
    .ol-silence-hint { padding: 4px 8px 2px; color: var(--hmm-fg-muted); font-size: var(--hmm-font-size-small); white-space: normal; }
    .ol-silenced { margin: -4px 0 10px; font-size: var(--hmm-font-size-small); }
    .ol-textbutton { border: 0; padding: 0; background: none; color: var(--hmm-accent); font: inherit; cursor: pointer; text-decoration: underline; }
    .ol-silenced-list { list-style: none; margin: 0 0 10px; padding: 0; display: flex; flex-direction: column; gap: 8px; }
    .ol-silenced-list li { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; min-width: 0; padding: 8px 10px; border: 1px dashed var(--hmm-border); border-radius: var(--hmm-radius); overflow-wrap: anywhere; }

    /* the component cards share the gauges' track (app.css, occulited task 54); the tiles of a row
       are stretched to the tallest */
    .head-badge { margin-left: auto; }
    /* occulited task 13: the interface process cards - the radio line under the name links to the
       connections, the figures wrap on a phone */
    .ol-if-cards:not(:empty) { margin-bottom: 12px; }
    .ol-if-module { display: flex; flex-direction: column; gap: 2px; }
    .ol-if-module a { color: var(--hmm-link); text-decoration: none; }
    .ol-if-module a:hover { text-decoration: underline; }
    .ol-if-figs { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; margin-top: 8px; font-size: var(--hmm-font-size-small); color: var(--hmm-fg-muted); }
    .ol-if-k { color: var(--hmm-fg); }
    .ol-if-rate { display: inline-flex; align-items: center; gap: 5px; font-variant-numeric: tabular-nums; }
    .ol-if-subs { margin-left: auto; border: 0; padding: 1px 6px; border-radius: var(--hmm-radius); background: none; color: var(--hmm-link); font: inherit; cursor: pointer; }
    .ol-if-subs:hover, .ol-if-subs[aria-expanded='true'] { background: var(--hmm-accent-bg); }
    .ol-if-clients { margin-top: 6px; font-size: var(--hmm-font-size-small); }
    .ol-if-clients ul { list-style: none; margin: 4px 0 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
    .ol-if-clients li { display: flex; min-width: 0; }
    .ol-if-title { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; overflow-wrap: normal; }
    .ol-if-proc { font-weight: 400; color: var(--hmm-fg-muted); }
    /* occulited B-45: the lite-rpc streams under occulited's own registration */
    .ol-if-clients li.via { padding-left: 14px; }
    .ol-if-clients li.via::before { content: '↳'; margin-left: -14px; width: 14px; display: inline-block; color: var(--hmm-fg-muted); }
    .ol-cert .small { font-size: var(--hmm-font-size-small); margin-top: 4px; }
    .ol-cert .ol-clamp { margin-top: 4px; }

    /* task 111: what is left of the storage health panel - the files beside the journal, and for
       a daemon without disk panels the host line and the reasons. The anchor leaves room above
       the card when the warning's button jumps to it. */
    .ol-storage-extra { margin-bottom: 14px; scroll-margin-top: 72px; }
    /* task 84: the files beside the journal - long paths wrap anywhere, so a phone never scrolls sideways */
    .ol-storage-logfiles { margin: 4px 0 0; padding-left: 18px; }
    .ol-storage-logfiles li { overflow-wrap: anywhere; margin: 2px 0; }
    .ol-storage-logfiles .ol-badge { margin-left: 6px; }
    .ol-storage-reasons { margin: 0 0 6px; padding-left: 20px; }
    .ol-storage-reasons li { overflow-wrap: anywhere; }
    .ol-storage-line { overflow-wrap: anywhere; }
    .ol-small { font-size: var(--hmm-font-size-small); }
    /* task 111: every row of the Utilisation grid as high as its tallest gauge, so a closed disk
       panel (which adds no height of its own) is as high as the gauges beside it - on a phone,
       where each row holds one panel, too. With the disks' Details open the rows size freely. */
    .ol-gauges.sp-rows { grid-auto-rows: 1fr; }
</style>
