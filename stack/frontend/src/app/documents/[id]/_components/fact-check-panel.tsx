"use client";

import { useEffect, useRef } from "react";
import type { DocumentSentence } from "@/lib/documents/api";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { DocumentSentenceRow } from "./document-sentence-row";

// FactCheckPanel is the viewer's right pane: every analysed sentence in document
// order, each a selectable row carrying its verdict cards or a muted reason.
// selectedSeq/selectToken/onSelect are the shared selection seam - a lifted
// sentence sequence number the PDF highlights also drive - so panel and PDF stay
// in sync. When the selection changes (including a click on a highlight), the
// panel scrolls the matching row into view; selectToken bumps on every select so
// re-selecting the same sentence still re-scrolls. Each row is memoized (see
// DocumentSentenceRow) so a selection change re-renders only the two rows whose
// emphasis actually flips.
export function FactCheckPanel({
  sentences,
  selectedSeq,
  selectToken,
  onSelect,
}: {
  sentences: DocumentSentence[];
  selectedSeq: number | null;
  selectToken: number;
  onSelect: (seq: number) => void;
}) {
  const { t } = useAppI18n();
  const listRef = useRef<HTMLOListElement>(null);

  useEffect(() => {
    if (selectedSeq === null) {
      return;
    }
    const list = listRef.current;
    const row = list?.querySelector(`[data-seq="${selectedSeq}"]`);
    if (!list || !(row instanceof HTMLElement)) {
      return;
    }
    // Scroll only this list's own scrollTop, never the page: native
    // scrollIntoView walks every scrollable ancestor and would yank the whole
    // viewer (the same reason live-statement-list scrolls its container). Move by
    // the minimum to bring the row back to a visible edge; a visible row stays put.
    const rowRect = row.getBoundingClientRect();
    const listRect = list.getBoundingClientRect();
    let delta = 0;
    if (rowRect.top < listRect.top) {
      delta = rowRect.top - listRect.top;
    } else if (rowRect.bottom > listRect.bottom) {
      delta = rowRect.bottom - listRect.bottom;
    }
    if (delta !== 0) {
      list.scrollTo({ top: list.scrollTop + delta, behavior: "smooth" });
    }
  }, [selectedSeq, selectToken]);

  if (sentences.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.viewer.panel.empty}
      </p>
    );
  }
  return (
    <ol
      ref={listRef}
      aria-label={t.viewer.panel.ariaLabel}
      // The list is its own scroll region (bounded to the viewport) so revealing a
      // selected card scrolls the list, not the page - keeping the PDF put while
      // the panel jumps to the card the reader clicked.
      className="flex max-h-[calc(100vh-8rem)] flex-col gap-2 overflow-y-auto pr-1"
    >
      {sentences.map((sentence) => (
        <DocumentSentenceRow
          key={sentence.seq}
          sentence={sentence}
          selected={sentence.seq === selectedSeq}
          onSelect={onSelect}
        />
      ))}
    </ol>
  );
}
