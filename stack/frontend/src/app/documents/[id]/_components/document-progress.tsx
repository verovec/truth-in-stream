"use client";

import type { DocumentRecord } from "@/lib/documents/api";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";

// DocumentProgress is the live progress channel shown while a document is
// analysing: a labelled bar plus the sentences_processed / sentences_total
// counter, both driven by the polled persisted state so they advance without a
// refresh. It renders nothing outside the analysing state - a complete or failed
// run is reflected by the panel and the failure banner respectively.
export function DocumentProgress({ document }: { document: DocumentRecord }) {
  const { t } = useAppI18n();
  if (document.analysisStatus !== "analysing") {
    return null;
  }
  const total = document.sentencesTotal;
  const processed = document.sentencesProcessed;
  const percent =
    total > 0 ? Math.min(100, Math.round((processed / total) * 100)) : 0;

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between text-xs">
        <span className="flex items-center gap-1.5 font-medium text-ink/70 dark:text-paper/70">
          <span
            aria-hidden="true"
            className="size-1.5 animate-pulse rounded-full bg-bleu-flag dark:bg-sky-400"
          />
          {t.viewer.progress.analysing}
        </span>
        <span className="font-mono tabular-nums text-ink/50 dark:text-paper/50">
          {formatTemplate(t.viewer.progress.counter, { processed, total })}
        </span>
      </div>
      <div
        role="progressbar"
        aria-valuenow={percent}
        aria-valuemin={0}
        aria-valuemax={100}
        className="h-1.5 overflow-hidden rounded-full bg-black/10 dark:bg-white/10"
      >
        <div
          className="h-full rounded-full bg-bleu transition-[width] dark:bg-sky-400"
          style={{ width: `${percent}%` }}
        />
      </div>
    </div>
  );
}
