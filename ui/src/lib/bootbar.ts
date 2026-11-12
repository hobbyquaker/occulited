// The reboot countdown: the bar that starts full and empties until the box is back, when the user
// reboots it, installs a system update or restores a backup. Pure logic only - no DOM, no fetch,
// no storage - so vitest runs it in node, and the same code runs in two places: occulited's own
// page (the power menu, the Status page's install, the Backup page's restore) and lighttpd's
// waiting page (deploy/lighttpd/occulite-starting.html, bundled from ui/src/starting at build).
//
// Both read and write one localStorage entry, `occulite.reboot`:
//
//   {v: 1, kind, started, expect: {down, http, ui, ready}, seen: {down?, http?, ui?}}
//
// `started` is the moment the page sent the request (epoch ms), `expect` the expected seconds of
// each phase - the shutdown until the box stops answering (down), then until lighttpd answers
// (http), then until occulited answers (ui), then until the radio interfaces are up (ready) - and
// `seen` the moments the page observed a phase end. The remaining time is the rest of the current
// phase plus the phases after it; a checkpoint replaces the finished phases' estimate with what
// they really took.
//
// The bar never refills and never jumps backwards. A checkpoint earlier than expected runs the
// fill down to the new estimate's level over half a second; a later one slows it so it ends at the
// new estimate. Empty with nothing answering, it turns into an indeterminate stripe.

import table from '../../../internal/bootexpect/defaults.json';
import {dateLocale} from './datelocale';

export type BootKind = 'reboot' | 'recovery' | 'update' | 'restore' | 'halt';
export type Checkpoint = 'down' | 'http' | 'ui';
export type Lang = 'de' | 'en';

export interface Expect {
    down: number;
    http: number;
    ui: number;
    ready: number;
}

export interface BootEntry {
    v: 1;
    kind: BootKind;
    started: number;
    expect: Expect;
    seen: Partial<Record<Checkpoint, number>>;
}

export const ENTRY_KEY = 'occulite.reboot';
/** An entry older than this belongs to no boot anybody is waiting for. */
export const STALE_MS = 30 * 60_000;
/** How long an early checkpoint takes to run the fill down. */
export const EASE_MS = 500;
/** How long past the estimate the recovery hint appears. */
export const HINT_AFTER_MS = 3 * 60_000;
/** How long past its estimate the interfaces' bar is given before it is dropped. */
export const READY_GIVE_UP_MS = 5 * 60_000;

const KINDS: readonly BootKind[] = ['reboot', 'recovery', 'update', 'restore', 'halt'];
/** The kinds that count down to the box coming back; recovery and halt do not come back here. */
export const BAR_KINDS: ReadonlySet<BootKind> = new Set<BootKind>(['reboot', 'update', 'restore']);
const ORDER: readonly Checkpoint[] = ['down', 'http', 'ui'];

interface DefaultsFile {
    default: string;
    products: Record<string, Record<string, Expect>>;
}
const DEFAULTS = table as unknown as DefaultsFile;

/** The product of the defaults table for /VERSION's PLATFORM: itself when listed, the default otherwise. */
export function productOf(platform?: string | null): string {
    const p = (platform ?? '').trim().toLowerCase();
    // not Object.hasOwn: the waiting page runs in whatever browser opens the box
    return Object.prototype.hasOwnProperty.call(DEFAULTS.products, p) ? p : DEFAULTS.default;
}

/** The built-in figures for a product and kind; recovery and halt shut down like a reboot. */
export function defaultExpect(platform: string | null | undefined, kind: BootKind): Expect {
    const k = kind === 'update' || kind === 'restore' ? kind : 'reboot';
    return {...DEFAULTS.products[productOf(platform)]![k]!};
}

export function newEntry(kind: BootKind, started: number, expect: Expect): BootEntry {
    return {v: 1, kind, started, expect: {down: expect.down, http: expect.http, ui: expect.ui, ready: expect.ready}, seen: {}};
}

const finite = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n);

/** An expectation from an answer that may be anything: every phase a number of seconds, none negative. */
export function validExpect(e: unknown): Expect | null {
    const x = e as Partial<Expect> | null;
    if (!x || typeof x !== 'object') return null;
    const out = {down: x.down, http: x.http, ui: x.ui, ready: x.ready};
    return Object.values(out).every((v) => finite(v) && v >= 0) ? (out as Expect) : null;
}

