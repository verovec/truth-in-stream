"use client";

import type { UploadJob } from "@/hooks/use-video-uploads";
import { formatTemplate } from "@/lib/i18n/text";
import {
  useAppI18n,
  type AppDictionary,
} from "@/components/i18n/app-i18n";
import { VideoPoster } from "./video-poster";

// UploadTile shows a file that is still uploading (or failed). Ready jobs are
// represented by their library row, so the gallery never renders them here.
export function UploadTile({
  job,
  onDismiss,
}: {
  job: UploadJob;
  onDismiss: () => void;
}) {
  const { t } = useAppI18n();
  return (
    <div className="flex h-full flex-col overflow-hidden rounded-xl border border-black/10 bg-white dark:border-white/10 dark:bg-white/5">
      <VideoPoster seed={job.id} title={job.title}>
        <div className="absolute inset-0 bg-night/40" />
        <div className="absolute inset-x-0 bottom-0 p-2">
          {renderOverlay(job, onDismiss, t.uploader)}
        </div>
      </VideoPoster>
      <span className="truncate px-3 py-2 text-sm font-medium text-ink dark:text-paper">
        {job.title}
      </span>
    </div>
  );
}

function renderOverlay(
  job: UploadJob,
  onDismiss: () => void,
  copy: AppDictionary["uploader"],
) {
  switch (job.state.status) {
    case "requesting":
      return <p className="text-xs font-medium text-white">{copy.preparing}</p>;
    case "uploading": {
      const percent = Math.round(job.state.progress * 100);
      return (
        <div className="flex flex-col gap-1">
          <div
            role="progressbar"
            aria-label={formatTemplate(copy.uploadingAria, {
              title: job.title,
            })}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent}
            className="h-1.5 overflow-hidden rounded-full bg-white/30"
          >
            <div
              className="h-full rounded-full bg-bleu-flag transition-[width] duration-200"
              style={{ width: `${percent}%` }}
            />
          </div>
          <p className="font-mono text-[11px] tabular-nums text-white">
            {percent}%
          </p>
        </div>
      );
    }
    case "confirming":
      return <p className="text-xs font-medium text-white">{copy.finalizing}</p>;
    case "error":
      return (
        <div className="flex flex-col items-start gap-1">
          <p
            role="alert"
            className="text-xs font-medium text-rose-100"
          >
            {job.state.error.kind === "unsupported"
              ? copy.unsupported
              : (job.state.error.message ?? copy.failed)}
          </p>
          <button
            type="button"
            onClick={onDismiss}
            className="rounded bg-white/90 px-2 py-0.5 text-[11px] font-semibold text-ink hover:bg-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white"
          >
            {copy.dismiss}
          </button>
        </div>
      );
    case "ready":
      return null;
  }
}
