"use client";

// Admin-only download controls for a completed video's analysis exports. The role
// is resolved server-side from the verified Keycloak session and passed in, so a
// guest never receives the controls in their tree; the backend independently gates
// the endpoints on a verified admin claim, so this is a reveal only. The controls
// appear only when a ready video is active. A 404 (no cached snapshot) is surfaced
// inline rather than failing silently.
import { useState } from "react";

import { downloadExport, type ExportFormat } from "@/lib/video/export";
import { ApiError } from "@/lib/http";
import type { Role } from "@/lib/auth/token";

type DownloadFn = (
  videoId: string,
  format: ExportFormat,
  signal?: AbortSignal,
) => Promise<string>;

const MISSING_SNAPSHOT_MESSAGE =
  "No cached analysis for this video. Re-run analysis to repopulate the export cache.";

// ExportControls renders the SRT and CSV download buttons for an admin viewing a
// ready video. download is an injection seam for tests; production uses the real
// client.
export function ExportControls({
  role,
  videoId,
  download = downloadExport,
}: {
  role: Role;
  videoId: string | null;
  download?: DownloadFn;
}) {
  const [pending, setPending] = useState<ExportFormat | null>(null);
  const [error, setError] = useState<string | null>(null);

  if (role !== "admin" || !videoId) {
    return null;
  }

  const run = async (format: ExportFormat) => {
    setError(null);
    setPending(format);
    try {
      await download(videoId, format);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setError(MISSING_SNAPSHOT_MESSAGE);
      } else {
        setError("Export failed. Please try again.");
      }
    } finally {
      setPending(null);
    }
  };

  return (
    <section
      aria-label="Admin exports"
      className="flex flex-col gap-2 rounded-md border border-amber-400/60 bg-amber-50/60 p-3 text-xs dark:border-amber-500/40 dark:bg-zinc-900/60"
    >
      <h3 className="font-semibold uppercase tracking-wide text-amber-700 dark:text-amber-400">
        Admin exports
      </h3>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={pending !== null}
          onClick={() => run("srt")}
          className="rounded border border-zinc-300 bg-white px-2.5 py-1 font-medium text-zinc-800 hover:bg-zinc-50 disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100"
        >
          {pending === "srt" ? "Preparing transcript…" : "Transcript (.srt)"}
        </button>
        <button
          type="button"
          disabled={pending !== null}
          onClick={() => run("csv")}
          className="rounded border border-zinc-300 bg-white px-2.5 py-1 font-medium text-zinc-800 hover:bg-zinc-50 disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100"
        >
          {pending === "csv" ? "Preparing claims…" : "Claims (.csv)"}
        </button>
      </div>
      {error ? (
        <p role="status" className="text-amber-700 dark:text-amber-400">
          {error}
        </p>
      ) : null}
    </section>
  );
}
