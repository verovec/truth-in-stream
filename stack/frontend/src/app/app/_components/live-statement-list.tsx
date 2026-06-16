"use client";

import { memo, useCallback, useEffect, useRef } from "react";
import {
  usePlayback,
  usePlaybackStore,
} from "@/components/playback/playback-provider";
import type { SkipReason } from "@/lib/fact-check/api";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import type { LiveClaim } from "@/lib/live/claims";
import { isScored, type LiveStatement } from "@/lib/live/statements";
import { formatTime } from "@/lib/playback/format-time";
import { LiveClaimList } from "./live-claim-list";

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
  claimsFor,
}: {
  statements: LiveStatement[];
  selectedStatementId: string | null;
  // Bumped by the parent on every fact-check selection so re-selecting the same
  // entry scrolls its origin back into view even when the id is unchanged.
  selectionTick?: number;
  // Returns a statement's atomic claims (retrieve-then-verify path), empty on a
  // legacy stream that emits no claim frames so the row renders exactly as
  // before when no claims arrive.
  claimsFor?: (statementId: string) => LiveClaim[];
}) {
  const store = usePlaybackStore();
  const activeIndex = usePlayback((snapshot) =>
    findActiveSegmentIndex(statements, snapshot.currentTime),
  );
  const listRef = useRef<HTMLOListElement>(null);
  const itemRefs = useRef(new Map<string, HTMLLIElement>());

  // scrollStatementIntoView reveals a statement by id, scrolling only the
  // subtitle list - never the page. The native Element.scrollIntoView walks every
  // scrollable ancestor up to the document, so a new line arriving would yank the
  // whole page to align the subtitle to the viewport; we instead adjust this
  // list's own scrollTop by the row's offset within it. A no-op when the row or
  // list is not mounted (e.g. cleared after a reset). block defaults to "nearest"
  // (scroll only when off-screen, by the minimum) for the playback-active line,
  // the selected line, and the inconsistency jump-to-earlier; the newest-statement
  // auto-reveal passes "start" to pull the new line to the top.
  const scrollStatementIntoView = useCallback(
    (id: string, block: ScrollLogicalPosition = "nearest") => {
      const list = listRef.current;
      const row = itemRefs.current.get(id);
      if (!list || !row) {
        return;
      }
      const rowRect = row.getBoundingClientRect();
      const listRect = list.getBoundingClientRect();
      let delta: number;
      if (block === "start") {
        delta = rowRect.top - listRect.top;
      } else if (block === "end") {
        delta = rowRect.bottom - listRect.bottom;
      } else if (rowRect.top < listRect.top) {
        delta = rowRect.top - listRect.top;
      } else if (rowRect.bottom > listRect.bottom) {
        delta = rowRect.bottom - listRect.bottom;
      } else {
        // Already fully visible within the list: "nearest" means no movement.
        return;
      }
      if (delta === 0) {
        return;
      }
      list.scrollTo({ top: list.scrollTop + delta, behavior: "smooth" });
    },
    [],
  );

  // The active line tracks the playback clock.
  const activeId = activeIndex >= 0 ? statements[activeIndex]?.id : undefined;
  useEffect(() => {
    if (activeId === undefined) {
      return;
    }
    scrollStatementIntoView(activeId);
  }, [activeId, scrollStatementIntoView]);

  // A line selected from the fact-check list scrolls into view on demand.
  useEffect(() => {
    if (selectedStatementId === null) {
      return;
    }
    scrollStatementIntoView(selectedStatementId);
  }, [selectedStatementId, selectionTick, scrollStatementIntoView]);

  // The newest statement renders at the top; reveal it there as it arrives so a
  // new line surfaces without the operator scrolling. Keyed on the newest id so
  // it fires only when a statement is appended, not on every active-segment or
  // selection change.
  const newestId = statements.at(-1)?.id;
  useEffect(() => {
    if (newestId === undefined) {
      return;
    }
    scrollStatementIntoView(newestId, "start");
  }, [newestId, scrollStatementIntoView]);

  return (
    <ol
      ref={listRef}
      aria-label="Subtitle transcript"
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto pr-2"
    >
      {/* index stays the chronological position so active-segment tracking and
          selection keep matching; .reverse() then renders newest-first (top)
          without disturbing those order-dependent computations. */}
      {statements
        .map((statement, index) => {
          const active = index === activeIndex;
          const selected = statement.id === selectedStatementId;
          const claims = claimsFor?.(statement.id) ?? [];
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
              {/* On the retrieve-then-verify path the unit fans into atomic
                  claims that each carry their own verdict; the per-statement
                  status marker is then redundant, so it yields to the claim
                  list. A legacy stream emits no claims and renders the marker as
                  before. */}
              {claims.length > 0 ? (
                <LiveClaimList claims={claims} />
              ) : (
                <SubtitleStatus statement={statement} />
              )}
              {statement.inconsistency ? (
                <InconsistencyFlag
                  inconsistency={statement.inconsistency}
                  onJumpToEarlier={scrollStatementIntoView}
                />
              ) : null}
            </li>
          );
        })
        .reverse()}
    </ol>
  );
});

