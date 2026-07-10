"use client";

// This module is loaded only in the browser via next/dynamic({ ssr: false }):
// pdf.js touches browser globals (DOMMatrix) that do not exist during server
// render, so it must never be imported on the server. Importing the pdfjs config
// module configures the worker (served from public/) on the same pinned build as
// the viewer. The text-layer and annotation-layer stylesheets align the selectable
// text spans with the rendered canvas, which the in-PDF highlight overlay depends on.
import "@/lib/pdf/pdfjs";
import "react-pdf/dist/Page/TextLayer.css";
import "react-pdf/dist/Page/AnnotationLayer.css";

import { useEffect, useMemo, useRef, useState } from "react";
import { Document } from "react-pdf";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { AnchoredSentence } from "@/lib/pdf/overlay";
import type { PageHighlightSentence } from "./highlight-sentences";
import { PdfPage } from "./pdf-page";

// A stable empty array for the no-highlights case (both the whole-document
// default and a page with none), so an unhighlighted page never hands the overlay
// a fresh array identity that would re-run its measure effect on every render.
const NO_SENTENCES: readonly PageHighlightSentence[] = [];

// PdfViewer renders every page of the PDF in a single scrollable column with its
// fact-check highlights overlaid. Pages are sized to the measured container width
// so the document is responsive and the text layer stays aligned with the canvas
// at any width. Selection is bidirectional: clicking a highlight calls onSelect
// (which scrolls the side panel to the sentence's card), and a selection made in
// the panel scrolls the matching page into view and flashes its highlight.
export default function PdfViewer({
  url,
  sentences = NO_SENTENCES,
  selectedSeq = null,
  selectToken = 0,
  onSelect = () => {},
}: {
  url: string;
  sentences?: readonly PageHighlightSentence[];
  selectedSeq?: number | null;
  selectToken?: number;
  onSelect?: (seq: number) => void;
}) {
  const { t } = useAppI18n();
  const containerRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState<number | undefined>(undefined);
  const [numPages, setNumPages] = useState(0);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) {
      return;
    }
    const measure = () => setWidth(element.clientWidth);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const sentencesByPage = useMemo(() => {
    const map = new Map<number, AnchoredSentence[]>();
    for (const sentence of sentences) {
      const list = map.get(sentence.page);
      if (list) {
        list.push(sentence);
      } else {
        map.set(sentence.page, [sentence]);
      }
    }
    return map;
  }, [sentences]);

  return (
    <div
      ref={containerRef}
      className="flex flex-col items-center gap-4 rounded-xl border border-black/10 bg-black/5 p-3 dark:border-white/10 dark:bg-white/5"
    >
      <Document
        file={url}
        aria-label={t.viewer.pdf.documentAria}
        onLoadSuccess={(pdf) => setNumPages(pdf.numPages)}
        loading={
          <p className="py-8 text-sm text-ink/60 dark:text-paper/60">
            {t.viewer.pdf.loading}
          </p>
        }
        error={
          <p className="py-8 text-sm text-rouge dark:text-rose-300">
            {t.viewer.pdf.error}
          </p>
        }
        className="flex w-full flex-col items-center gap-4"
      >
        {Array.from({ length: numPages }, (_value, index) => {
          const pageNumber = index + 1;
          return (
            <PdfPage
              key={pageNumber}
              pageNumber={pageNumber}
              width={width}
              sentences={sentencesByPage.get(pageNumber) ?? NO_SENTENCES}
              selectedSeq={selectedSeq}
              selectToken={selectToken}
              onSelect={onSelect}
            />
          );
        })}
      </Document>
    </div>
  );
}
