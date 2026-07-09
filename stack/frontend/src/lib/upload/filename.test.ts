import { describe, expect, test } from "vitest";
import { deriveTitle, failureMessage } from "./filename";

describe("deriveTitle", () => {
  test.each([
    ["strips a single extension", "Rapport annuel.pdf", "Rapport annuel"],
    ["strips only the last extension", "archive.tar.gz", "archive.tar"],
    ["keeps a name with no extension", "README", "README"],
    ["trims surrounding whitespace", "  Budget 2026 .pdf", "Budget 2026"],
    ["falls back for an extension-only name", ".pdf", "Untitled"],
    ["falls back for a bare dot", ".", "Untitled"],
    ["falls back for an empty name", "", "Untitled"],
    ["falls back for a whitespace-only base", "   .pdf", "Untitled"],
  ])("%s", (_name, input, expected) => {
    expect(deriveTitle(input, "Untitled")).toBe(expected);
  });
});

describe("failureMessage", () => {
  test("returns an Error's message", () => {
    expect(failureMessage(new Error("boom"))).toBe("boom");
  });

  test("returns null for a non-Error value", () => {
    expect(failureMessage("boom")).toBeNull();
    expect(failureMessage(null)).toBeNull();
  });
});
