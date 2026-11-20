import { describe, expect, test } from "vitest";
import type { DisplayStatement } from "./merge";
import type { LiveStatement } from "./statements";
import { groupSpeakerTurns, turnSentences } from "./turns";

const statement = (
  id: string,
  start: number,
  text: string,
  speaker?: string,
): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text,
  speaker,
  status: "analysing",
});

describe("groupSpeakerTurns", () => {
  test("groups consecutive statements by one speaker into a single turn", () => {
    const turns = groupSpeakerTurns([
      statement("0", 0, "First sentence.", "A"),
      statement("1", 3, "Second sentence.", "A"),
      statement("2", 6, "Another voice.", "B"),
      statement("3", 9, "Back to the first.", "A"),
    ]);

    expect(turns.map((turn) => turn.speaker)).toEqual(["A", "B", "A"]);
    expect(turns[0].sentences.map((sentence) => sentence.text)).toEqual([
      "First sentence.",
      "Second sentence.",
    ]);
    // A speaker returning later starts a new turn: order is chronological, never
    // regrouped across an interruption.
    expect(turns[2].sentences.map((sentence) => sentence.text)).toEqual([
      "Back to the first.",
    ]);
  });

  test("groups consecutive unattributed statements into one speakerless turn", () => {
    const turns = groupSpeakerTurns([
      statement("0", 0, "No diarization."),
      statement("1", 3, "Still none."),
      statement("2", 6, "A voice appears.", "A"),
    ]);

    expect(turns).toHaveLength(2);
    expect(turns[0].speaker).toBeUndefined();
    expect(turns[0].sentences).toHaveLength(2);
    expect(turns[1].speaker).toBe("A");
  });

  test("spans a turn from its first start to its latest end", () => {
    const turns = groupSpeakerTurns([
      statement("0", 0, "First.", "A"),
      statement("1", 7, "Later.", "A"),
    ]);

    expect(turns[0].start).toBe(0);
    expect(turns[0].end).toBe(9);
  });

  test("flattens a merged unit into one sentence per member part", () => {
    const merged: DisplayStatement = {
      ...statement("0", 0, "Le budget monte. Il monte de dix pour cent.", "A"),
      end: 6,
      parts: [
        { id: "0", text: "Le budget monte.", start: 0, end: 3 },
        { id: "1", text: "Il monte de dix pour cent.", start: 3, end: 6 },
      ],
    };
    const turns = groupSpeakerTurns([merged]);

    expect(turns).toHaveLength(1);
    expect(turns[0].sentences).toEqual([
      {
        id: "0",
        text: "Le budget monte.",
        start: 0,
        end: 3,
        statementId: "0",
        inconsistency: undefined,
      },
      {
        id: "1",
        text: "Il monte de dix pour cent.",
        start: 3,
        end: 6,
        statementId: "0",
        inconsistency: undefined,
      },
    ]);
  });

  test("attaches a statement's inconsistency to its first sentence only", () => {
    const inconsistency = {
      earlierId: "x",
      earlierText: "an earlier remark",
    };
    const merged: DisplayStatement = {
      ...statement("0", 0, "One. Two.", "A"),
      inconsistency,
      parts: [
        { id: "0", text: "One.", start: 0, end: 1 },
        { id: "1", text: "Two.", start: 1, end: 2 },
      ],
    };
    const turns = groupSpeakerTurns([merged]);

    expect(turns[0].sentences[0].inconsistency).toEqual(inconsistency);
    expect(turns[0].sentences[1].inconsistency).toBeUndefined();
  });

  test("a unit's members stay in one turn keyed to the unit's statement id", () => {
    const merged: DisplayStatement = {
      ...statement("u1", 0, "One. Two.", "A"),
      parts: [
        { id: "u1", text: "One.", start: 0, end: 1 },
        { id: "s2", text: "Two.", start: 1, end: 2 },
      ],
    };
    const turns = groupSpeakerTurns([
      merged,
      statement("s3", 3, "Three.", "A"),
    ]);

    // Every sentence of the unit selects by the unit's id; the following plain
    // statement selects by its own.
    expect(turns).toHaveLength(1);
    expect(
      turns[0].sentences.map((sentence) => sentence.statementId),
    ).toEqual(["u1", "u1", "s3"]);
  });

  test("returns no turns for an empty transcript", () => {
    expect(groupSpeakerTurns([])).toEqual([]);
  });
});

describe("turnSentences", () => {
  test("flattens turns back into one chronological sentence list", () => {
    const turns = groupSpeakerTurns([
      statement("0", 0, "First.", "A"),
      statement("1", 3, "Second.", "B"),
    ]);

    expect(turnSentences(turns).map((sentence) => sentence.id)).toEqual([
      "0",
      "1",
    ]);
  });
});
