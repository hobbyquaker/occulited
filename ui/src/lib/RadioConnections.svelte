<script module lang="ts">
    // openccu-lite B-272: the status names every detected module with its roles, and the HB-RF-ETH
    // configured under LAN devices - the Interfaces page's cards for the modules no process uses,
    // and its notice while a board's module is not detected yet
    export interface ConnModule { serial: string; hardware: string; node?: string; device_type?: string; sgtin?: string; version?: string; probe: string; detail?: string; roles: string[] }
    export interface ConnBoard { address: string; connected: boolean; detected: boolean; serial?: string }
    // openccu-lite B-300: the GPIO header's UART that answered nothing is no module and has no card;
    // the status says it only while a chosen module is missing - the one case a module was expected
    export interface ConnHeaderSilent { node: string; probe: string; detail?: string; missing: string[] }
    export interface ConnView { modules?: ConnModule[]; hb_rf_eth?: ConnBoard; header_silent?: ConnHeaderSilent }
</script>

<script lang="ts">
    import {dateTime, dateLocale} from './datefmt';
    // Task 129 phase 3 (D-81, D-82, D-98): which local module each interface process uses. Each
    // process has at most one; LAN gateways (rfd), HAPs and DRAPs (hmipserver's routers) can be
    // several and are not chosen here. The choice is only offered for what the box has detected,
    // it never flashes firmware, and a change that takes the local BidCos-RF radio away lists the
    // paired devices first. The box writes the choice into rfd.conf and hmip_user.conf, runs the
    // detection and the plan again and restarts the radio daemons; the page polls while it runs.
    import {onMount} from 'svelte';
    import {api, ApiError, type Service} from './api';
    import {pageLife} from './pagelife.svelte';
    import {statusDot, statusKind} from './units';
    import {ask, askText} from './dialog.svelte';
    import {sameHostname} from './factoryreset';
    import {firmwareRefusalLines, refusalOf, type FirmwareRefusal} from './hmipfirmware';
    import {t} from './i18n.svelte';
    import {link} from './router.svelte';
    import Help from './Help.svelte';
    import Icon from './Icon.svelte';
    import HmIPExchange from './HmIPExchange.svelte';
    import DevicesImportNotice from './DevicesImportNotice.svelte';

    // paths (task 150): how hmipserver can reach an HmIP option - "direct" on its node, "multimacd" through /dev/mmd_hmip
    // hmip_only (openccu-lite B-282): the module carries HmIP alone - an HmIP-RFUSB on the HmIP-only
    // firmware line (application HMIP_TRX_App, firmware below 4), the Telekom stick, a module
    // without a serial: not offered for BidCos-RF, reached directly only
    interface Option { id: string; hardware: string; node?: string; device_type?: string; sgtin?: string; version?: string; paths?: string[]; application?: string; hmip_only?: boolean }
    interface Daemon { run: boolean; node?: string; reason: string }
    interface Role { hardware: string; node?: string; serial: string; sgtin?: string }
    interface Plan {
        multimacd: Daemon; rfd: Daemon; hmipserver: Daemon; hmipserver_jar?: string; hmrf?: Role; hmip?: Role;
        rfd_local: boolean; rfd_usb_adapter: boolean; rfd_lan_gateway: boolean; hmip_advanced: boolean;
        missing_hmip?: string; missing_bidcos?: string; hmip_pin?: string; interfaces: string[]; notes: string[];
    }
    interface Change { choices: Choices; previous: Choices; started: string; finished?: string; ok: boolean; error?: string; lines: string[] }
    interface Choices { hmip: string; bidcos: string; hmip_path?: string }
    // openccu-lite B-285: a snapshot from before a move of HmIP-RF to another module is kept
    // openccu-lite B-302: the HmIP security counter gap of a way back
    interface CounterGap { from: string; to: string; highest: number; back: number; behind: number; until: string; at?: string }
    interface MoveBack { previous: string; at: string; choices: Choices; devices: number; counter_gap?: CounterGap }
    interface Status { available: boolean; choices: Choices; options: {hmip: Option[]; bidcos: Option[]}; plan?: Plan; mode?: string; hmip_fatal?: {code: string; line: string; adapter?: string; cause?: string}; modules?: ConnModule[]; hb_rf_eth?: ConnBoard; header_silent?: ConnHeaderSilent; running: Change | null; last: Change | null; hmip_move_back?: MoveBack; hmip_counter_gap?: CounterGap; hostname?: string }
    interface Preview { plan: Plan; changed: boolean; restarts: string[]; bidcos_lost: boolean; devices: {address: string; type: string}[]; devices_error?: string; hmip_off?: boolean; hmip_move?: {from: string; to: string; local_key: boolean; snapshot: boolean; snapshot_blocked?: boolean; to_version?: string; refused?: FirmwareRefusal} }

    // onready: called once, after the first load (answered or not) - the page scrolls to an anchor
    // only then, since this section appearing above it would push the anchor out of view
    // order: the protocols of the module cards above, in their order - the two panels follow it, so
    // the BidCos-RF panel stands under the BidCos-RF module; BidCos-RF first when it is not known
    // onstatus (B-272): every status read, for the page's module cards and its HB-RF-ETH notice
    let {admin = false, order = [], onchanged, onready, onstatus}: {admin?: boolean; order?: string[]; onchanged?: () => void; onready?: () => void; onstatus?: (s: ConnView) => void} = $props();
    const hmipFirst = $derived(order.includes('HmIP-RF') && (!order.includes('BidCos-RF') || order.indexOf('HmIP-RF') < order.indexOf('BidCos-RF')));
    let readied = false;

    let st = $state<Status | null>(null);
    let err = $state('');
    // task 155: what the exchange notice did last, shown once the marker is gone
    let exchangeDone = $state<'' | 'retry' | 'fresh-start'>('');
    // the HmIP dropdown's value is "<module>|<path>" (task 150): one entry per way to reach a module
    let pickHmIP = $state('|');
    let pickBidCos = $state('');
    let touched = false;
    let poll: ReturnType<typeof setTimeout> | undefined;
    let wasRunning = false;
    let anchored = false;

    // task 183: each panel's process with the Services page's dot (running, starting, failed, a
    // condition not met, stopped), read with the page and every 10 s while it shows
    const life = pageLife();
    let units = $state<Record<string, Service>>({});
    let unitPoll: ReturnType<typeof setInterval> | undefined;
    async function loadUnits() {
        try {
            const r = await api.get<{services: Service[]}>('/api/system/v1/services');
            units = Object.fromEntries(r.services.filter((x) => x.id === 'hmipserver' || x.id === 'rfd' || x.id === 'multimacd').map((x) => [x.id, x]));
        } catch {
            /* no dot then */
        }
    }
    function unitWord(u: Service): string {
        switch (statusKind(u)) {
            case 'running':
                return t('Running');
            case 'starting':
                return t('Starting');
            case 'completed':
                return t('Completed');
            case 'ended':
                return t('Exited');
            case 'failed':
                return t('Failed');
            case 'skipped':
                return t('Skipped');
            default:
                return t('Stopped');
        }
    }

    async function load() {
        void loadUnits();
        try {
            st = await api.get<Status>('/api/system/v1/radio/connections');
            err = '';
        } catch (e) {
            err = (e as Error).message;
            if (!readied) { readied = true; onready?.(); }
            return;
        }
        if (!readied) {
            readied = true;
            // after this render, so the section's height is in the layout
            setTimeout(() => onready?.(), 0);
        }
        // the Status page's cards link to /system/interfaces#connections
        if (!anchored && location.hash === '#connections') {
            anchored = true;
            setTimeout(() => document.getElementById('connections')?.scrollIntoView({block: 'start'}), 0);
        }
        if (!touched) {
            pickHmIP = hmipValue(st.choices);
            pickBidCos = st.choices.bidcos;
        }
        onstatus?.(st);
        clearTimeout(poll);
        if (st.running) {
            wasRunning = true;
            poll = setTimeout(load, 2000);
        } else if (wasRunning) {
            wasRunning = false;
            touched = false;
            pickHmIP = hmipValue(st.choices);
            pickBidCos = st.choices.bidcos;
            onchanged?.();
        } else if (st.hb_rf_eth && !st.hb_rf_eth.detected) {
            // B-272: a board added under LAN devices is attached and probed by the radio hotplug some
            // ten seconds later, and a board that does not answer is tried again by the system: the
            // status is read again while the page shows, until the board's module is in the detection
            boardPending = true;
            pollWhileShown(5000);
        } else if (boardPending) {
            boardPending = false;
            onchanged?.();
        }
    }
    let boardPending = false;
    function pollWhileShown(ms: number) {
        poll = setTimeout(() => (life.active ? void load() : pollWhileShown(ms)), ms);
    }
    /** the page's Try now for a pending board (B-272): read the status again at once */
    export function reload() {
        void load();
    }

    const dirty = $derived(!!st && (pickHmIP !== hmipValue(st.choices) || pickBidCos !== st.choices.bidcos));

    const curHmIP = $derived(st ? hmipValue(st.choices) : '|');
    function hmipValue(c: Choices): string {
        return `${c.hmip}|${c.hmip_path ?? ''}`;
    }
    function splitHmIP(v: string): {hmip: string; hmip_path: string} {
        const i = v.lastIndexOf('|');
        return i < 0 ? {hmip: v, hmip_path: ''} : {hmip: v.slice(0, i), hmip_path: v.slice(i + 1)};
    }
    function optionLabel(o: Option): string {
        return [o.hardware, o.id, o.node].filter(Boolean).join(' · ');
    }
    /** the /dev node the process opens for the option, and the one behind it through multimacd */
    function pathText(o: Option, path: string, mmd: string): string {
        if (path === 'multimacd') return t('{mmd} through multimacd on {node}', {mmd, node: o.node ?? ''});
        if (path === 'direct') return o.node ?? '';
        return t('path automatic');
    }
    /** the HmIP dropdown's entries: one per module and path, the path's /dev node named */
    function hmipEntries(list: Option[]): {value: string; label: string}[] {
        return list.flatMap((o) => (o.paths?.length ? o.paths : ['']).map((path) => ({value: `${o.id}|${path}`, label: `${o.hardware} · ${o.id} · ${pathText(o, path, '/dev/mmd_hmip')}${o.hmip_only ? ' · ' + t('HmIP only - firmware {version}', {version: o.version ?? '?'}) : ''}`})));
    }
    /** the HmIP-only sticks among the options (B-282): the page says why they are HmIP only and where the dual firmware is flashed */
    function hmipOnlySticks(list: Option[]): Option[] {
        return list.filter((o) => o.hmip_only && o.hardware === 'HMIP-RFUSB');
    }
    /** BidCos-RF reaches a module on the header or an HB-RF-USB/ETH through multimacd, the HM-CFG-USB-2 over USB */
    function bidcosLabel(o: Option): string {
        return `${o.hardware} · ${o.id} · ${o.node ? pathText(o, 'multimacd', '/dev/mmd_bidcos') : 'USB'}`;
    }
    function roleText(r: Role): string {
        return `${r.hardware} ${r.serial}`.trim();
    }
    /** openccu-lite task 322: BidCos-RF off by choice with no LAN gateway - the system runs HmIP only */
    function hmipOnly(p: Plan, c?: Choices): boolean {
        return !!c && c.bidcos === 'none' && !p.rfd.run && !p.rfd_lan_gateway;
    }
    function hmipText(p: Plan, c?: Choices): string {
        // occulited task 147: HMServer.jar only when HmIP is set to none
        if (p.hmipserver_jar === 'HMServer.jar') return t('HmIP off: HMServer.jar, virtual devices only');
        // openccu-lite task 318: on Automatic, the module that holds the HmIP network is kept; task
        // 147: HMIPServer.jar cannot start without its module, and there is no fallback
        if (!p.hmip && p.missing_hmip && p.hmip_pin === p.missing_hmip) return t('the module {id}, which holds the HmIP network, is missing: hmipserver cannot start', {id: p.missing_hmip});
        if (!p.hmip) return p.missing_hmip ? t('the chosen module {id} is missing: hmipserver cannot start', {id: p.missing_hmip}) : t('no HmIP module: hmipserver cannot start');
        if (p.hmipserver.node === '/dev/mmd_hmip') {
            if (hmipOnly(p, c)) return t('HmIP only: {module}, through multimacd on {node}', {module: roleText(p.hmip), node: p.multimacd.node ?? ''});
            if (p.rfd.node === '/dev/mmd_bidcos') return t('{module}, shared with BidCos-RF through multimacd', {module: roleText(p.hmip)});
            return t('{module}, through multimacd on {node}', {module: roleText(p.hmip), node: p.multimacd.node ?? ''});
        }
        // task 322: HmIP only says so; the HM-MOD-RPI-PCB keeps multimacd for HmIP (D-101), so not "directly" there
        if (hmipOnly(p, c)) return t('HmIP only: {module}, directly on {node}', {module: roleText(p.hmip), node: p.hmipserver.node ?? ''});
        return t('{module}, directly on {node}', {module: roleText(p.hmip), node: p.hmipserver.node ?? ''});
    }
    function bidcosText(p: Plan, c: Choices): string {
        if (!p.rfd.run) {
            if (p.missing_bidcos) return t('off: the chosen module {id} is missing', {id: p.missing_bidcos});
            if (c.bidcos === 'none') return t('off (HmIP only)');
            return t('off: no BidCos-RF hardware');
        }
        const parts: string[] = [];
        if (p.rfd_local && p.hmrf) parts.push(t('{module} through multimacd', {module: roleText(p.hmrf)}));
        if (p.rfd_usb_adapter && p.hmrf) parts.push(roleText(p.hmrf));
        if (p.rfd_lan_gateway) parts.push(t('LAN gateways'));
        let s = parts.join(' + ');
        if (p.missing_bidcos) s += ' · ' + t('the chosen module {id} is missing', {id: p.missing_bidcos});
        return s;
    }
    function multimacdText(p: Plan): string {
        return p.multimacd.run ? t('runs on {node}', {node: p.multimacd.node ?? ''}) : t('not needed');
    }
    /** the module behind multimacd's node, as the plan names it */
    function multimacdModule(p: Plan): string {
        const r = [p.hmrf, p.hmip].find((x) => x?.node && x.node === p.multimacd.node);
        return r ? roleText(r) : '';
    }

    /** BidCos-RF's "none" by context (task 322): HmIP only without a LAN gateway, the gateway alone with one */
    function noneLabel(lgw: boolean): string {
        return lgw ? t('No local radio (LAN gateway only)') : t('Off (HmIP only)');
    }
    function choiceLabel(v: string, list: Option[], lgw = false): string {
        if (v === '') return t('Automatic');
        if (v === 'none') return noneLabel(lgw);
        const o = list.find((x) => x.id === v || x.sgtin === v);
        return o ? optionLabel(o) : v;
    }
    function hmipChoiceLabel(v: string, list: Option[]): string {
        const {hmip, hmip_path} = splitHmIP(v);
        if (hmip === 'none') return t('None (virtual devices only)');
        const e = hmipEntries(list).find((x) => x.value === v);
        if (e) return e.label;
        const base = choiceLabel(hmip, list);
        return hmip_path === 'multimacd' ? `${base} · ${t('through multimacd')}` : hmip_path === 'direct' ? `${base} · ${t('directly')}` : base;
    }

    async function apply() {
        if (!st) return;
        const choice = {...splitHmIP(pickHmIP), bidcos: pickBidCos};
        let pv: Preview;
        try {
            pv = await api.post<Preview>('/api/system/v1/radio/connections/preview', choice);
        } catch (e) {
            err = (e as Error).message;
            return;
        }
        // openccu-lite B-289: not onto a module whose firmware cannot take the network key - the
        // refusal, the firmware and the update, and nothing to confirm
        if (pv.hmip_move?.refused) {
            await ask({title: t('HmIP-RF cannot move to this module'), message: firmwareRefusalLines(pv.hmip_move.refused, 'change', t).join('\n\n'), confirm: t('Close'), cancel: ''});
            return;
        }
        // openccu-lite B-301 (maintainer: the refusal stays): a snapshot of the module from before
        // local key mode was switched off is in the way of the move's own snapshot - said, and its
        // discard offered in red; after it the change is previewed and asked again
        if (pv.hmip_move?.snapshot_blocked) {
            const module = pv.hmip_move.from;
            const discarded = await ask({
                title: t('A kept snapshot blocks the move'),
                message: [
                    t('A snapshot of module {module} from before local key mode was switched off is still kept. Before HmIP-RF moves to another module, the system keeps a new snapshot of this module - the way back without eQ-3\'s key server - and the old one is in its way.', {module}),
                    t('Discarding it removes this module\'s identity as it was before the switch to local key mode, key material included. The identity in use is not touched. After the discard the change is asked again.'),
                ].join('\n\n'),
                confirm: t('Discard the snapshot'),
                danger: true,
                focusCancel: true,
                run: async () => {
                    await api.del(`/api/system/v1/radio/hmip/local-key/snapshots/${encodeURIComponent(module)}`);
                },
            });
            if (discarded) await apply();
            return;
        }
        const c = choice;
        const paras = [
            t('HmIP-RF: {choice}', {choice: hmipChoiceLabel(pickHmIP, st.options.hmip)}) + '\n' + t('BidCos-RF: {choice}', {choice: choiceLabel(c.bidcos, st.options.bidcos, pv.plan.rfd_lan_gateway)}),
            t('Afterwards:') + '\n' + [
                t('hmipserver: {what}', {what: hmipText(pv.plan, c)}),
                t('rfd: {what}', {what: bidcosText(pv.plan, c)}),
                t('multimacd: {what}', {what: multimacdText(pv.plan)}),
            ].join('\n'),
        ];
        if (pv.restarts.length) paras.push(t('{units} are stopped and started again. The radio is unavailable for about a minute; nothing is flashed.', {units: pv.restarts.join(', ')}));
        // openccu-lite B-285: HmIP-RF leaves its module - an adapter exchange through eQ-3's key server,
        // which may refuse the exchange back (task 301: "may", no claim about its rules); the system
        // keeps a snapshot first, the way back that needs no server
        if (pv.hmip_move && !pv.hmip_move.local_key) {
            paras.push(t('HmIP-RF moves from module {from} to module {to}. That is an adapter exchange through eQ-3\'s key server: the HmIP network belongs to the new module afterwards, and the key server may refuse the exchange back.', {from: pv.hmip_move.from, to: pv.hmip_move.to}));
            paras.push(pv.hmip_move.snapshot
                ? t('The system keeps a snapshot of the current module\'s identity and device files first. "Back to the previous module" on this page works from that snapshot without the key server.')
                : t('No snapshot can be kept here: the way back to the current module is another exchange, which the key server may refuse.'));
        } else if (pv.hmip_move) {
            paras.push(t('HmIP-RF moves from module {from} to module {to}. Local key mode is on: no key server is involved, and HmIP-RF writes its identity files for the new module.', {from: pv.hmip_move.from, to: pv.hmip_move.to}));
        }
        // occulited task 147: HmIP off - hmipserver runs HMServer.jar, the HmIP devices are out of reach
        if (pv.hmip_off) paras.push(t('HmIP is switched off: hmipserver runs HMServer.jar with the virtual devices only, and HmIP-RF is no longer offered. Paired HmIP devices are out of reach until HmIP is set back; their pairings and the HmIP network stay on the system.'));
        if (pv.bidcos_lost) {
            paras.push(t('BidCos-RF loses its local radio. Paired devices stop working until it is back, unless a LAN gateway reaches them. Pairings, keys and the rfd configuration stay untouched.'));
            if (pv.devices.length) paras.push(t('Paired BidCos-RF devices ({n}):', {n: pv.devices.length}) + '\n' + pv.devices.map((d) => `${d.address} · ${d.type}`).join('\n'));
            if (pv.devices_error) paras.push(t('rfd did not answer, so the list may be incomplete: {error}', {error: pv.devices_error}));
        }
        // openccu-lite task 317 (D-120): every move of HmIP-RF rewrites its identity files - the
        // danger dialog, and the daemon refuses the move without confirm
        const move = !!pv.hmip_move || !!pv.hmip_off;
        await ask({
            title: t('Change the radio connections?'),
            message: paras.join('\n\n'),
            confirm: t('Change'),
            danger: pv.bidcos_lost || move,
            focusCancel: pv.bidcos_lost || move,
            run: async () => {
                try {
                    await api.put('/api/system/v1/radio/connections', {...choice, confirm: pv.bidcos_lost || move});
                } catch (e) {
                    const r = e instanceof ApiError && e.code === 'hmip-firmware' ? refusalOf(e.detail) : null;
                    throw r ? new Error(firmwareRefusalLines(r, 'change', t).join(' ')) : e;
                }
                touched = false;
                wasRunning = true;
                await load();
            },
        });
    }

    // openccu-lite B-302: how long until the previous module's counter has caught up, in words
    function waitText(until: string): string {
        const ms = new Date(until).getTime() - Date.now();
        if (ms <= 60_000) return t('a moment');
        if (ms < 3_600_000) return t('{n} minutes', {n: Math.ceil(ms / 60_000)});
        if (ms < 48 * 3_600_000) return t('{n} hours', {n: Math.ceil(ms / 3_600_000)});
        return t('{n} days', {n: Math.ceil(ms / 86_400_000)});
    }
    function gapLines(g: CounterGap): string {
        return t('The HmIP security counter of module {to} is {n} behind what module {from} has sent. HmIP devices drop commands with a lower counter than the highest they accepted: devices that heard module {from} may ignore this system\'s commands (switching, configuration) until about {until} - about {wait}; their own reports still arrive. The counter catches up at the first start of HmIP-RF after that time. Nothing is written to the identity files for this. To avoid the wait, stay on the module in use.', {to: g.to, from: g.from, n: g.behind.toLocaleString(dateLocale()), until: dateTime(g.until), wait: waitText(g.until)});
    }

    // openccu-lite B-285: the way back after a module move, from the snapshot the change kept;
    // confirmed by typing the system's host name, as the fresh start is - B-302: with the expected
    // wait of the security counter before the user confirms
    async function moveBack(mb: MoveBack) {
        const host = st?.hostname ?? '';
        const message = [
            t('HmIP-RF goes back to module {module}. Its identity and its device files from before the move ({n} devices) return, the HmIP network is known on it again without eQ-3\'s key server, and the connections are set as they were before the move.', {module: mb.previous, n: mb.devices}),
            t('The identity the exchange made for the module in use now is moved aside into a kept identity of its own. Devices paired since the move have to be paired again.'),
            ...(mb.counter_gap ? [gapLines(mb.counter_gap)] : []),
            t('The radio is unavailable for about a minute. The snapshot is consumed.'),
            t('Type the host name {host} to confirm.', {host}),
        ].join('\n\n');
        const typed = await askText({
            title: t('Back to module {module}?', {module: mb.previous}),
            message,
            input: {label: t('Host name'), placeholder: host},
            confirm: t('Back to the previous module'),
            danger: true,
            focusCancel: true,
        });
        if (typed === null) return;
        if (!sameHostname(typed, host)) {
            err = t('The name typed is not this system\'s host name; nothing happened.');
            return;
        }
        try {
            await api.post('/api/system/v1/radio/hmip/module-move/back', {confirm: true, hostname: typed});
            err = '';
            touched = false;
            wasRunning = true;
            await load();
        } catch (e) {
            err = (e as Error).message;
        }
    }

    function reset() {
        if (!st) return;
        touched = false;
        pickHmIP = hmipValue(st.choices);
        pickBidCos = st.choices.bidcos;
    }

    onMount(() => {
        void load();
        unitPoll = setInterval(() => life.active && loadUnits(), 10_000);
        const stopReturn = life.onReturn(() => void loadUnits());
        return () => {
            clearTimeout(poll);
            clearInterval(unitPoll);
            stopReturn();
        };
    });
