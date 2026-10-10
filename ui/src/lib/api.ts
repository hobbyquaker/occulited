// The API client: JSON in, JSON out, errors as {error, message}. Nothing clever.
import type {AddonImages} from './addonimages';

export class ApiError extends Error {
    constructor(
        public status: number,
        public code: string,
        message: string,
        /** the answer's detail object, where the route sends one (e.g. no-space: free, required) */
        public detail?: Record<string, unknown>,
    ) {
        super(message);
    }
}

/**
 * The header credential (openccu-lite task 259, D-78): a state-changing API call that rides on the
 * session cookie alone must carry X-Occulite-Request - any value - or the daemon answers 403
 * `request-header`. A form post or a link from another site cannot set a custom header, which is
 * the point; the shell sets it on every call, the raw fetch() sites below spread it in.
 */
export const REQUEST_HEADER: Readonly<Record<string, string>> = {'X-Occulite-Request': '1'};

/** The session id the shell holds doubles as a bearer token: on a host without the cookie
 *  (confirming a network change from the new address, handed over by a ticket in main.ts) it is
 *  all we have. It never goes into a URL (task 125). */
function storedSid(): string {
    try {
        return sessionStorage.getItem('ol.sid') ?? '';
    } catch {
        return '';
    }
}

/** What every call of the shell carries as its credential beside the cookie: the header credential,
 *  and the session as Bearer when the shell holds it. For the calls that are not made through
 *  `api` - a stream read with fetch() (occulited B-47). */
export function credentialHeaders(): Record<string, string> {
    const headers: Record<string, string> = {...REQUEST_HEADER};
    const sid = storedSid();
    if (sid) headers['Authorization'] = `Bearer ${sid}`;
    return headers;
}

async function request<T>(method: string, path: string, body?: unknown, extra?: Record<string, string>, cache?: RequestCache): Promise<T> {
    const headers: Record<string, string> = {...REQUEST_HEADER, ...extra};
    // lighttpd's mod_proxy answers 411 to a body-less POST/PUT without Content-Length: always send
    // a JSON body for anything but GET
    if (body === undefined && method !== 'GET') body = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const sid = storedSid();
    if (sid) headers['Authorization'] = `Bearer ${sid}`;
    const res = await fetch(path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        ...(cache ? {cache} : {}),
    });
    const text = await res.text();
    let data: unknown = undefined;
    try {
        data = text ? JSON.parse(text) : undefined;
    } catch {
        /* non-JSON */
    }
    const e = (data ?? {}) as {error?: string; message?: string; detail?: Record<string, unknown>};
    // a wrong password typed into a confirmation (task 154) or the password change is a 401 too,
    // but the session is still there: only the others sign the shell out
    if (res.status === 401 && e.error !== 'invalid-credentials') window.dispatchEvent(new CustomEvent('ol:unauthenticated'));
    if (!res.ok && res.status !== 304) {
        throw new ApiError(res.status, e.error ?? 'http', e.message ?? `${res.status} ${res.statusText}`, e.detail);
    }
    return data as T;
}

// task 38: a multipart body (files chosen on the page) - the browser sets the boundary
async function requestForm<T>(method: string, path: string, form: FormData): Promise<T> {
    const headers: Record<string, string> = {...REQUEST_HEADER};
    const sid = storedSid();
    if (sid) headers['Authorization'] = `Bearer ${sid}`;
    const res = await fetch(path, {method, headers, body: form});
    const text = await res.text();
    let data: unknown = undefined;
    try {
        data = text ? JSON.parse(text) : undefined;
    } catch {
        /* non-JSON */
    }
    if (res.status === 401) window.dispatchEvent(new CustomEvent('ol:unauthenticated'));
    if (!res.ok) {
        const e = (data ?? {}) as {error?: string; message?: string};
        throw new ApiError(res.status, e.error ?? 'http', e.message ?? `${res.status} ${res.statusText}`);
    }
    return data as T;
}

