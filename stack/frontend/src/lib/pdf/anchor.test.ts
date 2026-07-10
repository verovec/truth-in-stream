import { describe, expect, test } from "vitest";
import { anchorSentence, buildPageAnchorIndex } from "./anchor";

describe("buildPageAnchorIndex", () => {
  test("reproduces the extraction page text (join with a space, normalize)", () => {
    const index = buildPageAnchorIndex(["Le chômage", "a baissé."]);
    expect(index.text).toBe("Le chômage a baissé.");
    expect(index.itemAt).toHaveLength(index.text.length);
    expect(index.offsetAt).toHaveLength(index.text.length);
  });

  test("the separator between items belongs to no item", () => {
    const index = buildPageAnchorIndex(["a", "b"]);
    // "a b" - the middle space is the join separator, item -1.
    expect(index.text).toBe("a b");
    expect(index.itemAt[0]).toBe(0);
    expect(index.itemAt[1]).toBe(-1);
    expect(index.itemAt[2]).toBe(1);
  });
});

describe("anchorSentence", () => {
  test("anchors a sentence contained in one item", () => {
    const index = buildPageAnchorIndex(["Bonjour. Le chômage a baissé."]);
    expect(anchorSentence(index, "Le chômage a baissé.", 1)).toEqual({
      startItem: 0,
      startOffset: 9,
      endItem: 0,
      endOffset: 29,
    });
  });

  test("anchors a sentence spanning several items and lines", () => {
    // Three items, as pdf.js splits a wrapped sentence across visual lines.
    const index = buildPageAnchorIndex([
      "Le chômage",
      "a nettement",
      "baissé.",
    ]);
    const range = anchorSentence(index, "Le chômage a nettement baissé.", 1);
    expect(range).not.toBeNull();
    expect(range?.startItem).toBe(0);
    expect(range?.startOffset).toBe(0);
    expect(range?.endItem).toBe(2);
    // "baissé." is 7 chars; the whole item is the sentence tail.
    expect(range?.endOffset).toBe(7);
  });

  test("anchors across an item boundary with a preserved hard hyphen", () => {
    // "inter-" ends one visual line, "national" continues the next. A hard hyphen
    // is left intact (only soft hyphens are stripped), so extraction stored
    // "inter- national concerne" and the anchor index reproduces it identically -
    // both engines share the normalizer, so the sentence still anchors.
    const index = buildPageAnchorIndex(["inter-", "national concerne"]);
    expect(index.text).toBe("inter- national concerne");
    const range = anchorSentence(index, "inter- national concerne", 1);
    expect(range).not.toBeNull();
    // The 'i' of "inter-" starts the match; the item still owns raw offset 0.
    expect(range?.startItem).toBe(0);
    expect(range?.startOffset).toBe(0);
    expect(range?.endItem).toBe(1);
  });

  test("anchors a sentence with a ligature (NFKC expansion)", () => {
    const index = buildPageAnchorIndex(["Un conﬂit ouvert."]);
    expect(index.text).toBe("Un conflit ouvert.");
    const range = anchorSentence(index, "Un conflit ouvert.", 1);
    expect(range).not.toBeNull();
    expect(range?.startItem).toBe(0);
    expect(range?.startOffset).toBe(0);
    // The raw string keeps the single ligature code point, so the trailing '.'
    // sits at raw offset 16 and the exclusive end is 17.
    expect(range?.endItem).toBe(0);
    expect(range?.endOffset).toBe(17);
  });

  test("disambiguates duplicate sentences by occurrence", () => {
    const index = buildPageAnchorIndex(["Oui. Oui. Non."]);
    const first = anchorSentence(index, "Oui.", 1);
    const second = anchorSentence(index, "Oui.", 2);
    expect(first?.startOffset).toBe(0);
    expect(second?.startOffset).toBe(5);
    expect(first?.endOffset).toBe(4);
    expect(second?.endOffset).toBe(9);
  });

  test("returns null for a sentence absent from the page", () => {
    const index = buildPageAnchorIndex(["Le chômage a baissé."]);
    expect(anchorSentence(index, "La croissance repart.", 1)).toBeNull();
  });

  test("returns null when the requested occurrence does not exist", () => {
    const index = buildPageAnchorIndex(["Oui. Non."]);
    expect(anchorSentence(index, "Oui.", 2)).toBeNull();
  });

  test("returns null for an empty sentence", () => {
    const index = buildPageAnchorIndex(["Le chômage a baissé."]);
    expect(anchorSentence(index, "", 1)).toBeNull();
  });
});
