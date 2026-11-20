import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { en } from "@/lib/i18n/dictionaries/en";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { json, stubBackend } from "@/test/fact-check";
import { RecapShell } from "./recap-shell";

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

vi.mock("next/link", () => import("@/test/next-link-mock"));

afterEach(() => {
  vi.restoreAllMocks();
});

const recapWire = (order: string[]) => ({
  video_id: "vid-1",
  title: "Le grand débat",
  duration: 600,
  speakers: order.map((label, index) => ({
    label,
    name: "",
    role: "principal",
    speaking_time: 300 - index,
    turns: 3,
    share: 0.5,
    counts: { detected: 0, credible: 0, disputed: 0, unverifiable: 0, not_verified: 0 },
    disputed: [],
    credible: [],
  })),
  principals: { speaking_time: 599, turns: 6, counts: {} },
  others: {},
});

function stubRecap() {
  stubBackend([
    {
      match: (url) => url.endsWith("/api/videos/vid-1/recap"),
      responses: [json(200, recapWire(["A", "B"]))],
    },
  ]);
}

describe("RecapShell", () => {
  test("renders the app chrome, keeps Videos a link back, and loads the recap of the route's video", async () => {
    stubRecap();
    render(
      <RecapShell
        videoId="vid-1"
        order={null}
        role="guest"
        authenticated={false}
        locale="fr"
        dict={fr}
      />,
    );

    expect(
      screen.getByRole("link", { name: fr.app.nav.videos }),
    ).toHaveAttribute("href", "/app");
    // The brand stays the page's only level-1 heading; the report nests under it.
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(
      await screen.findByRole("heading", { level: 2, name: "Le grand débat" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
  });

  test("passes the URL's sheet order to the report, in the request locale", async () => {
    stubRecap();
    render(
      <RecapShell
        videoId="vid-1"
        order="B,A"
        role="guest"
        authenticated={false}
        locale="en"
        dict={en}
      />,
    );

    await screen.findByRole("heading", { level: 2, name: "Le grand débat" });
    const sheets = screen.getAllByRole("region", { name: /^Sheet for / });
    expect(sheets.map((sheet) => sheet.getAttribute("aria-labelledby"))).toEqual([
      "recap-sheet-B",
      "recap-sheet-A",
    ]);
  });
});
