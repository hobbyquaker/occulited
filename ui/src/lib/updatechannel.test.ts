import {describe, expect, it} from 'vitest';
import {CHANNELS, normalChannel} from './updatechannel';

// occulited task 118: anything but beta or dev is stable, as the daemon reads system_update.channel
describe('normalChannel', () => {
    it('keeps the three channels and takes anything else for stable', () => {
        expect(CHANNELS).toEqual(['stable', 'beta', 'dev']);
        for (const c of CHANNELS) expect(normalChannel(c)).toBe(c);
        for (const c of [undefined, null, '', 'pre', 'all', 'Beta', 42]) expect(normalChannel(c)).toBe('stable');
    });
});
