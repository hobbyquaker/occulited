<script module lang="ts">
    /**
     * occulited task 142 (hobbyquaker/openccu-lite#19, D-125): what an uploaded backup is, said in
     * words before anything is done with it - the kind of backup, where it is from, encrypted or
     * not, what it holds. An (Open)CCU or CCU3 backup is imported; an openccu-lite backup is
     * restored. The check script's own output is kept behind "Details of the check".
     */
    export interface BackupAddon { id: string; name: string; version?: string; files: number; bytes: number; program: boolean; www?: boolean; config?: boolean; here?: boolean }
    export interface RadioBackup {
        bidcos_rf: {devices: number; address?: string; serial?: string; has_key: boolean; gateways: number};
        // openccu-lite task 299: what the access point file says about the HmIP security counter
        hmip: {devices: number; identity_sgtin?: string; local_key: boolean; device_key_map: boolean; security_counter?: {first_connect: string; offset: number; calc: number; behind?: boolean; verdict: string; wraps_at: string}};
        bidcos_wired: {devices: number; gateways: number};
        key_index: number;
        files: string[];
        version?: string;
    }
    export interface BackupReport {
        kind: 'openccu-lite' | 'ccu';
        system: string;
        version?: string;
        lite_version?: string;
        hostname?: string;
        key_index: number;
        radio: RadioBackup;
        rega: boolean;
        accounts: boolean;
        users: number;
        tokens: number;
        addons: BackupAddon[];
        settings: Record<string, {value: string; valid: boolean}>;
        classic_rpc?: {xmlrpc: string; sources: string[]; auth: boolean};
        left_behind: string[];
    }
    export interface ThisSystem { name: string; version?: string; base_version?: string }
    // task 91: what the upload's age header said, and whether the recovery key is still needed
    export interface Encryption { encrypted: boolean; format?: string; armored?: boolean; box_fingerprint?: string; recovery_fingerprint?: string; known?: 'current' | 'previous' | 'unknown'; key_created?: string; opened_with?: 'box' | 'recovery'; needs_recovery_key: boolean; passphrase?: boolean; created_here: boolean }
    export const radioEmpty = (b: RadioBackup) => b.bidcos_rf.devices === 0 && b.hmip.devices === 0 && b.bidcos_wired.devices === 0 && !b.hmip.identity_sgtin && !b.bidcos_rf.address;
</script>

<script lang="ts">
    import {t} from './i18n.svelte';

    let {report, system, ok, output, encryption}: {
        report: BackupReport | null;
        system: ThisSystem | null;
        /** the firmware's check: the archive is sound */
        ok: boolean;
        output: string;
        encryption: Encryption | null;
    } = $props();
    const b = $derived(report?.radio ?? null);
    const from = $derived(report ? [report.hostname, [report.system, report.kind === 'openccu-lite' ? (report.lite_version ?? '') : (report.version ?? '')].filter(Boolean).join(' ')].filter(Boolean).join(' · ') : '');
    const base = $derived(report?.kind === 'openccu-lite' && report.version ? t('on OpenCCU {v}', {v: report.version}) : '');
    const settings = $derived(report ? [
        report.settings.hb_rf_eth ? t('HB-RF-ETH address {v}', {v: report.settings.hb_rf_eth.value}) : '',
        report.settings.loghost ? t('syslog server {v}', {v: report.settings.loghost.value}) : '',
    ].filter(Boolean) : []);
</script>

