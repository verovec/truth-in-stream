"use client";

import { usePlayback } from "@/components/playback/playback-provider";
import { formatTime } from "@/lib/playback/format-time";

export function PlaybackClock() {
  const currentTime = usePlayback((snapshot) =>
    Math.floor(snapshot.currentTime),
  );

  return (
    <p className="text-sm tabular-nums text-ink/50 dark:text-paper/50">
      {formatTime(currentTime)}
    </p>
  );
}
