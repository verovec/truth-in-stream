import { describe, expect, test } from "vitest";
import { segmentPages } from "./segment";

describe("segmentPages", () => {
  test("segments a page into sentences with dense document-order seq", () => {
    const sentences = segmentPages(["La France compte 68 millions d'habitants. Le budget a doublé."]);
    expect(sentences).toEqual([
      { seq: 0, page: 1, text: "La France compte 68 millions d'habitants.", occurrence: 1 },
      { seq: 1, page: 1, text: "Le budget a doublé.", occurrence: 1 },
    ]);
  });

  test("numbers pages from 1 and continues seq across pages", () => {
    const sentences = segmentPages(["Une phrase.", "Deux. Trois."]);
    expect(sentences.map((s) => [s.seq, s.page])).toEqual([
      [0, 1],
      [1, 2],
      [2, 2],
    ]);
  });

  test("assigns occurrence per identical sentence text per page", () => {
    // The same sentence twice on page 1, once on page 2: occurrences reset per page.
    const sentences = segmentPages(["Le budget augmente. Le budget augmente.", "Le budget augmente."]);
    expect(sentences).toEqual([
      { seq: 0, page: 1, text: "Le budget augmente.", occurrence: 1 },
      { seq: 1, page: 1, text: "Le budget augmente.", occurrence: 2 },
      { seq: 2, page: 2, text: "Le budget augmente.", occurrence: 1 },
    ]);
  });

  test("normalizes each page's text before segmenting", () => {
    // A ligature and a line-broken word are folded by the shared normalizer, so a
    // stored sentence matches what the viewer will render.
    const sentences = segmentPages(["Le conﬂit inter-\nnational perdure."]);
    expect(sentences).toEqual([
      { seq: 0, page: 1, text: "Le conflit international perdure.", occurrence: 1 },
    ]);
  });

  test("drops blank sentences and blank pages", () => {
    const sentences = segmentPages(["", "   \n  ", "Une seule phrase."]);
    expect(sentences).toEqual([
      { seq: 0, page: 3, text: "Une seule phrase.", occurrence: 1 },
    ]);
  });

  test("a document with no extractable text yields no sentences", () => {
    expect(segmentPages(["", "  "])).toEqual([]);
  });
});
