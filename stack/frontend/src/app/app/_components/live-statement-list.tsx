"use client";

import { memo, useCallback, useEffect, useMemo, useRef } from "react";
import { usePlayback } from "@/components/playback/playback-provider";
import { useTranscriptDisplay } from "@/components/live/transcript-display";
import { findActiveSegmentIndex } from "@/lib/fact-check/segments";
import {
  type ClaimHighlight,
  segmentTextParts,
} from "@/lib/live/highlight";
import type { DisplayStatement } from "@/lib/live/merge";
import type { Inconsistency } from "@/lib/live/statements";
import {
  groupSpeakerTurns,
  type SpeakerTurn,
  turnSentences,
  type TurnSentence,
} from "@/lib/live/turns";
import { formatTime } from "@/lib/playback/format-time";
import { useAppI18n } from "@/components/i18n/app-i18n";

// PIN_THRESHOLD_PX is how close to the bottom the list must be scrolled to
// count as pinned. A few pixels of slack absorbs sub-pixel scroll positions and
// the momentum overscroll a touchpad leaves behind, so a list resting at the
// bottom stays pinned rather than flickering unpinned on a stray fractional
// scrollTop.
const PIN_THRESHOLD_PX = 8;

// LiveStatementList is the subtitle region: the running transcript of finalised
// statements, rendered as flowing text - one paragraph per speaker turn, each
// finalised sentence inline - so it reads like a written transcript rather than
// stacked blocks. The whole transcript is always shown; the only marks inside
// the text are the exact words of claims whose verdict corroborates or
// contradicts (plus unverifiable ones while the strip's unverified toggle is
// on). Verdict detail lives in the decoupled fact-check list below. Newest text
// is appended at the bottom in reading order; the list stays pinned to the
// bottom until the operator scrolls away. Clicking a sentence selects its
// statement for inspection - it never seeks, because a seek restarts the live
// session and would wipe the running speaker credibility and in-flight
// findings. Memoized so a caption-only update of the parent panel (every
// interim word) does not re-render the list.
export const LiveStatementList = memo(function LiveStatementList({
  statements,
  selectedStatementId,
  selectionTick = 0,
  highlightsFor,
  onSelect,
}: {
  statements: DisplayStatement[];
  selectedStatementId: string | null;
  // Bumped by the parent on every fact-check selection so re-selecting the same
  // entry scrolls its origin back into view even when the id is unchanged.
  selectionTick?: number;
  // Returns the claim word-ranges anchored inside a sentence's text so the
  // exact words that were checked render marked, tinted by the claim's live
  // verdict. Optional and empty on a legacy stream, leaving the text plain.
  highlightsFor?: (segmentId: string) => readonly ClaimHighlight[];
  // Lifts a clicked sentence's statement id to the parent so the sentence and
  // its fact-check entry highlight together. Optional so the list still renders
  // standalone.
  onSelect?: (statementId: string) => void;
}) {
  const { t } = useAppI18n();
  const { showUnverified } = useTranscriptDisplay();
  const turns = useMemo(() => groupSpeakerTurns(statements), [statements]);
  // The flat chronological sentence list active-position tracking searches
  // over: each sentence carries its own time range, so the tracker points at
  // one sentence even inside a merged multi-statement unit.
  const sentences = useMemo(() => turnSentences(turns), [turns]);
  const activeSentenceId = usePlayback((snapshot) => {
    const index = findActiveSegmentIndex(sentences, snapshot.currentTime);
    return index === -1 ? null : sentences[index].id;
  });
  const listRef = useRef<HTMLOListElement>(null);
  const sentenceRefs = useRef(new Map<string, HTMLElement>());
  // A selection carries a statement id (the id fact-check entries key on); the
  // scroll target is that statement's first sentence. A sentence id used
  // directly (the inconsistency jump-to-earlier) resolves through the refs
  // without this map.
  const firstSentenceOf = useMemo(() => {
    const byStatement = new Map<string, string>();
    for (const sentence of sentences) {
      if (!byStatement.has(sentence.statementId)) {
        byStatement.set(sentence.statementId, sentence.id);
      }
    }
    return byStatement;
  }, [sentences]);
  // pinned tracks whether the list is anchored to the bottom (the newest text).
  // True until the operator scrolls away from the bottom; scrolling back down
  // re-pins. While pinned, newly arrived text keeps the bottom in view; once
  // unpinned, new text never moves the operator's scroll position. A ref, not
  // state, because only the scroll effects read it and it must never trigger a
  // re-render.
  const pinnedRef = useRef(true);

  // registerSentence is stable (the refs map lives in a ref) so a sentence's
  // memoized ref callback never changes identity and React does not detach and
  // reattach every mounted sentence's ref on each list re-render - the
  // transcript grows unbounded over a live session, so that churn would be
  // O(transcript) per update.
  const registerSentence = useCallback((id: string, el: HTMLElement | null) => {
    const refs = sentenceRefs.current;
    if (el) {
      refs.set(id, el);
    } else {
      refs.delete(id);
    }
  }, []);

  // scrollSentenceIntoView reveals a sentence on demand (a selected fact-check,
  // an inconsistency jump-to-earlier), scrolling only the subtitle list - never
  // the page. The native Element.scrollIntoView walks every scrollable ancestor
  // up to the document, so it would yank the whole page to align the subtitle
  // to the viewport; we instead adjust this list's own scrollTop by the
  // sentence's offset within it. It moves by the minimum: only when the
  // sentence sits off the top or bottom edge, and only far enough to bring it
  // back, so a sentence already visible never jumps. A no-op when the sentence
  // or list is not mounted (e.g. cleared after a reset).
  const scrollSentenceIntoView = useCallback(
    (id: string) => {
      const list = listRef.current;
      const target =
        sentenceRefs.current.get(id) ??
        sentenceRefs.current.get(firstSentenceOf.get(id) ?? "");
      if (!list || !target) {
        return;
      }
      const targetRect = target.getBoundingClientRect();
      const listRect = list.getBoundingClientRect();
      let delta: number;
      if (targetRect.top < listRect.top) {
        delta = targetRect.top - listRect.top;
      } else if (targetRect.bottom > listRect.bottom) {
        delta = targetRect.bottom - listRect.bottom;
      } else {
        // Already fully visible within the list: no movement.
        return;
      }
      if (delta === 0) {
        return;
      }
      list.scrollTo({ top: list.scrollTop + delta, behavior: "smooth" });
    },
    [firstSentenceOf],
  );

  // onScroll re-evaluates the pin on every scroll: the list is pinned only
  // while resting at the bottom. The operator scrolling away drops the pin so
  // new text stops moving the view; scrolling back to the bottom restores it. A
  // programmatic pin-to-bottom scroll lands within the threshold, so it
  // re-confirms the pin rather than fighting it.
  const handleScroll = useCallback(() => {
    const list = listRef.current;
    if (!list) {
      return;
    }
    pinnedRef.current =
      list.scrollHeight - list.scrollTop - list.clientHeight <=
      PIN_THRESHOLD_PX;
  }, []);

  // A sentence selected from the fact-check list scrolls into view on demand.
  // This is operator-initiated, so it is honoured regardless of the pin (and
  // naturally drops the pin by scrolling the selection away from the bottom).
  useEffect(() => {
    if (selectedStatementId === null) {
      return;
    }
    scrollSentenceIntoView(selectedStatementId);
  }, [selectedStatementId, selectionTick, scrollSentenceIntoView]);

  // The newest text renders at the bottom. While pinned, snap back down as text
  // arrives so the newest stays in view; while the operator has scrolled away,
  // leave their position untouched. The snap is instant (no smooth behaviour)
  // so a rapid burst of statements never animates. Keyed on the newest sentence
  // id so it fires only when text is appended, not on every active-position or
  // selection change.
  const newestId = sentences.at(-1)?.id;
  useEffect(() => {
    const list = listRef.current;
    if (newestId === undefined || !pinnedRef.current || !list) {
      return;
    }
    list.scrollTo({ top: list.scrollHeight });
  }, [newestId]);

  return (
    <ol
      ref={listRef}
      onScroll={handleScroll}
      aria-label={t.subtitles.transcriptAria}
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-2"
    >
      {turns.map((turn) => (
        <TurnParagraph
          key={turn.id}
          turn={turn}
          activeSentenceId={activeSentenceId}
          selectedStatementId={selectedStatementId}
          showUnverified={showUnverified}
          highlightsFor={highlightsFor}
          onSelect={onSelect}
          onJumpToEarlier={scrollSentenceIntoView}
          registerSentence={registerSentence}
        />
      ))}
    </ol>
  );
});

