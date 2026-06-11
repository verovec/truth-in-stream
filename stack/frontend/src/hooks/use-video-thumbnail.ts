"use client";

import { type RefObject, useEffect, useRef, useState } from "react";
import { getVideo, type LibraryVideo, type PlayableVideo } from "@/lib/video/api";
import { loadThumbnailSource } from "@/lib/video/thumbnail";

type UseVideoThumbnailOptions = {
  video: LibraryVideo;
  // getPlayback is an injection seam for tests; it defaults to the real per-video
  // presigned playback request.
  getPlayback?: (id: string, signal?: AbortSignal) => Promise<PlayableVideo>;
};

type UseVideoThumbnailResult<T extends HTMLElement> = {
  ref: RefObject<T | null>;
  src: string | null;
};

// useVideoThumbnail lazily resolves a tile's poster frame URL. Only ready videos
// are playable, so only they capture a frame; the fetch is gated on the tile
// entering the viewport via IntersectionObserver, so painting a long library does
// not mint a presigned URL for every row at once. Any failure leaves src null and
// the tile keeps its gradient fallback. Where IntersectionObserver is unavailable
// (e.g. the test DOM), it is a no-op rather than an eager fetch.
export function useVideoThumbnail<T extends HTMLElement>({
  video,
  getPlayback = getVideo,
}: UseVideoThumbnailOptions): UseVideoThumbnailResult<T> {
  const ref = useRef<T | null>(null);
  const [src, setSrc] = useState<string | null>(null);
  const isReady = video.status === "ready";

  useEffect(() => {
    if (!isReady || src !== null) {
      return;
    }
    const node = ref.current;
    if (!node || typeof IntersectionObserver === "undefined") {
      return;
    }
    let cancelled = false;
    const observer = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) {
        return;
      }
      observer.disconnect();
      loadThumbnailSource(video.id, getPlayback)
        .then((url) => {
          if (!cancelled) {
            setSrc(url);
          }
        })
        .catch(() => {
          // Graceful: any failure keeps the gradient fallback.
        });
    });
    observer.observe(node);
    return () => {
      cancelled = true;
      observer.disconnect();
    };
  }, [isReady, src, video.id, getPlayback]);

  return { ref, src };
}
