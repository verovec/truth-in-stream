import type { VideoKind, VideoStatus } from "@/lib/video/api";

const STATUS_STYLES: Record<VideoStatus, { label: string; className: string }> = {
  ready: {
    label: "Ready",
    className:
      "bg-emerald-100 text-emerald-800 dark:bg-emerald-500/15 dark:text-emerald-300",
  },
  pending: {
    label: "Processing",
    className:
      "bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-300",
  },
  failed: {
    label: "Failed",
    className:
      "bg-rose-100 text-rose-800 dark:bg-rose-500/15 dark:text-rose-300",
  },
};

const badgeBase =
  "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide";

export function VideoStatusBadge({ status }: { status: VideoStatus }) {
  const { label, className } = STATUS_STYLES[status];
  return <span className={`${badgeBase} ${className}`}>{label}</span>;
}

const KIND_LABELS: Record<VideoKind, string> = {
  sample: "Sample",
  upload: "Upload",
  youtube: "YouTube",
};

export function VideoKindBadge({ kind }: { kind: VideoKind }) {
  const label = KIND_LABELS[kind];
  return (
    <span
      className={`${badgeBase} bg-white/85 text-zinc-700 dark:bg-zinc-900/80 dark:text-zinc-200`}
    >
      {label}
    </span>
  );
}
