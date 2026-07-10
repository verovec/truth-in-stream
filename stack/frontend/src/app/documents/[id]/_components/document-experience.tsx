"use client";

import type { ComponentType } from "react";
import { useState } from "react";
import dynamic from "next/dynamic";
import Link from "next/link";
import { useDocumentAnalysis } from "@/hooks/use-document-analysis";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import {
  type DocumentAnalysis,
  type DocumentDetail,
  reanalyseDocument,
} from "@/lib/documents/api";
import { formatTemplate } from "@/lib/i18n/text";
import { DocumentProgress } from "./document-progress";
import { FactCheckPanel } from "./fact-check-panel";
import { ReanalyseControl } from "./reanalyse-control";

// The react-pdf viewer touches browser globals during import, so it loads
// client-side only. Tests inject a stub through the pdfViewer prop, so this
// dynamic default is only reached in the browser.
const DefaultPdfViewer = dynamic(() => import("./pdf-viewer"), {
  ssr: false,
  loading: () => <PdfLoading />,
});

// DocumentExperience is the viewer's client experience: it drives the polling
// store, renders the two-pane layout (PDF left, fact-check panel right), the live
// progress bar, the admin reanalyse control, and the failure banner with a retry.
// loadDetail, loadClaims, reanalyse, pollIntervalMs, and pdfViewer are injection
// seams with production defaults so the whole experience tests without a backend
// or the browser-only PDF engine.
export function DocumentExperience({
  documentId,
  role = "guest",
  loadDetail,
  loadClaims,
  reanalyse = reanalyseDocument,
  pollIntervalMs,
  pdfViewer: PdfViewer = DefaultPdfViewer,
}: {
  documentId: string;
  role?: Role;
  loadDetail?: (id: string, signal?: AbortSignal) => Promise<DocumentDetail>;
  loadClaims?: (id: string, signal?: AbortSignal) => Promise<DocumentAnalysis>;
  reanalyse?: (id: string, signal?: AbortSignal) => Promise<void>;
  pollIntervalMs?: number;
  pdfViewer?: ComponentType<{ url: string }>;
}) {
  const { t } = useAppI18n();
  const { snapshot, refresh } = useDocumentAnalysis({
    documentId,
    loadDetail,
    loadClaims,
    pollIntervalMs,
  });
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);

  if (snapshot.status === "loading") {
    return (
      <div
        role="status"
        aria-label={t.viewer.loadingAria}
        className="flex flex-col gap-3"
      >
        <div className="h-5 w-1/3 animate-pulse rounded bg-black/10 dark:bg-white/10" />
        <div className="h-64 w-full animate-pulse rounded-xl bg-black/10 dark:bg-white/10" />
      </div>
    );
  }

  if (snapshot.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {snapshot.message === null
            ? t.viewer.loadErrorFallback
            : formatTemplate(t.viewer.loadError, { message: snapshot.message })}
        </p>
        <button
          type="button"
          onClick={refresh}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {t.viewer.retry}
        </button>
      </div>
    );
  }

  const { document, pdfUrl, sentences } = snapshot;
  // Reanalyse only applies to a document whose upload is ready: the backend
  // returns 409 for a not-ready document just as it does for a concurrent run,
  // so offering the control only when ready keeps the 409 unambiguous.
  const canReanalyse = role === "admin" && document.status === "ready";
  const failed = document.analysisStatus === "failed";

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex min-w-0 flex-col gap-1">
          <Link
            href="/documents"
            className="flex w-fit items-center gap-1 text-xs font-medium text-bleu hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-sky-300 dark:focus-visible:outline-paper/60"
          >
            <BackChevron />
            {t.viewer.back}
          </Link>
          <h1 className="truncate text-lg font-semibold tracking-tight text-ink dark:text-paper">
            {document.title}
          </h1>
        </div>
        {canReanalyse && !failed ? (
          <ReanalyseControl
            document={document}
            reanalyse={reanalyse}
            onStarted={refresh}
          />
        ) : null}
      </div>

      <DocumentProgress document={document} />

      {failed ? (
        <div className="flex flex-col items-start gap-2 rounded-lg border border-rouge/30 bg-rouge/5 p-3 dark:border-rose-400/30 dark:bg-rose-400/10">
          <p role="alert" className="text-sm text-rouge dark:text-rose-300">
            {document.analysisError || t.viewer.progress.failed}
          </p>
          {canReanalyse ? (
            <ReanalyseControl
              document={document}
              reanalyse={reanalyse}
              onStarted={refresh}
              variant="retry"
            />
          ) : null}
        </div>
      ) : null}

      <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="min-w-0">
          {pdfUrl ? (
            <PdfViewer url={pdfUrl} />
          ) : (
            <p className="rounded-xl border border-black/10 p-4 text-sm text-ink/60 dark:border-white/10 dark:text-paper/60">
              {t.viewer.pdf.unavailable}
            </p>
          )}
        </div>
        <section className="flex flex-col gap-2 lg:sticky lg:top-4">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
            {t.viewer.panel.heading}
          </h2>
          <FactCheckPanel
            sentences={sentences}
            selectedSeq={selectedSeq}
            onSelect={setSelectedSeq}
          />
        </section>
      </div>
    </div>
  );
}

function BackChevron() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 16 16"
      className="size-3.5"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M10 12 6 8l4-4" />
    </svg>
  );
}

function PdfLoading() {
  const { t } = useAppI18n();
  return (
    <div className="flex h-64 items-center justify-center rounded-xl border border-black/10 bg-black/5 text-sm text-ink/60 dark:border-white/10 dark:bg-white/5 dark:text-paper/60">
      {t.viewer.pdf.loading}
    </div>
  );
}
