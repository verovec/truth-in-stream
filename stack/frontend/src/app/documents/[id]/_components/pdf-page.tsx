"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Page } from "react-pdf";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { buildPageMeasurement } from "@/lib/pdf/text-layer-dom";
import type { AnchoredSentence } from "@/lib/pdf/overlay";
import { PageHighlights, type PageMeasurement } from "./page-highlights";

// PdfPage renders one page of the document with its highlight overlay stacked on
// top of the text layer. It bumps a layout version each time the text layer
// (re)renders - the signal react-pdf fires on first render, zoom, and resize - so
// the overlay re-measures against the current layout. When the reader selects a
// sentence that lives on this page (from the side panel), the page scrolls itself
// into view; the flash itself is driven by the overlay. Measurement is read from
// this page's DOM through buildPageMeasurement, the browser side of the seam the
// overlay is otherwise tested against.
export function PdfPage({
  pageNumber,
  width,
  sentences,
  selectedSeq,
  selectToken,
  onSelect,
}: {
  pageNumber: number;
  width: number | undefined;
  sentences: readonly AnchoredSentence[];
  selectedSeq: number | null;
  selectToken: number;
  onSelect: (seq: number) => void;
}) {
  const { t } = useAppI18n();
  const containerRef = useRef<HTMLDivElement>(null);
  const [layoutVersion, setLayoutVersion] = useState(0);

  // A stable seam identity (containerRef never changes) so the overlay recomputes
  // only when the layout version or the sentences change, not on every render.
  const getMeasurement = useCallback((): PageMeasurement => {
    const element = containerRef.current;
    return element
      ? buildPageMeasurement(element)
      : { items: [], measure: () => [] };
  }, []);

  const hasSelected =
    selectedSeq !== null &&
    sentences.some((sentence) => sentence.seq === selectedSeq);

  useEffect(() => {
    if (!hasSelected) {
      return;
    }
    // The pages flow in the document with no inner scroll region, so bringing a
    // highlight into view is a viewport move by design ("bring the PDF to the
    // highlight") - here scrollIntoView is the right tool, unlike the panel's
    // list, which scrolls its own container. selectToken changes on every
    // selection, so re-selecting the same sentence scrolls (and flashes) again.
    containerRef.current?.scrollIntoView?.({
      block: "center",
      behavior: "smooth",
    });
  }, [hasSelected, selectToken]);

  return (
    <div ref={containerRef} className="relative max-w-full">
      <Page
        pageNumber={pageNumber}
        width={width}
        aria-label={formatTemplate(t.viewer.pdf.page, { page: pageNumber })}
        onRenderTextLayerSuccess={() =>
          setLayoutVersion((version) => version + 1)
        }
        className="max-w-full overflow-hidden rounded-md shadow-sm"
      />
      <PageHighlights
        getMeasurement={getMeasurement}
        sentences={sentences}
        layoutVersion={layoutVersion}
        pageWidth={width ?? 0}
        selectedSeq={selectedSeq}
        selectToken={selectToken}
        onSelect={onSelect}
      />
    </div>
  );
}
