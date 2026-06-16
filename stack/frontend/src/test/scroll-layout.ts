import { vi } from "vitest";

// stubScrollLayout makes the subtitle list's container-scoped scroll math
// deterministic under jsdom, which reports zero-sized rects. It models the
// subtitle list as a 100px viewport pinned to the top of the screen with every
// row sitting below its bottom edge, so any reveal needs a real downward scroll.
// It spies on scrollTo - the list-scoped API a reveal must use - and on
// scrollIntoView - the page-scrolling API it must never use, since that would
// yank the whole page when a new line arrives. Call restore() in a finally block.
export function stubScrollLayout() {
  const rectFor = (el: Element): DOMRect => {
    const [top, bottom] = el.tagName === "OL" ? [0, 100] : [200, 220];
    return {
      top,
      bottom,
      left: 0,
      right: 0,
      width: 0,
      height: bottom - top,
      x: 0,
      y: top,
      toJSON: () => ({}),
    } as DOMRect;
  };
  const getRect = vi
    .spyOn(HTMLElement.prototype, "getBoundingClientRect")
    .mockImplementation(function (this: HTMLElement) {
      return rectFor(this);
    });
  const scrollIntoView = vi
    .spyOn(HTMLElement.prototype, "scrollIntoView")
    .mockImplementation(() => {});
  const scrollTo = vi.fn();
  const originalScrollTo = HTMLElement.prototype.scrollTo;
  HTMLElement.prototype.scrollTo =
    scrollTo as typeof HTMLElement.prototype.scrollTo;
  const restore = () => {
    getRect.mockRestore();
    scrollIntoView.mockRestore();
    HTMLElement.prototype.scrollTo = originalScrollTo;
  };
  return { scrollTo, scrollIntoView, restore };
}
