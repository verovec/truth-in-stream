// Layout rules of the recap PDF, kept free of any rendering library so they are
// unit-testable: the page geometry, how tall a speaker sheet is estimated to
// be, which of the two sheet layouts it gets, how its content splits into
// columns, and what the file is called.
import {
  QUOTE_LIMIT,
  REPORT_TIME_ZONE,
  type RecapSpeaker,
  type RecapStatement,
  truncateQuote,
} from "@/lib/video/recap";

// A4 portrait, in PDF points. BOTTOM is taller than MARGIN because every page
// after the cover reserves it for the footer: content never enters it.
export const PAGE = {
  width: 595.28,
  height: 841.89,
  margin: 40,
  bottom: 64,
} as const;

export const CONTENT_WIDTH = PAGE.width - 2 * PAGE.margin;
export const CONTENT_HEIGHT = PAGE.height - PAGE.margin - PAGE.bottom;

// SHEET_HEADER_HEIGHT is the space the head of a sheet takes on its first
// page: the speaker's name on one line, the name provenance, the figures row
// and the distribution bar. NAME_LINE is one more line of a name too long for
// one, NAME_CHARS_PER_LINE how many characters of the 20 pt display face fit
// the content width, and FOOTNOTE_HEIGHT the note counting the statements a
// sheet does not detail.
export const SHEET_HEADER_HEIGHT = 132;
const NAME_LINE = 26;
const NAME_CHARS_PER_LINE = 44;
const FOOTNOTE_HEIGHT = 26;

// BREAKABLE_HEIGHT is the estimated height from which a statement may be split
// across pages. Statements are normally kept whole, but one with an unusually
// long justification could be taller than a page, and an unbreakable block
// taller than a page cannot be placed anywhere.
export const BREAKABLE_HEIGHT = 300;

export const COLUMN_GAP = 16;

// SheetVariant is one of the two sheet layouts. A sheet that fits one page in
// a single readable column uses it; a longer one switches to the dense
// two-column layout, which holds roughly twice as much.
export type SheetVariant = "single" | "dense";

export const VARIANTS = {
  single: { fontSize: 10, columns: 1 },
  dense: { fontSize: 8.5, columns: 2 },
} as const satisfies Record<SheetVariant, { fontSize: number; columns: number }>;

// SheetBlock is one vertical unit of a sheet's body: a section heading, a
// statement, or the sentence that stands in for an empty section.
export type SheetBlock =
  | { kind: "heading"; key: string; text: string }
  | { kind: "empty"; key: string; text: string }
  | { kind: "statement"; key: string; statement: RecapStatement };

export type SheetCopy = {
  disputedHeading: string;
  credibleHeading: string;
  noDisputed: string;
  noCredible: string;
};

// sheetBlocks lists a sheet's body in reading order: the disputed statements,
// then the credible ones, each section under its heading. An empty section
// keeps its heading and says so, so a sheet never shows a bare title.
export function sheetBlocks(speaker: RecapSpeaker, copy: SheetCopy): SheetBlock[] {
  const section = (
    id: string,
    heading: string,
    empty: string,
    statements: RecapStatement[],
  ): SheetBlock[] => [
    { kind: "heading", key: `${id}-heading`, text: heading },
    ...(statements.length === 0
      ? [{ kind: "empty" as const, key: `${id}-empty`, text: empty }]
      : statements.map((statement) => ({
          kind: "statement" as const,
          key: `${id}-${statement.claimId}`,
          statement,
        }))),
  ];
  return [
    ...section("disputed", copy.disputedHeading, copy.noDisputed, speaker.disputed),
    ...section("credible", copy.credibleHeading, copy.noCredible, speaker.credible),
  ];
}

// LINE_HEIGHT is the text face's natural line box as a fraction of the font
// size; the document sets no line height of its own.
const LINE_HEIGHT = 1.3;
// AVERAGE_GLYPH is the mean advance of the text face as a fraction of the font
// size, and SAFETY inflates every estimate: an over-estimate only sends a
// borderline sheet to the dense layout, an under-estimate would let it spill.
const AVERAGE_GLYPH = 0.5;
const SAFETY = 1.1;

function lines(text: string, width: number, fontSize: number): number {
  const perLine = Math.max(1, Math.floor(width / (fontSize * AVERAGE_GLYPH)));
  return Math.max(1, Math.ceil(text.length / perLine));
}

