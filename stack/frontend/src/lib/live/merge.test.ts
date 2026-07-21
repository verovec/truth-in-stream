import { describe, expect, test } from "vitest";
import { mergeUnitStatements } from "./merge";
import type { LiveStatement } from "./statements";

function analysing(
  id: string,
  start: number,
  end: number,
  text: string,
  speaker?: string,
): LiveStatement {
  return { id, start, end, text, speaker, status: "analysing" };
}

describe("mergeUnitStatements", () => {
  test("merges a unit's member statements into one row at the group's position", () => {
    const statements = [
      analysing("0", 1, 2, "Le budget monte.", "A"),
      analysing("1", 2, 3, "Il monte de dix pour cent.", "A"),
      analysing("2", 4, 5, "Autre chose.", "B"),
    ];
    const members = new Map([["0", ["0", "1"]]]);

    const merged = mergeUnitStatements(statements, members);

    expect(merged).toHaveLength(2);
    expect(merged[0]).toMatchObject({
      id: "0",
      start: 1,
      end: 3,
      speaker: "A",
      text: "Le budget monte. Il monte de dix pour cent.",
      parts: [
        { id: "0", text: "Le budget monte." },
        { id: "1", text: "Il monte de dix pour cent." },
      ],
    });
    expect(merged[1]).toBe(statements[2]);
  });

  test("no membership renders per statement with the same identity", () => {
    const statements = [analysing("0", 1, 2, "Un.", "A")];
    expect(mergeUnitStatements(statements, new Map())).toBe(statements);
  });

  test("a single-member unit renders per statement", () => {
    const statements = [
      analysing("0", 1, 2, "Un.", "A"),
      analysing("1", 2, 3, "Deux.", "A"),
    ];
    const members = new Map([["0", ["0"]]]);
    expect(mergeUnitStatements(statements, members)).toBe(statements);
  });

  test("a unit whose anchor is absent leaves the remaining rows alone", () => {
    const statements = [analysing("1", 2, 3, "Deux.", "A")];
    const members = new Map([["0", ["0", "1"]]]);
    expect(mergeUnitStatements(statements, members)).toBe(statements);
  });

  test("a unit reduced to its anchor renders per statement", () => {
    const statements = [
      analysing("0", 1, 2, "Un.", "A"),
      analysing("5", 6, 7, "Autre.", "B"),
    ];
    const members = new Map([["0", ["0", "1"]]]);
    expect(mergeUnitStatements(statements, members)).toBe(statements);
  });

  test("an absent middle member merges the members still present", () => {
    const statements = [
      analysing("0", 1, 2, "Un.", "A"),
      analysing("2", 3, 4, "Trois.", "A"),
    ];
    const members = new Map([["0", ["0", "1", "2"]]]);

    const merged = mergeUnitStatements(statements, members);

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      id: "0",
      start: 1,
      end: 4,
      text: "Un. Trois.",
      parts: [
        { id: "0", text: "Un." },
        { id: "2", text: "Trois." },
      ],
    });
  });

  test("overlapping units never render a statement twice: first announced wins", () => {
    const statements = [
      analysing("0", 1, 2, "Zero.", "A"),
      analysing("1", 2, 3, "One.", "A"),
      analysing("2", 3, 4, "Two.", "A"),
    ];
    const members = new Map([
      ["0", ["0", "1"]],
      ["1", ["1", "2"]],
    ]);

    const merged = mergeUnitStatements(statements, members);

    expect(merged.map((s) => s.id)).toEqual(["0", "2"]);
    expect(merged[0].text).toBe("Zero. One.");
    expect(merged[1]).toBe(statements[2]);
  });

  test("a duplicated member id contributes its statement once", () => {
    const statements = [
      analysing("0", 1, 2, "Zero.", "A"),
      analysing("1", 2, 3, "One.", "A"),
    ];
    const members = new Map([["0", ["0", "0", "1"]]]);

    const merged = mergeUnitStatements(statements, members);

    expect(merged).toHaveLength(1);
    expect(merged[0].text).toBe("Zero. One.");
    expect(merged[0].parts?.map((p) => p.id)).toEqual(["0", "1"]);
  });

  test("a non-anchor member's inconsistency flag survives the merge", () => {
    const flagged: LiveStatement = {
      ...analysing("1", 2, 3, "One.", "A"),
      inconsistency: { earlierId: "9", earlierText: "earlier words" },
    };
    const statements = [analysing("0", 1, 2, "Zero.", "A"), flagged];
    const members = new Map([["0", ["0", "1"]]]);

    const merged = mergeUnitStatements(statements, members);

    expect(merged).toHaveLength(1);
    expect(merged[0].inconsistency).toEqual({
      earlierId: "9",
      earlierText: "earlier words",
    });
  });

  test("two merged units and interleaved plain statements keep list order", () => {
    const statements = [
      analysing("0", 1, 2, "A un.", "A"),
      analysing("1", 2, 3, "A deux.", "A"),
      analysing("2", 4, 5, "B seul.", "B"),
      analysing("3", 6, 7, "C un.", "C"),
      analysing("4", 7, 8, "C deux.", "C"),
    ];
    const members = new Map([
      ["0", ["0", "1"]],
      ["3", ["3", "4"]],
    ]);

    const merged = mergeUnitStatements(statements, members);

    expect(merged.map((s) => s.id)).toEqual(["0", "2", "3"]);
    expect(merged[0].text).toBe("A un. A deux.");
    expect(merged[2].text).toBe("C un. C deux.");
  });
});
