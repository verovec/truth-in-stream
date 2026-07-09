import { normalizeText } from "./normalize";

// ExtractedSentence is one normalized sentence in document order, ready to POST
// to the extraction endpoint. It mirrors the backend's document_sentences shape.
export type ExtractedSentence = {
  // seq is the document-order index, dense from 0 across all pages.
  seq: number;
  // page is 1-based.
  page: number;
  text: string;
  // occurrence is the 1-based nth identical text on that page, disambiguating
  // duplicate sentences so the viewer can anchor each to the right instance.
  occurrence: number;
};

// frenchSentenceSegmenter segments French text on sentence boundaries. It is
// built once - Intl.Segmenter construction is not free - and is locale-aware, so
// decimals inside a sentence do not split it the way a naive period split would.
const frenchSentenceSegmenter = new Intl.Segmenter("fr", {
  granularity: "sentence",
});

// French honorifics, titles, and common abbreviations whose trailing period is
// not a sentence end. V8's Intl.Segmenter does not load ICU's abbreviation
// exception list, so it terminates a sentence after "M.", "Mme.", an initial,
// etc., emitting a spurious two-char fragment and stripping the subject from the
// real claim ("M. Macron a declare ..." -> "M." + "Macron a declare ..."); the
// merge below re-joins such a fragment with the sentence it introduces.
const NON_TERMINAL_ABBREVIATIONS = new Set([
  "M.",
  "MM.",
  "Mme.",
  "Mmes.",
  "Mlle.",
  "Mlles.",
  "Dr.",
  "Dre.",
  "Pr.",
  "Pre.",
  "Me.",
  "Mgr.",
  "St.",
  "Ste.",
  "cf.",
  "p.",
  "pp.",
  "art.",
  "al.",
  "no.",
  "nos.",
  "ed.",
  "vol.",
  "chap.",
  "fig.",
]);

// A single uppercase (optionally accented) initial with a trailing period, e.g.
// "J." in "J. Dupont" - V8 also ends the sentence after a bare initial.
const TRAILING_INITIAL = /(?:^|\s)\p{Lu}\.$/u;

// endsWithNonTerminalAbbreviation reports whether an accumulated fragment ends on
// a token whose period does not close a sentence, so the next fragment folds in.
function endsWithNonTerminalAbbreviation(text: string): boolean {
  const lastSpace = text.lastIndexOf(" ");
  const lastToken = lastSpace === -1 ? text : text.slice(lastSpace + 1);
  return NON_TERMINAL_ABBREVIATIONS.has(lastToken) || TRAILING_INITIAL.test(text);
}

// splitSentences runs the locale segmenter, then folds any fragment that ends on
// a non-terminal abbreviation into the following one, yielding trimmed, non-empty
// sentences. Accumulation keeps the segmenter's own spacing so a merged sentence
// reads with the single space it placed after the abbreviation.
function splitSentences(normalized: string): string[] {
  const sentences: string[] = [];
  let pending = "";
  for (const { segment } of frenchSentenceSegmenter.segment(normalized)) {
    pending += segment;
    if (endsWithNonTerminalAbbreviation(pending.trimEnd())) {
      continue;
    }
    const text = pending.trim();
    if (text !== "") {
      sentences.push(text);
    }
    pending = "";
  }
  const tail = pending.trim();
  if (tail !== "") {
    sentences.push(tail);
  }
  return sentences;
}

// segmentPages turns per-page raw extracted text into the ordered sentence list.
// Each page's text is normalized with the shared rules first (so a stored
// sentence matches what the viewer renders), then segmented; blank fragments and
// blank pages are dropped. occurrence counts identical sentence text within a
// page, resetting per page.
export function segmentPages(pageTexts: string[]): ExtractedSentence[] {
  const sentences: ExtractedSentence[] = [];
  let seq = 0;
  pageTexts.forEach((raw, index) => {
    const page = index + 1;
    const normalized = normalizeText(raw);
    if (normalized === "") {
      return;
    }
    // occurrence bookkeeping is per page, so identical text on different pages
    // both start at 1.
    const seenOnPage = new Map<string, number>();
    for (const text of splitSentences(normalized)) {
      const occurrence = (seenOnPage.get(text) ?? 0) + 1;
      seenOnPage.set(text, occurrence);
      sentences.push({ seq, page, text, occurrence });
      seq += 1;
    }
  });
  return sentences;
}
