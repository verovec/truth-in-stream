// Per-speaker running credibility state for the retrieve-then-verify path. The
// backend aggregates each speaker's checked claims with Bayesian shrinkage and
// pushes a snapshot (a speaker_score frame) after every verdict; this pure reducer
// keeps the latest snapshot per speaker so the credibility widget can render a
// running score with its sample size. It is empty on a legacy stream that emits no
// speaker-score frames, so a flag-off session renders unchanged.
import type { SpeakerScoreFrame } from "./frames";

// SpeakerCredibility is one speaker's current credibility snapshot: the score in
// [0,1] and the verdict tallies behind it, so the widget can de-emphasize a thin
// sample. unverifiable claims are tracked but excluded from the score.
// misleadingFraming is the orthogonal count of the speaker's claims that carried at
// least one manipulation flag, so the widget can show honest-but-misleading framing
// apart from outright falsehood; it is zero on the credibility-only path.
export type SpeakerCredibility = {
  speaker: string;
  score: number;
  credible: number;
  disputed: number;
  unverifiable: number;
  misleadingFraming: number;
};

export type SpeakersState = ReadonlyMap<string, SpeakerCredibility>;

export function emptySpeakers(): SpeakersState {
  return new Map();
}

// sampleSize is the number of checked claims behind a snapshot. It only ever
// grows for a speaker within a session, so it doubles as a freshness ordinal.
function sampleSize(s: {
  credible: number;
  disputed: number;
  unverifiable: number;
}): number {
  return s.credible + s.disputed + s.unverifiable;
}

// applySpeakerScoreFrame stores the latest snapshot for a speaker. The verify path
// scores a speaker's claims concurrently, so two snapshots can arrive out of
// order; because a speaker's sample size only grows within a session, a frame is
// applied only when its sample is at least as large as the stored one, so a stale
// lower-sample frame can never overwrite a fresher one regardless of arrival
// order.
export function applySpeakerScoreFrame(
  state: SpeakersState,
  frame: SpeakerScoreFrame,
): SpeakersState {
  const existing = state.get(frame.speaker);
  if (existing && sampleSize(existing) > sampleSize(frame)) {
    return state;
  }
  const next = new Map(state);
  next.set(frame.speaker, {
    speaker: frame.speaker,
    score: frame.score,
    credible: frame.credible,
    disputed: frame.disputed,
    unverifiable: frame.unverifiable,
    misleadingFraming: frame.misleadingFraming,
  });
  return next;
}

// listSpeakers returns the speakers in a stable label order for rendering, so the
// widget rows do not reshuffle as scores update.
export function listSpeakers(state: SpeakersState): SpeakerCredibility[] {
  return [...state.values()].sort((a, b) =>
    a.speaker.localeCompare(b.speaker),
  );
}
