import { describe, expect, test } from "vitest";
import { normalizeSegment } from "./api";

describe("normalizeSegment", () => {
  test("maps a claim match, preserving order and verdict", () => {
    const segment = normalizeSegment({
      start: 0,
      end: 4.5,
      text: "hello world",
      matches: [
        {
          kind: "claim",
          claim: "the world exists",
          verdict: "corroborates",
          sources: [{ title: "Source", url: "https://example.com" }],
          similarity: 0.92,
        },
      ],
    });

    expect(segment).toEqual({
      start: 0,
      end: 4.5,
      text: "hello world",
      matches: [
        {
          kind: "claim",
          claim: "the world exists",
          verdict: "corroborates",
          sources: [{ title: "Source", url: "https://example.com" }],
          similarity: 0.92,
        },
      ],
      skipReason: undefined,
    });
  });

  test("carries a skip reason through onto the segment", () => {
    const segment = normalizeSegment({
      start: 4.5,
      end: 9,
      text: "quiet part",
      matches: [],
      skip_reason: "not_checked",
    });

    expect(segment).toMatchObject({ matches: [], skipReason: "not_checked" });
  });

  test("normalizes an evidence match into an excerpt with attribution", () => {
    const segment = normalizeSegment({
      start: 0,
      end: 4,
      text: "the great wall",
      matches: [
        {
          kind: "evidence",
          claim: "The Great Wall of China is a series of fortifications.",
          sources: [],
          similarity: 0.74,
          article: {
            title: "Great Wall of China",
            url: "https://en.wikipedia.org/wiki/Great_Wall_of_China",
          },
        },
      ],
    });

    expect(segment.matches).toEqual([
      {
        kind: "evidence",
        excerpt: "The Great Wall of China is a series of fortifications.",
        article: {
          title: "Great Wall of China",
          url: "https://en.wikipedia.org/wiki/Great_Wall_of_China",
        },
        similarity: 0.74,
      },
    ]);
  });

  test("keeps a malformed evidence match as evidence, never a fabricated verdict", () => {
    const segment = normalizeSegment({
      start: 0,
      end: 4,
      text: "the great wall",
      matches: [
        {
          kind: "evidence",
          claim: "The Great Wall of China is a series of fortifications.",
          sources: [],
          similarity: 0.74,
        },
      ],
    });

    expect(segment.matches).toEqual([
      {
        kind: "evidence",
        excerpt: "The Great Wall of China is a series of fortifications.",
        article: { title: "Wikipedia", url: "https://www.wikipedia.org" },
        similarity: 0.74,
      },
    ]);
  });

  test("reads a legacy match without a kind as a claim", () => {
    const segment = normalizeSegment({
      start: 0,
      end: 4,
      text: "legacy",
      matches: [
        {
          claim: "stored before evidence existed",
          verdict: "unclear",
          sources: [{ title: "Old", url: "https://old.example" }],
          similarity: 0.5,
        },
      ],
    });

    expect(segment.matches).toEqual([
      {
        kind: "claim",
        claim: "stored before evidence existed",
        verdict: "unclear",
        sources: [{ title: "Old", url: "https://old.example" }],
        similarity: 0.5,
      },
    ]);
  });

  test("defaults a claim match's missing fields", () => {
    const segment = normalizeSegment({
      start: 0,
      end: 4,
      text: "sparse",
      matches: [{ kind: "claim", similarity: 0.3 }],
    });

    expect(segment.matches).toEqual([
      { kind: "claim", claim: "", verdict: "unclear", sources: [], similarity: 0.3 },
    ]);
  });

  test("maps the confidence score onto the camelCase domain shape", () => {
    const segment = normalizeSegment({
      start: 0,
      end: 4,
      text: "checked",
      matches: [{ kind: "claim", claim: "c", verdict: "corroborates", similarity: 0.9 }],
      confidence: {
        score: 0.82,
        supporting: 1.4,
        contradicting: 0.3,
        evidence_items: 3,
      },
    });

    expect(segment.confidence).toEqual({
      score: 0.82,
      supporting: 1.4,
      contradicting: 0.3,
      evidenceItems: 3,
    });
  });

  test("leaves confidence absent when the wire carries none", () => {
    const segment = normalizeSegment({
      start: 4.5,
      end: 9,
      text: "skipped",
      matches: [],
      skip_reason: "not_checked",
    });

    expect(segment.confidence).toBeUndefined();
  });
});
