import {describe, expect, it} from 'vitest';
import {backgroundOf, changed, dimsAnything, disjoint, hexOf, isColor, mutedUntilText, normalChoice, normalizeColor, normalizeLook, overNormalOption, radioLevels, rgbOf, secondColors, swatchOf, type LEDConfig} from './led';

const COLORS = ['red', 'green', 'blue', 'yellow', 'cyan', 'magenta', 'white', 'off'];

describe('status LED model', () => {
    it('knows which colours can alternate', () => {
        expect(disjoint('yellow', 'blue')).toBe(true);
        expect(disjoint('blue', 'cyan')).toBe(false);
        expect(disjoint('white', 'red')).toBe(false);
        expect(secondColors('yellow', COLORS)).toEqual(['blue']);
        expect(secondColors('blue', COLORS)).toEqual(['red', 'green', 'yellow']);
        expect(secondColors('white', COLORS)).toEqual([]);
    });

    it('normalises a look as the API spells it', () => {
        expect(normalizeLook({color: 'off', pattern: 'fast', color2: 'red'}, COLORS)).toEqual({color: 'off', pattern: 'solid'});
        expect(normalizeLook({color: 'red', pattern: 'slow', color2: 'blue'}, COLORS)).toEqual({color: 'red', pattern: 'slow'});
        expect(normalizeLook({color: 'yellow', pattern: 'alternate'}, COLORS)).toEqual({color: 'yellow', pattern: 'alternate', color2: 'blue'});
        expect(normalizeLook({color: 'blue', pattern: 'alternate', color2: 'cyan'}, COLORS)).toEqual({color: 'blue', pattern: 'alternate', color2: 'red'});
        // white lights every channel: nothing to alternate with, so it stays steady
        expect(normalizeLook({color: 'white', pattern: 'alternate'}, COLORS)).toEqual({color: 'white', pattern: 'solid'});
    });

    it('keeps over_normal only where a pattern has an off phase (task 134)', () => {
        expect(normalizeLook({color: 'yellow', pattern: 'slow', over_normal: true}, COLORS)).toEqual({color: 'yellow', pattern: 'slow', over_normal: true});
        expect(normalizeLook({color: 'red', pattern: 'double', over_normal: true}, COLORS)).toEqual({color: 'red', pattern: 'double', over_normal: true});
        // off is not written at all, so a saved draft equals what the API answers
        expect(normalizeLook({color: 'yellow', pattern: 'slow', over_normal: false}, COLORS)).toEqual({color: 'yellow', pattern: 'slow'});
        expect(normalizeLook({color: 'yellow', pattern: 'solid', over_normal: true}, COLORS)).toEqual({color: 'yellow', pattern: 'solid'});
        expect(normalizeLook({color: 'yellow', pattern: 'alternate', over_normal: true}, COLORS)).toEqual({color: 'yellow', pattern: 'alternate', color2: 'blue'});
        expect(normalizeLook({color: 'off', pattern: 'fast', over_normal: true}, COLORS)).toEqual({color: 'off', pattern: 'solid'});
    });

    it('offers blinking over the normal colour for the four patterns, and not in the same colour', () => {
        const blue = {color: 'blue', pattern: 'solid'};
        for (const p of ['slow', 'fast', 'flash', 'double']) expect(overNormalOption({color: 'yellow', pattern: p}, blue)).toBe('yes');
        for (const p of ['solid', 'alternate']) expect(overNormalOption({color: 'yellow', pattern: p}, blue)).toBe('no');
        expect(overNormalOption({color: 'off', pattern: 'solid'}, blue)).toBe('no');
        expect(overNormalOption({color: 'blue', pattern: 'slow'}, blue)).toBe('same');
        expect(overNormalOption({color: 'cyan', pattern: 'flash'}, blue)).toBe('yes');
        // the background on the box: normal's colour, dark when normal is off, the LED off, or the option unset
        expect(backgroundOf({color: 'yellow', pattern: 'slow', over_normal: true}, blue)).toBe('blue');
        expect(backgroundOf({color: 'yellow', pattern: 'slow'}, blue)).toBe('');
        expect(backgroundOf({color: 'yellow', pattern: 'slow', over_normal: true}, {color: 'off', pattern: 'solid'})).toBe('');
        expect(backgroundOf({color: 'blue', pattern: 'slow', over_normal: true}, blue)).toBe('');
        expect(backgroundOf({color: 'yellow', pattern: 'slow', over_normal: true}, blue, false)).toBe('');
    });

    // openccu-lite task 315: any #rrggbb beside the names
    it('knows a free colour, its channels and its one spelling', () => {
        expect(isColor('#FF8000')).toBe(true);
        expect(isColor('orange')).toBe(false);
        expect(isColor('#12345')).toBe(false);
        expect(hexOf('blue')).toBe('#0000ff');
        expect(hexOf('#FF8000')).toBe('#ff8000');
        expect(hexOf('orange')).toBe('');
        expect(rgbOf('#ff8000')).toEqual([255, 128, 0]);
        expect(rgbOf('yellow')).toEqual([255, 255, 0]);
        expect(normalizeColor('#0000FF')).toBe('blue');
        expect(normalizeColor('#000000')).toBe('off');
        expect(normalizeColor('#FF8000')).toBe('#ff8000');
        expect(normalizeColor('red')).toBe('red');
        // a free colour alternates with a colour on other channels only, as the names do
        expect(disjoint('#ff8000', 'blue')).toBe(true);
        expect(disjoint('#ff8000', 'green')).toBe(false);
        expect(normalizeLook({color: '#0000FF', pattern: 'breathe'}, COLORS)).toEqual({color: 'blue', pattern: 'breathe'});
        expect(normalizeLook({color: '#ff8000', pattern: 'alternate'}, COLORS)).toEqual({color: '#ff8000', pattern: 'alternate', color2: 'blue'});
        // breathe over the normal colour pulses between the two
        expect(normalizeLook({color: 'red', pattern: 'breathe', over_normal: true}, COLORS)).toEqual({color: 'red', pattern: 'breathe', over_normal: true});
        expect(overNormalOption({color: 'red', pattern: 'breathe'}, {color: 'blue', pattern: 'solid'})).toBe('yes');
        // the swatch shows a name's softened colour and a hex value as it is
        expect(swatchOf('red')).toBe('#e5252c');
        expect(swatchOf('#FF8000')).toBe('#ff8000');
        expect(swatchOf('nothing')).toBe('transparent');
    });

    // openccu-lite task 316: the radio rows' levels by id
    it('finds a radio row\'s levels', () => {
        const c = {radio: {duty_cycle: [{enabled: true, threshold: 25, color: 'yellow', pattern: 'breathe'}], carrier_sense: [{enabled: false, threshold: 5, color: 'red', pattern: 'fast'}]}} as unknown as LEDConfig;
        expect(radioLevels(c, 'radio-duty-cycle').map((l) => l.threshold)).toEqual([25]);
        expect(radioLevels(c, 'radio-carrier-sense').map((l) => l.threshold)).toEqual([5]);
    });

    it('says whether a configuration dims, which an on/off LED cannot show', () => {
        const base = {brightness: 100, night: {enabled: false, from: '22:00', to: '06:30', show: 'errors', dim: 30}} as LEDConfig;
        expect(dimsAnything(base)).toBe(false);
        expect(dimsAnything({...base, brightness: 60})).toBe(true);
        expect(dimsAnything({...base, night: {...base.night, enabled: true}})).toBe(true);
        expect(dimsAnything({...base, night: {...base.night, enabled: true, dim: 100}})).toBe(false);
    });

    it('reads the segmented control of the look when all is fine', () => {
        expect(normalChoice({color: 'blue', pattern: 'solid'})).toBe('blue');
        expect(normalChoice({color: 'green', pattern: 'solid'})).toBe('green');
        expect(normalChoice({color: 'off', pattern: 'solid'})).toBe('off');
        expect(normalChoice({color: 'blue', pattern: 'flash'})).toBe('custom');
        expect(normalChoice({color: 'white', pattern: 'solid'})).toBe('custom');
    });

    it('tells a changed draft from the stored configuration', () => {
        const a = {enabled: true} as LEDConfig;
        expect(changed(a, {enabled: true} as LEDConfig)).toBe(false);
        expect(changed(a, {enabled: false} as LEDConfig)).toBe(true);
        expect(changed(null, a)).toBe(true);
    });

    it('says when a mute ends: the time on the same day, the date too on another (task 62)', () => {
        const now = new Date(2026, 9, 16, 10, 0);
        expect(mutedUntilText(new Date(2026, 9, 16, 11, 5).toISOString(), 'de', now)).toBe('11:05');
        expect(mutedUntilText(new Date(2026, 9, 16, 11, 5).toISOString(), 'en', now)).toBe('11:05');
        expect(mutedUntilText(new Date(2026, 9, 17, 9, 30).toISOString(), 'de', now)).toBe('17.10., 09:30');
        expect(mutedUntilText(new Date(2026, 9, 17, 9, 30).toISOString(), 'en', now)).toBe('17/10, 09:30');
        expect(mutedUntilText('nonsense', 'de', now)).toBe('');
    });
});
