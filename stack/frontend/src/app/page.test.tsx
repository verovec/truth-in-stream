import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import {
  json,
  resultsRoute,
  stubBackend,
  submitRoute,
} from "@/test/fact-check";
import Home from "./page";

vi.mock("react-player", () => import("@/test/react-player-mock"));

describe("Home", () => {
  test("renders the watch screen with player and fact-check panel", async () => {
    stubBackend([
      submitRoute(json(200, { video_id: "v1", status: "complete" })),
      resultsRoute(json(200, { video_id: "v1", segments: [] })),
    ]);

    render(<Home />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Truth in Stream" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: /common myths/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("complementary", { name: /fact checks/i }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("media")).toHaveAttribute("src");
    expect(
      await screen.findByText(/no speech segments were found/i),
    ).toBeInTheDocument();
  });
});
