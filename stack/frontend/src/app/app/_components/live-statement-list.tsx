"use client";

import { memo, useCallback, useEffect, useRef } from "react";
import { usePlayback } from "@/components/playback/playback-provider";
import type { SkipReason } from "@/lib/fact-check/api";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import { formatTemplate, plural } from "@/lib/i18n/text";
import type { LiveClaim } from "@/lib/live/claims";
import {
  type ClaimHighlight,
  segmentTextParts,
} from "@/lib/live/highlight";
import { isScored, type LiveStatement } from "@/lib/live/statements";
import { formatTime } from "@/lib/playback/format-time";
import { useAppI18n, type AppDictionary } from "@/components/i18n/app-i18n";
import { LiveClaimList } from "./live-claim-list";

// PIN_THRESHOLD_PX is how close to the top the list must be scrolled to count as
// pinned. A few pixels of slack absorbs sub-pixel scroll positions and the
// momentum overscroll a touchpad leaves behind, so a list resting at the top
// stays pinned rather than flickering unpinned on a stray fractional scrollTop.
const PIN_THRESHOLD_PX = 8;

// LiveStatementList is the subtitle region: the running transcript of finalised
// statements, rendered as one continuous, borderless flow (not boxed cards) so
// it reads like a transcript. Each line carries its speaker label (when
// diarized) and timestamp above the spoken text, plus a light status marker
// (analysing, could-not-check, skipped, or no-match); verdicts live in the
// decoupled fact-check list below. Two scroll drivers coexist without fighting:
// the active line tracks the playback clock, and a line selected from the
// fact-check list scrolls into view on demand. Clicking a line selects it for
// inspection - it never seeks, because a seek restarts the live session and would
// wipe the running speaker credibility and in-flight findings. Memoized so a
// caption-only update of the parent panel (every interim word) does not re-render
// the list.
export const LiveStatementList = memo(function LiveStatementList({
  statements,
  selectedStatementId,
  selectionTick = 0,
  claimsFor,
  highlightsFor,
  onSelect,
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
  // Returns the claim word-ranges anchored inside a statement's text so the
  // exact words that were checked render marked, tinted by the claim's live
  // verdict. Optional and empty on a legacy stream, leaving the text plain.
  highlightsFor?: (segmentId: string) => readonly ClaimHighlight[];
  // Lifts a clicked statement's id to the parent so the line and its fact-check
  // entry highlight together. Optional so the list still renders standalone.
  onSelect?: (statementId: string) => void;
}) {
  const { t } = useAppI18n();
  const activeIndex = usePlayback((snapshot) =>
    findActiveSegmentIndex(statements, snapshot.currentTime),
  );
  const listRef = useRef<HTMLOListElement>(null);
  const itemRefs = useRef(new Map<string, HTMLLIElement>());
  // pinned tracks whether the list is anchored to the top (the newest line).
  // True until the operator scrolls away from the top; scrolling back to the top
  // re-pins. While pinned, a newly arrived statement keeps the top in view; once
  // unpinned, new statements never move the operator's scroll position. A ref,
  // not state, because only the scroll effects read it and it must never trigger
  // a re-render.
  const pinnedRef = useRef(true);

  // scrollStatementIntoView reveals a statement by id on demand (a selected
  // fact-check, an inconsistency jump-to-earlier), scrolling only the subtitle
  // list - never the page. The native Element.scrollIntoView walks every
  // scrollable ancestor up to the document, so it would yank the whole page to
  // align the subtitle to the viewport; we instead adjust this list's own
  // scrollTop by the row's offset within it. It moves by the minimum: only when
  // the row sits off the top or bottom edge, and only far enough to bring it
  // back, so a row already visible never jumps. A no-op when the row or list is
  // not mounted (e.g. cleared after a reset).
  const scrollStatementIntoView = useCallback((id: string) => {
    const list = listRef.current;
    const row = itemRefs.current.get(id);
    if (!list || !row) {
      return;
    }
    const rowRect = row.getBoundingClientRect();
    const listRect = list.getBoundingClientRect();
    let delta: number;
    if (rowRect.top < listRect.top) {
      delta = rowRect.top - listRect.top;
    } else if (rowRect.bottom > listRect.bottom) {
      delta = rowRect.bottom - listRect.bottom;
    } else {
      // Already fully visible within the list: no movement.
      return;
    }
    if (delta === 0) {
      return;
    }
    list.scrollTo({ top: list.scrollTop + delta, behavior: "smooth" });
  }, []);

  // onScroll re-evaluates the pin on every scroll: the list is pinned only while
  // resting at the top. The operator scrolling away (in either direction) drops
  // the pin so new lines stop moving the view; scrolling back to the top restores
  // it. A programmatic pin-to-top scroll lands at 0, so it re-confirms the pin
  // rather than fighting it.
  const handleScroll = useCallback(() => {
    const list = listRef.current;
    if (!list) {
      return;
    }
    pinnedRef.current = list.scrollTop <= PIN_THRESHOLD_PX;
  }, []);

  // A line selected from the fact-check list scrolls into view on demand. This is
  // operator-initiated, so it is honoured regardless of the pin (and naturally
  // drops the pin by scrolling the selected line away from the top).
  useEffect(() => {
    if (selectedStatementId === null) {
      return;
    }
    scrollStatementIntoView(selectedStatementId);
  }, [selectedStatementId, selectionTick, scrollStatementIntoView]);

  // The newest statement renders at the top. While pinned, snap back to the top
  // as a line arrives so the newest stays in view; while the operator has
  // scrolled away, leave their position untouched so a new line never yanks the
  // view down-then-back. The snap is instant (no smooth behaviour) so a rapid
  // burst of statements never animates. Keyed on the newest id so it fires only
  // when a statement is appended, not on every active-segment or selection change.
  const newestId = statements.at(-1)?.id;
  useEffect(() => {
    if (newestId === undefined || !pinnedRef.current) {
      return;
    }
    listRef.current?.scrollTo({ top: 0 });
  }, [newestId]);

  return (
    <ol
      ref={listRef}
      onScroll={handleScroll}
      aria-label={t.subtitles.transcriptAria}
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pr-2"
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
                  ? "border-bleu-flag dark:border-sky-400/70"
                  : "border-transparent"
              } ${selected ? "bg-bleu-flag/5 dark:bg-sky-400/10" : ""}`}
            >
              <button
                type="button"
                onClick={() => onSelect?.(statement.id)}
                className="flex w-full flex-col gap-0.5 rounded-md py-1 pr-1 text-left hover:bg-ink/5 focus-visible:outline-2 focus-visible:outline-bleu-flag dark:hover:bg-white/5 dark:focus-visible:outline-paper/60"
              >
                <span className="flex items-baseline gap-2 text-[11px]">
                  {statement.speaker ? (
                    <span className="font-semibold uppercase tracking-wide text-ink/70 dark:text-paper/70">
                      {`${t.subtitles.speaker} ${statement.speaker}`}
                    </span>
                  ) : null}
                  <span
                    className={`tabular-nums ${
                      active
                        ? "text-bleu dark:text-sky-300"
                        : "text-ink/40 dark:text-paper/40"
                    }`}
                  >
                    {formatTime(statement.start)} – {formatTime(statement.end)}
                  </span>
                </span>
                <span className="text-[0.9375rem] leading-6 break-words text-ink dark:text-paper">
                  <HighlightedStatementText
                    text={statement.text}
                    highlights={highlightsFor?.(statement.id) ?? []}
                  />
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

// HIGHLIGHT_CLASSES maps a highlight's lifecycle to its mark tint: a soft
// neutral wash while the claim is pending/checking, the verdict tint once
// verified. An unchecked or errored claim renders no mark - a highlight asserts
// "these words were checked", which those terminals cannot honestly claim.
const HIGHLIGHT_VERDICT_CLASSES: Record<string, string> = {
  credible:
    "bg-verdict-credible/15 underline decoration-2 underline-offset-2 decoration-verdict-credible/60",
  disputed:
    "bg-verdict-disputed/15 underline decoration-2 underline-offset-2 decoration-verdict-disputed/60",
  unverifiable:
    "bg-verdict-unverifiable/15 underline decoration-2 underline-offset-2 decoration-verdict-unverifiable/60",
};

// highlightClass resolves one highlight's mark styling from its claim's live
// state, or null when the range must render plain (a shed or failed claim, or a
// verified frame that carried no verdict).
function highlightClass(highlight: ClaimHighlight): string | null {
  if (highlight.status === "pending" || highlight.status === "checking") {
    return "bg-ink/8 dark:bg-paper/10";
  }
  if (highlight.status === "verified" && highlight.verdict) {
    return HIGHLIGHT_VERDICT_CLASSES[highlight.verdict] ?? null;
  }
  return null;
}

// HighlightedStatementText renders a statement's text with the exact words each
// atomic claim was extracted from marked and tinted by that claim's live
// verdict, so the viewer sees precisely which words were checked and how they
// fared. With no anchored highlight the text renders as one plain string,
// byte-for-byte the legacy row.
function HighlightedStatementText({
  text,
  highlights,
}: {
  text: string;
  highlights: readonly ClaimHighlight[];
}) {
  if (highlights.length === 0) {
    return text;
  }
  const parts = segmentTextParts(text, highlights);
  return (
    <>
      {parts.map((part, index) => {
        const className = part.highlight ? highlightClass(part.highlight) : null;
        if (!part.highlight || className === null) {
          // A React key on the index is safe here: parts are derived positional
          // slices, never reordered independently of the text.
          return <span key={index}>{part.text}</span>;
        }
        return (
          <mark
            key={index}
            data-claim-id={part.highlight.claimId}
            className={`rounded-[3px] box-decoration-clone px-0.5 text-inherit ${className}`}
          >
            {part.text}
          </mark>
        );
      })}
    </>
  );
}

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
  const { t } = useAppI18n();
  return (
    <p className="pb-1 text-xs text-verdict-disputed">
      <span className="font-semibold">{t.subtitles.contradictsEarlier}</span>{" "}
      {t.subtitles.bySpeaker}{" "}
      <button
        type="button"
        onClick={() => onJumpToEarlier(inconsistency.earlierId)}
        className="italic underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:outline-2 focus-visible:outline-rouge"
      >
        “{inconsistency.earlierText}”
      </button>
      {inconsistency.rationale ? ` — ${inconsistency.rationale}` : null}
    </p>
  );
}

// SKIP_KEYS maps the wire's skip-reason vocabulary (which must not change) to
// the dictionary's keys, so each reason renders in the active locale.
const SKIP_KEYS: Record<
  SkipReason,
  keyof AppDictionary["subtitles"]["skipReasons"]
> = {
  not_a_claim: "notAClaim",
  not_covered: "notCovered",
  not_checked: "notChecked",
};

// skipLabel tolerates a skip reason the backend may add before the frontend
// knows it. The parameter is widened to string on purpose: the value crosses the
// wire unchecked. The fallback is a tail clause so the caller's "Not checked - "
// prefix never reads as "Not checked - Not checked".
function skipLabel(
  reason: string,
  labels: AppDictionary["subtitles"]["skipReasons"],
): string {
  // Object.hasOwn (not a bare index) so a wire reason that collides with an
  // Object.prototype member ("constructor", "toString") resolves to the
  // unknown-reason fallback instead of an inherited function.
  return Object.hasOwn(SKIP_KEYS, reason)
    ? labels[SKIP_KEYS[reason as SkipReason]]
    : labels.unknown;
}

// formatConfidence renders a corroboration score (a fraction in [0, 1]) as a
// whole-number percentage, the form the operator reads.
function formatConfidence(score: number): string {
  return `${Math.round(score * 100)}%`;
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
  const { locale, t } = useAppI18n();
  return (
    <p className="pb-1 text-[11px] text-ink/40 dark:text-paper/40">
      <span className="tabular-nums">
        {`${evidenceItems} ${plural(locale, evidenceItems, t.subtitles.match)}`}
      </span>
      {" · "}
      <span className="tabular-nums text-verdict-credible">
        {formatWeight(supporting)} {t.subtitles.supporting}
      </span>
      {" · "}
      <span className="tabular-nums text-verdict-disputed">
        {formatWeight(contradicting)} {t.subtitles.contradicting}
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
  const { t } = useAppI18n();
  if (statement.status === "analysing") {
    return (
      <p
        role="status"
        className="flex items-center gap-2 pb-1 text-xs text-ink/50 dark:text-paper/50"
      >
        <span
          aria-hidden="true"
          className="size-1.5 animate-pulse rounded-full bg-bleu-flag dark:bg-sky-400"
        />
        {t.subtitles.checking}
      </p>
    );
  }

  if (statement.error) {
    return (
      <p className="pb-1 text-xs text-verdict-flag dark:text-amber-300">
        {t.subtitles.checkFailed}
      </p>
    );
  }

  if (statement.skipReason) {
    return (
      <p className="pb-1 text-xs italic text-ink/40 dark:text-paper/40">
        {formatTemplate(t.subtitles.notChecked, {
          reason: skipLabel(statement.skipReason, t.subtitles.skipReasons),
        })}
      </p>
    );
  }

  if (statement.matches.length === 0) {
    return (
      <p className="pb-1 text-xs text-ink/50 dark:text-paper/50">
        {t.subtitles.noMatch}
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
        <p className="text-xs text-ink/50 dark:text-paper/50">
          <span className="font-semibold tabular-nums text-ink/80 dark:text-paper/80">
            {formatConfidence(score)}
          </span>{" "}
          {t.subtitles.corroborated}
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
