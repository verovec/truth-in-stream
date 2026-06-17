import { describe, expect, test } from "vitest";
import type { SpeakerScoreFrame } from "./frames";
import {
  applySpeakerScoreFrame,
  emptySpeakers,
  listSpeakers,
  type SpeakersState,
} from "./speakers";

const frame = (
  speaker: string,
  score: number,
  credible: number,
  disputed: number,
  unverifiable: number,
): SpeakerScoreFrame => ({
  type: "speaker_score",
  speaker,
  score,
  credible,
  disputed,
  unverifiable,
});

describe("applySpeakerScoreFrame", () => {
  test("stores the latest snapshot per speaker", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerScoreFrame(state, frame("A", 0.6, 1, 0, 0));
    state = applySpeakerScoreFrame(state, frame("B", 0.4, 0, 1, 0));

    expect(listSpeakers(state)).toEqual([
      { speaker: "A", score: 0.6, credible: 1, disputed: 0, unverifiable: 0 },
      { speaker: "B", score: 0.4, credible: 0, disputed: 1, unverifiable: 0 },
    ]);
  });

  test("a later, larger-sample snapshot replaces the earlier one", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerScoreFrame(state, frame("A", 0.6, 1, 0, 0));
    state = applySpeakerScoreFrame(state, frame("A", 0.55, 2, 1, 0));

    expect(listSpeakers(state)).toEqual([
      { speaker: "A", score: 0.55, credible: 2, disputed: 1, unverifiable: 0 },
    ]);
  });

  test("a stale lower-sample snapshot never overwrites a fresher one", () => {
    // Concurrent verdicts can deliver snapshots out of order; the freshest is the
    // one with the largest sample, so a late small-sample frame is ignored.
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerScoreFrame(state, frame("A", 0.55, 2, 1, 0));
    state = applySpeakerScoreFrame(state, frame("A", 0.6, 1, 0, 0));

    expect(listSpeakers(state)).toEqual([
      { speaker: "A", score: 0.55, credible: 2, disputed: 1, unverifiable: 0 },
    ]);
  });

  test("an equal-sample snapshot is applied (ties favour the newer frame)", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerScoreFrame(state, frame("A", 0.6, 1, 0, 0));
    state = applySpeakerScoreFrame(state, frame("A", 0.4, 0, 1, 0));

    expect(listSpeakers(state)[0]).toMatchObject({ score: 0.4, disputed: 1 });
  });

  test("listSpeakers returns speakers in stable label order", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerScoreFrame(state, frame("C", 0.5, 0, 0, 1));
    state = applySpeakerScoreFrame(state, frame("A", 0.6, 1, 0, 0));
    state = applySpeakerScoreFrame(state, frame("B", 0.4, 0, 1, 0));

    expect(listSpeakers(state).map((s) => s.speaker)).toEqual(["A", "B", "C"]);
  });
});
