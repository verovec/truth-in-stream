// Per-speaker running verdict breakdown for the retrieve-then-verify path. The
// backend tallies each speaker's checked claims and pushes a snapshot (a
// speaker_tally frame) after every verdict; this pure reducer keeps the latest
// snapshot per speaker so the speaker panel can render an itemised breakdown. It is
// empty on a legacy stream that emits no speaker-tally frames, so a flag-off session
// renders unchanged.
import type { SpeakerTallyFrame } from "./frames";

// SpeakerTally is one speaker's current verdict breakdown: the lifetime credible,
// disputed, and unverifiable counts, so the panel can show how many checkable
// claims the speaker made and how they broke down. misleadingFraming is the
// orthogonal count of the speaker's claims that carried at least one manipulation
// flag, so the panel can show honest-but-misleading framing apart from outright
// falsehood; it is zero on the credibility-only path.
export type SpeakerTally = {
  speaker: string;
  credible: number;
  disputed: number;
  unverifiable: number;
  misleadingFraming: number;
};

export type SpeakersState = ReadonlyMap<string, SpeakerTally>;

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

// applySpeakerTallyFrame stores the latest snapshot for a speaker. The verify path
// tallies a speaker's claims concurrently, so two snapshots can arrive out of
// order; because a speaker's sample size only grows within a session, a frame is
// applied only when its sample is at least as large as the stored one, so a stale
// lower-sample frame can never overwrite a fresher one regardless of arrival
// order.
export function applySpeakerTallyFrame(
  state: SpeakersState,
  frame: SpeakerTallyFrame,
): SpeakersState {
  const existing = state.get(frame.speaker);
  if (existing && sampleSize(existing) > sampleSize(frame)) {
    return state;
  }
  const next = new Map(state);
  next.set(frame.speaker, {
    speaker: frame.speaker,
    credible: frame.credible,
    disputed: frame.disputed,
    unverifiable: frame.unverifiable,
    misleadingFraming: frame.misleadingFraming,
  });
  return next;
}

// listSpeakers returns the speakers in a stable label order for rendering, so the
// panel rows do not reshuffle as tallies update.
export function listSpeakers(state: SpeakersState): SpeakerTally[] {
  return [...state.values()].sort((a, b) =>
    a.speaker.localeCompare(b.speaker),
  );
}
