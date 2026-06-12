// Incremental live fact-check state. Subtitles and results stream in out of
// order; this store reconciles them into a timestamp-ordered list of
// statements, each either still "analysing" or "checked". It is a pure reducer
// so the React layer only mirrors it.
import type {
  Confidence,
  FactCheckSegment,
  SegmentMatch,
  SkipReason,
} from "@/lib/fact-check/api";
import type { ResultFrame, SubtitleFrame } from "./frames";

// LiveStatement is one spoken statement on screen. The two states are a
// discriminated union so an analysing statement can never carry a verdict and a
// checked one always does: invalid combinations are unrepresentable.
export type LiveStatement =
  | {
      id: string;
      start: number;
      end: number;
      text: string;
      speaker?: string;
      status: "analysing";
    }
  | {
      id: string;
      start: number;
      end: number;
      text: string;
      speaker?: string;
      status: "checked";
      matches: SegmentMatch[];
      skipReason?: SkipReason;
      error?: string;
      // The corroboration score, present only on a checked, non-skipped,
      // non-errored statement; absent otherwise.
      confidence?: Confidence;
    };

// StatementsState keys statements by correlation id (namespaced per session by
// the caller) so a verdict reconciles to its subtitle. byStart maps a rounded
// start time to the id currently occupying it, so a replay after a reconnect
// supersedes the prior statement at that moment instead of duplicating it.
export type StatementsState = {
  byId: ReadonlyMap<string, LiveStatement>;
  byStart: ReadonlyMap<number, string>;
};

export function emptyStatements(): StatementsState {
  return { byId: new Map(), byStart: new Map() };
}

// startKey buckets a start time to the millisecond so the subtitle and result
// of one statement (identical starts) collapse, and a replayed statement lands
// on the same bucket as the original it replaces.
function startKey(start: number): number {
  return Math.round(start * 1000);
}

function checkedFromResult(
  id: string,
  segment: FactCheckSegment,
  error: string | undefined,
  speaker: string | undefined,
): LiveStatement {
  return {
    id,
    start: segment.start,
    end: segment.end,
    text: segment.text,
    speaker,
    status: "checked",
    matches: segment.matches,
    skipReason: segment.skipReason,
    error,
    confidence: segment.confidence,
  };
}

/**
 * Applies one committed frame, returning a new state. A subtitle creates an
 * analysing statement but never downgrades one already checked; a result
 * resolves its statement to checked. Either way, any other statement sharing
 * the same start bucket is superseded, so a reconnect that replays a moment
 * does not duplicate it. Interim captions are handled upstream and never reach
 * this reducer, so only the id-bearing committed frames are accepted here.
 */
export function applyFrame(
  state: StatementsState,
  frame: SubtitleFrame | ResultFrame,
): StatementsState {
  const start = frame.type === "subtitle" ? frame.start : frame.segment.start;
  const key = startKey(start);
  const byId = new Map(state.byId);
  const byStart = new Map(state.byStart);

  // Drop a different statement currently holding this start bucket (a replay
  // after reconnect supersedes the original at that timestamp).
  const incumbent = byStart.get(key);
  if (incumbent !== undefined && incumbent !== frame.id) {
    byId.delete(incumbent);
  }

  const existing = byId.get(frame.id);
  if (frame.type === "subtitle") {
    if (!existing || existing.status !== "checked") {
      byId.set(frame.id, {
        id: frame.id,
        start: frame.start,
        end: frame.end,
        text: frame.text,
        speaker: frame.speaker,
        status: "analysing",
      });
    } else if (frame.speaker && !existing.speaker) {
      // The verdict landed before its subtitle (out-of-order delivery or a
      // reconnect replay): keep the resolved statement but backfill the speaker
      // the subtitle carries, which the result frame could not supply.
      byId.set(frame.id, { ...existing, speaker: frame.speaker });
    }
  } else {
    // The result frame carries no speaker (diarization lands on the subtitle), so
    // the verdict inherits the label its subtitle already established.
    byId.set(
      frame.id,
      checkedFromResult(frame.id, frame.segment, frame.error, existing?.speaker),
    );
  }

  byStart.set(key, frame.id);
  return { byId, byStart };
}

/**
 * Drops statements still analysing, used when a session is torn down (seek or a
 * dropped connection) so an in-flight statement that will never resolve does
 * not linger; resolved verdicts are kept.
 */
export function clearAnalysing(state: StatementsState): StatementsState {
  const byId = new Map<string, LiveStatement>();
  const byStart = new Map<number, string>();
  for (const [id, statement] of state.byId) {
    if (statement.status === "checked") {
      byId.set(id, statement);
      byStart.set(startKey(statement.start), id);
    }
  }
  return { byId, byStart };
}

/** Returns the statements ordered by start time for rendering. */
export function listStatements(state: StatementsState): LiveStatement[] {
  return [...state.byId.values()].sort((a, b) => a.start - b.start);
}

// isScored is true for a statement that was fact-checked and may therefore carry
// verdict or evidence matches: it resolved to "checked" and was neither skipped
// by the check-worthiness gate nor errored. The fact-check list and the running
// summary both key off this one predicate, so a statement the list omits can
// never be tallied as a verdict in the summary.
export function isScored(
  statement: LiveStatement,
): statement is Extract<LiveStatement, { status: "checked" }> {
  return (
    statement.status === "checked" && !statement.error && !statement.skipReason
  );
}