</script>

{#if st?.available && st.plan}
    {@const p = st.plan}
    <h2 id="connections">{t('Connections')}<Help>{t('Each interface process uses at most one local radio module: a module on the header, on an HB-RF-USB or HB-RF-ETH, or a USB stick. LAN gateways, HmIP-HAPs and DRAPs are not chosen here and can be several. Automatic does what OpenCCU does with the same hardware. A choice is written into rfd.conf and hmip_user.conf, never flashes firmware, and stays when a chosen module is missing.')}</Help></h2>
    {#if err}<div class="ol-warn">{err}</div>{/if}
    {#if st.hmip_fatal}
        <!-- D-102: hmipserver's last start failed on a known fatal error; the unit waits for the next run.
             Task 155 (D-106): a rejected adapter exchange says which of its two causes it was and offers
             the retry or the guided fresh start (HmIPExchange) -->
        {#if st.hmip_fatal.code === 'adapter-exchange-rejected'}
            <HmIPExchange fatal={st.hmip_fatal} {admin} ondone={(kind) => { exchangeDone = kind; void load(); }} />
        {:else if st.hmip_fatal.code !== 'no-module' || p.hmip}
            <!-- task 147: no-module is said by the missing-module notices below -->
            <div class="ol-notice error" data-notice="hmip-fatal">{t('HmIP-RF is down: hmipserver stopped on a known error ({code}).', {code: st.hmip_fatal.code})}</div>
        {/if}
    {:else if exchangeDone}
        <div class="ol-notice" data-notice="hmip-exchange-done">
            {#if exchangeDone === 'fresh-start'}
                {t('HmIP-RF is starting with an empty network. Then pair your HmIP devices again: with their keys on the Keys page a device is paired from its QR code, without eQ-3\'s key server.')}
                <a href="/system/keys#device-keys" use:link>{t('HmIP device keys')}</a>
            {:else}
                {t('HmIP-RF is restarting and tries the move to this module again.')}
            {/if}
        </div>
    {/if}
    {#if st.hmip_counter_gap && !st.running}
        {@const g = st.hmip_counter_gap}
        <!-- openccu-lite B-302: after the way back, until the counter has caught up -->
        <div class="ol-notice warn" data-notice="hmip-counter-gap" data-until={g.until}>{t('HmIP-RF is back on module {to}, whose HmIP security counter is {n} behind what module {from} sent. Devices that heard module {from} may ignore this system\'s commands until about {until}; the counter catches up at the first start of HmIP-RF after that time, and this notice goes then.', {to: g.to, from: g.from, n: g.behind.toLocaleString(dateLocale()), until: dateTime(g.until)})}</div>
    {/if}
    {#if st.hmip_move_back && !st.running}
        <!-- openccu-lite B-285: the snapshot the move kept is the way back to the previous module -->
        <div class="ol-notice" data-notice="hmip-move-back">
            {t('HmIP-RF was moved away from module {module} on {when}; the system kept that module\'s identity and device files ({n} devices). Back on that module, the HmIP network is known again without eQ-3\'s key server.', {module: st.hmip_move_back.previous, when: dateTime(st.hmip_move_back.at), n: st.hmip_move_back.devices})}
            {#if admin}<button type="button" class="hmm-button" onclick={() => st?.hmip_move_back && moveBack(st.hmip_move_back)}>{t('Back to the previous module…')}</button>{/if}
        </div>
    {/if}
    <!-- openccu-lite task 275: after a device import from another module's backup, how hmipserver's
         move of the identity onto this module went, with the retry and the battery hint -->
    <DevicesImportNotice {admin} fatal={!!st.hmip_fatal} onretry={() => { exchangeDone = 'retry'; void load(); }} />
    {#if p.missing_hmip && p.hmip_pin === p.missing_hmip}
        <!-- openccu-lite task 318 (D-120): the module holding the HmIP network is not replaced
             unasked; the move is the choice below, confirmed -->
        <div class="ol-notice error" data-notice="missing-hmip" data-pinned>{t('The module that holds the HmIP network ({id}) is missing. hmipserver cannot start without it, so HmIP-RF and the virtual devices are down until it is back; no other module is taken without asking, since the network would have to move. To move HmIP-RF to another module, choose it for HmIP-RF below and confirm the change.', {id: p.missing_hmip})}</div>
    {:else if p.missing_hmip}<div class="ol-notice error" data-notice="missing-hmip">{t('The module chosen for HmIP-RF ({id}) is missing. hmipserver cannot start without it, so HmIP-RF and the virtual devices are down until it is back.', {id: p.missing_hmip})}</div>
    {:else if !p.hmip && p.hmipserver_jar === 'HMIPServer.jar' && st.mode !== 'HM-LGW'}
        <!-- occulited task 147: HMIPServer.jar waits for its module for good; no fallback to HMServer.jar -->
        <div class="ol-notice error" data-notice="no-hmip-module">{t('No HmIP radio module was found. hmipserver (HMIPServer.jar) cannot start without one, so HmIP-RF and the virtual devices are down. Connect the module, or set HmIP-RF to None below: hmipserver then runs HMServer.jar with the virtual devices only.')}</div>{/if}
    {#if p.missing_bidcos}<div class="ol-notice error" data-notice="missing-bidcos">{t('The module chosen for BidCos-RF ({id}) is missing. rfd runs without a local radio until it is back.', {id: p.missing_bidcos})}</div>{/if}
    {#if st.mode === 'HM-LGW'}
        <div class="ol-notice">{t('The system runs in LAN-gateway mode: the connections are not chosen here.')}</div>
    {:else}
        <!-- task 150: the two interface processes side by side on the modules' track (three to a row on
             a desktop), multimacd below and spanning the two it serves; each panel says what it uses
             now, and its dropdown sits at its foot so they line up across panels -->
        <div class="conn-wrap">
            <div class="conn-grid">
                {#snippet hmipCard(st: Status, p: Plan)}
                <div class="ol-card" data-process="hmipserver">
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles"><div class="ol-card-title">HmIP-RF</div><div class="ol-card-sub">{#if units.hmipserver}<span class={`ol-dot ${statusDot(units.hmipserver)}`} title={unitWord(units.hmipserver)} data-unit="hmipserver" data-state={statusKind(units.hmipserver)}></span>{/if}hmipserver</div></div>
                    </div>
                    <p class="conn-now"><strong>{st.choices.hmip ? t('Chosen') : t('Automatic')}:</strong> {hmipText(p, st.choices)}</p>
                    {#if p.hmip}
                        <!-- the maintainer, 2026-09-22: the sentence says what the module can or
                             cannot do, and the mark before it says which of the two at a glance -->
                        <p class="ol-muted meta conn-can" data-routing={p.hmip_advanced ? 'yes' : 'no'}>
                            <span class={p.hmip_advanced ? 'conn-mark ok' : 'conn-mark no'}><Icon name={p.hmip_advanced ? 'check-circle' : 'x-circle'} size={15} /></span>
                            {p.hmip_advanced ? t('HmIP-HAPs and DRAPs can route through this module.') : t('No routing through HmIP-HAPs or DRAPs: that needs an RPI-RF-MOD or an HmIP-RFUSB.')}
                        </p>
                    {/if}
                    <p class="ol-muted meta conn-hmip-none">{t('None: no HmIP at all. hmipserver runs HMServer.jar with the virtual devices only, and a module that carries both protocols serves BidCos-RF alone. Automatic and a chosen module always run HMIPServer.jar, which cannot start without its module.')}</p>
                    <label class="conn-pick">
                        <span>{t('Module')}</span>
                        <select class="hmm-select" bind:value={pickHmIP} disabled={!admin || !!st.running} onchange={() => (touched = true)} aria-label={t('Module for HmIP-RF')}>
                            <option value="|">{t('Automatic')}</option>
                            {#each hmipEntries(st.options.hmip) as e (e.value)}<option value={e.value}>{e.label}</option>{/each}
                            {#if curHmIP !== '|' && curHmIP !== 'none|' && !hmipEntries(st.options.hmip).some((e) => e.value === curHmIP)}
                                <option value={curHmIP}>{st.options.hmip.some((o) => o.id === st?.choices.hmip) ? hmipChoiceLabel(curHmIP, st.options.hmip) : `${st.choices.hmip || t('Automatic')} · ${t('missing')}`}</option>
                            {/if}
                            <option value="none|">{t('None (virtual devices only)')}</option>
                        </select>
                    </label>
                    {#each hmipOnlySticks(st.options.hmip) as o (o.id)}
                        <p class="ol-muted meta conn-hmip-only" data-hmip-only={o.id}>{t('{hardware} {id} runs the HmIP-only firmware {version}: BidCos-RF cannot use it, and hmipserver reaches it directly only, not through multimacd. The DualCoPro firmware adds BidCos-RF; the radio firmware section flashes it.', {hardware: o.hardware, id: o.id, version: o.version ?? '?'})} <a href="/system/updates#radio-firmware" use:link>{t('Radio firmware')}</a></p>
                    {/each}
                </div>
                {/snippet}
                {#snippet rfdCard(st: Status, p: Plan)}
                <div class="ol-card" data-process="rfd">
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles"><div class="ol-card-title">BidCos-RF</div><div class="ol-card-sub">{#if units.rfd}<span class={`ol-dot ${statusDot(units.rfd)}`} title={unitWord(units.rfd)} data-unit="rfd" data-state={statusKind(units.rfd)}></span>{/if}rfd</div></div>
                    </div>
                    <p class="conn-now"><strong>{st.choices.bidcos ? t('Chosen') : t('Automatic')}:</strong> {bidcosText(p, st.choices)}</p>
                    {#if admin && p.rfd_lan_gateway && (p.rfd_local || p.rfd_usb_adapter) && pickBidCos !== 'none'}
                        <button type="button" class="hmm-button conn-lgw-only" disabled={!!st.running} onclick={() => { pickBidCos = 'none'; touched = true; }}>{t('Use the LAN gateways only')}</button>
                    {/if}
                    <label class="conn-pick">
                        <span>{t('Module')}</span>
                        <select class="hmm-select" bind:value={pickBidCos} disabled={!admin || !!st.running} onchange={() => (touched = true)} aria-label={t('Module for BidCos-RF')}>
                            <option value="">{t('Automatic')}</option>
                            {#each st.options.bidcos as o (o.id)}<option value={o.id}>{bidcosLabel(o)}</option>{/each}
                            {#if st.choices.bidcos && st.choices.bidcos !== 'none' && !st.options.bidcos.some((o) => o.id === st?.choices.bidcos)}<option value={st.choices.bidcos}>{st.choices.bidcos} · {t('missing')}</option>{/if}
                            <option value="none">{noneLabel(p.rfd_lan_gateway)}</option>
                        </select>
                    </label>
                </div>
                {/snippet}
                <!-- the two panels in the module cards' order above, so each stands under its module -->
                {#if hmipFirst}{@render hmipCard(st, p)}{@render rfdCard(st, p)}{:else}{@render rfdCard(st, p)}{@render hmipCard(st, p)}{/if}
                <div class="ol-card conn-mmd" data-process="multimacd">
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles"><div class="ol-card-title">multimacd</div><div class="ol-card-sub">{#if units.multimacd}<span class={`ol-dot ${statusDot(units.multimacd)}`} title={unitWord(units.multimacd)} data-unit="multimacd" data-state={statusKind(units.multimacd)}></span>{/if}{t('shares one module between rfd and hmipserver')}</div></div>
                    </div>
                    {#if p.multimacd.run}
                        <!-- top: the nodes it provides and who opens them; bottom: the node it opens -->
                        <dl class="ol-kv conn-mmd-provides">
                            <dt class="hmm-mono">/dev/mmd_bidcos</dt><dd>{p.rfd.node === '/dev/mmd_bidcos' ? t('↑ rfd (BidCos-RF)') : t('not used')}</dd>
                            <dt class="hmm-mono">/dev/mmd_hmip</dt><dd>{p.hmipserver.node === '/dev/mmd_hmip' ? t('↑ hmipserver (HmIP-RF)') : t('not used')}</dd>
                        </dl>
                        <p class="conn-mmd-uses"><span class="ol-muted">{t('connected to')}</span> <span class="hmm-mono">{p.multimacd.node}</span>{multimacdModule(p) ? ` · ${multimacdModule(p)}` : ''}</p>
                    {:else}
                        <p class="conn-now">{t('not needed')}</p>
                        <p class="ol-muted meta conn-mmd-uses">{t('It runs when BidCos-RF uses a module on the header, an HB-RF-USB or HB-RF-ETH, or when HmIP is set to go through it.')}</p>
                    {/if}
                </div>
            </div>
        </div>
        {#if admin}
            <div class="ol-toolbar conn-toolbar">
                <button type="button" class="hmm-button primary" disabled={!dirty || !!st.running} onclick={apply}>{t('Apply changes')}</button>
                {#if dirty}<button type="button" class="hmm-button" onclick={reset}>{t('Reset')}</button>{/if}
            </div>
        {/if}
        {#if st.running}
            <h3>{t('Change running')}</h3>
            <pre class="ol-log conn-log">{st.running.lines.join('\n')}</pre>
        {:else if st.last}
            <details class="conn-last">
                <summary>{t('Last change')} <span class="ol-muted">· {dateTime(st.last.started)} · {st.last.ok ? t('succeeded') : t('failed')}</span></summary>
                {#if !st.last.ok}<div class="ol-notice error">{st.last.error}</div>{/if}
                <pre class="ol-log conn-log">{st.last.lines.join('\n')}</pre>
            </details>
        {/if}
    {/if}
{/if}

<style>
    .conn-now { margin: 10px 0 4px; }
    /* the dropdown at the panel's foot, so the dropdowns line up across panels of different height */
    .conn-pick { display: flex; flex-direction: column; gap: 4px; margin-top: auto; padding-top: 12px; }
    .conn-pick select { max-width: 100%; }
    .conn-lgw-only { margin-top: 8px; align-self: flex-start; }
    .conn-wrap { container-type: inline-size; }
    /* the modules' track (.ol-cards-radio): three to a row within the page's 1200 px */
    .conn-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr)); gap: 12px; align-items: stretch; }
    .conn-grid > .ol-card { display: flex; flex-direction: column; }
    /* multimacd under the two it serves, spanning them once they stand side by side */
    @container (min-width: 692px) { .conn-mmd { grid-column: 1 / span 2; } }
    .conn-mmd-provides { margin: 10px 0 0; }
    .conn-mmd-uses { margin: auto 0 0; padding-top: 12px; }
    .conn-toolbar { margin-top: 14px; }
    .meta { margin: 4px 0 0; font-size: 0.9em; }
    /* the mark keeps the sentence's first line company and the rest of it is indented under the
       text, not under the icon */
    .conn-can { display: flex; align-items: flex-start; gap: 6px; }
    .conn-mark { flex: 0 0 auto; line-height: 0; margin-top: 1px; }
    .conn-mark.ok { color: var(--hmm-ok); }
    .conn-mark.no { color: var(--hmm-error); }
    .conn-log { white-space: pre-wrap; word-break: break-word; max-height: 240px; overflow: auto; }
    .conn-last { margin-top: 8px; }
</style>