export function isStale(entry: BootEntry, now: number): boolean {
    return now - entry.started > STALE_MS;
}

/** The entry from storage, or null when there is none, it is not one, or it is stale. */
export function parseEntry(raw: string | null | undefined, now: number): BootEntry | null {
    if (!raw) return null;
    let e: Partial<BootEntry>;
    try {
        e = JSON.parse(raw) as Partial<BootEntry>;
    } catch {
        return null;
    }
    if (!e || e.v !== 1 || !KINDS.includes(e.kind as BootKind) || !finite(e.started) || e.started <= 0) return null;
    const expect = validExpect(e.expect);
    if (!expect || typeof e.seen !== 'object' || e.seen === null) return null;
    const seen: BootEntry['seen'] = {};
    let prev = e.started;
    for (const c of ORDER) {
        const at = (e.seen as Record<string, unknown>)[c];
        if (at === undefined) continue;
        if (!finite(at) || at < prev) return null;
        seen[c] = prev = at;
    }
    const entry: BootEntry = {v: 1, kind: e.kind as BootKind, started: e.started, expect, seen};
    // a start in the future is a clock that jumped, which is as good as stale
    if (isStale(entry, now) || entry.started > now + 60_000) return null;
    return entry;
}

/**
 * The entry with a checkpoint seen at `at`. A checkpoint seen before is kept, one that would come
 * after a later one is ignored (lighttpd answering again after occulited did is not a boot phase),
 * and none is earlier than the one before it.
 */
export function markSeen(entry: BootEntry, c: Checkpoint, at: number): BootEntry {
    if (entry.seen[c] !== undefined) return entry;
    const i = ORDER.indexOf(c);
    if (ORDER.slice(i + 1).some((later) => entry.seen[later] !== undefined)) return entry;
    let floor = entry.started;
    for (const before of ORDER.slice(0, i)) floor = Math.max(floor, entry.seen[before] ?? floor);
    return {...entry, seen: {...entry.seen, [c]: Math.max(at, floor)}};
}

function phasesAfter(entry: BootEntry, index: number): number {
    let s = 0;
    for (let i = index + 1; i < ORDER.length; i++) s += entry.expect[ORDER[i]!] * 1000;
    return s;
}

/** The checkpoints seen, in order, each with the estimated end it leads to. */
function events(entry: BootEntry): {at: number; end: number}[] {
    const out: {at: number; end: number}[] = [];
    ORDER.forEach((c, i) => {
        const at = entry.seen[c];
        if (at !== undefined) out.push({at, end: at + phasesAfter(entry, i)});
    });
    return out;
}

/** When occulited is expected to answer (epoch ms): the last checkpoint plus the phases after it. */
export function estimatedEnd(entry: BootEntry): number {
    const ev = events(entry);
    const last = ev[ev.length - 1];
    return last ? last.end : entry.started + phasesAfter(entry, -1);
}

/** The remaining time: the rest of the current phase and the phases after it, never below 0. */
export function remainingMs(entry: BootEntry, now: number): number {
    return Math.max(0, estimatedEnd(entry) - now);
}

const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

/** Where an on-schedule bar stands at `t` for an estimate ending at `end`. */
function level(end: number, started: number, t: number): number {
    return end <= started ? 0 : clamp01((end - t) / (end - started));
}

interface Path {
    t0: number;
    f0: number;
    end: number;
    ease: boolean;
}

function along(p: Path, t: number, started: number): number {
    if (t <= p.t0) return p.f0;
    if (p.ease) {
        if (t >= p.t0 + EASE_MS) return level(p.end, started, t);
        const target = level(p.end, started, p.t0 + EASE_MS);
        const u = (t - p.t0) / EASE_MS;
        return p.f0 + (target - p.f0) * (1 - (1 - u) ** 3);
    }
    if (p.end <= p.t0) return 0;
    return p.f0 * clamp01((p.end - t) / (p.end - p.t0));
}

/**
 * The fill at `now`, 1 (full) to 0 (empty), from the entry alone - so occulited's page and the
 * waiting page draw the same bar at the same moment. It runs toward the estimate; at each
 * checkpoint it either eases down to the new estimate's level (earlier than expected) or goes on
 * from where it stands toward the new end (later), so it never rises.
 */
