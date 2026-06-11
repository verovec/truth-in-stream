import { describe, expect, test } from "vitest";
import { factCheckSourceFor } from "./fact-check-source";

describe("factCheckSourceFor", () => {
  test("maps a known curated sample to its batch processing source", () => {
    expect(factCheckSourceFor({ title: "Common Myths", kind: "sample" })).toBe(
      "common-myths.mp4",
    );
  });

  test("returns null for an unknown sample", () => {
    expect(factCheckSourceFor({ title: "Mystery Clip", kind: "sample" })).toBeNull();
  });

  test("returns null for uploads, which have no batch source yet", () => {
    expect(factCheckSourceFor({ title: "Common Myths", kind: "upload" })).toBeNull();
  });
});
