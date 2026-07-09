"use client";

import { useState } from "react";
import type { DocumentRecord } from "@/lib/documents/api";
import { ApiError } from "@/lib/http";
import { useAppI18n } from "@/components/i18n/app-i18n";

// ReanalyseControl is the admin-only trigger for a fresh analysis run. It
// confirms before firing (a run replaces the previous results), disables itself
// while a run is in flight or already analysing, and surfaces a concurrent-run
// 409 or a disabled-path 503 as a clear message rather than a silent failure.
// The retry variant is used by the failure banner; both share this one flow so a
// failed document restarts exactly like a completed one. onStarted re-arms the
// caller's polling so progress appears at once.
export function ReanalyseControl({
  document,
  reanalyse,
  onStarted,
  variant = "action",
}: {
  document: DocumentRecord;
  reanalyse: (id: string, signal?: AbortSignal) => Promise<void>;
  onStarted: () => void;
  variant?: "action" | "retry";
}) {
  const { t } = useAppI18n();
  const [confirming, setConfirming] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const running = submitting || document.analysisStatus === "analysing";

  const fire = async () => {
    setSubmitting(true);
    setError(null);
    try {
      await reanalyse(document.id);
      setConfirming(false);
      onStarted();
    } catch (err) {
      setConfirming(false);
      if (err instanceof ApiError && err.status === 409) {
        setError(t.viewer.reanalyse.errors.conflict);
      } else if (err instanceof ApiError && err.status === 503) {
        setError(t.viewer.reanalyse.errors.disabled);
      } else {
        setError(t.viewer.reanalyse.errors.failed);
      }
    } finally {
      setSubmitting(false);
    }
  };

  const label =
    variant === "retry" ? t.viewer.reanalyse.retry : t.viewer.reanalyse.action;

  return (
    <div className="flex flex-col items-start gap-1.5 sm:items-end">
      {confirming ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-ink/60 dark:text-paper/60">
            {t.viewer.reanalyse.confirm}
          </span>
          <button
            type="button"
            onClick={fire}
            disabled={running}
            className="rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
          >
            {t.viewer.reanalyse.confirmYes}
          </button>
          <button
            type="button"
            onClick={() => setConfirming(false)}
            className="rounded-md px-2.5 py-1 text-xs font-medium text-ink/60 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-paper/60 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
          >
            {t.viewer.reanalyse.confirmNo}
          </button>
        </div>
      ) : (
        <button
          type="button"
          disabled={running}
          onClick={() => setConfirming(true)}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {running ? t.viewer.reanalyse.running : label}
        </button>
      )}
      {error ? (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {error}
        </p>
      ) : null}
    </div>
  );
}
