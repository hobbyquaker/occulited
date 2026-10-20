import {describe, expect, it} from 'vitest';
import {periodLabel, silencedLine, silencePath, splitSilenced, warningLink, warningText, type Warning, type Words} from './warnings';

const t = (key: string, params?: Record<string, string | number>) => {
    let s = key;
    for (const [k, v] of Object.entries(params ?? {})) s = s.replaceAll(`{${k}}`, String(v));
    return s;
};
const words: Words = {t, addonName: (a) => a.name || a.id, storage: (w) => `storage ${w.variant}`, when: (iso) => `at ${iso}`};
const w = (id: string, variant: string, params: Warning['params'] = {}, href = ''): Warning => ({id, variant, severity: 'error', params, href});

describe('the Status page warnings (task 81)', () => {
    it('names the addons that get the legacy session, one or several', () => {
        const one = warningText(w('legacy-session', 'mosquitto', {addons: [{id: 'mosquitto', name: 'Mosquitto', enabled: true}]}), words);
        expect(one).toContain('Mosquitto receives your session in its URL');
        expect(one).toContain('Addons page');
        const two = warningText(w('legacy-session', 'hmm,mosquitto', {addons: [{id: 'hmm', name: 'Homematic Manager', enabled: true}, {id: 'mosquitto', name: 'Mosquitto', enabled: true}]}), words);
        expect(two).toContain('2 addons receive your session in their URLs');
        expect(two).toContain('Homematic Manager, Mosquitto');
        expect(warningLink(w('legacy-session', 'mosquitto', {}, '/addons'), t)).toEqual({href: '/addons', label: 'Installed addons'});
    });

    it('says a newer bootloader is available, or prepared, and links to the Hardware page (openccu-lite task 366)', () => {
        const n = {...w('bootloader-update', '2026-11-20', {running: '2026-10-15', latest: '2026-11-20'}, '/system/hardware#bootloader'), severity: 'info' as const};
        expect(warningText(n, words)).toBe('A newer bootloader is available for this Raspberry Pi: 2026-11-20 (installed: 2026-10-15). The Hardware page installs it.');
        expect(warningText({...n, params: {...n.params, staged: true}}, words)).toBe('The bootloader update of 2026-11-20 is prepared; the Raspberry Pi writes it at the next start.');
        expect(warningLink(n, t)).toEqual({href: '/system/hardware#bootloader', label: 'Hardware'});
    });

    it('says which accounts were reset on the console (occulited task 14)', () => {
        const text = warningText(w('console-reset', 'admin@1700000000', {accounts: [{user: 'admin', at: '2026-10-03T12:00:00Z'}]}), words);
        expect(text).toContain('Access reset on the console: admin (at 2026-10-03T12:00:00Z).');
        expect(text).toContain('one-time password');
        expect(warningLink(w('console-reset', 'admin@1700000000', {}, '/system/users'), t)).toEqual({href: '/system/users', label: 'Users'});
    });

    it('names the host and the missing authority of a strict TLS failure (openccu-lite task 231)', () => {
        const plain = warningText(w('trust-ca', 'api.github.com', {hosts: ['api.github.com'], issuer: 'CN=DigiCert Global Root G2', store: 'occulited', candidate: false}), words);
        expect(plain).toBe('api.github.com: the server presents a certificate from CN=DigiCert Global Root G2, which the occulited trust store does not hold. The call fails until the authority is added.');
        const fix = warningText(w('trust-ca', 'a,b', {hosts: ['a', 'b'], issuer: 'X', store: 'occulited', candidate: true}), words);
        expect(fix).toContain('a, b: the server presents');
        expect(fix).toContain('one click on the Trust stores page copies it');
        expect(warningLink(w('trust-ca', 'a', {}, '/system/trust#occulited'), t)).toEqual({href: '/system/trust#occulited', label: 'Trust stores'});
    });

    it('names the addons and says disabled once', () => {
        const text = warningText(w('rega', 'email,hm-print', {addons: [{id: 'hm-print', name: 'Print', enabled: false}, {id: 'email', name: 'E-Mail', enabled: true}]}), words);
        expect(text).toBe('Disabled incompatible Addon: Print. Installed, not usable without ReGa, and still enabled: E-Mail.');
        expect(warningText(w('arch', 'cuxd', {addons: [{id: 'cuxd', enabled: false}]}), words)).toBe('Built for another architecture, and therefore disabled: cuxd. The repair is a release built for this system.');
    });

    it('says an HB-RF-ETH whose link is lost is reconnected, not retried (B-218)', () => {
        expect(warningText(w('hb-rf-eth', '192.0.2.209', {address: '192.0.2.209', reconnecting: true}), words)).toBe('The HB-RF-ETH at 192.0.2.209 lost its connection: the radio module on it is not available until it is back. The system reconnects it on its own.');
        expect(warningText(w('hb-rf-eth', '192.0.2.209', {address: '192.0.2.209', reconnecting: false}), words)).toContain('tries again every 30 seconds');
        // openccu-lite task 316: the radio's load
        expect(warningText(w('radio-load', 'dc', {interface: 'HmIP-RF', duty_cycle: 63}), words)).toBe('The radio is busy: HmIP-RF reports a duty cycle of 63 %.');
        expect(warningText(w('radio-load', 'dc,cs', {interface: 'HmIP-RF', duty_cycle: 63, carrier_sense: 12}), words)).toContain('a duty cycle of 63 % and a carrier sense of 12 %');
        // occulited task 61: during a firmware transfer the threshold is 75 %, and the warning says why it is more than usual
        expect(warningText(w('radio-load', 'dc', {interface: 'HmIP-RF', duty_cycle: 78, transfer: true}), words)).toBe('The radio is busy: HmIP-RF reports a duty cycle of 78 %. A device firmware transfer is running, which takes about half of the airtime; this is more.');
        expect(warningText(w('radio-load', 'cs', {interface: 'HmIP-RF', duty_cycle: 60, carrier_sense: 12, transfer: true}), words)).not.toContain('firmware transfer');
    });

    it('names the missing module that holds the HmIP network, with the way to the Interfaces page (openccu-lite task 318)', () => {
        expect(warningText(w('hmip-module-missing', '3014F711A0001F0000000A03', {module: '3014F711A0001F0000000A03'}), words)).toBe('HmIP-RF is down: the radio module 3014F711A0001F0000000A03, which holds the HmIP network, is missing or did not answer. hmipserver runs its virtual devices only, and no other module is taken without asking. Plug it back in, or move HmIP-RF to another module on the Interfaces page.');
        expect(warningLink(w('hmip-module-missing', 'x', {}, '/system/interfaces#connections'), t)).toEqual({href: '/system/interfaces#connections', label: 'Interfaces'});
    });

    it('names a backup target and its cause (task 86)', () => {
        expect(warningText(w('backup-delivery', 'tabc:auth-failed', {name: 'NAS', cause: 'auth-failed'}), words)).toBe('The backup target NAS refuses the login.');
        expect(warningText(w('backup-delivery', 'directory:too-old', {name: 'USB', cause: 'too-old'}), words)).toBe('No successful backup to USB for more than a day.');
        expect(warningText(w('backup-delivery', 'tabc:last-failed', {name: 'NAS', cause: 'last-failed'}), words)).toBe('The last backup to NAS failed.');
        expect(warningLink(w('backup-delivery', 'tabc:full', {name: 'NAS', cause: 'full'}, '/backup#targets'), t)).toEqual({href: '/backup#targets', label: 'Backup'});
    });

    it('says the certificate variants apart', () => {
        expect(warningText(w('certificate', 'renewal-failed', {days: 40}), words)).toContain('renewal failed');
        expect(warningText(w('certificate', 'expired', {days: -1}), words)).toBe('The certificate has expired.');
        expect(warningText(w('certificate', 'expiring', {days: 9}), words)).toBe('The certificate expires in 9 days.');
    });

    it('fills the time, the path and the root-owned files', () => {
        expect(warningText(w('unclean', '1', {at: '2026-09-12T03:00:00Z'}), words)).toContain('(at 2026-09-12T03:00:00Z)');
        expect(warningText(w('backup-target', '/media/usb0/backup', {path: '/media/usb0/backup'}), words)).toContain('/media/usb0/backup cannot be read');
        const own = warningText(w('addon-ownership', 'hm2mqtt', {addons: [{id: 'hm2mqtt', path: '/usr/local/addons/hm2mqtt/var/pid'}]}), words);
        expect(own).toContain('Files of hm2mqtt belong to root');
        expect(own).toContain('(/usr/local/addons/hm2mqtt/var/pid)');
        expect(warningText(w('storage', 'watch'), words)).toBe('storage watch');
        expect(warningText(w('security-key', 'default'), words)).toMatch(/^BidCos-RF uses the default security key\./);
        // a warning this page does not know yet still says which it is
        expect(warningText(w('future', 'x'), words)).toBe('future: x');
    });

    it('labels the way to where it is handled', () => {
        expect(warningLink(w('security-key', 'default', {}, '/radio#security-key'), t)).toEqual({href: '/radio#security-key', label: 'Set security key'});
        expect(warningLink(w('storage', 'watch', {}, '#storage'), t)).toEqual({href: '#storage', label: 'Storage health'});
        expect(warningLink(w('meta', '1'), t)).toBeNull();
    });

    it('splits off the silenced ones and escapes the variant', () => {
        const silenced = {...w('backup-target', '/media/usb0/backup'), silenced: {id: 'backup-target', variant: '/media/usb0/backup', by: 'admin', at: '', until: ''}};
        const {shown, silenced: off} = splitSilenced([w('meta', '1'), silenced]);
        expect(shown.map((x) => x.id)).toEqual(['meta']);
        expect(off.map((x) => x.id)).toEqual(['backup-target']);
        expect(silencePath(silenced)).toBe('/api/system/v1/warnings/silence/backup-target/%2Fmedia%2Fusb0%2Fbackup');
        expect(silencePath({id: 'rega', variant: 'email,hm-print'})).toBe('/api/system/v1/warnings/silence/rega/email%2Chm-print');
    });

    it('counts and names the periods', () => {
        expect(silencedLine(1, t)).toBe('1 silenced warning');
        expect(silencedLine(2, t)).toBe('2 silenced warnings');
        expect(periodLabel(1, t)).toBe('Silence for 1 day');
        expect(periodLabel(90, t)).toBe('Silence for 90 days');
    });
});