// a text answer (a firmware bundle's changelog): the body as it is, or the refusal's {error, message}
async function requestText(path: string): Promise<string> {
    const headers: Record<string, string> = {...REQUEST_HEADER};
    const sid = storedSid();
    if (sid) headers['Authorization'] = `Bearer ${sid}`;
    const res = await fetch(path, {headers, cache: 'no-store'});
    const text = await res.text();
    if (res.status === 401) window.dispatchEvent(new CustomEvent('ol:unauthenticated'));
    if (!res.ok) {
        let e: {error?: string; message?: string} = {};
        try {
            e = JSON.parse(text) as typeof e;
        } catch {
            /* non-JSON */
        }
        throw new ApiError(res.status, e.error ?? 'http', e.message ?? `${res.status} ${res.statusText}`);
    }
    return text;
}

export const api = {
    get: <T>(path: string) => request<T>('GET', path),
    /** task 154: a GET with headers of its own (the key sheet's confirmation) that never goes into
     *  the browser's HTTP cache - upstream's lighttpd answers every path with a cacheable header */
    getWith: <T>(path: string, headers: Record<string, string>) => request<T>('GET', path, undefined, headers, 'no-store'),
    getText: (path: string) => requestText(path),
    post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
    /** a POST with headers of its own - task 185: a confirmed ticket in X-Occulite-Confirm */
    postWith: <T>(path: string, body: unknown, headers: Record<string, string>) => request<T>('POST', path, body, headers),
    put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
    patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
    del: <T>(path: string, body?: unknown) => request<T>('DELETE', path, body),
    delWith: <T>(path: string, headers: Record<string, string>) => request<T>('DELETE', path, undefined, headers),
    putForm: <T>(path: string, form: FormData) => requestForm<T>('PUT', path, form),
    postForm: <T>(path: string, form: FormData) => requestForm<T>('POST', path, form),
};

