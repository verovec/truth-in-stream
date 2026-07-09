import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { AppHeader } from "./app-header";

describe("AppHeader", () => {
  test("renders Videos and Documents nav links to their routes", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    const videos = within(nav).getByRole("link", { name: fr.app.nav.videos });
    const documents = within(nav).getByRole("link", { name: fr.app.nav.documents });
    expect(videos).toHaveAttribute("href", "/app");
    expect(documents).toHaveAttribute("href", "/documents");
  });

  test("marks the current section with aria-current", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="documents" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    expect(
      within(nav).getByRole("link", { name: fr.app.nav.documents }),
    ).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).getByRole("link", { name: fr.app.nav.videos }),
    ).not.toHaveAttribute("aria-current");
  });

  test("shows the brand wordmark and the sign-out control", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" />);
    expect(screen.getByText(fr.brand.name)).toBeInTheDocument();
    expect(screen.getByText(fr.app.header.signOut)).toBeInTheDocument();
  });
});
