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

  test("parses a claims frame and marks each claim pending", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [
          { claim_id: "u0-0", text: "first", status: "pending" },
          { claim_id: "u0-1", text: "second", status: "pending" },
        ],
      }),
    );
    expect(frame).toEqual({
      type: "claims",
      id: "u0",
      claims: [
        { claimId: "u0-0", text: "first", status: "pending" },
        { claimId: "u0-1", text: "second", status: "pending" },
      ],
    });
  });

  test("skips a malformed claim entry rather than losing the whole frame", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [{ text: "no id" }, { claim_id: "u0-1", text: "kept" }],
      }),
    );
    expect(frame).toEqual({
      type: "claims",
      id: "u0",
      claims: [{ claimId: "u0-1", text: "kept", status: "pending" }],
    });
  });

  test("parses a verified claim_result with source, verdict, and normalized matches", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        source: "verified",
        verdict: "supports",
        confidence: 0.8,
        rationale: "the passage confirms it",
        matches: [
          {
            kind: "claim",
            claim: "x",
            verdict: "corroborates",
            sources: [],
            similarity: 0.7,
            evidence_id: "claim:42:0",
          },
        ],
      }),
    );
    expect(frame).toMatchObject({
      type: "claim_result",
      id: "u0",
      claimId: "u0-0",
      status: "verified",
      source: "verified",
      verdict: "supports",
      confidence: 0.8,
      rationale: "the passage confirms it",
    });
    expect((frame as { matches: unknown[] }).matches).toHaveLength(1);
  });

  test("parses a checking claim_result carrying only its status", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "checking",
      }),
    );
    expect(frame).toEqual({
      type: "claim_result",
      id: "u0",
      claimId: "u0-0",
      status: "checking",
    });
  });

  test("parses an unchecked claim_result with its capacity skip reason", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "unchecked",
        skip_reason: "not_checked",
      }),
    );
    expect(frame).toEqual({
      type: "claim_result",
      id: "u0",
      claimId: "u0-0",
      status: "unchecked",
      skipReason: "not_checked",
    });
  });

  test("drops an unrecognised source tag rather than rendering it", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        source: "guessed",
        verdict: "supports",
      }),
    );
    expect(frame).not.toHaveProperty("source");
    expect(frame).toMatchObject({ verdict: "supports" });
  });

  test("returns null for a claim_result with an unknown status", () => {
    expect(
      parseLiveFrame(
        JSON.stringify({
          type: "claim_result",
          id: "u0",
          claim_id: "u0-0",
          status: "exploded",
        }),
      ),
    ).toBeNull();
  });

  test("returns null for a claim_result missing its claim_id", () => {
    expect(
      parseLiveFrame(
        JSON.stringify({ type: "claim_result", id: "u0", status: "checking" }),
      ),
    ).toBeNull();
  });
});
