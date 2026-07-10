import type { DocumentSentence } from "@/lib/documents/api";
import type { LiveClaim } from "@/lib/live/claims";
import type { AnchoredSentence, HighlightVerdict } from "@/lib/pdf/overlay";
import { classifyDocumentSentence } from "./sentence-view";

// PageHighlightSentence is an AnchoredSentence plus the 1-based page it lives on,
// so the viewer can group highlights per rendered page. Everything the overlay
// needs to draw and anchor a sentence is here; nothing about the DOM is.
export type PageHighlightSentence = AnchoredSentence & { page: number };

// toHighlightSentences derives the in-PDF highlights from the analysed sentences,
// keeping only those the side panel emphasizes - a sentence that reached a
// credible or disputed verdict. Everything else (unverifiable, skipped, pending,
// errored) stays panel-only by design, so it is filtered out here rather than
// hidden later. A disputed claim wins the color over a credible one on the same
// sentence: the reader's eye should go to what does not hold.
export function toHighlightSentences(
  sentences: readonly DocumentSentence[],
): PageHighlightSentence[] {
  const highlights: PageHighlightSentence[] = [];
  for (const sentence of sentences) {
    const view = classifyDocumentSentence(sentence);
    if (view.kind !== "claims" || !view.substantive) {
      continue;
    }
    const deciding = decidingClaim(sentence.claims);
    if (deciding === null) {
      continue;
    }
    highlights.push({
      seq: sentence.seq,
      page: sentence.page,
      text: sentence.text,
      occurrence: sentence.occurrence,
      verdict: deciding.verdict,
      snippet: deciding.snippet,
    });
  }
  return highlights;
}

// decidingClaim picks the claim that sets a sentence's highlight: the first
// disputed claim if any (disputed dominates), otherwise the first credible one.
// The snippet is that claim's atomic text, falling back to the sentence-less empty
// string only if the claim carries none, so the tooltip always has something to
// show. Returns null when no claim is credible or disputed (a defensive guard;
// substantive sentences always have one).
function decidingClaim(
  claims: readonly LiveClaim[],
): { verdict: HighlightVerdict; snippet: string } | null {
  const disputed = claims.find((claim) => claim.verdict === "disputed");
  if (disputed) {
    return { verdict: "disputed", snippet: disputed.text };
  }
  const credible = claims.find((claim) => claim.verdict === "credible");
  if (credible) {
    return { verdict: "credible", snippet: credible.text };
  }
  return null;
}
