import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { LanguageToggle } from "./language-toggle";
import { fr } from "@/lib/i18n/dictionaries/fr";

vi.mock("next/link", () => import("@/test/next-link-mock"));

describe("LanguageToggle", () => {
  test("links to each locale home", () => {
    render(<LanguageToggle activeLocale="fr" copy={fr.langSwitch} />);

    expect(screen.getByRole("link", { name: fr.langSwitch.toFrench })).toHaveAttribute(
      "href",
      "/fr",
    );
    expect(screen.getByRole("link", { name: fr.langSwitch.toEnglish })).toHaveAttribute(
      "href",
      "/en",
    );
  });

  test("marks the active locale as current", () => {
    render(<LanguageToggle activeLocale="en" copy={fr.langSwitch} />);

    const en = screen.getByRole("link", { name: fr.langSwitch.toEnglish });
    const french = screen.getByRole("link", { name: fr.langSwitch.toFrench });
    expect(en).toHaveAttribute("aria-current", "true");
    expect(french).not.toHaveAttribute("aria-current");
  });
});
