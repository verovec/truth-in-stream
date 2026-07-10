"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  type DocumentAnalysis,
  type DocumentAnalysisStatus,
  type DocumentDetail,
  type DocumentRecord,
  type DocumentSentence,
  getDocument,
  getDocumentClaims,
} from "@/lib/documents/api";
import { ApiError } from "@/lib/http";

// DEFAULT_POLL_MS is the interval the viewer re-checks a document that is still
// analysing. Analysis is server-driven and persisted, so progress and new
// verdicts are observed by polling the stored truth, which makes the view
// refresh-safe with no realtime infrastructure.
const DEFAULT_POLL_MS = 2000;

// MAX_BACKOFF_STEPS caps the exponential backoff on transient poll failures so a
// flaky backend widens the interval up to 2^4 = 16x the base rather than
// hammering it every 2s, while never giving up while the document is analysing.
const MAX_BACKOFF_STEPS = 4;

// DocumentAnalysisSnapshot is the one value the viewer reads. The discriminated
// union makes the loading and error states unrepresentable alongside the data,
// so the page renders each state exhaustively rather than guarding sibling
// booleans. pdfUrl is stable across polls (fetched once) so react-pdf never
// reloads the PDF as verdicts stream in.
export type DocumentAnalysisSnapshot =
  | { status: "loading" }
  | { status: "error"; message: string | null }
  | {
      status: "ready";
      document: DocumentRecord;
      pdfUrl: string | null;
      sentences: DocumentSentence[];
    };

export type DocumentAnalysisState = {
  snapshot: DocumentAnalysisSnapshot;
  // refresh re-fetches the claims immediately (used after a reanalyse fires) so
  // the transition to analysing is observed at once and polling re-arms without
  // waiting a full interval; the PDF is not refetched.
  refresh: () => void;
};

function backoffDelay(baseMs: number, failures: number): number {
  return baseMs * 2 ** Math.min(failures, MAX_BACKOFF_STEPS);
}

// useDocumentAnalysis is the viewer's polling store: it loads the PDF URL and
// the sentence-level analysis once, then polls the persisted analysis every
// pollIntervalMs while the document is analysing, stopping on any terminal
// state and backing off on transient errors. loadDetail and loadClaims are
// injection seams so the page and its tests drive it without a live backend.
export function useDocumentAnalysis({
  documentId,
  loadDetail = getDocument,
  loadClaims = getDocumentClaims,
  pollIntervalMs = DEFAULT_POLL_MS,
}: {
  documentId: string;
  loadDetail?: (id: string, signal?: AbortSignal) => Promise<DocumentDetail>;
  loadClaims?: (id: string, signal?: AbortSignal) => Promise<DocumentAnalysis>;
  pollIntervalMs?: number;
}): DocumentAnalysisState {
  const [snapshot, setSnapshot] = useState<DocumentAnalysisSnapshot>({
    status: "loading",
  });
  const [refreshNonce, setRefreshNonce] = useState(0);

  // Reset to the skeleton only when a different document is requested - adjusting
  // state during render, the supported pattern for deriving state from a changed
  // prop, rather than a setState in the effect. A refresh of the same document
  // keeps the current results on screen while the claims re-fetch is in flight.
  const [trackedId, setTrackedId] = useState(documentId);
  if (trackedId !== documentId) {
    setTrackedId(documentId);
    setSnapshot({ status: "loading" });
  }

  // Kept in refs so a changing seam identity does not restart the poll loop; the
  // loop reads the latest through the ref.
  const loadDetailRef = useRef(loadDetail);
  const loadClaimsRef = useRef(loadClaims);
  useEffect(() => {
    loadDetailRef.current = loadDetail;
    loadClaimsRef.current = loadClaims;
  });

  // The presigned PDF URL is fetched once and reused across polls and refreshes
  // so the viewer never reloads the PDF; it resets only when the document id
  // changes (a different document is being viewed).
  const pdfUrlRef = useRef<string | null>(null);
  const lastIdRef = useRef<string | null>(null);

  useEffect(() => {
    if (lastIdRef.current !== documentId) {
      lastIdRef.current = documentId;
      pdfUrlRef.current = null;
    }

    let cancelled = false;
    const controller = new AbortController();
    let handle: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;

    const scheduleIfAnalysing = (status: DocumentAnalysisStatus) => {
      if (status === "analysing") {
        handle = setTimeout(() => void tick(), backoffDelay(pollIntervalMs, failures));
      }
    };

    const tick = async () => {
      try {
        const analysis = await loadClaimsRef.current(documentId, controller.signal);
        if (cancelled) {
          return;
        }
        failures = 0;
        setSnapshot((prev) =>
          prev.status === "ready"
            ? {
                ...prev,
                document: analysis.document,
                sentences: analysis.sentences,
              }
            : prev,
        );
        scheduleIfAnalysing(analysis.document.analysisStatus);
      } catch (err) {
        if (cancelled || controller.signal.aborted) {
          return;
        }
        if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
          // A client error (the document was deleted, or access was revoked)
          // will not self-heal by retrying the same request: stop polling and
          // surface it rather than looping forever.
          setSnapshot({ status: "error", message: err.message });
          return;
        }
        // A transient failure never abandons an analysing document: back off and
        // retry so the progress channel self-heals when the backend recovers.
        failures += 1;
        handle = setTimeout(() => void tick(), backoffDelay(pollIntervalMs, failures));
      }
    };

    const loadInitial = async () => {
      try {
        const needDetail = pdfUrlRef.current === null;
        const [detail, analysis] = await Promise.all([
          needDetail
            ? loadDetailRef.current(documentId, controller.signal)
            : Promise.resolve(null),
          loadClaimsRef.current(documentId, controller.signal),
        ]);
        if (cancelled) {
          return;
        }
        if (detail) {
          pdfUrlRef.current = detail.pdfUrl;
        }
        setSnapshot({
          status: "ready",
          document: analysis.document,
          pdfUrl: pdfUrlRef.current,
          sentences: analysis.sentences,
        });
        scheduleIfAnalysing(analysis.document.analysisStatus);
      } catch (err) {
        if (cancelled || controller.signal.aborted) {
          return;
        }
        setSnapshot({
          status: "error",
          message: err instanceof Error ? err.message : null,
        });
      }
    };

    void loadInitial();

    return () => {
      cancelled = true;
      controller.abort();
      if (handle) {
        clearTimeout(handle);
      }
    };
  }, [documentId, refreshNonce, pollIntervalMs]);

  const refresh = useCallback(() => setRefreshNonce((n) => n + 1), []);

  return { snapshot, refresh };
}
