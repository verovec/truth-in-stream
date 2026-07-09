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
import { useAppI18n } from "@/components/i18n/app-i18n";

type DownloadFn = (
  videoId: string,
  format: ExportFormat,
  signal?: AbortSignal,
) => Promise<string>;

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
  const { t } = useAppI18n();
  const [pending, setPending] = useState<ExportFormat | null>(null);
  const [error, setError] = useState<"missing" | "failed" | null>(null);

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
        setError("missing");
      } else {
        setError("failed");
      }
    } finally {
      setPending(null);
    }
  };

  const buttonClass =
    "rounded border border-black/10 bg-white px-2.5 py-1 font-medium text-ink/80 hover:bg-black/5 disabled:opacity-60 dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10";

  return (
    <section
      aria-label={t.exports.heading}
      className="flex flex-col gap-2 rounded-xl border border-verdict-flag/40 bg-verdict-flag/5 p-3 text-xs dark:border-verdict-flag/30 dark:bg-verdict-flag/10"
    >
      <h3 className="font-semibold uppercase tracking-wide text-verdict-flag dark:text-amber-300">
        {t.exports.heading}
      </h3>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={pending !== null}
          onClick={() => run("srt")}
          className={buttonClass}
        >
          {pending === "srt" ? t.exports.transcriptPending : t.exports.transcript}
        </button>
        <button
          type="button"
          disabled={pending !== null}
          onClick={() => run("csv")}
          className={buttonClass}
        >
          {pending === "csv" ? t.exports.claimsPending : t.exports.claims}
        </button>
      </div>
      {error ? (
        <p role="status" className="text-verdict-flag dark:text-amber-300">
          {error === "missing" ? t.exports.missingSnapshot : t.exports.failed}
        </p>
      ) : null}
    </section>
  );
}
