// @vitest-environment node
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { Font, renderToBuffer } from "@react-pdf/renderer";
import { getDocument } from "pdfjs-dist/legacy/build/pdf.mjs";
import { beforeAll, describe, expect, test } from "vitest";
import { en } from "@/lib/i18n/dictionaries/en";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { orderSheets, type Recap } from "@/lib/video/recap";
import { counts, debateRecap, recapSpeaker, statement } from "@/test/recap";
import { DISPLAY_FONT_RANGES, fontDataUri, registerRecapFonts } from "./fonts";
import { RecapDocument } from "./recap-document";

type TextItem = { str: string; x: number; width: number; font: string };
type RenderedPage = { text: string; links: string[]; items: TextItem[] };

beforeAll(() => {
  // The typefaces are registered the way the browser registers them: as data
  // URIs of the files' bytes. Here the bytes are read from the pinned
  // packages; the browser fetches the copies the prebuild script makes.
  registerRecapFonts(Font, (font) =>
    fontDataUri(
      font.file,
      new Uint8Array(readFileSync(join(process.cwd(), "node_modules", font.source))),
    ),
  );
});

async function renderRecap(
  recap: Recap,
  order: string[] = [],
  dict = fr,
): Promise<{ pages: RenderedPage[]; raw: string }> {
  const buffer = await renderToBuffer(
    <RecapDocument
      recap={recap}
      sheets={orderSheets(recap.speakers, order)}
      t={dict.app}
      locale={dict === fr ? "fr" : "en"}
      brandName="jeminforme.fr"
    />,
  );
  const pdf = await getDocument({ data: new Uint8Array(buffer) }).promise;
  const pages: RenderedPage[] = [];
  for (let n = 1; n <= pdf.numPages; n += 1) {
    const page = await pdf.getPage(n);
    const content = await page.getTextContent();
    const annotations = await page.getAnnotations();
    const items: TextItem[] = content.items.flatMap((item) =>
      "str" in item && item.str.trim() !== ""
        ? [
            {
              str: item.str,
              x: item.transform[4],
              width: item.width,
              font: (content.styles[item.fontName]?.fontFamily ?? "") + item.fontName,
            },
          ]
        : [],
    );
    pages.push({
      text: content.items
        .map((item) => ("str" in item ? item.str : ""))
        .join(" ")
        .replace(/\s+/g, " ")
        .trim(),
      links: annotations.flatMap((a: { url?: string }) => (a.url ? [a.url] : [])),
      items,
    });
  }
  return { pages, raw: buffer.toString("latin1") };
}

// A speaker with n sourced statements of realistic length, as the reference
// debate had: a quote of about 120 characters and a two-line justification.
function speakerWith(label: string, name: string, n: number) {
  const statements = Array.from({ length: n }, (_, i) =>
    statement({
      claimId: `${label}-${i}`,
      at: 60 * (i + 1),
      verdict: i % 2 === 0 ? "disputed" : "credible",
      flags: i % 3 === 0 ? ["cherry-picked"] : [],
      quote: `Affirmation ${label}${i} : le chômage a baissé de deux points en un an selon les chiffres publiés ce trimestre par l'institut.`,
      rationale: `Justification ${label}${i} : la série trimestrielle officielle montre une baisse de 0,3 point sur la période, pas de deux points.`,
      sources: [{ title: `Source ${label}${i}`, url: `https://example.org/${label}/${i}` }],
    }),
  );
  return recapSpeaker({
    label,
    name,
    share: 0.2,
    counts: counts({ detected: n + 3, credible: Math.floor(n / 2), disputed: Math.ceil(n / 2), unverifiable: 1, notVerified: 2 }),
    disputed: statements.filter((s) => s.verdict === "disputed"),
    credible: statements.filter((s) => s.verdict === "credible"),
  });
}

