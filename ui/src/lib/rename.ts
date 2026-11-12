// What a rename did (openccu-lite tasks 62 and 327): the answer of POST /api/system/v1/network for a
// hostname-only change, said the same way on the Network page and on the welcome page.
import {t} from './i18n.svelte';
import {timeOnly} from './datefmt';

export interface Lease { renewed: boolean; static?: boolean; error?: string; address?: string; at?: string }
export interface Rename { hostname: string; previous: string; lease: Lease }
export interface CertNames { names: string[]; fits: boolean; mode: string; known: boolean }
export interface AcmeNames { state: string; names: string[]; previous: string[] }

/** "Hostname set to …" and what became of the DHCP lease */
export function renameText(r: Rename): string {
    const l = r.lease;
    const lease = l.static
        ? t('Static address: no DHCP server to tell.')
        : l.renewed
            // task 327: a router that takes a name only with a new lease keeps the old one until it runs out
            ? `${t('The DHCP server was told at {time}{address}.', {time: l.at ? timeOnly(l.at) : '', address: l.address ? ` (${l.address})` : ''})} ${t('Some routers take a new name only with a new lease and show the old one until the lease runs out.')}`
            : t('The DHCP lease could not be renewed under the new name: {error} Until the next start the server knows the system as {old}.', {error: l.error ?? '', old: r.previous});
    return `${t('Hostname set to {name}.', {name: r.hostname})} ${lease}`;
}

/** the certificate reminder after a rename, or '' when the certificate names the new host or is not known */
export function certificateText(r: Rename, c: CertNames | null | undefined, acme: AcmeNames | null | undefined): string {
    if (!c || c.fits || !c.known) return '';
    const old = c.names.filter((n) => !/^\d+\.\d+\.\d+\.\d+$/.test(n)).join(', ') || r.previous;
    if (c.mode === 'self-signed') return t("The system's own certificate still names {old}. Browsers will warn until it is renewed.", {old});
    const base = t('The certificate names {old}; for {name} you most likely need a new one.', {old, name: r.hostname});
    if (acme?.state === 'adapted') return `${base} ${t('The ACME names were adapted to {names}; the certificate follows at the next renewal, or on Issue now.', {names: acme.names.join(', ')})}`;
    if (acme?.state === 'set-by-hand') return `${base} ${t('The ACME names were set by hand and stay: {names}.', {names: acme.names.join(', ')})}`;
    return base;
}
