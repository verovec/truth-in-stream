// Generates the recap PDF in the browser and hands it to the user as a file.
// The PDF library (about 440 kB gzipped) and the document are loaded only when
// an export is requested, so the recap page itself does not pay for them.
import { fetchRecapFonts, registerRecapFonts } from "./fonts";
import { pdfFileName } from "./layout";
import type { RecapDocumentProps } from "./recap-document";

let fontsRegistered = false;

// buildRecapPdf renders the recap to a PDF blob. It is generated from the same
// recap object and the same sheet order the page is showing, never from a
// second computation, so the file cannot disagree with the screen.
export async function buildRecapPdf(props: RecapDocumentProps): Promise<Blob> {
  const [{ Font, pdf }, { RecapDocument }] = await Promise.all([
    import("@react-pdf/renderer"),
    import("./recap-document"),
  ]);
  if (!fontsRegistered) {
    // Registered only once every file has arrived, so a failed download
    // leaves nothing behind and the next export fetches again.
    const fonts = await fetchRecapFonts();
    registerRecapFonts(Font, (font) => fonts.get(font.file) ?? "");
    fontsRegistered = true;
  }
  // RecapDocument is a plain function of its props (no hooks), so it is called
  // directly: the library wants the Document element itself.
  return pdf(RecapDocument(props)).toBlob();
}

// REVOKE_DELAY_MS is how long the blob URL outlives the click. Some browsers
// start a download asynchronously, and a URL revoked in the same tick is gone
// before they read it.
const REVOKE_DELAY_MS = 60_000;

// saveBlob downloads a blob under a file name through a transient link, the
// one way to name a generated file from the browser.
export function saveBlob(blob: Blob, fileName: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), REVOKE_DELAY_MS);
}

// exportRecapPdf builds the PDF and downloads it, named after the video and
// the analysis date.
export async function exportRecapPdf(props: RecapDocumentProps): Promise<void> {
  const blob = await buildRecapPdf(props);
  saveBlob(blob, pdfFileName(props.recap.title, props.recap.analyzedAt));
}