// TurnParagraph is one speaker turn: the speaker label and time range above one
// flowing paragraph of inline sentences, followed by the inconsistency flags of
// any statement in the turn. The paragraph is the only block-level structure
// left in the transcript - inside it the text flows exactly like written prose.
function TurnParagraph({
  turn,
  activeSentenceId,
  selectedStatementId,
  showUnverified,
  highlightsFor,
  onSelect,
  onJumpToEarlier,
  registerSentence,
}: {
  turn: SpeakerTurn;
  activeSentenceId: string | null;
  selectedStatementId: string | null;
  showUnverified: boolean;
  highlightsFor?: (segmentId: string) => readonly ClaimHighlight[];
  onSelect?: (statementId: string) => void;
  onJumpToEarlier: (id: string) => void;
  registerSentence: (id: string, el: HTMLElement | null) => void;
}) {
  const { t } = useAppI18n();
  const active = turn.sentences.some(
    (sentence) => sentence.id === activeSentenceId,
  );
  const flagged = turn.sentences.flatMap((sentence) =>
    sentence.inconsistency !== undefined
      ? [{ id: sentence.id, inconsistency: sentence.inconsistency }]
      : [],
  );
  return (
    <li className="flex flex-col gap-0.5">
      <span className="flex items-baseline gap-2 text-[11px]">
        {turn.speaker ? (
          <span className="font-semibold uppercase tracking-wide text-ink/70 dark:text-paper/70">
            {`${t.subtitles.speaker} ${turn.speaker}`}
          </span>
        ) : null}
        <span
          className={`tabular-nums ${
            active
              ? "text-bleu dark:text-sky-300"
              : "text-ink/40 dark:text-paper/40"
          }`}
        >
          {formatTime(turn.start)} – {formatTime(turn.end)}
        </span>
      </span>
      <p className="text-[0.9375rem] leading-6 break-words text-ink dark:text-paper">
        {turn.sentences.map((sentence, index) => (
          <span key={sentence.id}>
            {index > 0 ? " " : null}
            <SentenceSpan
              sentence={sentence}
              active={sentence.id === activeSentenceId}
              selected={sentence.statementId === selectedStatementId}
              highlights={highlightsFor?.(sentence.id) ?? []}
              showUnverified={showUnverified}
              onSelect={onSelect}
              registerSentence={registerSentence}
            />
          </span>
        ))}
      </p>
      {flagged.map((flag) => (
        <InconsistencyFlag
          key={flag.id}
          inconsistency={flag.inconsistency}
          onJumpToEarlier={onJumpToEarlier}
        />
      ))}
    </li>
  );
}

