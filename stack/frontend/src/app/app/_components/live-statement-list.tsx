"use client";

import { memo, useEffect, useRef } from "react";
import {
  usePlayback,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import type { SkipReason } from "@/lib/fact-check/api";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import type { LiveStatement } from "@/lib/live/statements";
import { formatTime } from "@/lib/playback/format-time";
import {
  LIVE_ROW_BASE_CLASS,
  LIVE_ROW_EMPHASIZED_CLASS,
} from "./live-row-classes";

// LiveStatementList is the subtitle region: the running transcript of finalised
// statements. Verdicts live in the decoupled fact-check list below, so each row
// here carries only the spoken text plus a light status marker (analysing,
// could-not-check, skipped, or no-match). Two scroll drivers coexist without
// fighting: the active row tracks the playback clock, and a row selected from
// the fact-check list scrolls into view on demand. Memoized so a caption-only
// update of the parent panel (every interim word) does not re-render the list.
export const LiveStatementList = memo(function LiveStatementList({
  statements,
  selectedStatementId,
  selectionTick = 0,
}: {
  statements: LiveStatement[];
  selectedStatementId: string | null;
  // Bumped by the parent on every fact-check selection so re-selecting the same
  // entry scrolls its origin back into view even when the id is unchanged.
  selectionTick?: number;
}) {
  const store = usePlaybackStore();
  const activeIndex = usePlayback((snapshot) =>
    findActiveSegmentIndex(statements, snapshot.currentTime),
  );
  const itemRefs = useRef(new Map<string, HTMLLIElement>());

  const activeId = activeIndex >= 0 ? statements[activeIndex]?.id : undefined;
  useEffect(() => {
    if (activeId === undefined) {
      return;
    }
    itemRefs.current.get(activeId)?.scrollIntoView({
      block: "nearest",
      behavior: "smooth",
    });
  }, [activeId]);

  useEffect(() => {
    if (selectedStatementId === null) {
      return;
    }
    itemRefs.current.get(selectedStatementId)?.scrollIntoView({
      block: "nearest",
      behavior: "smooth",
    });
  }, [selectedStatementId, selectionTick]);

  return (
    <ol
      aria-label="Subtitle transcript"
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pr-2"
    >
      {statements.map((statement, index) => {
        const active = index === activeIndex;
        const selected = statement.id === selectedStatementId;
        return (
          <li
            key={statement.id}
            ref={(el) => {
              const refs = itemRefs.current;
              if (el) {
                refs.set(statement.id, el);
              } else {
                refs.delete(statement.id);
              }
            }}
            aria-current={active ? "true" : undefined}
            className={`rounded-lg border transition-colors ${
              active ? LIVE_ROW_EMPHASIZED_CLASS : LIVE_ROW_BASE_CLASS
            } ${
              selected
                ? "ring-2 ring-sky-400 ring-offset-1 dark:ring-sky-500/60 dark:ring-offset-zinc-950"
                : ""
            }`}
          >
            <button
              type="button"
              onClick={() => store.seekTo(statement.start)}
              className="flex w-full flex-col gap-1 rounded-lg px-3 py-2 text-left hover:bg-zinc-900/5 focus-visible:outline-2 focus-visible:outline-sky-500 dark:hover:bg-white/5"
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
            <SubtitleStatus statement={statement} />
          </li>
        );
      })}
    </ol>
  );
});

// SKIP_LABELS explains, per skip reason, why a statement was not fact-checked.
const SKIP_LABELS: Record<SkipReason, string> = {
  not_a_claim: "No verifiable claim",
  not_covered: "Not covered by the reference corpus",
  not_checked: "The live checker was busy",
};

// skipLabel tolerates a skip reason the backend may add before the frontend
// knows it. The parameter is widened to string on purpose: the value crosses the
// wire unchecked.
function skipLabel(reason: string): string {
  return SKIP_LABELS[reason as SkipReason] ?? "Not checked";
}

// SubtitleStatus is the light per-row marker. It never shows a verdict (those
// live in the fact-check list); it only signals progress or why a statement
// produced no fact-check, so a row is never silently empty after analysis.
function SubtitleStatus({ statement }: { statement: LiveStatement }) {
  if (statement.status === "analysing") {
    return (
      <p
        role="status"
        className="flex items-center gap-2 px-3 pb-2 text-xs text-zinc-500 dark:text-zinc-400"
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
      <p className="px-3 pb-2 text-xs text-amber-700 dark:text-amber-400">
        This statement could not be checked.
      </p>
    );
  }

  if (statement.skipReason) {
    return (
      <p className="px-3 pb-2 text-xs italic text-zinc-400 dark:text-zinc-500">
        Not checked - {skipLabel(statement.skipReason)}.
      </p>
    );
  }

  if (statement.matches.length === 0) {
    return (
      <p className="px-3 pb-2 text-xs text-zinc-500 dark:text-zinc-400">
        No confident match.
      </p>
    );
  }

  return null;
}
