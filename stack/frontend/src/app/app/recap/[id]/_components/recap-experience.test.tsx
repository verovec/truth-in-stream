import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { Recap } from "@/lib/video/recap";
import { counts, debateRecap, recapSpeaker } from "@/test/recap";
import { RecapExperience } from "./recap-experience";

vi.mock("next/link", () => import("@/test/next-link-mock"));

const t = fr.app.recap;

beforeEach(() => {
  window.history.replaceState(null, "", "/app/recap/vid-1");
});

afterEach(() => {
  vi.restoreAllMocks();
});

function renderRecap(
  recap: Recap,
  props: { initialOrder?: string | null } = {},
) {
  const loadRecap = vi.fn(async () => recap);
  render(
    <RecapExperience
      videoId="vid-1"
      brandName="jeminforme.fr"
      loadRecap={loadRecap}
      {...props}
    />,
  );
  return loadRecap;
}

// sheetNames reads the speaker sheets in document order.
function sheetNames(): string[] {
  return screen
    .getAllByRole("region", { name: /^Fiche de / })
    .map((sheet) => sheet.getAttribute("aria-labelledby") ?? "")
    .map((id) => document.getElementById(id)?.textContent ?? "");
}

describe("RecapExperience", () => {
  test("renders the cover, the recap and one sheet per principal, in that order", async () => {
    const loadRecap = renderRecap(debateRecap());

    expect(screen.getByRole("status")).toHaveTextContent(t.loading);
    const title = await screen.findByRole("heading", { level: 2, name: "Le grand débat" });
    expect(loadRecap).toHaveBeenCalledWith("vid-1", expect.any(AbortSignal));

    const table = screen.getByRole("heading", { level: 3, name: t.table.heading });
    const sheets = screen.getAllByRole("region", { name: /^Fiche de / });
    // The moderator is in the recap note, not a sheet.
    expect(sheetNames()).toEqual(["Marine Durand", "Intervenant B"]);
    expect(screen.queryByRole("region", { name: "Fiche de Claire Martin" })).not.toBeInTheDocument();

    const follows = (a: Element, b: Element) =>
      Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
    expect(follows(title, table)).toBe(true);
    expect(follows(table, sheets[0])).toBe(true);
    expect(follows(sheets[0], sheets[1])).toBe(true);
  });

  test("the recap totals equal the sum of the sheets", async () => {
    const recap = debateRecap();
    renderRecap(recap);
    await screen.findByRole("heading", { level: 2 });

    const sum = recap.speakers
      .filter((speaker) => speaker.role === "principal")
      .reduce((total, speaker) => total + speaker.counts.detected, 0);
    const totals = within(screen.getByRole("table")).getAllByRole("row").at(-1);
    expect(totals).toBeDefined();
    expect(within(totals as HTMLElement).getAllByRole("cell")[3]).toHaveTextContent(
      String(sum),
    );
    expect(sum).toBe(recap.principals.counts.detected);
  });

  test("reordering the sheets rewrites the URL, and the URL restores the order", async () => {
    renderRecap(debateRecap());
    await screen.findByRole("heading", { level: 2 });
    expect(window.location.search).toBe("");

    await userEvent.click(
      screen.getByRole("button", { name: "Descendre la fiche de Marine Durand" }),
    );
    expect(sheetNames()).toEqual(["Intervenant B", "Marine Durand"]);
    expect(new URLSearchParams(window.location.search).get("order")).toBe("B,A");
    // The table and the cover follow the same order as the sheets.
    expect(
      within(screen.getByRole("table")).getAllByRole("rowheader")[0],
    ).toHaveTextContent("Intervenant B");

    // Back to the default order: the parameter goes away.
    await userEvent.click(
      screen.getByRole("button", { name: "Monter la fiche de Marine Durand" }),
    );
    expect(sheetNames()).toEqual(["Marine Durand", "Intervenant B"]);
    expect(window.location.search).toBe("");
  });

  test("the order carried by the URL is applied on load", async () => {
    renderRecap(debateRecap(), { initialOrder: "B,A" });
    await screen.findByRole("heading", { level: 2 });
    expect(sheetNames()).toEqual(["Intervenant B", "Marine Durand"]);
  });

  test("a stale order in the URL never hides a sheet", async () => {
    renderRecap(debateRecap(), { initialOrder: "Z,F" });
    await screen.findByRole("heading", { level: 2 });
    expect(sheetNames()).toEqual(["Marine Durand", "Intervenant B"]);
  });

  test("a single-speaker video with no verified statement is a coherent report", async () => {
    const solo = recapSpeaker({
      name: "",
      share: 1,
      counts: counts({ detected: 2, notVerified: 2 }),
      disputed: [],
      credible: [],
    });
    renderRecap(
      debateRecap({
        speakers: [solo],
        principals: { speakingTime: 1800, turns: 12, counts: solo.counts },
        others: { speakingTime: 0, turns: 0, counts: counts() },
      }),
    );
    await screen.findByRole("heading", { level: 2 });

    expect(sheetNames()).toEqual(["Intervenant A"]);
    expect(screen.getByText(t.sheet.noDisputed)).toBeInTheDocument();
    expect(screen.getByText(t.sheet.noCredible)).toBeInTheDocument();
    expect(
      screen.getByText(/100 % des affirmations des intervenants principaux.*\(2 sur 2\)/),
    ).toBeInTheDocument();
    // Nothing to reorder with one sheet.
    expect(screen.queryByRole("heading", { name: t.order.heading })).not.toBeInTheDocument();
  });

  test("a video with no principal still renders the cover and says why", async () => {
    renderRecap(
      debateRecap({
        speakers: [],
        principals: { speakingTime: 0, turns: 0, counts: counts() },
        others: { speakingTime: 0, turns: 0, counts: counts() },
      }),
    );
    await screen.findByRole("heading", { level: 2 });
    expect(screen.getByText(t.cover.noParticipants)).toBeInTheDocument();
    expect(screen.getByText(t.table.empty)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /^Fiche de / })).not.toBeInTheDocument();
  });

  test.each([
    [409, t.errors.notReady, false],
    [404, t.errors.notFound, false],
    [500, t.errors.failed, true],
  ])("a %d response explains itself", async (status, message, retryable) => {
    const loadRecap = vi.fn(async (): Promise<Recap> => {
      throw new ApiError("refused", status);
    });
    render(
      <RecapExperience videoId="vid-1" brandName="jeminforme.fr" loadRecap={loadRecap} />,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(screen.getByRole("link", { name: t.back })).toHaveAttribute("href", "/app");
    expect(screen.queryByRole("button", { name: t.retry }) !== null).toBe(retryable);
  });

  test("a failed load can be retried", async () => {
    const loadRecap = vi
      .fn<(id: string, signal?: AbortSignal) => Promise<Recap>>()
      .mockRejectedValueOnce(new Error("network"))
      .mockResolvedValueOnce(debateRecap());
    render(
      <RecapExperience videoId="vid-1" brandName="jeminforme.fr" loadRecap={loadRecap} />,
    );

    await userEvent.click(await screen.findByRole("button", { name: t.retry }));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Le grand débat" }),
    ).toBeInTheDocument();
    await waitFor(() => expect(loadRecap).toHaveBeenCalledTimes(2));
  });

  test("the PDF export button is unavailable, with a reason, until there is a report", async () => {
    let resolve: (recap: Recap) => void = () => {};
    const loadRecap = vi.fn(
      () =>
        new Promise<Recap>((res) => {
          resolve = res;
        }),
    );
    const exportPdf = vi.fn(async () => {});
    render(
      <RecapExperience
        videoId="vid-1"
        brandName="jeminforme.fr"
        loadRecap={loadRecap}
        exportPdf={exportPdf}
      />,
    );

    const loading = screen.getByRole("button", { name: t.pdf.export });
    expect(loading).toBeDisabled();
    expect(loading).toHaveAccessibleDescription(t.pdf.unavailableLoading);

    resolve(debateRecap());
    await screen.findByRole("heading", { level: 2, name: "Le grand débat" });
    expect(screen.getByRole("button", { name: t.pdf.export })).toBeEnabled();
  });

  test("the PDF export is unavailable when the analysis is not complete", async () => {
    const loadRecap = vi.fn(async (): Promise<Recap> => {
      throw new ApiError("no completed analysis", 409);
    });
    render(
      <RecapExperience videoId="vid-1" brandName="jeminforme.fr" loadRecap={loadRecap} />,
    );

    await screen.findByRole("alert");
    const button = screen.getByRole("button", { name: t.pdf.export });
    expect(button).toBeDisabled();
    expect(button).toHaveAccessibleDescription(t.pdf.unavailableNotReady);
  });

  test("the PDF export carries the recap and the sheet order shown on the page", async () => {
    const recap = debateRecap();
    const exportPdf = vi.fn(async () => {});
    render(
      <RecapExperience
        videoId="vid-1"
        brandName="jeminforme.fr"
        loadRecap={async () => recap}
        exportPdf={exportPdf}
      />,
    );
    await screen.findByRole("heading", { level: 2, name: "Le grand débat" });

    await userEvent.click(
      screen.getByRole("button", { name: "Descendre la fiche de Marine Durand" }),
    );
    await userEvent.click(screen.getByRole("button", { name: t.pdf.export }));

    expect(exportPdf).toHaveBeenCalledTimes(1);
    const [props] = exportPdf.mock.calls[0] as unknown as [
      { recap: Recap; sheets: { label: string }[]; brandName: string },
    ];
    expect(props.recap).toBe(recap);
    expect(props.sheets.map((speaker) => speaker.label)).toEqual(["B", "A"]);
    expect(props.brandName).toBe("jeminforme.fr");
  });
});
