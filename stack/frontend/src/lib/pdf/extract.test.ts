import { describe, expect, test } from "vitest";
import { extractDocument, ScannedPdfError } from "./extract";

function pdfFile(): File {
  return new File([new Uint8Array([1, 2, 3])], "rapport.pdf", {
    type: "application/pdf",
  });
}

describe("extractDocument", () => {
  test("reads pages, segments, and reports the page count", async () => {
    const result = await extractDocument(pdfFile(), async () => [
      "La France compte 68 millions d'habitants. Le budget a doublé.",
      "Une phrase sur la seconde page.",
    ]);
    expect(result.pageCount).toBe(2);
    expect(result.sentences).toEqual([
      { seq: 0, page: 1, text: "La France compte 68 millions d'habitants.", occurrence: 1 },
      { seq: 1, page: 1, text: "Le budget a doublé.", occurrence: 1 },
      { seq: 2, page: 2, text: "Une phrase sur la seconde page.", occurrence: 1 },
    ]);
  });

  test("rejects a PDF with no extractable text (scanned) before any server call", async () => {
    await expect(
      extractDocument(pdfFile(), async () => ["", "   "]),
    ).rejects.toBeInstanceOf(ScannedPdfError);
  });

  test("passes the file bytes to the reader", async () => {
    let received: ArrayBuffer | null = null;
    await extractDocument(pdfFile(), async (data) => {
      received = data;
      return ["Une phrase."];
    });
    expect(received).not.toBeNull();
    expect(new Uint8Array(received!)).toEqual(new Uint8Array([1, 2, 3]));
  });
});
