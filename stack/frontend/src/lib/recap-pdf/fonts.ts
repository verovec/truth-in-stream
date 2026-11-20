// The typefaces the recap PDF embeds: the app's own text and display faces, as
// static files a PDF generator can read. The file list is shared with
// scripts/copy-recap-fonts.mjs, which copies them into public/fonts/recap/.
import fontFiles from "./font-files.json";

export const TEXT_FONT = "Geist";
export const DISPLAY_FONT = "Spectral";

// FONT_BASE_URL is where the browser fetches the copied font files.
export const FONT_BASE_URL = "/fonts/recap/";

export type RecapFontFile = { file: string; source: string };

const FONT_MIME: Record<string, string> = { ttf: "font/ttf", woff: "font/woff" };

// fontDataUri wraps a font file's bytes as a data URI the PDF library accepts
// as a font source.
export function fontDataUri(file: string, bytes: Uint8Array): string {
  const extension = file.slice(file.lastIndexOf(".") + 1);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return `data:${FONT_MIME[extension] ?? "application/octet-stream"};base64,${btoa(binary)}`;
}

// fetchRecapFonts downloads every font file and returns them as data URIs
// keyed by file name. The fonts are fetched here rather than handed to the PDF
// library as URLs because the library caches a failed download for good: a
// single network blip would then break every later export until a reload.
// Fetched this way, a failure rejects before anything is registered and the
// next attempt starts clean.
export async function fetchRecapFonts(
  fetchFont: (url: string) => Promise<Response> = fetch,
): Promise<Map<string, string>> {
  const entries = await Promise.all(
    (fontFiles as RecapFontFile[]).map(async ({ file }) => {
      const response = await fetchFont(FONT_BASE_URL + file);
      if (!response.ok) {
        throw new Error(`font ${file}: HTTP ${response.status}`);
      }
      const bytes = new Uint8Array(await response.arrayBuffer());
      return [file, fontDataUri(file, bytes)] as const;
    }),
  );
  return new Map(entries);
}

type FontSource = {
  src: string;
  fontWeight: number;
  fontStyle: "normal" | "italic";
};

// FontRegistry is the slice of the PDF library's font API this module drives,
// so the registration is testable without the library.
export type FontRegistry = {
  register: (font: { family: string; fonts: FontSource[] }) => void;
  registerHyphenationCallback: (callback: (word: string) => string[]) => void;
};

// MAX_UNBROKEN is the longest run of characters printed without a break
// opportunity, and BREAK_CHUNK the size of the pieces a longer run is cut into.
const MAX_UNBROKEN = 30;
const BREAK_CHUNK = 15;

// breakOpportunities tells the PDF library where a word may be broken. Words
// are returned whole: the library's default hyphenates with English patterns,
// and a report quoting people verbatim must not hyphenate their French words
// wrongly. Only a run too long to be a word - a URL, an identifier - is cut
// into pieces, because left whole it would run out of its column.
export function breakOpportunities(word: string): string[] {
  if (word.length <= MAX_UNBROKEN) {
    return [word];
  }
  // Cut by code point, not by UTF-16 unit: a cut inside a surrogate pair would
  // leave two halves that print as missing glyphs.
  const chars = Array.from(word);
  const pieces: string[] = [];
  for (let i = 0; i < chars.length; i += BREAK_CHUNK) {
    pieces.push(chars.slice(i, i + BREAK_CHUNK).join(""));
  }
  return pieces;
}

// printable maps the few typographic characters neither typeface draws to
// ones both do, so they print as intended instead of as a missing-glyph box:
// the narrow no-break space French typography puts before "? ! ; :" and the
// other fixed-width spaces become a no-break space, the Unicode hyphens become
// the plain hyphen, and the soft hyphen, which is invisible by definition, is
// dropped. Every string that comes from data goes through it.
export function printable(text: string): string {
  return text
    .replace(/[\u2000-\u200a\u202f]/g, "\u00a0")
    .replace(/[\u2010\u2011]/g, "-")
    .replace(/\u00ad/g, "");
}

// displayText prepares a title or a name for the display face: printable, and
// with its no-break spaces turned into plain spaces, which that face cannot
// lay out. A title is a line or two, so losing the no-break behaviour there
// costs nothing; running text keeps its no-break spaces.
export function displayText(text: string): string {
  return printable(text).replace(/\u00a0/g, " ");
}

// DISPLAY_FONT_RANGES is the set of characters the display face's file really
// contains a glyph for. It was read from the file itself (the character map of
// spectral-latin-600-normal.woff in @fontsource/spectral 5.3.0), not from the
// subset's declared unicode-range, which is wider than what the file holds.
// Re-read it when that package is upgraded. The no-break space (U+00A0) is
// left out on purpose: the file maps it, but to a glyph the PDF library cannot
// measure - laying it out throws - so display text never carries one (see
// displayText).
export const DISPLAY_FONT_RANGES: readonly (readonly [number, number])[] = [
  [0x0020, 0x007e],
  [0x00a1, 0x00ac],
  [0x00ae, 0x00ff],
  [0x0131, 0x0131],
  [0x0152, 0x0153],
  [0x02bb, 0x02bc],
  [0x02c6, 0x02c6],
  [0x02da, 0x02da],
  [0x02dc, 0x02dc],
  [0x2013, 0x2014],
  [0x2018, 0x201a],
  [0x201c, 0x201e],
  [0x2022, 0x2022],
  [0x2026, 0x2026],
  [0x2032, 0x2033],
  [0x2039, 0x203a],
  [0x2044, 0x2044],
  [0x20ac, 0x20ac],
  [0x2122, 0x2122],
  [0x2212, 0x2212],
];

// displayFontFor picks the face for a title or a name. The display face is a
// subset and the PDF library has no per-glyph fallback, so a string with a
// character outside it ("Đoković", "Erdoğan") is set whole in the text face,
// which covers extended Latin, rather than printed with holes.
export function displayFontFor(text: string): string {
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if (!DISPLAY_FONT_RANGES.some(([from, to]) => code >= from && code <= to)) {
      return TEXT_FONT;
    }
  }
  return DISPLAY_FONT;
}

// registerRecapFonts registers every recap typeface with the PDF library.
// resolve maps a font file to something this runtime can read: a data URI
// built from fetched bytes in the browser, an absolute path in Node.
export function registerRecapFonts(
  registry: FontRegistry,
  resolve: (font: RecapFontFile) => string,
) {
  const families = new Map<string, FontSource[]>();
  for (const font of fontFiles) {
    const fonts = families.get(font.family) ?? [];
    fonts.push({
      src: resolve(font),
      fontWeight: font.weight,
      fontStyle: font.style === "italic" ? "italic" : "normal",
    });
    families.set(font.family, fonts);
  }
  for (const [family, fonts] of families) {
    registry.register({ family, fonts });
  }
  registry.registerHyphenationCallback(breakOpportunities);
}
