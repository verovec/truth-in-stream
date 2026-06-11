import type { UploadJob } from "@/hooks/use-video-uploads";
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
  return (
    <div className="flex h-full flex-col overflow-hidden rounded-xl border border-zinc-200 dark:border-zinc-800">
      <VideoPoster seed={job.id} title={job.title}>
        <div className="absolute inset-0 bg-black/35" />
        <div className="absolute inset-x-0 bottom-0 p-2">
          {renderOverlay(job, onDismiss)}
        </div>
      </VideoPoster>
      <span className="truncate px-3 py-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
        {job.title}
      </span>
    </div>
  );
}

function renderOverlay(job: UploadJob, onDismiss: () => void) {
  switch (job.state.status) {
    case "requesting":
      return <p className="text-xs font-medium text-white">Preparing…</p>;
    case "uploading": {
      const percent = Math.round(job.state.progress * 100);
      return (
        <div className="flex flex-col gap-1">
          <div
            role="progressbar"
            aria-label={`Uploading ${job.title}`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent}
            className="h-1.5 overflow-hidden rounded-full bg-white/30"
          >
            <div
              className="h-full rounded-full bg-sky-400 transition-[width] duration-200"
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
      return <p className="text-xs font-medium text-white">Finalizing…</p>;
    case "error":
      return (
        <div className="flex flex-col items-start gap-1">
          <p
            role="alert"
            className="text-xs font-medium text-rose-100"
          >
            {job.state.message}
          </p>
          <button
            type="button"
            onClick={onDismiss}
            className="rounded bg-white/90 px-2 py-0.5 text-[11px] font-semibold text-zinc-900 hover:bg-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white"
          >
            Dismiss
          </button>
        </div>
      );
    case "ready":
      return null;
  }
}
