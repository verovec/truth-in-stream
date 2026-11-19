// Typed client for the speaker naming API (stack/backend
// internal/handler/video_speakers.go): the mapping from a video's diarized
// speaker labels to people. The mapping arrives embedded in the analysis read;
// this module parses it and carries the two admin mutations.
import { API_BASE, toApiError } from "@/lib/http";

export const SPEAKER_ROLES = [
  "principal",
  "moderator",
  "guest",
  "unknown",
] as const;

export type SpeakerRole = (typeof SPEAKER_ROLES)[number];

// SpeakerMethod is how a name was decided: fused transcript signals, a model
// tie-break validated against the transcript, an operator, or not at all.
export type SpeakerMethod = "signals" | "tie_break" | "operator" | "none";

const SPEAKER_METHODS: ReadonlySet<string> = new Set([
  "signals",
  "tie_break",
  "operator",
  "none",
]);

// SpeakerEvidence is one verbatim transcript span supporting a name; at is the
// video time of the quoted turn, in seconds.
export type SpeakerEvidence = {
  kind: string;
  quote: string;
  at: number;
};

// VideoSpeaker maps one diarized label to a person. name is empty for a label
// left unnamed; operatorSet marks a mapping an operator confirmed or overrode.
export type VideoSpeaker = {
  label: string;
  name: string;
  role: SpeakerRole;
  confidence: number;
  method: SpeakerMethod;
  evidence: SpeakerEvidence[];
  operatorSet: boolean;
};

export type SpeakerUpdate = { name: string; role: SpeakerRole };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isRole(value: unknown): value is SpeakerRole {
  return SPEAKER_ROLES.some((role) => role === value);
}

function parseEvidence(value: unknown): SpeakerEvidence[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((item) =>
    isRecord(item) && typeof item.quote === "string"
      ? [
          {
            kind: typeof item.kind === "string" ? item.kind : "",
            quote: item.quote,
            at:
              typeof item.at === "number" && Number.isFinite(item.at)
                ? item.at
                : 0,
          },
        ]
      : [],
  );
}

// parseVideoSpeaker validates one wire speaker. An entry without a label
// cannot be applied to anything and is rejected; unknown role and method
// values degrade to their neutral members rather than failing the entry.
export function parseVideoSpeaker(value: unknown): VideoSpeaker | null {
  if (!isRecord(value) || typeof value.label !== "string" || value.label === "") {
    return null;
  }
  return {
    label: value.label,
    name: typeof value.name === "string" ? value.name : "",
    role: isRole(value.role) ? value.role : "unknown",
    confidence:
      typeof value.confidence === "number" && Number.isFinite(value.confidence)
        ? value.confidence
        : 0,
    method:
      typeof value.method === "string" && SPEAKER_METHODS.has(value.method)
        ? (value.method as SpeakerMethod)
        : "none",
    evidence: parseEvidence(value.evidence),
    operatorSet: value.operator_set === true,
  };
}

// parseVideoSpeakers validates a wire speaker list, dropping malformed entries
// individually. Anything that is not a list is an absent mapping.
export function parseVideoSpeakers(value: unknown): VideoSpeaker[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map(parseVideoSpeaker)
    .filter((speaker): speaker is VideoSpeaker => speaker !== null);
}

// speakerNamesByLabel is the label-to-name lookup the transcript and the
// credibility panel render from. Unnamed labels are left out, so a consumer
// falls back to the letter.
export function speakerNamesByLabel(
  speakers: VideoSpeaker[],
): Record<string, string> {
  const names: Record<string, string> = {};
  for (const speaker of speakers) {
    if (speaker.name !== "") {
      names[speaker.label] = speaker.name;
    }
  }
  return names;
}

// getVideoSpeakers reads the stored mapping of a video. It serves any
// authenticated caller and answers an empty list until the naming step has
// written its proposals.
export async function getVideoSpeakers(
  videoId: string,
  signal?: AbortSignal,
): Promise<VideoSpeaker[]> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(videoId)}/speakers`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire: unknown = await response.json();
  return parseVideoSpeakers(isRecord(wire) ? wire.speakers : null);
}

// updateVideoSpeaker records an operator's name and role for one label and
// confirms it. Admin-gated on the backend; it answers the updated speaker.
export async function updateVideoSpeaker(
  videoId: string,
  label: string,
  update: SpeakerUpdate,
  signal?: AbortSignal,
): Promise<VideoSpeaker> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(videoId)}/speakers/${encodeURIComponent(label)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(update),
      signal,
    },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const speaker = parseVideoSpeaker(await response.json());
  if (speaker === null) {
    throw new Error("malformed speaker in the update response");
  }
  return speaker;
}

// rerunSpeakerNaming re-runs the naming step on the video's stored analysis and
// answers the resulting mapping. Admin-gated on the backend; operator-confirmed
// speakers are kept.
export async function rerunSpeakerNaming(
  videoId: string,
  signal?: AbortSignal,
): Promise<VideoSpeaker[]> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(videoId)}/speakers/name`,
    { method: "POST", signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire: unknown = await response.json();
  return parseVideoSpeakers(isRecord(wire) ? wire.speakers : null);
}
