"use client";

import { memo, useEffect, useRef } from "react";
import {
  usePlayback,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import type { SkipReason } from "@/lib/fact-check/api";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import { isScored, type LiveStatement } from "@/lib/live/statements";
import { formatTime } from "@/lib/playback/format-time";

// speakerLabel renders the diarized speaker label as a reader-facing tag. The
// provider emits a bare identifier (e.g. "A"); the "Speaker" prefix makes it
// legible without the consumer knowing the provider's convention.
function speakerLabel(speaker: string): string {
  return `Speaker ${speaker}`;
}

// LiveStatementList is the subtitle region: the running transcript of finalised
// statements, rendered as one continuous, borderless flow (not boxed cards) so
// it reads like a transcript. Each line carries its speaker label (when
// diarized) and timestamp above the spoken text, plus a light status marker
// (analysing, could-not-check, skipped, or no-match); verdicts live in the
// decoupled fact-check list below. Two scroll drivers coexist without fighting:
// the active line tracks the playback clock, and a line selected from the
// fact-check list scrolls into view on demand. Memoized so a caption-only update
// of the parent panel (every interim word) does not re-render the list.
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
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto pr-2"
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
            className={`border-l-2 pl-3 transition-colors ${
              active
                ? "border-sky-400 dark:border-sky-500/70"
                : "border-transparent"
            } ${selected ? "bg-sky-50/70 dark:bg-sky-500/10" : ""}`}
          >
            <button
              type="button"
              onClick={() => store.seekTo(statement.start)}
              className="flex w-full flex-col gap-0.5 rounded-md py-1 pr-1 text-left hover:bg-zinc-900/5 focus-visible:outline-2 focus-visible:outline-sky-500 dark:hover:bg-white/5"
            >
              <span className="flex items-baseline gap-2 text-[11px]">
                {statement.speaker ? (
                  <span className="font-semibold uppercase tracking-wide text-zinc-600 dark:text-zinc-300">
                    {speakerLabel(statement.speaker)}
                  </span>
                ) : null}
                <span
                  className={`font-mono tabular-nums ${
                    active
                      ? "text-sky-700 dark:text-sky-300"
                      : "text-zinc-400 dark:text-zinc-500"
                  }`}
                >
                  {formatTime(statement.start)} – {formatTime(statement.end)}
                </span>
              </span>
              <span className="text-sm leading-6 text-zinc-800 dark:text-zinc-200">
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
// wire unchecked. The fallback is a tail clause so the caller's "Not checked - "
// prefix never reads as "Not checked - Not checked".
function skipLabel(reason: string): string {
  return SKIP_LABELS[reason as SkipReason] ?? "an unrecognised reason";
}

// formatConfidence renders a corroboration score (a fraction in [0, 1]) as a
// whole-number percentage, the form the operator reads.
function formatConfidence(score: number): string {
  return `${Math.round(score * 100)}%`;
}

// SubtitleStatus is the light per-row marker. It never shows a verdict (those
// live in the fact-check list); it signals progress, why a statement produced no
// fact-check, or - for a checked statement with evidence - how strongly the
// reference corpus corroborates it, so a row is never silently empty after
// analysis.
function SubtitleStatus({ statement }: { statement: LiveStatement }) {
  if (statement.status === "analysing") {
    return (
      <p
        role="status"
        className="flex items-center gap-2 pb-1 text-xs text-zinc-500 dark:text-zinc-400"
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
      <p className="pb-1 text-xs text-amber-700 dark:text-amber-400">
        This statement could not be checked.
      </p>
    );
  }

  if (statement.skipReason) {
    return (
      <p className="pb-1 text-xs italic text-zinc-400 dark:text-zinc-500">
        Not checked - {skipLabel(statement.skipReason)}.
      </p>
    );
  }

  if (statement.matches.length === 0) {
    return (
      <p className="pb-1 text-xs text-zinc-500 dark:text-zinc-400">
        No confident match.
      </p>
    );
  }

  // A scored statement with evidence shows its corroboration percentage: how
  // strongly the matched cluster supports the statement, not a per-source verdict.
  if (isScored(statement) && statement.confidence) {
    return (
      <p className="pb-1 text-xs text-zinc-500 dark:text-zinc-400">
        <span className="font-semibold tabular-nums text-zinc-700 dark:text-zinc-200">
          {formatConfidence(statement.confidence.score)}
        </span>{" "}
        corroborated by the reference corpus
      </p>
    );
  }

  return null;
}
