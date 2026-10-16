import {expect, test, type Locator, type Page} from '@playwright/test';
import {PORT} from './scroll';

// Task 98: the panels that open in the page. As the maintainer refined it, a Disclosure's button grows
// into its panel (*Set key* on the Keys page, *Add user*, *Create token*) and the panel shrinks back into
// it on Cancel, the page below moving in the same motion without a jump; a panel opened from elsewhere
// (task 199: the Interfaces page's *Add gateway* and *USB devices*, which sit in the row of their
// section's heading) grows out of that trigger. The open panel wears the card's surface, radius and
// shadow; under reduced motion nothing moves. Opening moves the focus to the panel's first field, Cancel
// gives it back to the button that opened it. The boot timeline's section and the LED page's <details>
// open and close in place too.
//
// The motion is seen through the Web Animations API. `holdMotion` holds every animation the page starts
// after `hold()` at its first frame, so the rects can be read at a chosen moment of it (`at`), then
// lets them run (`release`); `watchMotion` notes, every frame, which properties the running animations
// of some elements move.

type Rect = {left: number; top: number; width: number; height: number};
type Motion = {props: string[]; heights: number[]};
type Held = {__held: Animation[]; __hold: boolean};

async function holdMotion(page: Page) {
    await page.addInitScript(() => {
        const w = window as unknown as Held;
        w.__held = [];
        w.__hold = false;
        const animate = Element.prototype.animate;
        Element.prototype.animate = function (this: Element, keyframes: Keyframe[] | PropertyIndexedKeyframes | null, options?: number | KeyframeAnimationOptions) {
            const a = animate.call(this, keyframes, options);
            if (w.__hold) {
                a.pause();
                w.__held.push(a);
            }
            return a;
        };
    });
}
const hold = (page: Page) =>
    page.evaluate(() => {
        const w = window as unknown as Held;
        w.__held = [];
        w.__hold = true;
    });
/** the held motion at a moment of it: 0 its first frame, 1 its last (a hair before the end, where it still applies) */
const at = (page: Page, f: number) =>
    page.evaluate((f) => {
        const held = (window as unknown as Held).__held;
        if (held.length === 0) throw new Error('no motion was started');
        for (const a of held) {
            const d = Number(a.effect!.getTiming().duration);
            a.currentTime = Math.min(d * f, d - 0.5);
        }
    }, f);
const release = (page: Page) =>
    page.evaluate(() => {
        const w = window as unknown as Held;
        w.__hold = false;
        for (const a of w.__held) a.play();
        w.__held = [];
    });

async function watchMotion(page: Page, selector: string) {
    await page.addInitScript((sel) => {
        const w = window as unknown as {__motion: Motion};
        w.__motion = {props: [], heights: []};
        const frame = () => {
            for (const el of Array.from(document.querySelectorAll(sel))) {
                const animations = el.getAnimations();
                if (animations.length === 0) continue;
                w.__motion.heights.push(el.getBoundingClientRect().height);
                for (const a of animations) {
                    for (const k of (a.effect as KeyframeEffect).getKeyframes()) {
                        for (const p of Object.keys(k)) if (!w.__motion.props.includes(p)) w.__motion.props.push(p);
                    }
                }
            }
            requestAnimationFrame(frame);
        };
        requestAnimationFrame(frame);
    }, selector);
}
const motion = (page: Page) => page.evaluate(() => (window as unknown as {__motion: Motion}).__motion);
const resetMotion = (page: Page) =>
    page.evaluate(() => {
        const m = (window as unknown as {__motion: Motion}).__motion;
        m.props = [];
        m.heights = [];
    });

async function openRadio(page: Page) {
    await page.goto('/radio');
    await expect(page.locator('[data-process="rfd"]')).toBeVisible();
}

/** a Disclosure's own button, by its label */
const trigger = (page: Page, label: string) => page.locator('.ol-disclosure-trigger button', {hasText: new RegExp(`^${label}$`)});
/** task 199: a section's own action, in the row of its heading - what opens the Radio page's two panels */
const headButton = (page: Page, label: string) => page.locator('.ol-headrow button', {hasText: new RegExp(`^${label}$`)});
/** an open panel, by its heading */
const panel = (page: Page, heading: string) => page.getByRole('group', {name: heading});
const settled = (page: Page, selector: string) => expect.poll(() => page.evaluate((s) => document.querySelector(s)?.getAnimations().length ?? 0, selector)).toBe(0);
const still = (page: Page) => expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);

