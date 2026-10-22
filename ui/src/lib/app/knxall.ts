// occulited task 102: the whole KNX User Forum icon set for the icon picker (CC BY-SA 3.0 DE, see
// ./knx-all/README.txt). The names are in the bundle (names.json: the search index, and whether a
// chosen name is one of the set); the drawings are a separate gzipped asset the picker fetches when
// it opens and the browser inflates - never part of the main bundle, cached by the service worker
// and the browser like every hashed asset. The categories (the names' prefixes) carry German and
// English words, so "Lampe", "Steckdose" and "Lüfter" find something.
import namesJSON from './knx-all/names.json';
import iconsURL from './knx-all/icons.json.gz?url';

/** every icon of the set, by name, sorted */
export const ALL_NAMES: readonly string[] = namesJSON as string[];
const NAME_SET = new Set(ALL_NAMES);

/** whether the set has an icon of this name */
export function isKnxName(name: string | undefined): name is string {
    return !!name && NAME_SET.has(name);
}

/** the set's category of a name: its prefix up to the first underscore */
export function categoryOf(name: string): string {
    const i = name.indexOf('_');
    return i > 0 ? name.slice(0, i) : name;
}

/** the categories in the picker's order (the ones a switch is most likely to want first), with their words */
export const CATEGORIES: {id: string; label: string; de: string; en: string}[] = [
    {id: 'light', label: 'Lights', de: 'Licht Lampe Leuchte Beleuchtung Strahler Spot LED', en: 'light lamp lighting spot led'},
    {id: 'message', label: 'Sockets and messages', de: 'Steckdose Meldung Präsenz Anwesenheit Stecker', en: 'socket plug message presence outlet'},
    {id: 'vent', label: 'Ventilation and climate', de: 'Lüftung Lüfter Ventilator Klima Klimaanlage Luft', en: 'ventilation fan air conditioning climate'},
    {id: 'sani', label: 'Water and heating', de: 'Sanitär Wasser Heizung Pumpe Bad Dusche Bewässerung Garten Pool Heizkörper', en: 'sanitary water heating pump bath shower irrigation garden pool radiator'},
    {id: 'scene', label: 'Scenes and household', de: 'Szene Haushalt Gerät Küche Waschmaschine Spülmaschine Kaffee Essen Schlafen Lesen Fernsehen Party', en: 'scene household appliance kitchen washing dishwasher coffee dinner sleep reading tv party'},
    {id: 'it', label: 'Computers and media', de: 'Computer Rechner Netzwerk Router Fernseher Medien Drucker Server', en: 'computer network router television media printer server'},
    {id: 'audio', label: 'Audio', de: 'Audio Musik Lautsprecher Radio Ton', en: 'audio music speaker radio sound'},
    {id: 'fts', label: 'Windows, doors and shutters', de: 'Fenster Tür Tor Rollladen Rollo Jalousie Markise Garage Beschattung', en: 'window door gate shutter blind awning garage shading'},
    {id: 'secur', label: 'Security', de: 'Sicherheit Alarm Rauchmelder Kamera Sirene Schloss', en: 'security alarm smoke detector camera siren lock'},
    {id: 'measure', label: 'Measurement and energy', de: 'Messen Zähler Strom Energie Leistung Verbrauch Gas Wasser Solar', en: 'measure meter power energy consumption gas water solar'},
    {id: 'temp', label: 'Temperature', de: 'Temperatur Thermostat Thermometer Heizung Kühlen', en: 'temperature thermostat thermometer heating cooling'},
    {id: 'weather', label: 'Weather', de: 'Wetter Regen Wind Sonne Feuchte Feuchtigkeit Wolke Schnee Frost', en: 'weather rain wind sun humidity cloud snow frost'},
    {id: 'control', label: 'Controls', de: 'Steuerung Schalter Taster Regler Bedienung Pfeil Plus Minus Ein Aus', en: 'control switch button dial arrow plus minus on off'},
    {id: 'status', label: 'Status', de: 'Status Zustand Anwesenheit Urlaub Nacht Tag', en: 'status state presence holiday night day'},
    {id: 'time', label: 'Time', de: 'Zeit Uhr Timer Wecker Kalender', en: 'time clock timer alarm calendar'},
    {id: 'phone', label: 'Telephone', de: 'Telefon Anruf Klingel', en: 'phone telephone call bell'},
    {id: 'veh', label: 'Vehicles', de: 'Fahrzeug Auto Fahrrad Motorrad Laden', en: 'vehicle car bike motorcycle charging'},
    {id: 'user', label: 'People', de: 'Benutzer Person Mensch Kind', en: 'user person people child'},
    {id: 'info', label: 'Information', de: 'Info Information Hinweis Hilfe', en: 'info information notice help'},
    {id: 'edit', label: 'Editing', de: 'Bearbeiten Stift Löschen Hinzufügen Speichern', en: 'edit pen delete add save'},
    {id: 'text', label: 'Text', de: 'Text Schrift Buchstabe', en: 'text font letter'},
];
const CATEGORY_WORDS = new Map(CATEGORIES.map((c) => [c.id, fold(`${c.de} ${c.en}`)]));
const CATEGORY_RANK = new Map(CATEGORIES.map((c, i) => [c.id, i]));

