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
// abbreviations and decimals inside a sentence do not split it the way a naive
// period split would.
const frenchSentenceSegmenter = new Intl.Segmenter("fr", {
  granularity: "sentence",
});

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
    for (const { segment } of frenchSentenceSegmenter.segment(normalized)) {
      const text = segment.trim();
      if (text === "") {
        continue;
      }
      const occurrence = (seenOnPage.get(text) ?? 0) + 1;
      seenOnPage.set(text, occurrence);
      sentences.push({ seq, page, text, occurrence });
      seq += 1;
    }
  });
  return sentences;
}
