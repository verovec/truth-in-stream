"use client";

import type { ReactNode } from "react";
import { useFactCheck, type FactCheckState } from "@/hooks/use-fact-check";
import { PlaybackClock } from "./playback-clock";
import { SegmentList } from "./segment-list";

type FactCheckPanelProps = {
  source: string;
  pollIntervalMs?: number;
};

export function FactCheckPanel({ source, pollIntervalMs }: FactCheckPanelProps) {
  const state = useFactCheck(source, pollIntervalMs);

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
        <PlaybackClock />
      </header>
      {renderBody(state)}
    </aside>
  );
}

function renderBody(state: FactCheckState): ReactNode {
  switch (state.status) {
    case "loading":
      return (
        <div className="flex flex-col gap-3">
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            Preparing fact checks…
          </p>
          <div className="flex animate-pulse flex-col gap-2">
            <div className="h-16 rounded-lg bg-zinc-100 dark:bg-zinc-900" />
            <div className="h-16 rounded-lg bg-zinc-100 dark:bg-zinc-900" />
            <div className="h-16 rounded-lg bg-zinc-100 dark:bg-zinc-900" />
          </div>
        </div>
      );
    case "processing": {
      const { segmentsDone, segmentsTotal } = state;
      const percent =
        segmentsTotal > 0
          ? Math.round((segmentsDone / segmentsTotal) * 100)
          : 0;
      return (
        <div className="flex flex-col gap-2">
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            Checking this video against verified claims.
          </p>
          <div
            role="progressbar"
            aria-label="Segments checked"
            aria-valuemin={0}
            aria-valuemax={Math.max(segmentsTotal, 1)}
            aria-valuenow={segmentsDone}
            className="h-1.5 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800"
          >
            <div
              className="h-full rounded-full bg-sky-500 transition-[width] duration-500"
              style={{ width: `${percent}%` }}
            />
          </div>
          <p className="font-mono text-xs tabular-nums text-zinc-500 dark:text-zinc-400">
            {segmentsDone} of {segmentsTotal} segments checked
          </p>
        </div>
      );
    }
    case "error":
      return (
        <p
          role="alert"
          className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
        >
          Fact checks are unavailable: {state.message}
        </p>
      );
    case "ready":
      if (state.segments.length === 0) {
        return (
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            No speech segments were found in this video.
          </p>
        );
      }
      return <SegmentList segments={state.segments} />;
  }
}
