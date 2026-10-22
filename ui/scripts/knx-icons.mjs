// occulited task 75: Control's state icons, the subset of the KNX User Forum icon set
// (github.com/OpenAutomationProject/knx-uf-iconset, CC BY-SA 3.0 DE) that Control maps. Regenerates
// ui/src/lib/app/knx/ from a clone of the set at the pinned commit:
//
//   git clone https://github.com/OpenAutomationProject/knx-uf-iconset.git /some/dir
//   node scripts/knx-icons.mjs /some/dir
//
// Each mapped SVG of raw_svg/ is prepared as the mock-ups were: the XML prolog, doctype, comments and
// ids stripped, viewBox 0 0 361 361 without width and height, the set's white (#FFFFFF - it draws
// white lines on a transparent ground) replaced by currentColor so the theme colours it, then
// optimised with svgo. Only the mapped icons are shipped in the bundle, with the set's licence and
// authors. The files stay separate from the code: the icons are CC BY-SA, the code is not.
//
// occulited task 102: the whole set as well, for the icon picker - ui/src/lib/app/knx-all/: names.json
// (every icon's name, in the bundle: the search index and the check of a chosen name) and
// icons.json.gz ({name: svg}, prepared the same way at one decimal on the 361-unit grid, gzipped: a
// lazily loaded asset the picker fetches when it opens and the browser inflates with
// DecompressionStream - never part of the main bundle).
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import {COMMIT, NAMES} from './knx-names.mjs';

const SVGO = 'svgo@4.1.0';

const src = process.argv[2];
if (!src) {
    console.error('usage: node scripts/knx-icons.mjs <clone of knx-uf-iconset>');
    process.exit(2);
}
const head = execFileSync('git', ['-C', src, 'rev-parse', 'HEAD'], {encoding: 'utf8'}).trim();
if (head !== COMMIT) {
    console.error(`the clone is at ${head}, the pinned commit is ${COMMIT}: git -C ${src} checkout ${COMMIT}`);
    process.exit(1);
}
const out = new URL('../src/lib/app/knx/', import.meta.url).pathname;
const outAll = new URL('../src/lib/app/knx-all/', import.meta.url).pathname;

