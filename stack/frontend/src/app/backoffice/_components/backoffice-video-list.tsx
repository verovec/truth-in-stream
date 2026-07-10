"use client";

import { useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import type { LibraryVideo } from "@/lib/video/api";
import {
  VideoKindBadge,
  VideoStatusBadge,
} from "@/app/app/_components/video-status-badge";

// DeleteError keeps the failure as data (not a rendered string) so an already-
// shown error re-labels itself when the admin switches locales; a failed delete
// keeps the API's own message when it carried one (else null -> generic copy).
type DeleteError = { message: string | null };

// BackofficeVideoList is the admin management list: every video, one row each
// with its title, kind and status badges, and a two-step delete control. remove
// performs the deletion; onDeleted re-lists the catalog so a removed row leaves.
export function BackofficeVideoList({
  videos,
  remove,
  onDeleted,
}: {
  videos: LibraryVideo[];
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
}) {
  const { t } = useAppI18n();
  if (videos.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-black/15 px-4 py-8 text-center text-sm text-ink/50 dark:border-white/15 dark:text-paper/50">
        {t.backoffice.videos.list.empty}
      </p>
    );
  }
  return (
    <ul className="flex flex-col gap-2">
      {videos.map((video) => (
        <li key={video.id}>
          <BackofficeVideoRow
            video={video}
            remove={remove}
            onDeleted={onDeleted}
          />
        </li>
      ))}
    </ul>
  );
}

// BackofficeVideoRow mirrors the document viewer's reanalyse confirm pattern: a
// two-step confirm (not a native window.confirm), disabled while the request is
// in flight, with any API error surfaced inline as an alert. It owns only that
// per-row interaction; the parent owns the catalog and its refresh.
function BackofficeVideoRow({
  video,
  remove,
  onDeleted,
}: {
  video: LibraryVideo;
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
}) {
  const { t } = useAppI18n();
  const copy = t.backoffice.videos.list;
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<DeleteError | null>(null);

  const fire = async () => {
    setDeleting(true);
    setError(null);
    try {
      await remove(video.id);
      // onDeleted re-lists; this row unmounts once the video leaves the catalog,
      // so no success-path state reset is needed.
      setConfirming(false);
      onDeleted();
    } catch (err) {
      setConfirming(false);
      setDeleting(false);
      setError({ message: err instanceof Error ? err.message : null });
    }
  };

  return (
    <div className="flex flex-col gap-1.5 rounded-lg border border-black/10 bg-white px-3 py-2 dark:border-white/10 dark:bg-white/5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-ink dark:text-paper">
          {video.title}
        </span>
        <VideoKindBadge kind={video.kind} />
        <VideoStatusBadge status={video.status} />
        {confirming ? (
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-ink/60 dark:text-paper/60">
              {copy.confirm}
            </span>
            <button
              type="button"
              onClick={fire}
              disabled={deleting}
              className="rounded-md border border-rouge/30 bg-rouge/5 px-2.5 py-1 text-xs font-medium text-rouge hover:bg-rouge/10 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-rouge dark:text-rose-300"
            >
              {deleting ? copy.deleting : copy.confirmYes}
            </button>
            <button
              type="button"
              onClick={() => setConfirming(false)}
              disabled={deleting}
              className="rounded-md px-2.5 py-1 text-xs font-medium text-ink/60 hover:bg-black/5 disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:text-paper/60 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
            >
              {copy.confirmNo}
            </button>
          </span>
        ) : (
          <button
            type="button"
            onClick={() => setConfirming(true)}
            className="rounded-md border border-black/10 bg-white px-2.5 py-1 text-xs font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
          >
            {copy.delete}
          </button>
        )}
      </div>
      {error ? (
        <p role="alert" className="text-xs text-rouge dark:text-rose-300">
          {error.message === null
            ? copy.deleteErrorFallback
            : formatTemplate(copy.deleteError, { message: error.message })}
        </p>
      ) : null}
    </div>
  );
}
