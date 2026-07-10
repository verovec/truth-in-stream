"use client";

import type { DocumentSentence } from "@/lib/documents/api";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { DocumentSentenceRow } from "./document-sentence-row";

// FactCheckPanel is the viewer's right pane: every analysed sentence in document
// order, each a selectable row carrying its verdict cards or a muted reason.
// selectedSeq and onSelect are the shared selection seam - a lifted sentence
// sequence number the highlight card also drives - so panel and PDF stay in sync.
// Each row is memoized (see DocumentSentenceRow) so a selection change re-renders
// only the two rows whose emphasis actually flips.
export function FactCheckPanel({
  sentences,
  selectedSeq,
  onSelect,
}: {
  sentences: DocumentSentence[];
  selectedSeq: number | null;
  onSelect: (seq: number) => void;
}) {
  const { t } = useAppI18n();
  if (sentences.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.viewer.panel.empty}
      </p>
    );
  }
  return (
    <ol
      aria-label={t.viewer.panel.ariaLabel}
      className="flex flex-col gap-2"
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
