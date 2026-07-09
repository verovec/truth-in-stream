import { describe, expect, test } from "vitest";
import { formatTemplate, plural } from "./text";

describe("formatTemplate", () => {
  test("substitutes named placeholders", () => {
    expect(formatTemplate("Chargement de {title}", { title: "Débat" })).toBe(
      "Chargement de Débat",
    );
  });

  test("substitutes numbers and repeated placeholders", () => {
    expect(formatTemplate("{n} sur {n}", { n: 3 })).toBe("3 sur 3");
  });

  test("leaves unknown placeholders intact", () => {
    expect(formatTemplate("{n} restants", {})).toBe("{n} restants");
  });
});

describe("plural", () => {
  // French counts zero as singular; English does not. The helper must follow
  // the locale's own rule, not a shared n > 1 shortcut.
  test.each([
    ["fr", 0, "correspondance"],
    ["fr", 1, "correspondance"],
    ["fr", 2, "correspondances"],
  ] as const)("fr: %i", (locale, count, expected) => {
    expect(
      plural(locale, count, {
        one: "correspondance",
        other: "correspondances",
      }),
    ).toBe(expected);
  });

  test.each([
    ["en", 0, "matches"],
    ["en", 1, "match"],
    ["en", 2, "matches"],
  ] as const)("en: %i", (locale, count, expected) => {
    expect(plural(locale, count, { one: "match", other: "matches" })).toBe(
      expected,
    );
  });
});