/** prepares the named icons into dir: the set's white as currentColor, ids and metadata gone, svgo at the given precision */
function prepare(names, dir, precision) {
    fs.rmSync(dir, {recursive: true, force: true});
    fs.mkdirSync(dir, {recursive: true});
    for (const name of names) {
        let s = fs.readFileSync(path.join(src, 'raw_svg', `${name}.svg`), 'latin1');
        s = s.replace(/<\?xml[^>]*\?>/g, '').replace(/<!DOCTYPE[^>]*>/g, '').replace(/<!--[\s\S]*?-->/g, '');
        s = s.replace(/\sid="[^"]*"/g, '').replace(/#FFFFFF/gi, 'currentColor');
        s = s.replace(/<svg[^>]*>/, '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 361 361">');
        fs.writeFileSync(path.join(dir, `${name}.svg`), s.trim() + '\n');
    }
    const config = path.join(dir, '..', 'knx-svgo.config.mjs');
    fs.writeFileSync(config, `export default {multipass: true, floatPrecision: ${precision}, plugins: ['preset-default', 'removeDimensions', 'removeXMLNS']};\n`);
    try {
        execFileSync('npx', ['--yes', SVGO, '--quiet', '--config', config, '-f', dir], {stdio: 'inherit'});
    } finally {
        fs.rmSync(config, {force: true});
    }
    for (const name of names) {
        const s = fs.readFileSync(path.join(dir, `${name}.svg`), 'utf8');
        if (/#fff|\sid=|<\?xml|<!DOCTYPE/i.test(s)) throw new Error(`${name}: not clean after svgo`);
        // the inline SVG takes its colour from the circle: an icon without currentColor would stay invisible
        if (!s.includes('currentColor')) throw new Error(`${name}: no currentColor`);
    }
}
prepare(NAMES, out, 3);

// task 102: the whole set, lazily loaded
const ALL = fs.readdirSync(path.join(src, 'raw_svg')).filter((f) => f.endsWith('.svg')).map((f) => f.slice(0, -4)).sort();
const tmpAll = path.join(outAll, '..', 'knx-all-tmp');
prepare(ALL, tmpAll, 1);
const all = Object.fromEntries(ALL.map((name) => [name, fs.readFileSync(path.join(tmpAll, `${name}.svg`), 'utf8').trim()]));
fs.rmSync(tmpAll, {recursive: true, force: true});
fs.rmSync(outAll, {recursive: true, force: true});
fs.mkdirSync(outAll, {recursive: true});
const allJSON = JSON.stringify(all);
fs.writeFileSync(path.join(outAll, 'icons.json.gz'), zlib.gzipSync(allJSON, {level: 9}));
fs.writeFileSync(path.join(outAll, 'names.json'), JSON.stringify(ALL) + '\n');
fs.writeFileSync(path.join(outAll, 'README.txt'), `The whole KNX User Forum icon set for Control's icon picker (occulited task 102):
https://github.com/OpenAutomationProject/knx-uf-iconset at ${COMMIT}, ${ALL.length} icons,
licensed under CC BY-SA 3.0 DE (../knx/LICENSE.txt; authors in ../knx/AUTHORS.txt). Changed as the
icons in ../knx/ are (the white lines recoloured to currentColor, ids and metadata removed, optimised
with svgo at one decimal). icons.json.gz is {name: svg}, gzipped - a lazily loaded asset the picker
fetches when it opens, never part of the main bundle; names.json lists the names for the search
index. Regenerated by ui/scripts/knx-icons.mjs.
`);
console.log(`${ALL.length} icons in knx-all: ${allJSON.length} bytes of JSON, ${fs.statSync(path.join(outAll, 'icons.json.gz')).size} bytes gzipped`);
// the set's licence (Latin-1 upstream) and authors, as UTF-8
fs.writeFileSync(path.join(out, 'LICENSE.txt'), fs.readFileSync(path.join(src, 'license.txt'), 'latin1').trim() + '\n');
fs.writeFileSync(path.join(out, 'AUTHORS.txt'), fs.readFileSync(path.join(src, 'authors.txt'), 'latin1').replace(/\r\n/g, '\n').trim() + '\n');
fs.writeFileSync(path.join(out, 'README.txt'), `The icons Control shows for a channel's state: a subset of the KNX User Forum icon set
(https://github.com/OpenAutomationProject/knx-uf-iconset at ${COMMIT}),
licensed under CC BY-SA 3.0 DE (LICENSE.txt; authors in AUTHORS.txt). They were changed: the white
lines recoloured to currentColor, ids and metadata removed, optimised with svgo. Regenerated by
ui/scripts/knx-icons.mjs. The thermometer's fill is drawn in code (ui/src/lib/app/stateicons.ts);
the filled thermometer is a modification of temp_temperature and stays under CC BY-SA 3.0 DE as well.
`);
// the licence text the bundle's SBOM entry carries (scripts/bundle-licenses.ts VENDORED)
const vendored = new URL('./vendored/knx-uf-iconset-LICENSE', import.meta.url).pathname;
fs.writeFileSync(vendored, `knx-uf-iconset - the KNX User Forum icon set (knx-user-forum.de)
https://github.com/OpenAutomationProject/knx-uf-iconset at ${COMMIT}

${fs.readFileSync(path.join(out, 'LICENSE.txt'), 'utf8').trim()}

(All icons are licensed under Creative Commons Attribution-ShareAlike 3.0 Germany,
CC BY-SA 3.0 DE: https://creativecommons.org/licenses/by-sa/3.0/de/)

Authors:
${fs.readFileSync(path.join(out, 'AUTHORS.txt'), 'utf8').trim()}

Changes made for openccu-lite's Control: ${NAMES.length} icons in the bundle and the whole set (${ALL.length} icons) as a
lazily loaded asset for the icon picker; the white lines recoloured to currentColor, ids and
metadata removed, optimised with svgo; the thermometer (temp_temperature) drawn with a fill in its
tube. The changed icons are licensed under the same licence.
`);
console.log(`${NAMES.length} icons written to ${out}`);