export interface Version {
    version: string;
    product: string;
    platform: string;
    variant?: string;
    /** D-44: openccu-lite's own semantic version */
    lite?: string;
}
export interface Status {
    hostname: string;
    version: Version;
    /** occulited's own version (task 133): the commit it was built from, `-hot` for a hot deploy
     *  (occulited task 16; `1.0.0-dev.38` to `1.0.0-dev.40` reported the image version here) */
    occulited_version?: string;
    /** the commit occulited was built from, the bare hash */
    occulited_commit?: string;
    uptime_s: number;
    load: number[];
    mem_total_kb: number;
    mem_available_kb: number;
    disks: {mount: string; total_kb: number; used_kb: number}[];
    time: string;
    /** the zone name (Europe/Berlin) */
    timezone: string;
    /** the POSIX string of /etc/config/TZ, when it is not the zone name */
    tz?: string;
    hm_mode: string;
    meta_recovered?: boolean;
    unclean_shutdown?: {at: string};
    /** task 34: "lxc" when this box is a container - the template is swapped on the host */
    container?: string;
    /**
     * task 94: the boot's clock gate (/run/occulite/clock-state): rtc, ntp or timeout; after a
     * timeout `synchronised` says whether chrony has synchronised since. Absent without the gate.
     */
    clock?: {state: string; synchronised: boolean; rtc_implausible?: string};
}
/** task 69: GET /api/system/v1/storage - the storage health panel */
export type StorageVerdict = 'good' | 'watch' | 'replace';
export interface StorageReason {
    level: StorageVerdict;
    code: string;
    device: string;
    count?: number;
    percent?: number;
    filesystem?: string;
    years?: number;
    bytes_per_day?: number;
}
export interface StorageSmart {
    available: boolean;
    passed?: boolean;
    message?: string;
    model?: string;
    power_on_hours?: number;
    wear_percent?: number;
    reallocated_sectors?: number;
    pending_sectors?: number;
    media_errors?: number;
    temperature_c?: number;
    read: string;
}
export interface StorageFilesystem {
    name: string;
    mounts: string[];
    errors: number;
    first_error?: string;
    last_error?: string;
    lifetime_write_bytes: number;
}
export interface StorageDevice {
    name: string;
    kind: 'sd' | 'emmc' | 'usb' | 'sata' | 'nvme' | 'virtio' | 'other';
    virtual?: boolean;
    vendor?: string;
    vendor_id?: string;
    model?: string;
    capacity_bytes: number;
    /** YYYY-MM, an SD card's or eMMC's */
    manufactured?: string;
    age_months?: number;
    mounts: string[];
    emmc?: {life_time_a: number; life_time_b: number; pre_eol?: string};
    smart?: StorageSmart;
    filesystems: StorageFilesystem[];
    io_errors: number;
    last_io_error?: string;
    last_io_error_at?: string;
    writes: {since_boot_bytes: number; per_day_bytes?: number; per_day_days?: number; per_day_basis?: 'samples' | 'boot'};
    verdict: StorageVerdict;
    reasons: StorageReason[];
}
/** task 84: a file written beside the journal */
export interface StorageLogFile {
    path: string;
    size: number;
    /** on /usr/local, the card; otherwise RAM (/var, /run) */
    userfs: boolean;
    addon?: string;
    /** longer than at the previous look, by grown_bytes */
    growing: boolean;
    grown_bytes?: number;
}
export interface StorageReport {
    verdict: StorageVerdict;
    reasons: StorageReason[];
    devices: StorageDevice[];
    checked: string;
    kernel_log: boolean;
    /** task 111: who watches the disks' health - the box itself, or its host (a VM, a container, no smartctl) */
    health_source?: 'device' | 'host';
    /** task 84: the largest files beside the journal, at most 20 - a hint, no part of the verdict */
    log_files?: StorageLogFile[];
    log_files_more?: number;
}
export interface RadioModule {
    protocol: string;
    device: string;
    device_node?: string;
    device_type?: string;
    serial?: string;
    sgtin?: string;
    firmware?: string;
    address?: string;
    address_active?: string;
}
// task 42: one device of /sys/bus/usb/devices; parent names the hub it hangs on
export interface USBDevice {
    path: string;
    parent: string;
    bus: number;
    dev: number;
    vendor: string;
    product: string;
    vendor_name?: string;
    product_name?: string;
    manufacturer?: string;
    serial?: string;
    speed?: string;
    class: string;
    hub: boolean;
    driver?: string;
    nodes: string[];
    radio: {device_node: string; device_type: string; protocols: string[]} | null;
}
// task 41: the coprocessor firmware - the files on the box per module, and the flash attempt
export interface FirmwareFile {
    name: string;
    path: string;
    source: 'shipped' | 'uploaded';
    version?: string;
    size: number;
    sha256: string;
    direction: 'upgrade' | 'downgrade' | 'same' | 'unknown';
}
export interface FirmwareModule {
    protocols: string[];
    device: string;
    device_node: string;
    device_type?: string;
    family?: string;
    dir?: string;
    running_version: string;
    files: FirmwareFile[];
    newest?: string;
    verdict: 'up-to-date' | 'newer-available' | 'older-only' | 'no-file' | 'unknown' | 'skipped' | 'unusable';
    note?: string;
    flashable: boolean;
}
export interface FlashAttempt {
    module: string;
    device_node: string;
    file: string;
    file_version: string;
    started: string;
    finished?: string;
    ok: boolean;
    error?: string;
    before?: string;
    after?: string;
    exit: number;
    /** task 102: the run's lines are in the journal, GET /log?run=<run_id> */
    run_id?: string;
}
export interface RadioFirmware {
    modules: FirmwareModule[];
    force_no_update: boolean;
    forced_version: string;
    firmware_staged: boolean;
    radio_busy: boolean;
    radio_busy_interface?: string;
    upload_dir: string;
    running: FlashAttempt | null;
    last: FlashAttempt | null;
}
/** D-66: rfd's device descriptions - the image's plus what addons added in the writable layer */
export interface DeviceDescriptions {
    path: string;
    available: boolean;
    mode: 'overlay' | 'copy' | 'none';
    entries: number;
    image: number;
    added: number;
    replaced: number;
    removed: number;
    state: string;
    restarted?: boolean;
}
export interface Radio {
    mode: string;
    host: string;
    modules: RadioModule[];
    leds: Record<string, string>;
    // subscribers ride along on GET /radio rather than having an endpoint of their own: the page
    // already polls it, the files are on a tmpfs, and the list changes only when a client
    // registers or deregisters (see internal/system/handlers.go)
    interfaces: {name: string; url: string; info: string; subscribers: InterfaceSubscriber[]}[];
}
export interface Service {
    id: string;
    kind: 'system' | 'addon';
    running: boolean;
    /** 30.1: a one-shot that ran and ended; result is systemd's (success, exit-code, …) */
    oneshot?: boolean;
    result?: string;
    /** B-65: systemd's ActiveState is failed (any unit type); the only state the page marks red */
    failed?: boolean;
    /** B-65: inactive because a condition was not met at its last start (Condition*=, ExecCondition=) */
    skipped?: boolean;
    /** task 129: the radio plan's reason on a skipped interface daemon */
    note?: string;
    /** task 94: systemd is starting the unit (activating); `starting_s` is how long, in whole seconds, at the answer */
    starting?: boolean;
    starting_s?: number;
    /** task 48: the addon's daemon runs outside its unit (started by an installer or by hand);
     *  Restart puts it back into the unit */
    stray?: boolean;
    /** openccu-lite B-158: the addon keeps a daemon and its unit is empty - the daemon ended; the
     *  time of the unit's last journal line of this boot, and those lines */
    ended?: boolean;
    ended_at?: string;
    ended_log?: string[];
    /** openccu-lite task 100: a generated addon unit's addon images, kind → URL (lib/addonimages.ts) */
    images?: AddonImages;
    /** systemd's MainPID, or (task 49) the leader of the unit's cgroup for a unit without one;
     *  for a stray addon the leader of the processes found outside the unit */
    pid?: number;
    /** task 49: how many other processes there are besides `pid` (absent when none) */
    pids_more?: number;
    /** task 49: every process, the leader first, at most 32; only where `pid` is not a MainPID */
    procs?: ServiceProc[];
    enabled: boolean;
    /** task 49: systemd's raw UnitFileState behind `enabled` (enabled, generated, static, …);
     *  absent on busybox */
    unit_file_state?: string;
    protocol?: string;
    port?: number;
    managed: boolean;
    category?: 'core' | 'addon' | 'occu' | 'system';
    /** openccu-lite task 243: the web interface runs through it (lighttpd, occulited, its helper) - restart only, no stop, no disable */
    ui?: boolean;
    description?: string;
    user?: string;
    // D-36's confinement, generated addon units only: the mode it runs in, where that came from
    // ("default" | "catalog" | "user" | "migrated" | "fallback") and whether the addon ever
    // declared a runtime block at all.
    policy_mode?: 'root' | 'confined';
    policy_source?: string;
    undeclared?: boolean;
    /** D-66: a root addon whose entry declares CAP_SYS_ADMIN keeps the right to mount */
    may_mount?: boolean;
    /** D-66: this boot's log shows the addon's refused `mount -o remount,rw /` - a hint, not an error */
    remount_refused?: boolean;
    memory_bytes?: number;
    cpu_seconds?: number;
    since?: string;
}
export interface Addon {
    id: string;
    name: string;
    version: string;
    info?: string;
    update?: string;
    config_url?: string;
    operations: string[];
    running: boolean;
    pid?: number;
    settings?: {config_url: string; name: string; description: Record<string, string>};
    enabled: boolean;
    rega_dependent?: boolean;
    rega_reason?: string;
    binary_incompatible?: boolean;
    binary_reason?: string;
    policy_mode?: 'root' | 'confined';
    policy_source?: string;
    undeclared?: boolean;
    may_mount?: boolean;
    remount_refused?: boolean;
    /** occulited task 23: the addon's lighttpd fragment failed the check and is not in use - the
     *  verdict, the line the refused statement starts on and that statement; gone once an
     *  install brings a fragment that passes */
    lighttpd_rejected?: {reason: string; line?: number; statement?: string};
    /** occulited task 28: came along from the CCU (policy source "migrated"); the failed unit's last
     *  journal line; the file its rc.d entry leads to outside /usr/local/addons/ */
    from_ccu?: boolean;
    /** the addon's unit is in systemd's failed state (task 248) */
    failed?: boolean;
    failed_log?: string;
    rc_target?: string;
    /** task 66: the scopes of the addon's own API token, from its catalogue entry */
    api_scopes?: string[];
    /** occulited task 24: the manifest declares ui.fullscreen - the frontend offers its own way back, so
     *  Settings offers to show it as the whole window (the user's choice is the preference) */
    fullscreen?: boolean;
    /** task 146: the program files are gone (a restore brings no .nobackup directory back); the dirs; the hint dismissed */
    payload_missing?: boolean;
    payload_missing_dirs?: string[];
    reinstall_dismissed?: boolean;
    /** B-267: the unit did not start - its program check (ExecCondition=) found the program missing */
    skipped?: boolean;
    /** openccu-lite task 100: the icon and logo the manifest declares, kind → URL on this origin (lib/addonimages.ts) */
    images?: AddonImages;
    /** 30.1: ran once and ended */
    oneshot?: boolean;
    result?: string;
    stray?: boolean;
    /** openccu-lite B-158: the addon keeps a daemon and its unit is empty - the daemon ended; the
     *  time of the unit's last journal line of this boot, and those lines */
    ended?: boolean;
    ended_at?: string;
    ended_log?: string[];
    /** task 88 (D-67): the settings page reads the gate's session header; the shell opens it without ?sid= */
    session_header?: boolean;
    /** task 125: the settings page gets the session's legacy alias as ?sid=@..@ (no header, and the legacy session on for it) */
    legacy_session?: boolean;
    /** task 119: the catalogue entry declares runtime.start "early" (systemd products) */
    start_early_declared?: boolean;
    /** task 119: it starts early at the next boot - declared, and not switched off */
    start_early?: boolean;
}
export interface LogLine {
    time: string;
    timestamp?: string;
    host?: string;
    facility?: string;
    severity?: string;
    tag?: string;
    unit?: string;
    pid?: number;
    message: string;
    cursor?: string;
    /** task 93: microseconds since the boot began - the kernel's own stamp for a kernel line */
    monotonic_us?: number;
    /** task 186: the area of one of occulited's lines (OCCULITED_AREA) */
    area?: string;
}
export interface ServiceProc {
    pid: number;
    cmd: string;
}
export interface Timer {
    unit: string;
    activates: string;
    next?: string;
    last?: string;
    left?: string;
    active: boolean;
    /** task 50: made on the Services page (local-<name>.timer): edited as two files, deletable */
    own?: boolean;
    enabled?: boolean;
    unit_file_state?: string;
}
/** task 50: an own timer as stored - the two texts are what the box applies */
export interface LocalTimer {
    name: string;
    unit: string;
    service: string;
    timer_file: string;
    service_file: string;
    enabled: boolean;
}
/** task 50: `systemd-analyze calendar` on an OnCalendar= expression; available false without it */
export interface CalendarCheck {
    available: boolean;
    expression: string;
    normalized?: string;
    next?: string[];
    output?: string;
}
export interface MetaNode {
    id: string;
    name: string;
    icon?: string;
    children?: MetaNode[];
}
/** An object of the store: a device, a channel, anything with a ref. */
export interface MetaObject {
    name: string;
    enums: string[];
    meta: Record<string, unknown>;
    orphaned?: boolean;
}
/** A taxonomy: localised display names (`en` required) and an ordered tree. */
export interface MetaEnum {
    name: Record<string, string>;
    tree: MetaNode[];
}
export interface MetaSnapshot {
    format: number;
    revision: number;
    objects: Record<string, MetaObject>;
    enums: Record<string, MetaEnum>;
}
/** task 85: where the journal lives - RAM only, RAM copied to the target, or persistent on it */
export type JournalMode = 'ram' | 'ram-sync' | 'persistent';
/**
 * Task 214: occulited's database file - the health history of the Interfaces page (and later the
 * state store and the datapoint history). The journal's modes; '' = the product's default.
 */
