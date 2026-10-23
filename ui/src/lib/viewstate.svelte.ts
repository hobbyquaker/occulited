// occulited task 83 (the maintainer): one time range for every history chart of a page, and the
// subscriber lists of the component panels open or closed together. Both are view choices of this
// browser, kept in localStorage like the Addons page's filter; a browser without storage keeps them
// for the page's life.
import {SPANS, parseSpan} from './viewspan';

const SPAN_KEY = 'ol.charts.span';
const SUBS_KEY = 'ol.status.subscribers';
// task 111 (the maintainer): the disk panels' Details, open or closed in every disk panel at once
const STORAGE_KEY = 'ol.status.storage-details';

function read(key: string): string | null {
    try {
        return localStorage.getItem(key);
    } catch {
        return null;
    }
}

function write(key: string, value: string): void {
    try {
        localStorage.setItem(key, value);
    } catch {
        /* no storage */
    }
}

/** the shared choices: `span` in minutes (default 24 h, task 81), `subsOpen` the subscriber lists, `storageOpen` the disks' Details */
export const view = $state({span: parseSpan(read(SPAN_KEY)), subsOpen: read(SUBS_KEY) === '1', storageOpen: read(STORAGE_KEY) === '1'});

/** every chart switches to `m` minutes; a span the charts do not offer is ignored */
export function setChartSpan(m: number): void {
    if (!SPANS.includes(m)) return;
    view.span = m;
    write(SPAN_KEY, String(m));
}

/** opens or closes the subscriber lists of every component panel */
export function setSubsOpen(open: boolean): void {
    view.subsOpen = open;
    write(SUBS_KEY, open ? '1' : '0');
}

/** opens or closes the Details of every disk panel (task 111) */
export function setStorageOpen(open: boolean): void {
    view.storageOpen = open;
    write(STORAGE_KEY, open ? '1' : '0');
}
