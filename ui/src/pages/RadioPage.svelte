<script lang="ts">
    import {onMount} from 'svelte';
    import {pageLife} from '../lib/pagelife.svelte';
    import {api, type Radio, type RadioModule, type USBDevice, type RadioFirmware, type FeedStatus, type DeviceDescriptions} from '../lib/api';
    import {auth} from '../lib/auth.svelte';
    import {link} from '../lib/router.svelte';
    import {ask} from '../lib/dialog.svelte';
    import {t} from '../lib/i18n.svelte';
    import Loading from '../lib/Loading.svelte';
    import Disclosure from '../lib/Disclosure.svelte';
    import SystemTitle from '../lib/SystemTitle.svelte';
    import RadioConnections, {type ConnView} from '../lib/RadioConnections.svelte';
    import SectionHead from '../lib/SectionHead.svelte';
    import Subscribers from '../lib/Subscribers.svelte';
    import type {SubSummary} from '../lib/subscribers';
    import Icon from '../lib/Icon.svelte';
    import {carrierOf} from '../lib/radiocarrier';
    import type {HealthSample} from '../lib/history';
    import {anyStarting, detecting, type InterfaceUnit} from '../lib/starting';
    import WarnEdge from '../lib/WarnEdge.svelte';
    import {warnEdge, warnFor} from '../lib/systemmenu.svelte';

    // task 68: carrier_sense_source says where the level comes from - the interface list, or channel 0
    // of the module's own device; "device" without a value is a module that has not reported it yet
    interface RadioIf { interface: string; address: string; type: string; description?: string; connected: boolean; default: boolean; firmware?: string; duty_cycle: number; carrier_sense?: number; carrier_sense_source?: 'interface' | 'device' }
    // task 94: `units` are the radio stack's units - a process card says "starting" for one systemd is still starting
    interface Health { polled: string; interfaces: RadioIf[]; errors: Record<string, string>; history: Record<string, HealthSample[]>; busy: boolean; busy_interface: string; units?: InterfaceUnit[]; feed?: FeedStatus; subscribers?: Record<string, SubSummary> }
    // the gateways' keys are the Keys page's; the list itself is System → LAN devices' (task 222)
    interface Extra { security_key_set: boolean; security_key_known: boolean }
    const life = pageLife();
    let radio = $state<(Radio & Extra) | null>(null);
    let busy = $state('');
    let notice = $state('');
    async function reloadRadio() {
        radio = await api.get<Radio & Extra>('/api/system/v1/radio');
    }
    // openccu-lite B-272: the cards above come from /var/hm_mode, so only a module in a role had one -
    // a module on an HB-RF-ETH added while both processes are pinned to a stick appeared nowhere. The
    // connections' status lists every detected module with its roles: the ones in none get a card of
    // their own, and a configured board whose module is not detected yet is said, with Try now.
    let conn = $state<ConnView | null>(null);
    let connView = $state<RadioConnections | undefined>(undefined);
    const unusedModules = $derived((conn?.modules ?? []).filter((m) => m.roles.length === 0));
    const boardPending = $derived(conn?.hb_rf_eth && !conn.hb_rf_eth.detected ? conn.hb_rf_eth : null);
    let boardBusy = $state(false);
    let boardErr = $state('');
    async function tryBoard() {
        if (!boardPending) return;
        boardBusy = true;
        boardErr = '';
        try {
            // the LAN devices page's PUT with the same address: the system tries the board at once
            await api.put('/api/system/v1/radio/hb-rf-eth', {address: boardPending.address});
            connView?.reload();
        } catch (e) {
            boardErr = (e as Error).message;
        } finally {
            boardBusy = false;
        }
    }
    // the maintainer, 2026-09-20: the radio firmware section is the Updates page's; the module
    // cards here still say what a module runs and whether a newer file waits there
    // (GET /radio/firmware; a user's session gets 403 and sees neither).
    let fw = $state<RadioFirmware | null>(null);
    const admin = $derived(auth.role === 'admin');
    // D-66: rfd's device descriptions - the image's plus what addons added in the writable layer
    // (GET /radio/device-descriptions); the section shows only where the image has the unit
    let dd = $state<DeviceDescriptions | null>(null);
    let ddBusy = $state(false);
    async function loadDD() {
        try {
            dd = await api.get<DeviceDescriptions>('/api/system/v1/radio/device-descriptions');
        } catch {
            dd = null;
        }
    }
    async function resetDD() {
        const yes = await ask({
            title: t('Reset device descriptions'),
            message: t('Puts back every device description of the image an addon replaced or removed; what addons added stays. rfd is restarted, so BidCos-RF is unavailable for a moment.'),
            confirm: t('Reset'),
            danger: true,
        });
        if (!yes) return;
        ddBusy = true;
        try {
            const r = await api.post<DeviceDescriptions>('/api/system/v1/radio/device-descriptions/reset', {});
            dd = r;
            notice = r.restarted ? t('Device descriptions reset; rfd restarted.') : t('Device descriptions reset.');
        } catch (e) {
            notice = (e as Error).message;
        } finally {
            ddBusy = false;
        }
    }
    function ddMode(d: DeviceDescriptions) {
        if (d.mode === 'overlay') return t('an overlay on the userfs');
        if (d.mode === 'copy') return t('a copy on the userfs (this kernel has no overlayfs)');
        return t("not writable: the directory is the image's alone");
    }
    async function loadFirmware() {
        try {
            fw = await api.get<RadioFirmware>('/api/system/v1/radio/firmware');
        } catch {
            fw = null;
        }
    }
    /** the coprocessor module of a radio module, by its device name */
    const fwOf = (m: RadioModule) => fw?.modules.find((x) => x.device === m.device);
    const runningVersion = (m: RadioModule) => m.firmware || fwOf(m)?.running_version || '';
    const updateWaiting = (m: RadioModule) => fwOf(m)?.verdict === 'newer-available';

    // task 42: the USB devices, read when the section is opened and on Refresh - no poll, the
    // list changes when a stick is plugged in and at no other time. Hubs (the root hubs, the
    // Pi's on-board one) are folded away unless asked for: the one device that matters here is
    // the radio.
    let usb = $state<USBDevice[] | null>(null);
    let usbOpen = $state(false);
    let usbButton = $state<HTMLButtonElement | null>(null);
    let usbErr = $state('');
    let usbBusy = $state(false);
    let showHubs = $state(false);
    async function loadUSB() {
        usbBusy = true;
        try {
            usb = (await api.get<{devices: USBDevice[]}>('/api/system/v1/usb')).devices;
            usbErr = '';
        } catch (e) {
            usbErr = (e as Error).message;
        } finally {
            usbBusy = false;
        }
    }
    $effect(() => {
        if (usbOpen && !usb && !usbBusy) void loadUSB();
    });
    // nested by the port path: the depth is the topology's ("1-1.3" hangs on "1-1" on "usb1");
    // with the hubs hidden the remaining rows lose the root hub's level
    const usbRows = $derived.by(() => {
        const list = usb ?? [];
        return list
            .filter((d) => showHubs || !d.hub)
            .map((d) => ({d, depth: Math.max(0, (d.parent ? 1 + d.path.split('.').length - 1 : 0) - (showHubs ? 0 : 1))}));
    });
    const usbName = (d: USBDevice) => {
        const n = d.product_name || '';
        const m = d.manufacturer && (!n || !n.toLowerCase().includes(d.manufacturer.toLowerCase())) ? d.manufacturer : d.vendor_name && !n.includes(d.vendor_name) ? d.vendor_name : '';
        return n ? (m ? `${n} (${m})` : n) : m || '—';
    };
    // task 150: the module cards name their USB device - read once with the page, joined on the
    // raw-uart the firmware assigned it, or the HM-CFG-USB-2 by its id (it has no raw-uart)
    let modUSB = $state<USBDevice[]>([]);
    function usbOf(m: RadioModule): USBDevice | undefined {
        return modUSB.find((d) => (m.device_node && d.radio?.device_node === m.device_node) || (/HM-CFG-USB/i.test(m.device) && `${d.vendor}:${d.product}` === '1b1f:c00f'));
    }
    function showModule(protocol: string) {
        document.getElementById(`ol-module-${protocol}`)?.scrollIntoView({block: 'center', behavior: 'smooth'});
    }
    let health = $state<Health | null>(null);
    let error = $state('');
    async function loadHealth() {
        try {
            health = await api.get<Health>('/api/system/v1/radio/health');
        } catch {
            health = null;
        }
        // task 94: /var/hm_mode and InterfacesList.xml can appear after the page was opened during a
        // boot - the detection writes the one, occu-init-hs485d rewrites the other. The inventory is
        // read again while the box knows no module yet, or while the radio stack is starting.
        if (radio && (radio.modules.length === 0 || anyStarting(health?.units))) await reloadRadio().catch(() => undefined);
    }
    onMount(() => {
        // task 94: the radio load every 30 s, and every 5 s while the radio stack is starting - at boot
        // its starting states end, and the modules and processes arrive, within seconds of each other.
        // Each poll is timed from the end of the last, so a slow answer never overlaps the next.
        let timer: ReturnType<typeof setTimeout> | undefined;
        let mounted = true;
        const tick = async () => {
            if (life.active) await loadHealth(); // task 177: a kept page polls only while it shows
            if (mounted) timer = setTimeout(tick, anyStarting(health?.units) ? 5000 : 30000);
        };
        void tick();
        // the firmware list is an administrator's (it names paths on the box): a user's session is
        // not sent to be refused, which left a 403 in the console on every visit
        if (admin) void loadFirmware();
        void loadDD();
        api.get<{devices: USBDevice[]}>('/api/system/v1/usb').then((r) => (modUSB = r.devices ?? []), () => undefined);
        void (async () => {
            try {
                radio = await api.get<Radio & Extra>('/api/system/v1/radio');
            } catch (e) {
                error = (e as Error).message;
            }
        })();
        // task 177: back from another page - the radio, its load and the firmware as they are now
        const stopReturn = life.onReturn(() => {
            void reloadRadio().catch(() => undefined);
            void loadHealth();
            if (admin) void loadFirmware();
            void loadDD();
        });
        return () => {
            stopReturn();
            mounted = false;
            clearTimeout(timer);
        };
    });
