import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { recapSpeaker, statement } from "@/test/recap";
import {
  chooseSheetVariant,
  CONTENT_HEIGHT,
  CONTENT_WIDTH,
  estimateBlockHeight,
  isBreakable,
  PAGE,
  pdfFileName,
  type SheetBlock,
  sheetBlocks,
  splitColumns,
} from "./layout";

const copy = fr.app.recap.sheet;

function statements(prefix: string, n: number, verdict = "disputed") {
  return Array.from({ length: n }, (_, i) =>
    statement({
      claimId: `${prefix}${i}`,
      verdict,
      quote: `Affirmation ${prefix}${i} : le chômage a baissé de deux points en un an selon les chiffres publiés ce trimestre.`,
      rationale: "La série trimestrielle officielle montre une baisse de 0,3 point sur la période, pas de deux points.",
    }),
  );
}

const keys = (blocks: SheetBlock[]) => blocks.map((block) => block.key);

describe("page geometry", () => {
  test("is A4 portrait with a bottom band reserved for the footer", () => {
    expect(PAGE.width).toBeCloseTo(595.28);
    expect(PAGE.height).toBeCloseTo(841.89);
    expect(PAGE.bottom).toBeGreaterThan(PAGE.margin);
    expect(CONTENT_WIDTH).toBeCloseTo(PAGE.width - 2 * PAGE.margin);
    expect(CONTENT_HEIGHT).toBeCloseTo(PAGE.height - PAGE.margin - PAGE.bottom);
  });
});

describe("sheetBlocks", () => {
  test("lists the disputed then the credible statements, each under its heading", () => {
    const speaker = recapSpeaker({
      disputed: statements("d", 2),
      credible: statements("c", 1, "credible"),
    });
    expect(keys(sheetBlocks(speaker, copy))).toEqual([
      "disputed-heading",
      "disputed-d0",
      "disputed-d1",
      "credible-heading",
      "credible-c0",
    ]);
  });

  test("an empty section keeps its heading and says it is empty", () => {
    const blocks = sheetBlocks(recapSpeaker({ disputed: [], credible: [] }), copy);
    expect(blocks).toEqual([
      { kind: "heading", key: "disputed-heading", text: copy.disputedHeading },
      { kind: "empty", key: "disputed-empty", text: copy.noDisputed },
      { kind: "heading", key: "credible-heading", text: copy.credibleHeading },
      { kind: "empty", key: "credible-empty", text: copy.noCredible },
    ]);
  });
});

describe("estimateBlockHeight", () => {
  const block = (overrides = {}): SheetBlock => ({
    kind: "statement",
    key: "s",
    statement: statement(overrides),
  });

  test("grows with the text and shrinks with the column width", () => {
    const short = estimateBlockHeight(block({ quote: "Court." }), 500, 10);
    const long = estimateBlockHeight(block({ quote: "mot ".repeat(60) }), 500, 10);
    const narrow = estimateBlockHeight(block({ quote: "mot ".repeat(60) }), 250, 10);
    expect(long).toBeGreaterThan(short);
    expect(narrow).toBeGreaterThan(long);
  });

  test("a statement without a justification is shorter", () => {
    expect(estimateBlockHeight(block({ rationale: "" }), 500, 10)).toBeLessThan(
      estimateBlockHeight(block(), 500, 10),
    );
  });

  test("a quote counts at its printed, truncated length", () => {
    const capped = estimateBlockHeight(block({ quote: "mot ".repeat(70) }), 500, 10);
    const huge = estimateBlockHeight(block({ quote: "mot ".repeat(700) }), 500, 10);
    expect(huge).toBe(capped);
  });

  test("a heading is a single short line", () => {
    const heading: SheetBlock = { kind: "heading", key: "h", text: copy.disputedHeading };
    expect(estimateBlockHeight(heading, 500, 10)).toBeLessThan(30);
  });
});

describe("chooseSheetVariant", () => {
  const blocksFor = (n: number) =>
    sheetBlocks(recapSpeaker({ disputed: statements("d", n), credible: [] }), copy);
  const variantFor = (n: number, chrome = { name: "Marine Durand", hasFootnote: false }) =>
    chooseSheetVariant(blocksFor(n), chrome);

  test("a short sheet keeps the readable single column", () => {
    expect(variantFor(0)).toBe("single");
    expect(variantFor(4)).toBe("single");
  });

  test("a sheet that would not fit one page in a single column goes dense", () => {
    expect(variantFor(9)).toBe("dense");
    expect(variantFor(30)).toBe("dense");
  });
});

