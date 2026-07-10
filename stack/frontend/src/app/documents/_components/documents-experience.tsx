"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { type LibraryDocument, listDocuments } from "@/lib/documents/api";
import { formatTemplate } from "@/lib/i18n/text";
import { DocumentCard } from "./document-card";

// DEFAULT_ANALYSIS_POLL_MS is how often the library re-checks the backend while a
// document is still being analysed. Analysis is server-driven with no client
// step, so progress and the terminal verdict counts can only be observed by
// polling the persisted state.
const DEFAULT_ANALYSIS_POLL_MS = 2500;

// DEFAULT_MAX_IDLE_POLLS bounds polling by consecutive ticks with no observable
// progress, not by a flat attempt count. A document still advancing changes the
// progress signature and resets the idle counter, so active analysis keeps the
// poll alive; only a genuinely stuck document (analyzer crash) runs the counter
// to the bound, after which polling stops and a refresh restarts it. At the
// default interval this is ~2 minutes of total stall - far longer than any gap in
// a real analysis. Bounding on stall rather than a session attempt count means an
// unrelated document completing cannot silently refresh a stuck document's
// budget.
const DEFAULT_MAX_IDLE_POLLS = 48;

// progressSignature captures the settling-relevant state of a listed catalog:
// each document's id, analysis status, and processed-sentence count. It changes
// whenever any analysis advances or completes, and stays constant only while
// nothing moves - which is exactly when polling should stop. listDocuments
// returns a deterministic order, so the joined string is stable across ticks.
function progressSignature(docs: LibraryDocument[]): string {
  return docs
    .map((doc) => `${doc.id}:${doc.analysisStatus}:${doc.sentencesProcessed}`)
    .join("|");
}

const GRID_CLASS = "grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3";

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

// mergeListed refreshes every already-shown document to its freshly-listed state
// in place (status, analysis status, progress counters, verdict counts). It adds
// nothing (uploads happen in the backoffice, not here) and returns the previous
// array unchanged when nothing moved, so an idle poll does not trigger a
// re-render.
function mergeListed(
  prev: LibraryDocument[],
  listed: LibraryDocument[],
): LibraryDocument[] {
  const byId = new Map(listed.map((doc) => [doc.id, doc]));
  let changed = false;
  const next = prev.map((doc) => {
    const fresh = byId.get(doc.id);
    if (
      fresh &&
      (fresh.status !== doc.status ||
        fresh.analysisStatus !== doc.analysisStatus ||
        fresh.sentencesProcessed !== doc.sentencesProcessed ||
        fresh.credibleClaims !== doc.credibleClaims ||
        fresh.disputedClaims !== doc.disputedClaims)
    ) {
      changed = true;
      return fresh;
    }
    return doc;
  });
  return changed ? next : prev;
}

