import {describe, expect, it} from 'vitest';
import {warningLink, warningText, type Warning, type Words} from './warnings';

const t = (key: string, params?: Record<string, string | number>) => {
    let s = key;
    for (const [k, v] of Object.entries(params ?? {})) s = s.replaceAll(`{${k}}`, String(v));
    return s;
};
const words = (lang?: string): Words => ({t, addonName: (a) => (a.id === 'battery' ? 'Batterie' : a.id), storage: () => '', when: (iso) => iso, lang});

// occulited task 119: an addon's own notice - its words in the page's language, its name in front,
// the button to the addon
describe('an addon notice', () => {
    const w: Warning = {id: 'addon-notice', variant: 'battery/low', severity: 'warning', href: '/addon-settings/battery', params: {addon: 'battery', title: {de: 'Batterie schwach', en: 'Battery low'}, text: {de: '3 Geräte', en: '3 devices'}}};
    it('speaks the page\'s language, English when it is not German', () => {
        expect(warningText(w, words('de'))).toBe('Batterie: Batterie schwach. 3 Geräte');
        expect(warningText(w, words('en'))).toBe('Batterie: Battery low. 3 devices');
        expect(warningText(w, words())).toBe('Batterie: Battery low. 3 devices');
        // a title that ends itself keeps its own stop
        expect(warningText({...w, params: {...w.params, title: {de: 'Batterie schwach!', en: 'Battery low!'}}}, words('en'))).toBe('Batterie: Battery low! 3 devices');
    });
    it('without a text, the title alone; without params, the variant', () => {
        expect(warningText({...w, params: {addon: 'battery', title: {de: 'Voll', en: 'Full'}}}, words('de'))).toBe('Batterie: Voll');
        expect(warningText({...w, params: undefined}, words('en'))).toBe('Batterie: battery/low');
    });
    it('links to the addon', () => {
        expect(warningLink(w, t)).toEqual({href: '/addon-settings/battery', label: 'Open the addon'});
    });
});
