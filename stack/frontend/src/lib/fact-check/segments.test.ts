import { describe, expect, test } from "vitest";
import { findActiveSegmentIndex } from "./segments";

const span = (start: number, end: number) => ({ start, end });

describe("findActiveSegmentIndex", () => {
  const segments = [span(0, 4.5), span(4.5, 10), span(12, 20)];

  test("returns -1 for an empty list", () => {
    expect(findActiveSegmentIndex([], 3)).toBe(-1);
  });

  test("returns -1 before the first segment", () => {
    expect(findActiveSegmentIndex([span(5, 9)], 2)).toBe(-1);
  });

  test("finds the segment containing the time", () => {
    expect(findActiveSegmentIndex(segments, 2)).toBe(0);
    expect(findActiveSegmentIndex(segments, 7.25)).toBe(1);
    expect(findActiveSegmentIndex(segments, 15)).toBe(2);
  });

  test("treats a segment start as inclusive", () => {
    expect(findActiveSegmentIndex(segments, 0)).toBe(0);
    expect(findActiveSegmentIndex(segments, 12)).toBe(2);
  });

  test("hands a shared boundary to the next segment", () => {
    expect(findActiveSegmentIndex(segments, 4.5)).toBe(1);
  });

  test("returns -1 in a gap between segments", () => {
    expect(findActiveSegmentIndex(segments, 11)).toBe(-1);
  });

  test("returns -1 at and after the end of the last segment", () => {
    expect(findActiveSegmentIndex(segments, 20)).toBe(-1);
    expect(findActiveSegmentIndex(segments, 99)).toBe(-1);
  });

  test("resolves arbitrary jumps in either direction", () => {
    expect(findActiveSegmentIndex(segments, 15)).toBe(2);
    expect(findActiveSegmentIndex(segments, 1)).toBe(0);
    expect(findActiveSegmentIndex(segments, 10.5)).toBe(-1);
  });

  test("handles a long list at exact boundaries", () => {
    const many = Array.from({ length: 1000 }, (_, i) => span(i * 2, i * 2 + 1));
    expect(findActiveSegmentIndex(many, 0)).toBe(0);
    expect(findActiveSegmentIndex(many, 999 * 2)).toBe(999);
    expect(findActiveSegmentIndex(many, 501)).toBe(-1);
    expect(findActiveSegmentIndex(many, 500)).toBe(250);
  });
});
