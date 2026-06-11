// Wire decoding for the live fact-check WebSocket. The backend
// (stack/backend internal/handler/live.go) pushes three JSON text frame kinds:
// an "interim" caption (text only, no id) for the utterance still being spoken,
// then per finalized statement a "subtitle" the moment it is transcribed and a
// "result" once its verdict is ready, both tagged with a correlation id. The
// result frame embeds the batch per-segment shape, so it reuses the batch
// normalizer and the live and batch result types never drift.
import {
  type FactCheckSegment,
  normalizeSegment,
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

export type LiveFrame = InterimFrame | SubtitleFrame | ResultFrame;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
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

  return null;
}
