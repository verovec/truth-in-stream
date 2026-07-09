import { segmentPages, type ExtractedSentence } from "./segment";

// ScannedPdfError is thrown when a PDF yields no extractable text: a scanned or
// image-only document. The upload flow catches it to reject the file in the
// browser with a clear message before any server call, since the platform does
// not OCR.
export class ScannedPdfError extends Error {
  constructor() {
    super("scanned-pdf");
    this.name = "ScannedPdfError";
  }
}

// ExtractionResult is the browser extraction the upload flow posts: the page
// total and the ordered, normalized sentences.
export type ExtractionResult = {
  pageCount: number;
  sentences: ExtractedSentence[];
};

// PdfPageReader returns one raw text string per page. It is the seam between the
// pure segmentation logic and the browser-only pdf.js engine, so tests inject a
// fake and never load pdf.js.
export type PdfPageReader = (data: ArrayBuffer) => Promise<string[]>;

// defaultPageReader lazy-loads pdf.js only when a real extraction runs, so
// importing this module (a test, or a server bundle that never calls it) does
// not pull in the browser-only engine or its worker.
const defaultPageReader: PdfPageReader = (data) =>
  import("./pdfjs").then((module) => module.readPdfPages(data));

// extractDocument reads a PDF's per-page text in the browser, normalizes and
// segments it into ordered sentences, and reports the page count. A document
// with no extractable text is rejected with ScannedPdfError before it is
// returned, so a scanned PDF never reaches the server.
export async function extractDocument(
  file: File,
  readPages: PdfPageReader = defaultPageReader,
): Promise<ExtractionResult> {
  const data = await file.arrayBuffer();
  const pageTexts = await readPages(data);
  const sentences = segmentPages(pageTexts);
  if (sentences.length === 0) {
    throw new ScannedPdfError();
  }
  return { pageCount: pageTexts.length, sentences };
}
