import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { Header } from "./header";
import { fr } from "@/lib/i18n/dictionaries/fr";

vi.mock("next/link", () => import("@/test/next-link-mock"));

describe("Header", () => {
  test("shows the brand and points the app call to action at the login route", () => {
    render(<Header locale="fr" dict={fr} />);

    const cta = screen.getByRole("link", { name: fr.nav.openApp });
    expect(cta).toHaveAttribute("href", "/login");
    expect(
      screen.getByRole("link", { name: /jeminforme\.fr/i }),
    ).toHaveAttribute("href", "/fr");
  });

  test("offers the language switch", () => {
    render(<Header locale="fr" dict={fr} />);
    expect(
      screen.getByRole("navigation", { name: fr.langSwitch.label }),
    ).toBeInTheDocument();
  });
});
