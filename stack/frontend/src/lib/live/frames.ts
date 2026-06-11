// Wire decoding for the live fact-check WebSocket. The backend
// (stack/backend internal/handler/live.go) pushes two JSON text frame kinds per
// statement, both tagged with a correlation id: a "subtitle" the moment the
// statement is transcribed, then a "result" once its verdict is ready. The
// result frame embeds the batch per-segment shape, so it reuses the batch
// normalizer and the live and batch result types never drift.
import {
  type FactCheckSegment,
  normalizeSegment,
  type SegmentWire,
} from "@/lib/fact-check/api";

// SubtitleFrame is a statement's text the moment it is transcribed, before any
// verdict. Timestamps are stream-relative seconds; the caller offsets them to
// the playback clock.
export type SubtitleFrame = {
  type: "subtitle";
  id: string;
  start: number;
  end: number;
  text: string;
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

export type LiveFrame = SubtitleFrame | ResultFrame;

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

  if (value.type === "subtitle") {
    if (
      typeof value.id !== "string" ||
      !isFiniteNumber(value.start) ||
      !isFiniteNumber(value.end) ||
      typeof value.text !== "string"
    ) {
      return null;
    }
    return {
      type: "subtitle",
      id: value.id,
      start: value.start,
      end: value.end,
      text: value.text,
    };
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
