import type {Page} from '@playwright/test';
import {expect, test} from './fixtures';
import {fitsWindow} from './scroll';

// occulited task 55 (the maintainer, 2026-10-15): the star badge in an addon card's corner a size
// up, "gh-logo star-count star" - GitHub's mark (Octicons, currentColor), the count, the star, as
// high as the text; it links the repository and says what it counts; a card without a count keeps
// its Repository link, and the title beside the badge is not squeezed.

const card = (page: Page, id: string) => page.locator(`#addon-row-${id}`);

test('mark, count and star in that order, at the card text size, linking the repository', async ({page}) => {
    await page.goto('/addons');
    const badge = card(page, 'redmatic').locator('a.cat-stars');
    await expect(badge).toHaveAttribute('href', 'https://github.com/rdmtc/RedMatic');
    await expect(badge).toHaveAttribute('rel', /noopener/);
    await expect(badge).toHaveAttribute('aria-label', /^GitHub stars: \d+$/);
    // the three parts in order: the mark, the count, the star
    const parts = await badge.evaluate((a) => [...a.children].map((c) => c.className.split(' ').find((n) => n.startsWith('cat-'))));
    expect(parts).toEqual(['cat-mark', 'cat-count', 'cat-star']);
    await expect(badge.locator('.cat-count')).toHaveText('1.2k');
    // the mark follows the text colour (dark mode too): a filled shape in currentColor
    const mark = badge.locator('[data-github-mark] svg');
    await expect(mark).toHaveAttribute('fill', 'currentColor');
    const colours = await badge.evaluate((a) => ({text: getComputedStyle(a).color, mark: getComputedStyle(a.querySelector('svg')!).fill}));
    expect(colours.mark).toBe(colours.text);
    // at least the card's secondary text, and the mark and star as high as the text
    const size = (sel: string) => card(page, 'redmatic').locator(sel).first().evaluate((e) => parseFloat(getComputedStyle(e).fontSize));
    expect(await size('.cat-count')).toBeGreaterThanOrEqual(await size('.ad-desc'));
    const h = async (sel: string) => (await badge.locator(sel).boundingBox())!;
    const count = await h('.cat-count'), m = await h('[data-github-mark] svg'), star = await h('.cat-star');
    expect(m.height).toBeGreaterThanOrEqual(12);
    expect(m.height).toBeLessThanOrEqual(count.height + 1);
    // all three on one line, the mark before the count before the star
    expect(m.x + m.width).toBeLessThanOrEqual(count.x + 0.5);
    expect(count.x + count.width).toBeLessThanOrEqual(star.x + 0.5);
    expect(Math.abs(m.y + m.height / 2 - (count.y + count.height / 2))).toBeLessThanOrEqual(2);
    // the title keeps its room: the badge does not overlap it
    const title = (await card(page, 'redmatic').locator('.ad-name').boundingBox())!;
    const box = (await badge.boundingBox())!;
    expect(title.x + title.width).toBeLessThanOrEqual(box.x + 0.5);
    expect(await fitsWindow(page)).toBe(true);
});

test('no count: the Repository link stays, without the mark', async ({page}) => {
    await page.goto('/addons');
    const link = card(page, 'tm-devices').locator('a.cat-stars');
    await expect(link).toHaveText(/^Repository/);
    await expect(link.locator('[data-github-mark]')).toHaveCount(0);
});

test('German: GitHub-Sterne in the label', async ({page}) => {
    await page.addInitScript(() => localStorage.setItem('ol.language', 'de'));
    await page.goto('/addons');
    await expect(card(page, 'redmatic').locator('a.cat-stars')).toHaveAttribute('aria-label', /^GitHub-Sterne: \d+$/);
});
