"use client";

import { useVideoThumbnail } from "@/hooks/use-video-thumbnail";
import type { LibraryVideo } from "@/lib/video/api";
import { VideoPoster } from "./video-poster";
import { VideoKindBadge, VideoStatusBadge } from "./video-status-badge";

// VideoTile is one selectable library item. Only ready videos are playable, so a
// pending or failed video renders disabled with its status shown. A ready tile
// lazily captures a real poster frame once it scrolls into view.
export function VideoTile({
  video,
  selected,
  onSelect,
}: {
  video: LibraryVideo;
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
      className={`group flex flex-col overflow-hidden rounded-xl border text-left transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 disabled:cursor-not-allowed ${
        selected
          ? "border-sky-500 ring-2 ring-sky-500/40"
          : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-800 dark:hover:border-zinc-700"
      }`}
    >
      <VideoPoster seed={video.id} title={video.title} frameSrc={src}>
        <span className="pointer-events-none absolute left-2 top-2">
          <VideoKindBadge kind={video.kind} />
        </span>
        <span className="pointer-events-none absolute right-2 top-2">
          <VideoStatusBadge status={video.status} />
        </span>
      </VideoPoster>
      <span className="truncate px-3 py-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
        {video.title}
      </span>
    </button>
  );
}
