import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { Footer } from "./footer";
import { fr } from "@/lib/i18n/dictionaries/fr";

vi.mock("next/link", () => import("@/test/next-link-mock"));

describe("Footer", () => {
  test("renders every configured footer link at its href", () => {
    render(<Footer locale="fr" dict={fr} />);

    for (const link of fr.footer.links) {
      expect(screen.getByRole("link", { name: link.label })).toHaveAttribute(
        "href",
        link.href,
      );
    }
  });

  test("keeps the app link pointing at the login route", () => {
    render(<Footer locale="fr" dict={fr} />);
    const appLink = fr.footer.links.find((link) => link.href === "/login");
    expect(appLink).toBeDefined();
    expect(
      screen.getByRole("link", { name: appLink!.label }),
    ).toHaveAttribute("href", "/login");
  });
});
