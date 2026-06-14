import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { useVerticalSplit } from "./use-vertical-split";

// Host wires the hook to a real DOM subtree so the container ref, the separator
// props, and the pointer/keyboard handlers run against actual elements.
function Host({ initial }: { initial?: number }) {
  const { containerRef, topGrow, bottomGrow, separatorProps } = useVerticalSplit(
    "Resize",
    initial,
  );
  return (
    <div ref={containerRef} data-testid="container">
      <div data-testid="top" style={{ flexGrow: topGrow, flexBasis: 0 }} />
      <div {...separatorProps} data-testid="sep" />
      <div data-testid="bottom" style={{ flexGrow: bottomGrow, flexBasis: 0 }} />
    </div>
  );
}

// stubRect pins the container's measured height (happy-dom reports 0) so the
// pointer-position-to-ratio math has a known denominator.
function stubRect(el: HTMLElement, top: number, height: number) {
  el.getBoundingClientRect = () =>
    ({
      top,
      height,
      bottom: top + height,
      left: 0,
      right: 0,
      width: 0,
      x: 0,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
}

describe("useVerticalSplit", () => {
  test("starts at the given ratio, reported as a percentage", () => {
    render(<Host initial={0.5} />);
    expect(screen.getByTestId("sep")).toHaveAttribute("aria-valuenow", "50");
  });

  test("defaults to a top-pane majority so the bottom pane is the minority", () => {
    render(<Host />);
    const sep = screen.getByTestId("sep");
    // The default split favours the top pane (subtitles) so the bottom pane
    // (fact checks) is the minority of the height.
    expect(Number(sep.getAttribute("aria-valuenow"))).toBeGreaterThan(50);
  });

  test("clamps an out-of-range initial ratio to the max bound", () => {
    render(<Host initial={0.95} />);
    expect(screen.getByTestId("sep")).toHaveAttribute("aria-valuenow", "80");
  });

  test("arrow keys nudge the split and stop at the bounds", () => {
    render(<Host initial={0.78} />);
    const sep = screen.getByTestId("sep");
    // 0.78 + 0.04 = 0.82, clamped to the 0.8 max.
    fireEvent.keyDown(sep, { key: "ArrowDown" });
    expect(sep).toHaveAttribute("aria-valuenow", "80");
    // 0.8 - 0.04 = 0.76.
    fireEvent.keyDown(sep, { key: "ArrowUp" });
    expect(sep).toHaveAttribute("aria-valuenow", "76");
  });

  test("dragging repartitions from the pointer position and stops on release", () => {
    render(<Host initial={0.5} />);
    const sep = screen.getByTestId("sep");
    sep.setPointerCapture = () => {};
    sep.hasPointerCapture = () => false;
    stubRect(screen.getByTestId("container"), 0, 200);

    fireEvent.pointerDown(sep, { pointerId: 1, clientY: 0 });
    fireEvent.pointerMove(sep, { pointerId: 1, clientY: 60 }); // 60/200 = 0.30
    expect(sep).toHaveAttribute("aria-valuenow", "30");

    fireEvent.pointerUp(sep, { pointerId: 1 });
    // A move after release must not keep resizing.
    fireEvent.pointerMove(sep, { pointerId: 1, clientY: 140 });
    expect(sep).toHaveAttribute("aria-valuenow", "30");
  });

  test("clamps a drag past the minimum to the lower bound", () => {
    render(<Host initial={0.5} />);
    const sep = screen.getByTestId("sep");
    sep.setPointerCapture = () => {};
    sep.hasPointerCapture = () => false;
    stubRect(screen.getByTestId("container"), 0, 200);

    fireEvent.pointerDown(sep, { pointerId: 1, clientY: 0 });
    fireEvent.pointerMove(sep, { pointerId: 1, clientY: 10 }); // 0.05, clamped to 0.2
    expect(sep).toHaveAttribute("aria-valuenow", "20");
  });

  test("a move without a preceding pointer-down does nothing", () => {
    render(<Host initial={0.5} />);
    const sep = screen.getByTestId("sep");
    stubRect(screen.getByTestId("container"), 0, 200);
    fireEvent.pointerMove(sep, { pointerId: 1, clientY: 20 });
    expect(sep).toHaveAttribute("aria-valuenow", "50");
  });
});
