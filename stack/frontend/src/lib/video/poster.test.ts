import { describe, expect, test } from "vitest";
import { posterGradient, posterInitials } from "./poster";

describe("posterGradient", () => {
  test("is deterministic for a given seed", () => {
    expect(posterGradient("vid-1")).toBe(posterGradient("vid-1"));
  });

  test("differs across seeds", () => {
    expect(posterGradient("vid-1")).not.toBe(posterGradient("vid-2"));
  });

  test("produces a CSS linear-gradient", () => {
    expect(posterGradient("vid-1")).toMatch(/^linear-gradient\(/);
  });
});

describe("posterInitials", () => {
  test("takes the first and last word initials", () => {
    expect(posterInitials("Common Myths")).toBe("CM");
  });

  test("takes the first two letters of a single word", () => {
    expect(posterInitials("Interview")).toBe("IN");
  });

  test("falls back for an empty title", () => {
    expect(posterInitials("   ")).toBe("?");
  });
});
