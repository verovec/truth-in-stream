"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useDocumentUploads } from "@/hooks/use-document-uploads";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import {
  type LibraryDocument,
  listDocuments,
} from "@/lib/documents/api";
import type { ExtractionResult } from "@/lib/pdf/extract";
import { formatTemplate } from "@/lib/i18n/text";
import type { PutUploader } from "@/lib/video/upload";
import { DocumentCard } from "./document-card";
import { DocumentUploadTile } from "./document-upload-tile";
import { DocumentUploader } from "./document-uploader";

// DEFAULT_ANALYSIS_POLL_MS is how often the library re-checks the backend while a
// document is still being analysed. Analysis is server-driven with no client
// step, so progress and the terminal verdict counts can only be observed by
// polling the persisted state.
const DEFAULT_ANALYSIS_POLL_MS = 2500;

const GRID_CLASS =
  "grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3";

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

// mergeListed refreshes every already-shown document to its freshly-listed state
// in place (status, analysis status, progress counters, verdict counts), leaving
// in-flight upload tiles untouched and adding nothing (new documents arrive via
// the upload callback). It returns the previous array unchanged when nothing
// moved, so an idle poll does not trigger a re-render.
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

// isSettling is true while a document's analysis may still progress: it is
// analysing, or it was just uploaded and its analysis has not yet reached a
// terminal state (the extraction response reports "none" because auto-start
// fires just after, so a fresh upload is watched until it settles).
function isSettling(doc: LibraryDocument, watched: Set<string>): boolean {
  if (doc.analysisStatus === "analysing") {
    return true;
  }
  return (
    watched.has(doc.id) &&
    doc.analysisStatus !== "complete" &&
    doc.analysisStatus !== "failed"
  );
}

// DocumentsExperience owns the documents library: it loads the catalog, lets an
// admin upload PDFs (extracted in the browser), and polls while any document is
// still analysing so status and verdict counts update live. loadDocuments,
// pollDocuments, uploader, and extractor are injection seams for tests.
export function DocumentsExperience({
  role = "guest",
  loadDocuments = listDocuments,
  pollDocuments = listDocuments,
  uploader,
  extractor,
  pollIntervalMs = DEFAULT_ANALYSIS_POLL_MS,
}: {
  role?: Role;
  loadDocuments?: (signal?: AbortSignal) => Promise<LibraryDocument[]>;
  pollDocuments?: (signal?: AbortSignal) => Promise<LibraryDocument[]>;
  uploader?: PutUploader;
  extractor?: (file: File) => Promise<ExtractionResult>;
  pollIntervalMs?: number;
}) {
  const isAdmin = role === "admin";
  const [documents, setDocuments] = useState<LibraryDocument[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);
  // Ids of documents uploaded this session, watched until their analysis settles
  // so the none -> analysing -> complete transition is polled even though the
  // upload response reported "none".
  const [watched, setWatched] = useState<Set<string>>(new Set());

  const loadRef = useRef(loadDocuments);
  useEffect(() => {
    loadRef.current = loadDocuments;
  });
  const pollRef = useRef(pollDocuments);
  useEffect(() => {
    pollRef.current = pollDocuments;
  });

  const { jobs, startUploads, dismiss } = useDocumentUploads({
    uploader,
    extractor,
    onUploaded: (doc) => {
      setDocuments((prev) => [
        { ...doc, credibleClaims: 0, disputedClaims: 0 },
        ...prev,
      ]);
      setWatched((prev) => new Set(prev).add(doc.id));
    },
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

  // Poll while any document is still settling (analysing, or a fresh upload not
  // yet terminal), advancing each in place; stop once none remain. The prune of
  // watched ids happens off the merged list so a settled upload stops being
  // watched.
  const hasSettling = documents.some((doc) => isSettling(doc, watched));
  useEffect(() => {
    if (!hasSettling) {
      return;
    }
    const controller = new AbortController();
    const tick = () => {
      pollRef
        .current(controller.signal)
        .then((listed) => {
          if (controller.signal.aborted) {
            return;
          }
          setDocuments((prev) => mergeListed(prev, listed));
          setWatched((prev) => {
            if (prev.size === 0) {
              return prev;
            }
            const byId = new Map(listed.map((doc) => [doc.id, doc]));
            let changed = false;
            const next = new Set(prev);
            for (const id of prev) {
              const fresh = byId.get(id);
              if (
                fresh &&
                (fresh.analysisStatus === "complete" ||
                  fresh.analysisStatus === "failed")
              ) {
                next.delete(id);
                changed = true;
              }
            }
            return changed ? next : prev;
          });
        })
        .catch(() => {
          // A transient poll failure is ignored; the next tick retries while a
          // document is still settling.
        });
    };
    const handle = setInterval(tick, pollIntervalMs);
    return () => {
      controller.abort();
      clearInterval(handle);
    };
  }, [hasSettling, pollIntervalMs]);

  const { t } = useAppI18n();
  // A succeeded upload becomes a real document card via onUploaded, so only the
  // working and error tiles remain here.
  const activeJobs = jobs.filter((job) => job.state.status !== "ready");

  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-lg font-semibold tracking-tight text-ink dark:text-paper">
        {t.documents.heading}
      </h1>
      {isAdmin ? <DocumentUploader onFiles={startUploads} /> : null}
      <DocumentsList
        listState={listState}
        onRetry={retry}
        documents={documents}
        activeJobs={activeJobs}
        onDismiss={dismiss}
        isAdmin={isAdmin}
      />
    </div>
  );
}

function DocumentsList({
  listState,
  onRetry,
  documents,
  activeJobs,
  onDismiss,
  isAdmin,
}: {
  listState: ListState;
  onRetry: () => void;
  documents: LibraryDocument[];
  activeJobs: ReturnType<typeof useDocumentUploads>["jobs"];
  onDismiss: (id: string) => void;
  isAdmin: boolean;
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
  if (documents.length === 0 && activeJobs.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {isAdmin ? t.documents.emptyAdmin : t.documents.empty}
      </p>
    );
  }
  return (
    <ul className={GRID_CLASS}>
      {activeJobs.map((job) => (
        <li key={job.id}>
          <DocumentUploadTile job={job} onDismiss={onDismiss} />
        </li>
      ))}
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
