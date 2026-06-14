import { describe, expect, test } from "vitest";
import { parseLiveFrame } from "./frames";

describe("parseLiveFrame", () => {
  test("parses a subtitle frame", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "subtitle",
        id: "0",
        start: 1.5,
        end: 3,
        text: "the earth is round",
      }),
    );
    expect(frame).toEqual({
      type: "subtitle",
      id: "0",
      start: 1.5,
      end: 3,
      text: "the earth is round",
    });
  });

  test("parses a subtitle frame's diarized speaker when present", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "subtitle",
        id: "1",
        start: 0,
        end: 1,
        text: "hello",
        speaker: "A",
      }),
    );
    expect(frame).toMatchObject({ type: "subtitle", speaker: "A" });
  });

  test("omits an empty speaker rather than carrying a blank label", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "subtitle",
        id: "1",
        start: 0,
        end: 1,
        text: "hello",
        speaker: "",
      }),
    );
    expect(frame).not.toHaveProperty("speaker");
  });

  test("parses a result frame and normalizes its segment", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "result",
        id: "0",
        start: 1.5,
        end: 3,
        text: "the earth is round",
        matches: [
          {
            kind: "claim",
            claim: "Earth is an oblate spheroid",
            verdict: "corroborates",
            sources: [{ title: "NASA", url: "https://nasa.gov" }],
            similarity: 0.91,
          },
        ],
      }),
    );
    expect(frame).toEqual({
      type: "result",
      id: "0",
      segment: {
        start: 1.5,
        end: 3,
        text: "the earth is round",
        matches: [
          {
            kind: "claim",
            claim: "Earth is an oblate spheroid",
            verdict: "corroborates",
            sources: [{ title: "NASA", url: "https://nasa.gov" }],
            similarity: 0.91,
          },
        ],
        skipReason: undefined,
      },
    });
  });

  test("carries a skip reason and a non-fatal error on a result frame", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "result",
        id: "2",
        start: 4,
        end: 5,
        text: "what time is it",
        matches: [],
        skip_reason: "not_a_claim",
        error: "analysis failed",
      }),
    );
    expect(frame).toMatchObject({
      type: "result",
      id: "2",
      error: "analysis failed",
      segment: { skipReason: "not_a_claim", matches: [] },
    });
  });

  test("parses an interim frame (text only, no id or timestamps)", () => {
    const frame = parseLiveFrame(
      JSON.stringify({ type: "interim", text: "the earth is" }),
    );
    expect(frame).toEqual({ type: "interim", text: "the earth is" });
  });

  test("returns null for an interim frame missing its text", () => {
    expect(parseLiveFrame(JSON.stringify({ type: "interim" }))).toBeNull();
  });

  test("returns null for a non-JSON payload", () => {
    expect(parseLiveFrame("not json")).toBeNull();
  });

  test("returns null for an unknown frame type", () => {
    expect(parseLiveFrame(JSON.stringify({ type: "ping" }))).toBeNull();
  });

  test("returns null when required fields are missing or mistyped", () => {
    expect(
      parseLiveFrame(JSON.stringify({ type: "subtitle", id: 0, start: 1 })),
    ).toBeNull();
    expect(
      parseLiveFrame(
        JSON.stringify({ type: "result", id: "1", start: "x", end: 2, text: "" }),
      ),
    ).toBeNull();
  });

  test("parses a consistency frame, mapping wire keys to camelCase", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "consistency",
        id: "1",
        earlier_id: "0",
        earlier_text: "the bridge opened in 1937",
        speaker: "A",
        rationale: "1937 versus 1940",
      }),
    );
    expect(frame).toEqual({
      type: "consistency",
      id: "1",
      earlierId: "0",
      earlierText: "the bridge opened in 1937",
      speaker: "A",
      rationale: "1937 versus 1940",
    });
  });

  test("omits optional consistency fields when absent", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "consistency",
        id: "1",
        earlier_id: "0",
        earlier_text: "earlier",
      }),
    );
    expect(frame).toEqual({
      type: "consistency",
      id: "1",
      earlierId: "0",
      earlierText: "earlier",
    });
  });

  test("returns null for a consistency frame missing its earlier reference", () => {
    expect(
      parseLiveFrame(JSON.stringify({ type: "consistency", id: "1" })),
    ).toBeNull();
  });
});
