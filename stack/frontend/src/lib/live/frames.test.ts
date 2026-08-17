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

  test("carries the unit's member segment ids", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [{ claim_id: "u0-0", text: "one" }],
        segment_ids: ["u0", "u1"],
      }),
    );
    expect(frame).toMatchObject({ segmentIds: ["u0", "u1"] });
  });

  test("drops a member id list carrying a malformed entry", () => {
    // A partial group would render a statement twice or lose it; the whole
    // list is dropped so the unit falls back to per-statement rendering.
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [{ claim_id: "u0-0", text: "one" }],
        segment_ids: ["u0", 7],
      }),
    );
    expect(frame).not.toBeNull();
    expect(frame).not.toHaveProperty("segmentIds");
  });

  test("drops a member id list carrying a duplicated id", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [{ claim_id: "u0-0", text: "one" }],
        segment_ids: ["u0", "u1", "u0"],
      }),
    );
    expect(frame).not.toBeNull();
    expect(frame).not.toHaveProperty("segmentIds");
  });

  test("a claims frame without segment ids keeps the legacy shape", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [{ claim_id: "u0-0", text: "one" }],
      }),
    );
    expect(frame).not.toHaveProperty("segmentIds");
  });

  test("carries a claim's verbatim quote and highlight spans", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [
          {
            claim_id: "u0-0",
            text: "Le chomage en France a baisse.",
            status: "pending",
            quote: "chomage a baisse",
            spans: [
              { segment_id: "3", start: 3, end: 19 },
              { segment_id: "4", start: 0, end: 5 },
            ],
          },
        ],
      }),
    );
    expect(frame).toEqual({
      type: "claims",
      id: "u0",
      claims: [
        {
          claimId: "u0-0",
          text: "Le chomage en France a baisse.",
          status: "pending",
          quote: "chomage a baisse",
          spans: [
            { segmentId: "3", start: 3, end: 19 },
            { segmentId: "4", start: 0, end: 5 },
          ],
        },
      ],
    });
  });

  test("drops a malformed span but keeps the claim and its valid spans", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u0",
        claims: [
          {
            claim_id: "u0-0",
            text: "kept",
            status: "pending",
            quote: "kept",
            spans: [
              { segment_id: "", start: 0, end: 2 },
              { segment_id: "3", start: 5, end: 5 },
              { segment_id: "3", start: -1, end: 2 },
              { segment_id: "3", start: 0.5, end: 2 },
              { segment_id: "3", start: 1, end: 3 },
            ],
          },
        ],
      }),
    );
    expect(frame).toEqual({
      type: "claims",
      id: "u0",
      claims: [
        {
          claimId: "u0-0",
          text: "kept",
          status: "pending",
          quote: "kept",
          spans: [{ segmentId: "3", start: 1, end: 3 }],
        },
      ],
    });
  });

  test("parses a verified claim_result with source, verdict, basis, and normalized matches", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        source: "verified",
        verdict: "credible",
        basis: "evidence",
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
            contribution: 0.7,
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
      verdict: "credible",
      basis: "evidence",
      confidence: 0.8,
      rationale: "the passage confirms it",
    });
    const matches = (frame as { matches: { evidenceId?: string; contribution?: number }[] }).matches;
    expect(matches).toHaveLength(1);
    expect(matches[0].evidenceId).toBe("claim:42:0");
    expect(matches[0].contribution).toBe(0.7);
  });

  test("parses the source label and url, distinct from the verdict source tag", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        source: "verified",
        verdict: "credible",
        source_label: "INSEE",
        source_url: "https://insee.fr/x",
      }),
    );
    expect(frame).toMatchObject({
      source: "verified",
      sourceLabel: "INSEE",
      sourceUrl: "https://insee.fr/x",
    });
  });

  test("drops a non-http source_url rather than rendering it in a link", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "credible",
        source_label: "INSEE",
        source_url: "javascript:alert(1)",
      }),
    );
    expect(frame).toMatchObject({ sourceLabel: "INSEE" });
    expect(frame).not.toHaveProperty("sourceUrl");
  });

  test("omits the source label and url when the verdict names no provider", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "unverifiable",
        basis: "knowledge",
      }),
    );
    expect(frame).not.toHaveProperty("sourceLabel");
    expect(frame).not.toHaveProperty("sourceUrl");
  });

  test("drops an unrecognised basis tag rather than rendering it", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "credible",
        basis: "hunch",
      }),
    );
    expect(frame).not.toHaveProperty("basis");
    expect(frame).toMatchObject({ verdict: "credible" });
  });

  test("parses a knowledge-basis claim_result with no citations", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "credible",
        basis: "knowledge",
        confidence: 0.55,
      }),
    );
    expect(frame).toMatchObject({ verdict: "credible", basis: "knowledge" });
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
        verdict: "credible",
      }),
    );
    expect(frame).not.toHaveProperty("source");
    expect(frame).toMatchObject({ verdict: "credible" });
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

  test("parses a speaker_tally frame with the verdict counts", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "speaker_tally",
        speaker: "A",
        credible: 1,
        disputed: 0,
        unverifiable: 2,
      }),
    );
    expect(frame).toEqual({
      type: "speaker_tally",
      speaker: "A",
      credible: 1,
      disputed: 0,
      unverifiable: 2,
      misleadingFraming: 0,
    });
  });

  test("returns null for a speaker_tally frame missing its speaker", () => {
    expect(
      parseLiveFrame(JSON.stringify({ type: "speaker_tally", credible: 1 })),
    ).toBeNull();
  });

  test("defaults missing speaker_tally counts to zero", () => {
    const frame = parseLiveFrame(
      JSON.stringify({ type: "speaker_tally", speaker: "B" }),
    );
    expect(frame).toEqual({
      type: "speaker_tally",
      speaker: "B",
      credible: 0,
      disputed: 0,
      unverifiable: 0,
      misleadingFraming: 0,
    });
  });

  test("parses the speaker_tally misleading_framing count", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "speaker_tally",
        speaker: "C",
        credible: 4,
        disputed: 1,
        unverifiable: 0,
        misleading_framing: 2,
      }),
    );
    expect(frame).toEqual({
      type: "speaker_tally",
      speaker: "C",
      credible: 4,
      disputed: 1,
      unverifiable: 0,
      misleadingFraming: 2,
    });
  });

  test("parses the two-axis literal verdict and flags on a claim_result", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        source: "verified",
        verdict: "credible",
        literal: "accurate",
        flags: ["cherry-picked", "missing-context"],
        basis: "evidence",
      }),
    );
    expect(frame).toMatchObject({
      type: "claim_result",
      claimId: "u0-0",
      verdict: "credible",
      literal: "accurate",
      flags: ["cherry-picked", "missing-context"],
    });
  });

  test("drops unrecognised flags but keeps the valid ones", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "disputed",
        literal: "inaccurate",
        flags: ["outdated", "fabricated", "misattributed"],
      }),
    );
    expect((frame as { flags: string[] }).flags).toEqual([
      "outdated",
      "misattributed",
    ]);
  });

  test("drops an empty flags array so a flagless claim carries no flags field", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "credible",
        literal: "accurate",
        flags: [],
      }),
    );
    expect(frame).not.toHaveProperty("flags");
    expect(frame).toMatchObject({ literal: "accurate" });
  });

  test("drops an unrecognised literal verdict rather than rendering it", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "credible",
        literal: "probably",
      }),
    );
    expect(frame).not.toHaveProperty("literal");
    expect(frame).toMatchObject({ verdict: "credible" });
  });

  test("parses a legacy claim_result with neither literal nor flags", () => {
    const frame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u0",
        claim_id: "u0-0",
        status: "verified",
        verdict: "credible",
        basis: "knowledge",
      }),
    );
    expect(frame).not.toHaveProperty("literal");
    expect(frame).not.toHaveProperty("flags");
    expect(frame).toMatchObject({ verdict: "credible", basis: "knowledge" });
  });
});
