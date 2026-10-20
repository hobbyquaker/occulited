import {describe, expect, it} from 'vitest';

import {CARRIER_BAD, DUTY_LEVELS, MEM_LEVELS, LOAD_STEPS, RATE_OUTLIER_FLOOR, RATE_STEPS, carrierLevel, formatLoad, formatRate, levelOf, loadSeries, memSeries, mergeMax, niceTop, rateSeries, scalePeak, segments, sliceSpan, spanMax, timeLabels} from './history';

// task 53: the chart's arithmetic - the top tick, the time labels, the gaps, the merge of a
// dual-stack radio's two histories - and the levels the two pages share (task 54)
const at = (min: number) => new Date(Date.UTC(2026, 8, 11, 10, min)).toISOString();

describe('niceTop', () => {
    it.each([
        [0, 1],
        [0.4, 1],
        [0.8, 1],
        [1, 2],
        [1.2, 2],
        [1.6, 2],
        [1.7, 5],
        [3, 5],
        [4, 5],
        [4.1, 10],
        [7, 10],
        [12, 20],
        [16, 20],
        [21, 50],
        [40, 50],
        [41, 100],
        [50, 100],
        [80, 100],
    ])('a peak of %s % gets a top of %s % with room above it', (peak, top) => {
        expect(niceTop(peak)).toBe(top);
    });
    // B-67: a flat duty cycle drew as a full graph - the peak's own step was the top
    it.each([1, 2, 5, 10, 20, 50])('a steady %s % never runs along the top edge', (steady) => {
        expect(niceTop(steady)).toBeGreaterThan(steady);
        expect(steady / niceTop(steady)).toBeLessThanOrEqual(0.8);
    });
    it('leaves a fifth of the band above every peak up to 80 %', () => {
        for (let peak = 0.05; peak <= 80; peak += 0.05) expect(peak / niceTop(peak), `peak ${peak}`).toBeLessThanOrEqual(0.8 + 1e-9);
    });
    it.each([
        [81, 100],
        [100, 100],
        [101, 200],
        [160, 200],
        [161, 300],
    ])('a peak of %s % near or beyond the budget gets a top of %s %', (peak, top) => {
        expect(niceTop(peak)).toBe(top);
    });
    it.each([NaN, -3, Infinity])('a nonsense peak (%s) gets a top of 1 %', (peak) => {
        expect(niceTop(peak)).toBe(1);
    });
});

describe('timeLabels', () => {
    it('is HH:MM at both ends within a day', () => {
        const l = timeLabels(new Date(2026, 8, 11, 9, 5), new Date(2026, 8, 11, 10, 5), 'de-DE');
        expect(l).toEqual({start: '09:05', end: '10:05'});
    });
    it('puts the day in front when the span crosses midnight', () => {
        const l = timeLabels(new Date(2026, 8, 11, 23, 40), new Date(2026, 8, 12, 0, 20), 'de-DE');
        expect(l.start).toMatch(/^11\.09\.? 23:40$/);
        expect(l.end).toMatch(/^12\.09\.? 00:20$/);
    });
    it('writes a 24-hour clock in English too', () => {
        const l = timeLabels(new Date(2026, 8, 11, 20, 5), new Date(2026, 8, 11, 21, 5), 'en');
        expect(l).toEqual({start: '20:05', end: '21:05'});
    });
});

describe('sliceSpan', () => {
    it('keeps the samples of the last n minutes before the newest one', () => {
        const s = [0, 30, 59, 60, 61, 120].map((m) => ({t: at(m), v: m}));
        expect(sliceSpan(s, 60).map((x) => x.v)).toEqual([60, 61, 120]);
        expect(sliceSpan(s, 1440)).toHaveLength(6);
        expect(sliceSpan([], 60)).toEqual([]);
    });
    it('drops samples stamped after the last one written and sorts the rest (B-74)', () => {
        const s = [0, 30, 59, 61].map((m) => ({t: at(m), v: m}));
        // three from a boot whose clock read 2067, then the series goes on with the right time
        const wrong = [0, 1, 5].map((m) => ({t: new Date(Date.UTC(2067, 6, 4, 18, m)).toISOString(), v: -1}));
        const later = [{t: at(62), v: 62}, {t: at(50), v: 50}, {t: at(70), v: 70}];
        expect(sliceSpan([...s, ...wrong, ...later], 60).map((x) => x.v)).toEqual([30, 50, 59, 61, 62, 70]);
        expect(spanMax([...s, ...wrong, ...later], 60)).toBe(70);
    });
});

