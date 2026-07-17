// Wire decoding for the live fact-check WebSocket. The backend
// (stack/backend internal/handler/live.go) pushes three JSON text frame kinds:
// an "interim" caption (text only, no id) for the utterance still being spoken,
// then per finalized statement a "subtitle" the moment it is transcribed and a
// "result" once its verdict is ready, both tagged with a correlation id. The
// result frame embeds the batch per-segment shape, so it reuses the batch
// normalizer and the live and batch result types never drift.
import {
  type FactCheckSegment,
  type MatchWire,
  normalizeMatch,
  normalizeSegment,
  type SegmentMatch,
  type SegmentWire,
} from "@/lib/fact-check/api";

// InterimFrame is the live, still-revised caption for the current utterance,
// before the provider commits a statement. It carries only text - no id, no
// timestamps, no verdict - and the next interim or subtitle supersedes it.
export type InterimFrame = {
  type: "interim";
  text: string;
};

// SubtitleFrame is a statement's text the moment it is transcribed, before any
// verdict. Timestamps are stream-relative seconds; the caller offsets them to
// the playback clock. speaker is the diarized speaker label (e.g. "A", "B") when
// the provider supplies one; it is absent for unattributed speech.
export type SubtitleFrame = {
  type: "subtitle";
  id: string;
  start: number;
  end: number;
  text: string;
  speaker?: string;
};

// ResultFrame is the fact-check outcome for a statement, sharing the subtitle's
// id and segment. error is set only when analysis failed without ending the
// stream; the segment is still present so the statement resolves.
export type ResultFrame = {
  type: "result";
  id: string;
  segment: FactCheckSegment;
  error?: string;
};

// ConsistencyFrame flags a statement that contradicts an earlier statement by
// the same speaker. id is the offending (later) statement; earlierId and
// earlierText identify the statement it conflicts with so the UI can reference
// it. speaker and rationale are additive context. It arrives after the
// statement's subtitle and result, never before.
export type ConsistencyFrame = {
  type: "consistency";
  id: string;
  earlierId: string;
  earlierText: string;
  speaker?: string;
  rationale?: string;
};

// ClaimStatus is one atomic claim's lifecycle state on the retrieve-then-verify
// path. A claim is announced "pending" on the claims frame, may transit
// "checking" while a verify call is in flight, and ends "verified" (a verdict
// landed), "unchecked" (the verify pool was saturated - an honest terminal
// state, not a drop), or "error" (a retrieval or verifier failure, distinct from
// a reached verdict). These mirror service.ClaimStatus on the backend.
export type ClaimStatus =
  | "pending"
  | "checking"
  | "verified"
  | "unchecked"
  | "error";

// ClaimVerdict is the verify path's grounded credibility judgment for one atomic
// claim. It is a distinct enum from the legacy curated-match Verdict
// (corroborates/contradicts/unclear): the verifier answers "can I trust the
// speaker on this?" and returns credible/disputed/unverifiable, with unverifiable
// a first-class verdict.
export type ClaimVerdict = "credible" | "disputed" | "unverifiable";

// VerdictBasis tags what a verdict rests on: a supplied evidence passage the
// verifier cited, or its world-knowledge tiebreaker (used when no passage bears on
// the claim). A knowledge-basis verdict is lower-confidence and shown as having no
// direct sources, so the viewer can weigh it against an evidence-grounded one.
export type VerdictBasis = "evidence" | "knowledge";

// VerdictSource tags where a verified claim's verdict came from: borrowed from a
// curated near-match (instant, no LLM) or reasoned by the evidence verifier. The
// UI distinguishes a borrowed verdict from a reasoned one by this tag.
export type VerdictSource = "curated" | "verified";

// LiteralVerdict is the political path's face-value axis (FACTCHECK_POLITICAL on):
// is the claim, taken literally, accurate against the supplied evidence? It is
// orthogonal to ManipulationFlag - a claim can be literally accurate yet
// cherry-picked. The credibility ClaimVerdict is derived from it on the backend, so
// a flag-off frame carries the credibility verdict and no literal axis.
export type LiteralVerdict = "accurate" | "inaccurate" | "unverifiable";

// ManipulationFlag is one entry of the closed framing-honesty vocabulary the
// verifier may attach to a claim (FACTCHECK_POLITICAL on), orthogonal to the
// literal verdict. The set is fixed: a value outside it is dropped on parse so a
// future or hallucinated flag never mis-renders.
export type ManipulationFlag =
  | "missing-context"
  | "cherry-picked"
  | "outdated"
  | "misattributed"
  | "misleading-causation";

