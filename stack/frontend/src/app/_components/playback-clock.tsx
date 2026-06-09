"use client";

import { usePlayback } from "@/components/playback/playback-provider";
import { formatTime } from "@/lib/playback/format-time";

export function PlaybackClock() {
  const currentTime = usePlayback((snapshot) =>
    Math.floor(snapshot.currentTime),
  );

  return (
    <p className="font-mono text-sm tabular-nums text-zinc-500 dark:text-zinc-400">
      {formatTime(currentTime)}
    </p>
  );
}