/** in the scrolling box's coordinates, so a scroll between two readings does not count as a move
    (B-129: `.ol-scrollport` is what scrolls, not the document) */
function rectOf(target: Locator): Promise<Rect> {
    return target.evaluate((el, sel) => {
        const r = el.getBoundingClientRect();
        const port = document.querySelector(sel);
        const x = port ? port.scrollLeft : window.scrollX;
        const y = port ? port.scrollTop : window.scrollY;
        return {left: r.left + x, top: r.top + y, width: r.width, height: r.height};
    }, PORT);
}
function expectNear(seen: Rect, want: Rect, what: string, tolerance = 1) {
    for (const k of ['left', 'top', 'width', 'height'] as const) {
        expect(Math.abs(seen[k] - want[k]), `${what}: ${k} ${seen[k]} against ${want[k]}`).toBeLessThanOrEqual(tolerance);
    }
}

test.describe('a panel that opens in the page', () => {
    test('the button grows into the panel and the panel shrinks back into it, the page below moving along', async ({page, baseURL}) => {
        await holdMotion(page);
        // the Keys page's Set key: a Disclosure with a button of its own (the Radio page opens its
        // two from the row of a heading since task 199, which is the case below)
        await page.context().addCookies([{name: 'stub-key', value: 'default', url: baseURL!}]);
        await page.goto('/system/keys');
        await expect(trigger(page, 'Set key')).toBeVisible();
        await still(page);

        const button = trigger(page, 'Set key');
        await button.scrollIntoViewIfNeeded();
        const below = page.getByRole('heading', {level: 2, name: 'Local key mode'});
        const box = page.locator('.ol-disclosure');
        const b = await rectOf(button);
        const y0 = (await rectOf(below)).top;
        const buttonLook = await button.evaluate((el) => {
            const s = getComputedStyle(el);
            return {radius: s.borderTopLeftRadius, background: s.backgroundColor};
        });

        // opening: at the first frame the panel is the button, and nothing below has moved
        await hold(page);
        await button.click();
        await at(page, 0);
        expectNear(await rectOf(box), b, 'the panel at the first frame of the opening is the button');
        expect(await box.evaluate((el) => getComputedStyle(el).borderTopLeftRadius)).toBe(buttonLook.radius);
        expect(await page.locator('.ol-disclosure-ghost').evaluate((el) => getComputedStyle(el).opacity)).toBe('1');
        expect(Math.abs((await rectOf(below)).top - y0), 'the heading below at the start').toBeLessThanOrEqual(1);
        await at(page, 0.5);
        const midBox = await rectOf(box);
        const yMid = (await rectOf(below)).top;
        await at(page, 1);
        const end = await rectOf(box);
        const y1 = (await rectOf(below)).top;
        expect(midBox.height).toBeGreaterThan(b.height);
        expect(midBox.height).toBeLessThan(end.height);
        expect(yMid, 'the heading below half-way').toBeGreaterThan(y0);
        expect(yMid).toBeLessThan(y1);
        await release(page);
        await still(page);
        await expect(page.locator('.ol-disclosure-ghost')).toHaveCount(0);
        // at rest: where the last frame was - no jump at the end
        expectNear(await rectOf(box), end, 'the panel at rest against its last frame');
        expect(Math.abs((await rectOf(below)).top - y1), 'the heading below at rest').toBeLessThanOrEqual(1);

        // the card's surface, radius and shadow - and not the page ground
        const usb = panel(page, 'Security key');
        const look = await usb.evaluate((el) => {
            const s = getComputedStyle(el);
            // the panels of the Keys page wear the card's tokens; the open disclosure has to match them
            const card = getComputedStyle(document.querySelector('.ol-panel')!);
            return {
                bg: s.backgroundColor,
                cardBg: card.backgroundColor,
                shadow: s.boxShadow,
                cardShadow: card.boxShadow,
                radius: s.borderTopLeftRadius,
                cardRadius: card.borderTopLeftRadius,
                ground: getComputedStyle(document.body).backgroundColor,
            };
        });
        expect(look.bg).toBe(look.cardBg);
        expect(look.bg).not.toBe(look.ground);
        expect(look.bg).not.toBe(buttonLook.background);
        expect(look.shadow).toBe(look.cardShadow);
        expect(look.shadow).not.toBe('none');
        expect(look.radius).toBe(look.cardRadius);

        // Cancel: the panel shrinks back into the button, the page below with it
        await hold(page);
        await usb.getByRole('button', {name: 'Cancel'}).click();
        await at(page, 0);
        expectNear(await rectOf(box), end, 'the panel at the first frame of the closing');
        expect(Math.abs((await rectOf(below)).top - y1), 'the heading below at the start of the closing').toBeLessThanOrEqual(1);
        await at(page, 1);
        expectNear(await rectOf(box), b, 'the panel at the last frame of the closing is the button');
        expect(Math.abs((await rectOf(below)).top - y0), 'the heading below at the end of the closing').toBeLessThanOrEqual(1);
        await release(page);
        await expect(box).toHaveCount(0);
        await expect(button).toBeFocused();
        expectNear(await rectOf(button), b, 'the button back in its place');
        expect(Math.abs((await rectOf(below)).top - y0), 'the heading below where it was').toBeLessThanOrEqual(1);
    });

    test('a panel opened from elsewhere grows out of that trigger and shrinks back into it', async ({page}) => {
        await holdMotion(page);
        await page.goto('/system/lan-devices');
        // task 222: the section's action sits under its heading (on the LAN devices page), and the panel opens from there
        const add = headButton(page, 'Add gateway');
        await add.scrollIntoViewIfNeeded();
        const c = await rectOf(add);
        const box = page.locator('.ol-disclosure');

        await hold(page);
        await add.click();
        await at(page, 0);
        expectNear(await rectOf(box), c, 'the panel at the first frame is the heading\'s button');
        await release(page);
        const form = panel(page, 'Add a gateway');
        await expect(form.getByRole('combobox').first()).toBeFocused();
        await still(page);

        await hold(page);
        await form.getByRole('button', {name: 'Cancel'}).click();
        await at(page, 1);
        expectNear(await rectOf(box), await rectOf(add), 'the panel at the last frame is the heading\'s button');
        await release(page);
        await expect(box).toHaveCount(0);
        await expect(add).toBeFocused();
    });

    // task 268: the heading's button is hidden while its panel is open - what it leaves behind is
    // taken up in the same motion, so what lies below the panel does not jump at the first or the
    // last frame
    test('the hidden trigger moves nothing below the panel', async ({page}) => {
        await holdMotion(page);
        await page.goto('/system/lan-devices');
        const add = headButton(page, 'Add gateway');
        const below = page.getByRole('heading', {level: 2}).nth(1);
        await expect(below).toBeVisible();
        const before = await rectOf(below);

        await hold(page);
        await add.click();
        await at(page, 0);
        expectNear(await rectOf(below), before, 'the heading below at the first frame');
        await release(page);
        await still(page);
        await expect(add).toBeHidden();
        const open = await rectOf(below);

        await hold(page);
        await panel(page, 'Add a gateway').getByRole('button', {name: 'Cancel'}).click();
        await at(page, 0);
        expectNear(await rectOf(below), open, 'the heading below at the first frame of the closing');
        await at(page, 1);
        expectNear(await rectOf(below), before, 'the heading below at the last frame of the closing');
        await release(page);
        await still(page);
        await expect(add).toBeVisible();
        expectNear(await rectOf(below), before, 'the heading below once closed');
    });

    test('is there at once, and gone at once, under reduced motion', async ({page}) => {
        await page.emulateMedia({reducedMotion: 'reduce'});
        await watchMotion(page, '.ol-disclosure-slot, .ol-disclosure, .ol-disclosure-body, .ol-disclosure-ghost');
        await openRadio(page);
        await resetMotion(page);

        await headButton(page, 'USB devices').click();
        // no frame in between: the panel is on the page when the click returns
        expect(await page.locator('.ol-disclosure').count()).toBe(1);
        const usb = panel(page, 'USB devices');
        await expect(usb.locator('table.ol-usb')).toBeVisible();
        // task 249: a read-only view's button says Close
        await usb.getByRole('button', {name: 'Close'}).click();
        expect(await page.locator('.ol-disclosure').count()).toBe(0);
        expect((await motion(page)).props, 'nothing was animated').toEqual([]);
        await expect(headButton(page, 'USB devices')).toBeFocused();
    });

    test('the focus goes to the first field, and back to the button that opened the panel', async ({page, baseURL}) => {
        // the default key: the button says Set key (on the Keys page since task 183)
        await page.context().addCookies([{name: 'stub-key', value: 'default', url: baseURL!}]);
        await page.goto('/system/keys');

        // its own button, with the mouse
        await trigger(page, 'Set key').click();
        await expect(page.locator('#ol-sec-key')).toBeFocused();
        await panel(page, 'Security key').getByRole('button', {name: 'Cancel'}).click();
        await expect(trigger(page, 'Set key')).toBeFocused();
        await page.goto('/system/lan-devices');

        // the heading's button, with the keyboard: the first field is the class select
        await headButton(page, 'Add gateway').focus();
        await page.keyboard.press('Enter');
        const add = panel(page, 'Add a gateway');
        await expect(add.getByRole('combobox').first()).toBeFocused();
        await add.getByRole('button', {name: 'Cancel'}).click();
        await expect(headButton(page, 'Add gateway')).toBeFocused();

        // a panel with nothing to fill in yet (the list is still loading) takes the focus itself
        await openRadio(page);
        await headButton(page, 'USB devices').click();
        await expect.poll(() => page.evaluate(() => !!document.querySelector('.ol-disclosure')?.contains(document.activeElement))).toBe(true);
    });

    test('screenshots: the Interfaces page with USB devices open', async ({page, baseURL}, info) => {
        await page.context().addCookies([{name: 'stub-key', value: 'default', url: baseURL!}]);
        await openRadio(page);
        await headButton(page, 'USB devices').click();
        await expect(page.locator('table.ol-usb')).toBeVisible();
        await expect(page.locator('.ol-disclosure')).toHaveCount(1);
        await still(page);
        await page.evaluate(() => document.fonts.ready);
        const body = await page.screenshot({fullPage: true, animations: 'disabled'});
        await info.attach(`interfaces-panels-${info.project.name}`, {body, contentType: 'image/png'});
        const dir = process.env.T98_SHOTS;
        if (dir) await page.screenshot({path: `${dir}/interfaces-panels-${info.project.name}.png`, fullPage: true, animations: 'disabled'});
    });
});

