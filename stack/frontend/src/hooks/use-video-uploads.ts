"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  confirmVideo,
  isAcceptedVideoType,
  type LibraryVideo,
  requestUpload,
} from "@/lib/video/api";
import { putWithProgress, type PutUploader } from "@/lib/video/upload";

// UploadError is why a job failed, kept as data rather than a rendered string so
// the tile can localize it: an unsupported file type is a fixed, translatable
// reason, while a transport/backend failure carries the raw message when it has
// one (else null, so the tile falls back to a localized generic message).
export type UploadError =
  | { kind: "unsupported" }
  | { kind: "failed"; message: string | null };

// UploadJobState is the lifecycle of one file as it uploads. It is a
// discriminated union so a job can never be both uploading and failed.
export type UploadJobState =
  | { status: "requesting" }
  | { status: "uploading"; progress: number }
  | { status: "confirming" }
  | { status: "ready"; video: LibraryVideo }
  | { status: "error"; error: UploadError };

export type UploadJob = {
  id: string;
  title: string;
  fileName: string;
  state: UploadJobState;
};

type UseVideoUploadsOptions = {
  // uploader is the PUT transport; injected so tests avoid XMLHttpRequest.
  uploader?: PutUploader;
  // onUploaded fires once a job's object is confirmed ready in storage.
  onUploaded?: (video: LibraryVideo) => void;
};

function deriveTitle(fileName: string): string {
  const dot = fileName.lastIndexOf(".");
  const base = dot > 0 ? fileName.slice(0, dot) : fileName;
  return base.trim() || "Untitled video";
}

// The raw backend/transport message when the failure carried one, else null so
// the tile shows its localized generic fallback rather than baked-in English.
function failureMessage(err: unknown): string | null {
  return err instanceof Error ? err.message : null;
}

export function useVideoUploads({
  uploader = putWithProgress,
  onUploaded,
}: UseVideoUploadsOptions) {
  const [jobs, setJobs] = useState<UploadJob[]>([]);
  // Hold the latest callbacks in a ref so the async pipeline never closes over
  // stale props between renders.
  const callbacks = useRef({ uploader, onUploaded });
  useEffect(() => {
    callbacks.current = { uploader, onUploaded };
  });

  // One AbortController per in-flight job, so dismiss and unmount cancel the
  // request/upload/confirm chain instead of letting it write state after the
  // job is gone.
  const controllers = useRef(new Map<string, AbortController>());
  useEffect(() => {
    const inFlight = controllers.current;
    return () => {
      for (const controller of inFlight.values()) {
        controller.abort();
      }
    };
  }, []);

  const setState = useCallback((id: string, state: UploadJobState) => {
    setJobs((prev) =>
      prev.map((job) => (job.id === id ? { ...job, state } : job)),
    );
  }, []);

  const run = useCallback(
    async (id: string, title: string, file: File) => {
      const controller = new AbortController();
      controllers.current.set(id, controller);
      let confirmed: LibraryVideo | null = null;
      try {
        const ticket = await requestUpload(
          { title, contentType: file.type, sizeBytes: file.size },
          controller.signal,
        );
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
        confirmed = await confirmVideo(ticket.videoId, controller.signal);
        setState(id, { status: "ready", video: confirmed });
      } catch (err) {
        // A cancelled job has already been removed; do not resurrect it as an
        // error.
        if (!controller.signal.aborted) {
          setState(id, {
            status: "error",
            error: { kind: "failed", message: failureMessage(err) },
          });
        }
        return;
      } finally {
        controllers.current.delete(id);
      }
      // Notify outside the try so a throw in onUploaded cannot reclassify a
      // genuinely succeeded upload as failed. confirmed is non-null only on the
      // success path (the catch always returns).
      if (confirmed) {
        callbacks.current.onUploaded?.(confirmed);
      }
    },
    [setState],
  );

  const startUploads = useCallback(
    (files: Iterable<File>) => {
      const started: UploadJob[] = [];
      const toRun: { id: string; title: string; file: File }[] = [];
      for (const file of files) {
        const id = crypto.randomUUID();
        const title = deriveTitle(file.name);
        if (!isAcceptedVideoType(file.type)) {
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
          state: { status: "requesting" },
        });
        toRun.push({ id, title, file });
      }
      if (started.length === 0) {
        return;
      }
      // Register every job before starting its pipeline, so the first state
      // update always finds the job in state.
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
