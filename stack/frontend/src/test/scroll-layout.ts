import { vi } from "vitest";

// stubScrollLayout makes the subtitle list's container-scoped scroll math
// deterministic under jsdom, which reports zero-sized rects. It models the
// subtitle list as a 100px viewport (client height 100, scroll height 1000)
// pinned to the top of the screen with every row sitting below its bottom edge,
// so any reveal needs a real downward scroll and "resting at the bottom" is a
// scrollTop near 900. It spies on scrollTo - the list-scoped API a reveal must
// use - and on scrollIntoView - the page-scrolling API it must never use, since
// that would yank the whole page when a new line arrives. Call restore() in a
// finally block.
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
  // The scrollable list gets real dimensions so bottom-pin math (scrollHeight -
  // scrollTop - clientHeight) distinguishes resting at the bottom from having
  // scrolled away; other elements keep jsdom's zeros.
  const scrollHeight = vi
    .spyOn(Element.prototype, "scrollHeight", "get")
    .mockImplementation(function (this: Element) {
      return this.tagName === "OL" ? 1000 : 0;
    });
  // happy-dom defines clientHeight on HTMLElement, not Element.
  const clientHeight = vi
    .spyOn(HTMLElement.prototype, "clientHeight", "get")
    .mockImplementation(function (this: HTMLElement) {
      return this.tagName === "OL" ? 100 : 0;
    });
  const scrollTo = vi.fn();
  const originalScrollTo = HTMLElement.prototype.scrollTo;
  HTMLElement.prototype.scrollTo =
    scrollTo as typeof HTMLElement.prototype.scrollTo;
  const restore = () => {
    getRect.mockRestore();
    scrollIntoView.mockRestore();
    scrollHeight.mockRestore();
    clientHeight.mockRestore();
    HTMLElement.prototype.scrollTo = originalScrollTo;
  };
  return { scrollTo, scrollIntoView, restore };
}
