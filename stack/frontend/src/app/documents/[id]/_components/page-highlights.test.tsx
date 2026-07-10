import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { AnchorRange } from "@/lib/pdf/anchor";
import type { AnchoredSentence, Rect } from "@/lib/pdf/overlay";
import { PageHighlights, type PageMeasurement } from "./page-highlights";

afterEach(() => {
  vi.restoreAllMocks();
});

// A fake text layer: two items, and a measurement seam that returns one rect for
// a range anchored to the first item and nothing otherwise, so the overlay
// resolves boxes without a browser layout engine.
function measurement(over: Partial<PageMeasurement> = {}): PageMeasurement {
  return {
    items: ["Le chômage a baissé.", "Une autre phrase."],
    measure: (range: AnchorRange): Rect[] =>
      range.startItem === 0
        ? [{ left: 12, top: 30, width: 120, height: 14 }]
        : [],
    ...over,
  };
}

function credible(over: Partial<AnchoredSentence> = {}): AnchoredSentence {
  return {
    seq: 0,
    text: "Le chômage a baissé.",
    occurrence: 1,
    verdict: "credible",
    snippet: "Le chômage a baissé",
    ...over,
  };
}

function renderOverlay(props: Partial<Parameters<typeof PageHighlights>[0]> = {}) {
  return render(
    <PageHighlights
      getMeasurement={() => measurement()}
      sentences={[credible()]}
      layoutVersion={1}
      pageWidth={600}
      selectedSeq={null}
      selectToken={0}
      onSelect={() => {}}
      {...props}
    />,
  );
}

describe("PageHighlights", () => {
  test("draws a positioned, verdict-colored box for an anchored sentence", async () => {
    renderOverlay();
    const box = await screen.findByRole("button");
    expect(box.className).toContain("bg-verdict-credible/20");
    expect(box.style.left).toBe("12px");
    expect(box.style.top).toBe("30px");
    expect(box.style.width).toBe("120px");
  });

  test("renders nothing when no sentence anchors", async () => {
    const { container } = renderOverlay({
      sentences: [credible({ text: "Phrase absente." })],
    });
    await waitFor(() =>
      expect(screen.queryByRole("button")).not.toBeInTheDocument(),
    );
    expect(container).toBeEmptyDOMElement();
  });

  test("hovering a box shows the verdict tooltip; leaving hides it", async () => {
    renderOverlay();
    const box = await screen.findByRole("button");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

    fireEvent.mouseEnter(box);
    const tip = screen.getByRole("tooltip");
    expect(tip).toHaveTextContent(fr.app.claims.verdicts.credible);
    expect(tip).toHaveTextContent("Le chômage a baissé");

    // Leaving the whole overlay dismisses the tooltip.
    fireEvent.mouseLeave(box.parentElement as HTMLElement);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  test("focusing a box shows the tooltip (keyboard reachable)", async () => {
    renderOverlay();
    const box = await screen.findByRole("button");
    box.focus();
    fireEvent.focus(box);
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
  });

  test("clicking a box selects its sentence (the shared seam)", async () => {
    const onSelect = vi.fn();
    renderOverlay({ onSelect });
    fireEvent.click(await screen.findByRole("button"));
    expect(onSelect).toHaveBeenCalledWith(0);
  });

  test("the selected sentence's box carries the emphasis ring", async () => {
    renderOverlay({ selectedSeq: 0 });
    const box = await screen.findByRole("button");
    expect(box.className).toContain("ring-2");
  });

  test("a decorative flash layer plays over the selected box; the button never carries it", async () => {
    // The flash is a separate, non-focusable layer so the interactive button is
    // never remounted on selection (which would drop keyboard focus).
    const { container } = renderOverlay({ selectedSeq: 0, selectToken: 3 });
    const box = await screen.findByRole("button");
    expect(container.querySelector(".pdf-highlight-flash")).not.toBeNull();
    expect(box.className).not.toContain("pdf-highlight-flash");
  });

  test("selecting a focused box keeps keyboard focus on it (stable key, no remount)", async () => {
    const getMeasurement = () => measurement();
    const { rerender } = render(
      <PageHighlights
        getMeasurement={getMeasurement}
        sentences={[credible()]}
        layoutVersion={1}
        pageWidth={600}
        selectedSeq={null}
        selectToken={0}
        onSelect={() => {}}
      />,
    );
    const box = await screen.findByRole("button");
    box.focus();
    expect(document.activeElement).toBe(box);

    // The parent marks the sentence selected (as it would on Enter/click).
    rerender(
      <PageHighlights
        getMeasurement={getMeasurement}
        sentences={[credible()]}
        layoutVersion={1}
        pageWidth={600}
        selectedSeq={0}
        selectToken={1}
        onSelect={() => {}}
      />,
    );
    await waitFor(() =>
      expect(screen.getByRole("button").className).toContain("ring-2"),
    );
    // Same DOM node, still focused.
    expect(document.activeElement).toBe(box);
  });

  test("recomputes boxes when the layout version changes", async () => {
    const first = measurement({
      measure: () => [],
    });
    const second = measurement();
    const getMeasurement = vi
      .fn<() => PageMeasurement>()
      .mockReturnValueOnce(first)
      .mockReturnValue(second);

    const { rerender } = render(
      <PageHighlights
        getMeasurement={getMeasurement}
        sentences={[credible()]}
        layoutVersion={1}
        pageWidth={600}
        selectedSeq={null}
        selectToken={0}
        onSelect={() => {}}
      />,
    );
    // First layout measured no rects, so nothing draws.
    await waitFor(() =>
      expect(screen.queryByRole("button")).not.toBeInTheDocument(),
    );

    // A zoom bumps the layout version; the re-measure now yields a box.
    rerender(
      <PageHighlights
        getMeasurement={getMeasurement}
        sentences={[credible()]}
        layoutVersion={2}
        pageWidth={600}
        selectedSeq={null}
        selectToken={0}
        onSelect={() => {}}
      />,
    );
    expect(await screen.findByRole("button")).toBeInTheDocument();
  });
});
