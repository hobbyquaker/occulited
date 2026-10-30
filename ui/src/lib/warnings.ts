// Task 81: the Status page's warnings as occulited evaluates them (GET /api/system/v1/warnings),
// and their words. occulited decides which warnings are active and keeps the silences - so a
// silence can end when its warning clears while nobody has the page open - and the sentences stay
// here, keyed by id and variant and filled from the params. The page shows the list in the order
// it came, errors first.
import type {StorageReason} from './api';
import {stallSentence, type StallListener} from './rpcstalls';
import {formatDecimal} from './storagepanels';

/** task 366: info is a quiet notice - after the warnings, plain, never on the LED, no dot */
export type Severity = 'error' | 'warning' | 'info';

export interface Silence {
    id: string;
    variant: string;
    by: string;
    at: string;
    until: string;
}

/** an addon a warning names */
export interface WarnAddon {
    id: string;
    name?: string;
    enabled?: boolean;
    /** addon-ownership: the first root-owned entry */
    path?: string;
    /** addon-update: the installed and the available version */
    installed?: string;
    available?: string;
}

/** a unit in a crash loop (openccu-lite task 283) */
export interface WarnLoopUnit {
    unit: string;
    restarts: number;
    total: number;
    since: string;
    result?: string;
    addon?: boolean;
}

/** a device whose firmware is transferred and waits for the install (device-update-ready, occulited task 126) */
export interface WarnUpdate {
    address: string;
    name?: string;
    type?: string;
    version?: string;
}

export interface WarnDevice {
    name: string;
    kind: string;
    model?: string;
}

export interface Warning {
    id: string;
    variant: string;
    severity: Severity;
    params?: {addon?: string; title?: Record<string, string>; text?: Record<string, string>; posted?: string; expires?: string; addons?: WarnAddon[]; updates?: WarnUpdate[]; account?: string; at?: string; path?: string; days?: number; verdict?: string; reasons?: StorageReason[]; devices?: WarnDevice[]; mode?: string; reason?: string; adapter?: string; cause?: string; line?: string; families?: string[]; unreachable?: number; total?: number; port?: number; addresses?: string; sgtin?: string; address?: string; name?: string; detail?: string; label?: string; dir?: string; target?: string; share?: string; reconnecting?: boolean; interface?: string; kind?: string; since?: string; checked?: boolean; listeners?: StallListener[]; folder?: string; device?: string; running?: string; latest?: string; staged?: boolean; newest?: string; node?: string; drops_off?: boolean; hosts?: string[]; issuer?: string; store?: string; candidate?: boolean; from?: string; module?: string; version?: string; channel?: string; url?: string; minimum?: string; count?: number; bytes?: number; units?: WarnLoopUnit[]; fails?: number; failures?: number; restarts?: number; result?: string; first?: string; last?: string; written?: number; calc?: number; current?: number; offset?: number; wraps_at?: string; clock_state?: string; behind?: boolean; source?: string; duty_cycle?: number; carrier_sense?: number; transfer?: boolean; purpose?: string; host?: string; subject?: string; fingerprint?: string; spki?: string; accounts?: {user: string; at: string}[]};
    href?: string;
    /** this administrator's own silence (D-64) */
    silenced?: Silence;
}

export interface WarningsView {
    warnings?: Warning[];
    /** the silence periods in days, for an administrator (D-65) */
    periods?: number[];
}

export type Translate = (key: string, params?: Record<string, string | number>) => string;

export interface Words {
    t: Translate;
    /** an addon as a person knows it: the name it declares, else the catalogue's, else the id (B-69) */
    addonName: (a: WarnAddon) => string;
    /** the storage verdict and the reasons behind it, as the panel says them */
    storage: (w: Warning) => string;
    /** a time as this browser shows it */
    when: (iso: string) => string;
    /** the page's language, for words an addon brings in both (occulited task 119); absent = en */
    lang?: string;
}

/** the periods a silence may have when the answer carries none */
export const PERIODS = [1, 7, 90];