// SentenceSpan is one inline sentence: a click target that selects its
// statement for inspection, carrying the playback-position and selection
// washes, with the checked claim words marked inside it. It renders inline so
// consecutive sentences flow as one text.
function SentenceSpan({
  sentence,
  active,
  selected,
  highlights,
  showUnverified,
  onSelect,
  registerSentence,
}: {
  sentence: TurnSentence;
  active: boolean;
  selected: boolean;
  highlights: readonly ClaimHighlight[];
  showUnverified: boolean;
  onSelect?: (statementId: string) => void;
  registerSentence: (id: string, el: HTMLElement | null) => void;
}) {
  // Memoized on the stable registerSentence and the sentence id so the ref
  // callback keeps its identity across re-renders: React then leaves the
  // attached ref alone instead of detaching and reattaching it.
  const ref = useCallback(
    (el: HTMLElement | null) => registerSentence(sentence.id, el),
    [registerSentence, sentence.id],
  );
  return (
    <button
      type="button"
      ref={ref}
      onClick={() => onSelect?.(sentence.statementId)}
      aria-current={active ? "true" : undefined}
      className={`inline rounded-sm text-left align-baseline box-decoration-clone transition-colors hover:bg-ink/5 focus-visible:outline-2 focus-visible:outline-bleu-flag dark:hover:bg-white/5 dark:focus-visible:outline-paper/60 ${
        active ? "bg-bleu-flag/10 dark:bg-sky-400/15" : ""
      } ${selected ? "bg-bleu-flag/5 ring-1 ring-bleu-flag/40 dark:bg-sky-400/10 dark:ring-sky-400/40" : ""}`}
    >
      <HighlightedSentenceText
        text={sentence.text}
        highlights={highlights}
        showUnverified={showUnverified}
      />
    </button>
  );
}

