import {describe, expect, it} from 'vitest';
import fixture from '../../test/stub/sbom.json';
import {groupOf, licenceOf, parseSbom, partsOf, searchRows, type CdxBom, authorOf, holders} from './sbom';

const sbom = parseSbom(fixture as CdxBom);
const names = sbom.rows.map((r) => r.name);

describe('the Licenses page model (task 179)', () => {
    it('is one flat list: nested components are rows of their own, with their parent as origin', () => {
        expect(names).toContain('io.netty/netty-codec');
        expect(names).toContain('github.com/mdzio/go-hmccu');
        expect(sbom.rows.find((r) => r.name === 'io.netty/netty-codec')?.origin).toBe('HMIPServer.jar');
        // a nested component's own origin property wins over its parent
        expect(sbom.rows.find((r) => r.name === 'svelte')?.origin).toBe('occulited UI bundle');
        expect(sbom.rows.length).toBe(19);
    });

    it("puts the front groups first, then the rest by name", () => {
        // task 116: the KNX User Forum's icon set right below hmcfgusb, before eQ-3's parts
        expect(names.slice(0, 10)).toEqual(['occulited', 'OpenCCU', 'detect_radio_module', 'generic_raw_uart', 'github.com/mdzio/go-hmccu', 'hmlangw', 'hmcfgusb', 'knx-uf-iconset', 'HMIPServer.jar', 'rfd']);
        const rest = sbom.rows.filter((r) => r.group === 8).map((r) => r.name.toLowerCase());
        expect(rest).toEqual([...rest].sort((a, b) => a.localeCompare(b, 'en')));
    });

    it('recognises the groups by license, by author and by name', () => {
        expect(groupOf({name: 'occulited', licenses: [{license: {id: 'GPL-3.0-only'}}]})).toBe(0);
        expect(groupOf({name: 'github.com/mdzio/go-hmccu', licenses: [{license: {id: 'GPL-3.0-only'}}]})).toBe(3);
        expect(groupOf({name: 'x', authors: [{name: 'Mathias Dzionsko'}]})).toBe(3);
        expect(groupOf({name: 'recovery-system', authors: [{name: 'Jens Maus'}]})).toBe(4);
        expect(groupOf({name: 'raw-uart', authors: [{name: 'Alexander Reinert'}]})).toBe(2);
        expect(groupOf({name: 'generic_raw_uart'})).toBe(2);
        expect(groupOf({name: 'flash-hmcfgusb', supplier: {name: 'Michael Gernoth'}})).toBe(5);
        expect(groupOf({name: 'hmlangw', licenses: [{license: {id: 'MIT'}}]})).toBe(4);
        expect(groupOf({name: 'x', copyright: 'Copyright (c) 2015 Oliver Kastl, Jens Maus'})).toBe(4);
        expect(groupOf({name: 'OpenCCU', authors: [{name: 'Jens Maus'}], licenses: [{license: {id: 'Apache-2.0'}}]})).toBe(1);
        // task 116: the icon set by its name as the SBOM carries it, or by its author
        expect(groupOf({name: 'knx-uf-iconset', authors: [{name: 'KNX-User-Forum (mfd)'}], licenses: [{license: {id: 'CC-BY-SA-3.0-DE'}}]})).toBe(6);
        expect(groupOf({name: 'icons', authors: [{name: 'KNX User Forum'}]})).toBe(6);
        expect(groupOf({name: 'x', licenses: [{license: {name: 'LicenseRef-HMSL-2.0'}}]})).toBe(7);
        expect(groupOf({name: 'x', licenses: [{expression: 'HMSL-2.0 AND Apache-2.0'}]})).toBe(7);
        expect(groupOf({name: 'libXmlRpc.so', supplier: {name: 'eQ-3 AG'}, licenses: [{license: {id: 'LGPL-2.1-only'}}]})).toBe(7);
        expect(groupOf({name: 'busybox', licenses: [{license: {id: 'GPL-2.0-only'}}]})).toBe(8);
    });

    it('finds a license text stored once through its ref', () => {
        const linux = sbom.rows.find((r) => r.name === 'linux');
        expect(linux?.texts[0]?.text).toContain('GNU GENERAL PUBLIC LICENSE');
        expect(sbom.rows.find((r) => r.name === 'lighttpd')?.texts).toEqual([]);
    });

    it('searches name, version, license and origin, every word', () => {
        expect(searchRows(sbom.rows, 'busybox').map((r) => r.name)).toEqual(['busybox']);
        expect(searchRows(sbom.rows, 'hmipserver').map((r) => r.name)).toEqual(['HMIPServer.jar', 'com.google.code.gson/gson', 'io.netty/netty-codec']);
        expect(searchRows(sbom.rows, 'mit occulited').map((r) => r.name)).toEqual(['svelte']);
        expect(searchRows(sbom.rows, 'dzionsko').map((r) => r.name)).toEqual(['github.com/mdzio/go-hmccu']);
        expect(searchRows(sbom.rows, '  ')).toHaveLength(sbom.rows.length);
    });

    it('reads the product and version from the metadata', () => {
        expect(sbom.product).toBe('openccu-lite');
        expect(sbom.version).toBe('1.0.0-dev.14');
    });
});