export interface DataStoreConfig {
    mode: JournalStorage;
    /** where the file lives, as task 228's location id + folder ('userfs:etc/occulite/data'); '' = default_location. Never a share. */
    location: string;
    default_location: string;
    /** ram-sync's write interval as stored, '' = default_sync_interval */
    sync_interval: string;
    default_mode: JournalMode;
    default_sync_interval: string;
    sync_intervals: string[];
    platform: string;
    effective: JournalMode;
    /** the file is open (ram-sync and persistent, unless it could not be opened) */
    open: boolean;
    path: string;
    size: number;
    rows_per_series: number;
    last_sync: string | null;
    last_sync_error?: string;
    next_sync: string | null;
    /** why the file could not be opened: the history is in memory only */
    error?: string;
    /** task 229: the snapshot on a USB stick (a usb: location) - its label and folder, whether it is
     *  plugged in, the last copy and why it failed, when a newer snapshot was loaded */
    copy?: {label: string; folder: string; present: boolean; dir?: string; last_copy: string | null; last_error?: string; loaded?: string};
    /** task 194/195: how many things each keeper holds - state: the datapoints of the state store, history: the series */
    kept?: Record<string, number>;
}
/** task 195: the datapoint history's list - what is recorded (name -> shape), the defaults, the setting */
export interface HistoryListConfig {
    datapoints: Record<string, 'sampled' | 'event'>;
    defaults: string[];
    add: string[];
    remove: string[];
    series: number;
    capped: number;
    rows_per_series: number;
    max_series: number;
}
/** '' = the product's default */
export type JournalStorage = '' | JournalMode;
/** task 23, task 85: the journal's knobs, /etc/config/journal, and what is in effect */
export interface JournalConfig {
    storage: JournalStorage;
    /** 'userfs', or a USB stick as 'usb:<label>/<dir>' (task 216, ram-sync only) */
    target: string;
    /** a USB stick target's label (udev's, as /usb/storage's label_id) and directory */
    target_label?: string;
    // task 228: a network share as the target (share:<name>/<dir>)
    target_share?: string;
    target_dir?: string;
    /** where the copies go now: the userfs directory, or the directory on the stick while it is plugged in */
    target_path?: string;
    /** journald RuntimeMaxUse, the RAM limit */
    runtime_max_use: string;
    system_max_use: string;
    system_max_file: string;
    rate_limit_burst: string;
    /** ram-sync: the copy interval ('' = 6h), the copies' size and age limits ('' = 64M, no limit) */
    sync_interval: string;
    target_max_use: string;
    target_max_age: string;
    /** deprecated, derived from storage: '1' persistent, '0' ram and ram-sync, '' the default; never sent */
    persist: '' | '0' | '1';
    platform: string;
    /** what '' means on this product */
    default_storage: JournalMode;
    default_persistent: boolean;
    /** /var/log/journal is mounted from the userfs right now (persistent or ram-sync) */
    persistent: boolean;
    /** where journald writes now */
    effective: JournalMode;
    /** the setting says RAM, the userfs is still mounted until the next reboot */
    reboot_pending: boolean;
    /** why the chosen ram-sync or persistent could not be set up (the boot script's words) */
    fallback?: string;
    /** ram-sync's copies: the last one of this boot, and the next while the copies run */
    last_sync: string | null;
    last_sync_result?: 'ok' | 'failed' | '';
    last_sync_copied: number;
    last_sync_error?: string;
    /** task 85 (D-67): why the last copy ran; absent when the box did not say */
    last_sync_reason?: 'interval' | 'early' | 'shutdown' | 'manual' | 'plug' | 'unplug';
    next_sync: string | null;
    /** bytes; null = could not be measured */
    ram_usage: number | null;
    target_usage: number | null;
    target_free: number | null;
    /** the target can take the journal: the userfs mounted and writable, the stick plugged in and writable */
    target_ok: boolean;
    /** B-223: the copies are written, and the system's own user cannot read them back (a root-squashed NFS export) */
    target_unreadable?: boolean;
    usage?: string;
}