export function fillAt(entry: BootEntry, now: number): number {
    const s = entry.started;
    let path: Path = {t0: s, f0: 1, end: s + phasesAfter(entry, -1), ease: false};
    for (const {at, end} of events(entry)) {
        if (at > now) break;
        const f = along(path, at, s);
        path = {t0: at, f0: f, end, ease: level(end, s, at) < f};
    }
    return along(path, now, s);
}

// ---- texts: German and English, for occulited's page and the waiting page alike ----

const TEXT = {
    starting: {en: 'openccu-lite is starting …', de: 'openccu-lite startet …'},
    backInS: {en: 'Back in about {n} s', de: 'In etwa {n} s wieder da'},
    backInMin: {en: 'Back in about {n} min', de: 'In etwa {n} min wieder da'},
    back: {en: 'The system is back', de: 'Das System ist wieder da'},
    late: {en: 'Taking longer than usual', de: 'Dauert länger als üblich'},
    hint: {
        en: 'This takes unusually long. If the system does not come back, reboot it into the recovery system: it is then at {url}',
        de: 'Das dauert ungewöhnlich lange. Kommt das System nicht wieder, starten Sie es ins Recovery-System neu: es ist dann unter {url} erreichbar',
    },
    hintNoAddress: {
        en: 'This takes unusually long. If the system does not come back, reboot it into the recovery system: it is then reachable at the system’s IP address.',
        de: 'Das dauert ungewöhnlich lange. Kommt das System nicht wieder, starten Sie es ins Recovery-System neu: es ist dann unter der IP-Adresse des Systems erreichbar.',
    },
    recovery: {en: 'The recovery system starts; it is at {url}', de: 'Das Recovery-System startet; es ist erreichbar unter {url}'},
    ready: {en: 'Radio interfaces are starting', de: 'Funkschnittstellen starten'},
    // B-104: the interfaces' bar after the reload has a heading and one sentence, naming what still
    // starts and how long it is expected to take
    readyTitle: {en: 'The system has restarted', de: 'Das System wurde neu gestartet'},
    readyIn: {en: 'The radio interfaces are still starting – about {n} s until {names} are ready.', de: 'Die Funkschnittstellen starten noch – etwa {n} s, bis {names} bereit sind.'},
    readyInOne: {en: 'The radio interfaces are still starting – about {n} s until {names} is ready.', de: 'Die Funkschnittstellen starten noch – etwa {n} s, bis {names} bereit ist.'},
    readyInAny: {en: 'The radio interfaces are still starting – about {n} s.', de: 'Die Funkschnittstellen starten noch – etwa {n} s.'},
    readyLate: {en: 'The radio interfaces are still starting – {names} take longer than usual.', de: 'Die Funkschnittstellen starten noch – {names} brauchen länger als üblich.'},
    readyLateOne: {en: 'The radio interfaces are still starting – {names} takes longer than usual.', de: 'Die Funkschnittstellen starten noch – {names} braucht länger als üblich.'},
    readyLateAny: {en: 'The radio interfaces are still starting – it takes longer than usual.', de: 'Die Funkschnittstellen starten noch – es dauert länger als üblich.'},
    and: {en: 'and', de: 'und'},
    barLabel: {en: 'Time until the system is back', de: 'Zeit, bis das System wieder da ist'},
    readyLabel: {en: 'Time until the radio interfaces are up', de: 'Zeit, bis die Funkschnittstellen bereit sind'},
    shuttingDown: {en: 'Shutting down…', de: 'Wird heruntergefahren…'},
    // openccu-lite task 283: what the waiting page says from occulited's unit state (lighttpd's 503)
    restartingTitle: {en: 'openccu-lite is restarting …', de: 'openccu-lite startet neu …'},
    restartingIn: {
        en: 'The system service stopped unexpectedly at {time}. It starts again in about {n} ({restarts} restarts so far).',
        de: 'Der Systemdienst wurde um {time} unerwartet beendet. Er startet in etwa {n} neu (bisher {restarts} Neustarts).',
    },
    restartingNow: {
        en: 'The system service stopped unexpectedly at {time} and is starting again ({restarts} restarts so far).',
        de: 'Der Systemdienst wurde um {time} unerwartet beendet und startet gerade neu (bisher {restarts} Neustarts).',
    },
    loopTitle: {en: 'The system service keeps failing', de: 'Der Systemdienst scheitert immer wieder'},
    loop: {
        en: 'It failed {fails} times in a row since {time}; the system keeps trying at longer and longer intervals{next}. Wait a few minutes or restart the system. Why it fails is in the journal: over SSH, journalctl -u occulited.',
        de: 'Er ist seit {time} {fails}-mal in Folge gescheitert; das System versucht es in immer längeren Abständen weiter{next}. Warten Sie einige Minuten oder starten Sie das System neu. Warum er scheitert, steht im Journal: per SSH mit journalctl -u occulited.',
    },
    loopNext: {en: ' (next attempt in about {n})', de: ' (nächster Versuch in etwa {n})'},
    stoppedTitle: {en: 'The system service is stopped', de: 'Der Systemdienst ist angehalten'},
    stopped: {
        en: 'It was stopped on purpose – for an update, a restore or by an administrator – and starts again when that is done.',
        de: 'Er wurde absichtlich angehalten – für ein Update, eine Wiederherstellung oder von einem Administrator – und startet wieder, wenn das erledigt ist.',
    },
    goingDown: {en: 'The system is shutting down or restarting.', de: 'Das System wird heruntergefahren oder neu gestartet.'},
    seconds: {en: '{n} s', de: '{n} s'},
    minutes: {en: '{n} min', de: '{n} min'},
    haltWait: {en: 'Wait until the system has stopped answering before you unplug it.', de: 'Warten Sie, bis das System nicht mehr antwortet, bevor Sie den Stecker ziehen.'},
} satisfies Record<string, {en: string; de: string}>;

