import { describe, expect, test } from "vitest";
import type { DocumentSentence } from "@/lib/documents/api";
import type { LiveClaim } from "@/lib/live/claims";
import { classifyDocumentSentence } from "./sentence-view";

function claim(over: Partial<LiveClaim> = {}): LiveClaim {
  return {
    claimId: "c1",
    text: "Une affirmation.",
    status: "verified",
    confidence: 0.8,
    matches: [],
    ...over,
  };
}

function sentence(over: Partial<DocumentSentence> = {}): DocumentSentence {
  return {
    seq: 0,
    page: 1,
    text: "Une phrase.",
    occurrence: 1,
    skipReason: "",
    claims: [],
    ...over,
  };
}

describe("classifyDocumentSentence", () => {
  test("a credible verdict is a substantive claims sentence", () => {
    const view = classifyDocumentSentence(
      sentence({ claims: [claim({ verdict: "credible" })] }),
    );
    expect(view).toEqual({ kind: "claims", substantive: true });
  });

  test("a disputed verdict is substantive", () => {
    const view = classifyDocumentSentence(
      sentence({ claims: [claim({ verdict: "disputed" })] }),
    );
    expect(view).toEqual({ kind: "claims", substantive: true });
  });

  test("an unverifiable-only sentence is claims but not substantive", () => {
    const view = classifyDocumentSentence(
      sentence({ claims: [claim({ verdict: "unverifiable" })] }),
    );
    expect(view).toEqual({ kind: "claims", substantive: false });
  });

  test("an errored claim still renders as a (non-substantive) claims sentence", () => {
    const view = classifyDocumentSentence(
      sentence({ claims: [claim({ status: "error", verdict: undefined })] }),
    );
    expect(view).toEqual({ kind: "claims", substantive: false });
  });

  test("a skipped sentence carries its reason", () => {
    expect(
      classifyDocumentSentence(sentence({ skipReason: "not_a_claim" })),
    ).toEqual({ kind: "skipped", reason: "not_a_claim" });
    expect(
      classifyDocumentSentence(sentence({ skipReason: "not_covered" })),
    ).toEqual({ kind: "skipped", reason: "not_covered" });
  });

  test("an empty, unskipped sentence is still pending", () => {
    expect(classifyDocumentSentence(sentence())).toEqual({ kind: "pending" });
  });
});
