"use client";

import { useEffect, useState } from "react";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";
import {
  resolveHighlightBoxes,
  type AnchoredSentence,
  type HighlightBox,
  type HighlightVerdict,
  type MeasureRange,
  type Rect,
} from "@/lib/pdf/overlay";

// PageMeasurement is the seam between the overlay and the rendered text layer:
// the page's text-item strings in document order and a function that measures an
// item range into page-relative rects. Production reads both from the DOM;
// tests inject a fake text layer, so every interaction is covered without pdf.js.
export type PageMeasurement = { items: string[]; measure: MeasureRange };

// Translucent verdict fills over the text: emerald for credible, rose for
// disputed, both from the shared verdict tokens so they flip for contrast in dark
// mode and keep the underlying text readable. The badge mirrors the panel's
// verdict chips so a highlight and its card read as the same verdict.
const HIGHLIGHT_STYLES: Record<
  HighlightVerdict,
  { fill: string; selected: string; badge: string }
> = {
  credible: {
    fill: "bg-verdict-credible/20 hover:bg-verdict-credible/35",
    selected: "ring-2 ring-verdict-credible/70",
    badge:
      "bg-verdict-credible/10 text-verdict-credible dark:bg-verdict-credible/15",
  },
  disputed: {
    fill: "bg-verdict-disputed/20 hover:bg-verdict-disputed/35",
    selected: "ring-2 ring-verdict-disputed/70",
    badge:
      "bg-verdict-disputed/10 text-verdict-disputed dark:bg-verdict-disputed/15",
  },
};

const TOOLTIP_WIDTH = 260;
// A conservative upper bound on the tooltip's rendered height (verdict badge over
// a three-line-clamped snippet plus padding). The tooltip is placed above its
// highlight only when there is at least this much room, otherwise below, so it
// never clips off the top of the page.
const TOOLTIP_MAX_HEIGHT = 104;

