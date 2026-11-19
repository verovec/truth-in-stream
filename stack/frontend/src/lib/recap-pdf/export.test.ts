import { existsSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { debateRecap } from "@/test/recap";
import fontFiles from "./font-files.json";
import {
  breakOpportunities,
  displayFontFor,
  displayText,
  fetchRecapFonts,
  printable,
  fontDataUri,
  type FontRegistry,
  registerRecapFonts,
} from "./fonts";

const toBlob = vi.fn(async () => new Blob(["%PDF-"], { type: "application/pdf" }));
const register = vi.fn();
const registerHyphenationCallback = vi.fn();
const pdf = vi.fn(() => ({ toBlob }));

vi.mock("@react-pdf/renderer", () => ({
  Font: { register, registerHyphenationCallback },
  pdf,
}));

vi.mock("./recap-document", () => ({
  RecapDocument: (props: unknown) => ({ document: props }),
}));

const fontResponse = () => new Response(new Uint8Array([0, 1, 2, 3]));

beforeEach(() => {
  // A fresh module per test: the "fonts registered" flag must not leak.
  vi.resetModules();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("recap fonts", () => {
  test("every listed font file exists in its pinned package", () => {
    for (const font of fontFiles) {
      expect(existsSync(join(process.cwd(), "node_modules", font.source))).toBe(true);
    }
  });

  test("registers each family with its weights and styles", () => {
    const registry: FontRegistry = {
      register: vi.fn(),
      registerHyphenationCallback: vi.fn(),
    };
    registerRecapFonts(registry, (font) => `/x/${font.file}`);

    expect(registry.register).toHaveBeenCalledWith({
      family: "Geist",
      fonts: [
        { src: "/x/Geist-Regular.ttf", fontWeight: 400, fontStyle: "normal" },
        { src: "/x/Geist-Italic.ttf", fontWeight: 400, fontStyle: "italic" },
        { src: "/x/Geist-SemiBold.ttf", fontWeight: 600, fontStyle: "normal" },
      ],
    });
    expect(registry.register).toHaveBeenCalledWith({
      family: "Spectral",
      fonts: [
        { src: "/x/spectral-latin-600-normal.woff", fontWeight: 600, fontStyle: "normal" },
      ],
    });
    expect(registry.registerHyphenationCallback).toHaveBeenCalledWith(breakOpportunities);
  });

  test("words are never hyphenated, but a run too long to be a word may break", () => {
    expect(breakOpportunities("anticonstitutionnellement")).toEqual([
      "anticonstitutionnellement",
    ]);
    const url = "https://example.org/un/chemin/tres/long/sans/aucun/espace";
    const pieces = breakOpportunities(url);
    expect(pieces.length).toBeGreaterThan(1);
    expect(pieces.join("")).toBe(url);
    expect(Math.max(...pieces.map((piece) => piece.length))).toBeLessThanOrEqual(15);
  });

  test("a long run is cut between characters, never inside one", () => {
    const run = "😀".repeat(40);
    const pieces = breakOpportunities(run);
    expect(pieces.join("")).toBe(run);
    for (const piece of pieces) {
      expect(Array.from(piece)).toHaveLength(piece.length / 2);
      expect(piece.isWellFormed()).toBe(true);
    }
  });

  test("the display face is used only for text it can draw", () => {
    expect(displayFontFor("Le grand débat : « l'œil » – 100 €")).toBe("Spectral");
    expect(displayFontFor("Hélène Rousseau… “cité” ‘ici’")).toBe("Spectral");
    expect(displayFontFor("Novak Đoković")).toBe("Geist");
    expect(displayFontFor("Recep Erdoğan")).toBe("Geist");
    expect(displayFontFor("Дебаты")).toBe("Geist");
    // Inside the subset's declared range, yet absent from the file.
    expect(displayFontFor("Jean\u2011Luc")).toBe("Geist");
    expect(displayFontFor("Vrai\u202f?")).toBe("Geist");
    expect(displayFontFor("co\u00adopération")).toBe("Geist");
  });

  test("typography neither face draws is mapped to what both do", () => {
    expect(printable("Vrai\u202f? Oui\u2009!")).toBe("Vrai\u00a0? Oui\u00a0!");
    expect(printable("Jean\u2011Luc et Marie\u2010Claire")).toBe("Jean-Luc et Marie-Claire");
    expect(printable("co\u00adopération")).toBe("coopération");
    expect(printable("Le chômage a baissé – « vraiment » ?")).toBe(
      "Le chômage a baissé – « vraiment » ?",
    );
    // Once printable, such a name is back in the display face.
    expect(displayFontFor(printable("Jean\u2011Luc Mélenchon"))).toBe("Spectral");
  });

  test("display text carries no no-break space, which the display face cannot lay out", () => {
    expect(displayText("Débat\u00a0: vrai\u202f?")).toBe("Débat : vrai ?");
    expect(displayFontFor(displayText("Débat\u00a0: vrai\u202f?"))).toBe("Spectral");
    expect(displayFontFor("Débat\u00a0:")).toBe("Geist");
  });

  test("a font file becomes a data URI of its type", () => {
    expect(fontDataUri("a.ttf", new Uint8Array([0, 1, 2, 3]))).toBe(
      "data:font/ttf;base64,AAECAw==",
    );
    expect(fontDataUri("a.woff", new Uint8Array([255]))).toBe("data:font/woff;base64,/w==");
  });

  test("fetches every file from the public copies", async () => {
    const fetchFont = vi.fn(async (url: string) => {
      void url;
      return fontResponse();
    });
    const fonts = await fetchRecapFonts(fetchFont);

    expect(fetchFont.mock.calls.map(([url]) => url).sort()).toEqual(
      fontFiles.map((font) => `/fonts/recap/${font.file}`).sort(),
    );
    expect([...fonts.keys()].sort()).toEqual(fontFiles.map((font) => font.file).sort());
    expect(fonts.get("Geist-Regular.ttf")).toBe("data:font/ttf;base64,AAECAw==");
  });

  test("a missing font file fails the whole fetch with its name", async () => {
    const fetchFont = vi.fn(async (url: string) =>
      url.endsWith("Geist-Italic.ttf") ? new Response(null, { status: 404 }) : fontResponse(),
    );
    await expect(fetchRecapFonts(fetchFont)).rejects.toThrow("font Geist-Italic.ttf: HTTP 404");
  });
});

describe("exportRecapPdf", () => {
  const props = () => {
    const recap = debateRecap();
    return {
      recap,
      sheets: recap.speakers.slice(0, 2),
      t: fr.app,
      locale: "fr" as const,
      brandName: "jeminforme.fr",
    };
  };

  function stubBrowser(fetchFont: () => Promise<Response> = async () => fontResponse()) {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(fetchFont);
    const createObjectURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:recap");
    const revokeObjectURL = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const downloads: { href: string; download: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      downloads.push({ href: this.href, download: this.download });
    });
    return { fetchSpy, createObjectURL, revokeObjectURL, downloads };
  }

  test("builds the document from the props it is given and downloads it under the video's name", async () => {
    const { downloads } = stubBrowser();
    const { exportRecapPdf } = await import("./export");
    const given = props();

    await exportRecapPdf(given);

    // The same recap and sheet order the page holds, not a second computation.
    expect(pdf).toHaveBeenCalledWith({ document: given });
    expect(downloads).toEqual([
      { href: "blob:recap", download: "le-grand-debat-2026-11-19.pdf" },
    ]);
    // The transient link does not stay in the page.
    expect(document.querySelector("a[download]")).toBeNull();
  });

  test("keeps the blob URL alive after the click, then releases it", async () => {
    vi.useFakeTimers();
    const { revokeObjectURL } = stubBrowser();
    const { exportRecapPdf } = await import("./export");

    await exportRecapPdf(props());

    // A browser that starts the download asynchronously still finds the blob.
    expect(revokeObjectURL).not.toHaveBeenCalled();
    vi.advanceTimersByTime(60_000);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:recap");
  });

  test("registers the fetched fonts on the first export and never again", async () => {
    const { fetchSpy } = stubBrowser();
    const { exportRecapPdf } = await import("./export");

    await exportRecapPdf(props());

    expect(fetchSpy).toHaveBeenCalledTimes(fontFiles.length);
    const families = register.mock.calls.map(([font]) => font.family).sort();
    expect(families).toEqual(["Geist", "Spectral"]);
    for (const [font] of register.mock.calls) {
      for (const source of font.fonts) {
        expect(source.src).toMatch(/^data:font\/(ttf|woff);base64,/);
      }
    }

    await exportRecapPdf(props());
    expect(fetchSpy).toHaveBeenCalledTimes(fontFiles.length);
    expect(register).toHaveBeenCalledTimes(2);
  });

  test("a failed font download registers nothing, and the next export starts clean", async () => {
    let healthy = false;
    const { fetchSpy, downloads } = stubBrowser(async () =>
      healthy ? fontResponse() : new Response(null, { status: 503 }),
    );
    const { exportRecapPdf } = await import("./export");

    await expect(exportRecapPdf(props())).rejects.toThrow(/HTTP 503/);
    expect(register).not.toHaveBeenCalled();
    expect(downloads).toEqual([]);

    // The network is back: the same page exports without a reload.
    healthy = true;
    await exportRecapPdf(props());
    expect(fetchSpy).toHaveBeenCalledTimes(2 * fontFiles.length);
    expect(register).toHaveBeenCalledTimes(2);
    expect(downloads).toHaveLength(1);
  });

  test("a failed generation rejects and downloads nothing", async () => {
    const { downloads } = stubBrowser();
    toBlob.mockRejectedValueOnce(new Error("layout failed"));
    const { exportRecapPdf } = await import("./export");

    await expect(exportRecapPdf(props())).rejects.toThrow("layout failed");
    expect(downloads).toEqual([]);
  });
});
