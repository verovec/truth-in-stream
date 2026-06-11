import { describe, expect, test } from "vitest";
import type { ResultFrame, SubtitleFrame } from "./frames";
import {
  applyFrame,
  clearAnalysing,
  emptyStatements,
  listStatements,
} from "./statements";

const subtitle = (
  id: string,
  start: number,
  text: string,
  end = start + 1,
): SubtitleFrame => ({ type: "subtitle", id, start, end, text });

const result = (
  id: string,
  start: number,
  text: string,
  matches: ResultFrame["segment"]["matches"] = [],
  extra: Partial<ResultFrame> = {},
): ResultFrame => ({
  type: "result",
  id,
  segment: { start, end: start + 1, text, matches, skipReason: undefined },
  ...extra,
});

describe("statements store", () => {
  test("a subtitle creates an analysing statement", () => {
    const state = applyFrame(emptyStatements(), subtitle("0", 1, "hello"));
    expect(listStatements(state)).toEqual([
      { id: "0", start: 1, end: 2, text: "hello", status: "analysing" },
    ]);
  });

  test("a subtitle carries its diarized speaker onto the statement", () => {
    const state = applyFrame(emptyStatements(), {
      type: "subtitle",
      id: "0",
      start: 1,
      end: 2,
      text: "hello",
      speaker: "A",
    });
    expect(listStatements(state)[0]).toMatchObject({
      speaker: "A",
      status: "analysing",
    });
  });

  test("a resolved verdict keeps the speaker its subtitle established", () => {
    // The result frame carries no speaker, so the checked statement must inherit
    // the label the subtitle set rather than dropping it.
    let state = applyFrame(emptyStatements(), {
      type: "subtitle",
      id: "0",
      start: 1,
      end: 2,
      text: "hello",
      speaker: "B",
    });
    state = applyFrame(state, result("0", 1, "hello"));
    expect(listStatements(state)[0]).toMatchObject({
      status: "checked",
      speaker: "B",
    });
  });

  test("backfills the speaker when the verdict arrived before its subtitle", () => {
    // Out-of-order delivery: the result resolves the statement first (no speaker
    // on the result frame), then the subtitle arrives carrying the label. The
    // checked statement must adopt it rather than the guard dropping it.
    let state = applyFrame(emptyStatements(), result("0", 1, "hello"));
    state = applyFrame(state, {
      type: "subtitle",
      id: "0",
      start: 1,
      end: 2,
      text: "hello",
      speaker: "A",
    });
    expect(listStatements(state)[0]).toMatchObject({
      status: "checked",
      speaker: "A",
    });
  });

  test("a result reconciles to its subtitle by id and resolves it", () => {
    let state = applyFrame(emptyStatements(), subtitle("0", 1, "hello"));
    state = applyFrame(
      state,
      result("0", 1, "hello", [
        {
          kind: "claim",
          claim: "c",
          verdict: "corroborates",
          sources: [],
          similarity: 0.8,
        },
      ]),
    );
    expect(listStatements(state)).toEqual([
      {
        id: "0",
        start: 1,
        end: 2,
        text: "hello",
        status: "checked",
        matches: [
          {
            kind: "claim",
            claim: "c",
            verdict: "corroborates",
            sources: [],
            similarity: 0.8,
          },
        ],
        skipReason: undefined,
        error: undefined,
      },
    ]);
  });

  test("a result that arrives before its subtitle still resolves", () => {
    let state = applyFrame(emptyStatements(), result("0", 1, "hello"));
    state = applyFrame(state, subtitle("0", 1, "hello"));
    // The subtitle must not downgrade an already-checked statement back to
    // analysing.
    expect(listStatements(state)[0]).toMatchObject({ status: "checked" });
  });

  test("orders statements by start time regardless of arrival order", () => {
    let state = applyFrame(emptyStatements(), subtitle("1", 5, "later"));
    state = applyFrame(state, subtitle("0", 1, "earlier"));
    expect(listStatements(state).map((s) => s.text)).toEqual([
      "earlier",
      "later",
    ]);
  });

  test("carries a skip reason and a non-fatal error through to the statement", () => {
    const state = applyFrame(
      emptyStatements(),
      result("3", 2, "what time is it", [], {
        error: "analysis failed",
        segment: {
          start: 2,
          end: 3,
          text: "what time is it",
          matches: [],
          skipReason: "not_a_claim",
        },
      }),
    );
    expect(listStatements(state)[0]).toMatchObject({
      status: "checked",
      skipReason: "not_a_claim",
      error: "analysis failed",
    });
  });

  test("a replayed statement supersedes the prior one at the same timestamp", () => {
    // First session resolves a verdict, then a reconnect (namespaced id) replays
    // the same moment: the timestamp must not appear twice.
    let state = applyFrame(emptyStatements(), result("s1:0", 1, "hello"));
    state = applyFrame(state, subtitle("s2:0", 1, "hello"));
    const list = listStatements(state);
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({ id: "s2:0", status: "analysing" });
  });

  test("clearAnalysing drops dangling in-flight statements but keeps verdicts", () => {
    let state = applyFrame(emptyStatements(), result("0", 1, "checked"));
    state = applyFrame(state, subtitle("1", 5, "in flight"));
    state = clearAnalysing(state);
    const list = listStatements(state);
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({ id: "0", status: "checked" });
  });

  test("applyFrame does not mutate the prior state", () => {
    const first = applyFrame(emptyStatements(), subtitle("0", 1, "hello"));
    const second = applyFrame(first, subtitle("1", 2, "world"));
    expect(listStatements(first)).toHaveLength(1);
    expect(listStatements(second)).toHaveLength(2);
  });
});
