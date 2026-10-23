// Task 179: the image's SBOM (CycloneDX 1.6, GET /api/system/v1/sbom) as the Licences page shows
// it - one flat list (the maintainer, 2026-09-19: no tree), some groups of components first, then
// everything else by name.
//
// The fork's lite-sbom.py writes each distinct licence text once: the first licence object that
// carries it has `text` and a `bom-ref`, every other one names that ref in its property
// `openccu-lite:text-ref`. A component's origin is its property `openccu-lite:origin`; a nested
// component (a JAR's library, one of occulited's Go modules) without one takes its parent's name.

export interface CdxLicenseObject {
    id?: string;
    name?: string;
    'bom-ref'?: string;
    text?: {content?: string; contentType?: string; encoding?: string};
    url?: string;
    properties?: CdxProperty[];
}
export interface CdxLicenseChoice {
    license?: CdxLicenseObject;
    expression?: string;
}
export interface CdxProperty {
    name: string;
    value?: string;
}
export interface CdxContact {
    name?: string;
}
export interface CdxComponent {
    'bom-ref'?: string;
    type?: string;
    name: string;
    group?: string;
    version?: string;
    description?: string;
    author?: string;
    authors?: CdxContact[];
    supplier?: CdxContact;
    publisher?: string;
    copyright?: string;
    purl?: string;
    licenses?: CdxLicenseChoice[];
    externalReferences?: {type: string; url: string}[];
    properties?: CdxProperty[];
    components?: CdxComponent[];
}
export interface CdxBom {
    bomFormat?: string;
    specVersion?: string;
    metadata?: {component?: CdxComponent; timestamp?: string};
    components?: CdxComponent[];
}

/**
 * the rank of a component's group in the list: 0 occulited, 1 OpenCCU, 2 Alexander Reinert, 3 Mathias
 * Dzionsko, 4 Jens Maus, 5 Michael Gernoth's hmcfgusb, 6 the KNX User Forum's icon set (occulited task
 * 116: right below hmcfgusb), 7 eQ-3's HMSL parts, 8 everything else
 */
export type Group = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8;

export interface LicenceText {
    title: string;
    text: string;
}

export interface Row {
    ref: string;
    /** every component ref the row stands for: one, or several merged by mergeRows (their deep links) */
    refs: string[];
    name: string;
    version: string;
    licence: string;
    origin: string;
    /**
     * the origins as the page links them (the maintainer, 2026-09-19): a repository's URL from the
     * SBOM (`openccu-lite:origin-url`), or the parent component's page for a nested one
     */
    origins: Origin[];
    supplier: string;
    /** who made it, as far as the SBOM says: the authors or the supplier, else the copyright holder */
    author: string;
    group: Group;
    description: string;
    copyright: string;
    purl: string;
    links: {type: string; url: string}[];
    texts: LicenceText[];
    /** lower-cased name, version, licence, origin and author, for the search */
    haystack: string;
}

export interface Origin {
    name: string;
    /** the repository */
    url?: string;
    /** the parent component's ref, for a link to its page */
    ref?: string;
}

export interface Sbom {
    product: string;
    version: string;
    rows: Row[];
}

const prop = (props: CdxProperty[] | undefined, name: string): string => props?.find((p) => p.name === name)?.value ?? '';

function decode(t: CdxLicenseObject['text']): string {
    if (!t?.content) return '';
    if (t.encoding === 'base64') {
        try {
            const bin = atob(t.content);
            return new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
        } catch {
            return '';
        }
    }
    return t.content;
}

function walk(list: CdxComponent[] | undefined, parent: CdxComponent | null, out: {c: CdxComponent; parent: CdxComponent | null}[]) {
    for (const c of list ?? []) {
        out.push({c, parent});
        walk(c.components, c, out);
    }
}

/** the licence as the list shows it: the expression, or the ids and names joined with AND */
export function licenceOf(c: CdxComponent): string {
    const parts: string[] = [];
    for (const l of c.licenses ?? []) {
        if (l.expression) parts.push(l.expression);
        else if (l.license) parts.push(l.license.id ?? l.license.name ?? '');
    }
    // several licences apply all at once, unless the generator marks them as a choice (a library
    // under the EPL or, at the user's choice, the Apache licence)
    return parts.filter(Boolean).join(prop(c.properties, 'openccu-lite:licence-choice') === 'OR' ? ' OR ' : ' AND ');
}