/** task 27.8: per-daemon log levels, from /etc/config/syslog and lighttpd's drop-in */
export interface LogLevels {
    /** 1 debug, 2 info, 4 warning, 5 error */
    rfd: number;
    hs485d: number;
    /** task 101, task 297: LOGLEVEL_MULTIMACD, multimacd's own - 1 debug or 2 info, never rfd's */
    multimacd: number;
    /** task 101: occulited's own level, applied live; the areas log at debug while the level is above it */
    occulited: {level: 'debug' | 'info' | 'warn' | 'error'; debug_areas: string[]};
    /** log4j2 name: TRACE DEBUG INFO WARN ERROR */
    hmip: string;
    /** external syslog server, '' = none */
    loghost: string;
    /** the debug switches; access_log is the access log into the journal, a drop-in of its own */
    lighttpd: {request_handling: boolean; condition_handling: boolean; file_not_found: boolean; access_log: boolean};
    /** after a PUT: units that apply the change only at their next start */
    restart: string[];
    /** after a PUT: interfaces that took the level live */
    applied: string[];
    errors?: Record<string, string>;
}

/** task 27.4: a unit's effective text and occulited's own drop-in for it */
export interface UnitOverride {
    unit: string;
    /** systemctl cat: every fragment and drop-in systemd applies, read-only */
    effective: string;
    /** the override occulited keeps on the userfs and applies into /run; empty when none */
    override: string;
    /** where it is applied: /run/systemd/system/<unit>.d/50-occulite.conf */
    path: string;
}

