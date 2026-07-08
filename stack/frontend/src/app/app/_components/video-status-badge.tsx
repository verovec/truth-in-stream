"use client";

import type { VideoKind, VideoStatus } from "@/lib/video/api";
import { useAppI18n } from "@/components/i18n/app-i18n";

// Status tones ride the shared semantic verdict tokens so "ready" reads with
// the same green as a credible verdict and "failed" with the disputed rouge.
// The badges float over poster art, so they keep an opaque paper/night backdrop
// and carry the tone in the text; labels come from the active locale's
// dictionary.
const STATUS_STYLES: Record<VideoStatus, string> = {
  ready: "text-verdict-credible",
  pending: "text-verdict-flag dark:text-amber-300",
  failed: "text-verdict-disputed",
};

const badgeBase =
  "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide";

export function VideoStatusBadge({ status }: { status: VideoStatus }) {
  const { t } = useAppI18n();
  return (
    <span
      className={`${badgeBase} bg-white/85 dark:bg-night/80 ${STATUS_STYLES[status]}`}
    >
      {t.library.status[status]}
    </span>
  );
}

export function VideoKindBadge({ kind }: { kind: VideoKind }) {
  const { t } = useAppI18n();
  return (
    <span
      className={`${badgeBase} bg-white/85 text-ink/80 dark:bg-night/80 dark:text-paper/80`}
    >
      {t.library.kind[kind]}
    </span>
  );
}
