import { describe, expect, test } from "vitest";
import { normalizeText, normalizeWithMap } from "./normalize";

describe("normalizeText", () => {
  test.each([
    ["collapses runs of whitespace", "le  budget\n\ta\r\ndoublé", "le budget a doublé"],
    ["trims leading and trailing whitespace", "  bonjour  ", "bonjour"],
    // NFKC folds ligatures and compatibility forms so the viewer and the
    // extractor agree on the same code points at anchor time.
    ["folds the fi ligature (NFKC)", "ﬁnance", "finance"],
    ["folds the fl ligature (NFKC)", "conﬂit", "conflit"],
    // De-hyphenation joins a word split across a line break (hyphen + whitespace
    // + continuation) while leaving genuine compounds intact.
    ["joins a line-broken word", "inter-\nnational", "international"],
    ["joins a space-broken hyphenation", "inter- national", "international"],
    ["keeps a genuine compound hyphen", "arc-en-ciel", "arc-en-ciel"],
    ["keeps a numeric range with spaced hyphen", "12 - 15", "12 - 15"],
    ["leaves an already-clean sentence unchanged", "La France compte 68 millions d'habitants.", "La France compte 68 millions d'habitants."],
  ])("%s", (_name, input, expected) => {
    expect(normalizeText(input)).toBe(expected);
  });

  test("is idempotent (normalizing twice equals once)", () => {
    const messy = "conﬂit inter-\nnational  déjà  normalisé";
    const once = normalizeText(messy);
    expect(normalizeText(once)).toBe(once);
  });

  test("an empty or whitespace-only string normalizes to empty", () => {
    expect(normalizeText("   \n\t  ")).toBe("");
    expect(normalizeText("")).toBe("");
  });
});

describe("normalizeWithMap", () => {
  // The anchoring guarantee: the mapped normalization must produce exactly the
  // text normalizeText produces, or a stored sentence (a normalizeText output)
  // would not substring-match the page text the overlay builds.
  test.each([
    ["le  budget\n\ta\r\ndoublé"],
    ["  bonjour  "],
    ["ﬁnance"],
    ["conﬂit"],
    ["inter-\nnational"],
    ["inter- national"],
    ["arc-en-ciel"],
    ["12 - 15"],
    ["La France compte 68 millions d'habitants."],
    ["conﬂit inter-\nnational  déjà  normalisé"],
    ["  \n\t  "],
    [""],
    // A precomposed accent (é as U+00E9) and a decomposed one (e + U+0301) must
    // both fold to the same output, and the cluster fold must match the whole
    // string fold.
    ["décomposée: décomposeé"],
    ["œuvre ﬀ ﬃ   nbsp"],
  ])("text equals normalizeText for %j", (input) => {
    expect(normalizeWithMap(input).text).toBe(normalizeText(input));
  });

  test("the map has one source index per output character, in range and ordered", () => {
    const raw = "conﬂit inter-\nnational  déjà";
    const { text, sourceIndex } = normalizeWithMap(raw);
    expect(sourceIndex).toHaveLength(text.length);
    for (let i = 0; i < sourceIndex.length; i += 1) {
      expect(sourceIndex[i]).toBeGreaterThanOrEqual(0);
      expect(sourceIndex[i]).toBeLessThan(raw.length);
      if (i > 0) {
        // Normalization preserves order, so provenance never moves backwards.
        expect(sourceIndex[i]).toBeGreaterThanOrEqual(sourceIndex[i - 1]);
      }
    }
  });

  test("an astral character's two code units keep distinct provenance", () => {
    // A surrogate pair (here an emoji) is preserved by NFKC, so each half maps to
    // its own raw code unit - otherwise a sentence ending on it would resolve one
    // code unit short and split the pair in the DOM range.
    const raw = "ab\u{1F600}";
    const { text, sourceIndex } = normalizeWithMap(raw);
    expect(text).toBe(raw);
    expect(sourceIndex).toEqual([0, 1, 2, 3]);
  });

  test("a base with stacked combining marks stays equivalent and in range", () => {
    // NFKC canonically reorders these two marks (dot-above then dot-below), so a
    // positional map would misattribute them; the cluster collapses to the base
    // index instead, keeping text equal to normalizeText and every source in range.
    const raw = "q̣̇";
    const { text, sourceIndex } = normalizeWithMap(raw);
    expect(text).toBe(normalizeText(raw));
    expect(sourceIndex).toHaveLength(text.length);
    for (const source of sourceIndex) {
      expect(source).toBeGreaterThanOrEqual(0);
      expect(source).toBeLessThan(raw.length);
    }
  });

  test("a ligature's expanded characters both map to the single source char", () => {
    const { text, sourceIndex } = normalizeWithMap("ﬁnance");
    expect(text).toBe("finance");
    // 'f' and 'i' both originate at the one ligature code point (index 0).
    expect(sourceIndex[0]).toBe(0);
    expect(sourceIndex[1]).toBe(0);
    // 'n' is the next raw code point.
    expect(sourceIndex[2]).toBe(1);
  });

  test("a collapsed whitespace run maps to the run's first character", () => {
    const raw = "le  budget";
    const { text, sourceIndex } = normalizeWithMap(raw);
    expect(text).toBe("le budget");
    // The single output space points at the first space of the run (index 2).
    expect(sourceIndex[2]).toBe(2);
    // 'b' points at the 'b' in the raw string (index 4).
    expect(sourceIndex[3]).toBe(4);
  });

  test("a line-broken word maps the continuation past the dropped hyphen and break", () => {
    const raw = "inter- national";
    const { text, sourceIndex } = normalizeWithMap(raw);
    expect(text).toBe("international");
    // "inter" maps to 0..4; the continuation 'n' skips the dropped "- " to the
    // raw 'n' at index 7.
    expect(sourceIndex[4]).toBe(4);
    expect(sourceIndex[5]).toBe(7);
  });
});