export interface InterfaceSubscriber {
    id: string;
    url: string;
    /** the callback points at this box, rather than a client elsewhere on the network */
    local: boolean;
    /** the same id is registered with another callback too - one of the two is likely stale */
    duplicate?: boolean;
    /** the system's own subscriber, the RPC process (task 75): marked, and not removable */
    own?: boolean;
}

/** task 75: the box's own subscriber - each interface's life; connected is always true while occulited runs (D-115) */
export interface FeedStatus {
    connected: boolean;
    boot_id?: string;
    received: number;
    interfaces: {name: string; state: 'up' | 'down' | 'silent' | string; registered: boolean; last_activity: string; events: number; last_error?: string}[];
}

/** task 77: lite-rpc as the Remote access page shows it */
export interface LiteRPCView {
    available: boolean;
    streams: {per_token: number; total: number; open: number};
    buffer: {seconds: number; events: number};
}

/** task 77: one open lite-rpc stream (the Interfaces page's list) */
export interface LiteStream {
    id: string;
    transport: 'sse' | 'websocket';
    subject: {kind: 'token' | 'session'; name: string};
    remote?: string;
    filter: {interface?: string[]; address?: string[]; key?: string[]; type?: string[]};
    since: string;
    sent: number;
    dropped: number;
    last_resync?: string;
}

