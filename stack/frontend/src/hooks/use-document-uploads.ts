"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  DOCUMENT_CONTENT_TYPE,
  type DocumentRecord,
  ingestExtraction,
  isAcceptedDocumentFile,
  requestDocumentUpload,
} from "@/lib/documents/api";
import { extractDocument, ScannedPdfError } from "@/lib/pdf/extract";
import type { ExtractionResult } from "@/lib/pdf/extract";
import { putWithProgress, type PutUploader } from "@/lib/video/upload";
import { deriveTitle, failureMessage } from "@/lib/upload/filename";

// DocumentUploadError is why a job failed, kept as data so the tile localizes
// it. unsupported (not a PDF) and scanned (no extractable text) are rejected
// before any server call; tooLong exceeds the extraction cap (surfaced on the
// ticket) so the upload PUT is skipped; failed carries a transport/backend
// message when one exists.
export type DocumentUploadError =
  | { kind: "unsupported" }
  | { kind: "scanned" }
  | { kind: "tooLong"; max: number }
  | { kind: "failed"; message: string | null };

// DocumentUploadJobState is the extract-first lifecycle of one file. A
// discriminated union, so a job can never be both uploading and failed.
export type DocumentUploadJobState =
  | { status: "extracting" }
  | { status: "requesting" }
  | { status: "uploading"; progress: number }
  | { status: "confirming" }
  | { status: "ready"; document: DocumentRecord }
  | { status: "error"; error: DocumentUploadError };

export type DocumentUploadJob = {
  id: string;
  title: string;
  fileName: string;
  state: DocumentUploadJobState;
};

type UseDocumentUploadsOptions = {
  // uploader is the PUT transport; injected so tests avoid XMLHttpRequest.
  uploader?: PutUploader;
  // extractor runs the browser pdf.js text extraction; injected so tests avoid
  // loading pdf.js. The signal cancels a long parse when the job is dismissed or
  // the hook unmounts.
  extractor?: (file: File, signal?: AbortSignal) => Promise<ExtractionResult>;
  // onUploaded fires once a document's extraction is confirmed ready.
  onUploaded?: (document: DocumentRecord) => void;
};

// defaultExtractor adapts extractDocument (whose second parameter is the page
// reader seam) to the hook's (file, signal) shape so the abort signal reaches
// pdf.js while the real reader stays the default.
const defaultExtractor = (file: File, signal?: AbortSignal) =>
  extractDocument(file, undefined, signal);

export function useDocumentUploads({
  uploader = putWithProgress,
  extractor = defaultExtractor,
  onUploaded,
}: UseDocumentUploadsOptions) {
  const [jobs, setJobs] = useState<DocumentUploadJob[]>([]);
  // Hold the latest callbacks in a ref so the async pipeline never closes over
  // stale props between renders.
  const callbacks = useRef({ uploader, extractor, onUploaded });
  useEffect(() => {
    callbacks.current = { uploader, extractor, onUploaded };
  });

  // One AbortController per in-flight job, so dismiss and unmount cancel the
  // extract/request/upload/confirm chain instead of writing state after the job
  // is gone.
  const controllers = useRef(new Map<string, AbortController>());
  useEffect(() => {
    const inFlight = controllers.current;
    return () => {
      for (const controller of inFlight.values()) {
        controller.abort();
      }
    };
  }, []);

  const setState = useCallback((id: string, state: DocumentUploadJobState) => {
    setJobs((prev) =>
      prev.map((job) => (job.id === id ? { ...job, state } : job)),
    );
  }, []);

  const run = useCallback(
    async (id: string, title: string, file: File) => {
      const controller = new AbortController();
      controllers.current.set(id, controller);
      let confirmed: DocumentRecord | null = null;
      try {
        // Extract-first: a scanned PDF throws here and is rejected before any
        // server call. The signal cancels the parse if the job is dismissed
        // mid-extraction.
        const extraction = await callbacks.current.extractor(
          file,
          controller.signal,
        );
        setState(id, { status: "requesting" });
        const ticket = await requestDocumentUpload(
          {
            title,
            contentType: DOCUMENT_CONTENT_TYPE,
            sizeBytes: file.size,
          },
          controller.signal,
        );
        if (extraction.sentences.length > ticket.maxSentences) {
          // The document exceeds the analysis cap; stop before the upload PUT so
          // no bytes are transferred. The pending record left behind never
          // reaches ready, and the library list excludes pending rows, so it
          // does not surface as a ghost card.
          setState(id, {
            status: "error",
            error: { kind: "tooLong", max: ticket.maxSentences },
          });
          return;
        }
        setState(id, { status: "uploading", progress: 0 });
        await callbacks.current.uploader(
          ticket.upload,
          file,
          (loaded, total) => {
            setState(id, {
              status: "uploading",
              progress: total > 0 ? loaded / total : 0,
            });
          },
          controller.signal,
        );
        setState(id, { status: "confirming" });
        confirmed = await ingestExtraction(
          ticket.documentId,
          { pageCount: extraction.pageCount, sentences: extraction.sentences },
          controller.signal,
        );
        setState(id, { status: "ready", document: confirmed });
      } catch (err) {
        // A dismissed job has already been removed; do not resurrect it.
        if (!controller.signal.aborted) {
          setState(
            id,
            err instanceof ScannedPdfError
              ? { status: "error", error: { kind: "scanned" } }
              : {
                  status: "error",
                  error: { kind: "failed", message: failureMessage(err) },
                },
          );
        }
        return;
      } finally {
        controllers.current.delete(id);
      }
      // Notify outside the try so a throw in onUploaded cannot reclassify a
      // genuine success as failed. confirmed is non-null only on the success
      // path (the catch always returns). The parent lifts the confirmed record
      // to a real document card, so the ready job is then dropped from state -
      // without this, a succeeded upload leaves a permanent {status:'ready'}
      // entry that the display filters out but never garbage-collects.
      if (confirmed) {
        callbacks.current.onUploaded?.(confirmed);
        setJobs((prev) => prev.filter((job) => job.id !== id));
      }
    },
    [setState],
  );

  const startUploads = useCallback(
    (files: Iterable<File>) => {
      const started: DocumentUploadJob[] = [];
      const toRun: { id: string; title: string; file: File }[] = [];
      for (const file of files) {
        const id = crypto.randomUUID();
        const title = deriveTitle(file.name, "Untitled document");
        if (!isAcceptedDocumentFile(file.name, file.type)) {
          started.push({
            id,
            title,
            fileName: file.name,
            state: { status: "error", error: { kind: "unsupported" } },
          });
          continue;
        }
        started.push({
          id,
          title,
          fileName: file.name,
          state: { status: "extracting" },
        });
        toRun.push({ id, title, file });
      }
      if (started.length === 0) {
        return;
      }
      setJobs((prev) => [...started, ...prev]);
      for (const { id, title, file } of toRun) {
        void run(id, title, file);
      }
    },
    [run],
  );

  const dismiss = useCallback((id: string) => {
    controllers.current.get(id)?.abort();
    setJobs((prev) => prev.filter((job) => job.id !== id));
  }, []);

  return { jobs, startUploads, dismiss };
}