// ClaimSpan locates the verbatim words a claim was extracted from inside one
// transcript segment: the segment's correlation id (the subtitle id statement
// rows key on) and the [start, end) offsets of the quoted words within that
// segment's text. Offsets count Unicode code points, not UTF-16 units - the
// backend counts runes - so a renderer must slice by code point.
export type ClaimSpan = {
  segmentId: string;
  start: number;
  end: number;
};

// AtomicClaim is one self-contained claim a unit decomposed into, announced on a
// claims frame with its stable per-claim id (shared across that claim's
// pending/checking/verified results so the client replaces it in place) and its
// coreference-resolved text. quote is the verbatim run of statement words the
// claim came from and spans locates those words inside the unit's segments, so
// the transcript can highlight the exact words that were checked; both are
// absent when the backend could not anchor the claim.
export type AtomicClaim = {
  claimId: string;
  text: string;
  status: "pending";
  quote?: string;
  spans?: ClaimSpan[];
};

// ClaimsFrame announces the atomic claims a checkable unit decomposed into
// (retrieve-then-verify path). id is the unit's correlation id, shared with the
// unit's subtitle so the client groups the claims under the statement they came
// from; each claim carries its own claim_id the per-claim results key on.
export type ClaimsFrame = {
  type: "claims";
  id: string;
  claims: AtomicClaim[];
};

// ClaimResultFrame is one per-claim result on the retrieve-then-verify path. id
// is the unit (shared with the subtitle and the claims frame); claimId
// identifies the claim and is what the client replaces in place. status is the
// lifecycle state; for a verified claim, source tags the verdict's origin and
// verdict/confidence/rationale/matches carry the grounded judgment and its cited
// evidence. For an unchecked claim, skipReason is the capacity reason; for an
// errored claim, error is the failure reason.
//
// literal and flags are the political path's two orthogonal axes
// (FACTCHECK_POLITICAL on): literal is the face-value verdict and flags the subset
// of the manipulation vocabulary that applies. Both are absent on the
// credibility-only path, so a flag-off frame keeps the legacy shape; verdict
// (derived from literal on the backend) is present on both paths.
//
// sourceLabel is the French publisher of the verdict's winning citation (INSEE,
// Wikipédia, Assemblée nationale, ...), distinct from source (the verdict's
// curated|verified origin); sourceUrl links it. Both are absent for a
// knowledge-only or curated-borrow verdict that names no provider, so the chip
// is then omitted. matches is the operator evidence detail, present only when
// DEBUG_FACT_CHECK is on.
export type ClaimResultFrame = {
  type: "claim_result";
  id: string;
  claimId: string;
  status: ClaimStatus;
  source?: VerdictSource;
  sourceLabel?: string;
  sourceUrl?: string;
  verdict?: ClaimVerdict;
  literal?: LiteralVerdict;
  flags?: ManipulationFlag[];
  basis?: VerdictBasis;
  confidence?: number;
  rationale?: string;
  matches?: SegmentMatch[];
  skipReason?: string;
  error?: string;
};

// SpeakerTallyFrame is a speaker's running verdict breakdown (retrieve-then-verify
// path), pushed after each of that speaker's claim verdicts updates the counts.
// credible, disputed, and unverifiable are the lifetime verdict tallies, so the
// widget can show how many checkable claims the speaker made and how they broke
// down. It is additive: a client that ignores it renders everything else unchanged.
//
// misleadingFraming is the political path's separate tally of the speaker's claims
// that carried at least one manipulation flag, orthogonal to the credibility
// tallies, so the widget can distinguish an outright falsehood from honest-but-
// misleading framing. The wire field (misleading_framing) is omitted when zero; an
// absent value reads as zero.
export type SpeakerTallyFrame = {
  type: "speaker_tally";
  speaker: string;
  credible: number;
  disputed: number;
  unverifiable: number;
  misleadingFraming: number;
};

export type LiveFrame =
  | InterimFrame
  | SubtitleFrame
  | ResultFrame
  | ConsistencyFrame
  | ClaimsFrame
  | ClaimResultFrame
  | SpeakerTallyFrame;

