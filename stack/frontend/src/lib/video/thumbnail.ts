// Client-side thumbnail helpers for library tiles. A ready video's poster is a
// real frame: the tile renders the presigned playback URL in a muted <video>
// seeked a couple of seconds in. The presigned URL is per-video and short-lived,
// so it is fetched lazily (only for tiles near the viewport) and memoised here so
// a re-scroll does not re-mint it.

// THUMBNAIL_SEEK_SECONDS is the target offset for the poster frame. ~2s clears the
// usual leading keyframe gap so the frame is rarely black, while staying in the
// first moments of the clip.
const THUMBNAIL_SEEK_SECONDS = 2;

// seekTarget clamps the poster offset into the first half of the clip so a short
// video never seeks past its end. Returns 0 for an unknown/empty duration.
export function seekTarget(duration: number): number {
  if (!Number.isFinite(duration) || duration <= 0) {
    return 0;
  }
  return Math.min(THUMBNAIL_SEEK_SECONDS, duration / 2);
}

type PlaybackSource = (id: string) => Promise<{ playback: { url: string } }>;

// Memoised per video id: the playback URL for a tile is fetched at most once for
// the lifetime of the page, deduping concurrent callers and surviving a re-scroll.
// The fetch is deliberately not abortable: it is a cheap idempotent GET, and
// binding it to one caller's lifetime would let that caller's unmount reject the
// shared promise and defeat the dedup. A genuinely failed fetch is evicted so a
// later attempt can retry rather than caching the error forever.
const sourceCache = new Map<string, Promise<string>>();

export function loadThumbnailSource(
  id: string,
  getPlayback: PlaybackSource,
): Promise<string> {
  const cached = sourceCache.get(id);
  if (cached) {
    return cached;
  }
  const pending = getPlayback(id).then((playable) => playable.playback.url);
  pending.catch(() => sourceCache.delete(id));
  sourceCache.set(id, pending);
  return pending;
}

// resetThumbnailSourceCache clears the module-level cache. Exported for test
// isolation; production code never needs it.
export function resetThumbnailSourceCache(): void {
  sourceCache.clear();
}
