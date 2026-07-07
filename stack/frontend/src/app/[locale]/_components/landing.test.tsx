import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { Landing } from "./landing";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { en } from "@/lib/i18n/dictionaries/en";

vi.mock("next/link", () => import("@/test/next-link-mock"));

describe("Landing", () => {
  test("renders the French hero, process and mission", () => {
    render(<Landing dict={fr} />);

    expect(
      screen.getByRole("heading", { level: 1, name: /vérifié en direct/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: /trois étapes/i }),
    ).toBeInTheDocument();
    expect(screen.getByText("Écoute")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /responsabilité/i }),
    ).toBeInTheDocument();
    expect(screen.getByText(fr.hero.demo.claim)).toBeInTheDocument();
  });

  test("renders the English copy when given the English dictionary", () => {
    render(<Landing dict={en} />);

    expect(
      screen.getByRole("heading", { level: 1, name: /checked live/i }),
    ).toBeInTheDocument();
    expect(screen.getByText("Listen")).toBeInTheDocument();
    expect(screen.queryByText("Écoute")).not.toBeInTheDocument();
  });

  test("every app call to action points at the login route", () => {
    render(<Landing dict={fr} />);

    const ctas = screen.getAllByRole("link", {
      name: new RegExp(fr.hero.ctaPrimary, "i"),
    });
    expect(ctas.length).toBeGreaterThanOrEqual(1);
    for (const cta of ctas) {
      expect(cta).toHaveAttribute("href", "/login");
    }
  });
});