export function warningText(w: Warning, words: Words): string {
    const {t} = words;
    const p = w.params ?? {};
    const addons = p.addons ?? [];
    const names = (list: WarnAddon[]) => list.map(words.addonName).join(', ');
    switch (w.id) {
        case 'rega': {
            // "disabled" is the sentence's word, once (B-69)
            const off = addons.filter((a) => !a.enabled);
            const on = addons.filter((a) => a.enabled);
            return [
                off.length ? t('Disabled incompatible Addon: {list}.', {list: names(off)}) : '',
                on.length ? t('Installed, not usable without ReGa, and still enabled: {list}.', {list: names(on)}) : '',
            ].filter(Boolean).join(' ');
        }
        case 'arch': {
            const off = addons.filter((a) => !a.enabled);
            const on = addons.filter((a) => a.enabled);
            return [
                off.length ? t('Built for another architecture, and therefore disabled: {list}.', {list: names(off)}) : '',
                on.length ? t('Built for another architecture, and still enabled: {list}.', {list: names(on)}) : '',
                t('The repair is a release built for this system.'),
            ].filter(Boolean).join(' ');
        }
        case 'addon-payload':
            // task 146: a restore brought these addons back without their program files; B-267: their units
            // are skipped, not failed
            return t('Addons installed before the restore, now without their program files: {list}. They are not started; reinstall them on the Addons page.', {list: names(addons)});
        case 'addon-ended':
            // B-158: an addon that keeps a program running, and none of it runs
            return t('Addons whose program has ended: {list}. The Addons page starts them again; the log says why they ended.', {list: names(addons)});
        case 'addon-failed':
            // task 248: an addon's unit is in systemd's failed state
            return t('Addons that failed: {list}. The Addons page starts them again; the log says why they failed.', {list: names(addons)});
        case 'crash-loop': {
            // openccu-lite task 283: units that keep failing, each restart hidden by the backoff
            const list = (p.units ?? []).map((u) => t('{unit} ({n} restarts since {when})', {unit: u.unit, n: u.restarts, when: words.when(u.since)})).join(', ');
            return t('Keeps failing and restarting: {list}. The system tries again at longer and longer intervals; the log says why.', {list: list || w.variant});
        }
        case 'occulited-crash-loop':
            // task 283: occulited itself looped; it could not say so until it ran again
            return t('The system service occulited kept failing: {n} failures in a row between {first} and {last}. It runs again; the log of that time says why.', {n: p.fails ?? 0, first: p.first ? words.when(p.first) : '', last: p.last ? words.when(p.last) : ''});
        case 'meta':
            return t('The metadata store had to be recovered from its backup. The last change before the failure may be lost.');
        case 'trust-ca': {
            // openccu-lite task 231: a server presents an authority occulited's store lacks (strict)
            const hosts = (p.hosts ?? [w.variant]).join(', ');
            const base = t('{hosts}: the server presents a certificate from {issuer}, which the {store} trust store does not hold. The call fails until the authority is added.', {hosts, issuer: p.issuer ?? '', store: p.store ?? 'occulited'});
            return p.candidate ? `${base} ${t('The System store holds it: one click on the Trust stores page copies it.')}` : base;
        }
        case 'trust-pin': {
            // openccu-lite task 232: a connection's certificate matched none of its purpose's pins
            const store = p.purpose === 'acme' ? 'ACME' : t('OAuth / OIDC');
            const base = t('{host} presents a certificate none of the {store} store\'s pins match. The connection fails until the key is pinned on the Trust stores page (Re-pin) or the server presents a pinned key again.', {host: String(p.host ?? w.variant), store});
            return p.fingerprint ? `${base} ${t('Presented: {subject}, SHA-256 {fingerprint}.', {subject: String(p.subject ?? ''), fingerprint: String(p.fingerprint)})}` : base;
        }
        case 'app-public':
            return t('Control is public: anyone who reaches the web port operates the house, as {account}, without a login.', {account: String(p.account ?? w.variant)});
        case 'unclean':
            return t('The system was not shut down cleanly before this boot ({when}). Power loss or a hard reset.', {when: p.at ? words.when(p.at) : ''});
        case 'backup-target':
            return t('No backup target: {path} cannot be read, so the nightly backup probably writes nothing.', {path: p.path ?? w.variant});
        case 'backup-userfs': {
            const base = t('The nightly backup writes to {path}, which is on the system itself: it is lost with the system, and it fills the storage the system runs on.', {path: p.path ?? w.variant});
            // B-99: how much it takes there now
            const n = Number(p.count ?? 0);
            if (!n) return base;
            const size = formatDecimal(Number(p.bytes ?? 0), words.lang ?? 'en');
            return `${base} ${n === 1 ? t('The one backup there takes {size}.', {size}) : t('The {n} backups there take {size}.', {n, size})}`;
        }
        case 'backup-unencrypted':
            return t('Backups are not encrypted: anyone who can read the file has every key of this system.');
        case 'backup-delivery': {
            // task 86: a target whose last test or backup failed, or that has had no backup for 26 hours
            const name = String(p.name ?? w.variant);
            const why: Record<string, string> = {
                unreachable: t('The backup target {name} cannot be reached.', {name}),
                'auth-failed': t('The backup target {name} refuses the login.', {name}),
                'host-key-changed': t("The backup target {name} shows another server key than the one confirmed; nothing is written there until you check it.", {name}),
                'host-key-unknown': t("The server key of the backup target {name} is not confirmed yet.", {name}),
                'read-only': t('The backup target {name} cannot be written.', {name}),
                full: t('The backup target {name} is full.', {name}),
                stale: t('The backup target {name} does not answer.', {name}),
                // task 161: the stick of a USB directory target is not plugged in; the backup went to the other targets
                'no-medium': t('The USB stick of the backup target {name} is not plugged in; the other targets got their copy.', {name}),
                'no-sftp': t('The server of the backup target {name} offers no SFTP.', {name}),
                'too-old': t('No successful backup to {name} for more than a day.', {name}),
                // B-247: on the system's own storage the backup keeps room for a system update
                'update-room': t('The backup to {name} was skipped: on the system itself it would leave too little room for a system update, even with the older backups removed. Use a USB stick or a share.', {name}),
            };
            return why[String(p.cause ?? '')] ?? t('The last backup to {name} failed.', {name});
        }
        case 'certificate':
            if (w.variant === 'renewal-failed') return t('The last certificate renewal failed; the current certificate stays in service.');
            if (w.variant === 'expired') return t('The certificate has expired.');
            return t('The certificate expires in {n} days.', {n: p.days ?? 0});
        case 'storage':
            return words.storage(w);
        case 'addon-ownership':
            // B-92: the files an update wrote as root, in an addon that starts as its own user
            return [
                ...addons.map((a) => t('Files of {name} belong to root, but the addon runs as its own user and may fail to start on them ({path}).', {name: words.addonName(a), path: a.path ?? ''})),
                t("An update installed past occulited leaves such files: install_addon over SSH, or an addon's own updater."),
            ].join(' ');
        case 'security-key':
            return t('BidCos-RF uses the default security key. It is publicly known, so AES-protected BidCos devices are not protected against someone with a radio nearby. Setting your own key re-keys the AES-capable devices; write it down, a backup restored on another system asks for it.');
        case 'hmip-adapter':
            // D-102: hmipserver stopped on a known fatal error; the unit waits for the next run of the radio stack
            // task 155 (D-106): the cause behind the one rejection line picks the text
            if (w.variant === 'adapter-exchange-rejected' && p.cause === 'unreachable') return t("HmIP-RF is down: the system could not reach eQ-3's key server, which has to move the HmIP network to the module {adapter}. Check the network connection and the DNS settings; the Interfaces page tries again.", {adapter: p.adapter ?? ''});
            // task 301: the server does not say why it refuses; no claim about its rules, and the ways out as the Interfaces page names them
            if (w.variant === 'adapter-exchange-rejected' && p.cause === 'refused') return t("HmIP-RF is down: eQ-3's key server refused to move the HmIP network of this system to the module {adapter}. The Interfaces page names the ways out: the module the network is on now, the saved files of an earlier module, or a fresh start with this module (every HmIP device is paired again).", {adapter: p.adapter ?? ''});
            if (w.variant === 'adapter-exchange-rejected') return t("HmIP-RF is down: eQ-3's key server rejected the adapter exchange to {adapter}. This system's HmIP devices belong to the adapter it was set up with, and the key server does not hand them to this one. Put the previous adapter back, or set HmIP up afresh (all HmIP devices have to be paired again).", {adapter: p.adapter ?? ''});
            return t('HmIP-RF is down: hmipserver stopped on a known error ({code}).', {code: w.variant});
        case 'hmip-local-swap':
            // openccu-lite B-289: the network was moved onto the module in use without its key
            return t('HmIP-RF runs on module {module} without the HmIP network\'s key: the network was moved onto it from {from}, but its application firmware {version} is below {minimum} and could not take the key. HmIP devices do not answer, and eQ-3\'s key server refuses every move away from it. The way out is the saved files of {from} - a kept identity or a backup; update the module\'s firmware before the network moves onto it again.', {module: p.module ?? w.variant, from: p.from ?? '', version: p.version ?? '', minimum: p.minimum ?? '2.8.0'});
        case 'hmip-module-missing':
            // openccu-lite task 318 (D-120): the module holding the HmIP network is gone; no other is taken unasked
            return t('HmIP-RF is down: the radio module {module}, which holds the HmIP network, is missing or did not answer. hmipserver runs its virtual devices only, and no other module is taken without asking. Plug it back in, or move HmIP-RF to another module on the Interfaces page.', {module: p.module ?? w.variant});
        case 'rpc-stalled':
            // B-201: an interface process held by a callback listener that never answers
            return stallSentence({interface: p.interface ?? w.variant, kind: p.kind ?? 'delivery', checked: p.checked ?? false, stuck: p.listeners ?? []}, t);
        case 'catalog-check':
            // occulited B-52: the catalogue's check keeps failing (three in a row, or over a day)
            return t('The addon catalogue could not be loaded from GitHub {n} times in a row since {since}: {detail}. The Addons page shows the list the system holds; load the catalogue there once the connection works.', {n: String(p.failures ?? '?'), since: p.since ? words.when(p.since) : '?', detail: p.detail === 'the check did not finish in time' ? t('the check did not finish in time') : (p.detail ?? '')});
        case 'addon-update': {
            // task 248: what the Addons page's check found (the catalogue, or the addon's own check)
            const list = (p.addons ?? []) as {name?: string; id?: string; available?: string}[];
            const names = list.map((a) => (a.available ? `${a.name ?? a.id} ${a.available}` : (a.name ?? a.id ?? ''))).join(', ');
            return list.length === 1 ? t('An update is available for {names}.', {names}) : t('Updates are available for {n} addons: {names}.', {n: String(list.length), names});
        }
        case 'device-update-ready': {
            // occulited task 126: HmIP devices whose transferred firmware waits for the install
            const list = p.updates ?? [];
            const devices = list.map((d) => {
                const who = d.name || [d.type, d.address].filter(Boolean).join(' ');
                return d.version ? `${who} (${d.version})` : who;
            }).join(', ');
            return list.length === 1 ? t('Firmware update ready to install: {devices}.', {devices}) : t('Firmware updates ready to install on {n} devices: {devices}.', {n: String(list.length), devices});
        }
        case 'system-update-available':
            // occulited task 123: the feed's last check offers a newer release in the system's channel
            return p.channel && p.channel !== 'stable'
                ? t('A system update is available in the {channel} channel: {version}.', {channel: p.channel, version: p.version ?? w.variant})
                : t('A system update is available: {version}.', {version: p.version ?? w.variant});
        case 'radio-firmware':
            // task 137 (D-89): the boot never flashes; a newer file waits for the administrator
            if (p.drops_off) return t('The {device} runs firmware {running}; {newest} is on the system: flash it in the radio firmware section.', {device: p.device ?? '', running: p.running ?? '', newest: p.newest ?? ''});
            return t('A newer radio firmware is on the system for the {device}: it runs {running}, {newest} is available. Nothing is flashed on its own; the radio firmware section flashes it.', {device: p.device ?? '', running: p.running ?? '', newest: p.newest ?? ''});
        case 'radio-module-unusable':
            // task 137: an HmIP-RFUSB the detection found but could not read
            return t('The radio module {device} at {node} does not answer with a usable firmware: the system has no radio on it. Flash it in the radio firmware section to use it.', {device: p.device ?? '', node: p.node ?? w.variant});
        case 'radio-load': {
            // openccu-lite task 316: the radio's load high - the carrier sense above 10 %; occulited task 61:
            // the duty cycle from 25 %, from 75 % while a device firmware transfer runs (params.transfer);
            // occulited task 77: no sentence on late devices - the warning's link leads to the course
            const kinds = w.variant.split(',');
            const parts = [kinds.includes('dc') ? t('a duty cycle of {n} %', {n: p.duty_cycle ?? '?'}) : '', kinds.includes('cs') ? t('a carrier sense of {n} %', {n: p.carrier_sense ?? '?'}) : ''].filter(Boolean);
            const busy = t('The radio is busy: {interface} reports {what}.', {interface: p.interface ?? '', what: parts.join(t(' and '))});
            return kinds.includes('dc') && p.transfer ? `${busy} ${t('A device firmware transfer is running, which takes about half of the airtime; this is more.')}` : busy;
        }
        case 'hb-rf-eth':
            // task 218: a configured board that does not answer; the system retries in the background
            if (p.reconnecting) return t('The HB-RF-ETH at {address} lost its connection: the radio module on it is not available until it is back. The system reconnects it on its own.', {address: p.address ?? w.variant});
            return t('The HB-RF-ETH at {address} is not connected: the radio module on it is not available. The system tries again every 30 seconds.', {address: p.address ?? w.variant});
        case 'firewall': {
            const fams = p.families ?? w.variant.split(',');
            const which = fams.length > 1 ? t('IPv4 and IPv6') : fams[0] === 'ipv6' ? 'IPv6' : 'IPv4';
            // task 157: INPUT has no jump to the rules' chain - they are not loaded
            return t('The firewall rules are not loaded ({families}): only the policy decides, whatever the Firewall page lists. Applying the rules on the Firewall page loads them again, and so does a restart of the system.', {families: which});
        }
        case 'classic-rpc-open': {
            // task 173: classic RPC on without a login
            const which = w.variant === 'plain,tls' ? t('the plain and the TLS ports') : w.variant === 'tls' ? t('the TLS ports') : t('the plain ports');
            return t('Classic RPC is on without a login ({ports}): anyone the firewall lets in controls every device.', {ports: which});
        }
        case 'devices-import':
            // openccu-lite task 275: an imported HmIP identity of another module, not taken over yet
            if (w.variant === 'no-module') return t('The HmIP devices imported from the backup ({n}) belong to module {from}; this system has no HmIP module to take them over.', {n: p.count ?? 0, from: p.from ?? ''});
            return t('The HmIP devices imported from the backup ({n}) belong to module {from} and are not on {module} yet: hmipserver moves them at its start; battery devices answer when they wake up. The Interfaces page shows the state and offers a retry.', {n: p.count ?? 0, from: p.from ?? '', module: p.module ?? ''});
        case 'hmip-port-open':
            // B-89: hmipserver's HTTP port is not held on the loopback - the bind shim did not load
            return t("hmipserver's port {port} listens on {addresses}, not only on this system: the image's loopback shim did not take. The firewall still closes it.", {port: String(p.port ?? ''), addresses: p.addresses ?? ''});
        case 'hmip-key-declined':
            // task 201: the device's key in the system's key list is not the device's, so hmipserver
            // declines its pairing and the device never appears
            return t("Pairing {sgtin} was declined: the device's key given to the system (stored here, or entered for the pairing) does not match the device. Scan or type the key from its sticker again, apply it, and put the device into pairing mode once more; if it no longer tries, reset it to its factory settings first.", {sgtin: p.sgtin ?? ''});
        case 'hmip-security-counter': {
            // openccu-lite task 299 (eq-3/occu#134): the HmIP security counter near, past or below its wrap
            const sgtin = p.sgtin ?? '';
            if (w.variant === 'backwards') return t('The HmIP security counter of access point {sgtin} was set below what the devices have seen (the module holds {written}, the start computed {calc}). Every HmIP device refuses this system as a replay until it is power-cycled (battery out and in, fuse off and on) or paired again; a restart of the system does not help.', {sgtin, written: String(p.written ?? ''), calc: String(p.calc ?? '')});
            if (w.variant === 'wrapped') return t("The HmIP security counter of access point {sgtin} has passed 2^32 (the start computed {calc}): hmipserver's check against a lower value protects nothing any more. A start without a synchronised clock can set the counter below what the devices have seen and lock every HmIP device out; this system holds hmipserver back until the clock is trusted.", {sgtin, calc: String(p.calc ?? '')});
            return t('The HmIP security counter of access point {sgtin} is past 2^31 (the start computed {calc}) and reaches 2^32, where its protection ends, at about {wraps}. Keep the clock synchronised; a system without a real-time clock needs a time server.', {sgtin, calc: String(p.calc ?? ''), wraps: p.wraps_at ? new Date(p.wraps_at as string).toLocaleDateString() : '?'});
        }
        case 'hmip-clock-hold':
            // openccu-lite task 299: hmipserver held back for a trusted clock on a wrapped access point
            return t('HmIP-RF is waiting for a trusted clock: {reason} for access point {sgtin}, and the clock came neither from a real-time clock nor from a time server ({state}). A start now could set the HmIP security counter below what the devices have seen and lock every HmIP device out. Connect a time server, or set the time by hand on the Network page; hmipserver starts then.', {sgtin: p.sgtin ?? '', state: p.clock_state ?? w.variant, reason: p.behind ? t("the clock is before the access point's first connection") : t('the computed security counter has passed 2^32')});
        case 'hmip-local-key':
            // task 149: after the switch to local key mode most HmIP devices went silent; the way back is
            // on the Keys page since task 183 (B-291)
            return t("{n} of {total} HmIP devices have not answered since the switch to local key mode: the key was probably not the network's. The way back to eQ-3's key server is on the Keys page, under Local key mode.", {n: p.unreachable ?? 0, total: p.total ?? 0});
        case 'journal-target': {
            // task 216: the USB stick that is ram-sync's target is not plugged in
            if (w.variant === 'usb')
                return t('The USB stick {label} for the journal\'s copies is not plugged in. The journal is in RAM until it is: a reboot or a power loss before then loses what is only there.', {label: p.label ?? ''});
            // task 228: the network share that is ram-sync's target could not be reached
            if (w.variant === 'share')
                return t('The network share {share} for the journal\'s copies could not be reached. The journal is in RAM until a copy reaches it: a reboot or a power loss before then loses what is only there. {reason}', {share: p.share ?? '', reason: p.reason ?? ''});
            // task 85: ram-sync or persistent could not be set up at boot; the reason is the boot script's
            const mode = p.mode ?? w.variant;
            const name = mode === 'ram-sync' ? t('RAM, copied to the userfs') : mode === 'persistent' ? t('Persistent on the userfs') : mode;
            return t('The journal could not be kept on the userfs ({mode}) and is in RAM until the next boot. The boot script said: {reason}', {mode: name, reason: p.reason ?? ''});
        }
        case 'store-target':
            // task 229: the USB stick the database's copy goes to
            if (w.variant === 'failed')
                return t('The last copy of the database to the USB stick {label} failed: {reason}. The history stays on the userfs; a new card would start without it.', {label: p.label ?? '', reason: p.reason ?? ''});
            return t('The USB stick {label} for the database\'s copy is not plugged in. The history stays on the userfs; a new card would start without it.', {label: p.label ?? ''});
        case 'journal-sync':
            if (p.target === 'share')
                return t('The last copy of the journal to the network share {share} failed ({when}): {reason}. Until a copy works again, a power loss loses everything since the last good copy, and the RAM limit can drop the oldest lines.', {
                    share: p.share ?? '',
                    when: p.at ? words.when(p.at) : '—',
                    reason: p.reason ?? '',
                });
            if (p.target === 'usb')
                return t('The last copy of the journal to the USB stick {label} failed ({when}): {reason}. Until a copy works again, a power loss loses everything since the last good copy, and the RAM limit can drop the oldest lines.', {
                    label: p.label ?? '',
                    when: p.at ? words.when(p.at) : '—',
                    reason: p.reason ?? '',
                });
            return t('The last copy of the journal to the userfs failed ({when}): {reason}. Until a copy works again, a power loss loses everything since the last good copy, and the RAM limit can drop the oldest lines.', {
                when: p.at ? words.when(p.at) : '—',
                reason: p.reason ?? '',
            });
        case 'legacy-session':
            // task 125: the addons that get the session's alias in their URLs (the CCU's ?sid= convention)
            return [
                addons.length === 1
                    ? t('{name} receives your session in its URL (?sid=, the legacy CCU convention).', {name: names(addons)})
                    : t('{n} addons receive your session in their URLs (?sid=, the legacy CCU convention): {list}.', {n: addons.length, list: names(addons)}),
                t('What is in the URL is an alias that only addon pages accept, not the session itself, and it ends with your session. The way out: update the addon once a release reads the session header, or switch the legacy session off for it on the Addons page.'),
            ].join(' ');
        case 'console-reset': {
            // occulited task 14: `occulited admin reset-auth` on the system's console
            const list = (p.accounts ?? []).map((a) => t('{user} ({when})', {user: a.user, when: words.when(a.at)})).join(', ');
            return [
                t('Access reset on the console: {list}.', {list}),
                t('The account\'s passkeys were removed and a one-time password was set, which has to be changed at the next login. If nobody with root access to the system did this, change the passwords and check who can log in to the system.'),
            ].join(' ');
        }
        case 'bootloader-update':
            // openccu-lite task 366: a notice - the image carries a newer bootloader EEPROM
            return p.staged
                ? t('The bootloader update of {date} is prepared; the Raspberry Pi writes it at the next start.', {date: p.latest ?? w.variant})
                : t('A newer bootloader is available for this Raspberry Pi: {date} (installed: {current}). The Hardware page installs it.', {date: p.latest ?? w.variant, current: p.running ?? ''});
        case 'addon-notice': {
            // occulited task 119: the addon's own words, in the page's language; its name in front
            const lang = words.lang === 'de' ? 'de' : 'en';
            const pick = (m?: Record<string, string>) => m?.[lang] || m?.en || m?.de || '';
            const name = words.addonName({id: p.addon ?? w.variant.split('/')[0] ?? ''});
            const text = pick(p.text);
            const title = pick(p.title) || w.variant;
            // a title the addon did not end gets a full stop before the text
            const sep = /[.!?…:]$/.test(title) ? ' ' : '. ';
            return `${name}: ${title}` + (text ? `${sep}${text}` : '');
        }
    }
    // a warning newer than this page: at least its name
    return `${w.id}: ${w.variant}`;
}

