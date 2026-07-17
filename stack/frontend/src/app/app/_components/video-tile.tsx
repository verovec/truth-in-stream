"use client";

import { useVideoThumbnail } from "@/hooks/use-video-thumbnail";
import type { AnalysedLibraryVideo } from "@/lib/video/analysis";
import { VideoPoster } from "./video-poster";
import {
  VideoAnalysedBadge,
  VideoKindBadge,
  VideoStatusBadge,
} from "./video-status-badge";

// VideoTile is one selectable library item. Only ready videos are playable, so a
// pending or failed video renders disabled with its status shown. A ready tile
// lazily captures a real poster frame once it scrolls into view. A video with a
// stored pre-analysis carries an "Analysed" badge from the list payload.
export function VideoTile({
  video,
  selected,
  onSelect,
}: {
  video: AnalysedLibraryVideo;
  selected: boolean;
  onSelect: () => void;
}) {
  const selectable = video.status === "ready";
  const { ref, src } = useVideoThumbnail<HTMLButtonElement>({ video });
  return (
    <button
      ref={ref}
      type="button"
      onClick={onSelect}
      disabled={!selectable}
      aria-pressed={selected}
      className={`group flex h-full w-full flex-col overflow-hidden rounded-xl border bg-white text-left transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag disabled:cursor-not-allowed dark:bg-white/5 dark:focus-visible:outline-paper/60 ${
        selected
          ? "border-bleu-flag ring-2 ring-bleu-flag/40 dark:border-sky-400 dark:ring-sky-400/40"
          : "border-black/10 hover:border-black/25 dark:border-white/10 dark:hover:border-white/25"
      }`}
    >
      <VideoPoster seed={video.id} title={video.title} frameSrc={src}>
        <span className="pointer-events-none absolute left-2 top-2">
          <VideoKindBadge kind={video.kind} />
        </span>
        <span className="pointer-events-none absolute right-2 top-2">
          <VideoStatusBadge status={video.status} />
        </span>
        {video.analysisStatus === "complete" && (
          <span className="pointer-events-none absolute bottom-2 left-2">
            <VideoAnalysedBadge />
          </span>
        )}
      </VideoPoster>
      <span className="truncate px-3 py-2 text-sm font-medium text-ink dark:text-paper">
        {video.title}
      </span>
    </button>
  );
}