describe('the Author column', () => {
    it('names the authors or the supplier, else the copyright holders', () => {
        expect(authorOf({name: 'x', authors: [{name: 'Alexander Reinert'}]})).toBe('Alexander Reinert');
        expect(authorOf({name: 'x', supplier: {name: 'eQ-3 AG'}})).toBe('eQ-3 AG');
        expect(authorOf({name: 'x', copyright: 'Copyright (c) 2016-2020 Luke Edwards <luke@example.org>'})).toBe('Luke Edwards <luke@example.org>');
        expect(authorOf({name: 'x', copyright: 'Copyright © 2009, 2012 Google Inc. All rights reserved.\nCopyright 2014 The Go Authors'})).toBe('Google Inc, The Go Authors');
        expect(authorOf({name: 'x'})).toBe('');
    });
    it('reads a holder without years', () => {
        expect(holders('Copyright (C) Erik Andersen')).toEqual(['Erik Andersen']);
    });
});

describe('one component in several places', () => {
    it('is one row with the origins comma-separated, and every ref leads to it', () => {
        const bb = sbom.rows.filter((r) => r.name === 'busybox');
        expect(bb).toHaveLength(1);
        expect(bb[0]!.origin).toBe('buildroot, recovery-system');
        expect(bb[0]!.refs).toEqual(['busybox@1.37.0', 'recovery-system/busybox@1.37.0']);
        expect(searchRows(sbom.rows, 'recovery-system').map((r) => r.name).sort()).toEqual(['busybox', 'recovery-system']);
    });
    it('keeps rows apart that differ in version or license', () => {
        const bom: CdxBom = {components: [
            {name: 'a', version: '1', licenses: [{license: {id: 'MIT'}}], properties: [{name: 'openccu-lite:origin', value: 'x'}]},
            {name: 'a', version: '1', licenses: [{license: {id: 'MIT'}}], properties: [{name: 'openccu-lite:origin', value: 'y'}]},
            {name: 'a', version: '2', licenses: [{license: {id: 'MIT'}}], properties: [{name: 'openccu-lite:origin', value: 'z'}]},
            {name: 'a', version: '1', licenses: [{license: {id: 'ISC'}}], properties: [{name: 'openccu-lite:origin', value: 'w'}]},
        ]};
        expect(parseSbom(bom).rows.map((r) => `${r.version} ${r.licence} ${r.origin}`)).toEqual(['1 MIT x, y', '1 ISC w', '2 MIT z']);
    });
});

describe('the origins', () => {
    it('lead to a repository, or to the parent component', () => {
        const row = (n: string) => sbom.rows.find((r) => r.name === n)!;
        expect(row('generic_raw_uart').origins).toEqual([{name: 'piVCCU', url: 'https://github.com/alexreinert/piVCCU'}]);
        expect(row('io.netty/netty-codec').origins).toEqual([{name: 'HMIPServer.jar', ref: 'HMIPServer.jar@3.89.9'}]);
        expect(row('busybox').origins.map((o) => o.name)).toEqual(['buildroot', 'recovery-system']);
        expect(row('occulited').origins).toEqual([{name: 'openccu-lite'}]);
    });
});


describe('the license column', () => {
    it('joins licenses that all apply with AND, a choice with OR', () => {
        expect(licenceOf({name: 'x', licenses: [{license: {id: 'GPL-2.0-or-later'}}, {license: {id: 'LGPL-2.1-or-later'}}]})).toBe('GPL-2.0-or-later AND LGPL-2.1-or-later');
        expect(licenceOf({name: 'x', licenses: [{license: {id: 'EPL-2.0'}}, {license: {id: 'Apache-2.0'}}], properties: [{name: 'openccu-lite:licence-choice', value: 'OR'}]})).toBe('EPL-2.0 OR Apache-2.0');
    });
});

describe('a component of parts', () => {
    it('lists the rows it is the origin of', () => {
        const jar = sbom.rows.find((r) => r.name === 'HMIPServer.jar')!;
        expect(partsOf(sbom.rows, jar).map((r) => r.name)).toEqual(['com.google.code.gson/gson', 'io.netty/netty-codec']);
        expect(partsOf(sbom.rows, sbom.rows.find((r) => r.name === 'busybox')!)).toEqual([]);
    });
});
