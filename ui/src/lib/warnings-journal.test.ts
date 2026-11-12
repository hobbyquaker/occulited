import {describe, expect, it} from 'vitest';
import {warningLink, warningText, type Warning, type Words} from './warnings';

const t = (key: string, params?: Record<string, string | number>) => {
    let s = key;
    for (const [k, v] of Object.entries(params ?? {})) s = s.replaceAll(`{${k}}`, String(v));
    return s;
};
const words: Words = {t, addonName: (a) => a.id, storage: () => '', when: (iso) => `at ${iso}`};

describe('the journal warnings (task 85)', () => {
    it('says which mode could not be set up, and the boot script\'s reason', () => {
        const w: Warning = {id: 'journal-target', variant: 'ram-sync', severity: 'warning', href: '/log?settings=journal', params: {mode: 'ram-sync', reason: 'the userfs target cannot be mounted, the journal stays in RAM (STORAGE=ram-sync)'}};
        expect(warningText(w, words)).toBe(
            'The journal could not be kept on the userfs (RAM, copied to the userfs) and is in RAM until the next boot. The boot script said: the userfs target cannot be mounted, the journal stays in RAM (STORAGE=ram-sync)',
        );
        expect(warningText({...w, variant: 'persistent', params: {reason: 'x'}}, words)).toContain('(Persistent on the userfs)');
        expect(warningLink(w, t)).toEqual({href: '/log?settings=journal', label: 'Journal settings'});
    });

    it('says when the last copy failed and why', () => {
        const w: Warning = {id: 'journal-sync', variant: 'failed', severity: 'warning', href: '/log?settings=journal', params: {at: '2026-09-12T18:00:00Z', reason: 'copying system@x.journal failed (the userfs full or read-only?)'}};
        expect(warningText(w, words)).toBe(
            'The last copy of the journal to the userfs failed (at 2026-09-12T18:00:00Z): copying system@x.journal failed (the userfs full or read-only?). Until a copy works again, a power loss loses everything since the last good copy, and the RAM limit can drop the oldest lines.',
        );
        expect(warningText({...w, params: {}}, words)).toContain('failed (—): .');
        expect(warningLink(w, t)?.label).toBe('Journal settings');
    });

    it('names the USB stick that is not plugged in, and the stick a copy failed to reach (task 216)', () => {
        const w: Warning = {id: 'journal-target', variant: 'usb', severity: 'warning', href: '/log?settings=journal', params: {mode: 'ram-sync', label: 'LOG_STICK', dir: 'journal', reason: 'the USB stick LOG_STICK is not plugged in'}};
        expect(warningText(w, words)).toBe("The USB stick LOG_STICK for the journal's copies is not plugged in. The journal is in RAM until it is: a reboot or a power loss before then loses what is only there.");
        expect(warningLink(w, t)?.label).toBe('Journal settings');
        const f: Warning = {id: 'journal-sync', variant: 'failed', severity: 'warning', href: '/log?settings=journal', params: {at: '2026-09-24T18:00:00Z', target: 'usb', label: 'LOG_STICK', reason: 'the USB stick LOG_STICK is mounted read-only'}};
        expect(warningText(f, words)).toBe(
            'The last copy of the journal to the USB stick LOG_STICK failed (at 2026-09-24T18:00:00Z): the USB stick LOG_STICK is mounted read-only. Until a copy works again, a power loss loses everything since the last good copy, and the RAM limit can drop the oldest lines.',
        );
    });

    it('notes a journal in RAM only, and names the first boot\'s copy (openccu-lite task 341)', () => {
        const w: Warning = {id: 'journal-ram', variant: 'ram', severity: 'info', href: '/log?settings=journal', params: {}};
        expect(warningText(w, words)).toBe('The journal is kept in RAM only: the logs are lost at every reboot. The Journal settings keep them on the userfs, a USB stick or a network share.');
        expect(warningLink(w, t)).toEqual({href: '/log?settings=journal', label: 'Journal settings'});
        const c: Warning = {...w, params: {first_boot_journal: '/usr/local/var/log/first-boot-journal.txt.gz', first_boot_journal_size: 1048576}};
        expect(warningText(c, words)).toBe(
            'The journal is kept in RAM only: the logs are lost at every reboot. The Journal settings keep them on the userfs, a USB stick or a network share. The journal of the first boot after the switch to openccu-lite is kept in /usr/local/var/log/first-boot-journal.txt.gz (1.0 MB).',
        );
    });
});
