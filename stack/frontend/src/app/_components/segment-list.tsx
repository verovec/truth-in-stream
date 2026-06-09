"use client";

import { useEffect, useRef } from "react";
import {
  usePlayback,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import type { FactCheckSegment } from "@/lib/fact-check/api";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import { formatTime } from "@/lib/playback/format-time";
import { VerdictBadge } from "./verdict-badge";

export function SegmentList({ segments }: { segments: FactCheckSegment[] }) {
  const store = usePlaybackStore();
  const activeIndex = usePlayback((snapshot) =>
    findActiveSegmentIndex(segments, snapshot.currentTime),
  );
  const activeItemRef = useRef<HTMLLIElement>(null);

  useEffect(() => {
    activeItemRef.current?.scrollIntoView({
      block: "nearest",
      behavior: "smooth",
    });
  }, [activeIndex]);

  return (
    <ol
      aria-label="Fact-checked segments"
      className="-mr-2 flex max-h-[70svh] min-h-0 flex-col gap-3 overflow-y-auto pr-2"
    >
      {segments.map((segment, index) => {
        const active = index === activeIndex;
        return (
          <li
            key={segment.start}
            ref={active ? activeItemRef : undefined}
            aria-current={active ? "true" : undefined}
            className={`rounded-lg border transition-colors ${
              active
                ? "border-sky-400 bg-sky-50 dark:border-sky-500/60 dark:bg-sky-500/10"
                : "border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-950"
            }`}
          >
            <button
              type="button"
              onClick={() => store.seekTo(segment.start)}
              className="flex w-full flex-col gap-1 rounded-t-lg px-3 py-2 text-left hover:bg-zinc-900/5 focus-visible:outline-2 focus-visible:outline-sky-500 dark:hover:bg-white/5"
            >
              <span
                className={`font-mono text-[11px] tabular-nums ${
                  active
                    ? "text-sky-700 dark:text-sky-300"
                    : "text-zinc-500 dark:text-zinc-400"
                }`}
              >
                {formatTime(segment.start)} – {formatTime(segment.end)}
              </span>
              <span className="text-sm leading-5 text-zinc-800 dark:text-zinc-200">
                {segment.text}
              </span>
            </button>
            {segment.matches.length === 0 ? (
              <p className="border-t border-dashed border-zinc-200 px-3 py-2 text-xs text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
                No confident match for this segment.
              </p>
            ) : (
              <div className="flex flex-col divide-y divide-zinc-100 border-t border-zinc-200 dark:divide-zinc-900 dark:border-zinc-800">
                {segment.matches.map((match, matchIndex) => (
                  <article
                    key={matchIndex}
                    className="flex flex-col gap-1.5 px-3 py-2"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <VerdictBadge verdict={match.verdict} />
                      <span className="font-mono text-[11px] tabular-nums text-zinc-400 dark:text-zinc-500">
                        {Math.round(match.similarity * 100)}% match
                      </span>
                    </div>
                    <p className="text-sm leading-5 text-zinc-700 dark:text-zinc-300">
                      {match.claim}
                    </p>
                    <p className="flex flex-wrap gap-x-3 gap-y-1">
                      {match.sources.map((source) => (
                        <a
                          key={source.url}
                          href={source.url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="text-xs font-medium text-sky-700 underline decoration-sky-300 underline-offset-2 hover:decoration-sky-600 dark:text-sky-400 dark:decoration-sky-700 dark:hover:decoration-sky-400"
                        >
                          {source.title}
                        </a>
                      ))}
                    </p>
                  </article>
                ))}
              </div>
            )}
          </li>
        );
      })}
    </ol>
  );
}