<div class="ol-subblock ol-backupfile" data-restore="file" data-kind={report?.kind ?? 'unknown'}>
    <h3>{t('This file')}</h3>
    <dl class="ol-kv">
        <dt>{t('Kind')}</dt>
        <dd data-file="kind">
            {#if !report}
                <span class="ol-warn">{t('Not a backup this system can read.')}</span>
            {:else if report.kind === 'openccu-lite'}
                <strong>{t('A backup of openccu-lite')}</strong> - {t('it can be restored: this system becomes the one the backup was made of.')}
            {:else}
                <strong>{t('A backup of {system}', {system: report.system === 'CCU' ? t('a CCU') : report.system})}</strong> - {t('it can be imported: its devices, keys and names come onto this system, and what you choose of the rest. It cannot be restored here.')}
            {/if}
        </dd>
        {#if from}<dt>{t('From')}</dt><dd data-file="from">{from}{#if base}{' '}<span class="ol-muted">({base})</span>{/if}</dd>{/if}
        {#if system}<dt>{t('This system')}</dt><dd data-file="system">{system.name}{#if system.version}{' '}{system.version}{/if}{#if system.base_version}{' '}<span class="ol-muted">({t('on OpenCCU {v}', {v: system.base_version})})</span>{/if}</dd>{/if}
        <dt>{t('Check')}</dt><dd data-file="check">{ok ? t('The archive is complete and readable.') : t('The firmware\'s check rejected the file; see the details.')}</dd>
        <dt>{t('Encryption')}</dt>
        {#if encryption?.encrypted}
            <dd data-restore="opened-with">{encryption.opened_with === 'box' ? t("encrypted · opened with this system's key") : t('encrypted · decrypted with the recovery key')}{#if encryption.created_here}{' · '}{t('made by this system')}{:else}{' '}· <span class="ol-warn">{t('this system did not create this file')}</span>{/if}</dd>
        {:else}
            <dd data-file="plain">{t('not encrypted: the file holds its keys in plain')}</dd>
        {/if}
        {#if report && b}
            <dt>{t('Paired devices')}</dt>
            <dd data-file="devices">
                {#if radioEmpty(b)}
                    <span data-devices="none">{t('None: the backup holds no paired device and no radio identity.')}</span>
                {:else}
                    <span class="ol-fileline"><span class="ol-filekey">BidCos-RF</span> <span data-devices="bidcos-rf">{t('{n} devices', {n: b.bidcos_rf.devices})}{#if b.bidcos_rf.address}{' · '}{t('address {a}', {a: b.bidcos_rf.address})}{/if}{#if b.bidcos_rf.has_key}{' · '}{t('an individual security key')}{/if}{#if b.bidcos_rf.gateways}{' · '}{t('{n} LAN gateways', {n: b.bidcos_rf.gateways})}{/if}</span></span>
                    <span class="ol-fileline"><span class="ol-filekey">HmIP</span> <span data-devices="hmip">{t('{n} devices', {n: b.hmip.devices})}{#if b.hmip.identity_sgtin}{' · '}{t('identity of module {sgtin}', {sgtin: b.hmip.identity_sgtin})}{/if}{#if b.hmip.local_key}{' · '}{t('local key mode')}{/if}{#if b.hmip.device_key_map}{' · '}{t('device key map')}{/if}</span></span>
                    <span class="ol-fileline"><span class="ol-filekey">BidCos-Wired</span> <span data-devices="wired">{t('{n} devices', {n: b.bidcos_wired.devices})}{#if b.bidcos_wired.gateways}{' · '}{t('{n} LAN gateways', {n: b.bidcos_wired.gateways})}{/if}</span></span>
                {/if}
            </dd>
            <dt>{t('Names')}</dt>
            <dd data-file="names">
                {#if report.kind === 'openccu-lite'}{t('the names, rooms and functions of the system, as it had them')}
                {:else if report.rega}{t('a ReGa database: names, rooms and functions (its programs and system variables cannot be used here)')}
                {:else}{t('no ReGa database: no names come along')}{/if}
            </dd>
            <dt>{t('Addons')}</dt>
            <dd data-file="addons">{#if report.addons.length}{report.addons.map((a) => a.name + (a.version ? ' ' + a.version : '') + (a.program ? '' : ' (' + t('settings only') + ')')).join(', ')}{:else}{t('no addons')}{/if}</dd>
            {#if report.kind === 'openccu-lite'}
                <dt>{t('Accounts')}</dt>
                <dd data-file="accounts">{t('{u} users, {k} API tokens', {u: report.users, k: report.tokens})}</dd>
                <dt>{t('Settings')}</dt>
                <dd data-file="settings">{t('everything: network, firewall, certificates, remote access, the system\'s own settings')}</dd>
            {:else if settings.length}
                <dt>{t('Settings')}</dt>
                <dd data-file="settings">{settings.join(' · ')}</dd>
            {/if}
        {/if}
    </dl>
    {#if output}
        <details class="ol-filedetails" open={!ok} data-file="details">
            <summary>{t('Details of the check')}</summary>
            <pre class="ol-log">{output}</pre>
        </details>
    {/if}
</div>

<style>
    .ol-backupfile h3 { margin-top: 0; }
    .ol-fileline { display: block; }
    .ol-filekey { display: inline-block; min-width: 7.5em; color: var(--hmm-muted, inherit); }
    .ol-filedetails { margin-top: 8px; }
    .ol-filedetails summary { cursor: pointer; }
</style>
