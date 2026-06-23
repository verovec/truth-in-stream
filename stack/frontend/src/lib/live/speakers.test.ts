import { describe, expect, test } from "vitest";
import type { SpeakerTallyFrame } from "./frames";
import {
  applySpeakerTallyFrame,
  emptySpeakers,
  listSpeakers,
  type SpeakersState,
} from "./speakers";

const frame = (
  speaker: string,
  credible: number,
  disputed: number,
  unverifiable: number,
  misleadingFraming = 0,
): SpeakerTallyFrame => ({
  type: "speaker_tally",
  speaker,
  credible,
  disputed,
  unverifiable,
  misleadingFraming,
});

describe("applySpeakerTallyFrame", () => {
  test("stores the latest snapshot per speaker", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerTallyFrame(state, frame("A", 1, 0, 0));
    state = applySpeakerTallyFrame(state, frame("B", 0, 1, 0));

    expect(listSpeakers(state)).toEqual([
      {
        speaker: "A",
        credible: 1,
        disputed: 0,
        unverifiable: 0,
        misleadingFraming: 0,
      },
      {
        speaker: "B",
        credible: 0,
        disputed: 1,
        unverifiable: 0,
        misleadingFraming: 0,
      },
    ]);
  });

  test("a later, larger-sample snapshot replaces the earlier one", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerTallyFrame(state, frame("A", 1, 0, 0));
    state = applySpeakerTallyFrame(state, frame("A", 2, 1, 0));

    expect(listSpeakers(state)).toEqual([
      {
        speaker: "A",
        credible: 2,
        disputed: 1,
        unverifiable: 0,
        misleadingFraming: 0,
      },
    ]);
  });

  test("a stale lower-sample snapshot never overwrites a fresher one", () => {
    // Concurrent verdicts can deliver snapshots out of order; the freshest is the
    // one with the largest sample, so a late small-sample frame is ignored.
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerTallyFrame(state, frame("A", 2, 1, 0));
    state = applySpeakerTallyFrame(state, frame("A", 1, 0, 0));

    expect(listSpeakers(state)).toEqual([
      {
        speaker: "A",
        credible: 2,
        disputed: 1,
        unverifiable: 0,
        misleadingFraming: 0,
      },
    ]);
  });

  test("carries the misleading-framing tally through, orthogonal to the verdict counts", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerTallyFrame(state, frame("A", 4, 1, 0, 2));

    expect(listSpeakers(state)[0]).toMatchObject({
      credible: 4,
      disputed: 1,
      misleadingFraming: 2,
    });
  });

  test("an equal-sample snapshot is applied (ties favour the newer frame)", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerTallyFrame(state, frame("A", 1, 0, 0));
    state = applySpeakerTallyFrame(state, frame("A", 0, 1, 0));

    expect(listSpeakers(state)[0]).toMatchObject({ credible: 0, disputed: 1 });
  });

  test("listSpeakers returns speakers in stable label order", () => {
    let state: SpeakersState = emptySpeakers();
    state = applySpeakerTallyFrame(state, frame("C", 0, 0, 1));
    state = applySpeakerTallyFrame(state, frame("A", 1, 0, 0));
    state = applySpeakerTallyFrame(state, frame("B", 0, 1, 0));

    expect(listSpeakers(state).map((s) => s.speaker)).toEqual(["A", "B", "C"]);
  });
});