// DocumentsExperience is the documents reading surface for every authenticated
// user: it loads the catalog and polls while any document is still analysing so
// status and verdict counts update live. Ingestion (upload) lives in the
// backoffice; the viewer owns reanalyse and delete. loadDocuments and
// pollDocuments are injection seams for tests.
export function DocumentsExperience({
  loadDocuments = listDocuments,
  pollDocuments = listDocuments,
  pollIntervalMs = DEFAULT_ANALYSIS_POLL_MS,
  maxIdlePolls = DEFAULT_MAX_IDLE_POLLS,
}: {
  loadDocuments?: (signal?: AbortSignal) => Promise<LibraryDocument[]>;
  pollDocuments?: (signal?: AbortSignal) => Promise<LibraryDocument[]>;
  pollIntervalMs?: number;
  maxIdlePolls?: number;
} = {}) {
  const [documents, setDocuments] = useState<LibraryDocument[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);

  const loadRef = useRef(loadDocuments);
  useEffect(() => {
    loadRef.current = loadDocuments;
  });
  const pollRef = useRef(pollDocuments);
  useEffect(() => {
    pollRef.current = pollDocuments;
  });

  // The catalog loads on the client (like the rest of this app's data, riding
  // the same-origin proxy) and reloads when reloadToken changes; the fetch is
  // aborted on unmount/reload so a stale response cannot land.
  useEffect(() => {
    const controller = new AbortController();
    loadRef
      .current(controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) {
          return;
        }
        setDocuments(loaded);
        setListState({ status: "loaded" });
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        setListState({
          status: "error",
          message: err instanceof Error ? err.message : null,
        });
      });
    return () => controller.abort();
  }, [reloadToken]);

  const retry = useCallback(() => {
    setListState({ status: "loading" });
    setReloadToken((token) => token + 1);
  }, []);

  // Poll while any document is still analysing, advancing each in place; stop
  // once none remain. A settling document that never reaches a terminal state
  // would otherwise poll forever, so polling is bound by consecutive no-progress
  // ticks: each tick that observes a change resets the counter, so active
  // analysis keeps polling alive, while a genuinely stuck document runs the
  // counter to maxIdlePolls and stops the background churn.
  const hasAnalysing = documents.some(
    (doc) => doc.analysisStatus === "analysing",
  );
  useEffect(() => {
    if (!hasAnalysing) {
      return;
    }
    const controller = new AbortController();
    let idleTicks = 0;
    let lastSignature = "";
    const halt = () => {
      controller.abort();
      clearInterval(handle);
    };
    const noteIdle = () => {
      idleTicks += 1;
      if (idleTicks >= maxIdlePolls) {
        halt();
      }
    };
    const tick = () => {
      pollRef
        .current(controller.signal)
        .then((listed) => {
          if (controller.signal.aborted) {
            return;
          }
          setDocuments((prev) => mergeListed(prev, listed));
          const signature = progressSignature(listed);
          if (signature === lastSignature) {
            noteIdle();
          } else {
            lastSignature = signature;
            idleTicks = 0;
          }
        })
        .catch(() => {
          // A failing poll makes no progress; count it toward the idle bound so
          // a permanently erroring poll also terminates instead of retrying
          // forever.
          noteIdle();
        });
    };
    const handle = setInterval(tick, pollIntervalMs);
    return () => {
      controller.abort();
      clearInterval(handle);
    };
  }, [hasAnalysing, pollIntervalMs, maxIdlePolls]);

  const { t } = useAppI18n();

  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-lg font-semibold tracking-tight text-ink dark:text-paper">
        {t.documents.heading}
      </h1>
      <DocumentsCatalog
        listState={listState}
        onRetry={retry}
        documents={documents}
      />
    </div>
  );
}

function DocumentsCatalog({
  listState,
  onRetry,
  documents,
}: {
  listState: ListState;
  onRetry: () => void;
  documents: LibraryDocument[];
}) {
  const { t } = useAppI18n();
  if (listState.status === "loading") {
    return <DocumentsSkeleton />;
  }
  if (listState.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {listState.message === null
            ? t.documents.loadErrorFallback
            : formatTemplate(t.documents.loadError, {
                message: listState.message,
              })}
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {t.documents.retry}
        </button>
      </div>
    );
  }
  if (documents.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.documents.empty}
      </p>
    );
  }
  return (
    <ul className={GRID_CLASS}>
      {documents.map((doc) => (
        <li key={doc.id}>
          <DocumentCard doc={doc} />
        </li>
      ))}
    </ul>
  );
}

const SKELETON_TILES = 6;

function DocumentsSkeleton() {
  const { t } = useAppI18n();
  return (
    <ul role="status" aria-label={t.documents.loadingAria} className={GRID_CLASS}>
      {Array.from({ length: SKELETON_TILES }, (_v, index) => (
        <li
          key={index}
          aria-hidden
          className="rounded-xl border border-black/10 p-4 dark:border-white/10"
        >
          <div className="h-4 w-3/4 animate-pulse rounded bg-black/10 dark:bg-white/10" />
          <div className="mt-2 h-3 w-1/3 animate-pulse rounded bg-black/10 dark:bg-white/10" />
        </li>
      ))}
    </ul>
  );
}
