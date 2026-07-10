import type { DocumentSentence } from "@/lib/documents/api";

// DocumentSentenceView is how one analysed sentence presents in the fact-check
// panel. A sentence that produced claims renders them through the shared claim
// list (each claim carries its own verdict or error state); a sentence the gate
// skipped shows its reason; a sentence not yet reached mid-analysis is pending.
// substantive marks a claims sentence that reached at least one credible or
// disputed verdict, so the panel can emphasize it over an invérifiable-only or
// errored one exactly as the video fact-check section does.
export type DocumentSentenceView =
  | { kind: "claims"; substantive: boolean }
  | { kind: "skipped"; reason: "not_a_claim" | "not_covered" }
  | { kind: "pending" };

// classifyDocumentSentence is a pure function of one sentence's stored state,
// kept separate from the row component so its branches are table-tested without
// rendering. A sentence with any claims renders the claim list (the list itself
// shows a verified verdict or an errored claim); otherwise a recognised skip
// reason mutes the row, and an empty, unskipped sentence is still being analysed.
export function classifyDocumentSentence(
  sentence: DocumentSentence,
): DocumentSentenceView {
  if (sentence.claims.length > 0) {
    const substantive = sentence.claims.some(
      (claim) => claim.verdict === "credible" || claim.verdict === "disputed",
    );
    return { kind: "claims", substantive };
  }
  if (
    sentence.skipReason === "not_a_claim" ||
    sentence.skipReason === "not_covered"
  ) {
    return { kind: "skipped", reason: sentence.skipReason };
  }
  return { kind: "pending" };
}