// HIGHLIGHT_VERDICT_CLASSES maps a resolved verdict to its mark tint. Only
// credible (corroborated, green) and disputed (contradicted, red) mark by
// default; an unverifiable claim renders its muted mark only while the strip's
// unverified toggle is on, so the default transcript highlights exactly the
// claims a viewer can act on. Pending, checking, unchecked, and errored claims
// render plain - a mark asserts "these words were checked and resolved", which
// those states cannot honestly claim.
const HIGHLIGHT_VERDICT_CLASSES: Record<string, string> = {
  credible:
    "bg-verdict-credible/15 underline decoration-2 underline-offset-2 decoration-verdict-credible/60",
  disputed:
    "bg-verdict-disputed/15 underline decoration-2 underline-offset-2 decoration-verdict-disputed/60",
  unverifiable:
    "bg-verdict-unverifiable/15 underline decoration-2 underline-offset-2 decoration-verdict-unverifiable/50",
};

// highlightClass resolves one highlight's mark styling from its claim's live
// state and the unverified display toggle, or null when the range must render
// plain (an unresolved or shed claim, a verified frame that carried no verdict,
// or an unverifiable verdict while the toggle is off).
function highlightClass(
  highlight: ClaimHighlight,
  showUnverified: boolean,
): string | null {
  if (highlight.status !== "verified" || !highlight.verdict) {
    return null;
  }
  if (highlight.verdict === "unverifiable" && !showUnverified) {
    return null;
  }
  return HIGHLIGHT_VERDICT_CLASSES[highlight.verdict] ?? null;
}

// HighlightedSentenceText renders a sentence's text with the exact words each
// resolved claim was extracted from marked and tinted by that claim's verdict,
// so the viewer sees precisely which words were checked and how they fared.
// With no visible highlight the text renders as one plain string.
function HighlightedSentenceText({
  text,
  highlights,
  showUnverified,
}: {
  text: string;
  highlights: readonly ClaimHighlight[];
  showUnverified: boolean;
}) {
  if (highlights.length === 0) {
    return text;
  }
  const parts = segmentTextParts(text, highlights);
  return (
    <>
      {parts.map((part, index) => {
        const className = part.highlight
          ? highlightClass(part.highlight, showUnverified)
          : null;
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

// InconsistencyFlag is the compact marker that a statement contradicts an
// earlier one by the same speaker, rendered under the turn that contains the
// offending sentence. It quotes the earlier statement so the viewer can see the
// conflict, plus the stance check's rationale when present. It is additive to
// the fact-check verdicts: a statement can be corroborated yet internally
// inconsistent with the speaker's own earlier words.
function InconsistencyFlag({
  inconsistency,
  onJumpToEarlier,
}: {
  inconsistency: Inconsistency;
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
