<script lang="ts">
    import {onMount} from 'svelte';
    import {LiveClock, REANCHOR_MS, SecondTicker, clockLocale} from '../lib/liveclock';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api} from '../lib/api';
    import {ask} from '../lib/dialog.svelte';
    import {i18n, t} from '../lib/i18n.svelte';
    import {auth} from '../lib/auth.svelte';
    import {ticketFor} from '../lib/download';
    import {certificateText, renameText, type AcmeNames, type CertNames, type Rename} from '../lib/rename';
    import Loading from '../lib/Loading.svelte';
    import Help from '../lib/Help.svelte';
    import Icon from '../lib/Icon.svelte';
    import {formatBytes, formatRate, groupIPv6, interfaceColumns, showsAddressPanels, ipv6GatewayOf, linkLine, linkState, orderInterfaces, quietAddress, sampleOf, shownFlags, trafficRates, type CounterSample, type IPv6State, type LinkState, type NetIface, type TrafficRate} from '../lib/netpanels';
    import {link} from '../lib/router.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import WiFiPanel from '../lib/WiFiPanel.svelte';
    import {warnEdge, warnFor} from '../lib/systemmenu.svelte';

    // task 57: the firewall is its own page, pages/FirewallPage.svelte (/system/firewall)
    interface Network { hostname: string; domain?: string; mode: string; address?: string; netmask?: string; gateway?: string; dns: string[]; ipv6?: string; interfaces: NetIface[]; ipv6_state?: IPv6State }
    interface Settings { hostname: string; mode: 'dhcp' | 'static'; address?: string; netmask?: string; gateway?: string; dns: string[] }
    interface Pending { token: string; settings: Settings; previous: Settings; started: string; deadline: string }
    interface NetResponse { network: Network; settings: Settings; current: Settings; pending: Pending | null; writable: boolean; host_managed: boolean }
    interface TimeConfig { tz: string; zone: string; ntp_servers: string[]; has_ntp: boolean; now: string }
    interface Zone { name: string; country: string; code: string; comment?: string }

    let net = $state<NetResponse | null>(null);
    let time = $state<TimeConfig | null>(null);
    let wifiChips = $state(0);
    // openccu-lite task 227: IPv6 per interface - off, SLAAC, DHCPv6, static - applied live and
    // kept only when confirmed within the window; otherwise the system rolls back by itself
    interface V6Settings { mode: 'off' | 'slaac' | 'dhcpv6' | 'static'; address?: string; prefix?: number; gateway?: string; dns?: string[] }
    interface V6Pending { token: string; interface: string; settings: V6Settings; previous: V6Settings; deadline: string }
    interface V6View { interfaces: Record<string, V6Settings>; pending: V6Pending | null; window_seconds: number; writable: boolean; client_interface?: string }
    interface V6Form { mode: V6Settings['mode']; address: string; prefix: string; gateway: string; dns: string }
    let v6view = $state<V6View | null>(null);
    let v6forms = $state<Record<string, V6Form>>({});
    let v6left = $state(0);
    let v6tick: ReturnType<typeof setInterval> | null = null;
    const v6pending = $derived(v6view?.pending ?? null);
    function v6form(s: V6Settings): V6Form {
        return {mode: s.mode, address: s.address ?? '', prefix: s.prefix ? String(s.prefix) : '64', gateway: s.gateway ?? '', dns: (s.dns ?? []).join(', ')};
    }
    function v6body(name: string, f: V6Form) {
        const b: Record<string, unknown> = {interface: name, mode: f.mode};
        if (f.mode === 'static') Object.assign(b, {address: f.address.trim(), prefix: Number(f.prefix) || 64, gateway: f.gateway.trim(), dns: f.dns.split(/[\s,]+/).filter(Boolean)});
        return b;
    }
    function v6changed(name: string): boolean {
        const f = v6forms[name];
        const cur = v6view?.interfaces[name];
        if (!f || !cur) return false;
        return JSON.stringify(v6form(cur)) !== JSON.stringify(f);
    }
    function takeV6(v: V6View, keepForms = false) {
        v6view = v;
        const forms: Record<string, V6Form> = {};
        for (const [n, cfg] of Object.entries(v.interfaces)) forms[n] = keepForms && v6forms[n] ? v6forms[n] : v6form(cfg);
        v6forms = forms;
        if (v6tick) { clearInterval(v6tick); v6tick = null; }
        const p = v.pending;
        if (!p) { v6left = 0; return; }
        const update = () => {
            v6left = Math.max(0, Math.round((new Date(p.deadline).getTime() - Date.now()) / 1000));
            if (v6left === 0 && v6tick) {
                clearInterval(v6tick); v6tick = null;
                // occulited B-85: not kept in time - the system went back by itself; said, not only shown
                notice = t('IPv6 on {iface} is back to {mode}: the change was not kept in time.', {iface: p.interface, mode: v6ModeWord(p.previous.mode)});
                setTimeout(() => void loadV6(), 1500);
            }
        };
        // occulited B-85: an answer that arrives after its window (the change cut the connection it
        // was sent over) is the rollback already: the same as a countdown that ran out
        v6tick = setInterval(update, 500);
        update();
    }
    async function loadV6() {
        try { takeV6(await api.get<V6View>('/api/system/v1/network/ipv6')); } catch { v6view = null; }
    }
    /** occulited B-85: the browser reaches the system over IPv6 on this interface - a change cuts it off */
    const v6Cuts = (name: string) => v6view?.client_interface === name;
    /** this page at the interface's IPv4 address, where a change of its IPv6 does not cut the browser off */
    function v4Link(name: string): string {
        const a = net?.network.interfaces.find((x) => x.name === name)?.ipv4?.[0]?.address;
        return a ? `${location.protocol}//${a}${location.pathname}` : '';
    }
    const v6ModeWord = (m: string) => ({off: t('off'), slaac: 'SLAAC', dhcpv6: 'DHCPv6', static: t('static')} as Record<string, string>)[m] ?? m;
    async function applyV6(name: string) {
        const f = v6forms[name];
        if (!f || !v6view) return;
        const prev = v6ModeWord(v6view.interfaces[name]?.mode ?? 'slaac');
        let message = t('IPv6 on {iface} becomes {mode} now. Keep it within {s} seconds, or the system goes back to {prev} by itself - so a setting that cuts this browser off undoes itself. IPv4 is not touched.', {iface: name, mode: v6ModeWord(f.mode), s: v6view.window_seconds, prev});
        // occulited B-85: this browser is connected over IPv6 on that interface - the change cuts it off,
        // Keep cannot arrive, and the system goes back after the window
        if (v6Cuts(name)) {
            const link = v4Link(name);
            message += '\n\n' + t('This browser is connected to the system over IPv6 on {iface}. The change interrupts that connection: Keep cannot arrive, and after {s} seconds the system goes back to {prev}.', {iface: name, s: v6view.window_seconds, prev}) + (link ? ' ' + t('Open this page over IPv4 for the change: {link}', {link}) : '');
        }
        const ok = await ask({
            title: t('Change IPv6 on {iface}', {iface: name}),
            message,
            confirm: t('Apply'),
            danger: v6Cuts(name),
        });
        if (!ok) return;
        busy = 'v6';
        error = '';
        try {
            takeV6(await api.post<V6View & {changed: boolean}>('/api/system/v1/network/ipv6', v6body(name, f)), true);
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }
    async function finishV6(keep: boolean) {
        const p = v6view?.pending;
        if (!p) return;
        busy = 'v6';
        try {
            takeV6(await api.post<V6View>(`/api/system/v1/network/ipv6/${keep ? 'confirm' : 'revert'}`, {token: p.token}));
            notice = keep ? t('IPv6 on {iface} is kept.', {iface: p.interface}) : t('IPv6 on {iface} is back to {mode}.', {iface: p.interface, mode: v6ModeWord(p.previous.mode)});
            void pollInterfaces();
        } catch (e) {
            error = (e as Error).message;
            void loadV6();
        } finally {
            busy = '';
        }
    }

    // task 226: Wi-Fi switched off - its interface shows no address panels
    let wifiOff = $state(false);
    let zones = $state<Zone[]>([]);
    let error = $state('');
    let notice = $state('');
    let busy = $state('');

    // the network form
    let form = $state<Settings>({hostname: '', mode: 'dhcp', dns: []});
    // openccu-lite task 62: the last rename's outcome - the lease and the certificate reminder
    // (the texts in lib/rename.ts, shared with the welcome page's rename, task 327)
    let rename = $state<(Rename & {certificate: CertNames | null; acme: AcmeNames | null}) | null>(null);
    const certNotice = $derived(rename ? certificateText(rename, rename.certificate, rename.acme) : '');
    let dnsText = $state('');
    let pending = $state<Pending | null>(null);
    let secondsLeft = $state(0);
    const life = pageLife();
    let tick: ReturnType<typeof setInterval> | null = null;


    // task 225: the Time panel's clocks tick - the system's from its last reading, the browser's
    // own; the time is read again every minute and after every change, so a step shows
    const sysClock = new LiveClock();
    let clockTick = $state(0);
    const ticker = new SecondTicker(() => clockTick++);
    function takeTime(tm: TimeConfig) {
        time = tm;
        sysClock.anchor(tm.now);
        clockTick++;
    }
    async function reanchor() {
        try { takeTime({...(await api.get<TimeConfig>('/api/system/v1/time'))}); } catch { /* the next round tries again */ }
    }
    const clockLang = $derived(clockLocale(i18n.language, typeof navigator === 'undefined' ? [] : navigator.languages ?? [navigator.language]));
    const systemTimeText = $derived.by(() => { void clockTick; return sysClock.anchored ? new Date(sysClock.now()).toLocaleString(clockLang) : ''; });
    const browserTimeText = $derived.by(() => { void clockTick; return new Date().toLocaleString(clockLang); });
    // the ticker runs while the page is shown and the tab visible; a return re-reads the time
    let tabVisible = $state(typeof document === 'undefined' || document.visibilityState !== 'hidden');
    const clocksLive = $derived(tabVisible && life.active && !!time);
    let reread: ReturnType<typeof setInterval> | null = null;
    $effect(() => {
        const on = clocksLive;
        ticker.run(on);
        if (reread) { clearInterval(reread); reread = null; }
        if (on) reread = setInterval(() => void reanchor(), REANCHOR_MS);
    });

    // time form
    let zone = $state('');
    let ntpText = $state('');
    let clockText = $state('');

    async function load() {
        try {
            const [n, tm] = await Promise.all([
                api.get<NetResponse>('/api/system/v1/network'),
                api.get<TimeConfig>('/api/system/v1/time'),
            ]);
            net = n; takeTime(tm);
            noteCounters(n.network.interfaces);
            // task 89: the Wi-Fi panel shows where there is a chip, also while it is switched off
            try {
                const w = await api.get<{chips: unknown[]; settings?: {enabled?: boolean}}>('/api/system/v1/wifi');
                wifiChips = w.chips.length;
                wifiOff = w.settings?.enabled === false;
            } catch { wifiChips = 0; wifiOff = false; }
            form = {...n.settings, dns: [...n.settings.dns]};
            dnsText = n.settings.dns.join(', ');
            setPending(n.pending);
            if (n.pending && !confirmTicket) void sessionTicket();
            zone = tm.zone || tm.tz; ntpText = tm.ntp_servers.join(' ');
            void loadV6();
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }

    function setPending(p: Pending | null) {
        pending = p;
        if (tick) { clearInterval(tick); tick = null; }
        if (!p) { secondsLeft = 0; return; }
        const update = () => {
            secondsLeft = Math.max(0, Math.round((new Date(p.deadline).getTime() - Date.now()) / 1000));
            if (secondsLeft === 0) { setPending(null); void load(); }
        };
        update();
        tick = setInterval(update, 500);
    }

    // The interface panels are re-read while the page is open - cheap sysfs reads on the box - so a
    // pulled cable or a changed speed shows here. Only the live view is replaced: the forms below
    // keep what the user is typing.
    async function pollInterfaces() {
        if (!net) return;
        try {
            const n = await api.get<NetResponse>('/api/system/v1/network');
            if (net) net = {...net, network: n.network, host_managed: n.host_managed};
            noteCounters(n.network.interfaces);
        } catch {
            /* the next poll tries again */
        }
    }

    // The current rate per interface: the difference of the byte counters between two answers,
    // over the time between them. Two answers less than a second apart (a reload after a save
    // right after a poll) are too close to say anything; the older sample is kept for the next.
    let lastCounters: CounterSample | null = null;
    let rates = $state<Record<string, TrafficRate>>({});
    function noteCounters(list: NetIface[]) {
        const sample = sampleOf(list, performance.now());
        if (lastCounters && sample.at - lastCounters.at < 1000) return;
        if (lastCounters) rates = trafficRates(lastCounters, sample);
        lastCounters = sample;
    }

    onMount(() => {
        void load();
        // arriving on the new address with ?confirm=<token>: finish the transaction right away
        const params = new URLSearchParams(location.search);
        const token = params.get('confirm');
        if (token) void confirm(token);
        // task 177: no polls while another page shows, a refresh when it comes back
        const poll = setInterval(() => life.active && pollInterfaces(), 5000);
        const stopReturn = life.onReturn(() => { void pollInterfaces(); void reanchor(); });
        // task 225: no clock ticks in a hidden tab; the time read again on return
        const onVisibility = () => {
            tabVisible = document.visibilityState !== 'hidden';
            if (tabVisible && life.active) void reanchor();
        };
        document.addEventListener('visibilitychange', onVisibility);
        return () => {
            stopReturn();
            clearInterval(poll);
            if (tick) clearInterval(tick);
            document.removeEventListener('visibilitychange', onVisibility);
            ticker.stop();
            if (reread) clearInterval(reread);
            if (v6tick) clearInterval(v6tick);
        };
    });

    const ifaces = $derived(orderInterfaces(net?.network.interfaces ?? []));
    // task 89 (the maintainer, 2026-09-19): one half-width panel per interface; the Wi-Fi panel
    // stands for the wireless interface, also while its driver is unloaded. netconfig is the
    // Ethernet interface's: its mode and fields are that interface's IPv4 panel's.
    // openccu-lite task 221 (the maintainer, 2026-09-24): a column per interface - the interface's
    // panel, below it an IPv4 and an IPv6 panel of the same width; the addresses left the
    // interface panel. The system-wide IPv6 panel went: its gateway and SLAAC are the interfaces',
    // its DNS the name resolution's, its on/off every IPv6 panel's pill.
    const netconfigIface = $derived(ifaces.find((i) => i.name === 'eth0')?.name ?? ifaces.find((i) => i.kind === 'ethernet')?.name ?? '');
    const columns = $derived(interfaceColumns(net?.network.interfaces ?? [], netconfigIface, wifiChips > 0));
    const editable = $derived(!!net?.writable && auth.role === 'admin');
    const v6 = $derived(net?.network.ipv6_state);
    /** the IPv6 pill of an interface: the system's switch first, then the interface's own */
    function v6State(i?: NetIface): {on: boolean; text: string} {
        if (v6 && !v6.available) return {on: false, text: t('not available')};
        if (v6 && !v6.enabled) return {on: false, text: t('switched off')};
        if (i?.ipv6_enabled === false) return {on: false, text: t('switched off')};
        return {on: true, text: t('on')};
    }
    function kindWord(kind?: string): string {
        switch (kind) {
            case 'ethernet': return t('Ethernet');
            case 'wireless': return t('Wi-Fi');
            case 'bridge': return t('Bridge');
            case 'vlan': return 'VLAN';
            case 'veth': return t('virtual Ethernet (veth)');
            case 'virtual': return t('virtual interface');
        }
        return t('other kind');
    }
    function stateWord(s: LinkState): string {
        return s === 'up' ? t('up') : s === 'no-cable' ? t('no cable') : t('down');
    }
    function scopeWord(scope: string): string {
        switch (scope) {
            case 'global': return t('global');
            case 'unique-local': return t('unique local');
            case 'link-local': return t('link-local');
        }
        return scope;
    }
    function flagWord(flag: string): string {
        switch (flag) {
            case 'temporary': return t('temporary');
            case 'deprecated': return t('deprecated');
            case 'tentative': return t('tentative');
            case 'dad-failed': return t('address conflict');
        }
        return flag;
    }

    async function applyNetwork() {
        const s: Settings = {...form, dns: dnsText.split(/[\s,]+/).filter(Boolean)};
        if (s.mode === 'dhcp') { delete s.address; delete s.netmask; delete s.gateway; }
        const changesAddress = s.mode !== net?.current.mode || s.address !== net?.current.address;
        // task 262: security keys are bound to the system's full name - a new host name orphans them
        if (s.hostname !== net?.settings.hostname) {
            let registered = false;
            try { registered = (await api.get<{registered: boolean}>('/api/auth/v1/webauthn')).registered; } catch { /* an older daemon */ }
            if (registered && !(await ask({title: t('Rename the system?'), message: t('Passkeys are bound to the system\'s name. After the rename no registered passkey works any more: accounts sign in with their password until new passkeys are added. Rename anyway?'), confirm: t('Rename'), danger: true}))) return;
        }
        if (changesAddress && !(await ask(t('Apply the new network settings? They are reverted automatically unless you confirm within 90 seconds from the new address.')))) return;
        busy = 'net';
        notice = '';
        try {
            // task 125: the ticket for the confirm link is fetched before the address changes -
            // afterwards this page may not reach the box any more
            if (changesAddress) await sessionTicket();
            const r = await api.post<{applied: boolean; pending: Pending | null; rename?: Rename; certificate?: CertNames; acme_names?: AcmeNames}>('/api/system/v1/network', s);
            if (r.applied) {
                // openccu-lite task 62: a rename says what it did about the lease, and whether the
                // certificate still names the old host
                if (r.rename) {
                    notice = renameText(r.rename);
                    rename = {...r.rename, certificate: r.certificate ?? null, acme: r.acme_names ?? null};
                } else {
                    notice = t('Saved.');
                }
                await load();
            } else {
                setPending(r.pending);
                notice = t('Applied. Confirm within {s} seconds, or the previous settings come back.', {s: String(secondsLeft)});
            }
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function confirm(token?: string) {
        const tok = token ?? pending?.token;
        if (!tok) return;
        busy = 'confirm';
        try {
            await api.post('/api/system/v1/network/confirm', {token: tok});
            notice = t('Confirmed. The settings are now permanent.');
            history.replaceState(null, '', location.pathname);
            await load();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function revert() {
        if (!pending) return;
        busy = 'revert';
        try {
            await api.post('/api/system/v1/network/revert', {token: pending.token});
            notice = t('Reverted.');
            await load();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    // task 125: the link to the new address carries a one-time session ticket (single use, 90 s - as long as the confirm window, D-84;
    // main.ts exchanges it for a session on that host), never the session itself. Without one -
    // the ticket could not be fetched, or it has run out - the link lands on the login there, and
    // the change is confirmed after it.
    let confirmTicket = $state('');
    async function sessionTicket() {
        try {
            confirmTicket = await ticketFor('session');
        } catch {
            confirmTicket = '';
        }
    }
    const newAddressLink = () => {
        if (!pending) return '';
        const host = pending.settings.mode === 'static' ? pending.settings.address : '';
        if (!host || host === location.hostname) return '';
        const ticket = confirmTicket ? `&ticket=${encodeURIComponent(confirmTicket)}` : '';
        return `${location.protocol}//${host}${location.port ? ':' + location.port : ''}/system/network?confirm=${encodeURIComponent(pending.token)}${ticket}`;
    };

    async function loadZones() {
        if (zones.length) return;
        try { zones = (await api.get<{zones: Zone[]}>('/api/system/v1/time/zones')).zones; } catch (e) { error = (e as Error).message; }
    }

    async function saveTime() {
        busy = 'time';
        try {
            takeTime(await api.put<TimeConfig>('/api/system/v1/time', net?.host_managed ? {zone} : {zone, ntp_servers: ntpText.split(/[\s,]+/).filter(Boolean)}));
            notice = t('Time settings saved.');
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    async function setClock() {
        if (!clockText) return;
        busy = 'clock';
        try {
            takeTime(await api.post<TimeConfig>('/api/system/v1/time/clock', {time: new Date(clockText).toISOString()}));
            notice = t('Clock set.');
            clockText = '';
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = '';
        }
    }

    // the maintainer, 2026-09-20: the browser's own clock, for a system that has no real-time clock
    // and no NTP answer yet - the same route as the hand-set one
    async function syncToBrowser() {
        if (!(await ask({title: t('Sync to browser time'), message: t('Set the system clock to this browser\'s time, {time}? A running NTP service corrects it again at its next round.', {time: new Date().toLocaleString()}), confirm: t('Set')}))) return;
        busy = 'clock';
        try {
            takeTime(await api.post<TimeConfig>('/api/system/v1/time/clock', {time: new Date().toISOString()}));
            notice = t('System clock set.');
        } catch (e) {
            notice = (e as Error).message;
        } finally {
            busy = '';
        }
    }
</script>

{#snippet ifacePanel(i: NetIface)}
    {@const state = linkState(i)}
    {@const line = linkLine(i)}
    <div class="ol-card" data-iface={i.name}>
        <div class="ol-card-head">
            <span class="ol-card-icon"><Icon name={i.kind === 'wireless' ? 'wifi' : 'network'} size={14} /></span>
            <div class="ol-card-titles">
                <h3 class="ol-card-title">{i.name}</h3>
                <div class="ol-card-sub">{i.default_route ? `${kindWord(i.kind)} · ${t('default route')}` : kindWord(i.kind)}</div>
            </div>
            <span class="ol-badge ol-pill" class:good={state === 'up'} class:warn={state === 'no-cable'} data-state={state}>{stateWord(state)}</span>
        </div>
        <div class="ol-card-body ol-linkline">
            {#if line.kind === 'speed'}
                {line.duplex ? `${t('{speed} Mbit/s', {speed: line.speed})} · ${line.duplex === 'full' ? t('full duplex') : t('half duplex')}` : t('{speed} Mbit/s', {speed: line.speed})}
            {:else if line.kind === 'wireless'}
                {t('wireless')}
            {:else if line.kind === 'virtual'}
                {net?.host_managed ? `${t('virtual')} · ${t('managed by the host')}` : t('virtual')}
            {:else}
                <span class="ol-muted">{t('no link')}</span>
            {/if}
        </div>
        {@render ifaceDetails(i)}
    </div>
{/snippet}

<!-- what an interface is and what it moves - its addresses are its IPv4 and IPv6 panels' (task 221) -->
{#snippet ifaceDetails(i: NetIface)}
    <dl class="ol-fields">
        {#if i.mac}<dt class="ol-field-label">MAC</dt><dd class="ol-field-value hmm-mono">{i.mac}</dd>{/if}
        {#if i.mtu}<dt class="ol-field-label">MTU</dt><dd class="ol-field-value">{i.mtu}</dd>{/if}
        {#if i.driver}<dt class="ol-field-label">{t('Driver')}</dt><dd class="ol-field-value hmm-mono">{i.driver}</dd>{/if}
    </dl>
    {#if i.statistics}
        {@const rate = rates[i.name]}
        <div class="ol-trafficbox">
            <div class="ol-card-detail ol-traffic">{t('received {rx} · sent {tx}', {rx: formatBytes(i.statistics.rx_bytes), tx: formatBytes(i.statistics.tx_bytes)})}</div>
            <!-- the current rate, from two polls; the line is there from the start so the panel does not grow -->
            {#if rate}
                <div class="ol-card-detail ol-rate" title={t('receiving {rx}, sending {tx} right now', {rx: formatRate(rate.rx), tx: formatRate(rate.tx)})}>↓ {formatRate(rate.rx)}{' '}↑ {formatRate(rate.tx)}</div>
            {:else}
                <div class="ol-card-detail ol-rate ol-muted" title={t('the rate shows after the next update')}>↓ –{' '}↑ –</div>
            {/if}
        </div>
    {/if}
{/snippet}

{#snippet panelHead(title: string, icon: 'globe', pill?: {on: boolean; text: string})}
    <div class="ol-card-head">
        <span class="ol-card-icon"><Icon name={icon} size={14} /></span>
        <div class="ol-card-titles"><h3 class="ol-card-title">{title}</h3></div>
        {#if pill}<span class="ol-badge ol-pill" class:good={pill.on}>{pill.text}</span>{/if}
    </div>
{/snippet}

<!-- task 221: the interface's IPv4 - its addresses; for the configured interface its mode, gateway and
     DNS servers, and the netconfig form that was in the interface panel -->
{#snippet ipv4Panel(name: string, i?: NetIface)}
    {@const configured = name === netconfigIface}
    <div class="ol-card ol-ippanel" data-panel="ipv4" data-of={name}>
        {@render panelHead(`IPv4 · ${name}`, 'globe')}
        <!-- one grid for the values and the netconfig form, so the fields start where the values do -->
        <div class="ol-fields">
            <span class="ol-field-label">{t('Addresses')}</span>
            <div class="ol-field-value hmm-mono" data-addrs="ipv4">{#each i?.ipv4 ?? [] as a (a.address)}<div>{a.address}/{a.prefix}</div>{:else}<span class="ol-muted ol-none">{t('no address')}</span>{/each}</div>
            {#if configured && !editable}
                <span class="ol-field-label">{t('Mode')}</span><div class="ol-field-value">{net?.host_managed ? t('managed by the host') : net?.current.mode === 'dhcp' ? 'DHCP' : t('static')}</div>
            {/if}
            {#if configured || i?.default_route}
                {#if net?.current.gateway || net?.network.gateway}<span class="ol-field-label">{t('Gateway')}</span><div class="ol-field-value hmm-mono" data-v4-gateway>{net?.current.gateway || net?.network.gateway}</div>{/if}
                <span class="ol-field-label">{t('DNS servers')}</span><div class="ol-field-value hmm-mono">{net?.current.dns.length ? net.current.dns.join(', ') : '–'}</div>
            {/if}
        {#if configured && editable}
            <!-- netconfig's addressing, as it was in the interface's panel (task 89) -->
            <div class="ol-field-sep ol-ifform" aria-hidden="true"></div>
                <label class="ol-field-label" for="net-mode">{t('Mode')}</label>
                <div class="ol-field-value"><div class="ol-field-row"><select id="net-mode" class="hmm-select" bind:value={form.mode}><option value="dhcp">DHCP</option><option value="static">{t('static')}</option></select></div></div>
                {#if form.mode === 'static'}
                    <label class="ol-field-label" for="net-address">{t('Address')}</label>
                    <div class="ol-field-value"><div class="ol-field-row"><input id="net-address" class="hmm-input hmm-mono" bind:value={form.address} placeholder="192.168.1.20" /></div></div>
                    <label class="ol-field-label" for="net-netmask">{t('Netmask')}</label>
                    <div class="ol-field-value"><div class="ol-field-row"><input id="net-netmask" class="hmm-input hmm-mono" bind:value={form.netmask} placeholder="255.255.255.0" /></div></div>
                    <label class="ol-field-label" for="net-gateway">{t('Gateway')}</label>
                    <div class="ol-field-value"><div class="ol-field-row"><input id="net-gateway" class="hmm-input hmm-mono" bind:value={form.gateway} placeholder="192.168.1.1" /></div></div>
                    <label class="ol-field-label" for="net-dns">DNS</label>
                    <div class="ol-field-value"><div class="ol-field-row"><input id="net-dns" class="hmm-input hmm-mono" bind:value={dnsText} placeholder="192.168.1.1, 1.1.1.1" /></div></div>
                {/if}
                <span class="ol-field-label"></span>
                <div class="ol-field-value"><button class="hmm-button" onclick={applyNetwork} disabled={busy !== '' || !!pending} data-action="apply-network">{t('Apply')}</button></div>
        {/if}
        </div>
    </div>
{/snippet}

<!-- task 221: the interface's IPv6 - its addresses by scope, whether it takes router advertisements,
     the default gateway and the DNS servers where they leave by this interface -->
{#snippet ipv6Panel(name: string, i?: NetIface)}
    {@const gw = ipv6GatewayOf(name, v6)}
    <div class="ol-card ol-ippanel" data-panel="ipv6" data-of={name}>
        {@render panelHead(`IPv6 · ${name}`, 'globe', v6 ? v6State(i) : undefined)}
        <dl class="ol-fields">
            {#each groupIPv6(i?.ipv6) as g (g.scope)}
                <dt class="ol-field-label" data-scope={g.scope}>{scopeWord(g.scope)}</dt>
                <dd class="ol-field-value hmm-mono">
                    {#each g.addrs as a (a.address)}
                        <div class="ol-addr" class:ol-muted={quietAddress(a)} data-addr={a.address}>{a.address}/{a.prefix}{#each shownFlags(a) as f (f)} <span class="ol-badge">{flagWord(f)}</span>{/each}</div>
                    {/each}
                </dd>
            {:else}
                <dt class="ol-field-label">{t('Addresses')}</dt><dd class="ol-field-value" data-addrs="ipv6"><span class="ol-muted ol-none">{t('no address')}</span></dd>
            {/each}
            {#if i}<dt class="ol-field-label">SLAAC</dt><dd class="ol-field-value" data-slaac>{i.ipv6_autoconf && i.ipv6_enabled !== false ? t('router advertisements accepted') : t('off')}</dd>{/if}
            {#if gw}
                <dt class="ol-field-label">{t('Gateway')}</dt><dd class="ol-field-value hmm-mono" data-v6-gateway>{gw}</dd>
                <dt class="ol-field-label">{t('DNS servers')}</dt><dd class="ol-field-value hmm-mono">{v6?.dns.length ? v6.dns.join(', ') : '–'}</dd>
            {/if}
            {#if name === netconfigIface && net?.network.ipv6}<dt class="ol-field-label">netconfig</dt><dd class="ol-field-value hmm-mono">IPV6={net.network.ipv6}</dd>{/if}
            {#if v6forms[name] && v6view}
                {@const f = v6forms[name]}
                {#if editable && v6view.writable}
                    <!-- task 227: the interface's IPv6 mode, applied with a rollback window -->
                    <dd class="ol-field-sep" aria-hidden="true"></dd>
                    <dt class="ol-field-label"><label for={`v6-mode-${name}`}>{t('Mode')}</label></dt>
                    <dd class="ol-field-value"><div class="ol-field-row">
                        <select id={`v6-mode-${name}`} class="hmm-select" bind:value={f.mode} data-v6-mode={name}>
                            <option value="off">{t('off')}</option><option value="slaac">SLAAC</option><option value="dhcpv6">DHCPv6</option><option value="static">{t('static')}</option>
                        </select>
                    </div><p class="ol-field-hint">{f.mode === 'slaac' ? t('Addresses from the router\'s announcements.') : f.mode === 'dhcpv6' ? t('An address from a DHCPv6 server; the gateway from the router\'s announcements.') : f.mode === 'off' ? t('No IPv6 on this interface, not even a link-local address.') : t('A fixed address; the router\'s announcements are ignored.')}</p></dd>
                    {#if f.mode === 'static'}
                        <dt class="ol-field-label"><label for={`v6-addr-${name}`}>{t('Address')}</label></dt>
                        <dd class="ol-field-value"><div class="ol-field-row"><input id={`v6-addr-${name}`} class="hmm-input hmm-mono" bind:value={f.address} placeholder="2001:db8::20" /></div></dd>
                        <dt class="ol-field-label"><label for={`v6-prefix-${name}`}>{t('Prefix length')}</label></dt>
                        <dd class="ol-field-value"><div class="ol-field-row"><input id={`v6-prefix-${name}`} class="hmm-input hmm-mono ol-v6prefix" inputmode="numeric" bind:value={f.prefix} placeholder="64" /></div></dd>
                        <dt class="ol-field-label"><label for={`v6-gw-${name}`}>{t('Gateway')}</label></dt>
                        <dd class="ol-field-value"><div class="ol-field-row"><input id={`v6-gw-${name}`} class="hmm-input hmm-mono" bind:value={f.gateway} placeholder="fe80::1" /></div></dd>
                        <dt class="ol-field-label"><label for={`v6-dns-${name}`}>DNS</label></dt>
                        <dd class="ol-field-value"><div class="ol-field-row"><input id={`v6-dns-${name}`} class="hmm-input hmm-mono" bind:value={f.dns} placeholder="2001:db8::53" /></div></dd>
                    {/if}
                    {#if v6Cuts(name)}
                        <!-- occulited B-85: a change here cuts this browser off; the page at the IPv4 address does not -->
                        <dt class="ol-field-label"></dt>
                        <dd class="ol-field-value"><p class="ol-field-hint" data-v6-cuts={name}>{t('This browser is connected over IPv6 here: a change cuts it off and goes back by itself.')}{#if v4Link(name)}{' '}<a href={v4Link(name)}>{t('Open over IPv4')}</a>{/if}</p></dd>
                    {/if}
                    <dt class="ol-field-label"></dt>
                    <dd class="ol-field-value"><button class="hmm-button" onclick={() => applyV6(name)} disabled={busy !== '' || !!v6pending || !v6changed(name)} data-action="apply-v6">{t('Apply')}</button></dd>
                {:else}
                    <dt class="ol-field-label">{t('Mode')}</dt><dd class="ol-field-value" data-v6-mode-read>{v6ModeWord(v6view.interfaces[name]?.mode ?? 'slaac')}</dd>
                {/if}
            {/if}
        </dl>
    </div>
{/snippet}

<SystemTitle />
{#if !net || !time}
    <Loading {error} />
{:else}
    {#if error}<div class="ol-notice err">{error}</div>{/if}
    {#if notice}<div class="ol-notice" data-notice="network">{notice}</div>{/if}
    {#if certNotice}
        <div class="ol-panel ol-notice-panel warn ol-notice-link" data-notice="rename-certificate">
            <span>{certNotice}</span>
            <a class="hmm-button" href="/system/certificates" use:link data-action="open-certificate">{t('Open certificate settings')}</a>
        </div>
    {/if}
    <!-- task 34: a container's veth is configured by the container manager (Proxmox) and its
         clock is the host's, so this page shows the network and sets nothing but the zone. -->
    {#if net.host_managed}
        <div class="ol-notice">{t('This system is a container. Its network - address, gateway, DNS, hostname - and its clock are managed by the host; change them in the container\'s configuration there. What is shown here is what the container was given.')}</div>
    {/if}

    {#if pending}
        <div class="ol-notice warn">
            <strong>{t('Waiting for confirmation — {s} s left', {s: secondsLeft})}</strong>
            <div>{pending.settings.mode === 'dhcp' ? 'DHCP' : `${pending.settings.address} / ${pending.settings.netmask}`} — {t('previously')} {pending.previous.mode === 'dhcp' ? 'DHCP' : pending.previous.address}</div>
            {#if newAddressLink()}
                <div style="margin-top:6px">{t('If this page still responds, the old address is still live. Open the new address to confirm from there:')} <a href={newAddressLink()}>{newAddressLink()}</a></div>
            {/if}
            <div class="ol-actions" style="margin-top:6px">
                <button class="hmm-button" onclick={() => confirm()} disabled={busy !== ''}>{t('Confirm')}</button>
                <button class="hmm-button" onclick={revert} disabled={busy !== ''}>{t('Revert now')}</button>
            </div>
        </div>
    {/if}

    {#if v6pending}
        <!-- task 227: the IPv6 change waits for its confirmation; without it the system rolls back -->
        <div class="ol-notice warn" data-v6-pending>
            <strong>{t('IPv6 on {iface}: keep it? {s} s left', {iface: v6pending.interface, s: v6left})}</strong>
            <div>{v6ModeWord(v6pending.settings.mode)}{v6pending.settings.mode === 'static' ? ` ${v6pending.settings.address}/${v6pending.settings.prefix}` : ''} — {t('previously')} {v6ModeWord(v6pending.previous.mode)}</div>
            <div class="ol-muted">{t('If nothing confirms it in time - this page lost the system - the previous configuration comes back by itself.')}</div>
            <div class="ol-actions" style="margin-top:6px">
                <button class="hmm-button primary" onclick={() => finishV6(true)} disabled={busy !== ''} data-action="v6-keep">{t('Keep')}</button>
                <button class="hmm-button" onclick={() => finishV6(false)} disabled={busy !== ''} data-action="v6-revert">{t('Revert now')}</button>
            </div>
        </div>
    {/if}

    <!-- task 221: a column per interface - its panel, its IPv4, its IPv6 - two side by side on a wide
         screen; the rows of the columns share their heights (subgrid), so the IPv4 panels start at one
         height and the IPv6 panels at another -->
    <h2>{t('Interfaces')}</h2>
    {#if columns.length === 0}
        <div class="ol-muted">–</div>
    {:else}
        <div class="ol-ifaces ol-ifgrid">
            {#each columns as c (c.name)}
                <div class="ol-ifcol" data-column={c.name}>
                    {#if c.kind === 'wifi'}
                        <WiFiPanel admin={auth.role === 'admin'} iface={c.iface} details={ifaceDetails} onenabled={(on) => (wifiOff = !on)} />
                    {:else if c.iface}
                        {@render ifacePanel(c.iface)}
                    {/if}
                    <!-- task 226: an inactive or disabled interface is its panel alone -->
                    {#if showsAddressPanels(c.iface, {configured: c.name === netconfigIface, wifiOff: c.kind === 'wifi' && wifiOff})}
                        {@render ipv4Panel(c.name, c.iface)}
                        {@render ipv6Panel(c.name, c.iface)}
                    {/if}
                </div>
            {/each}
        </div>
    {/if}

    <!-- the maintainer, 2026-09-20: one panel for the names (the system's own and the servers that
         resolve them), one for the clock; task 221: in the interface columns' tracks, their fields on
         one grid -->
    <h2>{t('System')}</h2>
    <div class="ol-ifaces ol-syspanels">
        <section class="ol-card" data-panel="name-resolution">
            <div class="ol-card-head">
                <span class="ol-card-icon"><Icon name="tag" size={14} /></span>
                <div class="ol-card-titles"><h3 class="ol-card-title">{t('Name resolution')}{#if editable}<Help>{t('The name this system answers to, and the servers it asks. A hostname change is saved directly; a DNS change goes through the same confirmation as an address change.')}</Help>{/if}</h3></div>
            </div>
            <div class="ol-fields">
                <span class="ol-field-label">{t('Full name')}</span>
                <div class="ol-field-value hmm-mono" data-fqdn>{net.network.hostname}{net.network.domain ? `.${net.network.domain}` : ''}</div>
                {#if editable}
                    <label class="ol-field-label" for="net-hostname">{t('Hostname')}</label>
                    <div class="ol-field-value"><div class="ol-field-row">
                        <input id="net-hostname" class="hmm-input" bind:value={form.hostname} aria-label={t('Hostname')} />
                        <button class="hmm-button" onclick={applyNetwork} disabled={busy !== '' || !!pending || form.hostname === net.settings.hostname}>{t('Save')}</button>
                    </div></div>
                {/if}
                <span class="ol-field-label">{t('DNS servers in use')}</span>
                <div class="ol-field-value hmm-mono" data-dns-current>{net.current.dns.length ? net.current.dns.join(', ') : '–'}</div>
                {#if v6 && v6.dns.length}
                    <span class="ol-field-label">{t('DNS servers in use')} · IPv6</span>
                    <div class="ol-field-value hmm-mono">{v6.dns.join(', ')}</div>
                {/if}
                {#if editable}
                    <label class="ol-field-label" for="net-dns-override">{t('Override')}</label>
                    <div class="ol-field-value">
                        <div class="ol-field-row">
                            <input id="net-dns-override" class="hmm-input hmm-mono" bind:value={dnsText} placeholder="192.168.1.1, 1.1.1.1" aria-label={t('DNS servers')} />
                            <button class="hmm-button" onclick={applyNetwork} disabled={busy !== '' || !!pending}>{t('Save')}</button>
                        </div>
                        <p class="ol-field-hint">{form.mode === 'dhcp' ? t('Empty: the servers DHCP gives are used. What is entered here replaces them.') : t('The servers this system uses; at most two.')}</p>
                    </div>
                {/if}
            </div>
        </section>

        <!-- occulited task 12: HmIP-RF held back for a trusted clock is this card's matter -->
        <section class="ol-card {warnEdge(['hmip-clock-hold'])}" data-panel="time" {...warnFor(['hmip-clock-hold'])}>
            <div class="ol-card-head">
                <span class="ol-card-icon"><Icon name="clock" size={14} /></span>
                <div class="ol-card-titles"><h3 class="ol-card-title">{t('Time')}<Help>{t('The clock and where it comes from. A system without a real-time clock starts in the past until NTP answers, which is why the browser\'s time is offered here.')}</Help></h3></div>
            </div>
            <div class="ol-fields">
                <span class="ol-field-label">{t('System time')}</span>
                <div class="ol-field-value hmm-mono" data-system-time>{systemTimeText}</div>
                {#if !net.host_managed}
                    <span class="ol-field-label">{t('Synchronisation')}</span>
                    <div class="ol-field-value">{time.has_ntp ? t('by NTP') : t('not synchronised')}</div>
                {/if}
                <label class="ol-field-label" for="net-zone">{t('Timezone')}</label>
                <div class="ol-field-value"><div class="ol-field-row">
                    <select id="net-zone" class="hmm-select" bind:value={zone} onfocus={loadZones} disabled={auth.role !== 'admin'}>
                        {#if !zones.length}<option value={zone}>{zone}</option>{/if}
                        {#each zones as z (z.name)}<option value={z.name}>{z.name}{z.country ? ` — ${z.country}` : ''}{z.comment ? ` (${z.comment})` : ''}</option>{/each}
                    </select>
                </div></div>
                {#if !net.host_managed}
                    <label class="ol-field-label" for="net-ntp">NTP</label>
                    <div class="ol-field-value">
                        <div class="ol-field-row"><input id="net-ntp" class="hmm-input hmm-mono" bind:value={ntpText} placeholder="0.de.pool.ntp.org 1.de.pool.ntp.org" disabled={auth.role !== 'admin'} /></div>
                        <p class="ol-field-hint">{time.ntp_servers.length ? t('Servers, separated by spaces.') : t('Empty: the servers DHCP gives are used.')}</p>
                    </div>
                {:else}
                    <span class="ol-field-label"></span>
                    <div class="ol-field-value"><p class="ol-field-hint">{t('The clock is the host\'s; only the timezone is set here.')}</p></div>
                {/if}
                {#if auth.role === 'admin'}
                    <span class="ol-field-label"></span>
                    <div class="ol-field-value"><button class="hmm-button" onclick={saveTime} disabled={busy !== ''} data-action="save-time">{t('Save')}</button></div>
                    {#if !net.host_managed}
                        <!-- the maintainer, 2026-09-20: the browser's clock in one click, beside setting it by hand -->
                        <span class="ol-field-label">{t('Browser time')}</span>
                        <div class="ol-field-value">
                            <div class="ol-field-buttons"><button class="hmm-button" onclick={syncToBrowser} disabled={busy !== ''} data-action="sync-browser">{t('Sync to browser time')}</button></div>
                            <p class="ol-field-hint">{t('This browser: {time}', {time: browserTimeText})}</p>
                        </div>
                        <label class="ol-field-label" for="net-clock">{t('Set the clock by hand')}</label>
                        <div class="ol-field-value"><div class="ol-field-row">
                            <input id="net-clock" class="hmm-input" type="datetime-local" bind:value={clockText} />
                            <button class="hmm-button" onclick={setClock} disabled={busy !== '' || !clockText}>{t('Set')}</button>
                        </div></div>
                    {/if}
                {/if}
            </div>
        </section>
    </div>

{/if}

<style>
    .ol-notice.warn { border-left: 3px solid #e0a800; }
    .ol-notice.err { border-left: 3px solid #d33; }
    .ol-pill { margin-left: auto; }
    /* task 89: two interfaces side by side, one column on a narrow window. Task 221: a column per
       interface spanning three rows of the page's grid, its panels on those rows (subgrid), so the
       IPv4 and IPv6 panels of two columns line up even when one interface panel is taller. The
       system panels below take the same two tracks. */
    .ol-ifaces { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-bottom: 14px; }
    .ol-ifcol { display: grid; grid-row: span 3; grid-template-rows: subgrid; gap: 12px; min-width: 0; }
    /* task 226: a column may be its interface panel alone, its two other rows empty - so the space
       between the panels is the panels' own margin, not the grid's gap, and an empty row is 0 high */
    .ol-ifaces.ol-ifgrid { row-gap: 0; margin-bottom: 2px; }
    .ol-ifgrid > .ol-ifcol { row-gap: 0; margin-bottom: 12px; }
    .ol-ifcol > :global(.ol-card + .ol-card) { margin-top: 12px; }
    .ol-ifcol > :global(.ol-card) { display: flex; flex-direction: column; min-width: 0; }
    @media (max-width: 900px) { .ol-ifaces { grid-template-columns: minmax(0, 1fr); } }
    .ol-syspanels > .ol-card { display: flex; flex-direction: column; }
    .ol-field-sep { grid-column: 1 / -1; border-top: 1px solid var(--hmm-border-muted); margin: 0; }
    .ol-v6prefix { max-width: 6em; }
    .ol-linkline { font-size: var(--hmm-font-size); }
    .ol-addr .ol-badge, .ol-none { font-family: var(--hmm-font); }
    .ol-trafficbox { margin-top: auto; padding-top: 10px; }
    .ol-trafficbox .ol-card-detail { margin-top: 0; }
    .ol-rate { margin-top: 2px; font-variant-numeric: tabular-nums; }
</style>
