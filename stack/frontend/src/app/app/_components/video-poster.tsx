"use client";

import { type ReactNode, useRef, useState } from "react";
import { formatTime } from "@/lib/playback/format-time";
import { posterGradient, posterInitials } from "@/lib/video/poster";
import { seekTarget } from "@/lib/video/thumbnail";

// VideoPoster paints a library tile's thumbnail. A real captured frame is layered
// over a stable gradient monogram: the gradient is always present underneath so
// there is no flash or layout shift, and the frame fades in once it is seekable.
// Anything missing or failing (no frame URL, decode/seek error) leaves the
// gradient visible. children overlays badges on top of the art.
export function VideoPoster({
  seed,
  title,
  frameSrc,
  children,
}: {
  seed: string;
  title: string;
  frameSrc?: string | null;
  children?: ReactNode;
}) {
  // Track which src failed rather than a reset-on-change effect: when frameSrc
  // advances to a new URL, it no longer matches failedSrc, so the frame is tried
  // again without any effect synchronising the flag.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);

  const showFrame = Boolean(frameSrc) && frameSrc !== failedSrc;

  return (
    <div className="relative aspect-video w-full overflow-hidden">
      <GradientArt seed={seed} title={title} />
      {showFrame && frameSrc ? (
        <ThumbnailFrame
          key={frameSrc}
          src={frameSrc}
          onError={() => setFailedSrc(frameSrc)}
        />
      ) : null}
      {children}
    </div>
  );
}

function GradientArt({ seed, title }: { seed: string; title: string }) {
  return (
    <div
      aria-hidden
      className="absolute inset-0 flex items-center justify-center"
      style={{ backgroundImage: posterGradient(seed) }}
    >
      <span className="text-3xl font-bold tracking-tight text-white/90 drop-shadow-sm">
        {posterInitials(title)}
      </span>
    </div>
  );
}

function ThumbnailFrame({ src, onError }: { src: string; onError: () => void }) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [seeked, setSeeked] = useState(false);
  const [durationLabel, setDurationLabel] = useState<string | null>(null);

  // A range-served clip can report its duration only after loadedmetadata, so the
  // badge is (re)derived on durationchange too; an unknown duration omits it.
  function syncDuration() {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    const { duration } = video;
    setDurationLabel(
      Number.isFinite(duration) && duration > 0 ? formatTime(duration) : null,
    );
  }

  function handleLoadedMetadata() {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    syncDuration();
    video.currentTime = seekTarget(video.duration);
  }

  return (
    <>
      {/* Decorative, muted, aria-hidden poster frame; it never plays, so it
          carries no caption track. */}
      <video
        ref={videoRef}
        src={src}
        aria-hidden
        muted
        playsInline
        preload="metadata"
        tabIndex={-1}
        onLoadedMetadata={handleLoadedMetadata}
        onDurationChange={syncDuration}
        onSeeked={() => setSeeked(true)}
        onError={onError}
        className={`absolute inset-0 h-full w-full object-cover transition-opacity duration-300 ${
          seeked ? "opacity-100" : "opacity-0"
        }`}
      />
      {seeked ? <PlayOverlay /> : null}
      {seeked && durationLabel ? <DurationBadge label={durationLabel} /> : null}
    </>
  );
}

function PlayOverlay() {
  return (
    <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
      <span className="flex h-10 w-10 items-center justify-center rounded-full bg-black/45 backdrop-blur-sm">
        <svg
          viewBox="0 0 24 24"
          aria-hidden
          className="h-5 w-5 translate-x-px fill-white"
        >
          <path d="M8 5v14l11-7z" />
        </svg>
      </span>
    </div>
  );
}

function DurationBadge({ label }: { label: string }) {
  return (
    <span className="pointer-events-none absolute bottom-1.5 right-1.5 rounded bg-black/70 px-1.5 py-0.5 font-mono text-[10px] font-medium tabular-nums text-white">
      {label}
    </span>
  );
}