/** the rows whose origin is the given row (a JAR's libraries, occulited's modules, multilib32's packages) */
export function partsOf(rows: Row[], row: Row): Row[] {
    return rows.filter((r) => r.origins.some((o) => o.ref !== undefined && row.refs.includes(o.ref)));
}

function people(c: CdxComponent): string[] {
    return [c.supplier?.name, c.author, c.publisher, ...(c.authors ?? []).map((a) => a.name)].filter((x): x is string => !!x);
}

/**
 * Which of the page's front groups a component belongs to, as its rank in the list. The page does
 * not say so. The checks run in this order, and the first match wins.
 */
export function groupOf(c: CdxComponent, licence = licenceOf(c)): Group {
    const name = c.name.toLowerCase();
    if (name === 'occulited') return 0;
    if (name === 'openccu') return 1;
    const who = people(c).join(' ');
    if (/dzionsko/i.test(who) || name.startsWith('github.com/mdzio/')) return 3;
    if (/reinert/i.test(who) || name === 'generic_raw_uart' || name === 'detect_radio_module') return 2;
    if (/gernoth/i.test(who) || name === 'hmcfgusb') return 5;
    if (/jens maus/i.test(`${who} ${c.copyright ?? ''}`) || name === 'hmlangw') return 4;
    // task 116: the KNX User Forum's icon set (the SBOM names it knx-uf-iconset) right below hmcfgusb
    if (name.startsWith('knx-uf') || /knx[- ]user[- ]forum/i.test(who)) return 6;
    if (/\bHMSL\b/i.test(licence) || (c.licenses ?? []).some((l) => /HMSL/i.test(`${l.license?.id ?? ''} ${l.license?.name ?? ''}`)) || /\beQ-3\b/i.test(who)) return 7;
    return 8;
}

/** front groups first, then by name (case-insensitive), then by version */
export function compareRows(a: Row, b: Row): number {
    return a.group - b.group || a.name.localeCompare(b.name, 'en', {sensitivity: 'base'}) || a.version.localeCompare(b.version, 'en', {numeric: true});
}

export function parseSbom(bom: CdxBom): Sbom {
    // every licence text of the file, by the ref it is stored under
    const all: {c: CdxComponent; parent: CdxComponent | null}[] = [];
    walk(bom.components, null, all);
    const texts = new Map<string, string>();
    for (const {c} of all) {
        for (const l of c.licenses ?? []) {
            const ref = l.license?.['bom-ref'];
            const text = decode(l.license?.text);
            if (ref && text) texts.set(ref, text);
        }
    }
    const used = new Set<string>();
    const rows: Row[] = all.map(({c, parent}, i) => {
        const licence = licenceOf(c);
        let ref = c['bom-ref'] || `${c.name}@${c.version ?? ''}`;
        // refs are unique in a valid file; a broken one still gets distinct rows
        while (used.has(ref)) ref = `${ref}#${i}`;
        used.add(ref);
        const lt: LicenceText[] = [];
        for (const l of c.licenses ?? []) {
            const lic = l.license;
            if (!lic) continue;
            const text = decode(lic.text) || texts.get(prop(lic.properties, 'openccu-lite:text-ref')) || '';
            if (text && !lt.some((x) => x.text === text)) lt.push({title: lic.id ?? lic.name ?? '', text});
        }
        const own = prop(c.properties, 'openccu-lite:origin');
        const origin = own || parent?.name || '';
        const url = prop(c.properties, 'openccu-lite:origin-url');
        const o: Origin | null = !origin ? null : own ? {name: origin, ...(url ? {url} : {})} : {name: origin, ...(parent?.['bom-ref'] ? {ref: parent['bom-ref']} : {})};
        const version = c.version ?? '';
        const name = c.group ? `${c.group}/${c.name}` : c.name;
        return {
            ref,
            refs: [ref],
            name,
            version,
            licence,
            origin,
            origins: o ? [o] : [],
            supplier: people(c)[0] ?? '',
            author: authorOf(c),
            group: groupOf(c, licence),
            description: c.description ?? '',
            copyright: c.copyright ?? '',
            purl: c.purl ?? '',
            links: c.externalReferences ?? [],
            texts: lt,
            haystack: `${name} ${version} ${licence} ${origin} ${authorOf(c)}`.toLowerCase(),
        };
    });
    const merged = mergeRows(rows);
    merged.sort(compareRows);
    const top = bom.metadata?.component;
    return {product: top?.name ?? 'openccu-lite', version: top?.version ?? '', rows: merged};
}