describe('segments', () => {
    it('is one run for an unbroken minute-by-minute series', () => {
        const s = [0, 1, 2, 3].map((m) => ({t: at(m), v: 1}));
        expect(segments(s).map((r) => r.length)).toEqual([4]);
    });
    it('breaks the line at a sample that was not up, leaving that sample out', () => {
        const s = [{t: at(0), v: 1}, {t: at(1), v: 2, up: false}, {t: at(2), v: 3}, {t: at(3), v: 4}];
        expect(segments(s).map((r) => r.map((x) => x.v))).toEqual([[1], [3, 4]]);
    });
    it('breaks the line across a hole in time (a reboot)', () => {
        const s = [0, 1, 2, 3, 20, 21, 22].map((m) => ({t: at(m), v: m}));
        expect(segments(s).map((r) => r.map((x) => x.v))).toEqual([[0, 1, 2, 3], [20, 21, 22]]);
    });
    it('draws a lone sample as a run of one', () => {
        expect(segments([{t: at(0), v: 5}])).toEqual([[{t: at(0), v: 5}]]);
        expect(segments([])).toEqual([]);
    });
});

describe('mergeMax', () => {
    it('is the per-poll maximum over the stacks, aligned at the tails', () => {
        const a = [1, 2, 3, 4].map((dc, i) => ({t: at(i), dc, up: true}));
        const b = [9, 1].map((dc, i) => ({t: at(i + 2), dc, up: true}));
        expect(mergeMax([a, b])).toEqual([{t: at(2), v: 9, up: true}, {t: at(3), v: 4, up: true}]);
    });
    it('ignores a stack that was down at the poll and marks the poll down when every stack was', () => {
        const a = [{t: at(0), dc: 5, up: true}, {t: at(1), dc: 0, up: false}];
        const b = [{t: at(0), dc: 0, up: false}, {t: at(1), dc: 0, up: false}];
        expect(mergeMax([a, b])).toEqual([{t: at(0), v: 5, up: true}, {t: at(1), v: 0, up: false}]);
    });
    it('passes a single history through and copes with none', () => {
        expect(mergeMax([[{t: at(0), dc: 3, up: true}]])).toEqual([{t: at(0), v: 3, up: true}]);
        expect(mergeMax([])).toEqual([]);
        expect(mergeMax([[]])).toEqual([]);
    });
});

// task 151: the carrier sense series - the stacks that reported it, a gap where none did
describe('mergeMax over the carrier sense', () => {
    it('takes the stacks that reported it and leaves a gap where none did', () => {
        const bidcos = [0, 1, 2].map((i) => ({t: at(i), dc: 3, up: true}));
        const hmip = [{t: at(0), dc: 5, cs: 4, up: true}, {t: at(1), dc: 5, up: true}, {t: at(2), dc: 5, cs: 0, up: true}];
        expect(mergeMax([bidcos, hmip], 'cs')).toEqual([{t: at(0), v: 4, up: true}, {t: at(1), v: 0, up: false}, {t: at(2), v: 0, up: true}]);
    });
    it('leaves out a stack that was down', () => {
        expect(mergeMax([[{t: at(0), dc: 0, cs: 7, up: false}]], 'cs')).toEqual([{t: at(0), v: 0, up: false}]);
    });
});

describe('carrierLevel', () => {
    it('is red above 10 % and ok up to it, with no amber band', () => {
        expect(CARRIER_BAD).toBe(10);
        expect(carrierLevel(0)).toBe('ok');
        expect(carrierLevel(10)).toBe('ok');
        expect(carrierLevel(11)).toBe('error');
    });
});

// task 152: the span's maximum beside the current value
describe('spanMax', () => {
    const series = [{t: at(0), v: 30}, {t: at(50), v: 99, up: false}, {t: at(60), v: 12}, {t: at(90), v: 4}];
    it('is the highest sample that was up within the span', () => {
        expect(spanMax(series, 60)).toBe(12);
        expect(spanMax(series, 360)).toBe(30);
    });
    it('is null without a sample', () => {
        expect(spanMax([], 60)).toBeNull();
        expect(spanMax([{t: at(0), v: 5, up: false}], 60)).toBeNull();
    });
});

describe('levelOf', () => {
    it('is amber from 70 and red from 90 with the shared duty cycle levels', () => {
        expect(levelOf(69, DUTY_LEVELS.warn, DUTY_LEVELS.err)).toBe('ok');
        expect(levelOf(70, DUTY_LEVELS.warn, DUTY_LEVELS.err)).toBe('warn');
        expect(levelOf(75, DUTY_LEVELS.warn, DUTY_LEVELS.err)).toBe('warn');
        expect(levelOf(90, DUTY_LEVELS.warn, DUTY_LEVELS.err)).toBe('error');
    });
    // occulited task 91: the memory gauge warns from 90 % (was 85), red from 95 %
    it('is amber from 90 and red from 95 with the memory levels', () => {
        expect(levelOf(85, MEM_LEVELS.warn, MEM_LEVELS.err)).toBe('ok');
        expect(levelOf(89, MEM_LEVELS.warn, MEM_LEVELS.err)).toBe('ok');
        expect(levelOf(90, MEM_LEVELS.warn, MEM_LEVELS.err)).toBe('warn');
        expect(levelOf(94, MEM_LEVELS.warn, MEM_LEVELS.err)).toBe('warn');
        expect(levelOf(95, MEM_LEVELS.warn, MEM_LEVELS.err)).toBe('error');
    });
});