const LINK_LABELS: Record<string, string> = {
    rega: 'Addons',
    arch: 'Addons',
    meta: 'Control',
    'app-public': 'Settings',
    'trust-ca': 'Trust stores',
    'trust-pin': 'Trust stores',
    unclean: 'Log',
    'backup-target': 'Backup',
    'backup-userfs': 'Backup',
    'backup-unencrypted': 'Set up encryption',
    'backup-delivery': 'Backup',
    certificate: 'Certificate',
    storage: 'Storage health',
    'addon-ownership': 'Services',
    'addon-ended': 'Addons',
    'addon-failed': 'Addons',
    'crash-loop': 'Services',
    'occulited-crash-loop': 'Log',
    'security-key': 'Set security key',
    'hmip-adapter': 'Interfaces',
    'hmip-local-swap': 'Interfaces',
    'hmip-module-missing': 'Interfaces',
    'devices-import': 'Interfaces',
    'hb-rf-eth': 'LAN devices',
    'radio-load': 'Interfaces',
    'radio-firmware': 'Radio firmware',
    'addon-update': 'Manage addons',
    'system-update-available': 'Updates',
    'device-update-ready': 'Updates',
    'catalog-check': 'Manage addons',
    'radio-module-unusable': 'Radio firmware',
    'rpc-stalled': 'Interfaces',
    firewall: 'Firewall',
    'hmip-local-key': 'Local key mode',
    'hmip-key-declined': 'Device keys',
    'hmip-security-counter': 'Interfaces',
    'hmip-clock-hold': 'Network',
    'journal-target': 'Journal settings',
    'journal-sync': 'Journal settings',
    'store-target': 'History settings',
    'legacy-session': 'Installed addons',
    'console-reset': 'Users',
    'bootloader-update': 'Hardware',
    'addon-notice': 'Open the addon',
};

/** the button to where the matter is handled (task 53): the page's name, or what it does there */
export function warningLink(w: Warning, t: Translate): {href: string; label: string} | null {
    if (!w.href) return null;
    return {href: w.href, label: t(LINK_LABELS[w.id] ?? 'Open page')};
}

/** the warnings on show, and the ones this administrator silenced, each in the order they came */
export function splitSilenced(list: Warning[]): {shown: Warning[]; silenced: Warning[]} {
    return {shown: list.filter((w) => !w.silenced), silenced: list.filter((w) => !!w.silenced)};
}

/** DELETE's path: the variant is URL-escaped, a backup target's is a path itself */
export function silencePath(w: {id: string; variant: string}): string {
    return `/api/system/v1/warnings/silence/${encodeURIComponent(w.id)}/${encodeURIComponent(w.variant)}`;
}

export function silencedLine(n: number, t: Translate): string {
    return n === 1 ? t('1 silenced warning') : t('{n} silenced warnings', {n});
}

export function periodLabel(days: number, t: Translate): string {
    return days === 1 ? t('Silence for 1 day') : t('Silence for {n} days', {n: days});
}
