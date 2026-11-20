import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { counts, debateRecap, recapSpeaker, statement } from "@/test/recap";
import { DistributionBar, DistributionLegend } from "./distribution-bar";
import { RecapCover } from "./recap-cover";
import { RecapTable } from "./recap-table";
import { SheetOrderControl } from "./sheet-order-control";
import { SpeakerSheet } from "./speaker-sheet";

const t = fr.app.recap;

describe("DistributionBar", () => {
  test("states every count in words, so the bar is never colour alone", () => {
    render(
      <DistributionBar
        counts={counts({ detected: 8, credible: 2, disputed: 3, unverifiable: 1, notVerified: 2 })}
      />,
    );
    const bar = screen.getByRole("img", {
      name: "Répartition : 2 fiables, 3 contestées, 1 invérifiables, 2 non vérifiées",
    });
    const widths = Array.from(bar.children).map(
      (segment) => (segment as HTMLElement).style.width,
    );
    expect(widths).toEqual(["25%", "37.5%", "12.5%", "25%"]);
  });

  test("omits empty outcomes and says so when nothing was detected", () => {
    const { rerender } = render(
      <DistributionBar counts={counts({ detected: 2, credible: 2 })} />,
    );
    expect(screen.getByRole("img").children).toHaveLength(1);

    rerender(<DistributionBar counts={counts()} />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByText(t.distribution.none)).toBeInTheDocument();
  });

  test("the legend names the four outcomes", () => {
    render(<DistributionLegend />);
    const items = within(
      screen.getByRole("list", { name: t.distribution.legend }),
    ).getAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual([
      t.distribution.credible,
      t.distribution.disputed,
      t.distribution.unverifiable,
      t.distribution.notVerified,
    ]);
  });
});

describe("RecapCover", () => {
  test("shows the brand, the title, the participants, the four key figures and the date", () => {
    const recap = debateRecap();
    render(
      <RecapCover
        recap={recap}
        principals={recap.speakers.slice(0, 2)}
        brandName="jeminforme.fr"
      />,
    );

    expect(screen.getByText("jeminforme.fr")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: "Le grand débat" }),
    ).toBeInTheDocument();
    // An unnamed principal appears under its letter, never an invented name.
    expect(
      screen.getAllByRole("listitem").map((item) => item.textContent),
    ).toEqual(["Marine Durand", "Intervenant B"]);

    // The key figures cover the whole analysis: principals plus the others.
    const figures = screen.getByLabelText(t.cover.figuresAria);
    expect(figures).toHaveTextContent(`${t.cover.duration}2 h 51`);
    expect(figures).toHaveTextContent(`${t.cover.detected}9`);
    expect(figures).toHaveTextContent(`${t.cover.credible}3`);
    expect(figures).toHaveTextContent(`${t.cover.disputed}3`);
    expect(screen.getByText("Analyse du 19 novembre 2026")).toBeInTheDocument();
  });

  test("prints the date in the report's time zone, not UTC", () => {
    // 23:30 UTC on the 19th is already the 20th in France.
    const recap = debateRecap({ analyzedAt: "2026-11-19T23:30:00Z" });
    render(<RecapCover recap={recap} principals={[]} brandName="jeminforme.fr" />);
    expect(screen.getByText("Analyse du 20 novembre 2026")).toBeInTheDocument();
  });

  test("says so when no principal was identified and omits an unknown date", () => {
    render(
      <RecapCover
        recap={debateRecap({ analyzedAt: null })}
        principals={[]}
        brandName="jeminforme.fr"
      />,
    );
    expect(screen.getByText(t.cover.noParticipants)).toBeInTheDocument();
    expect(screen.queryByText(/Analyse du/)).not.toBeInTheDocument();
  });
});