</script>

<!-- task 131 (D-86): a System page now, so its heading is the title switcher (System › Interfaces ▾) -->
<SystemTitle />
{#if !radio}
    <Loading {error} />
{:else}
    <!-- the section's heading and its own action (task 199; the maintainer, 2026-09-24, task 222:
         "move the usb-devices, add gateway and search again buttons below the area headings"). The
         USB list is the modules' diagnostic, so its trigger stands here, its panel above the cards. -->
    {#snippet usbAction()}
        <!-- task 268: the button becomes the panel; its Close brings the button back -->
        <button type="button" class="hmm-button" aria-expanded={usbOpen} onclick={() => (usbOpen = true)} bind:this={usbButton} data-usb-toggle>{t('USB devices')}</button>
    {/snippet}
    <SectionHead id="modules" title={t('Modules')} actions={usbAction} />
    <div class="ol-cards ol-cards-radio ol-panelrow">
        <!-- task 42: the USB list, collapsed - a diagnostic for the moment a stick is plugged in
             and the box does not see it, or sees it as something else -->
        <Disclosure title={t('USB devices')} bind:open={usbOpen} readOnly trigger={usbButton}>
            {#snippet help()}{t('Read from /sys/bus/usb/devices - what the kernel says about each device, its driver and the device nodes it produced. A stick that carries a radio module is marked with the protocols the firmware gave it.')}{/snippet}
            {#if usbErr}<div class="ol-warn">{usbErr}</div>{/if}
            {#if usb}
                <div class="ol-toolbar">
                    <label><input type="checkbox" bind:checked={showHubs} /> {t('Show hubs')}</label>
                    <button class="hmm-button" disabled={usbBusy} onclick={loadUSB}>{t('Refresh')}</button>
                </div>
                {#if usb.length === 0}
                    <div class="ol-muted">{t('No USB devices.')}</div>
                {:else if usbRows.length === 0}
                    <div class="ol-muted">{t('No USB device besides the hubs.')}</div>
                {:else}
                    <div class="ol-scroll">
                        <table class="ol-table ol-tree ol-usb">
                            <thead><tr><th>{t('Name')}</th><th>ID</th><th>{t('Port')}</th><th>{t('Speed')}</th><th>{t('Serial')}</th><th>{t('Driver')}</th><th>{t('Device nodes')}</th><th>{t('Radio module')}</th></tr></thead>
                            <tbody>
                                {#each usbRows as {d, depth} (d.path)}
                                    <tr class:child={depth > 0} class:hub={d.hub}>
                                        <td class="usb-name" style={`padding-left:${8 + depth * 16}px`} title={usbName(d)}>{usbName(d)}</td>
                                        <td class="hmm-mono">{d.vendor}:{d.product}</td>
                                        <td><span class="hmm-mono">{d.path}</span> <span class="ol-muted">· Bus {d.bus} Dev {d.dev}</span></td>
                                        <td>{d.speed ? `${d.speed} Mbit/s` : ''}</td>
                                        <td class="hmm-mono">{d.serial ?? ''}</td>
                                        <td class="hmm-mono">{d.driver ?? ''}</td>
                                        <td class="hmm-mono">{d.nodes.join(', ')}</td>
                                        <td>
                                            {#if d.radio}
                                                {d.radio.protocols.join(', ')}
                                                {#if d.radio.protocols[0]}
                                                    <button type="button" class="hmm-button" onclick={() => showModule(d.radio!.protocols[0]!)}>{t('Show module')}</button>
                                                {/if}
                                            {/if}
                                        </td>
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {/if}
            {:else if !usbErr}
                <Loading />
            {/if}
        </Disclosure>
    </div>
    <!-- task 137: an HmIP-RFUSB the detection found but could not read is in no role, so it has no
         card here; it is said, with the way to the flash -->
    {#each (fw?.modules ?? []).filter((x) => x.verdict === 'unusable') as u (u.device_node)}
        <div class="ol-notice warn" data-module-unusable={u.device_node}>{t('The radio module {device} at {node} does not answer with a usable firmware: the system has no radio on it. Flash it in the radio firmware section to use it.', {device: u.device, node: u.device_node})} <a href="/system/updates#radio-firmware" use:link>{t('Radio firmware')}</a></div>
    {/each}
    {#if boardPending}
        <!-- openccu-lite B-272: an HB-RF-ETH configured under LAN devices whose module is not in the
             detection yet - just added (the radio hotplug attaches it in seconds) or not answering
             (the system tries it again by itself); the connections' status is read again meanwhile -->
        <div class="ol-notice" data-notice="hb-rf-eth-pending" data-board-address={boardPending.address}>
            <span class="ol-dot starting"></span>
            {#if boardPending.connected}
                {t('The HB-RF-ETH at {address} is connected; its radio module is being detected.', {address: boardPending.address})}
            {:else}
                {t('The HB-RF-ETH at {address} is configured but does not answer yet. The system tries it again by itself; its module appears here once the board answers.', {address: boardPending.address})}
            {/if}
            <a href="/system/lan-devices#hb-rf-eth" use:link>{t('LAN devices')}</a>
            {#if admin && !boardPending.connected}
                <button type="button" class="hmm-button" onclick={tryBoard} disabled={boardBusy} data-action="try-board">{t('Try now')}</button>
            {/if}
            {#if boardErr}<div class="ol-warn">{boardErr}</div>{/if}
        </div>
    {/if}
    {#if conn?.header_silent}
        {@const h = conn.header_silent}
        <!-- openccu-lite B-300: an empty GPIO header has no card; this is said only while a chosen
             module is missing, where the silent header may be the module that went quiet -->
        <div class="ol-notice warn" data-notice="header-silent" data-node={h.node}>{t('No radio module answered on the GPIO header ({node}: {why}), and the module chosen under Connections ({ids}) is missing. Check that the module is seated, or choose another one.', {node: h.node, why: h.detail || h.probe, ids: h.missing.join(', ')})}</div>
    {/if}
    {#if radio.modules.length === 0 && unusedModules.length === 0}
        {#if detecting(health?.units)}
            <!-- task 94: the detection writes /var/hm_mode seconds after the web UI is up (22 s on a Pi 4
                 with an HmIP-RFUSB): until then the box knows no module, and that is not "none" -->
            <div class="ol-notice" data-notice="detecting"><span class="ol-dot starting"></span>{t('The radio module detection is still running.')}</div>
        {:else}
            <div class="ol-notice">{t('No radio module detected.')}</div>
        {/if}
    {:else}
        <div class="ol-cards ol-cards-radio">
            {#each radio.modules as m (m.protocol)}
                <!-- occulited task 12: the radio's load (openccu-lite task 316) is the modules' -->
                <div class="ol-card {warnEdge(['radio-load'])}" id={`ol-module-${m.protocol}`} {...warnFor(['radio-load'])}>
                    <!-- task 53: the card's head row, the protocol as its title -->
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles"><div class="ol-card-title">{m.protocol}</div></div>
                    </div>
                    <dl class="ol-kv" style="margin-top:10px">
                        <!-- task 240: the module, and on the next line what carries it -->
                        <dt>{t('Device')}</dt><dd data-module-device>{m.device}</dd>
                        {#if carrierOf(m.device_type)}
                            {@const c = carrierOf(m.device_type)!}
                            <dt>{t('Via')}</dt><dd class="hmm-mono ol-module-via" data-module-via title={c.full}>{c.via}</dd>
                        {/if}
                        {#if m.serial}<dt>{t('Serial')}</dt><dd class="hmm-mono">{m.serial}</dd>{/if}
                        {#if m.sgtin}<dt>SGTIN</dt><dd class="hmm-mono">{m.sgtin}</dd>{/if}
                        <!-- the maintainer, 2026-09-20: the running version for every module, not
                             only the ones the interface reports, and a pill to the Updates page
                             while a newer file waits there -->
                        {#if runningVersion(m)}
                            <dt>{t('Firmware')}</dt>
                            <dd>{runningVersion(m)}{#if updateWaiting(m)}{' '}<a class="ol-badge ol-badge-accent" href="/system/updates#radio-firmware" use:link data-fw-update={m.protocol}>{t('update available')}</a>{/if}</dd>
                        {/if}
                        {#if usbOf(m)}
                            {@const d = usbOf(m)!}
                            <!-- task 150: the USB ids and what the device says it is -->
                            <dt>USB</dt><dd class="ol-module-usb"><span class="hmm-mono">{d.vendor}:{d.product}</span> · {usbName(d)}</dd>
                        {/if}
                        {#if m.address}<dt>{t('Address')}</dt><dd class="hmm-mono">{m.address}</dd>{/if}
                        {#if m.address_active}<dt>{t('Active address')}</dt><dd class="hmm-mono">{m.address_active}</dd>{/if}
                    </dl>
                </div>
            {/each}
            <!-- openccu-lite B-272: the detected modules no interface process uses - a module on an
                 HB-RF-ETH added while both processes are pinned to a stick, a second stick -->
            {#each unusedModules as m (m.serial || m.node)}
                <div class="ol-card" id={`ol-module-${m.serial || m.node}`} data-module-unused={m.serial || m.node}>
                    <div class="ol-card-head">
                        <span class="ol-card-icon"><Icon name="radio" size={14} /></span>
                        <div class="ol-card-titles"><div class="ol-card-title">{m.hardware || m.device_type}</div><div class="ol-card-sub">{t('Not used')}</div></div>
                    </div>
                    <dl class="ol-kv" style="margin-top:10px">
                        {#if m.hardware}<dt>{t('Device')}</dt><dd data-module-device>{m.hardware}</dd>{/if}
                        {#if carrierOf(m.device_type)}
                            {@const c = carrierOf(m.device_type)!}
                            <dt>{t('Via')}</dt><dd class="hmm-mono ol-module-via" data-module-via title={c.full}>{c.via}</dd>
                        {/if}
                        {#if m.serial}<dt>{t('Serial')}</dt><dd class="hmm-mono">{m.serial}</dd>{/if}
                        {#if m.sgtin}<dt>SGTIN</dt><dd class="hmm-mono">{m.sgtin}</dd>{/if}
                        {#if m.version}<dt>{t('Firmware')}</dt><dd>{m.version}</dd>{/if}
                        {#if m.node}<dt>{t('Device node')}</dt><dd class="hmm-mono">{m.node}</dd>{/if}
                    </dl>
                    <p class="ol-muted" style="margin:10px 0 0" data-module-unused-why>
                        {#if m.probe === 'ok'}
                            {t('No interface process uses this module. It can be chosen for HmIP-RF or BidCos-RF under Connections.')}
                        {:else}
                            {t('The module did not answer the detection ({why}).', {why: m.detail || m.probe})}
                        {/if}
                    </p>
                </div>
            {/each}
        </div>
    {/if}
    <!-- openccu-lite task 222: the BidCoS gateways, the HmIP access points and the LAN devices are a
         page of their own; the way there stands where they were -->
    <p class="ol-muted if-lan" data-lan-link><a href="/system/lan-devices" use:link>{t('BidCoS gateways, the HB-RF-ETH, HmIP access points and other LAN devices: LAN devices')}</a></p>
    <!-- task 129 phase 3: which module each interface process uses -->
    <!-- occulited task 12: the HmIP warnings of the Status page lead to the connections -->
    <WarnEdge ids={['hmip-adapter', 'hmip-local-swap', 'hmip-module-missing', 'devices-import', 'hmip-security-counter']}><RadioConnections bind:this={connView} {admin} order={(radio.modules ?? []).map((m) => m.protocol)} onchanged={() => void reloadRadio().catch(() => undefined)} onstatus={(s) => (conn = s)} /></WarnEdge>
    <!-- the maintainer, 2026-09-19: the clients on this system after the connections; the ones on
         the network are the Remote access page's -->
    <WarnEdge ids={['rpc-stalled']}><Subscribers {admin} scope="all" feed={health?.feed} streams={health?.subscribers} /></WarnEdge>
    <!-- task 79's RPC trace is the Remote access page's since openccu-lite task 224; no link to it
         here since task 242 (the old #rpc-trace anchor still leads there) -->
    <!-- D-66: rfd's device descriptions, writable through a layer on the userfs so that no addon
         remounts the system partition; the reset puts the image's files back and restarts rfd -->
    {#if dd?.available}
        {#snippet ddReset()}
            <button type="button" class="hmm-button" disabled={ddBusy || busy !== ''} onclick={resetDD} data-dd-reset>{t('Reset to the image')}</button>
        {/snippet}
        <SectionHead id="device-descriptions" title={t('Device descriptions')} help={t("rfd knows a device by its description in /firmware/rftypes. The image brings the descriptions of eQ-3's devices; an addon adds its own there for devices the image does not know. The directory is writable through a layer on the userfs, so no addon has to remount the system partition, and the image's files come back at every boot whatever an addon removed.")} actions={admin ? ddReset : undefined} />
        {#if notice}<div class="ol-notice" data-notice="dd">{notice}</div>{/if}
        <dl class="ol-kv ol-dd" data-dd-mode={dd.mode}>
            <dt>{t('Source')}</dt><dd>{t('{image} from the image, {added} added by addons', {image: dd.image, added: dd.added})}</dd>
            <dt>{t('Writable through')}</dt><dd class:ol-warn={dd.mode === 'none'}>{ddMode(dd)}</dd>
            {#if dd.replaced > 0 || dd.removed > 0}
                <dt>{t('Changed image files')}</dt><dd class="ol-warn">{t('{replaced} replaced, {removed} removed by addons. The next boot restores the removed ones; the reset restores both.', {replaced: dd.replaced, removed: dd.removed})}</dd>
            {/if}
        </dl>
    {/if}
    <!-- task 150: the radio load (duty cycle, carrier sense) is the Status page's alone -->
    {#if health?.busy}<div class="ol-notice">{t('The radio is busy ({i}): pairing and firmware updates should wait.', {i: health.busy_interface})}</div>{/if}
    <!-- task 183: the security key, the HmIP network key and the device keys are on the Keys page
         (/system/keys); the interface processes' panels went with the maintainer's word -->
{/if}

<style>
    .ol-module-via { overflow-wrap: anywhere; }
    /* 27.1: the cards were the shared 220 px track, and a 24-digit SGTIN in the mono face does
       not fit that next to its label - it ran out over the card's right edge. These two grids
       get a wider one; .ol-cards' own 220 px stays for the pages whose cards hold short values.
       The min() keeps a narrow window from overflowing the track it cannot fit. */
    .ol-cards.ol-cards-radio { grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr)); }
    .ol-tree tr.child td:first-child { padding-left: 22px; }
    .ol-tree tr.child td { color: var(--hmm-fg-muted); }
    /* task 42: the USB rows are devices, not subscribers - a child row keeps its colour, a hub is
       the muted one; the table scrolls sideways on a phone rather than the page */
    .ol-usb tr.child td { color: inherit; }
    .ol-usb tr.hub td { color: var(--hmm-fg-muted); }
    .ol-usb tr.child td:first-child { padding-left: 8px; }
    /* task 249: no cell wraps; a long name ends in an ellipsis, the whole one in its title */
    .ol-usb th, .ol-usb td { white-space: nowrap; }
    .ol-usb td.usb-name { max-width: 32ch; overflow: hidden; text-overflow: ellipsis; }
    /* a table wider than a phone scrolls inside its own box; the page never scrolls sideways
       (which also threw a fixed dialog off its place on a phone, task 41) */
    .ol-scroll { overflow-x: auto; max-width: 100%; }
    /* task 199, the maintainer 2026-09-22: a panel that opens from a heading row is as wide as the
       multimacd panel of the connections - two of the three card columns. The wrapper is the card
       grid itself, so the panel lines up with the cards below it, and on a narrow window, where the
       grid has one column, it takes that one. */
    .ol-panelrow { margin-bottom: 0; }
    /* task 249 (the maintainer): the USB devices panel takes the page's whole width */
    .ol-panelrow > :global(.ol-disclosure-slot) { grid-column: 1 / -1; min-width: 0; }
    /* the panel fills the two columns: the shared 720 px reading width is for a panel standing on
       the page, not for one that has to line up with the cards under it */
    .ol-panelrow :global(.ol-disclosure) { max-width: none; }
    .if-lan { margin: 14px 0 0; }
    .ol-dd { max-width: 720px; }
    .ol-cards-radio > .ol-card { display: flex; flex-direction: column; }
</style>