export type BootTextKey = keyof typeof TEXT;

export function bootText(key: BootTextKey, lang: Lang, params?: Record<string, string | number>): string {
    let s: string = TEXT[key][lang];
    for (const [k, v] of Object.entries(params ?? {})) s = s.replaceAll(`{${k}}`, String(v));
    return s;
}

/** The language of a page without the shell's catalogue: the shell's stored choice, then the browser's list, English otherwise. */
export function pickLanguage(stored: string | null | undefined, languages: readonly string[] | null | undefined): Lang {
    if (stored === 'de' || stored === 'en') return stored;
    for (const l of languages ?? []) {
        const p = l.toLowerCase();
        if (p.startsWith('de')) return 'de';
        if (p.startsWith('en')) return 'en';
    }
    return 'en';
}

/** "Back in about 25 s", minutes for the long waits of an install. */
export function backIn(seconds: number, lang: Lang): string {
    const n = Math.max(1, Math.ceil(seconds));
    return n < 90 ? bootText('backInS', lang, {n}) : bootText('backInMin', lang, {n: Math.round(n / 60)});
}

/** The hint after a long wait, with the recovery system's address when the page knows it. */
export function recoveryHint(url: string, lang: Lang): string {
    return url ? bootText('hint', lang, {url}) : bootText('hintNoAddress', lang);
}

export interface BarView {
    /** 1 full .. 0 empty */
    fill: number;
    indeterminate: boolean;
    /** occulited answered */
    done: boolean;
    remainingS: number;
    /** how far along, for aria-valuenow; null while indeterminate */
    percent: number | null;
    text: string;
    /** the recovery hint, once the box is well past its estimate; '' before */
    hint: string;
}

/** What the bar shows at `now`; `url` is where the recovery system would answer (the box's IP address). */
export function barView(entry: BootEntry, now: number, lang: Lang, url = ''): BarView {
    const done = entry.seen.ui !== undefined;
    const fill = fillAt(entry, now);
    const indeterminate = !done && fill <= 0;
    const rem = remainingMs(entry, now);
    const text = done ? bootText('back', lang) : indeterminate && rem <= 0 ? bootText('late', lang) : backIn(rem / 1000, lang);
    const hint = !done && now - estimatedEnd(entry) >= HINT_AFTER_MS ? recoveryHint(url, lang) : '';
    return {fill, indeterminate, done, remainingS: Math.ceil(rem / 1000), percent: indeterminate ? null : Math.round((1 - fill) * 100), text, hint};
}

