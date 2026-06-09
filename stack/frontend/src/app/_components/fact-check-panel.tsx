"use client";

import { usePlayback } from "@/components/playback/playback-provider";
import { formatTime } from "@/lib/playback/format-time";

export function FactCheckPanel() {
  const currentTime = usePlayback((snapshot) =>
    Math.floor(snapshot.currentTime),
  );

  return (
    <aside
      aria-labelledby="fact-check-heading"
      className="flex h-full flex-col gap-4 rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950"
    >
      <header className="flex items-baseline justify-between gap-2">
        <h2
          id="fact-check-heading"
          className="text-sm font-semibold uppercase tracking-wide text-zinc-900 dark:text-zinc-100"
        >
          Fact checks
        </h2>
        <p className="font-mono text-sm tabular-nums text-zinc-500 dark:text-zinc-400">
          {formatTime(currentTime)}
        </p>
      </header>
      <p className="text-sm leading-6 text-zinc-600 dark:text-zinc-400">
        Fact-check results for the segment being spoken will appear here as the
        video plays.
      </p>
    </aside>
  );
}