// referenceRecap has the reference structure: five principals with between 4
// and 9 sourced statements each, plus a moderator outside the sheets.
function referenceRecap(): Recap {
  const principals = [
    speakerWith("A", "Marine Durand", 9),
    speakerWith("B", "Paul Lefèvre", 8),
    speakerWith("C", "", 6),
    speakerWith("D", "Karim Benali", 5),
    speakerWith("E", "Hélène Rousseau", 4),
  ];
  return debateRecap({
    speakers: [
      ...principals,
      recapSpeaker({ label: "F", name: "Claire Martin", role: "moderator", nameConfirmed: true, share: 0, disputed: [], credible: [] }),
    ],
    principals: {
      speakingTime: 9000,
      turns: 60,
      counts: counts({ detected: 47, credible: 15, disputed: 17, unverifiable: 5, notVerified: 10 }),
    },
  });
}

describe("RecapDocument", () => {
  test("the reference structure is seven pages: cover, recap, and one page per sheet", async () => {
    const recap = referenceRecap();
    const { pages } = await renderRecap(recap);

    expect(pages).toHaveLength(7);
    for (const page of pages) {
      expect(page.text.length).toBeGreaterThan(40);
    }

    // Cover: brand, title, participants, key figures, date - and no footer.
    expect(pages[0].text).toContain("jeminforme.fr");
    expect(pages[0].text).toContain("Le grand débat");
    expect(pages[0].text).toContain("Marine Durand");
    expect(pages[0].text).toContain("Intervenant C");
    expect(pages[0].text).toContain("Analyse du 19 novembre 2026");
    expect(pages[0].text).not.toMatch(/Page \d+ \/ \d+/);

    // Recap: every principal, the totals, the limits, the other voices.
    for (const name of ["Marine Durand", "Paul Lefèvre", "Intervenant C", "Karim Benali", "Hélène Rousseau"]) {
      expect(pages[1].text).toContain(name);
    }
    expect(pages[1].text).toMatch(/Total 2 h 30 100 % 60 47 15 17 5 10/);
    expect(pages[1].text).toContain("(15 sur 47)");
    expect(pages[1].text).toContain("Claire Martin (Animation)");
    expect(pages[1].text).toContain("Page 2 / 7");

    // Sheets: one page each, in the page's order, with every statement.
    const names = ["Marine Durand", "Paul Lefèvre", "Intervenant C", "Karim Benali", "Hélène Rousseau"];
    const expectedStatements = [9, 8, 6, 5, 4];
    names.forEach((name, i) => {
      const page = pages[2 + i];
      expect(page.text.startsWith(name)).toBe(true);
      expect(page.text).not.toContain("(suite)");
      expect(page.text).toContain(`Page ${3 + i} / 7`);
      expect(page.links).toHaveLength(expectedStatements[i]);
      expect(page.text.match(/Justification [A-E]\d/g)).toHaveLength(expectedStatements[i]);
    });
  });

  test("source links are real links and the brand typefaces are embedded", async () => {
    const { pages, raw } = await renderRecap(referenceRecap());

    expect(pages[2].links).toContain("https://example.org/A/0");
    expect(raw).toMatch(/\/FontName\s*\/[A-Z]+\+Geist-Regular/);
    expect(raw).toMatch(/\/FontName\s*\/[A-Z]+\+Geist-SemiBold/);
    expect(raw).toMatch(/\/FontName\s*\/[A-Z]+\+Spectral-SemiBold/);
    // No standard-14 fallback: every glyph comes from an embedded face.
    expect(raw).not.toMatch(/\/BaseFont\s*\/Helvetica/);
  });

  test("a sheet longer than a page continues on the next with its header repeated", async () => {
    const long = speakerWith("A", "Marine Durand", 30);
    const short = speakerWith("B", "Paul Lefèvre", 2);
    const recap = debateRecap({ speakers: [long, short] });
    const { pages } = await renderRecap(recap);

    const first = pages.findIndex((page) => page.text.startsWith("Marine Durand"));
    const next = pages.findIndex((page) => page.text.startsWith("Paul Lefèvre"));
    expect(first).toBe(2);
    expect(next).toBeGreaterThan(first + 1);
    for (let i = first + 1; i < next; i += 1) {
      expect(pages[i].text.startsWith("Marine Durand (suite)")).toBe(true);
    }
    expect(pages[first].text).not.toContain("(suite)");
    expect(pages[next].text).not.toContain("(suite)");

    // Nothing is clipped: all thirty statements are in the document, once.
    const sheet = pages.slice(first, next).map((page) => page.text).join(" ");
    for (let i = 0; i < 30; i += 1) {
      expect(sheet.split(`Justification A${i} :`)).toHaveLength(2);
    }
    // Every page of the sheet carries the footer with a page number.
    for (let i = first; i < next; i += 1) {
      expect(pages[i].text).toContain(`Page ${i + 1} / ${pages.length}`);
    }
  });

  test("the sheets follow the order shown on the page", async () => {
    const recap = referenceRecap();
    const { pages } = await renderRecap(recap, ["E", "A"]);
    expect(pages[2].text.startsWith("Hélène Rousseau")).toBe(true);
    expect(pages[3].text.startsWith("Marine Durand")).toBe(true);
    expect(pages[4].text.startsWith("Paul Lefèvre")).toBe(true);
  });

  test("the figures equal the recap's for the same analysis", async () => {
    const recap = debateRecap();
    const { pages } = await renderRecap(recap);

    // Cover figures: the whole analysis (principals plus others).
    expect(pages[0].text).toMatch(/2 h 51 Durée 9 Affirmations détectées 3 Fiables 3 Contestées/);
    // Recap rows and totals, as on the page.
    expect(pages[1].text).toMatch(/Marine Durand 30 min 60 % 12 5 2 1 1 1/);
    expect(pages[1].text).toMatch(/Intervenant B 20 min 40 % 9 3 0 2 0 1/);
    expect(pages[1].text).toMatch(/Total 50 min 100 % 21 8 2 3 1 2/);
    // Sheet figures and the count of statements left undetailed.
    expect(pages[2].text).toMatch(/Marine Durand Nom attribué automatiquement 30 min/);
    expect(pages[2].text).toContain("Non détaillées dans cette fiche : 1 invérifiables, 1 non vérifiées.");
  });

  test("a recap with no principal and no statement is still a coherent two-page document", async () => {
    const recap = debateRecap({
      speakers: [],
      principals: { speakingTime: 0, turns: 0, counts: counts() },
      others: { speakingTime: 0, turns: 0, counts: counts() },
      analyzedAt: null,
    });
    const { pages } = await renderRecap(recap);

    expect(pages).toHaveLength(2);
    expect(pages[0].text).toContain(fr.app.recap.cover.noParticipants);
    expect(pages[1].text).toContain(fr.app.recap.table.empty);
    expect(pages[1].text).toContain(fr.app.recap.notes.noStatements);
  });

  test("renders in English with the English copy", async () => {
    const { pages } = await renderRecap(debateRecap(), [], en);
    expect(pages[0].text).toContain("Analyzed on November 19, 2026");
    expect(pages[1].text).toContain("Speaker B");
    expect(pages[2].text).toContain("Page 3 / 4");
  });

  test("no text runs past the right edge of the page, in French or in English", async () => {
    for (const dict of [fr, en]) {
      const { pages } = await renderRecap(referenceRecap(), [], dict);
      for (const page of pages) {
        for (const item of page.items) {
          // 595.28 pt wide with 40 pt margins: nothing may pass x = 555.28.
          expect(item.x + item.width, `"${item.str}"`).toBeLessThanOrEqual(556);
          expect(item.x, `"${item.str}"`).toBeGreaterThanOrEqual(39);
        }
      }
    }
  });

  test("the recap table's column headers fit their cells", async () => {
    const { pages } = await renderRecap(referenceRecap());
    for (const label of ["Invérifiables", "Contestées", "Détectées", "Fiables"]) {
      const header = pages[1].items.find((item) => item.str === label);
      expect(header, label).toBeDefined();
      // Each header cell is 46 pt wide.
      expect(header?.width, label).toBeLessThanOrEqual(46);
    }
  });

  test("a name the display face cannot draw is printed whole, with no missing glyph", async () => {
    const recap = debateRecap({ title: "Débat avec Đoković" });
    recap.speakers[0] = recapSpeaker({ name: "Novak Đoković" });
    const { pages } = await renderRecap(recap);

    expect(pages[0].text).toContain("Débat avec Đoković");
    expect(pages[2].text.startsWith("Novak Đoković")).toBe(true);
  });

  test("French typographic spaces and hyphens print, in titles, names and statements", async () => {
    const recap = debateRecap({ title: "Débat\u202f: vrai\u202f?" });
    recap.speakers[0] = recapSpeaker({
      name: "Jean\u2011Luc Durand",
      disputed: [statement({ quote: "Est\u2011ce vrai\u202f? Oui\u202f!", rationale: "Non\u202f: co\u00adopération." })],
      credible: [],
    });
    const { pages, raw } = await renderRecap(recap);

    expect(pages[0].text).toContain("Débat : vrai ?");
    expect(pages[2].text.startsWith("Jean-Luc Durand")).toBe(true);
    // (Text extraction folds every space, no-break included, to a plain one.)
    expect(pages[2].text).toContain("Est-ce vrai ? Oui !");
    expect(pages[2].text).toContain("Non : coopération.");
    // Printable again, the name keeps the display face.
    expect(raw).toMatch(/\/FontName\s*\/[A-Z]+\+Spectral-SemiBold/);
  });

  test("every character the display face is trusted with can be laid out and printed", async () => {
    // One title holding the whole trusted set: if the file lacked a glyph, or
    // held one the library cannot measure, the render would throw or drop it.
    const chars: string[] = [];
    for (const [from, to] of DISPLAY_FONT_RANGES) {
      for (let code = from; code <= to; code += 1) {
        chars.push(String.fromCodePoint(code));
      }
    }
    const letters = chars.filter((char) => char.trim() !== "");
    const title = letters.join("");
    const { pages } = await renderRecap(debateRecap({ title }));

    // Text extraction returns compatibility forms (the micro sign as a Greek
    // mu, a superscript two as "2"), so both sides are compared folded.
    const fold = (text: string) => text.normalize("NFKC").replace(/\s+/g, "");
    const printed = fold(pages[0].text);
    for (const char of letters) {
      expect(printed.includes(fold(char)), `U+${char.codePointAt(0)?.toString(16)}`).toBe(true);
    }
  });

  test("a statement taller than a page is continued, not clipped", async () => {
    const words = Array.from({ length: 1400 }, (_, i) => `mot${i}`);
    const recap = debateRecap();
    recap.speakers[0] = recapSpeaker({
      disputed: [statement({ claimId: "huge", rationale: words.join(" ") })],
      credible: [],
    });
    const { pages } = await renderRecap(recap);

    const sheet = pages.filter(
      (page) => page.text.startsWith("Marine Durand"),
    );
    expect(sheet.length).toBeGreaterThan(1);
    const text = sheet.map((page) => page.text).join(" ");
    // Every word of the justification is in the document: first, middle, last.
    for (const word of ["mot0 ", "mot700 ", "mot1399"]) {
      expect(text).toContain(word);
    }
  });

  test("an untitled source prints its host and a long token breaks instead of overflowing", async () => {
    const url = `https://example.org/${"segment-".repeat(30)}fin`;
    const recap = debateRecap();
    recap.speakers[0] = recapSpeaker({
      disputed: [
        statement({
          quote: `Voir ${"x".repeat(200)} pour le détail.`,
          sources: [{ title: url, url }],
        }),
      ],
      credible: [],
    });
    const { pages } = await renderRecap(recap);

    expect(pages[2].text).toContain("example.org");
    expect(pages[2].text).not.toContain("segment-segment-");
    expect(pages[2].links).toEqual([url]);
    for (const item of pages[2].items) {
      expect(item.x + item.width, `"${item.str.slice(0, 30)}"`).toBeLessThanOrEqual(556);
    }
  });
});
