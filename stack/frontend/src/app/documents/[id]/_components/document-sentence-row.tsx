"use client";

import { memo } from "react";
import type { DocumentSentence } from "@/lib/documents/api";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { LiveClaimList } from "@/app/app/_components/live-claim-list";
import {
  LIVE_ROW_BASE_CLASS,
  LIVE_ROW_EMPHASIZED_CLASS,
} from "@/app/app/_components/live-row-classes";
import {
  classifyDocumentSentence,
  type DocumentSentenceView,
} from "./sentence-view";

// DocumentSentenceRow is one sentence in the fact-check panel: a selectable
// header (page marker plus the sentence text, muted unless the sentence reached
// a credible/disputed verdict) over its verdict body. Selecting it lifts the
// sentence sequence number up - the seam the highlight card shares to scroll the
// PDF to the same sentence. The verdict body reuses the live claim list and its
// VerifiedClaim cards verbatim, so document and live verdicts render identically.
export const DocumentSentenceRow = memo(function DocumentSentenceRow({
  sentence,
  selected,
  onSelect,
}: {
  sentence: DocumentSentence;
  selected: boolean;
  onSelect: (seq: number) => void;
}) {
  const { t } = useAppI18n();
  const view = classifyDocumentSentence(sentence);
  const muted = view.kind !== "claims" || !view.substantive;

  return (
    <li
      data-seq={sentence.seq}
      aria-current={selected ? "true" : undefined}
      className={`rounded-lg border transition-colors ${
        selected ? LIVE_ROW_EMPHASIZED_CLASS : LIVE_ROW_BASE_CLASS
      }`}
    >
      <button
        type="button"
        onClick={() => onSelect(sentence.seq)}
        className="flex w-full items-baseline gap-2 rounded-t-lg px-3 py-1.5 text-left hover:bg-ink/5 focus-visible:outline-2 focus-visible:outline-bleu-flag dark:hover:bg-white/5 dark:focus-visible:outline-paper/60"
      >
        <span className="font-mono text-[11px] tabular-nums text-ink/50 dark:text-paper/50">
          {formatTemplate(t.viewer.panel.page, { page: sentence.page })}
        </span>
        <span
          className={`min-w-0 flex-1 text-xs leading-5 ${
            muted
              ? "text-ink/50 dark:text-paper/50"
              : "text-ink/80 dark:text-paper/80"
          }`}
        >
          {sentence.text}
        </span>
      </button>
      <div className="border-t border-black/10 px-3 py-2 dark:border-white/10">
        <SentenceBody sentence={sentence} view={view} />
      </div>
    </li>
  );
});

function SentenceBody({
  sentence,
  view,
}: {
  sentence: DocumentSentence;
  view: DocumentSentenceView;
}) {
  const { t } = useAppI18n();
  switch (view.kind) {
    case "claims":
      return <LiveClaimList claims={sentence.claims} />;
    case "skipped":
      return <MutedLabel text={t.viewer.panel.skipReasons[view.reason]} />;
    case "pending":
      return <MutedLabel text={t.viewer.panel.pending} />;
  }
}

function MutedLabel({ text }: { text: string }) {
  return (
    <p className="text-[11px] leading-5 text-ink/50 dark:text-paper/50">
      {text}
    </p>
  );
}
