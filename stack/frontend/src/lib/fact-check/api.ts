// Typed client for the video processing API (stack/backend
// internal/handler/processing.go). NEXT_PUBLIC_API_URL is empty in deployed
// environments (the ALB serves /api/* same-origin) and points at the backend
// container in local dev.
const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "";

export type Verdict = "corroborates" | "contradicts" | "unclear";

export type ClaimSource = {
  title: string;
  url: string;
};

export type ArticleRef = {
  title: string;
  url: string;
};

// A curated claim match carries a verdict and citation sources.
export type ClaimMatch = {
  kind: "claim";
  claim: string;
  verdict: Verdict;
  sources: ClaimSource[];
  similarity: number;
};

// SkipReason is why the check-worthiness gate declined to fact-check a segment.
// It is distinct from a Verdict: a skipped segment carries no verdict at all.
export type SkipReason = "not_a_claim" | "not_covered";

// A Wikipedia evidence match is supporting context: an article excerpt with
// attribution and no verdict. CC BY-SA 4.0 requires showing the article title
// and URL wherever the excerpt is displayed.
export type EvidenceMatch = {
  kind: "evidence";
  excerpt: string;
  article: ArticleRef;
  similarity: number;
};

export type SegmentMatch = ClaimMatch | EvidenceMatch;

export type FactCheckSegment = {
  start: number;
  end: number;
  text: string;
  matches: SegmentMatch[];
  // Set only when the segment was skipped; absent means it was checked and
  // matches (possibly empty) is authoritative.
  skipReason?: SkipReason;
};

export type ProcessingStatus = "processing" | "complete" | "failed";

export type Submission = {
  videoId: string;
  status: ProcessingStatus;
};

export type VideoStatus = {
  videoId: string;
  status: ProcessingStatus;
  segmentsTotal: number;
  segmentsDone: number;
  error: string | undefined;
};

export type ResultsOutcome =
  | { kind: "complete"; segments: FactCheckSegment[] }
  | { kind: "pending" };

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

type SubmitWire = { video_id: string; status: ProcessingStatus };

type StatusWire = {
  video_id: string;
  status: ProcessingStatus;
  segments_total: number;
  segments_done: number;
  error?: string;
};

// Wire shapes mirror stack/backend internal/handler/processing.go. A match's
// kind may be absent on results stored before the Wikipedia evidence feature;
// such matches read back as claims, matching the backend's own default.
type MatchWire = {
  kind?: "claim" | "evidence";
  claim?: string;
  verdict?: Verdict;
  sources?: ClaimSource[];
  similarity: number;
  article?: ArticleRef;
};

type SegmentWire = {
  start: number;
  end: number;
  text: string;
  matches: MatchWire[];
  skip_reason?: SkipReason;
};

type ResultsWire = { video_id: string; segments: SegmentWire[] };

function normalizeMatch(wire: MatchWire): SegmentMatch {
  // Discriminate on kind alone: evidence must never fall through to the claim
  // branch, or a missing attribution would fabricate an "unclear" verdict on
  // content the corpus cannot adjudicate. A malformed evidence payload without
  // an article degrades to a generic Wikipedia credit rather than a verdict.
  if (wire.kind === "evidence") {
    return {
      kind: "evidence",
      excerpt: wire.claim ?? "",
      article: wire.article ?? {
        title: "Wikipedia",
        url: "https://www.wikipedia.org",
      },
      similarity: wire.similarity,
    };
  }
  return {
    kind: "claim",
    claim: wire.claim ?? "",
    verdict: wire.verdict ?? "unclear",
    sources: wire.sources ?? [],
    similarity: wire.similarity,
  };
}

function normalizeSegment(wire: SegmentWire): FactCheckSegment {
  return {
    start: wire.start,
    end: wire.end,
    text: wire.text,
    matches: wire.matches.map(normalizeMatch),
    skipReason: wire.skip_reason,
  };
}

async function toApiError(response: Response): Promise<ApiError> {
  const fallback = `request failed with status ${response.status}`;
  try {
    const body: unknown = await response.json();
    if (
      typeof body === "object" &&
      body !== null &&
      "error" in body &&
      typeof body.error === "string"
    ) {
      return new ApiError(body.error, response.status);
    }
  } catch {
    // Non-JSON error body; fall through to the generic message.
  }
  return new ApiError(fallback, response.status);
}

export async function submitVideo(
  source: string,
  signal?: AbortSignal,
): Promise<Submission> {
  const response = await fetch(`${API_BASE}/api/videos`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source }),
    signal,
  });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as SubmitWire;
  return { videoId: wire.video_id, status: wire.status };
}

export async function fetchVideoStatus(
  videoId: string,
  signal?: AbortSignal,
): Promise<VideoStatus> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(videoId)}/status`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as StatusWire;
  return {
    videoId: wire.video_id,
    status: wire.status,
    segmentsTotal: wire.segments_total,
    segmentsDone: wire.segments_done,
    error: wire.error,
  };
}

export async function fetchVideoResults(
  videoId: string,
  signal?: AbortSignal,
): Promise<ResultsOutcome> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(videoId)}/results`,
    { signal },
  );
  if (response.status === 409 || response.status === 404) {
    return { kind: "pending" };
  }
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ResultsWire;
  return { kind: "complete", segments: wire.segments.map(normalizeSegment) };
}