// occulited task 13: the event rates - per minute (task 17), their own steps from half an event
// per minute, a gap where the subscriber was not registered, and the figure as the panel writes it
describe('rates', () => {
    it('scales a quiet rate to its own steps with room above it', () => {
        expect(niceTop(0.3, RATE_STEPS)).toBe(0.5);
        expect(niceTop(1, RATE_STEPS)).toBe(2);
        expect(niceTop(6, RATE_STEPS)).toBe(10);
        expect(niceTop(0, RATE_STEPS)).toBe(0.5);
        expect(niceTop(250, RATE_STEPS)).toBe(500);
        expect(niceTop(90, RATE_STEPS)).toBe(200);
        // the duty cycle keeps its own steps
        expect(niceTop(0.03)).toBe(1);
    });
    // occulited B-79: a lone spike of 500 a minute among values of two pressed the line onto the
    // baseline - the scale leaves it out and the chart draws it clipped at its top
    it('scales past a lone outlier', () => {
        const hour: number[] = Array.from({length: 60}, (_, i) => (i % 3 === 0 ? 3 : 2));
        hour[59] = 500;
        expect(scalePeak(hour, RATE_OUTLIER_FLOOR)).toBe(3);
        expect(niceTop(scalePeak(hour, RATE_OUTLIER_FLOOR), RATE_STEPS)).toBe(5);
        // without it, as the duty cycle's chart does, the band reaches the spike
        expect(niceTop(Math.max(...hour), RATE_STEPS)).toBe(1000);
        // two spikes in an hour are more than one in fifty: they are the data, and the band reaches them
        const two = [...hour];
        two[30] = 480;
        expect(scalePeak(two, RATE_OUTLIER_FLOOR)).toBe(500);
        // a day of 500 samples with a few restart spikes left: they are out
        const day = Array.from({length: 500}, () => 2);
        for (const i of [40, 200, 380]) day[i] = 600;
        expect(scalePeak(day, RATE_OUTLIER_FLOOR)).toBe(2);
        // below the floor nothing is an outlier: a quiet hour's burst of a dozen is drawn as it is
        const quiet = Array.from({length: 60}, () => 0);
        quiet[10] = 12;
        expect(scalePeak(quiet, RATE_OUTLIER_FLOOR)).toBe(12);
        // busy neighbours: no outlier
        expect(scalePeak([40, 50, 60, 150], RATE_OUTLIER_FLOOR)).toBe(150);
        expect(scalePeak([], RATE_OUTLIER_FLOOR)).toBe(0);
        expect(scalePeak([7], RATE_OUTLIER_FLOOR)).toBe(7);
    });
    it('makes the chart series, with the gaps', () => {
        const rs = [{t: '2026-10-03T12:00:00Z', in: 30, up: true}, {t: '2026-10-03T12:01:00Z', in: 0, up: false}];
        expect(rateSeries(rs)).toEqual([{t: rs[0]!.t, v: 30, up: true}, {t: rs[1]!.t, v: 0, up: false}]);
        expect(rateSeries(undefined)).toEqual([]);
    });
    it('writes a rate with the decimals it needs, in the language', () => {
        expect(formatRate(0, 'en')).toBe('0');
        expect(formatRate(0.017, 'en')).toBe('0.02');
        expect(formatRate(0.42, 'de')).toBe('0,42');
        expect(formatRate(2.345, 'en')).toBe('2.3');
        expect(formatRate(12.6, 'en')).toBe('13');
        expect(formatRate(2, 'en')).toBe('2');
    });
});

describe('the system history (occulited task 37)', () => {
    const h = [{t: '2026-10-13T10:00:00Z', load: 0.42, mem: 33}, {t: '2026-10-13T10:01:00Z', load: 3.6, mem: -1}];
    it('maps the load and the memory, an unknown memory breaking the line', () => {
        expect(loadSeries(h)).toEqual([{t: h[0]!.t, v: 0.42}, {t: h[1]!.t, v: 3.6}]);
        expect(memSeries(h)).toEqual([{t: h[0]!.t, v: 33, up: true}, {t: h[1]!.t, v: 0, up: false}]);
        expect(loadSeries(undefined)).toEqual([]);
        expect(memSeries(undefined)).toEqual([]);
    });
    it('writes a load with two places, in the language', () => {
        expect(formatLoad(0.4, 'en')).toBe('0.40');
        expect(formatLoad(0.4, 'de')).toBe('0,40');
        expect(formatLoad(12.345, 'en')).toBe('12.3');
    });
    it('scales the band to a readable top', () => {
        expect(niceTop(0.2, LOAD_STEPS)).toBe(0.5);
        expect(niceTop(3.6, LOAD_STEPS)).toBe(8);
    });
});