// PageHighlights is the interactive overlay for one rendered page: it resolves the
// page's credible/disputed sentences to boxes and draws them above the text layer,
// recomputing whenever the page renders, zooms, or resizes (layoutVersion bumps).
// Hovering or focusing a box shows a compact verdict tooltip; clicking it selects
// the sentence (the shared seam that scrolls the side panel to its card). The
// selected box carries a persistent emphasis ring and a one-shot flash that
// replays on each new selection - it is re-keyed on selectToken, so re-selecting
// the same sentence from the panel restarts the animation without any timer.
// getMeasurement is the injection seam that keeps the whole component testable
// against a fake text layer.
export function PageHighlights({
  getMeasurement,
  sentences,
  layoutVersion,
  pageWidth,
  selectedSeq,
  selectToken,
  onSelect,
}: {
  getMeasurement: () => PageMeasurement;
  sentences: readonly AnchoredSentence[];
  layoutVersion: number;
  pageWidth: number;
  selectedSeq: number | null;
  selectToken: number;
  onSelect: (seq: number) => void;
}) {
  const { t } = useAppI18n();
  const [boxes, setBoxes] = useState<HighlightBox[]>([]);
  const [hoveredSeq, setHoveredSeq] = useState<number | null>(null);

  useEffect(() => {
    // Measuring the rendered text layer through the seam is a read of an external
    // system (the DOM), so the boxes are re-derived whenever the layout version
    // bumps (first render, zoom, resize) or the sentences change.
    const drawHighlights = () => {
      const { items, measure } = getMeasurement();
      setBoxes(resolveHighlightBoxes({ items, sentences, measure }));
    };
    drawHighlights();
  }, [getMeasurement, sentences, layoutVersion]);

  if (boxes.length === 0) {
    return null;
  }

  const hovered = boxes.find((box) => box.seq === hoveredSeq) ?? null;
  const selectedBox = boxes.find((box) => box.seq === selectedSeq) ?? null;

  return (
    <div
      aria-label={t.viewer.highlight.layerAria}
      // z-10 lifts the overlay above react-pdf's text layer (z-index 2) so the
      // boxes paint over the text and receive hover and click; the layer itself
      // stays pointer-events-none so text selection passes through between boxes.
      className="pointer-events-none absolute inset-0 z-10"
      onMouseLeave={() => setHoveredSeq(null)}
    >
      {boxes.map((box) => {
        const styles = HIGHLIGHT_STYLES[box.verdict];
        const selected = box.seq === selectedSeq;
        return box.rects.map((rect, rectIndex) => (
          // Keys are stable (no selectToken) so a box is never remounted on
          // selection; remounting the focused button would drop keyboard focus.
          <button
            key={`${box.seq}:${rectIndex}`}
            type="button"
            aria-label={
              rectIndex === 0
                ? formatTemplate(t.viewer.highlight.aria, {
                    verdict: t.claims.verdicts[box.verdict],
                    snippet: box.snippet,
                  })
                : undefined
            }
            aria-hidden={rectIndex === 0 ? undefined : true}
            tabIndex={rectIndex === 0 ? 0 : -1}
            onMouseEnter={() => setHoveredSeq(box.seq)}
            onFocus={() => setHoveredSeq(box.seq)}
            onBlur={() => setHoveredSeq(null)}
            onClick={() => onSelect(box.seq)}
            style={{
              left: rect.left,
              top: rect.top,
              width: rect.width,
              height: rect.height,
            }}
            className={`pointer-events-auto absolute cursor-pointer rounded-[2px] transition-colors focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60 ${styles.fill} ${selected ? styles.selected : ""}`}
          />
        ));
      })}
      {/* The one-shot flash is a decorative, non-focusable layer over the selected
          box, re-keyed on selectToken so the animation replays on each new (or
          repeated) selection without remounting the interactive button. */}
      {selectedBox
        ? selectedBox.rects.map((rect, rectIndex) => (
            <span
              key={`flash:${selectedBox.seq}:${rectIndex}:${selectToken}`}
              aria-hidden="true"
              style={{
                left: rect.left,
                top: rect.top,
                width: rect.width,
                height: rect.height,
              }}
              className={`pdf-highlight-flash pointer-events-none absolute rounded-[2px] ${HIGHLIGHT_STYLES[selectedBox.verdict].fill}`}
            />
          ))
        : null}
      {hovered ? (
        <HighlightTooltip box={hovered} pageWidth={pageWidth} />
      ) : null}
    </div>
  );
}

// HighlightTooltip is the compact card shown while a highlight is hovered or
// focused: the verdict badge and the claim snippet. It anchors to the first line
// of the highlight, sits above it unless there is no room (then below), and clamps
// horizontally so it stays inside the page column rather than off-screen.
function HighlightTooltip({
  box,
  pageWidth,
}: {
  box: HighlightBox;
  pageWidth: number;
}) {
  const { t } = useAppI18n();
  const anchor: Rect = box.rects[0];
  const placeAbove = anchor.top > TOOLTIP_MAX_HEIGHT;
  const left = Math.max(
    4,
    Math.min(anchor.left, Math.max(4, pageWidth - TOOLTIP_WIDTH)),
  );
  const styles = HIGHLIGHT_STYLES[box.verdict];
  return (
    <div
      role="tooltip"
      style={{
        left,
        top: placeAbove ? anchor.top - 6 : anchor.top + anchor.height + 6,
        maxWidth: TOOLTIP_WIDTH,
        transform: placeAbove ? "translateY(-100%)" : undefined,
      }}
      className="pointer-events-none absolute z-10 rounded-md border border-black/10 bg-white p-2 shadow-md dark:border-white/15 dark:bg-night"
    >
      <span
        className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${styles.badge}`}
      >
        {t.claims.verdicts[box.verdict]}
      </span>
      <p className="mt-1 line-clamp-3 text-xs leading-5 text-ink/80 dark:text-paper/80">
        {box.snippet}
      </p>
    </div>
  );
}