// estimateBlockHeight approximates the height of one block at a column width
// and font size. It counts text lines from character counts, which is all a
// layout decision made before rendering can know.
export function estimateBlockHeight(
  block: SheetBlock,
  width: number,
  fontSize: number,
): number {
  const line = fontSize * LINE_HEIGHT;
  if (block.kind !== "statement") {
    return (line + fontSize) * SAFETY;
  }
  const { statement } = block;
  const text =
    1 +
    lines(truncateQuote(statement.quote, QUOTE_LIMIT), width, fontSize) +
    (statement.rationale === "" ? 0 : lines(statement.rationale, width, fontSize)) +
    lines(statement.sources.map((source) => source.title).join("   "), width, fontSize);
  return (text * line + fontSize) * SAFETY;
}

// isBreakable reports whether a block is tall enough, at its layout's column
// width, that it must be allowed to continue on the next page.
export function isBreakable(block: SheetBlock, variant: SheetVariant): boolean {
  return (
    estimateBlockHeight(block, columnWidth(variant), VARIANTS[variant].fontSize) >
    BREAKABLE_HEIGHT
  );
}

function columnWidth(variant: SheetVariant): number {
  const { columns } = VARIANTS[variant];
  return (CONTENT_WIDTH - (columns - 1) * COLUMN_GAP) / columns;
}

function blockHeights(blocks: SheetBlock[], variant: SheetVariant): number[] {
  const width = columnWidth(variant);
  return blocks.map((block) =>
    estimateBlockHeight(block, width, VARIANTS[variant].fontSize),
  );
}

// SheetChrome is what surrounds a sheet's statements and takes room on its
// page: the printed name, which may wrap, and whether the sheet ends with the
// note counting its undetailed statements.
export type SheetChrome = { name: string; hasFootnote: boolean };

// chooseSheetVariant picks a sheet's layout: single-column when everything the
// page must hold - the header with its name, the statements, the footnote - is
// estimated to fit one page, dense otherwise. A dense sheet that still does
// not fit simply continues on the next page.
export function chooseSheetVariant(
  blocks: SheetBlock[],
  chrome: SheetChrome,
): SheetVariant {
  const body = blockHeights(blocks, "single").reduce((sum, h) => sum + h, 0);
  const extraNameLines = Math.max(
    0,
    Math.ceil(chrome.name.length / NAME_CHARS_PER_LINE) - 1,
  );
  const total =
    SHEET_HEADER_HEIGHT +
    extraNameLines * NAME_LINE +
    body +
    (chrome.hasFootnote ? FOOTNOTE_HEIGHT : 0);
  return total <= CONTENT_HEIGHT ? "single" : "dense";
}

// splitColumns divides a sheet's blocks between the columns of its layout,
// keeping reading order (the first column is read before the second). The
// split falls where the first column reaches half of the estimated height, and
// never right after a heading: a heading stays with what it introduces.
export function splitColumns(
  blocks: SheetBlock[],
  variant: SheetVariant,
): SheetBlock[][] {
  if (VARIANTS[variant].columns === 1 || blocks.length < 2) {
    return [blocks];
  }
  const heights = blockHeights(blocks, variant);
  const half = heights.reduce((sum, h) => sum + h, 0) / 2;
  let cut = 0;
  for (let filled = 0; cut < blocks.length && filled < half; cut += 1) {
    filled += heights[cut];
  }
  if (cut >= blocks.length) {
    cut = blocks.length - 1;
  }
  while (cut > 1 && blocks[cut - 1].kind === "heading") {
    cut -= 1;
  }
  return [blocks.slice(0, cut), blocks.slice(cut)];
}

// slug folds a title to lowercase ASCII words joined by hyphens, so the file
// name is safe on every operating system. The French ligatures are spelled
// out first: unlike accents, they do not decompose and would be dropped.
function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/œ/g, "oe")
    .replace(/æ/g, "ae")
    .normalize("NFD")
    .replace(/\p{M}/gu, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 80)
    .replace(/-+$/, "");
}

// pdfFileName names the export after the video and the analysis date (in the
// report's time zone): "le-grand-debat-2026-11-19.pdf". A title with nothing
// usable falls back to "recap", and an unknown date is left out.
export function pdfFileName(title: string, analyzedAt: string | null): string {
  const parts = [slug(title) || "recap"];
  if (analyzedAt !== null) {
    // en-CA formats a date as YYYY-MM-DD.
    parts.push(
      new Intl.DateTimeFormat("en-CA", {
        timeZone: REPORT_TIME_ZONE,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
      }).format(new Date(analyzedAt)),
    );
  }
  return `${parts.join("-")}.pdf`;
}
