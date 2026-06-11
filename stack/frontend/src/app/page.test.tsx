import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import LandingPage, { metadata } from "./page";

vi.mock("next/link", () => import("@/test/next-link-mock"));

describe("LandingPage", () => {
  test("renders the hero, how-it-works, and a closing call to action", () => {
    render(<LandingPage />);

    expect(
      screen.getByRole("heading", {
        level: 1,
        name: /truth in the middle of the politics stage/i,
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: /how it works/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 3, name: /^listen$/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 3, name: /^verdict$/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: /bring the receipts/i,
      }),
    ).toBeInTheDocument();
  });

  test("every 'Open the app' call to action points at the login route", () => {
    render(<LandingPage />);

    const ctas = screen.getAllByRole("link", { name: /open the app/i });
    expect(ctas.length).toBeGreaterThanOrEqual(1);
    for (const cta of ctas) {
      expect(cta).toHaveAttribute("href", "/login");
    }
  });

  test("exposes landing metadata for SEO and sharing", () => {
    expect(metadata.title).toMatch(/truth in stream/i);
    expect(metadata.description).toBeTruthy();
    expect(metadata.openGraph?.title).toMatch(/truth in stream/i);
  });
});