/**
 * One row for one component in several places (the maintainer, 2026-09-19): the same name, version
 * and licence - busybox in the image and in the recovery system, a library in two of eQ-3's JARs -
 * with the origins comma-separated. The texts, links and copyright lines are the union, the author
 * the first one known; each merged ref still leads to the row.
 */
export function mergeRows(rows: Row[]): Row[] {
    const byKey = new Map<string, Row>();
    const out: Row[] = [];
    for (const r of rows) {
        const key = `${r.name}\u0000${r.version}\u0000${r.licence}`;
        const m = byKey.get(key);
        if (!m) {
            const first = {...r, refs: [...r.refs], texts: [...r.texts], links: [...r.links], origins: [...r.origins]};
            byKey.set(key, first);
            out.push(first);
            continue;
        }
        m.refs.push(...r.refs);
        for (const o of r.origins) if (!m.origins.some((x) => x.name === o.name)) m.origins.push(o);
        m.origin = m.origins.map((o) => o.name).join(', ');
        for (const t of r.texts) if (!m.texts.some((x) => x.text === t.text)) m.texts.push(t);
        for (const l of r.links) if (!m.links.some((x) => x.url === l.url)) m.links.push(l);
        const lines = m.copyright ? m.copyright.split('\n') : [];
        for (const l of r.copyright ? r.copyright.split('\n') : []) if (!lines.includes(l)) lines.push(l);
        m.copyright = lines.join('\n');
        m.author ||= r.author;
        m.supplier ||= r.supplier;
        m.description ||= r.description;
        m.purl ||= r.purl;
        m.group = Math.min(m.group, r.group) as Group;
        m.haystack = `${m.name} ${m.version} ${m.licence} ${m.origin} ${m.author}`.toLowerCase();
    }
    return out;
}

/**
 * The Author column (the maintainer, 2026-09-19): the named authors or the supplier, else the
 * holder of the component's copyright line ("Copyright (c) 2016-2020 Luke Edwards" -> Luke Edwards),
 * several joined; '' when the SBOM knows nobody.
 */
export function authorOf(c: CdxComponent): string {
    const named = [...(c.authors ?? []).map((a) => a.name), c.author, c.supplier?.name, c.publisher].filter((x): x is string => !!x);
    if (named.length) return [...new Set(named)].join(', ');
    return holders(c.copyright ?? '').join(', ');
}

/** the holders in copyright lines: the years, (c), © and "All rights reserved" dropped */
export function holders(copyright: string): string[] {
    const out: string[] = [];
    for (const line of copyright.split(/\n/)) {
        const h = line
            .replace(/^\s*copyright\b/i, '')
            .replace(/\(c\)|©/gi, '')
            .replace(/all rights reserved\.?/i, '')
            .replace(/^[\s,.:-]*(\d{4}(\s*[-–,]\s*\d{4})*[\s,.:-]*)+/, '')
            .replace(/^\s*by\s+/i, '')
            .replace(/[\s,.]+$/, '')
            .trim();
        if (h && !out.includes(h)) out.push(h);
    }
    return out;
}

/** the rows whose name, version, licence, origin or author contain every word of the query */
export function searchRows(rows: Row[], query: string): Row[] {
    const words = query.toLowerCase().split(/\s+/).filter(Boolean);
    if (words.length === 0) return rows;
    return rows.filter((r) => words.every((w) => r.haystack.includes(w)));
}