export interface ReadyView {
    fill: number;
    indeterminate: boolean;
    percent: number | null;
    /** the heading over the bar (B-104) */
    title: string;
    /** the sentence under it: what still starts, and about how long it takes (B-104) */
    text: string;
    /** the seconds left of the estimate, 0 once it has passed */
    remainingS: number;
    /** so far past the estimate that the bar is dropped */
    expired: boolean;
}

/** "HmIP-RF", "HmIP-RF and BidCos-RF" */
function joinNames(names: readonly string[], lang: Lang): string {
    if (names.length <= 1) return names[0] ?? '';
    return `${names.slice(0, -1).join(', ')} ${bootText('and', lang)} ${names[names.length - 1]}`;
}

/**
 * The thinner second bar after occulited answered: from the UI checkpoint over `expect.ready`.
 * `waiting` are the interfaces still starting (startingInterfaces); null before the services list
 * answered, and then the sentence names none.
 */
export function readyView(entry: BootEntry, now: number, lang: Lang, waiting: readonly string[] | null = null): ReadyView {
    const start = entry.seen.ui ?? entry.started;
    const end = start + entry.expect.ready * 1000;
    const fill = end <= start ? 0 : clamp01((end - now) / (end - start));
    const remainingS = Math.max(0, Math.ceil((end - now) / 1000));
    const n = waiting?.length ?? 0;
    const names = joinNames(waiting ?? [], lang);
    const text =
        remainingS > 0
            ? bootText(n === 0 ? 'readyInAny' : n === 1 ? 'readyInOne' : 'readyIn', lang, {n: remainingS, names})
            : bootText(n === 0 ? 'readyLateAny' : n === 1 ? 'readyLateOne' : 'readyLate', lang, {names});
    return {fill, indeterminate: fill <= 0, percent: fill <= 0 ? null : Math.round((1 - fill) * 100), title: bootText('readyTitle', lang), text, remainingS, expired: now > end + READY_GIVE_UP_MS};
}

// ---- checkpoints, from the health route's answers ----

export type HealthClass = Checkpoint | 'other';

/**
 * What an answer of /api/system/v1/health says about the box: no answer at all (a network error,
 * connection refused; `status` null) is down; lighttpd's 503 while occulited does not answer, or
 * any 502/503/504, is lighttpd up; occulited's own JSON with an uptime is occulited up. Anything
 * else - a 404 of the recovery system's lighttpd, a page that is not occulited's - says nothing.
 */
export function classifyHealth(status: number | null, body: unknown): HealthClass {
    if (status === null) return 'down';
    if (status === 502 || status === 503 || status === 504) return 'http';
    if (status === 200 && uptimeOf(body) !== null) return 'ui';
    return 'other';
}

export function uptimeOf(body: unknown): number | null {
    const u = (body as {uptime_s?: unknown} | null)?.uptime_s;
    return finite(u) ? u : null;
}

/**
 * Whether an answer from occulited is the box back: a smaller uptime than before the request, any
 * answer after the box was gone or lighttpd answered alone, or an occulited that started after the
 * request was sent (when the page did not know the uptime before).
 */
export function isBack(entry: BootEntry, uptimeS: number, before: number, now: number): boolean {
    if (before >= 0 && uptimeS < before) return true;
    if (entry.seen.down !== undefined || entry.seen.http !== undefined) return true;
    return uptimeS * 1000 + 2000 < now - entry.started;
}

/**
 * One poll's answer applied to the entry: the checkpoint it shows, and whether the box is back.
 * lighttpd's 503 counts only after the box was gone: lighttpd no longer depends on occulited, so
 * during the shutdown it answers alone for a while before it stops too - that 503 is not the boot.
 */
export function observe(entry: BootEntry, cls: HealthClass, uptimeS: number | null, before: number, now: number): {entry: BootEntry; back: boolean} {
    if (cls === 'down') return {entry: markSeen(entry, 'down', now), back: false};
    if (cls === 'http') return {entry: entry.seen.down === undefined ? entry : markSeen(entry, 'http', now), back: false};
    if (cls === 'ui' && uptimeS !== null && isBack(entry, uptimeS, before, now)) return {entry: markSeen(entry, 'ui', now), back: true};
    return {entry, back: false};
}

/** The part of a services listing the interfaces' bar reads. */
export interface ServiceState {
    id: string;
    running?: boolean;
    enabled?: boolean;
    skipped?: boolean;
    failed?: boolean;
}