// InconsistencyFlag is the inline marker that a statement contradicts an earlier
// one by the same speaker. It quotes the earlier statement so the viewer can see
// the conflict, plus the stance check's rationale when present. It is additive
// to the fact-check status: a statement can be both corroborated and internally
// inconsistent with the speaker's own earlier words.
function InconsistencyFlag({
  inconsistency,
  onJumpToEarlier,
}: {
  inconsistency: NonNullable<LiveStatement["inconsistency"]>;
  onJumpToEarlier: (id: string) => void;
}) {
  return (
    <p className="pb-1 text-xs text-rose-700 dark:text-rose-400">
      <span className="font-semibold">Contradicts an earlier statement</span> by
      this speaker:{" "}
      <button
        type="button"
        onClick={() => onJumpToEarlier(inconsistency.earlierId)}
        className="italic underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:outline-2 focus-visible:outline-rose-500"
      >
        “{inconsistency.earlierText}”
      </button>
      {inconsistency.rationale ? ` — ${inconsistency.rationale}` : null}
    </p>
  );
}

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

// formatEvidenceCount names how many matches carried stance-bearing weight into
// the score, pluralised so a lone match reads "1 match".
function formatEvidenceCount(items: number): string {
  return `${items} ${items === 1 ? "match" : "matches"}`;
}

// formatWeight renders an aggregated corroboration weight to two decimals: the
// raw supporting/contradicting magnitudes the percentage is derived from, shown
// so the score is explainable rather than opaque.
function formatWeight(weight: number): string {
  return weight.toFixed(2);
}

// ConfidenceBreakdown is the compact, explainable companion to the percentage:
// how many matches contributed and the supporting-versus-contradicting weights
// the score divides. It makes "how close are we to the corpus" legible instead
// of leaving the percentage opaque.
function ConfidenceBreakdown({
  supporting,
  contradicting,
  evidenceItems,
}: {
  supporting: number;
  contradicting: number;
  evidenceItems: number;
}) {
  return (
    <p className="pb-1 text-[11px] text-zinc-400 dark:text-zinc-500">
      <span className="tabular-nums">
        {formatEvidenceCount(evidenceItems)}
      </span>
      {" · "}
      <span className="tabular-nums text-emerald-700 dark:text-emerald-400">
        {formatWeight(supporting)} supporting
      </span>
      {" · "}
      <span className="tabular-nums text-rose-700 dark:text-rose-400">
        {formatWeight(contradicting)} contradicting
      </span>
    </p>
  );
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

  // A scored statement with evidence shows its corroboration percentage and the
  // supporting/contradicting breakdown that produced it: how strongly the matched
  // cluster supports the statement, not a per-source verdict.
  if (isScored(statement) && statement.confidence) {
    const { score, supporting, contradicting, evidenceItems } =
      statement.confidence;
    return (
      <>
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          <span className="font-semibold tabular-nums text-zinc-700 dark:text-zinc-200">
            {formatConfidence(score)}
          </span>{" "}
          corroborated by the reference corpus
        </p>
        <ConfidenceBreakdown
          supporting={supporting}
          contradicting={contradicting}
          evidenceItems={evidenceItems}
        />
      </>
    );
  }

  return null;
}
