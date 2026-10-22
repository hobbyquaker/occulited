import {describe, expect, it} from 'vitest';
import {shortenName} from './shorten';

describe('name shortening by the room or function shown (task 101)', () => {
    it.each([
        // the maintainer's examples
        ['Fenster Küche links', 'Küche', 'Fenster links'],
        ['Fenster Küche links', 'Fenster', 'Küche links'],
        // at the start and at the end
        ['Küche Licht', 'Küche', 'Licht'],
        ['Licht Küche', 'Küche', 'Licht'],
        // case-insensitive, umlauts included
        ['Fenster KÜCHE links', 'Küche', 'Fenster links'],
        ['Fenster küche links', 'KÜCHE', 'Fenster links'],
        // whole words only: a compound keeps its word
        ['Küchenlampe', 'Küche', 'Küchenlampe'],
        ['Küchen-Fenster', 'Küche', 'Küchen-Fenster'],
        ['Deckenlicht Wohnzimmer', 'Licht', 'Deckenlicht Wohnzimmer'],
        ['Bäder Lampe', 'Bad', 'Bäder Lampe'],
        // a hyphen is an edge: the word goes, the hyphen with it
        ['Küche-Fenster', 'Küche', 'Fenster'],
        ['Fenster - Küche - links', 'Küche', 'Fenster - links'],
        ['Küche: Fenster links', 'Küche', 'Fenster links'],
        ['Fenster, Küche', 'Küche', 'Fenster'],
        ['Küche · Licht', 'Küche', 'Licht'],
        ['Küche / Licht', 'Küche', 'Licht'],
        ['Küche – Licht', 'Küche', 'Licht'],
        // leftover blanks are collapsed
        ['Fenster  Küche   links', 'Küche', 'Fenster links'],
        // every occurrence
        ['Küche Küche Licht', 'Küche', 'Licht'],
        // a name of several words, blanks collapsed
        ['Lampe Flur im EG', 'Flur im EG', 'Lampe'],
        ['Lampe Flur  im EG links', 'Flur im EG', 'Lampe links'],
        ['Lampe Flur', 'Flur im EG', 'Lampe Flur'],
        // nothing to remove
        ['Wohnzimmer Deckenlicht', 'Küche', 'Wohnzimmer Deckenlicht'],
        ['Fenster links', '', 'Fenster links'],
        ['Fenster links', '   ', 'Fenster links'],
        // never to nothing: the name is the room's, or only a suffix would remain
        ['Küche', 'Küche', 'Küche'],
        ['Küche:1', 'Küche', 'Küche:1'],
        ['Küche - ', 'Küche', 'Küche - '],
        ['KÜCHE', 'Küche', 'KÜCHE'],
        // regex characters in a name are letters, not syntax
        ['Lampe (Bad) oben', 'Bad', 'Lampe () oben'],
        ['Lampe Bad (EG)', 'Bad (EG)', 'Lampe'],
        ['Licht A+B', 'A+B', 'Licht'],
        // digits are word characters too
        ['Lampe OG2 links', 'OG', 'Lampe OG2 links'],
        ['Lampe OG 2', 'OG', 'Lampe 2'],
    ])('%j without %j → %j', (name, context, want) => {
        expect(shortenName(name, context)).toBe(want);
    });
});
