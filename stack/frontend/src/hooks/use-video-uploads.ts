"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  confirmVideo,
  isAcceptedVideoType,
  type LibraryVideo,
  requestUpload,
} from "@/lib/video/api";
import { putWithProgress, type PutUploader } from "@/lib/video/upload";

// UploadJobState is the lifecycle of one file as it uploads. It is a
// discriminated union so a job can never be both uploading and failed.
export type UploadJobState =
  | { status: "requesting" }
  | { status: "uploading"; progress: number }
  | { status: "confirming" }
  | { status: "ready"; video: LibraryVideo }
  | { status: "error"; message: string };

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

const UNSUPPORTED_MESSAGE =
  "Unsupported file type. Upload an MP4, WebM, OGG, or MOV video.";

function deriveTitle(fileName: string): string {
  const dot = fileName.lastIndexOf(".");
  const base = dot > 0 ? fileName.slice(0, dot) : fileName;
  return base.trim() || "Untitled video";
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : "Upload failed.";
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

  const setState = useCallback((id: string, state: UploadJobState) => {
    setJobs((prev) =>
      prev.map((job) => (job.id === id ? { ...job, state } : job)),
    );
  }, []);

  const run = useCallback(
    async (id: string, title: string, file: File) => {
      try {
        const ticket = await requestUpload({
          title,
          contentType: file.type,
          sizeBytes: file.size,
        });
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
        );
        setState(id, { status: "confirming" });
        const video = await confirmVideo(ticket.videoId);
        setState(id, { status: "ready", video });
        callbacks.current.onUploaded?.(video);
      } catch (err) {
        setState(id, { status: "error", message: errorMessage(err) });
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
            state: { status: "error", message: UNSUPPORTED_MESSAGE },
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
    setJobs((prev) => prev.filter((job) => job.id !== id));
  }, []);

  return { jobs, startUploads, dismiss };
}
