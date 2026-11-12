// Task 50: an own timer - `local-<name>.timer` with its `local-<name>.service` - between the
// new-timer form and the two files. The form is a convenience: what is saved is the text of both
// files, so the form generates them and, for an own timer opened again, is read back out of them
// as far as they still have the form's shape. A file edited by hand keeps its edit (the editor
// compares it with what the form generates), and nothing here has to understand every directive.

import {dateLocale} from './datelocale';

export type Schedule = 'hourly' | 'daily' | 'weekly' | 'boot' | 'custom';

export interface TimerForm {
    name: string;
    schedule: Schedule;
    /** HH:MM, for daily and weekly */
    time: string;
    /** Mon … Sun, for weekly */
    weekday: string;
    /** a systemd time span after boot (`5min`), for boot */
    bootDelay: string;
    /** the free OnCalendar= expression, for custom */
    calendar: string;
    /** Persistent=: catch a run up that fell into a time the box was off (calendar schedules) */
    persistent: boolean;
    /** RandomizedDelaySec=, empty for none */
    randomDelay: string;
    /** ExecStart= */
    command: string;
    /** User=; empty (or root) runs as root */
    user: string;
}

export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const;

/** The API's rule for a name: 1 to 32 of letters, digits, - and _, starting with a letter or digit. */
export const TIMER_NAME = /^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/;

export function defaultForm(): TimerForm {
    return {name: '', schedule: 'daily', time: '03:00', weekday: 'Mon', bootDelay: '5min', calendar: '', persistent: true, randomDelay: '', command: '', user: ''};
}

// a time input can be cleared; a cleared one is midnight rather than an expression systemd refuses
function hhmm(v: string): string {
    const m = /^(\d{1,2}):(\d{2})/.exec(v.trim());
    return m ? `${m[1]!.padStart(2, '0')}:${m[2]}` : '00:00';
}

/** The OnCalendar= expression the form's schedule stands for; empty for the boot schedule. */
export function calendarOf(f: TimerForm): string {
    switch (f.schedule) {
        case 'hourly':
            return 'hourly';
        case 'daily':
            return `*-*-* ${hhmm(f.time)}:00`;
        case 'weekly':
            return `${f.weekday} *-*-* ${hhmm(f.time)}:00`;
        case 'custom':
            return f.calendar.trim();
        default:
            return '';
    }
}

/** The .timer file. WantedBy=timers.target is what `systemctl --runtime enable` links it into. */
export function timerText(f: TimerForm): string {
    const lines = ['[Unit]', `Description=${f.name.trim()}`, '', '[Timer]'];
    if (f.schedule === 'boot') {
        lines.push(`OnBootSec=${f.bootDelay.trim()}`);
    } else {
        lines.push(`OnCalendar=${calendarOf(f)}`);
        // Persistent= only means something for OnCalendar=: a boot timer runs after every boot
        if (f.persistent) lines.push('Persistent=true');
    }
    if (f.randomDelay.trim()) lines.push(`RandomizedDelaySec=${f.randomDelay.trim()}`);
    lines.push('', '[Install]', 'WantedBy=timers.target', '');
    return lines.join('\n');
}

/** The .service file: a oneshot running the command, as the user when one other than root is named. */
export function serviceText(f: TimerForm): string {
    const lines = ['[Unit]', `Description=${f.name.trim()}`, '', '[Service]', 'Type=oneshot', `ExecStart=${f.command.trim()}`];
    const user = f.user.trim();
    if (user && user !== 'root') lines.push(`User=${user}`);
    lines.push('');
    return lines.join('\n');
}

// the first `Key=value` of a file, wherever it stands; a file with the form's shape has each once
function directive(text: string, key: string): string | undefined {
    const m = new RegExp(`^[ \\t]*${key}[ \\t]*=(.*)$`, 'm').exec(text);
    return m ? m[1]!.trim() : undefined;
}

/**
 * The form read back out of an own timer's files. A schedule the presets do not produce becomes
 * custom with its expression; a timer on something else than OnCalendar= or OnBootSec= becomes
 * custom with an empty expression, and its file then no longer follows the form.
 */
export function formFromFiles(name: string, timer: string, service: string): TimerForm {
    const f = defaultForm();
    f.name = name;
    const calendar = directive(timer, 'OnCalendar');
    const boot = directive(timer, 'OnBootSec');
    let m: RegExpExecArray | null;
    if (calendar === 'hourly') {
        f.schedule = 'hourly';
    } else if (calendar !== undefined && (m = /^\*-\*-\* (\d{2}:\d{2}):00$/.exec(calendar))) {
        f.schedule = 'daily';
        f.time = m[1]!;
    } else if (calendar !== undefined && (m = /^(Mon|Tue|Wed|Thu|Fri|Sat|Sun) \*-\*-\* (\d{2}:\d{2}):00$/.exec(calendar))) {
        f.schedule = 'weekly';
        f.weekday = m[1]!;
        f.time = m[2]!;
    } else if (calendar !== undefined) {
        f.schedule = 'custom';
        f.calendar = calendar;
    } else if (boot !== undefined) {
        f.schedule = 'boot';
        f.bootDelay = boot;
    } else {
        f.schedule = 'custom';
    }
    f.persistent = /^(true|yes|on|1)$/i.test(directive(timer, 'Persistent') ?? '');
    f.randomDelay = directive(timer, 'RandomizedDelaySec') ?? '';
    f.command = directive(service, 'ExecStart') ?? '';
    f.user = directive(service, 'User') ?? '';
    return f;
}

// B-68: the Timers table showed `next` and `last` as the API sends them, RFC 3339
// (`2026-09-11T15:04:29+02:00`), and `left` as Go writes a duration (`2h3m1s`); on a phone each cell
// wrapped into four lines. The table shows the run in the reader's language and time zone, short -
// day, month and time, the year only when it is not this one - and the full stamp as its title.

const locale = dateLocale;

/** A timer's next or last run for its cell: `11.09., 15:04` / `11/09, 15:04`; empty without one. */
export function runTime(iso: string | undefined, lang: string, now: Date = new Date()): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    const opts: Intl.DateTimeFormatOptions = {day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23'};
    if (d.getFullYear() !== now.getFullYear()) opts.year = 'numeric';
    return d.toLocaleString(locale(lang), opts);
}

/** The whole stamp, to the second, for the cell's title. */
export function runTimeFull(iso: string | undefined, lang: string): string {
    if (!iso) return '';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(locale(lang), {dateStyle: 'medium', timeStyle: 'medium'});
}

/** Go's time.Duration.String in whole seconds (`2h3m1s`, `45m0s`, `3.5s`); undefined for anything else. */
export function goDuration(v: string | undefined): number | undefined {
    const m = /^(?:(\d+)h)?(?:(\d+)m(?!s))?(?:(\d+(?:\.\d+)?)s)?$/.exec((v ?? '').trim());
    if (!m || (m[1] === undefined && m[2] === undefined && m[3] === undefined)) return undefined;
    return Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Math.floor(Number(m[3] ?? 0));
}

/**
 * Seconds until the next run: the box's own count (`left`, measured against the box's clock) when
 * it is one, otherwise `next` against the browser's clock; undefined when there is no next run.
 */
export function timeLeft(left: string | undefined, next: string | undefined, now: number = Date.now()): number | undefined {
    const s = goDuration(left);
    if (s !== undefined) return s;
    const at = next ? new Date(next).getTime() : Number.NaN;
    return Number.isNaN(at) || at <= now ? undefined : Math.floor((at - now) / 1000);
}
