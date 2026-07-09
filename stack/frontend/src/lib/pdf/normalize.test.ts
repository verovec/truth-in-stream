import { describe, expect, test } from "vitest";
import { normalizeText } from "./normalize";

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