export interface LiteStreams {
    streams: LiteStream[];
    limits: {per_token: number; total: number};
}

/** task 79: the RPC trace's switch */
export interface RPCTraceView {
    mode: 'off' | 'until' | 'permanent';
    until?: string;
    on: boolean;
    sd_warning: boolean;
    available: boolean;
}

/** task 75: one active service message */
export interface ServiceMessage {
    interface: string;
    address: string;
    channel: string;
    key: string;
    value: unknown;
    since: string;
    /** event: seen happening; start: already active when the system looked, so the real start is earlier */
    seen: 'event' | 'start';
    type?: string;
    name?: string;
    enums?: string[];
}
export interface ServiceMessagesView {
    count: number;
    messages: ServiceMessage[];
    swept: string;
    errors: Record<string, string>;
    feed?: FeedStatus;
}

export interface NavEntry {
    /** the route, /nav/<id>; an addon's frontend is named after its proxied path (task 88: /nav/red) */
    id: string;
    /** the addon the entry belongs to (task 88); absent for a nav.d entry that is no addon, and from an older daemon */
    addon?: string;
    label: Record<string, string>;
    icon?: string;
    href: string;
    target: 'iframe' | 'blank';
    order: number;
    source: 'nav.d' | 'addon';
    /** task 88 (D-67): the addon reads the gate's session header; the shell opens href without ?sid= */
    session_header?: boolean;
    /** task 125: href gets the session's legacy alias as ?sid=@..@ */
    legacy_session?: boolean;
}