const CLAIM_STATUSES: ReadonlySet<string> = new Set([
  "pending",
  "checking",
  "verified",
  "unchecked",
  "error",
]);

const CLAIM_VERDICTS: ReadonlySet<string> = new Set([
  "credible",
  "disputed",
  "unverifiable",
]);

const VERDICT_BASES: ReadonlySet<string> = new Set(["evidence", "knowledge"]);

const LITERAL_VERDICTS: ReadonlySet<string> = new Set([
  "accurate",
  "inaccurate",
  "unverifiable",
]);

const MANIPULATION_FLAGS: ReadonlySet<string> = new Set([
  "missing-context",
  "cherry-picked",
  "outdated",
  "misattributed",
  "misleading-causation",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

// isClaimSpan validates one wire span: a non-empty segment id and a non-empty,
// non-negative [start, end) integer range. Anything else is dropped so a
// malformed range can never produce a nonsense highlight.
function isClaimSpan(
  value: unknown,
): value is { segment_id: string; start: number; end: number } {
  return (
    isRecord(value) &&
    typeof value.segment_id === "string" &&
    value.segment_id.length > 0 &&
    Number.isInteger(value.start) &&
    Number.isInteger(value.end) &&
    (value.start as number) >= 0 &&
    (value.end as number) > (value.start as number)
  );
}

// isHttpUrl reports whether value is a non-empty http(s) URL, the only schemes
// safe to place in a rendered href; it rejects anything else so a malformed
// frame cannot smuggle a javascript:/data: link into the source chip.
function isHttpUrl(value: unknown): value is string {
  return (
    typeof value === "string" &&
    (value.startsWith("https://") || value.startsWith("http://"))
  );
}

/**
 * Decodes one inbound WebSocket text frame into a typed live frame, or null
 * when the payload is malformed or carries an unknown type. Returning null
 * instead of throwing lets the socket loop skip a stray frame without tearing
 * the session down.
 */
export function parseLiveFrame(raw: string): LiveFrame | null {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!isRecord(value)) {
    return null;
  }

  if (value.type === "interim") {
    if (typeof value.text !== "string") {
      return null;
    }
    return { type: "interim", text: value.text };
  }

  if (value.type === "subtitle") {
    if (
      typeof value.id !== "string" ||
      !isFiniteNumber(value.start) ||
      !isFiniteNumber(value.end) ||
      typeof value.text !== "string"
    ) {
      return null;
    }
    const frame: SubtitleFrame = {
      type: "subtitle",
      id: value.id,
      start: value.start,
      end: value.end,
      text: value.text,
    };
    // speaker is additive and omitted when empty, so a stream without diarization
    // (or an unattributed turn) leaves the field absent rather than blank.
    if (typeof value.speaker === "string" && value.speaker.length > 0) {
      frame.speaker = value.speaker;
    }
    return frame;
  }

  if (value.type === "result") {
    if (
      typeof value.id !== "string" ||
      !isFiniteNumber(value.start) ||
      !isFiniteNumber(value.end) ||
      typeof value.text !== "string" ||
      !Array.isArray(value.matches)
    ) {
      return null;
    }
    const frame: ResultFrame = {
      type: "result",
      id: value.id,
      segment: normalizeSegment(value as unknown as SegmentWire),
    };
    if (typeof value.error === "string" && value.error.length > 0) {
      frame.error = value.error;
    }
    return frame;
  }

  if (value.type === "claims") {
    if (typeof value.id !== "string" || !Array.isArray(value.claims)) {
      return null;
    }
    const claims: AtomicClaim[] = [];
    for (const raw of value.claims) {
      if (
        !isRecord(raw) ||
        typeof raw.claim_id !== "string" ||
        raw.claim_id.length === 0 ||
        typeof raw.text !== "string"
      ) {
        // A malformed claim entry is skipped rather than tearing the whole frame
        // down, so one bad row does not lose the unit's other claims.
        continue;
      }
      const claim: AtomicClaim = {
        claimId: raw.claim_id,
        text: raw.text,
        status: "pending",
      };
      if (typeof raw.quote === "string" && raw.quote.length > 0) {
        claim.quote = raw.quote;
      }
      if (Array.isArray(raw.spans)) {
        // A malformed span is dropped individually so one bad range never costs
        // the claim (or its siblings) their verdicts - only the highlight.
        const spans = raw.spans.filter(isClaimSpan).map(
          (span): ClaimSpan => ({
            segmentId: span.segment_id,
            start: span.start,
            end: span.end,
          }),
        );
        if (spans.length > 0) {
          claim.spans = spans;
        }
      }
      claims.push(claim);
    }
    return { type: "claims", id: value.id, claims };
  }

  if (value.type === "claim_result") {
    if (
      typeof value.id !== "string" ||
      typeof value.claim_id !== "string" ||
      value.claim_id.length === 0 ||
      typeof value.status !== "string" ||
      !CLAIM_STATUSES.has(value.status)
    ) {
      return null;
    }
    const frame: ClaimResultFrame = {
      type: "claim_result",
      id: value.id,
      claimId: value.claim_id,
      status: value.status as ClaimStatus,
    };
    if (typeof value.source === "string" && value.source.length > 0) {
      // An unrecognised source tag is dropped rather than rendered, so a future
      // backend value cannot mis-style a row; the verdict still shows untagged.
      if (value.source === "curated" || value.source === "verified") {
        frame.source = value.source;
      }
    }
    if (typeof value.verdict === "string" && CLAIM_VERDICTS.has(value.verdict)) {
      frame.verdict = value.verdict as ClaimVerdict;
    }
    if (typeof value.literal === "string" && LITERAL_VERDICTS.has(value.literal)) {
      // An unrecognised literal verdict is dropped rather than rendered, so a
      // future axis value cannot mis-style the badge; verdict still carries the row.
      frame.literal = value.literal as LiteralVerdict;
    }
    if (Array.isArray(value.flags)) {
      // Keep only the closed-vocabulary flags, preserving order and discarding any
      // hallucinated or future value. An empty result leaves flags absent so a
      // flagless claim is byte-for-byte the legacy shape.
      const flags = value.flags.filter(
        (flag): flag is ManipulationFlag =>
          typeof flag === "string" && MANIPULATION_FLAGS.has(flag),
      );
      if (flags.length > 0) {
        frame.flags = flags;
      }
    }
    if (typeof value.basis === "string" && VERDICT_BASES.has(value.basis)) {
      frame.basis = value.basis as VerdictBasis;
    }
    if (isFiniteNumber(value.confidence)) {
      frame.confidence = value.confidence;
    }
    if (typeof value.rationale === "string" && value.rationale.length > 0) {
      frame.rationale = value.rationale;
    }
    if (typeof value.source_label === "string" && value.source_label.length > 0) {
      frame.sourceLabel = value.source_label;
    }
    if (isHttpUrl(value.source_url)) {
      // The label links this url, so only an http(s) source is accepted; a
      // javascript:/data: scheme on a malformed frame is dropped, not rendered.
      frame.sourceUrl = value.source_url;
    }
    if (Array.isArray(value.matches)) {
      frame.matches = (value.matches as MatchWire[]).map(normalizeMatch);
    }
    if (typeof value.skip_reason === "string" && value.skip_reason.length > 0) {
      frame.skipReason = value.skip_reason;
    }
    if (typeof value.error === "string" && value.error.length > 0) {
      frame.error = value.error;
    }
    return frame;
  }

  if (value.type === "speaker_tally") {
    if (typeof value.speaker !== "string" || value.speaker.length === 0) {
      return null;
    }
    return {
      type: "speaker_tally",
      speaker: value.speaker,
      credible: isFiniteNumber(value.credible) ? value.credible : 0,
      disputed: isFiniteNumber(value.disputed) ? value.disputed : 0,
      unverifiable: isFiniteNumber(value.unverifiable) ? value.unverifiable : 0,
      misleadingFraming: isFiniteNumber(value.misleading_framing)
        ? value.misleading_framing
        : 0,
    };
  }

  if (value.type === "consistency") {
    if (
      typeof value.id !== "string" ||
      typeof value.earlier_id !== "string" ||
      value.earlier_id.length === 0 ||
      typeof value.earlier_text !== "string" ||
      value.earlier_text.length === 0
    ) {
      return null;
    }
    const frame: ConsistencyFrame = {
      type: "consistency",
      id: value.id,
      earlierId: value.earlier_id,
      earlierText: value.earlier_text,
    };
    if (typeof value.speaker === "string" && value.speaker.length > 0) {
      frame.speaker = value.speaker;
    }
    if (typeof value.rationale === "string" && value.rationale.length > 0) {
      frame.rationale = value.rationale;
    }
    return frame;
  }

  return null;
}