describe('hmip-port-open (B-89)', () => {
    it('names the port and the addresses', () => {
        const text = warningText({id: 'hmip-port-open', variant: '39292', severity: 'warning', params: {port: 39292, addresses: '0.0.0.0, ::'}}, words);
        expect(text).toBe("hmipserver's port 39292 listens on 0.0.0.0, ::, not only on this system: the image's loopback shim did not take. The firewall still closes it.");
    });
});

describe('crash loops (openccu-lite task 283)', () => {
    it('names the units, their restarts and since when', () => {
        const text = warningText(w('crash-loop', 'addon-mosquitto,rfd', {units: [{unit: 'addon-mosquitto', restarts: 3, total: 3, since: '2026-09-27T20:00:00Z', addon: true}, {unit: 'rfd', restarts: 4, total: 9, since: '2026-09-27T20:01:00Z', result: 'exit-code'}]}, '/system/services'), words);
        expect(text).toBe('Keeps failing and restarting: addon-mosquitto (3 restarts since at 2026-09-27T20:00:00Z), rfd (4 restarts since at 2026-09-27T20:01:00Z). The system tries again at longer and longer intervals; the log says why.');
        expect(warningText(w('crash-loop', 'rfd'), words)).toContain('restarting: rfd.');
        expect(warningLink(w('crash-loop', 'rfd', {}, '/system/services'), t)).toEqual({href: '/system/services', label: 'Services'});
    });

    it("says occulited's own loop after it came back", () => {
        const text = warningText(w('occulited-crash-loop', '1790000000', {fails: 3, restarts: 5, first: '2026-09-27T20:00:00Z', last: '2026-09-27T20:05:00Z'}, '/system/log?unit=occulited'), words);
        expect(text).toBe('The system service occulited kept failing: 3 failures in a row between at 2026-09-27T20:00:00Z and at 2026-09-27T20:05:00Z. It runs again; the log of that time says why.');
        expect(warningLink(w('occulited-crash-loop', 'x', {}, '/system/log?unit=occulited'), t)).toEqual({href: '/system/log?unit=occulited', label: 'Log'});
    });
});