/** The radio interfaces the ready bar waits for: the unit, and the name the sentence gives it (B-104). */
const READY_UNITS: readonly {id: string; name: string}[] = [
    {id: 'hmipserver', name: 'HmIP-RF'},
    {id: 'rfd', name: 'BidCos-RF'},
];

/**
 * The radio interfaces still starting, by name, in the order the sentence names them (B-104). A unit
 * is up when it runs, or is not there, not configured (switched off, its condition unmet) or failed -
 * a failed one does not come up by waiting.
 */
export function startingInterfaces(services: readonly ServiceState[]): string[] {
    return READY_UNITS.filter(({id}) => {
        const s = services.find((x) => x.id === id);
        return !(!s || s.running || s.skipped || s.failed || s.enabled === false);
    }).map((u) => u.name);
}

/** The radio interfaces are up: rfd and hmipserver each are, by startingInterfaces's rule. */
export function readyDone(services: readonly ServiceState[]): boolean {
    return startingInterfaces(services).length === 0;
}

/** POST /api/system/v1/boot-timing's body: what the page saw. */
export function timingBody(entry: BootEntry, readyAt?: number): {kind: BootKind; started: number; down?: number; http?: number; ui?: number; ready?: number} {
    return {kind: entry.kind, started: entry.started, ...entry.seen, ...(readyAt !== undefined && entry.seen.ui !== undefined ? {ready: Math.max(readyAt, entry.seen.ui)} : {})};
}

/**
 * occulited's unit state as lighttpd's 503 carries it (openccu-lite task 283): the file systemd's
 * hooks keep in /run while occulited does not answer. Times are Unix seconds.
 */
export interface UnitState {
    state: 'starting' | 'running' | 'restarting' | 'crash-loop' | 'stopped' | string;
    since?: number;
    restarts?: number;
    fails?: number;
    first_fail?: number;
    last_fail?: number;
    result?: string;
    next_retry?: number;
    reason?: string;
}

/** The unit state out of a health answer's body, when lighttpd put one there. */
export function unitStateOf(body: unknown): UnitState | null {
    const u = (body as {unit?: unknown} | null)?.unit;
    if (!u || typeof u !== 'object' || typeof (u as UnitState).state !== 'string') return null;
    return u as UnitState;
}

export interface UnitStateView {
    title: string;
    line: string;
    /** the recovery hint belongs to the view at once (a crash loop) */
    hint: boolean;
}

function span(seconds: number, lang: Lang): string {
    const n = Math.max(1, Math.ceil(seconds));
    return n < 90 ? bootText('seconds', lang, {n}) : bootText('minutes', lang, {n: Math.round(n / 60)});
}

function clock(unix: number | undefined, lang: Lang): string {
    if (!unix) return '';
    return new Date(unix * 1000).toLocaleTimeString(dateLocale(lang), {hour: '2-digit', minute: '2-digit', second: '2-digit'});
}

/**
 * What the waiting page says for the unit state at `now` (ms): null for a start (the page's own
 * words), a title and a line for a restart after a crash, a crash loop and a stop on purpose.
 */
export function unitStateView(u: UnitState | null, now: number, lang: Lang): UnitStateView | null {
    if (!u) return null;
    const wait = u.next_retry ? u.next_retry - now / 1000 : 0;
    switch (u.state) {
        case 'restarting': {
            const p = {time: clock(u.last_fail, lang), restarts: u.restarts ?? 0, n: span(wait, lang)};
            return {title: bootText('restartingTitle', lang), line: bootText(wait > 0 ? 'restartingIn' : 'restartingNow', lang, p), hint: false};
        }
        case 'crash-loop': {
            const next = wait > 0 ? bootText('loopNext', lang, {n: span(wait, lang)}) : '';
            return {title: bootText('loopTitle', lang), line: bootText('loop', lang, {fails: u.fails ?? 0, time: clock(u.first_fail, lang), next}), hint: true};
        }
        case 'stopped':
            if (u.reason === 'shutdown') return {title: bootText('shuttingDown', lang), line: bootText('goingDown', lang), hint: false};
            return {title: bootText('stoppedTitle', lang), line: bootText('stopped', lang), hint: false};
    }
    return null;
}
