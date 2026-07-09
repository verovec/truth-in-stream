// Shared PDF text normalization. The extractor (upload time) and the viewer's
// highlight anchoring (view time) MUST apply these exact rules to the same raw
// text, or a stored sentence will not substring-match the rendered text layer
// and its highlight silently fails to anchor. Keeping the rules in one module is
// what makes anchoring deterministic across the two engines.
//
// The three passes, in order:
//   1. NFKC - folds ligatures (ﬁ -> fi), compatibility forms, and composed
//      diacritics to a single canonical code-point sequence, so the two engines
//      never disagree on the bytes of a character.
//   2. De-hyphenation - a word split across a line break appears as a trailing
//      hyphen, whitespace, then the continuation ("inter-\nnational"); the hyphen
//      and the break are removed to rejoin the word. A genuine compound
//      ("arc-en-ciel") has no whitespace around its hyphens, so it is untouched;
//      requiring whitespace AFTER the hyphen (and a letter before and after) is
//      what tells a line break from a real hyphen.
//   3. Whitespace collapse - every run of whitespace becomes one space and the
//      ends are trimmed, so line and item boundaries from extraction do not
//      leak into the stored text.
const LINE_BROKEN_WORD = /(\p{L})-\s+(?=\p{L})/gu;
const WHITESPACE_RUN = /\s+/g;

// normalizeText applies the shared normalization to one raw string. It is pure
// and idempotent: normalizing an already-normalized string returns it unchanged.
export function normalizeText(raw: string): string {
  return raw
    .normalize("NFKC")
    .replace(LINE_BROKEN_WORD, "$1")
    .replace(WHITESPACE_RUN, " ")
    .trim();
}
