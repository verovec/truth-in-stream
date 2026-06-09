import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import Home from "./page";

vi.mock("react-player", () => import("@/test/react-player-mock"));

describe("Home", () => {
  test("renders the watch screen with player and fact-check panel", () => {
    render(<Home />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Truth in Stream" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /video/i })).toBeInTheDocument();
    expect(
      screen.getByRole("complementary", { name: /fact checks/i }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("media")).toHaveAttribute("src");
  });
});
