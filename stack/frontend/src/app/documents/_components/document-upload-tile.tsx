"use client";

import type {
  DocumentUploadError,
  DocumentUploadJob,
} from "@/hooks/use-document-uploads";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";

// DocumentUploadTile shows one in-flight (or failed) upload job while its PDF is
// extracted, uploaded, and confirmed. A succeeded job leaves this list and
// becomes a real document card, so this tile only ever renders the working and
// error states. The dismiss control cancels an in-flight job or clears a failed
// one.
export function DocumentUploadTile({
  job,
  onDismiss,
}: {
  job: DocumentUploadJob;
  onDismiss: (id: string) => void;
}) {
  const { t } = useAppI18n();
  const copy = t.documents.uploader;

  return (
    <article
      aria-label={formatTemplate(copy.uploadingAria, { title: job.title })}
      className="flex flex-col gap-2 rounded-xl border border-black/10 bg-white p-4 dark:border-white/10 dark:bg-white/5"
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="min-w-0 truncate font-medium text-ink dark:text-paper">
          {job.title}
        </h3>
        <button
          type="button"
          onClick={() => onDismiss(job.id)}
          className="rounded-full px-2 py-0.5 text-xs font-medium text-ink/60 hover:bg-black/5 hover:text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-paper/60 dark:hover:bg-white/10 dark:hover:text-paper"
        >
          {copy.dismiss}
        </button>
      </div>
      <UploadProgress state={job.state} />
    </article>
  );
}

function UploadProgress({
  state,
}: {
  state: DocumentUploadJob["state"];
}) {
  const { t } = useAppI18n();
  const copy = t.documents.uploader;
  switch (state.status) {
    case "extracting":
      return <StatusLine>{copy.extracting}</StatusLine>;
    case "requesting":
      return <StatusLine>{copy.preparing}</StatusLine>;
    case "uploading":
      return (
        <div className="flex flex-col gap-1">
          <div className="h-1.5 w-full overflow-hidden rounded-full bg-black/10 dark:bg-white/10">
            <div
              className="h-full rounded-full bg-bleu transition-[width] dark:bg-sky-400"
              style={{ width: `${Math.round(state.progress * 100)}%` }}
            />
          </div>
        </div>
      );
    case "confirming":
      return <StatusLine>{copy.finalizing}</StatusLine>;
    case "ready":
      // A ready job is lifted to a real card; keep a benign line if it lingers a
      // frame before the list update.
      return <StatusLine>{copy.finalizing}</StatusLine>;
    case "error":
      return (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {errorMessage(copy, state.error)}
        </p>
      );
  }
}

function errorMessage(
  copy: {
    errors: { unsupported: string; scanned: string; tooLong: string; failed: string };
  },
  error: DocumentUploadError,
): string {
  switch (error.kind) {
    case "unsupported":
      return copy.errors.unsupported;
    case "scanned":
      return copy.errors.scanned;
    case "tooLong":
      return formatTemplate(copy.errors.tooLong, { max: error.max });
    case "failed":
      return error.message ?? copy.errors.failed;
  }
}

function StatusLine({ children }: { children: React.ReactNode }) {
  return (
    <p className="text-xs text-ink/60 dark:text-paper/60">{children}</p>
  );
}