describe("RecapTable", () => {
  test("lists one row per principal with every figure, then the totals", () => {
    const recap = debateRecap();
    render(<RecapTable recap={recap} principals={recap.speakers.slice(0, 2)} />);

    const table = screen.getByRole("table", { name: t.table.caption });
    const rows = within(table).getAllByRole("row");
    const cells = (row: HTMLElement) =>
      within(row)
        .getAllByRole("cell")
        .slice(0, 8)
        .map((cell) => cell.textContent);

    expect(within(rows[1]).getByRole("rowheader")).toHaveTextContent("Marine Durand");
    expect(cells(rows[1])).toEqual(["30 min", "60 %", "12", "5", "2", "1", "1", "1"]);
    expect(within(rows[2]).getByRole("rowheader")).toHaveTextContent("Intervenant B");
    expect(cells(rows[2])).toEqual(["20 min", "40 %", "9", "3", "0", "2", "0", "1"]);
    expect(within(rows[3]).getByRole("rowheader")).toHaveTextContent(t.table.total);
    expect(cells(rows[3])).toEqual(["50 min", "100 %", "21", "8", "2", "3", "1", "2"]);
    // One distribution per row, each a labelled image.
    expect(within(table).getAllByRole("img")).toHaveLength(3);
  });

  test("the table scrolls inside a keyboard-reachable region at phone width", () => {
    const recap = debateRecap();
    render(<RecapTable recap={recap} principals={recap.speakers.slice(0, 2)} />);
    const region = screen.getByRole("region", { name: t.table.caption });
    expect(region.className).toContain("overflow-x-auto");
    expect(region).toHaveAttribute("tabindex", "0");
  });

  test("states the limits in plain words", () => {
    const recap = debateRecap();
    render(<RecapTable recap={recap} principals={recap.speakers.slice(0, 2)} />);

    expect(screen.getByText(t.notes.reading)).toBeInTheDocument();
    // 1 unverifiable + 2 not verified out of 8 principal statements.
    expect(
      screen.getByText(/38 % des affirmations des intervenants principaux.*\(3 sur 8\)/),
    ).toBeInTheDocument();
    // One name proposed by the analysis (A), one confirmed (F), one unnamed (B).
    expect(
      screen.getByText(
        /Noms proposés par l'analyse : 1\. Noms confirmés par un opérateur : 1\. Voix sans nom : 1\./,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(t.notes.ranking)).toBeInTheDocument();
    // The moderator is outside the sheets and listed with her speaking time.
    expect(
      screen.getByText("Claire Martin (Animation) : 5 min de parole, 1 détectées"),
    ).toBeInTheDocument();
  });

  test("unattributed speech is not counted as an unnamed voice", () => {
    const recap = debateRecap();
    recap.speakers.push(
      recapSpeaker({ label: "", name: "", role: "unknown", share: 0, disputed: [], credible: [] }),
    );
    render(<RecapTable recap={recap} principals={recap.speakers.slice(0, 2)} />);
    expect(screen.getByText(/Voix sans nom : 1\./)).toBeInTheDocument();
    expect(screen.getByText(/^Voix non attribuée \(Rôle inconnu\)/)).toBeInTheDocument();
  });

  test("shares are not claimed when the principals have no timed speech", () => {
    const untimed = recapSpeaker({ speakingTime: 0, share: 0 });
    const recap = debateRecap({
      speakers: [untimed],
      principals: { speakingTime: 0, turns: 12, counts: untimed.counts },
    });
    render(<RecapTable recap={recap} principals={[untimed]} />);
    const rows = within(screen.getByRole("table")).getAllByRole("row");
    expect(within(rows[1]).getAllByRole("cell")[1]).toHaveTextContent("–");
    expect(within(rows[2]).getAllByRole("cell")[1]).toHaveTextContent("–");
  });

  test("renders a coherent block with no principal and no statement", () => {
    const recap = debateRecap({
      speakers: [],
      principals: { speakingTime: 0, turns: 0, counts: counts() },
      others: { speakingTime: 0, turns: 0, counts: counts() },
    });
    render(<RecapTable recap={recap} principals={[]} />);

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByText(t.table.empty)).toBeInTheDocument();
    expect(screen.getByText(t.notes.noStatements)).toBeInTheDocument();
    expect(screen.queryByText(t.notes.othersHeading)).not.toBeInTheDocument();
  });
});

describe("SpeakerSheet", () => {
  test("shows the figures, then the disputed and the credible statements with their sources", () => {
    render(<SpeakerSheet speaker={recapSpeaker()} />);

    const sheet = screen.getByRole("region", { name: "Fiche de Marine Durand" });
    expect(within(sheet).getByText(t.sheet.automaticName)).toBeInTheDocument();
    for (const [label, value] of [
      [t.table.speakingTime, "30 min"],
      [t.table.share, "60 %"],
      [t.table.turns, "12"],
      [t.table.detected, "5"],
      [t.table.unverifiable, "1"],
      [t.table.notVerified, "1"],
    ]) {
      expect(within(sheet).getByText(label).nextElementSibling ?? within(sheet).getByText(label).previousElementSibling).toHaveTextContent(value);
    }

    const [disputed, credible] = within(sheet).getAllByRole("list").filter(
      (list) => list.tagName === "OL",
    );
    const item = within(disputed).getAllByRole("listitem")[0];
    expect(item).toHaveTextContent("1:35");
    expect(item).toHaveTextContent(fr.app.claims.verdicts.disputed);
    expect(item).toHaveTextContent(fr.app.claims.flags["cherry-picked"]);
    expect(item).toHaveTextContent("Les salaires ont doublé en cinq ans");
    expect(item).toHaveTextContent(
      "Justification : Les séries officielles montrent une hausse de 8 %.",
    );
    const link = within(item).getByRole("link", { name: "INSEE" });
    expect(link).toHaveAttribute("href", "https://example.org/insee");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");

    // A credible statement with no citation says so instead of showing nothing.
    expect(within(credible).getByText(t.sheet.noSource)).toBeInTheDocument();
    // Statements without a usable verdict are counted on the sheet.
    expect(
      within(sheet).getByText(
        "Non détaillées dans cette fiche : 1 invérifiables, 1 non vérifiées.",
      ),
    ).toBeInTheDocument();
  });

  test("an unnamed speaker appears under its letter, with no name provenance", () => {
    render(<SpeakerSheet speaker={recapSpeaker({ label: "B", name: "" })} />);
    expect(
      screen.getByRole("region", { name: "Fiche de Intervenant B" }),
    ).toBeInTheDocument();
    expect(screen.queryByText(t.sheet.automaticName)).not.toBeInTheDocument();
    expect(screen.queryByText(t.sheet.confirmedName)).not.toBeInTheDocument();
  });

  test("an operator-confirmed name says so", () => {
    render(<SpeakerSheet speaker={recapSpeaker({ nameConfirmed: true })} />);
    expect(screen.getByText(t.sheet.confirmedName)).toBeInTheDocument();
  });

  test("a speaker with no verified statement gets sentences, not empty lists", () => {
    render(
      <SpeakerSheet
        speaker={recapSpeaker({
          counts: counts({ detected: 2, notVerified: 2 }),
          disputed: [],
          credible: [],
        })}
      />,
    );
    expect(screen.getByText(t.sheet.noDisputed)).toBeInTheDocument();
    expect(screen.getByText(t.sheet.noCredible)).toBeInTheDocument();
    expect(
      screen.getByText("Non détaillées dans cette fiche : 0 invérifiables, 2 non vérifiées."),
    ).toBeInTheDocument();
  });

  test("a long quote is cut on a word boundary and never rewritten", () => {
    const long = `${"mot ".repeat(100)}fin`;
    render(
      <SpeakerSheet
        speaker={recapSpeaker({ disputed: [statement({ quote: long })], credible: [] })}
      />,
    );
    const quote = screen.getByText(/^mot mot/);
    expect(quote.textContent?.endsWith("mot…")).toBe(true);
    expect((quote.textContent ?? "").length).toBeLessThanOrEqual(281);
  });

  test("an unknown verdict or flag falls back to its raw value", () => {
    render(
      <SpeakerSheet
        speaker={recapSpeaker({
          disputed: [statement({ verdict: "nouveau", flags: ["inconnu"] })],
          credible: [],
        })}
      />,
    );
    expect(screen.getByText("nouveau")).toBeInTheDocument();
    expect(screen.getByText("inconnu")).toBeInTheDocument();
  });
});

describe("SheetOrderControl", () => {
  test("offers move buttons per sheet, disabled at the ends", async () => {
    const onMove = vi.fn();
    const sheets = [
      recapSpeaker({ label: "A" }),
      recapSpeaker({ label: "B", name: "" }),
      recapSpeaker({ label: "C", name: "Sophie Garnier" }),
    ];
    render(<SheetOrderControl sheets={sheets} onMove={onMove} />);

    expect(
      screen.getByRole("button", { name: "Monter la fiche de Marine Durand" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Descendre la fiche de Sophie Garnier" }),
    ).toBeDisabled();

    await userEvent.click(
      screen.getByRole("button", { name: "Descendre la fiche de Intervenant B" }),
    );
    expect(onMove).toHaveBeenCalledWith(1, 1);
    await userEvent.click(
      screen.getByRole("button", { name: "Monter la fiche de Sophie Garnier" }),
    );
    expect(onMove).toHaveBeenCalledWith(2, -1);
  });

  test("renders nothing when there is nothing to reorder", () => {
    const { container } = render(
      <SheetOrderControl sheets={[recapSpeaker()]} onMove={vi.fn()} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