// task 35: the box's TLS certificate - the live one, the ACME settings without their secrets,
// the last attempt (system-api.md → Certificate)
export interface CertInfo {
    subject: string;
    /** the whole DN, attributes without a name hex-encoded: the tooltip */
    issuer: string;
    /** what a person reads of the issuer: its common name and first organisation */
    issuer_cn?: string;
    issuer_org?: string;
    names: string[];
    ips?: string[];
    not_before: string;
    not_after: string;
    self_signed: boolean;
    days_left: number;
    chain: number;
    serial: string;
    fingerprint: string;
}
export interface CertAttempt {
    kind: 'test' | 'issue' | 'renew';
    started: string;
    finished?: string;
    ok: boolean;
    error?: string;
    directory: string;
    names: string[];
    /** task 102: the attempt's lines are in the journal, GET /log?run=<run_id> */
    run_id?: string;
    installed: boolean;
    restarted_addons?: string[];
    certificate?: CertInfo;
    /** the run succeeded but deserves attention: a lifetime under 48 hours */
    warning?: string;
}
export interface CertField {
    key: string;
    label: string;
    secret?: boolean;
    optional?: boolean;
    placeholder?: string;
}
export interface CertProvider {
    id: string;
    name: string;
    fields: CertField[];
    note?: string;
}
export interface CertSettings {
    mode: 'self-signed' | 'acme' | 'manual';
    directory: 'letsencrypt' | 'letsencrypt-staging' | 'zerossl' | 'custom';
    directory_url: string;
    ca_root: string;
    email: string;
    eab_kid: string;
    eab_hmac_set: boolean;
    names: string[];
    challenge: 'http-01' | 'dns-01';
    dns_provider: string;
    dns_credentials: Record<string, string>;
    dns_secrets_set: Record<string, boolean>;
}
export interface CertStatus {
    settings: CertSettings;
    current: CertInfo | null;
    current_error?: string;
    /** the marker beside the live file: occulited installed it, S50lighttpd leaves it alone */
    managed: boolean;
    /** what the marker names: acme or manual (task 38) */
    managed_mode?: 'acme' | 'manual';
    managed_by?: string;
    /** the key + CSR made on the box that waits for its certificate (task 38) */
    pending: CertPending | null;
    issued: CertInfo | null;
    live_is_issued: boolean;
    last: CertAttempt | null;
    running: CertAttempt | null;
    renew_below_days: number;
    next_check?: string;
    warning?: 'expiring' | 'last-attempt-failed' | 'self-signed';
    providers: CertProvider[];
    /** the default names from the host name and the domain, proposed while none are stored */
    suggested_names?: string[];
}
// task 36: the HTTP → HTTPS redirect and HSTS (System → Security)
export interface HTTPSView {
    redirect_https: boolean;
    hsts: boolean;
    hsts_max_age_days: number;
    /** task 96: HSTS is off and the box sends max-age=0, so a browser that visits forgets it */
    hsts_clearing?: boolean;
    /** when that ends (RFC 3339); absent without a deadline */
    hsts_clearing_until?: string;
    /** the bare host name redirected to <host>.<domain>: the marker exists */
    redirect_fqdn: boolean;
    /** the bare name that is redirected */
    redirect_fqdn_host: string;
    /** <host>.<domain>; null while the box knows no domain */
    redirect_fqdn_target: string | null;
    /** active: on and redirecting; suspended: on, but not (the reason); unavailable: off and not possible (the reason); off */
    redirect_fqdn_state: 'active' | 'suspended' | 'unavailable' | 'off';
    redirect_fqdn_reason?: 'no-domain' | 'invalid-name' | 'no-certificate' | 'not-covered';
    /** the live certificate; null when none can be read - HSTS is not offered then */
    certificate: {self_signed: boolean; managed: boolean; mode: 'self-signed' | 'acme' | 'manual'} | null;
}
// task 38: mode manual
export interface CertPending {
    algorithm: string;
    fingerprint: string;
    cn: string;
    sans: string[];
    org?: string;
    created: string;
    /** the request's PEM, to copy; GET /certificate/csr downloads it */
    csr?: string;
}
export interface CertKeyInfo {
    algorithm: string;
    fingerprint: string;
}
/** POST /certificate/inspect: the parsed preview under a PEM field */
export interface CertInspect {
    certificate?: CertInfo;
    certificate_error?: string;
    chain?: number;
    chain_warning?: string;
    key?: CertKeyInfo;
    key_error?: string;
    matches?: boolean;
    pending_matches?: boolean;
    validity?: string;
}
export interface CertManualResult {
    status: CertStatus;
    result: {certificate: CertInfo; restarted_addons: string[]; warning?: string; key_from: 'upload' | 'pending'};
    restarted_addons: string[];
    warning: string;
}
