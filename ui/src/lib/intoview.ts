/*
 * B-137: something that appeared away from where the user was looking - the notice with a new API
 * token, shown once, above the Create panel that made it - is brought into view, and the focus goes
 * to the control the user needs next.
 *
 * `block: 'nearest'` moves nothing when the element is already in view, and only as far as needed
 * when it is not. The focus is moved without a scroll of its own.
 */
export function bringIntoView(el: Element | null | undefined, focus?: HTMLElement | null): void {
    if (!el) return;
    el.scrollIntoView({block: 'nearest', inline: 'nearest'});
    focus?.focus({preventScroll: true});
}