/** the picker's label for a category (English; the UI translates it) */
export function categoryLabel(id: string): string {
    return CATEGORIES.find((c) => c.id === id)?.label ?? id;
}

/** the names grouped by category, in the picker's order; a category the list above does not know comes last */
export function groupByCategory(names: readonly string[]): {id: string; names: string[]}[] {
    const groups = new Map<string, string[]>();
    for (const n of names) {
        const c = categoryOf(n);
        (groups.get(c) ?? groups.set(c, []).get(c)!).push(n);
    }
    return [...groups.entries()].map(([id, list]) => ({id, names: list})).sort((a, b) => (CATEGORY_RANK.get(a.id) ?? 99) - (CATEGORY_RANK.get(b.id) ?? 99) || a.id.localeCompare(b.id));
}

/** a readable label for a name of the set: the category prefix gone, underscores as blanks ("ceiling light") */
export function humanName(name: string): string {
    const i = name.indexOf('_');
    return (i > 0 ? name.slice(i + 1) : name).replace(/_/g, ' ');
}

/** lower case, without diacritics and with the German transliterations folded (ü → u and ue → u), for matching */
export function fold(s: string): string {
    return s
        .toLowerCase()
        .replace(/ß/g, 'ss')
        .replace(/ä/g, 'a')
        .replace(/ö/g, 'o')
        .replace(/ü/g, 'u')
        .replace(/ae/g, 'a')
        .replace(/oe/g, 'o')
        .replace(/ue/g, 'u')
        .normalize('NFD')
        .replace(/[̀-ͯ]/g, '');
}

/**
 * The names a query finds: every blank-separated word of the query must occur in the icon's name
 * (underscores read as blanks), in its label (the picker's translated label for a suggested icon,
 * if given) or in its category's German and English words. An empty query finds everything.
 */
export function searchIcons(query: string, names: readonly string[], labelOf?: (name: string) => string): string[] {
    const words = fold(query).split(/\s+/).filter(Boolean);
    if (words.length === 0) return [...names];
    return names.filter((n) => {
        const hay = `${fold(n.replace(/_/g, ' '))} ${labelOf ? fold(labelOf(n)) : ''} ${CATEGORY_WORDS.get(categoryOf(n)) ?? ''}`;
        return words.every((w) => hay.includes(w));
    });
}

// ---- the drawings: fetched once, inflated in the browser
let loading: Promise<Record<string, string>> | null = null;
const inflate = async (res: Response): Promise<string> => {
    if (typeof DecompressionStream === 'undefined') throw new Error('this browser cannot inflate the icon set');
    const stream = res.body!.pipeThrough(new DecompressionStream('gzip'));
    return new Response(stream).text();
};

/** The whole set's drawings, {name: svg}; one fetch per page load, shared by every caller. Throws when it cannot be loaded. */
export function loadAllIcons(): Promise<Record<string, string>> {
    if (!loading) {
        loading = fetch(iconsURL)
            .then(async (res) => {
                if (!res.ok) throw new Error(`icon set: ${res.status}`);
                return JSON.parse(await inflate(res)) as Record<string, string>;
            })
            .catch((e: unknown) => {
                loading = null; // the next caller tries again
                throw e;
            });
    }
    return loading;
}

/** the asset's address (tests: nothing fetches it until the picker opens) */
export const ALL_ICONS_URL = iconsURL;
