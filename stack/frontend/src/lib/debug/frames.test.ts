import { describe, expect, test } from "vitest";

import { encodeQueryFrame, parseDebugResultsFrame } from "./frames";

describe("encodeQueryFrame", () => {
  test("serializes the query and seq", () => {
    expect(encodeQueryFrame({ q: "foxes", seq: 4 })).toBe('{"q":"foxes","seq":4}');
  });
});

describe("parseDebugResultsFrame", () => {
  test("decodes a results frame with hits", () => {
    const raw = JSON.stringify({
      type: "results",
      seq: 2,
      hits: [
        {
          title: "Red fox",
          url: "https://en.wikipedia.org/wiki/Red_fox",
          snippet: "foxes are fast",
          similarity: 0.91,
        },
      ],
    });
    expect(parseDebugResultsFrame(raw)).toEqual({
      type: "results",
      seq: 2,
      hits: [
        {
          title: "Red fox",
          url: "https://en.wikipedia.org/wiki/Red_fox",
          snippet: "foxes are fast",
          similarity: 0.91,
        },
      ],
    });
  });

  test("carries an error message when present", () => {
    const raw = JSON.stringify({ type: "results", seq: 1, hits: [], error: "search failed" });
    expect(parseDebugResultsFrame(raw)).toEqual({
      type: "results",
      seq: 1,
      hits: [],
      error: "search failed",
    });
  });

  test("drops a malformed hit but keeps the valid ones", () => {
    const raw = JSON.stringify({
      type: "results",
      seq: 5,
      hits: [
        { title: "ok", url: "https://a.test", snippet: "s", similarity: 0.5 },
        { title: "bad", url: "https://b.test", snippet: "s" }, // missing similarity
      ],
    });
    const frame = parseDebugResultsFrame(raw);
    expect(frame?.hits).toHaveLength(1);
    expect(frame?.hits[0]?.title).toBe("ok");
  });

  test("returns null on invalid JSON", () => {
    expect(parseDebugResultsFrame("{not json")).toBeNull();
  });

  test("returns null when the type is not results", () => {
    expect(parseDebugResultsFrame(JSON.stringify({ type: "other", seq: 1, hits: [] }))).toBeNull();
  });

  test("returns null when seq is missing", () => {
    expect(parseDebugResultsFrame(JSON.stringify({ type: "results", hits: [] }))).toBeNull();
  });

  test("returns null when hits is not an array", () => {
    expect(
      parseDebugResultsFrame(JSON.stringify({ type: "results", seq: 1, hits: "nope" })),
    ).toBeNull();
  });
});
