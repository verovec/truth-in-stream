import "client-only";
import { pdfjs } from "react-pdf";

// The pdf.js worker is served from public/ (copied there at build time by
// scripts/copy-pdf-worker.mjs). react-pdf's bundler asset-URL pattern for the
// worker is unreliable under Turbopack, and standalone output ships public/
// automatically, so we point workerSrc at the copied file. Importing pdfjs from
// react-pdf (never a separate pdfjs-dist dependency) keeps the worker and the
// viewer on the exact same pinned build, avoiding an API version skew.
pdfjs.GlobalWorkerOptions.workerSrc = "/pdf.worker.min.mjs";

// readPdfPages runs a text-extraction-only pass: getDocument plus per-page
// getTextContent, with no canvas render. It returns one raw text string per
// page (items joined with a space); the shared normalizer strips soft hyphens
// and collapses whitespace downstream. pdfjs-dist 5.4.296 getTextContent already
// combines within-word glyphs into a single item, so the space join separates
// words, not fragments of one. The optional signal is checked before each page
// so a cancelled upload stops the parse (destroying the doc) within one page
// instead of reading the whole document.
export async function readPdfPages(
  data: ArrayBuffer,
  signal?: AbortSignal,
): Promise<string[]> {
  signal?.throwIfAborted();
  const loadingTask = pdfjs.getDocument({ data });
  const doc = await loadingTask.promise;
  try {
    const pages: string[] = [];
    for (let pageNumber = 1; pageNumber <= doc.numPages; pageNumber += 1) {
      signal?.throwIfAborted();
      const page = await doc.getPage(pageNumber);
      const content = await page.getTextContent();
      const text = content.items
        .map((item) => ("str" in item ? item.str : ""))
        .join(" ");
      pages.push(text);
      page.cleanup();
    }
    return pages;
  } finally {
    await doc.destroy();
  }
}
