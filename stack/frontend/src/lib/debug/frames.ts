// Wire types for the developer wiki-search WebSocket (dev only). The backend
// (stack/backend internal/handler/debug_search.go) accepts a JSON query frame
// {q, seq} and replies with a results frame {type:"results", seq, hits, error?}.
// seq echoes the request so a response superseded by a later keystroke can be
// dropped on the client (see applyResults in ./search-state).

// DebugHit is one nearest-neighbor result from the embedded wiki corpus: the
// article attribution, a bounded excerpt, and the cosine similarity in [-1, 1].
export type DebugHit = {
  title: string;
  url: string;
  snippet: string;
  similarity: number;
};

// DebugResultsFrame is the server's reply to one query. seq echoes the request;
// hits is empty when nothing matched or the search failed; error is a generic
// message present only on failure.
export type DebugResultsFrame = {
  type: "results";
  seq: number;
  hits: DebugHit[];
  error?: string;
};

// DebugQueryFrame is the client's request: the query text and a monotonically
// increasing sequence number the server echoes back.
export type DebugQueryFrame = {
  q: string;
  seq: number;
};

export function encodeQueryFrame(frame: DebugQueryFrame): string {
  return JSON.stringify(frame);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function parseHit(value: unknown): DebugHit | null {
  if (!isRecord(value)) {
    return null;
  }
  if (
    typeof value.title !== "string" ||
    typeof value.url !== "string" ||
    typeof value.snippet !== "string" ||
    !isFiniteNumber(value.similarity)
  ) {
    return null;
  }
  return {
    title: value.title,
    url: value.url,
    snippet: value.snippet,
    similarity: value.similarity,
  };
}

/**
 * Decodes one inbound WebSocket text frame into a typed results frame, or null
 * when the payload is malformed or is not a results frame. A malformed
 * individual hit is dropped rather than failing the whole frame, so one bad row
 * does not blank the result list. Returning null instead of throwing lets the
 * socket loop skip a stray frame without tearing the session down.
 */
export function parseDebugResultsFrame(raw: string): DebugResultsFrame | null {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!isRecord(value) || value.type !== "results" || !isFiniteNumber(value.seq)) {
    return null;
  }
  if (!Array.isArray(value.hits)) {
    return null;
  }
  const hits: DebugHit[] = [];
  for (const raw of value.hits) {
    const hit = parseHit(raw);
    if (hit) {
      hits.push(hit);
    }
  }
  const frame: DebugResultsFrame = { type: "results", seq: value.seq, hits };
  if (typeof value.error === "string" && value.error.length > 0) {
    frame.error = value.error;
  }
  return frame;
}
