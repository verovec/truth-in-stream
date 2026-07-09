"use client";

import type {
  DocumentAnalysisStatus,
  DocumentStatus,
} from "@/lib/documents/api";
import { useAppI18n } from "@/components/i18n/app-i18n";

// Status tones ride the shared semantic verdict tokens: a ready/complete
// document reads with the same green as a credible verdict, a failure with the
// disputed rouge, and work-in-progress with the amber flag tone.
const UPLOAD_STYLES: Record<DocumentStatus, string> = {
  ready: "text-verdict-credible",
  pending: "text-verdict-flag dark:text-amber-300",
  failed: "text-verdict-disputed",
};

const ANALYSIS_STYLES: Record<DocumentAnalysisStatus, string> = {
  none: "text-ink/60 dark:text-paper/60",
  analysing: "text-verdict-flag dark:text-amber-300",
  complete: "text-verdict-credible",
  failed: "text-verdict-disputed",
};

const badgeBase =
  "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide bg-white/85 dark:bg-night/80";

// DocumentStateBadge shows the one state that matters for a document: while the
// upload is still pending or failed, the upload lifecycle; once the upload is
// ready, the analysis lifecycle (unanalysed / analysing / analysed / failed).
// Collapsing the two orthogonal axes into a single badge keeps the card
// scannable.
export function DocumentStateBadge({
  status,
  analysisStatus,
}: {
  status: DocumentStatus;
  analysisStatus: DocumentAnalysisStatus;
}) {
  const { t } = useAppI18n();
  if (status !== "ready") {
    return (
      <span className={`${badgeBase} ${UPLOAD_STYLES[status]}`}>
        {t.documents.status[status]}
      </span>
    );
  }
  return (
    <span className={`${badgeBase} ${ANALYSIS_STYLES[analysisStatus]}`}>
      {t.documents.analysis[analysisStatus]}
    </span>
  );
}
