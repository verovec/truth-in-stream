import { describe, expect, test } from "vitest";
import { getDictionary } from "./dictionaries";
import { fr } from "./dictionaries/fr";
import { en } from "./dictionaries/en";

// A structural fingerprint: object keys recurse, arrays collapse to their
// length so fr and en must agree on how many pillars, steps, sources and links
// exist. The `Dictionary` type already enforces key names; this catches the
// array-length drift the type cannot see.
function shape(value: unknown): unknown {
  if (Array.isArray(value)) {
    return { __array: value.length, of: value.map(shape) };
  }
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as object).sort()) {
      out[key] = shape((value as Record<string, unknown>)[key]);
    }
    return out;
  }
  return typeof value;
}

describe("getDictionary", () => {
  test("returns the French dictionary", async () => {
    const dict = await getDictionary("fr");
    expect(dict).toBe(fr);
    expect(dict.brand.name).toBe("jeminforme.fr");
  });

  test("returns the English dictionary", async () => {
    const dict = await getDictionary("en");
    expect(dict.nav.openApp).toBe("Open the app");
  });
});

describe("dictionary parity", () => {
  test("French and English share the exact same structure", () => {
    expect(shape(en)).toEqual(shape(fr));
  });

  test("French copy carries its diacritics", () => {
    expect(fr.hero.eyebrow).toContain("é");
    expect(fr.nav.howItWorks).toContain("ç");
    expect(fr.mission.title).toContain("responsabilité");
    expect(fr.how.steps[0].title).toBe("Écoute");
  });

  test("both locales point the app call to action at the login route", () => {
    for (const dict of [fr, en]) {
      const openApp = dict.footer.links.find((link) =>
        link.href === "/login",
      );
      expect(openApp).toBeDefined();
    }
  });
});

describe("app dictionary section", () => {
  test("French is the default voice of the analyser chrome", () => {
    expect(fr.app.summary.heading).toBe("Constats en direct");
    expect(fr.app.panel.heading).toBe("Analyse en direct");
    expect(fr.app.panel.subtitles).toBe("Sous-titres");
    expect(fr.app.library.heading).toBe("Bibliothèque");
  });

  test("English translates the same keys", () => {
    expect(en.app.summary.heading).toBe("Live findings");
    expect(en.app.panel.subtitles).toBe("Subtitles");
    expect(en.app.library.heading).toBe("Library");
  });

  test("both verdict systems carry labels in both locales", () => {
    expect(fr.app.claims.verdicts.credible).toBe("Fiable");
    expect(en.app.claims.verdicts.credible).toBe("Credible");
    expect(fr.app.claims.literal.accurate).toBe("Exact");
    expect(en.app.claims.literal.accurate).toBe("Accurate");
    expect(fr.app.legacy.verdicts.corroborates).toBe("Corrobore");
    expect(en.app.legacy.verdicts.corroborates).toBe("Corroborates");
  });

  test("the manipulation-flag vocabulary is exhaustive in both locales", () => {
    const flags = [
      "missing-context",
      "cherry-picked",
      "outdated",
      "misattributed",
      "misleading-causation",
    ] as const;
    for (const flag of flags) {
      expect(fr.app.claims.flags[flag]).toBeTruthy();
      expect(en.app.claims.flags[flag]).toBeTruthy();
    }
  });

  test("the login chrome is translated and free of the retired brand", () => {
    for (const dict of [fr, en]) {
      expect(JSON.stringify(dict.login)).not.toContain("Truth in Stream");
      expect(dict.login.signIn).toBeTruthy();
    }
  });
});
