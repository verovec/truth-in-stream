"use client";

import { useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { exportRecapPdf } from "@/lib/recap-pdf/export";
import type { RecapDocumentProps } from "@/lib/recap-pdf/recap-document";
import type { Recap, RecapSpeaker } from "@/lib/video/recap";

// PdfAvailability says whether there is a report to export. When there is
// none, the reason is one the operator can read: the recap is still loading,
// the analysis is not complete, or the recap could not be loaded.
export type PdfAvailability =
  | { status: "ready"; recap: Recap; sheets: RecapSpeaker[] }
  | { status: "unavailable"; reason: "loading" | "notReady" | "failed" };

const REASON_KEY = {
  loading: "unavailableLoading",
  notReady: "unavailableNotReady",
  failed: "unavailable",
} as const;

// ExportPdfButton exports the recap on screen as a PDF file in one press: the
// cover, the recap, then the sheets in the order the page shows them. While
// there is nothing to export it stays visible but disabled, with the reason
// beside it and tied to it for assistive technology. exportPdf is an injection
// seam for tests; production generates the file in the browser.
export function ExportPdfButton({
  availability,
  brandName,
  exportPdf = exportRecapPdf,
}: {
  availability: PdfAvailability;
  brandName: string;
  exportPdf?: (props: RecapDocumentProps) => Promise<void>;
}) {
  const { locale, t } = useAppI18n();
  const copy = t.recap.pdf;
  const [state, setState] = useState<"idle" | "exporting" | "failed">("idle");

  const ready = availability.status === "ready";
  const run = async () => {
    if (availability.status !== "ready") {
      return;
    }
    setState("exporting");
    try {
      await exportPdf({
        recap: availability.recap,
        sheets: availability.sheets,
        t,
        locale,
        brandName,
      });
      setState("idle");
    } catch (err) {
      // The operator only needs to know it failed; the cause (a missing font
      // file, a layout exception, a chunk that did not load) is for whoever
      // investigates.
      console.error("recap PDF export failed", err);
      setState("failed");
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-3 print:hidden">
      <button
        type="button"
        onClick={run}
        disabled={!ready || state === "exporting"}
        aria-describedby={ready ? undefined : "recap-pdf-reason"}
        className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
      >
        {state === "exporting" ? copy.exporting : copy.export}
      </button>
      {availability.status === "unavailable" ? (
        <p id="recap-pdf-reason" className="text-xs text-ink/60 dark:text-paper/60">
          {copy[REASON_KEY[availability.reason]]}
        </p>
      ) : null}
      {state === "failed" ? (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {copy.failed}
        </p>
      ) : null}
    </div>
  );
}