describe("chooseSheetVariant on a borderline sheet", () => {
  const blocksFor = (n: number) =>
    sheetBlocks(recapSpeaker({ disputed: statements("d", n), credible: [] }), copy);
  const plain = { name: "Marine Durand", hasFootnote: false };
  // The largest sheet that fits a single column with nothing else on the page.
  const borderline = [...Array(12).keys()]
    .reverse()
    .find((n) => chooseSheetVariant(blocksFor(n), plain) === "single") as number;

  test("the footnote counts toward the page", () => {
    // Find a body the footnote tips over: pad the borderline sheet with short
    // statements until only the footnote makes the difference.
    const tipped = [...Array(40).keys()].some((extra) => {
      const blocks = [
        ...blocksFor(borderline),
        ...Array.from({ length: extra }, (_, i): SheetBlock => ({
          kind: "empty",
          key: `pad-${i}`,
          text: "x",
        })),
      ];
      return (
        chooseSheetVariant(blocks, plain) === "single" &&
        chooseSheetVariant(blocks, { ...plain, hasFootnote: true }) === "dense"
      );
    });
    expect(tipped).toBe(true);
  });

  test("a name too long for one line counts toward the page", () => {
    const longName = "Nom ".repeat(40);
    const tipped = [...Array(40).keys()].some((extra) => {
      const blocks = [
        ...blocksFor(borderline),
        ...Array.from({ length: extra }, (_, i): SheetBlock => ({
          kind: "empty",
          key: `pad-${i}`,
          text: "x",
        })),
      ];
      return (
        chooseSheetVariant(blocks, plain) === "single" &&
        chooseSheetVariant(blocks, { name: longName, hasFootnote: false }) === "dense"
      );
    });
    expect(tipped).toBe(true);
  });
});

describe("isBreakable", () => {
  const block = (rationale: string): SheetBlock => ({
    kind: "statement",
    key: "s",
    statement: statement({ rationale }),
  });

  test("an ordinary statement is kept whole", () => {
    expect(isBreakable(block("Une justification de longueur ordinaire."), "single")).toBe(false);
    expect(isBreakable(block("Une justification de longueur ordinaire."), "dense")).toBe(false);
  });

  test("a statement that could not fit a page may continue on the next", () => {
    expect(isBreakable(block("mot ".repeat(1500)), "single")).toBe(true);
    expect(isBreakable(block("mot ".repeat(1500)), "dense")).toBe(true);
  });

  test("headings are never breakable", () => {
    expect(isBreakable({ kind: "heading", key: "h", text: "H" }, "dense")).toBe(false);
  });
});

describe("splitColumns", () => {
  test("a single-column sheet is one column", () => {
    const blocks = sheetBlocks(recapSpeaker({ disputed: statements("d", 3), credible: [] }), copy);
    expect(splitColumns(blocks, "single")).toEqual([blocks]);
  });

  test("a dense sheet splits in two, in reading order, with nothing lost", () => {
    const blocks = sheetBlocks(
      recapSpeaker({ disputed: statements("d", 5), credible: statements("c", 4, "credible") }),
      copy,
    );
    const [left, right] = splitColumns(blocks, "dense");
    expect([...keys(left), ...keys(right)]).toEqual(keys(blocks));
    expect(left.length).toBeGreaterThan(0);
    expect(right.length).toBeGreaterThan(0);
    // Balanced: neither column holds more than about two thirds of the blocks.
    expect(Math.max(left.length, right.length)).toBeLessThanOrEqual(7);
  });

  test("a heading is never left alone at the bottom of the first column", () => {
    for (let disputed = 1; disputed <= 8; disputed += 1) {
      const blocks = sheetBlocks(
        recapSpeaker({
          disputed: statements("d", disputed),
          credible: statements("c", 9 - disputed, "credible"),
        }),
        copy,
      );
      const [left] = splitColumns(blocks, "dense");
      expect(left.at(-1)?.kind).not.toBe("heading");
    }
  });

  test("a lopsided sheet still gives the second column something to hold", () => {
    const blocks: SheetBlock[] = [
      { kind: "heading", key: "h", text: "H" },
      { kind: "statement", key: "long", statement: statement({ quote: "mot ".repeat(69), rationale: "mot ".repeat(200) }) },
      { kind: "statement", key: "short", statement: statement({ quote: "Court.", rationale: "" }) },
    ];
    const [left, right] = splitColumns(blocks, "dense");
    expect(keys(left)).toEqual(["h", "long"]);
    expect(keys(right)).toEqual(["short"]);
  });
});

describe("pdfFileName", () => {
  test.each([
    ["Le grand débat", "2026-11-19T20:00:00Z", "le-grand-debat-2026-11-19.pdf"],
    // 23:30 UTC is already the next day in the report's time zone.
    ["Le grand débat", "2026-11-19T23:30:00Z", "le-grand-debat-2026-11-20.pdf"],
    ["  Débat : « l'œil » & 5/5 ?  ", "2026-01-02T10:00:00Z", "debat-l-oeil-5-5-2026-01-02.pdf"],
    ["../../etc/passwd", "2026-01-02T10:00:00Z", "etc-passwd-2026-01-02.pdf"],
    ["???", "2026-01-02T10:00:00Z", "recap-2026-01-02.pdf"],
    ["Sans date", null, "sans-date.pdf"],
  ])("%s on %s is %s", (title, analyzedAt, expected) => {
    expect(pdfFileName(title, analyzedAt)).toBe(expected);
  });

  test("a very long title is bounded and never ends on a hyphen", () => {
    const name = pdfFileName(`${"mot ".repeat(40)}fin`, null);
    expect(name.length).toBeLessThanOrEqual(84);
    expect(name).toMatch(/[a-z0-9]\.pdf$/);
  });
});