test.describe('the other parts that open in the page move the same way', () => {
    test('the boot timeline on the Services page', async ({page}) => {
        await page.addInitScript(() => localStorage.setItem('ol.bootTimeline', 'closed'));
        await watchMotion(page, '#bt-body');
        await page.goto('/system/services');
        await expect(page.locator('table.ol-timers tbody tr').first()).toBeVisible();
        const toggle = page.locator('section#boot .bt-toggle');
        await resetMotion(page);
        await toggle.click();
        await expect(page.locator('#bt-body')).toBeVisible();
        await expect.poll(async () => (await motion(page)).props).toEqual(expect.arrayContaining(['height', 'opacity']));
        await settled(page, '#bt-body');
        await resetMotion(page);
        await toggle.click();
        await expect(page.locator('#bt-body')).toHaveCount(0);
        expect((await motion(page)).props).toContain('height');
    });

    test("the LED page's details, and the browser's own toggle under reduced motion", async ({page}) => {
        await watchMotion(page, '[data-led-advanced]');
        await page.goto('/system/hardware');
        await expect(page.locator('[data-led-simple]').first()).toBeVisible();
        const details = page.locator('[data-led-advanced]');
        const summary = details.locator('summary');
        await expect(details).not.toHaveAttribute('open', '');
        await resetMotion(page);

        await summary.click();
        await expect(details).toHaveAttribute('open', '');
        await expect.poll(async () => (await motion(page)).props).toContain('height');
        await settled(page, '[data-led-advanced]');
        expect(await page.evaluate(() => localStorage.getItem('ol.led.advanced')), 'the toggle event still reports it').toBe('1');

        // closing: open until the motion has ended, then closed
        await resetMotion(page);
        await summary.click();
        await expect(details).not.toHaveAttribute('open', '');
        expect((await motion(page)).props).toContain('height');
        expect(await page.evaluate(() => localStorage.getItem('ol.led.advanced'))).toBe('0');

        await page.emulateMedia({reducedMotion: 'reduce'});
        await resetMotion(page);
        await summary.click();
        await expect(details).toHaveAttribute('open', '');
        expect((await motion(page)).props, 'nothing was animated').toEqual([]);
    });
});
