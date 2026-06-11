import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import Home from "./page";

vi.mock("react-player", () => import("@/test/react-player-mock"));

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

describe("Home", () => {
  test("renders the header and the video library", async () => {
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos") && (init?.method ?? "GET") === "GET",
        responses: [json(200, { videos: [] })],
      },
    ]);

    render(<Home />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Truth in Stream" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /sign out/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText(/no videos yet/i)).toBeInTheDocument();
    });
  });
});
