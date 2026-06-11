"use client";

import { memo, useEffect, useRef } from "react";
import {
  usePlayback,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import type { LiveStatement } from "@/lib/live/statements";
import { formatTime } from "@/lib/playback/format-time";
import { SegmentDetail } from "./segment-detail";

// LiveStatementList renders incremental live statements with the same row
// presentation as the batch results list, plus an in-flight affordance while a
// statement's verdict is still being computed. The active statement tracks the
// playback clock and scrolls into view. Memoized so a caption-only update of the
// parent panel (every interim word) does not re-render the whole list.
export const LiveStatementList = memo(function LiveStatementList({
  statements,
}: {
  statements: LiveStatement[];
}) {
  const store = usePlaybackStore();
  const activeIndex = usePlayback((snapshot) =>
    findActiveSegmentIndex(statements, snapshot.currentTime),
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
      aria-label="Live fact-checked statements"
      className="-mr-2 flex max-h-[70svh] min-h-0 flex-col gap-3 overflow-y-auto pr-2"
    >
      {statements.map((statement, index) => {
        const active = index === activeIndex;
        return (
          <li
            key={statement.id}
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
              onClick={() => store.seekTo(statement.start)}
              className="flex w-full flex-col gap-1 rounded-t-lg px-3 py-2 text-left hover:bg-zinc-900/5 focus-visible:outline-2 focus-visible:outline-sky-500 dark:hover:bg-white/5"
            >
              <span
                className={`font-mono text-[11px] tabular-nums ${
                  active
                    ? "text-sky-700 dark:text-sky-300"
                    : "text-zinc-500 dark:text-zinc-400"
                }`}
              >
                {formatTime(statement.start)} – {formatTime(statement.end)}
              </span>
              <span className="text-sm leading-5 text-zinc-800 dark:text-zinc-200">
                {statement.text}
              </span>
            </button>
            <StatementBody statement={statement} />
          </li>
        );
      })}
    </ol>
  );
});

function StatementBody({ statement }: { statement: LiveStatement }) {
  if (statement.status === "analysing") {
    return (
      <p
        role="status"
        className="flex items-center gap-2 border-t border-dashed border-zinc-200 px-3 py-2 text-xs text-zinc-500 dark:border-zinc-800 dark:text-zinc-400"
      >
        <span
          aria-hidden="true"
          className="size-1.5 animate-pulse rounded-full bg-sky-500"
        />
        Checking this statement…
      </p>
    );
  }

  if (statement.error) {
    return (
      <p className="border-t border-dashed border-amber-200 px-3 py-2 text-xs text-amber-700 dark:border-amber-500/30 dark:text-amber-400">
        This statement could not be checked.
      </p>
    );
  }

  return (
    <SegmentDetail
      segment={{
        start: statement.start,
        end: statement.end,
        text: statement.text,
        matches: statement.matches,
        skipReason: statement.skipReason,
      }}
    />
  );
}
